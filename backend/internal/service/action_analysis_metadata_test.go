package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

func TestActionLifecycleMetadataAvoidsS3AndKeepsWrites(t *testing.T) {
	const summary = "SUMMARY_ONLY 저장된 회의 요약"
	const meetingKey = "USER#owner/MEETING#meeting"
	type attributes = map[string]map[string]interface{}
	rows := map[string]attributes{meetingKey: {
		"PK": {"S": "USER#owner"}, "SK": {"S": "MEETING#meeting"},
		"meetingId": {"S": "meeting"}, "userId": {"S": "owner"},
		"status": {"S": "done"}, "content": {"S": summary},
		"transcriptA": {"S": "s3://synthetic/transcripts/meeting/transcriptA.txt"},
		"transcriptB": {"S": "s3://synthetic/transcripts/meeting/transcriptB.txt"},
		"actionItems": {"S": `[{"id":"old","text":"할 일","completed":true}]`},
	}}
	s3Calls, modelCalls, transactions, updates := 0, 0, 0, 0
	reply := func(body []byte) *http.Response {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/x-amz-json-1.0"}},
			Body: io.NopCloser(strings.NewReader(string(body)))}
	}
	type operation struct {
		Key                       attributes
		UpdateExpression          string
		ExpressionAttributeNames  map[string]string
		ExpressionAttributeValues attributes
	}
	keyOf := func(key attributes) string { return key["PK"]["S"].(string) + "/" + key["SK"]["S"].(string) }
	// Apply only literal SETs emitted on the real SDK wire. CAS correctness has
	// separate repository tests; this fixture proves actual writes still occur.
	apply := func(op operation) {
		key := keyOf(op.Key)
		if rows[key] == nil {
			rows[key] = attributes{}
		}
		for _, assignment := range strings.Split(strings.TrimPrefix(op.UpdateExpression, "SET "), ",") {
			parts := strings.Fields(assignment)
			if len(parts) != 3 || parts[1] != "=" {
				t.Fatalf("unexpected update: %s", op.UpdateExpression)
			}
			rows[key][op.ExpressionAttributeNames[parts[0]]] = op.ExpressionAttributeValues[parts[2]]
		}
	}
	db := dynamodb.New(dynamodb.Options{
		Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{},
		HTTPClient: noteSourceHTTPClient(func(req *http.Request) (*http.Response, error) {
			var body struct {
				operation
				ConsistentRead bool
				TransactItems  []struct {
					Update         *operation
					ConditionCheck *operation
				}
			}
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			out := map[string]interface{}{}
			switch req.Header.Get("X-Amz-Target") {
			case "DynamoDB_20120810.GetItem":
				if !body.ConsistentRead {
					t.Fatal("lifecycle reads must be current")
				}
				if row := rows[keyOf(body.Key)]; row != nil {
					out["Item"] = row
				}
			case "DynamoDB_20120810.TransactWriteItems":
				transactions++
				for _, item := range body.TransactItems {
					if item.ConditionCheck != nil {
						found := false
						for _, value := range item.ConditionCheck.ExpressionAttributeValues {
							if value["S"] == summary {
								found = true
							}
						}
						if !found {
							t.Fatal("retry source is not the saved summary")
						}
					}
					if item.Update != nil {
						apply(*item.Update)
					}
				}
			case "DynamoDB_20120810.UpdateItem":
				updates++
				apply(body.operation)
			default:
				t.Fatalf("unexpected operation: %s", req.Header.Get("X-Amz-Target"))
			}
			encoded, err := json.Marshal(out)
			if err != nil {
				t.Fatal(err)
			}
			return reply(encoded), nil
		}),
	})
	s3Client := s3.New(s3.Options{Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{},
		HTTPClient: noteSourceHTTPClient(func(*http.Request) (*http.Response, error) {
			s3Calls++
			return reply([]byte("large transcript must remain unread")), nil
		}),
	})
	repo := repository.NewDynamoDBRepositoryWithS3(db, "synthetic", s3Client, "synthetic")
	modelClient := bedrockruntime.New(bedrockruntime.Options{
		Region: "ap-northeast-2",
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: "test-key", SecretAccessKey: "test-secret"}, nil
		}),
		HTTPClient: noteSourceHTTPClient(func(req *http.Request) (*http.Response, error) {
			modelCalls++
			payload, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(payload), summary) || strings.Contains(string(payload), "s3://") {
				t.Fatalf("model source was not isolated to the summary: %s", payload)
			}
			return reply([]byte(`{"content":[{"type":"text","text":"[{\"text\":\"할 일\"}]"}],"stop_reason":"end_turn"}`)), nil
		}),
	})
	var event model.ActionItemsRequested
	extractor := NewBedrockService(modelClient, s3Client, repo)
	svc := NewMetadataActionItemsAnalysisService(repo, extractor, func(_ context.Context, e model.ActionItemsRequested) error { event = e; return nil })
	if _, err := svc.Get(context.Background(), "owner", "meeting"); err != nil {
		t.Fatal(err)
	}
	response, err := svc.Request(context.Background(), "owner", "meeting")
	if err != nil || response.Analysis.Status != model.AnalysisQueued || event.RunID == "" {
		t.Fatalf("retry failed: %+v %v", response, err)
	}
	if err := svc.Process(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	response, err = svc.SetCompleted(context.Background(), "owner", "meeting", "old", false)
	if err != nil || response.Analysis.Status != model.AnalysisSucceeded || response.ActionItems[0].Completed {
		t.Fatalf("completion write failed: %+v %v", response, err)
	}
	if s3Calls != 0 || modelCalls != 1 || transactions != 2 || updates != 2 {
		t.Fatalf("metadata lifecycle IO: S3=%d model=%d transactions=%d updates=%d", s3Calls, modelCalls, transactions, updates)
	}
	worker := NewMetadataActionItemsAnalysisService(repo, extractor, nil)
	if err := worker.RunInline(context.Background(), "owner", "meeting"); err != nil {
		t.Fatal(err)
	}
	if s3Calls != 0 || modelCalls != 2 || transactions != 4 || updates != 3 {
		t.Fatalf("inline worker IO: S3=%d model=%d transactions=%d updates=%d", s3Calls, modelCalls, transactions, updates)
	}
	rows[meetingKey]["content"] = map[string]interface{}{"S": " \n"}
	if _, err := svc.Request(context.Background(), "owner", "meeting"); !errors.Is(err, ErrNoAnalysisSource) {
		t.Fatalf("blank retry source accepted: %v", err)
	}
	if s3Calls != 0 {
		t.Fatal("blank-summary retry hydrated transcripts")
	}
}

