package repository

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	"github.com/ttobak/backend/internal/model"
)

// transcriptSizeThreshold is the size above which transcripts are stored in S3
// DynamoDB has a 400KB item limit; we use 300KB to leave room for other attributes
const transcriptSizeThreshold = 300 * 1024

// DynamoDBRepository provides DynamoDB operations for the meeting assistant
type DynamoDBRepository struct {
	client     *dynamodb.Client
	tableName  string
	s3Client   *s3.Client
	bucketName string
}

// NewDynamoDBRepository creates a new DynamoDB repository
func NewDynamoDBRepository(client *dynamodb.Client, tableName string) *DynamoDBRepository {
	return &DynamoDBRepository{
		client:    client,
		tableName: tableName,
	}
}

// NewDynamoDBRepositoryWithS3 creates a new DynamoDB repository with S3 support for large transcripts
func NewDynamoDBRepositoryWithS3(client *dynamodb.Client, tableName string, s3Client *s3.Client, bucketName string) *DynamoDBRepository {
	return &DynamoDBRepository{
		client:     client,
		tableName:  tableName,
		s3Client:   s3Client,
		bucketName: bucketName,
	}
}

// SetS3Client sets the S3 client for transcript overflow storage
func (r *DynamoDBRepository) SetS3Client(s3Client *s3.Client, bucketName string) {
	r.s3Client = s3Client
	r.bucketName = bucketName
}

// storeTranscript stores a transcript, using S3 if it exceeds the size threshold
// Returns the value to store in DynamoDB (either the text or an s3:// reference)
func (r *DynamoDBRepository) storeTranscript(ctx context.Context, meetingID, field, text string) (string, error) {
	if text == "" {
		return "", nil
	}

	// If small enough or no S3 client, store inline
	if len(text) < transcriptSizeThreshold || r.s3Client == nil {
		return text, nil
	}

	// Store in S3
	key := fmt.Sprintf("transcripts/%s/%s.txt", meetingID, field)
	_, err := r.s3Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(r.bucketName),
		Key:         aws.String(key),
		Body:        strings.NewReader(text),
		ContentType: aws.String("text/plain; charset=utf-8"),
	})
	if err != nil {
		return "", fmt.Errorf("failed to store transcript in S3: %w", err)
	}

	return fmt.Sprintf("s3://%s/%s", r.bucketName, key), nil
}

// loadTranscript loads a transcript, fetching from S3 if it's an S3 reference
func (r *DynamoDBRepository) loadTranscript(ctx context.Context, ref string) (string, error) {
	if ref == "" {
		return "", nil
	}

	// If not an S3 reference, return as-is
	if !strings.HasPrefix(ref, "s3://") {
		return ref, nil
	}

	// Parse S3 URL: s3://bucket/key
	if r.s3Client == nil {
		return ref, nil // Return reference as-is if no S3 client
	}

	trimmed := strings.TrimPrefix(ref, "s3://")
	parts := strings.SplitN(trimmed, "/", 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid S3 reference: %s", ref)
	}
	bucket := parts[0]
	key := parts[1]

	result, err := r.s3Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return "", fmt.Errorf("failed to load transcript from S3: %w", err)
	}
	defer result.Body.Close()

	data, err := io.ReadAll(result.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read transcript from S3: %w", err)
	}

	return string(data), nil
}

// resolveTranscripts loads transcripts from S3 if they are S3 references
func (r *DynamoDBRepository) resolveTranscripts(ctx context.Context, meeting *model.Meeting) error {
	if meeting == nil {
		return nil
	}

	var err error
	if strings.HasPrefix(meeting.TranscriptA, "s3://") {
		meeting.TranscriptA, err = r.loadTranscript(ctx, meeting.TranscriptA)
		if err != nil {
			return fmt.Errorf("failed to load transcriptA: %w", err)
		}
	}

	if strings.HasPrefix(meeting.TranscriptB, "s3://") {
		meeting.TranscriptB, err = r.loadTranscript(ctx, meeting.TranscriptB)
		if err != nil {
			return fmt.Errorf("failed to load transcriptB: %w", err)
		}
	}

	return nil
}

// CreateMeeting creates a new meeting record
func (r *DynamoDBRepository) CreateMeeting(ctx context.Context, userID, title string, date time.Time, participants []string, sttProvider string) (*model.Meeting, error) {
	meetingID := uuid.New().String()
	now := time.Now().UTC()

	meeting := &model.Meeting{
		PK:           model.PrefixUser + userID,
		SK:           model.PrefixMeeting + meetingID,
		MeetingID:    meetingID,
		UserID:       userID,
		Title:        title,
		Date:         date,
		Participants: participants,
		SttProvider:  sttProvider,
		Status:       model.StatusRecording,
		CreatedAt:    now,
		UpdatedAt:    now,
		GSI1PK:       model.PrefixUser + userID,
		GSI1SK:       now.Format(time.RFC3339),
		EntityType:   "MEETING",
	}

	item, err := attributevalue.MarshalMap(meeting)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal meeting: %w", err)
	}

	_, err = r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(r.tableName),
		Item:      item,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to put meeting: %w", err)
	}

	return meeting, nil
}

// GetMeeting retrieves a meeting by userID and meetingID
func (r *DynamoDBRepository) GetMeeting(ctx context.Context, userID, meetingID string) (*model.Meeting, error) {
	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      aws.String(r.tableName),
		ConsistentRead: aws.Bool(true),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: model.PrefixUser + userID},
			"SK": &types.AttributeValueMemberS{Value: model.PrefixMeeting + meetingID},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get meeting: %w", err)
	}

	if result.Item == nil {
		return nil, nil
	}

	var meeting model.Meeting
	if err := attributevalue.UnmarshalMap(result.Item, &meeting); err != nil {
		return nil, fmt.Errorf("failed to unmarshal meeting: %w", err)
	}

	// Resolve S3 transcript references
	if err := r.resolveTranscripts(ctx, &meeting); err != nil {
		return nil, err
	}

	return &meeting, nil
}

// GetMeetingByID retrieves a meeting by meetingID using GSI3 (PK=meetingId, SK=entityType)
// This is used for internal operations where we know the meetingID but not the owner
func (r *DynamoDBRepository) GetMeetingByID(ctx context.Context, meetingID string) (*model.Meeting, error) {
	keyEx := expression.Key("meetingId").Equal(expression.Value(meetingID)).
		And(expression.Key("entityType").Equal(expression.Value("MEETING")))
	expr, err := expression.NewBuilder().WithKeyCondition(keyEx).Build()
	if err != nil {
		return nil, fmt.Errorf("failed to build expression: %w", err)
	}

	result, err := r.client.Query(ctx, &dynamodb.QueryInput{
		TableName:                 aws.String(r.tableName),
		IndexName:                 aws.String("GSI3"),
		KeyConditionExpression:    expr.KeyCondition(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
		Limit:                     aws.Int32(1),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to query for meeting: %w", err)
	}

	if len(result.Items) == 0 {
		return nil, nil
	}

	var meeting model.Meeting
	if err := attributevalue.UnmarshalMap(result.Items[0], &meeting); err != nil {
		return nil, fmt.Errorf("failed to unmarshal meeting: %w", err)
	}

	// Resolve S3 transcript references
	if err := r.resolveTranscripts(ctx, &meeting); err != nil {
		return nil, err
	}

	return &meeting, nil
}

// MeetingKey identifies a meeting by its owner and meeting ID (primary key).
type MeetingKey struct {
	OwnerID   string
	MeetingID string
}

// BatchGetMeetings retrieves multiple meetings in a single DynamoDB BatchGetItem call.
// Requires owner IDs to construct primary keys (PK=USER#{ownerID}, SK=MEETING#{meetingID}).
func (r *DynamoDBRepository) BatchGetMeetings(ctx context.Context, keys []MeetingKey) ([]*model.Meeting, error) {
	if len(keys) == 0 {
		return nil, nil
	}

	var meetings []*model.Meeting

	// Process in chunks of 100 (BatchGetItem limit)
	for i := 0; i < len(keys); i += 100 {
		end := i + 100
		if end > len(keys) {
			end = len(keys)
		}
		chunk := keys[i:end]

		ddbKeys := make([]map[string]types.AttributeValue, len(chunk))
		for j, k := range chunk {
			ddbKeys[j] = map[string]types.AttributeValue{
				"PK": &types.AttributeValueMemberS{Value: model.PrefixUser + k.OwnerID},
				"SK": &types.AttributeValueMemberS{Value: model.PrefixMeeting + k.MeetingID},
			}
		}

		requestItems := map[string]types.KeysAndAttributes{
			r.tableName: {Keys: ddbKeys},
		}

		for len(requestItems) > 0 {
			result, err := r.client.BatchGetItem(ctx, &dynamodb.BatchGetItemInput{
				RequestItems: requestItems,
			})
			if err != nil {
				return nil, fmt.Errorf("failed to batch get meetings: %w", err)
			}

			for _, item := range result.Responses[r.tableName] {
				var meeting model.Meeting
				if err := attributevalue.UnmarshalMap(item, &meeting); err != nil {
					return nil, fmt.Errorf("failed to unmarshal meeting: %w", err)
				}
				meetings = append(meetings, &meeting)
			}

			// Retry unprocessed keys
			if len(result.UnprocessedKeys) > 0 {
				requestItems = result.UnprocessedKeys
			} else {
				break
			}
		}
	}

	return meetings, nil
}

// UpdateMeeting updates a meeting record
// Large transcripts are automatically stored in S3 to avoid DynamoDB's 400KB limit
func (r *DynamoDBRepository) UpdateMeeting(ctx context.Context, meeting *model.Meeting) error {
	meeting.UpdatedAt = time.Now().UTC()

	// Store large transcripts in S3 if S3 client is available
	if r.s3Client != nil && r.bucketName != "" {
		// Store transcriptA if needed
		if meeting.TranscriptA != "" && !strings.HasPrefix(meeting.TranscriptA, "s3://") {
			ref, err := r.storeTranscript(ctx, meeting.MeetingID, "transcriptA", meeting.TranscriptA)
			if err != nil {
				return fmt.Errorf("failed to store transcriptA: %w", err)
			}
			meeting.TranscriptA = ref
		}

		// Store transcriptB if needed
		if meeting.TranscriptB != "" && !strings.HasPrefix(meeting.TranscriptB, "s3://") {
			ref, err := r.storeTranscript(ctx, meeting.MeetingID, "transcriptB", meeting.TranscriptB)
			if err != nil {
				return fmt.Errorf("failed to store transcriptB: %w", err)
			}
			meeting.TranscriptB = ref
		}
	}

	// UpdateMeeting is a whole-item PutItem, not a partial UpdateItem -- every
	// field on the in-memory *model.Meeting the caller passes in overwrites
	// whatever is currently stored, including ProjectIDs. ProjectIDs is a
	// String Set precisely so LinkMeeting/UnlinkMeeting can mutate it with
	// atomic ADD/DELETE and never race each other -- but that guarantee means
	// nothing if this call reads a Meeting BEFORE a concurrent (Un)LinkMeeting
	// commits and then overwrites the whole item AFTER, silently reverting or
	// (since the tag is `omitempty`) deleting the link.
	//
	// A plain re-read-then-write (read projectIds, then PutItem) only
	// NARROWS that window to ordinary read-then-write latency -- it doesn't
	// close it: a Link/Unlink can still land in the gap between this read
	// and the PutItem below. Narrowing isn't enough for a field this
	// codebase's own conventions single out (AGENTS.md: "a field another
	// code path can mutate concurrently ... needs a conditional UpdateItem,
	// not a whole-item PutItem carrying a stale read-time snapshot").
	// Closing it for real means detecting the race, not just shrinking it:
	// the PutItem below carries a ConditionExpression asserting projectIds
	// still equals what was just read, and a ConditionalCheckFailedException
	// (someone changed it in between) triggers a bounded retry -- re-read,
	// re-attempt -- instead of either silently overwriting the change or
	// giving up. A failed *read* (not a lost race) still aborts the whole
	// update rather than proceeding with whatever ProjectIDs the caller's
	// in-memory Meeting happened to carry (typically nil for STT pipeline
	// callers, which never populate it) -- proceeding on a read failure
	// risks wiping every project link on one transient GetItem error, which
	// is worse than the race this exists to close.
	const maxProjectIDsAttempts = 3
	for attempt := 1; ; attempt++ {
		current, err := r.getMeetingProjectIDs(ctx, meeting.UserID, meeting.MeetingID)
		if err != nil {
			return fmt.Errorf("failed to preserve projectIds on meeting update: %w", err)
		}
		meeting.ProjectIDs = current

		item, err := attributevalue.MarshalMap(meeting)
		if err != nil {
			return fmt.Errorf("failed to marshal meeting: %w", err)
		}

		condExpr, err := projectIDsUnchangedCondition(current)
		if err != nil {
			return fmt.Errorf("build projectIds-unchanged condition: %w", err)
		}

		_, err = r.client.PutItem(ctx, &dynamodb.PutItemInput{
			TableName:                 aws.String(r.tableName),
			Item:                      item,
			ConditionExpression:       condExpr.Condition(),
			ExpressionAttributeNames:  condExpr.Names(),
			ExpressionAttributeValues: condExpr.Values(),
		})
		action, terminalErr := classifyProjectIDsPutItemErr(err, attempt, maxProjectIDsAttempts)
		if action == putItemRetryActionRetry {
			continue
		}
		return terminalErr
	}
}

// putItemRetryAction is the outcome classifyProjectIDsPutItemErr decides for
// one PutItem attempt in UpdateMeeting's projectIds-preserving retry loop.
type putItemRetryAction int

const (
	putItemRetryActionDone putItemRetryAction = iota
	putItemRetryActionRetry
)

// classifyProjectIDsPutItemErr decides UpdateMeeting's retry loop outcome for
// one PutItem attempt: retry on ConditionalCheckFailedException while
// attempts remain, a distinct terminal error once exhausted (so a caller
// can tell "gave up after retrying" apart from any other DynamoDB failure),
// nil error on success, or the wrapped original error for anything else.
// Extracted as a pure function -- per AGENTS.md's rule that
// security/correctness-critical branching be unit-testable -- so this
// decision logic can be tested without a live DynamoDB client.
func classifyProjectIDsPutItemErr(err error, attempt, maxAttempts int) (action putItemRetryAction, terminalErr error) {
	if err == nil {
		return putItemRetryActionDone, nil
	}
	var ccfe *types.ConditionalCheckFailedException
	if errors.As(err, &ccfe) {
		if attempt < maxAttempts {
			return putItemRetryActionRetry, nil
		}
		return putItemRetryActionDone, fmt.Errorf("failed to update meeting: projectIds changed concurrently %d times, giving up", maxAttempts)
	}
	return putItemRetryActionDone, fmt.Errorf("failed to update meeting: %w", err)
}

// projectIDsUnchangedCondition builds the PutItem ConditionExpression
// UpdateMeeting uses to detect (not just narrow) a concurrent
// LinkMeeting/UnlinkMeeting -- see UpdateMeeting's comment. DynamoDB
// compares Set-typed attributes by their contents, not insertion order, so
// this correctly matches a projectIds set re-read in a different element
// order than when it was written.
func projectIDsUnchangedCondition(current []string) (expression.Expression, error) {
	var cond expression.ConditionBuilder
	if len(current) == 0 {
		cond = expression.AttributeNotExists(expression.Name("projectIds"))
	} else {
		cond = expression.Name("projectIds").Equal(expression.Value(&types.AttributeValueMemberSS{Value: current}))
	}
	return expression.NewBuilder().WithCondition(cond).Build()
}

// getMeetingProjectIDs reads only the projectIds attribute of a meeting,
// via ProjectionExpression -- see UpdateMeeting's comment for why this
// read-before-write matters. A missing item or missing attribute both
// resolve to a nil slice (not an error): the caller is mid-write on a
// meeting that either doesn't exist yet or has no project links, and in
// both cases overwriting with nil is correct, not a failure to report.
func (r *DynamoDBRepository) getMeetingProjectIDs(ctx context.Context, ownerUserID, meetingID string) ([]string, error) {
	expr, err := expression.NewBuilder().
		WithProjection(expression.NamesList(expression.Name("projectIds"))).
		Build()
	if err != nil {
		return nil, fmt.Errorf("build projectIds projection: %w", err)
	}
	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      aws.String(r.tableName),
		ConsistentRead: aws.Bool(true),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: model.PrefixUser + ownerUserID},
			"SK": &types.AttributeValueMemberS{Value: model.PrefixMeeting + meetingID},
		},
		ProjectionExpression:     expr.Projection(),
		ExpressionAttributeNames: expr.Names(),
	})
	if err != nil {
		return nil, fmt.Errorf("get meeting projectIds: %w", err)
	}
	if result.Item == nil {
		return nil, nil
	}
	var projected struct {
		ProjectIDs []string `dynamodbav:"projectIds,omitempty,stringset"`
	}
	if err := attributevalue.UnmarshalMap(result.Item, &projected); err != nil {
		return nil, fmt.Errorf("unmarshal meeting projectIds: %w", err)
	}
	return projected.ProjectIDs, nil
}

