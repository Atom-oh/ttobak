package connector

import (
	"errors"
	"testing"
)

const ReadScope = "https://example.invalid/api/mcp/read"

func TestReadScopeBelongsToConfiguredResource(t *testing.T) {
	for _, audience := range []string{"https://example.invalid/api/mcp", "https://second.example/api/mcp"} {
		policy, err := NewReadOnlyHTTPPolicy("readclient", audience, "webclient")
		if err != nil {
			t.Fatal(err)
		}
		for _, scopes := range []string{audience + "/read", "openid email profile " + audience + "/read"} {
			dedicated, err := policy.Authorize("access", "readclient", []string{audience}, scopes, "GET", "/api/meetings/meeting/reading")
			if !dedicated || err != nil {
				t.Fatalf("configured resource scope rejected: %v", err)
			}
		}
		for _, scopes := range []string{
			"ttobak.connector/read", "https://foreign.example/api/mcp/read",
			audience + "/read ttobak.connector/write",
			audience + "/read https://foreign.example/api/mcp/write",
			audience + "/read " + audience + "/write",
			audience + "/read " + audience + "/admin",
		} {
			if _, err := policy.Authorize("access", "readclient", []string{audience}, scopes, "GET", "/api/meetings/meeting/reading"); !errors.Is(err, ErrCapabilityDenied) {
				t.Fatalf("unassigned or escalated scopes accepted: %q (%v)", scopes, err)
			}
		}
	}
}

func TestReadOnlyConnectorHTTPPolicy(t *testing.T) {
	policy, err := NewReadOnlyHTTPPolicy("readclient", "https://example.invalid/api/mcp", "webclient")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, use, client, audience, scopes, method, path string
		connector, allowed                                bool
	}{
		{"bounded read", "access", "readclient", "https://example.invalid/api/mcp", ReadScope, "GET", "/api/meetings/meeting/reading", true, true},
		{"web token", "id", "", "webclient", "", "POST", "/api/meetings", false, true},
		{"unknown ID client deferred to existing auth", "id", "", "otherclient", "", "POST", "/api/meetings", false, true},
		{"unknown access client deferred to existing auth", "access", "otherclient", "https://example.invalid/api/mcp", "", "POST", "/api/meetings", false, true},
		{"web client claiming connector scopes", "access", "webclient", "https://example.invalid/api/mcp", ReadScope, "POST", "/api/meetings", true, false},
		{"connector ID token", "id", "", "readclient", "", "POST", "/api/meetings", true, false},
		{"wrong audience", "access", "readclient", "https://other.invalid/api/mcp", ReadScope, "GET", "/api/meetings/meeting/reading", true, false},
		{"identity-only scopes", "access", "readclient", "https://example.invalid/api/mcp", "openid email profile", "GET", "/api/meetings/meeting/reading", true, false},
		{"escalated scope", "access", "readclient", "https://example.invalid/api/mcp", ReadScope + " https://example.invalid/api/mcp/write", "POST", "/api/meetings", true, false},
		{"ordinary create", "access", "readclient", "https://example.invalid/api/mcp", ReadScope, "POST", "/api/meetings", true, false},
		{"share DELETE", "access", "readclient", "https://example.invalid/api/mcp", ReadScope, "DELETE", "/api/meetings/m/share/user", true, false},
		{"implicit mutation GET", "access", "readclient", "https://example.invalid/api/mcp", ReadScope, "GET", "/api/meetings", true, false},
		{"legacy collection not activated", "access", "readclient", "https://example.invalid/api/mcp", ReadScope, "GET", "/api/accounts", true, false},
		{"admin with GET", "access", "readclient", "https://example.invalid/api/mcp", ReadScope, "GET", "/api/settings/users", true, false},
		{"unreviewed route", "access", "readclient", "https://example.invalid/api/mcp", ReadScope, "GET", "/api/future", true, false},
		{"encoded slash", "access", "readclient", "https://example.invalid/api/mcp", ReadScope, "GET", "/api/meetings/m%2Fsecret/reading", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := policy.Authorize(tc.use, tc.client, []string{tc.audience}, tc.scopes, tc.method, tc.path)
			if got != tc.connector || (err == nil) != tc.allowed {
				t.Fatalf("connector=%v error=%v", got, err)
			}
		})
	}
	if op, err := HTTPOperation("DELETE", "/api/projects/p/members/pending"); err != nil ||
		op != "api.project.revokePendingMember" {
		t.Fatalf("literal route lost precedence: %q %v", op, err)
	}
	for _, cfg := range [][2]string{
		{"readclient", ""}, {"", "https://example.invalid/api/mcp"},
		{"readclient", "http://example.invalid/api/mcp"},
		{"readclient", "https://example.invalid/api/mcp?caller=1"},
	} {
		if _, err := NewReadOnlyHTTPPolicy(cfg[0], cfg[1]); !errors.Is(err, ErrInvalidConfig) {
			t.Fatal("partial or unsafe configuration accepted")
		}
	}
}

func TestReadClientCannotHideInAudienceArray(t *testing.T) {
	policy, err := NewReadOnlyHTTPPolicy("readclient", "https://example.invalid/api/mcp", "webclient")
	if err != nil {
		t.Fatal(err)
	}
	for _, audiences := range [][]string{{"webclient", "readclient"}, {"https://example.invalid/api/mcp", "other"}, nil} {
		dedicated, err := policy.Authorize("access", "readclient", audiences, ReadScope, "GET", "/api/meetings/m/reading")
		if !dedicated || !errors.Is(err, ErrCapabilityDenied) {
			t.Fatal("ambiguous audience accepted")
		}
	}
}
