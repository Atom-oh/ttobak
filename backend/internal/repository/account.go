package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
)

// queryAllPages runs a Query and follows LastEvaluatedKey across all pages, so
// account-partition reads are never silently capped at DynamoDB's 1MB page limit
// as accounts accumulate members/insights/meeting-refs/documents.
func (r *DynamoDBRepository) queryAllPages(ctx context.Context, input *dynamodb.QueryInput) ([]map[string]types.AttributeValue, error) {
	var items []map[string]types.AttributeValue
	for {
		out, err := r.client.Query(ctx, input)
		if err != nil {
			return nil, err
		}
		items = append(items, out.Items...)
		if out.LastEvaluatedKey == nil {
			break
		}
		input.ExclusiveStartKey = out.LastEvaluatedKey
	}
	return items, nil
}

// CreateAccount writes the account META item and the owner member item.
func (r *DynamoDBRepository) CreateAccount(ctx context.Context, account *model.Account, ownerMember *model.AccountMember) error {
	accItem, err := attributevalue.MarshalMap(account)
	if err != nil {
		return fmt.Errorf("marshal account: %w", err)
	}
	memItem, err := attributevalue.MarshalMap(ownerMember)
	if err != nil {
		return fmt.Errorf("marshal owner member: %w", err)
	}
	// Atomic two-item write: an account must never exist without its owner member
	// (a missing owner would make GetMember deny everyone, orphaning the account).
	if _, err := r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: []types.TransactWriteItem{
			{Put: &types.Put{TableName: aws.String(r.tableName), Item: accItem}},
			{Put: &types.Put{TableName: aws.String(r.tableName), Item: memItem}},
		},
	}); err != nil {
		return fmt.Errorf("create account transaction: %w", err)
	}
	return nil
}

func (r *DynamoDBRepository) GetAccount(ctx context.Context, accountID string) (*model.Account, error) {
	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      aws.String(r.tableName),
		ConsistentRead: aws.Bool(true),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: model.PrefixAccount + accountID},
			"SK": &types.AttributeValueMemberS{Value: model.SKAccountMeta},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("get account: %w", err)
	}
	if result.Item == nil {
		return nil, nil
	}
	var account model.Account
	if err := attributevalue.UnmarshalMap(result.Item, &account); err != nil {
		return nil, fmt.Errorf("unmarshal account: %w", err)
	}
	return &account, nil
}

func (r *DynamoDBRepository) GetMember(ctx context.Context, accountID, userID string) (*model.AccountMember, error) {
	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      aws.String(r.tableName),
		ConsistentRead: aws.Bool(true),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: model.PrefixAccount + accountID},
			"SK": &types.AttributeValueMemberS{Value: model.PrefixMember + userID},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("get member: %w", err)
	}
	if result.Item == nil {
		return nil, nil
	}
	var member model.AccountMember
	if err := attributevalue.UnmarshalMap(result.Item, &member); err != nil {
		return nil, fmt.Errorf("unmarshal member: %w", err)
	}
	return &member, nil
}

func (r *DynamoDBRepository) PutMember(ctx context.Context, member *model.AccountMember) error {
	item, err := attributevalue.MarshalMap(member)
	if err != nil {
		return fmt.Errorf("marshal member: %w", err)
	}
	if _, err := r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(r.tableName),
		Item:      item,
	}); err != nil {
		return fmt.Errorf("put member: %w", err)
	}
	return nil
}

// DeleteMember requires the member item to already exist so removing a
// non-member surfaces ErrConditionFailed (mapped to ErrNotFound in the
// service layer) instead of silently succeeding -- same pattern as
// DeleteAccountDocument below.
func (r *DynamoDBRepository) DeleteMember(ctx context.Context, accountID, userID string) error {
	condition := expression.AttributeExists(expression.Name("PK"))
	expr, err := expression.NewBuilder().WithCondition(condition).Build()
	if err != nil {
		return fmt.Errorf("build delete member condition: %w", err)
	}
	_, err = r.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: model.PrefixAccount + accountID},
			"SK": &types.AttributeValueMemberS{Value: model.PrefixMember + userID},
		},
		ConditionExpression:       expr.Condition(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
	})
	if err != nil {
		var ccfe *types.ConditionalCheckFailedException
		if errors.As(err, &ccfe) {
			return fmt.Errorf("%w: member %s not found", ErrConditionFailed, userID)
		}
		return fmt.Errorf("delete member: %w", err)
	}
	return nil
}

