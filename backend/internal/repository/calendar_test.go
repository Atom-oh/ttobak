package repository

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
)

const calendarTestSource = "b7aa27f9-9878-4f52-9b54-a3b73a941d31"
const calendarTestBatch = "90ec2d25-a32e-43a5-96a9-e203859b7d19"

var calendarTestNow = time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)

type calendarSDKMock struct {
	test        *testing.T
	get         func(*dynamodb.GetItemInput) (*dynamodb.GetItemOutput, error)
	query       func(*dynamodb.QueryInput) (*dynamodb.QueryOutput, error)
	transact    func(*dynamodb.TransactWriteItemsInput) (*dynamodb.TransactWriteItemsOutput, error)
	transactGet func(*dynamodb.TransactGetItemsInput) (*dynamodb.TransactGetItemsOutput, error)
}

func (mock *calendarSDKMock) GetItem(_ context.Context, input *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	mock.test.Helper()
	if !aws.ToBool(input.ConsistentRead) || aws.ToString(input.TableName) != "calendar-test" {
		mock.test.Fatal("canonical reads must be consistent and use the configured table")
	}
	if mock.get == nil {
		mock.test.Fatalf("unexpected GetItem: %v", input.Key)
	}
	return mock.get(input)
}

func (mock *calendarSDKMock) Query(_ context.Context, input *dynamodb.QueryInput, _ ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	mock.test.Helper()
	if mock.query == nil {
		mock.test.Fatalf("unexpected Query: %v", input)
	}
	return mock.query(input)
}

func (mock *calendarSDKMock) TransactGetItems(_ context.Context, input *dynamodb.TransactGetItemsInput, _ ...func(*dynamodb.Options)) (*dynamodb.TransactGetItemsOutput, error) {
	mock.test.Helper()
	if len(input.TransactItems) == 0 || len(input.TransactItems) > 100 {
		mock.test.Fatalf("a snapshot must read 1-100 items: %d", len(input.TransactItems))
	}
	for _, item := range input.TransactItems {
		if item.Get == nil || aws.ToString(item.Get.TableName) != "calendar-test" {
			mock.test.Fatal("snapshot reads must use the configured table")
		}
	}
	if mock.transactGet == nil {
		mock.test.Fatal("unexpected snapshot read")
	}
	return mock.transactGet(input)
}

func (mock *calendarSDKMock) TransactWriteItems(_ context.Context, input *dynamodb.TransactWriteItemsInput, options ...func(*dynamodb.Options)) (*dynamodb.TransactWriteItemsOutput, error) {
	mock.test.Helper()
	settings := dynamodb.Options{}
	for _, option := range options {
		option(&settings)
	}
	if settings.Retryer == nil || settings.Retryer.MaxAttempts() != 1 {
		mock.test.Fatal("ambiguous transactions must not be automatically rebound or retried")
	}
	if mock.transact == nil {
		mock.test.Fatal("unexpected transaction")
	}
	return mock.transact(input)
}

func calendarTestRepo(test *testing.T, mock *calendarSDKMock) *CalendarRepository {
	test.Helper()
	mock.test = test
	repo := NewCalendarRepository(mock, "calendar-test")
	repo.now = func() time.Time { return calendarTestNow }
	return repo
}

func calendarTestRow() CalendarEventRow {
	return CalendarEventRow{
		PK: "USER#owner", SK: "CALEVENT#" + calendarTestSource + "#event-1",
		UserID: "owner", SourceID: calendarTestSource, EventID: "event-1",
		StartsAt: calendarTestNow.Add(time.Hour), EndsAt: calendarTestNow.Add(2 * time.Hour),
		AccountID: "account-1", Ciphertext: "encrypted-payload", Revision: 1,
		ExpiresAt: calendarTestNow.Add(2*time.Hour + 90*24*time.Hour).Unix(),
	}
}

func calendarTestWrite() CalendarWriteBatch {
	row := calendarTestRow()
	return CalendarWriteBatch{
		ExpectedRevision: 0, PayloadDigest: strings.Repeat("a", 64),
		AcceptedAt: calendarTestNow, WindowStart: calendarTestNow, WindowEnd: calendarTestNow.Add(30 * 24 * time.Hour),
		ExpiresAt: calendarTestNow.Add(7 * 24 * time.Hour).Unix(),
		Receipt:   model.CalendarSyncReceipt{BatchID: calendarTestBatch, Revision: 1, Upserted: 1},
		Mutations: []CalendarWriteMutation{{Op: "upsert", EventID: row.EventID, ExternalID: "provider-occurrence-1", Event: &row}},
	}
}

func calendarTestItem(test *testing.T, value any) map[string]types.AttributeValue {
	test.Helper()
	item, err := attributevalue.MarshalMap(value)
	if err != nil {
		test.Fatal(err)
	}
	return item
}

func calendarTestString(test *testing.T, item map[string]types.AttributeValue, name string) string {
	test.Helper()
	value, ok := item[name].(*types.AttributeValueMemberS)
	if !ok {
		test.Fatalf("missing string %s in %v", name, item)
	}
	return value.Value
}

func calendarTestExpression(text *string, names map[string]string, values map[string]types.AttributeValue) string {
	return regexp.MustCompile(`[#:][0-9]+`).ReplaceAllStringFunc(aws.ToString(text), func(alias string) string {
		replacement, exists := names[alias]
		if !exists {
			var value any
			_ = attributevalue.Unmarshal(values[alias], &value)
			replacement = fmt.Sprint(value)
		}
		return replacement
	})
}

func calendarTestReceipt(batch CalendarWriteBatch) CalendarBatchReceipt {
	return CalendarBatchReceipt{
		PK: "USER#owner", SK: "CALBATCH#" + calendarTestSource + "#" + batch.Receipt.BatchID,
		UserID: "owner", SourceID: calendarTestSource, PayloadDigest: batch.PayloadDigest,
		Receipt: batch.Receipt, ExpiresAt: batch.ExpiresAt,
	}
}

func TestCalendarIdenticalRetryReturnsSavedReceiptWithoutMutation(test *testing.T) {
	batch := calendarTestWrite()
	saved := calendarTestReceipt(batch)
	mock := &calendarSDKMock{get: func(input *dynamodb.GetItemInput) (*dynamodb.GetItemOutput, error) {
		if calendarTestString(test, input.Key, "PK") != "USER#owner" || calendarTestString(test, input.Key, "SK") != saved.SK {
			test.Fatal("receipt lookup escaped caller/source/batch scope")
		}
		return &dynamodb.GetItemOutput{Item: calendarTestItem(test, saved)}, nil
	}}
	batch.Mutations[0].Event.Ciphertext = "new-randomized-ciphertext"
	receipt, err := calendarTestRepo(test, mock).ApplyCalendarBatch(context.Background(), "owner", calendarTestSource, batch)
	if err != nil || !reflect.DeepEqual(receipt, &saved.Receipt) {
		test.Fatalf("logical retry must return the original receipt: %v, %v", receipt, err)
	}
}

