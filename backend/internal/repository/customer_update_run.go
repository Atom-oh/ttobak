package repository

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/sealing"
)

// ErrInvalidUpdateRun means a run, day, item or decision input failed
// validation before any key was built.
var ErrInvalidUpdateRun = errors.New("invalid customer update run input")

// ErrUpdateRunTooLarge means a run's result partition holds more than
// model.MaxUpdateRunRows rows.
var ErrUpdateRunTooLarge = errors.New("customer update run has too many rows")

// ErrUpdateDecisionUnreadable means one stored decision cannot be opened.
var ErrUpdateDecisionUnreadable = errors.New("customer update decision unreadable")

var (
	runIDPattern     = regexp.MustCompile(`^[0-9a-f]{32}$`)
	urlHashPattern   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	accountIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	dayPattern       = regexp.MustCompile(`^[0-9]{8}$`)
)

const (
	maxRunPayloadBytes     = 64 * 1024
	maxRunDraftLinks       = 20
	maxRunDraftFetchedURLs = 20
	maxRunDraftOpptyIDs    = 10
	maxRunGateOptions      = 10
	// maxNewItemsScan bounds the item rows one ListNewItems call evaluates.
	maxNewItemsScan     = 2000
	runDeleteBatchSize  = 25
	runDeleteMaxRetries = 5
	dayAccountAttrPfx   = "acct#"
	dayResearchedAttr   = "researched"
	pendingMarkedAtAttr = "updatedAt"
	pendingCursorAttr   = "lastUserId"
)

var kst = time.FixedZone("KST", 9*3600)

func invalidRun(reason string) error { return fmt.Errorf("%w: %s", ErrInvalidUpdateRun, reason) }

// ---- Pending list ----

// pendingMarkInput builds the idempotent pending marker put. The marker value
// is a nanosecond timestamp so a re-mark during a tick changes it.
func pendingMarkInput(tableName, userID string, now time.Time) *dynamodb.PutItemInput {
	return &dynamodb.PutItemInput{
		TableName: aws.String(tableName),
		Item: map[string]types.AttributeValue{
			"PK":                &types.AttributeValueMemberS{Value: model.PKUpdatePending},
			"SK":                &types.AttributeValueMemberS{Value: userID},
			"entityType":        &types.AttributeValueMemberS{Value: "UPDATE_PENDING"},
			pendingMarkedAtAttr: &types.AttributeValueMemberS{Value: now.UTC().Format(time.RFC3339Nano)},
		},
	}
}

func pendingMarksFromItems(items []map[string]types.AttributeValue) []model.UpdatePendingMark {
	out := []model.UpdatePendingMark{}
	for _, item := range items {
		sk, ok := item["SK"].(*types.AttributeValueMemberS)
		if !ok || sk.Value == "" || sk.Value == model.SKUpdatePendingCursor {
			continue
		}
		m := model.UpdatePendingMark{UserID: sk.Value}
		if at, ok := item[pendingMarkedAtAttr].(*types.AttributeValueMemberS); ok {
			m.MarkedAt = at.Value
		}
		out = append(out, m)
	}
	return out
}

// ListPendingUsers returns up to limit pending markers in key order.
func (r *CustomerUpdateRepository) ListPendingUsers(ctx context.Context, limit int) ([]model.UpdatePendingMark, error) {
	if limit < 1 {
		return nil, fmt.Errorf("pending limit must be positive")
	}
	expr, err := expression.NewBuilder().WithKeyCondition(
		expression.Key("PK").Equal(expression.Value(model.PKUpdatePending))).Build()
	if err != nil {
		return nil, fmt.Errorf("failed to build expression: %w", err)
	}
	input := &dynamodb.QueryInput{
		TableName:                 aws.String(r.tableName),
		KeyConditionExpression:    expr.KeyCondition(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
	}
	out := []model.UpdatePendingMark{}
	for {
		input.Limit = aws.Int32(int32(limit - len(out)))
		res, err := r.client.Query(ctx, input)
		if err != nil {
			return nil, fmt.Errorf("failed to query pending users: %w", err)
		}
		out = append(out, pendingMarksFromItems(res.Items)...)
		if len(out) >= limit || len(res.LastEvaluatedKey) == 0 {
			return out, nil
		}
		input.ExclusiveStartKey = res.LastEvaluatedKey
	}
}

// pendingCursorQueryInput reads the rotation cursor row with a Query (the
// worker role has Query, not GetItem, on the pending partition).
func pendingCursorQueryInput(tableName string) (*dynamodb.QueryInput, error) {
	expr, err := expression.NewBuilder().WithKeyCondition(
		expression.Key("PK").Equal(expression.Value(model.PKUpdatePending)).
			And(expression.Key("SK").Equal(expression.Value(model.SKUpdatePendingCursor)))).Build()
	if err != nil {
		return nil, fmt.Errorf("build pending-cursor expression: %w", err)
	}
	return &dynamodb.QueryInput{
		TableName: aws.String(tableName), KeyConditionExpression: expr.KeyCondition(),
		ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(),
		ConsistentRead: aws.Bool(true), Limit: aws.Int32(1),
	}, nil
}

// GetPendingCursor returns the last user id the run phases served, or "".
func (r *CustomerUpdateRepository) GetPendingCursor(ctx context.Context) (string, error) {
	in, err := pendingCursorQueryInput(r.tableName)
	if err != nil {
		return "", err
	}
	res, err := r.client.Query(ctx, in)
	if err != nil {
		return "", fmt.Errorf("failed to read pending cursor: %w", err)
	}
	for _, item := range res.Items {
		if v, ok := stringAttr(item, pendingCursorAttr); ok {
			return v, nil
		}
	}
	return "", nil
}

func pendingCursorPutInput(tableName, lastUserID string) (*dynamodb.PutItemInput, error) {
	if lastUserID == "" {
		return nil, invalidRun("pending cursor")
	}
	return &dynamodb.PutItemInput{
		TableName: aws.String(tableName),
		Item: map[string]types.AttributeValue{
			"PK":              &types.AttributeValueMemberS{Value: model.PKUpdatePending},
			"SK":              &types.AttributeValueMemberS{Value: model.SKUpdatePendingCursor},
			"entityType":      &types.AttributeValueMemberS{Value: "UPDATE_PENDING_CURSOR"},
			pendingCursorAttr: &types.AttributeValueMemberS{Value: lastUserID},
		},
	}, nil
}

// SetPendingCursor saves the last user id a tick served (unconditional put).
func (r *CustomerUpdateRepository) SetPendingCursor(ctx context.Context, lastUserID string) error {
	in, err := pendingCursorPutInput(r.tableName, lastUserID)
	if err != nil {
		return err
	}
	if _, err := r.client.PutItem(ctx, in); err != nil {
		return fmt.Errorf("failed to save pending cursor: %w", err)
	}
	return nil
}

// pendingClearInput deletes a marker only if it still holds the value the
// caller read; a legacy marker read without a value is deleted only while it
// still has none.
func pendingClearInput(tableName string, mark model.UpdatePendingMark) (*dynamodb.DeleteItemInput, error) {
	if mark.UserID == "" {
		return nil, invalidRun("pending user")
	}
	cond := expression.AttributeNotExists(expression.Name(pendingMarkedAtAttr))
	if mark.MarkedAt != "" {
		cond = expression.Name(pendingMarkedAtAttr).Equal(expression.Value(mark.MarkedAt))
	}
	expr, err := expression.NewBuilder().WithCondition(cond).Build()
	if err != nil {
		return nil, fmt.Errorf("build clear-pending condition: %w", err)
	}
	return &dynamodb.DeleteItemInput{
		TableName: aws.String(tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: model.PKUpdatePending},
			"SK": &types.AttributeValueMemberS{Value: mark.UserID},
		},
		ConditionExpression: expr.Condition(), ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(),
	}, nil
}

