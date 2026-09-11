package repository

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

func TestListSharesForUser_PaginatesAllShares(t *testing.T) {
	for _, tt := range []struct {
		name           string
		emptyFirstPage bool
		secondPageErr  bool
		wantMeetingIDs []string
	}{
		{name: "accumulates both pages", wantMeetingIDs: []string{"first", "second"}},
		{name: "continues after empty page", emptyFirstPage: true, wantMeetingIDs: []string{"second"}},
		{name: "propagates second page error", secondPageErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			queryCount := 0
			client := dynamodb.New(dynamodb.Options{
				Region:      "ap-northeast-2",
				Credentials: aws.AnonymousCredentials{},
				Retryer:     aws.NopRetryer{},
				HTTPClient: meetingListHTTPClientFunc(func(req *http.Request) (*http.Response, error) {
					queryCount++
					if queryCount > 2 {
						t.Fatal("must stop when LastEvaluatedKey is absent or a query fails")
					}
					if req.Header.Get("X-Amz-Target") != "DynamoDB_20120810.Query" {
						t.Fatalf("expected Query, got %q", req.Header.Get("X-Amz-Target"))
					}
					var query struct {
						TableName                 string
						IndexName                 string
						KeyConditionExpression    string
						ExpressionAttributeNames  map[string]string
						ExpressionAttributeValues map[string]map[string]string
						ExclusiveStartKey         map[string]map[string]string
					}
					if err := json.NewDecoder(req.Body).Decode(&query); err != nil {
						t.Fatal(err)
					}
					if query.TableName != "test-table" || query.IndexName != "" {
						t.Fatalf("must query the base table: %+v", query)
					}
					condition := query.KeyConditionExpression
					for alias, name := range query.ExpressionAttributeNames {
						condition = strings.ReplaceAll(condition, alias, name)
					}
					for alias, value := range query.ExpressionAttributeValues {
						condition = strings.ReplaceAll(condition, alias, value["S"])
					}
					if !strings.Contains(condition, "PK = USER#viewer") ||
						!strings.Contains(condition, "begins_with (SK, SHARED#)") ||
						!strings.Contains(condition, "AND") {
						t.Fatalf("every page must stay scoped to the user's meeting shares: %s", condition)
					}
					status := http.StatusOK
					var body string
					if queryCount == 1 {
						if len(query.ExclusiveStartKey) != 0 {
							t.Fatalf("first page must start without a cursor: %+v", query.ExclusiveStartKey)
						}
						items := `[{"PK":{"S":"USER#viewer"},"SK":{"S":"SHARED#first"},"meetingId":{"S":"first"},"ownerId":{"S":"owner"},"sharedToId":{"S":"viewer"},"email":{"S":"viewer@example.com"},"permission":{"S":"read"}}]`
						if tt.emptyFirstPage {
							items = `[]`
						}
						body = `{"Items":` + items + `,"LastEvaluatedKey":{"PK":{"S":"USER#viewer"},"SK":{"S":"SHARED#first"}}}`
					} else {
						wantStart := map[string]map[string]string{
							"PK": {"S": "USER#viewer"},
							"SK": {"S": "SHARED#first"},
						}
						if !reflect.DeepEqual(query.ExclusiveStartKey, wantStart) {
							t.Fatalf("continuation key = %+v, want %+v", query.ExclusiveStartKey, wantStart)
						}
						body = `{"Items":[{"PK":{"S":"USER#viewer"},"SK":{"S":"SHARED#second"},"meetingId":{"S":"second"},"ownerId":{"S":"owner"},"sharedToId":{"S":"viewer"},"email":{"S":"viewer@example.com"},"permission":{"S":"edit"}}]}`
						if tt.secondPageErr {
							status = http.StatusBadRequest
							body = `{"__type":"com.amazonaws.dynamodb.v20120810#ResourceNotFoundException","message":"table missing"}`
						}
					}
					return &http.Response{
						StatusCode: status,
						Header:     http.Header{"Content-Type": {"application/x-amz-json-1.0"}},
						Body:       io.NopCloser(strings.NewReader(body)),
					}, nil
				}),
			})
			repo := NewDynamoDBRepository(client, "test-table")
			shares, err := repo.ListSharesForUser(context.Background(), "viewer")
			if queryCount != 2 {
				t.Fatalf("must read both pages, got %d queries (shares=%+v err=%v)", queryCount, shares, err)
			}
			if tt.secondPageErr {
				var notFound *types.ResourceNotFoundException
				if !errors.As(err, &notFound) || !strings.HasPrefix(err.Error(), "failed to query shares: ") || shares != nil {
					t.Fatalf("second-page error must preserve wrapping and discard partial results: shares=%+v err=%v", shares, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var ids []string
			for _, share := range shares {
				ids = append(ids, share.MeetingID)
				if share.SharedToID != "viewer" || share.OwnerID != "owner" {
					t.Fatalf("share identity was not decoded: %+v", share)
				}
				if share.MeetingID == "second" && share.Permission != "edit" {
					t.Fatalf("second-page edit permission was lost: %+v", share)
				}
			}
			if !reflect.DeepEqual(ids, tt.wantMeetingIDs) {
				t.Fatalf("meeting IDs = %v, want %v", ids, tt.wantMeetingIDs)
			}
		})
	}
}
