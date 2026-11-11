package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/service"
)

// sfdcHandlerRepo backs a real sync service; it records writes.
type sfdcHandlerRepo struct {
	member bool
	writes int
}

func (r *sfdcHandlerRepo) GetAccount(context.Context, string) (*model.Account, error) {
	return &model.Account{Name: "Acme"}, nil
}
func (r *sfdcHandlerRepo) GetMember(context.Context, string, string) (*model.AccountMember, error) {
	if !r.member {
		return nil, nil
	}
	return &model.AccountMember{}, nil
}
func (r *sfdcHandlerRepo) GetAccountContact(context.Context, string, string) (*model.AccountContact, error) {
	return nil, nil
}
func (r *sfdcHandlerRepo) ListAccountContacts(context.Context, string, int, string) ([]model.AccountContact, string, error) {
	return nil, "", nil
}
func (r *sfdcHandlerRepo) WriteAccountContact(context.Context, string, string, *model.AccountContact, string) error {
	r.writes++
	return nil
}
func (r *sfdcHandlerRepo) ListSpeakerTeamMembers(context.Context, string, int, string) ([]model.AccountMember, string, error) {
	return nil, "", nil
}
func (r *sfdcHandlerRepo) GetUserByID(context.Context, string) (*model.User, error) { return nil, nil }
func (r *sfdcHandlerRepo) ListSfdcContactLinks(context.Context, string) ([]repository.SfdcContactLink, error) {
	return nil, nil
}

func sfdcRouter(r *sfdcHandlerRepo) http.Handler {
	h := NewAccountContactSyncHandler(service.NewAccountContactSyncService(r, service.NewAccountContactsService(r)))
	router := chi.NewRouter()
	router.Post("/accounts/{accountId}/contacts/sfdc-sync", h.Sync)
	return router
}

func sfdcItems(n int) string {
	items := make([]string, n)
	for i := range items {
		items[i] = fmt.Sprintf(`{"sfdcContactId":"003%015d","sfdcAccountId":"001000000000001AAA","name":"N%d","title":"","action":"create"}`, i, i)
	}
	return `{"snapshotComplete":false,"sfdcAccountIds":["001000000000001AAA"],"items":[` + strings.Join(items, ",") + `]}`
}

func TestSfdcSyncHTTP(t *testing.T) {
	t.Run("creates_and_returns_results", func(t *testing.T) {
		r := &sfdcHandlerRepo{member: true}
		w := contactRequest(sfdcRouter(r), "POST", "/accounts/a/contacts/sfdc-sync", sfdcItems(2))
		var out model.SfdcContactSyncResponse
		if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || json.Unmarshal(w.Body.Bytes(), &out) != nil || len(out.Results) != 2 || out.Results[0].Result != "created" || r.writes != 2 {
			t.Fatalf("%d %s writes=%d", w.Code, w.Body, r.writes)
		}
	})
	t.Run("caps_and_strict_body_rejected", func(t *testing.T) {
		big := `{"snapshotComplete":true,"sfdcAccountIds":["001000000000001AAA"],"present":[` + strings.Repeat(`"003000000000001AAA",`, 26000) + `"003000000000001AAA"],"items":[]}`
		for name, body := range map[string]string{
			"201 items":     sfdcItems(201),
			"unknown field": `{"snapshotComplete":false,"sfdcAccountIds":["001000000000001AAA"],"items":[],"extra":1}`,
			"unknown item":  `{"snapshotComplete":false,"sfdcAccountIds":["001000000000001AAA"],"items":[{"sfdcContactId":"003000000000001AAA","sfdcAccountId":"001000000000001AAA","name":"A","title":"","action":"create","email":"a@example.test"}]}`,
			"bad sfdc id":   strings.Replace(sfdcItems(1), `"003000000000000000"`, `"003#BAD"`, 1),
			"trailing":      sfdcItems(1) + ` {}`,
			"over 512 KiB":  big,
		} {
			r := &sfdcHandlerRepo{member: true}
			w := contactRequest(sfdcRouter(r), "POST", "/accounts/a/contacts/sfdc-sync", body)
			if w.Code != 400 || r.writes != 0 || w.Header().Get("Cache-Control") != "no-store" {
				t.Errorf("%s: %d %s writes=%d", name, w.Code, w.Body, r.writes)
			}
		}
		if len(big) <= 512*1024 {
			t.Fatalf("test body must exceed the cap: %d", len(big))
		}
	})
	t.Run("non_member_forbidden", func(t *testing.T) {
		r := &sfdcHandlerRepo{}
		w := contactRequest(sfdcRouter(r), "POST", "/accounts/a/contacts/sfdc-sync", sfdcItems(1))
		if w.Code != 403 || r.writes != 0 {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
	})
}
