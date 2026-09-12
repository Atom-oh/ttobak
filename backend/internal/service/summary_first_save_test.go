package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/ttobak/backend/internal/repository"
)

func TestFirstSummaryMatchesOmittedTextAndRejectsConcurrentEdits(t *testing.T) {
	for _, scenario := range []string{"omitted", "empty", "metadata edit", "human edit", "empty inserted", "empty removed"} {
		t.Run(scenario, func(t *testing.T) {
			row := map[string]map[string]string{
				"PK": {"S": "USER#owner"}, "SK": {"S": "MEETING#meeting"},
				"userId": {"S": "owner"}, "meetingId": {"S": "meeting"},
				"transcriptA": {"S": "회의에서 예산을 검토했습니다."},
				"status":      {"S": "summarizing"},
			}
			if scenario == "empty" || scenario == "empty removed" {
				row["content"], row["notes"] = map[string]string{"S": ""}, map[string]string{"S": ""}
			}
			writes, conflicts := 0, 0
			respond := func(code int, body string) (*http.Response, error) {
				return &http.Response{StatusCode: code, Header: http.Header{"Content-Type": {"application/x-amz-json-1.0"}},
					Body: io.NopCloser(strings.NewReader(body))}, nil
			}
			client := noteSourceHTTPClient(func(req *http.Request) (*http.Response, error) {
				switch req.Header.Get("X-Amz-Target") {
				case "DynamoDB_20120810.GetItem":
					body, _ := json.Marshal(map[string]any{"Item": row})
					return respond(200, string(body))
				case "DynamoDB_20120810.Query":
					return respond(200, `{"Items":[]}`)
				case "DynamoDB_20120810.UpdateItem":
					var body struct {
						Condition string                    `json:"ConditionExpression"`
						Names     map[string]string         `json:"ExpressionAttributeNames"`
						Values    map[string]map[string]any `json:"ExpressionAttributeValues"`
					}
					if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					hasContent := false
					for _, name := range body.Names {
						if name == "content" {
							hasContent = true
						}
					}
					if !hasContent {
						conflicts++
						for _, name := range body.Names {
							if name == "notes" || name == "transcriptA" {
								t.Fatal("conflict marker must not rewrite evidence")
							}
						}
						return respond(200, `{}`)
					}
					writes++
					for alias, name := range body.Names {
						if name == "title" || name == "updatedAt" || name == "actionItems" {
							if regexp.MustCompile(regexp.QuoteMeta(alias) + `\b`).MatchString(body.Condition) {
								t.Fatalf("metadata pinned as summary evidence: %s", name)
							}
						}
					}
					// Evaluate the two optional-text predicates from the actual
					// SDK request against omitted, empty and newly edited rows.
					for _, field := range []string{"content", "notes"} {
						alias := ""
						for key, name := range body.Names {
							if name == field {
								alias = key
							}
						}
						match := regexp.MustCompile(regexp.QuoteMeta(alias) + `\s*=\s*(:\w+)`).FindStringSubmatch(body.Condition)
						value, exists := row[field]
						absent := regexp.MustCompile(`attribute_not_exists\s*\(` + regexp.QuoteMeta(alias) + `\)`).MatchString(body.Condition)
						capturedPresent := scenario == "empty" || scenario == "empty removed"
						if alias == "" || capturedPresent && (absent || len(match) != 2) ||
							!capturedPresent && (!absent || len(match) != 0) {
							t.Fatalf("attribute presence was not preserved for %s: %s", field, body.Condition)
						}
						allowed := !exists && absent
						if len(match) == 2 {
							allowed = exists && value["S"] == body.Values[match[1]]["S"]
						}
						if !allowed {
							return respond(400, `{"__type":"ConditionalCheckFailedException"}`)
						}
					}
					return respond(200, `{}`)
				case "":
					if scenario == "metadata edit" {
						row["title"], row["updatedAt"], row["actionItems"] = map[string]string{"S": "새 제목"}, map[string]string{"S": "2026-09-12T21:00:00Z"}, map[string]string{"S": "[]"}
					}
					if scenario == "human edit" {
						row["content"] = map[string]string{"S": "사람이 수정한 메모"}
					}
					if scenario == "empty inserted" {
						row["content"] = map[string]string{"S": ""}
					}
					if scenario == "empty removed" {
						delete(row, "content")
					}
					return respond(200, `{"content":[{"type":"text","text":"새 요약"}],"stop_reason":"end_turn"}`)
				default:
					t.Fatalf("unexpected operation %s", req.Header.Get("X-Amz-Target"))
					return nil, errors.New("unexpected request")
				}
			})
			cfg := aws.Config{Region: "ap-northeast-2", HTTPClient: client,
				Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
					return aws.Credentials{AccessKeyID: "fixture-key", SecretAccessKey: "fixture-secret"}, nil
				})}
			svc := NewBedrockService(bedrockruntime.NewFromConfig(cfg), nil, repository.NewDynamoDBRepository(dynamodb.NewFromConfig(cfg), "table"))
			content, err := svc.SummarizeTranscript(context.Background(), "meeting", "owner", "")
			if scenario == "human edit" || scenario == "empty inserted" || scenario == "empty removed" {
				if !errors.Is(err, repository.ErrConditionFailed) || !errors.Is(err, ErrSummaryConflict) || conflicts != 1 {
					t.Fatalf("concurrent edit not protected: %q %v", content, err)
				}
			} else if err != nil || content != "새 요약" {
				t.Fatalf("first summary failed: %q %v", content, err)
			}
			if writes != 1 {
				t.Fatalf("unexpected writes: %d", writes)
			}
		})
	}
}
