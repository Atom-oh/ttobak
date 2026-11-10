package repository

import (
	"context"
	"errors"
	"fmt"
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
	ErrInvalidPerformanceBatch = errors.New("invalid performance batch")
	ErrInvalidPerformanceInput = errors.New("invalid performance input")
	ErrPerformanceCorruptData  = errors.New("invalid performance storage identity")

	performanceUserID   = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	performanceExternal = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)
	performanceDigest   = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

const (
	performanceMaxMutations = 20
	performanceReceiptTTL   = 7 * 24 * time.Hour
	performanceMinYear      = 2000
	performanceMaxYear      = 2100
)

type performanceBatchReceipt struct {
	PK            string                       `dynamodbav:"PK"`
	SK            string                       `dynamodbav:"SK"`
	UserID        string                       `dynamodbav:"userId"`
	Source        string                       `dynamodbav:"source"`
	PayloadDigest string                       `dynamodbav:"payloadDigest"`
	Receipt       model.PerformanceSyncReceipt `dynamodbav:"receipt"`
	ExpiresAt     int64                        `dynamodbav:"pendingShareExpiresAt"`
}

type performanceAssignmentRow struct {
	PK     string `dynamodbav:"PK"`
	SK     string `dynamodbav:"SK"`
	UserID string `dynamodbav:"userId"`
	model.SFDCAssignment
}

type performanceTaskRow struct {
	PK        string `dynamodbav:"PK"`
	SK        string `dynamodbav:"SK"`
	UserID    string `dynamodbav:"userId"`
	ExpiresAt int64  `dynamodbav:"pendingShareExpiresAt"`
	model.SFDCTask
}

type performanceMappingRow struct {
	PK     string `dynamodbav:"PK"`
	SK     string `dynamodbav:"SK"`
	UserID string `dynamodbav:"userId"`
	model.AccountMapping
}

type performanceGoalsRow struct {
	PK        string `dynamodbav:"PK"`
	SK        string `dynamodbav:"SK"`
	UserID    string `dynamodbav:"userId"`
	ExpiresAt int64  `dynamodbav:"pendingShareExpiresAt"`
	model.PerformanceGoals
}

func performanceKey(userID, sortKey string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		"PK": &types.AttributeValueMemberS{Value: model.PrefixUser + userID},
		"SK": &types.AttributeValueMemberS{Value: sortKey},
	}
}

// performanceYearExpiry returns 00:00 UTC on January 1 of year+2.
func performanceYearExpiry(year int) int64 {
	return time.Date(year+2, time.January, 1, 0, 0, 0, 0, time.UTC).Unix()
}

func performanceTaskExpiry(activityDate string) (int64, error) {
	date, err := time.Parse("2006-01-02", activityDate)
	if err != nil {
		return 0, ErrInvalidPerformanceBatch
	}
	return performanceYearExpiry(date.Year()), nil
}

func validPerformanceSource(source string) bool {
	return source == model.PerformanceSourceSFDCAssignments || source == model.PerformanceSourceSFDCTasks
}

func performanceScopeCondition(userID, source string) expression.ConditionBuilder {
	return expression.Name("userId").Equal(expression.Value(userID)).
		And(expression.Name("source").Equal(expression.Value(source)))
}

