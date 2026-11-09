package repository

import (
	"context"
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

// ErrAccountNewsAccess means the account or the creator's membership row was
// absent when the create transaction ran.
var ErrAccountNewsAccess = errors.New("account news source access denied")

// accountNewsSourceDisabled is the CONFIG status a deleted account source
// keeps; the scheduler and current-news readers skip "disabled" sources.
const accountNewsSourceDisabled = "disabled"

type accountNewsRefItem struct {
	PK         string `dynamodbav:"PK"`
	SK         string `dynamodbav:"SK"`
	EntityType string `dynamodbav:"entityType"`
	model.AccountNewsSourceRef
}

func accountNewsKey(pk, sk string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		"PK": &types.AttributeValueMemberS{Value: pk},
		"SK": &types.AttributeValueMemberS{Value: sk},
	}
}

// CreateAccountNewsSource atomically creates a new crawler CONFIG row and its
// account ref, conditioned on the account META and the creator's direct
// membership. Both CONFIG and ref are created only if absent. CrawlerRepository
// and DynamoDBRepository share the same table, so the transaction lives here.
func (r *DynamoDBRepository) CreateAccountNewsSource(ctx context.Context, src *model.CrawlerSource, ref *model.AccountNewsSourceRef) error {
	if src == nil || ref == nil || src.SourceID == "" || src.AccountID == "" ||
		ref.AccountID != src.AccountID || ref.SourceID != src.SourceID || ref.CreatedBy == "" {
		return fmt.Errorf("invalid account news source")
	}
	config := crawlerItem{PK: model.PrefixCrawler + src.SourceID, SK: model.PrefixConfig, EntityType: "CRAWLER_SOURCE", CrawlerSource: *src}
	// Account sources are user-created: never global, tainted from the start.
	config.GlobalNewsSource = false
	if config.UserTiedAt == "" {
		config.UserTiedAt = time.Now().UTC().Format(time.RFC3339)
	}
	configAV, err := attributevalue.MarshalMap(config)
	if err != nil {
		return fmt.Errorf("marshal account news config: %w", err)
	}
	refItem := accountNewsRefItem{PK: model.PrefixAccount + ref.AccountID, SK: model.PrefixAccountNewsSource + ref.SourceID, EntityType: "ACCOUNT_NEWS_SOURCE", AccountNewsSourceRef: *ref}
	refAV, err := attributevalue.MarshalMap(refItem)
	if err != nil {
		return fmt.Errorf("marshal account news ref: %w", err)
	}
	exists, err := expression.NewBuilder().WithCondition(expression.AttributeExists(expression.Name("PK"))).Build()
	if err != nil {
		return err
	}
	absent, err := expression.NewBuilder().WithCondition(expression.AttributeNotExists(expression.Name("PK"))).Build()
	if err != nil {
		return err
	}
	accountPK := model.PrefixAccount + ref.AccountID
	check := func(sk string) types.TransactWriteItem {
		cc := &types.ConditionCheck{TableName: aws.String(r.tableName), Key: accountNewsKey(accountPK, sk)}
		cc.ConditionExpression, cc.ExpressionAttributeNames = exists.Condition(), exists.Names()
		return types.TransactWriteItem{ConditionCheck: cc}
	}
	put := func(item map[string]types.AttributeValue) types.TransactWriteItem {
		p := &types.Put{TableName: aws.String(r.tableName), Item: item}
		p.ConditionExpression, p.ExpressionAttributeNames = absent.Condition(), absent.Names()
		return types.TransactWriteItem{Put: p}
	}
	_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: []types.TransactWriteItem{
		check(model.SKAccountMeta), check(model.PrefixMember + ref.CreatedBy), put(configAV), put(refAV),
	}})
	if err != nil {
		return classifyAccountNewsCreate(err)
	}
	return nil
}

// classifyAccountNewsCreate maps only pure condition failures: a missing
// account/member is access, an existing CONFIG/ref is an ID collision.
func classifyAccountNewsCreate(err error) error {
	var cancelled *types.TransactionCanceledException
	if !errors.As(err, &cancelled) || len(cancelled.CancellationReasons) != 4 {
		return fmt.Errorf("create account news source: %w", err)
	}
	access, collision := false, false
	for i, reason := range cancelled.CancellationReasons {
		switch aws.ToString(reason.Code) {
		case "None":
		case "ConditionalCheckFailed":
			if i < 2 {
				access = true
			} else {
				collision = true
			}
		default:
			return fmt.Errorf("create account news source: %w", err)
		}
	}
	switch {
	case access:
		return ErrAccountNewsAccess
	case collision:
		return fmt.Errorf("%w: account news source exists", ErrConditionFailed)
	}
	return fmt.Errorf("create account news source: %w", err)
}

