package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

type calendarReadSDK struct {
	test       *testing.T
	items      map[string]map[string]types.AttributeValue
	partitions []string
}

func (sdk *calendarReadSDK) GetItem(_ context.Context, input *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	sdk.test.Helper()
	if !aws.ToBool(input.ConsistentRead) {
		sdk.test.Fatal("calendar canonical reads must be consistent")
	}
	partition := input.Key["PK"].(*types.AttributeValueMemberS).Value
	sortKey := input.Key["SK"].(*types.AttributeValueMemberS).Value
	sdk.partitions = append(sdk.partitions, partition)
	return &dynamodb.GetItemOutput{Item: sdk.items[partition+"/"+sortKey]}, nil
}

func (sdk *calendarReadSDK) Query(_ context.Context, input *dynamodb.QueryInput, _ ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	sdk.test.Helper()
	if !aws.ToBool(input.ConsistentRead) || !aws.ToBool(input.ScanIndexForward) {
		sdk.test.Fatal("calendar query must be consistent and chronological")
	}
	var partition string
	var boundaries []string
	for _, value := range input.ExpressionAttributeValues {
		if text, ok := value.(*types.AttributeValueMemberS); ok {
			if strings.HasPrefix(text.Value, "USER#") {
				partition = text.Value
			}
			if strings.HasPrefix(text.Value, "CALTIME#") {
				boundaries = append(boundaries, text.Value)
			}
		}
	}
	if len(boundaries) != 2 || partition == "" {
		sdk.test.Fatal("query omitted owner/window key bounds")
	}
	sort.Strings(boundaries)
	sdk.partitions = append(sdk.partitions, partition)
	after := ""
	if len(input.ExclusiveStartKey) != 0 {
		if input.ExclusiveStartKey["PK"].(*types.AttributeValueMemberS).Value != partition {
			sdk.test.Fatal("cursor escaped authenticated partition")
		}
		after = input.ExclusiveStartKey["SK"].(*types.AttributeValueMemberS).Value
	}
	var keys []string
	for _, item := range sdk.items {
		sortKey := item["SK"].(*types.AttributeValueMemberS).Value
		if item["PK"].(*types.AttributeValueMemberS).Value == partition && sortKey >= boundaries[0] && sortKey < boundaries[1] && sortKey > after {
			keys = append(keys, sortKey)
		}
	}
	sort.Strings(keys)
	result := &dynamodb.QueryOutput{}
	for index, sortKey := range keys {
		if index == int(aws.ToInt32(input.Limit)) {
			last := result.Items[len(result.Items)-1]
			result.LastEvaluatedKey = map[string]types.AttributeValue{"PK": last["PK"], "SK": last["SK"]}
			break
		}
		result.Items = append(result.Items, sdk.items[partition+"/"+sortKey])
	}
	return result, nil
}

func (sdk *calendarReadSDK) TransactWriteItems(context.Context, *dynamodb.TransactWriteItemsInput, ...func(*dynamodb.Options)) (*dynamodb.TransactWriteItemsOutput, error) {
	sdk.test.Fatal("calendar reads attempted a mutation")
	return nil, nil
}

func (sdk *calendarReadSDK) addRow(row repository.CalendarEventRow, referenceOnly bool) {
	sdk.test.Helper()
	if !referenceOnly {
		item, err := attributevalue.MarshalMap(row)
		if err != nil {
			sdk.test.Fatal(err)
		}
		sdk.items[row.PK+"/"+row.SK] = item
	}
	sortKey := model.CalendarTimeSortKey(row.StartsAt, row.SourceID, row.EventID)
	reference, err := attributevalue.MarshalMap(map[string]any{
		"PK": row.PK, "SK": sortKey, "userId": row.UserID, "sourceId": row.SourceID, "eventId": row.EventID,
		"startsAt": row.StartsAt, "endsAt": row.EndsAt, "revision": row.Revision, "pendingShareExpiresAt": row.ExpiresAt,
	})
	if err != nil {
		sdk.test.Fatal(err)
	}
	sdk.items[row.PK+"/"+sortKey] = reference
}

