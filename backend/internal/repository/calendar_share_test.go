package repository

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
)

func calendarTestShare(row CalendarEventRow, accountID, meetingKey, state string) CalendarShareRow {
	share := CalendarShareRow{
		AccountID: accountID, MeetingKey: meetingKey, UserID: row.UserID, SourceID: row.SourceID, EventID: row.EventID,
		StartsAt: row.StartsAt, EndsAt: row.EndsAt, State: state, SharerEmail: "owner@example.com", ExpiresAt: row.ExpiresAt,
	}
	if state == "shared" {
		share.Ciphertext = "encrypted-share"
	}
	return share
}

func calendarTestShareKey(row CalendarEventRow, accountID, meetingKey string) *ShareKey {
	return &ShareKey{AccountID: accountID, SK: model.CalendarShareSortKey(row.StartsAt, meetingKey, row.UserID)}
}

// calendarTestShareMutation turns the default upsert into a share-bearing one.
func calendarTestShareMutation(batch *CalendarWriteBatch, index int, accountID, meetingKey string) {
	mutation := &batch.Mutations[index]
	share := calendarTestShare(*mutation.Event, accountID, meetingKey, "shared")
	mutation.Share = &share
	mutation.Event.ShareAccountID, mutation.Event.MeetingKey, mutation.Event.ShareState = accountID, meetingKey, "shared"
}

// calendarTestObserved fills each mutation's missing Expected from the stored event row, as a
// caller that read the row before planning does.
func calendarTestObserved(test *testing.T, mock *calendarSDKMock, batch CalendarWriteBatch) CalendarWriteBatch {
	test.Helper()
	batch.Mutations = slices.Clone(batch.Mutations)
	for index, mutation := range batch.Mutations {
		if mutation.Expected != nil || mock.get == nil {
			continue
		}
		output, err := mock.get(&dynamodb.GetItemInput{TableName: aws.String("calendar-test"), ConsistentRead: aws.Bool(true),
			Key: calendarKey("owner", model.CalendarEventSortKey(calendarTestSource, mutation.EventID))})
		if err != nil {
			test.Fatal(err)
		}
		var row *CalendarEventRow
		if output != nil && len(output.Item) > 0 {
			row = &CalendarEventRow{}
			if err := attributevalue.UnmarshalMap(output.Item, row); err != nil {
				test.Fatal(err)
			}
		}
		version := CalendarRowVersionOf(row)
		batch.Mutations[index].Expected = &version
	}
	return batch
}

type calendarTestOps struct {
	puts    map[string]map[string]types.AttributeValue
	deletes map[string]bool
	count   int
}

func calendarTestCollect(test *testing.T, input *dynamodb.TransactWriteItemsInput) calendarTestOps {
	test.Helper()
	ops := calendarTestOps{puts: map[string]map[string]types.AttributeValue{}, deletes: map[string]bool{}, count: len(input.TransactItems)}
	for _, operation := range input.TransactItems {
		switch {
		case operation.Put != nil:
			key := calendarTestString(test, operation.Put.Item, "PK") + "|" + calendarTestString(test, operation.Put.Item, "SK")
			ops.puts[key] = operation.Put.Item
		case operation.Delete != nil:
			ops.deletes[calendarTestString(test, operation.Delete.Key, "PK")+"|"+calendarTestString(test, operation.Delete.Key, "SK")] = true
		}
	}
	return ops
}

func TestApplyBatchWritesShareRow(test *testing.T) {
	batch := calendarTestWrite()
	calendarTestShareMutation(&batch, 0, "acc_1", "mk")
	row := *batch.Mutations[0].Event
	wantedKey := "ACCOUNT#acc_1|" + model.CalendarShareSortKey(row.StartsAt, "mk", "owner")
	calls := 0
	mock := &calendarSDKMock{
		get: func(*dynamodb.GetItemInput) (*dynamodb.GetItemOutput, error) { return &dynamodb.GetItemOutput{}, nil },
		transact: func(input *dynamodb.TransactWriteItemsInput) (*dynamodb.TransactWriteItemsOutput, error) {
			calls++
			ops := calendarTestCollect(test, input)
			share, exists := ops.puts[wantedKey]
			if !exists || !strings.HasSuffix(wantedKey, "#mk#owner") {
				test.Fatalf("share row missing from the batch transaction: %v", ops.puts)
			}
			for name, wanted := range map[string]string{"accountId": "acc_1", "meetingKey": "mk", "userId": "owner", "sourceId": calendarTestSource,
				"eventId": "event-1", "state": "shared", "ciphertext": "encrypted-share", "sharerEmail": "owner@example.com"} {
				if got := calendarTestString(test, share, name); got != wanted {
					test.Errorf("share %s = %q, want %q", name, got, wanted)
				}
			}
			if _, ttl := share["pendingShareExpiresAt"].(*types.AttributeValueMemberN); !ttl {
				test.Error("share row must carry the calendar TTL attribute")
			}
			canonical := ops.puts["USER#owner|"+row.SK]
			if calendarTestString(test, canonical, "shareAccountId") != "acc_1" || calendarTestString(test, canonical, "meetingKey") != "mk" ||
				calendarTestString(test, canonical, "shareState") != "shared" {
				test.Error("event row must record its share state")
			}
			if ops.count != 5 {
				test.Fatalf("source, event, reference, share and receipt expected: %d", ops.count)
			}
			return &dynamodb.TransactWriteItemsOutput{}, nil
		},
	}
	if _, err := calendarTestRepo(test, mock).ApplyCalendarBatch(context.Background(), "owner", calendarTestSource, calendarTestObserved(test, mock, batch)); err != nil || calls != 1 {
		test.Fatalf("share write failed: %v calls=%d", err, calls)
	}
}

func TestApplyBatchDeletesPreviousShareKey(test *testing.T) {
	for _, scenario := range []string{"supplied", "derived from stored row"} {
		test.Run(scenario, func(test *testing.T) {
			batch := calendarTestWrite()
			calendarTestShareMutation(&batch, 0, "acc_1", "mk_new")
			row := *batch.Mutations[0].Event
			old := calendarTestRow()
			old.ShareAccountID, old.MeetingKey, old.ShareState = "acc_1", "mk_old", "shared"
			if scenario == "supplied" {
				batch.Mutations[0].PreviousShareKey = calendarTestShareKey(row, "acc_1", "mk_old")
			}
			calls := 0
			shareID, shareItem := calendarTestOwnedShare(test, old, "acc_1", "mk_old", "shared")
			mock := &calendarSDKMock{
				get: func(input *dynamodb.GetItemInput) (*dynamodb.GetItemOutput, error) {
					if calendarTestString(test, input.Key, "PK")+"|"+calendarTestString(test, input.Key, "SK") == shareID {
						return &dynamodb.GetItemOutput{Item: shareItem}, nil
					}
					if calendarTestString(test, input.Key, "SK") == row.SK {
						return &dynamodb.GetItemOutput{Item: calendarTestItem(test, old)}, nil
					}
					return &dynamodb.GetItemOutput{}, nil
				},
				transact: func(input *dynamodb.TransactWriteItemsInput) (*dynamodb.TransactWriteItemsOutput, error) {
					calls++
					ops := calendarTestCollect(test, input)
					if !ops.deletes["ACCOUNT#acc_1|"+model.CalendarShareSortKey(row.StartsAt, "mk_old", "owner")] {
						test.Fatalf("old meetingKey row left behind: %v", ops.deletes)
					}
					if _, exists := ops.puts["ACCOUNT#acc_1|"+model.CalendarShareSortKey(row.StartsAt, "mk_new", "owner")]; !exists {
						test.Fatal("new meetingKey row missing")
					}
					if ops.count != 6 {
						test.Fatalf("one delete for the old key expected: %d operations", ops.count)
					}
					for _, operation := range input.TransactItems {
						if operation.Delete != nil {
							condition := calendarTestExpression(operation.Delete.ConditionExpression, operation.Delete.ExpressionAttributeNames, operation.Delete.ExpressionAttributeValues)
							if !strings.Contains(condition, "userId = owner") || !strings.Contains(condition, "eventId = event-1") {
								test.Fatalf("share delete must stay inside caller/event scope: %s", condition)
							}
						}
					}
					return &dynamodb.TransactWriteItemsOutput{}, nil
				},
			}
			if _, err := calendarTestRepo(test, mock).ApplyCalendarBatch(context.Background(), "owner", calendarTestSource, calendarTestObserved(test, mock, batch)); err != nil || calls != 1 {
				test.Fatalf("rekey failed: %v calls=%d", err, calls)
			}
		})
	}
}

