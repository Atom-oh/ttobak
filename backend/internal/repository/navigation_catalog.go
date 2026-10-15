package repository

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
)

type NavigationCandidatePage struct {
	IDs    []string
	NextID string
}

var navigationID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func navigationKey(user, kind, account, id string) map[string]types.AttributeValue {
	stringsKey := map[string]string{}
	switch kind {
	case "accounts":
		stringsKey = map[string]string{"PK": model.PrefixAccount + id, "SK": model.PrefixMember + user,
			"GSI1PK": model.PrefixUser + user, "GSI1SK": model.PrefixAccount + id}
	case "owned":
		stringsKey = map[string]string{"PK": model.PrefixUser + user, "SK": model.PrefixProject + id}
	case "direct":
		stringsKey = map[string]string{"PK": model.PrefixProject + id, "SK": model.PrefixProjectMember + user,
			"GSI1PK": model.PrefixUser + user, "GSI1SK": model.PrefixProject + id}
	case "account-projects":
		stringsKey = map[string]string{"PK": model.PrefixAccount + account, "SK": model.PrefixProjectRef + id}
	}
	key := map[string]types.AttributeValue{}
	for name, value := range stringsKey {
		key[name] = &types.AttributeValueMemberS{Value: value}
	}
	return key
}

// QueryNavigationCandidates performs one limited index query. Index candidates
// never grant access: services reread canonical resources and memberships.
func (r *DynamoDBRepository) QueryNavigationCandidates(ctx context.Context, user, kind, account, after string, limit int) (*NavigationCandidatePage, error) {
	if !navigationID.MatchString(user) || limit < 1 || limit > 50 || (after != "" && !navigationID.MatchString(after)) {
		return nil, fmt.Errorf("invalid navigation query")
	}
	pkName, skName, pk, prefix, index := "PK", "SK", model.PrefixUser+user, model.PrefixProject, ""
	switch kind {
	case "accounts":
		pkName, skName, prefix, index = "GSI1PK", "GSI1SK", model.PrefixAccount, "GSI1"
	case "direct":
		pkName, skName, index = "GSI1PK", "GSI1SK", "GSI1"
	case "owned":
	case "account-projects":
		if !navigationID.MatchString(account) {
			return nil, fmt.Errorf("invalid account navigation query")
		}
		pk, prefix = model.PrefixAccount+account, model.PrefixProjectRef
	default:
		return nil, fmt.Errorf("invalid navigation kind")
	}
	keyCondition := expression.Key(pkName).Equal(expression.Value(pk)).
		And(expression.Key(skName).BeginsWith(prefix))
	expr, err := expression.NewBuilder().WithKeyCondition(keyCondition).WithProjection(expression.NamesList(
		expression.Name("PK"), expression.Name("SK"), expression.Name("GSI1PK"), expression.Name("GSI1SK"),
	)).Build()
	if err != nil {
		return nil, err
	}
	input := &dynamodb.QueryInput{
		TableName: aws.String(r.tableName), Limit: aws.Int32(int32(limit)), ScanIndexForward: aws.Bool(true),
		KeyConditionExpression: expr.KeyCondition(), ProjectionExpression: expr.Projection(),
		ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(),
	}
	if index != "" {
		input.IndexName = aws.String(index)
	} else {
		input.ConsistentRead = aws.Bool(true)
	}
	if after != "" {
		input.ExclusiveStartKey = navigationKey(user, kind, account, after)
	}
	out, err := r.client.Query(ctx, input)
	if err != nil {
		return nil, err
	}
	decode := func(item map[string]types.AttributeValue) (string, error) {
		sortKey, ok := item[skName].(*types.AttributeValueMemberS)
		if !ok || !strings.HasPrefix(sortKey.Value, prefix) {
			return "", fmt.Errorf("invalid navigation candidate key")
		}
		id := strings.TrimPrefix(sortKey.Value, prefix)
		if !navigationID.MatchString(id) {
			return "", fmt.Errorf("invalid navigation candidate identity")
		}
		for field, wanted := range navigationKey(user, kind, account, id) {
			actual, ok := item[field].(*types.AttributeValueMemberS)
			if !ok || actual.Value != wanted.(*types.AttributeValueMemberS).Value {
				return "", fmt.Errorf("navigation candidate is outside its query scope")
			}
		}
		return id, nil
	}
	page := &NavigationCandidatePage{IDs: []string{}}
	for _, item := range out.Items {
		id, err := decode(item)
		if err != nil {
			return nil, err
		}
		page.IDs = append(page.IDs, id)
	}
	if len(out.LastEvaluatedKey) != 0 {
		page.NextID, err = decode(out.LastEvaluatedKey)
		if err != nil {
			return nil, err
		}
	}
	return page, nil
}

func (r *DynamoDBRepository) GetProjectCatalogMetadata(ctx context.Context, id string) (*model.Project, error) {
	expr, err := expression.NewBuilder().WithProjection(expression.NamesList(
		expression.Name("PK"), expression.Name("SK"), expression.Name("projectId"), expression.Name("name"),
		expression.Name("ownerUserId"), expression.Name("accountIds"), expression.Name("stage"),
		expression.Name("sfdcOpptyId"), expression.Name("createdAt"), expression.Name("updatedAt"),
		expression.Name("entityType"),
	)).Build()
	if err != nil {
		return nil, err
	}
	out, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName), ConsistentRead: aws.Bool(true),
		Key: map[string]types.AttributeValue{"PK": &types.AttributeValueMemberS{Value: model.PrefixProject + id},
			"SK": &types.AttributeValueMemberS{Value: model.SKProjectConfig}},
		ProjectionExpression: expr.Projection(), ExpressionAttributeNames: expr.Names(),
	})
	if err != nil || len(out.Item) == 0 {
		return nil, err
	}
	var project model.Project
	if err := attributevalue.UnmarshalMap(out.Item, &project); err != nil {
		return nil, err
	}
	return &project, nil
}
