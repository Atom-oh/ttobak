package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/service"
)

// untouchedStore panics on any call: requests rejected by the handler must
// never reach the service's storage.
type untouchedStore struct{ service.DictionaryStore }

func dictRouter() http.Handler {
	h := NewDictionaryHandler(service.NewDictionaryServiceWithStore(untouchedStore{}, nil))
	r := chi.NewRouter()
	r.Post("/api/settings/dictionary/terms", h.CreateTerm)
	r.Patch("/api/settings/dictionary/terms/{termId}", h.UpdateTerm)
	r.Delete("/api/settings/dictionary/terms/{termId}", h.DeleteDictTerm)
	return r
}

func TestDictionaryRequestBounds(t *testing.T) {
	id := strings.Repeat("a", 32)
	cases := []struct {
		name, method, path, body string
		status                   int
	}{
		{"unknown field", "POST", "/api/settings/dictionary/terms", `{"displayAs":"Alpha","origin":"auto"}`, 400},
		{"trailing value", "POST", "/api/settings/dictionary/terms", `{"displayAs":"Alpha"}{}`, 400},
		{"over 4 KB", "POST", "/api/settings/dictionary/terms", `{"displayAs":"` + strings.Repeat("a", 4100) + `"}`, 400},
		{"patch without version", "PATCH", "/api/settings/dictionary/terms/" + id, `{"aliasMode":"always"}`, 400},
		{"patch unknown field", "PATCH", "/api/settings/dictionary/terms/" + id, `{"version":1,"userEditedAt":"x"}`, 400},
		{"bad term id", "PATCH", "/api/settings/dictionary/terms/ABC", `{"version":1}`, 404},
		{"traversal term id", "DELETE", "/api/settings/dictionary/terms/..%2F" + id, ``, 404},
		{"bad delete version", "DELETE", "/api/settings/dictionary/terms/" + id + "?version=0", ``, 400},
	}
	router := dictRouter()
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

func TestDictionaryErrorCodes(t *testing.T) {
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{fmt.Errorf("wrapped: %w", service.ErrDictionaryVersionConflict), 409, "VERSION_CONFLICT"},
		{service.ErrDictionaryFull, 409, "DICTIONARY_FULL"},
		{service.ErrDictionaryTermExists, 409, "TERM_EXISTS"},
		{fmt.Errorf("%w: 2 terms", service.ErrLegacyDictionaryConflict), 409, "LEGACY_DICTIONARY_CONFLICT"},
		{fmt.Errorf("%w", repository.ErrConditionFailed), 409, model.ErrCodeConflict},
		{service.ErrNotFound, 404, model.ErrCodeNotFound},
		{&service.TermInputError{Field: "heardAs", Reason: "must differ from the term itself"}, 400, model.ErrCodeBadRequest},
		{errors.New("storage down"), 500, model.ErrCodeInternalError},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		writeDictionaryError(rec, tc.err, "failed")
		var body struct {
			Error struct{ Code, Message string } `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != tc.status || body.Error.Code != tc.code {
			t.Fatalf("%v: got %d %s", tc.err, rec.Code, rec.Body.String())
		}
	}
}
