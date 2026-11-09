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

func TestGetMeetingActionStateProjectedStrongRead(t *testing.T) {
	client := dynamodb.New(dynamodb.Options{
		Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1,
		HTTPClient: meetingListHTTPClientFunc(func(req *http.Request) (*http.Response, error) {
			var input struct {
				Key                      map[string]map[string]string
				ConsistentRead           bool
				ProjectionExpression     string
				ExpressionAttributeNames map[string]string
			}
			if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
				t.Fatal(err)
			}
			if req.Header.Get("X-Amz-Target") != "DynamoDB_20120810.GetItem" || !input.ConsistentRead ||
				input.Key["PK"]["S"] != "USER#owner" || input.Key["SK"]["S"] != "MEETING#m1" {
				t.Fatalf("unexpected read: %+v", input)
			}
			fields := map[string]bool{}
			for _, alias := range strings.Split(input.ProjectionExpression, ",") {
				fields[input.ExpressionAttributeNames[strings.TrimSpace(alias)]] = true
			}
			for _, f := range []string{"PK", "SK", "meetingId", "userId", "entityType", "accountId", "sharedToAccount", "projectIds", "date", "actionItems", "title"} {
				if !fields[f] {
					t.Errorf("missing field %s", f)
				}
			}
			for _, f := range []string{"content", "notes", "transcriptA", "transcriptB", "transcriptSegments"} {
				if fields[f] {
					t.Errorf("loaded meeting content %s", f)
				}
			}
			body := `{"Item":{"PK":{"S":"USER#owner"},"SK":{"S":"MEETING#m1"},"meetingId":{"S":"m1"},"userId":{"S":"owner"},"projectIds":{"SS":["p1"]},"actionItems":{"S":"[]"}}}`
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/x-amz-json-1.0"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
		}),
	})
	repo := NewDynamoDBRepository(client, "overview-test")
	m, err := repo.GetMeetingActionState(context.Background(), "owner", "m1")
	if err != nil || m == nil || m.MeetingID != "m1" || len(m.ProjectIDs) != 1 || m.ActionItems != "[]" {
		t.Fatalf("m=%+v err=%v", m, err)
	}
}

func TestListProjectStageHistoryNewestFirstBounded(t *testing.T) {
	client := dynamodb.New(dynamodb.Options{
		Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1,
		HTTPClient: meetingListHTTPClientFunc(func(req *http.Request) (*http.Response, error) {
			var input struct {
				KeyConditionExpression    string
				ExpressionAttributeValues map[string]map[string]string
				ScanIndexForward          *bool
				Limit                     int
				ConsistentRead            bool
			}
			if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
				t.Fatal(err)
			}
			values := map[string]bool{}
			for _, v := range input.ExpressionAttributeValues {
				values[v["S"]] = true
			}
			if req.Header.Get("X-Amz-Target") != "DynamoDB_20120810.Query" || input.ScanIndexForward == nil || *input.ScanIndexForward ||
				input.Limit != 10 || !input.ConsistentRead || !values["PROJECT#p1"] || !values["STAGEHIST#"] ||
				!strings.Contains(input.KeyConditionExpression, "begins_with") {
				t.Fatalf("unexpected query: %+v", input)
			}
			body := `{"Items":[{"PK":{"S":"PROJECT#p1"},"SK":{"S":"STAGEHIST#2026-11-01T00:00:00Z"},"projectId":{"S":"p1"},"to":{"S":"Qualified"},"entityType":{"S":"PROJECT_STAGE_CHANGE"}}]}`
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/x-amz-json-1.0"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
		}),
	})
	repo := NewDynamoDBRepository(client, "overview-test")
	rows, err := repo.ListProjectStageHistory(context.Background(), "p1", 10)
	if err != nil || len(rows) != 1 || rows[0].To != "Qualified" || rows[0].ProjectID != "p1" {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	for _, limit := range []int{0, 101} {
		if _, err := repo.ListProjectStageHistory(context.Background(), "p1", limit); err == nil {
			t.Fatalf("limit %d accepted", limit)
		}
	}
}
