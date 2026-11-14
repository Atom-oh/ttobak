package repository

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
)

type updateFeedSubRow struct {
	customerUpdateKeys
	model.UpdateFeedSub
}

func feedSubKey(feedHash, userID, sourceID string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		"PK": &types.AttributeValueMemberS{Value: model.PKUpdateFeeds},
		"SK": &types.AttributeValueMemberS{Value: model.UpdateFeedSubSK(feedHash, userID, sourceID)},
	}
}

func validFeedSub(sub *model.UpdateFeedSub) error {
	if sub == nil || sub.FeedHash == "" || sub.UserID == "" || sub.SourceID == "" {
		return fmt.Errorf("feed subscription needs feedHash, userId and sourceId")
	}
	return nil
}

// putSourceWithFeedSubItems builds the transaction that creates a source row
// (attribute_not_exists) together with its registry row.
func putSourceWithFeedSubItems(tableName, userID string, s *model.UpdateSource, sub *model.UpdateFeedSub) ([]types.TransactWriteItem, error) {
	if err := validFeedSub(sub); err != nil {
		return nil, err
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
	subAV, err := attributevalue.MarshalMap(updateFeedSubRow{
		customerUpdateKeys: customerUpdateKeys{
			PK: model.PKUpdateFeeds, SK: model.UpdateFeedSubSK(sub.FeedHash, sub.UserID, sub.SourceID), EntityType: "UPDATE_FEED_SUB",
		},
		UpdateFeedSub: *sub,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal feed subscription: %w", err)
	}
	cond, err := absentCondition()
	if err != nil {
		return nil, fmt.Errorf("build create-source condition: %w", err)
	}
	return []types.TransactWriteItem{
		{Put: &types.Put{
			TableName: aws.String(tableName), Item: srcAV,
			ConditionExpression: cond.Condition(), ExpressionAttributeNames: cond.Names(),
		}},
		{Put: &types.Put{TableName: aws.String(tableName), Item: subAV}},
	}, nil
}

// setSourceStatusWithFeedSubItems builds the transaction that sets the source
// status and feedHash (the source row must exist) and upserts the full registry
// row, so a source created before the registry existed gains its row. An
// existing fannedAt is kept. With heal, the status is left alone and the source
// must still have sub.Status and no feedHash, so a concurrent change wins.
func setSourceStatusWithFeedSubItems(tableName, userID, sourceID string, sub *model.UpdateFeedSub, heal bool) ([]types.TransactWriteItem, error) {
	if err := validFeedSub(sub); err != nil {
		return nil, err
	}
	srcUpdate := expression.Set(expression.Name("feedHash"), expression.Value(sub.FeedHash))
	srcCond := expression.AttributeExists(expression.Name("PK"))
	if heal {
		srcCond = srcCond.And(expression.Name("status").Equal(expression.Value(sub.Status)),
			expression.AttributeNotExists(expression.Name("feedHash")))
	} else {
		srcUpdate = srcUpdate.Set(expression.Name("status"), expression.Value(sub.Status))
	}
	srcExpr, err := expression.NewBuilder().WithUpdate(srcUpdate).WithCondition(srcCond).Build()
	if err != nil {
		return nil, fmt.Errorf("build update-source expression: %w", err)
	}
	regExpr, err := expression.NewBuilder().WithUpdate(
		expression.Set(expression.Name("entityType"), expression.Value("UPDATE_FEED_SUB")).
			Set(expression.Name("feedHash"), expression.Value(sub.FeedHash)).
			Set(expression.Name("url"), expression.Value(sub.URL)).
			Set(expression.Name("kind"), expression.Value(sub.Kind)).
			Set(expression.Name("userId"), expression.Value(sub.UserID)).
			Set(expression.Name("sourceId"), expression.Value(sub.SourceID)).
			Set(expression.Name("status"), expression.Value(sub.Status)).
			Set(expression.Name("fannedAt"), expression.IfNotExists(expression.Name("fannedAt"), expression.Value(sub.FannedAt))),
	).Build()
	if err != nil {
		return nil, fmt.Errorf("build feed-subscription upsert: %w", err)
	}
	return []types.TransactWriteItem{
		{Update: &types.Update{
			TableName: aws.String(tableName), Key: updateUserKey(userID, model.UpdateSourceSK(sourceID)),
			UpdateExpression: srcExpr.Update(), ConditionExpression: srcExpr.Condition(),
			ExpressionAttributeNames: srcExpr.Names(), ExpressionAttributeValues: srcExpr.Values(),
		}},
		{Update: &types.Update{
			TableName: aws.String(tableName), Key: feedSubKey(sub.FeedHash, sub.UserID, sub.SourceID),
			UpdateExpression:         regExpr.Update(),
			ExpressionAttributeNames: regExpr.Names(), ExpressionAttributeValues: regExpr.Values(),
		}},
	}, nil
}

// deleteSourceWithFeedSubItems builds the transaction that deletes a source and
// its registry row. Neither delete is conditional: a legacy source has no
// registry row, and deleting an absent source succeeds.
func deleteSourceWithFeedSubItems(tableName, userID, sourceID, feedHash string) ([]types.TransactWriteItem, error) {
	if feedHash == "" || userID == "" || sourceID == "" {
		return nil, fmt.Errorf("feed deletion needs feedHash, userId and sourceId")
	}
	return []types.TransactWriteItem{
		{Delete: &types.Delete{TableName: aws.String(tableName), Key: updateUserKey(userID, model.UpdateSourceSK(sourceID))}},
		{Delete: &types.Delete{TableName: aws.String(tableName), Key: feedSubKey(feedHash, userID, sourceID)}},
	}, nil
}

func feedSubFannedAtInput(tableName string, sub model.UpdateFeedSub, at time.Time) (*dynamodb.UpdateItemInput, error) {
	if err := validFeedSub(&sub); err != nil {
		return nil, err
	}
	expr, err := expression.NewBuilder().
		WithUpdate(expression.Set(expression.Name("fannedAt"), expression.Value(at.UTC()))).
		WithCondition(expression.AttributeExists(expression.Name("PK"))).Build()
	if err != nil {
		return nil, fmt.Errorf("build fannedAt update: %w", err)
	}
	return &dynamodb.UpdateItemInput{
		TableName: aws.String(tableName), Key: feedSubKey(sub.FeedHash, sub.UserID, sub.SourceID),
		UpdateExpression: expr.Update(), ConditionExpression: expr.Condition(),
		ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(),
	}, nil
}

// transactCondition maps a cancelled transaction with a failed condition to
// ErrConditionFailed; other errors are wrapped with action.
func transactCondition(err error, action, conflict string) error {
	var cancelled *types.TransactionCanceledException
	if errors.As(err, &cancelled) {
		for _, reason := range cancelled.CancellationReasons {
			if aws.ToString(reason.Code) == "ConditionalCheckFailed" {
				return fmt.Errorf("%w: %s", ErrConditionFailed, conflict)
			}
		}
	}
	return fmt.Errorf("failed to %s: %w", action, err)
}

// PutSourceWithFeedSub creates a source and its feed registry row in one
// transaction; an existing sourceId returns ErrConditionFailed.
func (r *CustomerUpdateRepository) PutSourceWithFeedSub(ctx context.Context, userID string, s *model.UpdateSource, sub *model.UpdateFeedSub) error {
	items, err := putSourceWithFeedSubItems(r.tableName, userID, s, sub)
	if err != nil {
		return err
	}
	if _, err := r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: items}); err != nil {
		return transactCondition(err, "create update source with feed subscription", "update source already exists")
	}
	return nil
}