func TestActionProcessBlankSummaryNeverFallsBackToStoredS3Ref(t *testing.T) {
	for _, content := range []string{"", " \n\t"} {
		t.Run(content, func(t *testing.T) {
			svc, repo, meeting := newAnalysisTest(t, nil)
			meeting.Content = content
			meeting.TranscriptA = "s3://synthetic/transcripts/meeting/transcriptA.txt"
			repo.state = &model.ActionItemsAnalysis{RunID: "legacy", Status: model.AnalysisQueued,
				SourceHash: actionSourceHash(content), LeaseUntil: svc.now().Add(time.Minute).UnixMilli()}
			old := meeting.ActionItems
			request, _, _, err := invokeNoteSourceFixture(t, meeting,
				`{"content":[{"type":"text","text":"[]"}],"stop_reason":"end_turn"}`,
				func(extractor *BedrockService) (string, error) {
					svc.extractor = extractor
					return "", svc.Process(context.Background(), model.ActionItemsRequested{OwnerID: "owner", MeetingID: "meeting", RunID: "legacy"})
				})
			if err != nil {
				t.Fatal(err)
			}
			if len(request.Messages) != 0 {
				t.Fatal("blank summary sent a stored S3 reference to the real model request path")
			}
			if repo.state.Status != model.AnalysisFailed || repo.state.ErrorCode != model.AnalysisSourceChanged || meeting.ActionItems != old {
				t.Fatalf("blank source changed saved items or failure state: %+v", repo.state)
			}
		})
	}
}
