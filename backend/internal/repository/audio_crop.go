package repository

import (
	"context"
	"errors"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
)

func (r *DynamoDBRepository) CreateAudioCrop(ctx context.Context, source, cropped *model.Meeting) error {
	item, err := attributevalue.MarshalMap(cropped)
	if err != nil {
		return err
	}
	condition, err := expression.NewBuilder().WithCondition(
		expression.Name("updatedAt").Equal(expression.Value(source.UpdatedAt)).
			And(expression.Name("status").Equal(expression.Value(source.Status)))).
		Build()
	if err != nil {
		return err
	}
	createCondition, err := expression.NewBuilder().WithCondition(expression.AttributeNotExists(expression.Name("PK"))).Build()
	if err != nil {
		return err
	}
	_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: []types.TransactWriteItem{
			{ConditionCheck: &types.ConditionCheck{
				TableName: aws.String(r.tableName),
				Key: map[string]types.AttributeValue{
					"PK": &types.AttributeValueMemberS{Value: model.PrefixUser + source.UserID},
					"SK": &types.AttributeValueMemberS{Value: model.PrefixMeeting + source.MeetingID},
				},
				ConditionExpression: condition.Condition(), ExpressionAttributeNames: condition.Names(), ExpressionAttributeValues: condition.Values(),
			}},
			{Put: &types.Put{
				TableName: aws.String(r.tableName), Item: item,
				ConditionExpression: createCondition.Condition(), ExpressionAttributeNames: createCondition.Names(),
			}},
		},
	}, singleDynamoDBAttempt)
	var canceled *types.TransactionCanceledException
	if errors.As(err, &canceled) {
		for _, reason := range canceled.CancellationReasons {
			if aws.ToString(reason.Code) == "ConditionalCheckFailed" {
				return ErrConditionFailed
			}
		}
	}
	return err
}