// ApplyPerformanceBatch applies one revisioned sync batch in a single
// transaction: the source revision update, the row writes, and the batch
// receipt. An identical retry returns the saved receipt; a reused batch ID
// with a different digest, or a stale revision, returns ErrConditionFailed.
func (r *DynamoDBRepository) ApplyPerformanceBatch(ctx context.Context, userID, source string, req model.PerformanceSyncRequest, digest string) (*model.PerformanceSyncReceipt, error) {
	rows, receipt, err := buildPerformanceRows(userID, source, req, digest)
	if err != nil {
		return nil, err
	}
	saved, err := r.getPerformanceReceipt(ctx, userID, source, req.BatchID)
	if err != nil {
		return nil, err
	}
	if saved != nil {
		return performanceRetryReceipt(saved, digest)
	}
	now := time.Now().UTC()
	sourceUpdate, err := r.performanceSourceUpdate(userID, source, req, receipt.Revision, now)
	if err != nil {
		return nil, err
	}
	operations := []types.TransactWriteItem{{Update: sourceUpdate}}
	for _, row := range rows {
		operation, err := r.performanceOperation(userID, row)
		if err != nil {
			return nil, err
		}
		operations = append(operations, operation)
	}
	record := performanceBatchReceipt{
		PK: model.PrefixUser + userID, SK: model.PerformanceBatchSortKey(source, req.BatchID),
		UserID: userID, Source: source, PayloadDigest: digest, Receipt: *receipt,
		ExpiresAt: now.Add(performanceReceiptTTL).Unix(),
	}
	condition := expression.AttributeNotExists(expression.Name("PK")).Or(
		expression.Name("pendingShareExpiresAt").LessThanEqual(expression.Value(now.Unix())).
			And(performanceScopeCondition(userID, source)))
	receiptPut, err := r.performancePut(record, &condition)
	if err != nil {
		return nil, err
	}
	operations = append(operations, types.TransactWriteItem{Put: receiptPut})
	_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: operations},
		func(options *dynamodb.Options) { options.Retryer = aws.NopRetryer{} })
	if err != nil {
		// The outcome may be ambiguous: a lost response still leaves the receipt.
		saved, readErr := r.getPerformanceReceipt(ctx, userID, source, req.BatchID)
		if readErr != nil {
			return nil, errors.Join(fmt.Errorf("performance transaction: %w", err), readErr)
		}
		if saved != nil {
			return performanceRetryReceipt(saved, digest)
		}
		if isConditionalCheckFailedTransaction(err) {
			return nil, errors.Join(ErrConditionFailed, err)
		}
		return nil, fmt.Errorf("performance transaction: %w", err)
	}
	return receipt, nil
}

type performanceRow struct {
	sortKey string
	remove  bool
	item    any
}

func buildPerformanceRows(userID, source string, req model.PerformanceSyncRequest, digest string) ([]performanceRow, *model.PerformanceSyncReceipt, error) {
	if !performanceUserID.MatchString(userID) || !validPerformanceSource(source) || uuid.Validate(req.BatchID) != nil ||
		!performanceDigest.MatchString(digest) || req.ExpectedRevision < 0 || req.ExpectedRevision == 1<<63-1 ||
		len(req.Mutations) > performanceMaxMutations {
		return nil, nil, ErrInvalidPerformanceBatch
	}
	pk := model.PrefixUser + userID
	seen := make(map[string]bool)
	rows := make([]performanceRow, 0, len(req.Mutations))
	receipt := &model.PerformanceSyncReceipt{Revision: req.ExpectedRevision + 1, Completed: req.Complete}
	for _, mutation := range req.Mutations {
		var sortKey string
		var item any
		switch source {
		case model.PerformanceSourceSFDCAssignments:
			if mutation.Task != nil {
				return nil, nil, ErrInvalidPerformanceBatch
			}
			id := mutation.ExternalID
			if mutation.Op == "upsert" {
				a := mutation.Assignment
				if a == nil || mutation.ExternalID != "" || !performanceExternal.MatchString(a.SFDCAccountID) ||
					!utf8.ValidString(a.Name) || len(a.Name) > 512 || !utf8.ValidString(a.Role) || len(a.Role) > 64 {
					return nil, nil, ErrInvalidPerformanceBatch
				}
				id = a.SFDCAccountID
				sortKey = model.PerformanceAssignmentSortKey(id)
				item = performanceAssignmentRow{PK: pk, SK: sortKey, UserID: userID, SFDCAssignment: *a}
			} else {
				sortKey = model.PerformanceAssignmentSortKey(id)
			}
		case model.PerformanceSourceSFDCTasks:
			if mutation.Assignment != nil {
				return nil, nil, ErrInvalidPerformanceBatch
			}
			id := mutation.ExternalID
			if mutation.Op == "upsert" {
				t := mutation.Task
				if t == nil || mutation.ExternalID != "" || !performanceExternal.MatchString(t.TaskID) ||
					(t.SFDCAccountID != "" && !performanceExternal.MatchString(t.SFDCAccountID)) || !performanceTaskText(t) {
					return nil, nil, ErrInvalidPerformanceBatch
				}
				expires, err := performanceTaskExpiry(t.ActivityDate)
				if err != nil {
					return nil, nil, err
				}
				if t.SpecReqID != "" && !performanceExternal.MatchString(t.SpecReqID) {
					return nil, nil, ErrInvalidPerformanceBatch
				}
				id = t.TaskID
				sortKey = model.PerformanceTaskSortKey(id)
				item = performanceTaskRow{PK: pk, SK: sortKey, UserID: userID, ExpiresAt: expires, SFDCTask: *t}
			} else {
				sortKey = model.PerformanceTaskSortKey(id)
			}
		}
		switch mutation.Op {
		case "upsert":
			receipt.Upserted++
		case "remove":
			if !performanceExternal.MatchString(mutation.ExternalID) || mutation.Assignment != nil || mutation.Task != nil {
				return nil, nil, ErrInvalidPerformanceBatch
			}
			receipt.Removed++
		default:
			return nil, nil, ErrInvalidPerformanceBatch
		}
		if seen[sortKey] {
			return nil, nil, ErrInvalidPerformanceBatch
		}
		seen[sortKey] = true
		rows = append(rows, performanceRow{sortKey: sortKey, remove: mutation.Op == "remove", item: item})
	}
	return rows, receipt, nil
}