// ClearPending deletes the marker read by ListPendingUsers. A marker re-marked
// (or already removed) since that read returns ErrConditionFailed.
func (r *CustomerUpdateRepository) ClearPending(ctx context.Context, mark model.UpdatePendingMark) error {
	in, err := pendingClearInput(r.tableName, mark)
	if err != nil {
		return err
	}
	if _, err := r.client.DeleteItem(ctx, in); err != nil {
		if isConditionFailed(err) {
			return fmt.Errorf("%w: pending marker changed", ErrConditionFailed)
		}
		return fmt.Errorf("failed to clear pending marker: %w", err)
	}
	return nil
}

// ---- Items ----

// newItemsQueryInput builds one page of the scan of the user's item rows,
// filtered to state = new. startAfter ("" = from the first row) is the
// exclusive start key; upTo ("" = to the end of the prefix) is an inclusive
// upper bound, used by the wrap-around leg.
func newItemsQueryInput(tableName, userID, startAfter, upTo string, limit int) (*dynamodb.QueryInput, error) {
	skCond := expression.Key("SK").BeginsWith(model.PrefixUpdateItem)
	if upTo != "" {
		skCond = expression.Key("SK").Between(expression.Value(model.PrefixUpdateItem), expression.Value(upTo))
	}
	expr, err := expression.NewBuilder().
		WithKeyCondition(expression.Key("PK").Equal(expression.Value(model.PrefixUser + userID)).And(skCond)).
		WithFilter(expression.Name("state").Equal(expression.Value(model.UpdateItemNew))).Build()
	if err != nil {
		return nil, fmt.Errorf("build new-items expression: %w", err)
	}
	in := &dynamodb.QueryInput{
		TableName:                 aws.String(tableName),
		KeyConditionExpression:    expr.KeyCondition(),
		FilterExpression:          expr.Filter(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
		Limit:                     aws.Int32(int32(limit)),
		// Strongly consistent: the pending mark is cleared after a complete scan
		// that found nothing, so a stale page must not hide a just-written item.
		ConsistentRead: aws.Bool(true),
	}
	if startAfter != "" {
		in.ExclusiveStartKey = updateUserKey(userID, startAfter)
	}
	return in, nil
}

// itemScanPage is one page of the item scan: the rows that passed the filter,
// how many rows were evaluated, and the last evaluated sort key ("" when the
// leg is exhausted).
type itemScanPage struct {
	Rows    []updateItemRow
	Scanned int
	LastSK  string
}

// itemPageFunc reads one page after startAfter, bounded by upTo and limit.
type itemPageFunc func(ctx context.Context, startAfter, upTo string, limit int) (itemScanPage, error)

// validItemCursor reports whether a stored cursor is an item sort key.
func validItemCursor(c string) bool {
	return strings.HasPrefix(c, model.PrefixUpdateItem) && urlHashPattern.MatchString(strings.TrimPrefix(c, model.PrefixUpdateItem))
}

// scanNewItems walks the item rows from just after cursor to the end of the
// prefix, then wraps once from the first row up to and including cursor, so
// one call never evaluates a row twice. It stops at want collected new,
// unexpired rows, at maxScan evaluated rows, or after a full cycle, and returns
// the collected rows in key order plus the position to resume from. When it
// stops at want, the position is the last collected row, so later rows of the
// same page are evaluated again by the next call rather than skipped.
//
// A new item whose heldUntil is after now is not collected; it is counted in
// held, so held items never take a slot from eligible ones.
//
// complete is true only when this call evaluated every item row of the prefix
// (a full cycle): it did not stop at want or at maxScan. A partition of
// maxScan rows or more is therefore never complete in one call.
func scanNewItems(ctx context.Context, page itemPageFunc, cursor string, want, maxScan int, now time.Time) (rows []updateItemRow, pos string, held int, complete bool, err error) {
	type leg struct{ startAfter, upTo, end string }
	legs := []leg{{startAfter: cursor}}
	if cursor != "" {
		legs = append(legs, leg{upTo: cursor, end: cursor})
	}
	var out []updateItemRow
	scanned := 0
	pos = cursor
	for li, l := range legs {
		start := l.startAfter
		for {
			p, err := page(ctx, start, l.upTo, maxScan-scanned)
			if err != nil {
				return nil, "", 0, false, err
			}
			scanned += p.Scanned
			for _, row := range p.Rows {
				if row.State != model.UpdateItemNew || (row.ExpiresAt != 0 && row.ExpiresAt <= now.Unix()) {
					continue
				}
				if row.HeldUntil.After(now) {
					held++
					continue
				}
				out = append(out, row)
				if len(out) == want {
					return out, row.SK, held, false, nil
				}
			}
			pos = p.LastSK
			if pos == "" {
				pos = l.end
			}
			if p.LastSK == "" && li == len(legs)-1 {
				return out, pos, held, true, nil
			}
			if scanned >= maxScan {
				return out, pos, held, false, nil
			}
			if p.LastSK == "" {
				break
			}
			start = p.LastSK
		}
	}
	return out, pos, held, true, nil
}

func itemCursorKey(userID string) map[string]types.AttributeValue {
	return updateUserKey(userID, model.SKUpdateItemCursor)
}

// itemCursorUpdateInput saves the scan position with an unconditional
// attribute-level update; a lost write only repeats a window.
func itemCursorUpdateInput(tableName, userID, cursor string, now time.Time) (*dynamodb.UpdateItemInput, error) {
	expr, err := expression.NewBuilder().WithUpdate(
		expression.Set(expression.Name("itemCursor"), expression.Value(cursor)).
			Set(expression.Name("entityType"), expression.Value("UPDATE_ITEM_CURSOR")).
			Set(expression.Name(customerUpdateTTLAttribute), expression.Value(now.Add(model.UpdateItemCursorTTL).Unix()))).Build()
	if err != nil {
		return nil, fmt.Errorf("build item-cursor expression: %w", err)
	}
	return &dynamodb.UpdateItemInput{
		TableName: aws.String(tableName), Key: itemCursorKey(userID),
		UpdateExpression: expr.Update(), ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(),
	}, nil
}

// cursorFromItem returns the stored cursor, or "" when absent or malformed.
func cursorFromItem(item map[string]types.AttributeValue) string {
	c, ok := stringAttr(item, "itemCursor")
	if !ok || !validItemCursor(c) {
		return ""
	}
	return c
}

// oldestNewItems keeps unexpired new items, orders them by first-seen time
// then urlHash, and returns at most limit.
func oldestNewItems(rows []model.UpdateItem, now time.Time, limit int) []model.UpdateItem {
	out := []model.UpdateItem{}
	for _, it := range rows {
		if it.State == model.UpdateItemNew && (it.ExpiresAt == 0 || it.ExpiresAt > now.Unix()) {
			out = append(out, it)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].FirstSeenAt.Equal(out[j].FirstSeenAt) {
			return out[i].FirstSeenAt.Before(out[j].FirstSeenAt)
		}
		return out[i].URLHash < out[j].URLHash
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// ListNewItems returns up to limit new items ordered by first-seen time then
// urlHash. Item sort keys are urlHash-ordered, so one call evaluates at most
// maxNewItemsScan rows starting after a per-user cursor (USER#{u} / CUCURSOR),
// wrapping once around the prefix, and saves where it stopped. Successive
// calls therefore visit every item row, and a new item beyond one call's
// window is returned by a later call. Ordering holds within the returned
// window only; the result is not globally oldest-first. Held items (see
// ListEligibleItems) are left out.
func (r *CustomerUpdateRepository) ListNewItems(ctx context.Context, userID string, limit int) ([]model.UpdateItem, error) {
	items, _, _, err := r.ListEligibleItems(ctx, userID, limit)
	return items, err
}

// ListEligibleItems is ListNewItems that also returns how many new items the
// scan passed over because their heldUntil is after now, and whether this
// call evaluated the whole item prefix (a full cycle; see scanNewItems). Held
// items do not count toward limit, so they never crowd out eligible ones.
func (r *CustomerUpdateRepository) ListEligibleItems(ctx context.Context, userID string, limit int) ([]model.UpdateItem, int, bool, error) {
	if limit < 1 {
		return nil, 0, false, fmt.Errorf("item limit must be positive")
	}
	res, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName), Key: itemCursorKey(userID), ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return nil, 0, false, fmt.Errorf("failed to get item cursor: %w", err)
	}
	cursor := cursorFromItem(res.Item)
	page := func(ctx context.Context, startAfter, upTo string, n int) (itemScanPage, error) {
		in, err := newItemsQueryInput(r.tableName, userID, startAfter, upTo, n)
		if err != nil {
			return itemScanPage{}, err
		}
		out, err := r.client.Query(ctx, in)
		if err != nil {
			return itemScanPage{}, fmt.Errorf("failed to query update items: %w", err)
		}
		p := itemScanPage{Scanned: int(out.ScannedCount)}
		if err := attributevalue.UnmarshalListOfMaps(out.Items, &p.Rows); err != nil {
			return itemScanPage{}, fmt.Errorf("failed to unmarshal update items: %w", err)
		}
		if sk, ok := stringAttr(out.LastEvaluatedKey, "SK"); ok {
			p.LastSK = sk
		}
		return p, nil
	}
	now := r.clock()
	rows, pos, held, complete, err := scanNewItems(ctx, page, cursor, limit, maxNewItemsScan, now)
	if err != nil {
		return nil, 0, false, err
	}
	if pos != cursor {
		in, err := itemCursorUpdateInput(r.tableName, userID, pos, now)
		if err != nil {
			return nil, 0, false, err
		}
		if _, err := r.client.UpdateItem(ctx, in); err != nil {
			return nil, 0, false, fmt.Errorf("failed to save item cursor: %w", err)
		}
	}
	items := make([]model.UpdateItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, row.UpdateItem)
	}
	return oldestNewItems(items, now, limit), held, complete, nil
}

