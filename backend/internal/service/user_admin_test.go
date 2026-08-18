package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	cognitoidp "github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"
	cognitoidptypes "github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider/types"
)

// fakeCognitoAdminAPI is a configurable in-memory implementation of
// cognitoAdminAPI. Each field is an optional override; unset fields fall
// back to a zero-value success response so tests only wire what they need.
type fakeCognitoAdminAPI struct {
	listUsersFn              func(ctx context.Context, in *cognitoidp.ListUsersInput) (*cognitoidp.ListUsersOutput, error)
	listUsersInGroupFn       func(ctx context.Context, in *cognitoidp.ListUsersInGroupInput) (*cognitoidp.ListUsersInGroupOutput, error)
	adminGetUserFn           func(ctx context.Context, in *cognitoidp.AdminGetUserInput) (*cognitoidp.AdminGetUserOutput, error)
	adminCreateUserFn        func(ctx context.Context, in *cognitoidp.AdminCreateUserInput) (*cognitoidp.AdminCreateUserOutput, error)
	adminAddUserToGroupFn    func(ctx context.Context, in *cognitoidp.AdminAddUserToGroupInput) (*cognitoidp.AdminAddUserToGroupOutput, error)
	adminDeleteUserFn        func(ctx context.Context, in *cognitoidp.AdminDeleteUserInput) (*cognitoidp.AdminDeleteUserOutput, error)
	adminDisableUserFn       func(ctx context.Context, in *cognitoidp.AdminDisableUserInput) (*cognitoidp.AdminDisableUserOutput, error)
	adminEnableUserFn        func(ctx context.Context, in *cognitoidp.AdminEnableUserInput) (*cognitoidp.AdminEnableUserOutput, error)
	adminResetUserPasswordFn func(ctx context.Context, in *cognitoidp.AdminResetUserPasswordInput) (*cognitoidp.AdminResetUserPasswordOutput, error)
	adminUserGlobalSignOutFn func(ctx context.Context, in *cognitoidp.AdminUserGlobalSignOutInput) (*cognitoidp.AdminUserGlobalSignOutOutput, error)

	// Call-tracking for assertions.
	deletedUsers    []string
	disabledUsers   []string
	enabledUsers    []string
	signedOutUsers  []string
	createUserCalls []*cognitoidp.AdminCreateUserInput
	resetPwdUsers   []string
}

func (f *fakeCognitoAdminAPI) ListUsers(ctx context.Context, in *cognitoidp.ListUsersInput, _ ...func(*cognitoidp.Options)) (*cognitoidp.ListUsersOutput, error) {
	if f.listUsersFn != nil {
		return f.listUsersFn(ctx, in)
	}
	return &cognitoidp.ListUsersOutput{}, nil
}

func (f *fakeCognitoAdminAPI) ListUsersInGroup(ctx context.Context, in *cognitoidp.ListUsersInGroupInput, _ ...func(*cognitoidp.Options)) (*cognitoidp.ListUsersInGroupOutput, error) {
	if f.listUsersInGroupFn != nil {
		return f.listUsersInGroupFn(ctx, in)
	}
	return &cognitoidp.ListUsersInGroupOutput{}, nil
}

func (f *fakeCognitoAdminAPI) AdminGetUser(ctx context.Context, in *cognitoidp.AdminGetUserInput, _ ...func(*cognitoidp.Options)) (*cognitoidp.AdminGetUserOutput, error) {
	if f.adminGetUserFn != nil {
		return f.adminGetUserFn(ctx, in)
	}
	return &cognitoidp.AdminGetUserOutput{}, nil
}

func (f *fakeCognitoAdminAPI) AdminCreateUser(ctx context.Context, in *cognitoidp.AdminCreateUserInput, _ ...func(*cognitoidp.Options)) (*cognitoidp.AdminCreateUserOutput, error) {
	f.createUserCalls = append(f.createUserCalls, in)
	if f.adminCreateUserFn != nil {
		return f.adminCreateUserFn(ctx, in)
	}
	return &cognitoidp.AdminCreateUserOutput{}, nil
}

func (f *fakeCognitoAdminAPI) AdminAddUserToGroup(ctx context.Context, in *cognitoidp.AdminAddUserToGroupInput, _ ...func(*cognitoidp.Options)) (*cognitoidp.AdminAddUserToGroupOutput, error) {
	if f.adminAddUserToGroupFn != nil {
		return f.adminAddUserToGroupFn(ctx, in)
	}
	return &cognitoidp.AdminAddUserToGroupOutput{}, nil
}