func TestCalendarBatchIDDifferentDigestConflicts(test *testing.T) {
	batch := calendarTestWrite()
	saved := calendarTestReceipt(batch)
	saved.PayloadDigest = strings.Repeat("b", 64)
	mock := &calendarSDKMock{get: func(*dynamodb.GetItemInput) (*dynamodb.GetItemOutput, error) {
		return &dynamodb.GetItemOutput{Item: calendarTestItem(test, saved)}, nil
	}}
	if _, err := calendarTestRepo(test, mock).ApplyCalendarBatch(context.Background(), "owner", calendarTestSource, batch); !errors.Is(err, ErrConditionFailed) {
		test.Fatalf("batch ID reuse must conflict before writes: %v", err)
	}
}

func TestCalendarBatchWritesSourceEventReferenceReceiptAtomically(test *testing.T) {
	batch := calendarTestWrite()
	writes := 0
	mock := &calendarSDKMock{
		get: func(*dynamodb.GetItemInput) (*dynamodb.GetItemOutput, error) { return &dynamodb.GetItemOutput{}, nil },
		transact: func(input *dynamodb.TransactWriteItemsInput) (*dynamodb.TransactWriteItemsOutput, error) {
			writes++
			if len(input.TransactItems) != 4 {
				test.Fatalf("all four rows must publish together: %d", len(input.TransactItems))
			}
			var sourceFound, canonicalFound, referenceFound, receiptFound bool
			for _, operation := range input.TransactItems {
				if operation.Update != nil {
					sourceFound = true
					update := operation.Update
					condition := calendarTestExpression(update.ConditionExpression, update.ExpressionAttributeNames, update.ExpressionAttributeValues)
					if condition != "attribute_not_exists (PK)" {
						test.Fatalf("initial source must require absence: %s", condition)
					}
					fields := calendarTestExpression(update.UpdateExpression, update.ExpressionAttributeNames, update.ExpressionAttributeValues)
					for _, wanted := range []string{"userId = owner", "sourceId = " + calendarTestSource, "provider = outlook", "revision = 1", "lastAttemptAt"} {
						if !strings.Contains(fields, wanted) {
							test.Errorf("source metadata missing %s: %s", wanted, fields)
						}
					}
					if strings.Contains(fields, "lastCompletedAt") || strings.Contains(fields, "pendingShareExpiresAt") {
						test.Fatal("incomplete source must preserve completion metadata and have no TTL")
					}
					continue
				}
				if operation.Put == nil {
					test.Fatalf("incomplete input must not infer deletes: %+v", operation)
				}
				item := operation.Put.Item
				if calendarTestString(test, item, "PK") != "USER#owner" || calendarTestString(test, item, "userId") != "owner" {
					test.Fatal("every calendar row must carry caller scope")
				}
				sortKey := calendarTestString(test, item, "SK")
				var expiresAt int64
				if err := attributevalue.Unmarshal(item["pendingShareExpiresAt"], &expiresAt); err != nil {
					test.Fatal(err)
				}
				switch {
				case strings.HasPrefix(sortKey, "CALEVENT#"):
					canonicalFound = true
					if calendarTestString(test, item, "ciphertext") != "encrypted-payload" || expiresAt != batch.Mutations[0].Event.EndsAt.Add(90*24*time.Hour).Unix() {
						test.Fatal("canonical ciphertext or 90-day retention lost")
					}
				case strings.HasPrefix(sortKey, "CALTIME#"):
					referenceFound = true
					if expiresAt != batch.Mutations[0].Event.ExpiresAt {
						test.Fatal("reference TTL must match event TTL")
					}
					if _, exists := item["ciphertext"]; exists {
						test.Fatal("references must contain only identities and timestamps")
					}
				case strings.HasPrefix(sortKey, "CALBATCH#"):
					receiptFound = true
					if expiresAt != calendarTestNow.Add(7*24*time.Hour).Unix() {
						test.Fatal("receipts must expire seven days after acceptance")
					}
				default:
					test.Fatalf("unexpected calendar row: %s", sortKey)
				}
				for _, forbidden := range []string{"title", "location", "externalId"} {
					if _, exists := item[forbidden]; exists {
						test.Fatalf("plaintext %s leaked into storage", forbidden)
					}
				}
			}
			if !sourceFound || !canonicalFound || !referenceFound || !receiptFound {
				test.Fatal("transaction omitted a calendar entity")
			}
			return &dynamodb.TransactWriteItemsOutput{}, nil
		},
	}
	receipt, err := calendarTestRepo(test, mock).ApplyCalendarBatch(context.Background(), "owner", calendarTestSource, batch)
	if err != nil || !reflect.DeepEqual(receipt, &batch.Receipt) || writes != 1 {
		test.Fatalf("single atomic acceptance failed: %v, %v, writes=%d", receipt, err, writes)
	}
}

func TestCalendarStaleRevisionRejectsWholeReschedule(test *testing.T) {
	batch := calendarTestWrite()
	batch.ExpectedRevision = 4
	batch.Receipt.Revision = 5
	batch.Mutations[0].Event.Revision = 5
	old := calendarTestRow()
	old.Revision = 4
	old.StartsAt = calendarTestNow.Add(-time.Hour)
	old.EndsAt = calendarTestNow.Add(time.Hour)
	old.ExpiresAt = old.EndsAt.Add(90 * 24 * time.Hour).Unix()
	canonicalReads := 0
	writes := 0
	mock := &calendarSDKMock{
		get: func(input *dynamodb.GetItemInput) (*dynamodb.GetItemOutput, error) {
			if calendarTestString(test, input.Key, "SK") == old.SK {
				canonicalReads++
				return &dynamodb.GetItemOutput{Item: calendarTestItem(test, old)}, nil
			}
			return &dynamodb.GetItemOutput{}, nil
		},
		transact: func(input *dynamodb.TransactWriteItemsInput) (*dynamodb.TransactWriteItemsOutput, error) {
			writes++
			if canonicalReads != 1 || len(input.TransactItems) != 5 {
				test.Fatal("reschedule must consistently read canonical state before replacing both references")
			}
			var guarded, oldDeleted, newWritten bool
			for _, operation := range input.TransactItems {
				if operation.Update != nil {
					update := operation.Update
					condition := calendarTestExpression(update.ConditionExpression, update.ExpressionAttributeNames, update.ExpressionAttributeValues)
					for _, expected := range []string{"revision = 4", "userId = owner", "sourceId = " + calendarTestSource} {
						if !strings.Contains(condition, expected) {
							test.Errorf("source CAS missing %s: %s", expected, condition)
						}
					}
					guarded = true
				}
				if operation.Delete != nil {
					oldDeleted = calendarTestString(test, operation.Delete.Key, "SK") == "CALTIME#2026-11-07T11:00:00.000000000Z#"+calendarTestSource+"#event-1"
				}
				if operation.Put != nil && strings.HasPrefix(calendarTestString(test, operation.Put.Item, "SK"), "CALTIME#") {
					newWritten = calendarTestString(test, operation.Put.Item, "SK") == "CALTIME#2026-11-07T13:00:00.000000000Z#"+calendarTestSource+"#event-1"
				}
			}
			if !guarded || !oldDeleted || !newWritten {
				test.Fatal("reschedule/source fence not in the same transaction")
			}
			return nil, &types.TransactionCanceledException{CancellationReasons: []types.CancellationReason{{Code: aws.String("ConditionalCheckFailed")}}}
		},
	}
	if _, err := calendarTestRepo(test, mock).ApplyCalendarBatch(context.Background(), "owner", calendarTestSource, batch); !errors.Is(err, ErrConditionFailed) || writes != 1 {
		test.Fatalf("stale transaction must fail without a fallback write: %v, %d", err, writes)
	}
}

