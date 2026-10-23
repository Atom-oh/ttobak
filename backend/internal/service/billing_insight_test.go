package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakeBillingHandoffStore struct {
	hash, sub, email string
	expiresAt        int64
	calls            int
	err              error
}

func (f *fakeBillingHandoffStore) PutBillingHandoff(_ context.Context, hash, sub, email string, expiresAt int64) error {
	f.calls++
	f.hash, f.sub, f.email, f.expiresAt = hash, sub, email, expiresAt
	return f.err
}

func TestParseBillingInsightOrigin(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"", ""},
		{"https://d.example.net", "https://d.example.net"},
		{"https://d.example.net/", "https://d.example.net"},
		{"https://d.example.net:8443", "https://d.example.net:8443"},
	} {
		if got, err := ParseBillingInsightOrigin(tc.raw); err != nil || got != tc.want {
			t.Fatalf("%q => %q, %v", tc.raw, got, err)
		}
	}
	for _, raw := range []string{
		"http://d.example.net", "https://", "https:///", "d.example.net", "https://d.example.net/x",
		"https://d.example.net?", "https://d.example.net/?a=1", "https://d.example.net/#", "https://u@d.example.net",
		" https://d.example.net", "https://d.example.net//", "javascript:alert(1)", "https://:443",
	} {
		if got, err := ParseBillingInsightOrigin(raw); err == nil {
			t.Fatalf("%q accepted as %q", raw, got)
		}
	}
}

func TestCreateHandoffDeterministic(t *testing.T) {
	store := &fakeBillingHandoffStore{}
	s := NewBillingInsightService(store, "https://d.example.net")
	s.now = func() time.Time { return time.Unix(1000, 0) }
	s.random = bytes.NewReader(make([]byte, 32))
	got, err := s.CreateHandoff(context.Background(), "sub-1", "a@example.com")
	if err != nil {
		t.Fatal(err)
	}
	code := strings.Repeat("A", 43)
	if got != "https://d.example.net/login/ttobak?code="+code {
		t.Fatalf("url = %q", got)
	}
	sum := sha256.Sum256([]byte(code))
	if store.hash != hex.EncodeToString(sum[:]) || store.sub != "sub-1" || store.email != "a@example.com" || store.expiresAt != 1060 {
		t.Fatalf("store = %+v", store)
	}
}

func TestCreateHandoffFailures(t *testing.T) {
	if _, err := NewBillingInsightService(&fakeBillingHandoffStore{}, "").CreateHandoff(context.Background(), "s", "e"); !errors.Is(err, ErrBillingInsightDisabled) {
		t.Fatalf("disabled err = %v", err)
	}
	store := &fakeBillingHandoffStore{}
	s := NewBillingInsightService(store, "https://d.example.net")
	if _, err := s.CreateHandoff(context.Background(), "s", ""); !errors.Is(err, ErrForbidden) || store.calls != 0 {
		t.Fatalf("missing email err = %v calls = %d", err, store.calls)
	}
	s.random = bytes.NewReader(make([]byte, 5))
	if _, err := s.CreateHandoff(context.Background(), "s", "e"); err == nil || store.calls != 0 {
		t.Fatalf("short random err = %v calls = %d", err, store.calls)
	}
	store.err = errors.New("boom")
	s.random = bytes.NewReader(make([]byte, 32))
	if got, err := s.CreateHandoff(context.Background(), "s", "e"); err == nil || got != "" {
		t.Fatalf("store failure got %q, %v", got, err)
	}
}
