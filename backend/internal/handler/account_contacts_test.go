package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/service"
)

type contactsAPIStub struct {
	user  string
	limit int
	err   error
	calls int
}

func (s *contactsAPIStub) List(_ context.Context, _, user string, limit int, _ string) (*model.AccountContactsPage, error) {
	s.user = user
	s.limit = limit
	s.calls++
	return &model.AccountContactsPage{Contacts: []model.AccountContact{}}, s.err
}
func (s *contactsAPIStub) Mutate(_ context.Context, _, user, op, id string, c model.AccountContact) (*model.AccountContact, error) {
	s.user = user
	s.calls++
	return &c, s.err
}
func (s *contactsAPIStub) SpeakerTeamPage(_ context.Context, _, user string, _ int, _ string) (*model.SpeakerTeam, error) {
	s.user = user
	s.calls++
	return &model.SpeakerTeam{Members: []model.SpeakerTeamMember{}}, s.err
}
func contactRouter(s *contactsAPIStub) http.Handler {
	h := NewAccountContactsHandler(s)
	r := chi.NewRouter()
	r.Get("/accounts/{accountId}/contacts", h.List)
	r.Post("/accounts/{accountId}/contacts", h.Create)
	r.Put("/accounts/{accountId}/contacts/{contactId}", h.Update)
	r.Delete("/accounts/{accountId}/contacts/{contactId}", h.Delete)
	r.Get("/accounts/{accountId}/speaker-team", h.SpeakerTeam)
	return r
}
func contactRequest(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r = r.WithContext(context.WithValue(r.Context(), middleware.UserIDKey, "viewer"))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestContactHTTPValidationAndIdentity(t *testing.T) {
	s := &contactsAPIStub{}
	h := contactRouter(s)
	for _, tc := range []struct {
		method, path, body string
		status             int
	}{{"GET", "/accounts/a/contacts", "", 200}, {"GET", "/accounts/a/contacts?limit=1", "", 200}, {"GET", "/accounts/a/speaker-team", "", 200}, {"POST", "/accounts/a/contacts", `{"name":"A"}`, 201}, {"GET", "/accounts/a/contacts?limit=26", "", 400}, {"GET", "/accounts/a/contacts?limit=x", "", 400}, {"POST", "/accounts/a/contacts", `{"name":"A","unknown":"x"}`, 400}, {"POST", "/accounts/a/contacts", `{"name":"A"} {}`, 400}, {"POST", "/accounts/a/contacts", strings.Repeat("x", 65537), 400}} {
		w := contactRequest(h, tc.method, tc.path, tc.body)
		if w.Code != tc.status {
			t.Fatalf("%s %s: %d %s", tc.method, tc.path, w.Code, w.Body)
		}
	}
	if s.user != "viewer" {
		t.Fatalf("identity: %q", s.user)
	}
}
func TestContactRejectsMalformedCursorQuery(t *testing.T) {
	s := &contactsAPIStub{}
	w := contactRequest(contactRouter(s), "GET", "/accounts/a/contacts?cursor=%ZZ", "")
	if w.Code != http.StatusBadRequest || s.calls != 0 {
		t.Fatalf("malformed cursor accepted: %d calls %d", w.Code, s.calls)
	}
}

func TestContactHTTPErrorsAreGeneric(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
	}{{service.ErrInvalidInput, 400}, {service.ErrForbidden, 403}, {service.ErrNotFound, 404}, {service.ErrContactConflict, 409}, {fmt.Errorf("open contact: %w", service.ErrContactsUnavailable), 503}, {errors.New("private provider details"), 500}} {
		s := &contactsAPIStub{err: tc.err}
		w := contactRequest(contactRouter(s), "GET", "/accounts/a/contacts", "")
		if w.Code != tc.status || strings.Contains(w.Body.String(), "private") {
			t.Fatalf("status: %d %s", w.Code, w.Body)
		}
	}
}

func TestContactDeleteRequiresSingleRevision(t *testing.T) {
	s := &contactsAPIStub{}
	for _, path := range []string{"/accounts/a/contacts/id", "/accounts/a/contacts/id?revision=a&revision=b", "/accounts/a/contacts/id?revision=a&other=b"} {
		w := contactRequest(contactRouter(s), "DELETE", path, "")
		if w.Code != 400 || s.calls != 0 {
			t.Fatalf("unexpected %s: %d", path, w.Code)
		}
	}
}

func TestContactJSONContainsExactPublicFields(t *testing.T) {
	data, err := json.Marshal(model.AccountContact{PK: "secret", SK: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	var row map[string]interface{}
	if err = json.Unmarshal(data, &row); err != nil {
		t.Fatal(err)
	}
	keys := []string{"contactId", "revision", "name", "company", "title", "department", "email", "address", "workPhone", "fax", "phone", "cardRegisteredAt", "cardBook", "group", "notes"}
	if len(row) != len(keys) {
		t.Fatalf("unexpected JSON fields: %s", data)
	}
	for _, key := range keys {
		if _, ok := row[key]; !ok {
			t.Fatalf("missing %s", key)
		}
	}
}