// SetSourceStatusWithFeedSub sets a source's status (it must exist) and upserts
// its registry row; sub carries the new status. A missing source returns
// ErrConditionFailed.
func (r *CustomerUpdateRepository) SetSourceStatusWithFeedSub(ctx context.Context, userID, sourceID string, sub *model.UpdateFeedSub) error {
	items, err := setSourceStatusWithFeedSubItems(r.tableName, userID, sourceID, sub, false)
	if err != nil {
		return err
	}
	if _, err := r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: items}); err != nil {
		return transactCondition(err, "update source status with feed subscription", "update source not found")
	}
	return nil
}

// HealSourceFeedSub gives a legacy source (no stored feedHash) its registry row
// and stores the feedHash, keeping the source's status (sub.Status). A source
// that changed meanwhile returns ErrConditionFailed.
func (r *CustomerUpdateRepository) HealSourceFeedSub(ctx context.Context, userID, sourceID string, sub *model.UpdateFeedSub) error {
	items, err := setSourceStatusWithFeedSubItems(r.tableName, userID, sourceID, sub, true)
	if err != nil {
		return err
	}
	if _, err := r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: items}); err != nil {
		return transactCondition(err, "heal update source feed subscription", "update source changed")
	}
	return nil
}

// DeleteSourceWithFeedSub removes a source and its registry row in one
// transaction. It does not require either row to exist.
func (r *CustomerUpdateRepository) DeleteSourceWithFeedSub(ctx context.Context, userID, sourceID, feedHash string) error {
	items, err := deleteSourceWithFeedSubItems(r.tableName, userID, sourceID, feedHash)
	if err != nil {
		return err
	}
	if _, err := r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: items}); err != nil {
		return fmt.Errorf("failed to delete update source with feed subscription: %w", err)
	}
	return nil
}