// UpdateMeetingFields atomically updates only the specified fields on a meeting item
// using DynamoDB UpdateItem (SET expression). This avoids the read-modify-write race
// condition inherent in the PutItem-based UpdateMeeting method.
// Fields map keys must be DynamoDB attribute names (e.g., "status", "audioKey", "content").
func (r *DynamoDBRepository) UpdateMeetingFields(ctx context.Context, userID, meetingID string, fields map[string]interface{}) error {
	return r.updateMeetingFieldsWithCondition(ctx, userID, meetingID, expression.AttributeExists(expression.Name("PK")), fields)
}

// UpdateMeetingFieldsIfMatch is UpdateMeetingFields with an added condition
// that every field in expected must still equal its given value -- for
// callers that read a meeting, decided to write based on that read, and need
// to guard against a concurrent writer having changed the field in between.
// e.g. RediarizeMeeting conditions on status still being what its own
// GetMeeting just read: two concurrent RediarizeMeeting calls can both pass
// eligibility off the same read, but only one wins this write -- the loser
// gets ErrConditionFailed and reports "already processing" instead of both
// proceeding and racing two retrigger pipelines over the same meeting. A
// bare status match isn't enough on its own to distinguish a losing
// concurrent call from some other unrelated call that also happens to be
// mid-"transcribing" -- callers needing that distinction should also match
// on a value unique to their own attempt (e.g. diarizationSpeakerHint or a
// per-attempt token) alongside status. Returns ErrConditionFailed (not the
// raw DynamoDB SDK exception type) when the condition fails, so callers stay
// on repository-layer sentinel errors per this codebase's service/repository
// error-handling convention rather than importing dynamodb SDK types.
func (r *DynamoDBRepository) UpdateMeetingFieldsIfMatch(ctx context.Context, userID, meetingID string, expected map[string]interface{}, fields map[string]interface{}) error {
	condition := expression.AttributeExists(expression.Name("PK"))
	for k, v := range expected {
		condition = condition.And(expression.Name(k).Equal(expression.Value(v)))
	}
	return r.updateMeetingFieldsWithCondition(ctx, userID, meetingID, condition, fields)
}

func (r *DynamoDBRepository) updateMeetingFieldsWithCondition(ctx context.Context, userID, meetingID string, condition expression.ConditionBuilder, fields map[string]interface{}) error {
	// Handle S3 transcript overflow for large transcript fields
	if r.s3Client != nil && r.bucketName != "" {
		for _, field := range []string{"transcriptA", "transcriptB"} {
			if val, ok := fields[field]; ok {
				if text, isStr := val.(string); isStr && text != "" && !strings.HasPrefix(text, "s3://") {
					ref, err := r.storeTranscript(ctx, meetingID, field, text)
					if err != nil {
						return fmt.Errorf("failed to store %s: %w", field, err)
					}
					fields[field] = ref
				}
			}
		}
	}

	// Always include updatedAt
	fields["updatedAt"] = time.Now().UTC().Format(time.RFC3339Nano)

	// Build SET expression
	var update expression.UpdateBuilder
	first := true
	for k, v := range fields {
		if first {
			update = expression.Set(expression.Name(k), expression.Value(v))
			first = false
		} else {
			update = update.Set(expression.Name(k), expression.Value(v))
		}
	}

	expr, err := expression.NewBuilder().WithUpdate(update).WithCondition(condition).Build()
	if err != nil {
		return fmt.Errorf("failed to build update expression: %w", err)
	}

	_, err = r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: model.PrefixUser + userID},
			"SK": &types.AttributeValueMemberS{Value: model.PrefixMeeting + meetingID},
		},
		UpdateExpression:          expr.Update(),
		ConditionExpression:       expr.Condition(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
	})
	if err != nil {
		var ccfe *types.ConditionalCheckFailedException
		if errors.As(err, &ccfe) {
			return fmt.Errorf("%w: meeting %s condition not met", ErrConditionFailed, meetingID)
		}
		return fmt.Errorf("failed to update meeting fields: %w", err)
	}

	return nil
}

// PreAllocateAudioKeys initializes audioKeys as a list of empty strings for multi-file upload.
// Called lazily on first CompleteUpload with totalParts > 1.
// Uses ConditionExpression to be safe against concurrent calls.
func (r *DynamoDBRepository) PreAllocateAudioKeys(ctx context.Context, userID, meetingID string, totalParts int) error {
	emptyKeys := make([]string, totalParts)
	for i := range emptyKeys {
		emptyKeys[i] = ""
	}

	// (Re)initialize multi-part tracking AND reset status to transcribing so a NEW
	// multi-file batch on an already-finished meeting reprocesses cleanly. The prior
	// batch's readiness set and all-parts marker are cleared so counting restarts and
	// the merge can re-emit; audioPartCount becomes authoritative for this batch.
	update := expression.Set(expression.Name("audioKeys"), expression.Value(emptyKeys)).
		Set(expression.Name("audioPartCount"), expression.Value(totalParts)).
		Set(expression.Name("audioPartsReady"), expression.Value(0)).
		Set(expression.Name("status"), expression.Value(model.StatusTranscribing)).
		Set(expression.Name("updatedAt"), expression.Value(time.Now().UTC().Format(time.RFC3339Nano))).
		Remove(expression.Name("audioPartsReadySet")).
		Remove(expression.Name("allPartsEmittedAt"))

	// Init when this is the first part of a batch (no audioKeys yet) OR when
	// re-transcribing a finished meeting (status done/error). Subsequent parts of the
	// SAME batch see audioKeys present + status=transcribing → condition fails → no-op
	// below (the front-end uploads parts sequentially, so no race). This is also the
	// gate that stops a stray late part from resetting a finished meeting: only an
	// intentional re-upload (done/error) re-inits.
	condition := expression.And(
		expression.AttributeExists(expression.Name("PK")),
		expression.Or(
			expression.AttributeNotExists(expression.Name("audioKeys")),
			expression.Name("status").Equal(expression.Value(model.StatusDone)),
			expression.Name("status").Equal(expression.Value(model.StatusError)),
		),
	)

	expr, err := expression.NewBuilder().WithUpdate(update).WithCondition(condition).Build()
	if err != nil {
		return fmt.Errorf("failed to build pre-allocate expression: %w", err)
	}

	_, err = r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: model.PrefixUser + userID},
			"SK": &types.AttributeValueMemberS{Value: model.PrefixMeeting + meetingID},
		},
		UpdateExpression:          expr.Update(),
		ConditionExpression:       expr.Condition(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
	})
	if err != nil {
		var condErr *types.ConditionalCheckFailedException
		if ok := errors.As(err, &condErr); ok {
			return nil // already pre-allocated by concurrent call
		}
		return fmt.Errorf("failed to pre-allocate audio keys: %w", err)
	}
	return nil
}

// SetAudioKeyAtIndex sets a specific index in the audioKeys list. Idempotent — re-uploading
// the same part overwrites the same slot. Validates index is within pre-allocated range.
//
// Status transitions are gated: only `recording` or `transcribing` meetings
// flip to `transcribing`. Without this guard, a late part-upload (S3 retry,
// client clock skew) could regress a `summarizing`/`done`/`error` meeting back
// to `transcribing`, which the summarize Lambda's whitelist guard would then
// happily accept — replaying refine + Bedrock + KB export on top of finished
// content. ConditionalCheckFailedException on stale status is surfaced to the
// caller as a typed error so transcribe.go can log+skip rather than retry.
func (r *DynamoDBRepository) SetAudioKeyAtIndex(ctx context.Context, userID, meetingID, key string, partIndex int) error {
	indexPath := fmt.Sprintf("audioKeys[%d]", partIndex)

	_, err := r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: model.PrefixUser + userID},
			"SK": &types.AttributeValueMemberS{Value: model.PrefixMeeting + meetingID},
		},
		UpdateExpression: aws.String(fmt.Sprintf("SET %s = :key, #st = :transcribing, updatedAt = :now", indexPath)),
		ExpressionAttributeNames: map[string]string{
			"#st": "status",
		},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":key":          &types.AttributeValueMemberS{Value: key},
			":transcribing": &types.AttributeValueMemberS{Value: model.StatusTranscribing},
			":recording":    &types.AttributeValueMemberS{Value: model.StatusRecording},
			":now":          &types.AttributeValueMemberS{Value: time.Now().UTC().Format(time.RFC3339Nano)},
			":idx":          &types.AttributeValueMemberN{Value: strconv.Itoa(partIndex)},
		},
		ConditionExpression: aws.String(
			"attribute_exists(PK) AND attribute_exists(audioKeys) AND size(audioKeys) > :idx " +
				"AND (#st = :recording OR #st = :transcribing)",
		),
	})
	if err != nil {
		var ccfe *types.ConditionalCheckFailedException
		if errors.As(err, &ccfe) {
			return fmt.Errorf("set audio key at index %d rejected (meeting not in recording/transcribing state or index out of range): %w", partIndex, err)
		}
		return fmt.Errorf("failed to set audio key at index %d: %w", partIndex, err)
	}
	return nil
}

