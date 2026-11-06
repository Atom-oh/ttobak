package service

import (
	"context"
	"errors"
	"fmt"
	"path"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// Account-team members manage published meetings like the creator, except
// delete (ADR-016 2026-11-06 amendment). These doubles mirror the actor-aware
// repository writes.

func (m *mockMeetingRepo) CreateShareBy(ctx context.Context, meetingID, ownerID, ownerEmail, sharedToID, email, permission, sharedByID, sharedByEmail string) (*model.Share, error) {
	share, err := m.CreateShare(ctx, meetingID, ownerID, ownerEmail, sharedToID, email, permission, "")
	if err != nil {
		return nil, err
	}
	share.OwnerEmail, share.SharedByUserID, share.SharedByEmail = ownerEmail, sharedByID, sharedByEmail
	return share, nil
}

// actorAccountGuard mirrors the repository fences: the actor's direct
// membership, and a non-creator stays within the current account.
func (m *mockMeetingRepo) actorAccountGuard(ownerID, actorID, meetingID, accountID string) error {
	if m.members[accountID+"|"+actorID] == nil {
		return repository.ErrSummaryAccess
	}
	if current := m.meetings[meetingKey(ownerID, meetingID)]; actorID != ownerID && (current == nil || current.AccountID != accountID || !current.SharedToAccount) {
		return repository.ErrConditionFailed
	}
	return nil
}

func (m *mockMeetingRepo) LinkMeetingToAccountAs(ctx context.Context, ownerID, actorID, meetingID, accountID string) error {
	if err := m.actorAccountGuard(ownerID, actorID, meetingID, accountID); err != nil {
		return err
	}
	return m.UpdateMeetingFields(ctx, ownerID, meetingID, map[string]interface{}{"accountId": accountID, "sharedToAccount": false})
}

func (m *mockMeetingRepo) PublishMeetingToAccountAs(ctx context.Context, ownerID, actorID, meetingID, accountID string) error {
	if err := m.actorAccountGuard(ownerID, actorID, meetingID, accountID); err != nil {
		return err
	}
	return m.PublishMeetingToAccount(ctx, ownerID, meetingID, accountID)
}

func (m *mockMeetingRepo) LinkPredecessorsAs(ctx context.Context, ownerID, actorID, meetingID, accountID string, linkedIDs []string) error {
	current := m.meetings[meetingKey(ownerID, meetingID)]
	if m.members[accountID+"|"+actorID] == nil {
		return repository.ErrSummaryAccess
	}
	if current == nil || current.AccountID != accountID || !current.SharedToAccount {
		return repository.ErrConditionFailed
	}
	return m.UpdateMeetingFields(ctx, ownerID, meetingID, map[string]interface{}{"linkedMeetingIds": linkedIDs})
}

func (m *mockMeetingRepo) GetUserByID(_ context.Context, userID string) (*model.User, error) {
	for _, user := range m.users {
		if user.UserID == userID {
			copy := *user
			return &copy, nil
		}
	}
	return nil, nil
}

// managementRepo: creator "owner-1" published "m-1" to "acc"; "member-1" is a
// direct member; "reader-1" holds only a direct read email share.
func managementRepo() *mockMeetingRepo {
	repo := newMockMeetingRepo()
	now := time.Now()
	repo.addMeeting(&model.Meeting{
		MeetingID: "m-1", UserID: "owner-1", Title: "Meeting", AccountID: "acc", SharedToAccount: true,
		Status: model.StatusDone, Date: now, CreatedAt: now, UpdatedAt: now,
	})
	repo.addMember("acc", "owner-1", model.RoleSA)
	repo.addMember("acc", "member-1", model.RoleSA)
	repo.users["owner@example.com"] = &model.User{UserID: "owner-1", Email: "owner@example.com"}
	repo.users["member@example.com"] = &model.User{UserID: "member-1", Email: "member@example.com"}
	repo.users["guest@example.com"] = &model.User{UserID: "guest-1", Email: "guest@example.com"}
	repo.shares[shareKey("reader-1", "m-1")] = &model.Share{MeetingID: "m-1", OwnerID: "owner-1", SharedToID: "reader-1", Permission: model.PermissionEdit}
	return repo
}

func TestResolveMeetingManager(t *testing.T) {
	repo := managementRepo()
	for _, tc := range []struct {
		user string
		want error
	}{
		{"owner-1", nil}, {"member-1", nil},
		{"reader-1", ErrForbidden}, // a direct edit share is not management
		{"stranger", ErrNotFound},
	} {
		meeting, err := resolveMeetingManager(context.Background(), repo, tc.user, "m-1")
		if !errors.Is(err, tc.want) || (tc.want == nil && (meeting == nil || meeting.UserID != "owner-1")) {
			t.Fatalf("%s: got %v/%v, want %v with the creator's meeting", tc.user, meeting, err, tc.want)
		}
	}
	repo.meetingsByID["m-1"].SharedToAccount = false
	if _, err := resolveMeetingManager(context.Background(), repo, "member-1", "m-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unpublished meeting stays private: %v", err)
	}
}

