package repository

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/google/uuid"
	"github.com/ttobak/backend/internal/model"
)

var (
	ErrInvalidCalendarBatch  = errors.New("invalid calendar batch")
	ErrInvalidCalendarCursor = errors.New("invalid calendar cursor")
	ErrInvalidCalendarQuery  = errors.New("invalid calendar query")
	ErrCalendarCorruptData   = errors.New("invalid calendar storage identity")
	calendarStorageID        = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	calendarPayloadDigest    = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

const calendarMaxWindow = 93 * 24 * time.Hour

type CalendarDynamoDBClient interface {
	GetItem(context.Context, *dynamodb.GetItemInput, ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
	Query(context.Context, *dynamodb.QueryInput, ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error)
	TransactWriteItems(context.Context, *dynamodb.TransactWriteItemsInput, ...func(*dynamodb.Options)) (*dynamodb.TransactWriteItemsOutput, error)
}

type CalendarRepository struct {
	client    CalendarDynamoDBClient
	tableName string
	now       func() time.Time
}

type CalendarEventRow struct {
	PK         string    `dynamodbav:"PK"`
	SK         string    `dynamodbav:"SK"`
	UserID     string    `dynamodbav:"userId"`
	SourceID   string    `dynamodbav:"sourceId"`
	EventID    string    `dynamodbav:"eventId"`
	StartsAt   time.Time `dynamodbav:"startsAt"`
	EndsAt     time.Time `dynamodbav:"endsAt"`
	AccountID  string    `dynamodbav:"accountId,omitempty"`
	Ciphertext string    `dynamodbav:"ciphertext"`
	Revision   int64     `dynamodbav:"revision"`
	ExpiresAt  int64     `dynamodbav:"pendingShareExpiresAt"`
}

type CalendarWriteMutation struct {
	Op         string
	ExternalID string
	EventID    string
	Event      *CalendarEventRow
}

type CalendarWriteBatch struct {
	ExpectedRevision int64
	PayloadDigest    string
	AcceptedAt       time.Time
	WindowStart      time.Time
	WindowEnd        time.Time
	ExpiresAt        int64
	Receipt          model.CalendarSyncReceipt
	Mutations        []CalendarWriteMutation
}

type CalendarBatchReceipt struct {
	PK            string                    `dynamodbav:"PK"`
	SK            string                    `dynamodbav:"SK"`
	UserID        string                    `dynamodbav:"userId"`
	SourceID      string                    `dynamodbav:"sourceId"`
	PayloadDigest string                    `dynamodbav:"payloadDigest"`
	Receipt       model.CalendarSyncReceipt `dynamodbav:"receipt"`
	ExpiresAt     int64                     `dynamodbav:"pendingShareExpiresAt"`
}

type CalendarRowPage struct {
	Rows       []CalendarEventRow
	RowCursors []string
	NextCursor *string
}

type calendarReference struct {
	PK        string    `dynamodbav:"PK"`
	SK        string    `dynamodbav:"SK"`
	UserID    string    `dynamodbav:"userId"`
	SourceID  string    `dynamodbav:"sourceId"`
	EventID   string    `dynamodbav:"eventId"`
	StartsAt  time.Time `dynamodbav:"startsAt"`
	EndsAt    time.Time `dynamodbav:"endsAt"`
	Revision  int64     `dynamodbav:"revision"`
	ExpiresAt int64     `dynamodbav:"pendingShareExpiresAt"`
}

type calendarSourceRow struct {
	PK     string `dynamodbav:"PK"`
	SK     string `dynamodbav:"SK"`
	UserID string `dynamodbav:"userId"`
	model.CalendarSourceStatus
}

type calendarCursor struct {
	Version   int    `json:"version"`
	Kind      string `json:"kind"`
	UserID    string `json:"userId"`
	From      string `json:"from,omitempty"`
	To        string `json:"to,omitempty"`
	AccountID string `json:"accountId,omitempty"`
	Limit     int32  `json:"limit"`
	LastPK    string `json:"lastPK"`
	LastSK    string `json:"lastSK"`
}

func NewCalendarRepository(client CalendarDynamoDBClient, tableName string) *CalendarRepository {
	return &CalendarRepository{client: client, tableName: tableName, now: time.Now}
}

func (repo *CalendarRepository) ApplyCalendarBatch(ctx context.Context, userID, sourceID string, batch CalendarWriteBatch) (*model.CalendarSyncReceipt, error) {
	if err := validateCalendarBatch(userID, sourceID, batch); err != nil {
		return nil, err
	}
	saved, err := repo.GetCalendarBatchReceipt(ctx, userID, sourceID, batch.Receipt.BatchID)
	if err != nil {
		return nil, err
	}
	if saved != nil {
		return calendarRetryReceipt(saved, batch.PayloadDigest)
	}
	source, err := repo.calendarSourceUpdate(userID, sourceID, batch)
	if err != nil {
		return nil, err
	}
	operations := []types.TransactWriteItem{{Update: source}}
	for _, mutation := range batch.Mutations {
		old, err := repo.getCalendarEventRow(ctx, userID, sourceID, mutation.EventID)
		if err != nil {
			return nil, err
		}
		if old != nil && !validCalendarEventIdentity(old, userID, sourceID, mutation.EventID) {
			return nil, ErrCalendarCorruptData
		}
		if old != nil && (mutation.Op == "remove" || !old.StartsAt.Equal(mutation.Event.StartsAt)) {
			deletion, err := repo.calendarDelete(userID, sourceID, mutation.EventID, model.CalendarTimeSortKey(old.StartsAt, sourceID, mutation.EventID))
			if err != nil {
				return nil, err
			}
			operations = append(operations, types.TransactWriteItem{Delete: deletion})
		}
		if mutation.Op == "remove" {
			deletion, err := repo.calendarDelete(userID, sourceID, mutation.EventID, model.CalendarEventSortKey(sourceID, mutation.EventID))
			if err != nil {
				return nil, err
			}
			operations = append(operations, types.TransactWriteItem{Delete: deletion})
			continue
		}
		row := *mutation.Event
		row.PK, row.SK = model.PrefixUser+userID, model.CalendarEventSortKey(sourceID, mutation.EventID)
		row.UserID, row.SourceID, row.EventID = userID, sourceID, mutation.EventID
		row.StartsAt, row.EndsAt = row.StartsAt.UTC(), row.EndsAt.UTC()
		condition := calendarEventWriteCondition(userID, sourceID, row.EventID)
		canonical, err := repo.calendarPut(row, condition)
		if err != nil {
			return nil, err
		}
		reference := calendarReference{
			PK: row.PK, SK: model.CalendarTimeSortKey(row.StartsAt, sourceID, row.EventID),
			UserID: userID, SourceID: sourceID, EventID: row.EventID,
			StartsAt: row.StartsAt, EndsAt: row.EndsAt, Revision: row.Revision, ExpiresAt: row.ExpiresAt,
		}
		refPut, err := repo.calendarPut(reference, condition)
		if err != nil {
			return nil, err
		}
		operations = append(operations, types.TransactWriteItem{Put: canonical}, types.TransactWriteItem{Put: refPut})
	}
	receipt := CalendarBatchReceipt{
		PK: model.PrefixUser + userID, SK: model.CalendarBatchSortKey(sourceID, batch.Receipt.BatchID),
		UserID: userID, SourceID: sourceID, PayloadDigest: batch.PayloadDigest, Receipt: batch.Receipt, ExpiresAt: batch.ExpiresAt,
	}
	condition := expression.AttributeNotExists(expression.Name("PK")).Or(
		expression.Name("pendingShareExpiresAt").LessThanEqual(expression.Value(repo.now().Unix())).
			And(calendarScopeCondition(userID, sourceID)))
	receiptPut, err := repo.calendarPut(receipt, condition)
	if err != nil {
		return nil, err
	}
	operations = append(operations, types.TransactWriteItem{Put: receiptPut})
	_, err = repo.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: operations},
		func(options *dynamodb.Options) { options.Retryer = aws.NopRetryer{} })
	if err != nil {
		saved, readErr := repo.GetCalendarBatchReceipt(ctx, userID, sourceID, batch.Receipt.BatchID)
		if readErr != nil {
			return nil, errors.Join(fmt.Errorf("calendar transaction: %w", err), readErr)
		}
		if saved != nil {
			return calendarRetryReceipt(saved, batch.PayloadDigest)
		}
		var cancellation *types.TransactionCanceledException
		if errors.As(err, &cancellation) {
			for _, reason := range cancellation.CancellationReasons {
				if aws.ToString(reason.Code) == "ConditionalCheckFailed" {
					return nil, errors.Join(ErrConditionFailed, err)
				}
			}
		}
		return nil, fmt.Errorf("calendar transaction: %w", err)
	}
	return &receipt.Receipt, nil
}

