package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/google/uuid"
	"github.com/ttobak/backend/internal/model"
)

// MergeEventRow is one readable source event and the sort key it is stored at.
type MergeEventRow struct {
	SK    string
	Event model.AccountEvent
}

// ListAccountEventsForMerge reads one page of an account's events in sort-key
// order after afterSK. A row that cannot be opened (ErrEventUnreadable) does
// not fail the page: its sort key is returned in unreadable so the merge can
// leave it in place and report it. A missing key or a KMS transport failure
// still fails the whole page.
func (r *DynamoDBRepository) ListAccountEventsForMerge(ctx context.Context, account string, limit int, afterSK string) ([]MergeEventRow, []string, string, error) {
	if r.sealer == nil {
		return nil, nil, "", ErrEventEncryptionUnavailable
	}
	if account == "" || strings.Contains(account, "#") || limit < 1 || afterSK != "" && !strings.HasPrefix(afterSK, eventPrefix) {
		return nil, nil, "", errors.New("invalid merge event listing")
	}
	key := expression.Key("PK").Equal(expression.Value(model.PrefixAccount + account)).And(expression.Key("SK").BeginsWith(eventPrefix))
	expr, err := expression.NewBuilder().WithKeyCondition(key).Build()
	if err != nil {
		return nil, nil, "", err
	}
	in := &dynamodb.QueryInput{TableName: aws.String(r.tableName), KeyConditionExpression: expr.KeyCondition(),
		ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(),
		Limit: aws.Int32(int32(limit)), ConsistentRead: aws.Bool(true)}
	if afterSK != "" {
		in.ExclusiveStartKey = contactKey(account, afterSK)
	}
	out, err := r.client.Query(ctx, in)
	if err != nil {
		return nil, nil, "", fmt.Errorf("query merge events: %w", err)
	}
	rows := make([]MergeEventRow, len(out.Items))
	errs := make([]error, len(out.Items))
	sem := make(chan struct{}, contactReadConcurrency)
	var wg sync.WaitGroup
	for i, item := range out.Items {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, item map[string]types.AttributeValue) {
			defer wg.Done()
			defer func() { <-sem }()
			rows[i].Event, rows[i].SK, errs[i] = r.openEvent(ctx, account, item)
		}(i, item)
	}
	wg.Wait()
	readable := make([]MergeEventRow, 0, len(rows))
	var unreadable []string
	for i, err := range errs {
		switch {
		case err == nil:
			readable = append(readable, rows[i])
		case errors.Is(err, ErrEventUnreadable):
			sk, _ := out.Items[i]["SK"].(*types.AttributeValueMemberS)
			if sk == nil || !strings.HasPrefix(sk.Value, eventPrefix) {
				return nil, nil, "", errors.New("invalid event row key")
			}
			unreadable = append(unreadable, sk.Value)
		default:
			return nil, nil, "", fmt.Errorf("decode merge events: %w", err)
		}
	}
	next := ""
	if len(out.LastEvaluatedKey) > 0 {
		pk, pkOK := out.LastEvaluatedKey["PK"].(*types.AttributeValueMemberS)
		sk, skOK := out.LastEvaluatedKey["SK"].(*types.AttributeValueMemberS)
		if !pkOK || !skOK || pk.Value != model.PrefixAccount+account || !strings.HasPrefix(sk.Value, eventPrefix) {
			return nil, nil, "", errors.New("invalid event page key")
		}
		next = sk.Value
	}
	return readable, unreadable, next, nil
}

// remapAttendees returns a copy of attendees whose contact IDs are replaced
// through contactMap; an ID absent from the map is cleared, keeping the
// attendee's name and email.
func remapAttendees(attendees []model.EventAttendee, contactMap map[string]string) []model.EventAttendee {
	if attendees == nil {
		return nil
	}
	out := make([]model.EventAttendee, len(attendees))
	for i, a := range attendees {
		if a.ContactID != "" {
			a.ContactID = contactMap[a.ContactID]
		}
		out[i] = a
	}
	return out
}

