package repository

import (
	"context"
	"errors"
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

// CrawlerRepository provides DynamoDB operations for crawler entities
type CrawlerRepository struct {
	client    *dynamodb.Client
	tableName string
}

// NewCrawlerRepository creates a new CrawlerRepository
func NewCrawlerRepository(client *dynamodb.Client, tableName string) *CrawlerRepository {
	return &CrawlerRepository{
		client:    client,
		tableName: tableName,
	}
}

// crawlerItem wraps a CrawlerSource with DynamoDB key attributes
type crawlerItem struct {
	PK         string `dynamodbav:"PK"`
	SK         string `dynamodbav:"SK"`
	EntityType string `dynamodbav:"entityType"`
	model.CrawlerSource
}

// subscriptionItem wraps a CrawlerSubscription with DynamoDB key attributes
type subscriptionItem struct {
	PK         string `dynamodbav:"PK"`
	SK         string `dynamodbav:"SK"`
	EntityType string `dynamodbav:"entityType"`
	model.CrawlerSubscription
}

// documentItem wraps a CrawledDocument with DynamoDB key attributes
type documentItem struct {
	PK         string `dynamodbav:"PK"`
	SK         string `dynamodbav:"SK"`
	EntityType string `dynamodbav:"entityType"`
	model.CrawledDocument
}

// historyItem wraps a CrawlHistory with DynamoDB key attributes
type historyItem struct {
	PK         string `dynamodbav:"PK"`
	SK         string `dynamodbav:"SK"`
	EntityType string `dynamodbav:"entityType"`
	model.CrawlHistory
}

// GetSource retrieves a crawler source by sourceID
// PK: CRAWLER#{sourceID}, SK: CONFIG
func (r *CrawlerRepository) GetSource(ctx context.Context, sourceID string) (*model.CrawlerSource, error) {
	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: model.PrefixCrawler + sourceID},
			"SK": &types.AttributeValueMemberS{Value: model.PrefixConfig},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get crawler source: %w", err)
	}

	if result.Item == nil {
		return nil, nil
	}

	var item crawlerItem
	if err := attributevalue.UnmarshalMap(result.Item, &item); err != nil {
		return nil, fmt.Errorf("failed to unmarshal crawler source: %w", err)
	}

	return &item.CrawlerSource, nil
}

// PutSourceIfAbsent creates a brand-new crawler source, conditioned on the
// PK not already existing -- AddSource's new-source branch uses this
// instead of PutSource's unconditional overwrite, since a concurrent
// AddSource for the same not-yet-existing sourceId would otherwise let the
// last writer win as OwnerID (this PR's destructive-delete gate) on a
// source the other caller believes they created. Returns ErrConditionFailed
// (mapped by the caller back into the existing-source update path) if the
// item was created by a concurrent request in the meantime.
func (r *CrawlerRepository) PutSourceIfAbsent(ctx context.Context, source *model.CrawlerSource) error {
	item := crawlerItem{
		PK:            model.PrefixCrawler + source.SourceID,
		SK:            model.PrefixConfig,
		EntityType:    "CRAWLER_SOURCE",
		CrawlerSource: *source,
	}

	av, err := attributevalue.MarshalMap(item)
	if err != nil {
		return fmt.Errorf("failed to marshal crawler source: %w", err)
	}

	condExpr, err := expression.NewBuilder().
		WithCondition(expression.AttributeNotExists(expression.Name("PK"))).
		Build()
	if err != nil {
		return fmt.Errorf("build create-source condition: %w", err)
	}

	_, err = r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:                 aws.String(r.tableName),
		Item:                      av,
		ConditionExpression:       condExpr.Condition(),
		ExpressionAttributeNames:  condExpr.Names(),
		ExpressionAttributeValues: condExpr.Values(),
	})
	if err != nil {
		var ccfe *types.ConditionalCheckFailedException
		if errors.As(err, &ccfe) {
			return fmt.Errorf("%w: source %s already exists", ErrConditionFailed, source.SourceID)
		}
		return fmt.Errorf("failed to create crawler source: %w", err)
	}

	return nil
}

// SourcePartialFields lists which CrawlerSource fields UpdateSourcePartial
// should SET -- a fixed struct (not a field-name map) so OwnerID structurally
// CANNOT be included by any caller: there is no field for it. Every caller
// that used to do a whole-item PutSource keyed off a stale read now goes
// through this instead (AddSource's existing-source merge, Unsubscribe,
// rebuildSourceUnion), so none of them can silently erase an admin's manual
// ownerId backfill (this feature's destructive-delete gate) via a lost-update
// race.
type SourcePartialFields struct {
	Subscribers *[]string
	AWSServices *[]string
	NewsQueries *[]string
	NewsSources *[]string
	CustomUrls  *[]string
	Status      *string
}

