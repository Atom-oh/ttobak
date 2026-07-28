package repository

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
)

// ErrConditionFailed is returned when a conditional DynamoDB update fails (race condition).
var ErrConditionFailed = errors.New("conditional check failed")

// ResearchRepository provides DynamoDB operations for research entities
type ResearchRepository struct {
	client    *dynamodb.Client
	tableName string
}

// NewResearchRepository creates a new ResearchRepository
func NewResearchRepository(client *dynamodb.Client, tableName string) *ResearchRepository {
	return &ResearchRepository{
		client:    client,
		tableName: tableName,
	}
}

// researchItem wraps a Research with DynamoDB key attributes
type researchItem struct {
	PK         string `dynamodbav:"PK"`
	SK         string `dynamodbav:"SK"`
	EntityType string `dynamodbav:"entityType"`
	model.Research
}

// researchIndexItem is the user index record (PK: USER#{userId}, SK: RESEARCH#{researchId})
type researchIndexItem struct {
	PK         string `dynamodbav:"PK"`
	SK         string `dynamodbav:"SK"`
	EntityType string `dynamodbav:"entityType"`
	ResearchID string `dynamodbav:"researchId"`
}

// CreateResearch writes two items: the main record and the user index record
func (r *ResearchRepository) CreateResearch(ctx context.Context, research *model.Research) error {
	// Main record: PK=RESEARCH#{researchId}, SK=CONFIG
	mainItem := researchItem{
		PK:         model.PrefixResearch + research.ResearchID,
		SK:         model.PrefixConfig,
		EntityType: "RESEARCH",
		Research:   *research,
	}

	mainAV, err := attributevalue.MarshalMap(mainItem)
	if err != nil {
		return fmt.Errorf("failed to marshal research: %w", err)
	}

	// User index: PK=USER#{userId}, SK=RESEARCH#{researchId}
	indexItem := researchIndexItem{
		PK:         model.PrefixUser + research.UserID,
		SK:         model.PrefixResearch + research.ResearchID,
		EntityType: "RESEARCH_INDEX",
		ResearchID: research.ResearchID,
	}

	indexAV, err := attributevalue.MarshalMap(indexItem)
	if err != nil {
		return fmt.Errorf("failed to marshal research index: %w", err)
	}

	_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: []types.TransactWriteItem{
			{
				Put: &types.Put{
					TableName: aws.String(r.tableName),
					Item:      mainAV,
				},
			},
			{
				Put: &types.Put{
					TableName: aws.String(r.tableName),
					Item:      indexAV,
				},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to create research: %w", err)
	}

	return nil
}

// GetResearch retrieves a research by researchId
// PK: RESEARCH#{researchId}, SK: CONFIG
func (r *ResearchRepository) GetResearch(ctx context.Context, researchId string) (*model.Research, error) {
	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: model.PrefixResearch + researchId},
			"SK": &types.AttributeValueMemberS{Value: model.PrefixConfig},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get research: %w", err)
	}

	if result.Item == nil {
		return nil, nil
	}

	var item researchItem
	if err := attributevalue.UnmarshalMap(result.Item, &item); err != nil {
		return nil, fmt.Errorf("failed to unmarshal research: %w", err)
	}

	return &item.Research, nil
}

// UpdateResearchFieldsConditional updates fields only if current status matches expectedStatus.
func (r *ResearchRepository) UpdateResearchFieldsConditional(ctx context.Context, researchId string, fields map[string]interface{}, expectedStatus string) error {
	if len(fields) == 0 {
		return nil
	}

	var updateBuilder expression.UpdateBuilder
	first := true
	for k, v := range fields {
		if first {
			updateBuilder = expression.Set(expression.Name(k), expression.Value(v))
			first = false
		} else {
			updateBuilder = updateBuilder.Set(expression.Name(k), expression.Value(v))
		}
	}

	cond := expression.Name("status").Equal(expression.Value(expectedStatus))
	expr, err := expression.NewBuilder().WithUpdate(updateBuilder).WithCondition(cond).Build()
	if err != nil {
		return fmt.Errorf("failed to build conditional expression: %w", err)
	}

	_, err = r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: model.PrefixResearch + researchId},
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
			return fmt.Errorf("%w: expected status %q", ErrConditionFailed, expectedStatus)
		}
		return fmt.Errorf("conditional update failed: %w", err)
	}
	return nil
}

