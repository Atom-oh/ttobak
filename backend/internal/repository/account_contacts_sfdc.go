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

// SFDC link rows live in the Account partition beside the contacts, in plain
// form, and hold only opaque CRM record IDs (ADR-021):
//
//	SFDCCONTACT#{sfdcContactId}  index: one contact per (Account, SFDC contact)
//	SFDCSUPPRESS#{sfdcContactId} a member deleted the linked contact; the sync
//	                             never recreates it
//
// Within one partition an SFDC ID never has both rows: delete swaps the
// index for a suppression, and every writer that reserves an index checks
// that no suppression exists.
const (
	sfdcIndexPrefix    = "SFDCCONTACT#"
	sfdcSuppressPrefix = "SFDCSUPPRESS#"
	// sfdcLinksPerContact bounds the index rows one contact delete removes.
	sfdcLinksPerContact = 2
)

var (
	// ErrSfdcAlreadyLinked means another contact in the Account holds the
	// SFDC contact ID's index row.
	ErrSfdcAlreadyLinked = errors.New("sfdc contact already linked")
	// ErrSfdcSuppressed means a member deleted the contact linked to this
	// SFDC contact ID; it is not recreated or relinked.
	ErrSfdcSuppressed = errors.New("sfdc contact suppressed")
	// ErrInvalidSfdcLink means an SFDC contact or account ID is malformed.
	ErrInvalidSfdcLink = errors.New("invalid sfdc contact link")
)

// SfdcContactLink is one index row.
type SfdcContactLink struct {
	SfdcContactID string
	SfdcAccountID string
	ContactID     string
}

type sfdcIndexRow struct {
	PK            string `dynamodbav:"PK"`
	SK            string `dynamodbav:"SK"`
	SfdcContactID string `dynamodbav:"sfdcContactId"`
	ContactID     string `dynamodbav:"contactId"`
	SfdcAccountID string `dynamodbav:"sfdcAccountId,omitempty"`
}

type sfdcSuppressRow struct {
	PK            string `dynamodbav:"PK"`
	SK            string `dynamodbav:"SK"`
	SfdcContactID string `dynamodbav:"sfdcContactId"`
}

func validSfdcLink(c *model.AccountContact) error {
	if c.SfdcContactID == "" {
		if c.SfdcAccountID != "" {
			return ErrInvalidSfdcLink
		}
		return nil
	}
	if !model.ValidSfdcID(c.SfdcContactID) || c.SfdcAccountID != "" && !model.ValidSfdcID(c.SfdcAccountID) {
		return ErrInvalidSfdcLink
	}
	return nil
}

// stripSfdcLink removes every SFDC field from a contact copy.
func stripSfdcLink(c *model.AccountContact) {
	c.Source, c.SfdcContactID, c.SfdcAccountID, c.SfdcLeftAt = "", "", "", ""
}

func buildCondition(c expression.ConditionBuilder) (expression.Expression, error) {
	return expression.NewBuilder().WithCondition(c).Build()
}

// sfdcIndexPut writes the index row for contactID. With replaceOwn the row
// may already belong to contactID (a kept link); otherwise it must be absent.
func (r *DynamoDBRepository) sfdcIndexPut(account, sfdcID, sfdcAccountID, contactID string, replaceOwn bool) (types.TransactWriteItem, error) {
	item, err := attributevalue.MarshalMap(sfdcIndexRow{PK: model.PrefixAccount + account, SK: sfdcIndexPrefix + sfdcID,
		SfdcContactID: sfdcID, ContactID: contactID, SfdcAccountID: sfdcAccountID})
	if err != nil {
		return types.TransactWriteItem{}, err
	}
	cond := expression.AttributeNotExists(expression.Name("PK"))
	if replaceOwn {
		cond = cond.Or(expression.Name("contactId").Equal(expression.Value(contactID)))
	}
	expr, err := buildCondition(cond)
	if err != nil {
		return types.TransactWriteItem{}, err
	}
	return types.TransactWriteItem{Put: &types.Put{TableName: aws.String(r.tableName), Item: item,
		ConditionExpression: expr.Condition(), ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values()}}, nil
}

