package repository

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/ttobak/backend/internal/model"
)

// Only ASR inputs, transcript ownership and timing candidates belong in this
// fence. Notes/title/content changes do not invalidate an audio refinement.
func (r *DynamoDBRepository) CaptureTranscriptUpdate(ctx context.Context, ownerID, meetingID string, source ...string) (*model.SummarySnapshot, error) {
	row, err := r.summaryRow(ctx, model.PrefixUser+ownerID, model.PrefixMeeting+meetingID)
	if err != nil || len(row) == 0 {
		return nil, err
	}
	snapshot := &model.SummarySnapshot{Meeting: &model.Meeting{}, Stored: map[string]interface{}{}}
	if err := attributevalue.UnmarshalMap(row, snapshot.Meeting); err != nil {
		return nil, err
	}
	if snapshot.Meeting.UserID != ownerID || snapshot.Meeting.MeetingID != meetingID {
		return nil, ErrSummaryAccess
	}
	if err := attributevalue.UnmarshalMap(row, &snapshot.Stored); err != nil {
		return nil, err
	}
	check := model.SummaryCheck{PK: model.PrefixUser + ownerID, SK: model.PrefixMeeting + meetingID, Exists: true, Fields: map[string]model.SummaryValue{}}
	for _, field := range []string{
		"meetingId", "userId", "status", "audioKey", "audioKeys", "audioCrop",
		"audioPartCount", "audioPartsReadySet", "diarizationSpeakerHint", "summarizeRetryClaimedAt", "summaryBatchContentHash",
		"transcriptA", "transcriptB", "transcriptSegments", "selectedTranscript",
		"transcriptRevisionA", "transcriptRevisionB",
		"transcriptQualityA", "transcriptQualityB",
	} {
		value, present := row[field]
		// expression.Value accepts AttributeValue directly. Unmarshalling
		// here would turn NS/SS into slices and remarshal them as L.
		check.Fields[field] = model.SummaryValue{Present: present, Value: value}
	}
	snapshot.Checks = []model.SummaryCheck{check}
	variant, value := "A", snapshot.Meeting.TranscriptA
	if len(source) > 0 && source[0] == "B" {
		variant, value = "B", snapshot.Meeting.TranscriptB
	}
	if strings.HasPrefix(value, "s3://") {
		bucket, key, err := validateTranscriptRef(r.bucketName, meetingID, "transcript"+variant, value)
		if err != nil {
			return nil, err
		}
		if strings.HasSuffix(key, "/transcript"+variant+".txt") {
			// Revisions from new writers complement this legacy fixed-key
			// byte fence; they cannot attest writes made by old binaries.
			object, err := r.transcriptTargetHead(ctx, bucket, key)
			if err != nil {
				return nil, err
			}
			snapshot.Objects = append(snapshot.Objects, object)
		}
	}
	return snapshot, nil
}

func (r *DynamoDBRepository) transcriptTargetHead(ctx context.Context, bucket, key string) (model.SummaryObject, error) {
	object := model.SummaryObject{Bucket: bucket, Key: key}
	if r.s3Client == nil {
		return object, ErrSummaryObject
	}
	head, err := r.s3Client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		var status interface{ HTTPStatusCode() int }
		if errors.As(err, &status) && status.HTTPStatusCode() == 404 {
			return object, nil
		}
		return object, err
	}
	object.ETag, object.VersionID = aws.ToString(head.ETag), aws.ToString(head.VersionId)
	if object.ETag == "" {
		return object, ErrSummaryObject
	}
	return object, nil
}

