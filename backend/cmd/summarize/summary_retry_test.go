package main

import (
	"context"
	"errors"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

type retryStoreFixture struct {
	current             *model.Meeting
	claimed             bool
	claims, reads       int
	readErr, releaseErr error
	released            string
}

func (s *retryStoreFixture) ClaimSummaryRetry(context.Context, string, string) (string, error) {
	s.claims++
	if s.claimed {
		return "owned-claim", nil
	}
	return "", nil
}
func (s *retryStoreFixture) ReleaseSummaryRetryClaim(_ context.Context, _, _ string, claim string) error {
	s.released = claim
	return s.releaseErr
}
func (s *retryStoreFixture) GetMeeting(context.Context, string, string) (*model.Meeting, error) {
	s.reads++
	return s.current, s.readErr
}

func TestSummaryRetryReadFailureReleasesOnlyOwnedClaim(t *testing.T) {
	readErr := errors.New("read failed")
	releaseErr := errors.New("release failed")
	for _, failRelease := range []bool{false, true} {
		store := &retryStoreFixture{claimed: true, readErr: readErr}
		if failRelease {
			store.releaseErr = releaseErr
		}
		handled, err := resumeSummaryRetry(context.Background(), &model.Meeting{UserID: "owner", MeetingID: "m", Status: model.StatusSummarizing, SummaryRetryPending: true},
			store, func(context.Context, *model.Meeting, string) error {
				t.Fatal("read failure reached generation")
				return nil
			})
		if !handled || !errors.Is(err, readErr) || store.released != "owned-claim" || failRelease && !errors.Is(err, releaseErr) {
			t.Fatalf("claim leaked or release error hidden: %+v %v", store, err)
		}
	}
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