func TestCalendarServiceRepositoryOpaquePaginationDoesNotSkipEvents(test *testing.T) {
	anchor := time.Now().UTC().Truncate(time.Second)
	svc, store, _, _ := calendarFixture()
	svc.now = func() time.Time { return anchor }
	request := calendarRequest()
	request.WindowStart, request.WindowEnd = anchor.Add(-7*24*time.Hour), anchor.Add(30*24*time.Hour)
	request.Mutations = nil
	for index := 0; index < 20; index++ {
		event := *calendarRequest().Mutations[0].Event
		event.ExternalID = fmt.Sprintf("occurrence-%02d", index)
		event.Title, event.Location = strings.Repeat("<", 512), strings.Repeat(">", 256)
		event.StartsAt, event.EndsAt = anchor.Add(time.Duration(index-1)*time.Hour), anchor.Add(time.Duration(index+1)*time.Hour)
		request.Mutations = append(request.Mutations, model.CalendarMutation{Op: "upsert", Event: &event})
	}
	calendarStoredEvents(test, svc, store, request)
	sdk := &calendarReadSDK{test: test, items: make(map[string]map[string]types.AttributeValue)}
	for _, row := range store.rows.Rows {
		sdk.addRow(row, false)
	}
	moved := store.rows.Rows[5]
	moved.StartsAt, moved.EndsAt = anchor.Add(-time.Hour), anchor.Add(time.Hour)
	moved.ExpiresAt = moved.EndsAt.Add(90 * 24 * time.Hour).Unix()
	sdk.addRow(moved, true)
	deleted := store.rows.Rows[6]
	deleted.EventID, deleted.SK = strings.Repeat("d", 64), model.CalendarEventSortKey(calendarSourceID, strings.Repeat("d", 64))
	sdk.addRow(deleted, true)
	svc.store = repository.NewCalendarRepository(sdk, "calendar-test")
	query := model.CalendarQuery{From: anchor, To: anchor.Add(24 * time.Hour), Limit: 50}
	var seen []string
	for pageNumber := 0; ; pageNumber++ {
		if pageNumber > 20 {
			test.Fatal("continuation failed to advance")
		}
		page, err := svc.ListEvents(context.Background(), "owner", query)
		if err != nil {
			test.Fatal(err)
		}
		encoded, _ := json.Marshal(page)
		if len(encoded) > 14000 {
			test.Fatal("real cursor pushed API response above its byte limit")
		}
		for _, event := range page.Events {
			seen = append(seen, event.EventID)
			if event.Title != strings.Repeat("<", 512) {
				test.Fatal("private title was truncated")
			}
		}
		if page.NextCursor == nil {
			break
		}
		query.Cursor = *page.NextCursor
	}
	if len(seen) != 20 {
		test.Fatalf("ongoing/current occurrences dropped or stale/deleted references leaked: count=%d", len(seen))
	}
	for index, eventID := range seen {
		if eventID != store.rows.Rows[index].EventID {
			test.Fatal("occurrence duplicated, skipped, or out of order")
		}
	}
	query.Cursor, query.AccountID = "", "account-1"
	sdk.partitions = nil
	other, err := svc.ListEvents(context.Background(), "another-member", query)
	if err != nil || len(other.Events) != 0 {
		test.Fatalf("another account member read the owner's events: %v", err)
	}
	for _, partition := range sdk.partitions {
		if partition != "USER#another-member" {
			test.Fatal("account classification granted cross-owner reads")
		}
	}
	query.AccountID = ""
	query.From = anchor.Add(20 * time.Hour)
	ended, err := svc.ListEvents(context.Background(), "owner", query)
	if err != nil || len(ended.Events) != 0 {
		test.Fatal("events remained visible at their exclusive end")
	}
}
