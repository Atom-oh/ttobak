package repository

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestConnectorApprovalPinsPolicyGenerationAndAuditInOneTransaction(t *testing.T) {
	calls := 0
	repo := indexingWire(t, func(target string, body map[string]interface{}) string {
		calls++
		if !strings.HasSuffix(target, ".TransactWriteItems") {
			t.Fatal("approval was not transactional")
		}
		items := body["TransactItems"].([]interface{})
		if len(items) != 3 {
			t.Fatal("policy,approval,audit do not share a transaction")
		}
		policy := items[0].(map[string]interface{})["ConditionCheck"].(map[string]interface{})
		condition := policy["ConditionExpression"].(string)
		if strings.Contains(condition, "contains") || strings.Contains(condition, "enabled") {
			t.Fatal("blocking must remain possible after policy tightening")
		}
		approval := items[1].(map[string]interface{})["Put"].(map[string]interface{})
		key := approval["Item"].(map[string]interface{})
		if key["PK"].(map[string]interface{})["S"] != "USER#verified-user" {
			t.Fatal("caller partition lost")
		}
		if key["generation"].(map[string]interface{})["N"] != "8" {
			t.Fatal("generation not incremented")
		}
		if _, present := key["pendingShareExpiresAt"]; present {
			t.Fatal("blocked-client tombstone could expire into access")
		}
		if approval["ConditionExpression"] == nil {
			t.Fatal("stale whole-item approval replacement")
		}
		audit := items[2].(map[string]interface{})["Put"].(map[string]interface{})["Item"].(map[string]interface{})
		if audit["actorHash"] == nil || audit["pendingShareExpiresAt"] == nil || audit["arguments"] != nil {
			t.Fatal("audit lacks retention or retains private arguments")
		}
		return `{}`
	})
	generation, err := repo.UpdateConnectorApproval(context.Background(), ConnectorApprovalUpdate{
		UserID: "verified-user", ClientID: "client", ExpectedGeneration: 7,
		Capabilities: []string{"read", "write"}, ExpiresAt: time.Now().Unix() + 600, Block: true,
	})
	if err != nil || generation != 8 || calls != 1 {
		t.Fatalf("approval update: %d %v", generation, err)
	}
}