// UpdateResearchFields performs a partial update on a research record
func (r *ResearchRepository) UpdateResearchFields(ctx context.Context, researchId string, fields map[string]interface{}) error {
	if len(fields) == 0 {
		return nil
	}

	var updateBuilder expression.UpdateBuilder
	first := true
	for k, v := range fields {
		if first {
			updateBuilder = expression.Set(expression.Name(k), expression.Value(v))
			first = false
		} else {
			updateBuilder = updateBuilder.Set(expression.Name(k), expression.Value(v))
		}
	}

	expr, err := expression.NewBuilder().WithUpdate(updateBuilder).Build()
	if err != nil {
		return fmt.Errorf("failed to build update expression: %w", err)
	}

	_, err = r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: model.PrefixResearch + researchId},
			"SK": &types.AttributeValueMemberS{Value: model.PrefixConfig},
		},
		UpdateExpression:          expr.Update(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
	})
	if err != nil {
		return fmt.Errorf("failed to update research: %w", err)
	}

	return nil
}

// AddAccountLink atomically adds accountID to research.accountIds (a
// DynamoDB String Set). ADD on a set already containing the value is a
// no-op, so this is idempotent and race-free -- unlike a read-modify-write
// on a List, two concurrent LinkAccount calls for different accounts can
// never lose one side's change.
func (r *ResearchRepository) AddAccountLink(ctx context.Context, researchId, accountID string) error {
	// attribute_exists(PK): without it, UpdateItem's default upsert behavior
	// would silently CREATE a zombie RESEARCH#{id}/CONFIG item containing
	// only accountIds if the research was deleted between the service
	// layer's GetResearch call and this write -- same failure mode
	// SetPublicShareTokenIfAbsent (account.go) guards against, and for the
	// same reason.
	expr, err := expression.NewBuilder().
		WithCondition(expression.AttributeExists(expression.Name("PK"))).
		WithUpdate(expression.Add(expression.Name("accountIds"), expression.Value(&types.AttributeValueMemberSS{Value: []string{accountID}}))).
		Build()
	if err != nil {
		return fmt.Errorf("failed to build update expression: %w", err)
	}

	_, err = r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: model.PrefixResearch + researchId},
			"SK": &types.AttributeValueMemberS{Value: model.PrefixConfig},
		},
		ConditionExpression:       expr.Condition(),
		UpdateExpression:          expr.Update(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
	})
	if err != nil {
		var ccfe *types.ConditionalCheckFailedException
		if errors.As(err, &ccfe) {
			return fmt.Errorf("%w: research %s not found", ErrConditionFailed, researchId)
		}
		return fmt.Errorf("failed to add account link: %w", err)
	}
	return nil
}

// RemoveAccountLink atomically removes accountID from research.accountIds.
// Same idempotency/race-freedom as AddAccountLink -- DELETE on a set not
// containing the value is a no-op rather than an error. Same
// attribute_exists(PK) guard as AddAccountLink, for the same reason.
func (r *ResearchRepository) RemoveAccountLink(ctx context.Context, researchId, accountID string) error {
	expr, err := expression.NewBuilder().
		WithCondition(expression.AttributeExists(expression.Name("PK"))).
		WithUpdate(expression.Delete(expression.Name("accountIds"), expression.Value(&types.AttributeValueMemberSS{Value: []string{accountID}}))).
		Build()
	if err != nil {
		return fmt.Errorf("failed to build update expression: %w", err)
	}

	_, err = r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: model.PrefixResearch + researchId},
			"SK": &types.AttributeValueMemberS{Value: model.PrefixConfig},
		},
		ConditionExpression:       expr.Condition(),
		UpdateExpression:          expr.Update(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
	})
	if err != nil {
		var ccfe *types.ConditionalCheckFailedException
		if errors.As(err, &ccfe) {
			return fmt.Errorf("%w: research %s not found", ErrConditionFailed, researchId)
		}
		return fmt.Errorf("failed to remove account link: %w", err)
	}
	return nil
}