func TestResolveMeetingManagerInheritedMember(t *testing.T) {
	t.Setenv(accountDescendantReadsEnv, "1")
	r := &inheritedSpeakerRepo{mockMeetingRepo: newMockMeetingRepo(), hierarchy: newDescendantAccessRepo()}
	meeting := *r.hierarchy.meeting
	r.addMeeting(&meeting)
	if got, err := resolveMeetingManager(context.Background(), r, "reader", "meeting"); err != nil || got.UserID != "owner" {
		t.Fatalf("inherited member manages: %v/%v", got, err)
	}
	t.Setenv(accountDescendantReadsEnv, "")
	if _, err := resolveMeetingManager(context.Background(), r, "reader", "meeting"); err == nil {
		t.Fatal("inheritance must follow the descendant-reads flag")
	}
}

func TestMemberShareByEmailKeepsCreatorOwnershipAndRecordsActor(t *testing.T) {
	repo := managementRepo()
	svc := newMeetingServiceWithRepo(repo)
	share, pending, err := svc.ShareMeetingByEmail(context.Background(), "member-1", "member@example.com", "m-1", "guest@example.com", model.PermissionRead)
	if err != nil || pending || share == nil {
		t.Fatalf("member share: %v/%v/%v", share, pending, err)
	}
	stored := repo.shares[shareKey("guest-1", "m-1")]
	if stored.OwnerID != "owner-1" || stored.OwnerEmail != "owner@example.com" || stored.SharedByUserID != "member-1" ||
		stored.SharedByEmail != "member@example.com" || stored.Origin != "" {
		t.Fatalf("share row must stay the creator's direct grant with the actor recorded: %+v", stored)
	}
	for _, target := range []string{"owner@example.com", "member@example.com"} {
		if _, _, err := svc.ShareMeetingByEmail(context.Background(), "member-1", "member@example.com", "m-1", target, model.PermissionRead); !errors.Is(err, ErrSelfShare) {
			t.Fatalf("%s: want ErrSelfShare, got %v", target, err)
		}
	}
	if _, _, err := svc.ShareMeetingByEmail(context.Background(), "reader-1", "r@example.com", "m-1", "guest@example.com", model.PermissionRead); !errors.Is(err, ErrForbidden) {
		t.Fatalf("direct share holder cannot reshare: %v", err)
	}
	// The creator's own share records no actor.
	if _, _, err := svc.ShareMeetingByEmail(context.Background(), "owner-1", "owner@example.com", "m-1", "guest@example.com", model.PermissionEdit); err != nil {
		t.Fatal(err)
	}
	if got := repo.shares[shareKey("guest-1", "m-1")]; got.SharedByUserID != "" {
		t.Fatalf("creator share recorded an actor: %+v", got)
	}
}