// IncrementAudioPartsReady atomically adds partIndex to a Number Set (audioPartsReadySet),
// making it idempotent on re-upload of the same part. Returns the set size as partsReady.
func (r *DynamoDBRepository) IncrementAudioPartsReady(ctx context.Context, userID, meetingID string, partIndex int) (partsReady, partCount int, err error) {
	result, err := r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: model.PrefixUser + userID},
			"SK": &types.AttributeValueMemberS{Value: model.PrefixMeeting + meetingID},
		},
		UpdateExpression: aws.String("ADD audioPartsReadySet :partSet SET updatedAt = :now"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":partSet": &types.AttributeValueMemberNS{Value: []string{strconv.Itoa(partIndex)}},
			":now":     &types.AttributeValueMemberS{Value: time.Now().UTC().Format(time.RFC3339Nano)},
		},
		ConditionExpression: aws.String("attribute_exists(PK)"),
		ReturnValues:        types.ReturnValueAllNew,
	})
	if err != nil {
		return 0, 0, fmt.Errorf("failed to increment audio parts ready: %w", err)
	}

	attrs := result.Attributes
	// Count ready parts from the returned Number Set
	if ns, ok := attrs["audioPartsReadySet"].(*types.AttributeValueMemberNS); ok {
		partsReady = len(ns.Value)
	}
	// Read audioPartCount from the item
	var meeting model.Meeting
	if unmarshalErr := attributevalue.UnmarshalMap(attrs, &meeting); unmarshalErr != nil {
		return 0, 0, fmt.Errorf("failed to unmarshal updated meeting: %w", unmarshalErr)
	}

	// Mirror the set size onto the `audioPartsReady` int field so the API
	// response and any list-view projection see the current count without
	// having to read the set themselves. Best-effort second write; the
	// caller has already learned `partsReady` from the set above and the
	// emit-all-parts-transcribed decision uses that, so a failure here is
	// observability-only — log and continue.
	if _, mirrorErr := r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: model.PrefixUser + userID},
			"SK": &types.AttributeValueMemberS{Value: model.PrefixMeeting + meetingID},
		},
		UpdateExpression: aws.String("SET audioPartsReady = :n"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":n": &types.AttributeValueMemberN{Value: strconv.Itoa(partsReady)},
		},
		ConditionExpression: aws.String("attribute_exists(PK)"),
	}); mirrorErr != nil {
		// Stale audioPartsReady is preferable to retrying — the set is
		// the source of truth and the emit-all-parts decision already
		// happened above. Log so CloudWatch surfaces the drift instead
		// of silently leaving the frontend progress bar stuck.
		log.Printf("Failed to mirror audioPartsReady for meeting %s (partIndex=%d): %v",
			meetingID, partIndex, mirrorErr)
	}

	return partsReady, meeting.AudioPartCount, nil
}

// ReleaseAllPartsEmit clears the `allPartsEmittedAt` lock. Use this as the
// compensation path when the caller successfully claimed the emit lock but
// then the downstream `PutEvents` call failed — without the release, a
// throttle/network failure on PutEvents would leave the meeting permanently
// `transcribing` because every retry would see `claim=false` and skip.
//
// Safe to call when the field doesn't exist (idempotent REMOVE).
func (r *DynamoDBRepository) ReleaseAllPartsEmit(ctx context.Context, userID, meetingID string) error {
	_, err := r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: model.PrefixUser + userID},
			"SK": &types.AttributeValueMemberS{Value: model.PrefixMeeting + meetingID},
		},
		UpdateExpression:    aws.String("REMOVE allPartsEmittedAt"),
		ConditionExpression: aws.String("attribute_exists(PK)"),
	})
	if err != nil {
		return fmt.Errorf("failed to release all-parts emit lock: %w", err)
	}
	return nil
}

// AllPartsEmitClaimTTL is the maximum age of a stale `allPartsEmittedAt`
// claim before it can be reclaimed by another invocation. This bounds the
// failure mode where `PutEvents` succeeded after a claim but the Lambda
// crashed before any downstream side-effect (or where `ReleaseAllPartsEmit`
// itself failed, leaving the lock orphaned). After this window, the next
// retry can re-emit. Chosen > P99 emit latency and < the 30-minute
// meeting auto-expiry so recovery happens before the meeting is errored.
const AllPartsEmitClaimTTL = 5 * time.Minute

// ClaimAllPartsEmit attempts to atomically claim the right to emit the
// `AllPartsTranscribed` EventBridge event for this meeting. Returns
// (true, nil) for the first caller that wins the conditional write;
// returns (false, nil) for every subsequent caller (lost race or
// EventBridge redelivery). Returns (false, err) on real DynamoDB errors.
//
// Used to make `emitAllPartsTranscribedEvent` once-only despite S3
// at-least-once redelivery of part transcripts.
//
// The claim is TTL-bound (`AllPartsEmitClaimTTL`): if an existing claim
// is older than the TTL, this call reclaims it. This is the self-healing
// path for the case where the previous holder claimed the lock and then
// failed to either emit or release it (e.g., Lambda OOM between claim
// and PutEvents). Callers still SHOULD invoke `ReleaseAllPartsEmit` on
// emit failure for fast recovery; the TTL is the backstop.
func (r *DynamoDBRepository) ClaimAllPartsEmit(ctx context.Context, userID, meetingID string) (bool, error) {
	now := time.Now().UTC()
	staleBefore := now.Add(-AllPartsEmitClaimTTL).Format(time.RFC3339Nano)
	_, err := r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: model.PrefixUser + userID},
			"SK": &types.AttributeValueMemberS{Value: model.PrefixMeeting + meetingID},
		},
		UpdateExpression: aws.String("SET allPartsEmittedAt = :now"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":now":         &types.AttributeValueMemberS{Value: now.Format(time.RFC3339Nano)},
			":staleBefore": &types.AttributeValueMemberS{Value: staleBefore},
		},
		// Succeed when EITHER the field has never been written, OR the
		// existing claim is older than the TTL. The string compare is
		// well-defined because RFC3339Nano is lexicographically sortable.
		ConditionExpression: aws.String(
			"attribute_exists(PK) AND (attribute_not_exists(allPartsEmittedAt) OR allPartsEmittedAt < :staleBefore)",
		),
	})
	if err != nil {
		var ccfe *types.ConditionalCheckFailedException
		if errors.As(err, &ccfe) {
			// Lost the race — another invocation holds a fresh claim.
			return false, nil
		}
		return false, fmt.Errorf("failed to claim all-parts emit lock: %w", err)
	}
	return true, nil
}

// DeleteMeeting deletes a meeting and all related items atomically using TransactWriteItems.
// DynamoDB TransactWriteItems supports up to 100 items per transaction.
func (r *DynamoDBRepository) DeleteMeeting(ctx context.Context, userID, meetingID string) error {
	// Collect all items to delete
	var transactItems []types.TransactWriteItem

	// 1. The meeting itself
	transactItems = append(transactItems, types.TransactWriteItem{
		Delete: &types.Delete{
			TableName: aws.String(r.tableName),
			Key: map[string]types.AttributeValue{
				"PK": &types.AttributeValueMemberS{Value: model.PrefixUser + userID},
				"SK": &types.AttributeValueMemberS{Value: model.PrefixMeeting + meetingID},
			},
		},
	})

	// 2. Attachments
	attachments, err := r.ListAttachments(ctx, meetingID)
	if err != nil {
		return fmt.Errorf("failed to list attachments: %w", err)
	}
	for _, att := range attachments {
		transactItems = append(transactItems, types.TransactWriteItem{
			Delete: &types.Delete{
				TableName: aws.String(r.tableName),
				Key: map[string]types.AttributeValue{
					"PK": &types.AttributeValueMemberS{Value: model.PrefixMeeting + meetingID},
					"SK": &types.AttributeValueMemberS{Value: model.PrefixAttachment + att.AttachmentID},
				},
			},
		})
	}

	// 3. Shares (both recipient and meeting records)
	shares, err := r.ListSharesForMeeting(ctx, meetingID)
	if err != nil {
		return fmt.Errorf("failed to list shares: %w", err)
	}
	for _, share := range shares {
		// Recipient record: PK=USER#{sharedToId}, SK=SHARED#{meetingId}
		transactItems = append(transactItems, types.TransactWriteItem{
			Delete: &types.Delete{
				TableName: aws.String(r.tableName),
				Key: map[string]types.AttributeValue{
					"PK": &types.AttributeValueMemberS{Value: model.PrefixUser + share.SharedToID},
					"SK": &types.AttributeValueMemberS{Value: model.PrefixShare + meetingID},
				},
			},
		})
		// Meeting record: PK=MEETING#{meetingId}, SK=SHARE_TO#{userId}
		transactItems = append(transactItems, types.TransactWriteItem{
			Delete: &types.Delete{
				TableName: aws.String(r.tableName),
				Key: map[string]types.AttributeValue{
					"PK": &types.AttributeValueMemberS{Value: model.PrefixMeeting + meetingID},
					"SK": &types.AttributeValueMemberS{Value: model.PrefixShareTo + share.SharedToID},
				},
			},
		})
	}

	// 4. Sim run (ADR-031) -- unconditional delete; a Delete against a
	// nonexistent key is a no-op, not an error, so this is safe whether or
	// not a simulation was ever run for this meeting. Exactly +1 item
	// regardless of how many times the meeting was re-simulated, because
	// SimRun is a singleton (SK=SIMRUN), not a history.
	transactItems = append(transactItems, types.TransactWriteItem{
		Delete: &types.Delete{
			TableName: aws.String(r.tableName),
			Key: map[string]types.AttributeValue{
				"PK": &types.AttributeValueMemberS{Value: model.PrefixMeeting + meetingID},
				"SK": &types.AttributeValueMemberS{Value: model.PrefixSimRun},
			},
		},
	})

	// Execute in batches of 100 (TransactWriteItems limit)
	for i := 0; i < len(transactItems); i += 100 {
		end := i + 100
		if end > len(transactItems) {
			end = len(transactItems)
		}
		_, err := r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
			TransactItems: transactItems[i:end],
		})
		if err != nil {
			return fmt.Errorf("failed to delete meeting batch: %w", err)
		}
	}

	return nil
}

// ListMeetingsParams contains parameters for listing meetings
type ListMeetingsParams struct {
	UserID string
	Tab    string // "all" or "shared"
	Cursor string // base64-encoded LastEvaluatedKey
	Limit  int32
}

// ListMeetingsResult contains the result of listing meetings
type ListMeetingsResult struct {
	Meetings   []model.Meeting
	Shares     []model.Share
	NextCursor *string
}

// paginateMeetingsPage decides ListMeetings' final page and resume cursor
// for its GSI1 filter-loop. DynamoDB's FilterExpression runs after Limit is
// applied server-side, so that loop can overshoot Limit (concatenating
// multiple Query pages) before its own stop condition fires.
//   - Overshot: truncate to exactly limit and resume from the key of the
//     last KEPT item -- not wherever the underlying query's
//     LastEvaluatedKey landed, which may be well past the cutoff. Every
//     Meeting here already carries PK/SK/GSI1PK/GSI1SK (see the
//     projection), so this is a real, resumable DynamoDB key.
//   - Otherwise (filled exactly, or under-filled because the loop's own
//     maxPages bound was hit first): resume from lastEvaluatedKey as-is.
//     nil means the GSI1 partition is genuinely exhausted (the loop's own
//     break condition); non-nil means more items exist further down
//     regardless of how many landed on this page. Forcing it to nil here
//     was a real bug -- a page that happened to fill exactly Limit lost
//     its cursor and could never see anything beyond it again, which is
//     the common case for a user with few/no account-membership rows to
//     filter out (i.e. most users' very first page).
func paginateMeetingsPage(meetings []model.Meeting, limit int32, lastEvaluatedKey map[string]types.AttributeValue) ([]model.Meeting, map[string]types.AttributeValue) {
	if len(meetings) > int(limit) {
		last := meetings[limit-1]
		resumeKey := map[string]types.AttributeValue{
			"PK":     &types.AttributeValueMemberS{Value: last.PK},
			"SK":     &types.AttributeValueMemberS{Value: last.SK},
			"GSI1PK": &types.AttributeValueMemberS{Value: last.GSI1PK},
			"GSI1SK": &types.AttributeValueMemberS{Value: last.GSI1SK},
		}
		return meetings[:limit], resumeKey
	}
	return meetings, lastEvaluatedKey
}

