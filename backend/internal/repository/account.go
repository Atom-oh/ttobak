package repository

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
)

// CreateAccount writes the account META item and the owner member item.
func (r *DynamoDBRepository) CreateAccount(ctx context.Context, account *model.Account, ownerMember *model.AccountMember) error {
	accItem, err := attributevalue.MarshalMap(account)
	if err != nil {
		return fmt.Errorf("marshal account: %w", err)
	}
	memItem, err := attributevalue.MarshalMap(ownerMember)
	if err != nil {
		return fmt.Errorf("marshal owner member: %w", err)
	}
	// Atomic two-item write: an account must never exist without its owner member
	// (a missing owner would make GetMember deny everyone, orphaning the account).
	if _, err := r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: []types.TransactWriteItem{
			{Put: &types.Put{TableName: aws.String(r.tableName), Item: accItem}},
			{Put: &types.Put{TableName: aws.String(r.tableName), Item: memItem}},
		},
	}); err != nil {
		return fmt.Errorf("create account transaction: %w", err)
	}
	return nil
}

func (r *DynamoDBRepository) GetAccount(ctx context.Context, accountID string) (*model.Account, error) {
	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      aws.String(r.tableName),
		ConsistentRead: aws.Bool(true),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: model.PrefixAccount + accountID},
			"SK": &types.AttributeValueMemberS{Value: model.SKAccountMeta},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("get account: %w", err)
	}
	if result.Item == nil {
		return nil, nil
	}
	var account model.Account
	if err := attributevalue.UnmarshalMap(result.Item, &account); err != nil {
		return nil, fmt.Errorf("unmarshal account: %w", err)
	}
	return &account, nil
}

func (r *DynamoDBRepository) GetMember(ctx context.Context, accountID, userID string) (*model.AccountMember, error) {
	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      aws.String(r.tableName),
		ConsistentRead: aws.Bool(true),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: model.PrefixAccount + accountID},
			"SK": &types.AttributeValueMemberS{Value: model.PrefixMember + userID},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("get member: %w", err)
	}
	if result.Item == nil {
		return nil, nil
	}
	var member model.AccountMember
	if err := attributevalue.UnmarshalMap(result.Item, &member); err != nil {
		return nil, fmt.Errorf("unmarshal member: %w", err)
	}
	return &member, nil
}

func (r *DynamoDBRepository) PutMember(ctx context.Context, member *model.AccountMember) error {
	item, err := attributevalue.MarshalMap(member)
	if err != nil {
		return fmt.Errorf("marshal member: %w", err)
	}
	if _, err := r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(r.tableName),
		Item:      item,
	}); err != nil {
		return fmt.Errorf("put member: %w", err)
	}
	return nil
}

// ListAccountMembers queries the account partition for MEMBER# items.
func (r *DynamoDBRepository) ListAccountMembers(ctx context.Context, accountID string) ([]model.AccountMember, error) {
	keyEx := expression.Key("PK").Equal(expression.Value(model.PrefixAccount + accountID)).
		And(expression.Key("SK").BeginsWith(model.PrefixMember))
	expr, err := expression.NewBuilder().WithKeyCondition(keyEx).Build()
	if err != nil {
		return nil, fmt.Errorf("build members query: %w", err)
	}
	result, err := r.client.Query(ctx, &dynamodb.QueryInput{
		TableName:                 aws.String(r.tableName),
		KeyConditionExpression:    expr.KeyCondition(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
	})
	if err != nil {
		return nil, fmt.Errorf("query members: %w", err)
	}
	members := []model.AccountMember{}
	if err := attributevalue.UnmarshalListOfMaps(result.Items, &members); err != nil {
		return nil, fmt.Errorf("unmarshal members: %w", err)
	}
	return members, nil
}

// ListAccountsForUser reverse-looks-up memberships via GSI1
// (GSI1PK=USER#{userId}, GSI1SK begins_with ACCOUNT#). GSI1 is shared with
// meeting date-sorting items whose GSI1SK is an RFC3339 timestamp, so the
// begins_with("ACCOUNT#") condition isolates membership rows.
func (r *DynamoDBRepository) ListAccountsForUser(ctx context.Context, userID string) ([]model.AccountMember, error) {
	keyEx := expression.Key("GSI1PK").Equal(expression.Value(model.PrefixUser + userID)).
		And(expression.Key("GSI1SK").BeginsWith(model.PrefixAccount))
	expr, err := expression.NewBuilder().WithKeyCondition(keyEx).Build()
	if err != nil {
		return nil, fmt.Errorf("build accounts-for-user query: %w", err)
	}
	result, err := r.client.Query(ctx, &dynamodb.QueryInput{
		TableName:                 aws.String(r.tableName),
		IndexName:                 aws.String("GSI1"),
		KeyConditionExpression:    expr.KeyCondition(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
	})
	if err != nil {
		return nil, fmt.Errorf("query accounts for user: %w", err)
	}
	members := []model.AccountMember{}
	if err := attributevalue.UnmarshalListOfMaps(result.Items, &members); err != nil {
		return nil, fmt.Errorf("unmarshal memberships: %w", err)
	}
	return members, nil
}