func TestMemberDetailManagesSharesAndFlags(t *testing.T) {
	repo := managementRepo()
	repo.shares[shareKey("guest-1", "m-1")] = &model.Share{MeetingID: "m-1", OwnerID: "owner-1", SharedToID: "guest-1",
		Email: "guest@example.com", Permission: model.PermissionRead, SharedByUserID: "member-1", SharedByEmail: "member@example.com"}
	repo.meetingsByID["m-1"].ProjectIDs = []string{"p-1"}
	repo.meetingsByID["m-1"].NotionPageID = "notion-page"
	repo.meetings[meetingKey("owner-1", "m-1")] = repo.meetingsByID["m-1"]
	svc := newMeetingServiceWithRepo(repo)
	detail, err := svc.GetMeetingDetail(context.Background(), "member-1", "m-1")
	if err != nil || !detail.CanManage || len(detail.Shares) != 2 || len(detail.ProjectIDs) != 1 || detail.NotionPageID != "" {
		t.Fatalf("member detail: %+v/%v", detail, err)
	}
	sharedBy := ""
	for _, share := range detail.Shares {
		if share.UserID == "guest-1" {
			sharedBy = share.SharedByEmail
		}
	}
	if sharedBy != "member@example.com" {
		t.Fatalf("sharer not shown: %+v", detail.Shares)
	}
	reader, err := svc.GetMeetingDetail(context.Background(), "reader-1", "m-1")
	if err != nil || reader.CanManage || len(reader.Shares) != 0 || len(reader.ProjectIDs) != 0 {
		t.Fatalf("direct share holder must not manage: %+v/%v", reader, err)
	}
}

func TestMemberRevokesSharesButNotAsDirectShareHolder(t *testing.T) {
	repo := managementRepo()
	repo.shares[shareKey("guest-1", "m-1")] = &model.Share{MeetingID: "m-1", OwnerID: "owner-1", SharedToID: "guest-1", Permission: model.PermissionRead}
	svc := newMeetingServiceWithRepo(repo)
	if err := svc.RevokeShare(context.Background(), "reader-1", "m-1", "guest-1"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("direct share holder revoke: %v", err)
	}
	if err := svc.RevokeShare(context.Background(), "member-1", "m-1", "guest-1"); err != nil || repo.shares[shareKey("guest-1", "m-1")] != nil {
		t.Fatalf("member revoke: %v", err)
	}
	repo.pendingShares = append(repo.pendingShares, &model.PendingShare{Email: "new@example.com", Kind: model.PendingShareKindMeeting,
		MeetingID: "m-1", SK: model.PrefixPendingMeeting + "m-1"})
	if err := svc.RevokePendingShare(context.Background(), "member-1", "m-1", "new@example.com"); err != nil {
		t.Fatalf("member pending revoke: %v", err)
	}
}

func TestMemberAccountChangesStayWithinTheManagedAccount(t *testing.T) {
	repo := managementRepo()
	repo.addMember("other", "member-1", model.RoleSA)
	svc := newMeetingServiceWithRepo(repo)
	// Moving the creator's meeting to another account stays creator-only,
	// even when the member belongs to that account.
	if _, err := svc.ShareMeetingToAccount(context.Background(), "member-1", "member@example.com", "m-1", "other"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("member moved publication to another account: %v", err)
	}
	if err := svc.LinkMeetingToAccount(context.Background(), "member-1", "m-1", "other"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("member reclassified into another account: %v", err)
	}
	if len(repo.meetingRefs["other"]) != 0 || repo.meetings[meetingKey("owner-1", "m-1")].AccountID != "acc" {
		t.Fatal("a refused move wrote state")
	}
	// Republishing within the managed account works and names the creator.
	result, err := svc.ShareMeetingToAccount(context.Background(), "member-1", "member@example.com", "m-1", "acc")
	if err != nil {
		t.Fatalf("member republish: %v", err)
	}
	if refs := repo.meetingRefs["acc"]; len(refs) != 1 || refs[0].OwnerUserID != "owner-1" || result.SharedWith != 1 {
		t.Fatalf("team ref must name the creator: %+v %+v", refs, result)
	}
	if share := repo.shares[shareKey("member-1", "m-1")]; share == nil || share.OwnerID != "owner-1" {
		t.Fatalf("account-origin share must name the creator: %+v", share)
	}
	// Unpublishing within the account is allowed and removes management.
	if err := svc.LinkMeetingToAccount(context.Background(), "member-1", "m-1", "acc"); err != nil {
		t.Fatalf("member unpublish: %v", err)
	}
	if repo.meetings[meetingKey("owner-1", "m-1")].SharedToAccount {
		t.Fatal("classification must clear publication")
	}
	if err := svc.LinkMeetingToAccount(context.Background(), "member-1", "m-1", "acc"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after unpublishing the member no longer manages the meeting: %v", err)
	}
	// The creator may still move the meeting anywhere they belong.
	repo.addMember("other", "owner-1", model.RoleSA)
	if _, err := svc.ShareMeetingToAccount(context.Background(), "owner-1", "owner@example.com", "m-1", "other"); err != nil {
		t.Fatalf("creator move: %v", err)
	}
}

