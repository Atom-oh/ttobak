package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// meetingFake is the meeting half of fakeMoverRepo: account refs, canonical
// meetings, share rows keyed meeting -> recipient, and account insights.
type meetingFake struct {
	refs          map[string][]model.MeetingRef
	meetings      map[string]*model.Meeting
	shares        map[string]map[string]model.Share
	insights      map[string]map[string]model.AccountInsight
	meetingWrites []string
	// beforeGrant may fail a share grant before it is applied.
	beforeGrant func(user string) error
	// afterMove runs after a committed move, e.g. a concurrent unpublish.
	afterMove func(m *model.Meeting)
}

var meetingDate = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

func (f *meetingFake) putMeeting(m *model.Meeting, refAccount string) {
	if f.meetings == nil {
		f.meetings, f.refs = map[string]*model.Meeting{}, map[string][]model.MeetingRef{}
	}
	m.Date = meetingDate
	f.meetings[m.MeetingID] = m
	f.refs[refAccount] = append(f.refs[refAccount], model.MeetingRef{PK: model.PrefixAccount + refAccount,
		SK: model.PrefixMeetingRef + m.Date.Format(time.RFC3339) + "#" + m.MeetingID, AccountID: refAccount,
		MeetingID: m.MeetingID, OwnerUserID: m.UserID, Date: m.Date, EntityType: model.EntityTypeMeetingRef})
}

func (f *meetingFake) putShare(meetingID, user, origin, account string) {
	if f.shares == nil {
		f.shares = map[string]map[string]model.Share{}
	}
	if f.shares[meetingID] == nil {
		f.shares[meetingID] = map[string]model.Share{}
	}
	f.shares[meetingID][user] = model.Share{MeetingID: meetingID, SharedToID: user, Origin: origin, AccountID: account,
		OwnerEmail: "author@example.com", Permission: model.PermissionRead}
}

func (f *meetingFake) hasRef(account, meetingID string) bool {
	for _, r := range f.refs[account] {
		if r.MeetingID == meetingID {
			return true
		}
	}
	return false
}

func (f *meetingFake) ListMeetingRefsForAccountPage(_ context.Context, account, cursor string, limit int32) ([]model.MeetingRef, *string, error) {
	refs := append([]model.MeetingRef(nil), f.refs[account]...)
	sort.Slice(refs, func(i, j int) bool { return refs[i].SK > refs[j].SK })
	var out []model.MeetingRef
	for i, r := range refs {
		if cursor != "" && r.SK >= cursor {
			continue
		}
		if len(out) == int(limit) {
			next := refs[i-1].SK
			return out, &next, nil
		}
		out = append(out, r)
	}
	return out, nil, nil
}

func (f *meetingFake) GetMeetingByID(_ context.Context, id string) (*model.Meeting, error) {
	if m := f.meetings[id]; m != nil {
		copied := *m
		return &copied, nil
	}
	return nil, nil
}

func (f *meetingFake) MoveMeetingForAccountMerge(_ context.Context, job *model.AccountMergeJob, owner, id string, ref *model.MeetingRef) error {
	m := f.meetings[id]
	if m == nil || m.UserID != owner || m.AccountID != job.SourceAccountID || ref.MeetingID != id {
		return repository.ErrConditionFailed
	}
	m.AccountID = job.TargetAccountID
	m.AuthorizationRevision++
	if a := m.SummaryAccountApproval; a != nil && a.AccountID == job.SourceAccountID {
		rebound := *a
		rebound.AccountID = job.TargetAccountID
		m.SummaryAccountApproval = &rebound
	}
	moved := *ref
	moved.PK, moved.AccountID = model.PrefixAccount+job.TargetAccountID, job.TargetAccountID
	f.refs[job.TargetAccountID] = append(f.refs[job.TargetAccountID], moved)
	f.meetingWrites = append(f.meetingWrites, "move:"+id)
	if f.afterMove != nil {
		f.afterMove(m)
	}
	return nil
}

func (f *meetingFake) DeleteMeetingRefForMerge(_ context.Context, job *model.AccountMergeJob, ref *model.MeetingRef) error {
	kept := f.refs[job.SourceAccountID][:0]
	for _, r := range f.refs[job.SourceAccountID] {
		if r.SK != ref.SK {
			kept = append(kept, r)
		}
	}
	f.refs[job.SourceAccountID] = kept
	f.meetingWrites = append(f.meetingWrites, "unref:"+ref.MeetingID)
	return nil
}

func (f *meetingFake) ListSharesForMeeting(_ context.Context, id string) ([]model.Share, error) {
	var out []model.Share
	for _, s := range f.shares[id] {
		out = append(out, s)
	}
	return out, nil
}

