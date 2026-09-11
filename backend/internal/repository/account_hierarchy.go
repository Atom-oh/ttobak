package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/smithy-go"
	"github.com/ttobak/backend/internal/model"
)

// AccountHierarchyMaxAncestors bounds reads and keeps transactions below the
// 100-item limit (at most 64 ancestors + child/owner/parent-member = 67).
const AccountHierarchyMaxAncestors = 64

// AccountParentLink is a strongly consistent observation, checked again in the
// same transaction as the child write. Include the root's empty parent link.
type AccountParentLink struct {
	AccountID       string
	ParentAccountID string
}

func accountHierarchyKey(accountID, sk string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		"PK": &types.AttributeValueMemberS{Value: model.PrefixAccount + accountID},
		"SK": &types.AttributeValueMemberS{Value: sk},
	}
}

func accountParentCondition(parentID string) expression.ConditionBuilder {
	parent := expression.Name("parentAccountId")
	if parentID == "" {
		// Old records omit this attribute; accept explicit empty strings too.
		return parent.AttributeNotExists().Or(parent.Equal(expression.Value("")))
	}
	return parent.Equal(expression.Value(parentID))
}

func (r *DynamoDBRepository) hierarchyCheck(accountID, sk string, condition expression.ConditionBuilder) (types.TransactWriteItem, error) {
	expr, err := expression.NewBuilder().WithCondition(condition).Build()
	if err != nil {
		return types.TransactWriteItem{}, fmt.Errorf("build account hierarchy condition: %w", err)
	}
	return types.TransactWriteItem{ConditionCheck: &types.ConditionCheck{
		TableName: aws.String(r.tableName), Key: accountHierarchyKey(accountID, sk),
		ConditionExpression: expr.Condition(), ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(),
	}}, nil
}

// hierarchyParentChecks requires a complete, simple path from parent to root.
// It never checks ancestor membership: the hierarchy carries no access grants.
func (r *DynamoDBRepository) hierarchyParentChecks(childID, userID, parentID string, ancestors []AccountParentLink) ([]types.TransactWriteItem, error) {
	if len(ancestors) > AccountHierarchyMaxAncestors {
		return nil, fmt.Errorf("account hierarchy exceeds ancestor limit")
	}
	seen := map[string]bool{childID: true}
	next := parentID
	for _, ancestor := range ancestors {
		if next == "" || ancestor.AccountID != next || seen[next] {
			return nil, fmt.Errorf("invalid account hierarchy observations")
		}
		seen[next] = true
		next = ancestor.ParentAccountID
	}
	if next != "" {
		return nil, fmt.Errorf("incomplete account hierarchy observations")
	}
	if parentID == "" {
		return nil, nil
	}
	exists := expression.AttributeExists(expression.Name("PK"))
	member, err := r.hierarchyCheck(parentID, model.PrefixMember+userID, exists)
	if err != nil {
		return nil, err
	}
	items := []types.TransactWriteItem{member}
	for _, ancestor := range ancestors {
		check, err := r.hierarchyCheck(ancestor.AccountID, model.SKAccountMeta, exists.And(accountParentCondition(ancestor.ParentAccountID)))
		if err != nil {
			return nil, err
		}
		items = append(items, check)
	}
	return items, nil
}

