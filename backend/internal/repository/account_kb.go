package repository

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
)

// ErrInvalidAccountKBCursor reports a cursor that is not a KBREF# sort key.
var ErrInvalidAccountKBCursor = errors.New("invalid account KB cursor")

type accountKBRefItem struct {
	PK         string `dynamodbav:"PK"`
	SK         string `dynamodbav:"SK"`
	EntityType string `dynamodbav:"entityType"`
	model.AccountKBRef
}

const entityTypeAccountKBRef = "ACCOUNT_KB_REF"

func validAccountKBRef(ref *model.AccountKBRef) bool {
	return ref != nil && ref.AccountID != "" && !strings.ContainsAny(ref.AccountID, "#/") &&
		ref.UploadedBy != "" && strings.HasPrefix(ref.S3Key, "kb/") &&
		ref.RefID == model.AccountKBRefID(ref.S3Key)
}

// PutAccountKBRef creates the ref only if absent. An existing ref is
// ErrConditionFailed.
func (r *DynamoDBRepository) PutAccountKBRef(ctx context.Context, ref *model.AccountKBRef) error {
	if !validAccountKBRef(ref) {
		return fmt.Errorf("invalid account KB ref")
	}
	item := accountKBRefItem{PK: model.PrefixAccount + ref.AccountID, SK: model.PrefixAccountKBRef + ref.RefID,
		EntityType: entityTypeAccountKBRef, AccountKBRef: *ref}
	av, err := attributevalue.MarshalMap(item)
	if err != nil {
		return fmt.Errorf("marshal account KB ref: %w", err)
	}
	expr, err := expression.NewBuilder().WithCondition(expression.AttributeNotExists(expression.Name("PK"))).Build()
	if err != nil {
		return err
	}
	_, err = r.client.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(r.tableName), Item: av,
		ConditionExpression: expr.Condition(), ExpressionAttributeNames: expr.Names()})
	var ccf *types.ConditionalCheckFailedException
	if errors.As(err, &ccf) {
		return fmt.Errorf("%w: account KB ref exists", ErrConditionFailed)
	}
	if err != nil {
		return fmt.Errorf("put account KB ref: %w", err)
	}
	return nil
}

// decodeAccountKBRef accepts only a well-formed ref row of the account.
func decodeAccountKBRef(accountID string, row map[string]types.AttributeValue) (*model.AccountKBRef, bool) {
	var item accountKBRefItem
	if err := attributevalue.UnmarshalMap(row, &item); err != nil {
		return nil, false
	}
	ref := item.AccountKBRef
	if item.PK != model.PrefixAccount+accountID || item.SK != model.PrefixAccountKBRef+ref.RefID ||
		item.EntityType != entityTypeAccountKBRef || ref.AccountID != accountID || !validAccountKBRef(&ref) {
		return nil, false
	}
	return &ref, true
}

// GetAccountKBRef reads one ref (strongly consistent). Absent or malformed
// rows return nil.
func (r *DynamoDBRepository) GetAccountKBRef(ctx context.Context, accountID, refID string) (*model.AccountKBRef, error) {
	if accountID == "" || !model.ValidAccountKBRefID(refID) {
		return nil, fmt.Errorf("invalid account KB ref key")
	}
	out, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(r.tableName),
		Key: accountNewsKey(model.PrefixAccount+accountID, model.PrefixAccountKBRef+refID), ConsistentRead: aws.Bool(true)})
	if err != nil {
		return nil, fmt.Errorf("get account KB ref: %w", err)
	}
	if len(out.Item) == 0 {
		return nil, nil
	}
	ref, ok := decodeAccountKBRef(accountID, out.Item)
	if !ok {
		return nil, nil
	}
	return ref, nil
}

// DeleteAccountKBRef removes the ref row only (never the S3 object). A
// non-empty uploadedBy additionally requires the row's uploader to match. A
// missing row or a failed match is ErrConditionFailed.
func (r *DynamoDBRepository) DeleteAccountKBRef(ctx context.Context, accountID, refID, uploadedBy string) error {
	if accountID == "" || !model.ValidAccountKBRefID(refID) {
		return fmt.Errorf("invalid account KB ref key")
	}
	cond := expression.AttributeExists(expression.Name("PK"))
	if uploadedBy != "" {
		cond = cond.And(expression.Name("uploadedBy").Equal(expression.Value(uploadedBy)))
	}
	expr, err := expression.NewBuilder().WithCondition(cond).Build()
	if err != nil {
		return err
	}
	_, err = r.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{TableName: aws.String(r.tableName),
		Key:                 accountNewsKey(model.PrefixAccount+accountID, model.PrefixAccountKBRef+refID),
		ConditionExpression: expr.Condition(), ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values()})
	var ccf *types.ConditionalCheckFailedException
	if errors.As(err, &ccf) {
		return fmt.Errorf("%w: account KB ref", ErrConditionFailed)
	}
	if err != nil {
		return fmt.Errorf("delete account KB ref: %w", err)
	}
	return nil
}

// ListAccountKBRefs returns one page of the account's KBREF# rows in sort-key
// order. The cursor is the opaque last sort key within this account.
func (r *DynamoDBRepository) ListAccountKBRefs(ctx context.Context, accountID, cursor string, limit int32) ([]model.AccountKBRef, *string, error) {
	if accountID == "" || limit <= 0 {
		return nil, nil, fmt.Errorf("invalid account KB ref listing")
	}
	key := expression.Key("PK").Equal(expression.Value(model.PrefixAccount + accountID)).
		And(expression.Key("SK").BeginsWith(model.PrefixAccountKBRef))
	expr, err := expression.NewBuilder().WithKeyCondition(key).Build()
	if err != nil {
		return nil, nil, err
	}
	in := &dynamodb.QueryInput{TableName: aws.String(r.tableName), KeyConditionExpression: expr.KeyCondition(),
		ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(), ConsistentRead: aws.Bool(true), Limit: aws.Int32(limit)}
	if cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil || !model.ValidAccountKBRefID(strings.TrimPrefix(string(raw), model.PrefixAccountKBRef)) ||
			!strings.HasPrefix(string(raw), model.PrefixAccountKBRef) {
			return nil, nil, ErrInvalidAccountKBCursor
		}
		in.ExclusiveStartKey = accountNewsKey(model.PrefixAccount+accountID, string(raw))
	}
	out, err := r.client.Query(ctx, in)
	if err != nil {
		return nil, nil, fmt.Errorf("list account KB refs: %w", err)
	}
	refs := make([]model.AccountKBRef, 0, len(out.Items))
	for _, row := range out.Items {
		if ref, ok := decodeAccountKBRef(accountID, row); ok {
			refs = append(refs, *ref)
		}
	}
	if len(out.LastEvaluatedKey) == 0 {
		return refs, nil, nil
	}
	pk, okPK := out.LastEvaluatedKey["PK"].(*types.AttributeValueMemberS)
	sk, okSK := out.LastEvaluatedKey["SK"].(*types.AttributeValueMemberS)
	if !okPK || !okSK || pk.Value != model.PrefixAccount+accountID || !strings.HasPrefix(sk.Value, model.PrefixAccountKBRef) {
		return nil, nil, fmt.Errorf("unexpected account KB continuation key")
	}
	next := base64.RawURLEncoding.EncodeToString([]byte(sk.Value))
	return refs, &next, nil
}
