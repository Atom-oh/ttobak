package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// mockMeetingRepo is an in-memory implementation of meetingRepo for testing.
type mockMeetingRepo struct {
	meetings        map[string]*model.Meeting       // "userID|meetingID" -> meeting
	shares          map[string]*model.Share         // "sharedToID|meetingID" -> share
	attachments     map[string][]model.Attachment   // meetingID -> attachments
	meetingsByID    map[string]*model.Meeting       // meetingID -> meeting (for GSI3 lookup)
	users           map[string]*model.User          // email -> user
	members         map[string]*model.AccountMember // "accountID|userID"
	meetingRefs     map[string][]model.MeetingRef   // accountID -> refs
	accountInsights []model.AccountInsight

	// forceGetMemberNil simulates a concurrent RemoveMember that completed
	// between ShareMeetingToAccount's ListAccountMembers snapshot and its
	// per-member GetMember recheck: GetMember returns nil for this exact
	// "accountID|userID" key even though the member is still present in the
	// `members` map (so ListAccountMembers -- which reads `members` directly,
	// not through GetMember -- still returns the stale snapshot including
	// this member, exactly reproducing the race window the recheck exists to
	// close).
	forceGetMemberNil string

	// getMemberErrCount, when non-zero, makes the next N GetMember calls for
	// ANY key return an error instead of consulting `members` -- used to
	// verify a transient GetMember failure in ListMeetings isn't cached as
	// "not a member" (which would incorrectly suppress every other meeting
	// for the same account on the same page).
	getMemberErrCount int
}

func newMockMeetingRepo() *mockMeetingRepo {
	return &mockMeetingRepo{
		meetings:     make(map[string]*model.Meeting),
		shares:       make(map[string]*model.Share),
		attachments:  make(map[string][]model.Attachment),
		meetingsByID: make(map[string]*model.Meeting),
		users:        make(map[string]*model.User),
		members:      make(map[string]*model.AccountMember),
		meetingRefs:  make(map[string][]model.MeetingRef),
	}
}

func meetingKey(userID, meetingID string) string {
	return userID + "|" + meetingID
}

func shareKey(sharedToID, meetingID string) string {
	return sharedToID + "|" + meetingID
}

func (m *mockMeetingRepo) addMeeting(mtg *model.Meeting) {
	m.meetings[meetingKey(mtg.UserID, mtg.MeetingID)] = mtg
	m.meetingsByID[mtg.MeetingID] = mtg
}

func (m *mockMeetingRepo) CreateMeeting(_ context.Context, userID, title string, date time.Time, participants []string, sttProvider string) (*model.Meeting, error) {
	mtg := &model.Meeting{
		MeetingID:    "generated-id",
		UserID:       userID,
		Title:        title,
		Date:         date,
		Participants: participants,
		SttProvider:  sttProvider,
		Status:       model.StatusRecording,
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
	}
	m.addMeeting(mtg)
	return mtg, nil
}

func (m *mockMeetingRepo) GetMeeting(_ context.Context, userID, meetingID string) (*model.Meeting, error) {
	mtg, ok := m.meetings[meetingKey(userID, meetingID)]
	if !ok {
		return nil, nil
	}
	cp := *mtg
	return &cp, nil
}

func (m *mockMeetingRepo) GetMeetingByID(_ context.Context, meetingID string) (*model.Meeting, error) {
	mtg, ok := m.meetingsByID[meetingID]
	if !ok {
		return nil, nil
	}
	cp := *mtg
	return &cp, nil
}

func (m *mockMeetingRepo) UpdateMeeting(_ context.Context, meeting *model.Meeting) error {
	cp := *meeting
	cp.UpdatedAt = time.Now().UTC()
	m.meetings[meetingKey(meeting.UserID, meeting.MeetingID)] = &cp
	m.meetingsByID[meeting.MeetingID] = &cp
	return nil
}

// UpdateMeetingFields mirrors the real repo's SET-only semantics: only the
// given field names are mutated, everything else on the stored meeting is
// left untouched (unlike UpdateMeeting's full-item replace above).
func (m *mockMeetingRepo) UpdateMeetingFields(_ context.Context, userID, meetingID string, fields map[string]interface{}) error {
	key := meetingKey(userID, meetingID)
	existing, ok := m.meetings[key]
	if !ok {
		return errors.New("meeting not found")
	}
	cp := *existing
	for k, v := range fields {
		switch k {
		case "title":
			cp.Title = v.(string)
		case "content":
			cp.Content = v.(string)
		case "notes":
			cp.Notes = v.(string)
		case "liveSummary":
			cp.LiveSummary = v.(string)
		case "transcriptA":
			cp.TranscriptA = v.(string)
		case "selectedTranscript":
			cp.SelectedTranscript = v.(string)
		case "participants":
			cp.Participants = v.([]string)
		case "status":
			cp.Status = v.(string)
		}
	}
	cp.UpdatedAt = time.Now().UTC()
	m.meetings[key] = &cp
	m.meetingsByID[meetingID] = &cp
	return nil
}

func (m *mockMeetingRepo) DeleteMeeting(_ context.Context, userID, meetingID string) error {
	key := meetingKey(userID, meetingID)
	if _, ok := m.meetings[key]; !ok {
		return nil
	}
	delete(m.meetings, key)
	delete(m.meetingsByID, meetingID)
	return nil
}

func (m *mockMeetingRepo) GetShare(_ context.Context, sharedToID, meetingID string) (*model.Share, error) {
	sh, ok := m.shares[shareKey(sharedToID, meetingID)]
	if !ok {
		return nil, nil
	}
	cp := *sh
	return &cp, nil
}

func (m *mockMeetingRepo) ListAttachments(_ context.Context, meetingID string) ([]model.Attachment, error) {
	return m.attachments[meetingID], nil
}

func (m *mockMeetingRepo) ListSharesForMeeting(_ context.Context, meetingID string) ([]model.Share, error) {
	var result []model.Share
	for _, sh := range m.shares {
		if sh.MeetingID == meetingID {
			result = append(result, *sh)
		}
	}
	return result, nil
}

func (m *mockMeetingRepo) ListMeetings(_ context.Context, params repository.ListMeetingsParams) (*repository.ListMeetingsResult, error) {
	var meetings []model.Meeting
	for _, mtg := range m.meetings {
		if mtg.UserID == params.UserID {
			meetings = append(meetings, *mtg)
		}
	}
	var shares []model.Share
	for key, sh := range m.shares {
		if strings.HasPrefix(key, params.UserID+"|") {
			shares = append(shares, *sh)
		}
	}
	return &repository.ListMeetingsResult{Meetings: meetings, Shares: shares}, nil
}

func (m *mockMeetingRepo) BatchGetMeetings(_ context.Context, keys []repository.MeetingKey) ([]*model.Meeting, error) {
	var result []*model.Meeting
	for _, key := range keys {
		if mtg, ok := m.meetings[meetingKey(key.OwnerID, key.MeetingID)]; ok {
			result = append(result, mtg)
		}
	}
	return result, nil
}

func (m *mockMeetingRepo) GetOrCreateUser(_ context.Context, userID, email, name string) (*model.User, error) {
	return &model.User{UserID: userID, Email: email, Name: name}, nil
}

