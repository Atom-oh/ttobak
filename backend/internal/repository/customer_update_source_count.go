package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
)

// The followed-source cap is a counter (attribute sourceCount on
// USER#{userId} / SKUpdateSourceCount) changed in the same transaction as the
// source row. Every counted write requires the counter to exist, so a legacy
// user's counter is seeded from a strongly consistent count of the rows first
// and no counted write can change the rows between that count and the seed.
// The counter row is only ever written by UpdateItem, never replaced.
const sourceCountAttribute = "sourceCount"

// Transaction outcomes of the counted source writes. Each also matches
// ErrConditionFailed.
var (
	ErrUpdateSourceExists     = errors.New("update source already exists")
	ErrUpdateSourceCapReached = errors.New("update source cap reached")
	ErrUpdateSourceMissing    = errors.New("update source not found")
	ErrUpdateSourceCountFloor = errors.New("update source count is not positive")
)

// ErrUpdateSourceConflict means a counted source write or seed collided with a
// concurrent transaction on the same items and applied nothing; re-reading and
// retrying is safe. It does not match ErrConditionFailed.
var ErrUpdateSourceConflict = errors.New("update source write conflicted with a concurrent write")

func sourceCountKey(userID string) map[string]types.AttributeValue {
	return updateUserKey(userID, model.SKUpdateSourceCount)
}

// sourceCountUpdate builds the counter change of a counted source write.
func sourceCountUpdate(tableName, userID string, delta int, cond expression.ConditionBuilder) (*types.Update, error) {
	expr, err := expression.NewBuilder().
		WithUpdate(expression.Add(expression.Name(sourceCountAttribute), expression.Value(delta))).
		WithCondition(cond).Build()
	if err != nil {
		return nil, fmt.Errorf("build source count update: %w", err)
	}
	return &types.Update{
		TableName: aws.String(tableName), Key: sourceCountKey(userID),
		UpdateExpression: expr.Update(), ConditionExpression: expr.Condition(),
		ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(),
	}, nil
}