func TestMemberLinksOnlyCreatorPredecessorsTheyCanRead(t *testing.T) {
	repo := managementRepo()
	now := time.Now()
	repo.addMeeting(&model.Meeting{MeetingID: "p-team", UserID: "owner-1", AccountID: "acc", SharedToAccount: true, Date: now})
	repo.addMeeting(&model.Meeting{MeetingID: "p-private", UserID: "owner-1", Date: now})
	repo.addMeeting(&model.Meeting{MeetingID: "p-other", UserID: "owner-1", AccountID: "other", SharedToAccount: true, Date: now})
	repo.addMeeting(&model.Meeting{MeetingID: "p-member", UserID: "member-1", Date: now})
	// The member can read p-private only through a direct share; it must
	// not enter the team-visible summary context.
	repo.shares[shareKey("member-1", "p-private")] = &model.Share{MeetingID: "p-private", OwnerID: "owner-1", SharedToID: "member-1", Permission: model.PermissionEdit}
	svc := newMeetingServiceWithRepo(repo)
	for _, id := range []string{"p-private", "p-other"} {
		if err := svc.LinkPredecessors(context.Background(), "member-1", "m-1", []string{id}); !errors.Is(err, ErrForbidden) {
			t.Fatalf("%s: want ErrForbidden, got %v", id, err)
		}
	}
	if err := svc.LinkPredecessors(context.Background(), "member-1", "m-1", []string{"p-member"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the member's own meeting is not the creator's: %v", err)
	}
	if err := svc.LinkPredecessors(context.Background(), "member-1", "m-1", []string{"m-1"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("self link: %v", err)
	}
	if err := svc.LinkPredecessors(context.Background(), "member-1", "m-1", []string{"p-team"}); err != nil {
		t.Fatalf("member link: %v", err)
	}
	if got := repo.meetings[meetingKey("owner-1", "m-1")].LinkedMeetingIDs; len(got) != 1 || got[0] != "p-team" {
		t.Fatalf("links: %v", got)
	}
	if err := svc.LinkPredecessors(context.Background(), "owner-1", "m-1", []string{"p-private"}); err != nil {
		t.Fatalf("creator links private predecessor: %v", err)
	}
	if err := svc.LinkPredecessors(context.Background(), "reader-1", "m-1", []string{"p-team"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("direct share holder link: %v", err)
	}
	// The parent is unpublished after the manager check: the fenced write fails.
	unpublished := *repo.meetingsByID["m-1"]
	unpublished.SharedToAccount = false
	repo.meetings[meetingKey("owner-1", "m-1")] = &unpublished
	if err := svc.LinkPredecessors(context.Background(), "member-1", "m-1", []string{"p-team"}); !errors.Is(err, repository.ErrConditionFailed) {
		t.Fatalf("link after concurrent unpublish: %v", err)
	}
}

func TestDeleteStaysCreatorOnlyForManagers(t *testing.T) {
	repo := managementRepo()
	if err := newMeetingServiceWithRepo(repo).DeleteMeeting(context.Background(), "member-1", "m-1"); err == nil {
		t.Fatal("a managing member deleted the creator's meeting")
	}
	if repo.meetings[meetingKey("owner-1", "m-1")] == nil {
		t.Fatal("meeting removed")
	}
}

func TestSharingStatusAdvertisesApprovalPathOnlyToTheCreator(t *testing.T) {
	t.Setenv("SUMMARY_PRIVACY_WRITER_ENABLED", "1")
	t.Setenv("SUMMARY_OWNER_ACCOUNT_SHARE_ENABLED", "1")
	unsafe := &model.Meeting{Status: model.StatusDone, Content: "private notes", AccountID: "acc"}
	if !summarySharingStatus(unsafe, "owner", true).CanShareToAccount {
		t.Fatal("the creator keeps the approval path")
	}
	if summarySharingStatus(unsafe, model.PermissionEdit, true).CanShareToAccount {
		t.Fatal("a member was offered the owner approval path for an unpublished meeting")
	}
	published := *unsafe
	published.SharedToAccount = true
	if !summarySharingStatus(&published, model.PermissionEdit, true).CanShareToAccount {
		t.Fatal("a member may refresh the current publication")
	}
	if summarySharingStatus(&published, model.PermissionEdit, false).CanShareToAccount {
		t.Fatal("a non-manager was offered sharing")
	}
}

func TestMemberWritesFailAfterConcurrentUnpublish(t *testing.T) {
	repo := managementRepo()
	// The manager check saw the publication; the canonical row was then
	// unpublished before the write.
	unpublished := *repo.meetingsByID["m-1"]
	unpublished.SharedToAccount = false
	repo.meetings[meetingKey("owner-1", "m-1")] = &unpublished
	svc := newMeetingServiceWithRepo(repo)
	if _, err := svc.ShareMeetingToAccount(context.Background(), "member-1", "member@example.com", "m-1", "acc"); !errors.Is(err, repository.ErrConditionFailed) {
		t.Fatalf("member republish after unpublish: %v", err)
	}
	if err := svc.LinkMeetingToAccount(context.Background(), "member-1", "m-1", "acc"); !errors.Is(err, repository.ErrConditionFailed) {
		t.Fatalf("member classification after unpublish: %v", err)
	}
}

func TestMemberShareRejectsUnknownPermission(t *testing.T) {
	svc := newMeetingServiceWithRepo(managementRepo())
	if _, _, err := svc.ShareMeetingByEmail(context.Background(), "member-1", "member@example.com", "m-1", "guest@example.com", "owner"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("a member granted more than edit: %v", err)
	}
}

// removeMemberDuringRead revokes a membership between GetMeetingDetail's
// first and final access reads.
type removeMemberDuringRead struct {
	*mockMeetingRepo
	key string
}

func (r removeMemberDuringRead) ListAttachments(ctx context.Context, meetingID string) ([]model.Attachment, error) {
	delete(r.members, r.key)
	return r.mockMeetingRepo.ListAttachments(ctx, meetingID)
}

func TestDetailManagerFieldsFollowTheFinalAccessRead(t *testing.T) {
	base := managementRepo()
	// The member keeps an independent direct read share after removal.
	base.shares[shareKey("member-1", "m-1")] = &model.Share{MeetingID: "m-1", OwnerID: "owner-1", SharedToID: "member-1", Permission: model.PermissionRead}
	base.shares[shareKey("guest-1", "m-1")] = &model.Share{MeetingID: "m-1", OwnerID: "owner-1", SharedToID: "guest-1", Permission: model.PermissionRead}
	repo := removeMemberDuringRead{mockMeetingRepo: base, key: "acc|member-1"}
	detail, err := newMeetingServiceWithRepo(repo).GetMeetingDetail(context.Background(), "member-1", "m-1")
	if err != nil {
		t.Fatal(err)
	}
	if detail.CanManage || len(detail.Shares) != 0 || detail.Permission != model.PermissionRead || detail.SummarySharing.CanShareToAccount {
		t.Fatalf("removed member kept manager fields: canManage=%v shares=%d permission=%s", detail.CanManage, len(detail.Shares), detail.Permission)
	}
}

func TestTeamSharingOfferedOnlyToCreatorOrDirectMembers(t *testing.T) {
	repo := managementRepo()
	svc := newMeetingServiceWithRepo(repo)
	for _, user := range []string{"owner-1", "member-1"} {
		detail, err := svc.GetMeetingDetail(context.Background(), user, "m-1")
		if err != nil || !detail.SummarySharing.CanShareToAccount {
			t.Fatalf("%s: canShareToAccount=%v err=%v", user, detail != nil && detail.SummarySharing.CanShareToAccount, err)
		}
		if user != "owner-1" && detail.CanRecoverRecording {
			t.Fatal("recovery stays creator-only")
		}
	}

	t.Setenv(accountDescendantReadsEnv, "1")
	r := &inheritedSpeakerRepo{mockMeetingRepo: newMockMeetingRepo(), hierarchy: newDescendantAccessRepo()}
	meeting := *r.hierarchy.meeting
	meeting.Status = model.StatusDone
	r.addMeeting(&meeting)
	detail, err := newMeetingServiceWithRepo(r).GetMeetingDetail(context.Background(), "reader", "meeting")
	if err != nil || !detail.CanManage || detail.SummarySharing.CanShareToAccount {
		t.Fatalf("inherited manager: canManage=%v canShareToAccount=%v err=%v", detail != nil && detail.CanManage,
			detail != nil && detail.SummarySharing.CanShareToAccount, err)
	}
}

// The flag is offered to managers; RecoverMeeting still enforces the
// 10-minute quiet checkpoint for non-creators.
func TestRecoveryFlagOfferedToManagers(t *testing.T) {
	repo := managementRepo()
	repo.meetingsByID["m-1"].Status = model.StatusRecording
	repo.meetingsByID["m-1"].UpdatedAt = time.Now()
	repo.meetings[meetingKey("owner-1", "m-1")] = repo.meetingsByID["m-1"]
	svc := newMeetingServiceWithRepo(repo)
	member, err := svc.GetMeetingDetail(context.Background(), "member-1", "m-1")
	if err != nil || !member.CanRecoverRecording {
		t.Fatalf("member recovery flag: %v %v", member != nil && member.CanRecoverRecording, err)
	}
	reader, err := svc.GetMeetingDetail(context.Background(), "reader-1", "m-1")
	if err != nil || reader.CanRecoverRecording {
		t.Fatalf("direct share holder recovery flag: %v %v", reader != nil && reader.CanRecoverRecording, err)
	}
}

func TestMemberMayRecoverOnlyStaleRecordings(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		actor      string
		checkpoint time.Time
		want       bool
	}{
		{"owner", now, true},
		{"owner", time.Time{}, true},
		{"member", now.Add(-9 * time.Minute), false},
		{"member", now.Add(-10 * time.Minute), true},
		{"member", time.Time{}, false},
	} {
		if got := memberMayRecover(tc.actor, "owner", tc.checkpoint, now); got != tc.want {
			t.Fatalf("%s at %v: got %v", tc.actor, tc.checkpoint, got)
		}
	}
}

func TestRediarizeUsesCreatorAudioPrefix(t *testing.T) {
	meeting := &model.Meeting{UserID: "owner", MeetingID: "m", Status: model.StatusDone, AudioKey: "audio/owner/m/a.webm"}
	if key, err := validateRediarizeEligibility(meeting, meeting.UserID, 3); err != nil || key != "audio/owner/m/a.webm" {
		t.Fatalf("creator prefix: %q %v", key, err)
	}
	if _, err := validateRediarizeEligibility(meeting, "member", 3); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a non-owner prefix never matches: %v", err)
	}
}

// managedProjectRepo gives the project double the share/membership reads a
// manager check needs plus the account-fenced link write.
type managedProjectRepo struct{ *mockProjectRepo }

func (r managedProjectRepo) GetShare(context.Context, string, string) (*model.Share, error) {
	return nil, nil
}

func (r managedProjectRepo) GetMeetingByID(_ context.Context, meetingID string) (*model.Meeting, error) {
	for _, meeting := range r.meetings {
		if meeting.MeetingID == meetingID {
			copy := *meeting
			return &copy, nil
		}
	}
	return nil, nil
}

func (r managedProjectRepo) MeetingProjectLinkInAccount(ctx context.Context, ownerID, meetingID, projectID, accountID string, ref *model.ProjectMeetingRef) error {
	meeting := r.meetings[projectMeetingKey(ownerID, meetingID)]
	project := r.projects[projectID]
	if meeting == nil || project == nil || meeting.AccountID != accountID || !meeting.SharedToAccount || !contains(project.AccountIDs, accountID) {
		return repository.ErrConditionFailed
	}
	return r.MeetingProjectLinkTransactional(ctx, ownerID, meetingID, projectID, ref)
}

func TestMemberProjectLinkRequiresTheMeetingAccount(t *testing.T) {
	base := newMockProjectRepo()
	base.meetings[projectMeetingKey("owner-1", "m-1")] = &model.Meeting{MeetingID: "m-1", UserID: "owner-1", AccountID: "acc", SharedToAccount: true, Date: time.Now()}
	addProjectAccountMember(base, "acc", "member-1")
	addProjectAccountMember(base, "elsewhere", "member-1")
	inAccount := seedProject(base, "p-acc", "project-owner")
	inAccount.AccountIDs = []string{"acc"}
	other := seedProject(base, "p-other", "project-owner")
	other.AccountIDs = []string{"elsewhere"}
	svc := newProjectServiceWithRepo(managedProjectRepo{base})
	if err := svc.LinkMeeting(context.Background(), "member-1", "p-other", "m-1"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("member linked the meeting to a project outside its account: %v", err)
	}
	if err := svc.LinkMeeting(context.Background(), "member-1", "p-acc", "m-1"); err != nil {
		t.Fatalf("member link within the account: %v", err)
	}
	refs := base.meetingRefs["p-acc"]
	if len(refs) != 1 || refs[0].OwnerUserID != "owner-1" || !contains(base.meetings[projectMeetingKey("owner-1", "m-1")].ProjectIDs, "p-acc") {
		t.Fatalf("link must stay the creator's: %+v", refs)
	}
	if err := svc.UnlinkMeeting(context.Background(), "member-1", "p-acc", "m-1"); err != nil {
		t.Fatalf("member unlink: %v", err)
	}
	if err := svc.LinkMeeting(context.Background(), "stranger", "p-acc", "m-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stranger link: %v", err)
	}
}