func itemHoldUpdateInput(tableName, userID, urlHash string, until time.Time) (*dynamodb.UpdateItemInput, error) {
	if userID == "" || !urlHashPattern.MatchString(urlHash) {
		return nil, invalidRun("item key")
	}
	var update expression.UpdateBuilder
	if until.IsZero() {
		update = expression.Remove(expression.Name("heldUntil"))
	} else {
		update = expression.Set(expression.Name("heldUntil"), expression.Value(until.UTC().Format(time.RFC3339Nano)))
	}
	expr, err := expression.NewBuilder().WithUpdate(update).
		WithCondition(expression.AttributeExists(expression.Name("PK"))).Build()
	if err != nil {
		return nil, fmt.Errorf("build item-hold expression: %w", err)
	}
	return &dynamodb.UpdateItemInput{
		TableName: aws.String(tableName), Key: updateUserKey(userID, model.UpdateItemSK(urlHash)),
		UpdateExpression: expr.Update(), ConditionExpression: expr.Condition(),
		ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(),
	}, nil
}

// SetItemHold sets (or, with a zero until, clears) an existing item's
// heldUntil without touching its state or attempts; a missing item returns
// ErrConditionFailed.
func (r *CustomerUpdateRepository) SetItemHold(ctx context.Context, userID, urlHash string, until time.Time) error {
	in, err := itemHoldUpdateInput(r.tableName, userID, urlHash, until)
	if err != nil {
		return err
	}
	if _, err := r.client.UpdateItem(ctx, in); err != nil {
		if isConditionFailed(err) {
			return fmt.Errorf("%w: update item not found", ErrConditionFailed)
		}
		return fmt.Errorf("failed to set update item hold: %w", err)
	}
	return nil
}

func itemStateUpdateInput(tableName, userID, urlHash, state string, attemptsDelta int) (*dynamodb.UpdateItemInput, error) {
	if !urlHashPattern.MatchString(urlHash) {
		return nil, invalidRun("urlHash")
	}
	switch state {
	case model.UpdateItemNew, model.UpdateItemDecided, model.UpdateItemSkipped:
	default:
		return nil, invalidRun("item state")
	}
	if attemptsDelta < 0 {
		return nil, invalidRun("attempts delta")
	}
	update := expression.Set(expression.Name("state"), expression.Value(state))
	if attemptsDelta > 0 {
		update = update.Add(expression.Name("attempts"), expression.Value(attemptsDelta))
	}
	if state != model.UpdateItemNew {
		// Only a new item can be held.
		update = update.Remove(expression.Name("heldUntil"))
	}
	expr, err := expression.NewBuilder().WithUpdate(update).
		WithCondition(expression.AttributeExists(expression.Name("PK"))).Build()
	if err != nil {
		return nil, fmt.Errorf("build item-state expression: %w", err)
	}
	return &dynamodb.UpdateItemInput{
		TableName: aws.String(tableName), Key: updateUserKey(userID, model.UpdateItemSK(urlHash)),
		UpdateExpression: expr.Update(), ConditionExpression: expr.Condition(),
		ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(),
	}, nil
}

// SetItemState sets an existing item's state and adds attemptsDelta to its
// attempts counter; a missing item returns ErrConditionFailed.
func (r *CustomerUpdateRepository) SetItemState(ctx context.Context, userID, urlHash, state string, attemptsDelta int) error {
	in, err := itemStateUpdateInput(r.tableName, userID, urlHash, state, attemptsDelta)
	if err != nil {
		return err
	}
	if _, err := r.client.UpdateItem(ctx, in); err != nil {
		if isConditionFailed(err) {
			return fmt.Errorf("%w: update item not found", ErrConditionFailed)
		}
		return fmt.Errorf("failed to set update item state: %w", err)
	}
	return nil
}

// ---- Run lease ----

type storedUpdateRun struct {
	customerUpdateKeys
	model.UpdateRun
	ExpiresAt int64 `dynamodbav:"pendingShareExpiresAt"`
}

func validateRun(userID string, run *model.UpdateRun) error {
	if userID == "" {
		return invalidRun("user")
	}
	if run == nil || !runIDPattern.MatchString(run.RunID) {
		return invalidRun("runId")
	}
	if run.LeaseUntil.IsZero() {
		return invalidRun("leaseUntil")
	}
	if len(run.ItemHashes) > model.MaxUpdateRunItems || len(run.AccountIDs) > model.MaxUpdateRunAccounts {
		return invalidRun("run size")
	}
	for _, h := range run.ItemHashes {
		if !urlHashPattern.MatchString(h) {
			return invalidRun("urlHash")
		}
	}
	for _, a := range run.AccountIDs {
		if !accountIDPattern.MatchString(a) {
			return invalidRun("accountId")
		}
	}
	return nil
}

