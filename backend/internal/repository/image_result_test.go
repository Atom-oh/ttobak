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
	"github.com/ttobak/backend/internal/model"
)

func TestImagePublicationGuardsDeletionAndConcurrentCompletion(t *testing.T) {
	for _, rejected := range []bool{false, true} {
		client := dynamodb.New(dynamodb.Options{
			Region: "us-east-1", Credentials: aws.AnonymousCredentials{}, Retryer: aws.NopRetryer{},
			HTTPClient: indexRepositoryHTTP(func(req *http.Request) (*http.Response, error) {
				if !strings.HasSuffix(req.Header.Get("X-Amz-Target"), ".UpdateItem") {
					t.Fatal("image publication must not put a stale whole item")
				}
				var body struct {
					Condition string            `json:"ConditionExpression"`
					Update    string            `json:"UpdateExpression"`
					Names     map[string]string `json:"ExpressionAttributeNames"`
				}
				if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				condition, update := body.Condition, body.Update
				for name, field := range body.Names {
					condition = strings.ReplaceAll(condition, name, field)
					update = strings.ReplaceAll(update, name, field)
				}
				for _, field := range []string{"PK", "attachmentId", "meetingId", "userId", "originalKey", "status", "type", "processedContent"} {
					if !strings.Contains(condition, field) {
						t.Fatalf("missing publication guard: %s", field)
					}
				}
				if !strings.Contains(condition, "attribute_exists") || strings.Contains(update, "originalKey") {
					t.Fatal("publication could resurrect or rebind source")
				}
				status, response := 200, `{}`
				if rejected {
					status, response = 400, `{"__type":"ConditionalCheckFailedException","message":"stale"}`
				}
				return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/x-amz-json-1.0"}},
					Body: io.NopCloser(strings.NewReader(response))}, nil
			}),
		})
		expected := &model.Attachment{AttachmentID: "a", MeetingID: "m", UserID: "u",
			OriginalKey: "images/u/m/a.png", Type: "image", Status: model.AttachStatusUploaded}
		err := NewDynamoDBRepository(client, "table").SaveImageResult(context.Background(), expected, "photo", "output")
		if (rejected && err != ErrConditionFailed) || (!rejected && err != nil) {
			t.Fatalf("unexpected publication outcome: %v", err)
		}
		if expected.Status != model.AttachStatusUploaded || expected.ProcessedContent != "" {
			t.Fatal("caller snapshot mutated")
		}
	}
}
