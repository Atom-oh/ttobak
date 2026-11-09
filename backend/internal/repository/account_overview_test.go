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
			for _, f := range []string{"PK", "SK", "meetingId", "userId", "entityType", "accountId", "sharedToAccount", "projectIds", "date", "actionItems"} {
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