func TestCalendarDuplicateExternalOrEventIdentityRejectedBeforeReads(test *testing.T) {
	for _, sameExternal := range []bool{true, false} {
		test.Run(fmt.Sprint(sameExternal), func(test *testing.T) {
			batch := calendarTestWrite()
			duplicate := CalendarWriteMutation{Op: "remove", ExternalID: "second-external", EventID: "event-1"}
			if sameExternal {
				duplicate.ExternalID = batch.Mutations[0].ExternalID
				duplicate.EventID = "event-2"
			}
			batch.Mutations = append(batch.Mutations, duplicate)
			batch.Receipt.Removed = 1
			if _, err := calendarTestRepo(test, &calendarSDKMock{}).ApplyCalendarBatch(context.Background(), "owner", calendarTestSource, batch); !errors.Is(err, ErrInvalidCalendarBatch) {
				test.Fatalf("duplicate identity was not rejected: %v", err)
			}
		})
	}
}

func TestCalendarEmptyCompletionOnlyUpdatesProgressAndReceipt(test *testing.T) {
	batch := calendarTestWrite()
	batch.Mutations = nil
	batch.Receipt.Upserted = 0
	batch.Receipt.Completed = true
	mock := &calendarSDKMock{
		get: func(*dynamodb.GetItemInput) (*dynamodb.GetItemOutput, error) { return &dynamodb.GetItemOutput{}, nil },
		transact: func(input *dynamodb.TransactWriteItemsInput) (*dynamodb.TransactWriteItemsOutput, error) {
			if len(input.TransactItems) != 2 {
				test.Fatal("completion must never scan or delete missing events")
			}
			update := input.TransactItems[0].Update
			if update == nil {
				test.Fatal("source progress must update atomically")
			}
			fields := calendarTestExpression(update.UpdateExpression, update.ExpressionAttributeNames, update.ExpressionAttributeValues)
			for _, wanted := range []string{"lastCompletedAt", "completedWindowStart", "completedWindowEnd"} {
				if !strings.Contains(fields, wanted) {
					test.Errorf("completion omitted %s", wanted)
				}
			}
			return &dynamodb.TransactWriteItemsOutput{}, nil
		},
	}
	if _, err := calendarTestRepo(test, mock).ApplyCalendarBatch(context.Background(), "owner", calendarTestSource, batch); err != nil {
		test.Fatal(err)
	}
}

func calendarTestReference(test *testing.T, row CalendarEventRow) map[string]types.AttributeValue {
	test.Helper()
	return calendarTestItem(test, map[string]any{
		"PK": row.PK, "SK": "CALTIME#" + row.StartsAt.UTC().Format("2006-01-02T15:04:05.000000000Z") + "#" + row.SourceID + "#" + row.EventID,
		"userId": row.UserID, "sourceId": row.SourceID, "eventId": row.EventID,
		"startsAt": row.StartsAt, "endsAt": row.EndsAt, "revision": row.Revision,
		"pendingShareExpiresAt": row.ExpiresAt,
	})
}

func TestCalendarReadIncludesOngoingEventsAndRevalidatesReferences(test *testing.T) {
	valid := calendarTestRow()
	valid.StartsAt = calendarTestNow.Add(-92 * 24 * time.Hour)
	valid.EndsAt = calendarTestNow.Add(time.Hour)
	valid.ExpiresAt = valid.EndsAt.Add(90 * 24 * time.Hour).Unix()
	for _, scenario := range []string{"ongoing", "moved", "wrong owner", "wrong source", "wrong event", "expired canonical", "expired reference", "ends at from", "starts at to", "wrong account", "old revision", "forged reference owner"} {
		test.Run(scenario, func(test *testing.T) {
			row := valid
			reference := calendarTestReference(test, row)
			switch scenario {
			case "moved":
				row.StartsAt = calendarTestNow
			case "wrong owner":
				row.UserID = "another-user"
			case "wrong source":
				row.SourceID = "another-source"
			case "wrong event":
				row.EventID = "another-event"
			case "expired canonical":
				row.ExpiresAt = calendarTestNow.Unix()
			case "expired reference":
				reference["pendingShareExpiresAt"] = &types.AttributeValueMemberN{Value: fmt.Sprint(calendarTestNow.Unix())}
			case "ends at from":
				row.EndsAt = calendarTestNow
				row.ExpiresAt = row.EndsAt.Add(90 * 24 * time.Hour).Unix()
				reference = calendarTestReference(test, row)
			case "starts at to":
				row.StartsAt = calendarTestNow.Add(24 * time.Hour)
				row.EndsAt = row.StartsAt.Add(time.Hour)
				row.ExpiresAt = row.EndsAt.Add(90 * 24 * time.Hour).Unix()
				reference = calendarTestReference(test, row)
			case "wrong account":
				row.AccountID = "another-account"
			case "old revision":
				row.Revision++
			case "forged reference owner":
				reference["userId"] = &types.AttributeValueMemberS{Value: "another-user"}
			}
			mock := &calendarSDKMock{
				query: func(input *dynamodb.QueryInput) (*dynamodb.QueryOutput, error) {
					keyCondition := calendarTestExpression(input.KeyConditionExpression, input.ExpressionAttributeNames, input.ExpressionAttributeValues)
					for _, expected := range []string{"USER#owner", "CALTIME#2026-08-06T12:00:00.000000000Z", "CALTIME#2026-11-08T12:00:00.000000000Z"} {
						if !strings.Contains(keyCondition, expected) {
							test.Errorf("overlap query missing %s: %s", expected, keyCondition)
						}
					}
					if !aws.ToBool(input.ScanIndexForward) || !aws.ToBool(input.ConsistentRead) || aws.ToInt32(input.Limit) > 50 {
						test.Fatal("calendar query must be bounded, consistent and ascending")
					}
					return &dynamodb.QueryOutput{Items: []map[string]types.AttributeValue{reference}}, nil
				},
				get: func(input *dynamodb.GetItemInput) (*dynamodb.GetItemOutput, error) {
					if calendarTestString(test, input.Key, "PK") != "USER#owner" || calendarTestString(test, input.Key, "SK") != valid.SK {
						test.Fatal("reference must not dictate another caller's canonical key")
					}
					return &dynamodb.GetItemOutput{Item: calendarTestItem(test, row)}, nil
				},
			}
			page, err := calendarTestRepo(test, mock).ListCalendarEventRows(context.Background(), "owner", model.CalendarQuery{From: calendarTestNow, To: calendarTestNow.Add(24 * time.Hour), AccountID: "account-1", Limit: 50})
			if err != nil {
				test.Fatal(err)
			}
			expected := 0
			if scenario == "ongoing" {
				expected = 1
			}
			if len(page.Rows) != expected {
				test.Fatalf("canonical overlap/ownership validation failed: rows=%d", len(page.Rows))
			}
		})
	}
}

