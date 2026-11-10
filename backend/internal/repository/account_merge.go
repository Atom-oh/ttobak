package repository

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
)

// AccountPartitionHasPrefix reports, with one strongly consistent single-item
// query, whether the account partition holds any row whose sort key begins
// with prefix. It reads no row content.
func (r *DynamoDBRepository) AccountPartitionHasPrefix(ctx context.Context, accountID, prefix string) (bool, error) {
	if accountID == "" || prefix == "" || strings.ContainsAny(accountID, "#") {
		return false, fmt.Errorf("invalid account partition prefix probe")
	}
	key := expression.Key("PK").Equal(expression.Value(model.PrefixAccount + accountID)).
		And(expression.Key("SK").BeginsWith(prefix))
	projection := expression.NamesList(expression.Name("PK"), expression.Name("SK"))
	expr, err := expression.NewBuilder().WithKeyCondition(key).WithProjection(projection).Build()
	if err != nil {
		return false, err
	}
	out, err := r.client.Query(ctx, &dynamodb.QueryInput{TableName: aws.String(r.tableName), ConsistentRead: aws.Bool(true),
		Limit: aws.Int32(1), KeyConditionExpression: expr.KeyCondition(), ProjectionExpression: expr.Projection(),
		ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values()})
	if err != nil {
		return false, fmt.Errorf("probe account partition: %w", err)
	}
	return len(out.Items) > 0, nil
}

// ListAccountPartitionKeys returns the sort keys under prefix in the account
// partition with a keys-only projection, so no row content is read or decrypted.
// It reads at most limit+1 keys and reports exceeded when more than limit exist.
func (r *DynamoDBRepository) ListAccountPartitionKeys(ctx context.Context, accountID, prefix string, limit int) ([]string, bool, error) {
	if accountID == "" || prefix == "" || limit < 1 || strings.ContainsAny(accountID, "#") {
		return nil, false, fmt.Errorf("invalid account partition key listing")
	}
	key := expression.Key("PK").Equal(expression.Value(model.PrefixAccount + accountID)).
		And(expression.Key("SK").BeginsWith(prefix))
	expr, err := expression.NewBuilder().WithKeyCondition(key).WithProjection(expression.NamesList(expression.Name("SK"))).Build()
	if err != nil {
		return nil, false, err
	}
	var keys []string
	var start map[string]types.AttributeValue
	for {
		// Never read more than the limit+1 keys needed to detect overflow.
		pageLimit := int32(min(limit+1-len(keys), 1000))
		out, err := r.client.Query(ctx, &dynamodb.QueryInput{TableName: aws.String(r.tableName), ConsistentRead: aws.Bool(true),
			Limit:                  aws.Int32(pageLimit),
			KeyConditionExpression: expr.KeyCondition(), ProjectionExpression: expr.Projection(),
			ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(), ExclusiveStartKey: start})
		if err != nil {
			return nil, false, fmt.Errorf("list account partition keys: %w", err)
		}
		for _, item := range out.Items {
			sk, ok := item["SK"].(*types.AttributeValueMemberS)
			if !ok {
				return nil, false, fmt.Errorf("account partition key row without sort key")
			}
			if len(keys) == limit {
				return keys, true, nil
			}
			keys = append(keys, sk.Value)
		}
		if len(out.LastEvaluatedKey) == 0 {
			return keys, false, nil
		}
		start = out.LastEvaluatedKey
	}
}
