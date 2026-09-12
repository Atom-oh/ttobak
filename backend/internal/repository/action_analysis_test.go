package repository

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
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/ttobak/backend/internal/model"
)

func actionAnalysisWire(t *testing.T, inspect func(string, map[string]any), status int, response string) *DynamoDBRepository {
	t.Helper()
	client := dynamodb.New(dynamodb.Options{
		Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1,
		HTTPClient: meetingListHTTPClientFunc(func(r *http.Request) (*http.Response, error) {
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if inspect != nil {
				inspect(r.Header.Get("X-Amz-Target"), body)
			}
			return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/x-amz-json-1.0"}}, Body: io.NopCloser(strings.NewReader(response))}, nil
		}),
	})
	return NewDynamoDBRepository(client, "analysis-test")
}

func analysisCondition(t *testing.T, op map[string]any) string {
	t.Helper()
	condition := op["ConditionExpression"].(string)
	for alias, name := range op["ExpressionAttributeNames"].(map[string]any) {
		condition = strings.ReplaceAll(condition, alias, name.(string))
	}
	for alias, value := range op["ExpressionAttributeValues"].(map[string]any) {
		encoded, _ := json.Marshal(value)
		condition = strings.ReplaceAll(condition, alias, string(encoded))
	}
	return condition
}

func TestActionAnalysisCompletionIsOneGuardedTransaction(t *testing.T) {
	repo := actionAnalysisWire(t, func(target string, body map[string]any) {
		if !strings.HasSuffix(target, ".TransactWriteItems") {
			t.Fatalf("result/state must be atomic, got %s", target)
		}
		operations := body["TransactItems"].([]any)
		if len(operations) != 2 {
			t.Fatalf("expected result + state, got %d", len(operations))
		}
		for i, operation := range operations {
			op := operation.(map[string]any)["Update"].(map[string]any)
			condition := analysisCondition(t, op)
			for _, required := range []string{"attribute_exists", "PK"} {
				if !strings.Contains(condition, required) {
					t.Errorf("missing existence guard: %s", condition)
				}
			}
			if i == 0 {
				for _, required := range []string{"content", "snapshot", "actionItems", "previous"} {
					if !strings.Contains(condition, required) {
						t.Errorf("missing source/completion guard %s: %s", required, condition)
					}
				}
				if op["Key"].(map[string]any)["PK"].(map[string]any)["S"] != "USER#owner" {
					t.Fatal("result must stay in the owner's partition")
				}
			} else {
				for _, required := range []string{"runId", "run-1", "status", model.AnalysisRunning} {
					if !strings.Contains(condition, required) {
						t.Errorf("missing run guard %s: %s", required, condition)
					}
				}
				if op["Key"].(map[string]any)["SK"].(map[string]any)["S"] != model.ActionAnalysisSK {
					t.Fatal("state must not use a meeting-list/share prefix")
				}
			}
		}
	}, 200, `{}`)
	if err := repo.CompleteActionAnalysis(context.Background(), "owner", "meeting", "run-1", "snapshot", "previous", "[]", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
}

func TestActionAnalysisQueueChecksSourceAndPriorRun(t *testing.T) {
	prior := &model.ActionItemsAnalysis{RunID: "old-run", Status: model.AnalysisFailed}
	next := &model.ActionItemsAnalysis{RunID: "new-run", Status: model.AnalysisQueued, SourceHash: "hash", LeaseUntil: 12345}
	repo := actionAnalysisWire(t, func(_ string, body map[string]any) {
		ops := body["TransactItems"].([]any)
		if len(ops) != 2 {
			t.Fatal("queue must check the meeting and conditionally claim state")
		}
		check := ops[0].(map[string]any)["ConditionCheck"].(map[string]any)
		if c := analysisCondition(t, check); !strings.Contains(c, "content") || !strings.Contains(c, "snapshot") {
			t.Fatalf("queue missing source check: %s", c)
		}
		update := ops[1].(map[string]any)["Update"].(map[string]any)
		if c := analysisCondition(t, update); !strings.Contains(c, "old-run") || !strings.Contains(c, "failed") {
			t.Fatalf("queue missing old-run CAS: %s", c)
		}
		if _, put := ops[1].(map[string]any)["Put"]; put {
			t.Fatal("claims must use conditional updates")
		}
	}, 200, `{}`)
	if err := repo.QueueActionAnalysis(context.Background(), "owner", "meeting", "snapshot", prior, next); err != nil {
		t.Fatal(err)
	}
}

func TestActionAnalysisMapsOnlyConditionalCancellation(t *testing.T) {
	for _, code := range []string{"ConditionalCheckFailed", "TransactionConflict"} {
		body := `{"__type":"com.amazonaws.dynamodb.v20120810#TransactionCanceledException","CancellationReasons":[{"Code":"` + code + `"}]}`
		repo := actionAnalysisWire(t, nil, 400, body)
		err := repo.CompleteActionAnalysis(context.Background(), "owner", "meeting", "run", "content", "", "[]", time.Now())
		if errors.Is(err, ErrConditionFailed) != (code == "ConditionalCheckFailed") {
			t.Fatalf("%s mapped incorrectly: %v", code, err)
		}
	}
}