func TestApplyBatchRemoveDeletesShareRow(test *testing.T) {
	for _, state := range []string{"shared", "veto"} {
		test.Run(state, func(test *testing.T) {
			old := calendarTestRow()
			old.ShareAccountID, old.MeetingKey, old.ShareState = "acc_1", "mk", state
			batch := calendarTestWrite()
			batch.Receipt.Upserted, batch.Receipt.Removed = 0, 1
			batch.Mutations = []CalendarWriteMutation{{Op: "remove", EventID: old.EventID, ExternalID: "provider-occurrence-1",
				PreviousShareKey: calendarTestShareKey(old, "acc_1", "mk")}}
			calls := 0
			shareID, shareItem := calendarTestOwnedShare(test, old, "acc_1", "mk", state)
			mock := &calendarSDKMock{
				get: func(input *dynamodb.GetItemInput) (*dynamodb.GetItemOutput, error) {
					if calendarTestString(test, input.Key, "PK")+"|"+calendarTestString(test, input.Key, "SK") == shareID {
						return &dynamodb.GetItemOutput{Item: shareItem}, nil
					}
					if calendarTestString(test, input.Key, "SK") == old.SK {
						return &dynamodb.GetItemOutput{Item: calendarTestItem(test, old)}, nil
					}
					return &dynamodb.GetItemOutput{}, nil
				},
				transact: func(input *dynamodb.TransactWriteItemsInput) (*dynamodb.TransactWriteItemsOutput, error) {
					calls++
					ops := calendarTestCollect(test, input)
					if !ops.deletes["ACCOUNT#acc_1|"+model.CalendarShareSortKey(old.StartsAt, "mk", "owner")] || !ops.deletes["USER#owner|"+old.SK] {
						test.Fatalf("remove must delete event and share rows together: %v", ops.deletes)
					}
					if ops.count != 5 {
						test.Fatalf("source, time ref, event, share delete and receipt expected: %d", ops.count)
					}
					return &dynamodb.TransactWriteItemsOutput{}, nil
				},
			}
			if _, err := calendarTestRepo(test, mock).ApplyCalendarBatch(context.Background(), "owner", calendarTestSource, calendarTestObserved(test, mock, batch)); err != nil || calls != 1 {
				test.Fatalf("remove failed: %v calls=%d", err, calls)
			}
		})
	}
}

func TestApplyBatchRejectsOver100Items(test *testing.T) {
	// Worst case per mutation: old time-ref delete, event put, ref put, share put, old share delete.
	for _, scenario := range []struct {
		mutations int
		rejected  bool
	}{{17, false}, {20, true}} {
		test.Run(fmt.Sprint(scenario.mutations), func(test *testing.T) {
			batch := calendarTestWrite()
			batch.Mutations = nil
			batch.Receipt.Upserted = scenario.mutations
			stored := make(map[string]CalendarEventRow)
			shares := make(map[string]map[string]types.AttributeValue)
			for eventNumber := 0; eventNumber < scenario.mutations; eventNumber++ {
				row := calendarTestRow()
				row.EventID = fmt.Sprintf("event-%d", eventNumber)
				row.SK = "CALEVENT#" + calendarTestSource + "#" + row.EventID
				old := row
				old.StartsAt = old.StartsAt.Add(-time.Hour)
				old.ShareAccountID, old.MeetingKey, old.ShareState = "acc_1", "mk_old_"+row.EventID, "shared"
				stored[row.SK] = old
				shareID, shareItem := calendarTestOwnedShare(test, old, "acc_1", old.MeetingKey, "shared")
				shares[shareID] = shareItem
				batch.Mutations = append(batch.Mutations, CalendarWriteMutation{Op: "upsert", EventID: row.EventID, ExternalID: "external-" + row.EventID, Event: &row})
				calendarTestShareMutation(&batch, eventNumber, "acc_1", "mk_new_"+row.EventID)
				batch.Mutations[eventNumber].PreviousShareKey = calendarTestShareKey(old, "acc_1", old.MeetingKey)
			}
			calls := 0
			mock := &calendarSDKMock{
				get: func(input *dynamodb.GetItemInput) (*dynamodb.GetItemOutput, error) {
					if item, exists := shares[calendarTestString(test, input.Key, "PK")+"|"+calendarTestString(test, input.Key, "SK")]; exists {
						return &dynamodb.GetItemOutput{Item: item}, nil
					}
					if row, exists := stored[calendarTestString(test, input.Key, "SK")]; exists {
						return &dynamodb.GetItemOutput{Item: calendarTestItem(test, row)}, nil
					}
					return &dynamodb.GetItemOutput{}, nil
				},
				transact: func(input *dynamodb.TransactWriteItemsInput) (*dynamodb.TransactWriteItemsOutput, error) {
					calls++
					if len(input.TransactItems) > 100 {
						test.Fatalf("transaction exceeded DynamoDB's limit: %d", len(input.TransactItems))
					}
					return &dynamodb.TransactWriteItemsOutput{}, nil
				},
			}
			_, err := calendarTestRepo(test, mock).ApplyCalendarBatch(context.Background(), "owner", calendarTestSource, calendarTestObserved(test, mock, batch))
			if scenario.rejected {
				if !errors.Is(err, ErrInvalidCalendarBatch) || calls != 0 {
					test.Fatalf("over-limit batch must be rejected before the transaction: %v calls=%d", err, calls)
				}
				return
			}
			if err != nil || calls != 1 {
				test.Fatalf("87-operation batch must be accepted: %v calls=%d", err, calls)
			}
		})
	}
}