func TestCalendarEmptyFilteredPagesRetainBoundContinuation(test *testing.T) {
	row := calendarTestRow()
	reference := calendarTestReference(test, row)
	lastKey := map[string]types.AttributeValue{"PK": reference["PK"], "SK": reference["SK"]}
	queries := 0
	query := model.CalendarQuery{From: calendarTestNow, To: calendarTestNow.Add(24 * time.Hour), AccountID: "account-1", Limit: 1}
	mock := &calendarSDKMock{
		query: func(input *dynamodb.QueryInput) (*dynamodb.QueryOutput, error) {
			queries++
			if queries > 1 && !reflect.DeepEqual(input.ExclusiveStartKey, lastKey) {
				test.Fatal("query continuation was lost")
			}
			return &dynamodb.QueryOutput{Items: []map[string]types.AttributeValue{reference}, LastEvaluatedKey: lastKey}, nil
		},
		get: func(*dynamodb.GetItemInput) (*dynamodb.GetItemOutput, error) { return &dynamodb.GetItemOutput{}, nil },
	}
	repo := calendarTestRepo(test, mock)
	page, err := repo.ListCalendarEventRows(context.Background(), "owner", query)
	if err != nil || len(page.Rows) != 0 || page.NextCursor == nil || queries != 5 {
		test.Fatalf("filtered read must stop after five pages with continuation: %+v %v queries=%d", page, err, queries)
	}
	query.Cursor = *page.NextCursor
	for _, scope := range []string{"caller", "from", "to", "account", "limit", "source endpoint"} {
		test.Run(scope, func(test *testing.T) {
			changed := query
			userID := "owner"
			switch scope {
			case "caller":
				userID = "other"
			case "from":
				changed.From = changed.From.Add(time.Second)
			case "to":
				changed.To = changed.To.Add(time.Second)
			case "account":
				changed.AccountID = "other"
			case "limit":
				changed.Limit = 2
			case "source endpoint":
				_, err = repo.ListCalendarSources(context.Background(), userID, query.Cursor, 1)
			}
			if scope != "source endpoint" {
				_, err = repo.ListCalendarEventRows(context.Background(), userID, changed)
			}
			if !errors.Is(err, ErrInvalidCalendarCursor) || queries != 5 {
				test.Fatalf("cursor accepted another scope: %v queries=%d", err, queries)
			}
		})
	}
	mock.query = func(input *dynamodb.QueryInput) (*dynamodb.QueryOutput, error) {
		if !reflect.DeepEqual(input.ExclusiveStartKey, lastKey) {
			test.Fatal("resume did not use the retained continuation")
		}
		return &dynamodb.QueryOutput{}, nil
	}
	if resumed, err := repo.ListCalendarEventRows(context.Background(), "owner", query); err != nil || resumed.NextCursor != nil {
		test.Fatalf("valid cursor did not reach exhaustion: %+v %v", resumed, err)
	}
}

func TestCalendarExpiredReceiptCannotBypassRevisionFence(test *testing.T) {
	batch := calendarTestWrite()
	saved := calendarTestReceipt(batch)
	saved.ExpiresAt = calendarTestNow.Unix()
	writes := 0
	mock := &calendarSDKMock{
		get: func(input *dynamodb.GetItemInput) (*dynamodb.GetItemOutput, error) {
			if calendarTestString(test, input.Key, "SK") == saved.SK {
				return &dynamodb.GetItemOutput{Item: calendarTestItem(test, saved)}, nil
			}
			return &dynamodb.GetItemOutput{}, nil
		},
		transact: func(input *dynamodb.TransactWriteItemsInput) (*dynamodb.TransactWriteItemsOutput, error) {
			writes++
			source := input.TransactItems[0].Update
			if source == nil || calendarTestExpression(source.ConditionExpression, source.ExpressionAttributeNames, source.ExpressionAttributeValues) != "attribute_not_exists (PK)" {
				test.Fatal("expired receipt retry lost the original source creation fence")
			}
			receipt := input.TransactItems[len(input.TransactItems)-1].Put
			condition := calendarTestExpression(receipt.ConditionExpression, receipt.ExpressionAttributeNames, receipt.ExpressionAttributeValues)
			if !strings.Contains(condition, "pendingShareExpiresAt <=") || !strings.Contains(condition, "userId = owner") {
				test.Fatalf("TTL cleanup must not be required before replacing an expired receipt: %s", condition)
			}
			return nil, &types.TransactionCanceledException{CancellationReasons: []types.CancellationReason{{Code: aws.String("ConditionalCheckFailed")}}}
		},
	}
	repo := calendarTestRepo(test, mock)
	if receipt, err := repo.GetCalendarBatchReceipt(context.Background(), "owner", calendarTestSource, batch.Receipt.BatchID); err != nil || receipt != nil {
		test.Fatalf("expired receipt remained visible: %+v %v", receipt, err)
	}
	if _, err := repo.ApplyCalendarBatch(context.Background(), "owner", calendarTestSource, batch); !errors.Is(err, ErrConditionFailed) || writes != 1 {
		test.Fatalf("expired batch resurrected acceptance: %v writes=%d", err, writes)
	}
}

