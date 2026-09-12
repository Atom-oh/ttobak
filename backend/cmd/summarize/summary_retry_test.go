package main

import (
	"context"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

type retryStoreFixture struct {
	current       *model.Meeting
	claimed       bool
	claims, reads int
}

func (s *retryStoreFixture) ClaimSummarizeRetry(context.Context, string, string) (bool, error) {
	s.claims++
	return s.claimed, nil
}
func (s *retryStoreFixture) GetMeeting(context.Context, string, string) (*model.Meeting, error) {
	s.reads++
	return s.current, nil
}

func TestSummaryConflictRedeliveryGeneratesFromFreshSourceOnly(t *testing.T) {
	old := &model.Meeting{UserID: "owner", MeetingID: "m", Status: model.StatusSummarizing, SummaryRetryPending: true, Notes: "old"}
	fresh := &model.Meeting{UserID: "owner", MeetingID: "m", Status: model.StatusSummarizing, SummaryRetryPending: true, Notes: "new", TranscriptA: "edited"}
	for _, claimed := range []bool{true, false} {
		store := &retryStoreFixture{current: fresh, claimed: claimed}
		calls := 0
		handled, err := resumeSummaryRetry(context.Background(), old, store, func(_ context.Context, source *model.Meeting, _ string) error {
			calls++
			if source != fresh || source.Notes != "new" || source.TranscriptA != "edited" {
				t.Fatal("retry did not call generation with the fresh source")
			}
			return nil
		})
		if err != nil || !handled || store.claims != 1 || claimed && (calls != 1 || store.reads != 1) || !claimed && (calls != 0 || store.reads != 0) {
			t.Fatalf("unsafe duplicate/retry: %+v calls=%d err=%v", store, calls, err)
		}
	}
}
