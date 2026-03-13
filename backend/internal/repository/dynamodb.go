package repository

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/google/uuid"
	"github.com/ttobak/backend/internal/model"
)

// DynamoDBRepository provides DynamoDB operations for the meeting assistant
type DynamoDBRepository struct {
	client    *dynamodb.Client
	tableName string
}

// NewDynamoDBRepository creates a new DynamoDB repository
func NewDynamoDBRepository(client *dynamodb.Client, tableName string) *DynamoDBRepository {
	return &DynamoDBRepository{
		client:    client,
		tableName: tableName,
	}
}

// CreateMeeting creates a new meeting record
func (r *DynamoDBRepository) CreateMeeting(ctx context.Context, userID, title string, date time.Time, participants []string) (*model.Meeting, error) {
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
		TableName: aws.String(r.tableName),
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

	return &meeting, nil
}

// GetMeetingByID retrieves a meeting by meetingID by scanning all users
// This is used for internal operations where we know the meetingID but not the owner
func (r *DynamoDBRepository) GetMeetingByID(ctx context.Context, meetingID string) (*model.Meeting, error) {
	// Use GSI1 to find the meeting - GSI1PK is USER#{userId}, GSI1SK is timestamp
	// We need to scan or use a different approach since meetingID is in SK
	// Let's query by SK pattern using a scan with filter
	filterEx := expression.Name("meetingId").Equal(expression.Value(meetingID)).
		And(expression.Name("entityType").Equal(expression.Value("MEETING")))
	expr, err := expression.NewBuilder().WithFilter(filterEx).Build()
	if err != nil {
		return nil, fmt.Errorf("failed to build expression: %w", err)
	}

	result, err := r.client.Scan(ctx, &dynamodb.ScanInput{
		TableName:                 aws.String(r.tableName),
		FilterExpression:          expr.Filter(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
		Limit:                     aws.Int32(1),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to scan for meeting: %w", err)
	}

	if len(result.Items) == 0 {
		return nil, nil
	}

	var meeting model.Meeting
	if err := attributevalue.UnmarshalMap(result.Items[0], &meeting); err != nil {
		return nil, fmt.Errorf("failed to unmarshal meeting: %w", err)
	}

	return &meeting, nil
}

// BatchGetMeetings retrieves multiple meetings by their meetingIDs using a single scan.
// This avoids N+1 queries when loading shared meetings.
func (r *DynamoDBRepository) BatchGetMeetings(ctx context.Context, meetingIDs []string) ([]*model.Meeting, error) {
	if len(meetingIDs) == 0 {
		return nil, nil
	}

	// For a single meetingID, use the existing method
	if len(meetingIDs) == 1 {
		meeting, err := r.GetMeetingByID(ctx, meetingIDs[0])
		if err != nil {
			return nil, err
		}
		if meeting != nil {
			return []*model.Meeting{meeting}, nil
		}
		return nil, nil
	}

	// Build filter: entityType = MEETING AND meetingId IN (id1, id2, ...)
	values := make([]expression.OperandBuilder, len(meetingIDs))
	for i, id := range meetingIDs {
		values[i] = expression.Value(id)
	}

	filterEx := expression.Name("entityType").Equal(expression.Value("MEETING")).
		And(expression.Name("meetingId").In(values[0], values[1:]...))
	expr, err := expression.NewBuilder().WithFilter(filterEx).Build()
	if err != nil {
		return nil, fmt.Errorf("failed to build expression: %w", err)
	}

	result, err := r.client.Scan(ctx, &dynamodb.ScanInput{
		TableName:                 aws.String(r.tableName),
		FilterExpression:          expr.Filter(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to scan for meetings: %w", err)
	}

	var meetings []*model.Meeting
	for _, item := range result.Items {
		var meeting model.Meeting
		if err := attributevalue.UnmarshalMap(item, &meeting); err != nil {
			continue
		}
		meetings = append(meetings, &meeting)
	}

	return meetings, nil
}

// UpdateMeeting updates a meeting record
func (r *DynamoDBRepository) UpdateMeeting(ctx context.Context, meeting *model.Meeting) error {
	meeting.UpdatedAt = time.Now().UTC()

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

// DeleteMeeting deletes a meeting and all related items
func (r *DynamoDBRepository) DeleteMeeting(ctx context.Context, userID, meetingID string) error {
	// Delete the meeting
	_, err := r.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: model.PrefixUser + userID},
			"SK": &types.AttributeValueMemberS{Value: model.PrefixMeeting + meetingID},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to delete meeting: %w", err)
	}

	// Delete attachments
	attachments, err := r.ListAttachments(ctx, meetingID)
	if err != nil {
		return fmt.Errorf("failed to list attachments: %w", err)
	}
	for _, att := range attachments {
		if err := r.DeleteAttachment(ctx, meetingID, att.AttachmentID); err != nil {
			return fmt.Errorf("failed to delete attachment: %w", err)
		}
	}

	// Delete shares for meeting (MEETING#{meetingId}, SHARE_TO#)
	shares, err := r.ListSharesForMeeting(ctx, meetingID)
	if err != nil {
		return fmt.Errorf("failed to list shares: %w", err)
	}
	for _, share := range shares {
		// Delete both share records (recipient's and meeting's)
		if err := r.DeleteShare(ctx, share.SharedToID, meetingID); err != nil {
			return fmt.Errorf("failed to delete share: %w", err)
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

// ListMeetings lists meetings for a user with pagination
func (r *DynamoDBRepository) ListMeetings(ctx context.Context, params ListMeetingsParams) (*ListMeetingsResult, error) {
	if params.Limit == 0 {
		params.Limit = 20
	}

	result := &ListMeetingsResult{}

	// Decode cursor if provided
	var exclusiveStartKey map[string]types.AttributeValue
	if params.Cursor != "" {
		decoded, err := base64.StdEncoding.DecodeString(params.Cursor)
		if err == nil {
			json.Unmarshal(decoded, &exclusiveStartKey)
		}
	}

	if params.Tab == "shared" {
		// Query only shared meetings
		shares, nextCursor, err := r.listSharesForUserPaginated(ctx, params.UserID, params.Limit, exclusiveStartKey)
		if err != nil {
			return nil, err
		}
		result.Shares = shares
		result.NextCursor = nextCursor
	} else {
		// Query owned meetings
		keyEx := expression.Key("PK").Equal(expression.Value(model.PrefixUser + params.UserID)).
			And(expression.Key("SK").BeginsWith(model.PrefixMeeting))
		expr, err := expression.NewBuilder().WithKeyCondition(keyEx).Build()
		if err != nil {
			return nil, fmt.Errorf("failed to build expression: %w", err)
		}

		queryResult, err := r.client.Query(ctx, &dynamodb.QueryInput{
			TableName:                 aws.String(r.tableName),
			KeyConditionExpression:    expr.KeyCondition(),
			ExpressionAttributeNames:  expr.Names(),
			ExpressionAttributeValues: expr.Values(),
			Limit:                     aws.Int32(params.Limit),
			ExclusiveStartKey:         exclusiveStartKey,
			ScanIndexForward:          aws.Bool(false), // newest first
		})
		if err != nil {
			return nil, fmt.Errorf("failed to query meetings: %w", err)
		}

		if err := attributevalue.UnmarshalListOfMaps(queryResult.Items, &result.Meetings); err != nil {
			return nil, fmt.Errorf("failed to unmarshal meetings: %w", err)
		}

		// Encode next cursor
		if queryResult.LastEvaluatedKey != nil {
			cursorBytes, _ := json.Marshal(queryResult.LastEvaluatedKey)
			cursor := base64.StdEncoding.EncodeToString(cursorBytes)
			result.NextCursor = &cursor
		}

		// Also get shared meetings if tab is "all"
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
		cursorBytes, _ := json.Marshal(result.LastEvaluatedKey)
		cursor := base64.StdEncoding.EncodeToString(cursorBytes)
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
