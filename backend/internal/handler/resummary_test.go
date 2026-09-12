package handler

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ttobak/backend/internal/service"
)

type resummaryHandlerStub struct {
	calls         int
	user, meeting string
	err           error
}

func (s *resummaryHandlerStub) Get(_ context.Context, u, m string) (*service.ResummaryResponse, error) {
	s.calls++
	s.user = u
	s.meeting = m
	return &service.ResummaryResponse{Status: "queued", RunID: "run"}, s.err
}
func (s *resummaryHandlerStub) Request(ctx context.Context, u, m string) (*service.ResummaryResponse, error) {
	return s.Get(ctx, u, m)
}
func TestResummaryRequestRejectsCallerSources(t *testing.T) {
	for _, body := range []string{"", `{}`, `{"content":"untrusted"}`, `{"ownerId":"other"}`, `{} {}`, strings.Repeat("x", 2048)} {
		s := &resummaryHandlerStub{}
		h := &ResummaryHandler{service: s}
		r := withChiParam(withUserCtx(httptest.NewRequest("POST", "/", strings.NewReader(body)), "editor"), "meetingId", "meeting")
		w := httptest.NewRecorder()
		h.Request(w, r)
		if body == "" || body == `{}` {
			if w.Code != 202 || s.calls != 1 || s.user != "editor" || s.meeting != "meeting" {
				t.Fatalf("%d %+v", w.Code, s)
			}
		} else if w.Code != 400 || s.calls != 0 {
			t.Fatalf("accepted invalid body %q: %d", body, w.Code)
		}
	}
}
func TestResummaryHTTPFailureCodesAreSafe(t *testing.T) {
	for _, test := range []struct {
		err    error
		status int
	}{{service.ErrForbidden, 403}, {service.ErrResummaryBusy, 409}, {service.ErrResummarySourcePending, 409}, {service.ErrResummaryPublish, 503}, {errors.New("private source text"), 500}} {
		h := &ResummaryHandler{service: &resummaryHandlerStub{err: test.err}}
		w := httptest.NewRecorder()
		h.Get(w, withChiParam(withUserCtx(httptest.NewRequest("GET", "/", nil), "reader"), "meetingId", "meeting"))
		if w.Code != test.status || strings.Contains(w.Body.String(), "private source text") {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
	}
}
