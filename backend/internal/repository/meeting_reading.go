package repository

import (
	"context"
	"fmt"
	"strings"
)

// MetadataView is an independent read view for authorization and bounded
// reading. It shares concurrency-safe SDK clients, not mutable hydration flags.
// Existing repository readers retain their default hydration behavior.
func (r *DynamoDBRepository) MetadataView() *DynamoDBRepository {
	view := *r
	view.skipTranscriptHydration = true
	return &view
}

// ReadTranscriptField is an explicit, read-only hydration operation. The caller
// must authorize the meeting first. Unlike legacy detail hydration it fails
// visibly for unavailable/corrupt references rather than returning a ref/blank.
func (r *DynamoDBRepository) ReadTranscriptField(ctx context.Context, meetingID, field, value string) (string, error) {
	switch field {
	case "transcriptA", "transcriptB", "transcriptSegments":
	default:
		return "", ErrInvalidTranscriptRef
	}
	if strings.HasPrefix(value, "s3://") && (r.s3Client == nil || r.bucketName == "") {
		return "", fmt.Errorf("transcript storage is unavailable")
	}
	return r.loadTranscript(ctx, meetingID, field, value)
}
