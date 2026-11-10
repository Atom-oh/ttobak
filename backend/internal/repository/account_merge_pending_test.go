package repository

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
)

func mergedSourceMeta() string {
	return `{"Item":{"PK":{"S":"ACCOUNT#a"},"SK":{"S":"META"},"accountId":{"S":"a"},"mergedInto":{"S":"b"}}}`
}

func mergePendingFixture() *model.PendingShare {
	return &model.PendingShare{
		PK: "PENDING_SHARE#invitee@example.com", SK: "PENDING_ACCOUNT#a",
		Kind: model.PendingShareKindAccount, AccountID: "a", Role: model.RoleSSA,
		Email: "invitee@example.com", InvitedByUserID: "member", InvitedCognitoSub: "u",
		CreatedAt: time.Now().UTC(), TTL: time.Now().Add(time.Hour).Unix(),
	}
}

func TestMaterializePendingAccountGrantRedirectsToMergeTarget(t *testing.T) {
	repo := projectInviteWire(t, func(target string, body map[string]any) (int, string) {
		if strings.HasSuffix(target, ".GetItem") {
			return 200, mergedSourceMeta()
		}
		if !strings.HasSuffix(target, ".TransactWriteItems") {
			t.Fatalf("unexpected operation %s", target)
		}
		items := body["TransactItems"].([]any)
		if len(items) != 4 {
			t.Fatalf("items=%d, want 4", len(items))
		}
		put := items[1].(map[string]any)["Put"].(map[string]any)["Item"].(map[string]any)
		if put["PK"].(map[string]any)["S"] != "ACCOUNT#b" || put["SK"].(map[string]any)["S"] != "MEMBER#u" ||
			put["role"].(map[string]any)["S"] != model.RoleSSA || put["accountId"].(map[string]any)["S"] != "b" ||
			put["GSI1SK"].(map[string]any)["S"] != "ACCOUNT#b" {
			t.Fatalf("grant not redirected to the merge target: %+v", put)
		}
		inviter := items[0].(map[string]any)["ConditionCheck"].(map[string]any)["Key"].(map[string]any)
		if inviter["PK"].(map[string]any)["S"] != "ACCOUNT#b" {
			t.Fatalf("inviter authority not checked on the target: %+v", inviter)
		}
		if items[2].(map[string]any)["Delete"] == nil {
			t.Fatalf("pending row not deleted: %+v", items[2])
		}
		meta := items[3].(map[string]any)["ConditionCheck"].(map[string]any)
		key := meta["Key"].(map[string]any)
		if key["PK"].(map[string]any)["S"] != "ACCOUNT#b" || key["SK"].(map[string]any)["S"] != "META" ||
			!strings.Contains(meta["ConditionExpression"].(string), "attribute_not_exists") {
			t.Fatalf("target META not conditioned on no mergedInto: %+v", meta)
		}
		seen := map[any]bool{}
		for _, n := range meta["ExpressionAttributeNames"].(map[string]any) {
			seen[n] = true
		}
		if len(seen) != 2 || !seen["mergingInto"] || !seen["mergedInto"] {
			t.Fatalf("target META must be fenced on mergingInto and mergedInto: %+v", seen)
		}
		deleteNames := items[2].(map[string]any)["Delete"].(map[string]any)["ExpressionAttributeNames"].(map[string]any)
		for _, required := range []string{"createdAt", "invitedCognitoSub", "pendingShareExpiresAt"} {
			found := false
			for _, n := range deleteNames {
				found = found || n == required
			}
			if !found {
				t.Fatalf("redirected claim does not pin %s: %+v", required, deleteNames)
			}
		}
		return 200, "{}"
	})
	granted, err := repo.MaterializePendingAccountGrant(context.Background(), mergePendingFixture(), "u", "invitee@example.com")
	if err != nil || !granted {
		t.Fatalf("granted=%v err=%v", granted, err)
	}
}

