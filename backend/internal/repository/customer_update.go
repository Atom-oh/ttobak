package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/sealing"
)

// CustomerUpdateRepository stores customer update sources, inbox items, shared
// URL cache rows, drafts and per-account opt rows. Rows never log URLs, labels
// or draft text; draft text is stored sealed.
type CustomerUpdateRepository struct {
	client    *dynamodb.Client
	tableName string
	// now is the clock used for TTL defaults and expired-row conditions.
	now func() time.Time
	// sealer seals draft text; nil makes draft reads and writes fail closed.
	sealer sealing.Sealer
}

// NewCustomerUpdateRepository creates a CustomerUpdateRepository.
func NewCustomerUpdateRepository(client *dynamodb.Client, tableName string) *CustomerUpdateRepository {
	return &CustomerUpdateRepository{client: client, tableName: tableName, now: time.Now}
}

// SetRecordSealer sets the KMS envelope sealer for draft rows.
func (r *CustomerUpdateRepository) SetRecordSealer(s sealing.Sealer) { r.sealer = s }

func (r *CustomerUpdateRepository) clock() time.Time {
	if r.now == nil {
		return time.Now().UTC()
	}
	return r.now().UTC()
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

// absentOrExpiredCondition allows a create when the row is absent or is a TTL
// row already past its expiry that the table sweeper has not deleted yet.
func absentOrExpiredCondition(now time.Time) (expression.Expression, error) {
	cond := expression.AttributeNotExists(expression.Name("PK")).
		Or(expression.Name(customerUpdateTTLAttribute).LessThanEqual(expression.Value(now.Unix())))
	return expression.NewBuilder().WithCondition(cond).Build()
}

func putIfAbsentOrExpired(tableName string, av map[string]types.AttributeValue, now time.Time) (*dynamodb.PutItemInput, error) {
	cond, err := absentOrExpiredCondition(now)
	if err != nil {
		return nil, err
	}
	return &dynamodb.PutItemInput{
		TableName: aws.String(tableName), Item: av,
		ConditionExpression:       cond.Condition(),
		ExpressionAttributeNames:  cond.Names(),
		ExpressionAttributeValues: cond.Values(),
	}, nil
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

// getSourceInput builds the strongly consistent keyed read of one source.
func getSourceInput(tableName, userID, sourceID string) *dynamodb.GetItemInput {
	return &dynamodb.GetItemInput{
		TableName: aws.String(tableName), Key: updateUserKey(userID, model.UpdateSourceSK(sourceID)),
		ConsistentRead: aws.Bool(true),
	}
}

// GetSource returns one source by key with a strongly consistent read, or nil
// when absent.
func (r *CustomerUpdateRepository) GetSource(ctx context.Context, userID, sourceID string) (*model.UpdateSource, error) {
	res, err := r.client.GetItem(ctx, getSourceInput(r.tableName, userID, sourceID))
	if err != nil {
		return nil, fmt.Errorf("failed to get update source: %w", err)
	}
	if len(res.Item) == 0 {
		return nil, nil
	}
	var row updateSourceRow
	if err := attributevalue.UnmarshalMap(res.Item, &row); err != nil {
		return nil, fmt.Errorf("failed to unmarshal update source: %w", err)
	}
	return &row.UpdateSource, nil
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
	in, err := putIfAbsentOrExpired(tableName, av, now)
	if err != nil {
		return nil, fmt.Errorf("build create-item condition: %w", err)
	}
	return in, nil
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
	now := r.clock()
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

// urlPendingInput builds the create-if-absent write for the shared URL cache row
// (PK = CUURL#{urlHash}, SK = META).
func urlPendingInput(tableName, urlHash, url string, now time.Time) (*dynamodb.PutItemInput, error) {
	av, err := attributevalue.MarshalMap(updateURLRow{
		customerUpdateKeys: customerUpdateKeys{
			PK: model.UpdateURLPK(urlHash), SK: model.SKUpdateURLMeta, EntityType: "UPDATE_URL",
		},
		UpdateURL: model.UpdateURL{
			URLHash: urlHash, URL: url, Status: model.UpdateURLPending,
			ExpiresAt: now.Add(model.UpdateURLTTL).Unix(),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal update url: %w", err)
	}
	in, err := putIfAbsentOrExpired(tableName, av, now)
	if err != nil {
		return nil, fmt.Errorf("build create-url condition: %w", err)
	}
	return in, nil
}

// EnsureURLPending creates the shared URL cache row as pending when absent; an
// existing row is left unchanged.
func (r *CustomerUpdateRepository) EnsureURLPending(ctx context.Context, urlHash, url string) error {
	in, err := urlPendingInput(r.tableName, urlHash, url, r.clock())
	if err != nil {
		return err
	}
	_, err = r.client.PutItem(ctx, in)
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

// ErrUpdateDraftEncryptionUnavailable shares the contact sentinel: drafts hold
// billing-derived and user-written text and fail closed without a key.
var ErrUpdateDraftEncryptionUnavailable = ErrContactEncryptionUnavailable

// ErrUpdateDraftUnreadable means one stored draft cannot be opened: it is
// malformed, unsealed or bound to another record. It never covers a missing
// key or a transport failure.
var ErrUpdateDraftUnreadable = errors.New("customer update draft unreadable")

// storedUpdateDraft is the at-rest form: keys, status, revision and timestamps
// stay plaintext for conditions, listing and TTL; draft text is sealed.
type storedUpdateDraft struct {
	customerUpdateKeys
	AccountID string    `dynamodbav:"accountId"`
	URLHash   string    `dynamodbav:"urlHash"`
	Status    string    `dynamodbav:"status"`
	Revision  int       `dynamodbav:"revision"`
	RunID     string    `dynamodbav:"runId"`
	CreatedAt time.Time `dynamodbav:"createdAt"`
	UpdatedAt time.Time `dynamodbav:"updatedAt"`
	ExpiresAt int64     `dynamodbav:"pendingShareExpiresAt"`
	Sealed    string    `dynamodbav:"sealed"`
}

// sealedDraftFields is the sealed JSON payload of one draft.
type sealedDraftFields struct {
	SourceURL     string                   `json:"sourceUrl"`
	SourceTitle   string                   `json:"sourceTitle"`
	Why           string                   `json:"why"`
	Body          string                   `json:"body"`
	EditedBody    string                   `json:"editedBody"`
	Opportunities []model.UpdateDraftOppty `json:"opportunities"`
	Links         []string                 `json:"links"`
	DismissReason string                   `json:"dismissReason"`
}

func draftBinding(userID, accountID, urlHash string) map[string]string {
	return map[string]string{"purpose": "ttobak-customer-update-draft", "userId": userID, "accountId": accountID, "urlHash": urlHash}
}

// sealDraft seals the draft's text fields bound to its owner and record.
func sealDraft(ctx context.Context, s sealing.Sealer, userID string, d *model.UpdateDraft) (string, error) {
	if s == nil {
		return "", ErrUpdateDraftEncryptionUnavailable
	}
	c := *d
	c.NormalizeSlices()
	plain, err := json.Marshal(sealedDraftFields{
		SourceURL: c.SourceURL, SourceTitle: c.SourceTitle, Why: c.Why, Body: c.Body, EditedBody: c.EditedBody,
		Opportunities: c.Opportunities, Links: c.Links, DismissReason: c.DismissReason,
	})
	if err != nil {
		return "", err
	}
	sealed, err := s.Seal(ctx, plain, draftBinding(userID, d.AccountID, d.URLHash))
	if err != nil {
		return "", sealingError(err)
	}
	return sealed, nil
}

// openDraft fills d's text fields from its sealed payload.
func openDraft(ctx context.Context, s sealing.Sealer, userID string, d *model.UpdateDraft) error {
	if d == nil || d.Sealed == "" {
		return ErrUpdateDraftUnreadable
	}
	if s == nil {
		return ErrUpdateDraftEncryptionUnavailable
	}
	plain, err := s.Open(ctx, d.Sealed, draftBinding(userID, d.AccountID, d.URLHash))
	if err != nil {
		return openingError(ErrUpdateDraftUnreadable, err)
	}
	var f sealedDraftFields
	if err := json.Unmarshal(plain, &f); err != nil {
		return fmt.Errorf("%w: %w", ErrUpdateDraftUnreadable, err)
	}
	d.SourceURL, d.SourceTitle, d.Why, d.Body, d.EditedBody = f.SourceURL, f.SourceTitle, f.Why, f.Body, f.EditedBody
	d.Opportunities, d.Links, d.DismissReason = f.Opportunities, f.Links, f.DismissReason
	d.NormalizeSlices()
	return nil
}

// draftFromItem returns a row's metadata and sealed blob, unopened. A row
// whose key does not match its identity keeps no sealed value, so opening it
// reports ErrUpdateDraftUnreadable.
func draftFromItem(userID string, item map[string]types.AttributeValue) (model.UpdateDraft, error) {
	var row storedUpdateDraft
	if err := attributevalue.UnmarshalMap(item, &row); err != nil {
		return model.UpdateDraft{}, fmt.Errorf("%w: %w", ErrUpdateDraftUnreadable, err)
	}
	d := model.UpdateDraft{
		AccountID: row.AccountID, URLHash: row.URLHash, Status: row.Status, Revision: row.Revision, RunID: row.RunID,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, ExpiresAt: row.ExpiresAt, Sealed: row.Sealed,
	}
	if row.PK != model.PrefixUser+userID || row.SK != model.UpdateDraftSK(row.AccountID, row.URLHash) {
		d.Sealed = ""
	}
	return d, nil
}

// draftPutInput builds the sealed create-if-absent write for a draft. The
// caller's draft is not mutated.
func draftPutInput(ctx context.Context, s sealing.Sealer, tableName, userID string, d *model.UpdateDraft, now time.Time) (*dynamodb.PutItemInput, error) {
	sealed, err := sealDraft(ctx, s, userID, d)
	if err != nil {
		return nil, err
	}
	row := storedUpdateDraft{
		customerUpdateKeys: customerUpdateKeys{
			PK: model.PrefixUser + userID, SK: model.UpdateDraftSK(d.AccountID, d.URLHash), EntityType: "UPDATE_DRAFT",
		},
		AccountID: d.AccountID, URLHash: d.URLHash, Status: d.Status, Revision: d.Revision, RunID: d.RunID,
		CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt, ExpiresAt: ttlOrDefault(d.ExpiresAt, now, model.UpdateDraftTTL), Sealed: sealed,
	}
	if row.Status == "" {
		row.Status = model.UpdateDraftNew
	}
	av, err := attributevalue.MarshalMap(row)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal update draft: %w", err)
	}
	in, err := putIfAbsentOrExpired(tableName, av, now)
	if err != nil {
		return nil, fmt.Errorf("build create-draft condition: %w", err)
	}
	return in, nil
}

// PutDraftIfAbsent creates a draft; an existing, unexpired (account, URL) draft
// returns ErrConditionFailed.
func (r *CustomerUpdateRepository) PutDraftIfAbsent(ctx context.Context, userID string, d *model.UpdateDraft) error {
	in, err := draftPutInput(ctx, r.sealer, r.tableName, userID, d, r.clock())
	if err != nil {
		return err
	}
	_, err = r.client.PutItem(ctx, in)
	if err != nil {
		if isConditionFailed(err) {
			return fmt.Errorf("%w: update draft already exists", ErrConditionFailed)
		}
		return fmt.Errorf("failed to create update draft: %w", err)
	}
	return nil
}

// GetDraft returns one opened draft, or nil when absent.
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
	d, err := draftFromItem(userID, res.Item)
	if err != nil {
		return nil, err
	}
	if err := openDraft(ctx, r.sealer, userID, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// OpenDraft fills a draft returned by ListDrafts with its sealed text.
func (r *CustomerUpdateRepository) OpenDraft(ctx context.Context, userID string, d *model.UpdateDraft) error {
	return openDraft(ctx, r.sealer, userID, d)
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

// ListDrafts returns one page of unopened drafts (metadata plus the sealed
// blob; see OpenDraft) and the cursor for the next page ("" at the end).
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
		for _, item := range res.Items {
			d, err := draftFromItem(userID, item)
			if err != nil {
				return nil, "", err
			}
			out = append(out, d)
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
// revision = expected; the sealed payload is replaced and revision becomes
// expected+1.
func draftRevisionUpdate(d *model.UpdateDraft, expected int, sealed string) (expression.Expression, error) {
	update := expression.Set(expression.Name("revision"), expression.Value(expected+1)).
		Set(expression.Name("sealed"), expression.Value(sealed)).
		Set(expression.Name("status"), expression.Value(d.Status)).
		Set(expression.Name("runId"), expression.Value(d.RunID)).
		Set(expression.Name("updatedAt"), expression.Value(d.UpdatedAt))
	if d.ExpiresAt != 0 {
		update = update.Set(expression.Name(customerUpdateTTLAttribute), expression.Value(d.ExpiresAt))
	}
	cond := expression.AttributeExists(expression.Name("PK")).
		And(expression.Name("revision").Equal(expression.Value(expected)))
	return expression.NewBuilder().WithUpdate(update).WithCondition(cond).Build()
}

// UpdateDraftIfRevision re-seals the full draft (d must carry its opened text)
// when the stored revision equals expectedRevision; otherwise it returns
// ErrConditionFailed.
func (r *CustomerUpdateRepository) UpdateDraftIfRevision(ctx context.Context, userID string, d *model.UpdateDraft, expectedRevision int) error {
	sealed, err := sealDraft(ctx, r.sealer, userID, d)
	if err != nil {
		return err
	}
	expr, err := draftRevisionUpdate(d, expectedRevision, sealed)
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