func performanceTaskText(t *model.SFDCTask) bool {
	for _, value := range []string{t.Type, t.Subject, t.Status} {
		if !utf8.ValidString(value) || len(value) > 1024 {
			return false
		}
	}
	return true
}

func (r *DynamoDBRepository) performanceSourceUpdate(userID, source string, req model.PerformanceSyncRequest, revision int64, now time.Time) (*types.Update, error) {
	condition := expression.AttributeNotExists(expression.Name("PK"))
	if req.ExpectedRevision > 0 {
		condition = performanceScopeCondition(userID, source).
			And(expression.Name("revision").Equal(expression.Value(req.ExpectedRevision)))
	}
	stamp := now.Format(time.RFC3339)
	update := expression.Set(expression.Name("userId"), expression.Value(userID)).
		Set(expression.Name("source"), expression.Value(source)).
		Set(expression.Name("revision"), expression.Value(revision)).
		Set(expression.Name("lastAttemptAt"), expression.Value(stamp))
	if req.Complete {
		update = update.Set(expression.Name("lastCompletedAt"), expression.Value(stamp))
	}
	built, err := expression.NewBuilder().WithUpdate(update).WithCondition(condition).Build()
	if err != nil {
		return nil, err
	}
	return &types.Update{TableName: aws.String(r.tableName), Key: performanceKey(userID, model.PerformanceSourceSortKey(source)),
		UpdateExpression: built.Update(), ConditionExpression: built.Condition(),
		ExpressionAttributeNames: built.Names(), ExpressionAttributeValues: built.Values()}, nil
}

func (r *DynamoDBRepository) performanceOperation(userID string, row performanceRow) (types.TransactWriteItem, error) {
	if row.remove {
		return types.TransactWriteItem{Delete: &types.Delete{TableName: aws.String(r.tableName), Key: performanceKey(userID, row.sortKey)}}, nil
	}
	put, err := r.performancePut(row.item, nil)
	if err != nil {
		return types.TransactWriteItem{}, err
	}
	return types.TransactWriteItem{Put: put}, nil
}

func (r *DynamoDBRepository) performancePut(value any, condition *expression.ConditionBuilder) (*types.Put, error) {
	item, err := attributevalue.MarshalMap(value)
	if err != nil {
		return nil, err
	}
	put := &types.Put{TableName: aws.String(r.tableName), Item: item}
	if condition != nil {
		built, err := expression.NewBuilder().WithCondition(*condition).Build()
		if err != nil {
			return nil, err
		}
		put.ConditionExpression = built.Condition()
		put.ExpressionAttributeNames, put.ExpressionAttributeValues = built.Names(), built.Values()
	}
	return put, nil
}

func (r *DynamoDBRepository) getPerformanceReceipt(ctx context.Context, userID, source, batchID string) (*performanceBatchReceipt, error) {
	sortKey := model.PerformanceBatchSortKey(source, batchID)
	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName), Key: performanceKey(userID, sortKey), ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return nil, fmt.Errorf("read performance receipt: %w", err)
	}
	if len(result.Item) == 0 {
		return nil, nil
	}
	var saved performanceBatchReceipt
	if err := attributevalue.UnmarshalMap(result.Item, &saved); err != nil ||
		saved.PK != model.PrefixUser+userID || saved.SK != sortKey || saved.UserID != userID || saved.Source != source ||
		saved.Receipt.Revision < 1 || !performanceDigest.MatchString(saved.PayloadDigest) {
		return nil, ErrPerformanceCorruptData
	}
	if saved.ExpiresAt <= time.Now().Unix() {
		return nil, nil
	}
	return &saved, nil
}

func performanceRetryReceipt(saved *performanceBatchReceipt, digest string) (*model.PerformanceSyncReceipt, error) {
	if saved.PayloadDigest != digest {
		return nil, ErrConditionFailed
	}
	receipt := saved.Receipt
	receipt.Replayed = true
	return &receipt, nil
}

