package repository

import (
	"context"
	"fmt"
	"strconv"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
)

type rateCounterClient interface {
	UpdateItem(context.Context, *dynamodb.UpdateItemInput, ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error)
}

// RateCounterRepository stores fixed-window usage counters on
// USER#{userId}/RATE#{bucket}#{windowKey}. Rows expire through the table's
// pendingShareExpiresAt TTL sweep.
type RateCounterRepository struct {
	client    rateCounterClient
	tableName string
}

func NewRateCounterRepository(client rateCounterClient, tableName string) *RateCounterRepository {
	return &RateCounterRepository{client: client, tableName: tableName}
}

// buildRateCounterUpdate is separate so the request shape is testable.
func buildRateCounterUpdate(tableName, userID, sortKey string, n, expiresAt int64) (*dynamodb.UpdateItemInput, error) {
	update := expression.Add(expression.Name("count"), expression.Value(n)).
		Set(expression.Name(model.PendingShareTTLAttribute), expression.Value(expiresAt))
	expr, err := expression.NewBuilder().WithUpdate(update).Build()
	if err != nil {
		return nil, fmt.Errorf("build rate counter update: %w", err)
	}
	return &dynamodb.UpdateItemInput{
		TableName: aws.String(tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: model.PrefixUser + userID},
			"SK": &types.AttributeValueMemberS{Value: sortKey},
		},
		UpdateExpression:          expr.Update(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
		ReturnValues:              types.ReturnValueUpdatedNew,
	}, nil
}

// IncrementWindowCounter atomically adds n to the counter row and returns the
// new total. The TTL is rewritten on every call with the same window-derived
// value, so it never extends a window's lifetime.
func (r *RateCounterRepository) IncrementWindowCounter(ctx context.Context, userID, sortKey string, n, expiresAt int64) (int64, error) {
	input, err := buildRateCounterUpdate(r.tableName, userID, sortKey, n, expiresAt)
	if err != nil {
		return 0, err
	}
	out, err := r.client.UpdateItem(ctx, input)
	if err != nil {
		return 0, fmt.Errorf("increment rate counter: %w", err)
	}
	attr, ok := out.Attributes["count"].(*types.AttributeValueMemberN)
	if !ok {
		return 0, fmt.Errorf("rate counter response has no count")
	}
	count, err := strconv.ParseInt(attr.Value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse rate counter: %w", err)
	}
	return count, nil
}