// ListAccountMembers queries the account partition for MEMBER# items.
func (r *DynamoDBRepository) ListAccountMembers(ctx context.Context, accountID string) ([]model.AccountMember, error) {
	keyEx := expression.Key("PK").Equal(expression.Value(model.PrefixAccount + accountID)).
		And(expression.Key("SK").BeginsWith(model.PrefixMember))
	expr, err := expression.NewBuilder().WithKeyCondition(keyEx).Build()
	if err != nil {
		return nil, fmt.Errorf("build members query: %w", err)
	}
	items, err := r.queryAllPages(ctx, &dynamodb.QueryInput{
		TableName:                 aws.String(r.tableName),
		KeyConditionExpression:    expr.KeyCondition(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
	})
	if err != nil {
		return nil, fmt.Errorf("query members: %w", err)
	}
	members := []model.AccountMember{}
	if err := attributevalue.UnmarshalListOfMaps(items, &members); err != nil {
		return nil, fmt.Errorf("unmarshal members: %w", err)
	}
	return members, nil
}

// ListAccountsForUser reverse-looks-up memberships via GSI1
// (GSI1PK=USER#{userId}, GSI1SK begins_with ACCOUNT#). GSI1 is shared with
// meeting date-sorting items whose GSI1SK is an RFC3339 timestamp, so the
// begins_with("ACCOUNT#") condition isolates membership rows.
func (r *DynamoDBRepository) ListAccountsForUser(ctx context.Context, userID string) ([]model.AccountMember, error) {
	keyEx := expression.Key("GSI1PK").Equal(expression.Value(model.PrefixUser + userID)).
		And(expression.Key("GSI1SK").BeginsWith(model.PrefixAccount))
	expr, err := expression.NewBuilder().WithKeyCondition(keyEx).Build()
	if err != nil {
		return nil, fmt.Errorf("build accounts-for-user query: %w", err)
	}
	items, err := r.queryAllPages(ctx, &dynamodb.QueryInput{
		TableName:                 aws.String(r.tableName),
		IndexName:                 aws.String("GSI1"),
		KeyConditionExpression:    expr.KeyCondition(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
	})
	if err != nil {
		return nil, fmt.Errorf("query accounts for user: %w", err)
	}
	members := []model.AccountMember{}
	if err := attributevalue.UnmarshalListOfMaps(items, &members); err != nil {
		return nil, fmt.Errorf("unmarshal memberships: %w", err)
	}
	return members, nil
}

// PutMeetingRef writes a MeetingRef item (caller builds PK/SK).
func (r *DynamoDBRepository) PutMeetingRef(ctx context.Context, ref *model.MeetingRef) error {
	item, err := attributevalue.MarshalMap(ref)
	if err != nil {
		return fmt.Errorf("marshal meeting ref: %w", err)
	}
	if _, err := r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(r.tableName),
		Item:      item,
	}); err != nil {
		return fmt.Errorf("put meeting ref: %w", err)
	}
	return nil
}

