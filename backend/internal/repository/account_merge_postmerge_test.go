package repository

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

// postMergeJob is a copy of a done job driven by operator after the merge.
func postMergeJob(operator string) *model.AccountMergeJob {
	return &model.AccountMergeJob{SourceAccountID: "src", TargetAccountID: "tgt", RequesterUserID: "owner-1",
		State: model.MergeStateDone, Step: "finish", PostMergeOperator: operator}
}

func postMergeTaskRow() MergeUserRow {
	return MergeUserRow{Kind: MergeUserRowSpecReqTask, PK: "USER#u1", SK: "SRTASK#e1", UserID: "u1", EventID: "e1", Version: 3}
}

func TestPostMergeFenceReplacesTheLease(t *testing.T) {
	ctx := context.Background()
	var calls []fenceRequest
	repo := fenceRepo(t, &calls, func(fenceRequest) (int, string) { return 200, `{}` })
	if err := repo.MoveSpecReqTaskRowForMerge(ctx, postMergeJob("owner-1"), postMergeTaskRow()); err != nil {
		t.Fatal(err)
	}
	items := fenceItems(t, calls[0])
	wantKeys := []string{"USER#u1|SRTASK#e1", "ACCOUNT#src|MERGE#job", "ACCOUNT#src|META", "ACCOUNT#tgt|MEMBER#owner-1", "ACCOUNT#tgt|META"}
	if len(items) != 1+postMergeFenceCheckCount || len(items) != len(wantKeys) {
		t.Fatalf("got %d items", len(items))
	}
	for i, want := range wantKeys {
		if got := itemKey(items[i]); got != want {
			t.Errorf("item %d key %s, want %s", i, got, want)
		}
		if i > 0 && items[i]["Key"] == nil {
			t.Errorf("item %d must be a condition check", i)
		}
	}
	// Nothing writes into the source: only the user row is updated.
	for i, op := range items[1:] {
		if _, isUpdate := op["UpdateExpression"]; isUpdate {
			t.Errorf("fence item %d writes", i+1)
		}
	}
	if cond := resolvedExpression(items[0], "ConditionExpression"); !strings.Contains(cond, `accountId = "src"`) || !strings.Contains(cond, "version = 3") {
		t.Errorf("row must stay pinned to the source and its version: %q", cond)
	}
	if cond := resolvedExpression(items[1], "ConditionExpression"); !strings.Contains(cond, `state = "done"`) ||
		!strings.Contains(cond, `targetAccountId = "tgt"`) || !strings.Contains(cond, `requesterUserId = "owner-1"`) {
		t.Errorf("job check %q", cond)
	}
	if cond := resolvedExpression(items[2], "ConditionExpression"); !strings.Contains(cond, `mergedInto = "tgt"`) ||
		!strings.Contains(cond, "attribute_not_exists (mergingInto)") {
		t.Errorf("source tombstone check %q", cond)
	}
	target := resolvedExpression(items[4], "ConditionExpression")
	if !strings.Contains(target, "attribute_not_exists (mergedInto)") || strings.Contains(target, "ownerUserId") || items[4]["ReturnValuesOnConditionCheckFailure"] != "ALL_OLD" {
		t.Errorf("target check %q", target)
	}

	// The target's owner may run it too; the job then need not name them.
	calls = nil
	if err := repo.MoveSpecReqTaskRowForMerge(ctx, postMergeJob("tgt-owner"), postMergeTaskRow()); err != nil {
		t.Fatal(err)
	}
	items = fenceItems(t, calls[0])
	if itemKey(items[3]) != "ACCOUNT#tgt|MEMBER#tgt-owner" || strings.Contains(resolvedExpression(items[1], "ConditionExpression"), "requesterUserId") ||
		!strings.Contains(resolvedExpression(items[4], "ConditionExpression"), `ownerUserId = "tgt-owner"`) {
		t.Errorf("target-owner fence: member %s job %q target %q", itemKey(items[3]),
			resolvedExpression(items[1], "ConditionExpression"), resolvedExpression(items[4], "ConditionExpression"))
	}
}