func (repo *CalendarRepository) GetCalendarBatchReceipt(ctx context.Context, userID, sourceID, batchID string) (*CalendarBatchReceipt, error) {
	if !calendarStorageID.MatchString(userID) || uuid.Validate(sourceID) != nil || uuid.Validate(batchID) != nil {
		return nil, ErrInvalidCalendarBatch
	}
	sortKey := model.CalendarBatchSortKey(sourceID, batchID)
	item, err := repo.calendarGet(ctx, userID, sortKey)
	if err != nil || len(item) == 0 {
		return nil, err
	}
	var saved CalendarBatchReceipt
	if err := attributevalue.UnmarshalMap(item, &saved); err != nil {
		return nil, ErrCalendarCorruptData
	}
	if saved.PK != model.PrefixUser+userID || saved.SK != sortKey || saved.UserID != userID || saved.SourceID != sourceID ||
		saved.Receipt.BatchID != batchID || saved.Receipt.Revision < 1 || !calendarPayloadDigest.MatchString(saved.PayloadDigest) {
		return nil, ErrCalendarCorruptData
	}
	if saved.ExpiresAt <= repo.now().Unix() {
		return nil, nil
	}
	return &saved, nil
}

func (repo *CalendarRepository) ListCalendarEventRows(ctx context.Context, userID string, query model.CalendarQuery) (*CalendarRowPage, error) {
	limit, err := calendarQueryLimit(userID, query.Limit)
	if err != nil || !validCalendarWindow(query.From, query.To) || query.AccountID != "" && !calendarStorageID.MatchString(query.AccountID) {
		return nil, ErrInvalidCalendarQuery
	}
	scope := calendarCursor{Version: 1, Kind: "events", UserID: userID, From: query.From.UTC().Format(time.RFC3339Nano),
		To: query.To.UTC().Format(time.RFC3339Nano), AccountID: query.AccountID, Limit: limit}
	lower := model.PrefixCalendarTime + query.From.Add(-calendarMaxWindow).UTC().Format("2006-01-02T15:04:05.000000000Z")
	upper := model.PrefixCalendarTime + query.To.UTC().Format("2006-01-02T15:04:05.000000000Z")
	startKey, err := decodeCalendarCursor(query.Cursor, scope, lower, upper)
	if err != nil {
		return nil, err
	}
	keyCondition := expression.Key("PK").Equal(expression.Value(model.PrefixUser + userID)).
		And(expression.Key("SK").Between(expression.Value(lower), expression.Value(upper)))
	queryExpression, err := expression.NewBuilder().WithKeyCondition(keyCondition).Build()
	if err != nil {
		return nil, err
	}
	page := &CalendarRowPage{Rows: make([]CalendarEventRow, 0)}
	now := repo.now().Unix()
	for pageNumber := 0; pageNumber < 5; pageNumber++ {
		result, err := repo.client.Query(ctx, &dynamodb.QueryInput{
			TableName: aws.String(repo.tableName), KeyConditionExpression: queryExpression.KeyCondition(),
			ExpressionAttributeNames: queryExpression.Names(), ExpressionAttributeValues: queryExpression.Values(),
			ConsistentRead: aws.Bool(true), ScanIndexForward: aws.Bool(true), Limit: aws.Int32(limit - int32(len(page.Rows))), ExclusiveStartKey: startKey,
		})
		if err != nil {
			return nil, fmt.Errorf("query calendar references: %w", err)
		}
		for _, item := range result.Items {
			var reference calendarReference
			if attributevalue.UnmarshalMap(item, &reference) != nil || !validCalendarReference(reference, userID, now) {
				continue
			}
			row, err := repo.getCalendarEventRow(ctx, userID, reference.SourceID, reference.EventID)
			if err != nil {
				return nil, err
			}
			if row == nil || !validCalendarEventIdentity(row, userID, reference.SourceID, reference.EventID) ||
				row.ExpiresAt <= now || row.ExpiresAt != reference.ExpiresAt || row.Revision != reference.Revision ||
				!row.StartsAt.Equal(reference.StartsAt) || !row.EndsAt.Equal(reference.EndsAt) ||
				!row.StartsAt.Before(query.To) || !row.EndsAt.After(query.From) ||
				query.AccountID != "" && row.AccountID != query.AccountID {
				continue
			}
			page.Rows = append(page.Rows, *row)
			rowCursor, err := encodeCalendarCursor(scope, calendarKey(userID, reference.SK), lower, upper)
			if err != nil {
				return nil, err
			}
			page.RowCursors = append(page.RowCursors, *rowCursor)
		}
		startKey = result.LastEvaluatedKey
		if len(startKey) == 0 || len(page.Rows) >= int(limit) {
			break
		}
	}
	page.NextCursor, err = encodeCalendarCursor(scope, startKey, lower, upper)
	if err != nil {
		return nil, err
	}
	return page, nil
}