func runLeasePutInput(tableName, userID string, run *model.UpdateRun) (*dynamodb.PutItemInput, error) {
	if err := validateRun(userID, run); err != nil {
		return nil, err
	}
	row := storedUpdateRun{
		customerUpdateKeys: customerUpdateKeys{PK: model.PrefixUser + userID, SK: model.SKUpdateRun, EntityType: "UPDATE_RUN"},
		UpdateRun:          *run,
		ExpiresAt:          run.LeaseUntil.Add(model.UpdateRunLeaseTail).Unix(),
	}
	row.ItemHashes = append([]string{}, run.ItemHashes...)
	row.AccountIDs = append([]string{}, run.AccountIDs...)
	av, err := attributevalue.MarshalMap(row)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal update run: %w", err)
	}
	cond, err := absentCondition()
	if err != nil {
		return nil, fmt.Errorf("build create-run condition: %w", err)
	}
	return &dynamodb.PutItemInput{
		TableName: aws.String(tableName), Item: av,
		ConditionExpression: cond.Condition(), ExpressionAttributeNames: cond.Names(),
	}, nil
}

// CreateRunIfAbsent creates the user's run lease; any existing lease, live or
// stale, returns ErrConditionFailed.
func (r *CustomerUpdateRepository) CreateRunIfAbsent(ctx context.Context, userID string, run *model.UpdateRun) error {
	in, err := runLeasePutInput(r.tableName, userID, run)
	if err != nil {
		return err
	}
	if _, err := r.client.PutItem(ctx, in); err != nil {
		if isConditionFailed(err) {
			return fmt.Errorf("%w: update run already exists", ErrConditionFailed)
		}
		return fmt.Errorf("failed to create update run: %w", err)
	}
	return nil
}

func getRunInput(tableName, userID string) *dynamodb.GetItemInput {
	return &dynamodb.GetItemInput{
		TableName: aws.String(tableName), Key: updateUserKey(userID, model.SKUpdateRun), ConsistentRead: aws.Bool(true),
	}
}

func runFromItem(item map[string]types.AttributeValue) (*model.UpdateRun, error) {
	var row storedUpdateRun
	if err := attributevalue.UnmarshalMap(item, &row); err != nil {
		return nil, fmt.Errorf("failed to unmarshal update run: %w", err)
	}
	return &row.UpdateRun, nil
}

// GetRun returns the user's run lease with a consistent read, or nil.
func (r *CustomerUpdateRepository) GetRun(ctx context.Context, userID string) (*model.UpdateRun, error) {
	res, err := r.client.GetItem(ctx, getRunInput(r.tableName, userID))
	if err != nil {
		return nil, fmt.Errorf("failed to get update run: %w", err)
	}
	if len(res.Item) == 0 {
		return nil, nil
	}
	return runFromItem(res.Item)
}

func runDeleteIfMatchInput(tableName, userID, runID string) (*dynamodb.DeleteItemInput, error) {
	if !runIDPattern.MatchString(runID) {
		return nil, invalidRun("runId")
	}
	expr, err := expression.NewBuilder().
		WithCondition(expression.Name("runId").Equal(expression.Value(runID))).Build()
	if err != nil {
		return nil, fmt.Errorf("build delete-run condition: %w", err)
	}
	return &dynamodb.DeleteItemInput{
		TableName: aws.String(tableName), Key: updateUserKey(userID, model.SKUpdateRun),
		ConditionExpression: expr.Condition(), ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(),
	}, nil
}

// DeleteRunIfMatch deletes the lease only while it names runID; otherwise it
// returns ErrConditionFailed.
func (r *CustomerUpdateRepository) DeleteRunIfMatch(ctx context.Context, userID, runID string) error {
	in, err := runDeleteIfMatchInput(r.tableName, userID, runID)
	if err != nil {
		return err
	}
	if _, err := r.client.DeleteItem(ctx, in); err != nil {
		if isConditionFailed(err) {
			return fmt.Errorf("%w: update run changed", ErrConditionFailed)
		}
		return fmt.Errorf("failed to delete update run: %w", err)
	}
	return nil
}

// ---- Day counters ----

// dayDraftUpdateInput builds one conditional UpdateItem that adds 1 to the
// day total and to a flat per-account attribute (acct#{accountId}). ADD
// creates a missing row and missing attributes, so the first draft of a day
// or account needs no prior write and no read-modify-write; the condition
// admits the increment only while both counters are below their limits.
func dayDraftUpdateInput(tableName, userID, day, accountID string, maxTotal, maxPerAccount int) (*dynamodb.UpdateItemInput, error) {
	if !dayPattern.MatchString(day) {
		return nil, invalidRun("day")
	}
	start, err := time.ParseInLocation("20060102", day, kst)
	if err != nil {
		return nil, invalidRun("day")
	}
	if !accountIDPattern.MatchString(accountID) {
		return nil, invalidRun("accountId")
	}
	if maxTotal < 1 || maxPerAccount < 1 {
		// A zero limit admits no drafts; answer as a limit hit without writing.
		return nil, fmt.Errorf("%w: day draft limit is zero", ErrConditionFailed)
	}
	total, acct := expression.Name("total"), expression.Name(dayAccountAttrPfx+accountID)
	update := expression.Add(total, expression.Value(1)).
		Add(acct, expression.Value(1)).
		Set(expression.Name("entityType"), expression.Value("UPDATE_DAY")).
		Set(expression.Name(customerUpdateTTLAttribute), expression.Value(start.Add(model.UpdateDayTTL).Unix()))
	cond := expression.AttributeNotExists(total).Or(total.LessThan(expression.Value(maxTotal))).
		And(expression.AttributeNotExists(acct).Or(acct.LessThan(expression.Value(maxPerAccount))))
	expr, err := expression.NewBuilder().WithUpdate(update).WithCondition(cond).Build()
	if err != nil {
		return nil, fmt.Errorf("build day-draft expression: %w", err)
	}
	return &dynamodb.UpdateItemInput{
		TableName: aws.String(tableName), Key: updateUserKey(userID, model.UpdateDaySK(day)),
		UpdateExpression: expr.Update(), ConditionExpression: expr.Condition(),
		ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(),
	}, nil
}

// AddDayDraft counts one accepted draft for the day; a counter at its limit
// returns ErrConditionFailed and changes nothing.
func (r *CustomerUpdateRepository) AddDayDraft(ctx context.Context, userID, day, accountID string, maxTotal, maxPerAccount int) error {
	in, err := dayDraftUpdateInput(r.tableName, userID, day, accountID, maxTotal, maxPerAccount)
	if err != nil {
		return err
	}
	if _, err := r.client.UpdateItem(ctx, in); err != nil {
		if isConditionFailed(err) {
			return fmt.Errorf("%w: day draft limit reached", ErrConditionFailed)
		}
		return fmt.Errorf("failed to add day draft: %w", err)
	}
	return nil
}

