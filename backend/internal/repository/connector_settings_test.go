package repository

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestConnectorSettingsDisabledAndBlockedRemainManageableWithoutGrantingAccess(t *testing.T) {
	policy, approval := policyFixture(), approvalFixture()
	policy["enabled"] = map[string]any{"BOOL": false}
	approval["blocked"] = map[string]any{"BOOL": true}
	repo := indexingWire(t, func(target string, body map[string]any) string {
		if !strings.HasSuffix(target, ".TransactGetItems") {
			t.Fatal("not an atomic settings read")
		}
		items := body["TransactItems"].([]any)
		if len(items) != 2 || items[1].(map[string]any)["Get"].(map[string]any)["Key"].(map[string]any)["PK"].(map[string]any)["S"] != "USER#user" {
			t.Fatal("not caller scoped")
		}
		raw, _ := json.Marshal(map[string]any{"Responses": []any{map[string]any{"Item": policy}, map[string]any{"Item": approval}}})
		return string(raw)
	})
	view, err := repo.ConnectorSettings(context.Background(), "user", "client", grantAudience, grantIssuer)
	if err != nil || view.Enabled || view.PolicyGeneration != 2 || view.Approval == nil || !view.Approval.Blocked {
		t.Fatalf("management view: %+v %v", view, err)
	}
	policy["issuer"] = map[string]any{"S": "https://foreign.invalid/pool"}
	if _, err := repo.ConnectorSettings(context.Background(), "user", "client", grantAudience, grantIssuer); err == nil {
		t.Fatal("foreign deployment returned settings")
	}
}

func TestBoundApprovalTransactionPinsObservedPolicyRevisionAudienceAndIssuer(t *testing.T) {
	repo := indexingWire(t, func(target string, body map[string]any) string {
		if !strings.HasSuffix(target, ".TransactWriteItems") {
			t.Fatal("not a transaction")
		}
		check := body["TransactItems"].([]any)[0].(map[string]any)["ConditionCheck"].(map[string]any)
		names, values := check["ExpressionAttributeNames"].(map[string]any), check["ExpressionAttributeValues"].(map[string]any)
		condition := check["ConditionExpression"].(string)
		for _, field := range []string{"generation", "audience", "issuer"} {
			found := false
			for token, value := range names {
				if value == field && strings.Contains(condition, token) {
					found = true
				}
			}
			if !found {
				t.Fatalf("missing policy pin %s", field)
			}
		}
		for _, wanted := range []string{grantAudience, grantIssuer, "7"} {
			found := false
			for _, value := range values {
				v := value.(map[string]any)
				found = found || v["S"] == wanted || v["N"] == wanted
			}
			if !found {
				t.Fatalf("missing pinned value %s", wanted)
			}
		}
		return `{}`
	})
	_, err := repo.UpdateConnectorApprovalBound(context.Background(), ConnectorApprovalUpdate{
		UserID: "user", ClientID: "client", ExpectedGeneration: 3, Capabilities: []string{"read"},
		ExpiresAt: time.Now().Unix() + 3600, Block: true, Revoke: true,
	}, grantAudience, grantIssuer, 7)
	if err != nil {
		t.Fatal(err)
	}
}