// mapTransactionCanceledError inspects a TransactWriteItems cancellation to
// determine whether item 0 -- the CONFIG Update in both
// LinkAccountTransactional and UnlinkAccountTransactional -- is what
// actually failed its condition. TransactionCanceledException also fires
// for TransactionConflict (a concurrent transaction touching the same
// item, exactly what racing Link/Unlink calls for the same research
// produce) and throttling, neither of which mean the research doesn't
// exist. Mapping every cancellation to ErrConditionFailed would turn a
// transient, retryable conflict into a wrong "research not found" 404 for
// a caller who did nothing wrong.
func mapTransactionCanceledError(err error, researchId, verb string) error {
	var tce *types.TransactionCanceledException
	if errors.As(err, &tce) {
		if len(tce.CancellationReasons) > 0 && aws.ToString(tce.CancellationReasons[0].Code) == "ConditionalCheckFailed" {
			return fmt.Errorf("%w: research %s not found", ErrConditionFailed, researchId)
		}
		return fmt.Errorf("failed to %s account: transaction canceled, retry: %w", verb, err)
	}
	return fmt.Errorf("failed to %s account transactionally: %w", verb, err)
}

// LinkAccountTransactional atomically ADDs accountID to research.accountIds
// AND puts the RESEARCHREF# reverse-index item in one TransactWriteItems
// call. Doing these as two separate requests (the original AddAccountLink +
// mainRepo.PutResearchRef) left a gap: a concurrent LinkAccount/UnlinkAccount
// pair for the SAME (research, account) could interleave as
// "unlink's set-DELETE, link's set-ADD, link's ref-PUT, unlink's ref-DELETE"
// -- leaving the canonical accountIds set linked but the reverse-index ref
// deleted. ListAccountResearch reads from that ref, so a genuinely-linked
// research would then be permanently invisible from the account's list
// until someone happens to call LinkAccount again for the same pair.
// TransactWriteItems makes both writes land or fail together, closing that
// window entirely rather than narrowing it.
func (r *ResearchRepository) LinkAccountTransactional(ctx context.Context, researchId, accountID string, ref *model.ResearchRef) error {
	refItem, err := attributevalue.MarshalMap(ref)
	if err != nil {
		return fmt.Errorf("marshal research ref: %w", err)
	}
	updateExpr, err := expression.NewBuilder().
		WithCondition(expression.AttributeExists(expression.Name("PK"))).
		WithUpdate(expression.Add(expression.Name("accountIds"), expression.Value(&types.AttributeValueMemberSS{Value: []string{accountID}}))).
		Build()
	if err != nil {
		return fmt.Errorf("build update expression: %w", err)
	}
	_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: []types.TransactWriteItem{
			{
				Update: &types.Update{
					TableName: aws.String(r.tableName),
					Key: map[string]types.AttributeValue{
						"PK": &types.AttributeValueMemberS{Value: model.PrefixResearch + researchId},
						"SK": &types.AttributeValueMemberS{Value: model.PrefixConfig},
					},
					ConditionExpression:       updateExpr.Condition(),
					UpdateExpression:          updateExpr.Update(),
					ExpressionAttributeNames:  updateExpr.Names(),
					ExpressionAttributeValues: updateExpr.Values(),
				},
			},
			{
				Put: &types.Put{
					TableName: aws.String(r.tableName),
					Item:      refItem,
				},
			},
		},
	})
	if err != nil {
		return mapTransactionCanceledError(err, researchId, "link")
	}
	return nil
}

// UnlinkAccountTransactional is LinkAccountTransactional's inverse: atomic
// set-DELETE + ref-delete in one TransactWriteItems call, for the same
// reason (closing the canonical-vs-reverse-index interleaving gap).
func (r *ResearchRepository) UnlinkAccountTransactional(ctx context.Context, researchId, accountID string) error {
	updateExpr, err := expression.NewBuilder().
		WithCondition(expression.AttributeExists(expression.Name("PK"))).
		WithUpdate(expression.Delete(expression.Name("accountIds"), expression.Value(&types.AttributeValueMemberSS{Value: []string{accountID}}))).
		Build()
	if err != nil {
		return fmt.Errorf("build update expression: %w", err)
	}
	_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: []types.TransactWriteItem{
			{
				Update: &types.Update{
					TableName: aws.String(r.tableName),
					Key: map[string]types.AttributeValue{
						"PK": &types.AttributeValueMemberS{Value: model.PrefixResearch + researchId},
						"SK": &types.AttributeValueMemberS{Value: model.PrefixConfig},
					},
					ConditionExpression:       updateExpr.Condition(),
					UpdateExpression:          updateExpr.Update(),
					ExpressionAttributeNames:  updateExpr.Names(),
					ExpressionAttributeValues: updateExpr.Values(),
				},
			},
			{
				Delete: &types.Delete{
					TableName: aws.String(r.tableName),
					Key: map[string]types.AttributeValue{
						"PK": &types.AttributeValueMemberS{Value: model.PrefixAccount + accountID},
						"SK": &types.AttributeValueMemberS{Value: model.PrefixResearchRef + researchId},
					},
				},
			},
		},
	})
	if err != nil {
		return mapTransactionCanceledError(err, researchId, "unlink")
	}
	return nil
}

