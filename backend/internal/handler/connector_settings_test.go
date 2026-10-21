package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ttobak/backend/internal/service"
)

func TestConnectorSettingsManagementRejectsClientIdentityAndMissingAdmin(t *testing.T) {
	h := NewConnectorSettingsHandler(service.NewConnectorSettingsService(nil, service.ConnectorSettingsConfig{}))
	for _, body := range []string{
		`{"profile":"write","action":"approve_write","userId":"victim"}`,
		`{"profile":"write","action":"approve_write","clientId":"arbitrary"}`,
		`{"profile":"write","action":"approve_write"}{}`,
		`{"profile":"` + strings.Repeat("a", 1024) + `"}`,
	} {
		w := httptest.NewRecorder()
		h.UpdateApproval(w, httptest.NewRequest(http.MethodPut, "/api/connectors/approval", strings.NewReader(body)))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status %d: %s", w.Code, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	h.UpdatePolicy(w, httptest.NewRequest(http.MethodPut, "/api/settings/connector-policy",
		strings.NewReader(`{"profile":"write","enabled":true,"isAdmin":true}`)))
	if w.Code != http.StatusForbidden {
		t.Fatalf("unverified admin accepted: %d", w.Code)
	}
}