func TestApplyBatchRejectsMalformedShare(test *testing.T) {
	for _, scenario := range []string{"veto ciphertext", "shared without ciphertext", "bad state", "wrong owner", "wrong event", "time mismatch",
		"row state mismatch", "share on remove", "foreign previous key", "bad meeting key", "bad series key"} {
		test.Run(scenario, func(test *testing.T) {
			batch := calendarTestWrite()
			calendarTestShareMutation(&batch, 0, "acc_1", "mk")
			mutation := &batch.Mutations[0]
			switch scenario {
			case "veto ciphertext":
				mutation.Share.State, mutation.Event.ShareState = "veto", "veto"
			case "shared without ciphertext":
				mutation.Share.Ciphertext = ""
			case "bad state":
				mutation.Share.State, mutation.Event.ShareState = "public", "public"
			case "wrong owner":
				mutation.Share.UserID = "other"
			case "wrong event":
				mutation.Share.EventID = "event-2"
			case "time mismatch":
				mutation.Share.StartsAt = mutation.Share.StartsAt.Add(time.Minute)
			case "row state mismatch":
				mutation.Event.MeetingKey = "mk_other"
			case "share on remove":
				batch.Mutations[0] = CalendarWriteMutation{Op: "remove", EventID: "event-1", ExternalID: "x", Share: mutation.Share}
				batch.Receipt.Upserted, batch.Receipt.Removed = 0, 1
			case "foreign previous key":
				mutation.PreviousShareKey = &ShareKey{AccountID: "acc_1", SK: model.CalendarShareSortKey(calendarTestNow, "mk", "other")}
			case "bad meeting key":
				mutation.Share.MeetingKey, mutation.Event.MeetingKey = "mk#x", "mk#x"
			case "bad series key":
				mutation.Event.SeriesKey = "series#1"
			}
			mock := &calendarSDKMock{get: func(*dynamodb.GetItemInput) (*dynamodb.GetItemOutput, error) { return &dynamodb.GetItemOutput{}, nil }}
			if _, err := calendarTestRepo(test, mock).ApplyCalendarBatch(context.Background(), "owner", calendarTestSource, calendarTestObserved(test, mock, batch)); !errors.Is(err, ErrInvalidCalendarBatch) {
				test.Fatalf("malformed share reached storage: %v", err)
			}
		})
	}
}

// calendarTestStore serves GetItem from PK|SK items; unknown keys are absent.
func calendarTestStore(test *testing.T, items map[string]map[string]types.AttributeValue) func(*dynamodb.GetItemInput) (*dynamodb.GetItemOutput, error) {
	return func(input *dynamodb.GetItemInput) (*dynamodb.GetItemOutput, error) {
		return &dynamodb.GetItemOutput{Item: items[calendarTestString(test, input.Key, "PK")+"|"+calendarTestString(test, input.Key, "SK")]}, nil
	}
}

func calendarTestCancel(count int, failed ...int) error {
	reasons := make([]types.CancellationReason, count)
	for index := range reasons {
		reasons[index].Code = aws.String("None")
	}
	for _, index := range failed {
		reasons[index].Code = aws.String("ConditionalCheckFailed")
	}
	return &types.TransactionCanceledException{CancellationReasons: reasons}
}

func TestApplyBatchFencedAgainstConcurrentShareStateChange(test *testing.T) {
	batch := calendarTestWrite()
	calendarTestShareMutation(&batch, 0, "acc_1", "mk")
	old := calendarTestRow()
	old.ShareAccountID, old.MeetingKey, old.ShareState = "acc_1", "mk", "shared"
	shareKey := "ACCOUNT#acc_1|" + model.CalendarShareSortKey(old.StartsAt, "mk", "owner")
	owned := calendarTestShare(old, "acc_1", "mk", "shared")
	owned.PK, owned.SK = "ACCOUNT#acc_1", model.CalendarShareSortKey(old.StartsAt, "mk", "owner")
	items := map[string]map[string]types.AttributeValue{"USER#owner|" + old.SK: calendarTestItem(test, old), shareKey: calendarTestItem(test, owned)}
	var repo *CalendarRepository
	batchCalls, vetoCalls := 0, 0
	mock := &calendarSDKMock{get: calendarTestStore(test, items)}
	mock.transact = func(input *dynamodb.TransactWriteItemsInput) (*dynamodb.TransactWriteItemsOutput, error) {
		batchCalls++
		// The user's veto commits between the batch's reads and its transaction.
		vetoCalls++
		stored := old
		stored.ShareVersion, stored.ShareState = 1, "veto"
		items["USER#owner|"+old.SK] = calendarTestItem(test, stored)
		eventIndex := -1
		for index, operation := range input.TransactItems {
			if operation.Put != nil && calendarTestString(test, operation.Put.Item, "SK") == old.SK {
				eventIndex = index
				condition := calendarTestExpression(operation.Put.ConditionExpression, operation.Put.ExpressionAttributeNames, operation.Put.ExpressionAttributeValues)
				if !strings.Contains(condition, "attribute_not_exists (shareVersion)") {
					test.Fatalf("event put must be fenced on the share version read: %s", condition)
				}
			}
		}
		if eventIndex < 0 {
			test.Fatal("event put missing")
		}
		return nil, calendarTestCancel(len(input.TransactItems), eventIndex)
	}
	repo = calendarTestRepo(test, mock)
	_, err := repo.ApplyCalendarBatch(context.Background(), "owner", calendarTestSource, calendarTestObserved(test, mock, batch))
	if !errors.Is(err, ErrCalendarShareStateChanged) || errors.Is(err, ErrConditionFailed) || batchCalls != 1 || vetoCalls != 1 {
		test.Fatalf("lost veto must surface as a share-state change: %v batch=%d veto=%d", err, batchCalls, vetoCalls)
	}
	stored := old
	stored.ShareVersion = 2
	items["USER#owner|"+old.SK] = calendarTestItem(test, stored)
	mock.transact = func(input *dynamodb.TransactWriteItemsInput) (*dynamodb.TransactWriteItemsOutput, error) {
		for _, operation := range input.TransactItems {
			if operation.Put != nil && calendarTestString(test, operation.Put.Item, "SK") == old.SK {
				condition := calendarTestExpression(operation.Put.ConditionExpression, operation.Put.ExpressionAttributeNames, operation.Put.ExpressionAttributeValues)
				if !strings.Contains(condition, "shareVersion = 2") {
					test.Fatalf("event put must carry the read share version: %s", condition)
				}
				if version, ok := operation.Put.Item["shareVersion"].(*types.AttributeValueMemberN); !ok || version.Value != "2" {
					test.Fatal("sync must preserve the stored share version")
				}
			}
		}
		return &dynamodb.TransactWriteItemsOutput{}, nil
	}
	if _, err := repo.ApplyCalendarBatch(context.Background(), "owner", calendarTestSource, calendarTestObserved(test, mock, batch)); err != nil {
		test.Fatalf("fresh read must succeed: %v", err)
	}
}