// ListUserResearch lists all research tasks for a user
// Query PK=USER#{userId}, SK begins_with RESEARCH#, then fetch each full record
func (r *ResearchRepository) ListUserResearch(ctx context.Context, userId string) ([]model.Research, error) {
	keyEx := expression.Key("PK").Equal(expression.Value(model.PrefixUser + userId)).
		And(expression.Key("SK").BeginsWith(model.PrefixResearch))
	expr, err := expression.NewBuilder().WithKeyCondition(keyEx).Build()
	if err != nil {
		return nil, fmt.Errorf("failed to build expression: %w", err)
	}

	result, err := r.client.Query(ctx, &dynamodb.QueryInput{
		TableName:                 aws.String(r.tableName),
		KeyConditionExpression:    expr.KeyCondition(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
		ScanIndexForward:          aws.Bool(false),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to query user research: %w", err)
	}

	var indexItems []researchIndexItem
	if err := attributevalue.UnmarshalListOfMaps(result.Items, &indexItems); err != nil {
		return nil, fmt.Errorf("failed to unmarshal research index items: %w", err)
	}

	researches := make([]model.Research, 0, len(indexItems))
	for _, idx := range indexItems {
		research, err := r.GetResearch(ctx, idx.ResearchID)
		if err != nil {
			return nil, fmt.Errorf("failed to get research %s: %w", idx.ResearchID, err)
		}
		if research != nil {
			researches = append(researches, *research)
		}
	}

	return researches, nil
}

// BatchGetResearch retrieves multiple research items by ID using BatchGetItem.
// Handles DynamoDB's 100-item limit per request and retries UnprocessedKeys.
func (r *ResearchRepository) BatchGetResearch(ctx context.Context, researchIds []string) ([]model.Research, error) {
	if len(researchIds) == 0 {
		return nil, nil
	}

	allKeys := make([]map[string]types.AttributeValue, len(researchIds))
	for i, id := range researchIds {
		allKeys[i] = map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: model.PrefixResearch + id},
			"SK": &types.AttributeValueMemberS{Value: model.PrefixConfig},
		}
	}

	var researches []model.Research

	for start := 0; start < len(allKeys); start += 100 {
		end := start + 100
		if end > len(allKeys) {
			end = len(allKeys)
		}
		pending := allKeys[start:end]

		for attempt := 0; attempt < 3 && len(pending) > 0; attempt++ {
			result, err := r.client.BatchGetItem(ctx, &dynamodb.BatchGetItemInput{
				RequestItems: map[string]types.KeysAndAttributes{
					r.tableName: {Keys: pending},
				},
			})
			if err != nil {
				return researches, fmt.Errorf("failed to batch get research: %w", err)
			}

			for _, item := range result.Responses[r.tableName] {
				var ri researchItem
				if err := attributevalue.UnmarshalMap(item, &ri); err != nil {
					log.Printf("warn: failed to unmarshal research item: %v", err)
					continue
				}
				researches = append(researches, ri.Research)
			}

			if unprocessed, ok := result.UnprocessedKeys[r.tableName]; ok && len(unprocessed.Keys) > 0 {
				pending = unprocessed.Keys
			} else {
				pending = nil
				break
			}
		}
		if len(pending) > 0 {
			// Unprocessed keys after exhausting retries means some requested
			// items were never actually read -- silently returning what we
			// did get (the previous behavior) is indistinguishable from "the
			// item doesn't exist" to every caller, including ones using this
			// as a canonical-membership check for a destructive-delete guard
			// (ProjectService.hasLinkedResearch). Fail closed instead of
			// letting a throttled batch masquerade as a complete result.
			return researches, fmt.Errorf("BatchGetResearch: %d keys unprocessed after retries", len(pending))
		}
	}

	return researches, nil
}