func TestMemberAudioPresignNeverOverwritesALiveRecording(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	recording := &model.Meeting{UserID: "owner", MeetingID: "m", Status: model.StatusRecording}
	done := &model.Meeting{UserID: "owner", MeetingID: "m", Status: model.StatusDone}
	for _, name := range []string{"recording_progress.webm", "recording_progress.m4a"} {
		if err := memberAudioWriteAllowed("member", done, name, time.Time{}, now); !errors.Is(err, ErrForbidden) {
			t.Fatalf("member checkpoint %s presign: %v", name, err)
		}
	}
	for _, checkpoint := range []time.Time{{}, now.Add(-9 * time.Minute)} {
		if err := memberAudioWriteAllowed("member", recording, "extra.webm", checkpoint, now); !errors.Is(err, ErrRecordingStillActive) {
			t.Fatalf("member audio during an active recording (%v): %v", checkpoint, err)
		}
	}
	transcribing := *recording
	transcribing.Status = model.StatusTranscribing
	if err := memberAudioWriteAllowed("member", &transcribing, "extra.webm", now.Add(-time.Minute), now); !errors.Is(err, ErrRecordingStillActive) {
		t.Fatalf("member audio while transcribing right after a recording: %v", err)
	}
	if err := memberAudioWriteAllowed("member", &transcribing, "extra.webm", time.Time{}, now); err != nil {
		t.Fatalf("member audio while an upload batch transcribes: %v", err)
	}
	if err := memberAudioWriteAllowed("member", recording, "extra.webm", now.Add(-10*time.Minute), now); err != nil {
		t.Fatalf("stale recording allows member audio: %v", err)
	}
	if err := memberAudioWriteAllowed("member", done, "extra.webm", time.Time{}, now); err != nil {
		t.Fatalf("member audio on a done meeting: %v", err)
	}
	if err := memberAudioWriteAllowed("owner", recording, "recording_progress.webm", time.Time{}, now); err != nil {
		t.Fatalf("creator checkpoint presign changed: %v", err)
	}
}