// ListMeetingRefsForAccount queries the account partition for MEETINGREF# items
// (sorted by occurredAt via the SK prefix).
func (r *DynamoDBRepository) ListMeetingRefsForAccount(ctx context.Context, accountID string) ([]model.MeetingRef, error) {
	keyEx := expression.Key("PK").Equal(expression.Value(model.PrefixAccount + accountID)).
		And(expression.Key("SK").BeginsWith(model.PrefixMeetingRef))
	expr, err := expression.NewBuilder().WithKeyCondition(keyEx).Build()
	if err != nil {
		return nil, fmt.Errorf("build meeting refs query: %w", err)
	}
	items, err := r.queryAllPages(ctx, &dynamodb.QueryInput{
		TableName:                 aws.String(r.tableName),
		KeyConditionExpression:    expr.KeyCondition(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
		ScanIndexForward:          aws.Bool(false), // newest first
	})
	if err != nil {
		return nil, fmt.Errorf("query meeting refs: %w", err)
	}
	refs := []model.MeetingRef{}
	if err := attributevalue.UnmarshalListOfMaps(items, &refs); err != nil {
		return nil, fmt.Errorf("unmarshal meeting refs: %w", err)
	}
	return refs, nil
}

// PutAccountInsights REPLACES the account insights for a single meeting: it first
// deletes any existing items sharing the meeting's SK prefix
// (INSIGHT#{occurredAt}#{meetingId}#) and then writes the fresh set. This makes a
// re-extraction that yields FEWER insights (N→M, M<N) leave no stale items — a
// plain overwrite would orphan indices M..N-1. All items in `insights` must belong
// to one meeting (BuildAccountInsights guarantees this: same PK + SK prefix).
func (r *DynamoDBRepository) PutAccountInsights(ctx context.Context, insights []model.AccountInsight) error {
	if len(insights) == 0 {
		return nil
	}
	pk := insights[0].PK
	prefix := insights[0].SK
	if idx := strings.LastIndex(prefix, "#"); idx >= 0 {
		prefix = prefix[:idx+1] // INSIGHT#{occurredAt}#{meetingId}#
	}

	// Delete the meeting's existing insight items under the prefix.
	keyEx := expression.Key("PK").Equal(expression.Value(pk)).
		And(expression.Key("SK").BeginsWith(prefix))
	expr, err := expression.NewBuilder().WithKeyCondition(keyEx).Build()
	if err != nil {
		return fmt.Errorf("build insight replace query: %w", err)
	}
	res, err := r.client.Query(ctx, &dynamodb.QueryInput{
		TableName:                 aws.String(r.tableName),
		KeyConditionExpression:    expr.KeyCondition(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
		ProjectionExpression:      aws.String("PK, SK"),
	})
	if err != nil {
		return fmt.Errorf("query existing insights: %w", err)
	}
	for _, existing := range res.Items {
		if _, err := r.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
			TableName: aws.String(r.tableName),
			Key:       map[string]types.AttributeValue{"PK": existing["PK"], "SK": existing["SK"]},
		}); err != nil {
			return fmt.Errorf("delete stale insight: %w", err)
		}
	}

	// Write the fresh set.
	for i := range insights {
		item, err := attributevalue.MarshalMap(&insights[i])
		if err != nil {
			return fmt.Errorf("marshal account insight: %w", err)
		}
		if _, err := r.client.PutItem(ctx, &dynamodb.PutItemInput{
			TableName: aws.String(r.tableName),
			Item:      item,
		}); err != nil {
			return fmt.Errorf("put account insight: %w", err)
		}
	}
	return nil
}

// ListInsightsForAccount returns all INSIGHT# items for an account (newest first).
// Period/type filtering is done by the service layer (spec §6.3: client-side for v1).
func (r *DynamoDBRepository) ListInsightsForAccount(ctx context.Context, accountID string) ([]model.AccountInsight, error) {
	keyEx := expression.Key("PK").Equal(expression.Value(model.PrefixAccount + accountID)).
		And(expression.Key("SK").BeginsWith(model.PrefixInsight))
	expr, err := expression.NewBuilder().WithKeyCondition(keyEx).Build()
	if err != nil {
		return nil, fmt.Errorf("build insights query: %w", err)
	}
	items, err := r.queryAllPages(ctx, &dynamodb.QueryInput{
		TableName:                 aws.String(r.tableName),
		KeyConditionExpression:    expr.KeyCondition(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
		ScanIndexForward:          aws.Bool(false),
	})
	if err != nil {
		return nil, fmt.Errorf("query insights: %w", err)
	}
	insights := []model.AccountInsight{}
	if err := attributevalue.UnmarshalListOfMaps(items, &insights); err != nil {
		return nil, fmt.Errorf("unmarshal insights: %w", err)
	}
	return insights, nil
}

func (r *DynamoDBRepository) PutAccountDocument(ctx context.Context, doc *model.AccountDocument) error {
	item, err := attributevalue.MarshalMap(doc)
	if err != nil {
		return fmt.Errorf("marshal account doc: %w", err)
	}
	if _, err := r.client.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(r.tableName), Item: item}); err != nil {
		return fmt.Errorf("put account doc: %w", err)
	}
	return nil
}