func (repo *CalendarRepository) ListCalendarSources(ctx context.Context, userID, cursor string, limit int32) (*model.CalendarSourcePage, error) {
	limit, err := calendarQueryLimit(userID, limit)
	if err != nil {
		return nil, err
	}
	scope := calendarCursor{Version: 1, Kind: "sources", UserID: userID, Limit: limit}
	lower, upper := model.PrefixCalendarSource, model.PrefixCalendarSource+"~"
	startKey, err := decodeCalendarCursor(cursor, scope, lower, upper)
	if err != nil {
		return nil, err
	}
	keyCondition := expression.Key("PK").Equal(expression.Value(model.PrefixUser + userID)).
		And(expression.Key("SK").BeginsWith(model.PrefixCalendarSource))
	built, err := expression.NewBuilder().WithKeyCondition(keyCondition).Build()
	if err != nil {
		return nil, err
	}
	page := &model.CalendarSourcePage{Sources: make([]model.CalendarSourceStatus, 0)}
	for pageNumber := 0; pageNumber < 5; pageNumber++ {
		result, err := repo.client.Query(ctx, &dynamodb.QueryInput{
			TableName: aws.String(repo.tableName), KeyConditionExpression: built.KeyCondition(), ExpressionAttributeNames: built.Names(), ExpressionAttributeValues: built.Values(),
			ConsistentRead: aws.Bool(true), ScanIndexForward: aws.Bool(true), Limit: aws.Int32(limit - int32(len(page.Sources))), ExclusiveStartKey: startKey,
		})
		if err != nil {
			return nil, fmt.Errorf("query calendar sources: %w", err)
		}
		for _, item := range result.Items {
			var candidate calendarSourceRow
			if attributevalue.UnmarshalMap(item, &candidate) != nil || !validCalendarSource(candidate, userID, candidate.SourceID) {
				continue
			}
			canonical, err := repo.calendarGet(ctx, userID, model.CalendarSourceSortKey(candidate.SourceID))
			if err != nil {
				return nil, err
			}
			var current calendarSourceRow
			if len(canonical) == 0 || attributevalue.UnmarshalMap(canonical, &current) != nil || !validCalendarSource(current, userID, candidate.SourceID) {
				continue
			}
			page.Sources = append(page.Sources, current.CalendarSourceStatus)
		}
		startKey = result.LastEvaluatedKey
		if len(startKey) == 0 || len(page.Sources) >= int(limit) {
			break
		}
	}
	page.NextCursor, err = encodeCalendarCursor(scope, startKey, lower, upper)
	if err != nil {
		return nil, err
	}
	return page, nil
}

