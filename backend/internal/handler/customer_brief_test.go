package handler

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/service"
)

type customerBriefStub struct {
	calls         int
	user, account string
	err           error
	result        *model.CustomerBriefResponse
}

func (s *customerBriefStub) Get(_ context.Context, u, a string) (*model.CustomerBriefResponse, error) {
	s.calls, s.user, s.account = s.calls+1, u, a
	if s.err != nil {
		return nil, s.err
	}
	return s.result, nil
}
func (s *customerBriefStub) Request(ctx context.Context, u, a string) (*model.CustomerBriefResponse, error) {
	return s.Get(ctx, u, a)
}

func TestCustomerBriefRequestAcceptsNoCallerContent(t *testing.T) {
	for _, body := range []string{"", `{}`, `{"accountId":"other"}`, `{"prompt":"x"}`, `{} {}`, `[]`, strings.Repeat(" ", 2048) + "{}"} {
		s := &customerBriefStub{result: &model.CustomerBriefResponse{Run: model.CustomerBriefRun{Status: "queued", RunID: "run"}}}
		r := withChiParam(withUserCtx(httptest.NewRequest("POST", "/", strings.NewReader(body)), "user"), "accountId", "acct")
		w := httptest.NewRecorder()
		NewCustomerBriefHandler(s).Request(w, r)
		if body == "" || body == `{}` {
			if w.Code != 202 || s.calls != 1 || s.user != "user" || s.account != "acct" || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("%q: %d %+v", body, w.Code, s)
			}
			if got := strings.TrimSpace(w.Body.String()); got != `{"run":{"status":"queued","runId":"run"},"brief":null}` {
				t.Fatalf("shape: %s", got)
			}
		} else if w.Code != 400 || s.calls != 0 {
			t.Fatalf("accepted body %q: %d", body, w.Code)
		}
	}
	w := httptest.NewRecorder()
	NewCustomerBriefHandler(&customerBriefStub{}).Get(w, withChiParam(withUserCtx(httptest.NewRequest("GET", "/?x=1", nil), "user"), "accountId", "acct"))
	if w.Code != 400 {
		t.Fatalf("query must be rejected: %d", w.Code)
	}
}

func TestCustomerBriefGetShape(t *testing.T) {
	at := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	s := &customerBriefStub{result: &model.CustomerBriefResponse{
		Run:   model.CustomerBriefRun{Status: "failed", RunID: "run", RequestedAt: &at, ErrorCode: "SOURCE_CHANGED"},
		Brief: &model.CustomerBriefView{GeneratedAt: at, Content: &model.CustomerBrief{}},
	}}
	w := httptest.NewRecorder()
	NewCustomerBriefHandler(s).Get(w, withChiParam(withUserCtx(httptest.NewRequest("GET", "/", nil), "user"), "accountId", "acct"))
	want := `{"run":{"status":"failed","runId":"run","requestedAt":"2026-11-11T09:00:00Z","errorCode":"SOURCE_CHANGED"},` +
		`"brief":{"generatedAt":"2026-11-11T09:00:00Z","content":{"nextMeeting":null,"situation":null,"sinceLast":null,"news":null,"questions":null,"terms":null}}}`
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != want {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

// A non-member and a missing account must be indistinguishable.
func TestCustomerBriefErrorMapping(t *testing.T) {
	for _, call := range []string{"GET", "POST"} {
		bodies := map[error]string{}
		for _, tc := range []struct {
			err    error
			status int
		}{
			{service.ErrForbidden, 404}, {service.ErrNotFound, 404}, {service.ErrInvalidInput, 400},
			{service.ErrCustomerBriefPublish, 503}, {errors.New("private brief text"), 500},
		} {
			h := NewCustomerBriefHandler(&customerBriefStub{err: tc.err})
			w := httptest.NewRecorder()
			r := withChiParam(withUserCtx(httptest.NewRequest(call, "/", nil), "user"), "accountId", "acct")
			if call == "GET" {
				h.Get(w, r)
			} else {
				h.Request(w, r)
			}
			if w.Code != tc.status || strings.Contains(w.Body.String(), "private brief text") {
				t.Fatalf("%s %v: %d %s", call, tc.err, w.Code, w.Body)
			}
			bodies[tc.err] = w.Body.String()
		}
		if bodies[service.ErrForbidden] != bodies[service.ErrNotFound] {
			t.Fatalf("%s: 404 bodies differ: %q %q", call, bodies[service.ErrForbidden], bodies[service.ErrNotFound])
		}
	}
}
