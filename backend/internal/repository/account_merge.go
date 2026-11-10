package repository

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
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