func TestCalendarTransactionRetryReconcilesAcknowledgedWinner(test *testing.T) {
	for _, failure := range []string{"conditional race", "lost response", "different payload", "throttling", "read failure"} {
		test.Run(failure, func(test *testing.T) {
			batch := calendarTestWrite()
			saved := calendarTestReceipt(batch)
			writes := 0
			transportErr := errors.New("transaction response unavailable")
			readErr := errors.New("receipt unavailable")
			mock := &calendarSDKMock{
				get: func(*dynamodb.GetItemInput) (*dynamodb.GetItemOutput, error) {
					if writes == 0 || failure == "throttling" {
						return &dynamodb.GetItemOutput{}, nil
					}
					if failure == "read failure" {
						return nil, readErr
					}
					if failure == "different payload" {
						saved.PayloadDigest = strings.Repeat("b", 64)
					}
					return &dynamodb.GetItemOutput{Item: calendarTestItem(test, saved)}, nil
				},
				transact: func(*dynamodb.TransactWriteItemsInput) (*dynamodb.TransactWriteItemsOutput, error) {
					writes++
					if failure == "conditional race" {
						return nil, &types.TransactionCanceledException{CancellationReasons: []types.CancellationReason{{Code: aws.String("ConditionalCheckFailed")}}}
					}
					if failure == "throttling" {
						return nil, &types.TransactionCanceledException{CancellationReasons: []types.CancellationReason{{Code: aws.String("ProvisionedThroughputExceeded")}}}
					}
					return nil, transportErr
				},
			}
			receipt, err := calendarTestRepo(test, mock).ApplyCalendarBatch(context.Background(), "owner", calendarTestSource, batch)
			switch failure {
			case "different payload":
				if !errors.Is(err, ErrConditionFailed) {
					test.Fatalf("winner digest must match: %v", err)
				}
			case "throttling":
				var canceled *types.TransactionCanceledException
				if !errors.As(err, &canceled) || errors.Is(err, ErrConditionFailed) {
					test.Fatalf("capacity failure must not become a source conflict: %v", err)
				}
			case "read failure":
				if !errors.Is(err, transportErr) || !errors.Is(err, readErr) {
					test.Fatalf("ambiguous commit and failed reconciliation must both surface: %v", err)
				}
			default:
				if err != nil || !reflect.DeepEqual(receipt, &saved.Receipt) {
					test.Fatalf("persisted acknowledgement not recovered: %+v %v", receipt, err)
				}
			}
			if writes != 1 {
				test.Fatalf("retry changed or reapplied the source revision: %d", writes)
			}
		})
	}
}

func TestCalendarExplicitRemovalKeepsMissingIdentityInsideSourceFence(test *testing.T) {
	for _, existing := range []bool{false, true} {
		test.Run(fmt.Sprint(existing), func(test *testing.T) {
			batch := calendarTestWrite()
			batch.Receipt.Upserted, batch.Receipt.Removed = 0, 1
			batch.Mutations[0].Op, batch.Mutations[0].Event = "remove", nil
			row := calendarTestRow()
			mock := &calendarSDKMock{
				get: func(input *dynamodb.GetItemInput) (*dynamodb.GetItemOutput, error) {
					if existing && calendarTestString(test, input.Key, "SK") == row.SK {
						return &dynamodb.GetItemOutput{Item: calendarTestItem(test, row)}, nil
					}
					return &dynamodb.GetItemOutput{}, nil
				},
				transact: func(input *dynamodb.TransactWriteItemsInput) (*dynamodb.TransactWriteItemsOutput, error) {
					expected := 3
					if existing {
						expected = 4
					}
					if len(input.TransactItems) != expected || input.TransactItems[0].Update == nil {
						test.Fatalf("removal must keep source, receipt, canonical and known reference atomic: %d", len(input.TransactItems))
					}
					deletes := 0
					for _, operation := range input.TransactItems {
						if operation.Delete == nil {
							continue
						}
						deletes++
						condition := calendarTestExpression(operation.Delete.ConditionExpression, operation.Delete.ExpressionAttributeNames, operation.Delete.ExpressionAttributeValues)
						for _, expected := range []string{"attribute_not_exists (PK)", "userId = owner", "sourceId = " + calendarTestSource, "eventId = event-1"} {
							if !strings.Contains(condition, expected) {
								test.Errorf("removal missing identity guard %s: %s", expected, condition)
							}
						}
					}
					if deletes != expected-2 {
						test.Fatal("missing canonical identity must still have an explicit transactional delete")
					}
					return &dynamodb.TransactWriteItemsOutput{}, nil
				},
			}
			if _, err := calendarTestRepo(test, mock).ApplyCalendarBatch(context.Background(), "owner", calendarTestSource, batch); err != nil {
				test.Fatal(err)
			}
		})
	}
}

func TestCalendarMaximumRescheduleIsBoundedAndSameTimeHasOneReferenceOperation(test *testing.T) {
	for _, moved := range []bool{false, true} {
		test.Run(fmt.Sprint(moved), func(test *testing.T) {
			batch := calendarTestWrite()
			batch.Mutations = nil
			batch.Receipt.Upserted = 20
			rows := make(map[string]CalendarEventRow)
			for eventNumber := 0; eventNumber < 20; eventNumber++ {
				row := calendarTestRow()
				row.EventID = fmt.Sprintf("event-%d", eventNumber)
				row.SK = "CALEVENT#" + calendarTestSource + "#" + row.EventID
				old := row
				if moved {
					old.StartsAt = old.StartsAt.Add(-time.Hour)
				}
				rows[row.SK] = old
				batch.Mutations = append(batch.Mutations, CalendarWriteMutation{Op: "upsert", EventID: row.EventID, ExternalID: "external-" + row.EventID, Event: &row})
			}
			mock := &calendarSDKMock{
				get: func(input *dynamodb.GetItemInput) (*dynamodb.GetItemOutput, error) {
					if row, exists := rows[calendarTestString(test, input.Key, "SK")]; exists {
						return &dynamodb.GetItemOutput{Item: calendarTestItem(test, row)}, nil
					}
					return &dynamodb.GetItemOutput{}, nil
				},
				transact: func(input *dynamodb.TransactWriteItemsInput) (*dynamodb.TransactWriteItemsOutput, error) {
					expected := 42
					if moved {
						expected = 62
					}
					if len(input.TransactItems) != expected {
						test.Fatalf("maximum batch exceeded its bounded operations: %d", len(input.TransactItems))
					}
					keys := make(map[string]bool)
					for _, operation := range input.TransactItems {
						var key map[string]types.AttributeValue
						switch {
						case operation.Update != nil:
							key = operation.Update.Key
						case operation.Put != nil:
							key = operation.Put.Item
						case operation.Delete != nil:
							key = operation.Delete.Key
						}
						sortKey := calendarTestString(test, key, "SK")
						if keys[sortKey] {
							test.Fatalf("DynamoDB disallows two operations on the same item: %s", sortKey)
						}
						keys[sortKey] = true
					}
					return &dynamodb.TransactWriteItemsOutput{}, nil
				},
			}
			before := calendarTestItem(test, batch.Mutations[0].Event)
			if _, err := calendarTestRepo(test, mock).ApplyCalendarBatch(context.Background(), "owner", calendarTestSource, batch); err != nil {
				test.Fatal(err)
			}
			if !reflect.DeepEqual(before, calendarTestItem(test, batch.Mutations[0].Event)) {
				test.Fatal("repository mutated caller-owned ciphertext metadata")
			}
		})
	}
}