func (m *mockMeetingRepo) GetUserByEmail(_ context.Context, email string) (*model.User, error) {
	u, ok := m.users[email]
	if !ok {
		return nil, nil
	}
	return u, nil
}

func (m *mockMeetingRepo) CreateShare(_ context.Context, meetingID, ownerID, ownerEmail, sharedToID, email, permission, origin string) (*model.Share, error) {
	key := shareKey(sharedToID, meetingID)
	if origin == model.ShareOriginAccount {
		if existing, ok := m.shares[key]; ok && existing.Origin != model.ShareOriginAccount {
			// Never let an account-share write clobber a pre-existing direct
			// share for the same recipient+meeting.
			return existing, nil
		}
	}
	sh := &model.Share{
		MeetingID:  meetingID,
		OwnerID:    ownerID,
		SharedToID: sharedToID,
		Email:      email,
		Permission: permission,
		Origin:     origin,
	}
	m.shares[key] = sh
	return sh, nil
}

func (m *mockMeetingRepo) DeleteShare(_ context.Context, sharedToID, meetingID string) error {
	delete(m.shares, shareKey(sharedToID, meetingID))
	return nil
}

// CreateShareIfMember mirrors the real repo's atomic membership-check +
// clobber-guarded write. It reuses the forceGetMemberNil hook (same
// semantics as the standalone GetMember: simulates the member having been
// removed by write time even though ListAccountMembers' earlier snapshot --
// which reads `members` directly -- still included them), so a test can
// force the write-time check to fail for one specific member without ever
// removing them from the map ListAccountMembers already iterated.
func (m *mockMeetingRepo) CreateShareIfMember(_ context.Context, meetingID, ownerID, ownerEmail, accountID, sharedToID, email, permission string) (*model.Share, error) {
	memberKey := accountID + "|" + sharedToID
	if m.forceGetMemberNil != "" && m.forceGetMemberNil == memberKey {
		return nil, repository.ErrMemberRemoved
	}
	if _, ok := m.members[memberKey]; !ok {
		return nil, repository.ErrMemberRemoved
	}
	key := shareKey(sharedToID, meetingID)
	if existing, ok := m.shares[key]; ok && existing.Origin != model.ShareOriginAccount {
		return existing, nil
	}
	sh := &model.Share{
		MeetingID:  meetingID,
		OwnerID:    ownerID,
		SharedToID: sharedToID,
		Email:      email,
		Permission: permission,
		Origin:     model.ShareOriginAccount,
	}
	m.shares[key] = sh
	return sh, nil
}

func (m *mockMeetingRepo) GetMember(_ context.Context, accountID, userID string) (*model.AccountMember, error) {
	if m.getMemberErrCount > 0 {
		m.getMemberErrCount--
		return nil, errors.New("simulated transient GetMember error")
	}
	if m.forceGetMemberNil != "" && m.forceGetMemberNil == accountID+"|"+userID {
		return nil, nil
	}
	mem, ok := m.members[accountID+"|"+userID]
	if !ok {
		return nil, nil
	}
	cp := *mem
	return &cp, nil
}

func (m *mockMeetingRepo) ListAccountMembers(_ context.Context, accountID string) ([]model.AccountMember, error) {
	out := []model.AccountMember{}
	for _, mem := range m.members {
		if mem.AccountID == accountID {
			out = append(out, *mem)
		}
	}
	return out, nil
}

func (m *mockMeetingRepo) PutMeetingRef(_ context.Context, ref *model.MeetingRef) error {
	m.meetingRefs[ref.AccountID] = append(m.meetingRefs[ref.AccountID], *ref)
	return nil
}

func (m *mockMeetingRepo) PutAccountInsights(_ context.Context, insights []model.AccountInsight) error {
	if len(insights) == 0 {
		return nil
	}
	// Mirror the repo's replace-by-prefix semantics: drop existing items sharing
	// the meeting's SK prefix, then append the fresh set.
	prefix := insights[0].SK
	if idx := strings.LastIndex(prefix, "#"); idx >= 0 {
		prefix = prefix[:idx+1]
	}
	kept := make([]model.AccountInsight, 0, len(m.accountInsights))
	for _, ai := range m.accountInsights {
		if ai.PK == insights[0].PK && strings.HasPrefix(ai.SK, prefix) {
			continue
		}
		kept = append(kept, ai)
	}
	m.accountInsights = append(kept, insights...)
	return nil
}

// --- Tests ---

func (m *mockMeetingRepo) addMember(accountID, userID, role string) {
	m.members[accountID+"|"+userID] = &model.AccountMember{AccountID: accountID, UserID: userID, Role: role}
}

func TestLinkMeetingToAccount_OwnerMember(t *testing.T) {
	repo := newMockMeetingRepo()
	svc := newMeetingServiceWithRepo(repo)
	repo.addMeeting(&model.Meeting{MeetingID: "m-1", UserID: "owner-1", Title: "T", Status: model.StatusDone})
	repo.addMember("acc-1", "owner-1", model.RoleOwner)

	if err := svc.LinkMeetingToAccount(context.Background(), "owner-1", "m-1", "acc-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := repo.meetings[meetingKey("owner-1", "m-1")]
	if got.AccountID != "acc-1" {
		t.Errorf("expected accountId acc-1, got %s", got.AccountID)
	}
	if got.SharedToAccount {
		t.Error("link must not set SharedToAccount")
	}
}

func TestLinkMeetingToAccount_NotMemberForbidden(t *testing.T) {
	repo := newMockMeetingRepo()
	svc := newMeetingServiceWithRepo(repo)
	repo.addMeeting(&model.Meeting{MeetingID: "m-1", UserID: "owner-1", Status: model.StatusDone})
	// owner-1 is NOT a member of acc-1
	err := svc.LinkMeetingToAccount(context.Background(), "owner-1", "m-1", "acc-1")
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("expected ErrForbidden, got %v", err)
	}
}

func TestLinkMeetingToAccount_NotOwner(t *testing.T) {
	repo := newMockMeetingRepo()
	svc := newMeetingServiceWithRepo(repo)
	repo.addMeeting(&model.Meeting{MeetingID: "m-1", UserID: "owner-1", Status: model.StatusDone})
	repo.addMember("acc-1", "intruder-9", model.RoleSSA)
	err := svc.LinkMeetingToAccount(context.Background(), "intruder-9", "m-1", "acc-1")
	if !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrForbidden) {
		t.Errorf("expected ErrNotFound/ErrForbidden for non-owner, got %v", err)
	}
}

