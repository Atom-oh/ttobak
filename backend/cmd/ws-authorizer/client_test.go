package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/ttobak/backend/internal/middleware"
)

const testFirstPartyClient = "first-party-client"

func TestMain(m *testing.M) {
	allowedClients = parseClientList(" " + testFirstPartyClient + " ,,")
	os.Exit(m.Run())
}

// firstPartyClaims is a verified ID token issued to an allowed client.
func firstPartyClaims() *middleware.ALBOIDCClaims {
	return &middleware.ALBOIDCClaims{Sub: "synthetic-user", TokenUse: "id", Aud: []string{testFirstPartyClient}}
}

func TestConnectAllowsOnlyFirstPartyClients(t *testing.T) {
	fixture := &secretFixture{value: strings.Repeat("b", 64)}
	origin := newSecretFixture(t, fixture)
	oldPool, oldToken, oldOrigin := cognitoUserPoolID, verifyToken, cloudFrontOrigin
	t.Cleanup(func() { cognitoUserPoolID, verifyToken, cloudFrontOrigin = oldPool, oldToken, oldOrigin })
	cognitoUserPoolID, cloudFrontOrigin = "test-pool", origin
	cases := []struct {
		name   string
		claims middleware.ALBOIDCClaims
		allow  bool
	}{
		{"first-party id token", middleware.ALBOIDCClaims{Sub: "u", TokenUse: "id", Aud: []string{testFirstPartyClient}}, true},
		{"first-party access token", middleware.ALBOIDCClaims{Sub: "u", TokenUse: "access", ClientID: testFirstPartyClient}, true},
		{"companion id token", middleware.ALBOIDCClaims{Sub: "u", TokenUse: "id", Aud: []string{"companion-client"}}, false},
		{"companion access token", middleware.ALBOIDCClaims{Sub: "u", TokenUse: "access", ClientID: "companion-client"}, false},
		{"access token with first-party aud only", middleware.ALBOIDCClaims{Sub: "u", TokenUse: "access", Aud: []string{testFirstPartyClient}, ClientID: "companion-client"}, false},
		{"missing token_use", middleware.ALBOIDCClaims{Sub: "u", Aud: []string{testFirstPartyClient}}, false},
		{"no audience", middleware.ALBOIDCClaims{Sub: "u", TokenUse: "id"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			claims := tc.claims
			verifyToken = func(string) (*middleware.ALBOIDCClaims, error) { return &claims, nil }
			response, err := handler(context.Background(), proxyEvent(fixture.value))
			if err != nil {
				t.Fatal(err)
			}
			if got := response.PolicyDocument.Statement[0].Effect == "Allow"; got != tc.allow {
				t.Fatalf("allow=%v, want %v: %+v", got, tc.allow, response.PolicyDocument)
			}
		})
	}
}

func TestConnectDeniesEveryClientWhenAllowlistIsEmpty(t *testing.T) {
	fixture := &secretFixture{value: strings.Repeat("c", 64)}
	origin := newSecretFixture(t, fixture)
	oldPool, oldToken, oldOrigin, oldAllowed := cognitoUserPoolID, verifyToken, cloudFrontOrigin, allowedClients
	t.Cleanup(func() {
		cognitoUserPoolID, verifyToken, cloudFrontOrigin, allowedClients = oldPool, oldToken, oldOrigin, oldAllowed
	})
	cognitoUserPoolID, cloudFrontOrigin, allowedClients = "test-pool", origin, parseClientList("")
	verifyToken = func(string) (*middleware.ALBOIDCClaims, error) { return firstPartyClaims(), nil }
	response, _ := handler(context.Background(), proxyEvent(fixture.value))
	if response.PolicyDocument.Statement[0].Effect != "Deny" {
		t.Fatal("unset WS_ALLOWED_CLIENT_IDS must fail closed")
	}
}
