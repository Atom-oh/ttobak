package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
)

// CustomerUpdateRepository stores customer update sources, inbox items, shared
// URL cache rows, drafts and per-account opt rows. Rows never log URLs, labels
// or draft text.
type CustomerUpdateRepository struct {
	client    *dynamodb.Client
	tableName string
}

// NewCustomerUpdateRepository creates a CustomerUpdateRepository.
func NewCustomerUpdateRepository(client *dynamodb.Client, tableName string) *CustomerUpdateRepository {
	return &CustomerUpdateRepository{client: client, tableName: tableName}
}

const customerUpdateTTLAttribute = "pendingShareExpiresAt"

type customerUpdateKeys struct {
	PK         string `dynamodbav:"PK"`
	SK         string `dynamodbav:"SK"`
	EntityType string `dynamodbav:"entityType"`
}

type updateSourceRow struct {
	customerUpdateKeys
	model.UpdateSource
}

type updateItemRow struct {
	customerUpdateKeys
	model.UpdateItem
}

type updateDraftRow struct {
	customerUpdateKeys
	model.UpdateDraft
}

type updateURLRow struct {
	customerUpdateKeys
	model.UpdateURL
}

type updateOptRow struct {
	customerUpdateKeys
	AccountID string    `dynamodbav:"accountId"`
	Enabled   bool      `dynamodbav:"enabled"`
	UpdatedAt time.Time `dynamodbav:"updatedAt"`
}

func updateUserKey(userID, sk string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		"PK": &types.AttributeValueMemberS{Value: model.PrefixUser + userID},
		"SK": &types.AttributeValueMemberS{Value: sk},
	}
}

func isConditionFailed(err error) bool {
	var ccfe *types.ConditionalCheckFailedException
	return errors.As(err, &ccfe)
}

func absentCondition() (expression.Expression, error) {
	return expression.NewBuilder().
		WithCondition(expression.AttributeNotExists(expression.Name("PK"))).Build()
}

func ttlOrDefault(expiresAt int64, now time.Time, ttl time.Duration) int64 {
	if expiresAt != 0 {
		return expiresAt
	}
	return now.Add(ttl).Unix()
}

// PutSourceIfAbsent creates a source; an existing sourceId returns ErrConditionFailed.
func (r *CustomerUpdateRepository) PutSourceIfAbsent(ctx context.Context, userID string, s *model.UpdateSource) error {
	av, err := attributevalue.MarshalMap(updateSourceRow{
		customerUpdateKeys: customerUpdateKeys{
			PK: model.PrefixUser + userID, SK: model.UpdateSourceSK(s.SourceID), EntityType: "UPDATE_SOURCE",
		},
		UpdateSource: *s,
	})
	if err != nil {
		return fmt.Errorf("failed to marshal update source: %w", err)
	}
	cond, err := absentCondition()
	if err != nil {
		return fmt.Errorf("build create-source condition: %w", err)
	}
	_, err = r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(r.tableName), Item: av,
		ConditionExpression:      cond.Condition(),
		ExpressionAttributeNames: cond.Names(),
	})
	if err != nil {
		if isConditionFailed(err) {
			return fmt.Errorf("%w: update source already exists", ErrConditionFailed)
		}
		return fmt.Errorf("failed to create update source: %w", err)
	}
	return nil
}

