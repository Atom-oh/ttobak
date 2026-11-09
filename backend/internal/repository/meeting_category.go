package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/ttobak/backend/internal/model"
)

// SetMeetingCategory sets (or, for an empty category, removes) the owner's
// internal meeting category. The write is conditional on the meeting existing
// without an account link, so an internal category never coexists with an
// account classification even against a concurrent link.
func (r *DynamoDBRepository) SetMeetingCategory(ctx context.Context, userID, meetingID, category string) error {
	if !model.ValidMeetingCategory(category) {
		return fmt.Errorf("invalid meeting category")
	}
	now := expression.Value(time.Now().UTC().Format(time.RFC3339Nano))
	var update expression.UpdateBuilder
	if category == model.MeetingCategoryNone {
		update = expression.Set(expression.Name("updatedAt"), now).Remove(expression.Name("meetingCategory"))
	} else {
		update = expression.Set(expression.Name("updatedAt"), now).
			Set(expression.Name("meetingCategory"), expression.Value(category))
	}
	condition := expression.AttributeExists(expression.Name("PK")).And(
		expression.AttributeNotExists(expression.Name("accountId")).
			Or(expression.Name("accountId").Equal(expression.Value(""))))
	expr, err := expression.NewBuilder().WithUpdate(update).WithCondition(condition).Build()
	if err != nil {
		return fmt.Errorf("failed to build meeting category update: %w", err)
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
			return ErrConditionFailed
		}
		return fmt.Errorf("failed to set meeting category: %w", err)
	}
	return nil
}
