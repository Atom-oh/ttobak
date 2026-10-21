package middleware

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"time"

	"github.com/ttobak/backend/internal/connector"
)

// ConnectorGrantResolver must perform a current trusted server-side lookup.
// A missing resolver denies every dedicated client, even with a valid token.
type ConnectorGrantResolver func(context.Context, string, string) (connector.Grants, error)

// AuthWithConnectorGrants preserves the existing first-party client allowlist.
// Empty connector configuration is the reader-first deployment default.
func AuthWithConnectorGrants(resolve ConnectorGrantResolver) func(http.Handler) http.Handler {
	clients := make([]string, 0, len(apiAllowedClients))
	for id, allowed := range apiAllowedClients {
		if allowed {
			clients = append(clients, id)
		}
	}
	policy, err := connector.NewReadOnlyHTTPPolicy(os.Getenv("CONNECTOR_READ_CLIENT_ID"), os.Getenv("CONNECTOR_RESOURCE_AUDIENCE"), clients...)
	return func(next http.Handler) http.Handler {
		if err != nil {
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(`{"error":{"code":"AUTH_CONFIGURATION_UNAVAILABLE","message":"Authentication unavailable"}}`))
			})
		}
		return authWithConnectorPolicy(next, policy, resolve)
	}
}

func connectorDenied(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte(`{"error":{"code":"CONNECTOR_ACCESS_DENIED","message":"Connector access denied"}}`))
}

func currentConnectorRead(ctx context.Context, resolve ConnectorGrantResolver, claims *ALBOIDCClaims) bool {
	if resolve == nil || claims.Exp <= time.Now().Unix() {
		return false
	}
	grants, err := resolve(ctx, claims.Sub, claims.ClientID)
	return err == nil && claims.Exp > time.Now().Unix() && connector.AuthorizeReadGrant(grants, claims.Sub, claims.ClientID) == nil
}

const maxConnectorResponseBytes = 14000

type connectorResponse struct {
	headers  http.Header
	body     bytes.Buffer
	status   int
	overflow bool
}

func newConnectorResponse() *connectorResponse   { return &connectorResponse{headers: make(http.Header)} }
func (r *connectorResponse) Header() http.Header { return r.headers }
func (r *connectorResponse) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
}
func (r *connectorResponse) Write(data []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	if r.overflow || len(data) > maxConnectorResponseBytes-r.body.Len() {
		r.overflow = true
		return 0, connector.ErrCapabilityDenied
	}
	return r.body.Write(data)
}
func (r *connectorResponse) publish(w http.ResponseWriter) {
	for key, values := range r.headers {
		w.Header()[key] = append([]string(nil), values...)
	}
	status := r.status
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	_, _ = w.Write(r.body.Bytes())
}
