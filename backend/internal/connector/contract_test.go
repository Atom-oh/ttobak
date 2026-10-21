package connector

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"testing"
)

func TestContractInventory(t *testing.T) {
	raw, err := os.ReadFile("../../../contracts/connector/v1/operations.json")
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	if hex.EncodeToString(digest[:]) != ContractSHA256 {
		t.Fatal("stale operation table")
	}
	var inventory struct {
		Operations [][]json.RawMessage `json:"operations"`
	}
	if err := json.Unmarshal(raw, &inventory); err != nil {
		t.Fatal(err)
	}
	for _, row := range inventory.Operations {
		var op, dest string
		var caps []string
		var allowed bool
		if json.Unmarshal(row[0], &op) != nil || json.Unmarshal(row[1], &dest) != nil ||
			json.Unmarshal(row[2], &caps) != nil || json.Unmarshal(row[3], &allowed) != nil {
			t.Fatal("invalid inventory")
		}
		got, err := RequiredCapabilities(op, dest)
		if allowed && (err != nil || !slices.Equal(got, caps)) {
			t.Fatalf("lost policy: %s", op)
		}
		if !allowed && !errors.Is(err, ErrCapabilityDenied) {
			t.Fatalf("allowed blocked operation: %s", op)
		}
	}
	if _, err := RequiredCapabilities("api.unknown", "ttobak-api-bridge"); !errors.Is(err, ErrCapabilityDenied) {
		t.Fatal("unknown operation allowed")
	}
	// DELETE grant revocation is sharing; POST detection is read-only.
	if caps, _ := RequiredCapabilities("api.share.revokeShare", "ttobak-api-bridge"); !slices.Equal(caps, []string{"read", "share"}) {
		t.Fatal("HTTP verbs became policy")
	}
	caps, _ := RequiredCapabilities("api.reading.get", "ttobak-api-bridge")
	caps[0] = "delete"
	if next, _ := RequiredCapabilities("api.reading.get", "ttobak-api-bridge"); next[0] != "read" {
		t.Fatal("caller changed shared policy")
	}
}

func TestCurrentGrantIntersection(t *testing.T) {
	p := payload{Sub: "user", ClientID: "client", Capabilities: []string{"read", "write"}, ApprovalGeneration: 7, ExpiresAt: 150}
	full := []string{"delete", "read", "share", "write"}
	valid := func() Grants {
		return Grants{"user", "client", true, full, &Approval{"user", "client", 7, full, 200, false}}
	}
	for name, edit := range map[string]func(*Grants){
		"client maximum":           func(g *Grants) { g.ClientMaximum = []string{"read"} },
		"revoked approval":         func(g *Grants) { g.Approval.Revoked = true },
		"generation changed":       func(g *Grants) { g.Approval.Generation = 8 },
		"other user":               func(g *Grants) { g.Approval.UserID = "other" },
		"missing approval":         func(g *Grants) { g.Approval = nil },
		"disabled client":          func(g *Grants) { g.ClientEnabled = false },
		"approval expires earlier": func(g *Grants) { g.Approval.ExpiresAt = 149 },
		"unsafe integer expiry":    func(g *Grants) { g.Approval.ExpiresAt = maxSafeInteger + 1 },
	} {
		t.Run(name, func(t *testing.T) {
			g := valid()
			edit(&g)
			if _, err := effectiveCapabilities(p, g, 100); err == nil {
				t.Fatal("invalid grant accepted")
			}
		})
	}
	if caps, err := effectiveCapabilities(p, valid(), 100); err != nil || !slices.Equal(caps, p.Capabilities) {
		t.Fatal("valid intersection rejected")
	}
	p.Capabilities = []string{"read"}
	p.ApprovalGeneration = 0
	if _, err := effectiveCapabilities(p, Grants{"user", "client", true, []string{"read"}, nil}, 100); err != nil {
		t.Fatal(err)
	}
	if err := (Principal{}).Require("api.reading.get", "ttobak-api-bridge", 100); !errors.Is(err, ErrCapabilityDenied) {
		t.Fatal("zero principal allowed")
	}
}
