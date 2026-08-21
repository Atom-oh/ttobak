package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	cognitoidp "github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"
	cognitoidptypes "github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider/types"
	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/service"
)

// stubUserAdminRepo is a minimal service.UserAdminRepo for handler tests --
// no login records, no detach errors. It exists so these tests exercise
// UserAdminService's real guard/status logic through the handler, not a
// re-implementation of it.
type stubUserAdminRepo struct{}

func (stubUserAdminRepo) BatchGetUserLastLogins(context.Context, []string) (map[string]time.Time, error) {
	return map[string]time.Time{}, nil
}
func (stubUserAdminRepo) DetachDeletedUserProfile(context.Context, string) error { return nil }

// stubCognitoAdminAPI is a minimal service.CognitoAdminAPI. adminGroup is the
// current membership of "admins" (drives the self/last-admin guards);
// getUserStatus is what AdminGetUser reports (drives resend-invite /
// reset-password's status check).
type stubCognitoAdminAPI struct {
	adminGroup    []string
	getUserStatus cognitoidptypes.UserStatusType
}

func (s *stubCognitoAdminAPI) ListUsers(_ context.Context, _ *cognitoidp.ListUsersInput, _ ...func(*cognitoidp.Options)) (*cognitoidp.ListUsersOutput, error) {
	return &cognitoidp.ListUsersOutput{
		Users: []cognitoidptypes.UserType{
			{Username: aws.String("user-1"), Enabled: true, UserStatus: cognitoidptypes.UserStatusTypeConfirmed},
		},
	}, nil
}

func (s *stubCognitoAdminAPI) ListUsersInGroup(_ context.Context, _ *cognitoidp.ListUsersInGroupInput, _ ...func(*cognitoidp.Options)) (*cognitoidp.ListUsersInGroupOutput, error) {
	users := make([]cognitoidptypes.UserType, len(s.adminGroup))
	for i, u := range s.adminGroup {
		// Enabled: true -- guardNotSelfAndNotLastAdmin/warnIfNoAdminsLeft
		// only count Enabled members (listEnabledAdminUserIDs); adminGroup
		// represents currently-active admins for every test using this stub.
		users[i] = cognitoidptypes.UserType{Username: aws.String(u), Enabled: true}
	}
	return &cognitoidp.ListUsersInGroupOutput{Users: users}, nil
}

func (s *stubCognitoAdminAPI) AdminGetUser(_ context.Context, _ *cognitoidp.AdminGetUserInput, _ ...func(*cognitoidp.Options)) (*cognitoidp.AdminGetUserOutput, error) {
	return &cognitoidp.AdminGetUserOutput{UserStatus: s.getUserStatus}, nil
}

func (s *stubCognitoAdminAPI) AdminCreateUser(_ context.Context, _ *cognitoidp.AdminCreateUserInput, _ ...func(*cognitoidp.Options)) (*cognitoidp.AdminCreateUserOutput, error) {
	return &cognitoidp.AdminCreateUserOutput{}, nil
}
func (s *stubCognitoAdminAPI) AdminAddUserToGroup(_ context.Context, _ *cognitoidp.AdminAddUserToGroupInput, _ ...func(*cognitoidp.Options)) (*cognitoidp.AdminAddUserToGroupOutput, error) {
	return &cognitoidp.AdminAddUserToGroupOutput{}, nil
}
func (s *stubCognitoAdminAPI) AdminDeleteUser(_ context.Context, _ *cognitoidp.AdminDeleteUserInput, _ ...func(*cognitoidp.Options)) (*cognitoidp.AdminDeleteUserOutput, error) {
	return &cognitoidp.AdminDeleteUserOutput{}, nil
}
func (s *stubCognitoAdminAPI) AdminDisableUser(_ context.Context, _ *cognitoidp.AdminDisableUserInput, _ ...func(*cognitoidp.Options)) (*cognitoidp.AdminDisableUserOutput, error) {
	return &cognitoidp.AdminDisableUserOutput{}, nil
}
func (s *stubCognitoAdminAPI) AdminEnableUser(_ context.Context, _ *cognitoidp.AdminEnableUserInput, _ ...func(*cognitoidp.Options)) (*cognitoidp.AdminEnableUserOutput, error) {
	return &cognitoidp.AdminEnableUserOutput{}, nil
}
func (s *stubCognitoAdminAPI) AdminResetUserPassword(_ context.Context, _ *cognitoidp.AdminResetUserPasswordInput, _ ...func(*cognitoidp.Options)) (*cognitoidp.AdminResetUserPasswordOutput, error) {
	return &cognitoidp.AdminResetUserPasswordOutput{}, nil
}
func (s *stubCognitoAdminAPI) AdminUserGlobalSignOut(_ context.Context, _ *cognitoidp.AdminUserGlobalSignOutInput, _ ...func(*cognitoidp.Options)) (*cognitoidp.AdminUserGlobalSignOutOutput, error) {
	return &cognitoidp.AdminUserGlobalSignOutOutput{}, nil
}

