package repository

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
)

// ErrInvalidAccountKBCursor reports a cursor that is not a KBREF# sort key.
var ErrInvalidAccountKBCursor = errors.New("invalid account KB cursor")

// ErrAccountKBRefUntagged means a member untagged this ref (a KBUNTAG#
// tombstone exists), so it must not be recreated.
var ErrAccountKBRefUntagged = errors.New("account KB ref was untagged")

type accountKBRefItem struct {
	PK         string `dynamodbav:"PK"`
	SK         string `dynamodbav:"SK"`
	EntityType string `dynamodbav:"entityType"`
	model.AccountKBRef
}

const (
	entityTypeAccountKBRef   = "ACCOUNT_KB_REF"
	entityTypeAccountKBUntag = "ACCOUNT_KB_UNTAG"
)

type accountKBUntagItem struct {
	PK         string    `dynamodbav:"PK"`
	SK         string    `dynamodbav:"SK"`
	EntityType string    `dynamodbav:"entityType"`
	AccountID  string    `dynamodbav:"accountId"`
	RefID      string    `dynamodbav:"refId"`
	UntaggedAt time.Time `dynamodbav:"untaggedAt"`
}

// accountKBCancelled reports which transaction items failed their condition;
// ok is false for any other cancellation reason.
func accountKBCancelled(err error, items int) (failed []bool, ok bool) {
	var cancelled *types.TransactionCanceledException
	if !errors.As(err, &cancelled) || len(cancelled.CancellationReasons) != items {
		return nil, false
	}
	failed = make([]bool, items)
	for i, reason := range cancelled.CancellationReasons {
		switch aws.ToString(reason.Code) {
		case "None", "":
		case "ConditionalCheckFailed":
			failed[i] = true
		default:
			return nil, false
		}
	}
	return failed, true
}

func validAccountKBRef(ref *model.AccountKBRef) bool {
	return ref != nil && ref.AccountID != "" && !strings.ContainsAny(ref.AccountID, "#/") &&
		ref.UploadedBy != "" && strings.HasPrefix(ref.S3Key, "kb/") &&
		ref.RefID == model.AccountKBRefID(ref.S3Key)
}

// PutAccountKBRef creates the ref only if absent and never untagged, in one
// transaction with a KBUNTAG# absence check. An existing ref is
// ErrConditionFailed; an untagged one also wraps ErrAccountKBRefUntagged.
func (r *DynamoDBRepository) PutAccountKBRef(ctx context.Context, ref *model.AccountKBRef) error {
	if !validAccountKBRef(ref) {
		return fmt.Errorf("invalid account KB ref")
	}
	pk := model.PrefixAccount + ref.AccountID
	item := accountKBRefItem{PK: pk, SK: model.PrefixAccountKBRef + ref.RefID,
		EntityType: entityTypeAccountKBRef, AccountKBRef: *ref}
	av, err := attributevalue.MarshalMap(item)
	if err != nil {
		return fmt.Errorf("marshal account KB ref: %w", err)
	}
	absent, err := expression.NewBuilder().WithCondition(expression.AttributeNotExists(expression.Name("PK"))).Build()
	if err != nil {
		return err
	}
	items := []types.TransactWriteItem{
		{Put: &types.Put{TableName: aws.String(r.tableName), Item: av,
			ConditionExpression: absent.Condition(), ExpressionAttributeNames: absent.Names()}},
		{ConditionCheck: &types.ConditionCheck{TableName: aws.String(r.tableName), Key: accountNewsKey(pk, model.PrefixAccountKBUntag+ref.RefID),
			ConditionExpression: absent.Condition(), ExpressionAttributeNames: absent.Names()}},
	}
	_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: items})
	if err == nil {
		return nil
	}
	if failed, ok := accountKBCancelled(err, len(items)); ok {
		switch {
		case failed[1]:
			return fmt.Errorf("%w: %w", ErrConditionFailed, ErrAccountKBRefUntagged)
		case failed[0]:
			return fmt.Errorf("%w: account KB ref exists", ErrConditionFailed)
		}
	}
	return fmt.Errorf("put account KB ref: %w", err)
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

// DeleteAccountKBRef removes the ref row only (never the S3 object) and, in
// the same transaction, writes the KBUNTAG# tombstone that keeps the ref from
// being recreated. A non-empty uploadedBy additionally requires the row's
// uploader to match. A missing row or a failed match is ErrConditionFailed.
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
	pk := model.PrefixAccount + accountID
	tombstone, err := attributevalue.MarshalMap(accountKBUntagItem{PK: pk, SK: model.PrefixAccountKBUntag + refID,
		EntityType: entityTypeAccountKBUntag, AccountID: accountID, RefID: refID, UntaggedAt: time.Now().UTC()})
	if err != nil {
		return fmt.Errorf("marshal account KB untag row: %w", err)
	}
	items := []types.TransactWriteItem{
		{Delete: &types.Delete{TableName: aws.String(r.tableName), Key: accountNewsKey(pk, model.PrefixAccountKBRef+refID),
			ConditionExpression: expr.Condition(), ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values()}},
		{Put: &types.Put{TableName: aws.String(r.tableName), Item: tombstone}},
	}
	_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: items})
	if err == nil {
		return nil
	}
	if failed, ok := accountKBCancelled(err, len(items)); ok && failed[0] {
		return fmt.Errorf("%w: account KB ref", ErrConditionFailed)
	}
	return fmt.Errorf("delete account KB ref: %w", err)
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