// dayResearchedUpdateInput adds n researched pairs to the day row (created by
// ADD when absent), with the same entity type and TTL as the draft counters.
func dayResearchedUpdateInput(tableName, userID, day string, n int) (*dynamodb.UpdateItemInput, error) {
	start, err := time.ParseInLocation("20060102", day, kst)
	if !dayPattern.MatchString(day) || err != nil {
		return nil, invalidRun("day")
	}
	if userID == "" || n < 1 {
		return nil, invalidRun("researched count")
	}
	expr, err := expression.NewBuilder().WithUpdate(
		expression.Add(expression.Name(dayResearchedAttr), expression.Value(n)).
			Set(expression.Name("entityType"), expression.Value("UPDATE_DAY")).
			Set(expression.Name(customerUpdateTTLAttribute), expression.Value(start.Add(model.UpdateDayTTL).Unix()))).Build()
	if err != nil {
		return nil, fmt.Errorf("build day-researched expression: %w", err)
	}
	return &dynamodb.UpdateItemInput{
		TableName: aws.String(tableName), Key: updateUserKey(userID, model.UpdateDaySK(day)),
		UpdateExpression: expr.Update(), ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(),
	}, nil
}

// AddDayResearched counts n researched pairs for the day (unconditional ADD).
func (r *CustomerUpdateRepository) AddDayResearched(ctx context.Context, userID, day string, n int) error {
	in, err := dayResearchedUpdateInput(r.tableName, userID, day, n)
	if err != nil {
		return err
	}
	if _, err := r.client.UpdateItem(ctx, in); err != nil {
		return fmt.Errorf("failed to add day researched: %w", err)
	}
	return nil
}

func dayFromItem(item map[string]types.AttributeValue) *model.UpdateDay {
	day := &model.UpdateDay{PerAccount: map[string]int{}}
	for name, v := range item {
		n, ok := v.(*types.AttributeValueMemberN)
		if !ok {
			continue
		}
		count, err := strconv.Atoi(n.Value)
		if err != nil {
			continue
		}
		switch {
		case name == "total":
			day.Total = count
		case name == dayResearchedAttr:
			day.Researched = count
		case strings.HasPrefix(name, dayAccountAttrPfx):
			day.PerAccount[strings.TrimPrefix(name, dayAccountAttrPfx)] = count
		}
	}
	return day
}

// GetDay returns the day's counters with a consistent read; an absent row is
// a zero day.
func (r *CustomerUpdateRepository) GetDay(ctx context.Context, userID, day string) (*model.UpdateDay, error) {
	if !dayPattern.MatchString(day) {
		return nil, invalidRun("day")
	}
	res, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName), Key: updateUserKey(userID, model.UpdateDaySK(day)), ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get update day: %w", err)
	}
	return dayFromItem(res.Item), nil
}

// ---- Run result rows ----

func runRowBinding(runID, sk string) map[string]string {
	return map[string]string{"purpose": "ttobak-customer-update-run", "runId": runID, "sk": sk}
}

func runRowsQueryInput(tableName, runID string) (*dynamodb.QueryInput, error) {
	if !runIDPattern.MatchString(runID) {
		return nil, invalidRun("runId")
	}
	expr, err := expression.NewBuilder().WithKeyCondition(
		expression.Key("PK").Equal(expression.Value(model.UpdateRunPK(runID)))).Build()
	if err != nil {
		return nil, fmt.Errorf("failed to build expression: %w", err)
	}
	return &dynamodb.QueryInput{
		TableName: aws.String(tableName), ConsistentRead: aws.Bool(true),
		KeyConditionExpression: expr.KeyCondition(), ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(),
	}, nil
}

func runRowKeysQueryInput(tableName, runID string) (*dynamodb.QueryInput, error) {
	if !runIDPattern.MatchString(runID) {
		return nil, invalidRun("runId")
	}
	expr, err := expression.NewBuilder().
		WithKeyCondition(expression.Key("PK").Equal(expression.Value(model.UpdateRunPK(runID)))).
		WithProjection(expression.NamesList(expression.Name("PK"), expression.Name("SK"))).Build()
	if err != nil {
		return nil, fmt.Errorf("failed to build expression: %w", err)
	}
	return &dynamodb.QueryInput{
		TableName: aws.String(tableName), ConsistentRead: aws.Bool(true),
		KeyConditionExpression: expr.KeyCondition(), ProjectionExpression: expr.Projection(),
		ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(),
	}, nil
}

// ReadRunRows reads and classifies a run's whole result partition. A missing
// key fails closed before any read; more than MaxUpdateRunRows rows returns
// ErrUpdateRunTooLarge.
func (r *CustomerUpdateRepository) ReadRunRows(ctx context.Context, runID string) (*model.RunRows, error) {
	if r.sealer == nil {
		return nil, ErrUpdateDraftEncryptionUnavailable
	}
	input, err := runRowsQueryInput(r.tableName, runID)
	if err != nil {
		return nil, err
	}
	var items []map[string]types.AttributeValue
	for {
		res, err := r.client.Query(ctx, input)
		if err != nil {
			return nil, fmt.Errorf("failed to query run rows: %w", err)
		}
		items = append(items, res.Items...)
		if len(items) > model.MaxUpdateRunRows {
			return nil, ErrUpdateRunTooLarge
		}
		if len(res.LastEvaluatedKey) == 0 {
			break
		}
		input.ExclusiveStartKey = res.LastEvaluatedKey
	}
	return classifyRunRows(ctx, r.sealer, runID, items)
}

// rowFault classifies a skipped row.
type rowFault int

const (
	rowOK rowFault = iota
	rowMalformed
	rowUnreadable
)

// classifyRunRows sorts rows by sort key kind. Bad rows are counted and
// skipped; only a missing key or a sealer transport failure is an error.
func classifyRunRows(ctx context.Context, s sealing.Sealer, runID string, items []map[string]types.AttributeValue) (*model.RunRows, error) {
	if s == nil {
		return nil, ErrUpdateDraftEncryptionUnavailable
	}
	out := &model.RunRows{Items: []model.RunItemRow{}, Decisions: []model.RunDecisionRow{}, Drafts: []model.RunDraftRow{}}
	for _, item := range items {
		fault, err := classifyRunRow(ctx, s, runID, item, out)
		if err != nil {
			return nil, err
		}
		switch fault {
		case rowMalformed:
			out.Malformed++
		case rowUnreadable:
			out.Unreadable++
		}
	}
	return out, nil
}

func classifyRunRow(ctx context.Context, s sealing.Sealer, runID string, item map[string]types.AttributeValue, out *model.RunRows) (rowFault, error) {
	sk, ok := stringAttr(item, "SK")
	if !ok {
		return rowMalformed, nil
	}
	switch {
	case sk == model.SKUpdateRunClaim:
		return rowOK, nil
	case sk == model.SKUpdateRunDone:
		at, ok := stringAttr(item, "finishedAt")
		if !ok {
			return rowMalformed, nil
		}
		finished, err := time.Parse(time.RFC3339, at)
		if err != nil {
			return rowMalformed, nil
		}
		out.Done, out.FinishedAt = true, finished.UTC()
		return rowOK, nil
	case strings.HasPrefix(sk, model.PrefixRunItemSK):
		return classifyItemRow(strings.TrimPrefix(sk, model.PrefixRunItemSK), item, out), nil
	case strings.HasPrefix(sk, model.PrefixRunDecSK):
		return classifyDecisionRow(ctx, s, runID, sk, item, out)
	case strings.HasPrefix(sk, model.PrefixRunDraftSK):
		return classifyDraftRow(ctx, s, runID, sk, item, out)
	}
	return rowMalformed, nil
}