// UpdateSourcePartial performs a partial UpdateItem for the crawler source at
// sourceID, SETting exactly the non-nil fields in `fields`. Guarded by a
// ConditionExpression asserting each of those same fields still equals its
// value in `expected` (a snapshot read immediately before this call) --
// callers get ErrConditionFailed on a lost race and are expected to re-read,
// re-derive `fields`, and retry.
func (r *CrawlerRepository) UpdateSourcePartial(ctx context.Context, sourceID string, expected *model.CrawlerSource, fields SourcePartialFields) error {
	type entry struct {
		name    string
		value   interface{}
		current interface{}
	}
	var entries []entry
	if fields.Subscribers != nil {
		entries = append(entries, entry{"subscribers", *fields.Subscribers, expected.Subscribers})
	}
	if fields.AWSServices != nil {
		entries = append(entries, entry{"awsServices", *fields.AWSServices, expected.AWSServices})
	}
	if fields.NewsQueries != nil {
		entries = append(entries, entry{"newsQueries", *fields.NewsQueries, expected.NewsQueries})
	}
	if fields.NewsSources != nil {
		entries = append(entries, entry{"newsSources", *fields.NewsSources, expected.NewsSources})
	}
	if fields.CustomUrls != nil {
		entries = append(entries, entry{"customUrls", *fields.CustomUrls, expected.CustomUrls})
	}
	if fields.Status != nil {
		entries = append(entries, entry{"status", *fields.Status, expected.Status})
	}
	if len(entries) == 0 {
		return fmt.Errorf("UpdateSourcePartial: no fields given for source %s", sourceID)
	}

	update := expression.Set(expression.Name(entries[0].name), expression.Value(entries[0].value))
	cond := sourceFieldUnchangedCondition(entries[0].name, entries[0].current)
	for _, e := range entries[1:] {
		update = update.Set(expression.Name(e.name), expression.Value(e.value))
		cond = cond.And(sourceFieldUnchangedCondition(e.name, e.current))
	}

	expr, err := expression.NewBuilder().WithUpdate(update).WithCondition(cond).Build()
	if err != nil {
		return fmt.Errorf("build update-source-partial expression: %w", err)
	}

	_, err = r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: model.PrefixCrawler + sourceID},
			"SK": &types.AttributeValueMemberS{Value: model.PrefixConfig},
		},
		UpdateExpression:          expr.Update(),
		ConditionExpression:       expr.Condition(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
	})
	if err != nil {
		var ccfe *types.ConditionalCheckFailedException
		if errors.As(err, &ccfe) {
			return fmt.Errorf("%w: source %s fields changed concurrently", ErrConditionFailed, sourceID)
		}
		return fmt.Errorf("failed to update crawler source: %w", err)
	}

	return nil
}

// sourceFieldUnchangedCondition builds the per-field half of
// UpdateSourcePartial's ConditionExpression. A nil/empty list is treated as
// "attribute doesn't exist" rather than compared against an empty List
// value: a legacy source written before a given field existed has the
// attribute genuinely ABSENT in DynamoDB, not present-and-empty -- comparing
// Equal against an empty-list Value would then never match a real GetItem
// result for that item, so every UpdateSourcePartial attempt on it would
// fail its condition deterministically (not a real race, just a modeling
// mismatch), exhausting the caller's bounded retry and permanently 500ing
// legacy sources. A field this call itself previously cleared to nil (e.g.
// Unsubscribe's last-subscriber branch SETting AWSServices to a nil slice)
// is a THIRD case -- present, but marshaled to NULL rather than an empty
// List -- so the empty-value branch below ORs attribute_not_exists with an
// Equal against whatever the SDK marshals a nil/empty []string to, covering
// both "never written" and "explicitly cleared" without needing to know
// which. `status` is always non-empty (never omitempty), so it always takes
// the plain Equal path.
func sourceFieldUnchangedCondition(name string, current interface{}) expression.ConditionBuilder {
	if list, ok := current.([]string); ok {
		if len(list) == 0 {
			return expression.Or(
				expression.AttributeNotExists(expression.Name(name)),
				expression.Name(name).Equal(expression.Value(list)),
			)
		}
		return expression.Name(name).Equal(expression.Value(list))
	}
	return expression.Name(name).Equal(expression.Value(current))
}

