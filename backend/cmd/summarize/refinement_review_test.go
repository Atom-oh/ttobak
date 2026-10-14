package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/service"
)

func TestTranscriptHandoffPreservesHumanEditsAndClaimsDurably(t *testing.T) {
	for _, scenario := range []string{"normal", "human-edit", "audio-change", "selection-change", "handoff-read-failure"} {
		t.Run(scenario, func(t *testing.T) {
			human := scenario == "human-edit"
			row, _ := attributevalue.MarshalMap(model.Meeting{PK: "USER#owner", SK: "MEETING#m", UserID: "owner", MeetingID: "m", Status: model.StatusTranscribing, TranscriptA: "prior", Content: "prior saved note"})
			initial := true
			commits, rejected, releases := 0, 0, 0
			client := retryHTTP(func(request *http.Request) (*http.Response, error) {
				raw, _ := io.ReadAll(request.Body)
				switch request.Header.Get("X-Amz-Target") {
				case "DynamoDB_20120810.GetItem":
					if scenario == "handoff-read-failure" && commits > 0 && releases == 0 {
						return transcriptFixtureResponse(503, `{"__type":"InternalServerError"}`, nil), nil
					}
					data, _ := attributevalue.MarshalMapJSON(row)
					if initial {
						initial = false
						if scenario == "audio-change" {
							row["audioKey"] = &types.AttributeValueMemberS{Value: "new audio"}
						}
						if scenario == "selection-change" {
							row["selectedTranscript"] = &types.AttributeValueMemberS{Value: "B"}
						}
						if human {
							row["transcriptA"] = &types.AttributeValueMemberS{Value: "human correction"}
							row["transcriptRevisionA"] = &types.AttributeValueMemberS{Value: "human-revision"}
							row["content"] = &types.AttributeValueMemberS{Value: "human note"}
						}
					}
					return transcriptFixtureResponse(200, `{"Item":`+string(data)+`}`, nil), nil
				case "DynamoDB_20120810.UpdateItem":
					var wire struct {
						UpdateExpression          string
						ExpressionAttributeNames  map[string]string
						ExpressionAttributeValues map[string]json.RawMessage
					}
					if json.Unmarshal(raw, &wire) != nil {
						t.Fatal("bad update")
					}
					if strings.Contains(wire.UpdateExpression, "REMOVE") {
						if strings.Contains(string(raw), "RETRY_EXHAUSTED") {
							return transcriptFixtureResponse(400, `{"__type":"ConditionalCheckFailedException"}`, nil), nil
						}
						if request.Context().Err() != nil {
							t.Fatal("cleanup inherited cancellation")
						}
						delete(row, "summarizeRetryClaimedAt")
						releases++
					}
					for alias, name := range wire.ExpressionAttributeNames {
						if scenario != "normal" && scenario != "handoff-read-failure" && name == "transcriptQualityA" && strings.Contains(wire.UpdateExpression, alias) {
							rejected++
							return transcriptFixtureResponse(400, `{"__type":"ConditionalCheckFailedException"}`, nil), nil
						}
					}
					for _, pair := range regexp.MustCompile(`(#[0-9]+) = (:[0-9]+)`).FindAllStringSubmatch(wire.UpdateExpression, -1) {
						values, err := attributevalue.UnmarshalMapJSON([]byte(`{"v":` + string(wire.ExpressionAttributeValues[pair[2]]) + `}`))
						if err != nil {
							t.Fatal(err)
						}
						row[wire.ExpressionAttributeNames[pair[1]]] = values["v"]
					}
					if !strings.Contains(wire.UpdateExpression, "REMOVE") {
						commits++
					}
					return transcriptFixtureResponse(200, `{}`, nil), nil
				default:
					t.Fatalf("unexpected request: %s", request.Header.Get("X-Amz-Target"))
					return nil, nil
				}
			})
			old := repo
			t.Cleanup(func() { repo = old })
			cfg := aws.Config{Region: "us-west-2", Credentials: aws.AnonymousCredentials{}, HTTPClient: client, Retryer: func() aws.Retryer { return aws.NopRetryer{} }}
			repo = repository.NewDynamoDBRepository(dynamodb.NewFromConfig(cfg), "table")
			snapshot, err := repo.CaptureTranscriptUpdate(context.Background(), "owner", "m")
			if err != nil {
				t.Fatal(err)
			}
			parsed := &parsedTranscript{Text: "new refined output", Quality: &model.TranscriptQuality{Refinement: "validated", Timing: "unavailable"}}
			current, err := beginTranscriptSummary(context.Background(), snapshot, parsed, false)
			if scenario == "handoff-read-failure" {
				_, claimed := row["summarizeRetryClaimedAt"]
				pending, ok := row["summaryRetryPending"].(*types.AttributeValueMemberBOOL)
				if err == nil || current != nil || commits != 1 || releases != 1 || claimed || !ok || !pending.Value {
					t.Fatal("read failure left owned claim stranded", err, releases)
				}
				return
			}
			if scenario == "audio-change" || scenario == "selection-change" {
				if current != nil || commits != 0 || rejected < 1 || scenario == "audio-change" && err != nil || scenario == "selection-change" && err != repository.ErrConditionFailed {
					t.Fatal("superseded output was rebound", scenario, err)
				}
				return
			}
			if err != nil || current == nil {
				t.Fatal("handoff failed", err)
			}
			var stored model.Meeting
			if attributevalue.UnmarshalMap(row, &stored) != nil {
				t.Fatal("bad stored row")
			}
			if commits != 1 || stored.Status != model.StatusSummarizing || !stored.SummaryRetryPending || stored.SummarizeRetryClaimedAt == "" || current.SummarizeRetryClaimedAt != stored.SummarizeRetryClaimedAt || stored.SummaryRetryAttempts != 0 {
				t.Fatal("not one durable owned handoff", commits, stored.Status)
			}
			if human {
				if rejected < 1 || stored.TranscriptA != "human correction" || stored.Content != "human note" || stored.TranscriptQualityA != nil || stored.TranscriptRevisionA != "human-revision" {
					t.Fatal("human source replaced or re-attested")
				}
			} else if stored.TranscriptA != "new refined output" || stored.TranscriptRevisionA == "" || stored.TranscriptQualityA.ProcessingRunID != stored.SummarizeRetryClaimedAt {
				t.Fatal("publication was not atomic with claim")
			}
			observed := &model.SummarySnapshot{Stored: map[string]interface{}{"content": stored.Content}}
			if human != (stored.SummaryBatchContentHash != repository.SummaryContentHash(observed)) {
				t.Fatal("pre-ASR human-content fence changed")
			}
		})
	}
}

