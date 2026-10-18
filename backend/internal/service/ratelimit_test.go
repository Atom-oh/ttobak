package service

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeRateStore struct {
	calls     int
	userID    string
	sortKey   string
	n         int64
	expiresAt int64
	counts    map[string]int64
	err       error
}

func (f *fakeRateStore) IncrementWindowCounter(_ context.Context, userID, sortKey string, n, expiresAt int64) (int64, error) {
	f.calls++
	f.userID, f.sortKey, f.n, f.expiresAt = userID, sortKey, n, expiresAt
	if f.err != nil {
		return 0, f.err
	}
	if f.counts == nil {
		f.counts = map[string]int64{}
	}
	f.counts[userID+"|"+sortKey] += n
	return f.counts[userID+"|"+sortKey], nil
}

func TestWindowCounterKeysAndTTL(t *testing.T) {
	at := time.Date(2026, 10, 18, 13, 47, 5, 0, time.FixedZone("KST", 9*3600))
	tests := []struct {
		name       string
		window     time.Duration
		wantSK     string
		wantEnd    time.Time
		minTTLLife time.Duration
	}{
		{"hourly", time.Hour, "RATE#translate#2026-10-18T04:00Z", time.Date(2026, 10, 18, 5, 0, 0, 0, time.UTC), 2 * time.Hour},
		{"daily lives at least 26h", 24 * time.Hour, "RATE#translate#2026-10-18T00:00Z", time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC), 26 * time.Hour},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeRateStore{}
			c := &WindowCounter{store: store, now: func() time.Time { return at }}
			usage, err := c.Take(context.Background(), "user-1", "translate", tt.window, 1)
			if err != nil {
				t.Fatal(err)
			}
			if store.sortKey != tt.wantSK || store.userID != "user-1" || store.n != 1 {
				t.Fatalf("store got %q %q %d", store.userID, store.sortKey, store.n)
			}
			if !usage.WindowEnd.Equal(tt.wantEnd) || usage.Count != 1 {
				t.Fatalf("usage = %+v", usage)
			}
			if want := tt.wantEnd.Add(2 * time.Hour).Unix(); store.expiresAt != want {
				t.Fatalf("expiresAt = %d, want %d", store.expiresAt, want)
			}
			windowStart, _ := time.Parse("2006-01-02T15:04Z", tt.wantSK[len("RATE#translate#"):])
			if life := time.Unix(store.expiresAt, 0).Sub(windowStart); life < tt.minTTLLife || life < tt.window+2*time.Hour {
				t.Fatalf("row lifetime %s is shorter than window plus grace", life)
			}
		})
	}
}

func TestWindowCounterRejectsInvalidRequests(t *testing.T) {
	tests := []struct {
		name, user, bucket string
		window             time.Duration
		n                  int64
	}{
		{"empty user", "", "translate", time.Hour, 1},
		{"empty bucket", "u", "", time.Hour, 1},
		{"bucket with separator", "u", "a#b", time.Hour, 1},
		{"zero window", "u", "translate", 0, 1},
		{"zero amount", "u", "translate", time.Hour, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeRateStore{}
			if _, err := NewWindowCounter(store).Take(context.Background(), tt.user, tt.bucket, tt.window, tt.n); err == nil {
				t.Fatal("expected an error")
			}
			if store.calls != 0 {
				t.Fatal("store called for an invalid request")
			}
		})
	}
}

func TestWindowCounterExceeds(t *testing.T) {
	tests := []struct {
		name      string
		limit     int64
		prior     int64
		storeErr  error
		want      bool
		wantCalls int
	}{
		{"zero disables without storage", 0, 99, nil, false, 0},
		{"under limit", 3, 1, nil, false, 1},
		{"at limit", 3, 2, nil, false, 1},
		{"over limit", 3, 3, nil, true, 1},
		{"storage error fails open", 3, 0, errors.New("throttled"), false, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeRateStore{err: tt.storeErr}
			c := NewWindowCounter(store)
			if tt.prior > 0 && tt.storeErr == nil {
				_, _, key, _ := rateWindow(c.now(), time.Hour)
				store.counts = map[string]int64{"u|RATE#translate#" + key: tt.prior}
			}
			got, _ := c.Exceeds(context.Background(), "u", "translate", time.Hour, 1, tt.limit)
			if got != tt.want || store.calls != tt.wantCalls {
				t.Fatalf("Exceeds = %v (calls %d), want %v (calls %d)", got, store.calls, tt.want, tt.wantCalls)
			}
		})
	}
	var unset *WindowCounter
	if over, _ := unset.Exceeds(context.Background(), "u", "translate", time.Hour, 1, 5); over {
		t.Fatal("an unconfigured counter must fail open")
	}
}

func TestParseRateLimit(t *testing.T) {
	tests := []struct {
		raw  string
		want int64
	}{
		{"", 3000}, {"  ", 3000}, {"0", 0}, {"42", 42}, {" 7 ", 7}, {"-1", 3000}, {"abc", 3000}, {"1.5", 3000},
	}
	for _, tt := range tests {
		if got := ParseRateLimit("X", tt.raw, 3000); got != tt.want {
			t.Errorf("ParseRateLimit(%q) = %d, want %d", tt.raw, got, tt.want)
		}
	}
}