// GetSubscription retrieves a user's subscription to a crawler source
// PK: USER#{userID}, SK: CRAWL_SUB#{sourceID}
func (r *CrawlerRepository) GetSubscription(ctx context.Context, userID, sourceID string) (*model.CrawlerSubscription, error) {
	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: model.PrefixUser + userID},
			"SK": &types.AttributeValueMemberS{Value: model.PrefixCrawlSub + sourceID},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get subscription: %w", err)
	}

	if result.Item == nil {
		return nil, nil
	}

	var item subscriptionItem
	if err := attributevalue.UnmarshalMap(result.Item, &item); err != nil {
		return nil, fmt.Errorf("failed to unmarshal subscription: %w", err)
	}

	return &item.CrawlerSubscription, nil
}

// PutSubscription creates or updates a user's subscription to a crawler source
// PK: USER#{userID}, SK: CRAWL_SUB#{sourceID}
func (r *CrawlerRepository) PutSubscription(ctx context.Context, userID string, sub *model.CrawlerSubscription) error {
	item := subscriptionItem{
		PK:                  model.PrefixUser + userID,
		SK:                  model.PrefixCrawlSub + sub.SourceID,
		EntityType:          "CRAWLER_SUBSCRIPTION",
		CrawlerSubscription: *sub,
	}

	av, err := attributevalue.MarshalMap(item)
	if err != nil {
		return fmt.Errorf("failed to marshal subscription: %w", err)
	}

	_, err = r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(r.tableName),
		Item:      av,
	})
	if err != nil {
		return fmt.Errorf("failed to put subscription: %w", err)
	}

	return nil
}

// DeleteSubscription removes a user's subscription to a crawler source
// PK: USER#{userID}, SK: CRAWL_SUB#{sourceID}
func (r *CrawlerRepository) DeleteSubscription(ctx context.Context, userID, sourceID string) error {
	_, err := r.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: model.PrefixUser + userID},
			"SK": &types.AttributeValueMemberS{Value: model.PrefixCrawlSub + sourceID},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to delete subscription: %w", err)
	}
	return nil
}

// DeleteDocument removes a single crawled document's metadata item.
// PK=CRAWLER#{sourceID}, SK=DOC#{docHash}
// Only deletes the DynamoDB item -- the caller is responsible for also
// deleting the S3 KB object (service layer, which owns the S3 client).
func (r *CrawlerRepository) DeleteDocument(ctx context.Context, sourceID, docHash string) error {
	_, err := r.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: model.PrefixCrawler + sourceID},
			"SK": &types.AttributeValueMemberS{Value: model.PrefixDoc + docHash},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to delete document: %w", err)
	}
	return nil
}

// ListUserSubscriptions lists all crawler subscriptions for a user
// Query PK=USER#{userID}, SK begins_with CRAWL_SUB#
func (r *CrawlerRepository) ListUserSubscriptions(ctx context.Context, userID string) ([]model.CrawlerSubscription, error) {
	keyEx := expression.Key("PK").Equal(expression.Value(model.PrefixUser + userID)).
		And(expression.Key("SK").BeginsWith(model.PrefixCrawlSub))
	expr, err := expression.NewBuilder().WithKeyCondition(keyEx).Build()
	if err != nil {
		return nil, fmt.Errorf("failed to build expression: %w", err)
	}

	result, err := r.client.Query(ctx, &dynamodb.QueryInput{
		TableName:                 aws.String(r.tableName),
		KeyConditionExpression:    expr.KeyCondition(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to query subscriptions: %w", err)
	}

	var items []subscriptionItem
	if err := attributevalue.UnmarshalListOfMaps(result.Items, &items); err != nil {
		return nil, fmt.Errorf("failed to unmarshal subscriptions: %w", err)
	}

	subs := make([]model.CrawlerSubscription, len(items))
	for i, item := range items {
		subs[i] = item.CrawlerSubscription
	}

	return subs, nil
}

// GetDocument retrieves a single crawled document by sourceID and docHash.
// PK=CRAWLER#{sourceID}, SK=DOC#{docHash}
func (r *CrawlerRepository) GetDocument(ctx context.Context, sourceID, docHash string) (*model.CrawledDocument, error) {
	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: model.PrefixCrawler + sourceID},
			"SK": &types.AttributeValueMemberS{Value: model.PrefixDoc + docHash},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get document: %w", err)
	}
	if result.Item == nil {
		return nil, nil
	}

	var item documentItem
	if err := attributevalue.UnmarshalMap(result.Item, &item); err != nil {
		return nil, fmt.Errorf("failed to unmarshal document: %w", err)
	}
	doc := item.CrawledDocument
	if doc.DocHash == "" {
		doc.DocHash = docHash
	}
	if doc.SourceID == "" {
		doc.SourceID = sourceID
	}
	return &doc, nil
}

