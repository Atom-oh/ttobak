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

// PutSource creates or updates a crawler source
// PK: CRAWLER#{sourceID}, SK: CONFIG
func (r *CrawlerRepository) PutSource(ctx context.Context, source *model.CrawlerSource) error {
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

	_, err = r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(r.tableName),
		Item:      av,
	})
	if err != nil {
		return fmt.Errorf("failed to put crawler source: %w", err)
	}

	return nil
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