// createSourceItems builds the transaction that creates a source row
// (attribute_not_exists), its registry row when sub is non-nil, and increments
// the counter, which must exist and be below maxSources. The counter is last.
func createSourceItems(tableName, userID string, s *model.UpdateSource, sub *model.UpdateFeedSub, maxSources int) ([]types.TransactWriteItem, error) {
	if maxSources < 1 {
		return nil, fmt.Errorf("source cap must be positive")
	}
	srcAV, err := attributevalue.MarshalMap(updateSourceRow{
		customerUpdateKeys: customerUpdateKeys{
			PK: model.PrefixUser + userID, SK: model.UpdateSourceSK(s.SourceID), EntityType: "UPDATE_SOURCE",
		},
		UpdateSource: *s,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal update source: %w", err)
	}
	cond, err := absentCondition()
	if err != nil {
		return nil, fmt.Errorf("build create-source condition: %w", err)
	}
	items := []types.TransactWriteItem{{Put: &types.Put{
		TableName: aws.String(tableName), Item: srcAV,
		ConditionExpression: cond.Condition(), ExpressionAttributeNames: cond.Names(),
	}}}
	if sub != nil {
		if err := validFeedSub(sub); err != nil {
			return nil, err
		}
		subAV, err := attributevalue.MarshalMap(updateFeedSubRow{
			customerUpdateKeys: customerUpdateKeys{
				PK: model.PKUpdateFeeds, SK: model.UpdateFeedSubSK(sub.FeedHash, sub.UserID, sub.SourceID), EntityType: "UPDATE_FEED_SUB",
			},
			UpdateFeedSub: *sub,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to marshal feed subscription: %w", err)
		}
		items = append(items, types.TransactWriteItem{Put: &types.Put{TableName: aws.String(tableName), Item: subAV}})
	}
	counter, err := sourceCountUpdate(tableName, userID, 1,
		expression.AttributeExists(expression.Name(sourceCountAttribute)).
			And(expression.Name(sourceCountAttribute).LessThan(expression.Value(maxSources))))
	if err != nil {
		return nil, err
	}
	return append(items, types.TransactWriteItem{Update: counter}), nil
}

// removeSourceItems builds the transaction that deletes an existing source
// row, its registry row when feedHash is set (unconditionally: a legacy source
// has none), and decrements the positive counter. The counter is last.
func removeSourceItems(tableName, userID, sourceID, feedHash string) ([]types.TransactWriteItem, error) {
	if userID == "" || sourceID == "" {
		return nil, fmt.Errorf("source removal needs userId and sourceId")
	}
	exists, err := expression.NewBuilder().
		WithCondition(expression.AttributeExists(expression.Name("PK"))).Build()
	if err != nil {
		return nil, fmt.Errorf("build remove-source condition: %w", err)
	}
	items := []types.TransactWriteItem{{Delete: &types.Delete{
		TableName: aws.String(tableName), Key: updateUserKey(userID, model.UpdateSourceSK(sourceID)),
		ConditionExpression: exists.Condition(), ExpressionAttributeNames: exists.Names(),
	}}}
	if feedHash != "" {
		items = append(items, types.TransactWriteItem{Delete: &types.Delete{
			TableName: aws.String(tableName), Key: feedSubKey(feedHash, userID, sourceID),
		}})
	}
	counter, err := sourceCountUpdate(tableName, userID, -1,
		expression.Name(sourceCountAttribute).GreaterThan(expression.Value(0)))
	if err != nil {
		return nil, err
	}
	return append(items, types.TransactWriteItem{Update: counter}), nil
}

// sourceTxReason classifies a cancelled counted source write: whether the
// source row's own condition (item 0) or the counter's (the last item) failed,
// and whether any item hit a TransactionConflict.
func sourceTxReason(err error) (source, counter, conflict, ok bool) {
	var cancelled *types.TransactionCanceledException
	if !errors.As(err, &cancelled) || len(cancelled.CancellationReasons) < 2 {
		return false, false, false, false
	}
	reasons := cancelled.CancellationReasons
	failed := func(i int) bool { return aws.ToString(reasons[i].Code) == "ConditionalCheckFailed" }
	for _, r := range reasons {
		if aws.ToString(r.Code) == "TransactionConflict" {
			conflict = true
		}
	}
	return failed(0), failed(len(reasons) - 1), conflict, true
}

// sourceCreateError maps a failed create transaction. A failed condition wins
// over a conflict, and a taken source id over a full cap, so a duplicate never
// reports the cap.
func sourceCreateError(err error) error {
	source, counter, conflict, ok := sourceTxReason(err)
	switch {
	case ok && source:
		return fmt.Errorf("%w: %w", ErrConditionFailed, ErrUpdateSourceExists)
	case ok && counter:
		return fmt.Errorf("%w: %w", ErrConditionFailed, ErrUpdateSourceCapReached)
	case ok && conflict:
		return fmt.Errorf("%w: %w", ErrUpdateSourceConflict, err)
	}
	return fmt.Errorf("failed to create update source: %w", err)
}

// sourceRemoveError maps a failed remove transaction.
func sourceRemoveError(err error) error {
	source, counter, conflict, ok := sourceTxReason(err)
	switch {
	case ok && source:
		return fmt.Errorf("%w: %w", ErrConditionFailed, ErrUpdateSourceMissing)
	case ok && counter:
		return fmt.Errorf("%w: %w", ErrConditionFailed, ErrUpdateSourceCountFloor)
	case ok && conflict:
		return fmt.Errorf("%w: %w", ErrUpdateSourceConflict, err)
	}
	return fmt.Errorf("failed to remove update source: %w", err)
}

// sourceSeedError maps a failed seed write. A single-item write that collides
// with an in-flight transaction on the counter fails with
// TransactionConflictException.
func sourceSeedError(err error) error {
	var conflict *types.TransactionConflictException
	switch {
	case isConditionFailed(err):
		return fmt.Errorf("%w: update source count already seeded", ErrConditionFailed)
	case errors.As(err, &conflict):
		return fmt.Errorf("%w: %w", ErrUpdateSourceConflict, err)
	}
	return fmt.Errorf("failed to seed update source count: %w", err)
}

// CreateSource creates a source (and, with sub, its registry row) and counts
// it, in one transaction. A taken source id returns ErrUpdateSourceExists; a
// counter that is absent or at maxSources returns ErrUpdateSourceCapReached.
// The SDK reuses one ClientRequestToken across its retries, so an ambiguous
// attempt is not applied twice.
func (r *CustomerUpdateRepository) CreateSource(ctx context.Context, userID string, s *model.UpdateSource, sub *model.UpdateFeedSub, maxSources int) error {
	items, err := createSourceItems(r.tableName, userID, s, sub, maxSources)
	if err != nil {
		return err
	}
	if _, err := r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: items}); err != nil {
		return sourceCreateError(err)
	}
	return nil
}

