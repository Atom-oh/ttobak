package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ci "github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"
	ct "github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider/types"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

type onboardingRepo struct {
	*mockProjectRepo
	pending          *model.PendingShare
	materializeCalls int
	materializeErr   error
	revokeErr        error
}

func (r *onboardingRepo) PutRegisteredProjectMember(_ context.Context, owner, project, id, email string) error {
	r.projectMembers[projectMemberKey(project, id)] = &model.ProjectMember{ProjectID: project, UserID: id, Email: email}
	return nil
}
func (r *onboardingRepo) PutPendingShare(_ context.Context, p *model.PendingShare) error {
	p.SK = model.PrefixPendingProject + p.ProjectID
	p.CreatedAt = time.Now().UTC()
	p.TTL = time.Now().Add(time.Hour).Unix()
	cp := *p
	r.pending = &cp
	return nil
}
func (r *onboardingRepo) GetPendingShare(context.Context, string, string) (*model.PendingShare, error) {
	return r.pending, nil
}
func (r *onboardingRepo) ListPendingProjectShares(context.Context, string, string) ([]model.PendingShare, string, error) {
	if r.pending == nil {
		return nil, "", nil
	}
	return []model.PendingShare{*r.pending}, "next", nil
}
func (r *onboardingRepo) MaterializePendingProjectGrant(_ context.Context, p *model.PendingShare, id, email string) (bool, error) {
	r.materializeCalls++
	if r.materializeErr != nil {
		return false, r.materializeErr
	}
	r.projectMembers[projectMemberKey(p.ProjectID, id)] = &model.ProjectMember{ProjectID: p.ProjectID, UserID: id, Email: email}
	r.pending = nil
	return true, nil
}
func (r *onboardingRepo) RevokePendingProjectShare(context.Context, *model.PendingShare, string) error {
	if r.revokeErr != nil {
		return r.revokeErr
	}
	r.pending = nil
	return nil
}

