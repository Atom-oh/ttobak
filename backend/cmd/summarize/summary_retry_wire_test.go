package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

type retryHTTP func(*http.Request) (*http.Response, error)

func (f retryHTTP) Do(r *http.Request) (*http.Response, error) { return f(r) }

func TestSummaryRetryHandlerRecovery(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "status-write-failure", true: "status-changed"}[changed], func(t *testing.T) {
			current := model.Meeting{UserID: "owner", MeetingID: "m", Status: model.StatusError,
				SummaryRetryAttempts: 2, SummaryConflictCode: "RETRY_EXHAUSTED", Notes: "보존할 메모"}
			writes, claim := 0, ""
			httpClient := retryHTTP(func(req *http.Request) (*http.Response, error) {
				code, body := 200, `{}`
				var wire struct {
					UpdateExpression, ConditionExpression string
					ExpressionAttributeValues             map[string]struct{ S, N string }
				}
				raw, _ := io.ReadAll(req.Body)
				json.Unmarshal(raw, &wire)
				update := wire.UpdateExpression
				switch req.Header.Get("X-Amz-Target") {
				case "DynamoDB_20120810.GetItem":
					if changed && claim != "" {
						current.Status = model.StatusDone
					}
					item := map[string]any{"summaryRetryPending": map[string]bool{"BOOL": current.SummaryRetryPending},
						"summaryRetryAttempts": map[string]string{"N": fmt.Sprint(current.SummaryRetryAttempts)}}
					for k, v := range map[string]string{"userId": "owner", "meetingId": "m", "status": current.Status, "notes": current.Notes, "summarizeRetryClaimedAt": claim} {
						if v != "" {
							item[k] = map[string]string{"S": v}
						}
					}
					data, _ := json.Marshal(map[string]any{"Item": item})
					body = string(data)
				case "DynamoDB_20120810.UpdateItem":
					writes++
					switch {
					case strings.Contains(update, "ADD"):
						if claim != "" || current.SummaryRetryAttempts >= 2 {
							t.Fatal("redelivery could not acquire a bounded fresh claim")
						}
						for _, value := range wire.ExpressionAttributeValues {
							if strings.HasSuffix(value.S, "Z") && value.S > claim {
								claim = value.S
							}
						}
						current.SummaryRetryAttempts++
					case strings.Contains(update, "REMOVE"):
						if !strings.Contains(string(raw), claim) {
							t.Fatal("cleanup is not bound to its claim")
						}
						terminal := strings.Contains(string(raw), "RETRY_EXHAUSTED")
						if terminal && current.SummaryRetryAttempts < 2 || changed && strings.Contains(update, "SET") {
							code, body = 400, `{"__type":"ConditionalCheckFailedException"}`
							break
						}
						if changed && (!strings.HasPrefix(update, "REMOVE ") || !strings.Contains(wire.ConditionExpression, "<>")) {
							t.Fatal("cleanup changed the new status")
						}
						claim = ""
						if terminal {
							current.Status, current.SummaryRetryPending = model.StatusError, false
							current.SummaryConflictCode = "RETRY_EXHAUSTED"
						}
					default: // Actual generateSummary's initial status write fails.
						if strings.Contains(string(raw), "summaryRetryAttempts") {
							t.Fatal("resumed generation reset its finite budget")
						}
						code, body = 500, `{"__type":"InternalServerError","message":"synthetic status write failure"}`
					}
				default:
					t.Fatal("recovery attempted STT/model/other storage work")
				}
				return &http.Response{StatusCode: code, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			oldRepo, oldS3, oldModel := repo, s3Client, bedrockService
			s3Client, bedrockService = nil, nil // Unexpected STT/model work cannot reach AWS.
			repo = repository.NewDynamoDBRepository(dynamodb.NewFromConfig(aws.Config{Region: "ap-northeast-2",
				Credentials: aws.AnonymousCredentials{}, HTTPClient: httpClient, Retryer: func() aws.Retryer { return aws.NopRetryer{} }}), "table")
			t.Cleanup(func() { repo, s3Client, bedrockService = oldRepo, oldS3, oldModel })
			event := json.RawMessage(`{"source":"ttobak.transcribe","detail-type":"AllPartsTranscribed","detail":{"meetingId":"m","userId":"owner","partCount":1}}`)
			if err := Handler(context.Background(), event); err != nil || writes != 0 {
				t.Fatal("terminal event replay must be a no-op", err)
			}
			// Operator reset starts a new recovery budget.
			current.Status, current.SummaryRetryPending, current.SummaryRetryAttempts = model.StatusSummarizing, true, 0
			current.SummaryConflictCode = "SOURCE_CHANGED"
			for n := 1; n <= 2; n++ {
				err := Handler(context.Background(), event)
				if !changed && err == nil || changed && err != nil || claim != "" || current.SummaryRetryAttempts != n || current.Notes != "보존할 메모" {
					t.Fatalf("claim/budget/source lost: %+v %v", current, err)
				}
				if changed {
					break
				}
			}
			if !changed && (current.Status != model.StatusError || current.SummaryRetryPending || current.SummaryConflictCode != "RETRY_EXHAUSTED") {
				t.Fatal("final failure did not terminate")
			}
		})
	}
}
