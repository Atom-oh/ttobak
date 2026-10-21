package repository

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/connector"
)

const grantAudience = "https://example.invalid/api/mcp"
const grantIssuer = "https://cognito-idp.ap-northeast-2.amazonaws.com/ap-northeast-2_test"

func policyFixture() map[string]any {
	return map[string]any{
		"PK": map[string]any{"S": "CONNECTOR_CLIENT#client"}, "SK": map[string]any{"S": "POLICY"},
		"clientId": map[string]any{"S": "client"}, "enabled": map[string]any{"BOOL": true},
		"maximum": map[string]any{"SS": []string{"write", "read"}}, "audience": map[string]any{"S": grantAudience},
		"issuer": map[string]any{"S": grantIssuer}, "generation": map[string]any{"N": "2"},
	}
}
func approvalFixture() map[string]any {
	return map[string]any{
		"PK": map[string]any{"S": "USER#user"}, "SK": map[string]any{"S": "CONNECTOR_APPROVAL#client"},
		"userId": map[string]any{"S": "user"}, "clientId": map[string]any{"S": "client"},
		"generation": map[string]any{"N": "3"}, "expiresAt": map[string]any{"N": "2000000000"},
		"capabilities": map[string]any{"SS": []string{"read", "write"}}, "revoked": map[string]any{"BOOL": false}, "blocked": map[string]any{"BOOL": false},
	}
}
func TestConnectorGrantsUseAtomicMetadataSnapshotAndStrictTypes(t *testing.T) {
	for _, tc := range []struct {
		name                         string
		mutate                       func(map[string]any, map[string]any)
		invalid, blocked, noApproval bool
	}{
		{name: "current approval"},
		{name: "baseline read without approval", noApproval: true},
		{name: "permanent block", mutate: func(_, a map[string]any) { a["blocked"] = map[string]any{"BOOL": true} }, blocked: true},
		{name: "disabled", mutate: func(p, _ map[string]any) { p["enabled"] = map[string]any{"BOOL": false} }, invalid: true},
		{name: "foreign policy", mutate: func(p, _ map[string]any) { p["clientId"] = map[string]any{"S": "foreign"} }, invalid: true},
		{name: "foreign audience", mutate: func(p, _ map[string]any) { p["audience"] = map[string]any{"S": "https://foreign.invalid/api/mcp"} }, invalid: true},
		{name: "foreign approval", mutate: func(_, a map[string]any) { a["userId"] = map[string]any{"S": "foreign"} }, invalid: true},
		{name: "missing block", mutate: func(_, a map[string]any) { delete(a, "blocked") }, invalid: true},
		{name: "string boolean", mutate: func(p, _ map[string]any) { p["enabled"] = map[string]any{"S": "true"} }, invalid: true},
		{name: "list maximum", mutate: func(p, _ map[string]any) { p["maximum"] = map[string]any{"L": []any{map[string]any{"S": "read"}}} }, invalid: true},
		{name: "missing read", mutate: func(p, _ map[string]any) { p["maximum"] = map[string]any{"SS": []string{"write"}} }, invalid: true},
		{name: "invalid capability", mutate: func(_, a map[string]any) { a["capabilities"] = map[string]any{"SS": []string{"read", "admin"}} }, invalid: true},
		{name: "missing generation", mutate: func(p, _ map[string]any) { delete(p, "generation") }, invalid: true},
		{name: "negative generation", mutate: func(_, a map[string]any) { a["generation"] = map[string]any{"N": "-1"} }, invalid: true},
		{name: "fractional expiry", mutate: func(_, a map[string]any) { a["expiresAt"] = map[string]any{"N": "100.5"} }, invalid: true},
		{name: "unsafe generation", mutate: func(_, a map[string]any) { a["generation"] = map[string]any{"N": "9007199254740992"} }, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy, approval := policyFixture(), approvalFixture()
			if tc.mutate != nil {
				tc.mutate(policy, approval)
			}
			if tc.noApproval {
				approval = nil
			}
			calls := 0
			repo := indexingWire(t, func(target string, body map[string]interface{}) string {
				calls++
				if !strings.HasSuffix(target, ".TransactGetItems") {
					t.Fatal("non-atomic grant read")
				}
				items := body["TransactItems"].([]any)
				if len(items) != 2 {
					t.Fatal("unexpected grant selection")
				}
				for index, item := range items {
					get := item.(map[string]any)["Get"].(map[string]any)
					key := get["Key"].(map[string]any)
					wanted := []string{"CONNECTOR_CLIENT#client", "USER#user"}[index]
					if key["PK"].(map[string]any)["S"] != wanted || get["ProjectionExpression"] == nil {
						t.Fatal("identity/projection lost")
					}
					for _, name := range get["ExpressionAttributeNames"].(map[string]any) {
						if name == "content" || name == "token" {
							t.Fatal("private body projected")
						}
					}
				}
				encoded, _ := json.Marshal(map[string]any{"Responses": []any{map[string]any{"Item": policy}, map[string]any{"Item": approval}}})
				return string(encoded)
			})
			got, err := repo.ConnectorGrantResolver(grantAudience, grantIssuer)(context.Background(), "user", "client")
			if calls != 1 || (err != nil) != tc.invalid {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
			if !tc.invalid && (got.UserID != "user" || got.ClientID != "client" || got.ClientEnabled == tc.blocked || (got.Approval == nil) != tc.noApproval) {
				t.Fatalf("wrong grants: %+v", got)
			}
		})
	}
}
func TestConnectorGrantSelectionRejectsUnboundedIDsBeforeAWS(t *testing.T) {
	repo := indexingWire(t, func(string, map[string]interface{}) string { t.Fatal("invalid selection reached AWS"); return "{}" })
	for _, pair := range [][2]string{{"", "client"}, {"user", "bad#client"}, {strings.Repeat("x", 129), "client"}} {
		if _, err := repo.ConnectorGrantResolver(grantAudience, grantIssuer)(context.Background(), pair[0], pair[1]); !errors.Is(err, connector.ErrInvalidGrant) {
			t.Fatal(err)
		}
	}
	if _, err := repo.ConnectorGrantResolver("", grantIssuer)(context.Background(), "user", "client"); !errors.Is(err, connector.ErrInvalidGrant) {
		t.Fatal(err)
	}
}
func TestConnectorWriteErrorDistinguishesCASFromUnknownOutcome(t *testing.T) {
	err := &types.TransactionCanceledException{CancellationReasons: []types.CancellationReason{{Code: aws.String("ConditionalCheckFailed")}}}
	if !errors.Is(connectorWriteError(err), ErrConditionFailed) {
		t.Fatal("CAS conflict lost")
	}
	if !errors.Is(connectorWriteError(errors.New("private remote detail")), connector.ErrPolicyUnavailable) {
		t.Fatal("unknown outcome lost")
	}
}
func TestConnectorApprovalGrantPinsMaximumButRevocationDoesNotNeedEnabledPolicy(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		repo := indexingWire(t, func(target string, body map[string]interface{}) string {
			if !strings.HasSuffix(target, ".TransactWriteItems") || body["ClientRequestToken"] == nil {
				t.Fatal("transaction replay identity missing")
			}
			items := body["TransactItems"].([]any)
			check := items[0].(map[string]any)["ConditionCheck"].(map[string]any)
			contains := strings.Contains(check["ConditionExpression"].(string), "contains")
			if contains == revoke {
				t.Fatal("grant/revocation maximum checks inverted")
			}
			row := items[1].(map[string]any)["Put"].(map[string]any)
			if row["ConditionExpression"] == nil {
				t.Fatal("approval generation not pinned")
			}
			return "{}"
		})
		_, err := repo.UpdateConnectorApproval(context.Background(), ConnectorApprovalUpdate{UserID: "user", ClientID: "client", ExpectedGeneration: 1, Capabilities: []string{"read", "write"}, ExpiresAt: time.Now().Unix() + 60, Revoke: revoke})
		if err != nil {
			t.Fatal(err)
		}
	}
}
func TestConnectorPolicyHasOnlyAvailableProfilesAndCASAudit(t *testing.T) {
	calls := 0
	repo := indexingWire(t, func(target string, body map[string]interface{}) string {
		calls++
		if !strings.HasSuffix(target, ".TransactWriteItems") || body["ClientRequestToken"] == nil {
			t.Fatal("transaction identity missing")
		}
		items := body["TransactItems"].([]any)
		if len(items) != 2 {
			t.Fatal("audit not atomic")
		}
		policy := items[0].(map[string]any)["Put"].(map[string]any)
		if !strings.Contains(policy["ConditionExpression"].(string), "AND") {
			t.Fatal("policy generation not pinned")
		}
		return "{}"
	})
	for _, caps := range [][]string{{"write"}, {"read", "share"}, {"read", "read"}, {"write", "read"}} {
		_, err := repo.SetConnectorPolicy(context.Background(), "actor", "client", grantAudience, grantIssuer, true, 1, caps)
		if !errors.Is(err, connector.ErrInvalidGrant) {
			t.Fatal("invalid profile accepted")
		}
	}
	if calls != 0 {
		t.Fatal("invalid policy reached AWS")
	}
	generation, err := repo.SetConnectorPolicy(context.Background(), "actor", "client", grantAudience, grantIssuer, true, 1, []string{"read", "write"})
	if err != nil || generation != 2 || calls != 1 {
		t.Fatalf("policy result %d %v %d", generation, err, calls)
	}
}