func (f *meetingFake) RetagAccountShareForMerge(_ context.Context, job *model.AccountMergeJob, id, user string) error {
	s, ok := f.shares[id][user]
	if ok && s.Origin == model.ShareOriginAccount && s.AccountID == job.SourceAccountID {
		s.AccountID = job.TargetAccountID
		f.shares[id][user] = s
		f.meetingWrites = append(f.meetingWrites, "retag:"+user)
	}
	return nil
}

func (f *meetingFake) CreateShareIfMember(_ context.Context, id, owner, ownerEmail, account, user, email, permission string) (*model.Share, error) {
	if f.beforeGrant != nil {
		if err := f.beforeGrant(user); err != nil {
			return nil, err
		}
	}
	if s, ok := f.shares[id][user]; ok && (s.Origin != model.ShareOriginAccount || s.AccountID != account) {
		return &s, nil
	}
	f.putShare(id, user, model.ShareOriginAccount, account)
	f.meetingWrites = append(f.meetingWrites, "grant:"+user)
	s := f.shares[id][user]
	return &s, nil
}

func (f *meetingFake) PutAccountInsights(_ context.Context, items []model.AccountInsight) error {
	if f.insights == nil {
		f.insights = map[string]map[string]model.AccountInsight{}
	}
	for _, it := range items {
		if f.insights[it.AccountID] == nil {
			f.insights[it.AccountID] = map[string]model.AccountInsight{}
		}
		f.insights[it.AccountID][it.SK] = it
	}
	f.meetingWrites = append(f.meetingWrites, fmt.Sprintf("insights:%d", len(items)))
	return nil
}

func (f *meetingFake) ClearAccountInsights(_ context.Context, owner, id, account, _, _ string) error {
	for sk, it := range f.insights[account] {
		if it.SourceID == id && it.SourceUserID == owner {
			delete(f.insights[account], sk)
		}
	}
	f.meetingWrites = append(f.meetingWrites, "clear:"+id)
	return nil
}

func meetingMoverFixture() (*fakeMoverRepo, *AccountMergeService, *model.AccountMergeJob) {
	repo := newFakeMoverRepo()
	s, job := moverFixture(repo)
	job.Step = "meetings"
	return repo, s, job
}

func TestMoveMeetingsRetagsAccountShares(t *testing.T) {
	repo, s, job := meetingMoverFixture()
	repo.putMeeting(&model.Meeting{MeetingID: "m1", UserID: "author", AccountID: "src", SharedToAccount: true,
		SummaryAccountApproval: &model.SummaryAccountApproval{Version: 1, OwnerUserID: "author", AccountID: "src", SummaryGeneration: "g1"}}, "src")
	repo.putShare("m1", "amember", model.ShareOriginAccount, "src")
	repo.putShare("m1", "direct", "", "")
	for _, u := range []string{"author", "amember", "direct", "tgtonly"} {
		repo.add("tgt", u, model.RoleSA)
	}
	res, err := s.moveMeetings(newMergeFence(context.Background()), job)
	if err != nil {
		t.Fatal(err)
	}
	if res.Moved != 1 || !res.Done {
		t.Fatalf("result %+v", res)
	}
	m := repo.meetings["m1"]
	if m.AccountID != "tgt" || m.UserID != "author" || !m.SharedToAccount || m.SummaryAccountApproval.AccountID != "tgt" {
		t.Fatalf("meeting %+v approval %+v", m, m.SummaryAccountApproval)
	}
	if got := repo.shares["m1"]["amember"]; got.Origin != model.ShareOriginAccount || got.AccountID != "tgt" {
		t.Errorf("source-origin share %+v, want retagged to tgt", got)
	}
	if got := repo.shares["m1"]["direct"]; got.Origin != "" || got.AccountID != "" {
		t.Errorf("direct share %+v must be untouched", got)
	}
	if got, ok := repo.shares["m1"]["tgtonly"]; !ok || got.Origin != model.ShareOriginAccount || got.AccountID != "tgt" || got.OwnerEmail != "author@example.com" {
		t.Errorf("target-only member share %+v ok=%v", got, ok)
	}
	if _, ok := repo.shares["m1"]["author"]; ok {
		t.Error("the owner must not get a share of its own meeting")
	}
	if repo.hasRef("src", "m1") || !repo.hasRef("tgt", "m1") {
		t.Errorf("refs src=%v tgt=%v", repo.refs["src"], repo.refs["tgt"])
	}
}