// ListMeetings lists meetings for a user with pagination.
// Uses ProjectionExpression to exclude transcript fields (transcriptA/B, transcriptSegments)
// and other large fields (actionItems, notes) to stay within DynamoDB's 1MB per-query limit.
// content IS included because ToMeetingListItem uses it for the 200-char summary preview.
func (r *DynamoDBRepository) ListMeetings(ctx context.Context, params ListMeetingsParams) (*ListMeetingsResult, error) {
	if params.Limit == 0 {
		params.Limit = 20
	}

	result := &ListMeetingsResult{}

	exclusiveStartKey := decodeCursor(params.Cursor)

	if params.Tab == "shared" {
		shares, nextCursor, err := r.listSharesForUserPaginated(ctx, params.UserID, params.Limit, exclusiveStartKey)
		if err != nil {
			return nil, err
		}
		result.Shares = shares
		result.NextCursor = nextCursor
	} else {
		// Query GSI1 (GSI1PK=USER#{userId}, GSI1SK=date) instead of the base
		// table's PK/SK -- SK is MEETING#{meetingId} (a UUID), so sorting by
		// it has no relationship to recency at all. Querying the base table
		// here was the actual cause of "my just-finished meeting doesn't
		// show up": a user's Nth-most-recent meeting could land outside
		// page 1 purely because its UUID happened to sort late, while
		// genuinely older meetings with "smaller" UUIDs filled the page.
		//
		// AccountMember rows share this same GSI1PK (USER#{userId}) on GSI1
		// (see account.go's ListAccountsForUser), disambiguated only by
		// GSI1SK format (date string vs "ACCOUNT#{id}") -- entityType=MEETING
		// filters them out. FilterExpression runs AFTER Limit is applied
		// server-side, so a page can come back with fewer than Limit
		// matching items even when more meetings exist further down the
		// index; loop (bounded) until enough are collected or the
		// partition is exhausted in this scan direction.
		keyEx := expression.Key("GSI1PK").Equal(expression.Value(model.PrefixUser + params.UserID))
		filterEx := expression.Name("entityType").Equal(expression.Value("MEETING"))
		proj := expression.NamesList(
			expression.Name("PK"), expression.Name("SK"),
			expression.Name("meetingId"), expression.Name("userId"),
			expression.Name("title"), expression.Name("date"),
			expression.Name("status"), expression.Name("participants"),
			expression.Name("tags"), expression.Name("createdAt"),
			expression.Name("updatedAt"), expression.Name("content"),
			expression.Name("sttProvider"), expression.Name("speakerMap"),
			expression.Name("entityType"), expression.Name("GSI1PK"),
			expression.Name("GSI1SK"), expression.Name("audioKey"),
			expression.Name("selectedTranscript"), expression.Name("duration"),
			expression.Name("sentiment"),
		)
		expr, err := expression.NewBuilder().
			WithKeyCondition(keyEx).
			WithFilter(filterEx).
			WithProjection(proj).
			Build()
		if err != nil {
			return nil, fmt.Errorf("failed to build expression: %w", err)
		}

		var meetings []model.Meeting
		const maxPages = 25 // defensive bound -- this GSI1PK partition only ever mixes in a user's own (typically few) account memberships
		for i := 0; i < maxPages; i++ {
			queryResult, err := r.client.Query(ctx, &dynamodb.QueryInput{
				TableName:                 aws.String(r.tableName),
				IndexName:                 aws.String("GSI1"),
				KeyConditionExpression:    expr.KeyCondition(),
				FilterExpression:          expr.Filter(),
				ProjectionExpression:      expr.Projection(),
				ExpressionAttributeNames:  expr.Names(),
				ExpressionAttributeValues: expr.Values(),
				Limit:                     aws.Int32(params.Limit),
				ExclusiveStartKey:         exclusiveStartKey,
				ScanIndexForward:          aws.Bool(false),
			})
			if err != nil {
				return nil, fmt.Errorf("failed to query meetings: %w", err)
			}

			var page []model.Meeting
			if err := attributevalue.UnmarshalListOfMaps(queryResult.Items, &page); err != nil {
				return nil, fmt.Errorf("failed to unmarshal meetings: %w", err)
			}
			meetings = append(meetings, page...)
			exclusiveStartKey = queryResult.LastEvaluatedKey

			if len(meetings) >= int(params.Limit) || exclusiveStartKey == nil {
				break
			}
		}

		result.Meetings, exclusiveStartKey = paginateMeetingsPage(meetings, params.Limit, exclusiveStartKey)

		if exclusiveStartKey != nil {
			cursor := encodeCursor(exclusiveStartKey)
			result.NextCursor = &cursor
		}

		if params.Tab != "shared" {
			shares, err := r.ListSharesForUser(ctx, params.UserID)
			if err != nil {
				return nil, fmt.Errorf("failed to list shares: %w", err)
			}
			result.Shares = shares
		}
	}

	return result, nil
}

// encodeCursor serializes a DynamoDB ExclusiveStartKey to a base64 cursor string.
// Only string-typed attributes (PK/SK) are supported; non-string types are logged and skipped.
func encodeCursor(key map[string]types.AttributeValue) string {
	simple := make(map[string]string, len(key))
	for k, v := range key {
		if s, ok := v.(*types.AttributeValueMemberS); ok {
			simple[k] = s.Value
		} else {
			log.Printf("encodeCursor: unsupported attribute type for key %q: %T", k, v)
		}
	}
	b, err := json.Marshal(simple)
	if err != nil {
		log.Printf("encodeCursor: json.Marshal failed: %v", err)
		return ""
	}
	return base64.StdEncoding.EncodeToString(b)
}

// decodeCursor deserializes a base64 cursor string back to a DynamoDB ExclusiveStartKey.
// All keys are assumed to be string-typed (PK/SK).
func decodeCursor(cursor string) map[string]types.AttributeValue {
	if cursor == "" {
		return nil
	}
	decoded, err := base64.StdEncoding.DecodeString(cursor)
	if err != nil {
		log.Printf("decodeCursor: invalid base64: %v", err)
		return nil
	}
	var simple map[string]string
	if err := json.Unmarshal(decoded, &simple); err != nil {
		log.Printf("decodeCursor: invalid JSON: %v", err)
		return nil
	}
	result := make(map[string]types.AttributeValue, len(simple))
	for k, v := range simple {
		result[k] = &types.AttributeValueMemberS{Value: v}
	}
	return result
}

func (r *DynamoDBRepository) listSharesForUserPaginated(ctx context.Context, userID string, limit int32, exclusiveStartKey map[string]types.AttributeValue) ([]model.Share, *string, error) {
	keyEx := expression.Key("PK").Equal(expression.Value(model.PrefixUser + userID)).
		And(expression.Key("SK").BeginsWith(model.PrefixShare))
	expr, err := expression.NewBuilder().WithKeyCondition(keyEx).Build()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to build expression: %w", err)
	}

	result, err := r.client.Query(ctx, &dynamodb.QueryInput{
		TableName:                 aws.String(r.tableName),
		KeyConditionExpression:    expr.KeyCondition(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
		Limit:                     aws.Int32(limit),
		ExclusiveStartKey:         exclusiveStartKey,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to query shares: %w", err)
	}

	var shares []model.Share
	if err := attributevalue.UnmarshalListOfMaps(result.Items, &shares); err != nil {
		return nil, nil, fmt.Errorf("failed to unmarshal shares: %w", err)
	}

	var nextCursor *string
	if result.LastEvaluatedKey != nil {
		cursor := encodeCursor(result.LastEvaluatedKey)
		nextCursor = &cursor
	}

	return shares, nextCursor, nil
}

// CreateAttachment creates a new attachment record
func (r *DynamoDBRepository) CreateAttachment(ctx context.Context, meetingID, userID, originalKey, attachType string) (*model.Attachment, error) {
	attachmentID := uuid.New().String()
	now := time.Now().UTC()

	attachment := &model.Attachment{
		PK:           model.PrefixMeeting + meetingID,
		SK:           model.PrefixAttachment + attachmentID,
		AttachmentID: attachmentID,
		MeetingID:    meetingID,
		UserID:       userID,
		OriginalKey:  originalKey,
		Type:         attachType,
		Status:       model.AttachStatusUploaded,
		CreatedAt:    now,
		EntityType:   "ATTACHMENT",
	}

	item, err := attributevalue.MarshalMap(attachment)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal attachment: %w", err)
	}

	_, err = r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(r.tableName),
		Item:      item,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to put attachment: %w", err)
	}

	return attachment, nil
}

// ListAttachments lists all attachments for a meeting
func (r *DynamoDBRepository) ListAttachments(ctx context.Context, meetingID string) ([]model.Attachment, error) {
	keyEx := expression.Key("PK").Equal(expression.Value(model.PrefixMeeting + meetingID)).
		And(expression.Key("SK").BeginsWith(model.PrefixAttachment))
	expr, err := expression.NewBuilder().WithKeyCondition(keyEx).Build()
	if err != nil {
		return nil, fmt.Errorf("failed to build expression: %w", err)
	}

	result, err := r.client.Query(ctx, &dynamodb.QueryInput{
		TableName:                 aws.String(r.tableName),
		KeyConditionExpression:    expr.KeyCondition(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to query attachments: %w", err)
	}

	var attachments []model.Attachment
	if err := attributevalue.UnmarshalListOfMaps(result.Items, &attachments); err != nil {
		return nil, fmt.Errorf("failed to unmarshal attachments: %w", err)
	}

	return attachments, nil
}

// UpdateAttachment updates an attachment record
func (r *DynamoDBRepository) UpdateAttachment(ctx context.Context, attachment *model.Attachment) error {
	item, err := attributevalue.MarshalMap(attachment)
	if err != nil {
		return fmt.Errorf("failed to marshal attachment: %w", err)
	}

	_, err = r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(r.tableName),
		Item:      item,
	})
	if err != nil {
		return fmt.Errorf("failed to update attachment: %w", err)
	}

	return nil
}

// DeleteAttachment deletes an attachment
func (r *DynamoDBRepository) DeleteAttachment(ctx context.Context, meetingID, attachmentID string) error {
	_, err := r.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: model.PrefixMeeting + meetingID},
			"SK": &types.AttributeValueMemberS{Value: model.PrefixAttachment + attachmentID},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to delete attachment: %w", err)
	}
	return nil
}

// GetAttachment retrieves an attachment by meetingID and attachmentID
func (r *DynamoDBRepository) GetAttachment(ctx context.Context, meetingID, attachmentID string) (*model.Attachment, error) {
	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: model.PrefixMeeting + meetingID},
			"SK": &types.AttributeValueMemberS{Value: model.PrefixAttachment + attachmentID},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get attachment: %w", err)
	}

	if result.Item == nil {
		return nil, nil
	}

	var attachment model.Attachment
	if err := attributevalue.UnmarshalMap(result.Item, &attachment); err != nil {
		return nil, fmt.Errorf("failed to unmarshal attachment: %w", err)
	}

	return &attachment, nil
}

// CreateShare creates share records (both recipient and meeting lookup)
func (r *DynamoDBRepository) CreateShare(ctx context.Context, meetingID, ownerID, ownerEmail, sharedToID, email, permission, origin string) (*model.Share, error) {
	if origin == model.ShareOriginAccount {
		existing, err := r.GetShare(ctx, sharedToID, meetingID)
		if err != nil {
			return nil, err
		}
		if existing != nil && existing.Origin != model.ShareOriginAccount {
			return existing, nil
		}
	}

	now := time.Now().UTC()

	// Share record for recipient lookup: PK=USER#{sharedToId}, SK=SHARED#{meetingId}
	shareForRecipient := &model.Share{
		PK:         model.PrefixUser + sharedToID,
		SK:         model.PrefixShare + meetingID,
		MeetingID:  meetingID,
		OwnerID:    ownerID,
		OwnerEmail: ownerEmail,
		SharedToID: sharedToID,
		Email:      email,
		Permission: permission,
		Origin:     origin,
		CreatedAt:  now,
		EntityType: "SHARE",
	}

	item1, err := attributevalue.MarshalMap(shareForRecipient)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal share: %w", err)
	}

	// Share record for meeting lookup: PK=MEETING#{meetingId}, SK=SHARE_TO#{userId}
	shareForMeeting := &model.Share{
		PK:         model.PrefixMeeting + meetingID,
		SK:         model.PrefixShareTo + sharedToID,
		MeetingID:  meetingID,
		OwnerID:    ownerID,
		OwnerEmail: ownerEmail,
		SharedToID: sharedToID,
		Email:      email,
		Permission: permission,
		Origin:     origin,
		CreatedAt:  now,
		EntityType: "SHARE",
	}

	item2, err := attributevalue.MarshalMap(shareForMeeting)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal share: %w", err)
	}

	// Use TransactWriteItems for atomic creation of both records
	_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: []types.TransactWriteItem{
			{Put: &types.Put{TableName: aws.String(r.tableName), Item: item1}},
			{Put: &types.Put{TableName: aws.String(r.tableName), Item: item2}},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create share: %w", err)
	}

	return shareForRecipient, nil
}

// ErrMemberRemoved is returned by CreateShareIfMember when the target user is
// no longer an account member at write time -- the caller should treat this
// as a silent skip (matching ShareMeetingToAccount's pre-existing
// member==nil skip), not an error.
var ErrMemberRemoved = errors.New("member no longer present")

