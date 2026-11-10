package service

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

const peopleTestContact = "11111111-1111-4111-8111-111111111111"
const peopleTestContact2 = "22222222-2222-4222-8222-222222222222"

type fakePeopleRepo struct {
	accounts    map[string]*model.Account
	meetings    map[string]*model.Meeting
	users       map[string]*model.User
	members     []model.AccountMember
	contacts    []model.AccountContact
	children    map[string][]string
	projectRefs []model.ProjectRef
	projects    map[string]*model.Project
	refs        []model.MeetingRef

	existing  []model.PeopleLink
	ignores   map[string]bool
	putErr    map[model.PeopleLinkKey]error
	claimBusy bool
	reruns    []bool
	prior     []string

	claims, finishes, gets int
	puts                   []model.PeopleLink
	deletes                []string
	replaced               [][]model.UnresolvedMention
	replacePrior           [][]string
	finished               []model.PeopleRun
	resolvedMeetings       []string
	contactCalls           int

	onClaim    func()
	recordErr  error
	replaceErr error
	recorded   [][]string
	ops        []string

	leaseLostAfter, fencedWrites int

	// People panel reads.
	targetLinks []model.PeopleLink
	mentionRows []model.UnresolvedMention
	contactErr  map[string]error
	pubReads    int

	// People panel writes.
	stored         map[model.PeopleLinkKey]*model.PeopleLink
	putActors      []string
	mentionDeletes []string
	ignorePuts     []string
	staleMentions  map[string]bool
}

func newFakePeopleRepo() *fakePeopleRepo {
	return &fakePeopleRepo{
		accounts: map[string]*model.Account{"acc": {PK: "ACCOUNT#acc", SK: "META", AccountID: "acc", Name: "Acme", EntityType: model.EntityTypeAccount}},
		meetings: map[string]*model.Meeting{"owner:m1": peopleTestMeeting("m1")},
		users:    map[string]*model.User{},
		children: map[string][]string{},
		projects: map[string]*model.Project{},
		ignores:  map[string]bool{},
		putErr:   map[model.PeopleLinkKey]error{},

		contactErr: map[string]error{},
	}
}

func peopleTestMeeting(id string) *model.Meeting {
	return &model.Meeting{MeetingID: id, UserID: "owner", AccountID: "acc", SharedToAccount: true, EntityType: "MEETING"}
}

func (f *fakePeopleRepo) GetAccount(_ context.Context, id string) (*model.Account, error) {
	return f.accounts[id], nil
}
func (f *fakePeopleRepo) GetMeeting(_ context.Context, owner, id string) (*model.Meeting, error) {
	f.gets++
	// The pre-claim and leased reads of one meeting count once.
	if n := len(f.resolvedMeetings); n == 0 || f.resolvedMeetings[n-1] != id {
		f.resolvedMeetings = append(f.resolvedMeetings, id)
	}
	return f.meetings[owner+":"+id], nil
}
func (f *fakePeopleRepo) GetUserByID(_ context.Context, id string) (*model.User, error) {
	return f.users[id], nil
}
func (f *fakePeopleRepo) ListAccountMembers(context.Context, string) ([]model.AccountMember, error) {
	return f.members, nil
}
func (f *fakePeopleRepo) ListAccountContacts(_ context.Context, _ string, limit int, after string) ([]model.AccountContact, string, error) {
	f.contactCalls++
	start := 0
	if after != "" {
		fmt.Sscanf(after, "%d", &start)
	}
	end := min(start+limit, len(f.contacts))
	next := ""
	if end < len(f.contacts) {
		next = fmt.Sprint(end)
	}
	return f.contacts[start:end], next, nil
}
func (f *fakePeopleRepo) ListChildAccountIDsPage(_ context.Context, parent, _ string, _ int32) ([]string, *string, error) {
	return f.children[parent], nil, nil
}
func (f *fakePeopleRepo) ListProjectRefsForAccount(context.Context, string) ([]model.ProjectRef, error) {
	return f.projectRefs, nil
}
func (f *fakePeopleRepo) GetProject(_ context.Context, id string) (*model.Project, error) {
	return f.projects[id], nil
}
func (f *fakePeopleRepo) ListMeetingRefsForAccountPage(_ context.Context, _ string, cursor string, limit int32) ([]model.MeetingRef, *string, error) {
	start := 0
	if cursor != "" {
		fmt.Sscanf(cursor, "%d", &start)
	}
	end := min(start+int(limit), len(f.refs))
	if end < len(f.refs) {
		next := fmt.Sprint(end)
		return f.refs[start:end], &next, nil
	}
	return f.refs[start:end], nil, nil
}

