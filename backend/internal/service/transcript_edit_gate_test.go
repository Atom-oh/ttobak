package service

import (
	"context"
	"errors"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

func TestUpdateMeetingRefusesTranscriptEditWhileTranscribing(t *testing.T) {
	repo := &noteSourceMeetingRepo{mockMeetingRepo: newMockMeetingRepo()}
	repo.addMeeting(&model.Meeting{MeetingID: "m-1", UserID: "owner-1", Status: model.StatusTranscribing, TranscriptA: "machine text"})
	_, err := newMeetingServiceWithRepo(repo).UpdateMeeting(context.Background(), "owner-1", "m-1",
		&model.UpdateMeetingRequest{TranscriptA: "human correction"})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("transcript edit while transcribing = %v, want ErrInvalidInput", err)
	}
	if len(repo.updates) != 0 || repo.wholeUpdates != 0 {
		t.Fatal("refused transcript edit was still written")
	}
}
