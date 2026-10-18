package middleware

import "testing"

func TestIssuedToClient(t *testing.T) {
	allowed := map[string]bool{"spa": true}
	cases := []struct {
		name string
		c    ALBOIDCClaims
		want bool
	}{
		{"id token for allowed client", ALBOIDCClaims{TokenUse: "id", Aud: []string{"spa"}}, true},
		{"id token, allowed client among several audiences", ALBOIDCClaims{TokenUse: "id", Aud: []string{"x", "spa"}}, true},
		{"id token for other client", ALBOIDCClaims{TokenUse: "id", Aud: []string{"companion"}}, false},
		{"access token for allowed client", ALBOIDCClaims{TokenUse: "access", ClientID: "spa"}, true},
		{"access token judged by client_id, not aud", ALBOIDCClaims{TokenUse: "access", ClientID: "companion", Aud: []string{"spa"}}, false},
		{"unknown token_use", ALBOIDCClaims{TokenUse: "", Aud: []string{"spa"}, ClientID: "spa"}, false},
	}
	for _, tc := range cases {
		if got := tc.c.IssuedToClient(allowed); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
	if (&ALBOIDCClaims{TokenUse: "id", Aud: []string{"spa"}}).IssuedToClient(nil) {
		t.Error("nil allowlist must deny")
	}
}
