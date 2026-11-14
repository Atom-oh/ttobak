package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	cognitoidp "github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"
	cognitoidptypes "github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider/types"
	"github.com/ttobak/backend/internal/model"
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
	adminUpdateAttributesFn  func(ctx context.Context, in *cognitoidp.AdminUpdateUserAttributesInput) (*cognitoidp.AdminUpdateUserAttributesOutput, error)

	// Call-tracking for assertions.
	deletedUsers    []string
	disabledUsers   []string
	enabledUsers    []string
	signedOutUsers  []string
	createUserCalls []*cognitoidp.AdminCreateUserInput
	resetPwdUsers   []string
	updateAttrCalls []*cognitoidp.AdminUpdateUserAttributesInput
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

func (f *fakeCognitoAdminAPI) AdminUpdateUserAttributes(ctx context.Context, in *cognitoidp.AdminUpdateUserAttributesInput, _ ...func(*cognitoidp.Options)) (*cognitoidp.AdminUpdateUserAttributesOutput, error) {
	f.updateAttrCalls = append(f.updateAttrCalls, in)
	if f.adminUpdateAttributesFn != nil {
		return f.adminUpdateAttributesFn(ctx, in)
	}
	return &cognitoidp.AdminUpdateUserAttributesOutput{}, nil
}

// fakeUserAdminRepo is an in-memory implementation of userAdminRepo.
type fakeUserAdminRepo struct {
	lastLogins  map[string]time.Time
	batchErr    error
	detachErr   error
	detachedIDs []string
	nameErr     error
	nameUpdates map[string]string
}