func TestMemberProjectUnlinkRequiresTheMeetingAccount(t *testing.T) {
	base := newMockProjectRepo()
	base.meetings[projectMeetingKey("owner-1", "m-1")] = &model.Meeting{MeetingID: "m-1", UserID: "owner-1", AccountID: "acc", SharedToAccount: true, Date: time.Now()}
	addProjectAccountMember(base, "acc", "member-1")
	addProjectAccountMember(base, "elsewhere", "member-1")
	outside := seedProject(base, "p-other", "project-owner")
	outside.AccountIDs = []string{"elsewhere"}
	base.meetingRefs["p-other"] = []model.ProjectMeetingRef{{PK: model.PrefixProject + "p-other", SK: "MEETINGREF#m-1",
		ProjectID: "p-other", MeetingID: "m-1", OwnerUserID: "owner-1"}}
	base.meetings[projectMeetingKey("owner-1", "m-1")].ProjectIDs = []string{"p-other"}
	svc := newProjectServiceWithRepo(managedProjectRepo{base})
	if err := svc.UnlinkMeeting(context.Background(), "member-1", "p-other", "m-1"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("member unlinked the meeting from a project outside its account: %v", err)
	}
	if len(base.meetingRefs["p-other"]) != 1 {
		t.Fatal("a refused unlink removed the ref")
	}
	inside := seedProject(base, "p-acc", "project-owner")
	inside.AccountIDs = []string{"acc"}
	if err := svc.LinkMeeting(context.Background(), "member-1", "p-acc", "m-1"); err != nil {
		t.Fatal(err)
	}
	delete(base.accountMembers, projectAccountMemberKey("acc", "member-1"))
	if err := svc.UnlinkMeeting(context.Background(), "member-1", "p-acc", "m-1"); err == nil {
		t.Fatal("a former member unlinked")
	}
}

