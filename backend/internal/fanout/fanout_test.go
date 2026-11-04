package fanout

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestOrderedRunsEveryIndexWithinTheBound(t *testing.T) {
	for _, tc := range []struct{ n, limit int }{{0, 8}, {1, 8}, {7, 3}, {40, 8}, {5, 0}} {
		var inFlight, peak int64
		seen := make([]int32, tc.n)
		stop := Ordered(context.Background(), tc.n, tc.limit, func(_ context.Context, i int) error {
			now := atomic.AddInt64(&inFlight, 1)
			defer atomic.AddInt64(&inFlight, -1)
			for p := atomic.LoadInt64(&peak); now > p && !atomic.CompareAndSwapInt64(&peak, p, now); p = atomic.LoadInt64(&peak) {
			}
			time.Sleep(2 * time.Millisecond)
			atomic.AddInt32(&seen[i], 1)
			return nil
		})
		if stop != tc.n {
			t.Fatalf("n=%d: stop=%d", tc.n, stop)
		}
		for i, c := range seen {
			if c != 1 {
				t.Fatalf("n=%d: index %d ran %d times", tc.n, i, c)
			}
		}
		bound := int64(max(tc.limit, 1))
		if peak > bound {
			t.Fatalf("n=%d limit=%d: peak %d", tc.n, tc.limit, peak)
		}
		if tc.n >= 7 && tc.limit > 1 && peak < 2 {
			t.Fatalf("n=%d limit=%d: calls never overlapped", tc.n, tc.limit)
		}
	}
}

func TestOrderedReportsLowestFailureAndCancelsOnlyHigherIndices(t *testing.T) {
	errLow, errHigh := errors.New("low"), errors.New("high")
	const n = 30
	var mu sync.Mutex
	outcome := make([]error, n)
	started := make([]bool, n)
	stop := Ordered(context.Background(), n, 8, func(ctx context.Context, i int) error {
		mu.Lock()
		started[i] = true
		mu.Unlock()
		var err error
		switch {
		case i == 9: // fails fast
			err = errHigh
		case i == 4: // fails after index 9 already failed
			time.Sleep(30 * time.Millisecond)
			err = errLow
		default:
			select {
			case <-ctx.Done():
				err = ctx.Err()
			case <-time.After(15 * time.Millisecond):
			}
		}
		mu.Lock()
		outcome[i] = err
		mu.Unlock()
		return err
	})
	if stop != 4 || outcome[4] != errLow {
		t.Fatalf("stop=%d outcome=%v, want the lowest failing index 4", stop, outcome[stop])
	}
	for i := 0; i < 4; i++ {
		if outcome[i] != nil {
			t.Fatalf("index %d below the failure was canceled: %v", i, outcome[i])
		}
	}
	for i := 10; i < n; i++ {
		if started[i] && outcome[i] == nil {
			t.Fatalf("index %d above the failure completed instead of being canceled", i)
		}
	}
	if started[n-1] {
		t.Fatal("work after the failure kept being started")
	}
}

func TestOrderedReraisesPanicsOnTheCaller(t *testing.T) {
	defer func() {
		if r := recover(); r != "boom" {
			t.Fatalf("recovered %v", r)
		}
	}()
	Ordered(context.Background(), 4, 2, func(_ context.Context, i int) error {
		if i == 2 {
			panic("boom")
		}
		return nil
	})
	t.Fatal("panic was swallowed")
}
