package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/service"
)

type statusAPIStub struct {
	args []string
	err  error
}

func (s *statusAPIStub) Meeting(_ context.Context, user, id string) (*service.IndexStatusResponse, error) {
	s.args = []string{user, id}
	return &service.IndexStatusResponse{State: "INDEXED"}, s.err
}
func (s *statusAPIStub) PersonalDocument(_ context.Context, user, id string) (*service.IndexStatusResponse, error) {
	s.args = []string{user, id}
	return &service.IndexStatusResponse{State: "PENDING"}, s.err
}
func (s *statusAPIStub) AccountDocument(_ context.Context, user, account, id string) (*service.IndexStatusResponse, error) {
	s.args = []string{user, account, id}
	return &service.IndexStatusResponse{State: "WAITING_SOURCE"}, s.err
}

func TestIndexStatusRoutesUseAuthenticatedIdentity(t *testing.T) {
	for _, path := range []string{"/meetings/meeting", "/documents/doc", "/accounts/team/documents/doc"} {
		stub := &statusAPIStub{}
		h := &IndexStatusHandler{service: stub}
		router := chi.NewRouter()
		router.Get("/meetings/{meetingId}", h.Meeting)
		router.Get("/documents/{docId}", h.PersonalDocument)
		router.Get("/accounts/{accountId}/documents/{docId}", h.AccountDocument)
		request := httptest.NewRequest(http.MethodGet, path+"?userId=foreign", nil)
		request = request.WithContext(context.WithValue(request.Context(), middleware.UserIDKey, "viewer"))
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" ||
			len(stub.args) == 0 || stub.args[0] != "viewer" || strings.Contains(response.Body.String(), "foreign") {
			t.Fatalf("%s: %d %s args=%v", path, response.Code, response.Body, stub.args)
		}
	}
}

func TestIndexStatusErrorsRemainTypedAndDoNotLeakProviderDetails(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
	}{
		{service.ErrForbidden, http.StatusForbidden},
		{service.ErrNotFound, http.StatusNotFound},
		{service.ErrInvalidInput, http.StatusBadRequest},
		{errors.New("private/provider/path"), http.StatusServiceUnavailable},
	} {
		response := httptest.NewRecorder()
		writeIndexStatus(response, nil, errors.Join(tc.err, errors.New("private/source")))
		if response.Code != tc.status || response.Header().Get("Cache-Control") != "no-store" ||
			strings.Contains(response.Body.String(), "private") {
			t.Fatalf("%v: %d %s", tc.err, response.Code, response.Body)
		}
	}
}