// checkMemberAudioWrite is the guard both presign and CompleteUpload call;
// completion passes the key's base name.
func TestMemberAudioCompletionUsesThePresignGuard(t *testing.T) {
	svc := &UploadService{} // no S3 client: an unread checkpoint counts as active
	recording := &model.Meeting{UserID: "owner", MeetingID: "m", Status: model.StatusRecording}
	done := &model.Meeting{UserID: "owner", MeetingID: "m", Status: model.StatusDone}
	checkpointKey := path.Base("audio/owner/m/recording_progress.webm")
	if err := svc.checkMemberAudioWrite(context.Background(), "member", done, checkpointKey); !errors.Is(err, ErrForbidden) {
		t.Fatalf("member completion of a checkpoint key: %v", err)
	}
	if err := svc.checkMemberAudioWrite(context.Background(), "member", recording, path.Base("audio/owner/m/extra_1.webm")); !errors.Is(err, ErrRecordingStillActive) {
		t.Fatalf("member completion during an active recording: %v", err)
	}
	if err := svc.checkMemberAudioWrite(context.Background(), "member", done, path.Base("audio/owner/m/extra_1.webm")); err != nil {
		t.Fatalf("member completion on a done meeting: %v", err)
	}
	if err := svc.checkMemberAudioWrite(context.Background(), "owner", recording, checkpointKey); err != nil {
		t.Fatalf("creator completion changed: %v", err)
	}
}

