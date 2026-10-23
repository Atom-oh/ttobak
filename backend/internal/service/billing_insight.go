package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/ttobak/backend/internal/model"
)

// ErrBillingInsightDisabled reports that BILLING_INSIGHT_URL is unset or
// invalid, so the hand-off endpoint behaves as if it does not exist.
var ErrBillingInsightDisabled = errors.New("billing insight hand-off disabled")

var errInvalidBillingInsightURL = errors.New("BILLING_INSIGHT_URL must be an https origin without path, query or fragment")

type billingHandoffStore interface {
	PutBillingHandoff(ctx context.Context, codeHash, sub, email string, expiresAt int64) error
}

// ParseBillingInsightOrigin validates the companion dashboard URL. An empty
// value disables the feature without error. A valid value is an https URL
// with a host and nothing beyond an optional trailing "/"; the returned
// origin has no trailing slash.
func ParseBillingInsightOrigin(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || raw != strings.TrimSpace(raw) || u.Scheme != "https" || u.Host == "" ||
		u.Hostname() == "" || u.User != nil || u.Opaque != "" || (u.Path != "" && u.Path != "/") ||
		u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#") {
		return "", errInvalidBillingInsightURL
	}
	return "https://" + u.Host, nil
}

// BillingInsightService mints single-use codes for the Billing Insight
// companion dashboard. Only sha256(code) is persisted.
type BillingInsightService struct {
	store  billingHandoffStore
	origin string
	now    func() time.Time
	random io.Reader
}

// NewBillingInsightService returns a service that is disabled when origin is
// empty. Pass the result of ParseBillingInsightOrigin.
func NewBillingInsightService(store billingHandoffStore, origin string) *BillingInsightService {
	return &BillingInsightService{store: store, origin: origin, now: time.Now, random: rand.Reader}
}

// Enabled reports whether a valid companion origin is configured.
func (s *BillingInsightService) Enabled() bool { return s != nil && s.origin != "" && s.store != nil }

// CreateHandoff stores a hand-off row bound to the caller's verified identity
// and returns the companion redemption URL carrying the plaintext code.
func (s *BillingInsightService) CreateHandoff(ctx context.Context, sub, email string) (string, error) {
	if !s.Enabled() {
		return "", ErrBillingInsightDisabled
	}
	if sub == "" || email == "" {
		return "", ErrForbidden
	}
	raw := make([]byte, model.BillingInsightHandoffCodeBytes)
	if _, err := io.ReadFull(s.random, raw); err != nil {
		return "", fmt.Errorf("generate billing handoff code: %w", err)
	}
	code := base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(code))
	expiresAt := s.now().Unix() + model.BillingInsightHandoffTTLSeconds
	if err := s.store.PutBillingHandoff(ctx, hex.EncodeToString(sum[:]), sub, email, expiresAt); err != nil {
		return "", fmt.Errorf("store billing handoff: %w", err)
	}
	return s.origin + model.BillingInsightLoginPath + "?code=" + code, nil
}
