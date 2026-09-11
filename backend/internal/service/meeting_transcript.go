package service

import (
	"strings"

	"github.com/ttobak/backend/internal/model"
)

// selectMeetingTranscript resolves the stored preference against available text.
// Whitespace-only variants are unavailable; keep the chosen text unchanged.
// The shared TranscriptSegments field can describe either variant, so consumers
// must verify it against this text rather than infer ownership from A/B.
func selectMeetingTranscript(meeting *model.Meeting) (text, variant string) {
	if meeting == nil {
		return "", ""
	}
	hasA := strings.TrimSpace(meeting.TranscriptA) != ""
	hasB := strings.TrimSpace(meeting.TranscriptB) != ""
	if hasB && (meeting.SelectedTranscript == "B" || !hasA) {
		return meeting.TranscriptB, "B"
	}
	if hasA {
		return meeting.TranscriptA, "A"
	}
	return "", ""
}
