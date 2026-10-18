package service

import (
	"context"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// DeriveSummary captures before model work and publishes against that same
// source. ID-based extractor wrappers remain available for read-only callers.
func (s *BedrockService) DeriveSummary(ctx context.Context, ownerID, meetingID, field string) error {
	switch field {
	case "tags", "sentiment", "insights":
	default:
		return ErrInvalidInput
	}
	snapshot, err := s.repo.CaptureSummaryPrivacy(ctx, ownerID, meetingID)
	if err != nil {
		return err
	}
	if snapshot == nil {
		return ErrNotFound
	}
	if snapshot.Meeting.Status != model.StatusDone {
		return repository.ErrConditionFailed
	}
	meeting := *snapshot.Meeting
	if field == "tags" || meeting.Content == "" {
		raw, variant := selectMeetingTranscript(&meeting)
		if raw != "" {
			text, object, err := s.repo.ReadResummaryTranscript(ctx, meetingID, "transcript"+variant, raw)
			if err != nil {
				return err
			}
			meeting.TranscriptA, meeting.TranscriptB = "", ""
			if variant == "B" {
				meeting.TranscriptB = text
			} else {
				meeting.TranscriptA = text
			}
			if object != nil {
				snapshot.Objects = append(snapshot.Objects, *object)
			}
			segments, object, err := s.repo.ReadResummaryTranscript(ctx, meetingID, "transcriptSegments", meeting.TranscriptSegments)
			if err != nil {
				return err
			}
			meeting.TranscriptSegments = segments
			if object != nil {
				snapshot.Objects = append(snapshot.Objects, *object)
			}
		}
	}
	var value interface{}
	switch field {
	case "tags":
		value, err = s.ExtractTagsForMeeting(ctx, &meeting)
	case "sentiment":
		value, err = s.ExtractSentimentForMeeting(ctx, &meeting)
	case "insights":
		value, err = s.ExtractInsightsForMeeting(ctx, &meeting)
	}
	if err != nil {
		return err
	}
	return s.repo.PublishSummaryDerivatives(ctx, snapshot, map[string]interface{}{field: value})
}
