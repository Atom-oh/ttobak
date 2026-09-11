package repository

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
)

type meetingListHTTPClientFunc func(*http.Request) (*http.Response, error)

func (f meetingListHTTPClientFunc) Do(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestListMeetings_AccountQueryKeepsPaginationAndCallerScope(t *testing.T) {
	for _, accountID := range []string{"", "acc-a"} {
		t.Run("account="+accountID, func(t *testing.T) {
			queryCount := 0
			client := dynamodb.New(dynamodb.Options{
				Region:      "ap-northeast-2",
				Credentials: aws.AnonymousCredentials{},
				HTTPClient: meetingListHTTPClientFunc(func(req *http.Request) (*http.Response, error) {
					var query struct {
						IndexName                 string
						FilterExpression          string
						KeyConditionExpression    string
						ProjectionExpression      string
						ExpressionAttributeNames  map[string]string
						ExpressionAttributeValues map[string]map[string]string
						ExclusiveStartKey         map[string]map[string]string
					}
					if err := json.NewDecoder(req.Body).Decode(&query); err != nil {
						t.Fatal(err)
					}
					body := `{"Items":[]}`
					if query.IndexName == "GSI1" {
						queryCount++
						var accountName, accountValue, callerValue string
						for alias, name := range query.ExpressionAttributeNames {
							if name == "accountId" {
								accountName = alias
							}
						}
						for alias, value := range query.ExpressionAttributeValues {
							if value["S"] == "acc-a" {
								accountValue = alias
							}
							if value["S"] == "USER#viewer" {
								callerValue = alias
							}
						}
						if accountName == "" || !strings.Contains(query.ProjectionExpression, accountName) {
							t.Error("list projection must carry accountId")
						}
						if callerValue == "" || !strings.Contains(query.KeyConditionExpression, callerValue) {
							t.Error("account filter must preserve the caller's partition")
						}
						if accountID != "" {
							if accountName == "" || accountValue == "" ||
								!strings.Contains(query.FilterExpression, accountName+" = "+accountValue) {
								t.Errorf("account condition missing from query: %s", query.FilterExpression)
							}
						} else if accountValue != "" {
							t.Error("unfiltered request must not add an account condition")
						}
						switch queryCount {
						case 1:
							body = `{"Items":[],"LastEvaluatedKey":{"PK":{"S":"USER#viewer"},"SK":{"S":"MEETING#skip"},"GSI1PK":{"S":"USER#viewer"},"GSI1SK":{"S":"2026-09-09"}}}`
						case 2:
							if query.ExclusiveStartKey["SK"]["S"] != "MEETING#skip" {
								t.Fatalf("did not advance past the empty filtered page: %+v", query.ExclusiveStartKey)
							}
							body = `{"Items":[{"PK":{"S":"USER#viewer"},"SK":{"S":"MEETING#match"},"meetingId":{"S":"match"},"accountId":{"S":"acc-a"}}],"LastEvaluatedKey":{"PK":{"S":"USER#viewer"},"SK":{"S":"MEETING#match"},"GSI1PK":{"S":"USER#viewer"},"GSI1SK":{"S":"2026-09-08"}}}`
						default:
							t.Fatal("query continued after filling the requested page")
						}
					}
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": {"application/x-amz-json-1.0"}},
						Body:       io.NopCloser(strings.NewReader(body)),
					}, nil
				}),
			})
			repo := NewDynamoDBRepository(client, "test-table")
			got, err := repo.ListMeetings(context.Background(), ListMeetingsParams{
				UserID: "viewer", Tab: "all", AccountID: accountID, Limit: 1,
			})
			if err != nil {
				t.Fatal(err)
			}
			if queryCount != 2 || len(got.Meetings) != 1 || got.Meetings[0].AccountID != "acc-a" {
				t.Fatalf("unexpected filtered page: calls=%d result=%+v", queryCount, got)
			}
			if got.NextCursor == nil || len(decodeCursor(*got.NextCursor)) == 0 {
				t.Fatal("a filled page must preserve its resume cursor")
			}
		})
	}
}