func validateCalendarBatch(userID, sourceID string, batch CalendarWriteBatch) error {
	if !calendarStorageID.MatchString(userID) || uuid.Validate(sourceID) != nil || uuid.Validate(batch.Receipt.BatchID) != nil ||
		!calendarPayloadDigest.MatchString(batch.PayloadDigest) || batch.ExpectedRevision < 0 || batch.ExpectedRevision == math.MaxInt64 ||
		batch.Receipt.Revision != batch.ExpectedRevision+1 || batch.AcceptedAt.IsZero() ||
		!validCalendarWindow(batch.WindowStart, batch.WindowEnd) || len(batch.Mutations) > 20 ||
		batch.ExpiresAt != batch.AcceptedAt.Add(7*24*time.Hour).Unix() {
		return ErrInvalidCalendarBatch
	}
	externalIDs := make(map[string]bool)
	eventIDs := make(map[string]bool)
	upserted, removed := 0, 0
	for _, mutation := range batch.Mutations {
		if !calendarStorageID.MatchString(mutation.EventID) || len(mutation.ExternalID) == 0 || len(mutation.ExternalID) > 512 ||
			!utf8.ValidString(mutation.ExternalID) || externalIDs[mutation.ExternalID] || eventIDs[mutation.EventID] {
			return ErrInvalidCalendarBatch
		}
		externalIDs[mutation.ExternalID], eventIDs[mutation.EventID] = true, true
		switch mutation.Op {
		case "upsert":
			row := mutation.Event
			if row == nil || row.UserID != userID || row.SourceID != sourceID || row.EventID != mutation.EventID ||
				row.PK != "" && row.PK != model.PrefixUser+userID ||
				row.SK != "" && row.SK != model.CalendarEventSortKey(sourceID, mutation.EventID) ||
				!validCalendarWindow(row.StartsAt, row.EndsAt) || row.Revision != batch.Receipt.Revision ||
				row.ExpiresAt != row.EndsAt.Add(90*24*time.Hour).Unix() || row.Ciphertext == "" || len(row.Ciphertext) > 16*1024 ||
				row.AccountID != "" && !calendarStorageID.MatchString(row.AccountID) {
				return ErrInvalidCalendarBatch
			}
			upserted++
		case "remove":
			if mutation.Event != nil {
				return ErrInvalidCalendarBatch
			}
			removed++
		default:
			return ErrInvalidCalendarBatch
		}
	}
	if batch.Receipt.Upserted != upserted || batch.Receipt.Removed != removed {
		return ErrInvalidCalendarBatch
	}
	return nil
}

