package repository

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

func TestRecordTermCorrectionCountsDistinctMeetings(t *testing.T) {
	ctx := context.Background()
	sk := model.TermsCorrectionSK(acctKey, "gay culture")
	if sk != "TERMS_CORRECTION#"+acctKey+"#gay culture" {
		t.Fatalf("counter SK %q", sk)
	}

	// Personal/Common: one UpdateItem that adds the meeting to a capped set
	// and refreshes the TTL; the count comes back in the same response.
	repo, calls := dictWire(t, func(int, string, map[string]any) (int, string) {
		return 200, `{"Attributes":{"meetingIds":{"SS":["m1","m2"]}}}`
	})
	n, err := repo.RecordTermCorrection(ctx, "USER#u1", acctKey, "gay culture", "m2", 1800000000, nil)
	if err != nil || n != 2 {
		t.Fatalf("count=%d err=%v", n, err)
	}
	if len(*calls) != 1 || (*calls)[0].op != "UpdateItem" {
		t.Fatalf("one update: %+v", *calls)
	}
	body := (*calls)[0].body
	requireAll(t, "update", resolved(body, "UpdateExpression"), `ADD meetingIds {"SS":["m2"]}`, `pendingShareExpiresAt = {"N":"1800000000"}`)
	requireAll(t, "condition", resolved(body, "ConditionExpression"), "attribute_not_exists (meetingIds)", "size (meetingIds)", `{"N":"10"}`, `contains (meetingIds, {"S":"m2"})`)
	if got := body["Key"].(map[string]any)["SK"].(map[string]any)["S"]; got != sk {
		t.Fatalf("key SK %v", got)
	}

	// A full row stops growing but still reports the cap.
	repo, _ = dictWire(t, func(int, string, map[string]any) (int, string) { return 400, conditionalFailed })
	if n, err := repo.RecordTermCorrection(ctx, "USER#u1", acctKey, "gay culture", "m11", 1800000000, nil); err != nil || n != model.MaxTermCorrectionMeetings {
		t.Fatalf("full row: %d %v", n, err)
	}

	// Account: the update runs with the membership ConditionCheck, then a
	// consistent read returns the count.
	repo, calls = dictWire(t, func(_ int, op string, _ map[string]any) (int, string) {
		if op == "GetItem" {
			return 200, `{"Item":{"meetingIds":{"SS":["m1"]}}}`
		}
		return 200, `{}`
	})
	member := &MembershipCheck{AccountID: "a1", UserID: "u1"}
	if n, err := repo.RecordTermCorrection(ctx, "ACCOUNT#a1", acctKey, "gay culture", "m1", 1800000000, member); err != nil || n != 1 {
		t.Fatalf("account: %d %v", n, err)
	}
	items := txItems(t, (*calls)[0])
	if len(items) != 2 || items[1]["ConditionCheck"] == nil || (*calls)[1].op != "GetItem" || (*calls)[1].body["ConsistentRead"] != true {
		t.Fatalf("account write must be membership-checked: %+v", *calls)
	}

	// A lost membership is reported as such; a missing check is refused.
	repo, _ = dictWire(t, func(int, string, map[string]any) (int, string) {
		return 400, cancelled("None", "ConditionalCheckFailed")
	})
	if _, err := repo.RecordTermCorrection(ctx, "ACCOUNT#a1", acctKey, "gay culture", "m1", 1800000000, member); !errors.Is(err, ErrNotAccountMember) {
		t.Fatalf("lost membership: %v", err)
	}
	repo, calls = dictWire(t, nil)
	if _, err := repo.RecordTermCorrection(ctx, "ACCOUNT#a1", acctKey, "gay culture", "m1", 1800000000, nil); !errors.Is(err, ErrInvalidInput) || len(*calls) != 0 {
		t.Fatalf("unchecked account write: %v", err)
	}
	if _, err := repo.RecordTermCorrection(ctx, "USER#u1", acctKey, "", "m1", 1800000000, nil); !errors.Is(err, ErrInvalidInput) || len(*calls) != 0 {
		t.Fatalf("empty heard key: %v", err)
	}
}

func TestAppendHeardAsIsNotAUserEdit(t *testing.T) {
	ctx := context.Background()
	now := "2026-10-26T09:00:00.000000Z"
	repo, calls := dictWire(t, nil)
	if err := repo.AppendHeardAs(ctx, "USER#u1", acctKey, "gay culture", now, 3, nil); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 || (*calls)[0].op != "UpdateItem" {
		t.Fatalf("one guarded update: %+v", *calls)
	}
	body := (*calls)[0].body
	update := resolved(body, "UpdateExpression")
	requireAll(t, "update", update, "list_append", "if_not_exists(heardAs", `{"L":[{"S":"gay culture"}]}`, `updatedAt = {"S":"`+now+`"}`, "version = version +")
	for _, field := range []string{"userEditedAt", "updatedBy", "origin"} {
		if strings.Contains(update, field) {
			t.Fatalf("automatic append wrote %s: %s", field, update)
		}
	}
	requireAll(t, "condition", resolved(body, "ConditionExpression"), `version = {"N":"3"}`, `state = {"S":"active"}`)

	// A stale version is a term conflict.
	repo, _ = dictWire(t, func(int, string, map[string]any) (int, string) { return 400, conditionalFailed })
	if err := repo.AppendHeardAs(ctx, "USER#u1", acctKey, "gay culture", now, 3, nil); !errors.Is(err, ErrDictTermConflict) {
		t.Fatalf("stale version: %v", err)
	}

	// Account: membership ConditionCheck in the same transaction.
	repo, calls = dictWire(t, nil)
	if err := repo.AppendHeardAs(ctx, "ACCOUNT#a1", acctKey, "gay culture", now, 3, &MembershipCheck{AccountID: "a1", UserID: "u1"}); err != nil {
		t.Fatal(err)
	}
	if items := txItems(t, (*calls)[0]); len(items) != 2 || items[1]["ConditionCheck"] == nil {
		t.Fatalf("account append must be membership-checked: %+v", items)
	}
	repo, calls = dictWire(t, nil)
	if err := repo.AppendHeardAs(ctx, "ACCOUNT#a1", acctKey, "gay culture", now, 3, nil); !errors.Is(err, ErrInvalidInput) || len(*calls) != 0 {
		t.Fatalf("unchecked account append: %v", err)
	}
}
