package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ttobak/backend/internal/model"
)

type summaryRetryStore interface {
	ClaimSummaryRetry(context.Context, string, string) (string, error)
	ReleaseSummaryRetryClaim(context.Context, string, string, string) error
	GetMeeting(context.Context, string, string) (*model.Meeting, error)
}

// Resume only a durably recorded conflict. The callback generates new output;
// this path has no old model output and never re-runs transcript refinement.
func resumeSummaryRetry(ctx context.Context, meeting *model.Meeting, store summaryRetryStore,
	run func(context.Context, *model.Meeting, string) error) (bool, error) {
	if meeting == nil || !meeting.SummaryRetryPending || meeting.Status != model.StatusSummarizing {
		return false, nil
	}
	claim, err := store.ClaimSummaryRetry(ctx, meeting.UserID, meeting.MeetingID)
	if err != nil || claim == "" {
		return true, err
	}
	release := func(cause error) (bool, error) {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		return true, errors.Join(cause, store.ReleaseSummaryRetryClaim(cleanup, meeting.UserID, meeting.MeetingID, claim))
	}
	current, err := store.GetMeeting(ctx, meeting.UserID, meeting.MeetingID)
	if err != nil || current == nil {
		return release(err)
	}
	if current.UserID != meeting.UserID || current.MeetingID != meeting.MeetingID {
		return release(fmt.Errorf("summary retry source identity changed"))
	}
	if current.Status != model.StatusSummarizing {
		return release(nil)
	}
	err = run(ctx, current, buildLinkedMeetingContext(ctx, current))
	if err != nil {
		return release(err)
	}
	return true, err
}
