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

// ErrAccountNewsSourceLimit means the account's source counter was already at
// the cap when the create transaction ran.
var ErrAccountNewsSourceLimit = errors.New("account news source limit reached")

// accountNewsCountAttr is the counter attribute on the
// model.SKAccountNewsSourceCount row.
const accountNewsCountAttr = "sourceCount"

// accountNewsRevAttr is the counter row's revision. Every counter mutation
// (create, delete, reconcile) adds one, so a compare-and-set on it detects any
// intervening mutation even when sourceCount round-trips to the same value.
const accountNewsRevAttr = "sourceRev"

// accountNewsCounterAttempts bounds delete retries when the counter changed
// between the read and the commit.
const accountNewsCounterAttempts = 3

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
//
// The same transaction increments the account's source counter (and advances
// its sourceRev), conditioned on it being below limit, so concurrent creates
// cannot exceed the cap. Accounts
// whose sources predate the counter have no counter row: observedCount (the
// caller's strongly consistent ref count) seeds it via if_not_exists. A seed can
// only be stale high (a concurrent delete); the service repairs such drift with
// ReconcileAccountNewsSourceCount when the refs show room but this transaction
// reports ErrAccountNewsSourceLimit. A counter left low by a writer that
// predates it is reconciled by the service before the create. A concurrent
// first create seeds the row first and this one then increments it.
func (r *DynamoDBRepository) CreateAccountNewsSource(ctx context.Context, src *model.CrawlerSource, ref *model.AccountNewsSourceRef, observedCount, limit int) error {
	if src == nil || ref == nil || src.SourceID == "" || src.AccountID == "" ||
		ref.AccountID != src.AccountID || ref.SourceID != src.SourceID || ref.CreatedBy == "" ||
		observedCount < 0 || limit <= 0 || observedCount >= limit {
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
	open, err := r.accountOpenCheck(ref.AccountID)
	if err != nil {
		return err
	}
	count := expression.Name(accountNewsCountAttr)
	counterExpr, err := expression.NewBuilder().
		WithCondition(count.AttributeNotExists().Or(count.LessThan(expression.Value(limit)))).
		WithUpdate(expression.Set(count, expression.Plus(expression.IfNotExists(count, expression.Value(observedCount)), expression.Value(1))).
			Set(expression.Name("entityType"), expression.Value("ACCOUNT_NEWS_SOURCE_COUNT")).
			Add(expression.Name(accountNewsRevAttr), expression.Value(1))).
		Build()
	if err != nil {
		return err
	}
	counter := &types.Update{TableName: aws.String(r.tableName), Key: accountNewsKey(accountPK, model.SKAccountNewsSourceCount)}
	counter.UpdateExpression, counter.ConditionExpression = counterExpr.Update(), counterExpr.Condition()
	counter.ExpressionAttributeNames, counter.ExpressionAttributeValues = counterExpr.Names(), counterExpr.Values()
	_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: []types.TransactWriteItem{
		open, check(model.PrefixMember + ref.CreatedBy), put(configAV), put(refAV), {Update: counter},
	}})
	if err != nil {
		if merging := mergeFenceError(err); merging != nil {
			return merging
		}
		return classifyAccountNewsCreate(err)
	}
	return nil
}

