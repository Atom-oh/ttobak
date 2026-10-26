package repository

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
)

// Synthetic fixtures only.

func TestListRecentMeetingAccountIDsProjectsAndStopsAtLimit(t *testing.T) {
	queries := 0
	client := dynamodb.New(dynamodb.Options{
		Region:      "ap-northeast-2",
		Credentials: aws.AnonymousCredentials{},
		HTTPClient: meetingListHTTPClientFunc(func(req *http.Request) (*http.Response, error) {
			var q struct {
				IndexName                string
				ProjectionExpression     string
				FilterExpression         string
				ScanIndexForward         *bool
				Limit                    int
				ExpressionAttributeNames map[string]string
				ExclusiveStartKey        map[string]map[string]string
			}
			if err := json.NewDecoder(req.Body).Decode(&q); err != nil {
				t.Fatal(err)
			}
			queries++
			var projected []string
			for _, alias := range strings.Split(q.ProjectionExpression, ",") {
				projected = append(projected, q.ExpressionAttributeNames[strings.TrimSpace(alias)])
			}
			sort.Strings(projected)
			if q.IndexName != "GSI1" || strings.Join(projected, ",") != "SK,accountId" || q.ScanIndexForward == nil || *q.ScanIndexForward ||
				q.Limit != 3 || !strings.Contains(q.FilterExpression, "=") {
				t.Errorf("projection-only newest-first GSI1 query: %+v projected=%v", q, projected)
			}
			body := `{"Items":[{"SK":{"S":"MEETING#1"},"accountId":{"S":"a2"}},{"SK":{"S":"MEETING#2"}}],` +
				`"LastEvaluatedKey":{"PK":{"S":"USER#u1"},"SK":{"S":"MEETING#2"},"GSI1PK":{"S":"USER#u1"},"GSI1SK":{"S":"2026-10-02"}}}`
			if queries == 2 {
				if q.ExclusiveStartKey["SK"]["S"] != "MEETING#2" {
					t.Errorf("second page resumes: %+v", q.ExclusiveStartKey)
				}
				body = `{"Items":[{"SK":{"S":"MEETING#3"},"accountId":{"S":"a1"}},{"SK":{"S":"MEETING#4"},"accountId":{"S":"a9"}}],` +
					`"LastEvaluatedKey":{"PK":{"S":"USER#u1"},"SK":{"S":"MEETING#4"},"GSI1PK":{"S":"USER#u1"},"GSI1SK":{"S":"2026-10-01"}}}`
			}
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/x-amz-json-1.0"}},
				Body: io.NopCloser(strings.NewReader(body))}, nil
		}),
	})
	repo := NewDynamoDBRepository(client, "table")
	ids, err := repo.ListRecentMeetingAccountIDs(context.Background(), "u1", 3)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(ids, ",") != "a2,a1" || queries != 2 {
		t.Fatalf("three newest meetings, empty accountId skipped, stop at the limit: %v after %d queries", ids, queries)
	}
}
