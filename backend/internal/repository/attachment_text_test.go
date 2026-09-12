package repository

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
)

func TestQueueAttachmentTextChecksParentAttachmentAndRun(t *testing.T) {
	repo := actionAnalysisWire(t, func(target string, body map[string]any) {
		if !strings.HasSuffix(target, ".TransactWriteItems") {
			t.Fatalf("queue must be atomic: %s", target)
		}
		ops := body["TransactItems"].([]any)
		if len(ops) != 3 {
			t.Fatalf("expected parent, attachment and state guards: %d", len(ops))
		}
		parent := ops[0].(map[string]any)["ConditionCheck"].(map[string]any)
		if parent["Key"].(map[string]any)["PK"].(map[string]any)["S"] != "USER#owner" {
			t.Fatal("parent owner not pinned")
		}
		attachment := ops[1].(map[string]any)["ConditionCheck"].(map[string]any)
		condition := analysisCondition(t, attachment)
		for _, expected := range []string{"originalKey", "files/uploader/meeting/file.pdf", "userId", "uploader"} {
			if !strings.Contains(condition, expected) {
				t.Fatalf("missing attachment guard %s: %s", expected, condition)
			}
		}
		state := ops[2].(map[string]any)["Update"].(map[string]any)
		if state["Key"].(map[string]any)["SK"].(map[string]any)["S"] != "ATTEXT#attachment" {
			t.Fatal("text state overlaps attachment metadata")
		}
		if condition := analysisCondition(t, state); !strings.Contains(condition, "old-run") || !strings.Contains(condition, "failed") {
			t.Fatalf("prior run not guarded: %s", condition)
		}
	}, 200, `{}`)
	attachment := &model.Attachment{AttachmentID: "attachment", MeetingID: "meeting", UserID: "uploader", OriginalKey: "files/uploader/meeting/file.pdf"}
	prior := &model.AttachmentTextState{RunID: "old-run", Status: model.AttachmentTextFailed}
	next := &model.AttachmentTextState{RunID: "new-run", Status: model.AttachmentTextQueued, OwnerID: "owner", UploaderID: "uploader", SourceKey: attachment.OriginalKey, UpdatedAt: time.Now().UTC()}
	if err := repo.QueueAttachmentText(context.Background(), attachment, prior, next); err != nil {
		t.Fatal(err)
	}
}
