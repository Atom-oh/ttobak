package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/service"
)

// withUserEmailCtx injects both userID and email into the request context.
func withUserEmailCtx(r *http.Request, userID, email string) *http.Request {
	ctx := context.WithValue(r.Context(), middleware.UserIDKey, userID)
	ctx = context.WithValue(ctx, middleware.UserEmailKey, email)
	return r.WithContext(ctx)
}

// mockHandlerAccountRepo implements service.AccountRepo for handler tests.
type mockHandlerAccountRepo struct {
	accounts map[string]*model.Account
	members  map[string]*model.AccountMember
	users    map[string]*model.User
	meetingRefs map[string][]model.MeetingRef
}

func newMockHandlerAccountRepo() *mockHandlerAccountRepo {
	return &mockHandlerAccountRepo{
		accounts: make(map[string]*model.Account),
		members:  make(map[string]*model.AccountMember),
		users:    make(map[string]*model.User),
		meetingRefs: make(map[string][]model.MeetingRef),
	}
}

func acctMemberKey(accountID, userID string) string { return accountID + "|" + userID }

func (m *mockHandlerAccountRepo) CreateAccount(_ context.Context, a *model.Account, owner *model.AccountMember) error {
	ac := *a
	m.accounts[a.AccountID] = &ac
	oc := *owner
	m.members[acctMemberKey(owner.AccountID, owner.UserID)] = &oc
	return nil
}
func (m *mockHandlerAccountRepo) GetAccount(_ context.Context, id string) (*model.Account, error) {
	a, ok := m.accounts[id]
	if !ok {
		return nil, nil
	}
	c := *a
	return &c, nil
}
func (m *mockHandlerAccountRepo) GetMember(_ context.Context, accountID, userID string) (*model.AccountMember, error) {
	v, ok := m.members[acctMemberKey(accountID, userID)]
	if !ok {
		return nil, nil
	}
	c := *v
	return &c, nil
}
func (m *mockHandlerAccountRepo) PutMember(_ context.Context, member *model.AccountMember) error {
	c := *member
	m.members[acctMemberKey(member.AccountID, member.UserID)] = &c
	return nil
}
func (m *mockHandlerAccountRepo) ListAccountMembers(_ context.Context, accountID string) ([]model.AccountMember, error) {
	out := []model.AccountMember{}
	for _, v := range m.members {
		if v.AccountID == accountID {
			out = append(out, *v)
		}
	}
	return out, nil
}
func (m *mockHandlerAccountRepo) ListAccountsForUser(_ context.Context, userID string) ([]model.AccountMember, error) {
	out := []model.AccountMember{}
	for _, v := range m.members {
		if v.UserID == userID {
			out = append(out, *v)
		}
	}
	return out, nil
}
func (m *mockHandlerAccountRepo) GetUserByEmail(_ context.Context, email string) (*model.User, error) {
	u, ok := m.users[email]
	if !ok {
		return nil, nil
	}
	c := *u
	return &c, nil
}
func (m *mockHandlerAccountRepo) ListMeetingRefsForAccount(_ context.Context, accountID string) ([]model.MeetingRef, error) {
	return append([]model.MeetingRef(nil), m.meetingRefs[accountID]...), nil
}

func newStubAccountHandler() (*AccountHandler, *mockHandlerAccountRepo) {
	repo := newMockHandlerAccountRepo()
	svc := service.NewAccountServiceForTest(repo)
	return &AccountHandler{accountService: svc}, repo
}

func TestHandlerCreateAccount_Created(t *testing.T) {
	h, repo := newStubAccountHandler()
	body, _ := json.Marshal(model.CreateAccountRequest{Name: "하나은행"})
	r := httptest.NewRequest(http.MethodPost, "/api/accounts", bytes.NewReader(body))
	r = withUserEmailCtx(r, "owner-1", "o@x.com")
	w := httptest.NewRecorder()

	h.CreateAccount(w, r)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d (%s)", w.Code, w.Body.String())
	}
	var resp model.AccountResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.AccountID == "" || resp.OwnerUserID != "owner-1" {
		t.Errorf("unexpected response: %+v", resp)
	}
	if _, ok := repo.accounts[resp.AccountID]; !ok {
		t.Error("account not persisted")
	}
}

func TestHandlerGetAccount_Forbidden(t *testing.T) {
	h, repo := newStubAccountHandler()
	// seed an account owned by someone else
	repo.accounts["acc-1"] = &model.Account{AccountID: "acc-1", Name: "하나은행", OwnerUserID: "owner-1"}
	repo.members[acctMemberKey("acc-1", "owner-1")] = &model.AccountMember{AccountID: "acc-1", UserID: "owner-1", Role: model.RoleOwner}

	r := httptest.NewRequest(http.MethodGet, "/api/accounts/acc-1", nil)
	r = withUserEmailCtx(r, "stranger-9", "s@x.com")
	r = withChiParam(r, "accountId", "acc-1")
	w := httptest.NewRecorder()

	h.GetAccount(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d (%s)", w.Code, w.Body.String())
	}
}

func TestHandlerListAccountMeetings_Forbidden(t *testing.T) {
	h, repo := newStubAccountHandler()
	repo.accounts["acc-1"] = &model.Account{AccountID: "acc-1", Name: "하나은행", OwnerUserID: "owner-1"}
	repo.members[acctMemberKey("acc-1", "owner-1")] = &model.AccountMember{AccountID: "acc-1", UserID: "owner-1", Role: model.RoleOwner}

	r := httptest.NewRequest(http.MethodGet, "/api/accounts/acc-1/meetings", nil)
	r = withUserEmailCtx(r, "stranger-9", "s@x.com")
	r = withChiParam(r, "accountId", "acc-1")
	w := httptest.NewRecorder()

	h.ListAccountMeetings(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d (%s)", w.Code, w.Body.String())
	}
}

var _ = chi.URLParam // ensure chi import is used if helpers are trimmed