// ListDocuments lists crawled documents for a source with optional type filter and pagination
// Query PK=CRAWLER#{sourceID}, SK begins_with DOC#
func (r *CrawlerRepository) ListDocuments(ctx context.Context, sourceID, docType string, limit int32, lastKey map[string]types.AttributeValue) ([]model.CrawledDocument, map[string]types.AttributeValue, int, error) {
	keyEx := expression.Key("PK").Equal(expression.Value(model.PrefixCrawler + sourceID)).
		And(expression.Key("SK").BeginsWith(model.PrefixDoc))

	builder := expression.NewBuilder().WithKeyCondition(keyEx)

	if docType != "" {
		filterEx := expression.Name("type").Equal(expression.Value(docType))
		builder = builder.WithFilter(filterEx)
	}

	expr, err := builder.Build()
	if err != nil {
		return nil, nil, 0, fmt.Errorf("failed to build expression: %w", err)
	}

	if limit == 0 {
		limit = 20
	}

	input := &dynamodb.QueryInput{
		TableName:                 aws.String(r.tableName),
		KeyConditionExpression:    expr.KeyCondition(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
		Limit:                     aws.Int32(limit),
		ScanIndexForward:          aws.Bool(false),
	}

	if expr.Filter() != nil {
		input.FilterExpression = expr.Filter()
	}

	if lastKey != nil {
		input.ExclusiveStartKey = lastKey
	}

	result, err := r.client.Query(ctx, input)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("failed to query documents: %w", err)
	}

	var items []documentItem
	if err := attributevalue.UnmarshalListOfMaps(result.Items, &items); err != nil {
		return nil, nil, 0, fmt.Errorf("failed to unmarshal documents: %w", err)
	}

	docs := make([]model.CrawledDocument, len(items))
	for i, item := range items {
		docs[i] = item.CrawledDocument
		if docs[i].DocHash == "" && strings.HasPrefix(item.SK, model.PrefixDoc) {
			docs[i].DocHash = strings.TrimPrefix(item.SK, model.PrefixDoc)
		}
		if docs[i].SourceID == "" && strings.HasPrefix(item.PK, model.PrefixCrawler) {
			docs[i].SourceID = strings.TrimPrefix(item.PK, model.PrefixCrawler)
		}
	}

	return docs, result.LastEvaluatedKey, len(docs), nil
}

// QueryDocumentsByType queries GSI4 for documents by type, sorted by crawledAt descending.
// GSI4PK = "DOC#{type}", GSI4SK = crawledAt (number). Falls back to scan if GSI4 has no data.
func (r *CrawlerRepository) QueryDocumentsByType(ctx context.Context, docType string, limit int32, page int) ([]model.CrawledDocument, int, error) {
	if limit == 0 {
		limit = 20
	}

	gsi4pk := "DOC#" + docType
	keyEx := expression.Key("GSI4PK").Equal(expression.Value(gsi4pk))
	expr, err := expression.NewBuilder().WithKeyCondition(keyEx).Build()
	if err != nil {
		return nil, 0, fmt.Errorf("failed to build expression: %w", err)
	}

	var allDocs []model.CrawledDocument
	var lastKey map[string]types.AttributeValue

	for {
		input := &dynamodb.QueryInput{
			TableName:                 aws.String(r.tableName),
			IndexName:                 aws.String("GSI4"),
			KeyConditionExpression:    expr.KeyCondition(),
			ExpressionAttributeNames:  expr.Names(),
			ExpressionAttributeValues: expr.Values(),
			ScanIndexForward:          aws.Bool(false),
		}
		if lastKey != nil {
			input.ExclusiveStartKey = lastKey
		}

		result, err := r.client.Query(ctx, input)
		if err != nil {
			return r.ListAllDocumentsByType(ctx, docType, limit, page)
		}

		var items []documentItem
		if err := attributevalue.UnmarshalListOfMaps(result.Items, &items); err != nil {
			return nil, 0, fmt.Errorf("failed to unmarshal documents: %w", err)
		}

		for _, item := range items {
			doc := item.CrawledDocument
			if doc.DocHash == "" && strings.HasPrefix(item.SK, model.PrefixDoc) {
				doc.DocHash = strings.TrimPrefix(item.SK, model.PrefixDoc)
			}
			if doc.SourceID == "" && strings.HasPrefix(item.PK, model.PrefixCrawler) {
				doc.SourceID = strings.TrimPrefix(item.PK, model.PrefixCrawler)
			}
			allDocs = append(allDocs, doc)
		}

		lastKey = result.LastEvaluatedKey
		if lastKey == nil {
			break
		}
	}

	total := len(allDocs)
	start := int(limit) * page
	if start > total {
		return []model.CrawledDocument{}, total, nil
	}
	end := start + int(limit)
	if end > total {
		end = total
	}
	return allDocs[start:end], total, nil
}

