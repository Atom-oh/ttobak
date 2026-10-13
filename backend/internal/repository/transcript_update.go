package repository

import (
	"context"

	"github.com/ttobak/backend/internal/model"
)

// Capture before fetching/refining audio-derived text. Exact stored presence
// fences concurrent uploads, A/B selection, human edits and competing workers.
func (r *DynamoDBRepository) CaptureTranscriptUpdate(ctx context.Context, ownerID, meetingID string) (*model.SummarySnapshot, error) {
	// Notes, summary content and updatedAt are guarded by the summary CAS. Pinning
	// them here made unrelated edits retry an already-finished refinement.
	return r.captureMeetingSummaryMetadata(ctx, ownerID, meetingID, []string{
		"meetingId", "userId", "status", "audioKey", "audioKeys", "audioCrop",
		"audioPartCount", "audioPartsReadySet", "diarizationSpeakerHint", "summarizeRetryClaimedAt",
		"transcriptA", "transcriptB", "transcriptSegments", "selectedTranscript",
		"transcriptQualityA", "transcriptQualityB",
	})
}

// SaveTranscriptUpdate publishes text, segments and their processing status in
// one conditional update, retaining the immutable-spill writer's semantics.
func (r *DynamoDBRepository) SaveTranscriptUpdate(ctx context.Context, snapshot *model.SummarySnapshot, fields map[string]interface{}) error {
	if !validSummarySnapshot(snapshot) || len(snapshot.Checks) != 1 {
		return ErrConditionFailed
	}
	return r.updateMeetingFieldsWithCondition(ctx, snapshot.Meeting.UserID, snapshot.Meeting.MeetingID,
		summaryCondition(snapshot.Checks[0]), fields, true, nil)
}
