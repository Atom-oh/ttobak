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

// ErrAccountMerging means the account is the source of an in-flight or
// finished merge and rejects this write. Fenced writers return it instead of
// ErrConditionFailed when the open condition on the account META row fails.
var ErrAccountMerging = errors.New("account is merging or merged")

// accountOpenCondition holds while the account is neither mid-merge nor a
// merge tombstone.
func accountOpenCondition() expression.ConditionBuilder {
	return expression.AttributeNotExists(expression.Name("mergingInto")).
		And(expression.AttributeNotExists(expression.Name("mergedInto")))
}

// accountOpenCheck is a ConditionCheck on the account META row requiring that
// it exists and is open. A cancelled transaction reports the stored row, so
// classification tells a merge fence from any other failed condition.
func (r *DynamoDBRepository) accountOpenCheck(accountID string) (types.TransactWriteItem, error) {
	expr, err := expression.NewBuilder().WithCondition(expression.AttributeExists(expression.Name("PK")).And(accountOpenCondition())).Build()
	if err != nil {
		return types.TransactWriteItem{}, fmt.Errorf("build account open check: %w", err)
	}
	return types.TransactWriteItem{ConditionCheck: &types.ConditionCheck{
		TableName: aws.String(r.tableName), Key: accountHierarchyKey(accountID, model.SKAccountMeta),
		ConditionExpression: expr.Condition(), ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(),
		ReturnValuesOnConditionCheckFailure: types.ReturnValuesOnConditionCheckFailureAllOld,
	}}, nil
}

// andOpenIntoCheck extends an existing ConditionCheck on an account META row
// with the open condition. A transaction cannot touch one item twice, so a
// caller that already checks the META row folds the fence into that check.
func andOpenIntoCheck(check *types.ConditionCheck) {
	names := make(map[string]string, len(check.ExpressionAttributeNames)+2)
	for k, v := range check.ExpressionAttributeNames {
		names[k] = v
	}
	names["#mergeFenceA"], names["#mergeFenceB"] = "mergingInto", "mergedInto"
	check.ExpressionAttributeNames = names
	check.ConditionExpression = aws.String("(" + aws.ToString(check.ConditionExpression) + ") AND attribute_not_exists(#mergeFenceA) AND attribute_not_exists(#mergeFenceB)")
	check.ReturnValuesOnConditionCheckFailure = types.ReturnValuesOnConditionCheckFailureAllOld
}

// returnStoredRow asks a condition check or update that carries the open
// condition to report the stored row when it fails, so classification can see
// the merge marker. Items without the open condition never set it.
func returnStoredRow(item types.TransactWriteItem) {
	switch {
	case item.ConditionCheck != nil:
		item.ConditionCheck.ReturnValuesOnConditionCheckFailure = types.ReturnValuesOnConditionCheckFailureAllOld
	case item.Update != nil:
		item.Update.ReturnValuesOnConditionCheckFailure = types.ReturnValuesOnConditionCheckFailureAllOld
	}
}

// reasonShowsMerge reports whether a cancellation reason is a failed
// condition on an account META row that carries a merge marker.
func reasonShowsMerge(reason types.CancellationReason) bool {
	if aws.ToString(reason.Code) != "ConditionalCheckFailed" {
		return false
	}
	for _, name := range []string{"mergingInto", "mergedInto"} {
		if v, ok := reason.Item[name].(*types.AttributeValueMemberS); ok && v.Value != "" {
			return true
		}
	}
	return false
}

// mergeFenceTripped reports whether any cancellation reason is the merge fence.
func mergeFenceTripped(err error) bool {
	var cancelled *types.TransactionCanceledException
	if !errors.As(err, &cancelled) {
		return false
	}
	for _, reason := range cancelled.CancellationReasons {
		if reasonShowsMerge(reason) {
			return true
		}
	}
	return false
}

// mergeFenceError returns ErrAccountMerging (wrapping err) when a cancelled
// transaction tripped the merge fence, and nil otherwise.
func mergeFenceError(err error) error {
	if err != nil && mergeFenceTripped(err) {
		return fmt.Errorf("%w: %w", ErrAccountMerging, err)
	}
	return nil
}

// metaOpenCheck is accountOpenCheck's account-keyed variant for repositories
// without a DynamoDBRepository receiver (research).
func metaOpenCheck(tableName, accountID string) (types.TransactWriteItem, error) {
	return (&DynamoDBRepository{tableName: tableName}).accountOpenCheck(accountID)
}

// storedMeetingAccountID reads the account a meeting is currently classified
// to ("" when none or the meeting is absent).
func (r *DynamoDBRepository) storedMeetingAccountID(ctx context.Context, userID, meetingID string) (string, error) {
	out, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName), ConsistentRead: aws.Bool(true),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: model.PrefixUser + userID},
			"SK": &types.AttributeValueMemberS{Value: model.PrefixMeeting + meetingID},
		},
		ProjectionExpression: aws.String("accountId"),
	})
	if err != nil {
		return "", fmt.Errorf("read meeting account: %w", err)
	}
	if v, ok := out.Item["accountId"].(*types.AttributeValueMemberS); ok {
		return v.Value, nil
	}
	return "", nil
}

// foldOpenIntoExtra keeps a transaction from touching the account META row
// twice. When the caller's extra checks already include that row (an ancestor
// member's grant path), the open condition joins that check on a copy and the
// separate open item is dropped.
func foldOpenIntoExtra(extra []types.TransactWriteItem, accountID string, open **types.TransactWriteItem) []types.TransactWriteItem {
	pk, sk := model.PrefixAccount+accountID, model.SKAccountMeta
	for i, item := range extra {
		check := item.ConditionCheck
		if check == nil {
			continue
		}
		p, okP := check.Key["PK"].(*types.AttributeValueMemberS)
		s, okS := check.Key["SK"].(*types.AttributeValueMemberS)
		if !okP || !okS || p.Value != pk || s.Value != sk {
			continue
		}
		folded := *check
		andOpenIntoCheck(&folded)
		out := append([]types.TransactWriteItem(nil), extra...)
		out[i] = types.TransactWriteItem{ConditionCheck: &folded}
		*open = nil
		return out
	}
	return extra
}
