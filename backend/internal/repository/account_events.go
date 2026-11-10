package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
)

// Event rows sort by start time: EVENT#{2006-01-02T15:04:05Z}#{eventId}. A
// reference row EVENTREF#{eventId} records the current event sort key so an
// event can be addressed by ID after its start time changes.
const (
	eventPrefix    = "EVENT#"
	eventRefPrefix = "EVENTREF#"
)

var ErrEventAccess = errors.New("account event access revoked")

// ErrEventEncryptionUnavailable shares the contact sentinel: both hold
// third-party personal data and fail closed without a key (ADR-063).
var ErrEventEncryptionUnavailable = ErrContactEncryptionUnavailable

// ErrEventUnreadable means one stored event (or its reference row) cannot be
// opened; like ErrContactUnreadable it never covers a missing key or a
// transport failure.
var ErrEventUnreadable = errors.New("account event unreadable")

type storedEvent struct {
	PK       string `dynamodbav:"PK"`
	SK       string `dynamodbav:"SK"`
	EventID  string `dynamodbav:"eventId"`
	Revision string `dynamodbav:"revision"`
	StartAt  string `dynamodbav:"startAt"`
	Sealed   string `dynamodbav:"sealed"`
}

type storedEventRef struct {
	PK      string `dynamodbav:"PK"`
	SK      string `dynamodbav:"SK"`
	EventID string `dynamodbav:"eventId"`
	EventSK string `dynamodbav:"eventSK"`
}

// EventSortKey returns the sort key of an event starting at startKey, which
// must be a UTC second-precision RFC 3339 timestamp.
func EventSortKey(startKey, id string) string { return eventPrefix + startKey + "#" + id }

func eventBinding(account, id string) map[string]string {
	return map[string]string{"purpose": "ttobak-account-event", "accountId": account, "eventId": id}
}

func (r *DynamoDBRepository) sealedEventItem(ctx context.Context, account, sk string, e *model.AccountEvent) (map[string]types.AttributeValue, error) {
	if r.sealer == nil {
		return nil, ErrEventEncryptionUnavailable
	}
	plain, err := json.Marshal(e)
	if err != nil {
		return nil, err
	}
	sealed, err := r.sealer.Seal(ctx, plain, eventBinding(account, e.EventID))
	if err != nil {
		return nil, sealingError(err)
	}
	return attributevalue.MarshalMap(storedEvent{PK: model.PrefixAccount + account, SK: sk, EventID: e.EventID, Revision: e.Revision, StartAt: e.StartAt, Sealed: sealed})
}

func (r *DynamoDBRepository) openEvent(ctx context.Context, account string, item map[string]types.AttributeValue) (model.AccountEvent, string, error) {
	var row storedEvent
	if err := attributevalue.UnmarshalMap(item, &row); err != nil {
		return model.AccountEvent{}, "", fmt.Errorf("%w: %w", ErrEventUnreadable, err)
	}
	if r.sealer == nil {
		return model.AccountEvent{}, "", ErrEventEncryptionUnavailable
	}
	if row.Sealed == "" || row.PK != model.PrefixAccount+account || !strings.HasPrefix(row.SK, eventPrefix) || !strings.HasSuffix(row.SK, "#"+row.EventID) {
		return model.AccountEvent{}, "", fmt.Errorf("%w: event row is not sealed for this account", ErrEventUnreadable)
	}
	plain, err := r.sealer.Open(ctx, row.Sealed, eventBinding(account, row.EventID))
	if err != nil {
		return model.AccountEvent{}, "", fmt.Errorf("open event: %w", openingError(ErrEventUnreadable, err))
	}
	var e model.AccountEvent
	if err := json.Unmarshal(plain, &e); err != nil || e.EventID != row.EventID {
		return model.AccountEvent{}, "", fmt.Errorf("%w: sealed event does not match its row", ErrEventUnreadable)
	}
	e.Revision, e.StartAt = row.Revision, row.StartAt
	return e, row.SK, nil
}

// GetAccountEvent returns the event and its current sort key, or nil.
func (r *DynamoDBRepository) GetAccountEvent(ctx context.Context, account, id string) (*model.AccountEvent, string, error) {
	ref, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(r.tableName), Key: contactKey(account, eventRefPrefix+id), ConsistentRead: aws.Bool(true)})
	if err != nil {
		return nil, "", err
	}
	if len(ref.Item) == 0 {
		return nil, "", nil
	}
	var pointer storedEventRef
	if err := attributevalue.UnmarshalMap(ref.Item, &pointer); err != nil {
		return nil, "", fmt.Errorf("%w: %w", ErrEventUnreadable, err)
	}
	if pointer.EventID != id || !strings.HasPrefix(pointer.EventSK, eventPrefix) || !strings.HasSuffix(pointer.EventSK, "#"+id) {
		return nil, "", fmt.Errorf("%w: invalid event reference", ErrEventUnreadable)
	}
	out, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(r.tableName), Key: contactKey(account, pointer.EventSK), ConsistentRead: aws.Bool(true)})
	if err != nil {
		return nil, "", err
	}
	if len(out.Item) == 0 {
		return nil, "", fmt.Errorf("%w: event reference has no event", ErrEventUnreadable)
	}
	e, sk, err := r.openEvent(ctx, account, out.Item)
	if err != nil {
		return nil, "", err
	}
	return &e, sk, nil
}

