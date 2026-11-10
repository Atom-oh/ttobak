package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ttobak/backend/internal/connector"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/service"
)

// mergingAccountRepo fails member writes the way the repository fence does.
type mergingAccountRepo struct{ *mockHandlerAccountRepo }

func (mergingAccountRepo) PutMember(context.Context, *model.AccountMember) error {
	return repository.ErrAccountMerging
}

func (mergingAccountRepo) DeleteMember(context.Context, string, string, string) error {
	return repository.ErrAccountMerging
}

func (mergingAccountRepo) UpdateMemberRole(context.Context, string, string, string) error {
	return repository.ErrAccountMerging
}

func (mergingAccountRepo) PutAccountDocument(context.Context, *model.AccountDocument) error {
	return repository.ErrAccountMerging
}

func requireAccountMerging(t *testing.T, name string, w *httptest.ResponseRecorder) {
	t.Helper()
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), model.ErrCodeAccountMerging) {
		t.Fatalf("%s: status=%d body=%s, want 409 ACCOUNT_MERGING", name, w.Code, w.Body.String())
	}
}

func TestAccountMergingMapsTo409ForMembers(t *testing.T) {
	base := newMockHandlerAccountRepo()
	base.accounts["acc"] = &model.Account{AccountID: "acc", Name: "acc", OwnerUserID: "owner"}
	base.members[acctMemberKey("acc", "owner")] = &model.AccountMember{AccountID: "acc", UserID: "owner", Role: model.RoleOwner}
	base.members[acctMemberKey("acc", "tam")] = &model.AccountMember{AccountID: "acc", UserID: "tam", Role: model.RoleTAM}
	base.users["new@example.com"] = &model.User{UserID: "new", Email: "new@example.com"}
	h := &AccountHandler{accountService: service.NewAccountServiceForTest(mergingAccountRepo{base})}

	add := httptest.NewRequest(http.MethodPost, "/api/accounts/acc/members", strings.NewReader(`{"email":"new@example.com","role":"SSA"}`))
	add = withChiParam(withUserEmailCtx(add, "owner", "o@x.com"), "accountId", "acc")
	w := httptest.NewRecorder()
	h.AddMember(w, add)
	requireAccountMerging(t, "add member", w)

	role := httptest.NewRequest(http.MethodPut, "/api/accounts/acc/members/tam", strings.NewReader(`{"role":"SSA"}`))
	role = withChiParam(withChiParam(withUserEmailCtx(role, "owner", "o@x.com"), "accountId", "acc"), "userId", "tam")
	w = httptest.NewRecorder()
	h.UpdateMemberRole(w, role)
	requireAccountMerging(t, "update member role", w)

	doc := httptest.NewRequest(http.MethodPost, "/api/accounts/acc/documents", strings.NewReader(`{"title":"t","markdown":"m"}`))
	doc = withChiParam(withUserEmailCtx(doc, "owner", "o@x.com"), "accountId", "acc")
	w = httptest.NewRecorder()
	h.PutDocument(w, doc)
	requireAccountMerging(t, "account document", w)

	remove := httptest.NewRequest(http.MethodDelete, "/api/accounts/acc/members/tam", nil)
	remove = withChiParam(withChiParam(withUserEmailCtx(remove, "owner", "o@x.com"), "accountId", "acc"), "userId", "tam")
	w = httptest.NewRecorder()
	h.RemoveMember(w, remove)
	requireAccountMerging(t, "remove member", w)
}

func TestAccountMergingMapsTo409ForAccountWrites(t *testing.T) {
	for name, write := range map[string]func(http.ResponseWriter, error){
		"contact":   contactError,
		"event":     eventError,
		"link":      accountLinksError,
		"hierarchy": writeAccountHierarchyError,
		"project":   func(w http.ResponseWriter, err error) { writeProjectError(w, err, "denied") },
	} {
		w := httptest.NewRecorder()
		write(w, service.ErrAccountMerging)
		requireAccountMerging(t, name, w)
	}
}

func TestAccountMergingIsDefinitiveForConnectorCreates(t *testing.T) {
	for name, result := range map[string]connector.InvokeResult{
		"contact": contactCreateFailure(fmt.Errorf("wrapped: %w", service.ErrAccountMerging)),
		"event":   eventCreateFailure(fmt.Errorf("wrapped: %w", service.ErrAccountMerging)),
	} {
		if result.StatusCode != http.StatusConflict || !strings.Contains(result.Body, model.ErrCodeAccountMerging) ||
			strings.Contains(result.Body, "CONNECTOR_WRITE_UNCONFIRMED") {
			t.Fatalf("%s: status=%d body=%s, want definitive 409 ACCOUNT_MERGING", name, result.StatusCode, result.Body)
		}
	}
}

func TestReplayAccountMergingIsDistinctForConnectorCreates(t *testing.T) {
	for name, result := range map[string]connector.InvokeResult{
		"contact": contactCreateFailure(repository.ErrConnectorReplayAccountMerging),
		"event":   eventCreateFailure(repository.ErrConnectorReplayAccountMerging),
	} {
		if result.StatusCode != http.StatusConflict || !strings.Contains(result.Body, model.ErrCodeReplayAccountMerging) ||
			strings.Contains(result.Body, `"`+model.ErrCodeAccountMerging+`"`) {
			t.Fatalf("%s: status=%d body=%s, want 409 %s", name, result.StatusCode, result.Body, model.ErrCodeReplayAccountMerging)
		}
	}
}

func TestAccountMergingMapsTo409ForNewsAndPeople(t *testing.T) {
	for name, write := range map[string]func(http.ResponseWriter, error){
		"news":            accountNewsError,
		"people":          accountPeopleError,
		"insights delete": writeInsightsDeleteError,
		"audio crop":      writeCropError,
		"document share":  func(w http.ResponseWriter, err error) { writeDocumentServiceError(w, err, "") },
	} {
		w := httptest.NewRecorder()
		write(w, fmt.Errorf("wrapped: %w", service.ErrAccountMerging))
		requireAccountMerging(t, name, w)
	}
}
