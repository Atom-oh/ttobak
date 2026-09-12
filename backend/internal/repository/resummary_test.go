package repository

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
)

func TestResummaryCompletionGuardsSourcesAndPublishesAtomically(t *testing.T) {
	repo := actionAnalysisWire(t, func(target string, body map[string]any) {
		if !strings.HasSuffix(target, ".TransactWriteItems") {
			t.Fatalf("completion is not atomic: %s", target)
		}
		ops := body["TransactItems"].([]any)
		if len(ops) != 3 {
			t.Fatalf("missing source/state checks: %d", len(ops))
		}
		meeting := ops[0].(map[string]any)["Update"].(map[string]any)
		condition := analysisCondition(t, meeting)
		for _, value := range []string{"content", "old", "transcriptA", "saved source", "notes"} {
			if !strings.Contains(condition, value) {
				t.Fatalf("missing %s: %s", value, condition)
			}
		}
		if !strings.Contains(condition, "attribute_not_exists") {
			t.Fatal("absent note guard missing")
		}
		state := ops[1].(map[string]any)["Update"].(map[string]any)
		condition = analysisCondition(t, state)
		for _, value := range []string{"run", "running", "leaseUntil"} {
			if !strings.Contains(condition, value) {
				t.Fatalf("missing active run guard: %s", condition)
			}
		}
		if ops[2].(map[string]any)["ConditionCheck"] == nil {
			t.Fatal("attachment guard missing")
		}
	}, 200, `{}`)
	now := time.Now().UTC()
	snapshot := &model.SummarySnapshot{Meeting: &model.Meeting{UserID: "owner", MeetingID: "meeting"},
		Stored: map[string]interface{}{"content": "old", "transcriptA": "saved source"},
		Checks: []model.SummaryCheck{
			{PK: "USER#owner", SK: "MEETING#meeting", Exists: true, Fields: map[string]model.SummaryValue{
				"content": {Present: true, Value: "old"}, "transcriptA": {Present: true, Value: "saved source"}, "notes": {},
			}},
			{PK: "MEETING#meeting", SK: "ATTACH#attachment", Exists: true, Fields: map[string]model.SummaryValue{"originalKey": {Present: true, Value: "files/owner/meeting/a.pdf"}}},
		}}
	state := &model.ResummaryState{RunID: "run", Status: model.AnalysisRunning, OwnerID: "owner", RequestedBy: "editor", SourceHash: "hash", LeaseUntil: now.Add(time.Minute).UnixMilli()}
	if err := repo.CompleteResummary(context.Background(), snapshot, state, "new", "coverage", "result-hash", now); err != nil {
		t.Fatal(err)
	}
}
