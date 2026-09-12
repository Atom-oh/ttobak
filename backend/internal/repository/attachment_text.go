package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/google/uuid"
	"github.com/ttobak/backend/internal/model"
)

func attachmentTextKey(meetingID, attachmentID string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		"PK": &types.AttributeValueMemberS{Value: model.PrefixMeeting + meetingID},
		"SK": &types.AttributeValueMemberS{Value: model.PrefixAttachmentText + attachmentID},
	}
}

func (r *DynamoDBRepository) GetAttachmentText(ctx context.Context, meetingID, attachmentID string) (*model.AttachmentTextState, error) {
	out, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName), Key: attachmentTextKey(meetingID, attachmentID), ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return nil, err
	}
	if len(out.Item) == 0 {
		return nil, nil
	}
	var state model.AttachmentTextState
	if err := attributevalue.UnmarshalMap(out.Item, &state); err != nil {
		return nil, fmt.Errorf("decode attachment text state: %w", err)
	}
	return &state, nil
}

// deleteAttachmentTextStates runs after deleting the parent meeting. Strong
// pagination catches states missed by the earlier attachment enumeration.
func (r *DynamoDBRepository) deleteAttachmentTextStates(ctx context.Context, meetingID string) error {
	expr, err := expression.NewBuilder().
		WithKeyCondition(expression.Key("PK").Equal(expression.Value(model.PrefixMeeting + meetingID)).
			And(expression.Key("SK").BeginsWith(model.PrefixAttachmentText))).
		WithProjection(expression.NamesList(expression.Name("PK"), expression.Name("SK"))).Build()
	if err != nil {
		return err
	}
	pages := dynamodb.NewQueryPaginator(r.client, &dynamodb.QueryInput{
		TableName: aws.String(r.tableName), ConsistentRead: aws.Bool(true),
		KeyConditionExpression: expr.KeyCondition(), ProjectionExpression: expr.Projection(),
		ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(),
	})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("list remaining attachment states: %w", err)
		}
		for start := 0; start < len(page.Items); start += 100 {
			items := page.Items[start:min(start+100, len(page.Items))]
			deletes := make([]types.TransactWriteItem, 0, len(items))
			for _, item := range items {
				deletes = append(deletes, types.TransactWriteItem{Delete: &types.Delete{
					TableName: aws.String(r.tableName),
					Key:       map[string]types.AttributeValue{"PK": item["PK"], "SK": item["SK"]},
				}})
			}
			if _, err := r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: deletes}); err != nil {
				return fmt.Errorf("delete remaining attachment states: %w", err)
			}
		}
	}
	return nil
}

func (r *DynamoDBRepository) textSourceCheck(key map[string]types.AttributeValue, condition expression.ConditionBuilder) (*types.ConditionCheck, error) {
	expr, err := expression.NewBuilder().WithCondition(condition).Build()
	if err != nil {
		return nil, err
	}
	return &types.ConditionCheck{TableName: aws.String(r.tableName), Key: key,
		ConditionExpression: expr.Condition(), ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values()}, nil
}

func (r *DynamoDBRepository) QueueAttachmentText(ctx context.Context, attachment *model.Attachment, prior, next *model.AttachmentTextState) error {
	return r.startAttachmentText(ctx, attachment, prior, next, model.AttachmentTextQueued)
}

func (r *DynamoDBRepository) SaveUnsupportedAttachmentText(ctx context.Context, attachment *model.Attachment, prior, next *model.AttachmentTextState) error {
	return r.startAttachmentText(ctx, attachment, prior, next, model.AttachmentTextFailed)
}

