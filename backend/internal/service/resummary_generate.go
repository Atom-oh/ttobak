package service

import (
	"context"
	"github.com/ttobak/backend/internal/model"
	"strings"
)

// GenerateResummary consumes the caller's immutable prepared snapshot. It never
// reloads a meeting, runs STT/refinement, or writes a result to storage.
func (s *BedrockService) GenerateResummary(ctx context.Context, meeting *model.Meeting, attachments []model.Attachment) (string, error) {
	if meeting == nil {
		return "", ErrResummaryNoSource
	}
	transcript, _ := selectMeetingTranscript(meeting)
	hasSource := strings.TrimSpace(transcript) != "" || strings.TrimSpace(meeting.Notes) != "" || strings.TrimSpace(meeting.Content) != ""
	for _, att := range attachments {
		if att.ExtractedText != nil && len(att.ExtractedText.Units) > 0 {
			hasSource = true
		}
	}
	if !hasSource {
		return "", ErrResummaryNoSource
	}
	return s.generateSummarySnapshot(ctx, meeting, attachments, "", true, model.NoteLanguageKorean)
}

// GenerateEnglishNote writes the English note from the prepared transcript,
// saved notes, verified documents and image analyses. It never uses the
// Korean note or a prior English note as evidence, and writes nothing.
func (s *BedrockService) GenerateEnglishNote(ctx context.Context, meeting *model.Meeting, attachments []model.Attachment) (string, error) {
	if meeting == nil {
		return "", ErrResummaryNoSource
	}
	source := *meeting
	source.Content, source.LiveSummary, source.ContentEn = "", "", ""
	transcript, _ := selectMeetingTranscript(&source)
	hasSource := strings.TrimSpace(transcript) != "" || strings.TrimSpace(source.Notes) != ""
	for _, att := range attachments {
		if att.ExtractedText != nil && len(att.ExtractedText.Units) > 0 {
			hasSource = true
		}
	}
	if !hasSource {
		return "", ErrResummaryNoSource
	}
	return s.generateSummarySnapshot(ctx, &source, attachments, "", false, model.NoteLanguageEnglish)
}