func TestMoveMeetingsSkipsStaleRef(t *testing.T) {
	repo, s, job := meetingMoverFixture()
	repo.putMeeting(&model.Meeting{MeetingID: "m1", UserID: "author", AccountID: "c", SharedToAccount: true}, "src")
	repo.putMeeting(&model.Meeting{MeetingID: "gone", UserID: "author"}, "src")
	delete(repo.meetings, "gone")
	repo.add("tgt", "tgtonly", model.RoleSA)
	res, err := s.moveMeetings(newMergeFence(context.Background()), job)
	if err != nil {
		t.Fatal(err)
	}
	if res.Skipped != 2 || res.Moved != 0 || !res.Done || len(repo.meetingWrites) != 0 {
		t.Fatalf("result %+v writes %v", res, repo.meetingWrites)
	}
	if repo.meetings["m1"].AccountID != "c" || !repo.hasRef("src", "m1") {
		t.Error("a meeting on another account and its stale ref must stay as they are")
	}
}

func TestMoveMeetings_Idempotent(t *testing.T) {
	t.Run("repeat run", func(t *testing.T) {
		repo, s, job := meetingMoverFixture()
		repo.putMeeting(&model.Meeting{MeetingID: "m1", UserID: "author", AccountID: "src", SharedToAccount: true}, "src")
		repo.add("tgt", "tgtonly", model.RoleSA)
		fence := newMergeFence(context.Background())
		if _, err := s.moveMeetings(fence, job); err != nil {
			t.Fatal(err)
		}
		writes := len(repo.meetingWrites)
		res, err := s.moveMeetings(fence, job)
		if err != nil || res.Moved != 0 || len(repo.meetingWrites) != writes || repo.meetings["m1"].AuthorizationRevision != 1 {
			t.Fatalf("second run %+v err=%v writes %v", res, err, repo.meetingWrites)
		}
	})
	// Review Focus 2: a crash after the move but before its shares finish
	// must leave the source ref so a rerun completes the grants once.
	t.Run("resume after interrupted grants", func(t *testing.T) {
		repo, s, job := meetingMoverFixture()
		repo.putMeeting(&model.Meeting{MeetingID: "m1", UserID: "author", AccountID: "src", SharedToAccount: true}, "src")
		repo.add("tgt", "tgtonly", model.RoleSA)
		repo.putShare("m1", "amember", model.ShareOriginAccount, "src")
		crash := errors.New("crash")
		repo.beforeGrant = func(string) error { return crash }
		fence := newMergeFence(context.Background())
		if res, err := s.moveMeetings(fence, job); !errors.Is(err, crash) || res.Cursor != job.Cursor {
			t.Fatalf("first run err=%v cursor=%q", err, res.Cursor)
		}
		if repo.meetings["m1"].AccountID != "tgt" || !repo.hasRef("src", "m1") {
			t.Fatal("the move committed but the source ref must remain until the meeting is finished")
		}
		repo.beforeGrant = nil
		res, err := s.moveMeetings(fence, job)
		if err != nil || res.Moved != 0 || res.Resumed != 1 || res.Skipped != 0 {
			t.Fatalf("resume %+v err=%v", res, err)
		}
		if got := repo.shares["m1"]["tgtonly"]; got.AccountID != "tgt" {
			t.Errorf("resume must grant the target member, got %+v", got)
		}
		if repo.hasRef("src", "m1") || repo.meetings["m1"].AuthorizationRevision != 1 {
			t.Errorf("resume must not move twice and must drop the source ref: %v", repo.meetingWrites)
		}
	})
}

func TestMoveMeetingsRebuildsInsightsForB(t *testing.T) {
	repo, s, job := meetingMoverFixture()
	insights := `[{"type":"risk","text":"Budget"},{"type":"opportunity","text":"Expand"}]`
	repo.putMeeting(&model.Meeting{MeetingID: "pub", UserID: "author", AccountID: "src", SharedToAccount: true,
		Insights: insights, InsightsRevision: "r1"}, "src")
	repo.putMeeting(&model.Meeting{MeetingID: "link", UserID: "author", AccountID: "src", Insights: insights}, "src")
	repo.add("tgt", "tgtonly", model.RoleSA)
	res, err := s.moveMeetings(newMergeFence(context.Background()), job)
	if err != nil || res.Moved != 2 {
		t.Fatalf("result %+v err=%v", res, err)
	}
	got := repo.insights["tgt"]
	if len(got) != 2 {
		t.Fatalf("target insights %v", got)
	}
	for _, it := range got {
		if it.PK != "ACCOUNT#tgt" || it.SourceID != "pub" || it.InsightsRevision != "r1" {
			t.Errorf("insight %+v", it)
		}
	}
	// A link-only meeting moves but stays unpublished: no share, no insight.
	if link := repo.meetings["link"]; link.AccountID != "tgt" || link.SharedToAccount {
		t.Errorf("link-only meeting %+v", link)
	}
	if _, ok := repo.shares["link"]["tgtonly"]; ok {
		t.Error("a link-only meeting must not be granted to target members")
	}
}