// UpdateAccountParent changes only parentAccountId and updatedAt. Checking the
// child and every ancestor closes opposing-move races; checking both memberships
// and canonical ownership closes authorization changes between read and write.
func (r *DynamoDBRepository) UpdateAccountParent(ctx context.Context, accountID, requesterID, expectedParentID, parentID string, ancestors []AccountParentLink) error {
	parentChecks, err := r.hierarchyParentChecks(accountID, requesterID, parentID, ancestors)
	if err != nil {
		return err
	}
	update := expression.Set(expression.Name("updatedAt"), expression.Value(time.Now().UTC()))
	if parentID == "" {
		update = update.Remove(expression.Name("parentAccountId"))
	} else {
		update = update.Set(expression.Name("parentAccountId"), expression.Value(parentID))
	}
	exists := expression.AttributeExists(expression.Name("PK"))
	condition := exists.And(accountParentCondition(expectedParentID)).
		And(expression.Name("ownerUserId").Equal(expression.Value(requesterID)))
	expr, err := expression.NewBuilder().WithUpdate(update).WithCondition(condition).Build()
	if err != nil {
		return fmt.Errorf("build account parent update: %w", err)
	}
	ownerCheck, err := r.hierarchyCheck(accountID, model.PrefixMember+requesterID,
		exists.And(expression.Name("role").Equal(expression.Value(model.RoleOwner))))
	if err != nil {
		return err
	}
	items := []types.TransactWriteItem{
		{Update: &types.Update{
			TableName: aws.String(r.tableName), Key: accountHierarchyKey(accountID, model.SKAccountMeta),
			UpdateExpression: expr.Update(), ConditionExpression: expr.Condition(),
			ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(),
		}},
		ownerCheck,
	}
	items = append(items, parentChecks...)
	return r.writeAccountHierarchy(ctx, items)
}

func (r *DynamoDBRepository) accountCreationItems(account *model.Account, owner *model.AccountMember) ([]types.TransactWriteItem, error) {
	condition, err := expression.NewBuilder().WithCondition(expression.AttributeNotExists(expression.Name("PK"))).Build()
	if err != nil {
		return nil, fmt.Errorf("build account creation condition: %w", err)
	}
	items := make([]types.TransactWriteItem, 0, 2)
	for _, value := range []any{account, owner} {
		item, err := attributevalue.MarshalMap(value)
		if err != nil {
			return nil, fmt.Errorf("marshal account creation item: %w", err)
		}
		items = append(items, types.TransactWriteItem{Put: &types.Put{
			TableName: aws.String(r.tableName), Item: item,
			ConditionExpression: condition.Condition(), ExpressionAttributeNames: condition.Names(), ExpressionAttributeValues: condition.Values(),
		}})
	}
	return items, nil
}

// CreateAccountWithParent atomically creates the account and its sole initial
// member, conditioned on the observed ancestry and requester's parent membership.
func (r *DynamoDBRepository) CreateAccountWithParent(ctx context.Context, account *model.Account, owner *model.AccountMember, ancestors []AccountParentLink) error {
	checks, err := r.hierarchyParentChecks(account.AccountID, owner.UserID, account.ParentAccountID, ancestors)
	if err != nil {
		return err
	}
	items, err := r.accountCreationItems(account, owner)
	if err != nil {
		return err
	}
	return r.writeAccountHierarchy(ctx, append(items, checks...))
}

func (r *DynamoDBRepository) writeAccountHierarchy(ctx context.Context, items []types.TransactWriteItem) error {
	_, err := r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: items})
	if err == nil {
		return nil
	}
	var canceled *types.TransactionCanceledException
	// TransactWriteItems may deserialize an unmodeled service error into
	// GenericAPIError. Match the structured code, never error message text.
	var apiErr smithy.APIError
	retryable := errors.As(err, &apiErr) && apiErr.ErrorCode() == "TransactionConflictException"
	if errors.As(err, &canceled) {
		for _, reason := range canceled.CancellationReasons {
			switch aws.ToString(reason.Code) {
			case "ConditionalCheckFailed", "TransactionConflict":
				retryable = true
			case "None":
			default:
				return fmt.Errorf("account hierarchy transaction: %w", err)
			}
		}
	}
	if retryable {
		return fmt.Errorf("account hierarchy transaction: %w: %w", ErrConditionFailed, err)
	}
	// Do not turn throttling, validation, or unknown cancellations into a
	// hierarchy conflict; preserve the underlying failure for the caller.
	return fmt.Errorf("account hierarchy transaction: %w", err)
}