func TestApplyBatchSourceRevisionMismatchStillConflicts(test *testing.T) {
	batch := calendarTestWrite()
	calendarTestShareMutation(&batch, 0, "acc_1", "mk")
	mock := &calendarSDKMock{
		get: func(*dynamodb.GetItemInput) (*dynamodb.GetItemOutput, error) { return &dynamodb.GetItemOutput{}, nil },
		transact: func(input *dynamodb.TransactWriteItemsInput) (*dynamodb.TransactWriteItemsOutput, error) {
			all := make([]int, len(input.TransactItems))
			for index := range all {
				all[index] = index
			}
			return nil, calendarTestCancel(len(input.TransactItems), all...)
		},
	}
	_, err := calendarTestRepo(test, mock).ApplyCalendarBatch(context.Background(), "owner", calendarTestSource, calendarTestObserved(test, mock, batch))
	if !errors.Is(err, ErrConditionFailed) || errors.Is(err, ErrCalendarShareStateChanged) {
		test.Fatalf("source revision mismatch must stay a 409 conflict: %v", err)
	}
}

func calendarTestSecondUpsert(batch *CalendarWriteBatch, eventID string) {
	second := calendarTestRow()
	second.EventID, second.SK = eventID, "CALEVENT#"+calendarTestSource+"#"+eventID
	batch.Mutations = append(batch.Mutations, CalendarWriteMutation{Op: "upsert", EventID: eventID, ExternalID: "external-" + eventID, Event: &second})
	batch.Receipt.Upserted = len(batch.Mutations)
}

func calendarTestSharePuts(test *testing.T, input *dynamodb.TransactWriteItemsInput) (map[string]string, map[string]map[string]types.AttributeValue) {
	test.Helper()
	shares := make(map[string]string)
	events := make(map[string]map[string]types.AttributeValue)
	seen := make(map[string]bool)
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
		id := calendarTestString(test, key, "PK") + "|" + calendarTestString(test, key, "SK")
		if seen[id] {
			test.Fatalf("two operations on one item: %s", id)
		}
		seen[id] = true
		if operation.Put == nil {
			continue
		}
		sortKey := calendarTestString(test, operation.Put.Item, "SK")
		switch {
		case strings.HasPrefix(sortKey, "CALSHARE#"):
			shares[sortKey] = calendarTestString(test, operation.Put.Item, "eventId") + " if " +
				calendarTestExpression(operation.Put.ConditionExpression, operation.Put.ExpressionAttributeNames, operation.Put.ExpressionAttributeValues)
		case strings.HasPrefix(sortKey, "CALEVENT#"):
			events[calendarTestString(test, operation.Put.Item, "eventId")] = operation.Put.Item
		}
	}
	return shares, events
}

func TestApplyBatchShareCollisionSameBatchKeepsFirst(test *testing.T) {
	batch := calendarTestWrite()
	calendarTestShareMutation(&batch, 0, "acc_1", "mk")
	calendarTestSecondUpsert(&batch, "event-2")
	calendarTestShareMutation(&batch, 1, "acc_1", "mk")
	calls := 0
	mock := &calendarSDKMock{
		get: func(*dynamodb.GetItemInput) (*dynamodb.GetItemOutput, error) { return &dynamodb.GetItemOutput{}, nil },
		transact: func(input *dynamodb.TransactWriteItemsInput) (*dynamodb.TransactWriteItemsOutput, error) {
			calls++
			shares, events := calendarTestSharePuts(test, input)
			if len(shares) != 1 || !strings.HasPrefix(shares[model.CalendarShareSortKey(calendarTestRow().StartsAt, "mk", "owner")], "event-1 if attribute_not_exists (PK)") {
				test.Fatalf("exactly one share put for the first mutation expected: %v", shares)
			}
			if calendarTestString(test, events["event-1"], "meetingKey") != "mk" || events["event-2"]["meetingKey"] != nil || events["event-2"]["shareState"] != nil {
				test.Fatal("only the winner may record the share key")
			}
			return &dynamodb.TransactWriteItemsOutput{}, nil
		},
	}
	if _, err := calendarTestRepo(test, mock).ApplyCalendarBatch(context.Background(), "owner", calendarTestSource, calendarTestObserved(test, mock, batch)); err != nil || calls != 1 {
		test.Fatalf("collision must not fail the batch: %v calls=%d", err, calls)
	}
	if batch.Mutations[1].Event.MeetingKey != "mk" {
		test.Fatal("repository mutated the caller's event")
	}
}

func TestApplyBatchShareCollisionAcrossBatchesSkipsShare(test *testing.T) {
	batch := calendarTestWrite()
	calendarTestShareMutation(&batch, 0, "acc_1", "mk")
	other := calendarTestRow()
	other.EventID = "event-9"
	owned := calendarTestShare(other, "acc_1", "mk", "shared")
	owned.PK, owned.SK = "ACCOUNT#acc_1", model.CalendarShareSortKey(other.StartsAt, "mk", "owner")
	items := map[string]map[string]types.AttributeValue{owned.PK + "|" + owned.SK: calendarTestItem(test, owned)}
	calls := 0
	mock := &calendarSDKMock{
		get: calendarTestStore(test, items),
		transact: func(input *dynamodb.TransactWriteItemsInput) (*dynamodb.TransactWriteItemsOutput, error) {
			calls++
			shares, events := calendarTestSharePuts(test, input)
			if len(shares) != 0 || events["event-1"]["meetingKey"] != nil || len(input.TransactItems) != 4 {
				test.Fatalf("another event's share row must be left alone: %v", shares)
			}
			return &dynamodb.TransactWriteItemsOutput{}, nil
		},
	}
	if _, err := calendarTestRepo(test, mock).ApplyCalendarBatch(context.Background(), "owner", calendarTestSource, calendarTestObserved(test, mock, batch)); err != nil || calls != 1 {
		test.Fatalf("cross-batch collision must not fail the batch: %v calls=%d", err, calls)
	}
}

func TestApplyBatchShareSwapSucceeds(test *testing.T) {
	batch := calendarTestWrite()
	calendarTestShareMutation(&batch, 0, "acc_1", "mk_y")
	calendarTestSecondUpsert(&batch, "event-2")
	calendarTestShareMutation(&batch, 1, "acc_1", "mk_x")
	storedA := calendarTestRow()
	storedA.ShareAccountID, storedA.MeetingKey, storedA.ShareState = "acc_1", "mk_x", "shared"
	owned := calendarTestShare(storedA, "acc_1", "mk_x", "shared")
	owned.PK, owned.SK = "ACCOUNT#acc_1", model.CalendarShareSortKey(storedA.StartsAt, "mk_x", "owner")
	items := map[string]map[string]types.AttributeValue{
		"USER#owner|" + storedA.SK: calendarTestItem(test, storedA),
		owned.PK + "|" + owned.SK:  calendarTestItem(test, owned),
	}
	calls := 0
	mock := &calendarSDKMock{
		get: calendarTestStore(test, items),
		transact: func(input *dynamodb.TransactWriteItemsInput) (*dynamodb.TransactWriteItemsOutput, error) {
			calls++
			shares, events := calendarTestSharePuts(test, input)
			start := storedA.StartsAt
			if len(shares) != 2 || !strings.HasPrefix(shares[model.CalendarShareSortKey(start, "mk_y", "owner")], "event-1 if attribute_not_exists (PK)") ||
				!strings.HasPrefix(shares[model.CalendarShareSortKey(start, "mk_x", "owner")], "event-2 if") ||
				!strings.Contains(shares[model.CalendarShareSortKey(start, "mk_x", "owner")], "eventId = event-1") {
				test.Fatalf("swap must move mk_x to event-2 over event-1's row: %v", shares)
			}
			for _, operation := range input.TransactItems {
				if operation.Delete != nil && strings.HasPrefix(calendarTestString(test, operation.Delete.Key, "SK"), "CALSHARE#") {
					test.Fatal("a swapped key is overwritten, never deleted")
				}
			}
			if calendarTestString(test, events["event-1"], "meetingKey") != "mk_y" || calendarTestString(test, events["event-2"], "meetingKey") != "mk_x" {
				test.Fatal("both events must record their new keys")
			}
			return &dynamodb.TransactWriteItemsOutput{}, nil
		},
	}
	if _, err := calendarTestRepo(test, mock).ApplyCalendarBatch(context.Background(), "owner", calendarTestSource, calendarTestObserved(test, mock, batch)); err != nil || calls != 1 {
		test.Fatalf("swap must succeed: %v calls=%d", err, calls)
	}
}

