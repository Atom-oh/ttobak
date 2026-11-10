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
	"github.com/google/uuid"
	"github.com/ttobak/backend/internal/model"
)

// ListAccountContactsForMerge reads one page of an account's contacts like
// ListAccountContacts, except that a row which cannot be opened
// (ErrContactUnreadable) does not fail the page: its contact ID is returned
// in unreadable instead, so the merge can leave it in place and report it.
// A missing key or a KMS transport failure still fails the whole page.
func (r *DynamoDBRepository) ListAccountContactsForMerge(ctx context.Context, account string, limit int, after string) ([]model.AccountContact, []string, string, error) {
	if _, err := r.contactSealer(); err != nil {
		return nil, nil, "", err
	}
	items, next, err := r.queryContactPage(ctx, account, limit, after)
	if err != nil {
		return nil, nil, "", err
	}
	opened, errs := r.openContactPage(ctx, account, items)
	contacts := make([]model.AccountContact, 0, len(items))
	var unreadable []string
	for i, err := range errs {
		switch {
		case err == nil:
			contacts = append(contacts, opened[i])
		case errors.Is(err, ErrContactUnreadable):
			sk, _ := items[i]["SK"].(*types.AttributeValueMemberS)
			if sk == nil || !strings.HasPrefix(sk.Value, contactPrefix) {
				return nil, nil, "", errors.New("invalid contact row key")
			}
			unreadable = append(unreadable, strings.TrimPrefix(sk.Value, contactPrefix))
		default:
			return nil, nil, "", fmt.Errorf("decode contacts: %w", err)
		}
	}
	return contacts, unreadable, next, nil
}

