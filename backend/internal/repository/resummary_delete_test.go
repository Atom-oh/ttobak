package repository

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestResummaryDeletionDoesNotOrphanStateWhenLaterBatchFails(t *testing.T) {
	deleted := map[string]bool{}
	transactions := 0
	repo := summarySDKRepo(func(req *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		target := req.Header.Get("X-Amz-Target")
		if strings.HasSuffix(target, ".Query") {
			encoded, _ := json.Marshal(body)
			if strings.Contains(string(encoded), `"ATTACH#"`) {
				var items []map[string]any
				for i := 0; i < 55; i++ {
					id := string([]byte{byte('a' + i/26), byte('a' + i%26)})
					items = append(items, map[string]any{
						"attachmentId": map[string]string{"S": id},
						"meetingId":    map[string]string{"S": "meeting"},
					})
				}
				data, _ := json.Marshal(map[string]any{"Items": items})
				return summaryHTTPResponse(200, string(data), nil), nil
			}
			return summaryHTTPResponse(200, `{"Items":[]}`, nil), nil
		}
		if !strings.HasSuffix(target, ".TransactWriteItems") {
			t.Fatalf("unexpected %s", target)
		}
		transactions++
		if transactions > 1 {
			return summaryHTTPResponse(400, `{"__type":"AccessDeniedException","message":"synthetic later-batch failure"}`, nil), nil
		}
		for _, item := range body["TransactItems"].([]any) {
			key := item.(map[string]any)["Delete"].(map[string]any)["Key"].(map[string]any)
			deleted[key["SK"].(map[string]any)["S"].(string)] = true
		}
		return summaryHTTPResponse(200, `{}`, nil), nil
	})
	if err := repo.DeleteMeeting(context.Background(), "owner", "meeting"); err == nil {
		t.Fatal("missing injected error")
	}
	if transactions != 2 {
		t.Fatalf("expected two batches, got %d", transactions)
	}
	if deleted["MEETING#meeting"] && !deleted["ANALYSIS#summary"] {
		t.Fatal("source committed deletion in batch 1 but summary state survived batch 2 failure; service retry returns not found")
	}
}