func (f *fakeCognitoAdminAPI) AdminDeleteUser(ctx context.Context, in *cognitoidp.AdminDeleteUserInput, _ ...func(*cognitoidp.Options)) (*cognitoidp.AdminDeleteUserOutput, error) {
	f.deletedUsers = append(f.deletedUsers, aws.ToString(in.Username))
	if f.adminDeleteUserFn != nil {
		return f.adminDeleteUserFn(ctx, in)
	}
	return &cognitoidp.AdminDeleteUserOutput{}, nil
}

func (f *fakeCognitoAdminAPI) AdminDisableUser(ctx context.Context, in *cognitoidp.AdminDisableUserInput, _ ...func(*cognitoidp.Options)) (*cognitoidp.AdminDisableUserOutput, error) {
	f.disabledUsers = append(f.disabledUsers, aws.ToString(in.Username))
	if f.adminDisableUserFn != nil {
		return f.adminDisableUserFn(ctx, in)
	}
	return &cognitoidp.AdminDisableUserOutput{}, nil
}

func (f *fakeCognitoAdminAPI) AdminEnableUser(ctx context.Context, in *cognitoidp.AdminEnableUserInput, _ ...func(*cognitoidp.Options)) (*cognitoidp.AdminEnableUserOutput, error) {
	f.enabledUsers = append(f.enabledUsers, aws.ToString(in.Username))
	if f.adminEnableUserFn != nil {
		return f.adminEnableUserFn(ctx, in)
	}
	return &cognitoidp.AdminEnableUserOutput{}, nil
}

func (f *fakeCognitoAdminAPI) AdminResetUserPassword(ctx context.Context, in *cognitoidp.AdminResetUserPasswordInput, _ ...func(*cognitoidp.Options)) (*cognitoidp.AdminResetUserPasswordOutput, error) {
	f.resetPwdUsers = append(f.resetPwdUsers, aws.ToString(in.Username))
	if f.adminResetUserPasswordFn != nil {
		return f.adminResetUserPasswordFn(ctx, in)
	}
	return &cognitoidp.AdminResetUserPasswordOutput{}, nil
}

func (f *fakeCognitoAdminAPI) AdminUserGlobalSignOut(ctx context.Context, in *cognitoidp.AdminUserGlobalSignOutInput, _ ...func(*cognitoidp.Options)) (*cognitoidp.AdminUserGlobalSignOutOutput, error) {
	f.signedOutUsers = append(f.signedOutUsers, aws.ToString(in.Username))
	if f.adminUserGlobalSignOutFn != nil {
		return f.adminUserGlobalSignOutFn(ctx, in)
	}
	return &cognitoidp.AdminUserGlobalSignOutOutput{}, nil
}

// fakeUserAdminRepo is an in-memory implementation of userAdminRepo.
type fakeUserAdminRepo struct {
	lastLogins  map[string]time.Time
	batchErr    error
	detachErr   error
	detachedIDs []string
}

func (f *fakeUserAdminRepo) BatchGetUserLastLogins(_ context.Context, _ []string) (map[string]time.Time, error) {
	if f.batchErr != nil {
		return nil, f.batchErr
	}
	return f.lastLogins, nil
}

func (f *fakeUserAdminRepo) DetachDeletedUserProfile(_ context.Context, userID string) error {
	f.detachedIDs = append(f.detachedIDs, userID)
	return f.detachErr
}

// adminGroupOf returns a ListUsersInGroup responder that always reports the
// given usernames as the sole membership of the admins group, all Enabled --
// i.e. all currently able to act as an admin. guardNotSelfAndNotLastAdmin/
// warnIfNoAdminsLeft only count Enabled members (see
// listEnabledAdminUserIDs), so a disabled entry here would silently drop out
// of every last-admin scenario these tests exercise. Use
// adminGroupWithDisabled below for a test that specifically needs a
// disabled admin still counted as a group member but not as active.
func adminGroupOf(usernames ...string) func(context.Context, *cognitoidp.ListUsersInGroupInput) (*cognitoidp.ListUsersInGroupOutput, error) {
	return func(_ context.Context, _ *cognitoidp.ListUsersInGroupInput) (*cognitoidp.ListUsersInGroupOutput, error) {
		users := make([]cognitoidptypes.UserType, len(usernames))
		for i, u := range usernames {
			users[i] = cognitoidptypes.UserType{Username: aws.String(u), Enabled: true}
		}
		return &cognitoidp.ListUsersInGroupOutput{Users: users}, nil
	}
}

