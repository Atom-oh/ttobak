package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
)

// SaveImageResult publishes only onto the same unfinished attachment. A retry
// cannot resurrect a deletion or replace another attempt's completed result.
func (r *DynamoDBRepository) SaveImageResult(ctx context.Context, expected *model.Attachment, kind, content string) error {
	if expected == nil || expected.AttachmentID == "" || expected.MeetingID == "" ||
		(expected.Status != model.AttachStatusUploaded && expected.Status != model.AttachStatusProcessing) {
		return ErrConditionFailed
	}
	condition := expression.AttributeExists(expression.Name("PK")).
		And(expression.Name("attachmentId").Equal(expression.Value(expected.AttachmentID))).
		And(expression.Name("meetingId").Equal(expression.Value(expected.MeetingID))).
		And(expression.Name("userId").Equal(expression.Value(expected.UserID))).
		And(expression.Name("originalKey").Equal(expression.Value(expected.OriginalKey))).
		And(expression.Name("status").Equal(expression.Value(expected.Status))).
		And(expression.Name("type").Equal(expression.Value(expected.Type)))
	prior := expression.Name("processedContent").Equal(expression.Value(expected.ProcessedContent))
	if expected.ProcessedContent == "" {
		prior = prior.Or(expression.AttributeNotExists(expression.Name("processedContent")))
	}
	condition = condition.And(prior)
	update := expression.Set(expression.Name("type"), expression.Value(kind)).
		Set(expression.Name("processedContent"), expression.Value(content)).
		Set(expression.Name("status"), expression.Value(model.AttachStatusDone))
	expr, err := expression.NewBuilder().WithCondition(condition).WithUpdate(update).Build()
	if err != nil {
		return fmt.Errorf("build image publication: %w", err)
	}
	_, err = r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: model.PrefixMeeting + expected.MeetingID},
			"SK": &types.AttributeValueMemberS{Value: model.PrefixAttachment + expected.AttachmentID},
		},
		UpdateExpression: expr.Update(), ConditionExpression: expr.Condition(),
		ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(),
	})
	var rejected *types.ConditionalCheckFailedException
	if errors.As(err, &rejected) {
		return ErrConditionFailed
	}
	return err
}