func TestShareMeetingToAccount_GrantsAndRefs(t *testing.T) {
	repo := newMockMeetingRepo()
	svc := newMeetingServiceWithRepo(repo)
	repo.addMeeting(&model.Meeting{MeetingID: "m-1", UserID: "owner-1", Title: "ROSA 리뷰", Status: model.StatusDone})
	repo.addMember("acc-1", "owner-1", model.RoleOwner)
	repo.addMember("acc-1", "tam-1", model.RoleTAM)
	repo.addMember("acc-1", "ssa-1", model.RoleSSA)

	res, err := svc.ShareMeetingToAccount(context.Background(), "owner-1", "o@x.com", "m-1", "acc-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.SharedWith != 2 { // tam-1, ssa-1 (owner excluded)
		t.Errorf("expected 2 shares, got %d", res.SharedWith)
	}
	got := repo.meetings[meetingKey("owner-1", "m-1")]
	if got.AccountID != "acc-1" || !got.SharedToAccount {
		t.Errorf("expected accountId+sharedToAccount set, got %+v", got)
	}
	if repo.shares[shareKey("tam-1", "m-1")] == nil || repo.shares[shareKey("ssa-1", "m-1")] == nil {
		t.Error("expected shares created for tam-1 and ssa-1")
	}
	if repo.shares[shareKey("owner-1", "m-1")] != nil {
		t.Error("owner must not be shared to themselves")
	}
	if len(repo.meetingRefs["acc-1"]) != 1 || repo.meetingRefs["acc-1"][0].MeetingID != "m-1" {
		t.Errorf("expected 1 meeting ref for acc-1, got %+v", repo.meetingRefs["acc-1"])
	}
}

func TestShareMeetingToAccount_SkipsRemovedMember(t *testing.T) {
	repo := newMockMeetingRepo()
	svc := newMeetingServiceWithRepo(repo)
	repo.addMeeting(&model.Meeting{MeetingID: "m-1", UserID: "owner-1", Title: "ROSA 리뷰", Status: model.StatusDone})
	repo.addMember("acc-1", "owner-1", model.RoleOwner)
	repo.addMember("acc-1", "tam-1", model.RoleTAM)
	repo.addMember("acc-1", "ssa-1", model.RoleSSA)

	// Simulate tam-1 having been removed from the account in the gap between
	// ListAccountMembers' snapshot (which still includes tam-1, since it reads
	// the `members` map directly) and CreateShareIfMember's atomic
	// membership-check-and-write for this member.
	repo.forceGetMemberNil = "acc-1|tam-1"

	res, err := svc.ShareMeetingToAccount(context.Background(), "owner-1", "o@x.com", "m-1", "acc-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.SharedWith != 1 { // only ssa-1; tam-1 skipped, owner excluded
		t.Errorf("expected 1 share (ssa-1 only), got %d", res.SharedWith)
	}
	if repo.shares[shareKey("tam-1", "m-1")] != nil {
		t.Error("expected no share created for tam-1 (removed mid-race)")
	}
	if repo.shares[shareKey("ssa-1", "m-1")] == nil {
		t.Error("expected share created for ssa-1")
	}
}

func TestCreateShare_AccountOriginNeverClobbersDirectShare(t *testing.T) {
	repo := newMockMeetingRepo()
	// Seed a pre-existing direct share for tam-1/m-1.
	repo.shares[shareKey("tam-1", "m-1")] = &model.Share{
		MeetingID: "m-1", SharedToID: "tam-1", Permission: model.PermissionEdit, Origin: "",
	}

	got, err := repo.CreateShare(context.Background(), "m-1", "owner-1", "o@x.com", "tam-1", "tam@x.com", model.PermissionRead, model.ShareOriginAccount)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Origin == model.ShareOriginAccount || got.Permission != model.PermissionEdit {
		t.Errorf("expected the pre-existing direct share preserved unchanged, got %+v", got)
	}
	stored := repo.shares[shareKey("tam-1", "m-1")]
	if stored.Origin == model.ShareOriginAccount {
		t.Errorf("direct share must not be overwritten by an account-origin write, got %+v", stored)
	}
}

func TestShareMeetingToAccount_TransactionalWriteNeverClobbersDirectShare(t *testing.T) {
	repo := newMockMeetingRepo()
	svc := newMeetingServiceWithRepo(repo)
	repo.addMeeting(&model.Meeting{MeetingID: "m-1", UserID: "owner-1", Title: "ROSA 리뷰", Status: model.StatusDone})
	repo.addMember("acc-1", "owner-1", model.RoleOwner)
	repo.addMember("acc-1", "tam-1", model.RoleTAM)

	// tam-1 already has a direct (non-account-origin) share on this meeting,
	// e.g. from an earlier ShareMeetingByEmail call by the owner.
	repo.shares[shareKey("tam-1", "m-1")] = &model.Share{
		MeetingID: "m-1", SharedToID: "tam-1", Permission: model.PermissionEdit, Origin: "",
	}

	res, err := svc.ShareMeetingToAccount(context.Background(), "owner-1", "o@x.com", "m-1", "acc-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.SharedWith != 1 {
		t.Errorf("expected tam-1 counted as shared (already has access via direct share), got %d", res.SharedWith)
	}
	stored := repo.shares[shareKey("tam-1", "m-1")]
	if stored.Origin == model.ShareOriginAccount || stored.Permission != model.PermissionEdit {
		t.Errorf("expected pre-existing direct share preserved unchanged through the transactional path, got %+v", stored)
	}
}

func TestShareMeetingToAccount_NotMemberForbidden(t *testing.T) {
	repo := newMockMeetingRepo()
	svc := newMeetingServiceWithRepo(repo)
	repo.addMeeting(&model.Meeting{MeetingID: "m-1", UserID: "owner-1", Status: model.StatusDone})
	_, err := svc.ShareMeetingToAccount(context.Background(), "owner-1", "o@x.com", "m-1", "acc-1")
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("expected ErrForbidden, got %v", err)
	}
}

func TestCreateMeeting(t *testing.T) {
	repo := newMockMeetingRepo()
	svc := newMeetingServiceWithRepo(repo)

	meeting, err := svc.CreateMeeting(context.Background(), "user-1", "Test Meeting", time.Now(), []string{"Alice"}, "transcribe")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if meeting.Title != "Test Meeting" {
		t.Errorf("expected title 'Test Meeting', got %q", meeting.Title)
	}
	if meeting.Status != model.StatusRecording {
		t.Errorf("expected status 'recording', got %q", meeting.Status)
	}
}

func TestCreateMeeting_EmptyTitle(t *testing.T) {
	repo := newMockMeetingRepo()
	svc := newMeetingServiceWithRepo(repo)

	_, err := svc.CreateMeeting(context.Background(), "user-1", "", time.Now(), nil, "")
	if err == nil {
		t.Fatal("expected error for empty title, got nil")
	}
}

func TestGetMeetingDetail_Owner(t *testing.T) {
	repo := newMockMeetingRepo()
	svc := newMeetingServiceWithRepo(repo)

	repo.addMeeting(&model.Meeting{
		MeetingID: "m-1", UserID: "user-1", Title: "My Meeting",
		Status: model.StatusDone, Content: "summary text",
		Date: time.Now(), CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})

	detail, err := svc.GetMeetingDetail(context.Background(), "user-1", "m-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if detail.Title != "My Meeting" {
		t.Errorf("expected title 'My Meeting', got %q", detail.Title)
	}
	if detail.Content != "summary text" {
		t.Errorf("expected content 'summary text', got %q", detail.Content)
	}
}

