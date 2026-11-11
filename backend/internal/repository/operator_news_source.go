package repository

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
)

// OperatorNewsSource is the code-managed part of an operator-curated global
// news source (cmd/seed-news-sources). Status is set only on creation: an
// operator who disables a seeded source keeps it disabled across reseeding.
type OperatorNewsSource struct {
	SourceID      string
	SourceName    string
	Aliases       []string
	NewsQueries   []string
	SearchDomains []string
}

// OperatorSourceOutcome is what UpsertOperatorNewsSource did (or, in a dry
// run, would do).
type OperatorSourceOutcome string

const (
	OperatorSourceCreated   OperatorSourceOutcome = "CREATE"
	OperatorSourceUpdated   OperatorSourceOutcome = "UPDATE"
	OperatorSourceUnchanged OperatorSourceOutcome = "UNCHANGED"
)

// ErrNotOperatorSource refuses to seed over a CONFIG row that a user,
// subscriber or account owns, or that is not marked global.
var ErrNotOperatorSource = errors.New("crawler source is not operator-managed")

// SearchDomainsAttribute holds an operator source's search domain allowlist
// (news_discovery.search_domains). It is outside the seven-field source
// revision but inside the crawler's transactional config guard, so changing
// it fails an in-flight run's reservation or publication instead of
// republishing under a stale scope. No API path reads or writes it.
const SearchDomainsAttribute = "searchDomains"

// operatorManaged reports whether item may be rewritten by the seed: the same
// shape IsGlobalNewsSource accepts, except that any status is allowed.
func operatorManaged(item map[string]types.AttributeValue) bool {
	marker, ok := item[GlobalNewsSourceAttribute].(*types.AttributeValueMemberBOOL)
	if !ok || !marker.Value {
		return false
	}
	for _, name := range []string{"userTiedAt", "accountId"} {
		if _, ok := item[name]; ok {
			return false
		}
	}
	if owner, ok := item["ownerId"]; ok {
		if s, isS := owner.(*types.AttributeValueMemberS); !isS || s.Value != "" {
			return false
		}
	}
	for _, name := range []string{"subscribers", "customUrls"} {
		if v, ok := item[name]; ok && !emptyAttributeList(v) {
			return false
		}
	}
	return true
}

func stringList(v types.AttributeValue) ([]string, bool) {
	if v == nil {
		return nil, true
	}
	if _, isNull := v.(*types.AttributeValueMemberNULL); isNull {
		return nil, true
	}
	var out []string
	if err := attributevalue.Unmarshal(v, &out); err != nil {
		return nil, false
	}
	return out, true
}

// operatorFieldsEqual compares the managed fields; nil and empty lists match.
func operatorFieldsEqual(item map[string]types.AttributeValue, src OperatorNewsSource) bool {
	name, _ := item["sourceName"].(*types.AttributeValueMemberS)
	if name == nil || name.Value != src.SourceName {
		return false
	}
	for attr, want := range map[string][]string{
		"aliases": src.Aliases, "newsQueries": src.NewsQueries, SearchDomainsAttribute: src.SearchDomains,
	} {
		got, ok := stringList(item[attr])
		if !ok || len(got) != len(want) || (len(want) > 0 && !slices.Equal(got, want)) {
			return false
		}
	}
	return true
}

func configKey(sourceID string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		"PK": &types.AttributeValueMemberS{Value: model.PrefixCrawler + sourceID},
		"SK": &types.AttributeValueMemberS{Value: model.PrefixConfig},
	}
}

