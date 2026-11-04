// Package fanout runs independent reads concurrently for callers that must
// keep the observable behavior of a sequential loop.
package fanout

import (
	"context"
	"sync"
)

// Ordered calls fn(ctx, i) for every i in [0, n), keeping at most limit calls
// in flight and starting them in index order.
//
// A non-nil error from fn(i) is fatal at index i: calls for higher indices are
// canceled through their context or never started, while calls for lower
// indices still run to completion. Ordered returns the lowest fatal index, or
// n when no call failed.
//
// Callers store per-index results themselves (each call writes only its own
// slot) and replay them in index order after Ordered returns. Because every
// index below the returned one completed and nothing at or above it is
// consulted except the failing slot itself, the replay observes exactly what a
// sequential loop that stops at its first fatal error would have observed.
// Non-fatal outcomes should be recorded and reported as a nil error.
//
// A panic in fn is re-raised on the caller's goroutine after all calls finish.
func Ordered(ctx context.Context, n, limit int, fn func(context.Context, int) error) int {
	if n <= 0 {
		return 0
	}
	if limit < 1 {
		limit = 1
	}
	var (
		mu       sync.Mutex
		cutoff   = n
		cancels  = make([]context.CancelFunc, n)
		panicked any
		wg       sync.WaitGroup
	)
	sem := make(chan struct{}, limit)
	for i := 0; i < n; i++ {
		sem <- struct{}{}
		mu.Lock()
		if i > cutoff {
			mu.Unlock()
			<-sem
			break
		}
		callCtx, cancel := context.WithCancel(ctx)
		cancels[i] = cancel
		mu.Unlock()
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			defer func() {
				if r := recover(); r != nil {
					mu.Lock()
					if panicked == nil {
						panicked = r
					}
					mu.Unlock()
				}
			}()
			if err := fn(callCtx, i); err != nil {
				mu.Lock()
				if i < cutoff {
					cutoff = i
					for j := i + 1; j < n; j++ {
						if cancels[j] != nil {
							cancels[j]()
						}
					}
				}
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	for _, cancel := range cancels {
		if cancel != nil {
			cancel()
		}
	}
	if panicked != nil {
		panic(panicked)
	}
	return cutoff
}