func TestCalendarMalformedBatchCannotReachDynamoDB(test *testing.T) {
	for _, scenario := range []string{"21 identities", "bad ttl", "bad receipt ttl", "wrong owner", "wrong source", "wrong key", "missing ciphertext", "oversized ciphertext", "revision", "receipt counts", "window", "duration", "external size", "bad op"} {
		test.Run(scenario, func(test *testing.T) {
			batch := calendarTestWrite()
			switch scenario {
			case "21 identities":
				for eventNumber := 1; eventNumber <= 20; eventNumber++ {
					batch.Mutations = append(batch.Mutations, CalendarWriteMutation{Op: "remove", EventID: fmt.Sprintf("extra-%d", eventNumber), ExternalID: fmt.Sprintf("external-%d", eventNumber)})
				}
				batch.Receipt.Removed = 20
			case "bad ttl":
				batch.Mutations[0].Event.ExpiresAt++
			case "bad receipt ttl":
				batch.ExpiresAt++
			case "wrong owner":
				batch.Mutations[0].Event.UserID = "other"
			case "wrong source":
				batch.Mutations[0].Event.SourceID = "other"
			case "wrong key":
				batch.Mutations[0].Event.PK = "USER#other"
			case "missing ciphertext":
				batch.Mutations[0].Event.Ciphertext = ""
			case "oversized ciphertext":
				batch.Mutations[0].Event.Ciphertext = strings.Repeat("x", 16*1024+1)
			case "revision":
				batch.ExpectedRevision = 1
			case "receipt counts":
				batch.Receipt.Removed = 1
			case "window":
				batch.WindowEnd = batch.WindowStart.Add(93*24*time.Hour + time.Second)
			case "duration":
				batch.Mutations[0].Event.EndsAt = batch.Mutations[0].Event.StartsAt.Add(93*24*time.Hour + time.Second)
			case "external size":
				batch.Mutations[0].ExternalID = strings.Repeat("가", 171)
			case "bad op":
				batch.Mutations[0].Op = "delete-all"
			}
			if _, err := calendarTestRepo(test, &calendarSDKMock{}).ApplyCalendarBatch(context.Background(), "owner", calendarTestSource, batch); !errors.Is(err, ErrInvalidCalendarBatch) {
				test.Fatalf("invalid batch reached storage: %v", err)
			}
		})
	}
}

func calendarTestSourceItem(test *testing.T) map[string]types.AttributeValue {
	test.Helper()
	return calendarTestItem(test, map[string]any{
		"PK": "USER#owner", "SK": "CALSOURCE#" + calendarTestSource, "userId": "owner",
		"sourceId": calendarTestSource, "provider": "outlook", "revision": int64(5), "lastAttemptAt": calendarTestNow,
		"lastCompletedAt": calendarTestNow.Add(-time.Hour), "completedWindowStart": calendarTestNow.Add(-24 * time.Hour),
		"completedWindowEnd": calendarTestNow.Add(24 * time.Hour),
	})
}

func TestCalendarSourcesRequireCallerScopeAndCanonicalRevalidation(test *testing.T) {
	for _, scenario := range []string{"valid", "forged query owner", "canonical owner changed", "canonical source changed", "canonical missing", "canonical newer", "wrong canonical key"} {
		test.Run(scenario, func(test *testing.T) {
			queried := calendarTestSourceItem(test)
			canonical := calendarTestSourceItem(test)
			switch scenario {
			case "forged query owner":
				queried["userId"] = &types.AttributeValueMemberS{Value: "other"}
			case "canonical owner changed":
				canonical["userId"] = &types.AttributeValueMemberS{Value: "other"}
			case "canonical source changed":
				canonical["sourceId"] = &types.AttributeValueMemberS{Value: "other"}
			case "canonical missing":
				canonical = nil
			case "canonical newer":
				canonical["revision"] = &types.AttributeValueMemberN{Value: "6"}
			case "wrong canonical key":
				canonical["PK"] = &types.AttributeValueMemberS{Value: "USER#other"}
			}
			mock := &calendarSDKMock{
				query: func(input *dynamodb.QueryInput) (*dynamodb.QueryOutput, error) {
					condition := calendarTestExpression(input.KeyConditionExpression, input.ExpressionAttributeNames, input.ExpressionAttributeValues)
					if !strings.Contains(condition, "USER#owner") || !strings.Contains(condition, "CALSOURCE#") || !aws.ToBool(input.ConsistentRead) {
						test.Fatalf("source query escaped scope: %s", condition)
					}
					return &dynamodb.QueryOutput{Items: []map[string]types.AttributeValue{queried}}, nil
				},
				get: func(input *dynamodb.GetItemInput) (*dynamodb.GetItemOutput, error) {
					if calendarTestString(test, input.Key, "PK") != "USER#owner" || calendarTestString(test, input.Key, "SK") != "CALSOURCE#"+calendarTestSource {
						test.Fatal("source revalidation escaped query scope")
					}
					return &dynamodb.GetItemOutput{Item: canonical}, nil
				},
			}
			page, err := calendarTestRepo(test, mock).ListCalendarSources(context.Background(), "owner", "", 50)
			if err != nil {
				test.Fatal(err)
			}
			expected := 0
			if scenario == "valid" || scenario == "canonical newer" {
				expected = 1
			}
			if len(page.Sources) != expected {
				test.Fatalf("invalid source metadata exposed: %+v", page)
			}
			if expected == 1 {
				if page.Sources[0].LastCompletedAt == nil || page.Sources[0].CompletedWindowStart == nil || page.Sources[0].CompletedWindowEnd == nil {
					test.Fatal("last completed window was lost")
				}
				if scenario == "canonical newer" && page.Sources[0].Revision != 6 {
					test.Fatal("source status must come from current canonical metadata")
				}
			}
		})
	}
}

func TestCalendarSourceEmptyPageContinuationIsBoundAndResumable(test *testing.T) {
	item := calendarTestSourceItem(test)
	lastKey := map[string]types.AttributeValue{"PK": item["PK"], "SK": item["SK"]}
	queries := 0
	mock := &calendarSDKMock{
		query: func(input *dynamodb.QueryInput) (*dynamodb.QueryOutput, error) {
			queries++
			if aws.ToInt32(input.Limit) != 1 {
				test.Fatal("source query limit was not applied")
			}
			return &dynamodb.QueryOutput{Items: []map[string]types.AttributeValue{item}, LastEvaluatedKey: lastKey}, nil
		},
		get: func(*dynamodb.GetItemInput) (*dynamodb.GetItemOutput, error) { return &dynamodb.GetItemOutput{}, nil },
	}
	repo := calendarTestRepo(test, mock)
	page, err := repo.ListCalendarSources(context.Background(), "owner", "", 1)
	if err != nil {
		test.Fatal(err)
	}
	if len(page.Sources) != 0 || page.NextCursor == nil || queries != 5 {
		test.Fatalf("source filtering lost bounded continuation: %+v queries=%d", page, queries)
	}
	if _, err := repo.ListCalendarSources(context.Background(), "other", *page.NextCursor, 1); !errors.Is(err, ErrInvalidCalendarCursor) {
		test.Fatalf("source cursor leaked caller scope: %v", err)
	}
	if _, err := repo.ListCalendarSources(context.Background(), "owner", *page.NextCursor, 2); !errors.Is(err, ErrInvalidCalendarCursor) {
		test.Fatalf("source cursor leaked limit scope: %v", err)
	}
	mock.query = func(input *dynamodb.QueryInput) (*dynamodb.QueryOutput, error) {
		if !reflect.DeepEqual(input.ExclusiveStartKey, lastKey) {
			test.Fatal("source resume lost the actual last key")
		}
		return &dynamodb.QueryOutput{}, nil
	}
	if resumed, err := repo.ListCalendarSources(context.Background(), "owner", *page.NextCursor, 1); err != nil || resumed.NextCursor != nil {
		test.Fatalf("source cursor failed to resume: %+v %v", resumed, err)
	}
}