// calendarTestOwnedShare is the stored share row an event owns, keyed PK|SK.
func calendarTestOwnedShare(test *testing.T, row CalendarEventRow, accountID, meetingKey, state string) (string, map[string]types.AttributeValue) {
	test.Helper()
	share := calendarTestShare(row, accountID, meetingKey, state)
	share.PK, share.SK = "ACCOUNT#"+accountID, model.CalendarShareSortKey(row.StartsAt, meetingKey, row.UserID)
	if state == "veto" {
		share.VetoEventIDs = []string{share.SourceID + "#" + share.EventID}
	}
	return share.PK + "|" + share.SK, calendarTestItem(test, share)
}

func TestApplyBatchVetoBeatsShareInSameBatch(test *testing.T) {
	batch := calendarTestWrite()
	calendarTestShareMutation(&batch, 0, "acc_1", "mk")
	calendarTestSecondUpsert(&batch, "event-2")
	calendarTestShareMutation(&batch, 1, "acc_1", "mk")
	veto := batch.Mutations[1].Share
	veto.State, veto.Ciphertext, batch.Mutations[1].Event.ShareState = "veto", "", "veto"
	calls := 0
	mock := &calendarSDKMock{
		get: func(*dynamodb.GetItemInput) (*dynamodb.GetItemOutput, error) { return &dynamodb.GetItemOutput{}, nil },
		transact: func(input *dynamodb.TransactWriteItemsInput) (*dynamodb.TransactWriteItemsOutput, error) {
			calls++
			shares, events := calendarTestSharePuts(test, input)
			if len(shares) != 1 || !strings.HasPrefix(shares[model.CalendarShareSortKey(calendarTestRow().StartsAt, "mk", "owner")], "event-2 if attribute_not_exists (PK)") {
				test.Fatalf("the veto claimant must own the key: %v", shares)
			}
			for _, operation := range input.TransactItems {
				if operation.Put != nil && strings.HasPrefix(calendarTestString(test, operation.Put.Item, "SK"), "CALSHARE#") &&
					calendarTestString(test, operation.Put.Item, "state") != "veto" {
					test.Fatal("veto row expected")
				}
			}
			if calendarTestString(test, events["event-2"], "shareState") != "veto" || events["event-1"]["meetingKey"] != nil {
				test.Fatal("displaced shared claimant must not record the key")
			}
			return &dynamodb.TransactWriteItemsOutput{}, nil
		},
	}
	if _, err := calendarTestRepo(test, mock).ApplyCalendarBatch(context.Background(), "owner", calendarTestSource, calendarTestObserved(test, mock, batch)); err != nil || calls != 1 {
		test.Fatalf("veto collision must succeed: %v calls=%d", err, calls)
	}
}

func calendarTestForeignOwner(test *testing.T, state string) (CalendarEventRow, map[string]map[string]types.AttributeValue) {
	other := calendarTestRow()
	other.EventID = "event-9"
	other.ShareAccountID, other.MeetingKey, other.ShareState = "acc_1", "mk", state
	shareID, shareItem := calendarTestOwnedShare(test, other, "acc_1", "mk", state)
	return other, map[string]map[string]types.AttributeValue{shareID: shareItem}
}

func TestApplyBatchVetoOverwritesForeignSharedOwner(test *testing.T) {
	batch := calendarTestWrite()
	calendarTestShareMutation(&batch, 0, "acc_1", "mk")
	veto := batch.Mutations[0].Share
	veto.State, veto.Ciphertext, batch.Mutations[0].Event.ShareState = "veto", "", "veto"
	_, items := calendarTestForeignOwner(test, "shared")
	calls := 0
	mock := &calendarSDKMock{
		get: calendarTestStore(test, items),
		transact: func(input *dynamodb.TransactWriteItemsInput) (*dynamodb.TransactWriteItemsOutput, error) {
			calls++
			shares, events := calendarTestSharePuts(test, input)
			put := shares[model.CalendarShareSortKey(calendarTestRow().StartsAt, "mk", "owner")]
			for _, wanted := range []string{"event-1 if", "userId = owner", "sourceId = " + calendarTestSource, "eventId = event-9", "state = shared"} {
				if !strings.Contains(put, wanted) {
					test.Fatalf("veto must replace the observed shared owner under an owner-scoped condition, missing %q: %s", wanted, put)
				}
			}
			if calendarTestString(test, events["event-1"], "shareState") != "veto" {
				test.Fatal("veto owner must record the key")
			}
			return &dynamodb.TransactWriteItemsOutput{}, nil
		},
	}
	if _, err := calendarTestRepo(test, mock).ApplyCalendarBatch(context.Background(), "owner", calendarTestSource, calendarTestObserved(test, mock, batch)); err != nil || calls != 1 {
		test.Fatalf("cross-batch veto failed: %v calls=%d", err, calls)
	}
}

func TestApplyBatchShareNeverReplacesForeignVeto(test *testing.T) {
	batch := calendarTestWrite()
	calendarTestShareMutation(&batch, 0, "acc_1", "mk")
	_, items := calendarTestForeignOwner(test, "veto")
	calls := 0
	mock := &calendarSDKMock{
		get: calendarTestStore(test, items),
		transact: func(input *dynamodb.TransactWriteItemsInput) (*dynamodb.TransactWriteItemsOutput, error) {
			calls++
			shares, events := calendarTestSharePuts(test, input)
			if len(shares) != 0 || events["event-1"]["meetingKey"] != nil {
				test.Fatalf("a share must not replace a veto: %v", shares)
			}
			return &dynamodb.TransactWriteItemsOutput{}, nil
		},
	}
	if _, err := calendarTestRepo(test, mock).ApplyCalendarBatch(context.Background(), "owner", calendarTestSource, calendarTestObserved(test, mock, batch)); err != nil || calls != 1 {
		test.Fatalf("share over veto must be skipped without failure: %v calls=%d", err, calls)
	}
}

