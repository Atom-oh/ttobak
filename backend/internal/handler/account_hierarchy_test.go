package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/service"
)

type handlerHierarchyRepo struct {
	*mockHandlerAccountRepo
	writeErr error
}

func (r *handlerHierarchyRepo) UpdateAccountParent(_ context.Context, id, user, old, parent string, ancestors []repository.AccountParentLink) error {
	if r.writeErr != nil {
		return r.writeErr
	}
	r.accounts[id].ParentAccountID = parent
	return nil
}

func (r *handlerHierarchyRepo) CreateAccountWithParent(ctx context.Context, a *model.Account, owner *model.AccountMember, ancestors []repository.AccountParentLink) error {
	if r.writeErr != nil {
		return r.writeErr
	}
	return r.CreateAccount(ctx, a, owner)
}

func newHierarchyHandler() (*AccountHandler, *handlerHierarchyRepo) {
	r := &handlerHierarchyRepo{mockHandlerAccountRepo: newMockHandlerAccountRepo()}
	r.accounts["child"] = &model.Account{AccountID: "child", OwnerUserID: "owner"}
	r.accounts["parent"] = &model.Account{AccountID: "parent", OwnerUserID: "other"}
	r.members[acctMemberKey("child", "owner")] = &model.AccountMember{AccountID: "child", UserID: "owner", Role: model.RoleOwner}
	r.members[acctMemberKey("parent", "owner")] = &model.AccountMember{AccountID: "parent", UserID: "owner", Role: model.RoleTAM}
	return NewAccountHandler(service.NewAccountServiceForTest(r), nil), r
}

func TestHandlerAccountHierarchyUpdate(t *testing.T) {
	for _, tc := range []struct {
		name, body, user string
		setup            func(*handlerHierarchyRepo)
		status           int
		code             string
	}{
		{name: "attach", body: `{"parentAccountId":"parent"}`, user: "owner", status: 200},
		{name: "detach", body: `{"parentAccountId":""}`, user: "owner", status: 200},
		{name: "missing", body: `{}`, user: "owner", status: 400, code: "BAD_REQUEST"},
		{name: "null", body: `{"parentAccountId":null}`, user: "owner", status: 400, code: "BAD_REQUEST"},
		{name: "wrong type", body: `{"parentAccountId":123}`, user: "owner", status: 400, code: "BAD_REQUEST"},
		{name: "bad JSON", body: `{`, user: "owner", status: 400, code: "BAD_REQUEST"},
		{name: "self", body: `{"parentAccountId":"child"}`, user: "owner", status: 400, code: "BAD_REQUEST"},
		{name: "non-owner", body: `{"parentAccountId":"parent"}`, user: "other", status: 403, code: "FORBIDDEN"},
		{name: "missing parent", body: `{"parentAccountId":"missing"}`, user: "owner", status: 404, code: "NOT_FOUND"},
		{name: "forbidden parent", body: `{"parentAccountId":"parent"}`, user: "owner", setup: func(r *handlerHierarchyRepo) {
			delete(r.members, acctMemberKey("parent", "owner"))
		}, status: 403, code: "FORBIDDEN"},
		{name: "conflict", body: `{"parentAccountId":"parent"}`, user: "owner", setup: func(r *handlerHierarchyRepo) {
			r.writeErr = fmt.Errorf("stale: %w", repository.ErrConditionFailed)
		}, status: 409, code: "CONFLICT"},
		{name: "storage error", body: `{"parentAccountId":"parent"}`, user: "owner", setup: func(r *handlerHierarchyRepo) {
			r.writeErr = fmt.Errorf("database unavailable")
		}, status: 500, code: "INTERNAL_ERROR"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, repo := newHierarchyHandler()
			if tc.setup != nil {
				tc.setup(repo)
			}
			r := httptest.NewRequest(http.MethodPut, "/api/accounts/child/parent", strings.NewReader(tc.body))
			r = withChiParam(withUserEmailCtx(r, tc.user, ""), "accountId", "child")
			w := httptest.NewRecorder()
			h.UpdateAccountParent(w, r)
			if w.Code != tc.status {
				t.Fatalf("status=%d body=%s, want %d", w.Code, w.Body.String(), tc.status)
			}
			var fields map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &fields); err != nil {
				t.Fatal(err)
			}
			if tc.status == 200 {
				if fields["accountId"] != "child" {
					t.Fatalf("wrong account: %s", w.Body.String())
				}
				parent, present := fields["parentAccountId"]
				if tc.name == "attach" && parent != "parent" || tc.name == "detach" && present {
					t.Fatalf("wrong parent contract: %s", w.Body.String())
				}
			} else {
				detail, ok := fields["error"].(map[string]any)
				if !ok || detail["code"] != tc.code {
					t.Fatalf("wrong error contract: %s", w.Body.String())
				}
			}
		})
	}
}

func TestHandlerAccountHierarchyCreate(t *testing.T) {
	for _, tc := range []struct {
		parent, scenario string
		status           int
	}{
		{"parent", "success", 201}, {"", "root", 201}, {"missing", "missing", 404},
		{"parent", "forbidden", 403}, {"parent", "conflict", 409},
	} {
		t.Run(tc.scenario, func(t *testing.T) {
			h, repo := newHierarchyHandler()
			if tc.scenario == "forbidden" {
				delete(repo.members, acctMemberKey("parent", "owner"))
			}
			if tc.scenario == "conflict" {
				repo.writeErr = repository.ErrConditionFailed
			}
			body := fmt.Sprintf(`{"name":"자회사","parentAccountId":%q}`, tc.parent)
			r := withUserEmailCtx(httptest.NewRequest(http.MethodPost, "/api/accounts", strings.NewReader(body)), "owner", "owner@example.com")
			w := httptest.NewRecorder()
			h.CreateAccount(w, r)
			if w.Code != tc.status {
				t.Fatalf("status=%d body=%s, want %d", w.Code, w.Body.String(), tc.status)
			}
			if tc.status == 201 {
				var fields map[string]any
				if err := json.Unmarshal(w.Body.Bytes(), &fields); err != nil {
					t.Fatal(err)
				}
				parent, present := fields["parentAccountId"]
				if tc.parent == "" && present || tc.parent != "" && parent != tc.parent {
					t.Fatalf("incorrect parent in created response: %s", w.Body.String())
				}
			}
		})
	}
}