// fenced counts lease-fenced writes and fails them once leaseLostAfter is
// reached (0 means the lease is never lost).
func (f *fakePeopleRepo) fenced(run *model.PeopleRun) error {
	if run == nil || run.LeaseToken != "tok" {
		return fmt.Errorf("unfenced write")
	}
	f.fencedWrites++
	if f.leaseLostAfter > 0 && f.fencedWrites > f.leaseLostAfter {
		return fmt.Errorf("%w: %w", repository.ErrConditionFailed, repository.ErrPeopleRunLeaseLost)
	}
	return nil
}
func (f *fakePeopleRepo) PutPeopleLinkInRun(_ context.Context, l *model.PeopleLink, run *model.PeopleRun) error {
	f.ops = append(f.ops, "put")
	if err := f.fenced(run); err != nil {
		return err
	}
	if err := f.putErr[l.Key()]; err != nil {
		return err
	}
	f.puts = append(f.puts, *l)
	return nil
}
func (f *fakePeopleRepo) DeletePeopleLinkInRun(_ context.Context, _ string, k model.PeopleLinkKey, run *model.PeopleRun) error {
	if err := f.fenced(run); err != nil {
		return err
	}
	f.deletes = append(f.deletes, fmt.Sprintf("%s/%s/%s/actor=%q/%s/deny=%t", k.Role, k.ToKind, k.ToID, "", model.LinkOriginAuto, false))
	return nil
}
func (f *fakePeopleRepo) ListPeopleLinksForMeeting(context.Context, string, string, int) ([]model.PeopleLink, bool, error) {
	return f.existing, false, nil
}
func (f *fakePeopleRepo) GetMentionIgnores(_ context.Context, _ string, hashes []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, h := range hashes {
		if f.ignores[h] {
			out[h] = true
		}
	}
	return out, nil
}
func (f *fakePeopleRepo) ReplaceUnresolvedMentions(_ context.Context, _, _ string, rows []model.UnresolvedMention, prior []string, run *model.PeopleRun) error {
	f.ops = append(f.ops, "replace")
	if f.replaceErr != nil {
		return f.replaceErr
	}
	if err := f.fenced(run); err != nil {
		return err
	}
	f.replaced = append(f.replaced, rows)
	f.replacePrior = append(f.replacePrior, prior)
	return nil
}
func (f *fakePeopleRepo) RecordPeopleRunMentions(_ context.Context, _ *model.PeopleRun, keys []string) error {
	f.ops = append(f.ops, "record")
	if f.recordErr != nil {
		return f.recordErr
	}
	f.recorded = append(f.recorded, keys)
	return nil
}
func (f *fakePeopleRepo) ClaimPeopleRun(_ context.Context, account, scope, token string, _ time.Time, _ time.Duration) (*model.PeopleRun, bool, error) {
	f.claims++
	f.ops = append(f.ops, "claim")
	if f.onClaim != nil {
		f.onClaim()
	}
	if f.claimBusy {
		return nil, false, nil
	}
	return &model.PeopleRun{AccountID: account, Scope: scope, LeaseToken: token, Mentions: f.prior}, true, nil
}
func (f *fakePeopleRepo) FinishPeopleRun(_ context.Context, run *model.PeopleRun) (bool, error) {
	f.finishes++
	f.ops = append(f.ops, "finish")
	f.finished = append(f.finished, *run)
	if len(f.reruns) > 0 {
		r := f.reruns[0]
		f.reruns = f.reruns[1:]
		return r, nil
	}
	return false, nil
}

func newPeopleTestService(f *fakePeopleRepo) *AccountPeopleService {
	s := NewAccountPeopleService(f)
	s.now = func() time.Time { return time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC) }
	s.newToken = func() string { return "tok" }
	return s
}