// isConditionalCheckFailedTransaction reports whether every failed item in a
// TransactWriteItems cancellation was ConditionalCheckFailed specifically --
// i.e. the transaction was cancelled purely because a precondition wasn't
// met, not because of an unrelated transient failure (throttling,
// TransactionConflict, item size limits, etc.) that happened to hit while
// OTHER items in the same transaction also failed their conditions.
// TransactWriteItems cancels every item together, so a single condition
// miss and a single throttle both surface as one TransactionCanceledException
// -- inspecting err.Error() or just checking errors.As without walking
// CancellationReasons can't tell them apart, and treating a transient
// failure as a benign "precondition not met" skip (as an earlier version of
// DeleteShareIfAccountOrigin and BackfillShareOrigin did) silently drops
// real failures that should have been retried or reported.
func isConditionalCheckFailedTransaction(err error) bool {
	var tce *types.TransactionCanceledException
	if !errors.As(err, &tce) || len(tce.CancellationReasons) == 0 {
		return false
	}
	sawConditionFailure := false
	for _, reason := range tce.CancellationReasons {
		if reason.Code == nil || *reason.Code == "None" {
			continue // this item wasn't the one that caused the cancellation
		}
		if *reason.Code != "ConditionalCheckFailed" {
			return false // a non-condition failure (throttling, conflict, etc.) is in the mix
		}
		sawConditionFailure = true
	}
	return sawConditionFailure
}

// CreateShareIfMember atomically verifies account membership and creates an
// account-origin Share in a single DynamoDB transaction, closing the TOCTOU
// window between a plain GetMember check and a later CreateShare call (the
// gap where a concurrent RemoveMember could complete after the check but
// before the write, permanently orphaning the Share -- nothing re-triggers
// cleanup for an already-fully-removed member). Every Share write here is
// origin="account".
//
// Two independent conditions are enforced by the SAME transaction:
//  1. ConditionCheck on the AccountMember item (attribute_exists(PK)) --
//     fails with ErrMemberRemoved if the member was removed.
//  2. ConditionExpression on each Share Put (attribute_not_exists(PK) OR
//     (origin = :accountOrigin AND accountId = :accountID)) -- ports
//     CreateShare's existing clobber-guard (dynamodb.go's plain CreateShare,
//     added in a prior fix) into the transaction so this path can NEVER
//     overwrite a pre-existing direct share, NOR a pre-existing account-origin
//     share belonging to a DIFFERENT account (a meeting re-shared from
//     account A to account B: without the accountId half of this condition,
//     a stale/delayed A-origin write landing after B's grant was created
//     could silently clobber B's row, since both rows satisfy plain
//     origin==:accountOrigin). Conditioning on PK (not on the `origin`
//     attribute itself) matters: Origin has `dynamodbav:"origin,omitempty"`,
//     so a direct share (Origin=="") never writes the origin attribute at
//     all -- attribute_not_exists(origin) would be true for a direct share
//     too, wrongly permitting the clobber that attribute_not_exists(PK)
//     correctly excludes. The caller sees either failure mode (a genuine
//     direct share, or another account's grant) as an existing share
//     returned unchanged, matching the plain CreateShare's behavior --
//     it does not need to distinguish which case blocked the write.
func (r *DynamoDBRepository) CreateShareIfMember(ctx context.Context, meetingID, ownerID, ownerEmail, accountID, sharedToID, email, permission string) (*model.Share, error) {
	now := time.Now().UTC()

	shareForRecipient := &model.Share{
		PK:         model.PrefixUser + sharedToID,
		SK:         model.PrefixShare + meetingID,
		MeetingID:  meetingID,
		OwnerID:    ownerID,
		OwnerEmail: ownerEmail,
		SharedToID: sharedToID,
		Email:      email,
		Permission: permission,
		Origin:     model.ShareOriginAccount,
		AccountID:  accountID,
		CreatedAt:  now,
		EntityType: "SHARE",
	}
	item1, err := attributevalue.MarshalMap(shareForRecipient)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal share: %w", err)
	}

	shareForMeeting := &model.Share{
		PK:         model.PrefixMeeting + meetingID,
		SK:         model.PrefixShareTo + sharedToID,
		MeetingID:  meetingID,
		OwnerID:    ownerID,
		OwnerEmail: ownerEmail,
		SharedToID: sharedToID,
		Email:      email,
		Permission: permission,
		Origin:     model.ShareOriginAccount,
		AccountID:  accountID,
		CreatedAt:  now,
		EntityType: "SHARE",
	}
	item2, err := attributevalue.MarshalMap(shareForMeeting)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal share: %w", err)
	}

	shareExpr, err := expression.NewBuilder().WithCondition(
		expression.AttributeNotExists(expression.Name("PK")).Or(
			expression.Name("origin").Equal(expression.Value(model.ShareOriginAccount)).And(
				expression.Name("accountId").Equal(expression.Value(accountID)),
			),
		),
	).Build()
	if err != nil {
		return nil, fmt.Errorf("build share clobber-guard condition: %w", err)
	}

	memberExpr, err := expression.NewBuilder().WithCondition(
		expression.AttributeExists(expression.Name("PK")),
	).Build()
	if err != nil {
		return nil, fmt.Errorf("build member condition: %w", err)
	}

	_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: []types.TransactWriteItem{
			{
				ConditionCheck: &types.ConditionCheck{
					TableName: aws.String(r.tableName),
					Key: map[string]types.AttributeValue{
						"PK": &types.AttributeValueMemberS{Value: model.PrefixAccount + accountID},
						"SK": &types.AttributeValueMemberS{Value: model.PrefixMember + sharedToID},
					},
					ConditionExpression:       memberExpr.Condition(),
					ExpressionAttributeNames:  memberExpr.Names(),
					ExpressionAttributeValues: memberExpr.Values(),
				},
			},
			{
				Put: &types.Put{
					TableName:                 aws.String(r.tableName),
					Item:                      item1,
					ConditionExpression:       shareExpr.Condition(),
					ExpressionAttributeNames:  shareExpr.Names(),
					ExpressionAttributeValues: shareExpr.Values(),
				},
			},
			{
				Put: &types.Put{
					TableName:                 aws.String(r.tableName),
					Item:                      item2,
					ConditionExpression:       shareExpr.Condition(),
					ExpressionAttributeNames:  shareExpr.Names(),
					ExpressionAttributeValues: shareExpr.Values(),
				},
			},
		},
	})
	if err != nil {
		// Gate the item-by-item classification below on
		// isConditionalCheckFailedTransaction (same helper
		// BackfillShareOrigin/DeleteShareIfAccountOrigin use) rather than a
		// bare errors.As + non-empty-reasons check: that bare check alone
		// can't tell "every failed item failed its condition" apart from "a
		// transient error (throttling, TransactionConflict) landed on one
		// item while a condition also failed on another" -- the latter
		// would otherwise get silently classified as ErrMemberRemoved or a
		// clobber-guard hit instead of propagating as a real error.
		if isConditionalCheckFailedTransaction(err) {
			var tce *types.TransactionCanceledException
			errors.As(err, &tce)
			// Item 0 is the member ConditionCheck; items 1-2 are the Share Puts.
			if code := tce.CancellationReasons[0].Code; code != nil && *code == "ConditionalCheckFailed" {
				return nil, ErrMemberRemoved
			}
			for _, reason := range tce.CancellationReasons[1:] {
				if reason.Code != nil && *reason.Code == "ConditionalCheckFailed" {
					// A pre-existing direct share blocked the write -- same
					// outcome as the plain CreateShare's clobber-guard:
					// return the existing share, not an error.
					existing, getErr := r.GetShare(ctx, sharedToID, meetingID)
					if getErr != nil {
						return nil, getErr
					}
					return existing, nil
				}
			}
		}
		return nil, fmt.Errorf("failed to create share if member: %w", err)
	}

	return shareForRecipient, nil
}

// BackfillShareOrigin conditionally tags BOTH copies of a legacy Share
// (recipient-lookup PK=USER#{sharedToID}/SK=SHARED#{meetingID}, and
// meeting-lookup PK=MEETING#{meetingID}/SK=SHARE_TO#{sharedToID} --
// CreateShare/CreateShareIfMember always write both) as account-origin AND
// stamps accountId, in a single TransactWriteItems call. Used only by the
// one-time backfill CLI (cmd/backfill-share-origin) for Share records
// written by ShareMeetingToAccount before the Origin/AccountID fields
// existed. Stamping accountId here (not just origin) matters: without it, a
// backfilled row would have origin=="account" but accountId=="", which
// DeleteShareIfAccountOrigin's accountId-scoped condition would then refuse
// to ever delete -- reintroducing the exact un-revocable state this backfill
// exists to fix. Each item's ConditionCheck requires origin to still be
// absent/empty at write time, so a concurrent RemoveMember cleanup (which
// deletes the item) can't race this into a resurrected/half-updated state --
// and because both updates are in one transaction, either both rows end up
// tagged or neither does: there is no partially-tagged pair for a re-run to
// miss (a re-run's CLI-level candidate detection, GetShare on the recipient
// row, would otherwise see Origin=="account" already and skip
// re-attempting a still-stale meeting-lookup row).
//
// A third ConditionCheck item verifies the meeting row still has
// sharedToAccount=true and accountId==accountID at commit time -- the CLI's
// own eligibility check (GetMeetingByID) happens BEFORE this transaction,
// non-atomically, so without this the meeting could be un-shared or
// re-shared to a different account in that gap, and this call would tag the
// share for an accountID that the meeting no longer has anything to do
// with (sharebackfill.Classify would have called it VerdictOrphaned had the
// CLI re-checked at this instant). This closes that race at the actual
// write, not just at the read the CLI already does.
//
// On TransactionCanceledException this returns ErrConditionFailed and the
// caller treats that meeting/member pair as a no-op skip, not an error.
func (r *DynamoDBRepository) BackfillShareOrigin(ctx context.Context, accountID, meetingOwnerUserID, sharedToID, meetingID string) error {
	condition := expression.AttributeExists(expression.Name("PK")).And(
		expression.Or(
			expression.AttributeNotExists(expression.Name("origin")),
			expression.Name("origin").Equal(expression.Value("")),
		),
	)
	update := expression.Set(expression.Name("origin"), expression.Value(model.ShareOriginAccount)).
		Set(expression.Name("accountId"), expression.Value(accountID))
	expr, err := expression.NewBuilder().WithCondition(condition).WithUpdate(update).Build()
	if err != nil {
		return fmt.Errorf("build backfill origin condition: %w", err)
	}
	keys := []map[string]types.AttributeValue{
		{
			"PK": &types.AttributeValueMemberS{Value: model.PrefixUser + sharedToID},
			"SK": &types.AttributeValueMemberS{Value: model.PrefixShare + meetingID},
		},
		{
			"PK": &types.AttributeValueMemberS{Value: model.PrefixMeeting + meetingID},
			"SK": &types.AttributeValueMemberS{Value: model.PrefixShareTo + sharedToID},
		},
	}
	items := make([]types.TransactWriteItem, 0, len(keys)+1)
	for _, key := range keys {
		items = append(items, types.TransactWriteItem{
			Update: &types.Update{
				TableName:                 aws.String(r.tableName),
				Key:                       key,
				ConditionExpression:       expr.Condition(),
				UpdateExpression:          expr.Update(),
				ExpressionAttributeNames:  expr.Names(),
				ExpressionAttributeValues: expr.Values(),
			},
		})
	}
	meetingExpr, err := expression.NewBuilder().WithCondition(
		expression.Name("sharedToAccount").Equal(expression.Value(true)).And(
			expression.Name("accountId").Equal(expression.Value(accountID)),
		),
	).Build()
	if err != nil {
		return fmt.Errorf("build backfill meeting-state condition: %w", err)
	}
	items = append(items, types.TransactWriteItem{
		ConditionCheck: &types.ConditionCheck{
			TableName: aws.String(r.tableName),
			Key: map[string]types.AttributeValue{
				"PK": &types.AttributeValueMemberS{Value: model.PrefixUser + meetingOwnerUserID},
				"SK": &types.AttributeValueMemberS{Value: model.PrefixMeeting + meetingID},
			},
			ConditionExpression:       meetingExpr.Condition(),
			ExpressionAttributeNames:  meetingExpr.Names(),
			ExpressionAttributeValues: meetingExpr.Values(),
		},
	})
	_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: items})
	if err != nil {
		if isConditionalCheckFailedTransaction(err) {
			return fmt.Errorf("%w: share %s/%s not eligible (missing, origin already set, or meeting no longer shared to this account)", ErrConditionFailed, sharedToID, meetingID)
		}
		return fmt.Errorf("backfill share origin: %w", err)
	}
	return nil
}

// GetShare retrieves a share record
func (r *DynamoDBRepository) GetShare(ctx context.Context, sharedToID, meetingID string) (*model.Share, error) {
	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: model.PrefixUser + sharedToID},
			"SK": &types.AttributeValueMemberS{Value: model.PrefixShare + meetingID},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get share: %w", err)
	}

	if result.Item == nil {
		return nil, nil
	}

	var share model.Share
	if err := attributevalue.UnmarshalMap(result.Item, &share); err != nil {
		return nil, fmt.Errorf("failed to unmarshal share: %w", err)
	}

	return &share, nil
}