func validCalendarWindow(from, to time.Time) bool {
	return !from.IsZero() && from.Year() >= 1 && to.Year() <= 9999 && to.After(from) && to.Sub(from) <= calendarMaxWindow
}

func calendarRetryReceipt(saved *CalendarBatchReceipt, digest string) (*model.CalendarSyncReceipt, error) {
	if saved.PayloadDigest != digest {
		return nil, ErrConditionFailed
	}
	return &saved.Receipt, nil
}

func calendarKey(userID, sortKey string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		"PK": &types.AttributeValueMemberS{Value: model.PrefixUser + userID},
		"SK": &types.AttributeValueMemberS{Value: sortKey},
	}
}

func (repo *CalendarRepository) calendarGet(ctx context.Context, userID, sortKey string) (map[string]types.AttributeValue, error) {
	result, err := repo.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(repo.tableName), Key: calendarKey(userID, sortKey), ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return nil, fmt.Errorf("read calendar row: %w", err)
	}
	return result.Item, nil
}

func (repo *CalendarRepository) getCalendarEventRow(ctx context.Context, userID, sourceID, eventID string) (*CalendarEventRow, error) {
	item, err := repo.calendarGet(ctx, userID, model.CalendarEventSortKey(sourceID, eventID))
	if err != nil || len(item) == 0 {
		return nil, err
	}
	var row CalendarEventRow
	if err := attributevalue.UnmarshalMap(item, &row); err != nil {
		return nil, ErrCalendarCorruptData
	}
	return &row, nil
}