// MoveEventForMerge moves one source event, stored at sk, into the target
// account under the same sort key and event ID. The target row is sealed with
// the target's binding and a new revision, its attendees' contact IDs mapped
// through contactMap (an ID absent from the map is cleared), and it is
// create-only together with its EVENTREF# row. The source event is deleted
// at the revision that was read and the source reference row with it (a
// missing source reference is tolerated; one pointing elsewhere fails). All
// four commit with the merge fence in one transaction, so the source event
// never disappears without its re-sealed target copy.
func (r *DynamoDBRepository) MoveEventForMerge(ctx context.Context, job *model.AccountMergeJob, sk string, from model.AccountEvent, contactMap map[string]string) error {
	if job == nil || job.SourceAccountID == "" || job.TargetAccountID == "" || job.SourceAccountID == job.TargetAccountID {
		return fmt.Errorf("invalid merge event job")
	}
	if from.EventID == "" || from.Revision == "" || strings.Contains(from.EventID, "#") ||
		!strings.HasPrefix(sk, eventPrefix) || !strings.HasSuffix(sk, "#"+from.EventID) {
		return fmt.Errorf("invalid merge source event")
	}
	for _, id := range contactMap {
		if strings.Contains(id, "#") {
			return fmt.Errorf("invalid merge event contact map")
		}
	}
	fence, err := r.mergeFenceChecks(job)
	if err != nil {
		return err
	}
	moved := from
	moved.Revision = uuid.NewString()
	moved.Attendees = remapAttendees(from.Attendees, contactMap)
	eventItem, err := r.sealedEventItem(ctx, job.TargetAccountID, sk, &moved)
	if err != nil {
		return err
	}
	refItem, err := attributevalue.MarshalMap(storedEventRef{PK: model.PrefixAccount + job.TargetAccountID, SK: eventRefPrefix + from.EventID, EventID: from.EventID, EventSK: sk})
	if err != nil {
		return fmt.Errorf("marshal merge event ref: %w", err)
	}
	absent, err := expression.NewBuilder().WithCondition(expression.AttributeNotExists(expression.Name("PK"))).Build()
	if err != nil {
		return err
	}
	read, err := expression.NewBuilder().WithCondition(expression.AttributeExists(expression.Name("PK")).
		And(expression.Name("revision").Equal(expression.Value(from.Revision)))).Build()
	if err != nil {
		return err
	}
	pointsHere, err := expression.NewBuilder().WithCondition(expression.AttributeNotExists(expression.Name("PK")).
		Or(expression.Name("eventSK").Equal(expression.Value(sk)))).Build()
	if err != nil {
		return err
	}
	table := aws.String(r.tableName)
	create := func(item map[string]types.AttributeValue) types.TransactWriteItem {
		return types.TransactWriteItem{Put: &types.Put{TableName: table, Item: item,
			ConditionExpression: absent.Condition(), ExpressionAttributeNames: absent.Names()}}
	}
	remove := func(sk string, c expression.Expression) types.TransactWriteItem {
		return types.TransactWriteItem{Delete: &types.Delete{TableName: table, Key: contactKey(job.SourceAccountID, sk),
			ConditionExpression: c.Condition(), ExpressionAttributeNames: c.Names(), ExpressionAttributeValues: c.Values()}}
	}
	items := append([]types.TransactWriteItem{create(eventItem)}, fence...)
	items = append(items, create(refItem), remove(sk, read), remove(eventRefPrefix+from.EventID, pointsHere))
	if _, err := r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: items}); err != nil {
		moverFailed, fenceErr := classifyMergeFence(err)
		if fenceErr != nil {
			return fenceErr
		}
		var cancelled *types.TransactionCanceledException
		if moverFailed || errors.As(err, &cancelled) && mergeTailFailed(cancelled.CancellationReasons) {
			return fmt.Errorf("merge event: %w: %w", ErrConditionFailed, err)
		}
		return fmt.Errorf("merge event: %w", err)
	}
	return nil
}
