package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/service"
)

type eventsAPIStub struct {
	err    error
	list   service.EventListOptions
	user   string
	op, id string
	input  service.AccountEventInput
	calls  int
}

func (s *eventsAPIStub) List(_ context.Context, _, user string, o service.EventListOptions) (*model.AccountEventsPage, error) {
	s.calls++
	s.user, s.list = user, o
	return &model.AccountEventsPage{Events: []model.AccountEvent{}}, s.err
}
func (s *eventsAPIStub) Get(_ context.Context, _, user, id string) (*model.AccountEvent, error) {
	s.calls++
	s.user, s.id = user, id
	return &model.AccountEvent{EventID: id}, s.err
}
func (s *eventsAPIStub) Mutate(_ context.Context, _, user, op, id string, in service.AccountEventInput) (*model.AccountEvent, error) {
	s.calls++
	s.user, s.op, s.id, s.input = user, op, id, in
	return &model.AccountEvent{EventID: "e"}, s.err
}

func eventRouter(s *eventsAPIStub) http.Handler {
	h := NewAccountEventsHandler(s)
	r := chi.NewRouter()
	r.Get("/accounts/{accountId}/events", h.List)
	r.Post("/accounts/{accountId}/events", h.Create)
	r.Get("/accounts/{accountId}/events/{eventId}", h.Get)
	r.Put("/accounts/{accountId}/events/{eventId}", h.Update)
	r.Delete("/accounts/{accountId}/events/{eventId}", h.Delete)
	return r
}

func TestEventHTTPRoutesAndIdentity(t *testing.T) {
	s := &eventsAPIStub{}
	h := eventRouter(s)
	if w := contactRequest(h, "GET", "/accounts/a/events?from=2026-11-01T00:00:00Z&order=desc&limit=5", ""); w.Code != 200 || s.user != "viewer" || !s.list.Descending || s.list.Limit != 5 || s.list.From != "2026-11-01T00:00:00Z" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("list: %d %+v", w.Code, s.list)
	}
	if w := contactRequest(h, "POST", "/accounts/a/events", `{"title":"t","startAt":"2026-11-10T00:00:00Z","attendees":[{"name":"n"}]}`); w.Code != 201 || s.op != "create" || s.input.Title != "t" {
		t.Fatalf("create: %d", w.Code)
	}
	if w := contactRequest(h, "PUT", "/accounts/a/events/e1", `{"revision":"r","title":"t","startAt":"2026-11-10T00:00:00Z"}`); w.Code != 200 || s.op != "update" || s.id != "e1" || s.input.Revision != "r" {
		t.Fatalf("update: %d", w.Code)
	}
	if w := contactRequest(h, "DELETE", "/accounts/a/events/e1?revision=r", ""); w.Code != 204 || s.op != "delete" || s.input.Revision != "r" {
		t.Fatalf("delete: %d", w.Code)
	}
	if w := contactRequest(h, "GET", "/accounts/a/events/e1", ""); w.Code != 200 || s.id != "e1" {
		t.Fatalf("get: %d", w.Code)
	}
}

func TestEventHTTPRejectsMalformedInputBeforeTheService(t *testing.T) {
	for _, tc := range [][3]string{
		{"GET", "/accounts/a/events?limit=0", ""},
		{"GET", "/accounts/a/events?limit=26", ""},
		{"GET", "/accounts/a/events?order=sideways", ""},
		{"GET", "/accounts/a/events?from=a&from=b", ""},
		{"GET", "/accounts/a/events?unknown=1", ""},
		{"POST", "/accounts/a/events", `{"title":"t","startAt":"x","createdBy":"someone-else"}`},
		{"POST", "/accounts/a/events", `{"title":"t"}{"title":"u"}`},
		{"DELETE", "/accounts/a/events/e1", ""},
		{"DELETE", "/accounts/a/events/e1?revision=r&x=1", ""},
		{"GET", "/accounts/a/events/e1?x=1", ""},
	} {
		s := &eventsAPIStub{}
		if w := contactRequest(eventRouter(s), tc[0], tc[1], tc[2]); w.Code != 400 || s.calls != 0 {
			t.Fatalf("%v: %d calls=%d", tc, w.Code, s.calls)
		}
	}
}

func TestEventHTTPErrorsAreGeneric(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
	}{{service.ErrInvalidInput, 400}, {service.ErrForbidden, 403}, {service.ErrNotFound, 404}, {service.ErrEventConflict, 409}, {fmt.Errorf("open event: %w", service.ErrEventsUnavailable), 503}, {errors.New("private provider details"), 500}} {
		w := contactRequest(eventRouter(&eventsAPIStub{err: tc.err}), "GET", "/accounts/a/events", "")
		if w.Code != tc.status || strings.Contains(w.Body.String(), "private") {
			t.Fatalf("status: %d %s", w.Code, w.Body)
		}
	}
}