// RemoveSource deletes an existing source (and its registry row when feedHash
// is set) and decrements the counter, in one transaction. An absent source
// returns ErrUpdateSourceMissing; a counter that is not positive returns
// ErrUpdateSourceCountFloor. Neither failure writes anything.
func (r *CustomerUpdateRepository) RemoveSource(ctx context.Context, userID, sourceID, feedHash string) error {
	items, err := removeSourceItems(r.tableName, userID, sourceID, feedHash)
	if err != nil {
		return err
	}
	if _, err := r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: items}); err != nil {
		return sourceRemoveError(err)
	}
	return nil
}

func sourceCountGetInput(tableName, userID string) *dynamodb.GetItemInput {
	return &dynamodb.GetItemInput{
		TableName: aws.String(tableName), Key: sourceCountKey(userID), ConsistentRead: aws.Bool(true),
	}
}

// GetSourceCount returns the user's source counter with a strongly consistent
// read; found is false when the counter was never seeded.
func (r *CustomerUpdateRepository) GetSourceCount(ctx context.Context, userID string) (count int, found bool, err error) {
	res, err := r.client.GetItem(ctx, sourceCountGetInput(r.tableName, userID))
	if err != nil {
		return 0, false, fmt.Errorf("failed to get update source count: %w", err)
	}
	av, ok := res.Item[sourceCountAttribute]
	if !ok {
		return 0, false, nil
	}
	if err := attributevalue.Unmarshal(av, &count); err != nil {
		return 0, false, fmt.Errorf("failed to unmarshal update source count: %w", err)
	}
	return count, true, nil
}

func countSourcesInput(tableName, userID string) (*dynamodb.QueryInput, error) {
	expr, err := expression.NewBuilder().WithKeyCondition(
		expression.Key("PK").Equal(expression.Value(model.PrefixUser + userID)).
			And(expression.Key("SK").BeginsWith(model.PrefixUpdateSource))).Build()
	if err != nil {
		return nil, fmt.Errorf("failed to build expression: %w", err)
	}
	return &dynamodb.QueryInput{
		TableName: aws.String(tableName), KeyConditionExpression: expr.KeyCondition(),
		ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(),
		ConsistentRead: aws.Bool(true), Select: types.SelectCount,
	}, nil
}

// CountSources counts all of the user's source rows with strongly consistent
// reads, following every page, so a seed reflects the real number of rows.
func (r *CustomerUpdateRepository) CountSources(ctx context.Context, userID string) (int, error) {
	input, err := countSourcesInput(r.tableName, userID)
	if err != nil {
		return 0, err
	}
	total := 0
	for {
		res, err := r.client.Query(ctx, input)
		if err != nil {
			return 0, fmt.Errorf("failed to count update sources: %w", err)
		}
		total += int(res.Count)
		if len(res.LastEvaluatedKey) == 0 {
			return total, nil
		}
		input.ExclusiveStartKey = res.LastEvaluatedKey
	}
}

func sourceCountSeedInput(tableName, userID string, n int) (*dynamodb.UpdateItemInput, error) {
	if n < 0 {
		return nil, fmt.Errorf("source count seed must not be negative")
	}
	expr, err := expression.NewBuilder().
		WithUpdate(expression.Set(expression.Name(sourceCountAttribute), expression.Value(n)).
			Set(expression.Name("entityType"), expression.Value("UPDATE_SOURCE_COUNT"))).
		WithCondition(expression.AttributeNotExists(expression.Name(sourceCountAttribute))).Build()
	if err != nil {
		return nil, fmt.Errorf("build source count seed: %w", err)
	}
	return &dynamodb.UpdateItemInput{
		TableName: aws.String(tableName), Key: sourceCountKey(userID),
		UpdateExpression: expr.Update(), ConditionExpression: expr.Condition(),
		ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(),
	}, nil
}

// SeedSourceCount writes the counter only when it is absent; a counter that
// already exists (a concurrent seed won) returns ErrConditionFailed, and a
// collision with an in-flight transaction returns ErrUpdateSourceConflict.
func (r *CustomerUpdateRepository) SeedSourceCount(ctx context.Context, userID string, n int) error {
	input, err := sourceCountSeedInput(r.tableName, userID, n)
	if err != nil {
		return err
	}
	if _, err := r.client.UpdateItem(ctx, input); err != nil {
		return sourceSeedError(err)
	}
	return nil
}