// queryPerformanceRows pages through every row under prefix in the owner's partition.
func (r *DynamoDBRepository) queryPerformanceRows(ctx context.Context, userID, prefix string) ([]map[string]types.AttributeValue, error) {
	if !performanceUserID.MatchString(userID) {
		return nil, ErrInvalidPerformanceInput
	}
	built, err := expression.NewBuilder().WithKeyCondition(
		expression.Key("PK").Equal(expression.Value(model.PrefixUser + userID)).
			And(expression.Key("SK").BeginsWith(prefix))).Build()
	if err != nil {
		return nil, err
	}
	var items []map[string]types.AttributeValue
	var startKey map[string]types.AttributeValue
	for {
		result, err := r.client.Query(ctx, &dynamodb.QueryInput{
			TableName: aws.String(r.tableName), KeyConditionExpression: built.KeyCondition(),
			ExpressionAttributeNames: built.Names(), ExpressionAttributeValues: built.Values(),
			ConsistentRead: aws.Bool(true), ExclusiveStartKey: startKey,
		})
		if err != nil {
			return nil, fmt.Errorf("query performance rows: %w", err)
		}
		items = append(items, result.Items...)
		if len(result.LastEvaluatedKey) == 0 {
			return items, nil
		}
		startKey = result.LastEvaluatedKey
	}
}

// ListPerformanceTasks returns the owner's tasks with activityDate in the
// inclusive YYYY-MM-DD range. An empty bound is open.
func (r *DynamoDBRepository) ListPerformanceTasks(ctx context.Context, userID string, from, to string) ([]model.SFDCTask, error) {
	for _, bound := range []string{from, to} {
		if bound == "" {
			continue
		}
		if _, err := time.Parse("2006-01-02", bound); err != nil {
			return nil, ErrInvalidPerformanceInput
		}
	}
	items, err := r.queryPerformanceRows(ctx, userID, model.PrefixPerformanceTask)
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	tasks := make([]model.SFDCTask, 0, len(items))
	for _, item := range items {
		var row performanceTaskRow
		if attributevalue.UnmarshalMap(item, &row) != nil || row.PK != model.PrefixUser+userID || row.UserID != userID ||
			row.SK != model.PerformanceTaskSortKey(row.TaskID) {
			continue
		}
		if row.ExpiresAt != 0 && row.ExpiresAt <= now { // TTL deletion lags; hide expired rows.
			continue
		}
		if from != "" && row.ActivityDate < from || to != "" && row.ActivityDate > to {
			continue
		}
		tasks = append(tasks, row.SFDCTask)
	}
	return tasks, nil
}

func (r *DynamoDBRepository) ListPerformanceAssignments(ctx context.Context, userID string) ([]model.SFDCAssignment, error) {
	items, err := r.queryPerformanceRows(ctx, userID, model.PrefixPerformanceAssignment)
	if err != nil {
		return nil, err
	}
	assignments := make([]model.SFDCAssignment, 0, len(items))
	for _, item := range items {
		var row performanceAssignmentRow
		if attributevalue.UnmarshalMap(item, &row) != nil || row.PK != model.PrefixUser+userID || row.UserID != userID ||
			row.SK != model.PerformanceAssignmentSortKey(row.SFDCAccountID) {
			continue
		}
		assignments = append(assignments, row.SFDCAssignment)
	}
	return assignments, nil
}

func (r *DynamoDBRepository) ListAccountMappings(ctx context.Context, userID string) ([]model.AccountMapping, error) {
	items, err := r.queryPerformanceRows(ctx, userID, model.PrefixPerformanceMapping)
	if err != nil {
		return nil, err
	}
	mappings := make([]model.AccountMapping, 0, len(items))
	for _, item := range items {
		var row performanceMappingRow
		if attributevalue.UnmarshalMap(item, &row) != nil || row.PK != model.PrefixUser+userID || row.UserID != userID ||
			row.SK != model.PerformanceMappingSortKey(row.SFDCAccountID) {
			continue
		}
		mappings = append(mappings, row.AccountMapping)
	}
	return mappings, nil
}

