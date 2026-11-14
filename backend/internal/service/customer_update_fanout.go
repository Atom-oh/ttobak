package service

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/ttobak/backend/internal/model"
)

const (
	fanoutSubPageSize   = 100
	fanoutEntryLimit    = 50
	fanoutInitialWindow = 72 * time.Hour
)

// fanoutRepo is the repository surface the feed fan-out needs.
type fanoutRepo interface {
	ListFeedSubs(ctx context.Context, cursor string, limit int) ([]model.UpdateFeedSub, string, error)
	GetFeedState(ctx context.Context, feedHash string) (*model.UpdateFeedState, error)
	ListFeedEntries(ctx context.Context, feedHash string, since time.Time, limit int) ([]model.UpdateFeedEntry, error)
	ListNewestFeedEntries(ctx context.Context, feedHash string, since time.Time, limit int) ([]model.UpdateFeedEntry, error)
	SetFeedSubFannedAt(ctx context.Context, sub model.UpdateFeedSub, at time.Time) error
	PutItemsIfAbsent(ctx context.Context, userID, sourceID string, items []model.UpdateItem) (int, error)
	EnsureURLPending(ctx context.Context, urlHash, url string) error
	MarkPending(ctx context.Context, userID string) error
}

// FanoutStats summarizes one fan-out run.
type FanoutStats struct {
	Subs      int
	Delivered int
	Rejected  int
	Skipped   int
}

// CustomerUpdateFanout delivers collected feed entries into each subscriber's
// link inbox. Only this worker (and the API's link upload) writes USER# items.
type CustomerUpdateFanout struct {
	repo fanoutRepo
	now  func() time.Time
}

// NewCustomerUpdateFanout creates a CustomerUpdateFanout.
func NewCustomerUpdateFanout(repo fanoutRepo, now func() time.Time) *CustomerUpdateFanout {
	if now == nil {
		now = time.Now
	}
	return &CustomerUpdateFanout{repo: repo, now: now}
}

type fanoutEntriesKey struct {
	feedHash string
	since    time.Time
	newest   bool
}

// fanoutRun holds per-run caches so subscribers of one feed share reads.
type fanoutRun struct {
	tick    time.Time
	states  map[string]*model.UpdateFeedState
	entries map[fanoutEntriesKey][]model.UpdateFeedEntry
}

// Run fans out every active subscription once. A failing subscription is
// logged by feed hash prefix and leaves its FannedAt unchanged; only a failure
// to list subscriptions fails the run. When ctx is done (the fan-out budget)
// it stops cleanly between subscriptions: those not reached keep their
// watermarks for the next tick.
func (f *CustomerUpdateFanout) Run(ctx context.Context) (FanoutStats, error) {
	var stats FanoutStats
	run := &fanoutRun{
		tick:    f.now().UTC(),
		states:  map[string]*model.UpdateFeedState{},
		entries: map[fanoutEntriesKey][]model.UpdateFeedEntry{},
	}
	cursor := ""
	for {
		if ctx.Err() != nil {
			log.Printf("customer update fan-out stopped at its budget after subs=%d", stats.Subs)
			return stats, nil
		}
		subs, next, err := f.repo.ListFeedSubs(ctx, cursor, fanoutSubPageSize)
		if err != nil {
			return stats, err
		}
		for _, sub := range subs {
			if ctx.Err() != nil {
				log.Printf("customer update fan-out stopped at its budget after subs=%d", stats.Subs)
				return stats, nil
			}
			stats.Subs++
			if err := f.fanOneSub(ctx, run, sub, &stats); err != nil {
				log.Printf("customer update fan-out failed feed=%s: %v", shortHash(sub.FeedHash), err)
			}
		}
		if next == "" {
			return stats, nil
		}
		cursor = next
	}
}

func (f *CustomerUpdateFanout) feedState(ctx context.Context, run *fanoutRun, feedHash string) (*model.UpdateFeedState, error) {
	if st, ok := run.states[feedHash]; ok {
		return st, nil
	}
	st, err := f.repo.GetFeedState(ctx, feedHash)
	if err != nil {
		return nil, err
	}
	run.states[feedHash] = st
	return st, nil
}

