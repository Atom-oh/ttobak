package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
)

// MarkNewsDeleted revokes canonical publication before object cleanup. A retained
// tombstone keeps immutable cleanup keys available for retries without deleting a
// concurrently published replacement. Source authorization is pinned in the
// write: an admin needs only the source; a non-empty accountID requires the
// source to still belong to that account and, in the same transaction, the
// account META row and the caller's direct MEMBER# row to exist (a revoked
// member fails with ErrAccountNewsAccess); otherwise the caller must own a
// per-user source.
func (r *CrawlerRepository) MarkNewsDeleted(ctx context.Context, key NewsReadKey, observed map[string]types.AttributeValue, userID string, isAdmin bool, accountID string) error {
	if userID == "" {
		return ErrConditionFailed
	}
	owner := expression.Name("PK").AttributeExists().
		And(expression.Name("sourceId").Equal(expression.Value(key.SourceID)))
	switch {
	case isAdmin:
	case accountID != "":
		owner = owner.And(expression.Name("accountId").Equal(expression.Value(accountID)))
	default:
		owner = owner.And(expression.Name("ownerId").Equal(expression.Value(userID))).
			And(expression.Name("accountId").AttributeNotExists())
	}
	source, err := expression.NewBuilder().WithCondition(owner).Build()
	if err != nil {
		return err
	}
	condition := expression.Name("PK").AttributeExists()
	for _, field := range []string{
		"schemaVersion", "qualityStatus", "sourceId", "docHash", "attemptToken",
		"sourceRevision", "s3Key", "artifactKey", "markdownSha256", "artifactSha256", "type",
	} {
		if observed[field] == nil {
			return ErrConditionFailed
		}
		condition = condition.And(expression.Name(field).Equal(expression.Value(observed[field])))
	}
	update := expression.Set(expression.Name("qualityStatus"), expression.Value("DELETED")).
		Set(expression.Name("inKB"), expression.Value(false))
	document, err := expression.NewBuilder().WithCondition(condition).WithUpdate(update).Build()
	if err != nil {
		return err
	}
	items := []types.TransactWriteItem{
		{ConditionCheck: &types.ConditionCheck{
			TableName: aws.String(r.tableName),
			Key: map[string]types.AttributeValue{
				"PK": &types.AttributeValueMemberS{Value: "CRAWLER#" + key.SourceID},
				"SK": &types.AttributeValueMemberS{Value: "CONFIG"},
			},
			ConditionExpression: source.Condition(), ExpressionAttributeNames: source.Names(),
			ExpressionAttributeValues: source.Values(),
		}},
		{Update: &types.Update{
			TableName: aws.String(r.tableName),
			Key: map[string]types.AttributeValue{
				"PK": &types.AttributeValueMemberS{Value: "CRAWLER#" + key.SourceID},
				"SK": &types.AttributeValueMemberS{Value: key.SortKey},
			},
			ConditionExpression: document.Condition(), UpdateExpression: document.Update(),
			ExpressionAttributeNames: document.Names(), ExpressionAttributeValues: document.Values(),
		}},
	}
	const firstAccess = 2
	if !isAdmin && accountID != "" {
		access, err := accountAccessChecks(r.tableName, accountID, userID)
		if err != nil {
			return err
		}
		items = append(items, access...)
	}
	_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: items})
	if err != nil {
		if merging := mergeFenceError(err); merging != nil {
			return merging
		}
		var canceled *types.TransactionCanceledException
		if errors.As(err, &canceled) {
			failed := false
			for i, reason := range canceled.CancellationReasons {
				if aws.ToString(reason.Code) == "ConditionalCheckFailed" {
					if i >= firstAccess {
						return ErrAccountNewsAccess
					}
					failed = true
				}
			}
			if failed {
				return fmt.Errorf("%w: publication or source owner changed", ErrConditionFailed)
			}
		}
		return fmt.Errorf("unpublish news document: %w", err)
	}
	return nil
}