func TestGetMeetingDetail_SharedReadAccess(t *testing.T) {
	repo := newMockMeetingRepo()
	svc := newMeetingServiceWithRepo(repo)

	repo.addMeeting(&model.Meeting{
		MeetingID: "m-1", UserID: "owner-1", Title: "Shared Meeting",
		Status: model.StatusDone, Date: time.Now(), CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	repo.shares[shareKey("reader-1", "m-1")] = &model.Share{
		MeetingID: "m-1", OwnerID: "owner-1", SharedToID: "reader-1",
		Permission: model.PermissionRead,
	}

	detail, err := svc.GetMeetingDetail(context.Background(), "reader-1", "m-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if detail.Title != "Shared Meeting" {
		t.Errorf("expected title 'Shared Meeting', got %q", detail.Title)
	}
	// Shares should not be visible to non-owners
	if len(detail.Shares) > 0 {
		t.Error("expected no shares visible to non-owner")
	}
}

func TestGetMeetingDetail_NotFound(t *testing.T) {
	repo := newMockMeetingRepo()
	svc := newMeetingServiceWithRepo(repo)

	_, err := svc.GetMeetingDetail(context.Background(), "user-1", "nonexistent")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestGetMeetingDetail_ExpiresStuckRecording(t *testing.T) {
	repo := newMockMeetingRepo()
	svc := newMeetingServiceWithRepo(repo)

	old := time.Now().Add(-7 * time.Hour)
	repo.addMeeting(&model.Meeting{
		MeetingID: "m-1", UserID: "user-1", Title: "Abandoned Recording",
		Status: model.StatusRecording,
		Date:   old, CreatedAt: old, UpdatedAt: old,
	})

	detail, err := svc.GetMeetingDetail(context.Background(), "user-1", "m-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if detail.Status != model.StatusError {
		t.Errorf("expected status %q, got %q", model.StatusError, detail.Status)
	}

	persisted, _ := repo.GetMeetingByID(context.Background(), "m-1")
	if persisted.Status != model.StatusError {
		t.Errorf("expected persisted status %q, got %q", model.StatusError, persisted.Status)
	}
}

func TestGetMeetingDetail_RecentRecordingNotExpired(t *testing.T) {
	repo := newMockMeetingRepo()
	svc := newMeetingServiceWithRepo(repo)

	recent := time.Now().Add(-10 * time.Minute)
	repo.addMeeting(&model.Meeting{
		MeetingID: "m-1", UserID: "user-1", Title: "Live Recording",
		Status: model.StatusRecording,
		Date:   recent, CreatedAt: recent, UpdatedAt: recent,
	})

	detail, err := svc.GetMeetingDetail(context.Background(), "user-1", "m-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if detail.Status != model.StatusRecording {
		t.Errorf("expected status still %q, got %q", model.StatusRecording, detail.Status)
	}
}

func TestGetMeetingDetail_ExpiresStuckTranscribing(t *testing.T) {
	repo := newMockMeetingRepo()
	svc := newMeetingServiceWithRepo(repo)

	old := time.Now().Add(-31 * time.Minute)
	repo.addMeeting(&model.Meeting{
		MeetingID: "m-1", UserID: "user-1", Title: "Stuck Transcribing",
		Status: model.StatusTranscribing,
		Date:   old, CreatedAt: old, UpdatedAt: old,
	})

	detail, err := svc.GetMeetingDetail(context.Background(), "user-1", "m-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if detail.Status != model.StatusError {
		t.Errorf("expected status %q, got %q", model.StatusError, detail.Status)
	}
}

func TestUpdateMeeting_OwnerCanUpdate(t *testing.T) {
	repo := newMockMeetingRepo()
	svc := newMeetingServiceWithRepo(repo)

	repo.addMeeting(&model.Meeting{
		MeetingID: "m-1", UserID: "user-1", Title: "Old Title",
		Status: model.StatusDone, Date: time.Now(), CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})

	result, err := svc.UpdateMeeting(context.Background(), "user-1", "m-1", &model.UpdateMeetingRequest{Title: "New Title"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.MeetingID != "m-1" {
		t.Errorf("expected meetingId 'm-1', got %q", result.MeetingID)
	}

	// Verify the update was persisted
	updated := repo.meetingsByID["m-1"]
	if updated.Title != "New Title" {
		t.Errorf("expected title 'New Title', got %q", updated.Title)
	}
}

func TestUpdateMeeting_OmittedNotesPreservesExisting(t *testing.T) {
	repo := newMockMeetingRepo()
	svc := newMeetingServiceWithRepo(repo)

	repo.addMeeting(&model.Meeting{
		MeetingID: "m-1", UserID: "user-1", Title: "Title", Notes: "existing notes",
		Status: model.StatusDone, Date: time.Now(), CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})

	_, err := svc.UpdateMeeting(context.Background(), "user-1", "m-1", &model.UpdateMeetingRequest{Title: "Title"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo.meetingsByID["m-1"].Notes != "existing notes" {
		t.Errorf("expected notes preserved when omitted, got %q", repo.meetingsByID["m-1"].Notes)
	}
}

func TestUpdateMeeting_ExplicitEmptyNotesClearsExisting(t *testing.T) {
	repo := newMockMeetingRepo()
	svc := newMeetingServiceWithRepo(repo)

	repo.addMeeting(&model.Meeting{
		MeetingID: "m-1", UserID: "user-1", Title: "Title", Notes: "existing notes",
		Status: model.StatusDone, Date: time.Now(), CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})

	// A non-nil pointer to "" (the user deleted everything in the notes
	// editor) must actually clear the stored notes -- distinct from the
	// omitted-field (nil) "preserve" case above.
	_, err := svc.UpdateMeeting(context.Background(), "user-1", "m-1", &model.UpdateMeetingRequest{
		Title: "Title", Notes: mdPtr(""),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo.meetingsByID["m-1"].Notes != "" {
		t.Errorf("expected notes cleared to empty, got %q", repo.meetingsByID["m-1"].Notes)
	}
}

func TestUpdateMeeting_LiveSummarySemantics(t *testing.T) {
	// Same omit-vs-explicit-empty contract as Notes, plus the write-time cap
	// (untrusted client input fed into the summarize prompt).
	newRepoWith := func(liveSummary string) (*mockMeetingRepo, *MeetingService) {
		repo := newMockMeetingRepo()
		repo.addMeeting(&model.Meeting{
			MeetingID: "m-1", UserID: "user-1", Title: "Title", LiveSummary: liveSummary,
			Status: model.StatusDone, Date: time.Now(), CreatedAt: time.Now(), UpdatedAt: time.Now(),
		})
		return repo, newMeetingServiceWithRepo(repo)
	}

	t.Run("omitted preserves existing", func(t *testing.T) {
		repo, svc := newRepoWith("existing live summary")
		if _, err := svc.UpdateMeeting(context.Background(), "user-1", "m-1", &model.UpdateMeetingRequest{Title: "Title"}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := repo.meetingsByID["m-1"].LiveSummary; got != "existing live summary" {
			t.Errorf("expected live summary preserved when omitted, got %q", got)
		}
	})

	t.Run("explicit empty clears existing", func(t *testing.T) {
		repo, svc := newRepoWith("existing live summary")
		if _, err := svc.UpdateMeeting(context.Background(), "user-1", "m-1", &model.UpdateMeetingRequest{
			Title: "Title", LiveSummary: mdPtr(""),
		}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := repo.meetingsByID["m-1"].LiveSummary; got != "" {
			t.Errorf("expected live summary cleared to empty, got %q", got)
		}
	})

	t.Run("set stores the value", func(t *testing.T) {
		repo, svc := newRepoWith("")
		if _, err := svc.UpdateMeeting(context.Background(), "user-1", "m-1", &model.UpdateMeetingRequest{
			Title: "Title", LiveSummary: mdPtr("## live\nnew summary"),
		}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := repo.meetingsByID["m-1"].LiveSummary; got != "## live\nnew summary" {
			t.Errorf("expected live summary stored, got %q", got)
		}
	})

	t.Run("over-cap rejected with ErrInvalidInput", func(t *testing.T) {
		repo, svc := newRepoWith("keep me")
		oversized := strings.Repeat("가", model.MaxLiveSummaryRunes+1)
		_, err := svc.UpdateMeeting(context.Background(), "user-1", "m-1", &model.UpdateMeetingRequest{
			Title: "Title", LiveSummary: mdPtr(oversized),
		})
		if !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("expected ErrInvalidInput, got %v", err)
		}
		if got := repo.meetingsByID["m-1"].LiveSummary; got != "keep me" {
			t.Errorf("expected stored live summary untouched after rejection, got %d chars", len(got))
		}
	})
}

func TestUpdateMeeting_OutOfOrderNotesUpdateDoesNotClobberOtherFields(t *testing.T) {
	// Simulates a lingering notes autosave PUT landing AFTER a status
	// transition -- with the old read-modify-write PutItem, a stale notes
	// update would read the meeting BEFORE the status change, then write
	// the whole item back, silently reverting status. UpdateMeetingFields
	// (SET expression) must only ever touch the field it's given.
	repo := newMockMeetingRepo()
	svc := newMeetingServiceWithRepo(repo)

	repo.addMeeting(&model.Meeting{
		MeetingID: "m-1", UserID: "user-1", Title: "Title", Notes: "old notes",
		Status: model.StatusRecording, Date: time.Now(), CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})

	// Status transitions to "transcribing" (as resumeUploadFlow does).
	if _, err := svc.UpdateMeeting(context.Background(), "user-1", "m-1", &model.UpdateMeetingRequest{
		Title: "Title", Status: model.StatusTranscribing,
	}); err != nil {
		t.Fatalf("unexpected error on status update: %v", err)
	}

	// A stale notes-only autosave "arrives late" and must not touch status.
	if _, err := svc.UpdateMeeting(context.Background(), "user-1", "m-1", &model.UpdateMeetingRequest{
		Title: "Title", Notes: mdPtr("stale notes from before the transition"),
	}); err != nil {
		t.Fatalf("unexpected error on notes update: %v", err)
	}

	updated := repo.meetingsByID["m-1"]
	if updated.Status != model.StatusTranscribing {
		t.Errorf("expected status to remain %q, got %q", model.StatusTranscribing, updated.Status)
	}
	if updated.Notes != "stale notes from before the transition" {
		t.Errorf("expected notes updated, got %q", updated.Notes)
	}
}

func TestUpdateMeeting_ReadOnlyShareForbidden(t *testing.T) {
	repo := newMockMeetingRepo()
	svc := newMeetingServiceWithRepo(repo)

	repo.addMeeting(&model.Meeting{
		MeetingID: "m-1", UserID: "owner-1", Title: "Meeting",
		Status: model.StatusDone, Date: time.Now(), CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	repo.shares[shareKey("reader-1", "m-1")] = &model.Share{
		MeetingID: "m-1", OwnerID: "owner-1", SharedToID: "reader-1",
		Permission: model.PermissionRead,
	}

	_, err := svc.UpdateMeeting(context.Background(), "reader-1", "m-1", &model.UpdateMeetingRequest{Title: "Hacked"})
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("expected ErrForbidden, got %v", err)
	}
}

func TestUpdateMeeting_EditShareAllowed(t *testing.T) {
	repo := newMockMeetingRepo()
	svc := newMeetingServiceWithRepo(repo)

	repo.addMeeting(&model.Meeting{
		MeetingID: "m-1", UserID: "owner-1", Title: "Meeting",
		Status: model.StatusDone, Date: time.Now(), CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	repo.shares[shareKey("editor-1", "m-1")] = &model.Share{
		MeetingID: "m-1", OwnerID: "owner-1", SharedToID: "editor-1",
		Permission: model.PermissionEdit,
	}

	_, err := svc.UpdateMeeting(context.Background(), "editor-1", "m-1", &model.UpdateMeetingRequest{Title: "Updated"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDeleteMeeting_OwnerOnly(t *testing.T) {
	repo := newMockMeetingRepo()
	svc := newMeetingServiceWithRepo(repo)

	repo.addMeeting(&model.Meeting{
		MeetingID: "m-1", UserID: "owner-1", Title: "Meeting",
		Status: model.StatusDone, Date: time.Now(), CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})

	// Non-owner should be forbidden
	err := svc.DeleteMeeting(context.Background(), "other-user", "m-1")
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("expected ErrForbidden for non-owner, got %v", err)
	}

	// Owner should succeed
	err = svc.DeleteMeeting(context.Background(), "owner-1", "m-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify deleted
	if _, ok := repo.meetingsByID["m-1"]; ok {
		t.Error("expected meeting to be deleted")
	}
}

func TestDeleteMeeting_NotFound(t *testing.T) {
	repo := newMockMeetingRepo()
	svc := newMeetingServiceWithRepo(repo)

	err := svc.DeleteMeeting(context.Background(), "user-1", "nonexistent")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestUpdateSpeakers_ReplacesInAllFields(t *testing.T) {
	repo := newMockMeetingRepo()
	svc := newMeetingServiceWithRepo(repo)

	repo.addMeeting(&model.Meeting{
		MeetingID: "m-1", UserID: "user-1", Title: "Meeting",
		Status:             model.StatusDone,
		Content:            "spk_0 said hello",
		TranscriptA:        "spk_0: hello",
		TranscriptSegments: `[{"speaker":"spk_0","text":"hello"}]`,
		ActionItems:        `[{"text":"spk_0 will do it"}]`,
		Date:               time.Now(), CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})

	_, err := svc.UpdateSpeakers(context.Background(), "user-1", "m-1", &model.UpdateSpeakersRequest{
		SpeakerMap: map[string]string{"spk_0": "Kim"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated := repo.meetingsByID["m-1"]
	if updated.Content != "Kim said hello" {
		t.Errorf("expected content 'Kim said hello', got %q", updated.Content)
	}
	if updated.TranscriptA != "Kim: hello" {
		t.Errorf("expected transcriptA 'Kim: hello', got %q", updated.TranscriptA)
	}
	if updated.TranscriptSegments != `[{"speaker":"Kim","text":"hello","startTime":0,"endTime":0}]` {
		t.Errorf("expected renamed TranscriptSegments, got %q", updated.TranscriptSegments)
	}
}

// TestUpdateSpeakers_NoPrefixCollisionWithNamespacedLabel covers the bug the
// round-2 review found: a multi-part meeting's namespaced label "spk_1000000"
// (see internal/speaker.Namespace) must not be corrupted when the user
// renames the unrelated part-0 label "spk_1" -- a plain strings.ReplaceAll
// would turn "spk_1000000" into "Kim000000".
func TestUpdateSpeakers_NoPrefixCollisionWithNamespacedLabel(t *testing.T) {
	repo := newMockMeetingRepo()
	svc := newMeetingServiceWithRepo(repo)

	repo.addMeeting(&model.Meeting{
		MeetingID: "m-1", UserID: "user-1", Title: "Meeting",
		Status:             model.StatusDone,
		Content:            "[spk_1]\nhello\n\n[spk_1000000]\nworld",
		TranscriptSegments: `[{"speaker":"spk_1","text":"hello"},{"speaker":"spk_1000000","text":"world"}]`,
		Date:               time.Now(), CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})

	_, err := svc.UpdateSpeakers(context.Background(), "user-1", "m-1", &model.UpdateSpeakersRequest{
		SpeakerMap: map[string]string{"spk_1": "Kim"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated := repo.meetingsByID["m-1"]
	if updated.Content != "[Kim]\nhello\n\n[spk_1000000]\nworld" {
		t.Errorf("expected spk_1000000 left untouched, got %q", updated.Content)
	}
	if updated.TranscriptSegments != `[{"speaker":"Kim","text":"hello","startTime":0,"endTime":0},{"speaker":"spk_1000000","text":"world","startTime":0,"endTime":0}]` {
		t.Errorf("expected spk_1000000 segment untouched, got %q", updated.TranscriptSegments)
	}
}

func TestShareMeeting_CannotShareWithSelf(t *testing.T) {
	repo := newMockMeetingRepo()
	svc := newMeetingServiceWithRepo(repo)

	repo.addMeeting(&model.Meeting{
		MeetingID: "m-1", UserID: "user-1", Title: "Meeting",
		Status: model.StatusDone, Date: time.Now(), CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	repo.users["user@test.com"] = &model.User{UserID: "user-1", Email: "user@test.com"}

	_, err := svc.ShareMeetingByEmail(context.Background(), "user-1", "user@test.com", "m-1", "user@test.com", "read")
	if err == nil {
		t.Fatal("expected error for self-share, got nil")
	}
}

func TestSelectTranscript_ReadOnlyForbidden(t *testing.T) {
	repo := newMockMeetingRepo()
	svc := newMeetingServiceWithRepo(repo)

	repo.addMeeting(&model.Meeting{
		MeetingID: "m-1", UserID: "owner-1", Title: "Meeting",
		Status: model.StatusDone, Date: time.Now(), CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	repo.shares[shareKey("reader-1", "m-1")] = &model.Share{
		MeetingID: "m-1", OwnerID: "owner-1", SharedToID: "reader-1",
		Permission: model.PermissionRead,
	}

	err := svc.SelectTranscript(context.Background(), "reader-1", "m-1", "B")
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("expected ErrForbidden, got %v", err)
	}
}

func TestBuildAccountInsights(t *testing.T) {
	when := time.Date(2026, 5, 12, 9, 0, 0, 0, time.UTC)
	meeting := &model.Meeting{
		MeetingID: "m-1", UserID: "owner-1", Date: when,
		Insights: `[{"id":"ins_1","type":"risk","text":"일정 지연","evidence":"보안 검토 일정 미확정","implication":"오픈 일정 영향","nextAction":"검토 일정 확정","entities":["PoC"]},{"id":"ins_2","type":"opportunity","text":"확대 여지"}]`,
	}
	items, err := BuildAccountInsights("acc-1", meeting)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2, got %d", len(items))
	}
	got := items[0]
	if got.PK != model.PrefixAccount+"acc-1" {
		t.Errorf("bad PK: %s", got.PK)
	}
	wantSK := model.PrefixInsight + when.Format(time.RFC3339) + "#m-1#0"
	if got.SK != wantSK {
		t.Errorf("bad SK: got %s want %s", got.SK, wantSK)
	}
	if got.Type != "risk" || got.SourceType != "meeting" || got.SourceID != "m-1" || got.SourceUserID != "owner-1" {
		t.Errorf("bad fields: %+v", got)
	}
	if got.Evidence != "보안 검토 일정 미확정" || got.Implication != "오픈 일정 영향" || got.NextAction != "검토 일정 확정" {
		t.Errorf("structured fields were not fanned out: %+v", got)
	}
	if !got.OccurredAt.Equal(when) {
		t.Errorf("bad occurredAt: %v", got.OccurredAt)
	}
}

func TestBuildAccountInsights_Empty(t *testing.T) {
	items, err := BuildAccountInsights("acc-1", &model.Meeting{MeetingID: "m-1"})
	if err != nil || items != nil {
		t.Errorf("expected nil,nil for no insights; got %v, %v", items, err)
	}
}

func TestShareMeetingToAccount_FansOutInsights(t *testing.T) {
	repo := newMockMeetingRepo()
	svc := newMeetingServiceWithRepo(repo)
	repo.addMeeting(&model.Meeting{
		MeetingID: "m-1", UserID: "owner-1", Title: "ROSA", Status: model.StatusDone,
		Date:     time.Date(2026, 5, 12, 9, 0, 0, 0, time.UTC),
		Insights: `[{"type":"risk","text":"지연"},{"type":"opportunity","text":"확대"}]`,
	})
	repo.addMember("acc-1", "owner-1", model.RoleOwner)

	if _, err := svc.ShareMeetingToAccount(context.Background(), "owner-1", "o@x.com", "m-1", "acc-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(repo.accountInsights) != 2 {
		t.Fatalf("expected 2 fanned-out insights, got %d", len(repo.accountInsights))
	}
	if repo.accountInsights[0].AccountID != "acc-1" || repo.accountInsights[0].SourceID != "m-1" {
		t.Errorf("unexpected fanned insight: %+v", repo.accountInsights[0])
	}
}

func TestShareMeetingToAccount_NoInsightsNoFanout(t *testing.T) {
	repo := newMockMeetingRepo()
	svc := newMeetingServiceWithRepo(repo)
	repo.addMeeting(&model.Meeting{MeetingID: "m-1", UserID: "owner-1", Status: model.StatusDone})
	repo.addMember("acc-1", "owner-1", model.RoleOwner)
	if _, err := svc.ShareMeetingToAccount(context.Background(), "owner-1", "o@x.com", "m-1", "acc-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(repo.accountInsights) != 0 {
		t.Errorf("expected no fanout for meeting without insights, got %d", len(repo.accountInsights))
	}
}

func TestGetMeetingDetail_AccountMemberReadsSharedMeeting(t *testing.T) {
	repo := newMockMeetingRepo()
	svc := newMeetingServiceWithRepo(repo)
	// owner-1's meeting shared to acc-1; "late-1" is an account member added later
	// with NO explicit Share record.
	repo.addMeeting(&model.Meeting{MeetingID: "m-1", UserID: "owner-1", Title: "ROSA", Status: model.StatusDone, AccountID: "acc-1", SharedToAccount: true})
	repo.addMember("acc-1", "owner-1", model.RoleOwner)
	repo.addMember("acc-1", "late-1", model.RoleTAM)

	resp, err := svc.GetMeetingDetail(context.Background(), "late-1", "m-1")
	if err != nil {
		t.Fatalf("expected account member to read shared meeting, got %v", err)
	}
	if resp == nil {
		t.Fatal("expected meeting detail for account member")
	}

	// A non-member must not read it.
	if _, err := svc.GetMeetingDetail(context.Background(), "stranger-9", "m-1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound for non-member, got %v", err)
	}
}

func TestGetMeetingDetail_LinkedButNotSharedStaysPrivate(t *testing.T) {
	repo := newMockMeetingRepo()
	svc := newMeetingServiceWithRepo(repo)
	// AccountID set but NOT shared (Link only) → account member must NOT read.
	repo.addMeeting(&model.Meeting{MeetingID: "m-2", UserID: "owner-1", Status: model.StatusDone, AccountID: "acc-1", SharedToAccount: false})
	repo.addMember("acc-1", "late-1", model.RoleTAM)

	if _, err := svc.GetMeetingDetail(context.Background(), "late-1", "m-2"); !errors.Is(err, ErrNotFound) {
		t.Errorf("linked-but-not-shared meeting must stay private to account members, got %v", err)
	}
}

func TestResolveSharedAccessOrNotFound_NoAccessReturnsErrNotFound(t *testing.T) {
	repo := newMockMeetingRepo()
	// No meeting, no share, no membership at all -- resolveSharedAccess's
	// zero-value (nil, "", nil) contract must be converted to ErrNotFound by
	// the OrNotFound wrapper, the exact regression flagged for
	// KnowledgeService.Ask (a caller that has no fallthrough of its own and
	// needs an error, not a value it must remember to nil-check).
	repo.addMeeting(&model.Meeting{MeetingID: "m-1", UserID: "owner-1", Status: model.StatusDone})

	meeting, permission, err := resolveSharedAccessOrNotFound(context.Background(), repo, "stranger-1", "m-1")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
	if meeting != nil {
		t.Errorf("expected nil meeting on no access, got %+v", meeting)
	}
	if permission != "" {
		t.Errorf("expected empty permission on no access, got %q", permission)
	}
}

func TestResolveSharedAccessOrNotFound_ValidShareReturnsNoError(t *testing.T) {
	repo := newMockMeetingRepo()
	repo.addMeeting(&model.Meeting{MeetingID: "m-1", UserID: "owner-1", Status: model.StatusDone})
	repo.shares[shareKey("reader-1", "m-1")] = &model.Share{
		MeetingID: "m-1", OwnerID: "owner-1", SharedToID: "reader-1", Permission: model.PermissionRead,
	}

	meeting, permission, err := resolveSharedAccessOrNotFound(context.Background(), repo, "reader-1", "m-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if meeting == nil || meeting.MeetingID != "m-1" {
		t.Errorf("expected meeting m-1, got %+v", meeting)
	}
	if permission != model.PermissionRead {
		t.Errorf("expected read permission, got %q", permission)
	}
}

func TestGetMeetingDetail_StaleAccountShareDeniedAfterMembershipRemoved(t *testing.T) {
	repo := newMockMeetingRepo()
	svc := newMeetingServiceWithRepo(repo)
	// Simulates RemoveMember's cleanup failing to delete the account-origin
	// Share row (e.g. a transient DynamoDB error) after membership itself was
	// already deleted -- the exact permanent-access gap this checkAccess fix
	// closes: read-time membership re-verification denies access even though
	// the stale Share row is still present.
	repo.addMeeting(&model.Meeting{MeetingID: "m-1", UserID: "owner-1", Status: model.StatusDone, AccountID: "acc-1", SharedToAccount: true})
	repo.shares[shareKey("removed-1", "m-1")] = &model.Share{
		MeetingID: "m-1", OwnerID: "owner-1", SharedToID: "removed-1",
		Permission: model.PermissionRead, Origin: model.ShareOriginAccount,
	}
	// "removed-1" is NOT in repo.members -- membership already deleted.

	if _, err := svc.GetMeetingDetail(context.Background(), "removed-1", "m-1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound for a removed member with a stale account-origin share, got %v", err)
	}
}

func TestGetMeetingDetail_DirectSharePersistsAfterMembershipRemoved(t *testing.T) {
	repo := newMockMeetingRepo()
	svc := newMeetingServiceWithRepo(repo)
	// A direct share (Origin=="") is an independent grant the owner made
	// explicitly -- it must survive account membership removal, unlike an
	// account-origin share.
	repo.addMeeting(&model.Meeting{MeetingID: "m-1", UserID: "owner-1", Status: model.StatusDone, AccountID: "acc-1", SharedToAccount: true})
	repo.shares[shareKey("direct-1", "m-1")] = &model.Share{
		MeetingID: "m-1", OwnerID: "owner-1", SharedToID: "direct-1",
		Permission: model.PermissionRead,
	}
	// "direct-1" is NOT an account member at all.

	if _, err := svc.GetMeetingDetail(context.Background(), "direct-1", "m-1"); err != nil {
		t.Errorf("expected direct share to grant access regardless of account membership, got %v", err)
	}
}

func TestGetMeetingDetail_AccountShareGrantsAccessDespiteSnapshotRace(t *testing.T) {
	repo := newMockMeetingRepo()
	svc := newMeetingServiceWithRepo(repo)
	// Simulates the snapshot-race MAJOR: a CreateShareIfMember transaction
	// commits a Share row for a member NOT present in RemoveMember's earlier
	// ListMeetingRefsForAccount snapshot (e.g. the ref was created after the
	// snapshot). Read-time membership re-verification means this doesn't
	// depend on the Share row being freshly written -- it works the same way
	// whether or not the member is still present.
	repo.addMeeting(&model.Meeting{MeetingID: "m-1", UserID: "owner-1", Status: model.StatusDone, AccountID: "acc-1", SharedToAccount: true})
	repo.addMember("acc-1", "late-1", model.RoleTAM)
	repo.shares[shareKey("late-1", "m-1")] = &model.Share{
		MeetingID: "m-1", OwnerID: "owner-1", SharedToID: "late-1",
		Permission: model.PermissionRead, Origin: model.ShareOriginAccount,
	}

	if _, err := svc.GetMeetingDetail(context.Background(), "late-1", "m-1"); err != nil {
		t.Errorf("expected account member with a valid account-origin share to have access, got %v", err)
	}
}

func TestListMeetings_StaleAccountShareOmittedAfterMembershipRemoved(t *testing.T) {
	repo := newMockMeetingRepo()
	svc := newMeetingServiceWithRepo(repo)
	// Metadata (title/summary) must not leak via the list endpoint either --
	// checkAccess blocking the detail view isn't enough on its own.
	repo.addMeeting(&model.Meeting{MeetingID: "m-1", UserID: "owner-1", Title: "Secret Plans", Status: model.StatusDone, AccountID: "acc-1", SharedToAccount: true})
	repo.shares[shareKey("removed-1", "m-1")] = &model.Share{
		MeetingID: "m-1", OwnerID: "owner-1", SharedToID: "removed-1",
		Permission: model.PermissionRead, Origin: model.ShareOriginAccount,
	}
	// "removed-1" is NOT in repo.members -- membership already deleted.

	resp, err := svc.ListMeetings(context.Background(), "removed-1", "", "", 20)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, item := range resp.Meetings {
		if item.MeetingID == "m-1" {
			t.Errorf("expected stale account-origin share to be omitted from list, got %+v", item)
		}
	}
}

func TestListMeetings_ValidAccountShareIncluded(t *testing.T) {
	repo := newMockMeetingRepo()
	svc := newMeetingServiceWithRepo(repo)
	repo.addMeeting(&model.Meeting{MeetingID: "m-1", UserID: "owner-1", Title: "Team Sync", Status: model.StatusDone, AccountID: "acc-1", SharedToAccount: true})
	repo.addMember("acc-1", "member-1", model.RoleTAM)
	repo.shares[shareKey("member-1", "m-1")] = &model.Share{
		MeetingID: "m-1", OwnerID: "owner-1", SharedToID: "member-1",
		Permission: model.PermissionRead, Origin: model.ShareOriginAccount,
	}

	resp, err := svc.ListMeetings(context.Background(), "member-1", "", "", 20)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	found := false
	for _, item := range resp.Meetings {
		if item.MeetingID == "m-1" {
			found = true
		}
	}
	if !found {
		t.Error("expected valid account-origin share to appear in list")
	}
}

func TestListMeetings_UnsharedFromAccountOmittedDespiteLingeringShareAndMembership(t *testing.T) {
	repo := newMockMeetingRepo()
	svc := newMeetingServiceWithRepo(repo)
	// The owner un-shared the meeting from the account (or it was only ever
	// Link-only) -- SharedToAccount is false -- but a stale account-origin
	// Share row lingers, and the caller is still a member of the account.
	// Without the SharedToAccount check, this leaks the title/summary here
	// even though checkAccess correctly blocks the detail view for the same
	// row (an invariant mismatch the review flagged).
	repo.addMeeting(&model.Meeting{MeetingID: "m-1", UserID: "owner-1", Title: "Confidential", Status: model.StatusDone, AccountID: "acc-1", SharedToAccount: false})
	repo.addMember("acc-1", "member-1", model.RoleTAM)
	repo.shares[shareKey("member-1", "m-1")] = &model.Share{
		MeetingID: "m-1", OwnerID: "owner-1", SharedToID: "member-1",
		Permission: model.PermissionRead, Origin: model.ShareOriginAccount,
	}

	resp, err := svc.ListMeetings(context.Background(), "member-1", "", "", 20)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, item := range resp.Meetings {
		if item.MeetingID == "m-1" {
			t.Errorf("expected unshared-from-account meeting to be omitted from list, got %+v", item)
		}
	}
}

func TestListMeetings_TransientGetMemberErrorNotCachedAsNonMember(t *testing.T) {
	repo := newMockMeetingRepo()
	svc := newMeetingServiceWithRepo(repo)
	// Two meetings shared via the same account. If a transient GetMember
	// error were cached as "not a member" (keyed by accountID, shared across
	// meetings on this page), BOTH meetings would be suppressed instead of
	// just the one whose GetMember call actually failed. Map iteration order
	// is non-deterministic, so this doesn't assert which specific meeting
	// hits the forced error -- only that exactly one of the two is missing
	// (the one that errored), not both.
	repo.addMeeting(&model.Meeting{MeetingID: "m-1", UserID: "owner-1", Title: "First", Status: model.StatusDone, AccountID: "acc-1", SharedToAccount: true})
	repo.addMeeting(&model.Meeting{MeetingID: "m-2", UserID: "owner-1", Title: "Second", Status: model.StatusDone, AccountID: "acc-1", SharedToAccount: true})
	repo.addMember("acc-1", "member-1", model.RoleTAM)
	repo.shares[shareKey("member-1", "m-1")] = &model.Share{
		MeetingID: "m-1", OwnerID: "owner-1", SharedToID: "member-1",
		Permission: model.PermissionRead, Origin: model.ShareOriginAccount,
	}
	repo.shares[shareKey("member-1", "m-2")] = &model.Share{
		MeetingID: "m-2", OwnerID: "owner-1", SharedToID: "member-1",
		Permission: model.PermissionRead, Origin: model.ShareOriginAccount,
	}
	repo.getMemberErrCount = 1 // exactly one GetMember call fails

	resp, err := svc.ListMeetings(context.Background(), "member-1", "", "", 20)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Meetings) != 1 {
		t.Errorf("expected exactly 1 meeting to survive the single transient GetMember error (caching would suppress both), got %d: %+v", len(resp.Meetings), resp.Meetings)
	}
}

func TestListMeetings_DirectShareIncludedRegardlessOfMembership(t *testing.T) {
	repo := newMockMeetingRepo()
	svc := newMeetingServiceWithRepo(repo)
	repo.addMeeting(&model.Meeting{MeetingID: "m-1", UserID: "owner-1", Title: "1:1", Status: model.StatusDone})
	repo.shares[shareKey("direct-1", "m-1")] = &model.Share{
		MeetingID: "m-1", OwnerID: "owner-1", SharedToID: "direct-1",
		Permission: model.PermissionRead,
	}

	resp, err := svc.ListMeetings(context.Background(), "direct-1", "", "", 20)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	found := false
	for _, item := range resp.Meetings {
		if item.MeetingID == "m-1" {
			found = true
		}
	}
	if !found {
		t.Error("expected direct share to appear in list regardless of account membership")
	}
}

func TestShareMeetingToAccount_ReplacesStaleInsights(t *testing.T) {
	repo := newMockMeetingRepo()
	svc := newMeetingServiceWithRepo(repo)
	when := time.Date(2026, 5, 12, 9, 0, 0, 0, time.UTC)
	repo.addMeeting(&model.Meeting{
		MeetingID: "m-1", UserID: "owner-1", Title: "ROSA", Status: model.StatusDone, Date: when,
		Insights: `[{"type":"risk","text":"a"},{"type":"opportunity","text":"b"}]`,
	})
	repo.addMember("acc-1", "owner-1", model.RoleOwner)
	if _, err := svc.ShareMeetingToAccount(context.Background(), "owner-1", "o@x.com", "m-1", "acc-1"); err != nil {
		t.Fatalf("first share: %v", err)
	}
	if len(repo.accountInsights) != 2 {
		t.Fatalf("expected 2 after first share, got %d", len(repo.accountInsights))
	}
	// Re-extraction yields FEWER insights → stale index must not linger.
	repo.addMeeting(&model.Meeting{
		MeetingID: "m-1", UserID: "owner-1", Title: "ROSA", Status: model.StatusDone, Date: when,
		Insights: `[{"type":"risk","text":"a-updated"}]`,
	})
	if _, err := svc.ShareMeetingToAccount(context.Background(), "owner-1", "o@x.com", "m-1", "acc-1"); err != nil {
		t.Fatalf("second share: %v", err)
	}
	if len(repo.accountInsights) != 1 {
		t.Fatalf("expected 1 after re-share with fewer insights (stale removed), got %d", len(repo.accountInsights))
	}
	if repo.accountInsights[0].Text != "a-updated" {
		t.Errorf("expected updated insight text, got %q", repo.accountInsights[0].Text)
	}
}
