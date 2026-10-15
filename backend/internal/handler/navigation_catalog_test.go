package handler

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/service"
)

func TestNavigationHTTPRejectsUnauthenticatedAndMalformedOptionsWithoutRepositoryReads(t *testing.T) {
	handler := NewNavigationCatalogHandler(service.NewNavigationCatalogService(nil), "projects")
	for _, test := range []struct {
		user, query, code string
		status            int
	}{
		{"", "", "UNAUTHORIZED", 401}, {"bad/user", "", "FORBIDDEN", 403},
		{"owner", "pageSize=51", "INVALID_ARGUMENT", 400}, {"owner", "pageSize=1&pageSize=2", "INVALID_ARGUMENT", 400},
		{"owner", "cursor=!!", "INVALID_CURSOR", 400}, {"owner", "cursor=%XX", "INVALID_ARGUMENT", 400},
	} {
		request := httptest.NewRequest("GET", "/api/navigation/projects?"+test.query, nil)
		request = request.WithContext(context.WithValue(request.Context(), middleware.UserIDKey, test.user))
		response := httptest.NewRecorder()
		handler.Get(response, request)
		var result struct{ Error struct{ Code string } }
		if json.Unmarshal(response.Body.Bytes(), &result) != nil || response.Code != test.status || result.Error.Code != test.code || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatal(test, response.Code, response.Body.String())
		}
	}
}