// ListSources returns at most limit sources in key order.
func (r *CustomerUpdateRepository) ListSources(ctx context.Context, userID string, limit int) ([]model.UpdateSource, error) {
	if limit < 1 {
		return nil, fmt.Errorf("source limit must be positive")
	}
	expr, err := expression.NewBuilder().WithKeyCondition(
		expression.Key("PK").Equal(expression.Value(model.PrefixUser + userID)).
			And(expression.Key("SK").BeginsWith(model.PrefixUpdateSource))).Build()
	if err != nil {
		return nil, fmt.Errorf("failed to build expression: %w", err)
	}
	input := &dynamodb.QueryInput{
		TableName:                 aws.String(r.tableName),
		KeyConditionExpression:    expr.KeyCondition(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
	}
	out := []model.UpdateSource{}
	for {
		input.Limit = aws.Int32(int32(limit - len(out)))
		res, err := r.client.Query(ctx, input)
		if err != nil {
			return nil, fmt.Errorf("failed to query update sources: %w", err)
		}
		var rows []updateSourceRow
		if err := attributevalue.UnmarshalListOfMaps(res.Items, &rows); err != nil {
			return nil, fmt.Errorf("failed to unmarshal update sources: %w", err)
		}
		for _, row := range rows {
			out = append(out, row.UpdateSource)
		}
		if len(out) >= limit || len(res.LastEvaluatedKey) == 0 {
			return out, nil
		}
		input.ExclusiveStartKey = res.LastEvaluatedKey
	}
}

// UpdateSource sets the non-nil label and status; the source must exist.
func (r *CustomerUpdateRepository) UpdateSource(ctx context.Context, userID, sourceID string, label *string, status *string) error {
	var update expression.UpdateBuilder
	n := 0
	if label != nil {
		update = update.Set(expression.Name("label"), expression.Value(*label))
		n++
	}
	if status != nil {
		update = update.Set(expression.Name("status"), expression.Value(*status))
		n++
	}
	if n == 0 {
		return fmt.Errorf("UpdateSource: no fields given")
	}
	expr, err := expression.NewBuilder().WithUpdate(update).
		WithCondition(expression.AttributeExists(expression.Name("PK"))).Build()
	if err != nil {
		return fmt.Errorf("build update-source expression: %w", err)
	}
	_, err = r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(r.tableName), Key: updateUserKey(userID, model.UpdateSourceSK(sourceID)),
		UpdateExpression: expr.Update(), ConditionExpression: expr.Condition(),
		ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(),
	})
	if err != nil {
		if isConditionFailed(err) {
			return fmt.Errorf("%w: update source not found", ErrConditionFailed)
		}
		return fmt.Errorf("failed to update update source: %w", err)
	}
	return nil
}

// DeleteSource removes a source row.
func (r *CustomerUpdateRepository) DeleteSource(ctx context.Context, userID, sourceID string) error {
	_, err := r.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(r.tableName), Key: updateUserKey(userID, model.UpdateSourceSK(sourceID)),
	})
	if err != nil {
		return fmt.Errorf("failed to delete update source: %w", err)
	}
	return nil
}

