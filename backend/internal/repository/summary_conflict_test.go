package repository

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

func TestSummaryConflictMarkerOwnsOnlyItsClaimAndNeverWritesOldOutput(t *testing.T) {
	repo := actionAnalysisWire(t, func(target string, body map[string]any) {
		if !strings.HasSuffix(target, ".UpdateItem") {
			t.Fatal(target)
		}
		condition := analysisCondition(t, body)
		if !strings.Contains(condition, "summarizing") || !strings.Contains(condition, "claim-1") ||
			!strings.Contains(body["UpdateExpression"].(string), "REMOVE") {
			t.Fatal("retry claim not guarded")
		}
		wire, _ := json.Marshal(body)
		if strings.Contains(string(wire), "old model output") {
			t.Fatal("conflict reuses model output")
		}
		for _, name := range body["ExpressionAttributeNames"].(map[string]any) {
			if name == "content" || name == "notes" || name == "transcriptA" {
				t.Fatal("conflict overwrites source")
			}
		}
	}, 200, `{}`)
	snapshot := &model.SummarySnapshot{Meeting: &model.Meeting{UserID: "owner", MeetingID: "m"},
		Stored: map[string]interface{}{"summarizeRetryClaimedAt": "claim-1", "content": "old model output"},
		Checks: []model.SummaryCheck{{PK: "USER#owner", SK: "MEETING#m", Exists: true}}}
	if err := repo.MarkSummaryConflict(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
}
