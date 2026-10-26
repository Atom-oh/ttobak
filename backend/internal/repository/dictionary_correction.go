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

// errCorrectionRowFull marks a counter that already holds the maximum number
// of meeting IDs; it never leaves this file.
var errCorrectionRowFull = errors.New("transcript correction counter full")

// RecordTermCorrection adds meetingID to the TERMS_CORRECTION#{termKey}#{heardKey}
// counter under pk and returns how many distinct meetings the counter now
// holds. The set stops growing at model.MaxTermCorrectionMeetings (a full
// counter reports the cap) and the row expires at expiresAt (Unix seconds,
// the table's TTL attribute). An ACCOUNT# counter is written in a
// transaction with the membership ConditionCheck and then read back
// consistently, since a transaction returns no attributes.
func (r *DictionaryRepository) RecordTermCorrection(ctx context.Context, pk, termKey, heardKey, meetingID string, expiresAt int64, member *MembershipCheck) (int, error) {
	if pk == "" || termKey == "" || heardKey == "" || meetingID == "" || expiresAt <= 0 {
		return 0, ErrInvalidInput
	}
	check, err := r.memberCheck(pk, member)
	if err != nil {
		return 0, err
	}
	ids := expression.Name("meetingIds")
	update := expression.Add(ids, expression.Value(&types.AttributeValueMemberSS{Value: []string{meetingID}})).
		Set(expression.Name(model.PendingShareTTLAttribute), expression.Value(expiresAt)).
		Set(expression.Name("termKey"), expression.Value(termKey))
	condition := expression.AttributeNotExists(ids).
		Or(ids.Size().LessThan(expression.Value(model.MaxTermCorrectionMeetings))).
		Or(expression.Contains(ids, meetingID))
	expr, err := expression.NewBuilder().WithUpdate(update).WithCondition(condition).Build()
	if err != nil {
		return 0, err
	}
	key := dictKey(pk, model.TermsCorrectionSK(termKey, heardKey))

	if check != nil {
		items := []types.TransactWriteItem{{Update: &types.Update{
			TableName: aws.String(r.tableName), Key: key,
			UpdateExpression: expr.Update(), ConditionExpression: expr.Condition(),
			ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(),
		}}, *check}
		_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: items})
		if err = dictMemberTransactError(err, 1, []error{errCorrectionRowFull}); err != nil {
			if errors.Is(err, errCorrectionRowFull) {
				return model.MaxTermCorrectionMeetings, nil
			}
			return 0, err
		}
		result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
			TableName: aws.String(r.tableName), Key: key, ConsistentRead: aws.Bool(true),
		})
		if err != nil {
			return 0, fmt.Errorf("failed to read transcript correction counter: %w", err)
		}
		return correctionMeetingCount(result.Item), nil
	}

	result, err := r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(r.tableName), Key: key,
		UpdateExpression: expr.Update(), ConditionExpression: expr.Condition(),
		ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(),
		ReturnValues: types.ReturnValueUpdatedNew,
	})
	if err != nil {
		var failed *types.ConditionalCheckFailedException
		if errors.As(err, &failed) {
			return model.MaxTermCorrectionMeetings, nil
		}
		return 0, err
	}
	return correctionMeetingCount(result.Attributes), nil
}

func correctionMeetingCount(item map[string]types.AttributeValue) int {
	if set, ok := item["meetingIds"].(*types.AttributeValueMemberSS); ok {
		return len(set.Value)
	}
	return 0
}