// itemPutInput builds the create-if-absent write for an inbox item. SourceIDs is
// stored as a String Set so a later duplicate can ADD to it atomically. The
// caller's item is not mutated.
func itemPutInput(tableName, userID string, it *model.UpdateItem, now time.Time) (*dynamodb.PutItemInput, error) {
	row := *it
	if row.State == "" {
		row.State = model.UpdateItemNew
	}
	if row.FirstSeenAt.IsZero() {
		row.FirstSeenAt = now
	}
	row.ExpiresAt = ttlOrDefault(row.ExpiresAt, now, model.UpdateItemTTL)
	av, err := attributevalue.MarshalMap(updateItemRow{
		customerUpdateKeys: customerUpdateKeys{
			PK: model.PrefixUser + userID, SK: model.UpdateItemSK(row.URLHash), EntityType: "UPDATE_ITEM",
		},
		UpdateItem: row,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal update item: %w", err)
	}
	if len(row.SourceIDs) > 0 {
		av["sourceIds"] = &types.AttributeValueMemberSS{Value: append([]string(nil), row.SourceIDs...)}
	} else {
		delete(av, "sourceIds")
	}
	cond, err := absentCondition()
	if err != nil {
		return nil, fmt.Errorf("build create-item condition: %w", err)
	}
	return &dynamodb.PutItemInput{
		TableName: aws.String(tableName), Item: av,
		ConditionExpression:      cond.Condition(),
		ExpressionAttributeNames: cond.Names(),
	}, nil
}

// itemAddSourceExpression adds one source ID to an existing item's set.
func itemAddSourceExpression(sourceID string) (expression.Expression, error) {
	return expression.NewBuilder().
		WithUpdate(expression.Add(expression.Name("sourceIds"), expression.Value(&types.AttributeValueMemberSS{Value: []string{sourceID}}))).
		WithCondition(expression.AttributeExists(expression.Name("PK"))).Build()
}

// PutItemsIfAbsent creates each item that does not exist and returns how many
// were created. For an item that already exists, sourceID is added to its
// SourceIDs set; such an item is not counted.
func (r *CustomerUpdateRepository) PutItemsIfAbsent(ctx context.Context, userID, sourceID string, items []model.UpdateItem) (int, error) {
	created := 0
	now := time.Now().UTC()
	for i := range items {
		it := items[i]
		it.SourceIDs = []string{sourceID}
		in, err := itemPutInput(r.tableName, userID, &it, now)
		if err != nil {
			return created, err
		}
		_, err = r.client.PutItem(ctx, in)
		if err == nil {
			created++
			continue
		}
		if !isConditionFailed(err) {
			return created, fmt.Errorf("failed to create update item: %w", err)
		}
		expr, err := itemAddSourceExpression(sourceID)
		if err != nil {
			return created, fmt.Errorf("build add-source expression: %w", err)
		}
		_, err = r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
			TableName: aws.String(r.tableName), Key: updateUserKey(userID, model.UpdateItemSK(it.URLHash)),
			UpdateExpression: expr.Update(), ConditionExpression: expr.Condition(),
			ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(),
		})
		// A row that expired between the two calls is skipped; the next delivery recreates it.
		if err != nil && !isConditionFailed(err) {
			return created, fmt.Errorf("failed to add source to update item: %w", err)
		}
	}
	return created, nil
}

// EnsureURLPending creates the shared URL cache row as pending when absent; an
// existing row is left unchanged.
func (r *CustomerUpdateRepository) EnsureURLPending(ctx context.Context, urlHash, url string) error {
	pk := model.UpdateURLPK(urlHash)
	av, err := attributevalue.MarshalMap(updateURLRow{
		customerUpdateKeys: customerUpdateKeys{PK: pk, SK: pk, EntityType: "UPDATE_URL"},
		UpdateURL: model.UpdateURL{
			URLHash: urlHash, URL: url, Status: model.UpdateURLPending,
			ExpiresAt: time.Now().Add(model.UpdateURLTTL).Unix(),
		},
	})
	if err != nil {
		return fmt.Errorf("failed to marshal update url: %w", err)
	}
	cond, err := absentCondition()
	if err != nil {
		return fmt.Errorf("build create-url condition: %w", err)
	}
	_, err = r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(r.tableName), Item: av,
		ConditionExpression:      cond.Condition(),
		ExpressionAttributeNames: cond.Names(),
	})
	if err != nil && !isConditionFailed(err) {
		return fmt.Errorf("failed to create update url: %w", err)
	}
	return nil
}

// MarkPending records that the user has pending update work.
func (r *CustomerUpdateRepository) MarkPending(ctx context.Context, userID string) error {
	_, err := r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(r.tableName),
		Item: map[string]types.AttributeValue{
			"PK":         &types.AttributeValueMemberS{Value: model.PKUpdatePending},
			"SK":         &types.AttributeValueMemberS{Value: userID},
			"entityType": &types.AttributeValueMemberS{Value: "UPDATE_PENDING"},
			"updatedAt":  &types.AttributeValueMemberS{Value: time.Now().UTC().Format(time.RFC3339)},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to mark update pending: %w", err)
	}
	return nil
}

func draftRow(userID string, d *model.UpdateDraft) updateDraftRow {
	return updateDraftRow{
		customerUpdateKeys: customerUpdateKeys{
			PK: model.PrefixUser + userID, SK: model.UpdateDraftSK(d.AccountID, d.URLHash), EntityType: "UPDATE_DRAFT",
		},
		UpdateDraft: *d,
	}
}

