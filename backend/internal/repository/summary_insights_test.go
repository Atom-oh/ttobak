package repository

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/ttobak/backend/internal/model"
)

func TestCurrentInsightCopyUsesOneCanonicalTransaction(t *testing.T) {
	for _, scenario := range []string{"current", "generation", "revision", "canonical text", "unpublished", "source race", "ambiguous"} {
		t.Run(scenario, func(t *testing.T) {
			date := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
			parent := model.Meeting{
				PK: "USER#owner", SK: "MEETING#meeting", EntityType: "MEETING",
				UserID: "owner", MeetingID: "meeting", AccountID: "account", SharedToAccount: true,
				Content: "summary", Insights: `[{"type":"risk","text":"current insight"}]`,
				InsightsRevision: "revision", AuthorizationRevision: 7, Date: date,
				SummaryPrivacy: &model.SummaryPrivacy{Version: 1, State: model.SummaryPrivacySafe,
					GenerationID: "generation", ContentSHA256: model.SummaryPrivacyHash("summary")},
			}
			copy := model.AccountInsight{
				PK: "ACCOUNT#account", SK: "INSIGHT#2026-10-13T12:00:00Z#meeting#0",
				AccountID: "account", SourceType: "meeting", SourceID: "meeting", SourceUserID: "owner",
				Type: "risk", Text: "current insight", OccurredAt: date,
				SummaryGeneration: "generation", InsightsRevision: "revision",
			}
			switch scenario {
			case "generation":
				copy.SummaryGeneration = "old"
			case "revision":
				copy.InsightsRevision = "old"
			case "canonical text":
				copy.Text = "unobserved text"
			case "unpublished":
				parent.SharedToAccount = false
			}
			writes := 0
			client := dynamodb.New(dynamodb.Options{
				Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{},
				HTTPClient: summaryHTTP(func(req *http.Request) (*http.Response, error) {
					var input map[string]any
					if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
						t.Fatal(err)
					}
					switch {
					case strings.HasSuffix(req.Header.Get("X-Amz-Target"), ".GetItem"):
						if input["ConsistentRead"] != true {
							t.Fatal("canonical source was not strongly consistent")
						}
						row, err := attributevalue.MarshalMap(parent)
						if err != nil {
							t.Fatal(err)
						}
						wire, err := attributevalue.MarshalMapJSON(row)
						if err != nil {
							t.Fatal(err)
						}
						body, _ := json.Marshal(map[string]any{"Item": json.RawMessage(wire)})
						return summaryHTTPResponse(200, string(body), nil), nil
					case strings.HasSuffix(req.Header.Get("X-Amz-Target"), ".Query"):
						return summaryHTTPResponse(200, `{"Items":[]}`, nil), nil
					case strings.HasSuffix(req.Header.Get("X-Amz-Target"), ".TransactWriteItems"):
						writes++
						ops := input["TransactItems"].([]any)
						if len(ops) != 2 {
							t.Fatal("copy must publish with its canonical condition in one transaction")
						}
						check := ops[0].(map[string]any)["ConditionCheck"].(map[string]any)
						// Expand complete tokens so aliases such as #1 and #10
						// cannot corrupt each other in randomized map order.
						condition := regexp.MustCompile(`#[A-Za-z0-9_]+|:[A-Za-z0-9_]+`).ReplaceAllStringFunc(check["ConditionExpression"].(string), func(alias string) string {
							if strings.HasPrefix(alias, "#") {
								return check["ExpressionAttributeNames"].(map[string]any)[alias].(string)
							}
							encoded, _ := json.Marshal(check["ExpressionAttributeValues"].(map[string]any)[alias])
							return string(encoded)
						})
						for _, field := range []string{"summaryPrivacy", "generation", "insightsRevision", "revision", "authorizationRevision", "content", "sharedToAccount"} {
							if !strings.Contains(condition, field) {
								t.Fatalf("canonical condition omitted %s", field)
							}
						}
						item := ops[1].(map[string]any)["Put"].(map[string]any)["Item"].(map[string]any)
						if item["text"].(map[string]any)["S"] != copy.Text {
							t.Fatal("copy did not retain the validated canonical text")
						}
						if scenario == "source race" {
							return summaryHTTPResponse(400, `{"__type":"TransactionCanceledException","CancellationReasons":[{"Code":"ConditionalCheckFailed"},{"Code":"None"}]}`, nil), nil
						}
						if scenario == "ambiguous" {
							return summaryHTTPResponse(500, `{"__type":"InternalServerError"}`, nil), nil
						}
						return summaryHTTPResponse(200, `{}`, nil), nil
					default:
						t.Fatal("unexpected SDK operation")
						return nil, nil
					}
				}),
			})
			err := NewDynamoDBRepository(client, "synthetic-insights").putCurrentAccountInsights(context.Background(), []model.AccountInsight{copy})
			expectedWrites := 0
			if scenario == "current" || scenario == "source race" || scenario == "ambiguous" {
				expectedWrites = 1
			}
			if writes != expectedWrites || (err == nil) != (scenario == "current") ||
				errors.Is(err, ErrConditionFailed) != (scenario != "current" && scenario != "ambiguous") {
				t.Fatalf("conditional outcome or single-attempt contract changed: %v writes=%d", err, writes)
			}
		})
	}
}