// classifyAccountNewsCreate maps only pure condition failures: a missing
// account/member is access, an existing CONFIG/ref is an ID collision, and a
// counter at the cap is ErrAccountNewsSourceLimit.
func classifyAccountNewsCreate(err error) error {
	var cancelled *types.TransactionCanceledException
	if !errors.As(err, &cancelled) || len(cancelled.CancellationReasons) != 5 {
		return fmt.Errorf("create account news source: %w", err)
	}
	access, collision, full := false, false, false
	for i, reason := range cancelled.CancellationReasons {
		switch aws.ToString(reason.Code) {
		case "None":
		case "ConditionalCheckFailed":
			switch {
			case i < 2:
				access = true
			case i < 4:
				collision = true
			default:
				full = true
			}
		default:
			return fmt.Errorf("create account news source: %w", err)
		}
	}
	switch {
	case access:
		return ErrAccountNewsAccess
	case full:
		return ErrAccountNewsSourceLimit
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
// to accountID, with the account META row and the acting user's direct
// MEMBER# row checked in the same transaction. The same transaction decrements
// the source counter, conditioned on the count and revision read just before,
// and advances the revision; an absent (pre-counter) or non-positive counter is
// only pinned, never created, so a later create still seeds it from the actual
// refs. A counter that changed in between is retried. ErrConditionFailed means the ref or matching CONFIG
// is absent; ErrAccountNewsAccess means the account or membership was gone at
// commit.
func (r *DynamoDBRepository) DeleteAccountNewsSource(ctx context.Context, accountID, userID, sourceID string) error {
	for attempt := 0; attempt < accountNewsCounterAttempts; attempt++ {
		retry, err := r.deleteAccountNewsSourceOnce(ctx, accountID, userID, sourceID)
		if !retry {
			return err
		}
	}
	return fmt.Errorf("delete account news source: source counter kept changing")
}

// accountNewsCounter is the observed state of the NEWSSRC_COUNT row.
type accountNewsCounter struct {
	count, rev     int
	exists, hasRev bool
}

// unchanged is the condition that the row still has the observed count and
// revision (a row written before revisions existed has no sourceRev).
func (c accountNewsCounter) unchanged() expression.ConditionBuilder {
	if !c.exists {
		return expression.Name(accountNewsCountAttr).AttributeNotExists()
	}
	rev := expression.Name(accountNewsRevAttr).AttributeNotExists()
	if c.hasRev {
		rev = expression.Name(accountNewsRevAttr).Equal(expression.Value(c.rev))
	}
	return expression.Name(accountNewsCountAttr).Equal(expression.Value(c.count)).And(rev)
}

// readAccountNewsSourceCount returns the counter row's observed state.
func (r *DynamoDBRepository) readAccountNewsSourceCount(ctx context.Context, accountID string) (accountNewsCounter, error) {
	out, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName), Key: accountNewsKey(model.PrefixAccount+accountID, model.SKAccountNewsSourceCount),
		ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return accountNewsCounter{}, fmt.Errorf("read account news source count: %w", err)
	}
	var state accountNewsCounter
	if value, ok := out.Item[accountNewsCountAttr]; ok {
		if err := attributevalue.Unmarshal(value, &state.count); err != nil {
			return accountNewsCounter{}, fmt.Errorf("decode account news source count: %w", err)
		}
		state.exists = true
	}
	if value, ok := out.Item[accountNewsRevAttr]; ok && state.exists {
		if err := attributevalue.Unmarshal(value, &state.rev); err != nil {
			return accountNewsCounter{}, fmt.Errorf("decode account news source revision: %w", err)
		}
		state.hasRev = true
	}
	return state, nil
}

// GetAccountNewsSourceCount returns the source counter (strongly consistent)
// and whether the counter row exists.
func (r *DynamoDBRepository) GetAccountNewsSourceCount(ctx context.Context, accountID string) (int, bool, error) {
	state, err := r.readAccountNewsSourceCount(ctx, accountID)
	if err != nil {
		return 0, false, err
	}
	return state.count, state.exists, nil
}

// ReconcileAccountNewsSourceCount sets an existing source counter to the
// canonical NEWSSRC# ref count and returns that count. The counter is read
// before the refs are listed, and the write is conditioned on the count and
// revision read, so any create, delete or reconcile that commits in between
// (each advances the revision) makes the write fail and the reconcile retry.
// An absent counter is left absent: the next create seeds it from the refs.
// Writers deployed before this counter do not touch it; their effect is only
// repaired by a later reconcile.
func (r *DynamoDBRepository) ReconcileAccountNewsSourceCount(ctx context.Context, accountID string) (int, error) {
	for attempt := 0; attempt < accountNewsCounterAttempts; attempt++ {
		state, err := r.readAccountNewsSourceCount(ctx, accountID)
		if err != nil {
			return 0, err
		}
		refs, err := r.ListAccountNewsSources(ctx, accountID)
		if err != nil {
			return 0, err
		}
		if !state.exists || state.count == len(refs) {
			return len(refs), nil
		}
		expr, err := expression.NewBuilder().
			WithCondition(state.unchanged()).
			WithUpdate(expression.Set(expression.Name(accountNewsCountAttr), expression.Value(len(refs))).
				Add(expression.Name(accountNewsRevAttr), expression.Value(1))).Build()
		if err != nil {
			return 0, err
		}
		_, err = r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
			TableName: aws.String(r.tableName), Key: accountNewsKey(model.PrefixAccount+accountID, model.SKAccountNewsSourceCount),
			UpdateExpression: expr.Update(), ConditionExpression: expr.Condition(),
			ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(),
		})
		if err == nil {
			return len(refs), nil
		}
		var changed *types.ConditionalCheckFailedException
		if !errors.As(err, &changed) {
			return 0, fmt.Errorf("reconcile account news source count: %w", err)
		}
	}
	return 0, fmt.Errorf("reconcile account news source count: counter kept changing")
}

