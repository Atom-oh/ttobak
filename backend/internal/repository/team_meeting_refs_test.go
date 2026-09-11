package repository

import (
	"context"
	"encoding/base64"
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

func TestListMeetingRefsForAccountPage_ScopeAndPagination(t *testing.T) {
	for _, tt := range []struct {
		name      string
		limit     int32
		wantLimit int32
		resume    bool
		emptyPage bool
	}{
		{name: "explicit limit and cursor", limit: 2, wantLimit: 2, resume: true},
		{name: "zero limit defaults", limit: 0, wantLimit: 20},
		{name: "negative limit defaults", limit: -1, wantLimit: 20},
		{name: "empty page retains cursor", limit: 2, wantLimit: 2, emptyPage: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var cursor string
			var wantStart map[string]map[string]string
			if tt.resume {
				cursor = base64.StdEncoding.EncodeToString([]byte(`{"PK":"ACCOUNT#acc-a","SK":"MEETINGREF#2026-09-10T00:00:00Z#newer"}`))
				wantStart = map[string]map[string]string{
					"PK": {"S": "ACCOUNT#acc-a"},
					"SK": {"S": "MEETINGREF#2026-09-10T00:00:00Z#newer"},
				}
			}
			queryCount := 0
			client := dynamodb.New(dynamodb.Options{
				Region:      "ap-northeast-2",
				Credentials: aws.AnonymousCredentials{},
				HTTPClient: meetingListHTTPClientFunc(func(req *http.Request) (*http.Response, error) {
					queryCount++
					if queryCount > 2 {
						t.Fatal("page method must not follow LastEvaluatedKey itself")
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
						ScanIndexForward          *bool
						Limit                     int32
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
					if !strings.Contains(condition, "PK = ACCOUNT#acc-a") ||
						!strings.Contains(condition, "begins_with (SK, MEETINGREF#)") ||
						!strings.Contains(condition, "AND") {
						t.Fatalf("query must be scoped to this account's meeting refs: %s", condition)
					}
					if query.ScanIndexForward == nil || *query.ScanIndexForward || query.Limit != tt.wantLimit {
						t.Fatalf("expected newest-first query with limit %d: %+v", tt.wantLimit, query)
					}
					if !reflect.DeepEqual(query.ExclusiveStartKey, wantStart) {
						t.Fatalf("resume key = %+v, want %+v", query.ExclusiveStartKey, wantStart)
					}
					body := `{"Items":[]}`
					if queryCount == 1 {
						items := `[{"PK":{"S":"ACCOUNT#acc-a"},"SK":{"S":"MEETINGREF#2026-09-09T00:00:00Z#meeting-1"},"accountId":{"S":"acc-a"},"meetingId":{"S":"meeting-1"},"ownerUserId":{"S":"owner-1"},"title":{"S":"Earlier meeting"},"date":{"S":"2026-09-09T00:00:00Z"},"entityType":{"S":"MEETING_REF"}}]`
						if tt.emptyPage {
							items = `[]`
						}
						body = `{"Items":` + items + `,"LastEvaluatedKey":{"PK":{"S":"ACCOUNT#acc-a"},"SK":{"S":"MEETINGREF#2026-09-09T00:00:00Z#meeting-1"}}}`
					}
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": {"application/x-amz-json-1.0"}},
						Body:       io.NopCloser(strings.NewReader(body)),
					}, nil
				}),
			})
			repo := NewDynamoDBRepository(client, "test-table")
			refs, next, err := repo.ListMeetingRefsForAccountPage(context.Background(), "acc-a", cursor, tt.limit)
			if err != nil {
				t.Fatal(err)
			}
			if queryCount != 1 {
				t.Fatalf("one page must issue one query, got %d", queryCount)
			}
			if tt.emptyPage {
				if len(refs) != 0 {
					t.Fatalf("expected empty page, got %+v", refs)
				}
			} else if len(refs) != 1 || refs[0].MeetingID != "meeting-1" ||
				refs[0].AccountID != "acc-a" || refs[0].OwnerUserID != "owner-1" ||
				refs[0].Title != "Earlier meeting" || refs[0].Date.IsZero() {
				t.Fatalf("unexpected decoded refs: %+v", refs)
			}
			if next == nil || *next == "" {
				t.Fatal("LastEvaluatedKey must produce a continuation cursor")
			}
			wantStart = map[string]map[string]string{
				"PK": {"S": "ACCOUNT#acc-a"},
				"SK": {"S": "MEETINGREF#2026-09-09T00:00:00Z#meeting-1"},
			}
			refs, next, err = repo.ListMeetingRefsForAccountPage(context.Background(), "acc-a", *next, tt.limit)
			if err != nil {
				t.Fatal(err)
			}
			if queryCount != 2 || len(refs) != 0 || next != nil {
				t.Fatalf("expected terminal empty page: calls=%d refs=%+v cursor=%v", queryCount, refs, next)
			}
		})
	}
}

func TestListMeetingRefsForAccountPage_PropagatesReadErrors(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status int
		body   string
	}{
		{
			name: "query failure", status: http.StatusBadRequest,
			body: `{"__type":"com.amazonaws.dynamodb.v20120810#ResourceNotFoundException","message":"table missing"}`,
		},
		{
			name: "invalid meeting ref", status: http.StatusOK,
			body: `{"Items":[{"meetingId":{"M":{"unexpected":{"S":"value"}}}}],"LastEvaluatedKey":{"PK":{"S":"ACCOUNT#acc-a"},"SK":{"S":"MEETINGREF#bad"}}}`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			queryCount := 0
			client := dynamodb.New(dynamodb.Options{
				Region:      "ap-northeast-2",
				Credentials: aws.AnonymousCredentials{},
				Retryer:     aws.NopRetryer{},
				HTTPClient: meetingListHTTPClientFunc(func(req *http.Request) (*http.Response, error) {
					queryCount++
					return &http.Response{
						StatusCode: tt.status,
						Header:     http.Header{"Content-Type": {"application/x-amz-json-1.0"}},
						Body:       io.NopCloser(strings.NewReader(tt.body)),
					}, nil
				}),
			})
			repo := NewDynamoDBRepository(client, "test-table")
			refs, next, err := repo.ListMeetingRefsForAccountPage(context.Background(), "acc-a", "", 20)
			if err == nil || refs != nil || next != nil || queryCount != 1 {
				t.Fatalf("read failure must propagate without a partial page: refs=%+v cursor=%v calls=%d err=%v", refs, next, queryCount, err)
			}
			if tt.status == http.StatusBadRequest {
				var notFound *types.ResourceNotFoundException
				if !errors.As(err, &notFound) {
					t.Fatalf("query error must preserve its SDK type: %v", err)
				}
			}
		})
	}
}
