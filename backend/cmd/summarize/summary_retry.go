package main

import (
	"context"
	"fmt"

	"github.com/ttobak/backend/internal/model"
)

type summaryRetryStore interface {
	ClaimSummarizeRetry(context.Context, string, string) (bool, error)
	GetMeeting(context.Context, string, string) (*model.Meeting, error)
}

// Resume only a durably recorded conflict. The callback generates new output;
// this path has no old model output and never re-runs transcript refinement.
func resumeSummaryRetry(ctx context.Context, meeting *model.Meeting, store summaryRetryStore,
	run func(context.Context, *model.Meeting, string) error) (bool, error) {
	if meeting == nil || !meeting.SummaryRetryPending || meeting.Status != model.StatusSummarizing {
		return false, nil
	}
	claimed, err := store.ClaimSummarizeRetry(ctx, meeting.UserID, meeting.MeetingID)
	if err != nil || !claimed {
		return true, err
	}
	current, err := store.GetMeeting(ctx, meeting.UserID, meeting.MeetingID)
	if err != nil || current == nil {
		return true, err
	}
	if current.UserID != meeting.UserID || current.MeetingID != meeting.MeetingID {
		return true, fmt.Errorf("summary retry source identity changed")
	}
	if current.Status != model.StatusSummarizing {
		return true, nil
	}
	return true, run(ctx, current, buildLinkedMeetingContext(ctx, current))
}