func encodeFeedSubCursor(sk string) string { return base64.RawURLEncoding.EncodeToString([]byte(sk)) }

func decodeFeedSubCursor(cursor string) (map[string]types.AttributeValue, error) {
	if cursor == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil || len(raw) == 0 {
		return nil, fmt.Errorf("invalid feed subscription cursor")
	}
	return map[string]types.AttributeValue{
		"PK": &types.AttributeValueMemberS{Value: model.PKUpdateFeeds},
		"SK": &types.AttributeValueMemberS{Value: string(raw)},
	}, nil
}

// ListFeedSubs returns up to limit registry rows after cursor in key order and
// the cursor for the next page ("" on the last page).
func (r *CustomerUpdateRepository) ListFeedSubs(ctx context.Context, cursor string, limit int) ([]model.UpdateFeedSub, string, error) {
	if limit < 1 {
		return nil, "", fmt.Errorf("feed subscription limit must be positive")
	}
	start, err := decodeFeedSubCursor(cursor)
	if err != nil {
		return nil, "", err
	}
	expr, err := expression.NewBuilder().WithKeyCondition(
		expression.Key("PK").Equal(expression.Value(model.PKUpdateFeeds))).Build()
	if err != nil {
		return nil, "", fmt.Errorf("failed to build expression: %w", err)
	}
	res, err := r.client.Query(ctx, &dynamodb.QueryInput{
		TableName:                 aws.String(r.tableName),
		KeyConditionExpression:    expr.KeyCondition(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
		ExclusiveStartKey:         start,
		Limit:                     aws.Int32(int32(limit)),
	})
	if err != nil {
		return nil, "", fmt.Errorf("failed to query feed subscriptions: %w", err)
	}
	var rows []updateFeedSubRow
	if err := attributevalue.UnmarshalListOfMaps(res.Items, &rows); err != nil {
		return nil, "", fmt.Errorf("failed to unmarshal feed subscriptions: %w", err)
	}
	out := make([]model.UpdateFeedSub, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.UpdateFeedSub)
	}
	next := ""
	if len(res.LastEvaluatedKey) > 0 {
		if sk, ok := res.LastEvaluatedKey["SK"].(*types.AttributeValueMemberS); ok {
			next = encodeFeedSubCursor(sk.Value)
		}
	}
	return out, next, nil
}

// GetFeedState returns a feed's shared state, or nil when none exists yet.
func (r *CustomerUpdateRepository) GetFeedState(ctx context.Context, feedHash string) (*model.UpdateFeedState, error) {
	if feedHash == "" {
		return nil, fmt.Errorf("feedHash is required")
	}
	res, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName), ConsistentRead: aws.Bool(true),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: model.UpdateFeedPK(feedHash)},
			"SK": &types.AttributeValueMemberS{Value: model.SKUpdateFeedState},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get feed state: %w", err)
	}
	if res.Item == nil {
		return nil, nil
	}
	var state model.UpdateFeedState
	if err := attributevalue.UnmarshalMap(res.Item, &state); err != nil {
		return nil, fmt.Errorf("failed to unmarshal feed state: %w", err)
	}
	return &state, nil
}

