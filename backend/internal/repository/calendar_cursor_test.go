package repository

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
)

func TestCalendarRowCursorsResumeAtRetainedReference(test *testing.T) {
	first := calendarTestRow()
	second := first
	second.EventID = "event-2"
	second.SK = model.CalendarEventSortKey(calendarTestSource, second.EventID)
	second.StartsAt = second.StartsAt.Add(time.Hour)
	second.EndsAt = second.EndsAt.Add(time.Hour)
	second.ExpiresAt = second.EndsAt.Add(90 * 24 * time.Hour).Unix()
	firstReference, secondReference := calendarTestReference(test, first), calendarTestReference(test, second)
	firstKey := map[string]types.AttributeValue{"PK": firstReference["PK"], "SK": firstReference["SK"]}
	resuming := false
	mock := &calendarSDKMock{
		query: func(input *dynamodb.QueryInput) (*dynamodb.QueryOutput, error) {
			if resuming {
				if !reflect.DeepEqual(input.ExclusiveStartKey, firstKey) {
					test.Fatal("resumption skipped the overflowing event")
				}
				return &dynamodb.QueryOutput{Items: []map[string]types.AttributeValue{secondReference}}, nil
			}
			return &dynamodb.QueryOutput{Items: []map[string]types.AttributeValue{firstReference, secondReference}}, nil
		},
		get: func(input *dynamodb.GetItemInput) (*dynamodb.GetItemOutput, error) {
			if calendarTestString(test, input.Key, "SK") == first.SK {
				return &dynamodb.GetItemOutput{Item: calendarTestItem(test, first)}, nil
			}
			return &dynamodb.GetItemOutput{Item: calendarTestItem(test, second)}, nil
		},
	}
	repo := calendarTestRepo(test, mock)
	query := model.CalendarQuery{From: calendarTestNow, To: calendarTestNow.Add(24 * time.Hour), Limit: 2}
	page, err := repo.ListCalendarEventRows(context.Background(), "owner", query)
	if err != nil || len(page.Rows) != 2 || len(page.RowCursors) != 2 || page.NextCursor != nil {
		test.Fatalf("missing per-row cursors: %+v, %v", page, err)
	}
	query.Cursor = page.RowCursors[0]
	resuming = true
	for _, scenario := range []string{"owner", "limit", "window", "account"} {
		changed := query
		userID := "owner"
		switch scenario {
		case "owner":
			userID = "other"
		case "limit":
			changed.Limit = 1
		case "window":
			changed.From = changed.From.Add(time.Second)
		case "account":
			changed.AccountID = "other"
		}
		if _, err := repo.ListCalendarEventRows(context.Background(), userID, changed); !errors.Is(err, ErrInvalidCalendarCursor) {
			test.Fatalf("row cursor failed to bind %s: %v", scenario, err)
		}
	}
	resumed, err := repo.ListCalendarEventRows(context.Background(), "owner", query)
	if err != nil || len(resumed.Rows) != 1 || resumed.Rows[0].EventID != second.EventID || resumed.NextCursor != nil {
		test.Fatalf("overflowing row lost on resume: %+v, %v", resumed, err)
	}
}
