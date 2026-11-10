package repository

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

var specReqTestNow = time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)

type specReqSDKMock struct {
	test     *testing.T
	query    func(*dynamodb.QueryInput) (*dynamodb.QueryOutput, error)
	transact func(*dynamodb.TransactWriteItemsInput) (*dynamodb.TransactWriteItemsOutput, error)
	update   func(*dynamodb.UpdateItemInput) (*dynamodb.UpdateItemOutput, error)
}

func (mock *specReqSDKMock) GetItem(_ context.Context, input *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	mock.test.Fatalf("unexpected GetItem: %v", input.Key)
	return nil, nil
}

func (mock *specReqSDKMock) Query(_ context.Context, input *dynamodb.QueryInput, _ ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	mock.test.Helper()
	if !aws.ToBool(input.ConsistentRead) {
		mock.test.Fatal("task queries must be consistent")
	}
	return mock.query(input)
}

func (mock *specReqSDKMock) TransactWriteItems(_ context.Context, input *dynamodb.TransactWriteItemsInput, options ...func(*dynamodb.Options)) (*dynamodb.TransactWriteItemsOutput, error) {
	settings := dynamodb.Options{}
	for _, option := range options {
		option(&settings)
	}
	if settings.Retryer == nil || settings.Retryer.MaxAttempts() != 1 {
		mock.test.Fatal("task creation must not be retried automatically")
	}
	return mock.transact(input)
}

func (mock *specReqSDKMock) UpdateItem(_ context.Context, input *dynamodb.UpdateItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
	return mock.update(input)
}

func specReqTestRepo(test *testing.T, mock *specReqSDKMock) *SpecReqTaskRepository {
	mock.test = test
	repo := NewSpecReqTaskRepository(mock, "specreq-test")
	repo.now = func() time.Time { return specReqTestNow }
	return repo
}

func specReqTestRow(eventID string) SpecReqTaskRow {
	return SpecReqTaskRow{
		PK: "USER#owner", SK: "SRTASK#" + eventID, UserID: "owner", EventID: eventID, SourceID: calendarTestSource,
		State: "issue_needed", Path: "mine", OccurrenceStartsAt: specReqTestNow, OccurrenceEndsAt: specReqTestNow.Add(time.Hour),
		CreatedAt: specReqTestNow, UpdatedAt: specReqTestNow, StateChangedAt: specReqTestNow, Version: 1, EntityType: "SPECREQ_TASK",
	}
}

// specReqResolve substitutes expression placeholders so assertions read naturally.
func specReqResolve(expr *string, names map[string]string, values map[string]types.AttributeValue) string {
	return specReqPlaceholder.ReplaceAllStringFunc(aws.ToString(expr), func(token string) string {
		if name, ok := names[token]; ok {
			return name
		}
		if value, ok := values[token]; ok {
			var decoded any
			_ = attributevalue.Unmarshal(value, &decoded)
			return "<" + fmt.Sprint(decoded) + ">"
		}
		return token
	})
}

var specReqPlaceholder = regexp.MustCompile(`[#:][A-Za-z0-9_]+`)

func specReqCancellation(codes ...string) error {
	reasons := make([]types.CancellationReason, 0, len(codes))
	for _, code := range codes {
		reasons = append(reasons, types.CancellationReason{Code: aws.String(code)})
	}
	return &types.TransactionCanceledException{CancellationReasons: reasons}
}

