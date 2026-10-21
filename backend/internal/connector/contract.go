// Package connector defines staged connector authorization policy. This contract
// alone does not install token verification, routes, grants or private executors.
package connector

import (
	"context"
	"errors"
	"slices"
	"strings"
)

const (
	Protocol                 = "ttobak.connector"
	SigningAlgorithm         = "RSASSA_PSS_SHA_256"
	MessageType              = "DIGEST"
	MaxWireBytes             = 8192
	MaxPayloadBytes          = 4096
	MaxArgumentBytes         = 512 * 1024
	MaxLifetimeSeconds int64 = 60
	maxSafeInteger     int64 = 9007199254740991
)

var (
	ErrInvalidConfig     = errors.New("INVALID_CONNECTOR_CONFIG")
	ErrInvalidDelegation = errors.New("INVALID_DELEGATION")
	ErrInvalidSignature  = errors.New("INVALID_SIGNATURE")
	ErrUntrustedKey      = errors.New("UNTRUSTED_KEY")
	ErrRequestMismatch   = errors.New("REQUEST_MISMATCH")
	ErrExpired           = errors.New("EXPIRED_DELEGATION")
	ErrInvalidGrant      = errors.New("INVALID_GRANT")
	ErrCapabilityDenied  = errors.New("CAPABILITY_DENIED")
	ErrPolicyUnavailable = errors.New("POLICY_UNAVAILABLE")
)

// Field order is part of the signed wire contract. Every field is ASCII except
// the separately supplied argument bytes. Canonical equality rejects duplicate
// or unknown fields, alternate numbers, whitespace and escaped identifiers.
type payload struct {
	Version            int      `json:"version"`
	Protocol           string   `json:"protocol"`
	Destination        string   `json:"destination"`
	Sub                string   `json:"sub"`
	ClientID           string   `json:"clientId"`
	Capabilities       []string `json:"capabilities"`
	ApprovalGeneration int64    `json:"approvalGeneration"`
	Operation          string   `json:"operation"`
	ArgumentsSHA256    string   `json:"argumentsSHA256"`
	RequestID          string   `json:"requestId"`
	IssuedAt           int64    `json:"issuedAt"`
	ExpiresAt          int64    `json:"expiresAt"`
	UpstreamExpiresAt  int64    `json:"upstreamExpiresAt"`
}

type operationRule struct {
	destination  string
	capabilities string
	allowed      bool
}

func AvailableReadOperation(operation string) bool {
	switch operation {
	case "api.reading.get", "api.documentReading.get", "api.navigationCatalog.accounts",
		"api.navigationCatalog.projects", "api.documentCatalog.get", "api.kb.readFiles",
		"api.accountReading.get", "api.projectReading.get":
		return true
	default:
		return false
	}
}

// RequiredCapabilities returns a copy, never a mutable reference to policy.
func RequiredCapabilities(operation, destination string) ([]string, error) {
	rule, ok := operationRules[operation]
	if !ok || !rule.allowed || rule.destination != destination {
		return nil, ErrCapabilityDenied
	}
	return strings.Split(rule.capabilities, ","), nil
}

type Approval struct {
	UserID       string
	ClientID     string
	Generation   int64
	Capabilities []string
	ExpiresAt    int64
	Revoked      bool
}

// Grants must come from a current trusted server-side lookup, not the envelope.
// The lookup must distinguish a missing/revoked approval from a read-only grant.
type Grants struct {
	UserID        string
	ClientID      string
	ClientEnabled bool
	ClientMaximum []string
	Approval      *Approval
}

type GrantResolver func(context.Context, string, string) (Grants, error)

// Principal is constructed only by a successful verifier. Accessors copy slices.
// Domain ownership, source revisions and queued-work grants still need revalidation.
type Principal struct {
	userID, clientID, operation, requestID string
	capabilities                           []string
	approvalGeneration, expiresAt          int64
}

func (p Principal) UserID() string            { return p.userID }
func (p Principal) ClientID() string          { return p.clientID }
func (p Principal) Operation() string         { return p.operation }
func (p Principal) RequestID() string         { return p.requestID }
func (p Principal) Capabilities() []string    { return slices.Clone(p.capabilities) }
func (p Principal) ApprovalGeneration() int64 { return p.approvalGeneration }
func (p Principal) ExpiresAt() int64          { return p.expiresAt }
func (p Principal) Require(operation, destination string, now int64) error {
	if operation != p.operation || p.userID == "" || now <= 0 || p.expiresAt <= now {
		return ErrCapabilityDenied
	}
	required, err := RequiredCapabilities(operation, destination)
	if err != nil {
		return err
	}
	for _, cap := range required {
		if !slices.Contains(p.capabilities, cap) {
			return ErrCapabilityDenied
		}
	}
	return nil
}

func validCapabilities(caps []string) bool {
	if len(caps) < 1 || len(caps) > 4 || !slices.IsSorted(caps) || !slices.Contains(caps, "read") {
		return false
	}
	for i, cap := range caps {
		if cap != "read" && cap != "write" && cap != "share" && cap != "delete" {
			return false
		}
		if i > 0 && caps[i-1] == cap {
			return false
		}
	}
	return true
}

func effectiveCapabilities(p payload, grants Grants, now int64) ([]string, error) {
	if !grants.ClientEnabled || grants.UserID != p.Sub || grants.ClientID != p.ClientID ||
		!validCapabilities(grants.ClientMaximum) {
		return nil, ErrInvalidGrant
	}
	approved := []string{"read"}
	if p.ApprovalGeneration != 0 {
		a := grants.Approval
		if a == nil || a.Revoked || a.UserID != p.Sub || a.ClientID != p.ClientID ||
			a.Generation != p.ApprovalGeneration || a.ExpiresAt <= now || a.ExpiresAt < p.ExpiresAt ||
			a.ExpiresAt > maxSafeInteger ||
			!validCapabilities(a.Capabilities) {
			return nil, ErrInvalidGrant
		}
		approved = a.Capabilities
	} else if len(p.Capabilities) != 1 || p.Capabilities[0] != "read" {
		return nil, ErrInvalidGrant
	}
	effective := make([]string, 0, len(p.Capabilities))
	for _, cap := range p.Capabilities {
		if slices.Contains(grants.ClientMaximum, cap) && slices.Contains(approved, cap) {
			effective = append(effective, cap)
		}
	}
	// Dropped privileges mean this delegation is stale; never silently downgrade it.
	if len(effective) != len(p.Capabilities) {
		return nil, ErrCapabilityDenied
	}
	return effective, nil
}