// accountNewsCounterDelete builds the delete transaction's counter item: a
// decrement of a positive observed count, conditioned on the observed count
// and revision and advancing the revision; otherwise a check that the observed
// state (absent, or the same count and revision) still holds.
func (r *DynamoDBRepository) accountNewsCounterDelete(accountID string, state accountNewsCounter) (types.TransactWriteItem, error) {
	key := accountNewsKey(model.PrefixAccount+accountID, model.SKAccountNewsSourceCount)
	if state.exists && state.count > 0 {
		expr, err := expression.NewBuilder().
			WithCondition(state.unchanged()).
			WithUpdate(expression.Set(expression.Name(accountNewsCountAttr), expression.Value(state.count-1)).
				Add(expression.Name(accountNewsRevAttr), expression.Value(1))).Build()
		if err != nil {
			return types.TransactWriteItem{}, err
		}
		update := &types.Update{TableName: aws.String(r.tableName), Key: key}
		update.UpdateExpression, update.ConditionExpression = expr.Update(), expr.Condition()
		update.ExpressionAttributeNames, update.ExpressionAttributeValues = expr.Names(), expr.Values()
		return types.TransactWriteItem{Update: update}, nil
	}
	expr, err := expression.NewBuilder().WithCondition(state.unchanged()).Build()
	if err != nil {
		return types.TransactWriteItem{}, err
	}
	check := &types.ConditionCheck{TableName: aws.String(r.tableName), Key: key}
	check.ConditionExpression, check.ExpressionAttributeNames, check.ExpressionAttributeValues = expr.Condition(), expr.Names(), expr.Values()
	return types.TransactWriteItem{ConditionCheck: check}, nil
}

// deleteAccountNewsSourceOnce runs one delete transaction; retry reports a
// counter compare-and-set conflict with no other condition failure.
func (r *DynamoDBRepository) deleteAccountNewsSourceOnce(ctx context.Context, accountID, userID, sourceID string) (bool, error) {
	refCond, err := expression.NewBuilder().WithCondition(expression.AttributeExists(expression.Name("PK"))).Build()
	if err != nil {
		return false, err
	}
	configExpr, err := expression.NewBuilder().
		WithCondition(expression.AttributeExists(expression.Name("PK")).
			And(expression.Name("accountId").Equal(expression.Value(accountID)))).
		WithUpdate(expression.Set(expression.Name("status"), expression.Value(accountNewsSourceDisabled))).
		Build()
	if err != nil {
		return false, err
	}
	del := &types.Delete{TableName: aws.String(r.tableName), Key: accountNewsKey(model.PrefixAccount+accountID, model.PrefixAccountNewsSource+sourceID)}
	del.ConditionExpression, del.ExpressionAttributeNames = refCond.Condition(), refCond.Names()
	update := &types.Update{TableName: aws.String(r.tableName), Key: accountNewsKey(model.PrefixCrawler+sourceID, model.PrefixConfig)}
	update.UpdateExpression, update.ConditionExpression = configExpr.Update(), configExpr.Condition()
	update.ExpressionAttributeNames, update.ExpressionAttributeValues = configExpr.Names(), configExpr.Values()
	access, err := accountAccessChecks(r.tableName, accountID, userID)
	if err != nil {
		return false, err
	}
	state, err := r.readAccountNewsSourceCount(ctx, accountID)
	if err != nil {
		return false, err
	}
	counter, err := r.accountNewsCounterDelete(accountID, state)
	if err != nil {
		return false, err
	}
	items := append([]types.TransactWriteItem{{Delete: del}, {Update: update}}, access...)
	items = append(items, counter)
	_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: items})
	if err == nil {
		return false, nil
	}
	if merging := mergeFenceError(err); merging != nil {
		return false, merging
	}
	var cancelled *types.TransactionCanceledException
	if errors.As(err, &cancelled) && len(cancelled.CancellationReasons) == len(items) {
		failed, revoked, counterChanged := false, false, false
		for i, reason := range cancelled.CancellationReasons {
			switch aws.ToString(reason.Code) {
			case "None":
			case "ConditionalCheckFailed":
				switch {
				case i < 2:
					failed = true
				case i < 4:
					revoked = true
				default:
					counterChanged = true
				}
			default:
				return false, fmt.Errorf("delete account news source: %w", err)
			}
		}
		switch {
		case revoked:
			return false, ErrAccountNewsAccess
		case failed:
			return false, fmt.Errorf("%w: account news source %s", ErrConditionFailed, sourceID)
		case counterChanged:
			return true, nil
		}
	}
	return false, fmt.Errorf("delete account news source: %w", err)
}

