package repository

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
)

func TestMaterializePendingAccountGrant_InviterMembershipAndIdentity(t *testing.T) {
	repo := projectInviteWire(t, func(target string, body map[string]any) (int, string) {
		if !strings.HasSuffix(target, ".TransactWriteItems") {
			t.Fatalf("unexpected operation %s", target)
		}
		items := body["TransactItems"].([]any)
		check := items[0].(map[string]any)["ConditionCheck"].(map[string]any)
		names := check["ExpressionAttributeNames"].(map[string]any)
		for _, name := range names {
			if name == "role" {
				t.Fatal("current members must not be required to be owners")
			}
		}
		deletion := items[2].(map[string]any)["Delete"].(map[string]any)
		deleteNames := deletion["ExpressionAttributeNames"].(map[string]any)
		for _, required := range []string{"createdAt", "invitedCognitoSub", "pendingShareExpiresAt"} {
			found := false
			for _, name := range deleteNames {
				found = found || name == required
			}
			if !found {
				t.Fatalf("claim does not pin %s: %+v", required, deletion)
			}
		}
		return 200, "{}"
	})
	pending := &model.PendingShare{
		PK: "PENDING_SHARE#invitee@example.com", SK: "PENDING_ACCOUNT#customer",
		Kind: model.PendingShareKindAccount, AccountID: "customer", Role: model.RoleSSA,
		Email: "invitee@example.com", InvitedByUserID: "ordinary-member", InvitedCognitoSub: "invitee",
		CreatedAt: time.Now().UTC(), TTL: time.Now().Add(time.Hour).Unix(),
	}
	granted, err := repo.MaterializePendingAccountGrant(context.Background(), pending, "invitee", "invitee@example.com")
	if err != nil || !granted {
		t.Fatalf("grant=%v err=%v", granted, err)
	}
}

func TestPendingAccountGrant_RemovedInviterCannotMaterialize(t *testing.T) {
	deleted := false
	repo := projectInviteWire(t, func(target string, body map[string]any) (int, string) {
		switch {
		case strings.HasSuffix(target, ".TransactWriteItems"):
			items := body["TransactItems"].([]any)
			if len(items) != 3 || items[0].(map[string]any)["ConditionCheck"] == nil {
				t.Fatalf("claim omitted membership authority: %+v", items)
			}
			return 400, projectCancellation(3, 0)
		case strings.HasSuffix(target, ".DeleteItem"):
			deleted = true
			if body["ConditionExpression"] == nil {
				t.Fatal("dead invitation deletion must remain version conditioned")
			}
			return 200, "{}"
		default:
			t.Fatalf("unexpected operation: %s", target)
			return 500, "{}"
		}
	})
	pending := &model.PendingShare{
		PK: "PENDING_SHARE#invitee@example.com", SK: "PENDING_ACCOUNT#customer",
		Kind: model.PendingShareKindAccount, AccountID: "customer", Role: model.RoleSSA,
		Email: "invitee@example.com", InvitedByUserID: "removed-member", InvitedCognitoSub: "invitee",
		CreatedAt: time.Now().UTC(), TTL: time.Now().Add(time.Hour).Unix(),
	}
	resolved, err := repo.MaterializePendingAccountGrant(context.Background(), pending, "invitee", "invitee@example.com")
	if err != nil || !resolved || !deleted {
		t.Fatalf("invalid invitation was not cleared: resolved=%v deleted=%v error=%v", resolved, deleted, err)
	}
}

func TestPutPendingAccountGrant_ChecksCurrentMembership(t *testing.T) {
	repo := projectInviteWire(t, func(target string, body map[string]any) (int, string) {
		if !strings.HasSuffix(target, ".TransactWriteItems") {
			t.Fatalf("queue authority was not checked transactionally: %s", target)
		}
		items := body["TransactItems"].([]any)
		if len(items) != 2 || items[0].(map[string]any)["ConditionCheck"] == nil || items[1].(map[string]any)["Put"] == nil {
			t.Fatalf("invalid queue transaction: %+v", items)
		}
		return 400, projectCancellation(2, 0)
	})
	err := repo.PutPendingShare(context.Background(), &model.PendingShare{
		Email: "invitee@example.com", Kind: model.PendingShareKindAccount, AccountID: "customer",
		Role: model.RoleSSA, InvitedByUserID: "removed-member", InvitedCognitoSub: "invitee",
	})
	if err == nil {
		t.Fatal("removed inviter queued an account grant")
	}
}

func TestMaterializePendingAccountGrant_RejectsOwnerRole(t *testing.T) {
	repo := projectInviteWire(t, func(string, map[string]any) (int, string) {
		t.Fatal("owner grant reached DynamoDB")
		return 500, "{}"
	})
	_, err := repo.MaterializePendingAccountGrant(context.Background(), &model.PendingShare{
		AccountID: "customer", Role: model.RoleOwner, InvitedByUserID: "member", InvitedCognitoSub: "invitee",
		Email: "invitee@example.com", CreatedAt: time.Now(), TTL: time.Now().Add(time.Hour).Unix(),
	}, "invitee", "invitee@example.com")
	if err == nil {
		t.Fatal("pending grant can promote an owner")
	}
}