func stringAttr(item map[string]types.AttributeValue, name string) (string, bool) {
	v, ok := item[name].(*types.AttributeValueMemberS)
	if !ok {
		return "", false
	}
	return v.Value, true
}

func classifyItemRow(urlHash string, item map[string]types.AttributeValue, out *model.RunRows) rowFault {
	state, ok1 := stringAttr(item, "state")
	reason, ok2 := stringAttr(item, "reason")
	if !urlHashPattern.MatchString(urlHash) || !ok1 || !ok2 {
		return rowMalformed
	}
	switch state {
	case "decided", "deferred", "skipped":
	default:
		return rowMalformed
	}
	out.Items = append(out.Items, model.RunItemRow{URLHash: urlHash, State: state, Reason: reason})
	return rowOK
}

// splitPair splits "{a}#{b}" at the first '#'; neither part may be empty.
func splitPair(s string) (string, string, bool) {
	a, b, ok := strings.Cut(s, "#")
	return a, b, ok && a != "" && b != ""
}

type sealedRunDecision struct {
	Reason    string                 `json:"reason"`
	GateInput *model.UpdateGateInput `json:"gateInput"`
}

func classifyDecisionRow(ctx context.Context, s sealing.Sealer, runID, sk string, item map[string]types.AttributeValue, out *model.RunRows) (rowFault, error) {
	urlHash, accountID, ok := splitPair(strings.TrimPrefix(sk, model.PrefixRunDecSK))
	if !ok || !urlHashPattern.MatchString(urlHash) || !accountIDPattern.MatchString(accountID) {
		return rowMalformed, nil
	}
	choice, ok1 := stringAttr(item, "choice")
	backend, ok2 := stringAttr(item, "backend")
	outcome, ok3 := stringAttr(item, "outcome")
	discardReason, ok4 := stringAttr(item, "discardReason")
	scoreAttr, ok5 := item["score"].(*types.AttributeValueMemberN)
	if !ok1 || !ok2 || !ok3 || !ok4 || !ok5 {
		return rowMalformed, nil
	}
	score, err := strconv.ParseFloat(scoreAttr.Value, 64)
	if err != nil || math.IsNaN(score) || math.IsInf(score, 0) {
		return rowMalformed, nil
	}
	if choice != model.UpdateChoiceNeeded && choice != model.UpdateChoiceNotNeeded || !validOutcome(outcome) {
		return rowMalformed, nil
	}
	var payload sealedRunDecision
	fault, err := openRunPayload(ctx, s, runID, sk, item, &payload)
	if fault != rowOK || err != nil {
		return fault, err
	}
	if choice != model.UpdateChoiceNeeded {
		payload.GateInput = nil
	} else if payload.GateInput != nil && len(payload.GateInput.Options) > maxRunGateOptions {
		return rowMalformed, nil
	}
	out.Decisions = append(out.Decisions, model.RunDecisionRow{
		URLHash: urlHash, AccountID: accountID, Choice: choice, Score: score, Backend: backend,
		Outcome: outcome, DiscardReason: discardReason, Reason: payload.Reason, GateInput: payload.GateInput,
	})
	return rowOK, nil
}

type sealedRunDraft struct {
	SourceURL      string   `json:"sourceUrl"`
	SourceTitle    string   `json:"sourceTitle"`
	Why            string   `json:"why"`
	Body           string   `json:"body"`
	Links          []string `json:"links"`
	OpportunityIDs []string `json:"opportunityIds"`
	FetchedURLs    []string `json:"fetchedUrls"`
}

func classifyDraftRow(ctx context.Context, s sealing.Sealer, runID, sk string, item map[string]types.AttributeValue, out *model.RunRows) (rowFault, error) {
	accountID, urlHash, ok := splitPair(strings.TrimPrefix(sk, model.PrefixRunDraftSK))
	if !ok || !accountIDPattern.MatchString(accountID) || !urlHashPattern.MatchString(urlHash) {
		return rowMalformed, nil
	}
	var p sealedRunDraft
	fault, err := openRunPayload(ctx, s, runID, sk, item, &p)
	if fault != rowOK || err != nil {
		return fault, err
	}
	if len(p.Links) > maxRunDraftLinks || len(p.FetchedURLs) > maxRunDraftFetchedURLs || len(p.OpportunityIDs) > maxRunDraftOpptyIDs {
		return rowMalformed, nil
	}
	out.Drafts = append(out.Drafts, model.RunDraftRow{
		AccountID: accountID, URLHash: urlHash, SourceURL: p.SourceURL, SourceTitle: p.SourceTitle, Why: p.Why, Body: p.Body,
		Links: nonNil(p.Links), OpportunityIDs: nonNil(p.OpportunityIDs), FetchedURLs: nonNil(p.FetchedURLs),
	})
	return rowOK, nil
}

// openRunPayload opens a row's sealed JSON with the run binding. A payload
// over 64 KiB is malformed; one that does not open, is not valid UTF-8,
// carries a lone surrogate escape or is not the expected JSON is unreadable.
// A missing key or a KMS transport failure is returned as an error.
func openRunPayload(ctx context.Context, s sealing.Sealer, runID, sk string, item map[string]types.AttributeValue, dst any) (rowFault, error) {
	sealed, ok := stringAttr(item, "sealed")
	if !ok || sealed == "" {
		return rowUnreadable, nil
	}
	plain, err := s.Open(ctx, sealed, runRowBinding(runID, sk))
	if err != nil {
		if errors.Is(err, sealing.ErrInvalid) {
			return rowUnreadable, nil
		}
		return rowOK, sealingError(err)
	}
	if len(plain) > maxRunPayloadBytes {
		return rowMalformed, nil
	}
	if !utf8.Valid(plain) || hasLoneSurrogateEscape(plain) {
		return rowUnreadable, nil
	}
	// Exactly one JSON value: anything after it (even a stray "}") makes the
	// second Decode return something other than io.EOF. Field names match
	// case-insensitively, as encoding/json does; the payload is authenticated
	// by the envelope, so that leniency admits nothing an attacker controls.
	dec := json.NewDecoder(bytes.NewReader(plain))
	if err := dec.Decode(dst); err != nil {
		return rowUnreadable, nil
	}
	if err := dec.Decode(&json.RawMessage{}); err != io.EOF {
		return rowUnreadable, nil
	}
	return rowOK, nil
}

// hasLoneSurrogateEscape reports whether JSON text contains a \uD800-\uDFFF
// escape that is not a high surrogate immediately followed by a low one.
// encoding/json would otherwise replace it silently with U+FFFD. Backslashes
// occur only inside JSON strings, so a linear escape walk is exact.
func hasLoneSurrogateEscape(b []byte) bool {
	for i := 0; i < len(b); i++ {
		if b[i] != '\\' {
			continue
		}
		if i+1 >= len(b) {
			return false
		}
		if b[i+1] != 'u' {
			i++ // skip the escaped character
			continue
		}
		r, ok := hex4(b, i+2)
		if !ok {
			i++
			continue
		}
		switch {
		case r >= 0xDC00 && r <= 0xDFFF:
			return true
		case r >= 0xD800 && r <= 0xDBFF:
			if i+11 < len(b) && b[i+6] == '\\' && b[i+7] == 'u' {
				if lo, ok := hex4(b, i+8); ok && lo >= 0xDC00 && lo <= 0xDFFF {
					i += 11
					continue
				}
			}
			return true
		}
		i += 5
	}
	return false
}