// ListAccountDocuments lists documents under pk (either model.PrefixAccount+id
// for a shared account doc or model.PrefixUser+id for a personal doc). Content
// is excluded via ProjectionExpression -- callers needing the body use
// GetAccountDocument for a single doc, matching how AccountDocumentDTO never
// carries Content either.
func (r *DynamoDBRepository) ListAccountDocuments(ctx context.Context, pk string) ([]model.AccountDocument, error) {
	keyEx := expression.Key("PK").Equal(expression.Value(pk)).
		And(expression.Key("SK").BeginsWith(model.PrefixDoc))
	proj := expression.NamesList(
		expression.Name("PK"), expression.Name("SK"), expression.Name("accountId"),
		expression.Name("docId"), expression.Name("title"), expression.Name("docType"),
		expression.Name("path"), expression.Name("links"), expression.Name("fileKey"),
		expression.Name("fileName"), expression.Name("mimeType"), expression.Name("fileSize"),
		expression.Name("sourceUserId"), expression.Name("ttobakOrigin"),
		expression.Name("createdAt"), expression.Name("updatedAt"), expression.Name("entityType"),
	)
	expr, err := expression.NewBuilder().WithKeyCondition(keyEx).WithProjection(proj).Build()
	if err != nil {
		return nil, fmt.Errorf("build docs query: %w", err)
	}
	items, err := r.queryAllPages(ctx, &dynamodb.QueryInput{
		TableName:                 aws.String(r.tableName),
		KeyConditionExpression:    expr.KeyCondition(),
		ProjectionExpression:      expr.Projection(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
		ScanIndexForward:          aws.Bool(false),
	})
	if err != nil {
		return nil, fmt.Errorf("query docs: %w", err)
	}
	docs := []model.AccountDocument{}
	if err := attributevalue.UnmarshalListOfMaps(items, &docs); err != nil {
		return nil, fmt.Errorf("unmarshal docs: %w", err)
	}
	return docs, nil
}

func (r *DynamoDBRepository) GetAccountDocument(ctx context.Context, pk, docID string) (*model.AccountDocument, error) {
	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      aws.String(r.tableName),
		ConsistentRead: aws.Bool(true),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: pk},
			"SK": &types.AttributeValueMemberS{Value: model.PrefixDoc + docID},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("get account doc: %w", err)
	}
	if result.Item == nil {
		return nil, nil
	}
	var doc model.AccountDocument
	if err := attributevalue.UnmarshalMap(result.Item, &doc); err != nil {
		return nil, fmt.Errorf("unmarshal account doc: %w", err)
	}
	return &doc, nil
}

// DeleteAccountDocument requires the item to already exist so a delete of a
// non-existent docID surfaces ErrConditionFailed (mapped to ErrNotFound in
// the service layer) instead of silently succeeding -- matching the 404
// documented in API-SPEC.md.
func (r *DynamoDBRepository) DeleteAccountDocument(ctx context.Context, pk, docID string) error {
	// Same builder-based "item must already exist" condition as
	// UpdateMeetingFields (dynamodb.go) -- expression.AttributeExists,
	// not a raw ConditionExpression string.
	condition := expression.AttributeExists(expression.Name("PK"))
	expr, err := expression.NewBuilder().WithCondition(condition).Build()
	if err != nil {
		return fmt.Errorf("build delete condition: %w", err)
	}
	_, err = r.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: pk},
			"SK": &types.AttributeValueMemberS{Value: model.PrefixDoc + docID},
		},
		ConditionExpression:       expr.Condition(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
	})
	if err != nil {
		var ccfe *types.ConditionalCheckFailedException
		if errors.As(err, &ccfe) {
			return fmt.Errorf("%w: doc %s not found", ErrConditionFailed, docID)
		}
		return fmt.Errorf("delete account doc: %w", err)
	}
	return nil
}
