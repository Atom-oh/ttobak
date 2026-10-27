package repository

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
)

// CreateAccountWithInitialMembers publishes canonical ownership and optional
// creator access together. Parent checks use the creator, never the invitee.
func (r *DynamoDBRepository) CreateAccountWithInitialMembers(ctx context.Context, account *model.Account, owner, creator *model.AccountMember, requesterID string, ancestors []AccountParentLink) error {
	if account == nil || owner == nil || requesterID == "" ||
		account.CreatedByUserID != requesterID || owner.UserID != account.OwnerUserID ||
		owner.Role != model.RoleOwner || owner.AccountID != account.AccountID {
		return fmt.Errorf("invalid initial account ownership")
	}
	if creator != nil && (creator.UserID != requesterID || creator.UserID == owner.UserID ||
		creator.AccountID != account.AccountID || creator.Role != model.RoleAM) {
		return fmt.Errorf("invalid initial creator membership")
	}
	checks, err := r.hierarchyParentChecks(account.AccountID, requesterID, account.ParentAccountID, ancestors)
	if err != nil {
		return err
	}
	items, err := r.accountCreationItems(account, stampSummaryMembership(owner))
	if err != nil {
		return err
	}
	if creator != nil {
		item, err := attributevalue.MarshalMap(stampSummaryMembership(creator))
		if err != nil {
			return err
		}
		condition, err := expression.NewBuilder().WithCondition(expression.AttributeNotExists(expression.Name("PK"))).Build()
		if err != nil {
			return err
		}
		items = append(items, types.TransactWriteItem{Put: &types.Put{
			TableName: aws.String(r.tableName), Item: item, ConditionExpression: condition.Condition(),
			ExpressionAttributeNames: condition.Names(), ExpressionAttributeValues: condition.Values(),
		}})
	}
	return r.writeAccountHierarchy(ctx, append(items, checks...))
}
