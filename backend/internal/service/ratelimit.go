package service

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/ttobak/backend/internal/model"
)

// rateCounterStore is the repository primitive behind WindowCounter.
type rateCounterStore interface {
	IncrementWindowCounter(ctx context.Context, userID, sortKey string, n, expiresAt int64) (int64, error)
}

// windowCounterGrace keeps a counter row past its window so a TTL sweep can
// never delete a still-current window; a daily window lives at least 26 h.
const windowCounterGrace = 2 * time.Hour

// WindowCounter is a per-user fixed-window usage counter. It is an abuse and
// cost brake, not an access boundary: callers fail open on storage errors.
type WindowCounter struct {
	store rateCounterStore
	now   func() time.Time
}

func NewWindowCounter(store rateCounterStore) *WindowCounter {
	return &WindowCounter{store: store, now: time.Now}
}

// WindowUsage is the counter total after a Take and the end of its window.
type WindowUsage struct {
	Count     int64
	WindowEnd time.Time
}

// rateWindow returns the UTC-aligned window containing now, its key and the
// row expiry (window end plus grace).
func rateWindow(now time.Time, window time.Duration) (start, end time.Time, key string, expiresAt int64) {
	start = now.UTC().Truncate(window)
	end = start.Add(window)
	return start, end, start.Format("2006-01-02T15:04Z"), end.Add(windowCounterGrace).Unix()
}

// Take atomically adds n to the caller's counter for bucket in the current
// window and returns the new total.
func (c *WindowCounter) Take(ctx context.Context, userID, bucket string, window time.Duration, n int64) (WindowUsage, error) {
	if c == nil || c.store == nil {
		return WindowUsage{}, fmt.Errorf("window counter is not configured")
	}
	if userID == "" || bucket == "" || strings.Contains(bucket, "#") || window <= 0 || n <= 0 {
		return WindowUsage{}, fmt.Errorf("invalid window counter request")
	}
	_, end, key, expiresAt := rateWindow(c.now(), window)
	count, err := c.store.IncrementWindowCounter(ctx, userID, model.PrefixRate+bucket+"#"+key, n, expiresAt)
	if err != nil {
		return WindowUsage{}, err
	}
	return WindowUsage{Count: count, WindowEnd: end}, nil
}

// rateCounterTimeout caps one counter update before Exceeds fails open.
const rateCounterTimeout = 400 * time.Millisecond

// Exceeds reports whether taking n puts the caller over limit. A limit of 0
// (or less) disables the check without touching storage. Storage errors fail
// open: the error is logged without the user ID and the call is allowed.
func (c *WindowCounter) Exceeds(ctx context.Context, userID, bucket string, window time.Duration, n, limit int64) (bool, WindowUsage) {
	if limit <= 0 {
		return false, WindowUsage{}
	}
	// Bound the abuse brake so a slow table fails open quickly instead of
	// spending the request's own deadline on SDK retries.
	takeCtx, cancel := context.WithTimeout(ctx, rateCounterTimeout)
	defer cancel()
	usage, err := c.Take(takeCtx, userID, bucket, window, n)
	if err != nil {
		log.Printf("rate counter unavailable, failing open: bucket=%s err=%v", bucket, err)
		return false, WindowUsage{}
	}
	return usage.Count > limit, usage
}

// ParseRateLimit reads a non-negative integer limit from configuration. Blank
// or invalid values use def; 0 disables the limit.
func ParseRateLimit(name, raw string, def int64) int64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return def
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 {
		log.Printf("warn: invalid %s %q; using default %d", name, raw, def)
		return def
	}
	return value
}