func TestCalendarCursorRejectsForeignKeysAndTrailingJSON(test *testing.T) {
	scope := map[string]any{
		"version": 1, "kind": "events", "userId": "owner", "from": "2026-11-07T12:00:00Z", "to": "2026-11-08T12:00:00Z", "limit": 50,
		"lastPK": "USER#owner", "lastSK": "CALTIME#2026-11-07T13:00:00.000000000Z#" + calendarTestSource + "#event-1",
	}
	for _, scenario := range []string{"foreign partition", "foreign kind", "out of window", "trailing JSON"} {
		test.Run(scenario, func(test *testing.T) {
			payload := make(map[string]any)
			for name, value := range scope {
				payload[name] = value
			}
			switch scenario {
			case "foreign partition":
				payload["lastPK"] = "USER#other"
			case "foreign kind":
				payload["lastSK"] = "MEETING#secret"
			case "out of window":
				payload["lastSK"] = "CALTIME#2027-01-01T00:00:00.000000000Z#event"
			}
			encoded, err := json.Marshal(payload)
			if err != nil {
				test.Fatal(err)
			}
			if scenario == "trailing JSON" {
				encoded = append(encoded, []byte(` {}`)...)
			}
			query := model.CalendarQuery{From: calendarTestNow, To: calendarTestNow.Add(24 * time.Hour), Limit: 50, Cursor: base64.RawURLEncoding.EncodeToString(encoded)}
			if _, err := calendarTestRepo(test, &calendarSDKMock{}).ListCalendarEventRows(context.Background(), "owner", query); !errors.Is(err, ErrInvalidCalendarCursor) {
				test.Fatalf("malformed cursor reached DynamoDB: %v", err)
			}
		})
	}
}

func TestCalendarRealSDKSerializesSingleConditionalTransaction(test *testing.T) {
	requests := 0
	client := dynamodb.New(dynamodb.Options{
		Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1,
		HTTPClient: meetingListHTTPClientFunc(func(request *http.Request) (*http.Response, error) {
			requests++
			var body map[string]any
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				test.Fatal(err)
			}
			target := request.Header.Get("X-Amz-Target")
			response := `{}`
			status := 200
			switch requests {
			case 1, 2, 4:
				if !strings.HasSuffix(target, ".GetItem") || body["ConsistentRead"] != true {
					test.Fatalf("expected consistent canonical/receipt read: %s %v", target, body)
				}
			case 3:
				if !strings.HasSuffix(target, ".TransactWriteItems") {
					test.Fatalf("write bypassed transaction: %s", target)
				}
				operations := body["TransactItems"].([]any)
				if len(operations) != 4 {
					test.Fatalf("SDK serialized an incomplete transaction: %d", len(operations))
				}
				source := operations[0].(map[string]any)["Update"].(map[string]any)
				condition := analysisCondition(test, source)
				for _, wanted := range []string{"revision", `"N":"4"`, "userId", "owner", "sourceId", calendarTestSource} {
					if !strings.Contains(condition, wanted) {
						test.Fatalf("SDK transaction missing %s: %s", wanted, condition)
					}
				}
				status = 400
				response = `{"__type":"com.amazonaws.dynamodb.v20120810#TransactionCanceledException","Message":"fence changed","CancellationReasons":[{"Code":"ConditionalCheckFailed"},{"Code":"None"},{"Code":"None"},{"Code":"None"}]}`
			default:
				test.Fatalf("unexpected SDK retry or fallback write: %d", requests)
			}
			return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/x-amz-json-1.0"}}, Body: io.NopCloser(strings.NewReader(response))}, nil
		}),
	})
	batch := calendarTestWrite()
	batch.ExpectedRevision, batch.Receipt.Revision, batch.Mutations[0].Event.Revision = 4, 5, 5
	repo := NewCalendarRepository(client, "calendar-test")
	repo.now = func() time.Time { return calendarTestNow }
	if _, err := repo.ApplyCalendarBatch(context.Background(), "owner", calendarTestSource, batch); !errors.Is(err, ErrConditionFailed) || requests != 4 {
		test.Fatalf("real SDK cancellation did not retain atomic conflict semantics: %v requests=%d", err, requests)
	}
}

func TestCalendarAcceptedBatchRetryUsesActuallyWrittenReceipt(test *testing.T) {
	batch := calendarTestWrite()
	var saved map[string]types.AttributeValue
	writes := 0
	mock := &calendarSDKMock{
		get: func(input *dynamodb.GetItemInput) (*dynamodb.GetItemOutput, error) {
			if strings.HasPrefix(calendarTestString(test, input.Key, "SK"), "CALBATCH#") {
				return &dynamodb.GetItemOutput{Item: saved}, nil
			}
			return &dynamodb.GetItemOutput{}, nil
		},
		transact: func(input *dynamodb.TransactWriteItemsInput) (*dynamodb.TransactWriteItemsOutput, error) {
			writes++
			for _, operation := range input.TransactItems {
				if operation.Put != nil && strings.HasPrefix(calendarTestString(test, operation.Put.Item, "SK"), "CALBATCH#") {
					saved = operation.Put.Item
				}
			}
			return &dynamodb.TransactWriteItemsOutput{}, nil
		},
	}
	repo := calendarTestRepo(test, mock)
	first, err := repo.ApplyCalendarBatch(context.Background(), "owner", calendarTestSource, batch)
	if err != nil {
		test.Fatal(err)
	}
	batch.Mutations[0].Event.Ciphertext = "different-encryption-nonce"
	retried, err := repo.ApplyCalendarBatch(context.Background(), "owner", calendarTestSource, batch)
	if err != nil || !reflect.DeepEqual(first, retried) || writes != 1 {
		test.Fatalf("accepted retry duplicated mutation or lost receipt: first=%+v retry=%+v err=%v writes=%d", first, retried, err, writes)
	}
}

