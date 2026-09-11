package repository

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
)

func TestListMeetings_MultiAccountExpressionAndBound(t *testing.T) {
	for _, count := range []int{0, 2, 100} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			ids := make([]string, count)
			for i := range ids {
				ids[i] = fmt.Sprintf("acc-%03d", i)
			}
			calls := 0
			client := dynamodb.New(dynamodb.Options{
				Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{},
				HTTPClient: meetingListHTTPClientFunc(func(req *http.Request) (*http.Response, error) {
					calls++
					if calls > 25 {
						t.Fatal("multi filter exceeded query work bound")
					}
					var q struct {
						IndexName                 string
						FilterExpression          string
						KeyConditionExpression    string
						ExpressionAttributeNames  map[string]string
						ExpressionAttributeValues map[string]map[string]string
						ExclusiveStartKey         map[string]map[string]string
						ScanIndexForward          *bool
						Limit                     int32
					}
					if err := json.NewDecoder(req.Body).Decode(&q); err != nil {
						t.Fatal(err)
					}
					if q.IndexName != "GSI1" || q.Limit != 2 || q.ScanIndexForward == nil || *q.ScanIndexForward {
						t.Fatalf("owned chronology or page limit changed: %+v", q)
					}
					// Resolve expression aliases without prefix substitution collisions.
					values := map[string]bool{}
					var accountAlias, callerAlias string
					for k, v := range q.ExpressionAttributeNames {
						if v == "accountId" {
							accountAlias = k
						}
					}
					for k, v := range q.ExpressionAttributeValues {
						if v["S"] == "USER#viewer" {
							callerAlias = k
						}
						if slices.Contains(ids, v["S"]) {
							values[v["S"]] = true
						}
					}
					if callerAlias == "" || !strings.Contains(q.KeyConditionExpression, callerAlias) ||
						len(values) != count {
						t.Fatalf("filter/caller values lost: %+v", q)
					}
					if count > 0 && !strings.Contains(q.FilterExpression, accountAlias+" IN (") {
						t.Fatalf("expected account OR selection: %s", q.FilterExpression)
					}
					if calls > 1 && q.ExclusiveStartKey["SK"]["S"] != fmt.Sprintf("MEETING#skip-%d", calls-1) {
						t.Fatalf("did not advance sparse filter: %+v", q)
					}
					body := fmt.Sprintf(`{"Items":[],"LastEvaluatedKey":{"PK":{"S":"USER#viewer"},"SK":{"S":"MEETING#skip-%d"},"GSI1PK":{"S":"USER#viewer"},"GSI1SK":{"S":"2026-09-11"}}}`, calls)
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/x-amz-json-1.0"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
				}),
			})
			r := NewDynamoDBRepository(client, "test")
			got, err := r.ListMeetings(context.Background(), ListMeetingsParams{UserID: "viewer", Tab: "all", AccountIDs: ids, Limit: 2})
			if err != nil || calls != 25 || len(got.Meetings) != 0 || got.NextCursor == nil {
				t.Fatalf("sparse page lost continuation: %+v, calls=%d, error=%v", got, calls, err)
			}
		})
	}
}

func TestListMeetings_RejectsInvalidRawCursorBeforeQuery(t *testing.T) {
	encode := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	for _, cursor := range []string{
		"garbage", encode(`null`), encode(`{}`), encode(`{"PK":"USER#viewer"}`),
		encode(`{"PK":"USER#other","SK":"MEETING#x","GSI1PK":"USER#other","GSI1SK":"2026-09-11"}`),
		encode(`{"PK":"USER#viewer","SK":"SHARED#x"}`),
		encode(`{"PK":"USER#viewer","SK":"MEETING#x","GSI1PK":"USER#viewer","GSI1SK":"2026-09-11","extra":"x"}`),
	} {
		t.Run(cursor, func(t *testing.T) {
			r := NewDynamoDBRepository(nil, "test")
			_, err := r.ListMeetings(context.Background(), ListMeetingsParams{UserID: "viewer", Tab: "all", Cursor: cursor})
			if !errors.Is(err, ErrInvalidMeetingCursor) {
				t.Fatalf("invalid cursor should fail before storage: %v", err)
			}
		})
	}
}

func TestValidateMeetingListCursor_AcceptsSparseMembershipPositions(t *testing.T) {
	for _, kind := range []string{"ACCOUNT", "PROJECT"} {
		key := fmt.Sprintf(`{"PK":"%s#acc-a","SK":"MEMBER#viewer","GSI1PK":"USER#viewer","GSI1SK":"%s#acc-a"}`, kind, kind)
		cursor := base64.StdEncoding.EncodeToString([]byte(key))
		if err := ValidateMeetingListCursor(cursor, "viewer", "all"); err != nil {
			t.Fatalf("valid GSI1 %s membership position rejected: %v", kind, err)
		}
		if err := ValidateMeetingListCursor(cursor, "other", "all"); !errors.Is(err, ErrInvalidMeetingCursor) {
			t.Fatal("membership cursor crossed the caller partition")
		}
	}
}
