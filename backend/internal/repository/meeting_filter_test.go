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

func TestDecodeMeetingKey_ByteLimits(t *testing.T) {
	for _, tt := range []struct {
		name  string
		limit int
	}{
		{"PK", 2048},
		{"GSI1PK", 2048},
		{"SK", 1024},
		{"GSI1SK", 1024},
	} {
		for _, multibyte := range []bool{false, true} {
			for _, extra := range []int{0, 1} {
				t.Run(fmt.Sprintf("%s/multibyte=%t/extra=%d", tt.name, multibyte, extra), func(t *testing.T) {
					length := tt.limit + extra
					value := strings.Repeat("x", length)
					if multibyte {
						value = strings.Repeat("가", length/3) + strings.Repeat("x", length%3)
					}
					key := map[string]string{"PK": "p", "SK": "s", "GSI1PK": "p", "GSI1SK": "s"}
					key[tt.name] = value
					data, err := json.Marshal(key)
					if err != nil {
						t.Fatal(err)
					}
					got, err := decodeMeetingKey(base64.StdEncoding.EncodeToString(data), 4)
					if extra == 0 {
						if err != nil || got[tt.name] != value {
							t.Fatalf("%s at %d bytes should be accepted: %v", tt.name, length, err)
						}
					} else if !errors.Is(err, ErrInvalidMeetingCursor) {
						t.Fatalf("%s at %d bytes should be rejected: %v", tt.name, length, err)
					}
				})
			}
		}
	}
}

func TestListMeetings_RejectsOverlongSortKeysBeforeStorage(t *testing.T) {
	for _, tt := range []struct {
		name, tab, field, value string
	}{
		{"direct share", "shared", "SK", "SHARED#" + strings.Repeat("x", 1024)},
		{"owned base sort key", "all", "SK", "MEETING#" + strings.Repeat("x", 1024)},
		{"owned index sort key", "all", "GSI1SK", strings.Repeat("x", 1025)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			key := map[string]string{"PK": "USER#viewer", "SK": "SHARED#meeting"}
			if tt.tab == "all" {
				key["SK"], key["GSI1PK"], key["GSI1SK"] = "MEETING#meeting", "USER#viewer", "2026-09-11"
			}
			key[tt.field] = tt.value
			data, err := json.Marshal(key)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			client := dynamodb.New(dynamodb.Options{
				Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{},
				HTTPClient: meetingListHTTPClientFunc(func(*http.Request) (*http.Response, error) {
					calls++
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/x-amz-json-1.0"}}, Body: io.NopCloser(strings.NewReader(`{"Items":[]}`))}, nil
				}),
			})
			r := NewDynamoDBRepository(client, "test")
			_, err = r.ListMeetings(context.Background(), ListMeetingsParams{
				UserID: "viewer", Tab: tt.tab, AccountIDs: []string{}, Cursor: base64.StdEncoding.EncodeToString(data),
			})
			if !errors.Is(err, ErrInvalidMeetingCursor) || calls != 0 {
				t.Fatalf("overlong sort key reached storage: calls=%d, error=%v", calls, err)
			}
		})
	}
}
