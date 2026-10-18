package middleware

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Companion-app tokens are valid pool tokens but must never act as API
// credentials, on the Bearer path or the legacy x-amzn-oidc-data fallback.
func TestAuthRejectsTokensNotIssuedToFirstPartyClients(t *testing.T) {
	oldPool, oldVerify, oldAllowed := cognitoUserPoolID, verifyJWT, apiAllowedClients
	t.Cleanup(func() { cognitoUserPoolID, verifyJWT, apiAllowedClients = oldPool, oldVerify, oldAllowed })
	cognitoUserPoolID = "ap-northeast-2_test"
	apiAllowedClients = ParseClientIDs("spa")
	tokens := map[string]*ALBOIDCClaims{
		"spa-id":           {Sub: "caller", TokenUse: "id", Aud: []string{"spa"}},
		"spa-access":       {Sub: "caller", TokenUse: "access", ClientID: "spa"},
		"companion-id":     {Sub: "victim", TokenUse: "id", Aud: []string{"companion"}},
		"companion-access": {Sub: "victim", TokenUse: "access", ClientID: "companion"},
	}
	verifyJWT = func(token string) (*ALBOIDCClaims, error) {
		if c, ok := tokens[token]; ok {
			copied := *c
			return &copied, nil
		}
		return nil, errors.New("invalid")
	}
	handler := Auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(r.Context().Value(UserIDKey).(string)))
	}))
	cases := []struct {
		name    string
		headers map[string]string
		status  int
		user    string
	}{
		{"bearer first-party id token", map[string]string{"Authorization": "Bearer spa-id"}, 200, "caller"},
		{"bearer first-party access token", map[string]string{"Authorization": "Bearer spa-access"}, 200, "caller"},
		{"bearer companion id token", map[string]string{"Authorization": "Bearer companion-id"}, 401, ""},
		{"bearer companion access token", map[string]string{"Authorization": "Bearer companion-access"}, 401, ""},
		{"oidc fallback companion token", map[string]string{"x-amzn-oidc-data": "companion-id"}, 401, ""},
		{"bare first-party token plus companion fallback", map[string]string{"Authorization": "spa-id", "x-amzn-oidc-data": "companion-id"}, 401, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/meetings", nil)
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tc.status || (tc.status == 200 && rec.Body.String() != tc.user) {
				t.Fatalf("status=%d body=%q, want %d %q", rec.Code, rec.Body.String(), tc.status, tc.user)
			}
		})
	}
	apiAllowedClients = ParseClientIDs("")
	req := httptest.NewRequest(http.MethodGet, "/api/meetings", nil)
	req.Header.Set("Authorization", "Bearer spa-id")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("unset API_ALLOWED_CLIENT_IDS must fail closed, got %d", rec.Code)
	}
}