func (r *DynamoDBRepository) TranscriptTargetsMatch(ctx context.Context, snapshot *model.SummarySnapshot) (bool, error) {
	for _, before := range snapshot.Objects {
		current, err := r.transcriptTargetHead(ctx, before.Bucket, before.Key)
		if err != nil {
			return false, err
		}
		if current.ETag != before.ETag || current.VersionID != before.VersionID {
			return false, nil
		}
	}
	return true, nil
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

// Publish transcript/quality and acquire summary ownership in the same write.
// An empty fields map starts from a human-edited CURRENT saved transcript.
// Pending plus an owned claim makes a crashed handoff recoverable without STT.
func (r *DynamoDBRepository) StartTranscriptSummary(ctx context.Context, snapshot *model.SummarySnapshot, fields map[string]interface{}, contentHash string) (string, error) {
	if len(contentHash) != 64 || !validSummarySnapshot(snapshot) ||
		(snapshot.Meeting.Status != model.StatusTranscribing &&
			(snapshot.Meeting.Status != model.StatusSummarizing || snapshot.Meeting.SummaryRetryPending)) {
		return "", ErrConditionFailed
	}
	claim := time.Now().UTC().Format(time.RFC3339Nano)
	update := make(map[string]interface{}, len(fields)+5)
	for name, value := range fields {
		update[name] = value
	}
	for _, variant := range []string{"A", "B"} {
		if quality, ok := update["transcriptQuality"+variant].(model.TranscriptQuality); ok {
			quality.ProcessingRunID = claim
			update["transcriptQuality"+variant] = quality
		}
	}
	update["status"], update["summaryRetryPending"] = model.StatusSummarizing, true
	update["summaryRetryAttempts"], update["summaryConflictCode"] = 0, ""
	update["summarizeRetryClaimedAt"] = claim
	update["summaryBatchContentHash"] = contentHash
	options := r.client.Options()
	options.Retryer, options.RetryMaxAttempts = aws.NopRetryer{}, 1
	writer := *r
	writer.client = dynamodb.New(options)
	err := writer.SaveTranscriptUpdate(ctx, snapshot, update)
	if err == nil {
		return claim, nil
	}
	// A lost response is not permission to resend the old transcript. Read
	// back our unique claim; subsequent human edits remain the summary input.
	current, readErr := r.MetadataView().GetMeeting(ctx, snapshot.Meeting.UserID, snapshot.Meeting.MeetingID)
	if readErr == nil && current != nil && current.Status == model.StatusSummarizing &&
		current.SummaryRetryPending && current.SummarizeRetryClaimedAt == claim {
		return claim, nil
	}
	return "", errors.Join(err, readErr)
}

// Presence distinguishes an absent legacy field from an explicit empty edit.
func SummaryContentHash(snapshot *model.SummarySnapshot) string {
	value, present := snapshot.Stored["content"]
	body, _ := json.Marshal([]interface{}{present, value})
	return model.TranscriptHash(string(body))
}

// End only this owned batch. Preserve the human content and all existing
// coverage/provenance; this is deliberately not summary-success publication.
func (r *DynamoDBRepository) FinishSummaryPreservingContent(ctx context.Context, snapshot *model.SummarySnapshot) error {
	if !validSummarySnapshot(snapshot) || snapshot.Meeting.Status != model.StatusSummarizing ||
		!snapshot.Meeting.SummaryRetryPending || snapshot.Meeting.SummarizeRetryClaimedAt == "" ||
		snapshot.Meeting.SummaryBatchContentHash == "" || snapshot.Meeting.SummaryBatchContentHash == SummaryContentHash(snapshot) {
		return ErrConditionFailed
	}
	parent := snapshot.Checks[0]
	parent.Fields = map[string]model.SummaryValue{}
	for _, field := range []string{"meetingId", "userId", "status", "content", "summaryRetryPending", "summarizeRetryClaimedAt", "summaryBatchContentHash"} {
		parent.Fields[field] = snapshot.Checks[0].Fields[field]
	}
	copy := *snapshot
	copy.Checks = []model.SummaryCheck{parent}
	return r.SaveTranscriptUpdate(ctx, &copy, map[string]interface{}{
		"status": model.StatusDone, "summaryRetryPending": false, "summarizeRetryClaimedAt": "",
		"summaryConflictCode": "HUMAN_CONTENT_PRESERVED",
	})
}