func TestPostMergeFenceRefusals(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		failAt int
		marker bool
		want   error
	}{
		"row changed or moved":                {0, false, ErrConditionFailed},
		"wrong target or job not done":        {1, false, ErrMergeFenceLost},
		"source not tombstoned":               {2, false, ErrMergeFenceLost},
		"requester not a target member":       {3, false, ErrMergeFenceLost},
		"operator not job requester or owner": {4, false, ErrMergeFenceLost},
		"target merging":                      {4, true, ErrAccountMerging},
	} {
		t.Run(name, func(t *testing.T) {
			repo := fenceRepo(t, new([]fenceRequest), func(fenceRequest) (int, string) {
				return 400, cancelledAt(1+postMergeFenceCheckCount, tc.failAt, tc.marker)
			})
			err := repo.MoveSpecReqTaskRowForMerge(ctx, postMergeJob("owner-1"), postMergeTaskRow())
			if !errors.Is(err, tc.want) || (tc.want != ErrConditionFailed && errors.Is(err, ErrConditionFailed)) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
	var calls []fenceRequest
	repo := fenceRepo(t, &calls, func(fenceRequest) (int, string) { return 200, `{}` })
	for name, job := range map[string]*model.AccountMergeJob{
		"running job": func() *model.AccountMergeJob {
			j := postMergeJob("owner-1")
			j.State = model.MergeStateRunning
			return j
		}(),
		"failed job": func() *model.AccountMergeJob {
			j := postMergeJob("owner-1")
			j.State = model.MergeStateFailed
			return j
		}(),
		"invalid operator": postMergeJob("a#b"),
	} {
		if err := repo.MoveSpecReqTaskRowForMerge(ctx, job, postMergeTaskRow()); err == nil {
			t.Errorf("%s: post-merge write accepted", name)
		}
	}
	if len(calls) != 0 {
		t.Fatalf("refused post-merge writes reached storage: %d", len(calls))
	}
	// The in-job fence never accepts a post-merge job copy.
	if _, err := repo.mergeFenceChecks(func() *model.AccountMergeJob { j := mergeRowsJob(); j.PostMergeOperator = "owner-1"; return j }()); err == nil {
		t.Fatal("lease fence accepted a post-merge job")
	}
}

func TestPostMergeMeetingMoveIsLinkOnly(t *testing.T) {
	ctx := context.Background()
	var calls []fenceRequest
	r := meetingMoveRepo(t, &calls, mergeMeetingRow("src"), -1, false)
	if err := r.MoveMeetingForAccountMerge(ctx, postMergeJob("owner-1"), "author", "m1", mergeMeetingRef()); err != nil {
		t.Fatal(err)
	}
	items := fenceItems(t, calls[len(calls)-1])
	if len(items) != 1+postMergeFenceCheckCount || itemKey(items[0]) != "USER#author|MEETING#m1" {
		t.Fatalf("link-only post-merge move: %d items, first %s", len(items), itemKey(items[0]))
	}
	for _, op := range items {
		if strings.HasPrefix(itemKey(op), "ACCOUNT#tgt|MEETINGREF#") {
			t.Fatal("a link-only meeting must not get a target ref")
		}
	}
	calls = nil
	published := meetingMoveRepo(t, &calls, mergeMeetingRow("src", `"sharedToAccount":{"BOOL":true}`), -1, false)
	if err := published.MoveMeetingForAccountMerge(ctx, postMergeJob("owner-1"), "author", "m1", mergeMeetingRef()); !errors.Is(err, ErrConditionFailed) {
		t.Fatalf("published post-merge move err = %v", err)
	}
	for _, call := range calls {
		if call.target == "TransactWriteItems" {
			t.Fatal("a published meeting was moved after the merge")
		}
	}
}