// PutDraftIfAbsent creates a draft; an existing (account, URL) draft returns ErrConditionFailed.
func (r *CustomerUpdateRepository) PutDraftIfAbsent(ctx context.Context, userID string, d *model.UpdateDraft) error {
	now := time.Now().UTC()
	row := draftRow(userID, d)
	row.ExpiresAt = ttlOrDefault(row.ExpiresAt, now, model.UpdateDraftTTL)
	if row.Status == "" {
		row.Status = model.UpdateDraftNew
	}
	av, err := attributevalue.MarshalMap(row)
	if err != nil {
		return fmt.Errorf("failed to marshal update draft: %w", err)
	}
	cond, err := absentCondition()
	if err != nil {
		return fmt.Errorf("build create-draft condition: %w", err)
	}
	_, err = r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(r.tableName), Item: av,
		ConditionExpression:      cond.Condition(),
		ExpressionAttributeNames: cond.Names(),
	})
	if err != nil {
		if isConditionFailed(err) {
			return fmt.Errorf("%w: update draft already exists", ErrConditionFailed)
		}
		return fmt.Errorf("failed to create update draft: %w", err)
	}
	return nil
}

// GetDraft returns one draft, or nil when absent.
func (r *CustomerUpdateRepository) GetDraft(ctx context.Context, userID, accountID, urlHash string) (*model.UpdateDraft, error) {
	res, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName), Key: updateUserKey(userID, model.UpdateDraftSK(accountID, urlHash)),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get update draft: %w", err)
	}
	if res.Item == nil {
		return nil, nil
	}
	var row updateDraftRow
	if err := attributevalue.UnmarshalMap(res.Item, &row); err != nil {
		return nil, fmt.Errorf("failed to unmarshal update draft: %w", err)
	}
	return &row.UpdateDraft, nil
}

// draftListExpression builds the key condition for one account's drafts, or all
// of the user's drafts when accountID is empty.
func draftListExpression(userID, accountID string) (expression.Expression, error) {
	prefix := model.PrefixUpdateDraft
	if accountID != "" {
		prefix = model.UpdateDraftSK(accountID, "")
	}
	return expression.NewBuilder().WithKeyCondition(
		expression.Key("PK").Equal(expression.Value(model.PrefixUser + userID)).
			And(expression.Key("SK").BeginsWith(prefix))).Build()
}