func (r *DynamoDBRepository) startAttachmentText(ctx context.Context, attachment *model.Attachment, prior, next *model.AttachmentTextState, status string) error {
	parent, err := r.textSourceCheck(meetingKey(next.OwnerID, attachment.MeetingID),
		expression.AttributeExists(expression.Name("PK")).
			And(expression.Name("meetingId").Equal(expression.Value(attachment.MeetingID))).
			And(expression.Name("userId").Equal(expression.Value(next.OwnerID))))
	if err != nil {
		return err
	}
	source, err := r.textSourceCheck(map[string]types.AttributeValue{
		"PK": &types.AttributeValueMemberS{Value: model.PrefixMeeting + attachment.MeetingID},
		"SK": &types.AttributeValueMemberS{Value: model.PrefixAttachment + attachment.AttachmentID},
	}, expression.AttributeExists(expression.Name("PK")).
		And(expression.Name("attachmentId").Equal(expression.Value(attachment.AttachmentID))).
		And(expression.Name("originalKey").Equal(expression.Value(attachment.OriginalKey))).
		And(expression.Name("meetingId").Equal(expression.Value(attachment.MeetingID))).
		And(expression.Name("userId").Equal(expression.Value(attachment.UserID))))
	if err != nil {
		return err
	}
	condition := expression.AttributeNotExists(expression.Name("PK"))
	if prior != nil {
		condition = analysisRunCondition(prior.RunID, prior.Status).
			And(expression.Name("leaseUntil").Equal(expression.Value(prior.LeaseUntil)))
		if prior.Pending() {
			condition = condition.And(expression.Name("leaseUntil").LessThanEqual(expression.Value(next.UpdatedAt.UnixMilli())))
		}
	}
	update := expression.Set(expression.Name("runId"), expression.Value(next.RunID)).
		Set(expression.Name("status"), expression.Value(status)).
		Set(expression.Name("sourceKey"), expression.Value(next.SourceKey)).
		Set(expression.Name("ownerId"), expression.Value(next.OwnerID)).
		Set(expression.Name("uploaderId"), expression.Value(next.UploaderID)).
		Set(expression.Name("leaseUntil"), expression.Value(next.LeaseUntil)).
		Set(expression.Name("updatedAt"), expression.Value(next.UpdatedAt)).
		Set(expression.Name("errorCode"), expression.Value(next.ErrorCode)).
		Set(expression.Name("entityType"), expression.Value("ATTACHMENT_TEXT"))
	state, err := analysisUpdate(r.tableName, attachmentTextKey(attachment.MeetingID, attachment.AttachmentID), update, condition)
	if err != nil {
		return err
	}
	_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: []types.TransactWriteItem{{ConditionCheck: parent}, {ConditionCheck: source}, {Update: state}},
	}, func(options *dynamodb.Options) { options.Retryer = aws.NopRetryer{} })
	return analysisWriteError(err)
}

// CreateFileAttachment saves all metadata together and checks that its
// owner parent still exists. A later whole-item metadata PUT is unnecessary.
func (r *DynamoDBRepository) CreateFileAttachment(ctx context.Context, ownerID string, req *model.UploadCompleteRequest, attachType string) (*model.Attachment, error) {
	att := &model.Attachment{AttachmentID: uuid.NewString(), MeetingID: req.MeetingID,
		UserID: ownerID, OriginalKey: req.Key, Type: attachType, Status: model.AttachStatusDone,
		FileName: req.FileName, FileSize: req.FileSize, MimeType: req.MimeType,
		CreatedAt: time.Now().UTC(), EntityType: "ATTACHMENT"}
	att.PK, att.SK = model.PrefixMeeting+req.MeetingID, model.PrefixAttachment+att.AttachmentID
	parent, err := r.textSourceCheck(meetingKey(ownerID, req.MeetingID),
		expression.AttributeExists(expression.Name("PK")).
			And(expression.Name("userId").Equal(expression.Value(ownerID))).
			And(expression.Name("meetingId").Equal(expression.Value(req.MeetingID))))
	if err != nil {
		return nil, err
	}
	item, err := attributevalue.MarshalMap(att)
	if err != nil {
		return nil, err
	}
	expr, err := expression.NewBuilder().WithCondition(expression.AttributeNotExists(expression.Name("PK"))).Build()
	if err != nil {
		return nil, err
	}
	_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: []types.TransactWriteItem{
		{ConditionCheck: parent},
		{Put: &types.Put{TableName: aws.String(r.tableName), Item: item, ConditionExpression: expr.Condition(), ExpressionAttributeNames: expr.Names()}},
	}}, func(options *dynamodb.Options) { options.Retryer = aws.NopRetryer{} })
	if err != nil {
		return nil, analysisWriteError(err)
	}
	return att, nil
}

func (r *DynamoDBRepository) FailAttachmentText(ctx context.Context, meetingID, attachmentID string, expected *model.AttachmentTextState, code string, now time.Time) error {
	condition := analysisRunCondition(expected.RunID, expected.Status).
		And(expression.Name("leaseUntil").Equal(expression.Value(expected.LeaseUntil)))
	update := expression.Set(expression.Name("status"), expression.Value(model.AttachmentTextFailed)).
		Set(expression.Name("errorCode"), expression.Value(code)).
		Set(expression.Name("updatedAt"), expression.Value(now)).
		Set(expression.Name("leaseUntil"), expression.Value(int64(0)))
	op, err := analysisUpdate(r.tableName, attachmentTextKey(meetingID, attachmentID), update, condition)
	if err != nil {
		return err
	}
	_, err = r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: op.TableName, Key: op.Key, UpdateExpression: op.UpdateExpression,
		ConditionExpression: op.ConditionExpression, ExpressionAttributeNames: op.ExpressionAttributeNames,
		ExpressionAttributeValues: op.ExpressionAttributeValues,
	})
	return analysisWriteError(err)
}
