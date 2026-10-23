package middleware

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/ttobak/backend/internal/connector"
)

func TestAuthEnforcesReadClientAfterCryptographicVerification(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	otherKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	oldAllowed := apiAllowedClients
	apiAllowedClients = ParseClientIDs("webclient")
	t.Cleanup(func() { apiAllowedClients = oldAllowed })
	oldPool := cognitoUserPoolID
	cognitoUserPoolID = "syntheticpool"
	defer func() { cognitoUserPoolID = oldPool }()
	jwksCache.Lock()
	oldKeys, oldFetched := jwksCache.keys, jwksCache.fetchedAt
	jwksCache.keys = map[string]*rsa.PublicKey{"synthetic": &key.PublicKey}
	jwksCache.fetchedAt = time.Now()
	jwksCache.Unlock()
	defer func() {
		jwksCache.Lock()
		jwksCache.keys, jwksCache.fetchedAt = oldKeys, oldFetched
		jwksCache.Unlock()
	}()
	policy, err := connector.NewReadOnlyHTTPPolicy("readclient", "https://example.invalid/api/mcp", "webclient")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, use, client, audience, method, path string
		forged, expired                           bool
		want                                      int
	}{
		{"read", "access", "readclient", "https://example.invalid/api/mcp", "GET", "/api/meetings/m/reading", false, false, 204},
		{"direct write", "access", "readclient", "https://example.invalid/api/mcp", "POST", "/api/meetings", false, false, 403},
		{"ID token alternative", "id", "", "readclient", "POST", "/api/meetings", false, false, 403},
		{"wrong resource", "access", "readclient", "https://other.invalid/api/mcp", "GET", "/api/meetings/m/reading", false, false, 403},
		{"forged signature", "access", "readclient", "https://example.invalid/api/mcp", "GET", "/api/meetings/m/reading", true, false, 401},
		{"expired", "access", "readclient", "https://example.invalid/api/mcp", "GET", "/api/meetings/m/reading", false, true, 401},
		{"web unchanged", "id", "", "webclient", "POST", "/api/meetings", false, false, 204},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expires := time.Now().Add(time.Minute).Unix()
			if tc.expired {
				expires = time.Now().Add(-time.Minute).Unix()
			}
			scope := "https://example.invalid/api/mcp/read"
			if tc.audience == "webclient" {
				scope = "openid email profile"
			}
			claims := jwt.MapClaims{
				"iss": "https://cognito-idp." + cognitoRegion + ".amazonaws.com/syntheticpool",
				"sub": "user", "exp": expires, "token_use": tc.use, "client_id": tc.client,
				"aud": tc.audience, "scope": scope, "cognito:groups": []string{"admins"},
			}
			token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
			token.Header["kid"] = "synthetic"
			signingKey := key
			if tc.forged {
				signingKey = otherKey
			}
			signed, err := token.SignedString(signingKey)
			if err != nil {
				t.Fatal(err)
			}
			called := false
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				if GetUserID(r.Context()) != "user" {
					t.Fatal("verified owner lost")
				}
				w.WriteHeader(http.StatusNoContent)
			})
			request := httptest.NewRequest(tc.method, tc.path, nil)
			request.Header.Set("Authorization", "Bearer "+signed)
			response := httptest.NewRecorder()
			authWithConnectorPolicy(next, policy, func(_ context.Context, user, client string) (connector.Grants, error) {
				return connector.Grants{UserID: user, ClientID: client, ClientEnabled: true, ClientMaximum: []string{"read"}}, nil
			}).ServeHTTP(response, request)
			if response.Code != tc.want || called != (tc.want == 204) {
				t.Fatalf("status=%d next=%v", response.Code, called)
			}
			if strings.Contains(response.Body.String(), signed) {
				t.Fatal("token leaked into response")
			}
		})
	}
}