func (f *CustomerUpdateFanout) feedEntries(ctx context.Context, run *fanoutRun, feedHash string, since time.Time, newest bool) ([]model.UpdateFeedEntry, error) {
	key := fanoutEntriesKey{feedHash, since, newest}
	if es, ok := run.entries[key]; ok {
		return es, nil
	}
	var es []model.UpdateFeedEntry
	var err error
	if newest {
		es, err = f.repo.ListNewestFeedEntries(ctx, feedHash, since, fanoutEntryLimit)
	} else {
		es, err = f.repo.ListFeedEntries(ctx, feedHash, since, fanoutEntryLimit+1)
	}
	if err != nil {
		return nil, err
	}
	run.entries[key] = es
	return es, nil
}

func (f *CustomerUpdateFanout) fanOneSub(ctx context.Context, run *fanoutRun, sub model.UpdateFeedSub, stats *FanoutStats) error {
	if sub.Status != model.UpdateSourceActive {
		stats.Skipped++
		return nil
	}
	state, err := f.feedState(ctx, run, sub.FeedHash)
	if err != nil {
		return err
	}
	if state == nil || state.UpdatedAt.IsZero() || (!sub.FannedAt.IsZero() && !state.UpdatedAt.After(sub.FannedAt)) {
		stats.Skipped++
		return nil
	}
	// The collector stamps seenAt and updatedAt with its run-start time but
	// writes them later, so the watermark is the updatedAt read before the
	// entries, never the wall clock: entries still being written stay after it.
	at := state.UpdatedAt.UTC()
	window := run.tick.Add(-fanoutInitialWindow)
	first := sub.FannedAt.IsZero()
	since := window
	if !first && sub.FannedAt.After(window) {
		since = sub.FannedAt
	}
	entries, err := f.feedEntries(ctx, run, sub.FeedHash, since, first)
	if err != nil {
		return err
	}

	// First delivery takes the newest fanoutEntryLimit; the older backlog is skipped.
	if !first && len(entries) > fanoutEntryLimit {
		var undelivered int
		entries, at, undelivered = capEntries(entries)
		if undelivered > 0 {
			log.Printf("customer update fan-out: %d entries share one seenAt beyond the cap, not delivered feed=%s", undelivered, shortHash(sub.FeedHash))
		}
	}

	// accepted counts created plus duplicate items, so a retry whose items all
	// exist still heals a pending marker lost to an earlier partial failure.
	accepted := 0
	seen := make(map[string]bool, len(entries))
	deliver := func() error {
		for _, e := range entries {
			normalized, hash, nerr := model.NormalizeUpdateURL(e.URL)
			if nerr != nil {
				stats.Rejected++
				continue
			}
			if seen[hash] {
				continue
			}
			seen[hash] = true
			first := e.SeenAt.UTC()
			if first.IsZero() || first.After(run.tick) {
				first = run.tick
			}
			item := model.UpdateItem{URLHash: hash, URL: normalized, FirstSeenAt: first}
			// URL row first, as in UploadLinks: an orphan URL row is harmless.
			if err := f.repo.EnsureURLPending(ctx, hash, normalized); err != nil {
				return err
			}
			n, err := f.repo.PutItemsIfAbsent(ctx, sub.UserID, sub.SourceID, []model.UpdateItem{item})
			if err != nil {
				return err
			}
			accepted++
			stats.Delivered += n
		}
		return nil
	}
	derr := deliver()
	// Mark pending even after a partial failure so written items are not stranded.
	if accepted > 0 {
		if merr := f.repo.MarkPending(ctx, sub.UserID); merr != nil {
			derr = errors.Join(derr, merr)
		}
	}
	if derr != nil {
		return derr
	}

	return f.repo.SetFeedSubFannedAt(ctx, sub, at)
}

// capEntries trims a read of fanoutEntryLimit+1 entries (oldest first) to the
// entries to deliver now and the FannedAt to record. Entries sharing one seenAt
// are never split across ticks, because the next read is strictly after FannedAt.
// undelivered is nonzero only when all entries share one seenAt: the first
// fanoutEntryLimit are delivered and the rest are dropped to avoid looping.
func capEntries(entries []model.UpdateFeedEntry) (deliver []model.UpdateFeedEntry, at time.Time, undelivered int) {
	t := entries[fanoutEntryLimit].SeenAt.UTC()
	last := entries[fanoutEntryLimit-1].SeenAt.UTC()
	if last.Before(t) {
		return entries[:fanoutEntryLimit], last, 0
	}
	n := 0
	for n < len(entries) && entries[n].SeenAt.UTC().Before(t) {
		n++
	}
	if n == 0 {
		return entries[:fanoutEntryLimit], t, len(entries) - fanoutEntryLimit
	}
	return entries[:n], entries[n-1].SeenAt.UTC(), 0
}