func TestMemberPresignsAreShortLivedAndMarkSources(t *testing.T) {
	if got := presignExpiry("owner", "owner"); got != time.Hour {
		t.Fatalf("creator presign expiry changed: %v", got)
	}
	if got := presignExpiry("member", "owner"); got != 5*time.Minute {
		t.Fatalf("member presign expiry: %v", got)
	}
	for _, tc := range []struct {
		category, actor string
		want            bool
	}{
		{"audio", "member", true}, {"file", "member", true}, {"image", "member", false},
		{"audio", "owner", false}, {"file", "owner", false},
	} {
		if got := presignMarksSourceEdit(tc.category, tc.actor, "owner"); got != tc.want {
			t.Fatalf("%s by %s: marks=%v want %v", tc.category, tc.actor, got, tc.want)
		}
	}
}

// A member's multipart batch moves the meeting to transcribing on its first
// part (PreAllocateAudioKeys); later parts must still pass the guard, while a
// live recording (fresh checkpoint) stays refused.
func TestMemberMultipartBatchDoesNotDeadlock(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	meeting := &model.Meeting{UserID: "owner", MeetingID: "m", Status: model.StatusDone}
	for part := 0; part < 3; part++ {
		name := fmt.Sprintf("part_%03d_take.webm", part)
		// presign then complete; no checkpoint exists for an uploaded batch
		for _, stage := range []string{"presign", "complete"} {
			if err := memberAudioWriteAllowed("member", meeting, name, time.Time{}, now); err != nil {
				t.Fatalf("part %d %s: %v", part, stage, err)
			}
		}
		meeting.Status = model.StatusTranscribing // first completion pre-allocates
	}
	recording := &model.Meeting{UserID: "owner", MeetingID: "m", Status: model.StatusRecording}
	if err := memberAudioWriteAllowed("member", recording, "part_000_take.webm", now.Add(-2*time.Minute), now); !errors.Is(err, ErrRecordingStillActive) {
		t.Fatalf("creator recording with a fresh checkpoint: %v", err)
	}
	if err := memberAudioWriteAllowed("member", meeting, "part_000_take.webm", now.Add(-2*time.Minute), now); !errors.Is(err, ErrRecordingStillActive) {
		t.Fatalf("transcribing with a fresh checkpoint: %v", err)
	}
}