// sfdcIndexDelete removes contactID's index row; a row another contact holds
// fails the transaction, and an already absent row is fine.
func (r *DynamoDBRepository) sfdcIndexDelete(account, sfdcID, contactID string) (types.TransactWriteItem, error) {
	expr, err := buildCondition(expression.AttributeNotExists(expression.Name("PK")).Or(expression.Name("contactId").Equal(expression.Value(contactID))))
	if err != nil {
		return types.TransactWriteItem{}, err
	}
	return types.TransactWriteItem{Delete: &types.Delete{TableName: aws.String(r.tableName), Key: contactKey(account, sfdcIndexPrefix+sfdcID),
		ConditionExpression: expr.Condition(), ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values()}}, nil
}

// sfdcAbsentCheck requires that the row prefix+sfdcID does not exist.
func (r *DynamoDBRepository) sfdcAbsentCheck(account, prefix, sfdcID string) (types.TransactWriteItem, error) {
	expr, err := buildCondition(expression.AttributeNotExists(expression.Name("PK")))
	if err != nil {
		return types.TransactWriteItem{}, err
	}
	return types.TransactWriteItem{ConditionCheck: &types.ConditionCheck{TableName: aws.String(r.tableName), Key: contactKey(account, prefix+sfdcID),
		ConditionExpression: expr.Condition(), ExpressionAttributeNames: expr.Names()}}, nil
}

// sfdcSuppressPut writes the (idempotent) suppression row.
func (r *DynamoDBRepository) sfdcSuppressPut(account, sfdcID string) (types.TransactWriteItem, error) {
	item, err := attributevalue.MarshalMap(sfdcSuppressRow{PK: model.PrefixAccount + account, SK: sfdcSuppressPrefix + sfdcID, SfdcContactID: sfdcID})
	if err != nil {
		return types.TransactWriteItem{}, err
	}
	return types.TransactWriteItem{Put: &types.Put{TableName: aws.String(r.tableName), Item: item}}, nil
}

// sfdcReserve is the pair a linking write adds: the index put and the
// suppression check. Their cancellation reasons map to ErrSfdcAlreadyLinked
// and ErrSfdcSuppressed.
func (r *DynamoDBRepository) sfdcReserve(account string, c *model.AccountContact, replaceOwn bool) ([]types.TransactWriteItem, error) {
	put, err := r.sfdcIndexPut(account, c.SfdcContactID, c.SfdcAccountID, c.ContactID, replaceOwn)
	if err != nil {
		return nil, err
	}
	check, err := r.sfdcAbsentCheck(account, sfdcSuppressPrefix, c.SfdcContactID)
	if err != nil {
		return nil, err
	}
	return []types.TransactWriteItem{put, check}, nil
}

// querySfdcRows reads every row under prefix in the account partition,
// strongly consistently, following continuations. filter is optional.
func (r *DynamoDBRepository) querySfdcRows(ctx context.Context, account, prefix string, filter *expression.ConditionBuilder, visit func(map[string]types.AttributeValue) error) error {
	key := expression.Key("PK").Equal(expression.Value(model.PrefixAccount + account)).And(expression.Key("SK").BeginsWith(prefix))
	builder := expression.NewBuilder().WithKeyCondition(key)
	if filter != nil {
		builder = builder.WithFilter(*filter)
	}
	expr, err := builder.Build()
	if err != nil {
		return err
	}
	var start map[string]types.AttributeValue
	for {
		out, err := r.client.Query(ctx, &dynamodb.QueryInput{TableName: aws.String(r.tableName), KeyConditionExpression: expr.KeyCondition(),
			FilterExpression: expr.Filter(), ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(),
			ConsistentRead: aws.Bool(true), ExclusiveStartKey: start})
		if err != nil {
			return fmt.Errorf("query sfdc rows: %w", err)
		}
		for _, item := range out.Items {
			if err := visit(item); err != nil {
				return err
			}
		}
		if len(out.LastEvaluatedKey) == 0 {
			return nil
		}
		pk, pkOK := out.LastEvaluatedKey["PK"].(*types.AttributeValueMemberS)
		sk, skOK := out.LastEvaluatedKey["SK"].(*types.AttributeValueMemberS)
		if !pkOK || !skOK || pk.Value != model.PrefixAccount+account || !strings.HasPrefix(sk.Value, prefix) {
			return errors.New("invalid sfdc page key")
		}
		start = out.LastEvaluatedKey
	}
}