// adminGroupWithDisabled reports enabledUsernames as Enabled admins and
// disabledUsernames as group members that are NOT Enabled -- for exercising
// listEnabledAdminUserIDs' filtering directly.
func adminGroupWithDisabled(enabledUsernames, disabledUsernames []string) func(context.Context, *cognitoidp.ListUsersInGroupInput) (*cognitoidp.ListUsersInGroupOutput, error) {
	return func(_ context.Context, _ *cognitoidp.ListUsersInGroupInput) (*cognitoidp.ListUsersInGroupOutput, error) {
		var users []cognitoidptypes.UserType
		for _, u := range enabledUsernames {
			users = append(users, cognitoidptypes.UserType{Username: aws.String(u), Enabled: true})
		}
		for _, u := range disabledUsernames {
			users = append(users, cognitoidptypes.UserType{Username: aws.String(u), Enabled: false})
		}
		return &cognitoidp.ListUsersInGroupOutput{Users: users}, nil
	}
}

func newTestUserAdminService(cognito *fakeCognitoAdminAPI, repo *fakeUserAdminRepo) *UserAdminService {
	return NewUserAdminServiceForTest(cognito, "test-pool", repo)
}

func TestListUsers_ThreeStateDormancy(t *testing.T) {
	longAgo := time.Now().Add(-100 * 24 * time.Hour)
	cognito := &fakeCognitoAdminAPI{
		listUsersInGroupFn: adminGroupOf("admin1"),
		listUsersFn: func(_ context.Context, _ *cognitoidp.ListUsersInput) (*cognitoidp.ListUsersOutput, error) {
			return &cognitoidp.ListUsersOutput{
				Users: []cognitoidptypes.UserType{
					{
						Username:   aws.String("admin1"),
						Enabled:    true,
						UserStatus: cognitoidptypes.UserStatusTypeConfirmed,
						Attributes: []cognitoidptypes.AttributeType{{Name: aws.String("email"), Value: aws.String("admin@example.com")}},
					},
					{
						Username:   aws.String("user2"),
						Enabled:    true,
						UserStatus: cognitoidptypes.UserStatusTypeConfirmed,
						Attributes: []cognitoidptypes.AttributeType{{Name: aws.String("email"), Value: aws.String("user2@example.com")}},
					},
					{
						Username:   aws.String("user3"),
						Enabled:    true,
						UserStatus: cognitoidptypes.UserStatusTypeForceChangePassword,
						Attributes: []cognitoidptypes.AttributeType{{Name: aws.String("email"), Value: aws.String("user3@example.com")}},
					},
				},
			}, nil
		},
	}
	repo := &fakeUserAdminRepo{lastLogins: map[string]time.Time{"admin1": longAgo}}
	svc := newTestUserAdminService(cognito, repo)

	resp, err := svc.ListUsers(context.Background())
	if err != nil {
		t.Fatalf("ListUsers returned error: %v", err)
	}
	if resp.LastLoginUnavailable {
		t.Fatalf("expected LastLoginUnavailable=false")
	}

	byID := map[string]bool{}
	for _, u := range resp.Users {
		byID[u.UserID] = u.Dormant
		if u.UserID == "admin1" && !u.IsAdmin {
			t.Errorf("expected admin1 to be flagged IsAdmin")
		}
	}
	if !byID["admin1"] {
		t.Errorf("admin1 has a lastLoginAt older than 90 days and should be dormant")
	}
	if byID["user2"] {
		t.Errorf("user2 has never logged in (no record) and must NOT be flagged dormant")
	}
	if byID["user3"] {
		t.Errorf("user3 is still pending invite and must NOT be flagged dormant")
	}
}

