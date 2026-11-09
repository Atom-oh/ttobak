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

type accountKBStub struct {
	err                       error
	calls                     int
	user, account, kind, curs string
	ref                       string
}

func (s *accountKBStub) List(_ context.Context, user, account, kind, cursor string) (*model.AccountKBPage, error) {
	s.calls++
	s.user, s.account, s.kind, s.curs = user, account, kind, cursor
	if s.err != nil {
		return nil, s.err
	}
	return &model.AccountKBPage{Items: []model.AccountKBItem{{Kind: "upload", ID: "r", Title: "a.pdf", IndexState: "INDEXED", CanDownload: true}}}, nil
}

func (s *accountKBStub) DownloadURL(_ context.Context, user, account, ref string) (string, error) {
	s.calls++
	s.user, s.account, s.ref = user, account, ref
	if s.err != nil {
		return "", s.err
	}
	return "https://signed.example/x", nil
}

func (s *accountKBStub) Untag(_ context.Context, user, account, ref string) error {
	s.calls++
	s.user, s.account, s.ref = user, account, ref
	return s.err
}

func serveAccountKB(s *accountKBStub, method, target string) *httptest.ResponseRecorder {
	h := NewAccountKBHandler(s)
	r := chi.NewRouter()
	r.Get("/accounts/{accountId}/kb", h.List)
	r.Get("/accounts/{accountId}/kb/{refId}/download", h.Download)
	r.Delete("/accounts/{accountId}/kb/{refId}", h.Untag)
	req := httptest.NewRequest(method, target, nil)
	req = req.WithContext(context.WithValue(req.Context(), middleware.UserIDKey, "u1"))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestAccountKBHandler_List(t *testing.T) {
	s := &accountKBStub{}
	rec := serveAccountKB(s, http.MethodGet, "/accounts/acc/kb?kind=upload&cursor=c1")
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status %d", rec.Code)
	}
	if s.user != "u1" || s.account != "acc" || s.kind != "upload" || s.curs != "c1" {
		t.Fatalf("args: %+v", s)
	}
	var page model.AccountKBPage
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || len(page.Items) != 1 {
		t.Fatalf("body: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "kb/") || strings.Contains(rec.Body.String(), "s3Key") {
		t.Fatalf("leaked key: %s", rec.Body.String())
	}
	for _, target := range []string{"/accounts/acc/kb", "/accounts/acc/kb?kind=upload&x=1", "/accounts/acc/kb?kind=upload&kind=news"} {
		s := &accountKBStub{}
		if rec := serveAccountKB(s, http.MethodGet, target); rec.Code != http.StatusBadRequest || s.calls != 0 {
			t.Fatalf("%s: %d calls=%d", target, rec.Code, s.calls)
		}
	}
}

func TestAccountKBHandler_DownloadAndUntag(t *testing.T) {
	s := &accountKBStub{}
	rec := serveAccountKB(s, http.MethodGet, "/accounts/acc/kb/ref1/download")
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" || s.ref != "ref1" {
		t.Fatalf("download %d %+v", rec.Code, s)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["url"] != "https://signed.example/x" || len(body) != 1 {
		t.Fatalf("body: %s", rec.Body.String())
	}
	if rec := serveAccountKB(&accountKBStub{}, http.MethodDelete, "/accounts/acc/kb/ref1"); rec.Code != http.StatusNoContent {
		t.Fatalf("untag %d", rec.Code)
	}
	for err, code := range map[error]int{service.ErrForbidden: 403, service.ErrNotFound: 404, service.ErrInvalidInput: 400} {
		if rec := serveAccountKB(&accountKBStub{err: err}, http.MethodGet, "/accounts/acc/kb/ref1/download"); rec.Code != code {
			t.Fatalf("download %v: %d", err, rec.Code)
		}
		if rec := serveAccountKB(&accountKBStub{err: err}, http.MethodDelete, "/accounts/acc/kb/ref1"); rec.Code != code {
			t.Fatalf("untag %v: %d", err, rec.Code)
		}
	}
	if rec := serveAccountKB(&accountKBStub{}, http.MethodGet, "/accounts/acc/kb/ref1/download?x=1"); rec.Code != http.StatusBadRequest {
		t.Fatalf("query on download: %d", rec.Code)
	}
}
