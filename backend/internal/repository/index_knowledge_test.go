package repository

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

func TestKnowledgeJobsPersistIdentityWithRunLeaseCASAndNoFakeSourceRow(t *testing.T) {
	resource, _ := model.KnowledgeIndexResource("kb/owner/file.pdf")
	calls := 0
	repo := indexingWire(t, func(target string, body map[string]interface{}) string {
		calls++
		if !strings.HasSuffix(target, ".UpdateItem") {
			t.Fatal("S3 authority was represented as a nonexistent DynamoDB source row")
		}
		names := body["ExpressionAttributeNames"].(map[string]interface{})
		condition := body["ConditionExpression"].(string)
		for _, expected := range []string{"version", "runId", "leaseUntil"} {
			found := false
			for alias, name := range names {
				if name == expected && strings.Contains(condition, alias) {
					found = true
				}
			}
			if !found {
				t.Fatalf("missing %s CAS guard: %s", expected, condition)
			}
		}
		foundSource := false
		for _, value := range body["ExpressionAttributeValues"].(map[string]interface{}) {
			fields, ok := value.(map[string]interface{})["M"].(map[string]interface{})
			if !ok {
				continue
			}
			if source, ok := fields["sourceKey"].(map[string]interface{}); ok {
				foundSource = source["S"] == resource.SourceKey
			}
		}
		if !foundSource {
			t.Fatal("known source key was not persisted for deletion recovery")
		}
		return "{}"
	})
	prior := &model.IndexJob{Resource: resource, Version: 2, State: model.IndexPreparing, RunID: "run", LeaseUntil: 2000}
	next := *prior
	next.State = model.IndexWaitingSync
	if err := repo.SaveIndexJob(context.Background(), prior, &next, nil, false, 1000); err != nil {
		t.Fatal(err)
	}
	if err := repo.SaveIndexJob(context.Background(), prior, &next, nil, true, 1000); !errors.Is(err, ErrInvalidIndexResource) {
		t.Fatalf("accidental absent-row source proof accepted: %v", err)
	}
	if _, err := repo.GetIndexSource(context.Background(), resource); !errors.Is(err, ErrInvalidIndexResource) {
		t.Fatalf("manual source read as canonical DynamoDB record: %v", err)
	}
	bad := resource
	bad.Kind = model.IndexSharedKind
	if err := repo.RequestIndexResource(context.Background(), bad, "revision", 1000); !errors.Is(err, ErrInvalidIndexResource) {
		t.Fatalf("private source relabeled shared: %v", err)
	}
	if calls != 1 {
		t.Fatal("invalid requests reached DynamoDB")
	}
}