func newTestUserAdminHandler(adminGroup []string, getUserStatus cognitoidptypes.UserStatusType) *UserAdminHandler {
	cognito := &stubCognitoAdminAPI{adminGroup: adminGroup, getUserStatus: getUserStatus}
	svc := service.NewUserAdminServiceForTest(cognito, "test-pool", stubUserAdminRepo{})
	return NewUserAdminHandler(svc)
}

func TestListUsersHandler_ReturnsUserList(t *testing.T) {
	h := newTestUserAdminHandler(nil, "")

	req := httptest.NewRequest(http.MethodGet, "/api/settings/users", nil)
	w := httptest.NewRecorder()
	h.ListUsers(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp model.AdminUserListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(resp.Users) != 1 || resp.Users[0].UserID != "user-1" {
		t.Fatalf("unexpected users in response: %+v", resp.Users)
	}
}

func TestDeleteUserHandler_RejectsSelfWith400(t *testing.T) {
	h := newTestUserAdminHandler(nil, "")

	req := httptest.NewRequest(http.MethodDelete, "/api/settings/users/user-1", nil)
	req = withUserCtx(req, "user-1")
	req = withChiParam(req, "userId", "user-1")
	w := httptest.NewRecorder()
	h.DeleteUser(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for self-delete, got %d: %s", w.Code, w.Body.String())
	}
	var resp model.ErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode error response: %v", err)
	}
	if resp.Error.Code != model.ErrCodeBadRequest {
		t.Fatalf("expected BAD_REQUEST error code, got %q", resp.Error.Code)
	}
}

func TestDeleteUserHandler_RejectsLastAdminWith400(t *testing.T) {
	h := newTestUserAdminHandler([]string{"target"}, "")

	req := httptest.NewRequest(http.MethodDelete, "/api/settings/users/target", nil)
	req = withUserCtx(req, "actor")
	req = withChiParam(req, "userId", "target")
	w := httptest.NewRecorder()
	h.DeleteUser(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for last-admin delete, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDeleteUserHandler_AllowsNonLastAdmin(t *testing.T) {
	h := newTestUserAdminHandler([]string{"target", "other-admin"}, "")

	req := httptest.NewRequest(http.MethodDelete, "/api/settings/users/target", nil)
	req = withUserCtx(req, "actor")
	req = withChiParam(req, "userId", "target")
	w := httptest.NewRecorder()
	h.DeleteUser(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDisableUserHandler_RejectsSelfWith400(t *testing.T) {
	h := newTestUserAdminHandler(nil, "")

	req := httptest.NewRequest(http.MethodPut, "/api/settings/users/user-1/disable", nil)
	req = withUserCtx(req, "user-1")
	req = withChiParam(req, "userId", "user-1")
	w := httptest.NewRecorder()
	h.DisableUser(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for self-disable, got %d: %s", w.Code, w.Body.String())
	}
}

func TestEnableUserHandler_Success(t *testing.T) {
	h := newTestUserAdminHandler(nil, "")

	req := httptest.NewRequest(http.MethodPut, "/api/settings/users/user-1/enable", nil)
	req = withChiParam(req, "userId", "user-1")
	w := httptest.NewRecorder()
	h.EnableUser(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestResendInviteHandler_RejectsConfirmedUserWith400(t *testing.T) {
	h := newTestUserAdminHandler(nil, cognitoidptypes.UserStatusTypeConfirmed)

	req := httptest.NewRequest(http.MethodPost, "/api/settings/users/user-1/resend-invite", nil)
	req = withChiParam(req, "userId", "user-1")
	w := httptest.NewRecorder()
	h.ResendInvite(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 when resending an invite to an already-confirmed user, got %d: %s", w.Code, w.Body.String())
	}
}

func TestResendInviteHandler_AllowsPendingUser(t *testing.T) {
	h := newTestUserAdminHandler(nil, cognitoidptypes.UserStatusTypeForceChangePassword)

	req := httptest.NewRequest(http.MethodPost, "/api/settings/users/user-1/resend-invite", nil)
	req = withChiParam(req, "userId", "user-1")
	w := httptest.NewRecorder()
	h.ResendInvite(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestResetPasswordHandler_RejectsPendingUserWith400(t *testing.T) {
	h := newTestUserAdminHandler(nil, cognitoidptypes.UserStatusTypeForceChangePassword)

	req := httptest.NewRequest(http.MethodPost, "/api/settings/users/user-1/reset-password", nil)
	req = withChiParam(req, "userId", "user-1")
	w := httptest.NewRecorder()
	h.ResetPassword(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 -- a FORCE_CHANGE_PASSWORD user has no code-entry screen to consume a reset, got %d: %s", w.Code, w.Body.String())
	}
}

func TestResetPasswordHandler_AllowsConfirmedUser(t *testing.T) {
	h := newTestUserAdminHandler(nil, cognitoidptypes.UserStatusTypeConfirmed)

	req := httptest.NewRequest(http.MethodPost, "/api/settings/users/user-1/reset-password", nil)
	req = withChiParam(req, "userId", "user-1")
	w := httptest.NewRecorder()
	h.ResetPassword(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

// middleware import is used only by withUserCtx (defined in meeting_test.go);
// this blank reference keeps goimports from flagging it if that helper ever
// moves, without duplicating the helper here.
var _ = middleware.UserIDKey