// ListAccountNewsSources reads every NEWSSRC# ref of the account (strongly
// consistent, all pages). Callers must revalidate each CONFIG row.
func (r *DynamoDBRepository) ListAccountNewsSources(ctx context.Context, accountID string) ([]model.AccountNewsSourceRef, error) {
	key := expression.Key("PK").Equal(expression.Value(model.PrefixAccount + accountID)).
		And(expression.Key("SK").BeginsWith(model.PrefixAccountNewsSource))
	expr, err := expression.NewBuilder().WithKeyCondition(key).Build()
	if err != nil {
		return nil, err
	}
	in := &dynamodb.QueryInput{TableName: aws.String(r.tableName), KeyConditionExpression: expr.KeyCondition(), ConsistentRead: aws.Bool(true)}
	in.ExpressionAttributeNames, in.ExpressionAttributeValues = expr.Names(), expr.Values()
	refs := []model.AccountNewsSourceRef{}
	for {
		out, err := r.client.Query(ctx, in)
		if err != nil {
			return nil, fmt.Errorf("list account news sources: %w", err)
		}
		for _, row := range out.Items {
			var item accountNewsRefItem
			if err := attributevalue.UnmarshalMap(row, &item); err != nil {
				return nil, fmt.Errorf("decode account news source: %w", err)
			}
			// The key, not the payload, is canonical for both identifiers.
			if item.PK != model.PrefixAccount+accountID || !strings.HasPrefix(item.SK, model.PrefixAccountNewsSource) {
				continue
			}
			item.AccountID = accountID
			item.SourceID = strings.TrimPrefix(item.SK, model.PrefixAccountNewsSource)
			refs = append(refs, item.AccountNewsSourceRef)
		}
		if len(out.LastEvaluatedKey) == 0 {
			return refs, nil
		}
		in.ExclusiveStartKey = out.LastEvaluatedKey
	}
}

// DeleteAccountNewsSource atomically removes the account ref and disables the
// crawler CONFIG, both conditioned on existing and the CONFIG still belonging
// to accountID. ErrConditionFailed means the ref or matching CONFIG is absent.
func (r *DynamoDBRepository) DeleteAccountNewsSource(ctx context.Context, accountID, sourceID string) error {
	refCond, err := expression.NewBuilder().WithCondition(expression.AttributeExists(expression.Name("PK"))).Build()
	if err != nil {
		return err
	}
	configExpr, err := expression.NewBuilder().
		WithCondition(expression.AttributeExists(expression.Name("PK")).
			And(expression.Name("accountId").Equal(expression.Value(accountID)))).
		WithUpdate(expression.Set(expression.Name("status"), expression.Value(accountNewsSourceDisabled))).
		Build()
	if err != nil {
		return err
	}
	del := &types.Delete{TableName: aws.String(r.tableName), Key: accountNewsKey(model.PrefixAccount+accountID, model.PrefixAccountNewsSource+sourceID)}
	del.ConditionExpression, del.ExpressionAttributeNames = refCond.Condition(), refCond.Names()
	update := &types.Update{TableName: aws.String(r.tableName), Key: accountNewsKey(model.PrefixCrawler+sourceID, model.PrefixConfig)}
	update.UpdateExpression, update.ConditionExpression = configExpr.Update(), configExpr.Condition()
	update.ExpressionAttributeNames, update.ExpressionAttributeValues = configExpr.Names(), configExpr.Values()
	_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: []types.TransactWriteItem{{Delete: del}, {Update: update}}})
	if err == nil {
		return nil
	}
	var cancelled *types.TransactionCanceledException
	if errors.As(err, &cancelled) && len(cancelled.CancellationReasons) == 2 {
		failed := false
		for _, reason := range cancelled.CancellationReasons {
			switch aws.ToString(reason.Code) {
			case "None":
			case "ConditionalCheckFailed":
				failed = true
			default:
				return fmt.Errorf("delete account news source: %w", err)
			}
		}
		if failed {
			return fmt.Errorf("%w: account news source %s", ErrConditionFailed, sourceID)
		}
	}
	return fmt.Errorf("delete account news source: %w", err)
}
