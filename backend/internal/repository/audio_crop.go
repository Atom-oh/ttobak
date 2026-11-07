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

// CreateAudioCropForTeam creates a managing member's crop copy, published to
// the source's team account, in one transaction (ADR-047 note): the source
// must still match and still be published to that account, the actor's live
// grant (direct member row, or ancestor member row plus parent links) must be
// unchanged, and the copy and its team MEETINGREF are written together. The
// copy carries only accountId/sharedToAccount, as a publication of a meeting
// with no derived content does.
func (r *DynamoDBRepository) CreateAudioCropForTeam(ctx context.Context, source, cropped *model.Meeting, actorID string, ref *model.MeetingRef) error {
	team := cropped.AccountID
	if team == "" || !cropped.SharedToAccount || ref == nil || ref.AccountID != team || ref.MeetingID != cropped.MeetingID ||
		ref.OwnerUserID != source.UserID || cropped.UserID != source.UserID {
		return ErrConditionFailed
	}
	item, err := attributevalue.MarshalMap(cropped)
	if err != nil {
		return err
	}
	refItem, err := attributevalue.MarshalMap(ref)
	if err != nil {
		return err
	}
	condition, err := expression.NewBuilder().WithCondition(
		expression.Name("updatedAt").Equal(expression.Value(source.UpdatedAt)).
			And(expression.Name("status").Equal(expression.Value(source.Status))).
			And(expression.Name("accountId").Equal(expression.Value(team))).
			And(expression.Name("sharedToAccount").Equal(expression.Value(true)))).
		Build()
	if err != nil {
		return err
	}
	createCondition, err := expression.NewBuilder().WithCondition(expression.AttributeNotExists(expression.Name("PK"))).Build()
	if err != nil {
		return err
	}
	items := []types.TransactWriteItem{
		{ConditionCheck: &types.ConditionCheck{
			TableName:           aws.String(r.tableName),
			Key:                 summaryRowKey(model.PrefixUser+source.UserID, model.PrefixMeeting+source.MeetingID),
			ConditionExpression: condition.Condition(), ExpressionAttributeNames: condition.Names(), ExpressionAttributeValues: condition.Values(),
		}},
		{Put: &types.Put{TableName: aws.String(r.tableName), Item: item,
			ConditionExpression: createCondition.Condition(), ExpressionAttributeNames: createCondition.Names()}},
		{Put: &types.Put{TableName: aws.String(r.tableName), Item: refItem}},
	}
	checks, err := r.accountTeamMemberChecks(ctx, team, actorID)
	if err != nil {
		return err
	}
	for _, check := range checks {
		cc, err := r.conditionCheckFor(check)
		if err != nil {
			return err
		}
		items = append(items, types.TransactWriteItem{ConditionCheck: cc})
	}
	_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: items}, singleDynamoDBAttempt)
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
