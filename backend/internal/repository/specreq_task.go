package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
)

var (
	// ErrSpecReqEventGone reports that the calendar occurrence a task derives
	// from is absent, owned by another user, or at a different revision.
	ErrSpecReqEventGone = errors.New("calendar occurrence is gone")
	// ErrInvalidSpecReqCursor reports a malformed or foreign task cursor.
	ErrInvalidSpecReqCursor = errors.New("invalid specreq task cursor")
	// ErrInvalidSpecReqTask reports a row or query that fails local validation.
	ErrInvalidSpecReqTask = errors.New("invalid specreq task")
)

const (
	specReqTaskEntityType = "SPECREQ_TASK"
	specReqTaskCursorKind = "specreq-tasks"
	specReqTaskMaxLimit   = 20
	// specReqTerminalRetention bounds how long a terminal task row is kept.
	specReqTerminalRetention = 14 * 24 * time.Hour
	// specReqHorizonRetention keeps a terminal row past its occurrence end so it
	// outlives the runner's 7-day lookback, 7-day match window and 30-day future window.
	specReqHorizonRetention = 45 * 24 * time.Hour
)

// specReqTaskTerminal reports whether the row's state needs no further action:
// closed, resolved, or issued on the mine path (the user issued it themselves).
func specReqTaskTerminal(row SpecReqTaskRow) bool {
	switch row.State {
	case model.SpecReqStateClosed, model.SpecReqStateResolved:
		return true
	case model.SpecReqStateIssued:
		return row.Path == model.SpecReqPathMine
	}
	return false
}

// SpecReqTaskDynamoDBClient is the DynamoDB surface the task repository uses.
type SpecReqTaskDynamoDBClient interface {
	GetItem(context.Context, *dynamodb.GetItemInput, ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
	Query(context.Context, *dynamodb.QueryInput, ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error)
	TransactWriteItems(context.Context, *dynamodb.TransactWriteItemsInput, ...func(*dynamodb.Options)) (*dynamodb.TransactWriteItemsOutput, error)
	UpdateItem(context.Context, *dynamodb.UpdateItemInput, ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error)
}

// SpecReqTaskRow is the storage form of a task. The SpecReq title is stored
// sealed; the service layer owns sealing and unsealing.
type SpecReqTaskRow struct {
	PK                 string    `dynamodbav:"PK"`
	SK                 string    `dynamodbav:"SK"`
	UserID             string    `dynamodbav:"userId"`
	EventID            string    `dynamodbav:"eventId"`
	SourceID           string    `dynamodbav:"sourceId"`
	AccountID          string    `dynamodbav:"accountId,omitempty"`
	Path               string    `dynamodbav:"path,omitempty"`
	State              string    `dynamodbav:"state"`
	CounterpartUserID  string    `dynamodbav:"counterpartUserId,omitempty"`
	SpecReqID          string    `dynamodbav:"specReqId,omitempty"`
	SpecReqTitleSealed string    `dynamodbav:"specReqTitleSealed,omitempty"`
	SpecReqStatus      string    `dynamodbav:"specReqStatus,omitempty"`
	FailureCode        string    `dynamodbav:"failureCode,omitempty"`
	OccurrenceStartsAt time.Time `dynamodbav:"occurrenceStartsAt"`
	OccurrenceEndsAt   time.Time `dynamodbav:"occurrenceEndsAt"`
	CreatedAt          time.Time `dynamodbav:"createdAt"`
	UpdatedAt          time.Time `dynamodbav:"updatedAt"`
	StateChangedAt     time.Time `dynamodbav:"stateChangedAt"`
	Version            int64     `dynamodbav:"version"`
	ExpiresAt          int64     `dynamodbav:"pendingShareExpiresAt,omitempty"`
	EntityType         string    `dynamodbav:"entityType"`

	// PreviousState is the state the caller read before changing the row. It is
	// not stored; UpdateSpecReqTask uses it to decide whether stateChangedAt moves.
	PreviousState string `dynamodbav:"-"`
}

// SpecReqTaskRepository persists private per-occurrence SpecReq tasks.
type SpecReqTaskRepository struct {
	client    SpecReqTaskDynamoDBClient
	tableName string
	now       func() time.Time
}

// NewSpecReqTaskRepository creates a task repository.
func NewSpecReqTaskRepository(client SpecReqTaskDynamoDBClient, tableName string) *SpecReqTaskRepository {
	return &SpecReqTaskRepository{client: client, tableName: tableName, now: time.Now}
}

func validSpecReqTaskIdentity(userID, sourceID, eventID string) bool {
	return calendarStorageID.MatchString(userID) && calendarStorageID.MatchString(sourceID) && calendarStorageID.MatchString(eventID)
}

func specReqTaskKey(userID, eventID string) map[string]types.AttributeValue {
	return calendarKey(userID, model.SpecReqTaskSortKey(eventID))
}

func specReqTaskRowValid(row SpecReqTaskRow, userID string) bool {
	return row.UserID == userID && row.PK == model.PrefixUser+userID && calendarStorageID.MatchString(row.EventID) &&
		row.SK == model.SpecReqTaskSortKey(row.EventID) && row.EntityType == specReqTaskEntityType
}

// CreateSpecReqTask writes a new task only while the calendar occurrence still
// exists at eventRevision for the same user and the task is absent.
func (repo *SpecReqTaskRepository) CreateSpecReqTask(ctx context.Context, row SpecReqTaskRow, eventRevision int64) error {
	if !validSpecReqTaskIdentity(row.UserID, row.SourceID, row.EventID) || row.State == "" || eventRevision < 1 {
		return ErrInvalidSpecReqTask
	}
	row.PK, row.SK, row.EntityType = model.PrefixUser+row.UserID, model.SpecReqTaskSortKey(row.EventID), specReqTaskEntityType
	item, err := attributevalue.MarshalMap(row)
	if err != nil {
		return err
	}
	putCondition, err := expression.NewBuilder().WithCondition(expression.AttributeNotExists(expression.Name("PK"))).Build()
	if err != nil {
		return err
	}
	checkCondition, err := expression.NewBuilder().WithCondition(
		expression.AttributeExists(expression.Name("PK")).
			And(expression.Name("userId").Equal(expression.Value(row.UserID))).
			And(expression.Name("revision").Equal(expression.Value(eventRevision)))).Build()
	if err != nil {
		return err
	}
	_, err = repo.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: []types.TransactWriteItem{
		{ConditionCheck: &types.ConditionCheck{
			TableName: aws.String(repo.tableName), Key: calendarKey(row.UserID, model.CalendarEventSortKey(row.SourceID, row.EventID)),
			ConditionExpression: checkCondition.Condition(), ExpressionAttributeNames: checkCondition.Names(), ExpressionAttributeValues: checkCondition.Values(),
		}},
		{Put: &types.Put{
			TableName: aws.String(repo.tableName), Item: item,
			ConditionExpression: putCondition.Condition(), ExpressionAttributeNames: putCondition.Names(),
		}},
	}}, func(options *dynamodb.Options) { options.Retryer = aws.NopRetryer{} })
	if err != nil {
		var cancellation *types.TransactionCanceledException
		if errors.As(err, &cancellation) {
			for index, reason := range cancellation.CancellationReasons {
				if aws.ToString(reason.Code) != "ConditionalCheckFailed" {
					continue
				}
				if index == 0 {
					return errors.Join(ErrSpecReqEventGone, err)
				}
				return errors.Join(ErrConditionFailed, err)
			}
		}
		return fmt.Errorf("create specreq task: %w", err)
	}
	return nil
}

