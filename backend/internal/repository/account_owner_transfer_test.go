package repository

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func hierarchyUpdateText(op *hierarchyWireOperation) string {
	s := op.UpdateExpression
	for alias, name := range op.ExpressionAttributeNames {
		s = strings.ReplaceAll(s, alias, name)
	}
	for alias, value := range op.ExpressionAttributeValues {
		s = strings.ReplaceAll(s, alias, `"`+value["S"]+`"`)
	}
	return s
}

func TestTransferAccountOwnerTransaction(t *testing.T) {
	called := false
	r := hierarchyWireRepo(t, func(items []hierarchyWireItem) {
		called = true
		if len(items) != 3 {
			t.Fatalf("items=%d, want 3", len(items))
		}
		for i, item := range items {
			if item.Update == nil {
				t.Fatalf("item %d is not an update: %+v", i, item)
			}
		}
		meta, target, old := items[0].Update, items[1].Update, items[2].Update
		if meta.Key["PK"]["S"] != "ACCOUNT#a" || meta.Key["SK"]["S"] != "META" {
			t.Fatalf("wrong META key: %+v", meta.Key)
		}
		requireHierarchyCondition(t, meta, "attribute_exists(PK)", `ownerUserId = "u-old"`)
		update := hierarchyUpdateText(meta)
		for _, want := range []string{`ownerUserId = "u-new"`, "updatedAt", "REMOVE", "initialOwnerEmail", "initialOwnerInvitationPending"} {
			if !strings.Contains(update, want) {
				t.Errorf("META update %q missing %q", update, want)
			}
		}
		if target.Key["PK"]["S"] != "ACCOUNT#a" || target.Key["SK"]["S"] != "MEMBER#u-new" {
			t.Fatalf("wrong target key: %+v", target.Key)
		}
		requireHierarchyCondition(t, target, "attribute_exists(PK)", `userId = "u-new"`, `role <> "owner"`)
		if u := hierarchyUpdateText(target); !strings.Contains(u, `role = "owner"`) {
			t.Errorf("target update %q must set owner", u)
		}
		if old.Key["PK"]["S"] != "ACCOUNT#a" || old.Key["SK"]["S"] != "MEMBER#u-old" {
			t.Fatalf("wrong previous owner key: %+v", old.Key)
		}
		requireHierarchyCondition(t, old, `role = "owner"`)
		if u := hierarchyUpdateText(old); !strings.Contains(u, `role = "SA"`) {
			t.Errorf("previous owner update %q must set SA", u)
		}
	}, 200, `{}`)
	if err := r.TransferAccountOwner(context.Background(), "a", "u-old", "u-new", time.Unix(1700000000, 0)); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("transaction not sent")
	}
}

func TestTransferAccountOwnerConditionFailure(t *testing.T) {
	r := hierarchyWireRepo(t, nil, 400, `{"__type":"TransactionCanceledException","CancellationReasons":[{"Code":"None"},{"Code":"ConditionalCheckFailed"},{"Code":"None"}]}`)
	err := r.TransferAccountOwner(context.Background(), "a", "u-old", "u-new", time.Now())
	if err == nil || !errors.Is(err, ErrConditionFailed) {
		t.Fatalf("error=%v, want ErrConditionFailed", err)
	}
}

func TestTransferAccountOwnerRejectsSelfTransfer(t *testing.T) {
	r := hierarchyWireRepo(t, func([]hierarchyWireItem) { t.Fatal("must not send a transaction") }, 200, `{}`)
	if err := r.TransferAccountOwner(context.Background(), "a", "u-1", "u-1", time.Now()); err == nil {
		t.Fatal("expected error")
	}
}
