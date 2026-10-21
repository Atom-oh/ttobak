package connectorinventory

import (
	"encoding/json"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// This test checks the source inventory before any generated consumer exists.
// Listed eligibility does not activate a route or grant a caller access.
func TestInventoryCoversCurrentHTTPRoutes(t *testing.T) {
	raw, err := os.ReadFile("../../../contracts/connector/v1/operations.json")
	if err != nil {
		t.Fatal(err)
	}
	var inventory struct {
		Version    int                 `json:"version"`
		Columns    []string            `json:"columns"`
		Operations [][]json.RawMessage `json:"operations"`
	}
	if err := json.Unmarshal(raw, &inventory); err != nil {
		t.Fatal(err)
	}
	if inventory.Version != 1 || !slices.Equal(inventory.Columns,
		[]string{"operation", "destination", "capabilities", "delegationAllowed", "method", "route"}) {
		t.Fatal("unexpected inventory schema")
	}
	bindings := map[string]string{}
	policies := map[string]string{}
	for i, row := range inventory.Operations {
		if len(row) != 6 {
			t.Fatalf("row %d: expected six columns", i)
		}
		var operation, destination, method, route string
		var capabilities []string
		var allowed bool
		if json.Unmarshal(row[0], &operation) != nil || json.Unmarshal(row[1], &destination) != nil ||
			json.Unmarshal(row[2], &capabilities) != nil || json.Unmarshal(row[3], &allowed) != nil ||
			json.Unmarshal(row[4], &method) != nil || json.Unmarshal(row[5], &route) != nil ||
			string(row[3]) == "null" {
			t.Fatalf("row %d: invalid column type", i)
		}
		if operation == "" || route == "" ||
			!slices.Contains([]string{"ttobak-api-bridge", "ttobak-qa-bridge"}, destination) ||
			!slices.Contains([]string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS", "CONNECT", "TRACE", "TOOL", "INTERNAL"}, method) {
			t.Fatalf("row %d: invalid operation binding", i)
		}
		if !slices.Contains(capabilities, "read") || !slices.IsSorted(capabilities) {
			t.Fatalf("row %d: capabilities must be sorted and include read", i)
		}
		for j, capability := range capabilities {
			if !slices.Contains([]string{"delete", "read", "share", "write"}, capability) ||
				(j > 0 && capability == capabilities[j-1]) {
				t.Fatalf("row %d: invalid or duplicate capability", i)
			}
		}
		policy := destination + "|" + strings.Join(capabilities, ",") + "|" + string(row[3])
		if prior, exists := policies[operation]; exists && prior != policy {
			t.Fatalf("inconsistent policy for %s", operation)
		}
		policies[operation] = policy
		if destination == "ttobak-api-bridge" {
			binding := method + " " + route
			if _, exists := bindings[binding]; exists {
				t.Fatalf("duplicate HTTP binding: %s", binding)
			}
			bindings[binding] = operation
		}
	}
	main, err := os.ReadFile("../../cmd/api/main.go")
	if err != nil {
		t.Fatal(err)
	}
	routes := regexp.MustCompile(`\b\w+\.(Get|Post|Put|Patch|Delete|Head|Options|Connect|Trace)\("([^"]+)"`).FindAllSubmatch(main, -1)
	if len(routes) == 0 {
		t.Fatal("no HTTP routes inspected")
	}
	for _, route := range routes {
		binding := strings.ToUpper(string(route[1])) + " " + string(route[2])
		if bindings[binding] == "" {
			t.Errorf("HTTP route missing explicit policy: %s", binding)
		}
	}
}