// ListSubPages returns research items that have the given parentId
// Filters the user's research list in-memory (suitable for small volumes)
func (r *ResearchRepository) ListSubPages(ctx context.Context, userId, parentId string) ([]model.Research, error) {
	all, err := r.ListUserResearch(ctx, userId)
	if err != nil {
		return nil, fmt.Errorf("failed to list user research for sub-pages: %w", err)
	}

	subPages := make([]model.Research, 0)
	for _, res := range all {
		if res.ParentID == parentId {
			subPages = append(subPages, res)
		}
	}

	return subPages, nil
}

// RemoveResearchField removes a single attribute from a research record
func (r *ResearchRepository) RemoveResearchField(ctx context.Context, researchId, fieldName string) error {
	update := expression.Remove(expression.Name(fieldName))
	expr, err := expression.NewBuilder().WithUpdate(update).Build()
	if err != nil {
		return fmt.Errorf("failed to build remove expression: %w", err)
	}

	_, err = r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: model.PrefixResearch + researchId},
			"SK": &types.AttributeValueMemberS{Value: model.PrefixConfig},
		},
		UpdateExpression:         expr.Update(),
		ExpressionAttributeNames: expr.Names(),
	})
	if err != nil {
		return fmt.Errorf("failed to remove field %s: %w", fieldName, err)
	}

	return nil
}

// DeleteResearch deletes the main record, user index, and all associated share records
func (r *ResearchRepository) DeleteResearch(ctx context.Context, researchId, userId string) error {
	_, err := r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: []types.TransactWriteItem{
			{
				Delete: &types.Delete{
					TableName: aws.String(r.tableName),
					Key: map[string]types.AttributeValue{
						"PK": &types.AttributeValueMemberS{Value: model.PrefixResearch + researchId},
						"SK": &types.AttributeValueMemberS{Value: model.PrefixConfig},
					},
				},
			},
			{
				Delete: &types.Delete{
					TableName: aws.String(r.tableName),
					Key: map[string]types.AttributeValue{
						"PK": &types.AttributeValueMemberS{Value: model.PrefixUser + userId},
						"SK": &types.AttributeValueMemberS{Value: model.PrefixResearch + researchId},
					},
				},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to delete research: %w", err)
	}

	r.cleanupResearchShares(ctx, researchId)
	return nil
}

func (r *ResearchRepository) cleanupResearchShares(ctx context.Context, researchId string) {
	keyEx := expression.Key("PK").Equal(expression.Value(model.PrefixResearch + researchId)).
		And(expression.Key("SK").BeginsWith("SHARE_TO#"))
	expr, err := expression.NewBuilder().WithKeyCondition(keyEx).Build()
	if err != nil {
		log.Printf("warn: failed to build cleanup expression for research %s: %v", researchId, err)
		return
	}

	var lastKey map[string]types.AttributeValue
	for {
		input := &dynamodb.QueryInput{
			TableName:                 aws.String(r.tableName),
			KeyConditionExpression:    expr.KeyCondition(),
			ExpressionAttributeNames:  expr.Names(),
			ExpressionAttributeValues: expr.Values(),
			ProjectionExpression:      aws.String("PK, SK, sharedToId"),
		}
		if lastKey != nil {
			input.ExclusiveStartKey = lastKey
		}

		result, err := r.client.Query(ctx, input)
		if err != nil {
			log.Printf("warn: failed to query share records for research %s: %v", researchId, err)
			return
		}

	for _, item := range result.Items {
		sharedToID := ""
		if v, ok := item["sharedToId"].(*types.AttributeValueMemberS); ok {
			sharedToID = v.Value
		}
		if _, err := r.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
			TableName: aws.String(r.tableName),
			Key:       map[string]types.AttributeValue{"PK": item["PK"], "SK": item["SK"]},
		}); err != nil {
			log.Printf("warn: failed to delete share record for research %s: %v", researchId, err)
		}
		if sharedToID != "" {
			if _, err := r.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
				TableName: aws.String(r.tableName),
				Key: map[string]types.AttributeValue{
					"PK": &types.AttributeValueMemberS{Value: model.PrefixUser + sharedToID},
					"SK": &types.AttributeValueMemberS{Value: "SHARED#" + researchId},
				},
			}); err != nil {
				log.Printf("warn: failed to delete recipient share record for user %s research %s: %v", sharedToID, researchId, err)
			}
		}
	}

	lastKey = result.LastEvaluatedKey
	if lastKey == nil {
		break
	}
	}
}