// ListAccountEvents returns events starting in [fromKey, toKey), ascending or
// descending, continuing after the sort key afterSK when given.
func (r *DynamoDBRepository) ListAccountEvents(ctx context.Context, account, fromKey, toKey string, descending bool, limit int, afterSK string) ([]model.AccountEvent, string, error) {
	lo, hi := eventPrefix+fromKey, eventPrefix+toKey
	key := expression.Key("PK").Equal(expression.Value(model.PrefixAccount + account)).And(expression.Key("SK").Between(expression.Value(lo), expression.Value(hi)))
	expr, err := expression.NewBuilder().WithKeyCondition(key).Build()
	if err != nil {
		return nil, "", err
	}
	in := &dynamodb.QueryInput{TableName: aws.String(r.tableName), KeyConditionExpression: expr.KeyCondition(), ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(), Limit: aws.Int32(int32(limit)), ConsistentRead: aws.Bool(true), ScanIndexForward: aws.Bool(!descending)}
	if afterSK != "" {
		if afterSK < lo || afterSK >= hi || !strings.HasPrefix(afterSK, eventPrefix) {
			return nil, "", errors.New("event cursor outside the requested range")
		}
		in.ExclusiveStartKey = contactKey(account, afterSK)
	}
	out, err := r.client.Query(ctx, in)
	if err != nil {
		return nil, "", fmt.Errorf("query events: %w", err)
	}
	events := make([]model.AccountEvent, len(out.Items))
	errs := make([]error, len(out.Items))
	sem := make(chan struct{}, contactReadConcurrency)
	var wg sync.WaitGroup
	for i, item := range out.Items {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, item map[string]types.AttributeValue) {
			defer wg.Done()
			defer func() { <-sem }()
			events[i], _, errs[i] = r.openEvent(ctx, account, item)
		}(i, item)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, "", fmt.Errorf("decode events: %w", err)
		}
	}
	next := ""
	if len(out.LastEvaluatedKey) > 0 {
		pk, pkOK := out.LastEvaluatedKey["PK"].(*types.AttributeValueMemberS)
		sk, skOK := out.LastEvaluatedKey["SK"].(*types.AttributeValueMemberS)
		if !pkOK || !skOK || pk.Value != model.PrefixAccount+account || !strings.HasPrefix(sk.Value, eventPrefix) {
			return nil, "", errors.New("invalid event page key")
		}
		next = sk.Value
	}
	return events, next, nil
}

// ExistingAccountContactIDs reports which contact IDs exist in the account.
// It reads keys only and never opens sealed contact data.
func (r *DynamoDBRepository) ExistingAccountContactIDs(ctx context.Context, account string, ids []string) (map[string]bool, error) {
	found := map[string]bool{}
	if len(ids) == 0 {
		return found, nil
	}
	if len(ids) > 100 {
		return nil, errors.New("too many contact references")
	}
	keys := make([]map[string]types.AttributeValue, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			keys = append(keys, contactKey(account, contactPrefix+id))
		}
	}
	request := map[string]types.KeysAndAttributes{r.tableName: {Keys: keys, ConsistentRead: aws.Bool(true), ProjectionExpression: aws.String("contactId")}}
	for attempt := 0; len(request) > 0; attempt++ {
		if attempt == 5 {
			return nil, errors.New("contact reference check did not complete")
		}
		out, err := r.client.BatchGetItem(ctx, &dynamodb.BatchGetItemInput{RequestItems: request})
		if err != nil {
			return nil, err
		}
		for _, item := range out.Responses[r.tableName] {
			if v, ok := item["contactId"].(*types.AttributeValueMemberS); ok && seen[v.Value] {
				found[v.Value] = true
			}
		}
		request = out.UnprocessedKeys
	}
	return found, nil
}

func classifyEventTransaction(err error, accessChecks int) error {
	var cancelled *types.TransactionCanceledException
	if !errors.As(err, &cancelled) || len(cancelled.CancellationReasons) < accessChecks {
		return err
	}
	failed := false
	for _, reason := range cancelled.CancellationReasons {
		switch code := aws.ToString(reason.Code); code {
		case "ConditionalCheckFailed":
			failed = true
		case "None", "":
		default:
			return err
		}
	}
	if !failed {
		return err
	}
	for i := 0; i < accessChecks; i++ {
		if aws.ToString(cancelled.CancellationReasons[i].Code) == "ConditionalCheckFailed" {
			return ErrEventAccess
		}
	}
	return ErrConditionFailed
}