func TestListUsers_JoinErrorDegradesInsteadOfFailing(t *testing.T) {
	cognito := &fakeCognitoAdminAPI{
		listUsersInGroupFn: adminGroupOf(),
		listUsersFn: func(_ context.Context, _ *cognitoidp.ListUsersInput) (*cognitoidp.ListUsersOutput, error) {
			return &cognitoidp.ListUsersOutput{
				Users: []cognitoidptypes.UserType{{Username: aws.String("user1"), UserStatus: cognitoidptypes.UserStatusTypeConfirmed}},
			}, nil
		},
	}
	repo := &fakeUserAdminRepo{batchErr: errors.New("dynamodb unavailable")}
	svc := newTestUserAdminService(cognito, repo)

	resp, err := svc.ListUsers(context.Background())
	if err != nil {
		t.Fatalf("expected ListUsers to degrade rather than fail, got error: %v", err)
	}
	if !resp.LastLoginUnavailable {
		t.Errorf("expected LastLoginUnavailable=true when the DynamoDB join fails")
	}
	if len(resp.Users) != 1 {
		t.Fatalf("expected users to still be returned, got %d", len(resp.Users))
	}
	if resp.Users[0].Dormant {
		t.Errorf("a user with an unknown last-login must never be marked dormant")
	}
}

func TestDeleteUser_RejectsSelf(t *testing.T) {
	cognito := &fakeCognitoAdminAPI{}
	svc := newTestUserAdminService(cognito, &fakeUserAdminRepo{})

	_, err := svc.DeleteUser(context.Background(), "user1", "user1")
	if !errors.Is(err, ErrCannotModifySelf) {
		t.Fatalf("expected ErrCannotModifySelf, got %v", err)
	}
	if len(cognito.deletedUsers) != 0 {
		t.Errorf("AdminDeleteUser must not be called when the guard rejects the request")
	}
}

func TestDeleteUser_RejectsLastAdmin(t *testing.T) {
	cognito := &fakeCognitoAdminAPI{listUsersInGroupFn: adminGroupOf("target")}
	svc := newTestUserAdminService(cognito, &fakeUserAdminRepo{})

	_, err := svc.DeleteUser(context.Background(), "actor", "target")
	if !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("expected ErrLastAdmin, got %v", err)
	}
	if len(cognito.deletedUsers) != 0 {
		t.Errorf("AdminDeleteUser must not be called when the guard rejects the request")
	}
}

func TestDeleteUser_AllowsNonLastAdmin(t *testing.T) {
	cognito := &fakeCognitoAdminAPI{listUsersInGroupFn: adminGroupOf("target", "other-admin")}
	repo := &fakeUserAdminRepo{}
	svc := newTestUserAdminService(cognito, repo)

	resp, err := svc.DeleteUser(context.Background(), "actor", "target")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cognito.deletedUsers) != 1 || cognito.deletedUsers[0] != "target" {
		t.Errorf("expected AdminDeleteUser to be called for target, got %v", cognito.deletedUsers)
	}
	if len(repo.detachedIDs) != 1 || repo.detachedIDs[0] != "target" {
		t.Errorf("expected the profile's GSI2 keys to be detached, got %v", repo.detachedIDs)
	}
	if resp.Warning != "" {
		t.Errorf("expected no warning, got %q", resp.Warning)
	}
}

func TestDeleteUser_SignOutBeforeDelete_NonFatalOnFailure(t *testing.T) {
	cognito := &fakeCognitoAdminAPI{
		listUsersInGroupFn: adminGroupOf(),
		adminUserGlobalSignOutFn: func(_ context.Context, _ *cognitoidp.AdminUserGlobalSignOutInput) (*cognitoidp.AdminUserGlobalSignOutOutput, error) {
			return nil, errors.New("sign-out failed")
		},
	}
	svc := newTestUserAdminService(cognito, &fakeUserAdminRepo{})

	resp, err := svc.DeleteUser(context.Background(), "actor", "target")
	if err != nil {
		t.Fatalf("a global-signout failure must not fail the whole delete: %v", err)
	}
	if len(cognito.deletedUsers) != 1 {
		t.Errorf("expected the delete to still proceed")
	}
	if resp.Warning == "" {
		t.Errorf("expected a warning to surface the sign-out failure")
	}
}

func TestDeleteUser_DisabledAdminIsNotCountedAsLastAdmin(t *testing.T) {
	// A disabled admin contributes nothing to "is someone still able to act
	// as admin" -- deleting them must not be blocked by the last-admin
	// guard even if they're the sole group member. Before
	// listEnabledAdminUserIDs, this would have incorrectly returned
	// ErrLastAdmin.
	cognito := &fakeCognitoAdminAPI{
		listUsersInGroupFn: adminGroupWithDisabled(nil, []string{"target"}),
	}
	svc := newTestUserAdminService(cognito, &fakeUserAdminRepo{})

	if _, err := svc.DeleteUser(context.Background(), "actor", "target"); err != nil {
		t.Fatalf("deleting an already-disabled sole admin must not be blocked: %v", err)
	}
	if len(cognito.deletedUsers) != 1 {
		t.Errorf("expected the delete to proceed")
	}
}