func peopleContact(id, name, email string) model.AccountContact {
	return model.AccountContact{ContactID: id, Name: name, Email: email}
}

func TestResolveMeeting_WritesAutoEdgesWithProvenance(t *testing.T) {
	f := newFakePeopleRepo()
	f.contacts = []model.AccountContact{peopleContact(peopleTestContact, "Kim Minsu", "kim@acme.example")}
	f.meetings["owner:m1"].Participants = []string{"Kim <kim@acme.example>", "Unknown Person"}
	res, err := newPeopleTestService(f).ResolveMeeting(context.Background(), "owner", "m1", PeopleRunOptions{})
	if err != nil || res.Written != 1 || res.Unresolved != 1 || res.Meetings != 1 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	l := f.puts[0]
	if l.Origin != model.LinkOriginAuto || l.Rule != model.PeopleRuleEmail || l.SourceField != model.PeopleSourceParticipants ||
		l.Mention != "Kim <kim@acme.example>" || l.ToKind != model.LinkKindContact || l.ToID != peopleTestContact ||
		l.MeetingID != "owner:m1" || l.Role != model.PeopleRoleParticipant {
		t.Fatalf("edge %+v", l)
	}
	if len(f.replaced) != 1 || len(f.replaced[0]) != 1 || f.replaced[0][0].Mention != "Unknown Person" {
		t.Fatalf("unresolved %+v", f.replaced)
	}
	fin := f.finished[0]
	if f.finishes != 1 || len(fin.Mentions) != 1 || fin.Mentions[0] != model.MentionSortKey(f.replaced[0][0].Hash, "owner:m1", "participant") || fin.LeaseToken != "tok" {
		t.Fatalf("finish %+v", fin)
	}
	if s := res.String(); strings.Contains(s, "Kim") || strings.Contains(s, "acme") {
		t.Fatalf("result string leaks text: %s", s)
	}
}

func TestResolveMeeting_RemovesOnlyOwnStaleAutoEdges(t *testing.T) {
	f := newFakePeopleRepo()
	f.existing = []model.PeopleLink{
		{MeetingID: "owner:m1", ToKind: "contact", ToID: "c1", Role: "speaker", Origin: model.LinkOriginAuto},
		{MeetingID: "owner:m1", ToKind: "contact", ToID: "c2", Role: "speaker", Origin: model.LinkOriginManual},
	}
	res, err := newPeopleTestService(f).ResolveMeeting(context.Background(), "owner", "m1", PeopleRunOptions{})
	if err != nil || res.Removed != 1 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if len(f.deletes) != 1 || f.deletes[0] != `speaker/contact/c1/actor=""/auto/deny=false` {
		t.Fatalf("deletes %v", f.deletes)
	}
}

func TestResolveMeeting_RespectsDeny(t *testing.T) {
	f := newFakePeopleRepo()
	f.contacts = []model.AccountContact{peopleContact(peopleTestContact, "Kim Minsu", "")}
	f.meetings["owner:m1"].Participants = []string{"Kim Minsu"}
	k := model.PeopleLinkKey{MeetingID: "owner:m1", ToKind: "contact", ToID: peopleTestContact, Role: "participant"}
	f.putErr[k] = fmt.Errorf("%w: %w", repository.ErrConditionFailed, repository.ErrAccountLinkDenied)
	res, err := newPeopleTestService(f).ResolveMeeting(context.Background(), "owner", "m1", PeopleRunOptions{})
	if err != nil || res.Skipped != 1 || res.Written != 0 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	// A contact deleted mid-run is skipped the same way.
	f.putErr[k] = fmt.Errorf("%w: %w", repository.ErrConditionFailed, repository.ErrAccountLinkTarget)
	if res, err := newPeopleTestService(f).ResolveMeeting(context.Background(), "owner", "m1", PeopleRunOptions{}); err != nil || res.Skipped != 1 {
		t.Fatalf("target: res=%+v err=%v", res, err)
	}
}