// WriteAccountEvent creates, updates or deletes an event together with its
// reference row and both canonical access checks in one transaction.
// previousSK is the event's sort key as observed by the caller (update/delete).
func (r *DynamoDBRepository) WriteAccountEvent(ctx context.Context, account, user string, e *model.AccountEvent, op, newSK, previousSK, previousRevision string) error {
	exists, err := expression.NewBuilder().WithCondition(expression.AttributeExists(expression.Name("PK"))).Build()
	if err != nil {
		return err
	}
	absent, err := expression.NewBuilder().WithCondition(expression.AttributeNotExists(expression.Name("PK"))).Build()
	if err != nil {
		return err
	}
	table := aws.String(r.tableName)
	check := func(sk string) types.TransactWriteItem {
		return types.TransactWriteItem{ConditionCheck: &types.ConditionCheck{TableName: table, Key: contactKey(account, sk), ConditionExpression: exists.Condition(), ExpressionAttributeNames: exists.Names()}}
	}
	open, err := r.accountOpenCheck(account)
	if err != nil {
		return err
	}
	items := []types.TransactWriteItem{open, check(model.PrefixMember + user)}
	refSK := eventRefPrefix + e.EventID
	refAt := func(sk string) (expression.Expression, error) {
		return expression.NewBuilder().WithCondition(expression.Name("eventSK").Equal(expression.Value(sk))).Build()
	}
	observed := func() (expression.Expression, error) {
		return expression.NewBuilder().WithCondition(expression.AttributeExists(expression.Name("PK")).And(expression.Name("revision").Equal(expression.Value(previousRevision)))).Build()
	}
	put := func(item map[string]types.AttributeValue) types.TransactWriteItem {
		return types.TransactWriteItem{Put: &types.Put{TableName: table, Item: item, ConditionExpression: absent.Condition(), ExpressionAttributeNames: absent.Names()}}
	}
	refItem := func(sk string) (map[string]types.AttributeValue, error) {
		return attributevalue.MarshalMap(storedEventRef{PK: model.PrefixAccount + account, SK: refSK, EventID: e.EventID, EventSK: sk})
	}
	switch op {
	case "create":
		item, err := r.sealedEventItem(ctx, account, newSK, e)
		if err != nil {
			return err
		}
		ref, err := refItem(newSK)
		if err != nil {
			return err
		}
		items = append(items, put(item), put(ref))
	case "update":
		item, err := r.sealedEventItem(ctx, account, newSK, e)
		if err != nil {
			return err
		}
		cond, err := observed()
		if err != nil {
			return err
		}
		at, err := refAt(previousSK)
		if err != nil {
			return err
		}
		if newSK == previousSK {
			items = append(items,
				types.TransactWriteItem{Put: &types.Put{TableName: table, Item: item, ConditionExpression: cond.Condition(), ExpressionAttributeNames: cond.Names(), ExpressionAttributeValues: cond.Values()}},
				types.TransactWriteItem{ConditionCheck: &types.ConditionCheck{TableName: table, Key: contactKey(account, refSK), ConditionExpression: at.Condition(), ExpressionAttributeNames: at.Names(), ExpressionAttributeValues: at.Values()}})
		} else {
			moved, err := expression.NewBuilder().WithCondition(expression.Name("eventSK").Equal(expression.Value(previousSK))).WithUpdate(expression.Set(expression.Name("eventSK"), expression.Value(newSK))).Build()
			if err != nil {
				return err
			}
			items = append(items,
				types.TransactWriteItem{Delete: &types.Delete{TableName: table, Key: contactKey(account, previousSK), ConditionExpression: cond.Condition(), ExpressionAttributeNames: cond.Names(), ExpressionAttributeValues: cond.Values()}},
				put(item),
				types.TransactWriteItem{Update: &types.Update{TableName: table, Key: contactKey(account, refSK), ConditionExpression: moved.Condition(), UpdateExpression: moved.Update(), ExpressionAttributeNames: moved.Names(), ExpressionAttributeValues: moved.Values()}})
		}
	case "delete":
		cond, err := observed()
		if err != nil {
			return err
		}
		at, err := refAt(previousSK)
		if err != nil {
			return err
		}
		items = append(items,
			types.TransactWriteItem{Delete: &types.Delete{TableName: table, Key: contactKey(account, previousSK), ConditionExpression: cond.Condition(), ExpressionAttributeNames: cond.Names(), ExpressionAttributeValues: cond.Values()}},
			types.TransactWriteItem{Delete: &types.Delete{TableName: table, Key: contactKey(account, refSK), ConditionExpression: at.Condition(), ExpressionAttributeNames: at.Names(), ExpressionAttributeValues: at.Values()}})
	default:
		return errors.New("invalid event operation")
	}
	if _, err := r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: items}); err != nil {
		if merging := mergeFenceError(err); merging != nil {
			return merging
		}
		return classifyEventTransaction(err, 2)
	}
	return nil
}
