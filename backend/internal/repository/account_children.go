package repository

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
)

const accountChildPrefix = "CHILDREF#"

var accountChildID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

type accountChildReference struct {
	PK              string `dynamodbav:"PK"`
	SK              string `dynamodbav:"SK"`
	AccountID       string `dynamodbav:"accountId"`
	ParentAccountID string `dynamodbav:"parentAccountId"`
	EntityType      string `dynamodbav:"entityType"`
}

type accountChildCursor struct {
	Parent string `json:"parent"`
	After  string `json:"after"`
}

func (r *DynamoDBRepository) childReferencePut(childID, parentID string) (types.TransactWriteItem, error) {
	if !accountChildID.MatchString(childID) || !accountChildID.MatchString(parentID) || childID == parentID {
		return types.TransactWriteItem{}, fmt.Errorf("invalid account child relation")
	}
	item, err := attributevalue.MarshalMap(accountChildReference{
		PK: model.PrefixAccount + parentID, SK: accountChildPrefix + childID,
		AccountID: childID, ParentAccountID: parentID, EntityType: "ACCOUNT_CHILD_REF",
	})
	if err != nil {
		return types.TransactWriteItem{}, err
	}
	// Idempotent repair may replace only the same reference identity.
	condition := expression.AttributeNotExists(expression.Name("PK")).Or(
		expression.Name("entityType").Equal(expression.Value("ACCOUNT_CHILD_REF")).
			And(expression.Name("accountId").Equal(expression.Value(childID))).
			And(expression.Name("parentAccountId").Equal(expression.Value(parentID))))
	expr, err := expression.NewBuilder().WithCondition(condition).Build()
	if err != nil {
		return types.TransactWriteItem{}, err
	}
	return types.TransactWriteItem{Put: &types.Put{
		TableName: aws.String(r.tableName), Item: item, ConditionExpression: expr.Condition(),
		ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(),
	}}, nil
}

// EnsureAccountChildReference repairs discovery, never membership. A concurrent
// move cannot publish a reference against a stale observed parent.
func (r *DynamoDBRepository) EnsureAccountChildReference(ctx context.Context, childID, expectedParentID string) error {
	put, err := r.childReferencePut(childID, expectedParentID)
	if err != nil {
		return err
	}
	check, err := r.hierarchyCheck(childID, model.SKAccountMeta,
		expression.AttributeExists(expression.Name("PK")).
			And(expression.Name("entityType").Equal(expression.Value(model.EntityTypeAccount))).
			And(expression.Name("accountId").Equal(expression.Value(childID))).
			And(accountParentCondition(expectedParentID)))
	if err != nil {
		return err
	}
	return r.writeAccountHierarchy(ctx, []types.TransactWriteItem{check, put})
}

// Child references are scoped discovery candidates. Callers recheck current
// META parent links and authorization before using them.
func (r *DynamoDBRepository) ListChildAccountIDsPage(ctx context.Context, parentID, cursor string, limit int32) ([]string, *string, error) {
	if !accountChildID.MatchString(parentID) || limit < 1 || limit > 100 || len(cursor) > 2048 {
		return nil, nil, fmt.Errorf("invalid account child selection")
	}
	after := ""
	if cursor != "" {
		data, err := base64.RawURLEncoding.Strict().DecodeString(cursor)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid account child cursor")
		}
		var position accountChildCursor
		decoder := json.NewDecoder(strings.NewReader(string(data)))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&position) != nil || decoder.Decode(new(any)) != io.EOF ||
			position.Parent != parentID || !strings.HasPrefix(position.After, accountChildPrefix) ||
			!accountChildID.MatchString(strings.TrimPrefix(position.After, accountChildPrefix)) {
			return nil, nil, fmt.Errorf("invalid account child cursor")
		}
		after = position.After
	}
	key := expression.Key("PK").Equal(expression.Value(model.PrefixAccount + parentID)).
		And(expression.Key("SK").BeginsWith(accountChildPrefix))
	projection := expression.NamesList(expression.Name("PK"), expression.Name("SK"), expression.Name("accountId"),
		expression.Name("parentAccountId"), expression.Name("entityType"))
	expr, err := expression.NewBuilder().WithKeyCondition(key).WithProjection(projection).Build()
	if err != nil {
		return nil, nil, err
	}
	input := &dynamodb.QueryInput{TableName: aws.String(r.tableName), ConsistentRead: aws.Bool(true), Limit: aws.Int32(limit),
		KeyConditionExpression: expr.KeyCondition(), ProjectionExpression: expr.Projection(),
		ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(), ScanIndexForward: aws.Bool(true)}
	if after != "" {
		input.ExclusiveStartKey = accountHierarchyKey(parentID, after)
	}
	out, err := r.client.Query(ctx, input)
	if err != nil {
		return nil, nil, fmt.Errorf("query account children: %w", err)
	}
	var rows []accountChildReference
	if err := attributevalue.UnmarshalListOfMaps(out.Items, &rows); err != nil {
		return nil, nil, err
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.PK == model.PrefixAccount+parentID && row.SK == accountChildPrefix+row.AccountID &&
			row.ParentAccountID == parentID && row.EntityType == "ACCOUNT_CHILD_REF" && accountChildID.MatchString(row.AccountID) {
			ids = append(ids, row.AccountID)
		}
	}
	if len(out.LastEvaluatedKey) == 0 {
		return ids, nil, nil
	}
	pk, validPK := out.LastEvaluatedKey["PK"].(*types.AttributeValueMemberS)
	sk, validSK := out.LastEvaluatedKey["SK"].(*types.AttributeValueMemberS)
	if !validPK || !validSK || pk.Value != model.PrefixAccount+parentID ||
		!strings.HasPrefix(sk.Value, accountChildPrefix) ||
		!accountChildID.MatchString(strings.TrimPrefix(sk.Value, accountChildPrefix)) || sk.Value <= after {
		return nil, nil, fmt.Errorf("invalid account child continuation")
	}
	data, err := json.Marshal(accountChildCursor{Parent: parentID, After: sk.Value})
	if err != nil {
		return nil, nil, err
	}
	next := base64.RawURLEncoding.EncodeToString(data)
	return ids, &next, nil
}
