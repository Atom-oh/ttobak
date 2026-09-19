package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/service"
)

type sessionProbe struct {
	called          bool
	id, email, name string
	verified        bool
}

func (s *sessionProbe) BootstrapSession(_ context.Context, id, email, name string, verified bool, cursor string) (*service.SessionBootstrapResponse, error) {
	s.called = true
	s.id = id
	s.email = email
	s.name = name
	s.verified = verified
	return &service.SessionBootstrapResponse{EmailVerified: verified}, nil
}
func TestSessionBootstrapUsesOnlyVerifiedMiddlewareIdentity(t *testing.T) {
	p := &sessionProbe{}
	h := NewSessionHandler(p)
	req := httptest.NewRequest(http.MethodPost, "/api/session/bootstrap", strings.NewReader(`{"userId":"victim","email":"victim@example.com","emailVerified":true}`))
	ctx := context.WithValue(req.Context(), middleware.UserIDKey, "caller")
	ctx = context.WithValue(ctx, middleware.UserEmailKey, "caller@example.com")
	ctx = context.WithValue(ctx, middleware.UserNameKey, "Caller")
	ctx = context.WithValue(ctx, middleware.UserEmailVerifiedKey, false)
	w := httptest.NewRecorder()
	h.Bootstrap(w, req.WithContext(ctx))
	if w.Code != 200 || !p.called || p.id != "caller" || p.email != "caller@example.com" || p.verified {
		t.Fatalf("code=%d probe=%+v", w.Code, p)
	}
}
func TestSessionBootstrapRejectsMissingIdentityAndOversizedBody(t *testing.T) {
	for _, test := range []struct {
		name   string
		auth   bool
		body   string
		status int
	}{{"missing identity", false, "", 401}, {"oversized", true, strings.Repeat("x", 1025), 400}} {
		t.Run(test.name, func(t *testing.T) {
			p := &sessionProbe{}
			h := NewSessionHandler(p)
			req := httptest.NewRequest(http.MethodPost, "/api/session/bootstrap", strings.NewReader(test.body))
			if test.auth {
				req = req.WithContext(context.WithValue(req.Context(), middleware.UserIDKey, "caller"))
			}
			w := httptest.NewRecorder()
			h.Bootstrap(w, req)
			if w.Code != test.status || p.called {
				t.Fatalf("code=%d called=%v", w.Code, p.called)
			}
		})
	}
}