// PutAccountMapping stores a mapping. With onlyIfNotManual, an existing manual
// mapping is kept and ErrConditionFailed is returned.
func (r *DynamoDBRepository) PutAccountMapping(ctx context.Context, userID string, m model.AccountMapping, onlyIfNotManual bool) error {
	if !performanceUserID.MatchString(userID) || !performanceExternal.MatchString(m.SFDCAccountID) ||
		!performanceUserID.MatchString(m.AccountID) {
		return ErrInvalidPerformanceInput
	}
	if m.UpdatedAt == "" {
		m.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	sortKey := model.PerformanceMappingSortKey(m.SFDCAccountID)
	var condition *expression.ConditionBuilder
	if onlyIfNotManual {
		c := expression.AttributeNotExists(expression.Name("PK")).Or(expression.Name("manual").Equal(expression.Value(false)))
		condition = &c
	}
	put, err := r.performancePut(performanceMappingRow{PK: model.PrefixUser + userID, SK: sortKey, UserID: userID, AccountMapping: m}, condition)
	if err != nil {
		return err
	}
	_, err = r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: put.TableName, Item: put.Item, ConditionExpression: put.ConditionExpression,
		ExpressionAttributeNames: put.ExpressionAttributeNames, ExpressionAttributeValues: put.ExpressionAttributeValues,
	})
	if err != nil {
		var rejected *types.ConditionalCheckFailedException
		if errors.As(err, &rejected) {
			return errors.Join(ErrConditionFailed, err)
		}
		return fmt.Errorf("put account mapping: %w", err)
	}
	return nil
}

func validPerformanceYear(year int) bool {
	return year >= performanceMinYear && year <= performanceMaxYear
}

// GetPerformanceGoals returns nil when no goals are stored for the year.
func (r *DynamoDBRepository) GetPerformanceGoals(ctx context.Context, userID string, year int) (*model.PerformanceGoals, error) {
	if !performanceUserID.MatchString(userID) || !validPerformanceYear(year) {
		return nil, ErrInvalidPerformanceInput
	}
	sortKey := model.PerformanceGoalsSortKey(year)
	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName), Key: performanceKey(userID, sortKey), ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return nil, fmt.Errorf("read performance goals: %w", err)
	}
	if len(result.Item) == 0 {
		return nil, nil
	}
	var row performanceGoalsRow
	if err := attributevalue.UnmarshalMap(result.Item, &row); err != nil || row.PK != model.PrefixUser+userID ||
		row.SK != sortKey || row.UserID != userID || row.Year != year {
		return nil, ErrPerformanceCorruptData
	}
	goals := row.PerformanceGoals
	return &goals, nil
}

func (r *DynamoDBRepository) PutPerformanceGoals(ctx context.Context, userID string, g model.PerformanceGoals) error {
	if !performanceUserID.MatchString(userID) || !validPerformanceYear(g.Year) || !utf8.ValidString(g.Text) ||
		len(g.Text) > model.PerformanceGoalsMaxBytes || strings.ContainsRune(g.Text, 0) {
		return ErrInvalidPerformanceInput
	}
	if g.UpdatedAt == "" {
		g.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	put, err := r.performancePut(performanceGoalsRow{
		PK: model.PrefixUser + userID, SK: model.PerformanceGoalsSortKey(g.Year), UserID: userID,
		ExpiresAt: performanceYearExpiry(g.Year), PerformanceGoals: g,
	}, nil)
	if err != nil {
		return err
	}
	if _, err := r.client.PutItem(ctx, &dynamodb.PutItemInput{TableName: put.TableName, Item: put.Item}); err != nil {
		return fmt.Errorf("put performance goals: %w", err)
	}
	return nil
}

// ListPerformanceSources returns the owner's stored source states. Rows that
// fail identity checks or name an unknown source are skipped.
func (r *DynamoDBRepository) ListPerformanceSources(ctx context.Context, userID string) ([]model.PerformanceSourceStatus, error) {
	items, err := r.queryPerformanceRows(ctx, userID, model.PrefixPerformanceSource)
	if err != nil {
		return nil, err
	}
	sources := make([]model.PerformanceSourceStatus, 0, len(items))
	for _, item := range items {
		var row struct {
			PK     string `dynamodbav:"PK"`
			SK     string `dynamodbav:"SK"`
			UserID string `dynamodbav:"userId"`
			model.PerformanceSourceStatus
		}
		if attributevalue.UnmarshalMap(item, &row) != nil || row.PK != model.PrefixUser+userID || row.UserID != userID ||
			!validPerformanceSource(row.Source) || row.SK != model.PerformanceSourceSortKey(row.Source) || row.Revision < 0 {
			continue
		}
		sources = append(sources, row.PerformanceSourceStatus)
	}
	return sources, nil
}
