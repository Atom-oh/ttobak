package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestDeleteAttachmentAtomicallyRemovesTextState(t *testing.T) {
	var deleted []string
	repo := indexingWire(t, func(target string, body map[string]interface{}) string {
		if !strings.HasSuffix(target, ".TransactWriteItems") {
			t.Fatalf("expected atomic deletion, got %s", target)
		}
		for _, raw := range body["TransactItems"].([]interface{}) {
			key := raw.(map[string]interface{})["Delete"].(map[string]interface{})["Key"].(map[string]interface{})
			deleted = append(deleted, key["SK"].(map[string]interface{})["S"].(string))
		}
		return "{}"
	})
	if err := repo.DeleteAttachment(context.Background(), "meeting", "attachment"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(deleted, ",") != "ATTACH#attachment,ATTEXT#attachment" {
		t.Fatalf("incomplete deletion: %v", deleted)
	}
}

func TestDeleteMeetingCascadesTextStatesAcrossBatchesAndPages(t *testing.T) {
	deleted := map[string]bool{}
	sweep := 0
	repo := indexingWire(t, func(target string, body map[string]interface{}) string {
		if strings.HasSuffix(target, ".Query") {
			encoded, _ := json.Marshal(body)
			if strings.Contains(string(encoded), `"ATTEXT#"`) {
				if body["ConsistentRead"] != true {
					t.Fatal("cleanup must read current states")
				}
				sweep++
				if sweep == 1 {
					return `{"Items":[],"LastEvaluatedKey":{"PK":{"S":"MEETING#meeting"},"SK":{"S":"ATTEXT#gone"}}}`
				}
				if body["ExclusiveStartKey"] == nil {
					t.Fatal("cleanup pagination lost")
				}
				return `{"Items":[{"PK":{"S":"MEETING#meeting"},"SK":{"S":"ATTEXT#late"}}]}`
			}
			if strings.Contains(string(encoded), `"ATTACH#"`) {
				items := make([]string, 55)
				for i := range items {
					items[i] = fmt.Sprintf(`{"attachmentId":{"S":"a%02d"},"meetingId":{"S":"meeting"}}`, i)
				}
				return `{"Items":[` + strings.Join(items, ",") + `]}`
			}
			return `{"Items":[]}`
		}
		if !strings.HasSuffix(target, ".TransactWriteItems") {
			t.Fatalf("unexpected call %s", target)
		}
		items := body["TransactItems"].([]interface{})
		if len(items) > 100 {
			t.Fatal("transaction exceeds DynamoDB limit")
		}
		current := map[string]bool{}
		for _, item := range items {
			key := item.(map[string]interface{})["Delete"].(map[string]interface{})["Key"].(map[string]interface{})
			id := key["SK"].(map[string]interface{})["S"].(string)
			deleted[id], current[id] = true, true
		}
		for id := range current {
			if strings.HasPrefix(id, "ATTACH#") && !current["ATTEXT#"+strings.TrimPrefix(id, "ATTACH#")] {
				t.Fatalf("attachment and state split across transactions: %s", id)
			}
		}
		return "{}"
	})
	if err := repo.DeleteMeeting(context.Background(), "owner", "meeting"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 55; i++ {
		if !deleted[fmt.Sprintf("ATTEXT#a%02d", i)] {
			t.Fatalf("state %d survived", i)
		}
	}
	if !deleted["ATTEXT#late"] || sweep != 2 {
		t.Fatalf("late/orphan state survived: %v pages=%d", deleted["ATTEXT#late"], sweep)
	}
	if !deleted["ANALYSIS#summary"] {
		t.Fatal("saved re-summary state survived meeting deletion")
	}
}
