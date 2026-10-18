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

func TestClearAccountInsightsBindsCanonicalEmptyPublication(t *testing.T) {
	for _, scenario := range []string{"cleared", "stale generation", "newer insights", "unpublished", "other account", "source race"} {
		t.Run(scenario, func(t *testing.T) {
			date := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
			parent := model.Meeting{
				PK: "USER#owner", SK: "MEETING#meeting", EntityType: "MEETING", UserID: "owner", MeetingID: "meeting",
				AccountID: "account", SharedToAccount: true, Content: "summary", Insights: "[]", Date: date,
				SummaryPrivacy: &model.SummaryPrivacy{Version: 1, State: model.SummaryPrivacySafe, GenerationID: "generation"},
			}
			generation, account := "generation", "account"
			switch scenario {
			case "stale generation":
				generation = "old"
			case "newer insights":
				parent.Insights = `[{"type":"risk","text":"newer"}]`
			case "unpublished":
				parent.SharedToAccount = false
			case "other account":
				account = "other"
			}
			queries, writes, deleted := 0, 0, 0
			client := dynamodb.New(dynamodb.Options{
				Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{},
				HTTPClient: summaryHTTP(func(req *http.Request) (*http.Response, error) {
					var input map[string]any
					if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
						t.Fatal(err)
					}
					target := req.Header.Get("X-Amz-Target")
					switch {
					case strings.HasSuffix(target, ".GetItem"):
						row, _ := attributevalue.MarshalMap(parent)
						wire, _ := attributevalue.MarshalMapJSON(row)
						body, _ := json.Marshal(map[string]any{"Item": json.RawMessage(wire)})
						return summaryHTTPResponse(200, string(body), nil), nil
					case strings.HasSuffix(target, ".Query"):
						queries++
						values, _ := json.Marshal(input["ExpressionAttributeValues"])
						if input["ConsistentRead"] != true || !strings.Contains(string(values), "INSIGHT#2026-10-13T12:00:00Z#meeting#") {
							t.Fatal("prefix query must be consistent and meeting-scoped")
						}
						if queries == 1 {
							return summaryHTTPResponse(200, `{"Items":[{"PK":{"S":"ACCOUNT#account"},"SK":{"S":"INSIGHT#2026-10-13T12:00:00Z#meeting#0"}}],"LastEvaluatedKey":{"PK":{"S":"ACCOUNT#account"},"SK":{"S":"INSIGHT#2026-10-13T12:00:00Z#meeting#0"}}}`, nil), nil
						}
						return summaryHTTPResponse(200, `{"Items":[{"PK":{"S":"ACCOUNT#account"},"SK":{"S":"INSIGHT#2026-10-13T12:00:00Z#meeting#1"}}]}`, nil), nil
					case strings.HasSuffix(target, ".TransactWriteItems"):
						writes++
						ops := input["TransactItems"].([]any)
						check := ops[0].(map[string]any)["ConditionCheck"].(map[string]any)
						names, _ := json.Marshal(check["ExpressionAttributeNames"])
						for _, field := range []string{"insights", "insightsRevision", "summaryPrivacy", "sharedToAccount", "accountId"} {
							if !strings.Contains(string(names), `"`+field+`"`) {
								t.Fatalf("delete batch omitted canonical %s", field)
							}
						}
						for _, op := range ops[1:] {
							if op.(map[string]any)["Delete"] == nil {
								t.Fatal("clear may only delete account rows")
							}
							deleted++
						}
						if scenario == "source race" {
							return summaryHTTPResponse(400, `{"__type":"TransactionCanceledException","CancellationReasons":[{"Code":"ConditionalCheckFailed"},{"Code":"None"},{"Code":"None"}]}`, nil), nil
						}
						return summaryHTTPResponse(200, `{}`, nil), nil
					}
					t.Fatal("unexpected SDK operation")
					return nil, nil
				}),
			})
			err := NewDynamoDBRepository(client, "synthetic-insights").ClearAccountInsights(context.Background(), "owner", "meeting", account, generation, "")
			switch scenario {
			case "cleared":
				if err != nil || queries != 2 || writes != 1 || deleted != 2 {
					t.Fatalf("nonempty-to-empty must delete every paginated row: %v q=%d w=%d d=%d", err, queries, writes, deleted)
				}
			case "source race":
				if !errors.Is(err, ErrConditionFailed) || writes != 1 {
					t.Fatalf("race must surface condition failure: %v", err)
				}
			default:
				if !errors.Is(err, ErrConditionFailed) || queries != 0 || writes != 0 {
					t.Fatalf("stale or unpublished clear must not touch account rows: %v q=%d w=%d", err, queries, writes)
				}
			}
		})
	}
}
