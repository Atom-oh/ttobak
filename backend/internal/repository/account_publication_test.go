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
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func TestGetMeetingPublicationStrongMetadataRead(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		missing    bool
		wantError  bool
	}{
		{"revoked", `{"Item":{"PK":{"S":"USER#owner"},"SK":{"S":"MEETING#meeting"},"meetingId":{"S":"meeting"},"userId":{"S":"owner"},"entityType":{"S":"MEETING"},"accountId":{"S":"account-a"},"sharedToAccount":{"BOOL":false},"transcriptA":{"S":"s3://test-bucket/transcripts/meeting/transcriptA.txt"}}}`, 200, false, false},
		{"missing", `{}`, 200, true, false},
		{"empty item", `{"Item":{}}`, 200, true, false},
		{"storage failure", `{"__type":"ProvisionedThroughputExceededException","message":"synthetic failure"}`, 400, false, true},
		{"invalid metadata", `{"Item":{"sharedToAccount":{"S":"not-a-boolean"}}}`, 200, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client := dynamodb.New(dynamodb.Options{
				Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1,
				HTTPClient: meetingListHTTPClientFunc(func(req *http.Request) (*http.Response, error) {
					calls++
					if req.Header.Get("X-Amz-Target") != "DynamoDB_20120810.GetItem" {
						t.Fatal("publication check used an eventual query instead of the canonical key")
					}
					var input struct {
						TableName                string
						Key                      map[string]map[string]string
						ConsistentRead           bool
						ProjectionExpression     string
						ExpressionAttributeNames map[string]string
					}
					if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
						t.Fatal(err)
					}
					if !input.ConsistentRead || input.TableName != "publication-test" ||
						input.Key["PK"]["S"] != "USER#owner" || input.Key["SK"]["S"] != "MEETING#meeting" {
						t.Fatalf("publication read must pin the source's strong primary key: %+v", input)
					}
					fields := map[string]bool{}
					for _, alias := range strings.Split(input.ProjectionExpression, ",") {
						fields[input.ExpressionAttributeNames[strings.TrimSpace(alias)]] = true
					}
					for _, field := range []string{"PK", "SK", "meetingId", "userId", "entityType", "accountId", "sharedToAccount"} {
						if !fields[field] {
							t.Errorf("missing identity/publication field: %s", field)
						}
					}
					if len(fields) != 7 {
						t.Fatalf("publication read loaded unrelated meeting content: %+v", fields)
					}
					return &http.Response{StatusCode: tc.status,
						Header: http.Header{"Content-Type": {"application/x-amz-json-1.0"}},
						Body:   io.NopCloser(strings.NewReader(tc.body))}, nil
				}),
			})
			objects := s3.New(s3.Options{
				Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{},
				HTTPClient: meetingListHTTPClientFunc(func(*http.Request) (*http.Response, error) {
					t.Fatal("publication read hydrated a transcript from S3")
					return nil, nil
				}),
			})
			repo := NewDynamoDBRepositoryWithS3(client, "publication-test", objects, "test-bucket")
			meeting, err := repo.GetMeetingPublication(context.Background(), "owner", "meeting")
			if (err != nil) != tc.wantError || calls != 1 {
				t.Fatalf("meeting=%+v err=%v calls=%d", meeting, err, calls)
			}
			if tc.wantError || tc.missing {
				if meeting != nil {
					t.Fatalf("failure/missing source returned a grant: %+v", meeting)
				}
			} else if meeting == nil || meeting.SharedToAccount || meeting.MeetingID != "meeting" || meeting.UserID != "owner" {
				t.Fatalf("revoked canonical metadata was lost: %+v", meeting)
			}
		})
	}
}