// GetMergeContactMap returns, for each given source contact ID that already
// has a MERGE#contact# row in the source partition, the target contact ID it
// became. IDs without a row are absent from the result.
func (r *DynamoDBRepository) GetMergeContactMap(ctx context.Context, sourceAccountID string, ids []string) (map[string]string, error) {
	if sourceAccountID == "" || strings.Contains(sourceAccountID, "#") {
		return nil, fmt.Errorf("invalid merge contact map read")
	}
	keys := make([]batchRowKey, 0, len(ids))
	for _, id := range ids {
		if id == "" || strings.Contains(id, "#") {
			return nil, fmt.Errorf("invalid merge contact map read")
		}
		keys = append(keys, batchRowKey{PK: model.PrefixAccount + sourceAccountID, SK: model.PrefixMergeContactMap + id})
	}
	out := map[string]string{}
	err := r.batchGetConsistent(ctx, keys, nil, func(_ batchRowKey, item map[string]types.AttributeValue) error {
		var row model.AccountMergeContactMap
		if err := attributevalue.UnmarshalMap(item, &row); err != nil {
			return fmt.Errorf("unmarshal merge contact map: %w", err)
		}
		if row.SK != model.PrefixMergeContactMap+row.SourceContactID || row.TargetContactID == "" {
			return fmt.Errorf("invalid merge contact map row")
		}
		out[row.SourceContactID] = row.TargetContactID
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// MoveContactForMerge moves one source contact into the target account under
// the same contact ID. The target row is sealed with the target's binding and
// a new revision and is create-only; the source row is deleted at the
// revision that was read; the MERGE#contact# map row records the identity
// mapping. All three commit together with the merge fence, so the source row
// never disappears without its re-sealed target copy.
func (r *DynamoDBRepository) MoveContactForMerge(ctx context.Context, job *model.AccountMergeJob, from model.AccountContact) error {
	if err := validMergeContact(job, from); err != nil {
		return err
	}
	fence, err := r.mergeFenceChecks(job)
	if err != nil {
		return err
	}
	moved := from
	moved.PK, moved.SK, moved.Revision = "", "", uuid.NewString()
	item, err := r.SealedContactItem(ctx, job.TargetAccountID, &moved)
	if err != nil {
		return err
	}
	absent, err := expression.NewBuilder().WithCondition(expression.AttributeNotExists(expression.Name("PK"))).Build()
	if err != nil {
		return err
	}
	put := types.TransactWriteItem{Put: &types.Put{
		TableName: aws.String(r.tableName), Item: item,
		ConditionExpression: absent.Condition(), ExpressionAttributeNames: absent.Names(),
	}}
	return r.commitContactMerge(ctx, job, put, fence, from, from.ContactID)
}

// MergeContactIntoExisting folds one source contact into an existing target
// contact. into carries the merged fields and the target revision that was
// observed; the target row is re-sealed only if that revision still holds.
// The source row is deleted and the map row points at into.ContactID, all in
// one fenced transaction.
func (r *DynamoDBRepository) MergeContactIntoExisting(ctx context.Context, job *model.AccountMergeJob, from model.AccountContact, into model.AccountContact) error {
	if err := validMergeContact(job, from); err != nil {
		return err
	}
	if into.ContactID == "" || into.Revision == "" || strings.Contains(into.ContactID, "#") {
		return fmt.Errorf("invalid merge target contact")
	}
	fence, err := r.mergeFenceChecks(job)
	if err != nil {
		return err
	}
	sealed, err := r.sealContact(ctx, job.TargetAccountID, &into)
	if err != nil {
		return err
	}
	cond := expression.AttributeExists(expression.Name("PK")).And(expression.Name("revision").Equal(expression.Value(into.Revision)))
	update := expression.Set(expression.Name("sealed"), expression.Value(sealed)).
		Set(expression.Name("revision"), expression.Value(uuid.NewString()))
	expr, err := expression.NewBuilder().WithCondition(cond).WithUpdate(update).Build()
	if err != nil {
		return err
	}
	upd := types.TransactWriteItem{Update: &types.Update{
		TableName: aws.String(r.tableName), Key: contactKey(job.TargetAccountID, contactPrefix+into.ContactID),
		ConditionExpression: expr.Condition(), UpdateExpression: expr.Update(),
		ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(),
	}}
	return r.commitContactMerge(ctx, job, upd, fence, from, into.ContactID)
}

func validMergeContact(job *model.AccountMergeJob, from model.AccountContact) error {
	if job == nil || job.SourceAccountID == "" || job.TargetAccountID == "" || job.SourceAccountID == job.TargetAccountID {
		return fmt.Errorf("invalid merge contact job")
	}
	if from.ContactID == "" || from.Revision == "" || strings.Contains(from.ContactID, "#") {
		return fmt.Errorf("invalid merge source contact")
	}
	return nil
}

// commitContactMerge writes [target write, fence..., source delete, map put].
// A fence failure wins; any failure of the contact items is
// ErrConditionFailed (a concurrent target edit, a target ID collision or an
// already recorded mapping) and nothing is written.
func (r *DynamoDBRepository) commitContactMerge(ctx context.Context, job *model.AccountMergeJob, target types.TransactWriteItem, fence []types.TransactWriteItem, from model.AccountContact, targetID string) error {
	present := expression.AttributeExists(expression.Name("PK")).And(expression.Name("revision").Equal(expression.Value(from.Revision)))
	delExpr, err := expression.NewBuilder().WithCondition(present).Build()
	if err != nil {
		return err
	}
	mapItem, err := attributevalue.MarshalMap(model.AccountMergeContactMap{
		PK: model.PrefixAccount + job.SourceAccountID, SK: model.PrefixMergeContactMap + from.ContactID,
		SourceContactID: from.ContactID, TargetContactID: targetID, EntityType: model.EntityTypeAccountMergeContactMap,
	})
	if err != nil {
		return fmt.Errorf("marshal merge contact map: %w", err)
	}
	absent, err := expression.NewBuilder().WithCondition(expression.AttributeNotExists(expression.Name("PK"))).Build()
	if err != nil {
		return err
	}
	items := append([]types.TransactWriteItem{target}, fence...)
	items = append(items,
		types.TransactWriteItem{Delete: &types.Delete{
			TableName: aws.String(r.tableName), Key: contactKey(job.SourceAccountID, contactPrefix+from.ContactID),
			ConditionExpression: delExpr.Condition(), ExpressionAttributeNames: delExpr.Names(), ExpressionAttributeValues: delExpr.Values(),
		}},
		types.TransactWriteItem{Put: &types.Put{
			TableName: aws.String(r.tableName), Item: mapItem,
			ConditionExpression: absent.Condition(), ExpressionAttributeNames: absent.Names(),
		}})
	if _, err := r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: items}); err != nil {
		moverFailed, fenceErr := classifyMergeFence(err)
		if fenceErr != nil {
			return fenceErr
		}
		var cancelled *types.TransactionCanceledException
		if moverFailed || errors.As(err, &cancelled) && contactTailFailed(cancelled.CancellationReasons) {
			return fmt.Errorf("merge contact: %w: %w", ErrConditionFailed, err)
		}
		return fmt.Errorf("merge contact: %w", err)
	}
	return nil
}

// contactTailFailed reports a condition failure on the source delete or map
// put, which follow the mover item and the fence.
func contactTailFailed(reasons []types.CancellationReason) bool {
	for i := 1 + mergeFenceCheckCount; i < len(reasons); i++ {
		if aws.ToString(reasons[i].Code) == "ConditionalCheckFailed" {
			return true
		}
	}
	return false
}