func TestMoveMeetingsBoundedPerRun(t *testing.T) {
	repo, s, job := meetingMoverFixture()
	for i := 0; i < mergeMeetingsPerRun+10; i++ {
		repo.putMeeting(&model.Meeting{MeetingID: fmt.Sprintf("m%03d", i), UserID: "author", AccountID: "c"}, "src")
	}
	fence := newMergeFence(context.Background())
	res, err := s.moveMeetings(fence, job)
	if err != nil || res.Done || res.Skipped != mergeMeetingsPerRun || res.Cursor == "" {
		t.Fatalf("first run %+v err=%v", res, err)
	}
	job.Cursor = res.Cursor
	res, err = s.moveMeetings(fence, job)
	if err != nil || !res.Done || res.Skipped != 10 {
		t.Fatalf("second run %+v err=%v", res, err)
	}
}

func TestMoveMeetingsStopsOnLostFence(t *testing.T) {
	repo, s, job := meetingMoverFixture()
	repo.putMeeting(&model.Meeting{MeetingID: "m1", UserID: "author", AccountID: "src", SharedToAccount: true}, "src")
	repo.add("tgt", "tgtonly", model.RoleSA)
	repo.beforeGrant = func(string) error { return repository.ErrMergeFenceLost }
	fence := newMergeFence(context.Background())
	res, err := s.moveMeetings(fence, job)
	if !errors.Is(err, ErrMergeFenceLost) || fence.Err() == nil || res.Cursor != job.Cursor || !repo.hasRef("src", "m1") {
		t.Fatalf("err=%v fence=%v cursor=%q", err, fence.Err(), res.Cursor)
	}
}

// Production runs with the summary privacy writer off, where the share and
// legacy insight writers do not revalidate publication: grants must follow a
// read taken after the move.
func TestMoveMeetingsConcurrentUnpublishSkipsGrants(t *testing.T) {
	repo, s, job := meetingMoverFixture()
	repo.putMeeting(&model.Meeting{MeetingID: "m1", UserID: "author", AccountID: "src", SharedToAccount: true,
		Insights: `[{"type":"risk","text":"Budget"}]`}, "src")
	repo.add("tgt", "tgtonly", model.RoleSA)
	repo.afterMove = func(m *model.Meeting) { m.SharedToAccount = false }
	res, err := s.moveMeetings(newMergeFence(context.Background()), job)
	if err != nil || res.Moved != 1 {
		t.Fatalf("result %+v err=%v", res, err)
	}
	if _, ok := repo.shares["m1"]["tgtonly"]; ok {
		t.Error("a meeting unpublished after the move must not be granted to target members")
	}
	if len(repo.insights["tgt"]) != 0 {
		t.Errorf("a meeting unpublished after the move must not fan out insights: %v", repo.insights["tgt"])
	}
	if repo.hasRef("src", "m1") {
		t.Error("the finished meeting's source ref must be deleted")
	}
}

func TestMoveMeetingsClearsEmptyInsightsForB(t *testing.T) {
	repo, s, job := meetingMoverFixture()
	repo.putMeeting(&model.Meeting{MeetingID: "m1", UserID: "author", AccountID: "src", SharedToAccount: true}, "src")
	if err := repo.PutAccountInsights(context.Background(), []model.AccountInsight{{PK: "ACCOUNT#tgt", SK: "INSIGHT#old#m1#0",
		AccountID: "tgt", SourceID: "m1", SourceUserID: "author"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.moveMeetings(newMergeFence(context.Background()), job); err != nil {
		t.Fatal(err)
	}
	if len(repo.insights["tgt"]) != 0 {
		t.Errorf("stale target insight rows must be cleared: %v", repo.insights["tgt"])
	}
}

func TestMoveMeetingsListsTargetMembersOncePerRun(t *testing.T) {
	repo, s, job := meetingMoverFixture()
	for i := 0; i < 3; i++ {
		repo.putMeeting(&model.Meeting{MeetingID: fmt.Sprintf("m%d", i), UserID: "author", AccountID: "src", SharedToAccount: true}, "src")
	}
	repo.add("tgt", "tgtonly", model.RoleSA)
	res, err := s.moveMeetings(newMergeFence(context.Background()), job)
	if err != nil || res.Moved != 3 {
		t.Fatalf("result %+v err=%v", res, err)
	}
	if repo.memberLists != 1 {
		t.Errorf("target members listed %d times, want 1", repo.memberLists)
	}
	for i := 0; i < 3; i++ {
		if _, ok := repo.shares[fmt.Sprintf("m%d", i)]["tgtonly"]; !ok {
			t.Errorf("meeting m%d not granted", i)
		}
	}
}
