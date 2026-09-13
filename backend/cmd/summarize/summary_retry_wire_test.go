package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/service"
)

type retryHTTP func(*http.Request) (*http.Response, error)

func (f retryHTTP) Do(r *http.Request) (*http.Response, error) { return f(r) }

func TestSummaryRetryHandlerRecovery(t *testing.T) {
	for _, mode := range []string{"status", "changed", "model", "source", "marker", "fresh", "fresh-clean"} {
		t.Run(mode, func(t *testing.T) {
			changed := mode == "changed"
			fresh := strings.HasPrefix(mode, "fresh")
			current := model.Meeting{PK: "USER#owner", SK: "MEETING#m", UserID: "owner", MeetingID: "m", Status: model.StatusError,
				SummaryRetryAttempts: 2, SummaryConflictCode: "RETRY_EXHAUSTED", Notes: "보존할 메모", TranscriptA: "발언 근거"}
			if mode == "source" {
				current.TranscriptA = "s3://bucket/transcripts/m/transcriptA.txt"
			}
			writes, models, claim := 0, 0, ""
			httpClient := retryHTTP(func(req *http.Request) (*http.Response, error) {
				code, body := 200, `{}`
				var wire struct {
					UpdateExpression, ConditionExpression string
					ExpressionAttributeValues             map[string]struct{ S, N string }
				}
				var raw []byte
				if req.Body != nil {
					raw, _ = io.ReadAll(req.Body)
				}
				json.Unmarshal(raw, &wire)
				update := wire.UpdateExpression
				switch req.Header.Get("X-Amz-Target") {
				case "DynamoDB_20120810.GetItem":
					if changed && claim != "" {
						current.Status = model.StatusDone
					}
					current.SummarizeRetryClaimedAt = claim
					item, _ := attributevalue.MarshalMap(current)
					item["summaryRetryAttempts"], _ = attributevalue.Marshal(current.SummaryRetryAttempts)
					data, _ := attributevalue.MarshalMapJSON(item)
					body = `{"Item":` + string(data) + `}`
				case "DynamoDB_20120810.Query":
					body = `{"Items":[]}`
				case "DynamoDB_20120810.TransactWriteItems":
					code, body = 400, `{"__type":"TransactionCanceledException","CancellationReasons":[{"Code":"ConditionalCheckFailed"}]}`
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
						isMarker := strings.Contains(update, "SET") && !strings.Contains(string(raw), "summaryRetryAttempts") && !strings.Contains(wire.ConditionExpression, "<>")
						if !isMarker && (claim == "" || !strings.Contains(string(raw), claim)) {
							code, body = 400, `{"__type":"ConditionalCheckFailedException"}`
							break
						}
						terminal := strings.Contains(string(raw), "RETRY_EXHAUSTED")
						if mode == "marker" && strings.Contains(update, "SET") && !strings.Contains(string(raw), "summaryRetryAttempts") {
							code, body = 500, `{"__type":"InternalServerError"}`
							break
						}
						if terminal && current.SummaryRetryAttempts < 2 || changed && !strings.Contains(wire.ConditionExpression, "<>") {
							code, body = 400, `{"__type":"ConditionalCheckFailedException"}`
							break
						}
						claim = ""
						if changed {
							current.SummaryRetryPending = false
						}
						if terminal {
							current.Status, current.SummaryRetryPending = model.StatusError, false
							current.SummaryConflictCode = "RETRY_EXHAUSTED"
						} else if !changed {
							current.SummaryRetryPending = true
						}
					default:
						if strings.Contains(string(raw), "summaryRetryAttempts") {
							if !fresh || current.Status != model.StatusTranscribing || !strings.Contains(string(raw), `"N":"0"`) {
								t.Fatal("resumed generation reset its finite budget")
							}
							current.SummaryRetryAttempts, current.SummaryRetryPending, current.SummaryConflictCode = 0, false, ""
						}
						if mode == "status" {
							code, body = 500, `{"__type":"InternalServerError"}`
						} else {
							current.Status = model.StatusSummarizing
						}
						if strings.Contains(string(raw), `"S":"error"`) {
							current.Status = model.StatusError
						}
					}
				default:
					if req.Method == "HEAD" {
						code, body = 500, `<Error><Code>InternalError</Code></Error>`
					} else if req.Method == "POST" {
						models++
						if mode == "marker" || fresh {
							body = `{"content":[{"type":"text","text":"새 요약"}],"stop_reason":"end_turn"}`
						} else {
							code, body = 500, `{"message":"synthetic model failure"}`
						}
					} else {
						t.Fatal("unexpected source request")
					}
				}
				return &http.Response{StatusCode: code, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			oldRepo, oldS3, oldModel := repo, s3Client, bedrockService
			cfg := aws.Config{Region: "ap-northeast-2", HTTPClient: httpClient, Retryer: func() aws.Retryer { return aws.NopRetryer{} },
				Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
					return aws.Credentials{AccessKeyID: "fixture", SecretAccessKey: "fixture"}, nil
				})}
			s3Client = s3.NewFromConfig(cfg)
			repo = repository.NewDynamoDBRepositoryWithS3(dynamodb.NewFromConfig(cfg), "table", s3Client, "bucket")
			bedrockService = service.NewBedrockService(bedrockruntime.NewFromConfig(cfg), s3Client, repo)
			t.Cleanup(func() { repo, s3Client, bedrockService = oldRepo, oldS3, oldModel })
			event := json.RawMessage(`{"source":"ttobak.transcribe","detail-type":"AllPartsTranscribed","detail":{"meetingId":"m","userId":"owner","partCount":1}}`)
			if err := Handler(context.Background(), event); err != nil || writes != 0 {
				t.Fatal("terminal event replay must be a no-op", err)
			}
			if fresh {
				current.Status, current.SummaryRetryPending = model.StatusTranscribing, mode == "fresh"
				if err := generateSummary(context.Background(), &current, ""); err == nil || !current.SummaryRetryPending || current.SummaryRetryAttempts != 0 {
					t.Fatal("new single-file/rediarize run retained exhausted budget", err, current)
				}
			} else {
				// Operator reset starts a new recovery budget.
				current.Status, current.SummaryRetryPending, current.SummaryRetryAttempts = model.StatusSummarizing, true, 0
				current.SummaryConflictCode = "SOURCE_CHANGED"
			}
			for n := 1; n <= 2; n++ {
				err := Handler(context.Background(), event)
				if !changed && err == nil || changed && err != nil || claim != "" || current.SummaryRetryAttempts != n || current.Notes != "보존할 메모" {
					t.Fatalf("claim/budget/source lost: %+v %v", current, err)
				}
				if changed {
					if current.SummaryRetryPending {
						t.Fatal("changed lifecycle retained retry marker")
					}
					break
				}
				if n == 1 && (current.Status != model.StatusSummarizing || !current.SummaryRetryPending) {
					t.Fatal("remaining retry was lost")
				}
			}
			if !changed && (current.Status != model.StatusError || current.SummaryRetryPending || current.SummaryConflictCode != "RETRY_EXHAUSTED") {
				t.Fatal("final failure did not terminate")
			}
			if (mode == "model" || mode == "marker") && models != 2 {
				t.Fatal("retry did not regenerate")
			}
			if fresh && models != 3 {
				t.Fatal("new run did not receive two fresh retries")
			}
			before := models
			if err := Handler(context.Background(), event); err != nil || models != before {
				t.Fatal("terminal delivery started an unbounded retry", err)
			}
		})
	}
}