// CreateResearchShare creates share records for a research (both recipient and entity lookup)
func (r *ResearchRepository) CreateResearchShare(ctx context.Context, researchID, ownerID, ownerEmail, sharedToID, email, permission string) (*model.Share, error) {
	now := time.Now().UTC()

	shareForRecipient := &model.Share{
		PK:         model.PrefixUser + sharedToID,
		SK:         model.PrefixShare + researchID,
		MeetingID:  researchID,
		OwnerID:    ownerID,
		OwnerEmail: ownerEmail,
		SharedToID: sharedToID,
		Email:      email,
		Permission: permission,
		CreatedAt:  now,
		EntityType: "RESEARCH_SHARE",
	}

	item1, err := attributevalue.MarshalMap(shareForRecipient)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal share: %w", err)
	}

	shareForResearch := &model.Share{
		PK:         model.PrefixResearch + researchID,
		SK:         model.PrefixShareTo + sharedToID,
		MeetingID:  researchID,
		OwnerID:    ownerID,
		OwnerEmail: ownerEmail,
		SharedToID: sharedToID,
		Email:      email,
		Permission: permission,
		CreatedAt:  now,
		EntityType: "RESEARCH_SHARE",
	}

	item2, err := attributevalue.MarshalMap(shareForResearch)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal share: %w", err)
	}

	_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: []types.TransactWriteItem{
			{Put: &types.Put{TableName: aws.String(r.tableName), Item: item1}},
			{Put: &types.Put{TableName: aws.String(r.tableName), Item: item2}},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create research share: %w", err)
	}

	return shareForRecipient, nil
}

// GetResearchShare retrieves a share record for a research
func (r *ResearchRepository) GetResearchShare(ctx context.Context, sharedToID, researchID string) (*model.Share, error) {
	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: model.PrefixUser + sharedToID},
			"SK": &types.AttributeValueMemberS{Value: model.PrefixShare + researchID},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get research share: %w", err)
	}
	if result.Item == nil {
		return nil, nil
	}

	var share model.Share
	if err := attributevalue.UnmarshalMap(result.Item, &share); err != nil {
		return nil, fmt.Errorf("failed to unmarshal share: %w", err)
	}
	return &share, nil
}

// DeleteResearchShare deletes both share records for a research
func (r *ResearchRepository) DeleteResearchShare(ctx context.Context, sharedToID, researchID string) error {
	_, err := r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: []types.TransactWriteItem{
			{
				Delete: &types.Delete{
					TableName: aws.String(r.tableName),
					Key: map[string]types.AttributeValue{
						"PK": &types.AttributeValueMemberS{Value: model.PrefixUser + sharedToID},
						"SK": &types.AttributeValueMemberS{Value: model.PrefixShare + researchID},
					},
				},
			},
			{
				Delete: &types.Delete{
					TableName: aws.String(r.tableName),
					Key: map[string]types.AttributeValue{
						"PK": &types.AttributeValueMemberS{Value: model.PrefixResearch + researchID},
						"SK": &types.AttributeValueMemberS{Value: model.PrefixShareTo + sharedToID},
					},
				},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to delete research share: %w", err)
	}
	return nil
}

// ListSharesForResearch lists all shares for a research
func (r *ResearchRepository) ListSharesForResearch(ctx context.Context, researchID string) ([]model.Share, error) {
	keyEx := expression.Key("PK").Equal(expression.Value(model.PrefixResearch + researchID)).
		And(expression.Key("SK").BeginsWith(model.PrefixShareTo))
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
		return nil, fmt.Errorf("failed to query research shares: %w", err)
	}

	var shares []model.Share
	if err := attributevalue.UnmarshalListOfMaps(result.Items, &shares); err != nil {
		return nil, fmt.Errorf("failed to unmarshal shares: %w", err)
	}
	return shares, nil
}