// GetSpecReqTask reads one task with a strong read. It returns nil when absent.
func (repo *SpecReqTaskRepository) GetSpecReqTask(ctx context.Context, userID, eventID string) (*SpecReqTaskRow, error) {
	if !calendarStorageID.MatchString(userID) || !calendarStorageID.MatchString(eventID) {
		return nil, ErrInvalidSpecReqTask
	}
	result, err := repo.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(repo.tableName), Key: specReqTaskKey(userID, eventID), ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return nil, fmt.Errorf("read specreq task: %w", err)
	}
	if len(result.Item) == 0 {
		return nil, nil
	}
	var row SpecReqTaskRow
	if attributevalue.UnmarshalMap(result.Item, &row) != nil || !specReqTaskRowValid(row, userID) || row.EventID != eventID {
		return nil, ErrCalendarCorruptData
	}
	return &row, nil
}

// ListSpecReqTasks returns one page of the user's tasks in sort-key order.
func (repo *SpecReqTaskRepository) ListSpecReqTasks(ctx context.Context, userID, cursor string, limit int32) ([]SpecReqTaskRow, *string, error) {
	if !calendarStorageID.MatchString(userID) || limit < 1 || limit > specReqTaskMaxLimit {
		return nil, nil, ErrInvalidSpecReqTask
	}
	scope := calendarCursor{Version: 1, Kind: specReqTaskCursorKind, UserID: userID, Limit: limit}
	lower, upper := model.PrefixSpecReqTask, model.PrefixSpecReqTask+"~"
	startKey, err := decodeCalendarCursor(cursor, scope, lower, upper)
	if err != nil {
		return nil, nil, ErrInvalidSpecReqCursor
	}
	built, err := expression.NewBuilder().WithKeyCondition(
		expression.Key("PK").Equal(expression.Value(model.PrefixUser + userID)).
			And(expression.Key("SK").BeginsWith(model.PrefixSpecReqTask))).Build()
	if err != nil {
		return nil, nil, err
	}
	result, err := repo.client.Query(ctx, &dynamodb.QueryInput{
		TableName: aws.String(repo.tableName), KeyConditionExpression: built.KeyCondition(),
		ExpressionAttributeNames: built.Names(), ExpressionAttributeValues: built.Values(),
		ConsistentRead: aws.Bool(true), ScanIndexForward: aws.Bool(true), Limit: aws.Int32(limit), ExclusiveStartKey: startKey,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("query specreq tasks: %w", err)
	}
	rows := make([]SpecReqTaskRow, 0, len(result.Items))
	for _, item := range result.Items {
		var row SpecReqTaskRow
		if attributevalue.UnmarshalMap(item, &row) != nil || !specReqTaskRowValid(row, userID) {
			return nil, nil, ErrCalendarCorruptData
		}
		rows = append(rows, row)
	}
	next, err := encodeCalendarCursor(scope, result.LastEvaluatedKey, lower, upper)
	if err != nil {
		return nil, nil, ErrInvalidSpecReqCursor
	}
	return rows, next, nil
}

// SpecReqTaskRowCursor returns the continuation cursor that resumes a task list
// of the given page size immediately after row. It performs no I/O.
func (repo *SpecReqTaskRepository) SpecReqTaskRowCursor(userID string, limit int32, row SpecReqTaskRow) (string, error) {
	if !calendarStorageID.MatchString(userID) || limit < 1 || limit > specReqTaskMaxLimit || !specReqTaskRowValid(row, userID) {
		return "", ErrInvalidSpecReqTask
	}
	scope := calendarCursor{Version: 1, Kind: specReqTaskCursorKind, UserID: userID, Limit: limit}
	lower, upper := model.PrefixSpecReqTask, model.PrefixSpecReqTask+"~"
	key := map[string]types.AttributeValue{
		"PK": &types.AttributeValueMemberS{Value: row.PK}, "SK": &types.AttributeValueMemberS{Value: row.SK},
	}
	cursor, err := encodeCalendarCursor(scope, key, lower, upper)
	if err != nil || cursor == nil {
		return "", ErrInvalidSpecReqCursor
	}
	return *cursor, nil
}

// UpdateSpecReqTask writes the mutable fields of row when the stored version
// equals expectedVersion, then bumps the version by one. A terminal state (see
// specReqTaskTerminal) also caps the row TTL at max(now + 14 days, occurrence end + 45 days), never extending it. It sets updatedAt from
// the repository clock and moves stateChangedAt only when row.State differs from
// row.PreviousState (the state the caller read). Empty optional fields are removed.
func (repo *SpecReqTaskRepository) UpdateSpecReqTask(ctx context.Context, row SpecReqTaskRow, expectedVersion int64) error {
	if !validSpecReqTaskIdentity(row.UserID, row.SourceID, row.EventID) || row.State == "" || row.PreviousState == "" || expectedVersion < 1 {
		return ErrInvalidSpecReqTask
	}
	now := repo.now().UTC()
	update := expression.Set(expression.Name("state"), expression.Value(row.State)).
		Set(expression.Name("updatedAt"), expression.Value(now)).
		Set(expression.Name("version"), expression.Value(expectedVersion+1))
	if row.State != row.PreviousState {
		update = update.Set(expression.Name("stateChangedAt"), expression.Value(now))
	}
	// A terminal row expires at min(existing TTL, max(now + 14 days, occurrence end
	// + 45 days)), so it survives while its occurrence is in the matching horizon. row.ExpiresAt is
	// the value the caller read; the version condition below guarantees it is current.
	if specReqTaskTerminal(row) {
		cap := now.Add(specReqTerminalRetention).Unix()
		if horizon := row.OccurrenceEndsAt.Add(specReqHorizonRetention).Unix(); horizon > cap {
			cap = horizon
		}
		if row.ExpiresAt == 0 || cap < row.ExpiresAt {
			update = update.Set(expression.Name("pendingShareExpiresAt"), expression.Value(cap))
		}
	}
	for _, field := range []struct{ name, value string }{
		{"accountId", row.AccountID}, {"path", row.Path}, {"counterpartUserId", row.CounterpartUserID},
		{"specReqId", row.SpecReqID}, {"specReqTitleSealed", row.SpecReqTitleSealed},
		{"specReqStatus", row.SpecReqStatus}, {"failureCode", row.FailureCode},
	} {
		if field.value == "" {
			update = update.Remove(expression.Name(field.name))
		} else {
			update = update.Set(expression.Name(field.name), expression.Value(field.value))
		}
	}
	condition := expression.AttributeExists(expression.Name("PK")).
		And(expression.Name("version").Equal(expression.Value(expectedVersion)))
	built, err := expression.NewBuilder().WithUpdate(update).WithCondition(condition).Build()
	if err != nil {
		return err
	}
	_, err = repo.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(repo.tableName), Key: specReqTaskKey(row.UserID, row.EventID),
		UpdateExpression: built.Update(), ConditionExpression: built.Condition(),
		ExpressionAttributeNames: built.Names(), ExpressionAttributeValues: built.Values(),
	})
	if err != nil {
		var failed *types.ConditionalCheckFailedException
		if errors.As(err, &failed) {
			return errors.Join(ErrConditionFailed, err)
		}
		return fmt.Errorf("update specreq task: %w", err)
	}
	return nil
}