func calendarScopeCondition(userID, sourceID string) expression.ConditionBuilder {
	return expression.Name("userId").Equal(expression.Value(userID)).
		And(expression.Name("sourceId").Equal(expression.Value(sourceID)))
}

func calendarEventWriteCondition(userID, sourceID, eventID string) expression.ConditionBuilder {
	return expression.AttributeNotExists(expression.Name("PK")).Or(
		calendarScopeCondition(userID, sourceID).And(expression.Name("eventId").Equal(expression.Value(eventID))))
}

func (repo *CalendarRepository) calendarSourceUpdate(userID, sourceID string, batch CalendarWriteBatch) (*types.Update, error) {
	condition := expression.AttributeNotExists(expression.Name("PK"))
	if batch.ExpectedRevision > 0 {
		condition = calendarScopeCondition(userID, sourceID).
			And(expression.Name("provider").Equal(expression.Value("outlook"))).
			And(expression.Name("revision").Equal(expression.Value(batch.ExpectedRevision)))
	}
	update := expression.Set(expression.Name("userId"), expression.Value(userID)).
		Set(expression.Name("sourceId"), expression.Value(sourceID)).
		Set(expression.Name("provider"), expression.Value("outlook")).
		Set(expression.Name("revision"), expression.Value(batch.Receipt.Revision)).
		Set(expression.Name("lastAttemptAt"), expression.Value(batch.AcceptedAt.UTC()))
	if batch.Receipt.Completed {
		update = update.Set(expression.Name("lastCompletedAt"), expression.Value(batch.AcceptedAt.UTC())).
			Set(expression.Name("lastCompletedRevision"), expression.Value(batch.Receipt.Revision)).
			Set(expression.Name("completedWindowStart"), expression.Value(batch.WindowStart.UTC())).
			Set(expression.Name("completedWindowEnd"), expression.Value(batch.WindowEnd.UTC()))
	}
	built, err := expression.NewBuilder().WithUpdate(update).WithCondition(condition).Build()
	if err != nil {
		return nil, err
	}
	return &types.Update{TableName: aws.String(repo.tableName), Key: calendarKey(userID, model.CalendarSourceSortKey(sourceID)),
		UpdateExpression: built.Update(), ConditionExpression: built.Condition(), ExpressionAttributeNames: built.Names(), ExpressionAttributeValues: built.Values()}, nil
}

func (repo *CalendarRepository) calendarPut(value any, condition expression.ConditionBuilder) (*types.Put, error) {
	item, err := attributevalue.MarshalMap(value)
	if err != nil {
		return nil, err
	}
	built, err := expression.NewBuilder().WithCondition(condition).Build()
	if err != nil {
		return nil, err
	}
	return &types.Put{TableName: aws.String(repo.tableName), Item: item, ConditionExpression: built.Condition(),
		ExpressionAttributeNames: built.Names(), ExpressionAttributeValues: built.Values()}, nil
}

func (repo *CalendarRepository) calendarDelete(userID, sourceID, eventID, sortKey string) (*types.Delete, error) {
	built, err := expression.NewBuilder().WithCondition(calendarEventWriteCondition(userID, sourceID, eventID)).Build()
	if err != nil {
		return nil, err
	}
	return &types.Delete{TableName: aws.String(repo.tableName), Key: calendarKey(userID, sortKey), ConditionExpression: built.Condition(),
		ExpressionAttributeNames: built.Names(), ExpressionAttributeValues: built.Values()}, nil
}

func validCalendarEventIdentity(row *CalendarEventRow, userID, sourceID, eventID string) bool {
	return row.PK == model.PrefixUser+userID && row.SK == model.CalendarEventSortKey(sourceID, eventID) &&
		row.UserID == userID && row.SourceID == sourceID && row.EventID == eventID &&
		validCalendarWindow(row.StartsAt, row.EndsAt) && row.Ciphertext != "" && row.Revision > 0 &&
		row.ExpiresAt == row.EndsAt.Add(90*24*time.Hour).Unix()
}

