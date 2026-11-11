package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/service"
)

func pagedLister(pages [][]meetingRef, failAt int) meetingLister {
	return func(_ context.Context, cursor map[string]any) ([]meetingRef, map[string]any, error) {
		page := 0
		if cursor != nil {
			page = cursor["page"].(int)
		}
		if page == failAt {
			return nil, nil, errors.New("scan failed")
		}
		var next map[string]any
		if page+1 < len(pages) {
			next = map[string]any{"page": page + 1}
		}
		return pages[page], next, nil
	}
}

func TestBackfillEnqueuesEveryPageAndSpacesQueuedOnly(t *testing.T) {
	pages := [][]meetingRef{
		{{UserID: "u1", MeetingID: "m1"}, {UserID: "u1", MeetingID: "m2"}, {UserID: "", MeetingID: "orphan"}},
		{{UserID: "u2", MeetingID: "m3"}},
	}
	outcomes := map[string]string{"m1": service.TermsEnqueueQueued, "m2": service.TermsEnqueueUnchanged, "m3": service.TermsEnqueueFailed}
	var calls []string
	sleeps := 0
	result, err := backfill(context.Background(), pagedLister(pages, -1), func(_ context.Context, owner, meeting string) string {
		calls = append(calls, owner+"/"+meeting)
		return outcomes[meeting]
	}, backfillOptions{interval: time.Second, sleep: func(context.Context, time.Duration) error { sleeps++; return nil }})
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 3 || calls[0] != "u1/m1" || calls[2] != "u2/m3" {
		t.Fatalf("calls %v", calls)
	}
	if result.Scanned != 3 || result.Outcomes[service.TermsEnqueueQueued] != 1 || result.Outcomes[service.TermsEnqueueFailed] != 1 {
		t.Fatalf("result %+v", result)
	}
	if sleeps != 1 {
		t.Fatalf("slept %d times, want once after the single QUEUED outcome", sleeps)
	}
}

func TestBackfillLimitAndListingError(t *testing.T) {
	pages := [][]meetingRef{{{UserID: "u1", MeetingID: "m1"}, {UserID: "u1", MeetingID: "m2"}}, {{UserID: "u2", MeetingID: "m3"}}}
	count := 0
	enqueue := func(context.Context, string, string) string { count++; return "DRY_RUN" }
	result, err := backfill(context.Background(), pagedLister(pages, -1), enqueue, backfillOptions{limit: 1})
	if err != nil || result.Scanned != 1 || count != 1 {
		t.Fatalf("limit: result %+v err %v calls %d", result, err, count)
	}
	count = 0
	result, err = backfill(context.Background(), pagedLister(pages, 1), enqueue, backfillOptions{})
	if err == nil || result.Scanned != 2 || count != 2 {
		t.Fatalf("listing error: result %+v err %v calls %d", result, err, count)
	}
}
