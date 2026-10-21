package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"

	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/service"
)

type settingsConfigRepo struct {
	client string
	block  bool
}

func (r *settingsConfigRepo) ConnectorSettings(_ context.Context, _, client, _, _ string) (model.ConnectorSettings, error) {
	r.client = client
	return model.ConnectorSettings{ClientID: client, Enabled: true, PolicyGeneration: 1}, nil
}
func (r *settingsConfigRepo) UpdateConnectorApprovalBound(_ context.Context, in repository.ConnectorApprovalUpdate, _, _ string, _ int64) (int64, error) {
	r.client, r.block = in.ClientID, in.Block
	return 1, nil
}
func (*settingsConfigRepo) SetConnectorPolicy(context.Context, string, string, string, string, bool, int64, []string) (int64, error) {
	panic("unexpected administrator operation")
}

func TestConnectorSettingsAndIngressUseSameConfiguredReadClient(t *testing.T) {
	if os.Getenv("CONNECTOR_SETTINGS_CONFIG_TEST") != "1" {
		// The existing first-party allowlist is intentionally frozen at package
		// initialization. Exercise production initialization in a fresh process.
		t.Setenv("CONNECTOR_SETTINGS_CONFIG_TEST", "1")
		t.Setenv("API_ALLOWED_CLIENT_IDS", "firstpartyclient")
		command := exec.Command(os.Args[0], "-test.run=^TestConnectorSettingsAndIngressUseSameConfiguredReadClient$")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("configured subprocess: %v\n%s", err, output)
		}
		return
	}
	t.Setenv("CONNECTOR_READ_CLIENT_ID", "dedicatedreadclient")
	t.Setenv("CONNECTOR_WRITE_CLIENT_ID", "")
	t.Setenv("CONNECTOR_RESOURCE_AUDIENCE", "https://app.invalid/api/mcp")
	// Legacy AWS_DEFAULT_REGION or SDK resolution must not produce a different
	// issuer in management from the API's existing grant resolver.
	t.Setenv("AWS_REGION", "")
	config := connectorSettingsConfig("ap-northeast-2", "pool")
	repo := &settingsConfigRepo{}
	settings := service.NewConnectorSettingsService(repo, config)
	rows, err := settings.List(context.Background(), "user")
	if err != nil || len(rows) != 1 || rows[0].ClientID != "dedicatedreadclient" ||
		config.Issuer != "https://cognito-idp.ap-northeast-2.amazonaws.com/pool" {
		t.Fatalf("active client missing from settings: %+v %v", rows, err)
	}
	if _, err := settings.UpdateApproval(context.Background(), "user", service.ConnectorApprovalRequest{Profile: "read", Action: "block"}); err != nil || !repo.block || repo.client != "dedicatedreadclient" {
		t.Fatalf("cannot block the ingress client: %v", err)
	}
	response := httptest.NewRecorder()
	middleware.AuthWithConnectorGrants(nil)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("unauthenticated request reached the handler")
	})).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/connectors", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("valid shared configuration broke ingress: %d", response.Code)
	}
}