func validCalendarReference(reference calendarReference, userID string, now int64) bool {
	return reference.UserID == userID && reference.PK == model.PrefixUser+userID &&
		uuid.Validate(reference.SourceID) == nil && calendarStorageID.MatchString(reference.EventID) &&
		reference.SK == model.CalendarTimeSortKey(reference.StartsAt, reference.SourceID, reference.EventID) &&
		validCalendarWindow(reference.StartsAt, reference.EndsAt) && reference.ExpiresAt > now &&
		reference.ExpiresAt == reference.EndsAt.Add(90*24*time.Hour).Unix() && reference.Revision > 0
}

func validCalendarSource(row calendarSourceRow, userID, sourceID string) bool {
	if row.PK != model.PrefixUser+userID || row.SK != model.CalendarSourceSortKey(sourceID) ||
		row.UserID != userID || row.SourceID != sourceID || uuid.Validate(sourceID) != nil ||
		row.Provider != "outlook" || row.Revision < 1 || row.LastAttemptAt.IsZero() {
		return false
	}
	if row.LastCompletedAt == nil {
		return row.CompletedWindowStart == nil && row.CompletedWindowEnd == nil && row.LastCompletedRevision == nil
	}
	if row.LastCompletedRevision != nil && (*row.LastCompletedRevision < 1 || *row.LastCompletedRevision > row.Revision) {
		return false
	}
	return !row.LastCompletedAt.IsZero() && row.CompletedWindowStart != nil && row.CompletedWindowEnd != nil &&
		validCalendarWindow(*row.CompletedWindowStart, *row.CompletedWindowEnd)
}

func calendarQueryLimit(userID string, limit int32) (int32, error) {
	if !calendarStorageID.MatchString(userID) || limit < 0 || limit > 50 {
		return 0, ErrInvalidCalendarQuery
	}
	if limit == 0 {
		limit = 50
	}
	return limit, nil
}

func decodeCalendarCursor(encoded string, scope calendarCursor, lower, upper string) (map[string]types.AttributeValue, error) {
	if encoded == "" {
		return nil, nil
	}
	if len(encoded) > 4096 {
		return nil, ErrInvalidCalendarCursor
	}
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, ErrInvalidCalendarCursor
	}
	var cursor calendarCursor
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&cursor) != nil {
		return nil, ErrInvalidCalendarCursor
	}
	if decoder.Decode(new(any)) != io.EOF {
		return nil, ErrInvalidCalendarCursor
	}
	expected := scope
	expected.LastPK, expected.LastSK = cursor.LastPK, cursor.LastSK
	if cursor != expected || cursor.LastPK != model.PrefixUser+scope.UserID || cursor.LastSK < lower || cursor.LastSK >= upper {
		return nil, ErrInvalidCalendarCursor
	}
	return calendarKey(scope.UserID, cursor.LastSK), nil
}

func encodeCalendarCursor(scope calendarCursor, key map[string]types.AttributeValue, lower, upper string) (*string, error) {
	if len(key) == 0 {
		return nil, nil
	}
	partition, partitionOK := key["PK"].(*types.AttributeValueMemberS)
	sortKey, sortOK := key["SK"].(*types.AttributeValueMemberS)
	if len(key) != 2 || !partitionOK || !sortOK {
		return nil, ErrInvalidCalendarCursor
	}
	scope.LastPK, scope.LastSK = partition.Value, sortKey.Value
	data, err := json.Marshal(scope)
	if err != nil {
		return nil, err
	}
	encoded := base64.RawURLEncoding.EncodeToString(data)
	if _, err := decodeCalendarCursor(encoded, calendarCursor{
		Version: scope.Version, Kind: scope.Kind, UserID: scope.UserID, From: scope.From, To: scope.To, AccountID: scope.AccountID, Limit: scope.Limit,
	}, lower, upper); err != nil {
		return nil, err
	}
	return &encoded, nil
}