// ListDrafts returns one page of drafts and the cursor for the next page ("" at the end).
func (r *CustomerUpdateRepository) ListDrafts(ctx context.Context, userID, accountID string, limit int, cursor string) ([]model.UpdateDraft, string, error) {
	if limit < 1 {
		return nil, "", fmt.Errorf("draft limit must be positive")
	}
	expr, err := draftListExpression(userID, accountID)
	if err != nil {
		return nil, "", fmt.Errorf("failed to build expression: %w", err)
	}
	input := &dynamodb.QueryInput{
		TableName:                 aws.String(r.tableName),
		KeyConditionExpression:    expr.KeyCondition(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
		ExclusiveStartKey:         decodeCursor(cursor),
	}
	out := []model.UpdateDraft{}
	for {
		input.Limit = aws.Int32(int32(limit - len(out)))
		res, err := r.client.Query(ctx, input)
		if err != nil {
			return nil, "", fmt.Errorf("failed to query update drafts: %w", err)
		}
		var rows []updateDraftRow
		if err := attributevalue.UnmarshalListOfMaps(res.Items, &rows); err != nil {
			return nil, "", fmt.Errorf("failed to unmarshal update drafts: %w", err)
		}
		for _, row := range rows {
			out = append(out, row.UpdateDraft)
		}
		if len(res.LastEvaluatedKey) == 0 {
			return out, "", nil
		}
		if len(out) >= limit {
			return out, encodeCursor(res.LastEvaluatedKey), nil
		}
		input.ExclusiveStartKey = res.LastEvaluatedKey
	}
}

// draftRevisionUpdate builds the optimistic update: the row must exist with
// revision = expected, and revision becomes expected+1.
func draftRevisionUpdate(d *model.UpdateDraft, expected int) (expression.Expression, error) {
	update := expression.Set(expression.Name("revision"), expression.Value(expected+1)).
		Set(expression.Name("body"), expression.Value(d.Body)).
		Set(expression.Name("editedBody"), expression.Value(d.EditedBody)).
		Set(expression.Name("why"), expression.Value(d.Why)).
		Set(expression.Name("opportunities"), expression.Value(d.Opportunities)).
		Set(expression.Name("links"), expression.Value(d.Links)).
		Set(expression.Name("status"), expression.Value(d.Status)).
		Set(expression.Name("dismissReason"), expression.Value(d.DismissReason)).
		Set(expression.Name("runId"), expression.Value(d.RunID)).
		Set(expression.Name("updatedAt"), expression.Value(d.UpdatedAt))
	if d.ExpiresAt != 0 {
		update = update.Set(expression.Name(customerUpdateTTLAttribute), expression.Value(d.ExpiresAt))
	}
	cond := expression.AttributeExists(expression.Name("PK")).
		And(expression.Name("revision").Equal(expression.Value(expected)))
	return expression.NewBuilder().WithUpdate(update).WithCondition(cond).Build()
}

// UpdateDraftIfRevision writes the draft's mutable fields when the stored
// revision equals expectedRevision; otherwise it returns ErrConditionFailed.
func (r *CustomerUpdateRepository) UpdateDraftIfRevision(ctx context.Context, userID string, d *model.UpdateDraft, expectedRevision int) error {
	expr, err := draftRevisionUpdate(d, expectedRevision)
	if err != nil {
		return fmt.Errorf("build update-draft expression: %w", err)
	}
	_, err = r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(r.tableName), Key: updateUserKey(userID, model.UpdateDraftSK(d.AccountID, d.URLHash)),
		UpdateExpression: expr.Update(), ConditionExpression: expr.Condition(),
		ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(),
	})
	if err != nil {
		if isConditionFailed(err) {
			return fmt.Errorf("%w: update draft revision changed", ErrConditionFailed)
		}
		return fmt.Errorf("failed to update update draft: %w", err)
	}
	return nil
}

// GetAccountOpt reports whether updates are enabled for an account; a missing row means true.
func (r *CustomerUpdateRepository) GetAccountOpt(ctx context.Context, userID, accountID string) (bool, error) {
	res, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName), Key: updateUserKey(userID, model.UpdateOptSK(accountID)),
	})
	if err != nil {
		return false, fmt.Errorf("failed to get update opt: %w", err)
	}
	if res.Item == nil {
		return true, nil
	}
	var row updateOptRow
	if err := attributevalue.UnmarshalMap(res.Item, &row); err != nil {
		return false, fmt.Errorf("failed to unmarshal update opt: %w", err)
	}
	return row.Enabled, nil
}

// PutAccountOpt stores the per-account enabled flag.
func (r *CustomerUpdateRepository) PutAccountOpt(ctx context.Context, userID, accountID string, enabled bool) error {
	av, err := attributevalue.MarshalMap(updateOptRow{
		customerUpdateKeys: customerUpdateKeys{
			PK: model.PrefixUser + userID, SK: model.UpdateOptSK(accountID), EntityType: "UPDATE_OPT",
		},
		AccountID: accountID, Enabled: enabled, UpdatedAt: time.Now().UTC(),
	})
	if err != nil {
		return fmt.Errorf("failed to marshal update opt: %w", err)
	}
	if _, err := r.client.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(r.tableName), Item: av}); err != nil {
		return fmt.Errorf("failed to put update opt: %w", err)
	}
	return nil
}