// DeleteShare deletes both share records unconditionally. Used by the
// owner-initiated RevokeShare path, where the caller has already decided
// (with fresh authorization) to revoke whatever share currently exists --
// direct or account-origin.
func (r *DynamoDBRepository) DeleteShare(ctx context.Context, sharedToID, meetingID string) error {
	// Delete both records atomically
	_, err := r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: []types.TransactWriteItem{
			{
				Delete: &types.Delete{
					TableName: aws.String(r.tableName),
					Key: map[string]types.AttributeValue{
						"PK": &types.AttributeValueMemberS{Value: model.PrefixUser + sharedToID},
						"SK": &types.AttributeValueMemberS{Value: model.PrefixShare + meetingID},
					},
				},
			},
			{
				Delete: &types.Delete{
					TableName: aws.String(r.tableName),
					Key: map[string]types.AttributeValue{
						"PK": &types.AttributeValueMemberS{Value: model.PrefixMeeting + meetingID},
						"SK": &types.AttributeValueMemberS{Value: model.PrefixShareTo + sharedToID},
					},
				},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to delete share: %w", err)
	}
	return nil
}

// DeleteShareIfAccountOrigin deletes both share records ONLY if the
// recipient-lookup row still has origin=="account" AND accountId==accountID
// at delete time, in a single transaction. Used by RemoveMember's cleanup
// loop, which decides what to delete from an earlier, separate GetShare +
// GetMeetingByID read -- the accountID condition (not just origin) is what
// actually closes the cross-account race: without it, a meeting re-shared
// from THIS account to a DIFFERENT account in the gap between that read and
// this delete would have the new account's fresh CreateShareIfMember grant
// silently deleted by the old account's RemoveMember, because both rows
// carry origin=="account" and the cleanup loop's meeting.AccountID re-check
// is a separate, non-atomic read that can't itself prevent the race -- only
// a condition on the row being deleted can. Also protects an owner's fresh
// direct share the same way the origin-only condition always did (a direct
// share has accountId=="" too, so it fails this condition either way). On
// ConditionalCheckFailedException (origin/accountId changed, or the row no
// longer exists) this returns ErrConditionFailed and the caller treats it as
// a no-op skip, matching BackfillShareOrigin's convention for the same kind
// of condition failure.
func (r *DynamoDBRepository) DeleteShareIfAccountOrigin(ctx context.Context, accountID, sharedToID, meetingID string) error {
	condition := expression.Name("origin").Equal(expression.Value(model.ShareOriginAccount)).And(
		expression.Name("accountId").Equal(expression.Value(accountID)),
	)
	expr, err := expression.NewBuilder().WithCondition(condition).Build()
	if err != nil {
		return fmt.Errorf("build delete-if-account-origin condition: %w", err)
	}
	_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: []types.TransactWriteItem{
			{
				Delete: &types.Delete{
					TableName: aws.String(r.tableName),
					Key: map[string]types.AttributeValue{
						"PK": &types.AttributeValueMemberS{Value: model.PrefixUser + sharedToID},
						"SK": &types.AttributeValueMemberS{Value: model.PrefixShare + meetingID},
					},
					ConditionExpression:       expr.Condition(),
					ExpressionAttributeNames:  expr.Names(),
					ExpressionAttributeValues: expr.Values(),
				},
			},
			{
				Delete: &types.Delete{
					TableName: aws.String(r.tableName),
					Key: map[string]types.AttributeValue{
						"PK": &types.AttributeValueMemberS{Value: model.PrefixMeeting + meetingID},
						"SK": &types.AttributeValueMemberS{Value: model.PrefixShareTo + sharedToID},
					},
					ConditionExpression:       expr.Condition(),
					ExpressionAttributeNames:  expr.Names(),
					ExpressionAttributeValues: expr.Values(),
				},
			},
		},
	})
	if err != nil {
		if isConditionalCheckFailedTransaction(err) {
			return fmt.Errorf("%w: share %s/%s no longer account-origin", ErrConditionFailed, sharedToID, meetingID)
		}
		return fmt.Errorf("failed to delete share if account origin: %w", err)
	}
	return nil
}

// ListSharesForUser lists all shares for a user (meetings shared with them)
func (r *DynamoDBRepository) ListSharesForUser(ctx context.Context, userID string) ([]model.Share, error) {
	keyEx := expression.Key("PK").Equal(expression.Value(model.PrefixUser + userID)).
		And(expression.Key("SK").BeginsWith(model.PrefixShare))
	expr, err := expression.NewBuilder().WithKeyCondition(keyEx).Build()
	if err != nil {
		return nil, fmt.Errorf("failed to build expression: %w", err)
	}

	result, err := r.client.Query(ctx, &dynamodb.QueryInput{
		TableName:                 aws.String(r.tableName),
		KeyConditionExpression:    expr.KeyCondition(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to query shares: %w", err)
	}

	var shares []model.Share
	if err := attributevalue.UnmarshalListOfMaps(result.Items, &shares); err != nil {
		return nil, fmt.Errorf("failed to unmarshal shares: %w", err)
	}

	return shares, nil
}

// ListSharesForMeeting lists all shares for a meeting. Drains every page
// (queryAllPages) rather than returning only DynamoDB's first ~1MB page --
// a caller deciding whether ANY share exists (e.g. service.AnyNonOwnerShare,
// gating cross-meeting prompt injection) must see the complete set, not a
// possibly-truncated prefix, or a share past the first page silently
// defeats the check. Eventually-consistent (DynamoDB's table default) --
// fine for the common UI-display callers, but the exfiltration gate itself
// must NOT use this: see ListSharesForMeetingConsistent.
func (r *DynamoDBRepository) ListSharesForMeeting(ctx context.Context, meetingID string) ([]model.Share, error) {
	return r.listSharesForMeeting(ctx, meetingID, false)
}

// ListSharesForMeetingConsistent is ListSharesForMeeting with
// ConsistentRead:true -- use this, not the eventually-consistent version,
// anywhere the result gates whether untrusted content may be folded into a
// prompt (service.HasNonOwnerCollaborator). Without strong consistency, a
// share or account-membership grant made an instant before summarize runs
// could be invisible to this read (TOCTOU), letting a just-added
// collaborator's injected liveSummary smuggle a linked meeting's content
// they have no read access to into a summary they DO get to read.
func (r *DynamoDBRepository) ListSharesForMeetingConsistent(ctx context.Context, meetingID string) ([]model.Share, error) {
	return r.listSharesForMeeting(ctx, meetingID, true)
}

func (r *DynamoDBRepository) listSharesForMeeting(ctx context.Context, meetingID string, consistent bool) ([]model.Share, error) {
	keyEx := expression.Key("PK").Equal(expression.Value(model.PrefixMeeting + meetingID)).
		And(expression.Key("SK").BeginsWith(model.PrefixShareTo))
	expr, err := expression.NewBuilder().WithKeyCondition(keyEx).Build()
	if err != nil {
		return nil, fmt.Errorf("failed to build expression: %w", err)
	}

	items, err := r.queryAllPages(ctx, &dynamodb.QueryInput{
		TableName:                 aws.String(r.tableName),
		KeyConditionExpression:    expr.KeyCondition(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
		ConsistentRead:            aws.Bool(consistent),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to query shares: %w", err)
	}

	var shares []model.Share
	if err := attributevalue.UnmarshalListOfMaps(items, &shares); err != nil {
		return nil, fmt.Errorf("failed to unmarshal shares: %w", err)
	}

	return shares, nil
}

// GetOrCreateUser gets or creates a user profile. The returned bool is true
// only when this call actually created the PROFILE row -- the Put is
// conditioned on attribute_not_exists(PK), so two concurrent first requests
// for the same brand-new userID can't both get created=true (AGENTS.md's
// conditional-write rule): the loser's condition fails, and it falls back to
// re-reading the winner's row instead. Callers should NOT treat created=true
// as a reliable "exactly once, forever" signal for anything beyond this
// specific PutItem, though -- see MeetingService.MaterializePendingShares,
// which deliberately does not gate on it.
func (r *DynamoDBRepository) GetOrCreateUser(ctx context.Context, userID, email, name string) (*model.User, bool, error) {
	// Try to get existing user
	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: model.PrefixUser + userID},
			"SK": &types.AttributeValueMemberS{Value: model.PrefixProfile},
		},
	})
	if err != nil {
		return nil, false, fmt.Errorf("failed to get user: %w", err)
	}

	if result.Item != nil {
		var user model.User
		if err := attributevalue.UnmarshalMap(result.Item, &user); err != nil {
			return nil, false, fmt.Errorf("failed to unmarshal user: %w", err)
		}
		return &user, false, nil
	}

	// Create new user
	now := time.Now().UTC()
	user := &model.User{
		PK:         model.PrefixUser + userID,
		SK:         model.PrefixProfile,
		UserID:     userID,
		Email:      email,
		Name:       name,
		CreatedAt:  now,
		GSI2PK:     model.PrefixEmail + strings.ToLower(email),
		GSI2SK:     model.PrefixUser + userID,
		EntityType: "USER",
	}

	item, err := attributevalue.MarshalMap(user)
	if err != nil {
		return nil, false, fmt.Errorf("failed to marshal user: %w", err)
	}

	condExpr, err := expression.NewBuilder().
		WithCondition(expression.AttributeNotExists(expression.Name("PK"))).
		Build()
	if err != nil {
		return nil, false, fmt.Errorf("failed to build condition: %w", err)
	}

	_, err = r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:                 aws.String(r.tableName),
		Item:                      item,
		ConditionExpression:       condExpr.Condition(),
		ExpressionAttributeNames:  condExpr.Names(),
		ExpressionAttributeValues: condExpr.Values(),
	})
	if err != nil {
		var ccfe *types.ConditionalCheckFailedException
		if errors.As(err, &ccfe) {
			// Lost the race to a concurrent first request -- re-read its
			// winning row instead of erroring. ConsistentRead: the winner's
			// write may not have propagated to an eventually-consistent
			// read yet even though it already won the condition check.
			existing, getErr := r.client.GetItem(ctx, &dynamodb.GetItemInput{
				TableName:      aws.String(r.tableName),
				ConsistentRead: aws.Bool(true),
				Key: map[string]types.AttributeValue{
					"PK": &types.AttributeValueMemberS{Value: model.PrefixUser + userID},
					"SK": &types.AttributeValueMemberS{Value: model.PrefixProfile},
				},
			})
			if getErr != nil {
				return nil, false, fmt.Errorf("failed to re-get user after lost create race: %w", getErr)
			}
			if existing.Item == nil {
				return nil, false, fmt.Errorf("user %s vanished after losing create race (deleted between condition-check failure and re-read)", userID)
			}
			var winner model.User
			if unmarshalErr := attributevalue.UnmarshalMap(existing.Item, &winner); unmarshalErr != nil {
				return nil, false, fmt.Errorf("failed to unmarshal user after lost create race: %w", unmarshalErr)
			}
			return &winner, false, nil
		}
		return nil, false, fmt.Errorf("failed to put user: %w", err)
	}

	return user, true, nil
}

func pendingShareKey(email, sk string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		"PK": &types.AttributeValueMemberS{Value: model.PrefixPendingShare + strings.ToLower(email)},
		"SK": &types.AttributeValueMemberS{Value: sk},
	}
}

// PutPendingShare queues an Account- or Meeting-share grant for an email
// that AddMember/ShareMeetingByEmail couldn't resolve via GetUserByEmail
// (see PendingShare's doc comment). Upserts on the (email, accountId) or
// (email, meetingId) pair -- a repeat invite to the same target simply
// refreshes the queued role/permission rather than erroring.
func (r *DynamoDBRepository) PutPendingShare(ctx context.Context, share *model.PendingShare) error {
	share.Email = strings.ToLower(share.Email)
	share.PK = model.PrefixPendingShare + share.Email
	switch share.Kind {
	case model.PendingShareKindAccount:
		share.SK = model.PrefixPendingAccount + share.AccountID
	case model.PendingShareKindMeeting:
		share.SK = model.PrefixPendingMeeting + share.MeetingID
	default:
		return fmt.Errorf("invalid pending share kind %q", share.Kind)
	}
	now := time.Now().UTC()
	share.CreatedAt = now
	share.TTL = now.Add(model.PendingShareTTL).Unix()
	share.EntityType = model.EntityTypePendingShare

	item, err := attributevalue.MarshalMap(share)
	if err != nil {
		return fmt.Errorf("failed to marshal pending share: %w", err)
	}
	_, err = r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(r.tableName),
		Item:      item,
	})
	if err != nil {
		return fmt.Errorf("failed to put pending share: %w", err)
	}
	return nil
}

// ListPendingShares returns every queued Account/Meeting grant for email,
// across all inviters. Uses queryAllPages (not a bare single Query) so a
// heavily-invited email's pending partition is never silently truncated at
// DynamoDB's 1MB page limit -- same convention as ListAccountMembers etc.
func (r *DynamoDBRepository) ListPendingShares(ctx context.Context, email string) ([]model.PendingShare, error) {
	keyEx := expression.Key("PK").Equal(expression.Value(model.PrefixPendingShare + strings.ToLower(email)))
	expr, err := expression.NewBuilder().WithKeyCondition(keyEx).Build()
	if err != nil {
		return nil, fmt.Errorf("failed to build expression: %w", err)
	}
	items, err := r.queryAllPages(ctx, &dynamodb.QueryInput{
		TableName:                 aws.String(r.tableName),
		KeyConditionExpression:    expr.KeyCondition(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list pending shares: %w", err)
	}
	var out []model.PendingShare
	if err := attributevalue.UnmarshalListOfMaps(items, &out); err != nil {
		return nil, fmt.Errorf("failed to unmarshal pending shares: %w", err)
	}
	return out, nil
}

// DeletePendingShare removes one queued grant after MeetingService.
// MaterializePendingShares has turned it into a real AccountMember/Share
// row (or found its target account/meeting gone, or its inviter no longer
// authorized, and skipped it).
func (r *DynamoDBRepository) DeletePendingShare(ctx context.Context, email, sk string) error {
	_, err := r.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(r.tableName),
		Key:       pendingShareKey(email, sk),
	})
	if err != nil {
		return fmt.Errorf("failed to delete pending share: %w", err)
	}
	return nil
}