func TestDedicatedReadRequiresCurrentGrantBeforeAndAfterBoundedReader(t *testing.T) {
	oldPool, oldVerify, oldAllowed := cognitoUserPoolID, verifyJWT, apiAllowedClients
	t.Cleanup(func() { cognitoUserPoolID, verifyJWT, apiAllowedClients = oldPool, oldVerify, oldAllowed })
	cognitoUserPoolID = "test"
	apiAllowedClients = ParseClientIDs("webclient")
	policy, err := connector.NewReadOnlyHTTPPolicy("readclient", "https://example.invalid/api/mcp", "webclient")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name                                                                         string
		absent, disabled, wrongUser, lookupError, revokeAfter, expireAfter, overflow bool
		want                                                                         int
	}{
		{name: "bounded current read", want: 200},
		{name: "unwired resolver", absent: true, want: 403},
		{name: "disabled client", disabled: true, want: 403},
		{name: "foreign grant", wrongUser: true, want: 403},
		{name: "lookup failure", lookupError: true, want: 403},
		{name: "disable during read", revokeAfter: true, want: 403},
		{name: "upstream expires during read", expireAfter: true, want: 403},
		{name: "oversized output", overflow: true, want: 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			claims := &ALBOIDCClaims{Sub: "user", TokenUse: "access", ClientID: "readclient", Aud: []string{"https://example.invalid/api/mcp"}, Scope: "https://example.invalid/api/mcp/read", Exp: time.Now().Add(time.Minute).Unix()}
			verifyJWT = func(string) (*ALBOIDCClaims, error) { return claims, nil }
			lookups, calls := 0, 0
			var resolve ConnectorGrantResolver = func(_ context.Context, user, client string) (connector.Grants, error) {
				lookups++
				if tc.lookupError {
					return connector.Grants{}, errors.New("private service detail")
				}
				if tc.wrongUser {
					user = "other"
				}
				return connector.Grants{UserID: user, ClientID: client, ClientEnabled: !tc.disabled && !(tc.revokeAfter && lookups > 1), ClientMaximum: []string{"read"}}, nil
			}
			if tc.absent {
				resolve = nil
			}
			next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls++
				w.Header().Set("X-Private-Reader", "sensitive")
				if tc.expireAfter {
					claims.Exp = time.Now().Unix() - 1
				}
				body := "sensitive result"
				if tc.overflow {
					body = strings.Repeat("x", 14001)
				}
				_, _ = w.Write([]byte(body))
			})
			req := httptest.NewRequest("GET", "/api/meetings/m/reading?cursor=opaque", nil)
			req.Header.Set("Authorization", "Bearer synthetic-verified-token")
			res := httptest.NewRecorder()
			authWithConnectorPolicy(next, policy, resolve).ServeHTTP(res, req)
			if res.Code != tc.want {
				t.Fatalf("status %d want %d", res.Code, tc.want)
			}
			if tc.want == 403 && (strings.Contains(res.Body.String(), "sensitive") || res.Header().Get("X-Private-Reader") != "") {
				t.Fatal("denied bytes leaked")
			}
			if tc.want == 200 && (lookups != 2 || calls != 1 || res.Body.String() != "sensitive result") {
				t.Fatal("read checks missing")
			}
			if (tc.absent || tc.disabled || tc.wrongUser || tc.lookupError) && calls != 0 {
				t.Fatal("denied reader executed")
			}
		})
	}
}

func TestConfiguredConnectorCannotFallThroughFirstPartyOrOIDCPaths(t *testing.T) {
	oldPool, oldVerify, oldAllowed := cognitoUserPoolID, verifyJWT, apiAllowedClients
	t.Cleanup(func() { cognitoUserPoolID, verifyJWT, apiAllowedClients = oldPool, oldVerify, oldAllowed })
	cognitoUserPoolID = "test"
	apiAllowedClients = ParseClientIDs("webclient")
	t.Setenv("CONNECTOR_READ_CLIENT_ID", "readclient")
	t.Setenv("CONNECTOR_RESOURCE_AUDIENCE", "https://example.invalid/api/mcp")
	for _, tc := range []struct {
		name   string
		claims ALBOIDCClaims
		oidc   bool
		want   int
	}{
		{"dedicated token without current grant", ALBOIDCClaims{Sub: "user", TokenUse: "access", ClientID: "readclient", Aud: []string{"https://example.invalid/api/mcp"}, Scope: "https://example.invalid/api/mcp/read"}, false, 403},
		{"mixed client audience", ALBOIDCClaims{Sub: "user", TokenUse: "id", Aud: []string{"webclient", "readclient"}}, false, 403},
		{"OIDC dedicated ID", ALBOIDCClaims{Sub: "user", TokenUse: "id", Aud: []string{"readclient"}}, true, 403},
		{"foreign app", ALBOIDCClaims{Sub: "user", TokenUse: "id", Aud: []string{"foreign"}}, false, 401},
		{"web array preserved", ALBOIDCClaims{Sub: "user", TokenUse: "id", Aud: []string{"webclient"}}, false, 204},
		{"web access preserved", ALBOIDCClaims{Sub: "user", TokenUse: "access", ClientID: "webclient"}, false, 204},
	} {
		t.Run(tc.name, func(t *testing.T) {
			claims := tc.claims
			claims.Exp = time.Now().Add(time.Minute).Unix()
			verifyJWT = func(string) (*ALBOIDCClaims, error) { return &claims, nil }
			req := httptest.NewRequest("GET", "/api/meetings/m/reading", nil)
			if tc.oidc {
				req.Header.Set("x-amzn-oidc-data", "verified")
			} else {
				req.Header.Set("Authorization", "Bearer verified")
			}
			res := httptest.NewRecorder()
			Auth(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })).ServeHTTP(res, req)
			if res.Code != tc.want {
				t.Fatalf("got %d want %d", res.Code, tc.want)
			}
		})
	}
	t.Setenv("CONNECTOR_READ_CLIENT_ID", "webclient")
	req := httptest.NewRequest("GET", "/api/meetings/m/reading", nil)
	res := httptest.NewRecorder()
	Auth(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("overlapping config executed") })).ServeHTTP(res, req)
	if res.Code != 503 {
		t.Fatal("overlapping client configuration was accepted")
	}
}