func TestDeleteUser_WarnsWhenAdminsGroupEndsUpEmpty(t *testing.T) {
	// Simulates the TOCTOU window: the pre-action guard sees two admins (so
	// deleting "target" is allowed), but by the time the post-hoc check runs
	// a concurrent removal has left the group empty.
	calls := 0
	cognito := &fakeCognitoAdminAPI{
		listUsersInGroupFn: func(_ context.Context, _ *cognitoidp.ListUsersInGroupInput) (*cognitoidp.ListUsersInGroupOutput, error) {
			calls++
			if calls == 1 {
				return &cognitoidp.ListUsersInGroupOutput{Users: []cognitoidptypes.UserType{
					{Username: aws.String("target"), Enabled: true}, {Username: aws.String("other-admin"), Enabled: true},
				}}, nil
			}
			return &cognitoidp.ListUsersInGroupOutput{}, nil
		},
	}
	svc := newTestUserAdminService(cognito, &fakeUserAdminRepo{})

	resp, err := svc.DeleteUser(context.Background(), "actor", "target")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Warning == "" {
		t.Errorf("expected a warning when the admins group ends up empty after the action")
	}
}

func TestDisableUser_GuardsAndSignsOut(t *testing.T) {
	cognito := &fakeCognitoAdminAPI{listUsersInGroupFn: adminGroupOf("target", "other-admin")}
	svc := newTestUserAdminService(cognito, &fakeUserAdminRepo{})

	_, err := svc.DisableUser(context.Background(), "actor", "target")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cognito.disabledUsers) != 1 || cognito.disabledUsers[0] != "target" {
		t.Errorf("expected AdminDisableUser to be called, got %v", cognito.disabledUsers)
	}
	if len(cognito.signedOutUsers) != 1 || cognito.signedOutUsers[0] != "target" {
		t.Errorf("expected AdminUserGlobalSignOut to be called to close the existing-token window, got %v", cognito.signedOutUsers)
	}
}

func TestDisableUser_RejectsSelfAndLastAdmin(t *testing.T) {
	svc := newTestUserAdminService(&fakeCognitoAdminAPI{}, &fakeUserAdminRepo{})
	if _, err := svc.DisableUser(context.Background(), "user1", "user1"); !errors.Is(err, ErrCannotModifySelf) {
		t.Fatalf("expected ErrCannotModifySelf, got %v", err)
	}

	cognito := &fakeCognitoAdminAPI{listUsersInGroupFn: adminGroupOf("target")}
	svc2 := newTestUserAdminService(cognito, &fakeUserAdminRepo{})
	if _, err := svc2.DisableUser(context.Background(), "actor", "target"); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("expected ErrLastAdmin, got %v", err)
	}
}

func TestDisableUser_WarnsWhenAdminsGroupEndsUpEmpty(t *testing.T) {
	// Mirrors TestDeleteUser_WarnsWhenAdminsGroupEndsUpEmpty for the disable
	// path. Before listEnabledAdminUserIDs this scenario was structurally
	// impossible to detect: AdminDisableUser never changes group
	// membership at all, so a membership-based count could never reach
	// zero as a *result* of a disable action, and warnIfNoAdminsLeft could
	// never fire on this path.
	calls := 0
	cognito := &fakeCognitoAdminAPI{
		listUsersInGroupFn: func(_ context.Context, _ *cognitoidp.ListUsersInGroupInput) (*cognitoidp.ListUsersInGroupOutput, error) {
			calls++
			if calls == 1 {
				// Pre-action guard: two enabled admins, so disabling
				// "target" is allowed.
				return &cognitoidp.ListUsersInGroupOutput{Users: []cognitoidptypes.UserType{
					{Username: aws.String("target"), Enabled: true}, {Username: aws.String("other-admin"), Enabled: true},
				}}, nil
			}
			// Post-hoc check: a concurrent disable already took out
			// other-admin by the time this runs -- same group membership,
			// zero enabled.
			return &cognitoidp.ListUsersInGroupOutput{Users: []cognitoidptypes.UserType{
				{Username: aws.String("target"), Enabled: false}, {Username: aws.String("other-admin"), Enabled: false},
			}}, nil
		},
	}
	svc := newTestUserAdminService(cognito, &fakeUserAdminRepo{})

	resp, err := svc.DisableUser(context.Background(), "actor", "target")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Warning == "" {
		t.Errorf("expected a warning when the admins group ends up with zero enabled admins after disabling")
	}
}