// DeletePendingShareIfVersionMatches drops p's row only if it's still
// exactly the version that was read (matched by CreatedAt -- PutPendingShare
// upserts on the same (email, entity) key, so an unconditioned delete could
// otherwise remove a fresher re-invite that landed in the gap between a
// caller's read and this delete) --
// used to drop a PendingShare that a materialize transaction determined is
// dead (inviter lost authority, or the target already has a grant via
// another path) without racing a concurrent re-invite of the same target.
// A condition failure here means someone re-invited in the meantime; that
// fresh row isn't this call's to delete, so it's treated as a no-op, not
// an error.
func (r *DynamoDBRepository) DeletePendingShareIfVersionMatches(ctx context.Context, email string, p *model.PendingShare) error {
	expr, err := expression.NewBuilder().WithCondition(
		expression.Name("createdAt").Equal(expression.Value(p.CreatedAt)),
	).Build()
	if err != nil {
		return fmt.Errorf("build delete-version condition: %w", err)
	}
	_, err = r.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName:                 aws.String(r.tableName),
		Key:                       pendingShareKey(email, p.SK),
		ConditionExpression:       expr.Condition(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
	})
	if err != nil {
		var ccfe *types.ConditionalCheckFailedException
		if errors.As(err, &ccfe) {
			return nil
		}
		return fmt.Errorf("delete versioned pending share %s: %w", p.SK, err)
	}
	return nil
}

// transactionItemFailed reports whether TransactWriteItems item index idx
// was the (or one of the) items that failed its own condition, given a
// TransactionCanceledException with exactly wantLen reasons. Returns
// (failed, ok) -- ok is false if err isn't a TransactionCanceledException
// with the expected shape, meaning the caller should treat it as an
// ordinary transient error instead of inspecting cancellation reasons.
func transactionItemFailed(err error, idx, wantLen int) (failed bool, ok bool) {
	var tce *types.TransactionCanceledException
	if !errors.As(err, &tce) || len(tce.CancellationReasons) != wantLen {
		return false, false
	}
	return aws.ToString(tce.CancellationReasons[idx].Code) == "ConditionalCheckFailed", true
}

// MaterializePendingAccountGrant atomically re-verifies that
// p.InvitedByUserID is still RoleOwner of p.AccountID, grants userID
// membership (only if they don't already have a row), and clears the
// queued PendingShare -- all in one transaction, so nothing can observe
// "inviter verified as owner" and "membership granted" as two separable
// steps (AGENTS.md's conditional-write rule; mirrors CreateShareIfMember's
// existing pattern in this file). The transaction's own Delete is
// version-conditioned (see pendingShareDeleteCondition) so a concurrent
// re-invite (fresher role/permission) can't be silently overwritten by a
// grant using stale data -- if that condition is what failed, this returns
// (false, nil) so the caller retries with the fresh row next call.
//
// If instead the inviter-authority check or the membership Put is what
// failed (inviter lost ownership, or userID already has a grant via
// another path), the queued row is genuinely dead -- rather than leaving
// it queued (which could otherwise resurrect a deliberate LATER revocation
// of that other-path grant, up to PendingShareTTL later), it's dropped
// immediately via a separate versioned delete, and this still returns
// (true, nil): "resolved," even though nothing was granted.
func (r *DynamoDBRepository) MaterializePendingAccountGrant(ctx context.Context, p *model.PendingShare, userID, email string) (bool, error) {
	member := &model.AccountMember{
		PK:         model.PrefixAccount + p.AccountID,
		SK:         model.PrefixMember + userID,
		AccountID:  p.AccountID,
		UserID:     userID,
		Email:      email,
		Role:       p.Role,
		AddedAt:    time.Now().UTC(),
		GSI1PK:     model.PrefixUser + userID,
		GSI1SK:     model.PrefixAccount + p.AccountID,
		EntityType: model.EntityTypeAccountMember,
	}
	item, err := attributevalue.MarshalMap(member)
	if err != nil {
		return false, fmt.Errorf("marshal account member: %w", err)
	}

	inviterExpr, err := expression.NewBuilder().WithCondition(
		expression.AttributeExists(expression.Name("PK")).And(
			expression.Name("role").Equal(expression.Value(model.RoleOwner)),
		),
	).Build()
	if err != nil {
		return false, fmt.Errorf("build inviter-owner condition: %w", err)
	}
	notExistsExpr, err := expression.NewBuilder().
		WithCondition(expression.AttributeNotExists(expression.Name("PK"))).
		Build()
	if err != nil {
		return false, fmt.Errorf("build not-exists condition: %w", err)
	}
	deleteExpr, err := expression.NewBuilder().WithCondition(
		expression.Name("createdAt").Equal(expression.Value(p.CreatedAt)),
	).Build()
	if err != nil {
		return false, fmt.Errorf("build delete-version condition: %w", err)
	}

	const ( // TransactItems indices, for CancellationReasons inspection below
		idxInviter = 0
		idxPut     = 1
		idxDelete  = 2
		numItems   = 3
	)
	_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: []types.TransactWriteItem{
			{
				ConditionCheck: &types.ConditionCheck{
					TableName: aws.String(r.tableName),
					Key: map[string]types.AttributeValue{
						"PK": &types.AttributeValueMemberS{Value: model.PrefixAccount + p.AccountID},
						"SK": &types.AttributeValueMemberS{Value: model.PrefixMember + p.InvitedByUserID},
					},
					ConditionExpression:       inviterExpr.Condition(),
					ExpressionAttributeNames:  inviterExpr.Names(),
					ExpressionAttributeValues: inviterExpr.Values(),
				},
			},
			{
				Put: &types.Put{
					TableName:                 aws.String(r.tableName),
					Item:                      item,
					ConditionExpression:       notExistsExpr.Condition(),
					ExpressionAttributeNames:  notExistsExpr.Names(),
					ExpressionAttributeValues: notExistsExpr.Values(),
				},
			},
			{
				Delete: &types.Delete{
					TableName:                 aws.String(r.tableName),
					Key:                       pendingShareKey(email, p.SK),
					ConditionExpression:       deleteExpr.Condition(),
					ExpressionAttributeNames:  deleteExpr.Names(),
					ExpressionAttributeValues: deleteExpr.Values(),
				},
			},
		},
	})
	if err == nil {
		return true, nil
	}
	if deleteFailed, ok := transactionItemFailed(err, idxDelete, numItems); ok && deleteFailed {
		return false, nil
	}
	inviterFailed, ok1 := transactionItemFailed(err, idxInviter, numItems)
	putFailed, ok2 := transactionItemFailed(err, idxPut, numItems)
	if (ok1 && inviterFailed) || (ok2 && putFailed) {
		if delErr := r.DeletePendingShareIfVersionMatches(ctx, email, p); delErr != nil {
			return false, fmt.Errorf("drop dead pending account grant for %s: %w", p.AccountID, delErr)
		}
		return true, nil
	}
	return false, fmt.Errorf("materialize pending account grant for %s: %w", p.AccountID, err)
}

// MaterializePendingMeetingGrant atomically re-verifies that
// p.InvitedByUserID still owns p.MeetingID, grants userID a direct Share
// (only if they don't already have one), and clears the queued
// PendingShare -- all in one transaction. See
// MaterializePendingAccountGrant's doc comment for why this needs to be
// one atomic operation, why the Delete is version-conditioned, and why a
// dead (not just stale) grant is dropped via a separate delete rather than
// left queued.
func (r *DynamoDBRepository) MaterializePendingMeetingGrant(ctx context.Context, p *model.PendingShare, userID, email string) (bool, error) {
	now := time.Now().UTC()
	shareForRecipient := &model.Share{
		PK:         model.PrefixUser + userID,
		SK:         model.PrefixShare + p.MeetingID,
		MeetingID:  p.MeetingID,
		OwnerID:    p.InvitedByUserID,
		OwnerEmail: p.InvitedByEmail,
		SharedToID: userID,
		Email:      email,
		Permission: p.Permission,
		CreatedAt:  now,
		EntityType: "SHARE",
	}
	item1, err := attributevalue.MarshalMap(shareForRecipient)
	if err != nil {
		return false, fmt.Errorf("marshal share (recipient): %w", err)
	}
	shareForMeeting := &model.Share{
		PK:         model.PrefixMeeting + p.MeetingID,
		SK:         model.PrefixShareTo + userID,
		MeetingID:  p.MeetingID,
		OwnerID:    p.InvitedByUserID,
		OwnerEmail: p.InvitedByEmail,
		SharedToID: userID,
		Email:      email,
		Permission: p.Permission,
		CreatedAt:  now,
		EntityType: "SHARE",
	}
	item2, err := attributevalue.MarshalMap(shareForMeeting)
	if err != nil {
		return false, fmt.Errorf("marshal share (meeting): %w", err)
	}

	meetingExistsExpr, err := expression.NewBuilder().
		WithCondition(expression.AttributeExists(expression.Name("PK"))).
		Build()
	if err != nil {
		return false, fmt.Errorf("build meeting-exists condition: %w", err)
	}
	notExistsExpr, err := expression.NewBuilder().
		WithCondition(expression.AttributeNotExists(expression.Name("PK"))).
		Build()
	if err != nil {
		return false, fmt.Errorf("build not-exists condition: %w", err)
	}
	deleteExpr, err := expression.NewBuilder().WithCondition(
		expression.Name("createdAt").Equal(expression.Value(p.CreatedAt)),
	).Build()
	if err != nil {
		return false, fmt.Errorf("build delete-version condition: %w", err)
	}

	const ( // TransactItems indices, for CancellationReasons inspection below
		idxMeeting = 0
		idxPut1    = 1
		idxPut2    = 2
		idxDelete  = 3
		numItems   = 4
	)
	_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: []types.TransactWriteItem{
			{
				// PK: USER#{ownerId}, SK: MEETING#{meetingId} -- exists only
				// if InvitedByUserID still owns this exact meeting; covers
				// both "meeting deleted" and "inviter isn't the owner
				// anymore" with one condition.
				ConditionCheck: &types.ConditionCheck{
					TableName: aws.String(r.tableName),
					Key: map[string]types.AttributeValue{
						"PK": &types.AttributeValueMemberS{Value: model.PrefixUser + p.InvitedByUserID},
						"SK": &types.AttributeValueMemberS{Value: model.PrefixMeeting + p.MeetingID},
					},
					ConditionExpression:       meetingExistsExpr.Condition(),
					ExpressionAttributeNames:  meetingExistsExpr.Names(),
					ExpressionAttributeValues: meetingExistsExpr.Values(),
				},
			},
			{
				Put: &types.Put{
					TableName:                 aws.String(r.tableName),
					Item:                      item1,
					ConditionExpression:       notExistsExpr.Condition(),
					ExpressionAttributeNames:  notExistsExpr.Names(),
					ExpressionAttributeValues: notExistsExpr.Values(),
				},
			},
			{
				Put: &types.Put{
					TableName:                 aws.String(r.tableName),
					Item:                      item2,
					ConditionExpression:       notExistsExpr.Condition(),
					ExpressionAttributeNames:  notExistsExpr.Names(),
					ExpressionAttributeValues: notExistsExpr.Values(),
				},
			},
			{
				Delete: &types.Delete{
					TableName:                 aws.String(r.tableName),
					Key:                       pendingShareKey(email, p.SK),
					ConditionExpression:       deleteExpr.Condition(),
					ExpressionAttributeNames:  deleteExpr.Names(),
					ExpressionAttributeValues: deleteExpr.Values(),
				},
			},
		},
	})
	if err == nil {
		return true, nil
	}
	if deleteFailed, ok := transactionItemFailed(err, idxDelete, numItems); ok && deleteFailed {
		return false, nil
	}
	meetingFailed, ok1 := transactionItemFailed(err, idxMeeting, numItems)
	put1Failed, ok2 := transactionItemFailed(err, idxPut1, numItems)
	put2Failed, ok3 := transactionItemFailed(err, idxPut2, numItems)
	if (ok1 && meetingFailed) || (ok2 && put1Failed) || (ok3 && put2Failed) {
		if delErr := r.DeletePendingShareIfVersionMatches(ctx, email, p); delErr != nil {
			return false, fmt.Errorf("drop dead pending meeting grant for %s: %w", p.MeetingID, delErr)
		}
		return true, nil
	}
	return false, fmt.Errorf("materialize pending meeting grant for %s: %w", p.MeetingID, err)
}

