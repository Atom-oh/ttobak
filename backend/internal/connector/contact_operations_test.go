package connector

import "testing"

func TestContactOperationPolicy(t *testing.T) {
	for _, tc := range []struct{ method, operation string }{{"GET", "api.accountContacts.list"}, {"POST", "api.accountContacts.create"}} {
		op, err := HTTPOperation(tc.method, "/api/accounts/account-1/contacts")
		if err != nil || op != tc.operation {
			t.Fatalf("%s contacts: got %q, %v", tc.method, op, err)
		}
		caps, err := RequiredCapabilities(op, "ttobak-api-bridge")
		if err != nil || len(caps) == 0 || caps[0] != "read" || (tc.method == "POST" && (len(caps) != 2 || caps[1] != "write")) {
			t.Fatalf("%s capabilities: %v, %v", tc.method, caps, err)
		}
	}
	if !AvailableReadOperation("api.accountContacts.list") {
		t.Fatal("contact list unavailable")
	}
}

func TestContactOperationsDoNotDelegateOtherRoutes(t *testing.T) {
	for _, tc := range []struct{ method, path, operation string }{
		{"PUT", "/api/accounts/acct/contacts/id", "api.accountContacts.update"},
		{"DELETE", "/api/accounts/acct/contacts/id", "api.accountContacts.delete"},
		{"GET", "/api/accounts/acct/speaker-team", "api.accountContacts.speakerTeam"},
	} {
		op, err := HTTPOperation(tc.method, tc.path)
		if err != nil || op != tc.operation {
			t.Fatalf("route: %q %v", op, err)
		}
		if _, err = RequiredCapabilities(op, "ttobak-api-bridge"); err == nil {
			t.Fatalf("unexpected delegation: %s", op)
		}
	}
}