func TestClaimedSummaryPreservesHumanContentWithoutModelEvidence(t *testing.T) {
	calls, writes := 0, 0
	row, _ := attributevalue.MarshalMap(model.Meeting{PK: "USER#owner", SK: "MEETING#m", UserID: "owner", MeetingID: "m", Status: model.StatusSummarizing, SummaryRetryPending: true, SummarizeRetryClaimedAt: "claim", Content: "human note", TranscriptA: "human correction", SummaryBatchContentHash: repository.SummaryContentHash(&model.SummarySnapshot{Stored: map[string]interface{}{"content": "prior note"}})})
	client := retryHTTP(func(request *http.Request) (*http.Response, error) {
		switch request.Header.Get("X-Amz-Target") {
		case "DynamoDB_20120810.GetItem":
			data, _ := attributevalue.MarshalMapJSON(row)
			return transcriptFixtureResponse(200, `{"Item":`+string(data)+`}`, nil), nil
		case "DynamoDB_20120810.Query":
			return transcriptFixtureResponse(200, `{"Items":[]}`, nil), nil
		case "DynamoDB_20120810.UpdateItem":
			raw, _ := io.ReadAll(request.Body)
			if !strings.Contains(string(raw), "HUMAN_CONTENT_PRESERVED") || strings.Contains(string(raw), "attachmentSummarySources") {
				t.Fatal("human preservation published model coverage")
			}
			writes++
			return transcriptFixtureResponse(200, `{}`, nil), nil
		default:
			calls++
			t.Fatal("model/evidence bytes read before human-content fence")
			return nil, nil
		}
	})
	cfg := aws.Config{Region: "us-west-2", Credentials: aws.AnonymousCredentials{}, HTTPClient: client, Retryer: func() aws.Retryer { return aws.NopRetryer{} }}
	storage := repository.NewDynamoDBRepository(dynamodb.NewFromConfig(cfg), "table")
	svc := service.NewBedrockService(bedrockruntime.NewFromConfig(cfg), nil, storage)
	content, err := svc.SummarizeClaimedTranscript(context.Background(), "m", "owner", "", "claim", false)
	if err != service.ErrSummaryHumanContentPreserved || content != "human note" || calls != 0 || writes != 1 {
		t.Fatal("human note was overwritten or left stranded", err, calls, writes)
	}
}