func TestCreateSpecReqTask_ChecksEventRevisionAndAbsence(test *testing.T) {
	var captured *dynamodb.TransactWriteItemsInput
	repo := specReqTestRepo(test, &specReqSDKMock{transact: func(input *dynamodb.TransactWriteItemsInput) (*dynamodb.TransactWriteItemsOutput, error) {
		captured = input
		return &dynamodb.TransactWriteItemsOutput{}, nil
	}})
	if err := repo.CreateSpecReqTask(context.Background(), specReqTestRow("event-1"), 7); err != nil {
		test.Fatal(err)
	}
	if len(captured.TransactItems) != 2 || captured.TransactItems[0].ConditionCheck == nil || captured.TransactItems[1].Put == nil {
		test.Fatalf("want ConditionCheck then Put, got %+v", captured.TransactItems)
	}
	check, put := captured.TransactItems[0].ConditionCheck, captured.TransactItems[1].Put
	if pk := check.Key["PK"].(*types.AttributeValueMemberS).Value; pk != "USER#owner" {
		test.Fatalf("check PK = %s", pk)
	}
	if sk := check.Key["SK"].(*types.AttributeValueMemberS).Value; sk != "CALEVENT#"+calendarTestSource+"#event-1" {
		test.Fatalf("check SK = %s", sk)
	}
	got := specReqResolve(check.ConditionExpression, check.ExpressionAttributeNames, check.ExpressionAttributeValues)
	for _, want := range []string{"attribute_exists (PK)", "userId = <owner>", "revision = <7>"} {
		if !strings.Contains(got, want) {
			test.Fatalf("check condition %q lacks %q", got, want)
		}
	}
	if got := specReqResolve(put.ConditionExpression, put.ExpressionAttributeNames, nil); got != "attribute_not_exists (PK)" {
		test.Fatalf("put condition = %q", got)
	}
	if put.Item["PK"].(*types.AttributeValueMemberS).Value != "USER#owner" || put.Item["SK"].(*types.AttributeValueMemberS).Value != "SRTASK#event-1" ||
		put.Item["entityType"].(*types.AttributeValueMemberS).Value != "SPECREQ_TASK" {
		test.Fatalf("unexpected put item keys: %v", put.Item)
	}
	if _, stored := put.Item["PreviousState"]; stored {
		test.Fatal("PreviousState must not be stored")
	}
}

