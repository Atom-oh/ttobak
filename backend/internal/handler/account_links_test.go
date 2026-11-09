package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/service"
)

type accountLinksStub struct {
	err           error
	calls         int
	user, account string
	cursor        string
	in            model.AccountLinkInput
	next          *string
}

func (s *accountLinksStub) Link(_ context.Context, user, account string, in model.AccountLinkInput) (*model.AccountLink, error) {
	s.calls++
	s.user, s.account, s.in = user, account, in
	if s.err != nil {
		return nil, s.err
	}
	return &model.AccountLink{AccountID: account, FromKind: in.FromKind, FromID: in.FromID, ToKind: in.ToKind, ToID: in.ToID,
		Origin: "manual", CreatedBy: user, CreatedAt: "t", ModelID: "secret-model"}, nil
}
func (s *accountLinksStub) Unlink(_ context.Context, user, account string, in model.AccountLinkInput) error {
	s.calls++
	s.user, s.account, s.in = user, account, in
	return s.err
}
func (s *accountLinksStub) List(_ context.Context, user, account, cursor string) ([]model.AccountLink, *string, error) {
	s.calls++
	s.user, s.account, s.cursor = user, account, cursor
	return nil, s.next, s.err
}

func accountLinksRouter(s *accountLinksStub) http.Handler {
	h := NewAccountLinksHandler(s)
	r := chi.NewRouter()
	r.Get("/accounts/{accountId}/links", h.List)
	r.Post("/accounts/{accountId}/links", h.Create)
	r.Delete("/accounts/{accountId}/links", h.Delete)
	return r
}

func serveLinks(s *accountLinksStub, method, target, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), middleware.UserIDKey, "u1"))
	rec := httptest.NewRecorder()
	accountLinksRouter(s).ServeHTTP(rec, req)
	return rec
}

func TestAccountLinksHTTPRoutes(t *testing.T) {
	s := &accountLinksStub{}
	rec := serveLinks(s, http.MethodPost, "/accounts/a1/links", `{"fromKind":"event","fromId":"e1","toKind":"project","toId":"p1"}`)
	if rec.Code != http.StatusCreated || s.user != "u1" || s.account != "a1" || s.in.ToID != "p1" {
		t.Fatalf("create: %d %s %+v", rec.Code, rec.Body, s)
	}
	if strings.Contains(rec.Body.String(), "secret-model") || strings.Contains(rec.Body.String(), "accountId") {
		t.Fatalf("internal fields leaked: %s", rec.Body)
	}
	next := "abc"
	s.next = &next
	rec = serveLinks(s, http.MethodGet, "/accounts/a1/links?cursor=xyz", "")
	var page model.AccountLinksResponse
	if rec.Code != http.StatusOK || s.cursor != "xyz" || json.Unmarshal(rec.Body.Bytes(), &page) != nil || page.Links == nil || page.NextCursor != "abc" {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	rec = serveLinks(s, http.MethodDelete, "/accounts/a1/links?fromKind=news&fromId=s:h&toKind=meeting&toId=o:m", "")
	if rec.Code != http.StatusNoContent || s.in != (model.AccountLinkInput{FromKind: "news", FromID: "s:h", ToKind: "meeting", ToID: "o:m"}) {
		t.Fatalf("delete: %d %+v", rec.Code, s.in)
	}
}

func TestAccountLinksHTTPRejectsMalformedRequests(t *testing.T) {
	for _, tc := range []struct{ method, target, body string }{
		{http.MethodPost, "/accounts/a1/links", `{"fromKind":"event","extra":1}`},
		{http.MethodPost, "/accounts/a1/links", `{"fromKind":"event"} {}`},
		{http.MethodPost, "/accounts/a1/links?x=1", `{}`},
		{http.MethodGet, "/accounts/a1/links?limit=5", ""},
		{http.MethodGet, "/accounts/a1/links?cursor=a&cursor=b", ""},
		{http.MethodDelete, "/accounts/a1/links?fromKind=news&fromId=s:h&toKind=meeting", ""},
		{http.MethodDelete, "/accounts/a1/links?fromKind=news&fromId=s:h&toKind=meeting&toId=o:m&x=1", ""},
	} {
		s := &accountLinksStub{}
		rec := serveLinks(s, tc.method, tc.target, tc.body)
		if rec.Code != http.StatusBadRequest || s.calls != 0 {
			t.Fatalf("%s %s: %d calls=%d", tc.method, tc.target, rec.Code, s.calls)
		}
	}
}

func TestAccountLinksHTTPErrorMapping(t *testing.T) {
	for err, code := range map[error]int{
		service.ErrInvalidInput: http.StatusBadRequest,
		service.ErrForbidden:    http.StatusForbidden,
		service.ErrNotFound:     http.StatusNotFound,
		service.ErrConflict:     http.StatusConflict,
	} {
		s := &accountLinksStub{err: err}
		rec := serveLinks(s, http.MethodDelete, "/accounts/a1/links?fromKind=news&fromId=s:h&toKind=meeting&toId=o:m", "")
		if rec.Code != code || !strings.Contains(rec.Body.String(), `"error"`) {
			t.Fatalf("%v: %d %s", err, rec.Code, rec.Body)
		}
	}
}