func hex4(b []byte, at int) (int, bool) {
	if at+4 > len(b) {
		return 0, false
	}
	v, err := strconv.ParseUint(string(b[at:at+4]), 16, 16)
	if err != nil {
		return 0, false
	}
	return int(v), true
}

func runDeleteBatches(tableName string, keys []map[string]types.AttributeValue) []*dynamodb.BatchWriteItemInput {
	var out []*dynamodb.BatchWriteItemInput
	for start := 0; start < len(keys); start += runDeleteBatchSize {
		end := min(start+runDeleteBatchSize, len(keys))
		reqs := make([]types.WriteRequest, 0, end-start)
		for _, k := range keys[start:end] {
			reqs = append(reqs, types.WriteRequest{DeleteRequest: &types.DeleteRequest{Key: k}})
		}
		out = append(out, &dynamodb.BatchWriteItemInput{RequestItems: map[string][]types.WriteRequest{tableName: reqs}})
	}
	return out
}

// DeleteRunRows deletes a run's result rows, at most MaxUpdateRunRows of them
// (the rest keep their 2-day TTL; ErrUpdateRunTooLarge is returned after the
// bounded delete). Deleting absent rows is a no-op, so a repeat call is safe.
func (r *CustomerUpdateRepository) DeleteRunRows(ctx context.Context, runID string) error {
	input, err := runRowKeysQueryInput(r.tableName, runID)
	if err != nil {
		return err
	}
	var keys []map[string]types.AttributeValue
	tooLarge := false
	for {
		res, err := r.client.Query(ctx, input)
		if err != nil {
			return fmt.Errorf("failed to query run rows: %w", err)
		}
		for _, item := range res.Items {
			if len(keys) == model.MaxUpdateRunRows {
				tooLarge = true
				break
			}
			keys = append(keys, map[string]types.AttributeValue{"PK": item["PK"], "SK": item["SK"]})
		}
		if tooLarge || len(res.LastEvaluatedKey) == 0 {
			break
		}
		input.ExclusiveStartKey = res.LastEvaluatedKey
	}
	for _, batch := range runDeleteBatches(r.tableName, keys) {
		if err := r.batchDeleteWithRetry(ctx, batch); err != nil {
			return err
		}
	}
	if tooLarge {
		return ErrUpdateRunTooLarge
	}
	return nil
}

func (r *CustomerUpdateRepository) batchDeleteWithRetry(ctx context.Context, in *dynamodb.BatchWriteItemInput) error {
	for attempt := 0; ; attempt++ {
		res, err := r.client.BatchWriteItem(ctx, in)
		if err != nil {
			return fmt.Errorf("failed to delete run rows: %w", err)
		}
		if len(res.UnprocessedItems) == 0 {
			return nil
		}
		if attempt == runDeleteMaxRetries {
			return fmt.Errorf("failed to delete run rows: unprocessed after %d retries", runDeleteMaxRetries)
		}
		in = &dynamodb.BatchWriteItemInput{RequestItems: res.UnprocessedItems}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(50<<attempt) * time.Millisecond):
		}
	}
}

// ---- Decisions ----

// storedUpdateDecision is the at-rest form: keys, codes and the score stay
// plaintext; the reason and gate input are sealed.
type storedUpdateDecision struct {
	customerUpdateKeys
	URLHash       string    `dynamodbav:"urlHash"`
	AccountID     string    `dynamodbav:"accountId"`
	Choice        string    `dynamodbav:"choice"`
	Score         float64   `dynamodbav:"score"`
	Backend       string    `dynamodbav:"backend"`
	RunID         string    `dynamodbav:"runId"`
	Outcome       string    `dynamodbav:"outcome"`
	DiscardReason string    `dynamodbav:"discardReason"`
	DecidedAt     time.Time `dynamodbav:"decidedAt"`
	ExpiresAt     int64     `dynamodbav:"pendingShareExpiresAt"`
	Sealed        string    `dynamodbav:"sealed"`
}

func decisionBinding(userID, accountID, urlHash string) map[string]string {
	return map[string]string{"purpose": "ttobak-customer-update-decision", "userId": userID, "accountId": accountID, "urlHash": urlHash}
}

func validOutcome(o string) bool {
	switch o {
	case "", model.UpdateOutcomeDrafted, model.UpdateOutcomeDiscarded, model.UpdateOutcomeDeferred:
		return true
	}
	return false
}

// decisionPutInput builds the sealed create-if-absent-or-expired write. The
// caller's decision is not mutated.
func decisionPutInput(ctx context.Context, s sealing.Sealer, tableName, userID string, d *model.UpdateDecision, now time.Time) (*dynamodb.PutItemInput, error) {
	if userID == "" || d == nil {
		return nil, invalidRun("decision")
	}
	if !urlHashPattern.MatchString(d.URLHash) || !accountIDPattern.MatchString(d.AccountID) {
		return nil, invalidRun("decision key")
	}
	if d.Choice != model.UpdateChoiceNeeded && d.Choice != model.UpdateChoiceNotNeeded || !validOutcome(d.Outcome) {
		return nil, invalidRun("decision code")
	}
	if s == nil {
		return nil, ErrUpdateDraftEncryptionUnavailable
	}
	payload := sealedRunDecision{Reason: d.Reason}
	if d.Choice == model.UpdateChoiceNeeded {
		payload.GateInput = d.GateInput
	}
	plain, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	sealed, err := s.Seal(ctx, plain, decisionBinding(userID, d.AccountID, d.URLHash))
	if err != nil {
		return nil, sealingError(err)
	}
	decidedAt := d.DecidedAt
	if decidedAt.IsZero() {
		decidedAt = now
	}
	av, err := attributevalue.MarshalMap(storedUpdateDecision{
		customerUpdateKeys: customerUpdateKeys{
			PK: model.PrefixUser + userID, SK: model.UpdateDecisionSK(d.URLHash, d.AccountID), EntityType: "UPDATE_DECISION",
		},
		URLHash: d.URLHash, AccountID: d.AccountID, Choice: d.Choice, Score: d.Score, Backend: d.Backend, RunID: d.RunID,
		Outcome: d.Outcome, DiscardReason: d.DiscardReason, DecidedAt: decidedAt,
		ExpiresAt: ttlOrDefault(d.ExpiresAt, now, model.UpdateDecisionTTL), Sealed: sealed,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal update decision: %w", err)
	}
	in, err := putIfAbsentOrExpired(tableName, av, now)
	if err != nil {
		return nil, fmt.Errorf("build create-decision condition: %w", err)
	}
	return in, nil
}

// PutDecisionIfAbsent creates a decision; a live decision for the same (URL,
// account) returns ErrConditionFailed.
func (r *CustomerUpdateRepository) PutDecisionIfAbsent(ctx context.Context, userID string, d *model.UpdateDecision) error {
	in, err := decisionPutInput(ctx, r.sealer, r.tableName, userID, d, r.clock())
	if err != nil {
		return err
	}
	if _, err := r.client.PutItem(ctx, in); err != nil {
		if isConditionFailed(err) {
			return fmt.Errorf("%w: update decision already exists", ErrConditionFailed)
		}
		return fmt.Errorf("failed to create update decision: %w", err)
	}
	return nil
}

func getDecisionInput(tableName, userID, urlHash, accountID string) (*dynamodb.GetItemInput, error) {
	if !urlHashPattern.MatchString(urlHash) || !accountIDPattern.MatchString(accountID) {
		return nil, invalidRun("decision key")
	}
	return &dynamodb.GetItemInput{
		TableName: aws.String(tableName), Key: updateUserKey(userID, model.UpdateDecisionSK(urlHash, accountID)),
		ConsistentRead: aws.Bool(true),
	}, nil
}

// decisionFromItem opens a stored decision. A row whose key does not match
// its identity, or whose sealed payload does not open, is unreadable.
func decisionFromItem(ctx context.Context, s sealing.Sealer, userID string, item map[string]types.AttributeValue) (*model.UpdateDecision, error) {
	var row storedUpdateDecision
	if err := attributevalue.UnmarshalMap(item, &row); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUpdateDecisionUnreadable, err)
	}
	if s == nil {
		return nil, ErrUpdateDraftEncryptionUnavailable
	}
	if row.Sealed == "" || row.PK != model.PrefixUser+userID || row.SK != model.UpdateDecisionSK(row.URLHash, row.AccountID) {
		return nil, ErrUpdateDecisionUnreadable
	}
	plain, err := s.Open(ctx, row.Sealed, decisionBinding(userID, row.AccountID, row.URLHash))
	if err != nil {
		return nil, openingError(ErrUpdateDecisionUnreadable, err)
	}
	var p sealedRunDecision
	if err := json.Unmarshal(plain, &p); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUpdateDecisionUnreadable, err)
	}
	d := &model.UpdateDecision{
		URLHash: row.URLHash, AccountID: row.AccountID, Choice: row.Choice, Score: row.Score, Reason: p.Reason,
		Backend: row.Backend, RunID: row.RunID, Outcome: row.Outcome, DiscardReason: row.DiscardReason,
		DecidedAt: row.DecidedAt, ExpiresAt: row.ExpiresAt,
	}
	if d.Choice == model.UpdateChoiceNeeded {
		d.GateInput = p.GateInput
	}
	return d, nil
}

