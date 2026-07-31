package model

import (
	"testing"
	"time"
)

// TestToMeetingListItemCarriesSentimentAndDuration guards the Meeting →
// MeetingListItem mapping: a missing field mapping here is exactly what
// previously dropped sentiment/duration from the list response.
func TestToMeetingListItemCarriesSentimentAndDuration(t *testing.T) {
	ts := time.Date(2026, 3, 5, 10, 0, 0, 0, time.UTC)
	m := &Meeting{
		MeetingID: "mtg-001",
		Title:     "Quarterly Review",
		Sentiment: "positive",
		Duration:  1830,
		Date:      ts,
		CreatedAt: ts,
		UpdatedAt: ts,
	}

	item := ToMeetingListItem(m, false, nil, nil)

	if item.Sentiment != "positive" {
		t.Errorf("Sentiment = %q, want %q", item.Sentiment, "positive")
	}
	if item.Duration != 1830 {
		t.Errorf("Duration = %d, want %d", item.Duration, 1830)
	}
}