// selectFeedEntries keeps entries seen after since, orders them by seenAt then
// entryHash ascending, and returns at most limit.
func selectFeedEntries(entries []model.UpdateFeedEntry, since time.Time, limit int) []model.UpdateFeedEntry {
	out := make([]model.UpdateFeedEntry, 0, len(entries))
	for _, e := range entries {
		if e.SeenAt.After(since) {
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].SeenAt.Equal(out[j].SeenAt) {
			return out[i].SeenAt.Before(out[j].SeenAt)
		}
		return out[i].EntryHash < out[j].EntryHash
	})
	if limit >= 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// selectNewestFeedEntries is selectFeedEntries keeping the newest limit
// entries instead of the oldest, still oldest first.
func selectNewestFeedEntries(entries []model.UpdateFeedEntry, since time.Time, limit int) []model.UpdateFeedEntry {
	out := selectFeedEntries(entries, since, -1)
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

// ListFeedEntries returns at most limit entries of a feed seen after since,
// oldest first. Entry sort keys are not time ordered, so every ENTRY# row is
// read (paginated) and filtered in memory.
func (r *CustomerUpdateRepository) ListFeedEntries(ctx context.Context, feedHash string, since time.Time, limit int) ([]model.UpdateFeedEntry, error) {
	all, err := r.queryFeedEntries(ctx, feedHash, limit)
	if err != nil {
		return nil, err
	}
	return selectFeedEntries(all, since, limit), nil
}

// ListNewestFeedEntries returns the newest limit entries of a feed seen after
// since, oldest first.
func (r *CustomerUpdateRepository) ListNewestFeedEntries(ctx context.Context, feedHash string, since time.Time, limit int) ([]model.UpdateFeedEntry, error) {
	all, err := r.queryFeedEntries(ctx, feedHash, limit)
	if err != nil {
		return nil, err
	}
	return selectNewestFeedEntries(all, since, limit), nil
}

func (r *CustomerUpdateRepository) queryFeedEntries(ctx context.Context, feedHash string, limit int) ([]model.UpdateFeedEntry, error) {
	if feedHash == "" || limit < 1 {
		return nil, fmt.Errorf("feedHash and a positive limit are required")
	}
	expr, err := expression.NewBuilder().WithKeyCondition(
		expression.Key("PK").Equal(expression.Value(model.UpdateFeedPK(feedHash))).
			And(expression.Key("SK").BeginsWith(model.PrefixUpdateFeedEntry))).Build()
	if err != nil {
		return nil, fmt.Errorf("failed to build expression: %w", err)
	}
	input := &dynamodb.QueryInput{
		TableName:                 aws.String(r.tableName),
		KeyConditionExpression:    expr.KeyCondition(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
	}
	var all []model.UpdateFeedEntry
	for {
		res, err := r.client.Query(ctx, input)
		if err != nil {
			return nil, fmt.Errorf("failed to query feed entries: %w", err)
		}
		var rows []model.UpdateFeedEntry
		if err := attributevalue.UnmarshalListOfMaps(res.Items, &rows); err != nil {
			return nil, fmt.Errorf("failed to unmarshal feed entries: %w", err)
		}
		all = append(all, rows...)
		if len(res.LastEvaluatedKey) == 0 {
			return all, nil
		}
		input.ExclusiveStartKey = res.LastEvaluatedKey
	}
}

// SetFeedSubFannedAt records when the fan-out worker last processed sub's feed
// for that subscriber. A deleted registry row returns ErrConditionFailed.
func (r *CustomerUpdateRepository) SetFeedSubFannedAt(ctx context.Context, sub model.UpdateFeedSub, at time.Time) error {
	in, err := feedSubFannedAtInput(r.tableName, sub, at)
	if err != nil {
		return err
	}
	if _, err := r.client.UpdateItem(ctx, in); err != nil {
		if isConditionFailed(err) {
			return fmt.Errorf("%w: feed subscription not found", ErrConditionFailed)
		}
		return fmt.Errorf("failed to update feed subscription: %w", err)
	}
	return nil
}
