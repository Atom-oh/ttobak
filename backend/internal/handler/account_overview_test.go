package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/service"
)

type accountOverviewStub struct {
	err           error
	calls         int
	user, account string
	now           time.Time
}

func (s *accountOverviewStub) Signals(_ context.Context, user, account string, now time.Time) (*model.AccountSignals, error) {
	s.calls++
	s.user, s.account, s.now = user, account, now
	if s.err != nil {
		return nil, s.err
	}
	return &model.AccountSignals{NewsState: model.NewsStateNoSource, News: []model.SignalNews{}, Opportunities: []model.SignalOppty{}, Insights: []model.AccountInsightDTO{}}, nil
}

func (s *accountOverviewStub) Flow(_ context.Context, user, account string, now time.Time) (*model.AccountFlow, error) {
	s.calls++
	s.user, s.account, s.now = user, account, now
	if s.err != nil {
		return nil, s.err
	}
	return &model.AccountFlow{Rows: []model.FlowRow{}, Unassigned: model.FlowRow{Activity: []model.FlowItem{}, Response: []model.FlowItem{}}}, nil
}

func serveOverview(s *accountOverviewStub, target string) *httptest.ResponseRecorder {
	h := NewAccountOverviewHandler(s)
	fixed := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	h.now = func() time.Time { return fixed }
	r := chi.NewRouter()
	r.Get("/accounts/{accountId}/signals", h.Signals)
	r.Get("/accounts/{accountId}/flow", h.Flow)
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req = req.WithContext(context.WithValue(req.Context(), middleware.UserIDKey, "u1"))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestAccountSignalsHandler(t *testing.T) {
	s := &accountOverviewStub{}
	rec := serveOverview(s, "/accounts/acc1/signals")
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("code=%d headers=%v", rec.Code, rec.Header())
	}
	if s.user != "u1" || s.account != "acc1" || !s.now.Equal(time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("stub %+v", s)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["newsState"] != "NO_SOURCE" {
		t.Fatalf("body=%s", rec.Body.String())
	}
	for _, key := range []string{"news", "opportunities", "insights", "restrictedCount"} {
		if _, ok := body[key]; !ok {
			t.Fatalf("missing %s in %s", key, rec.Body.String())
		}
	}
}

func TestAccountSignalsHandlerErrors(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
	}{
		{service.ErrForbidden, http.StatusForbidden},
		{service.ErrInvalidInput, http.StatusBadRequest},
		{service.ErrNotFound, http.StatusNotFound},
		{context.DeadlineExceeded, http.StatusInternalServerError},
	} {
		rec := serveOverview(&accountOverviewStub{err: tc.err}, "/accounts/acc1/signals")
		if rec.Code != tc.status || rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%v: code=%d", tc.err, rec.Code)
		}
	}
	s := &accountOverviewStub{}
	if rec := serveOverview(s, "/accounts/acc1/signals?x=1"); rec.Code != http.StatusBadRequest || s.calls != 0 {
		t.Fatalf("query accepted: code=%d calls=%d", rec.Code, s.calls)
	}
}

func TestAccountFlowHandler(t *testing.T) {
	s := &accountOverviewStub{}
	rec := serveOverview(s, "/accounts/acc1/flow")
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" || s.user != "u1" || s.account != "acc1" {
		t.Fatalf("code=%d stub=%+v", rec.Code, s)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"rows", "unassigned", "truncated"} {
		if _, ok := body[key]; !ok {
			t.Fatalf("missing %s in %s", key, rec.Body.String())
		}
	}
	if rec := serveOverview(&accountOverviewStub{err: service.ErrForbidden}, "/accounts/acc1/flow"); rec.Code != http.StatusForbidden {
		t.Fatalf("forbidden code=%d", rec.Code)
	}
	q := &accountOverviewStub{}
	if rec := serveOverview(q, "/accounts/acc1/flow?x=1"); rec.Code != http.StatusBadRequest || q.calls != 0 {
		t.Fatalf("query accepted: code=%d calls=%d", rec.Code, q.calls)
	}
}
