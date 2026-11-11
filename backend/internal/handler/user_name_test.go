package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/service"
)

func newUserNameHandlers() (*ProfileHandler, *UserAdminHandler, *stubCognitoAdminAPI) {
	cognito := &stubCognitoAdminAPI{}
	svc := service.NewUserAdminServiceForTest(cognito, "test-pool", stubUserAdminRepo{})
	return NewProfileHandler(svc), NewUserAdminHandler(svc), cognito
}

func TestUpdateMyNameTargetsVerifiedSubject(t *testing.T) {
	profile, _, cognito := newUserNameHandlers()

	// An extra userId field is rejected outright; the target can only come
	// from the verified JWT subject.
	req := httptest.NewRequest(http.MethodPut, "/api/settings/profile/name", strings.NewReader(`{"name":"홍길동","userId":"victim"}`))
	req = withUserCtx(req, "self")
	w := httptest.NewRecorder()
	profile.UpdateMyName(w, req)
	if w.Code != http.StatusBadRequest || len(cognito.updatedUsers) != 0 {
		t.Fatalf("unknown field: status %d, updated %v", w.Code, cognito.updatedUsers)
	}

	req = httptest.NewRequest(http.MethodPut, "/api/settings/profile/name", strings.NewReader(`{"name":" 홍길동 "}`))
	req = withUserCtx(req, "self")
	w = httptest.NewRecorder()
	profile.UpdateMyName(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var resp model.UserNameResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.UserID != "self" || resp.Name != "홍길동" {
		t.Fatalf("response %+v", resp)
	}
	if len(cognito.updatedUsers) != 1 || cognito.updatedUsers[0] != "self" {
		t.Fatalf("updated %v, want [self]", cognito.updatedUsers)
	}
}

func TestUpdateMyNameRequiresIdentity(t *testing.T) {
	profile, _, cognito := newUserNameHandlers()
	req := httptest.NewRequest(http.MethodPut, "/api/settings/profile/name", strings.NewReader(`{"name":"홍길동"}`))
	w := httptest.NewRecorder()
	profile.UpdateMyName(w, req)
	if w.Code != http.StatusUnauthorized || len(cognito.updatedUsers) != 0 {
		t.Fatalf("status %d, updated %v", w.Code, cognito.updatedUsers)
	}
}

func TestUpdateUserNameValidation(t *testing.T) {
	tests := []struct {
		name string
		body string
		want int
	}{
		{name: "blank", body: `{"name":"   "}`, want: http.StatusBadRequest},
		{name: "control", body: `{"name":"홍\n길동"}`, want: http.StatusBadRequest},
		{name: "malformed", body: `{"name":`, want: http.StatusBadRequest},
		{name: "oversized body", body: `{"name":"` + strings.Repeat("a", maxUserNameBody) + `"}`, want: http.StatusBadRequest},
		{name: "valid", body: `{"name":"김철수"}`, want: http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, admin, _ := newUserNameHandlers()
			req := httptest.NewRequest(http.MethodPut, "/api/settings/users/target/name", strings.NewReader(tc.body))
			req = withUserCtx(req, "admin-1")
			req = withChiParam(req, "userId", "target")
			w := httptest.NewRecorder()
			admin.UpdateUserName(w, req)
			if w.Code != tc.want {
				t.Fatalf("status %d, want %d: %s", w.Code, tc.want, w.Body.String())
			}
		})
	}
}