func TestCalendarExpiredEventHiddenBeforeTTLDeletion(test *testing.T) {
	row := calendarTestRow()
	row.EndsAt = calendarTestNow.Add(-90 * 24 * time.Hour)
	row.StartsAt = row.EndsAt.Add(-time.Hour)
	row.ExpiresAt = calendarTestNow.Unix()
	reference := calendarTestReference(test, row)
	mock := &calendarSDKMock{
		query: func(*dynamodb.QueryInput) (*dynamodb.QueryOutput, error) {
			return &dynamodb.QueryOutput{Items: []map[string]types.AttributeValue{reference}}, nil
		},
	}
	query := model.CalendarQuery{From: row.StartsAt, To: row.EndsAt, Limit: 50}
	page, err := calendarTestRepo(test, mock).ListCalendarEventRows(context.Background(), "owner", query)
	if err != nil || len(page.Rows) != 0 {
		test.Fatalf("expired overlapping event survived synchronous retention: %+v %v", page, err)
	}
}

func TestCalendarWriteRejectsCorruptCanonicalOrReceiptScope(test *testing.T) {
	for _, scenario := range []string{"canonical owner", "canonical source", "canonical sort key", "receipt owner", "receipt source", "receipt batch"} {
		test.Run(scenario, func(test *testing.T) {
			batch := calendarTestWrite()
			row := calendarTestRow()
			receipt := calendarTestReceipt(batch)
			switch scenario {
			case "canonical owner":
				row.UserID = "other"
			case "canonical source":
				row.SourceID = "other"
			case "canonical sort key":
				row.SK = "CALEVENT#other#event"
			case "receipt owner":
				receipt.UserID = "other"
			case "receipt source":
				receipt.SourceID = "other"
			case "receipt batch":
				receipt.Receipt.BatchID = "other"
			}
			mock := &calendarSDKMock{
				get: func(input *dynamodb.GetItemInput) (*dynamodb.GetItemOutput, error) {
					if strings.HasPrefix(calendarTestString(test, input.Key, "SK"), "CALBATCH#") {
						if strings.HasPrefix(scenario, "receipt") {
							return &dynamodb.GetItemOutput{Item: calendarTestItem(test, receipt)}, nil
						}
						return &dynamodb.GetItemOutput{}, nil
					}
					return &dynamodb.GetItemOutput{Item: calendarTestItem(test, row)}, nil
				},
			}
			if _, err := calendarTestRepo(test, mock).ApplyCalendarBatch(context.Background(), "owner", calendarTestSource, batch); !errors.Is(err, ErrCalendarCorruptData) {
				test.Fatalf("untrusted canonical metadata supplied reference replacement keys or a receipt: %v", err)
			}
		})
	}
}

func TestCalendarFilteredPaginationKeepsLimitOrderingAndContinuation(test *testing.T) {
	first := calendarTestRow()
	second := calendarTestRow()
	second.EventID, second.SK = "event-2", "CALEVENT#"+calendarTestSource+"#event-2"
	second.StartsAt, second.EndsAt = first.StartsAt.Add(time.Hour), first.EndsAt.Add(time.Hour)
	second.ExpiresAt = second.EndsAt.Add(90 * 24 * time.Hour).Unix()
	firstReference := calendarTestReference(test, first)
	secondReference := calendarTestReference(test, second)
	missingReference := calendarTestReference(test, first)
	missingReference["eventId"] = &types.AttributeValueMemberS{Value: "missing"}
	missingReference["SK"] = &types.AttributeValueMemberS{Value: "CALTIME#2026-11-07T13:00:00.000000000Z#" + calendarTestSource + "#missing"}
	firstLastKey := map[string]types.AttributeValue{"PK": missingReference["PK"], "SK": missingReference["SK"]}
	secondLastKey := map[string]types.AttributeValue{"PK": secondReference["PK"], "SK": secondReference["SK"]}
	queries := 0
	mock := &calendarSDKMock{
		query: func(input *dynamodb.QueryInput) (*dynamodb.QueryOutput, error) {
			queries++
			if queries == 1 {
				if aws.ToInt32(input.Limit) != 2 {
					test.Fatal("initial page overfetched")
				}
				return &dynamodb.QueryOutput{Items: []map[string]types.AttributeValue{firstReference, missingReference}, LastEvaluatedKey: firstLastKey}, nil
			}
			if queries != 2 || aws.ToInt32(input.Limit) != 1 || !reflect.DeepEqual(input.ExclusiveStartKey, firstLastKey) {
				test.Fatal("filtered continuation must request only the remaining output capacity")
			}
			return &dynamodb.QueryOutput{Items: []map[string]types.AttributeValue{secondReference}, LastEvaluatedKey: secondLastKey}, nil
		},
		get: func(input *dynamodb.GetItemInput) (*dynamodb.GetItemOutput, error) {
			switch calendarTestString(test, input.Key, "SK") {
			case first.SK:
				return &dynamodb.GetItemOutput{Item: calendarTestItem(test, first)}, nil
			case second.SK:
				return &dynamodb.GetItemOutput{Item: calendarTestItem(test, second)}, nil
			default:
				return &dynamodb.GetItemOutput{}, nil
			}
		},
	}
	query := model.CalendarQuery{From: calendarTestNow, To: calendarTestNow.Add(24 * time.Hour), Limit: 2}
	page, err := calendarTestRepo(test, mock).ListCalendarEventRows(context.Background(), "owner", query)
	if err != nil {
		test.Fatal(err)
	}
	if len(page.Rows) != 2 || page.Rows[0].EventID != first.EventID || page.Rows[1].EventID != second.EventID || page.NextCursor == nil || queries != 2 {
		test.Fatalf("filtered pagination dropped ordering, rows or continuation: %+v", page)
	}
}

func TestGetCalendarEventRow_IdentityChecked(test *testing.T) {
	row := calendarTestRow()
	var item map[string]types.AttributeValue
	mock := &calendarSDKMock{get: func(*dynamodb.GetItemInput) (*dynamodb.GetItemOutput, error) {
		return &dynamodb.GetItemOutput{Item: item}, nil
	}}
	repo := calendarTestRepo(test, mock)
	item = calendarTestItem(test, row)
	got, err := repo.GetCalendarEventRow(context.Background(), "owner", calendarTestSource, "event-1")
	if err != nil || got == nil || got.EventID != "event-1" {
		test.Fatalf("matching row rejected: %v", err)
	}
	for name, mutate := range map[string]func(*CalendarEventRow){
		"user":   func(r *CalendarEventRow) { r.UserID = "other" },
		"source": func(r *CalendarEventRow) { r.SourceID = "other" },
		"event":  func(r *CalendarEventRow) { r.EventID = "other" },
	} {
		test.Run(name, func(test *testing.T) {
			mismatched := row
			mutate(&mismatched)
			item = calendarTestItem(test, mismatched)
			got, err := repo.GetCalendarEventRow(context.Background(), "owner", calendarTestSource, "event-1")
			if !errors.Is(err, ErrCalendarCorruptData) || got != nil {
				test.Fatalf("foreign identity accepted: %v", err)
			}
		})
	}
	item = nil
	got, err = repo.GetCalendarEventRow(context.Background(), "owner", calendarTestSource, "event-1")
	if err != nil || got != nil {
		test.Fatalf("absent row must be nil, nil: %v %v", got, err)
	}
}