// ListAllDocumentsByType performs a table scan to find documents across all sources filtered by type.
// This is used for cross-source queries. Uses pagination with limit and page offset.
func (r *CrawlerRepository) ListAllDocumentsByType(ctx context.Context, docType string, limit int32, page int) ([]model.CrawledDocument, int, error) {
	if limit == 0 {
		limit = 20
	}

	filterEx := expression.Name("PK").BeginsWith(model.PrefixCrawler).
		And(expression.Name("SK").BeginsWith(model.PrefixDoc))
	if docType != "" {
		filterEx = filterEx.And(expression.Name("type").Equal(expression.Value(docType)))
	}

	expr, err := expression.NewBuilder().WithFilter(filterEx).Build()
	if err != nil {
		return nil, 0, fmt.Errorf("failed to build expression: %w", err)
	}

	var allDocs []model.CrawledDocument
	var lastKey map[string]types.AttributeValue
	skip := int(limit) * page

	for {
		input := &dynamodb.ScanInput{
			TableName:                 aws.String(r.tableName),
			FilterExpression:          expr.Filter(),
			ExpressionAttributeNames:  expr.Names(),
			ExpressionAttributeValues: expr.Values(),
		}

		if lastKey != nil {
			input.ExclusiveStartKey = lastKey
		}

		result, err := r.client.Scan(ctx, input)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to scan documents: %w", err)
		}

		var items []documentItem
		if err := attributevalue.UnmarshalListOfMaps(result.Items, &items); err != nil {
			return nil, 0, fmt.Errorf("failed to unmarshal documents: %w", err)
		}

		for _, item := range items {
			doc := item.CrawledDocument
			if doc.DocHash == "" && strings.HasPrefix(item.SK, model.PrefixDoc) {
				doc.DocHash = strings.TrimPrefix(item.SK, model.PrefixDoc)
			}
			if doc.SourceID == "" && strings.HasPrefix(item.PK, model.PrefixCrawler) {
				doc.SourceID = strings.TrimPrefix(item.PK, model.PrefixCrawler)
			}
			allDocs = append(allDocs, doc)
		}

		lastKey = result.LastEvaluatedKey
		if lastKey == nil {
			break
		}
	}

	total := len(allDocs)

	// Apply pagination
	start := skip
	if start > total {
		return []model.CrawledDocument{}, total, nil
	}
	end := start + int(limit)
	if end > total {
		end = total
	}

	return allDocs[start:end], total, nil
}

// ListHistory lists crawl execution history for a source
// Query PK=CRAWLER#{sourceID}, SK begins_with HISTORY#, ordered newest first
func (r *CrawlerRepository) ListHistory(ctx context.Context, sourceID string, limit int32) ([]model.CrawlHistory, error) {
	keyEx := expression.Key("PK").Equal(expression.Value(model.PrefixCrawler + sourceID)).
		And(expression.Key("SK").BeginsWith(model.PrefixHistory))
	expr, err := expression.NewBuilder().WithKeyCondition(keyEx).Build()
	if err != nil {
		return nil, fmt.Errorf("failed to build expression: %w", err)
	}

	if limit == 0 {
		limit = 20
	}

	result, err := r.client.Query(ctx, &dynamodb.QueryInput{
		TableName:                 aws.String(r.tableName),
		KeyConditionExpression:    expr.KeyCondition(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
		Limit:                     aws.Int32(limit),
		ScanIndexForward:          aws.Bool(false), // newest first
	})
	if err != nil {
		return nil, fmt.Errorf("failed to query history: %w", err)
	}

	var items []historyItem
	if err := attributevalue.UnmarshalListOfMaps(result.Items, &items); err != nil {
		return nil, fmt.Errorf("failed to unmarshal history: %w", err)
	}

	history := make([]model.CrawlHistory, len(items))
	for i, item := range items {
		history[i] = item.CrawlHistory
	}

	return history, nil
}

// nonAlphanumericRegex matches any character that is not a lowercase letter or digit
var nonAlphanumericRegex = regexp.MustCompile(`[^a-z0-9]+`)

// NormalizeSourceID converts a name to a normalized source ID
// Lowercases the string and strips non-alphanumeric characters
func (r *CrawlerRepository) NormalizeSourceID(name string) string {
	lower := strings.ToLower(name)
	return nonAlphanumericRegex.ReplaceAllString(lower, "")
}