// ClaimLegacyAccountDocumentDelete fences a non-admin delete of a legacy
// (unversioned) document under an account-owned source before any object is
// removed. One transaction requires the CONFIG row to still name accountID,
// the account META row and the caller's direct MEMBER# row to exist, and the
// document to still be a legacy row, and stamps the row with the claim. A
// revoked member (or a source no longer owned by the account) fails with
// ErrAccountNewsAccess; a missing or concurrently republished document fails
// with ErrConditionFailed. The stamp is harmless on retry and DeleteDocument
// still conditions the final removal on the legacy shape.
func (r *CrawlerRepository) ClaimLegacyAccountDocumentDelete(ctx context.Context, sourceID, docHash, accountID, userID string) error {
	if sourceID == "" || docHash == "" {
		return fmt.Errorf("legacy document claim needs source and document")
	}
	access, err := accountAccessChecks(r.tableName, accountID, userID)
	if err != nil {
		return err
	}
	source, err := expression.NewBuilder().WithCondition(expression.Name("PK").AttributeExists().
		And(expression.Name("accountId").Equal(expression.Value(accountID)))).Build()
	if err != nil {
		return err
	}
	document, err := expression.NewBuilder().WithCondition(expression.Name("PK").AttributeExists().
		And(expression.Name("schemaVersion").AttributeNotExists()).
		And(expression.Name("qualityStatus").AttributeNotExists()).
		And(expression.Name("attemptToken").AttributeNotExists())).
		WithUpdate(expression.Set(expression.Name("deleteClaimedBy"), expression.Value(userID)).
			Set(expression.Name("deleteClaimedAt"), expression.Value(time.Now().UTC().Format(time.RFC3339)))).
		Build()
	if err != nil {
		return err
	}
	items := []types.TransactWriteItem{
		{ConditionCheck: &types.ConditionCheck{
			TableName: aws.String(r.tableName),
			Key: map[string]types.AttributeValue{
				"PK": &types.AttributeValueMemberS{Value: model.PrefixCrawler + sourceID},
				"SK": &types.AttributeValueMemberS{Value: model.PrefixConfig},
			},
			ConditionExpression: source.Condition(), ExpressionAttributeNames: source.Names(),
			ExpressionAttributeValues: source.Values(),
		}},
		{Update: &types.Update{
			TableName: aws.String(r.tableName),
			Key: map[string]types.AttributeValue{
				"PK": &types.AttributeValueMemberS{Value: model.PrefixCrawler + sourceID},
				"SK": &types.AttributeValueMemberS{Value: model.PrefixDoc + docHash},
			},
			ConditionExpression: document.Condition(), UpdateExpression: document.Update(),
			ExpressionAttributeNames: document.Names(), ExpressionAttributeValues: document.Values(),
		}},
	}
	items = append(items, access...)
	_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: items})
	if err == nil {
		return nil
	}
	if merging := mergeFenceError(err); merging != nil {
		return merging
	}
	var canceled *types.TransactionCanceledException
	if errors.As(err, &canceled) && len(canceled.CancellationReasons) == len(items) {
		failed := false
		for i, reason := range canceled.CancellationReasons {
			switch aws.ToString(reason.Code) {
			case "None":
			case "ConditionalCheckFailed":
				if i != 1 {
					return ErrAccountNewsAccess
				}
				failed = true
			default:
				return fmt.Errorf("claim legacy document delete: %w", err)
			}
		}
		if failed {
			return fmt.Errorf("%w: document was removed or replaced", ErrConditionFailed)
		}
	}
	return fmt.Errorf("claim legacy document delete: %w", err)
}

// accountAccessChecks returns ConditionChecks that the account META row is open
// (exists, not merging or merged) and the user's direct MEMBER# row exists, like the other account writers.
func accountAccessChecks(table, accountID, userID string) ([]types.TransactWriteItem, error) {
	if accountID == "" || userID == "" {
		return nil, fmt.Errorf("account access check needs account and user")
	}
	exists, err := expression.NewBuilder().WithCondition(expression.AttributeExists(expression.Name("PK"))).Build()
	if err != nil {
		return nil, err
	}
	open, err := metaOpenCheck(table, accountID)
	if err != nil {
		return nil, err
	}
	return []types.TransactWriteItem{open, {ConditionCheck: &types.ConditionCheck{TableName: aws.String(table),
		Key: accountNewsKey(model.PrefixAccount+accountID, model.PrefixMember+userID), ConditionExpression: exists.Condition(), ExpressionAttributeNames: exists.Names()}}}, nil
}