func TestEnableUser_NoGuard(t *testing.T) {
	cognito := &fakeCognitoAdminAPI{}
	svc := newTestUserAdminService(cognito, &fakeUserAdminRepo{})

	// Enabling the actor's own account (or the sole admin) must never be blocked.
	if _, err := svc.EnableUser(context.Background(), "target"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cognito.enabledUsers) != 1 {
		t.Errorf("expected AdminEnableUser to be called")
	}
}

func TestResendInvite_RejectsWrongStatus(t *testing.T) {
	cognito := &fakeCognitoAdminAPI{
		adminGetUserFn: func(_ context.Context, _ *cognitoidp.AdminGetUserInput) (*cognitoidp.AdminGetUserOutput, error) {
			return &cognitoidp.AdminGetUserOutput{UserStatus: cognitoidptypes.UserStatusTypeConfirmed}, nil
		},
	}
	svc := newTestUserAdminService(cognito, &fakeUserAdminRepo{})

	err := svc.ResendInvite(context.Background(), "target")
	if !errors.Is(err, ErrInvalidStatusForAction) {
		t.Fatalf("expected ErrInvalidStatusForAction for a CONFIRMED user, got %v", err)
	}
	if len(cognito.createUserCalls) != 0 {
		t.Errorf("AdminCreateUser must not be called when the status check fails")
	}
}

func TestResendInvite_SuccessUsesUsernameNotEmail(t *testing.T) {
	cognito := &fakeCognitoAdminAPI{
		adminGetUserFn: func(_ context.Context, _ *cognitoidp.AdminGetUserInput) (*cognitoidp.AdminGetUserOutput, error) {
			return &cognitoidp.AdminGetUserOutput{UserStatus: cognitoidptypes.UserStatusTypeForceChangePassword}, nil
		},
	}
	svc := newTestUserAdminService(cognito, &fakeUserAdminRepo{})

	if err := svc.ResendInvite(context.Background(), "target-sub"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cognito.createUserCalls) != 1 {
		t.Fatalf("expected exactly one AdminCreateUser call, got %d", len(cognito.createUserCalls))
	}
	call := cognito.createUserCalls[0]
	if aws.ToString(call.Username) != "target-sub" {
		t.Errorf("expected Username to be the immutable sub, got %q", aws.ToString(call.Username))
	}
	if call.MessageAction != cognitoidptypes.MessageActionTypeResend {
		t.Errorf("expected MessageAction=RESEND, got %q", call.MessageAction)
	}
}

func TestForceResetPassword_RejectsWrongStatus(t *testing.T) {
	cognito := &fakeCognitoAdminAPI{
		adminGetUserFn: func(_ context.Context, _ *cognitoidp.AdminGetUserInput) (*cognitoidp.AdminGetUserOutput, error) {
			return &cognitoidp.AdminGetUserOutput{UserStatus: cognitoidptypes.UserStatusTypeForceChangePassword}, nil
		},
	}
	svc := newTestUserAdminService(cognito, &fakeUserAdminRepo{})

	err := svc.ForceResetPassword(context.Background(), "target")
	if !errors.Is(err, ErrInvalidStatusForAction) {
		t.Fatalf("expected ErrInvalidStatusForAction for a user who never completed setup, got %v", err)
	}
	if len(cognito.resetPwdUsers) != 0 {
		t.Errorf("AdminResetUserPassword must not be called on a FORCE_CHANGE_PASSWORD user -- there is no code-entry screen for that state")
	}
}

func TestForceResetPassword_Success(t *testing.T) {
	cognito := &fakeCognitoAdminAPI{
		adminGetUserFn: func(_ context.Context, _ *cognitoidp.AdminGetUserInput) (*cognitoidp.AdminGetUserOutput, error) {
			return &cognitoidp.AdminGetUserOutput{UserStatus: cognitoidptypes.UserStatusTypeConfirmed}, nil
		},
	}
	svc := newTestUserAdminService(cognito, &fakeUserAdminRepo{})

	if err := svc.ForceResetPassword(context.Background(), "target"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cognito.resetPwdUsers) != 1 || cognito.resetPwdUsers[0] != "target" {
		t.Errorf("expected AdminResetUserPassword to be called for target, got %v", cognito.resetPwdUsers)
	}
}