func decodeSfdcIndexRow(account string, item map[string]types.AttributeValue) (sfdcIndexRow, error) {
	var row sfdcIndexRow
	if err := attributevalue.UnmarshalMap(item, &row); err != nil {
		return row, fmt.Errorf("decode sfdc index row: %w", err)
	}
	id := strings.TrimPrefix(row.SK, sfdcIndexPrefix)
	if row.PK != model.PrefixAccount+account || !model.ValidSfdcID(id) || row.ContactID == "" {
		return row, errors.New("invalid sfdc index row")
	}
	row.SfdcContactID = id
	return row, nil
}

// ListSfdcContactLinks returns every index row of the account.
func (r *DynamoDBRepository) ListSfdcContactLinks(ctx context.Context, account string) ([]SfdcContactLink, error) {
	links := []SfdcContactLink{}
	err := r.querySfdcRows(ctx, account, sfdcIndexPrefix, nil, func(item map[string]types.AttributeValue) error {
		row, err := decodeSfdcIndexRow(account, item)
		if err != nil {
			return err
		}
		links = append(links, SfdcContactLink{SfdcContactID: row.SfdcContactID, SfdcAccountID: row.SfdcAccountID, ContactID: row.ContactID})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return links, nil
}

// ListSfdcSuppressions returns the set of suppressed SFDC contact IDs.
func (r *DynamoDBRepository) ListSfdcSuppressions(ctx context.Context, account string) (map[string]bool, error) {
	out := map[string]bool{}
	err := r.querySfdcRows(ctx, account, sfdcSuppressPrefix, nil, func(item map[string]types.AttributeValue) error {
		sk, _ := item["SK"].(*types.AttributeValueMemberS)
		if sk == nil || !model.ValidSfdcID(strings.TrimPrefix(sk.Value, sfdcSuppressPrefix)) {
			return errors.New("invalid sfdc suppression row")
		}
		out[strings.TrimPrefix(sk.Value, sfdcSuppressPrefix)] = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// contactSfdcLinks returns the SFDC IDs whose index rows point at contactID.
// It reads the plain index, so deleting a contact never needs its key.
func (r *DynamoDBRepository) contactSfdcLinks(ctx context.Context, account, contactID string) ([]string, error) {
	filter := expression.Name("contactId").Equal(expression.Value(contactID))
	var ids []string
	err := r.querySfdcRows(ctx, account, sfdcIndexPrefix, &filter, func(item map[string]types.AttributeValue) error {
		row, err := decodeSfdcIndexRow(account, item)
		if err != nil {
			return err
		}
		if row.ContactID == contactID {
			ids = append(ids, row.SfdcContactID)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(ids) > sfdcLinksPerContact {
		return nil, fmt.Errorf("contact has %d sfdc links", len(ids))
	}
	return ids, nil
}

// sfdcRowState reads the account's index owner ("" when absent) and
// suppression for one SFDC ID, strongly consistently.
func (r *DynamoDBRepository) sfdcRowState(ctx context.Context, account, sfdcID string) (string, bool, error) {
	get := func(sk string) (map[string]types.AttributeValue, error) {
		out, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(r.tableName), Key: contactKey(account, sk), ConsistentRead: aws.Bool(true)})
		if err != nil {
			return nil, fmt.Errorf("read sfdc row: %w", err)
		}
		return out.Item, nil
	}
	index, err := get(sfdcIndexPrefix + sfdcID)
	if err != nil {
		return "", false, err
	}
	owner := ""
	if len(index) > 0 {
		v, _ := index["contactId"].(*types.AttributeValueMemberS)
		if v == nil || v.Value == "" {
			return "", false, errors.New("invalid sfdc index row")
		}
		owner = v.Value
	}
	suppress, err := get(sfdcSuppressPrefix + sfdcID)
	if err != nil {
		return "", false, err
	}
	return owner, len(suppress) > 0, nil
}

// sfdcMoveItems returns the extra merge items for moving or folding a linked
// source contact into the target, and the target contact as it must be
// sealed. target is the contact that ends up holding from's link: the moved
// copy (target.ContactID == from.ContactID) or the existing contact. Rules:
//   - the source index row is always deleted;
//   - target carrying from's link reserves the target index unless the
//     target already holds that link for another contact or suppresses it,
//     in which case the link is dropped from target (the target's state wins);
//   - target keeping a different link suppresses from's ID on the target
//     unless the target holds it for another contact, so the sync does not
//     recreate the merged duplicate.
func (r *DynamoDBRepository) sfdcMoveItems(ctx context.Context, job *model.AccountMergeJob, from model.AccountContact, target *model.AccountContact) ([]types.TransactWriteItem, error) {
	if from.SfdcContactID == "" {
		if err := validSfdcLink(target); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if err := validSfdcLink(&from); err != nil {
		return nil, err
	}
	if err := validSfdcLink(target); err != nil {
		return nil, err
	}
	srcDelete, err := r.sfdcIndexDelete(job.SourceAccountID, from.SfdcContactID, from.ContactID)
	if err != nil {
		return nil, err
	}
	items := []types.TransactWriteItem{srcDelete}
	owner, suppressed, err := r.sfdcRowState(ctx, job.TargetAccountID, from.SfdcContactID)
	if err != nil {
		return nil, err
	}
	if target.SfdcContactID == from.SfdcContactID {
		if suppressed || owner != "" && owner != target.ContactID {
			stripSfdcLink(target)
			return items, nil
		}
		reserve, err := r.sfdcReserve(job.TargetAccountID, target, true)
		if err != nil {
			return nil, err
		}
		return append(items, reserve...), nil
	}
	if owner != "" {
		return items, nil
	}
	put, err := r.sfdcSuppressPut(job.TargetAccountID, from.SfdcContactID)
	if err != nil {
		return nil, err
	}
	check, err := r.sfdcAbsentCheck(job.TargetAccountID, sfdcIndexPrefix, from.SfdcContactID)
	if err != nil {
		return nil, err
	}
	return append(items, put, check), nil
}

// MoveSfdcSuppressionsForMerge moves up to limit source suppression rows to
// the target, one fenced transaction each: the source row is deleted and the
// target row written, unless the target links that SFDC ID, in which case
// the target's link wins and only the source row is deleted. done reports
// that no source suppression row is left.
func (r *DynamoDBRepository) MoveSfdcSuppressionsForMerge(ctx context.Context, job *model.AccountMergeJob, limit int) (int, bool, error) {
	if job == nil || job.SourceAccountID == "" || job.TargetAccountID == "" || job.SourceAccountID == job.TargetAccountID || limit <= 0 {
		return 0, false, fmt.Errorf("invalid merge suppression move")
	}
	var ids []string
	more := false
	errStop := errors.New("stop")
	err := r.querySfdcRows(ctx, job.SourceAccountID, sfdcSuppressPrefix, nil, func(item map[string]types.AttributeValue) error {
		if len(ids) == limit {
			more = true
			return errStop
		}
		sk, _ := item["SK"].(*types.AttributeValueMemberS)
		if sk == nil || !model.ValidSfdcID(strings.TrimPrefix(sk.Value, sfdcSuppressPrefix)) {
			return errors.New("invalid sfdc suppression row")
		}
		ids = append(ids, strings.TrimPrefix(sk.Value, sfdcSuppressPrefix))
		return nil
	})
	if err != nil && !errors.Is(err, errStop) {
		return 0, false, err
	}
	moved := 0
	for _, id := range ids {
		fence, err := r.mergeFenceChecks(job)
		if err != nil {
			return moved, false, err
		}
		owner, _, err := r.sfdcRowState(ctx, job.TargetAccountID, id)
		if err != nil {
			return moved, false, err
		}
		// [source delete, fence..., target put, target index check]: the
		// layout classifyMergeFence expects.
		items := []types.TransactWriteItem{{Delete: &types.Delete{TableName: aws.String(r.tableName), Key: contactKey(job.SourceAccountID, sfdcSuppressPrefix+id)}}}
		items = append(items, fence...)
		if owner == "" {
			put, err := r.sfdcSuppressPut(job.TargetAccountID, id)
			if err != nil {
				return moved, false, err
			}
			check, err := r.sfdcAbsentCheck(job.TargetAccountID, sfdcIndexPrefix, id)
			if err != nil {
				return moved, false, err
			}
			items = append(items, put, check)
		}
		if _, err := r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: items}); err != nil {
			if _, fenceErr := classifyMergeFence(err); fenceErr != nil {
				return moved, false, fenceErr
			}
			var cancelled *types.TransactionCanceledException
			if errors.As(err, &cancelled) {
				return moved, false, fmt.Errorf("merge sfdc suppression: %w: %w", ErrConditionFailed, err)
			}
			return moved, false, fmt.Errorf("merge sfdc suppression: %w", err)
		}
		moved++
	}
	return moved, !more, nil
}
