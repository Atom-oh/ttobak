package repository

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
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

	item, err := attributevalue.MarshalMap(meeting)
	if err != nil {
		return fmt.Errorf("failed to marshal meeting: %w", err)
	}

	_, err = r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(r.tableName),
		Item:      item,
	})
	if err != nil {
		return fmt.Errorf("failed to update meeting: %w", err)
	}

	return nil
}

// UpdateMeetingFields atomically updates only the specified fields on a meeting item
// using DynamoDB UpdateItem (SET expression). This avoids the read-modify-write race
// condition inherent in the PutItem-based UpdateMeeting method.
// Fields map keys must be DynamoDB attribute names (e.g., "status", "audioKey", "content").
func (r *DynamoDBRepository) UpdateMeetingFields(ctx context.Context, userID, meetingID string, fields map[string]interface{}) error {
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

	// Condition: item must already exist
	condition := expression.AttributeExists(expression.Name("PK"))

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
		return fmt.Errorf("failed to update meeting fields: %w", err)
	}

	return nil
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
	UserID     string
	Tab        string // "all" or "shared"
	Cursor     string // base64-encoded LastEvaluatedKey
	Limit      int32
}

// ListMeetingsResult contains the result of listing meetings
type ListMeetingsResult struct {
	Meetings   []model.Meeting
	Shares     []model.Share
	NextCursor *string
}

// ListMeetings lists meetings for a user with pagination.
// Uses ProjectionExpression to exclude large fields (transcripts, actionItems, notes)
// and avoid DynamoDB's 1MB per-query response size limit.
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
		keyEx := expression.Key("PK").Equal(expression.Value(model.PrefixUser + params.UserID)).
			And(expression.Key("SK").BeginsWith(model.PrefixMeeting))
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
		)
		expr, err := expression.NewBuilder().
			WithKeyCondition(keyEx).
			WithProjection(proj).
			Build()
		if err != nil {
			return nil, fmt.Errorf("failed to build expression: %w", err)
		}

		queryResult, err := r.client.Query(ctx, &dynamodb.QueryInput{
			TableName:                 aws.String(r.tableName),
			KeyConditionExpression:    expr.KeyCondition(),
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

		if err := attributevalue.UnmarshalListOfMaps(queryResult.Items, &result.Meetings); err != nil {
			return nil, fmt.Errorf("failed to unmarshal meetings: %w", err)
		}

		if queryResult.LastEvaluatedKey != nil {
			cursor := encodeCursor(queryResult.LastEvaluatedKey)
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
func decodeCursor(cursor string) map[string]types.AttributeValue {
	if cursor == "" {
		return nil
	}
	decoded, err := base64.StdEncoding.DecodeString(cursor)
	if err != nil {
		return nil
	}
	var simple map[string]string
	if err := json.Unmarshal(decoded, &simple); err != nil {
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
func (r *DynamoDBRepository) CreateShare(ctx context.Context, meetingID, ownerID, ownerEmail, sharedToID, email, permission string) (*model.Share, error) {
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

// DeleteShare deletes both share records
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

// ListSharesForMeeting lists all shares for a meeting
func (r *DynamoDBRepository) ListSharesForMeeting(ctx context.Context, meetingID string) ([]model.Share, error) {
	keyEx := expression.Key("PK").Equal(expression.Value(model.PrefixMeeting + meetingID)).
		And(expression.Key("SK").BeginsWith(model.PrefixShareTo))
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

// GetOrCreateUser gets or creates a user profile
func (r *DynamoDBRepository) GetOrCreateUser(ctx context.Context, userID, email, name string) (*model.User, error) {
	// Try to get existing user
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

	if result.Item != nil {
		var user model.User
		if err := attributevalue.UnmarshalMap(result.Item, &user); err != nil {
			return nil, fmt.Errorf("failed to unmarshal user: %w", err)
		}
		return &user, nil
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
		return nil, fmt.Errorf("failed to marshal user: %w", err)
	}

	_, err = r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(r.tableName),
		Item:      item,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to put user: %w", err)
	}

	return user, nil
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
