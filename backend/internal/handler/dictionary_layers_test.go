package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/service"
)

func layersRouter(isMember bool) http.Handler {
	svc := service.NewDictionaryServiceWithStore(untouchedStore{}, nil)
	svc.SetMembershipReader(func(context.Context, string, string) (bool, error) { return isMember, nil })
	h := NewDictionaryHandler(svc)
	r := chi.NewRouter()
	r.Get("/api/settings/dictionary/layers/{layer}/terms", h.ListLayerTerms)
	r.Post("/api/settings/dictionary/layers/{layer}/terms", h.CreateLayerTerm)
	r.Patch("/api/settings/dictionary/layers/{layer}/terms/{termId}", h.UpdateLayerTerm)
	r.Delete("/api/settings/dictionary/layers/{layer}/terms/{termId}", h.DeleteLayerTerm)
	r.Get("/api/settings/dictionary/layers/{layer}/terms/{termId}/log", h.ListTermLog)
	r.Post("/api/settings/dictionary/layers/{layer}/terms/{termId}/log/{logId}/restore", h.RestoreTerm)
	return r
}

func TestLayersForeignAccountForbidden(t *testing.T) {
	id := strings.Repeat("a", 32)
	base := "/api/settings/dictionary/layers/"
	cases := []struct {
		name, method, path, body string
		status                   int
	}{
		{"list foreign account", "GET", base + "account:acc9/terms", ``, 403},
		{"create foreign account", "POST", base + "account:acc9/terms", `{"displayAs":"AlphaGrid"}`, 403},
		{"patch foreign account", "PATCH", base + "account:acc9/terms/" + id, `{"version":1,"aliasMode":"always"}`, 403},
		{"delete foreign account", "DELETE", base + "account:acc9/terms/" + id, ``, 403},
		{"deleted on account", "GET", base + "account:acc9/terms?deleted=1", ``, 400},
		{"deleted on personal", "GET", base + "personal/terms?deleted=1", ``, 400},
		{"deleted bad value", "GET", base + "common/terms?deleted=true", ``, 400},
		{"unknown layer", "GET", base + "team:acc9/terms", ``, 400},
		{"bad term id", "PATCH", base + "common/terms/XYZ", `{"version":1}`, 404},
		{"patch without version", "PATCH", base + "common/terms/" + id, `{"aliasMode":"always"}`, 400},
		{"bad log id", "POST", base + "common/terms/" + id + "/log/..%2Fx/restore", ``, 404},
		{"personal log", "GET", base + "personal/terms/" + id + "/log", ``, 404},
		{"account restore", "POST", base + "account:acc9/terms/" + id + "/log/2026-10-26T09:00:00.000000Z/restore", ``, 404},
	}
	router := layersRouter(false)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)))
			if rec.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.status, rec.Body.String())
			}
		})
	}
}

func TestLayersErrorCodes(t *testing.T) {
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{service.ErrForbidden, 403, model.ErrCodeForbidden},
		{repository.ErrNotAccountMember, 403, model.ErrCodeForbidden},
		{repository.ErrInvalidInput, 400, model.ErrCodeBadRequest},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		writeDictionaryError(rec, tc.err, "failed")
		if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.code) {
			t.Fatalf("%v: got %d %s", tc.err, rec.Code, rec.Body.String())
		}
	}
}
