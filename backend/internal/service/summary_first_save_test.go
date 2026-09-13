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

	"github.com/ttobak/backend/internal/repository"
)

type summaryTestUpdate struct {
	Condition string                    `json:"ConditionExpression"`
	Names     map[string]string         `json:"ExpressionAttributeNames"`
	Values    map[string]map[string]any `json:"ExpressionAttributeValues"`
}

func TestFirstSummaryMatchesOmittedTextAndRejectsConcurrentEdits(t *testing.T) {
	for _, scenario := range []string{"omitted", "empty", "metadata edit", "human edit", "empty inserted", "empty removed",
		"image failure", "diagram failure", "image changed", "diagram changed"} {
		t.Run(scenario, func(t *testing.T) {
			kind := strings.Fields(scenario)[0]
			if kind != "image" && kind != "diagram" {
				kind = ""
			}
			row := map[string]map[string]string{
				"PK": {"S": "USER#owner"}, "SK": {"S": "MEETING#meeting"},
				"userId": {"S": "owner"}, "meetingId": {"S": "meeting"},
				"transcriptA": {"S": "회의에서 예산을 검토했습니다."},
				"status":      {"S": "summarizing"},
			}
			if scenario == "empty" || scenario == "empty removed" {
				row["content"], row["notes"] = map[string]string{"S": ""}, map[string]string{"S": ""}
			}
			writes, conflicts, models := 0, 0, 0
			respond := func(code int, body string) (*http.Response, error) {
				return indexHTTPResponse(code, body, nil), nil
			}
			client := noteSourceHTTPClient(func(req *http.Request) (*http.Response, error) {
				switch req.Header.Get("X-Amz-Target") {
				case "DynamoDB_20120810.GetItem":
					key, _ := io.ReadAll(req.Body)
					if strings.Contains(string(key), "ATTACH#a") {
						if strings.HasSuffix(scenario, "failure") {
							return respond(500, `{"__type":"InternalServerError"}`)
						}
						return respond(200, `{}`)
					}
					body, _ := json.Marshal(map[string]any{"Item": row})
					return respond(200, string(body))
				case "DynamoDB_20120810.Query":
					if kind != "" {
						return respond(200, `{"Items":[{"attachmentId":{"S":"a"},"meetingId":{"S":"meeting"},"userId":{"S":"editor"},"type":{"S":"`+kind+`"},"status":{"S":"done"},"fileName":{"S":"drawing"},"processedContent":{"S":"HIDDEN"}}]}`)
					}
					return respond(200, `{"Items":[]}`)
				case "DynamoDB_20120810.UpdateItem", "DynamoDB_20120810.TransactWriteItems":
					var envelope struct {
						summaryTestUpdate
						TransactItems []struct{ Update summaryTestUpdate }
					}
					if err := json.NewDecoder(req.Body).Decode(&envelope); err != nil {
						t.Fatal(err)
					}
					body := envelope.summaryTestUpdate
					if len(envelope.TransactItems) > 0 {
						body = envelope.TransactItems[0].Update
					}
					aliases := map[string]string{}
					for key, name := range body.Names {
						aliases[name] = key
					}
					if aliases["content"] == "" {
						conflicts++
						if aliases["notes"] != "" || aliases["transcriptA"] != "" {
							t.Fatal("conflict marker rewrites evidence")
						}
						return respond(200, `{}`)
					}
					writes++
					for _, name := range []string{"title", "updatedAt", "actionItems"} {
						if alias := aliases[name]; alias != "" && regexp.MustCompile(regexp.QuoteMeta(alias)+`\b`).MatchString(body.Condition) {
							t.Fatalf("metadata pinned: %s", name)
						}
					}
					// Check the actual SDK predicates against stored presence.
					for _, field := range []string{"content", "notes"} {
						alias := aliases[field]
						match := regexp.MustCompile(regexp.QuoteMeta(alias) + `\s*=\s*(:\w+)`).FindStringSubmatch(body.Condition)
						value, exists := row[field]
						absent := regexp.MustCompile(`attribute_not_exists\s*\(` + regexp.QuoteMeta(alias) + `\)`).MatchString(body.Condition)
						capturedPresent := scenario == "empty" || scenario == "empty removed"
						if alias == "" || capturedPresent && (absent || len(match) != 2) ||
							!capturedPresent && (!absent || len(match) != 0) {
							t.Fatalf("wrong presence for %s: %s", field, body.Condition)
						}
						allowed := !exists && absent
						if len(match) == 2 {
							allowed = exists && value["S"] == body.Values[match[1]]["S"]
						}
						if !allowed {
							return respond(400, `{"__type":"TransactionCanceledException","CancellationReasons":[{"Code":"ConditionalCheckFailed"}]}`)
						}
					}
					return respond(200, `{}`)
				case "":
					models++
					prompt, _ := io.ReadAll(req.Body)
					if strings.Contains(string(prompt), "HIDDEN") {
						t.Fatal("changed attachment reached model")
					}
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
			svc := summaryTestService(client)
			content, err := svc.SummarizeTranscript(context.Background(), "meeting", "owner", "")
			if strings.HasSuffix(scenario, "failure") {
				if err == nil || errors.Is(err, ErrSummaryConflict) || writes != 0 || models != 0 {
					t.Fatalf("read failure: %q %v writes=%d models=%d", content, err, writes, models)
				}
				return
			}
			if scenario == "human edit" || scenario == "empty inserted" || scenario == "empty removed" {
				if !errors.Is(err, repository.ErrConditionFailed) || !errors.Is(err, ErrSummaryConflict) || conflicts != 1 {
					t.Fatalf("edit lost: %q %v", content, err)
				}
			} else if kind != "" {
				if err != nil || !strings.Contains(content, "drawing") || !strings.Contains(content, "제외") {
					t.Fatalf("missing notice: %q %v", content, err)
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