func TestApplyBatchDisplacedOwnerLeavesWithoutFailing(test *testing.T) {
	// event-9 was displaced by event-1's veto in an earlier batch; its row still records mk.
	displaced, _ := calendarTestForeignOwner(test, "shared")
	displaced.SK = "CALEVENT#" + calendarTestSource + "#event-9"
	vetoOwner := calendarTestRow()
	shareID, shareItem := calendarTestOwnedShare(test, vetoOwner, "acc_1", "mk", "veto")
	items := map[string]map[string]types.AttributeValue{shareID: shareItem, "USER#owner|" + displaced.SK: calendarTestItem(test, displaced)}
	for _, op := range []string{"remove", "rekey"} {
		test.Run(op, func(test *testing.T) {
			batch := calendarTestWrite()
			batch.Mutations = []CalendarWriteMutation{{Op: "remove", EventID: "event-9", ExternalID: "external-9",
				PreviousShareKey: calendarTestShareKey(displaced, "acc_1", "mk")}}
			batch.Receipt.Upserted, batch.Receipt.Removed = 0, 1
			if op == "rekey" {
				row := calendarTestRow()
				row.EventID, row.SK = "event-9", displaced.SK
				batch.Mutations = []CalendarWriteMutation{{Op: "upsert", EventID: "event-9", ExternalID: "external-9", Event: &row}}
				batch.Receipt.Upserted, batch.Receipt.Removed = 1, 0
				calendarTestShareMutation(&batch, 0, "acc_1", "mk_new")
			}
			mock := &calendarSDKMock{
				get: calendarTestStore(test, items),
				transact: func(input *dynamodb.TransactWriteItemsInput) (*dynamodb.TransactWriteItemsOutput, error) {
					for _, operation := range input.TransactItems {
						if operation.Delete != nil && calendarTestString(test, operation.Delete.Key, "PK") == "ACCOUNT#acc_1" {
							test.Fatal("a displaced owner must not delete the veto it no longer owns")
						}
					}
					return &dynamodb.TransactWriteItemsOutput{}, nil
				},
			}
			if _, err := calendarTestRepo(test, mock).ApplyCalendarBatch(context.Background(), "owner", calendarTestSource, calendarTestObserved(test, mock, batch)); err != nil {
				test.Fatalf("displaced owner must sync: %v", err)
			}
		})
	}
}

// calendarTestVetoRow is a veto row for mk held by the given events of calendarTestSource.
func calendarTestVetoRow(test *testing.T, events ...string) (string, map[string]types.AttributeValue) {
	row := calendarTestRow()
	row.EventID = events[0]
	share := calendarTestShare(row, "acc_1", "mk", "veto")
	share.PK, share.SK = "ACCOUNT#acc_1", model.CalendarShareSortKey(row.StartsAt, "mk", "owner")
	for _, event := range events {
		share.VetoEventIDs = append(share.VetoEventIDs, calendarTestSource+"#"+event)
	}
	return share.PK + "|" + share.SK, calendarTestItem(test, share)
}

// calendarTestLeaveVeto runs a batch where event-1 (a veto member) drops its veto on mk.
func calendarTestLeaveVeto(test *testing.T, members []string, check func(types.TransactWriteItem)) {
	test.Helper()
	leaving := calendarTestRow()
	leaving.ShareAccountID, leaving.MeetingKey, leaving.ShareState = "acc_1", "mk", "veto"
	shareID, shareItem := calendarTestVetoRow(test, members...)
	items := map[string]map[string]types.AttributeValue{shareID: shareItem, "USER#owner|" + leaving.SK: calendarTestItem(test, leaving)}
	batch := calendarTestWrite() // event-1 is re-sent without a share
	calls := 0
	mock := &calendarSDKMock{
		get: calendarTestStore(test, items),
		transact: func(input *dynamodb.TransactWriteItemsInput) (*dynamodb.TransactWriteItemsOutput, error) {
			calls++
			found := false
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
				if calendarTestString(test, key, "PK") == "ACCOUNT#acc_1" {
					found = true
					check(operation)
				}
			}
			if !found {
				test.Fatal("leaving a veto must touch its row")
			}
			return &dynamodb.TransactWriteItemsOutput{}, nil
		},
	}
	if _, err := calendarTestRepo(test, mock).ApplyCalendarBatch(context.Background(), "owner", calendarTestSource, calendarTestObserved(test, mock, batch)); err != nil || calls != 1 {
		test.Fatalf("leaving a veto failed: %v calls=%d", err, calls)
	}
}

func TestApplyBatchLeavingSharedVetoKeepsRemainingMember(test *testing.T) {
	calendarTestLeaveVeto(test, []string{"event-9", "event-1"}, func(operation types.TransactWriteItem) {
		update := operation.Update
		if update == nil {
			test.Fatalf("a veto with remaining members must be updated, not deleted or replaced: %+v", operation)
		}
		fields := calendarTestExpression(update.UpdateExpression, update.ExpressionAttributeNames, update.ExpressionAttributeValues)
		condition := calendarTestExpression(update.ConditionExpression, update.ExpressionAttributeNames, update.ExpressionAttributeValues)
		if strings.Contains(fields, "ADD") || !strings.Contains(fields, "DELETE vetoEventIds ["+calendarTestSource+"#event-1]") {
			test.Fatalf("leaver must DELETE only itself: %q", fields)
		}
		if !strings.Contains(condition, "contains (vetoEventIds, "+calendarTestSource+"#event-1)") || !strings.Contains(condition, "size (vetoEventIds) > 1") {
			test.Fatalf("leave must be conditioned on membership: %s", condition)
		}
	})
}

// calendarTestVetoPathActions counts the update actions (SET/ADD/DELETE/REMOVE) that name vetoEventIds.
func calendarTestVetoPathActions(fields string) int {
	count := 0
	for _, clause := range regexp.MustCompile(`\b(SET|ADD|DELETE|REMOVE)\b`).Split(fields, -1) {
		if strings.Contains(clause, "vetoEventIds") {
			count++
		}
	}
	return count
}