func TestMaterializePendingAccountGrantTombstonedTargetDropsRow(t *testing.T) {
	deleted := false
	reads := 0
	repo := projectInviteWire(t, func(target string, body map[string]any) (int, string) {
		switch {
		case strings.HasSuffix(target, ".GetItem"):
			reads++
			if reads == 1 {
				return 200, mergedSourceMeta()
			}
			return 200, `{"Item":{"PK":{"S":"ACCOUNT#b"},"SK":{"S":"META"},"accountId":{"S":"b"},"mergedInto":{"S":"c"}}}`
		case strings.HasSuffix(target, ".TransactWriteItems"):
			return 400, projectCancellation(4, 3) // target META check failed
		case strings.HasSuffix(target, ".DeleteItem"):
			deleted = true
			if body["ConditionExpression"] == nil {
				t.Fatal("row deletion must stay version conditioned")
			}
			return 200, "{}"
		}
		t.Fatalf("unexpected operation %s", target)
		return 500, "{}"
	})
	resolved, err := repo.MaterializePendingAccountGrant(context.Background(), mergePendingFixture(), "u", "invitee@example.com")
	if err != nil || !resolved || !deleted {
		t.Fatalf("resolved=%v deleted=%v err=%v", resolved, deleted, err)
	}
}

func TestMaterializePendingAccountGrantSourceTombstonedRacesRetry(t *testing.T) {
	repo := projectInviteWire(t, func(target string, _ map[string]any) (int, string) {
		switch {
		case strings.HasSuffix(target, ".GetItem"):
			return 200, "{}"
		case strings.HasSuffix(target, ".TransactWriteItems"):
			return 400, projectCancellation(4, 3)
		}
		t.Fatalf("row must be kept for a retry, got %s", target)
		return 500, "{}"
	})
	resolved, err := repo.MaterializePendingAccountGrant(context.Background(), mergePendingFixture(), "u", "invitee@example.com")
	if err != nil || resolved {
		t.Fatalf("resolved=%v err=%v, want false,nil", resolved, err)
	}
}

func TestMaterializePendingAccountGrantMergingTargetKeepsRow(t *testing.T) {
	reads := 0
	repo := projectInviteWire(t, func(target string, _ map[string]any) (int, string) {
		switch {
		case strings.HasSuffix(target, ".GetItem"):
			reads++
			if reads == 1 {
				return 200, mergedSourceMeta()
			}
			return 200, `{"Item":{"PK":{"S":"ACCOUNT#b"},"SK":{"S":"META"},"accountId":{"S":"b"},"mergingInto":{"S":"c"}}}`
		case strings.HasSuffix(target, ".TransactWriteItems"):
			return 400, projectCancellation(4, 3)
		}
		t.Fatalf("row must be kept while the target is merging, got %s", target)
		return 500, "{}"
	})
	resolved, err := repo.MaterializePendingAccountGrant(context.Background(), mergePendingFixture(), "u", "invitee@example.com")
	if err != nil || resolved {
		t.Fatalf("resolved=%v err=%v, want false,nil", resolved, err)
	}
}

func TestMaterializePendingAccountGrantSourceMergingKeepsRow(t *testing.T) {
	repo := projectInviteWire(t, func(target string, _ map[string]any) (int, string) {
		switch {
		case strings.HasSuffix(target, ".GetItem"):
			return 200, `{"Item":{"PK":{"S":"ACCOUNT#a"},"SK":{"S":"META"},"accountId":{"S":"a"},"mergingInto":{"S":"b"}}}`
		case strings.HasSuffix(target, ".TransactWriteItems"):
			return 400, projectCancellation(4, 3)
		}
		t.Fatalf("row must be kept while the source is merging, got %s", target)
		return 500, "{}"
	})
	resolved, err := repo.MaterializePendingAccountGrant(context.Background(), mergePendingFixture(), "u", "invitee@example.com")
	if err != nil || resolved {
		t.Fatalf("resolved=%v err=%v, want false,nil", resolved, err)
	}
}

func TestMaterializePendingAccountGrantPreReadErrorLeavesRow(t *testing.T) {
	repo := projectInviteWire(t, func(target string, _ map[string]any) (int, string) {
		if strings.HasSuffix(target, ".GetItem") {
			return 500, `{"__type":"InternalServerError","message":"boom"}`
		}
		t.Fatalf("no write may follow a failed pre-read, got %s", target)
		return 500, "{}"
	})
	resolved, err := repo.MaterializePendingAccountGrant(context.Background(), mergePendingFixture(), "u", "invitee@example.com")
	if err == nil || resolved {
		t.Fatalf("resolved=%v err=%v, want error", resolved, err)
	}
}
