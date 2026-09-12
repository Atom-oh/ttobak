package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/service"
)

type actionHandlerStub struct {
	user, meeting, item string
	completed           bool
	calls               int
	err                 error
}

func (s *actionHandlerStub) Get(_ context.Context, user, meeting string) (*service.ActionItemsResponse, error) {
	s.user, s.meeting, s.calls = user, meeting, s.calls+1
	return &service.ActionItemsResponse{ActionItems: []service.ActionItem{}, Analysis: &model.ActionItemsAnalysis{Status: model.AnalysisQueued}}, s.err
}
func (s *actionHandlerStub) Request(ctx context.Context, user, meeting string) (*service.ActionItemsResponse, error) {
	return s.Get(ctx, user, meeting)
}
func (s *actionHandlerStub) SetCompleted(ctx context.Context, user, meeting, item string, completed bool) (*service.ActionItemsResponse, error) {
	s.item, s.completed = item, completed
	return s.Get(ctx, user, meeting)
}

func TestActionAnalysisCompletionRequiresExplicitBoolean(t *testing.T) {
	for _, body := range []string{`{}`, `{"completed":null}`, `{"completed":"true"}`, `{"completed":false,"extra":1}`, `{"completed":true}{}`, `{"completed":false}`} {
		stub := &actionHandlerStub{}
		h := &ActionItemsHandler{service: stub}
		r := withChiParam(withChiParam(withUserCtx(httptest.NewRequest(http.MethodPut, "/", strings.NewReader(body)), "editor"), "meetingId", "meeting"), "itemId", "task")
		w := httptest.NewRecorder()
		h.SetCompleted(w, r)
		if body == `{"completed":false}` {
			if w.Code != 200 || stub.calls != 1 || stub.user != "editor" || stub.meeting != "meeting" || stub.item != "task" || stub.completed {
				t.Fatalf("explicit false lost: response=%s stub=%+v", w.Body, stub)
			}
		} else if w.Code != 400 || stub.calls != 0 {
			t.Fatalf("invalid body accepted: %s status=%d", body, w.Code)
		}
	}
}

func TestActionAnalysisHTTPErrorContract(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
	}{
		{nil, 202}, {service.ErrForbidden, 403}, {service.ErrNotFound, 404},
		{service.ErrNoAnalysisSource, 409}, {repository.ErrConditionFailed, 409},
		{errors.New("private provider output"), 500},
	} {
		h := &ActionItemsHandler{service: &actionHandlerStub{err: tc.err}}
		w := httptest.NewRecorder()
		h.Retry(w, withChiParam(withUserCtx(httptest.NewRequest(http.MethodPost, "/", nil), "editor"), "meetingId", "meeting"))
		if w.Code != tc.status || strings.Contains(w.Body.String(), "private provider output") {
			t.Fatalf("status=%d body=%s", w.Code, w.Body)
		}
	}
}