func TestApplyBatchVetoJoinAndLeaveSameKeyUsesSingleSet(test *testing.T) {
	// The veto row for mk holds only event-1. In one batch event-1 drops its veto and
	// event-2 newly vetoes mk: one joiner and one leaver on the same key.
	leaving := calendarTestRow()
	leaving.ShareAccountID, leaving.MeetingKey, leaving.ShareState = "acc_1", "mk", "veto"
	shareID, shareItem := calendarTestVetoRow(test, "event-1")
	items := map[string]map[string]types.AttributeValue{shareID: shareItem, "USER#owner|" + leaving.SK: calendarTestItem(test, leaving)}
	batch := calendarTestWrite() // event-1 is re-sent without a share
	calendarTestSecondUpsert(&batch, "event-2")
	calendarTestShareMutation(&batch, 1, "acc_1", "mk")
	veto := batch.Mutations[1].Share
	veto.State, veto.Ciphertext, batch.Mutations[1].Event.ShareState = "veto", "", "veto"
	final := calendarTestSource + "#event-2"
	calls := 0
	mock := &calendarSDKMock{
		get: calendarTestStore(test, items),
		transact: func(input *dynamodb.TransactWriteItemsInput) (*dynamodb.TransactWriteItemsOutput, error) {
			calls++
			touches := 0
			for _, operation := range input.TransactItems {
				if operation.Put != nil && calendarTestString(test, operation.Put.Item, "PK") == "ACCOUNT#acc_1" {
					test.Fatal("a joiner must not replace a veto row")
				}
				if operation.Delete != nil && calendarTestString(test, operation.Delete.Key, "PK") == "ACCOUNT#acc_1" {
					test.Fatal("a veto row that keeps a member must not be deleted")
				}
				if operation.Update == nil {
					continue
				}
				update := operation.Update
				fields := calendarTestExpression(update.UpdateExpression, update.ExpressionAttributeNames, update.ExpressionAttributeValues)
				if calendarTestVetoPathActions(fields) > 1 {
					test.Fatalf("vetoEventIds must appear in at most one update action: %q", fields)
				}
				if calendarTestString(test, update.Key, "PK") != "ACCOUNT#acc_1" {
					continue
				}
				touches++
				if strings.Contains(fields, "ADD") || strings.Contains(fields, "DELETE") || !strings.Contains(fields, "SET vetoEventIds = ["+final+"]") {
					test.Fatalf("join plus leave must be one SET of the final set: %q", fields)
				}
				condition := calendarTestExpression(update.ConditionExpression, update.ExpressionAttributeNames, update.ExpressionAttributeValues)
				for _, want := range []string{"userId = owner", "state = veto", "size (vetoEventIds) = 1", "contains (vetoEventIds, " + calendarTestSource + "#event-1)"} {
					if !strings.Contains(condition, want) {
						test.Fatalf("SET must be conditioned on the set as read, missing %q: %s", want, condition)
					}
				}
			}
			if touches != 1 {
				test.Fatalf("exactly one update on the share key expected: %d", touches)
			}
			return &dynamodb.TransactWriteItemsOutput{}, nil
		},
	}
	if _, err := calendarTestRepo(test, mock).ApplyCalendarBatch(context.Background(), "owner", calendarTestSource, calendarTestObserved(test, mock, batch)); err != nil || calls != 1 {
		test.Fatalf("join plus leave failed: %v calls=%d", err, calls)
	}
}

func TestApplyBatchLastVetoerDeletesRow(test *testing.T) {
	calendarTestLeaveVeto(test, []string{"event-1"}, func(operation types.TransactWriteItem) {
		deletion := operation.Delete
		if deletion == nil {
			test.Fatalf("the last vetoer must delete the row: %+v", operation)
		}
		condition := calendarTestExpression(deletion.ConditionExpression, deletion.ExpressionAttributeNames, deletion.ExpressionAttributeValues)
		if !strings.Contains(condition, "size (vetoEventIds) = 1") || !strings.Contains(condition, "contains (vetoEventIds, "+calendarTestSource+"#event-1)") ||
			!strings.Contains(condition, "state = veto") {
			test.Fatalf("delete must pin the set as read: %s", condition)
		}
	})
}

func TestApplyBatchVetoJoinsExistingForeignVeto(test *testing.T) {
	batch := calendarTestWrite()
	calendarTestShareMutation(&batch, 0, "acc_1", "mk")
	veto := batch.Mutations[0].Share
	veto.State, veto.Ciphertext, batch.Mutations[0].Event.ShareState = "veto", "", "veto"
	shareID, shareItem := calendarTestVetoRow(test, "event-9")
	calls := 0
	mock := &calendarSDKMock{
		get: calendarTestStore(test, map[string]map[string]types.AttributeValue{shareID: shareItem}),
		transact: func(input *dynamodb.TransactWriteItemsInput) (*dynamodb.TransactWriteItemsOutput, error) {
			calls++
			adds := 0
			for _, operation := range input.TransactItems {
				if operation.Update != nil && calendarTestString(test, operation.Update.Key, "PK") == "ACCOUNT#acc_1" {
					fields := calendarTestExpression(operation.Update.UpdateExpression, operation.Update.ExpressionAttributeNames, operation.Update.ExpressionAttributeValues)
					if !strings.Contains(fields, "ADD vetoEventIds ["+calendarTestSource+"#event-1]") || strings.Contains(fields, "DELETE") {
						test.Fatalf("joiner must ADD itself: %s", fields)
					}
					adds++
				}
				if operation.Put != nil && calendarTestString(test, operation.Put.Item, "PK") == "ACCOUNT#acc_1" {
					test.Fatal("joining must not overwrite the veto row")
				}
				if operation.Put != nil && calendarTestString(test, operation.Put.Item, "SK") == batch.Mutations[0].Event.SK &&
					calendarTestString(test, operation.Put.Item, "shareState") != "veto" {
					test.Fatal("joined veto must be recorded on the event")
				}
			}
			if adds != 1 {
				test.Fatalf("one ADD expected: %d", adds)
			}
			return &dynamodb.TransactWriteItemsOutput{}, nil
		},
	}
	if _, err := calendarTestRepo(test, mock).ApplyCalendarBatch(context.Background(), "owner", calendarTestSource, calendarTestObserved(test, mock, batch)); err != nil || calls != 1 {
		test.Fatalf("joining a veto failed: %v calls=%d", err, calls)
	}
}

func TestApplyBatchSharedClaimantReplacesOnlyEmptiedVeto(test *testing.T) {
	// event-1 was the only vetoer and now wants mk shared: the veto row becomes its shared row.
	leaving := calendarTestRow()
	leaving.ShareAccountID, leaving.MeetingKey, leaving.ShareState = "acc_1", "mk", "veto"
	shareID, shareItem := calendarTestVetoRow(test, "event-1")
	batch := calendarTestWrite()
	calendarTestShareMutation(&batch, 0, "acc_1", "mk")
	mock := &calendarSDKMock{
		get: calendarTestStore(test, map[string]map[string]types.AttributeValue{shareID: shareItem, "USER#owner|" + leaving.SK: calendarTestItem(test, leaving)}),
		transact: func(input *dynamodb.TransactWriteItemsInput) (*dynamodb.TransactWriteItemsOutput, error) {
			shares, _ := calendarTestSharePuts(test, input)
			put := shares[model.CalendarShareSortKey(leaving.StartsAt, "mk", "owner")]
			if !strings.HasPrefix(put, "event-1 if") || !strings.Contains(put, "size (vetoEventIds) = 1") {
				test.Fatalf("emptied veto must be replaced under its set as read: %v", shares)
			}
			return &dynamodb.TransactWriteItemsOutput{}, nil
		},
	}
	if _, err := calendarTestRepo(test, mock).ApplyCalendarBatch(context.Background(), "owner", calendarTestSource, calendarTestObserved(test, mock, batch)); err != nil {
		test.Fatal(err)
	}
}

// Identity and absence checks live in TestGetCalendarEventRow_IdentityChecked; this
// pins that the share state written by the share planner survives the read.
func TestGetCalendarEventRowReturnsShareFields(test *testing.T) {
	stored := calendarTestRow()
	stored.ShareAccountID, stored.MeetingKey, stored.ShareState = "acc_1", "mk", "shared"
	items := map[string]map[string]types.AttributeValue{stored.PK + "|" + stored.SK: calendarTestItem(test, stored)}
	repo := calendarTestRepo(test, &calendarSDKMock{get: calendarTestStore(test, items)})
	row, err := repo.GetCalendarEventRow(context.Background(), "owner", calendarTestSource, "event-1")
	if err != nil || row == nil || row.ShareAccountID != "acc_1" || row.MeetingKey != "mk" || row.ShareState != "shared" {
		test.Fatalf("stored row: %+v %v", row, err)
	}
}