func onboardingService(t *testing.T, status ct.UserStatusType, verified bool) (*ProjectService, *onboardingRepo, *fakeCognitoAdminAPI) {
	t.Helper()
	t.Setenv("PROJECT_INVITATIONS_ENABLED", "true")
	r := &onboardingRepo{mockProjectRepo: newMockProjectRepo()}
	r.projects["project"] = &model.Project{ProjectID: "project", OwnerUserID: "owner"}
	c := &fakeCognitoAdminAPI{adminGetUserFn: func(_ context.Context, in *ci.AdminGetUserInput) (*ci.AdminGetUserOutput, error) {
		if aws.ToString(in.Username) != "invitee@example.com" {
			t.Errorf("non-normalized email: %s", aws.ToString(in.Username))
		}
		return &ci.AdminGetUserOutput{Enabled: true, UserStatus: status, UserAttributes: []ct.AttributeType{{Name: aws.String("sub"), Value: aws.String("current-sub")}, {Name: aws.String("email"), Value: aws.String("invitee@example.com")}, {Name: aws.String("email_verified"), Value: aws.String(map[bool]string{true: "true", false: "false"}[verified])}}}, nil
	}}
	s := newProjectServiceWithRepo(r)
	s.SetCognitoAdminAPI(c, "pool")
	return s, r, c
}
func TestProjectAddMemberQueuesInvitedIdentityWithoutSendingMail(t *testing.T) {
	s, r, c := onboardingService(t, ct.UserStatusTypeForceChangePassword, true)
	got, err := s.AddMember(context.Background(), "owner", "project", &model.AddProjectMemberRequest{AllowPending: true, Email: " Invitee@Example.com "})
	if err != nil || !got.Pending || got.UserID != "" {
		t.Fatalf("result=%+v err=%v", got, err)
	}
	if r.pending.InvitedCognitoSub != "current-sub" || r.pending.ProjectID != "project" || r.materializeCalls != 0 || len(c.createUserCalls) != 0 {
		t.Fatal("invitation must bind current identity without sending mail or granting early")
	}
}
func TestProjectAddMemberUnknownRequiresAdminInvitation(t *testing.T) {
	s, r, c := onboardingService(t, ct.UserStatusTypeConfirmed, true)
	c.adminGetUserFn = func(context.Context, *ci.AdminGetUserInput) (*ci.AdminGetUserOutput, error) {
		return nil, &ct.UserNotFoundException{}
	}
	_, err := s.AddMember(context.Background(), "owner", "project", &model.AddProjectMemberRequest{AllowPending: true, Email: "invitee@example.com"})
	if !errors.Is(err, ErrInvitationRequired) || r.pending != nil || len(c.createUserCalls) != 0 {
		t.Fatalf("unexpected unknown-user behavior: %v", err)
	}
}
func TestProjectAddMemberRecreatedEmailNeverGrantsOldProfile(t *testing.T) {
	s, r, _ := onboardingService(t, ct.UserStatusTypeConfirmed, true)
	r.users["invitee@example.com"] = &model.User{UserID: "deleted-sub"}
	got, err := s.AddMember(context.Background(), "owner", "project", &model.AddProjectMemberRequest{AllowPending: true, Email: "invitee@example.com"})
	if err != nil || !got.Pending || r.pending.InvitedCognitoSub != "current-sub" || r.materializeCalls != 0 {
		t.Fatalf("recreated identity result=%+v err=%v", got, err)
	}
}
func TestProjectAddMemberRegisteredVerifiedUserIsIdempotent(t *testing.T) {
	s, r, _ := onboardingService(t, ct.UserStatusTypeConfirmed, true)
	r.users["invitee@example.com"] = &model.User{UserID: "current-sub"}
	for i := 0; i < 2; i++ {
		got, err := s.AddMember(context.Background(), "owner", "project", &model.AddProjectMemberRequest{AllowPending: true, Email: "invitee@example.com"})
		if err != nil || got.Pending || got.UserID != "current-sub" {
			t.Fatalf("result=%+v err=%v", got, err)
		}
	}
	if r.materializeCalls != 1 || r.pending != nil {
		t.Fatal("duplicate request granted or queued twice")
	}
}
func TestProjectAddMemberUnverifiedAndResetUsersRemainPending(t *testing.T) {
	for _, test := range []struct {
		status   ct.UserStatusType
		verified bool
	}{{ct.UserStatusTypeConfirmed, false}, {ct.UserStatusTypeResetRequired, true}} {
		s, r, _ := onboardingService(t, test.status, test.verified)
		r.users["invitee@example.com"] = &model.User{UserID: "current-sub"}
		got, err := s.AddMember(context.Background(), "owner", "project", &model.AddProjectMemberRequest{AllowPending: true, Email: "invitee@example.com"})
		if err != nil || !got.Pending || r.materializeCalls != 0 {
			t.Fatalf("result=%+v err=%v", got, err)
		}
	}
}
func TestProjectInvitationOwnerGuardPrecedesIdentityLookup(t *testing.T) {
	s, r, c := onboardingService(t, ct.UserStatusTypeConfirmed, true)
	c.adminGetUserFn = func(context.Context, *ci.AdminGetUserInput) (*ci.AdminGetUserOutput, error) {
		t.Fatal("unauthorized identity lookup")
		return nil, nil
	}
	_, err := s.AddMember(context.Background(), "outsider", "project", &model.AddProjectMemberRequest{AllowPending: true, Email: "invitee@example.com"})
	if !errors.Is(err, ErrForbidden) || r.pending != nil {
		t.Fatalf("err=%v", err)
	}
	if _, err := s.ListPendingMembers(context.Background(), "outsider", "project", ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("list err=%v", err)
	}
	if err := s.RevokePendingMember(context.Background(), "outsider", "project", "invitee@example.com"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("revoke err=%v", err)
	}
}
func TestProjectInvitationRejectsDisabledRecipientAndLookupFailure(t *testing.T) {
	s, r, c := onboardingService(t, ct.UserStatusTypeConfirmed, true)
	c.adminGetUserFn = func(context.Context, *ci.AdminGetUserInput) (*ci.AdminGetUserOutput, error) {
		return &ci.AdminGetUserOutput{Enabled: false}, nil
	}
	_, err := s.AddMember(context.Background(), "owner", "project", &model.AddProjectMemberRequest{AllowPending: true, Email: "invitee@example.com"})
	if !errors.Is(err, ErrUserDisabled) || r.pending != nil {
		t.Fatalf("err=%v", err)
	}
	c.adminGetUserFn = func(context.Context, *ci.AdminGetUserInput) (*ci.AdminGetUserOutput, error) {
		return nil, errors.New("unavailable")
	}
	_, err = s.AddMember(context.Background(), "owner", "project", &model.AddProjectMemberRequest{AllowPending: true, Email: "invitee@example.com"})
	if err == nil || r.pending != nil {
		t.Fatal("lookup failure must fail closed")
	}
}
func TestProjectRevokeDetectsConcurrentMaterialization(t *testing.T) {
	s, r, _ := onboardingService(t, ct.UserStatusTypeForceChangePassword, true)
	_, err := s.AddMember(context.Background(), "owner", "project", &model.AddProjectMemberRequest{AllowPending: true, Email: "invitee@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	r.projectMembers[projectMemberKey("project", "current-sub")] = &model.ProjectMember{UserID: "current-sub"}
	r.revokeErr = repository.ErrConditionFailed
	if err := s.RevokePendingMember(context.Background(), "owner", "project", "invitee@example.com"); !errors.Is(err, ErrPendingAlreadyClaimed) {
		t.Fatalf("err=%v", err)
	}
}

func TestProjectRevokeRequiresStoredRecipientBinding(test *testing.T) {
	service, store, _ := onboardingService(test, ct.UserStatusTypeConfirmed, true)
	store.projectMembers[projectMemberKey("project", "old-sub")] = &model.ProjectMember{UserID: "old-sub", Email: "invitee@example.com"}
	if err := service.RevokePendingMember(context.Background(), "owner", "project", "invitee@example.com"); !errors.Is(err, repository.ErrConditionFailed) {
		test.Fatalf("missing invitation reported revocation success: %v", err)
	}
}

func TestProjectInvitationMaterializationFailureIsNotSuccess(t *testing.T) {
	s, r, _ := onboardingService(t, ct.UserStatusTypeConfirmed, true)
	r.users["invitee@example.com"] = &model.User{UserID: "current-sub"}
	r.materializeErr = errors.New("temporary database failure")
	_, err := s.AddMember(context.Background(), "owner", "project", &model.AddProjectMemberRequest{AllowPending: true, Email: "invitee@example.com"})
	if err == nil || r.pending == nil {
		t.Fatal("surface failure while retaining retryable grant")
	}
}
func TestNormalizeInvitationEmailRejectsDisplayNamesAndOversize(t *testing.T) {
	for _, email := range []string{"Name <invitee@example.com>", "bad", "a@b\nCc:x@y", ""} {
		if _, err := normalizeInvitationEmail(email); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("accepted %q", email)
		}
	}
}

func TestProjectInvitationRequiresPendingCapabilityAndHonorsWriterFence(t *testing.T) {
	s, r, _ := onboardingService(t, ct.UserStatusTypeForceChangePassword, true)
	_, err := s.AddMember(context.Background(), "owner", "project", &model.AddProjectMemberRequest{Email: "invitee@example.com"})
	if !errors.Is(err, ErrClientUpgradeRequired) || r.pending != nil {
		t.Fatalf("legacy request queued: %v", err)
	}
	for _, value := range []string{"false", "", "TRUE", "invalid"} {
		t.Setenv("PROJECT_INVITATIONS_ENABLED", value)
		_, err = s.AddMember(context.Background(), "owner", "project", &model.AddProjectMemberRequest{AllowPending: true, Email: "invitee@example.com"})
		if !errors.Is(err, ErrInvitationsPaused) || r.pending != nil {
			t.Fatalf("writer fence failed for %q: %v", value, err)
		}
	}
}
func TestLegacyClientCanAddRegisteredMemberWithoutQueuing(t *testing.T) {
	s, r, _ := onboardingService(t, ct.UserStatusTypeConfirmed, true)
	r.users["invitee@example.com"] = &model.User{UserID: "current-sub"}
	got, err := s.AddMember(context.Background(), "owner", "project", &model.AddProjectMemberRequest{Email: "invitee@example.com"})
	if err != nil || got.UserID != "current-sub" || got.Pending || r.pending != nil || r.materializeCalls != 0 {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}