// AccountNewsSourceFields lists the CONFIG fields an account news source edit
// replaces; nil fields are unchanged. The source ID, the ref and accountId
// have no field and never change.
type AccountNewsSourceFields struct {
	SourceName  *string
	NewsQueries *[]string
	Aliases     *[]string
}

// UpdateAccountNewsSource replaces the given CONFIG fields of an account-owned
// source in one transaction that also requires the NEWSSRC# ref, a CONFIG that
// still names accountID, is not disabled and is not operator-curated, the open
// account META row and the caller's direct MEMBER# row. Delete disables the
// CONFIG and removes the ref atomically, so an edit racing a delete cannot
// revive the source. ErrConditionFailed means the ref or a matching enabled
// CONFIG is absent; ErrAccountNewsAccess means the account or membership was
// gone at commit.
func (r *DynamoDBRepository) UpdateAccountNewsSource(ctx context.Context, accountID, userID, sourceID string, fields AccountNewsSourceFields) error {
	names, values := []string{}, []interface{}{}
	if fields.SourceName != nil {
		names, values = append(names, "sourceName"), append(values, *fields.SourceName)
	}
	if fields.NewsQueries != nil {
		names, values = append(names, "newsQueries"), append(values, *fields.NewsQueries)
	}
	if fields.Aliases != nil {
		names, values = append(names, "aliases"), append(values, *fields.Aliases)
	}
	if accountID == "" || sourceID == "" || len(names) == 0 {
		return fmt.Errorf("invalid account news source update")
	}
	update := expression.Set(expression.Name(names[0]), expression.Value(values[0]))
	for i := 1; i < len(names); i++ {
		update = update.Set(expression.Name(names[i]), expression.Value(values[i]))
	}
	configExpr, err := expression.NewBuilder().
		WithCondition(expression.AttributeExists(expression.Name("PK")).
			And(expression.Name("accountId").Equal(expression.Value(accountID))).
			And(expression.Name("status").NotEqual(expression.Value(accountNewsSourceDisabled))).
			And(expression.AttributeNotExists(expression.Name("globalNewsSource")))).
		WithUpdate(update).Build()
	if err != nil {
		return err
	}
	refCond, err := expression.NewBuilder().WithCondition(expression.AttributeExists(expression.Name("PK"))).Build()
	if err != nil {
		return err
	}
	ref := &types.ConditionCheck{TableName: aws.String(r.tableName), Key: accountNewsKey(model.PrefixAccount+accountID, model.PrefixAccountNewsSource+sourceID)}
	ref.ConditionExpression, ref.ExpressionAttributeNames = refCond.Condition(), refCond.Names()
	config := &types.Update{TableName: aws.String(r.tableName), Key: accountNewsKey(model.PrefixCrawler+sourceID, model.PrefixConfig)}
	config.UpdateExpression, config.ConditionExpression = configExpr.Update(), configExpr.Condition()
	config.ExpressionAttributeNames, config.ExpressionAttributeValues = configExpr.Names(), configExpr.Values()
	access, err := accountAccessChecks(r.tableName, accountID, userID)
	if err != nil {
		return err
	}
	items := append([]types.TransactWriteItem{{ConditionCheck: ref}, {Update: config}}, access...)
	_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: items})
	if err == nil {
		return nil
	}
	if merging := mergeFenceError(err); merging != nil {
		return merging
	}
	var cancelled *types.TransactionCanceledException
	if errors.As(err, &cancelled) && len(cancelled.CancellationReasons) == len(items) {
		missing, revoked := false, false
		for i, reason := range cancelled.CancellationReasons {
			switch aws.ToString(reason.Code) {
			case "None":
			case "ConditionalCheckFailed":
				if i < 2 {
					missing = true
				} else {
					revoked = true
				}
			default:
				return fmt.Errorf("update account news source: %w", err)
			}
		}
		switch {
		case revoked:
			return ErrAccountNewsAccess
		case missing:
			return fmt.Errorf("%w: account news source %s", ErrConditionFailed, sourceID)
		}
	}
	return fmt.Errorf("update account news source: %w", err)
}
