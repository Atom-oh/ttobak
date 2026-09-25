package repository

import (
	"context"
	"errors"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
)

// BindRecoveredRecording never retries an ambiguous write: a later condition
// failure must not authorize deletion of an audio object already committed.
func (r *DynamoDBRepository) BindRecoveredRecording(ctx context.Context, meeting *model.Meeting, key string) error {
	condition := expression.Name("status").Equal(expression.Value(meeting.Status)).
		And(expression.Name("updatedAt").Equal(expression.Value(meeting.UpdatedAt.Format(time.RFC3339Nano))))
	for _, field := range []string{"audioKey", "transcriptA", "transcriptB", "content"} {
		condition = condition.And(expression.AttributeNotExists(expression.Name(field)).Or(expression.Name(field).Equal(expression.Value(""))))
	}
	condition = condition.And(expression.AttributeNotExists(expression.Name("audioKeys")).Or(expression.Size(expression.Name("audioKeys")).Equal(expression.Value(0))))
	expr, err := expression.NewBuilder().WithCondition(condition).WithUpdate(
		expression.Set(expression.Name("audioKey"), expression.Value(key)).
			Set(expression.Name("status"), expression.Value(model.StatusTranscribing)).
			Set(expression.Name("updatedAt"), expression.Value(time.Now().UTC().Format(time.RFC3339Nano))),
	).Build()
	if err != nil {
		return err
	}
	_, err = r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(r.tableName), Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: model.PrefixUser + meeting.UserID},
			"SK": &types.AttributeValueMemberS{Value: model.PrefixMeeting + meeting.MeetingID},
		}, UpdateExpression: expr.Update(), ConditionExpression: expr.Condition(),
		ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(),
	}, singleDynamoDBAttempt)
	var conflict *types.ConditionalCheckFailedException
	if errors.As(err, &conflict) {
		return ErrConditionFailed
	}
	return err
}
