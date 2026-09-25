package repository

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/ttobak/backend/internal/model"
)

func TestAudioCropCreationPinsSourceAndDoesNotOverwriteEitherMeeting(t *testing.T) {
	now := time.Date(2026, 9, 21, 0, 0, 0, 123000000, time.UTC)
	source := &model.Meeting{UserID: "owner", MeetingID: "source", Status: model.StatusDone, UpdatedAt: now}
	cropped := &model.Meeting{PK: "USER#owner", SK: "MEETING#copy", UserID: "owner", MeetingID: "copy",
		Notes: "Human notes", AudioCrop: &model.AudioCrop{SourceMeetingID: "source", State: "queued"}}
	calls := 0
	client := dynamodb.New(dynamodb.Options{Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1,
		HTTPClient: meetingListHTTPClientFunc(func(request *http.Request) (*http.Response, error) {
			calls++
			if request.Header.Get("X-Amz-Target") != "DynamoDB_20120810.TransactWriteItems" {
				t.Fatal("creation must be atomic with its source check")
			}
			var input struct {
				TransactItems []struct {
					ConditionCheck struct {
						Key                       map[string]map[string]string
						ConditionExpression       string
						ExpressionAttributeValues map[string]map[string]string
					}
					Put struct {
						Item                map[string]interface{}
						ConditionExpression string
					}
				}
			}
			if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
				t.Fatal(err)
			}
			if len(input.TransactItems) != 2 {
				t.Fatal("expected source condition and independent copy")
			}
			check := input.TransactItems[0].ConditionCheck
			if check.Key["PK"]["S"] != "USER#owner" || check.Key["SK"]["S"] != "MEETING#source" {
				t.Fatal("source check lost owner partition isolation")
			}
			matchedRevision := false
			for _, value := range check.ExpressionAttributeValues {
				matchedRevision = matchedRevision || value["S"] == now.Format(time.RFC3339Nano)
			}
			if !matchedRevision {
				t.Fatal("source updatedAt must match the reviewed snapshot")
			}
			copy := input.TransactItems[1].Put
			if !strings.Contains(copy.ConditionExpression, "attribute_not_exists") || copy.Item["notes"] == nil ||
				copy.Item["transcriptA"] != nil || copy.Item["content"] != nil || copy.Item["actionItems"] != nil {
				t.Fatal("copy must preserve notes without copying derived evidence or overwriting a prior request")
			}
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/x-amz-json-1.0"}}, Body: io.NopCloser(strings.NewReader("{}"))}, nil
		}),
	})
	if err := NewDynamoDBRepository(client, "test").CreateAudioCrop(context.Background(), source, cropped); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("got %d calls; ambiguous creation must not be retried automatically", calls)
	}
}
