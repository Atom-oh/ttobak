package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
)

func (r *DynamoDBRepository) ownerTransferUpdate(accountID, sk string, update expression.UpdateBuilder, condition expression.ConditionBuilder) (types.TransactWriteItem, error) {
	expr, err := expression.NewBuilder().WithUpdate(update).WithCondition(condition).Build()
	if err != nil {
		return types.TransactWriteItem{}, fmt.Errorf("build account owner transfer update: %w", err)
	}
	return types.TransactWriteItem{Update: &types.Update{
		TableName: aws.String(r.tableName), Key: accountHierarchyKey(accountID, sk),
		UpdateExpression: expr.Update(), ConditionExpression: expr.Condition(),
		ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(),
	}}, nil
}

// TransferAccountOwner atomically moves ownership from currentOwnerID to an
// existing non-owner member. The META owner and both MEMBER roles are
// conditioned together, so a concurrent role change, removal or competing
// transfer cancels the whole transaction with ErrConditionFailed.
func (r *DynamoDBRepository) TransferAccountOwner(ctx context.Context, accountID, currentOwnerID, targetUserID string, at time.Time) error {
	if accountID == "" || currentOwnerID == "" || targetUserID == "" || currentOwnerID == targetUserID {
		return errors.New("account owner transfer requires distinct current owner and target")
	}
	exists := expression.AttributeExists(expression.Name("PK"))
	now := at.UTC()
	meta, err := r.ownerTransferUpdate(accountID, model.SKAccountMeta,
		expression.Set(expression.Name("ownerUserId"), expression.Value(targetUserID)).
			Set(expression.Name("updatedAt"), expression.Value(now)).
			// Creation receipts replay the original owner, never a later one (ADR-055).
			Set(expression.Name("originalOwnerUserId"), expression.IfNotExists(expression.Name("originalOwnerUserId"), expression.Value(currentOwnerID))).
			Remove(expression.Name("initialOwnerEmail")).
			Remove(expression.Name("initialOwnerInvitationPending")),
		exists.And(expression.Name("ownerUserId").Equal(expression.Value(currentOwnerID))).
			And(expression.AttributeNotExists(expression.Name("mergingInto"))).
			And(expression.AttributeNotExists(expression.Name("mergedInto"))))
	if err != nil {
		return err
	}
	target, err := r.ownerTransferUpdate(accountID, model.PrefixMember+targetUserID,
		expression.Set(expression.Name("role"), expression.Value(model.RoleOwner)),
		exists.And(expression.Name("userId").Equal(expression.Value(targetUserID))).
			And(expression.Name("role").NotEqual(expression.Value(model.RoleOwner))))
	if err != nil {
		return err
	}
	previous, err := r.ownerTransferUpdate(accountID, model.PrefixMember+currentOwnerID,
		expression.Set(expression.Name("role"), expression.Value(model.RoleSA)),
		expression.Name("role").Equal(expression.Value(model.RoleOwner)))
	if err != nil {
		return err
	}
	return r.writeAccountHierarchy(ctx, []types.TransactWriteItem{meta, target, previous})
}