func TestApplyBatchExpectedVersionMismatchFailsBeforeWrite(test *testing.T) {
	stored := calendarTestRow()
	stored.ShareVersion = 2
	for _, scenario := range []struct {
		name     string
		expected CalendarRowVersion
		wantErr  error
	}{
		{"share version moved", CalendarRowVersion{Exists: true, Revision: 1, ShareVersion: 1}, ErrCalendarShareStateChanged},
		{"revision moved", CalendarRowVersion{Exists: true, Revision: 0, ShareVersion: 2}, ErrCalendarShareStateChanged},
		{"planned as absent", CalendarRowVersion{}, ErrCalendarShareStateChanged},
		{"as read", CalendarRowVersionOf(&stored), nil},
	} {
		test.Run(scenario.name, func(test *testing.T) {
			batch := calendarTestWrite()
			expected := scenario.expected
			batch.Mutations[0].Expected = &expected
			items := map[string]map[string]types.AttributeValue{"USER#owner|" + stored.SK: calendarTestItem(test, stored)}
			calls := 0
			mock := &calendarSDKMock{get: calendarTestStore(test, items), transact: func(*dynamodb.TransactWriteItemsInput) (*dynamodb.TransactWriteItemsOutput, error) {
				calls++
				return &dynamodb.TransactWriteItemsOutput{}, nil
			}}
			_, err := calendarTestRepo(test, mock).ApplyCalendarBatch(context.Background(), "owner", calendarTestSource, calendarTestObserved(test, mock, batch))
			if scenario.wantErr == nil {
				if err != nil || calls != 1 {
					test.Fatalf("matching version must write: %v calls=%d", err, calls)
				}
				return
			}
			if !errors.Is(err, scenario.wantErr) || calls != 0 {
				test.Fatalf("stale plan must fail before the transaction: %v calls=%d", err, calls)
			}
		})
	}
}

// TestApplyBatchStalePlanCannotReplaceVeto is the review scenario: the event vetoes mk, and a
// sync planned before that veto asks for shared on mk. The stale plan must never replace the veto.
func TestApplyBatchStalePlanCannotReplaceVeto(test *testing.T) {
	stored := calendarTestRow()
	stored.ShareAccountID, stored.MeetingKey, stored.ShareState, stored.ShareVersion = "acc_1", "mk", "veto", 1
	shareID, shareItem := calendarTestOwnedShare(test, stored, "acc_1", "mk", "veto")
	items := map[string]map[string]types.AttributeValue{"USER#owner|" + stored.SK: calendarTestItem(test, stored), shareID: shareItem}
	calls := 0
	mock := &calendarSDKMock{get: calendarTestStore(test, items), transact: func(*dynamodb.TransactWriteItemsInput) (*dynamodb.TransactWriteItemsOutput, error) {
		calls++
		return &dynamodb.TransactWriteItemsOutput{}, nil
	}}
	batch := calendarTestWrite()
	calendarTestShareMutation(&batch, 0, "acc_1", "mk")
	planned := CalendarRowVersion{Exists: true, Revision: stored.Revision}
	batch.Mutations[0].Expected = &planned
	repo := calendarTestRepo(test, mock)
	if _, err := repo.ApplyCalendarBatch(context.Background(), "owner", calendarTestSource, batch); !errors.Is(err, ErrCalendarShareStateChanged) || calls != 0 {
		test.Fatalf("a plan from before the veto must fail before any write: %v calls=%d", err, calls)
	}
	batch.Mutations[0].Expected = nil
	if _, err := repo.ApplyCalendarBatch(context.Background(), "owner", calendarTestSource, batch); !errors.Is(err, ErrInvalidCalendarBatch) || calls != 0 {
		test.Fatalf("a share mutation without an observed version must be rejected: %v calls=%d", err, calls)
	}
}

// TestApplyBatchNilExpectedNeverChangesShareState pins the only mutations that may omit Expected.
func TestApplyBatchNilExpectedNeverChangesShareState(test *testing.T) {
	plain := calendarTestRow()
	shared := calendarTestRow()
	shared.ShareAccountID, shared.MeetingKey, shared.ShareState = "acc_1", "mk", "shared"
	vetoed := shared
	vetoed.ShareState = "veto"
	shareID, shareItem := calendarTestOwnedShare(test, shared, "acc_1", "mk", "shared")
	for _, scenario := range []struct {
		name    string
		stored  *CalendarEventRow
		mutate  func(*CalendarWriteBatch)
		wantErr error
	}{
		{"share on a new event", nil, func(batch *CalendarWriteBatch) { calendarTestShareMutation(batch, 0, "acc_1", "mk") }, ErrInvalidCalendarBatch},
		{"previous key only", nil, func(batch *CalendarWriteBatch) {
			batch.Mutations[0].PreviousShareKey = calendarTestShareKey(plain, "acc_1", "mk")
		}, ErrInvalidCalendarBatch},
		{"plain upsert of a shared event", &shared, nil, ErrInvalidCalendarBatch},
		{"plain upsert of a vetoed event", &vetoed, nil, ErrInvalidCalendarBatch},
		{"plain upsert of a new event", nil, nil, nil},
		{"plain upsert of a never-shared event", &plain, nil, nil},
		{"remove of a shared event", &shared, func(batch *CalendarWriteBatch) {
			batch.Receipt.Upserted, batch.Receipt.Removed = 0, 1
			batch.Mutations = []CalendarWriteMutation{{Op: "remove", EventID: "event-1", ExternalID: "provider-occurrence-1"}}
		}, nil},
	} {
		test.Run(scenario.name, func(test *testing.T) {
			items := map[string]map[string]types.AttributeValue{shareID: shareItem}
			if scenario.stored != nil {
				items["USER#owner|"+plain.SK] = calendarTestItem(test, *scenario.stored)
			}
			calls := 0
			var ops calendarTestOps
			mock := &calendarSDKMock{get: calendarTestStore(test, items), transact: func(input *dynamodb.TransactWriteItemsInput) (*dynamodb.TransactWriteItemsOutput, error) {
				calls++
				ops = calendarTestCollect(test, input)
				return &dynamodb.TransactWriteItemsOutput{}, nil
			}}
			batch := calendarTestWrite()
			if scenario.mutate != nil {
				scenario.mutate(&batch)
			}
			_, err := calendarTestRepo(test, mock).ApplyCalendarBatch(context.Background(), "owner", calendarTestSource, batch)
			if scenario.wantErr != nil {
				if !errors.Is(err, scenario.wantErr) || calls != 0 {
					test.Fatalf("must be rejected before any write: %v calls=%d", err, calls)
				}
				return
			}
			if err != nil || calls != 1 {
				test.Fatalf("must write: %v calls=%d", err, calls)
			}
			if scenario.name == "remove of a shared event" && !ops.deletes[shareID] {
				test.Fatalf("a remove retires the event's share row: %v", ops.deletes)
			}
			for id := range ops.puts {
				if strings.HasPrefix(id, "ACCOUNT#") {
					test.Fatalf("a mutation without Expected wrote a share row: %s", id)
				}
			}
		})
	}
}
