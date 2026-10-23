package repository

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
)

type billingHandoffClient interface {
	PutItem(context.Context, *dynamodb.PutItemInput, ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error)
}

// BillingHandoffRepository writes single-use Billing Insight hand-off rows
// (see model.PrefixHandoff). It never reads or deletes them; the companion
// stack redeems them.
type BillingHandoffRepository struct {
	client    billingHandoffClient
	tableName string
}

func NewBillingHandoffRepository(client billingHandoffClient, tableName string) *BillingHandoffRepository {
	return &BillingHandoffRepository{client: client, tableName: tableName}
}

// buildBillingHandoffPut is separate so the request shape is testable.
func buildBillingHandoffPut(tableName, codeHash, sub, email string, expiresAt int64) (*dynamodb.PutItemInput, error) {
	cond := expression.AttributeNotExists(expression.Name("PK"))
	expr, err := expression.NewBuilder().WithCondition(cond).Build()
	if err != nil {
		return nil, fmt.Errorf("build billing handoff condition: %w", err)
	}
	return &dynamodb.PutItemInput{
		TableName: aws.String(tableName),
		Item: map[string]types.AttributeValue{
			"PK":                           &types.AttributeValueMemberS{Value: model.PrefixHandoff + codeHash},
			"SK":                           &types.AttributeValueMemberS{Value: model.SKHandoff},
			"target":                       &types.AttributeValueMemberS{Value: model.BillingInsightHandoffTarget},
			"sub":                          &types.AttributeValueMemberS{Value: sub},
			"email":                        &types.AttributeValueMemberS{Value: email},
			model.PendingShareTTLAttribute: &types.AttributeValueMemberN{Value: strconv.FormatInt(expiresAt, 10)},
		},
		ConditionExpression:       expr.Condition(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
	}, nil
}

// PutBillingHandoff conditionally creates the row keyed by the code's hash.
// A pre-existing row returns ErrConditionFailed.
func (r *BillingHandoffRepository) PutBillingHandoff(ctx context.Context, codeHash, sub, email string, expiresAt int64) error {
	input, err := buildBillingHandoffPut(r.tableName, codeHash, sub, email, expiresAt)
	if err != nil {
		return err
	}
	if _, err := r.client.PutItem(ctx, input); err != nil {
		var failed *types.ConditionalCheckFailedException
		if errors.As(err, &failed) {
			return ErrConditionFailed
		}
		return fmt.Errorf("put billing handoff: %w", err)
	}
	return nil
}