func TestCreateSpecReqTask_MapsCancellationReasons(test *testing.T) {
	for _, tc := range []struct {
		name  string
		codes []string
		want  error
	}{
		{"event gone", []string{"ConditionalCheckFailed", "None"}, ErrSpecReqEventGone},
		{"task exists", []string{"None", "ConditionalCheckFailed"}, ErrConditionFailed},
	} {
		test.Run(tc.name, func(test *testing.T) {
			repo := specReqTestRepo(test, &specReqSDKMock{transact: func(*dynamodb.TransactWriteItemsInput) (*dynamodb.TransactWriteItemsOutput, error) {
				return nil, specReqCancellation(tc.codes...)
			}})
			if err := repo.CreateSpecReqTask(context.Background(), specReqTestRow("event-1"), 1); !errors.Is(err, tc.want) {
				test.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestUpdateSpecReqTask_VersionCondition(test *testing.T) {
	var captured *dynamodb.UpdateItemInput
	repo := specReqTestRepo(test, &specReqSDKMock{update: func(input *dynamodb.UpdateItemInput) (*dynamodb.UpdateItemOutput, error) {
		captured = input
		return &dynamodb.UpdateItemOutput{}, nil
	}})
	row := specReqTestRow("event-1")
	row.PreviousState, row.State = "issue_needed", "issued"
	row.SpecReqID = "SR-1"
	if err := repo.UpdateSpecReqTask(context.Background(), row, 4); err != nil {
		test.Fatal(err)
	}
	update := specReqResolve(captured.UpdateExpression, captured.ExpressionAttributeNames, captured.ExpressionAttributeValues)
	condition := specReqResolve(captured.ConditionExpression, captured.ExpressionAttributeNames, captured.ExpressionAttributeValues)
	for _, want := range []string{"version = <5>", "stateChangedAt = <", "updatedAt = <", "specReqId = <SR-1>", "REMOVE", "failureCode"} {
		if !strings.Contains(update, want) {
			test.Fatalf("update %q lacks %q", update, want)
		}
	}
	for _, want := range []string{"version = <4>", "attribute_exists (PK)"} {
		if !strings.Contains(condition, want) {
			test.Fatalf("condition %q lacks %q", condition, want)
		}
	}
	if removed := strings.SplitN(strings.SplitN(update, "REMOVE", 2)[1], "\n", 2)[0]; strings.Contains(removed, "specReqId") || strings.Contains(removed, "path") {
		test.Fatalf("non-empty optional must not be removed: %q", update)
	}
}

func TestUpdateSpecReqTask_StateChangedAtOnlyOnChange(test *testing.T) {
	var captured *dynamodb.UpdateItemInput
	repo := specReqTestRepo(test, &specReqSDKMock{update: func(input *dynamodb.UpdateItemInput) (*dynamodb.UpdateItemOutput, error) {
		captured = input
		return &dynamodb.UpdateItemOutput{}, nil
	}})
	row := specReqTestRow("event-1")
	row.PreviousState = row.State
	if err := repo.UpdateSpecReqTask(context.Background(), row, 1); err != nil {
		test.Fatal(err)
	}
	if update := specReqResolve(captured.UpdateExpression, captured.ExpressionAttributeNames, captured.ExpressionAttributeValues); strings.Contains(update, "stateChangedAt") {
		test.Fatalf("unchanged state must not move stateChangedAt: %q", update)
	}
	row.PreviousState = ""
	if err := repo.UpdateSpecReqTask(context.Background(), row, 1); !errors.Is(err, ErrInvalidSpecReqTask) {
		test.Fatalf("missing previous state: %v", err)
	}
}

func TestUpdateSpecReqTask_MapsConditionFailure(test *testing.T) {
	repo := specReqTestRepo(test, &specReqSDKMock{update: func(*dynamodb.UpdateItemInput) (*dynamodb.UpdateItemOutput, error) {
		return nil, &types.ConditionalCheckFailedException{}
	}})
	row := specReqTestRow("event-1")
	row.PreviousState = row.State
	if err := repo.UpdateSpecReqTask(context.Background(), row, 1); !errors.Is(err, ErrConditionFailed) {
		test.Fatalf("got %v", err)
	}
}

func specReqItem(test *testing.T, row SpecReqTaskRow) map[string]types.AttributeValue {
	item, err := attributevalue.MarshalMap(row)
	if err != nil {
		test.Fatal(err)
	}
	return item
}

func TestListSpecReqTasks_PaginatesAndBindsCursor(test *testing.T) {
	var inputs []*dynamodb.QueryInput
	repo := specReqTestRepo(test, &specReqSDKMock{query: func(input *dynamodb.QueryInput) (*dynamodb.QueryOutput, error) {
		inputs = append(inputs, input)
		if input.ExclusiveStartKey == nil {
			return &dynamodb.QueryOutput{Items: []map[string]types.AttributeValue{specReqItem(test, specReqTestRow("event-1"))},
				LastEvaluatedKey: map[string]types.AttributeValue{
					"PK": &types.AttributeValueMemberS{Value: "USER#owner"}, "SK": &types.AttributeValueMemberS{Value: "SRTASK#event-1"}}}, nil
		}
		if sk := input.ExclusiveStartKey["SK"].(*types.AttributeValueMemberS).Value; sk != "SRTASK#event-1" {
			test.Fatalf("second page started at %s", sk)
		}
		return &dynamodb.QueryOutput{Items: []map[string]types.AttributeValue{specReqItem(test, specReqTestRow("event-2"))}}, nil
	}})
	rows, next, err := repo.ListSpecReqTasks(context.Background(), "owner", "", 1)
	if err != nil || len(rows) != 1 || next == nil {
		test.Fatalf("page 1: %v %v %v", rows, next, err)
	}
	if got := specReqResolve(inputs[0].KeyConditionExpression, inputs[0].ExpressionAttributeNames, inputs[0].ExpressionAttributeValues); !strings.Contains(got, "begins_with (SK, <SRTASK#>)") {
		test.Fatalf("key condition = %q", got)
	}
	rows, next, err = repo.ListSpecReqTasks(context.Background(), "owner", *firstCursor(test, repo), 1)
	if err != nil || len(rows) != 1 || rows[0].EventID != "event-2" || next != nil {
		test.Fatalf("page 2: %v %v %v", rows, next, err)
	}
	cursor := firstCursor(test, repo)
	if _, _, err := repo.ListSpecReqTasks(context.Background(), "other", *cursor, 1); !errors.Is(err, ErrInvalidSpecReqCursor) {
		test.Fatalf("foreign user cursor: %v", err)
	}
	if _, _, err := repo.ListSpecReqTasks(context.Background(), "owner", *cursor, 2); !errors.Is(err, ErrInvalidSpecReqCursor) {
		test.Fatalf("limit-changed cursor: %v", err)
	}
	for _, limit := range []int32{0, 21, -1} {
		if _, _, err := repo.ListSpecReqTasks(context.Background(), "owner", "", limit); !errors.Is(err, ErrInvalidSpecReqTask) {
			test.Fatalf("limit %d: %v", limit, err)
		}
	}
}

func firstCursor(test *testing.T, repo *SpecReqTaskRepository) *string {
	test.Helper()
	scope := calendarCursor{Version: 1, Kind: specReqTaskCursorKind, UserID: "owner", Limit: 1}
	cursor, err := encodeCalendarCursor(scope, map[string]types.AttributeValue{
		"PK": &types.AttributeValueMemberS{Value: "USER#owner"}, "SK": &types.AttributeValueMemberS{Value: "SRTASK#event-1"}}, "SRTASK#", "SRTASK#~")
	if err != nil || cursor == nil {
		test.Fatal(err)
	}
	return cursor
}

func TestListSpecReqTasks_RejectsForeignRows(test *testing.T) {
	foreign := specReqTestRow("event-1")
	foreign.UserID = "intruder"
	for name, row := range map[string]SpecReqTaskRow{"foreign userId": foreign, "foreign partition": func() SpecReqTaskRow {
		row := specReqTestRow("event-1")
		row.PK = "USER#intruder"
		return row
	}()} {
		test.Run(name, func(test *testing.T) {
			repo := specReqTestRepo(test, &specReqSDKMock{query: func(*dynamodb.QueryInput) (*dynamodb.QueryOutput, error) {
				return &dynamodb.QueryOutput{Items: []map[string]types.AttributeValue{specReqItem(test, row)}}, nil
			}})
			if _, _, err := repo.ListSpecReqTasks(context.Background(), "owner", "", 5); !errors.Is(err, ErrCalendarCorruptData) {
				test.Fatalf("got %v", err)
			}
		})
	}
}

func TestSpecReqTaskRowCursor_ResumesAfterRow(test *testing.T) {
	var started string
	repo := specReqTestRepo(test, &specReqSDKMock{query: func(input *dynamodb.QueryInput) (*dynamodb.QueryOutput, error) {
		started = input.ExclusiveStartKey["SK"].(*types.AttributeValueMemberS).Value
		return &dynamodb.QueryOutput{}, nil
	}})
	row := specReqTestRow("event-1")
	row.PK, row.SK, row.UserID, row.EntityType = "USER#owner", "SRTASK#event-1", "owner", specReqTaskEntityType
	cursor, err := repo.SpecReqTaskRowCursor("owner", 5, row)
	if err != nil || cursor == "" {
		test.Fatalf("cursor: %q %v", cursor, err)
	}
	if _, _, err := repo.ListSpecReqTasks(context.Background(), "owner", cursor, 5); err != nil || started != "SRTASK#event-1" {
		test.Fatalf("resume: %q %v", started, err)
	}
	if _, _, err := repo.ListSpecReqTasks(context.Background(), "owner", cursor, 4); !errors.Is(err, ErrInvalidSpecReqCursor) {
		test.Fatalf("limit-changed: %v", err)
	}
	if _, err := repo.SpecReqTaskRowCursor("other", 5, row); !errors.Is(err, ErrInvalidSpecReqTask) {
		test.Fatalf("foreign row: %v", err)
	}
}

func TestUpdateSpecReqTask_TerminalTTL(test *testing.T) {
	cap := specReqTestNow.Add(14 * 24 * time.Hour).Unix()
	cases := []struct {
		name    string
		state   string
		path    string
		current int64
		want    string // substring of the resolved update; empty means TTL untouched
	}{
		{"closed", "closed", "mine", cap + 1000, fmt.Sprintf("pendingShareExpiresAt = <%v>", float64(cap))},
		{"resolved", "resolved", "theirs", cap + 1000, fmt.Sprintf("pendingShareExpiresAt = <%v>", float64(cap))},
		{"mine issued", "issued", "mine", cap + 1000, fmt.Sprintf("pendingShareExpiresAt = <%v>", float64(cap))},
		{"missing ttl", "closed", "mine", 0, fmt.Sprintf("pendingShareExpiresAt = <%v>", float64(cap))},
		{"earlier ttl kept", "closed", "mine", cap - 1000, ""},
		{"theirs issued is not terminal", "issued", "theirs", cap + 1000, ""},
		{"non-terminal", "requested", "theirs", cap + 1000, ""},
	}
	for _, tc := range cases {
		test.Run(tc.name, func(test *testing.T) {
			var captured *dynamodb.UpdateItemInput
			repo := specReqTestRepo(test, &specReqSDKMock{update: func(input *dynamodb.UpdateItemInput) (*dynamodb.UpdateItemOutput, error) {
				captured = input
				return &dynamodb.UpdateItemOutput{}, nil
			}})
			row := specReqTestRow("event-1")
			row.PreviousState, row.State, row.Path, row.ExpiresAt = "issue_needed", tc.state, tc.path, tc.current
			if err := repo.UpdateSpecReqTask(context.Background(), row, 1); err != nil {
				test.Fatal(err)
			}
			update := specReqResolve(captured.UpdateExpression, captured.ExpressionAttributeNames, captured.ExpressionAttributeValues)
			if tc.want == "" {
				if strings.Contains(update, "pendingShareExpiresAt") {
					test.Fatalf("ttl must be unchanged: %q", update)
				}
			} else if !strings.Contains(update, tc.want) {
				test.Fatalf("update %q lacks %q", update, tc.want)
			}
		})
	}
}