// UpsertOperatorNewsSource creates src as an operator-curated global news
// source (globalNewsSource = true, active, no owner, subscribers or custom
// URLs), or updates the managed fields of an existing operator-managed row.
// With apply false it only reads and reports the outcome. A row a user,
// subscriber or account owns is refused with ErrNotOperatorSource and never
// written. Both writes are conditional: the create on the key being absent,
// the update on the row still being operator-managed with the name that was
// read; a concurrent change returns ErrConditionFailed.
func (r *CrawlerRepository) UpsertOperatorNewsSource(ctx context.Context, src OperatorNewsSource, apply bool) (OperatorSourceOutcome, error) {
	if src.SourceID == "" || src.SourceName == "" {
		return "", errors.New("operator source needs an ID and a name")
	}
	current, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName), Key: configKey(src.SourceID), ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return "", fmt.Errorf("failed to get crawler source: %w", err)
	}
	if current.Item == nil {
		if apply {
			if err := r.createOperatorSource(ctx, src); err != nil {
				return "", err
			}
		}
		return OperatorSourceCreated, nil
	}
	if !operatorManaged(current.Item) {
		return "", fmt.Errorf("%w: %s", ErrNotOperatorSource, src.SourceID)
	}
	if operatorFieldsEqual(current.Item, src) {
		return OperatorSourceUnchanged, nil
	}
	if apply {
		if err := r.updateOperatorSource(ctx, src, current.Item["sourceName"]); err != nil {
			return "", err
		}
	}
	return OperatorSourceUpdated, nil
}

func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func (r *CrawlerRepository) createOperatorSource(ctx context.Context, src OperatorNewsSource) error {
	item := crawlerItem{
		PK: model.PrefixCrawler + src.SourceID, SK: model.PrefixConfig, EntityType: "CRAWLER_SOURCE",
		CrawlerSource: model.CrawlerSource{
			SourceID: src.SourceID, SourceName: src.SourceName,
			Subscribers: []string{}, AWSServices: []string{}, NewsQueries: nonNil(src.NewsQueries),
			NewsSources: []string{}, CustomUrls: []string{}, Aliases: nonNil(src.Aliases),
			Schedule: "daily", Status: "active", GlobalNewsSource: true,
		},
	}
	av, err := attributevalue.MarshalMap(item)
	if err != nil {
		return fmt.Errorf("failed to marshal operator source: %w", err)
	}
	if len(src.SearchDomains) > 0 {
		domains, err := attributevalue.Marshal(src.SearchDomains)
		if err != nil {
			return fmt.Errorf("failed to marshal search domains: %w", err)
		}
		av[SearchDomainsAttribute] = domains
	}
	cond, err := expression.NewBuilder().
		WithCondition(expression.AttributeNotExists(expression.Name("PK"))).Build()
	if err != nil {
		return fmt.Errorf("build operator source condition: %w", err)
	}
	_, err = r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(r.tableName), Item: av,
		ConditionExpression:       cond.Condition(),
		ExpressionAttributeNames:  cond.Names(),
		ExpressionAttributeValues: cond.Values(),
	})
	return conditionalError(err, "create operator source "+src.SourceID)
}

func (r *CrawlerRepository) updateOperatorSource(ctx context.Context, src OperatorNewsSource, readName types.AttributeValue) error {
	update := expression.Set(expression.Name("sourceName"), expression.Value(src.SourceName)).
		Set(expression.Name("aliases"), expression.Value(nonNil(src.Aliases))).
		Set(expression.Name("newsQueries"), expression.Value(nonNil(src.NewsQueries)))
	if len(src.SearchDomains) > 0 {
		update = update.Set(expression.Name(SearchDomainsAttribute), expression.Value(src.SearchDomains))
	} else {
		update = update.Remove(expression.Name(SearchDomainsAttribute))
	}
	cond := expression.Equal(expression.Name(GlobalNewsSourceAttribute), expression.Value(true)).
		And(expression.AttributeNotExists(expression.Name("userTiedAt"))).
		And(expression.AttributeNotExists(expression.Name("accountId"))).
		And(expression.Equal(expression.Name("sourceName"), expression.Value(readName)))
	expr, err := expression.NewBuilder().WithUpdate(update).WithCondition(cond).Build()
	if err != nil {
		return fmt.Errorf("build operator source update: %w", err)
	}
	_, err = r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(r.tableName), Key: configKey(src.SourceID),
		UpdateExpression:          expr.Update(),
		ConditionExpression:       expr.Condition(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
	})
	return conditionalError(err, "update operator source "+src.SourceID)
}

func conditionalError(err error, action string) error {
	if err == nil {
		return nil
	}
	var ccfe *types.ConditionalCheckFailedException
	if errors.As(err, &ccfe) {
		return fmt.Errorf("%w: %s changed concurrently", ErrConditionFailed, action)
	}
	return fmt.Errorf("failed to %s: %w", action, err)
}
