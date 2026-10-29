package repository

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
)

type AccountHierarchyPage struct {
	Accounts   []model.Account
	NextCursor string
}

// Operator-only metadata scan. It neither reads meeting text nor grants access.
// Scan's evaluated-item limit also applies to empty filtered pages.
func (r *DynamoDBRepository) ScanAccountHierarchyPage(ctx context.Context, cursor string) (*AccountHierarchyPage, error) {
	var start map[string]types.AttributeValue
	if cursor != "" {
		var key [2]string
		raw, err := base64.RawURLEncoding.Strict().DecodeString(cursor)
		if err != nil || len(cursor) > 4096 || json.Unmarshal(raw, &key) != nil || key[0] == "" || key[1] == "" {
			return nil, fmt.Errorf("invalid account hierarchy scan cursor")
		}
		start = map[string]types.AttributeValue{"PK": &types.AttributeValueMemberS{Value: key[0]}, "SK": &types.AttributeValueMemberS{Value: key[1]}}
	}
	filter := expression.Name("entityType").Equal(expression.Value(model.EntityTypeAccount)).And(expression.Name("SK").Equal(expression.Value(model.SKAccountMeta)))
	projection := expression.NamesList(expression.Name("PK"), expression.Name("SK"), expression.Name("accountId"), expression.Name("parentAccountId"), expression.Name("entityType"))
	expr, err := expression.NewBuilder().WithFilter(filter).WithProjection(projection).Build()
	if err != nil {
		return nil, err
	}
	result, err := r.client.Scan(ctx, &dynamodb.ScanInput{TableName: aws.String(r.tableName), Limit: aws.Int32(100), ConsistentRead: aws.Bool(true), ExclusiveStartKey: start,
		FilterExpression: expr.Filter(), ProjectionExpression: expr.Projection(), ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values()})
	if err != nil {
		return nil, err
	}
	page := &AccountHierarchyPage{Accounts: []model.Account{}}
	if err := attributevalue.UnmarshalListOfMaps(result.Items, &page.Accounts); err != nil {
		return nil, err
	}
	for _, a := range page.Accounts {
		if !accountChildID.MatchString(a.AccountID) || a.PK != model.PrefixAccount+a.AccountID || a.SK != model.SKAccountMeta || a.EntityType != model.EntityTypeAccount ||
			a.ParentAccountID != "" && (!accountChildID.MatchString(a.ParentAccountID) || a.ParentAccountID == a.AccountID) {
			return nil, fmt.Errorf("invalid canonical account hierarchy metadata")
		}
	}
	if len(result.LastEvaluatedKey) > 0 {
		pk, pkOK := result.LastEvaluatedKey["PK"].(*types.AttributeValueMemberS)
		sk, skOK := result.LastEvaluatedKey["SK"].(*types.AttributeValueMemberS)
		if !pkOK || !skOK || pk.Value == "" || sk.Value == "" || len(result.LastEvaluatedKey) != 2 {
			return nil, fmt.Errorf("invalid account hierarchy scan continuation")
		}
		raw, _ := json.Marshal([2]string{pk.Value, sk.Value})
		page.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
		if page.NextCursor == cursor {
			return nil, fmt.Errorf("non-advancing account hierarchy scan continuation")
		}
	}
	return page, nil
}