func TestResolveMeeting_ManualResolutionNotRelisted(t *testing.T) {
	f := newFakePeopleRepo()
	f.meetings["owner:m1"].Participants = []string{"  Park  Jisoo ", "Lee"}
	f.existing = []model.PeopleLink{{MeetingID: "owner:m1", ToKind: "contact", ToID: "c9", Role: "participant",
		Origin: model.LinkOriginManual, Mention: "park jisoo"}}
	res, err := newPeopleTestService(f).ResolveMeeting(context.Background(), "owner", "m1", PeopleRunOptions{})
	if err != nil || res.Unresolved != 1 || len(f.replaced[0]) != 1 || f.replaced[0][0].Mention != "Lee" {
		t.Fatalf("res=%+v err=%v rows=%+v", res, err, f.replaced)
	}
	if len(f.deletes) != 0 {
		t.Fatalf("manual edge removed: %v", f.deletes)
	}
}

func TestResolveMeeting_IgnoredMentionSkipped(t *testing.T) {
	f := newFakePeopleRepo()
	f.contacts = []model.AccountContact{peopleContact(peopleTestContact, "Ops Team", "")}
	f.meetings["owner:m1"].Participants = []string{"Ops Team", "Nobody"}
	f.ignores[model.MentionHash(NormalizePersonName("Ops Team"))] = true
	f.ignores[model.MentionHash(NormalizePersonName("Nobody"))] = true
	res, err := newPeopleTestService(f).ResolveMeeting(context.Background(), "owner", "m1", PeopleRunOptions{})
	if err != nil || res.Mentions != 0 || len(f.puts) != 0 || len(f.replaced[0]) != 0 {
		t.Fatalf("res=%+v err=%v puts=%v rows=%v", res, err, f.puts, f.replaced)
	}
}

func TestResolveMeeting_UnpublishedSkipped(t *testing.T) {
	for _, mutate := range []func(*model.Meeting){
		func(m *model.Meeting) { m.SharedToAccount = false },
		func(m *model.Meeting) { m.AccountID = "" },
	} {
		f := newFakePeopleRepo()
		mutate(f.meetings["owner:m1"])
		res, err := newPeopleTestService(f).ResolveMeeting(context.Background(), "owner", "m1", PeopleRunOptions{})
		if err != nil || res.Skipped != 1 || f.claims != 0 || len(f.puts) != 0 || len(f.replaced) != 0 {
			t.Fatalf("res=%+v err=%v claims=%d", res, err, f.claims)
		}
	}
	f := newFakePeopleRepo()
	if res, err := newPeopleTestService(f).ResolveMeeting(context.Background(), "owner", "missing", PeopleRunOptions{}); err != nil || res.Skipped != 1 {
		t.Fatalf("missing: %+v %v", res, err)
	}
	if _, err := newPeopleTestService(f).ResolveMeeting(context.Background(), "own#er", "m1", PeopleRunOptions{}); err != ErrInvalidInput {
		t.Fatalf("invalid owner: %v", err)
	}
}