// SearchUsersByEmail searches users by email prefix using GSI2
func (r *DynamoDBRepository) SearchUsersByEmail(ctx context.Context, emailPrefix string) ([]model.User, error) {
	// GSI2PK = EMAIL#{email}, so we query for prefix match
	keyEx := expression.Key("GSI2PK").BeginsWith(model.PrefixEmail + strings.ToLower(emailPrefix))
	expr, err := expression.NewBuilder().WithKeyCondition(keyEx).Build()
	if err != nil {
		return nil, fmt.Errorf("failed to build expression: %w", err)
	}

	result, err := r.client.Query(ctx, &dynamodb.QueryInput{
		TableName:                 aws.String(r.tableName),
		IndexName:                 aws.String("GSI2"),
		KeyConditionExpression:    expr.KeyCondition(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
		Limit:                     aws.Int32(10),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to query users: %w", err)
	}

	var users []model.User
	if err := attributevalue.UnmarshalListOfMaps(result.Items, &users); err != nil {
		return nil, fmt.Errorf("failed to unmarshal users: %w", err)
	}

	return users, nil
}

// GetUserByID retrieves a user by ID
func (r *DynamoDBRepository) GetUserByID(ctx context.Context, userID string) (*model.User, error) {
	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: model.PrefixUser + userID},
			"SK": &types.AttributeValueMemberS{Value: model.PrefixProfile},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get user: %w", err)
	}

	if result.Item == nil {
		return nil, nil
	}

	var user model.User
	if err := attributevalue.UnmarshalMap(result.Item, &user); err != nil {
		return nil, fmt.Errorf("failed to unmarshal user: %w", err)
	}

	return &user, nil
}

// GetUserByEmail retrieves a user by email using GSI2
func (r *DynamoDBRepository) GetUserByEmail(ctx context.Context, email string) (*model.User, error) {
	keyEx := expression.Key("GSI2PK").Equal(expression.Value(model.PrefixEmail + strings.ToLower(email)))
	expr, err := expression.NewBuilder().WithKeyCondition(keyEx).Build()
	if err != nil {
		return nil, fmt.Errorf("failed to build expression: %w", err)
	}

	result, err := r.client.Query(ctx, &dynamodb.QueryInput{
		TableName:                 aws.String(r.tableName),
		IndexName:                 aws.String("GSI2"),
		KeyConditionExpression:    expr.KeyCondition(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
		Limit:                     aws.Int32(1),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to query user by email: %w", err)
	}

	if len(result.Items) == 0 {
		return nil, nil
	}

	var user model.User
	if err := attributevalue.UnmarshalMap(result.Items[0], &user); err != nil {
		return nil, fmt.Errorf("failed to unmarshal user: %w", err)
	}

	return &user, nil
}

// batchGetMaxRetries/batchGetRetryBackoff bound BatchGetUserLastLogins'
// UnprocessedKeys retry against sustained throttling.
const (
	batchGetMaxRetries   = 5
	batchGetRetryBackoff = 100 * time.Millisecond
)

// BatchGetUserLastLogins retrieves lastLoginAt for a set of user IDs from
// their USER#{userId}/LOGIN items (written by the PostAuthentication
// trigger, see model.UserLogin). Users who have never logged in simply have
// no entry in the returned map -- callers must treat that as "unknown", not
// as a signal to zero-value it, since a zero time.Time would incorrectly
// read as "logged in at the Unix epoch" rather than "no record".
func (r *DynamoDBRepository) BatchGetUserLastLogins(ctx context.Context, userIDs []string) (map[string]time.Time, error) {
	if len(userIDs) == 0 {
		return map[string]time.Time{}, nil
	}

	result := make(map[string]time.Time, len(userIDs))

	// Process in chunks of 100 (BatchGetItem limit)
	for i := 0; i < len(userIDs); i += 100 {
		end := i + 100
		if end > len(userIDs) {
			end = len(userIDs)
		}
		chunk := userIDs[i:end]

		ddbKeys := make([]map[string]types.AttributeValue, len(chunk))
		for j, id := range chunk {
			ddbKeys[j] = map[string]types.AttributeValue{
				"PK": &types.AttributeValueMemberS{Value: model.PrefixUser + id},
				"SK": &types.AttributeValueMemberS{Value: model.SKUserLogin},
			}
		}

		requestItems := map[string]types.KeysAndAttributes{
			r.tableName: {Keys: ddbKeys},
		}

		// Bounded retry with backoff on UnprocessedKeys (throttling) -- an
		// unconditional retry loop would tight-spin against DynamoDB if the
		// table stays throttled. (Other BatchGetItem call sites in this file
		// retry UnprocessedKeys unconditionally too; not touched here --
		// keep this fix scoped to what the review flagged.)
		for attempt := 0; len(requestItems) > 0 && attempt < batchGetMaxRetries; attempt++ {
			if attempt > 0 {
				time.Sleep(batchGetRetryBackoff * time.Duration(attempt))
			}
			out, err := r.client.BatchGetItem(ctx, &dynamodb.BatchGetItemInput{
				RequestItems: requestItems,
			})
			if err != nil {
				return nil, fmt.Errorf("failed to batch get user logins: %w", err)
			}

			for _, item := range out.Responses[r.tableName] {
				var login model.UserLogin
				if err := attributevalue.UnmarshalMap(item, &login); err != nil {
					return nil, fmt.Errorf("failed to unmarshal user login: %w", err)
				}
				// A zero LastLoginAt (missing/malformed attribute -- should
				// not happen since the PostAuthentication trigger always
				// writes it, but defensively) must never be stored: callers
				// treat "present in this map" as "has a real login record",
				// and a zero time.Time reads as 1970 -- old enough to flip
				// Dormant=true for a user who may simply have no login yet.
				if login.LastLoginAt.IsZero() {
					continue
				}
				userID := strings.TrimPrefix(login.PK, model.PrefixUser)
				result[userID] = login.LastLoginAt
			}

			if len(out.UnprocessedKeys) > 0 {
				requestItems = out.UnprocessedKeys
			} else {
				requestItems = nil
			}
		}

		// Retry budget exhausted with keys still unprocessed (sustained
		// throttling) -- returning the partial result with a nil error
		// would let those users' absence from the map read as "no login
		// record" (not dormant) instead of "unknown", which is a different
		// and misleading claim. Surface it as an error so ListUsers can
		// degrade to LastLoginUnavailable=true instead.
		if len(requestItems) > 0 {
			return nil, fmt.Errorf("failed to batch get user logins: exhausted %d retries with unprocessed keys remaining", batchGetMaxRetries)
		}
	}

	return result, nil
}

// DetachDeletedUserProfile removes the GSI2 email-search keys from a user's
// PROFILE item after their Cognito account is deleted, without deleting the
// profile itself (meeting/document data referencing this userID must keep
// resolving). Without this, re-inviting the same email creates a second
// PROFILE item with the same GSI2PK, and GetUserByEmail could non-
// deterministically resolve to the dead userID instead of the new one. A
// missing profile (nothing to detach) is not an error.
func (r *DynamoDBRepository) DetachDeletedUserProfile(ctx context.Context, userID string) error {
	update := expression.Set(expression.Name("deletedAt"), expression.Value(time.Now().UTC())).
		Remove(expression.Name("GSI2PK")).
		Remove(expression.Name("GSI2SK"))
	expr, err := expression.NewBuilder().
		WithUpdate(update).
		WithCondition(expression.AttributeExists(expression.Name("PK"))).
		Build()
	if err != nil {
		return fmt.Errorf("failed to build expression: %w", err)
	}

	_, err = r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: model.PrefixUser + userID},
			"SK": &types.AttributeValueMemberS{Value: model.PrefixProfile},
		},
		UpdateExpression:          expr.Update(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
		ConditionExpression:       expr.Condition(),
	})
	if err != nil {
		var cce *types.ConditionalCheckFailedException
		if errors.As(err, &cce) {
			return nil // no profile to detach -- not an error
		}
		return fmt.Errorf("failed to detach deleted user profile: %w", err)
	}
	return nil
}

// GetIntegration retrieves an integration by userID and service
func (r *DynamoDBRepository) GetIntegration(ctx context.Context, userID, service string) (*model.Integration, error) {
	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: model.PrefixUser + userID},
			"SK": &types.AttributeValueMemberS{Value: model.PrefixIntegration + service},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get integration: %w", err)
	}

	if result.Item == nil {
		return nil, nil
	}

	var integration model.Integration
	if err := attributevalue.UnmarshalMap(result.Item, &integration); err != nil {
		return nil, fmt.Errorf("failed to unmarshal integration: %w", err)
	}

	return &integration, nil
}

// SaveIntegration saves an integration record
func (r *DynamoDBRepository) SaveIntegration(ctx context.Context, integration *model.Integration) error {
	item, err := attributevalue.MarshalMap(integration)
	if err != nil {
		return fmt.Errorf("failed to marshal integration: %w", err)
	}

	_, err = r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(r.tableName),
		Item:      item,
	})
	if err != nil {
		return fmt.Errorf("failed to save integration: %w", err)
	}

	return nil
}

// DeleteIntegration deletes an integration record
func (r *DynamoDBRepository) DeleteIntegration(ctx context.Context, userID, service string) error {
	_, err := r.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: model.PrefixUser + userID},
			"SK": &types.AttributeValueMemberS{Value: model.PrefixIntegration + service},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to delete integration: %w", err)
	}
	return nil
}

// ChatSession represents a chat session metadata item in DynamoDB
type ChatSession struct {
	SessionID     string `dynamodbav:"sessionId" json:"sessionId"`
	Title         string `dynamodbav:"title" json:"title"`
	CreatedAt     string `dynamodbav:"createdAt" json:"createdAt"`
	LastMessageAt string `dynamodbav:"lastMessageAt" json:"lastMessageAt"`
	MessageCount  int    `dynamodbav:"messageCount" json:"messageCount"`
}

// ListChatSessions returns all chat sessions for a user, newest first
func (r *DynamoDBRepository) ListChatSessions(ctx context.Context, userID string) ([]ChatSession, error) {
	keyEx := expression.Key("PK").Equal(expression.Value(model.PrefixUser + userID)).
		And(expression.Key("SK").BeginsWith("CHAT_SESSION#"))
	expr, err := expression.NewBuilder().WithKeyCondition(keyEx).Build()
	if err != nil {
		return nil, fmt.Errorf("failed to build expression: %w", err)
	}

	queryResult, err := r.client.Query(ctx, &dynamodb.QueryInput{
		TableName:                 aws.String(r.tableName),
		KeyConditionExpression:    expr.KeyCondition(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
		ScanIndexForward:          aws.Bool(false),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to query chat sessions: %w", err)
	}

	var sessions []ChatSession
	if err := attributevalue.UnmarshalListOfMaps(queryResult.Items, &sessions); err != nil {
		return nil, fmt.Errorf("failed to unmarshal chat sessions: %w", err)
	}

	return sessions, nil
}

// DeleteChatSession deletes both session metadata and session messages
func (r *DynamoDBRepository) DeleteChatSession(ctx context.Context, userID, sessionID string) error {
	// Delete session metadata: PK=USER#{userID}, SK=CHAT_SESSION#{sessionID}
	_, err := r.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: model.PrefixUser + userID},
			"SK": &types.AttributeValueMemberS{Value: "CHAT_SESSION#" + sessionID},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to delete chat session metadata: %w", err)
	}

	// Delete session messages: PK=SESSION#{userID}#{sessionID}, SK=MESSAGES
	_, err = r.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: "SESSION#" + userID + "#" + sessionID},
			"SK": &types.AttributeValueMemberS{Value: "MESSAGES"},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to delete chat session messages: %w", err)
	}

	return nil
}

// GetAllowedDomains retrieves the allowed email domains configuration
func (r *DynamoDBRepository) GetAllowedDomains(ctx context.Context) ([]string, error) {
	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: model.PrefixConfig},
			"SK": &types.AttributeValueMemberS{Value: model.ConfigSKAllowedDomains},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get allowed domains: %w", err)
	}
	if result.Item == nil {
		return nil, nil
	}

	var config model.AllowedDomainsConfig
	if err := attributevalue.UnmarshalMap(result.Item, &config); err != nil {
		return nil, fmt.Errorf("failed to unmarshal allowed domains: %w", err)
	}
	return config.Domains, nil
}

// SaveAllowedDomains saves the allowed email domains configuration
func (r *DynamoDBRepository) SaveAllowedDomains(ctx context.Context, domains []string, updatedBy string) error {
	config := &model.AllowedDomainsConfig{
		PK:         model.PrefixConfig,
		SK:         model.ConfigSKAllowedDomains,
		Domains:    domains,
		UpdatedAt:  time.Now().UTC(),
		UpdatedBy:  updatedBy,
		EntityType: "CONFIG",
	}
	item, err := attributevalue.MarshalMap(config)
	if err != nil {
		return fmt.Errorf("failed to marshal allowed domains: %w", err)
	}
	_, err = r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(r.tableName),
		Item:      item,
	})
	if err != nil {
		return fmt.Errorf("failed to save allowed domains: %w", err)
	}
	return nil
}