func (f *fakeUserAdminRepo) UpdateUserProfileName(_ context.Context, userID, name string) error {
	if f.nameErr != nil {
		return f.nameErr
	}
	if f.nameUpdates == nil {
		f.nameUpdates = map[string]string{}
	}
	f.nameUpdates[userID] = name
	return nil
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
			return &cognitoidp.AdminGetUserOutput{Enabled: true, UserStatus: cognitoidptypes.UserStatusTypeConfirmed, UserAttributes: []cognitoidptypes.AttributeType{{Name: aws.String("email_verified"), Value: aws.String("true")}}}, nil
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

func TestResendInvite_UsesEmailFromCurrentUser(t *testing.T) {
	const targetID = "target-sub"
	const email = "invitee@example.com"
	cognito := &fakeCognitoAdminAPI{
		adminGetUserFn: func(_ context.Context, in *cognitoidp.AdminGetUserInput) (*cognitoidp.AdminGetUserOutput, error) {
			if aws.ToString(in.Username) != targetID {
				t.Fatalf("status lookup must use the requested user ID, got %q", aws.ToString(in.Username))
			}
			return &cognitoidp.AdminGetUserOutput{
				Enabled:    true,
				Username:   aws.String(targetID),
				UserStatus: cognitoidptypes.UserStatusTypeForceChangePassword,
				UserAttributes: []cognitoidptypes.AttributeType{
					{Name: aws.String("sub"), Value: aws.String(targetID)},
					{Name: aws.String("email"), Value: aws.String(email)},
					{Name: aws.String("email_verified"), Value: aws.String("false")},
				},
			}, nil
		},
		adminCreateUserFn: func(_ context.Context, in *cognitoidp.AdminCreateUserInput) (*cognitoidp.AdminCreateUserOutput, error) {
			// The deployed pool uses UsernameAttributes=["email"]. Cognito
			// rejects a sub here even though AdminGetUser accepts it.
			if aws.ToString(in.Username) != email {
				return nil, &cognitoidptypes.InvalidParameterException{
					Message: aws.String("Username should be an email."),
				}
			}
			return &cognitoidp.AdminCreateUserOutput{}, nil
		},
	}
	svc := newTestUserAdminService(cognito, &fakeUserAdminRepo{})

	if err := svc.ResendInvite(context.Background(), targetID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cognito.createUserCalls) != 1 {
		t.Fatalf("expected exactly one AdminCreateUser call, got %d", len(cognito.createUserCalls))
	}
	call := cognito.createUserCalls[0]
	if aws.ToString(call.Username) != email {
		t.Errorf("expected Username to be the current user's email, got %q", aws.ToString(call.Username))
	}
	if call.MessageAction != cognitoidptypes.MessageActionTypeResend {
		t.Errorf("expected MessageAction=RESEND, got %q", call.MessageAction)
	}
	if len(call.DesiredDeliveryMediums) != 1 || call.DesiredDeliveryMediums[0] != cognitoidptypes.DeliveryMediumTypeEmail {
		t.Errorf("expected email delivery, got %v", call.DesiredDeliveryMediums)
	}
	if len(call.UserAttributes) != 1 ||
		aws.ToString(call.UserAttributes[0].Name) != "email" ||
		aws.ToString(call.UserAttributes[0].Value) != email {
		t.Errorf("resend must supply only the existing email without changing verification or other attributes, got %v", call.UserAttributes)
	}
}

func TestResendInvite_RejectsMissingEmail(t *testing.T) {
	tests := []struct {
		name  string
		attrs []cognitoidptypes.AttributeType
	}{
		{name: "no attributes"},
		{name: "no email", attrs: []cognitoidptypes.AttributeType{
			{Name: aws.String("sub"), Value: aws.String("target-sub")},
		}},
		{name: "empty email", attrs: []cognitoidptypes.AttributeType{
			{Name: aws.String("email"), Value: aws.String("")},
		}},
		{name: "nil email", attrs: []cognitoidptypes.AttributeType{
			{Name: aws.String("email")},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cognito := &fakeCognitoAdminAPI{
				adminGetUserFn: func(_ context.Context, _ *cognitoidp.AdminGetUserInput) (*cognitoidp.AdminGetUserOutput, error) {
					return &cognitoidp.AdminGetUserOutput{
						UserStatus:     cognitoidptypes.UserStatusTypeForceChangePassword,
						UserAttributes: tt.attrs,
					}, nil
				},
			}
			svc := newTestUserAdminService(cognito, &fakeUserAdminRepo{})
			if err := svc.ResendInvite(context.Background(), "target-sub"); err == nil {
				t.Fatal("expected an error when the current user has no email")
			}
			if len(cognito.createUserCalls) != 0 {
				t.Fatal("AdminCreateUser must not be called without the current user's email")
			}
		})
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
			return &cognitoidp.AdminGetUserOutput{Enabled: true, UserStatus: cognitoidptypes.UserStatusTypeConfirmed, UserAttributes: []cognitoidptypes.AttributeType{{Name: aws.String("email_verified"), Value: aws.String("true")}}}, nil
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

func TestRecoveryActionsRejectDisabledOrUnverifiedUsersBeforeMail(t *testing.T) {
	for _, test := range []struct {
		name                      string
		enabled, verified, resend bool
		want                      error
	}{
		{"disabled reset", false, true, false, ErrUserDisabled},
		{"unverified reset", true, false, false, ErrEmailVerificationRequired},
		{"disabled resend", false, true, true, ErrUserDisabled},
	} {
		t.Run(test.name, func(t *testing.T) {
			status := cognitoidptypes.UserStatusTypeConfirmed
			if test.resend {
				status = cognitoidptypes.UserStatusTypeForceChangePassword
			}
			c := &fakeCognitoAdminAPI{adminGetUserFn: func(context.Context, *cognitoidp.AdminGetUserInput) (*cognitoidp.AdminGetUserOutput, error) {
				return &cognitoidp.AdminGetUserOutput{Enabled: test.enabled, UserStatus: status, UserAttributes: []cognitoidptypes.AttributeType{{Name: aws.String("email_verified"), Value: aws.String(map[bool]string{true: "true", false: "false"}[test.verified])}}}, nil
			}}
			s := newTestUserAdminService(c, &fakeUserAdminRepo{})
			var err error
			if test.resend {
				err = s.ResendInvite(context.Background(), "target")
			} else {
				err = s.ForceResetPassword(context.Background(), "target")
			}
			if !errors.Is(err, test.want) || len(c.resetPwdUsers) != 0 || len(c.createUserCalls) != 0 {
				t.Fatalf("unexpected recovery result: %v", err)
			}
		})
	}
}

// teamSyncUser builds an AdminGetUser result for team-sync tests.
func teamSyncUser(email string, verified bool, dept string) *cognitoidp.AdminGetUserOutput {
	attrs := []cognitoidptypes.AttributeType{
		{Name: aws.String("email"), Value: aws.String(email)},
		{Name: aws.String("email_verified"), Value: aws.String(map[bool]string{true: "true", false: "false"}[verified])},
	}
	if dept != "" {
		attrs = append(attrs, cognitoidptypes.AttributeType{Name: aws.String("custom:department"), Value: aws.String(dept)})
	}
	return &cognitoidp.AdminGetUserOutput{UserAttributes: attrs, Enabled: true}
}

func teamSyncService(users map[string]*cognitoidp.AdminGetUserOutput) (*UserAdminService, *fakeCognitoAdminAPI) {
	cognito := &fakeCognitoAdminAPI{
		adminGetUserFn: func(_ context.Context, in *cognitoidp.AdminGetUserInput) (*cognitoidp.AdminGetUserOutput, error) {
			if u, ok := users[aws.ToString(in.Username)]; ok {
				return u, nil
			}
			return nil, &cognitoidptypes.UserNotFoundException{}
		},
	}
	return newTestUserAdminService(cognito, &fakeUserAdminRepo{}), cognito
}

func TestValidTeamValue(t *testing.T) {
	long50 := "a" + strings.Repeat("b", 49)
	for _, ok := range []string{"mihong", "a", "team-1", long50} {
		if !ValidTeamValue(ok) {
			t.Errorf("ValidTeamValue(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"", "Mihong", "-x", "a b", "a=b", "a,b", long50 + "c"} {
		if ValidTeamValue(bad) {
			t.Errorf("ValidTeamValue(%q) = true, want false", bad)
		}
	}
}

func TestListUsers_IncludesDepartment(t *testing.T) {
	cognito := &fakeCognitoAdminAPI{
		listUsersFn: func(context.Context, *cognitoidp.ListUsersInput) (*cognitoidp.ListUsersOutput, error) {
			return &cognitoidp.ListUsersOutput{Users: []cognitoidptypes.UserType{
				{Username: aws.String("u1"), Attributes: []cognitoidptypes.AttributeType{
					{Name: aws.String("custom:department"), Value: aws.String("mihong")},
				}},
				{Username: aws.String("u2")},
			}}, nil
		},
	}
	resp, err := newTestUserAdminService(cognito, &fakeUserAdminRepo{}).ListUsers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if resp.Users[0].Department != "mihong" || resp.Users[1].Department != "" {
		t.Fatalf("departments = %q, %q", resp.Users[0].Department, resp.Users[1].Department)
	}
}

func TestTeamSync_WritesOnlyDepartment(t *testing.T) {
	svc, cognito := teamSyncService(map[string]*cognitoidp.AdminGetUserOutput{"u1": teamSyncUser("a@amazon.com", true, "")})
	resp, err := svc.SyncTeams(context.Background(), "admin", []model.TeamSyncItem{{UserID: "u1", Team: "mihong"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Updated) != 1 || resp.Updated[0] != "u1" {
		t.Fatalf("updated = %v", resp.Updated)
	}
	if len(cognito.updateAttrCalls) != 1 {
		t.Fatalf("update calls = %d", len(cognito.updateAttrCalls))
	}
	in := cognito.updateAttrCalls[0]
	if aws.ToString(in.Username) != "u1" || len(in.UserAttributes) != 1 ||
		aws.ToString(in.UserAttributes[0].Name) != "custom:department" || aws.ToString(in.UserAttributes[0].Value) != "mihong" {
		t.Fatalf("unexpected update input: %+v", in)
	}
}

func TestTeamSync_ConflictWritesNothing(t *testing.T) {
	svc, cognito := teamSyncService(map[string]*cognitoidp.AdminGetUserOutput{"u1": teamSyncUser("a@amazon.com", true, "hana")})
	resp, err := svc.SyncTeams(context.Background(), "admin", []model.TeamSyncItem{{UserID: "u1", Team: "mihong", Expected: ""}})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Conflicts) != 1 || resp.Conflicts[0] != "u1" || len(cognito.updateAttrCalls) != 0 {
		t.Fatalf("conflicts=%v calls=%d", resp.Conflicts, len(cognito.updateAttrCalls))
	}
}

func TestTeamSync_UnchangedIsCounted(t *testing.T) {
	svc, cognito := teamSyncService(map[string]*cognitoidp.AdminGetUserOutput{"u1": teamSyncUser("a@amazon.com", true, "mihong")})
	resp, err := svc.SyncTeams(context.Background(), "admin", []model.TeamSyncItem{{UserID: "u1", Team: "mihong", Expected: "mihong"}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Unchanged != 1 || len(resp.Updated) != 0 || len(cognito.updateAttrCalls) != 0 {
		t.Fatalf("resp=%+v calls=%d", resp, len(cognito.updateAttrCalls))
	}
}

func TestTeamSync_RejectsNonDirectoryUsers(t *testing.T) {
	svc, cognito := teamSyncService(map[string]*cognitoidp.AdminGetUserOutput{
		"gmail":      teamSyncUser("a@gmail.com", true, ""),
		"unverified": teamSyncUser("a@amazon.com", false, ""),
		"upper":      teamSyncUser("X@AMAZON.COM", true, ""),
	})
	resp, err := svc.SyncTeams(context.Background(), "admin", []model.TeamSyncItem{
		{UserID: "gmail", Team: "t"}, {UserID: "unverified", Team: "t"}, {UserID: "upper", Team: "t"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Rejected) != 2 || resp.Rejected[0].Code != "NOT_DIRECTORY_USER" || resp.Rejected[1].Code != "NOT_DIRECTORY_USER" {
		t.Fatalf("rejected = %+v", resp.Rejected)
	}
	if len(resp.Updated) != 1 || resp.Updated[0] != "upper" || len(cognito.updateAttrCalls) != 1 {
		t.Fatalf("updated=%v calls=%d", resp.Updated, len(cognito.updateAttrCalls))
	}
}

func TestTeamSync_RejectsInvalidTeamAndMissingUser(t *testing.T) {
	svc, cognito := teamSyncService(map[string]*cognitoidp.AdminGetUserOutput{"u1": teamSyncUser("a@amazon.com", true, "")})
	resp, err := svc.SyncTeams(context.Background(), "admin", []model.TeamSyncItem{
		{UserID: "u1", Team: "Bad Team"}, {UserID: "ghost", Team: "ok"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Rejected) != 2 || resp.Rejected[0].Code != "INVALID_TEAM" || resp.Rejected[1].Code != "NOT_FOUND" || resp.Rejected[1].UserID != "ghost" {
		t.Fatalf("rejected = %+v", resp.Rejected)
	}
	if len(cognito.updateAttrCalls) != 0 {
		t.Fatal("unexpected update")
	}
}

func TestTeamSync_PerItemCognitoErrorContinues(t *testing.T) {
	svc, cognito := teamSyncService(map[string]*cognitoidp.AdminGetUserOutput{
		"u1": teamSyncUser("a@amazon.com", true, ""),
		"u2": teamSyncUser("b@amazon.com", true, ""),
		"u3": teamSyncUser("c@amazon.com", true, ""),
	})
	cognito.adminUpdateAttributesFn = func(_ context.Context, in *cognitoidp.AdminUpdateUserAttributesInput) (*cognitoidp.AdminUpdateUserAttributesOutput, error) {
		if aws.ToString(in.Username) == "u2" {
			return nil, errors.New("boom")
		}
		return &cognitoidp.AdminUpdateUserAttributesOutput{}, nil
	}
	resp, err := svc.SyncTeams(context.Background(), "admin", []model.TeamSyncItem{
		{UserID: "u1", Team: "t"}, {UserID: "u2", Team: "t"}, {UserID: "u3", Team: "t"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Rejected) != 1 || resp.Rejected[0].UserID != "u2" || resp.Rejected[0].Code != "COGNITO_ERROR" {
		t.Fatalf("rejected = %+v", resp.Rejected)
	}
	if len(resp.Updated) != 2 || resp.Updated[0] != "u1" || resp.Updated[1] != "u3" {
		t.Fatalf("updated = %v", resp.Updated)
	}
}

func TestTeamSync_RequestLimits(t *testing.T) {
	over := make([]model.TeamSyncItem, 201)
	for i := range over {
		over[i] = model.TeamSyncItem{UserID: fmt.Sprintf("u%d", i), Team: "t"}
	}
	cases := map[string][]model.TeamSyncItem{
		"empty":     nil,
		"over":      over,
		"duplicate": {{UserID: "u1", Team: "t"}, {UserID: "u1", Team: "t"}},
	}
	for name, items := range cases {
		t.Run(name, func(t *testing.T) {
			getCalls := 0
			svc, cognito := teamSyncService(nil)
			inner := cognito.adminGetUserFn
			cognito.adminGetUserFn = func(ctx context.Context, in *cognitoidp.AdminGetUserInput) (*cognitoidp.AdminGetUserOutput, error) {
				getCalls++
				return inner(ctx, in)
			}
			_, err := svc.SyncTeams(context.Background(), "admin", items)
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("err = %v, want ErrInvalidInput", err)
			}
			if getCalls != 0 || len(cognito.updateAttrCalls) != 0 {
				t.Fatal("Cognito was called")
			}
		})
	}
}