func TestResolveMeeting_BusyReturnsBusy(t *testing.T) {
	f := newFakePeopleRepo()
	f.claimBusy = true
	res, err := newPeopleTestService(f).ResolveMeeting(context.Background(), "owner", "m1", PeopleRunOptions{})
	if err != nil || !res.Busy || f.finishes != 0 || len(f.replaced) != 0 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func TestResolveMeeting_RerunsWhenRequested(t *testing.T) {
	f := newFakePeopleRepo()
	f.meetings["owner:m1"].Participants = []string{"Lee"}
	f.reruns = []bool{true, true, true}
	res, err := newPeopleTestService(f).ResolveMeeting(context.Background(), "owner", "m1", PeopleRunOptions{})
	if err != nil || f.claims != 2 || f.finishes != 2 || f.gets != 4 || res.Meetings != 1 || res.Unresolved != 1 {
		t.Fatalf("res=%+v err=%v claims=%d finishes=%d gets=%d", res, err, f.claims, f.finishes, f.gets)
	}
}

func TestResolveMeeting_DryRunWritesNothing(t *testing.T) {
	f := newFakePeopleRepo()
	f.contacts = []model.AccountContact{peopleContact(peopleTestContact, "Kim Minsu", "")}
	f.meetings["owner:m1"].Participants = []string{"Kim Minsu", "Lee"}
	f.existing = []model.PeopleLink{{MeetingID: "owner:m1", ToKind: "contact", ToID: "stale", Role: "speaker", Origin: model.LinkOriginAuto}}
	res, err := newPeopleTestService(f).ResolveMeeting(context.Background(), "owner", "m1", PeopleRunOptions{DryRun: true})
	if err != nil || res.Resolved != 1 || res.Unresolved != 1 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if f.claims+f.finishes+len(f.puts)+len(f.deletes)+len(f.replaced) != 0 {
		t.Fatalf("dry run wrote: claims=%d finishes=%d puts=%d deletes=%d replaced=%d", f.claims, f.finishes, len(f.puts), len(f.deletes), len(f.replaced))
	}
}

func TestResolveMeeting_PassesPriorMentionKeys(t *testing.T) {
	f := newFakePeopleRepo()
	f.prior = []string{model.MentionSortKey(strings.Repeat("a", 32), "owner:m1", "participant")}
	if _, err := newPeopleTestService(f).ResolveMeeting(context.Background(), "owner", "m1", PeopleRunOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(f.replacePrior) != 1 || len(f.replacePrior[0]) != 1 || f.replacePrior[0][0] != f.prior[0] {
		t.Fatalf("prior %v", f.replacePrior)
	}
}

func TestLoadDirectory_ContactsCappedAt2000(t *testing.T) {
	f := newFakePeopleRepo()
	for i := 0; i < 2001; i++ {
		f.contacts = append(f.contacts, peopleContact(fmt.Sprintf("%08d-1111-4111-8111-111111111111", i), fmt.Sprint("c", i), ""))
	}
	dir, err := newPeopleTestService(f).loadDirectory(context.Background(), "acc")
	if err != nil || len(dir.People) != 2000 || !dir.ContactsTruncated {
		t.Fatalf("people=%d truncated=%v err=%v", len(dir.People), dir != nil && dir.ContactsTruncated, err)
	}
	f.contacts = f.contacts[:2000]
	dir, err = newPeopleTestService(f).loadDirectory(context.Background(), "acc")
	if err != nil || len(dir.People) != 2000 || dir.ContactsTruncated {
		t.Fatalf("exactly 2000: people=%d truncated=%v err=%v", len(dir.People), dir.ContactsTruncated, err)
	}
}

func TestLoadDirectory_MembersAndDescendants(t *testing.T) {
	f := newFakePeopleRepo()
	f.members = []model.AccountMember{
		{PK: "ACCOUNT#acc", SK: "MEMBER#u1", AccountID: "acc", UserID: "u1", Email: "u1@x.example", EntityType: model.EntityTypeAccountMember},
		{PK: "ACCOUNT#acc", SK: "MEMBER#u2", AccountID: "acc", UserID: "u2", Email: "u2@x.example", EntityType: model.EntityTypeAccountMember},
		{PK: "ACCOUNT#other", SK: "MEMBER#u3", AccountID: "other", UserID: "u3", EntityType: model.EntityTypeAccountMember},
	}
	f.users["u1"] = &model.User{UserID: "u1", Name: "Kim Minsu"}
	f.children["acc"] = []string{"child", "moved"}
	f.children["child"] = []string{"grand", "acc"}
	f.accounts["child"] = &model.Account{PK: "ACCOUNT#child", SK: "META", AccountID: "child", ParentAccountID: "acc", Name: "Acme Korea", Aliases: []string{"AK"}, EntityType: model.EntityTypeAccount}
	f.accounts["moved"] = &model.Account{PK: "ACCOUNT#moved", SK: "META", AccountID: "moved", ParentAccountID: "elsewhere", Name: "Moved", EntityType: model.EntityTypeAccount}
	f.accounts["grand"] = &model.Account{PK: "ACCOUNT#grand", SK: "META", AccountID: "grand", ParentAccountID: "child", Name: "Grand", EntityType: model.EntityTypeAccount}
	dir, err := newPeopleTestService(f).loadDirectory(context.Background(), "acc")
	if err != nil {
		t.Fatal(err)
	}
	if len(dir.People) != 2 || dir.People[0].Name != "Kim Minsu" || dir.People[1].Name != "u2@x.example" || dir.People[1].Email != "u2@x.example" {
		t.Fatalf("members %+v", dir.People)
	}
	var ids []string
	for _, e := range dir.Entities {
		ids = append(ids, e.Kind+":"+e.ID)
	}
	if strings.Join(ids, ",") != "account:child,account:grand" {
		t.Fatalf("entities %v", ids)
	}
}

func TestLoadDirectory_ExcludesProjectNoLongerInAccount(t *testing.T) {
	f := newFakePeopleRepo()
	f.projectRefs = []model.ProjectRef{{AccountID: "acc", ProjectID: "p1"}, {AccountID: "acc", ProjectID: "p2"}, {AccountID: "acc", ProjectID: "gone"}}
	f.projects["p1"] = &model.Project{ProjectID: "p1", Name: "Migration", AccountIDs: []string{"acc"}}
	f.projects["p2"] = &model.Project{ProjectID: "p2", Name: "Unlinked", AccountIDs: []string{"other"}}
	dir, err := newPeopleTestService(f).loadDirectory(context.Background(), "acc")
	if err != nil || len(dir.Entities) != 1 || dir.Entities[0].ID != "p1" || dir.Entities[0].Kind != model.LinkKindProject {
		t.Fatalf("entities %+v err=%v", dir.Entities, err)
	}
}

func TestResolveAccount_WindowAndCap(t *testing.T) {
	now := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	f := newFakePeopleRepo()
	for i, days := range []int{10, 80, 100} {
		id := fmt.Sprint("w", i)
		f.meetings["owner:"+id] = peopleTestMeeting(id)
		f.refs = append(f.refs, model.MeetingRef{AccountID: "acc", MeetingID: id, OwnerUserID: "owner", Date: now.Add(-time.Duration(days) * 24 * time.Hour)})
	}
	res, err := newPeopleTestService(f).ResolveAccount(context.Background(), "acc", now.Add(-peopleBackfillWindow), maxPeopleBackfillMeetings, PeopleRunOptions{})
	if err != nil || res.Meetings != 2 || strings.Join(f.resolvedMeetings, ",") != "w0,w1" || res.Truncated {
		t.Fatalf("window: res=%+v err=%v meetings=%v", res, err, f.resolvedMeetings)
	}
	// One account claim plus one per meeting; the account run finishes last.
	if f.claims != 3 || f.finished[len(f.finished)-1].Scope != model.PeopleRunScopeAccount {
		t.Fatalf("claims=%d finished=%+v", f.claims, f.finished)
	}

	f = newFakePeopleRepo()
	for i := 0; i < 250; i++ {
		id := fmt.Sprint("c", i)
		f.meetings["owner:"+id] = peopleTestMeeting(id)
		f.refs = append(f.refs, model.MeetingRef{AccountID: "acc", MeetingID: id, OwnerUserID: "owner", Date: now.Add(-time.Hour)})
	}
	res, err = newPeopleTestService(f).ResolveAccount(context.Background(), "acc", now.Add(-peopleBackfillWindow), maxPeopleBackfillMeetings, PeopleRunOptions{})
	if err != nil || res.Meetings != 200 || len(f.resolvedMeetings) != 200 || !res.Truncated {
		t.Fatalf("cap: res=%+v err=%v", res, err)
	}
}

func TestResolveAccount_BusyAndDeadline(t *testing.T) {
	f := newFakePeopleRepo()
	f.claimBusy = true
	if res, err := newPeopleTestService(f).ResolveAccount(context.Background(), "acc", time.Time{}, 10, PeopleRunOptions{}); err != nil || !res.Busy || f.gets != 0 {
		t.Fatalf("busy: %+v %v", res, err)
	}

	f = newFakePeopleRepo()
	f.refs = []model.MeetingRef{{AccountID: "acc", MeetingID: "m1", OwnerUserID: "owner", Date: time.Now()}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := newPeopleTestService(f).ResolveAccount(ctx, "acc", time.Time{}, 10, PeopleRunOptions{})
	if err != nil || !res.Truncated || f.gets != 0 || f.finishes != 1 {
		t.Fatalf("deadline: res=%+v err=%v gets=%d finishes=%d", res, err, f.gets, f.finishes)
	}

	if _, err := newPeopleTestService(f).ResolveAccount(context.Background(), "missing", time.Time{}, 10, PeopleRunOptions{}); err != ErrNotFound {
		t.Fatalf("missing account: %v", err)
	}
}

func TestResolveMeeting_PlansFromSnapshotTakenUnderLease(t *testing.T) {
	f := newFakePeopleRepo()
	f.contacts = []model.AccountContact{peopleContact(peopleTestContact, "Kim Minsu", "")}
	f.meetings["owner:m1"].Participants = []string{"Old Name"}
	// While this run stalls before its claim, another run applies newer
	// fields and finishes; the claim then succeeds on the free lease.
	f.onClaim = func() {
		f.meetings["owner:m1"] = peopleTestMeeting("m1")
		f.meetings["owner:m1"].Participants = []string{"Kim Minsu"}
	}
	res, err := newPeopleTestService(f).ResolveMeeting(context.Background(), "owner", "m1", PeopleRunOptions{})
	if err != nil || res.Written != 1 || res.Unresolved != 0 || f.gets != 2 {
		t.Fatalf("res=%+v err=%v gets=%d", res, err, f.gets)
	}
	if f.puts[0].Mention != "Kim Minsu" || len(f.replaced[0]) != 0 {
		t.Fatalf("stale snapshot applied: puts=%+v rows=%+v", f.puts, f.replaced)
	}

	// A meeting unpublished meanwhile is skipped and the lease released.
	f = newFakePeopleRepo()
	f.onClaim = func() { f.meetings["owner:m1"].SharedToAccount = false }
	res, err = newPeopleTestService(f).ResolveMeeting(context.Background(), "owner", "m1", PeopleRunOptions{})
	if err != nil || res.Skipped != 1 || len(f.puts)+len(f.replaced) != 0 || f.finishes != 1 {
		t.Fatalf("unpublished under lease: res=%+v err=%v finishes=%d", res, err, f.finishes)
	}
}

func TestResolveMeeting_ManualBindingSuppressesOtherAutoTarget(t *testing.T) {
	f := newFakePeopleRepo()
	f.contacts = []model.AccountContact{peopleContact(peopleTestContact2, "Kim", "")}
	f.meetings["owner:m1"].Participants = []string{"Kim"}
	f.existing = []model.PeopleLink{
		{MeetingID: "owner:m1", ToKind: "contact", ToID: peopleTestContact, Role: "participant", Origin: model.LinkOriginManual, Mention: "kim"},
		{MeetingID: "owner:m1", ToKind: "contact", ToID: peopleTestContact2, Role: "participant", Origin: model.LinkOriginAuto, Mention: "Kim"},
	}
	res, err := newPeopleTestService(f).ResolveMeeting(context.Background(), "owner", "m1", PeopleRunOptions{})
	if err != nil || len(f.puts) != 0 || res.Resolved != 0 || res.Unresolved != 0 {
		t.Fatalf("res=%+v err=%v puts=%+v", res, err, f.puts)
	}
	if len(f.deletes) != 1 || f.deletes[0] != `participant/contact/`+peopleTestContact2+`/actor=""/auto/deny=false` {
		t.Fatalf("contradicting auto edge kept: %v", f.deletes)
	}
}

func TestResolveMeeting_RecordsMentionInventoryBeforeWrites(t *testing.T) {
	f := newFakePeopleRepo()
	f.contacts = []model.AccountContact{peopleContact(peopleTestContact, "Kim Minsu", "")}
	f.meetings["owner:m1"].Participants = []string{"Kim Minsu", "Lee"}
	f.prior = []string{model.MentionSortKey(strings.Repeat("a", 32), "owner:m1", "participant")}
	f.replaceErr = fmt.Errorf("timeout")
	if _, err := newPeopleTestService(f).ResolveMeeting(context.Background(), "owner", "m1", PeopleRunOptions{}); err == nil {
		t.Fatal("replace failure not returned")
	}
	if strings.Join(f.ops, ",") != "claim,record,put,replace,finish" {
		t.Fatalf("ops %v", f.ops)
	}
	lee := model.MentionSortKey(model.MentionHash("lee"), "owner:m1", "participant")
	if len(f.recorded) != 1 || strings.Join(f.recorded[0], ",") != f.prior[0]+","+lee {
		t.Fatalf("recorded %v", f.recorded)
	}
	// The release keeps the union, so the next run deletes both.
	if got := f.finished[0].Mentions; strings.Join(got, ",") != f.prior[0]+","+lee {
		t.Fatalf("released inventory %v", got)
	}

	f = newFakePeopleRepo()
	f.meetings["owner:m1"].Participants = []string{"Lee"}
	f.recordErr = fmt.Errorf("%w: lease", repository.ErrConditionFailed)
	res, err := newPeopleTestService(f).ResolveMeeting(context.Background(), "owner", "m1", PeopleRunOptions{})
	if err != nil || !res.Busy || len(f.replaced) != 0 || len(f.puts) != 0 {
		t.Fatalf("lost lease: res=%+v err=%v", res, err)
	}
}

func TestResolveMeeting_LostLeaseStopsWrites(t *testing.T) {
	f := newFakePeopleRepo()
	f.contacts = []model.AccountContact{peopleContact(peopleTestContact, "Kim Minsu", ""), peopleContact(peopleTestContact2, "Park Jisoo", "")}
	f.meetings["owner:m1"].Participants = []string{"Kim Minsu", "Park Jisoo", "Lee"}
	f.existing = []model.PeopleLink{{MeetingID: "owner:m1", ToKind: "contact", ToID: "stale", Role: "speaker", Origin: model.LinkOriginAuto}}
	f.leaseLostAfter = 1
	res, err := newPeopleTestService(f).ResolveMeeting(context.Background(), "owner", "m1", PeopleRunOptions{})
	if err != nil || !res.Busy || len(f.puts) != 1 || len(f.deletes) != 0 || len(f.replaced) != 0 || f.finishes != 0 {
		t.Fatalf("mid-edge loss: res=%+v err=%v puts=%d deletes=%v replaced=%d finishes=%d", res, err, len(f.puts), f.deletes, len(f.replaced), f.finishes)
	}
	if strings.Join(f.ops, ",") != "claim,record,put,put" {
		t.Fatalf("ops %v", f.ops)
	}

	// Lost between the inventory record and the mention replace: no rows.
	// No edges are planned, so the replace is the first fenced write.
	lost := newFakePeopleRepo()
	lost.meetings["owner:m1"].Participants = []string{"Lee"}
	lost.leaseLostAfter, lost.fencedWrites = 1, 1
	res, err = newPeopleTestService(lost).ResolveMeeting(context.Background(), "owner", "m1", PeopleRunOptions{})
	if err != nil || !res.Busy || len(lost.replaced) != 0 || lost.finishes != 0 {
		t.Fatalf("record-replace loss: res=%+v err=%v replaced=%d finishes=%d", res, err, len(lost.replaced), lost.finishes)
	}
}

func TestResolveMeeting_ReportsPendingRerun(t *testing.T) {
	f := newFakePeopleRepo()
	f.reruns = []bool{true, true}
	res, err := newPeopleTestService(f).ResolveMeeting(context.Background(), "owner", "m1", PeopleRunOptions{})
	if err != nil || !res.RerunPending || !strings.Contains(res.String(), "rerun_pending=true") {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	f = newFakePeopleRepo()
	f.reruns = []bool{true, false}
	if res, _ := newPeopleTestService(f).ResolveMeeting(context.Background(), "owner", "m1", PeopleRunOptions{}); res.RerunPending {
		t.Fatalf("served rerun reported pending: %+v", res)
	}
}

func TestLoadDirectory_DescendantCapMarksEntitiesTruncated(t *testing.T) {
	f := newFakePeopleRepo()
	for i := 0; i < 201; i++ {
		id := fmt.Sprint("d", i)
		f.children["acc"] = append(f.children["acc"], id)
		f.accounts[id] = &model.Account{PK: "ACCOUNT#" + id, SK: "META", AccountID: id, ParentAccountID: "acc", Name: id, EntityType: model.EntityTypeAccount}
	}
	dir, err := newPeopleTestService(f).loadDirectory(context.Background(), "acc")
	if err != nil || len(dir.Entities) != 200 || !dir.EntitiesTruncated {
		t.Fatalf("entities=%d truncated=%v err=%v", len(dir.Entities), dir.EntitiesTruncated, err)
	}
}
