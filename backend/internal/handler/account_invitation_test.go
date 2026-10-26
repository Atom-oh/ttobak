package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/model"
)

func TestAccountMemberInvitation_UnverifiedRequester(t *testing.T) {
	h, repo := newStubAccountHandler()
	repo.members[acctMemberKey("account", "member")] = &model.AccountMember{AccountID: "account", UserID: "member", Role: model.RoleSSA}
	repo.users["recipient@example.com"] = &model.User{UserID: "recipient", Email: "recipient@example.com"}
	req := httptest.NewRequest(http.MethodPost, "/api/accounts/account/members", strings.NewReader(`{"email":"recipient@example.com","role":"SSA"}`))
	req = withUserEmailCtx(req, "member", "member@example.com")
	req = req.WithContext(context.WithValue(req.Context(), middleware.UserEmailVerifiedKey, false))
	req = withChiParam(req, "accountId", "account")
	response := httptest.NewRecorder()
	h.AddMember(response, req)
	if response.Code != http.StatusForbidden {
		t.Fatalf("unverified user accepted: status=%d body=%s", response.Code, response.Body)
	}
}

func TestAccountMemberInvitation_BoundedSingleRequest(t *testing.T) {
	for _, body := range []string{
		`{"email":"recipient@example.com","role":"SSA"}` + strings.Repeat(" ", 2000),
		`{"email":"recipient@example.com","role":"SSA"} {}`,
	} {
		h, repo := newStubAccountHandler()
		repo.members[acctMemberKey("account", "member")] = &model.AccountMember{AccountID: "account", UserID: "member", Role: model.RoleSSA}
		repo.users["recipient@example.com"] = &model.User{UserID: "recipient", Email: "recipient@example.com"}
		req := httptest.NewRequest(http.MethodPost, "/api/accounts/account/members", strings.NewReader(body))
		req = withChiParam(withUserEmailCtx(req, "member", "member@example.com"), "accountId", "account")
		response := httptest.NewRecorder()
		h.AddMember(response, req)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("unbounded or trailing request accepted: status=%d", response.Code)
		}
	}
}