// GetDecision returns one opened decision with a consistent read, or nil.
func (r *CustomerUpdateRepository) GetDecision(ctx context.Context, userID, urlHash, accountID string) (*model.UpdateDecision, error) {
	in, err := getDecisionInput(r.tableName, userID, urlHash, accountID)
	if err != nil {
		return nil, err
	}
	res, err := r.client.GetItem(ctx, in)
	if err != nil {
		return nil, fmt.Errorf("failed to get update decision: %w", err)
	}
	if len(res.Item) == 0 {
		return nil, nil
	}
	return decisionFromItem(ctx, r.sealer, userID, res.Item)
}

func decisionOutcomeUpdateInput(tableName, userID, urlHash, accountID, outcome, discardReason string) (*dynamodb.UpdateItemInput, error) {
	if !urlHashPattern.MatchString(urlHash) || !accountIDPattern.MatchString(accountID) || !validOutcome(outcome) {
		return nil, invalidRun("decision outcome")
	}
	expr, err := expression.NewBuilder().
		WithUpdate(expression.Set(expression.Name("outcome"), expression.Value(outcome)).
			Set(expression.Name("discardReason"), expression.Value(discardReason))).
		WithCondition(expression.AttributeExists(expression.Name("PK"))).Build()
	if err != nil {
		return nil, fmt.Errorf("build decision-outcome expression: %w", err)
	}
	return &dynamodb.UpdateItemInput{
		TableName: aws.String(tableName), Key: updateUserKey(userID, model.UpdateDecisionSK(urlHash, accountID)),
		UpdateExpression: expr.Update(), ConditionExpression: expr.Condition(),
		ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(),
	}, nil
}

// SetDecisionOutcome sets an existing decision's outcome codes without
// touching its sealed payload; a missing decision returns ErrConditionFailed.
func (r *CustomerUpdateRepository) SetDecisionOutcome(ctx context.Context, userID, urlHash, accountID, outcome, discardReason string) error {
	in, err := decisionOutcomeUpdateInput(r.tableName, userID, urlHash, accountID, outcome, discardReason)
	if err != nil {
		return err
	}
	if _, err := r.client.UpdateItem(ctx, in); err != nil {
		if isConditionFailed(err) {
			return fmt.Errorf("%w: update decision not found", ErrConditionFailed)
		}
		return fmt.Errorf("failed to set update decision outcome: %w", err)
	}
	return nil
}

// decidedAccountsQueryInput reads one item's decision keys (SK
// CUDEC#{urlHash}#{accountId}) that hold a final, live decision: not_needed,
// or needed with outcome drafted or discarded, and not past the TTL (an
// expired row is overwritable, so it does not count). The filter reads the
// plaintext codes on the server; only the keys are returned.
func decidedAccountsQueryInput(tableName, userID, urlHash string, now time.Time) (*dynamodb.QueryInput, error) {
	if userID == "" || !urlHashPattern.MatchString(urlHash) {
		return nil, invalidRun("decision key")
	}
	final := expression.Name("choice").Equal(expression.Value(model.UpdateChoiceNotNeeded)).
		Or(expression.Name("outcome").In(expression.Value(model.UpdateOutcomeDrafted), expression.Value(model.UpdateOutcomeDiscarded)))
	live := expression.AttributeNotExists(expression.Name(customerUpdateTTLAttribute)).
		Or(expression.Name(customerUpdateTTLAttribute).GreaterThan(expression.Value(now.Unix())))
	expr, err := expression.NewBuilder().
		WithKeyCondition(expression.Key("PK").Equal(expression.Value(model.PrefixUser + userID)).
			And(expression.Key("SK").BeginsWith(model.UpdateDecisionSK(urlHash, "")))).
		WithFilter(final.And(live)).
		WithProjection(expression.NamesList(expression.Name("PK"), expression.Name("SK"))).Build()
	if err != nil {
		return nil, fmt.Errorf("build decided-accounts expression: %w", err)
	}
	return &dynamodb.QueryInput{
		TableName: aws.String(tableName), ConsistentRead: aws.Bool(true),
		KeyConditionExpression: expr.KeyCondition(), FilterExpression: expr.Filter(), ProjectionExpression: expr.Projection(),
		ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(),
	}, nil
}

// decidedAccountIDs maps one item's decision keys to account ids, skipping a
// key that is not CUDEC#{urlHash}#{valid account id}.
func decidedAccountIDs(items []map[string]types.AttributeValue, urlHash string) []string {
	prefix := model.UpdateDecisionSK(urlHash, "")
	out := []string{}
	for _, item := range items {
		sk, _ := stringAttr(item, "SK")
		if id, ok := strings.CutPrefix(sk, prefix); ok && accountIDPattern.MatchString(id) {
			out = append(out, id)
		}
	}
	return out
}

// ListDecidedAccounts returns the account ids whose pair with the item has a
// final, live stored decision (decidedAccountsQueryInput); a deferred
// decision does not count. Nothing is unsealed.
func (r *CustomerUpdateRepository) ListDecidedAccounts(ctx context.Context, userID, urlHash string) ([]string, error) {
	in, err := decidedAccountsQueryInput(r.tableName, userID, urlHash, r.clock())
	if err != nil {
		return nil, err
	}
	var items []map[string]types.AttributeValue
	for {
		res, err := r.client.Query(ctx, in)
		if err != nil {
			return nil, fmt.Errorf("failed to query update decisions: %w", err)
		}
		items = append(items, res.Items...)
		if len(res.LastEvaluatedKey) == 0 {
			break
		}
		in.ExclusiveStartKey = res.LastEvaluatedKey
	}
	return decidedAccountIDs(items, urlHash), nil
}
