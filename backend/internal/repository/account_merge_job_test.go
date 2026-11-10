package repository

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
)

func startJob() *model.AccountMergeJob {
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	return &model.AccountMergeJob{SourceAccountID: "src", TargetAccountID: "tgt", RequesterUserID: "owner-1",
		State: model.MergeStateRunning, Step: "members", AcceptUnmoved: true, StartedAt: now, UpdatedAt: now}
}

func startAncestors() []AccountParentLink {
	return []AccountParentLink{{AccountID: "tgt", ParentAccountID: "root"}, {AccountID: "root"}}
}

func mergeJobOpKey(op map[string]any) string {
	key, _ := op["Key"].(map[string]any)
	if key == nil {
		key, _ = op["Item"].(map[string]any)
	}
	pk, _ := key["PK"].(map[string]any)
	sk, _ := key["SK"].(map[string]any)
	return pk["S"].(string) + "|" + sk["S"].(string)
}

func TestStartAccountMergeTransaction(t *testing.T) {
	var calls []fenceRequest
	r := fenceRepo(t, &calls, func(fenceRequest) (int, string) { return 200, `{}` })
	if err := r.StartAccountMerge(context.Background(), startJob(), startAncestors()); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0].target != "TransactWriteItems" {
		t.Fatalf("calls = %+v", calls)
	}
	raw := calls[0].body["TransactItems"].([]any)
	items := fenceItems(t, calls[0])
	wantKeys := []string{"ACCOUNT#src|MERGE#job", "ACCOUNT#src|META", "ACCOUNT#tgt|META", "ACCOUNT#src|MEMBER#owner-1",
		"ACCOUNT#tgt|MEMBER#owner-1", "ACCOUNT#root|META"}
	if len(items) != len(wantKeys) {
		t.Fatalf("items = %d, want %d", len(items), len(wantKeys))
	}
	for i, want := range wantKeys {
		if got := mergeJobOpKey(items[i]); got != want {
			t.Errorf("item %d key %s, want %s", i, got, want)
		}
	}
	if _, ok := raw[0].(map[string]any)["Put"]; !ok {
		t.Fatal("job is not a put")
	}
	job := items[0]["Item"].(map[string]any)
	for field, want := range map[string]string{"state": "running", "step": "members", "targetAccountId": "tgt",
		"requesterUserId": "owner-1", "entityType": model.EntityTypeAccountMergeJob} {
		if got := job[field].(map[string]any)["S"]; got != want {
			t.Errorf("job %s = %v, want %s", field, got, want)
		}
	}
	if _, leased := job["leaseOwner"]; leased {
		t.Error("start must not lease the job")
	}
	if c := resolvedCondition(items[0]); !strings.Contains(c, "attribute_not_exists (PK)") || !strings.Contains(c, "state") {
		t.Errorf("job condition %q", c)
	}
	source := items[1]
	if _, ok := raw[1].(map[string]any)["Update"]; !ok {
		t.Fatal("source META is not an update")
	}
	if u := resolvedCondition(map[string]any{"ConditionExpression": source["UpdateExpression"], "ExpressionAttributeNames": source["ExpressionAttributeNames"]}); !strings.Contains(u, "mergingInto") {
		t.Errorf("source update %q does not set mergingInto", u)
	}
	for _, frag := range []string{"attribute_not_exists (mergingInto)", "attribute_not_exists (mergedInto)", "ownerUserId", "incomingMerges"} {
		if c := resolvedCondition(source); !strings.Contains(c, frag) {
			t.Errorf("source condition %q missing %s", c, frag)
		}
	}
	target := items[2]
	if u, _ := target["UpdateExpression"].(string); !strings.HasPrefix(u, "ADD ") {
		t.Errorf("target update %q must ADD incomingMerges", u)
	}
	for _, frag := range []string{"parentAccountId", "attribute_not_exists (mergingInto)", "attribute_not_exists (mergedInto)"} {
		if c := resolvedCondition(target); !strings.Contains(c, frag) {
			t.Errorf("target condition %q missing %s", c, frag)
		}
	}
	if c := resolvedCondition(items[3]); !strings.Contains(c, "role") {
		t.Errorf("owner member check %q", c)
	}
	if c := resolvedCondition(items[5]); !strings.Contains(c, "parentAccountId") {
		t.Errorf("ancestor check %q", c)
	}
	for _, i := range []int{1, 2} {
		if items[i]["ReturnValuesOnConditionCheckFailure"] != "ALL_OLD" {
			t.Errorf("META item %d must report the stored row", i)
		}
	}
}

func TestResumeAccountMergeTransaction(t *testing.T) {
	var calls []fenceRequest
	r := fenceRepo(t, &calls, func(fenceRequest) (int, string) { return 200, `{}` })
	job := startJob()
	job.Step, job.Cursor = "events", "EVENT#e1"
	if err := r.ResumeAccountMerge(context.Background(), job, startAncestors()); err != nil {
		t.Fatal(err)
	}
	raw := calls[0].body["TransactItems"].([]any)
	items := fenceItems(t, calls[0])
	if c := resolvedCondition(items[0]); strings.Contains(c, "attribute_not_exists") || !strings.Contains(c, "targetAccountId") {
		t.Errorf("resume job condition %q must require the failed job for this target", c)
	}
	for i := 1; i <= 2; i++ {
		if _, ok := raw[i].(map[string]any)["ConditionCheck"]; !ok {
			t.Errorf("resume item %d must be a check (no second mergingInto or incomingMerges write)", i)
		}
	}
	if c := resolvedCondition(items[1]); !strings.Contains(c, "mergingInto =") || strings.Contains(c, "attribute_not_exists (mergingInto)") {
		t.Errorf("resume source condition %q", c)
	}
}

func TestStartAccountMergeOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name string
		idx  int
		want error
	}{
		{"job running or done", 0, ErrMergeJobExists},
		{"source changed", 1, ErrConditionFailed},
		{"target changed", 2, ErrConditionFailed},
		{"requester lost ownership", 3, ErrConditionFailed},
		{"requester left target", 4, ErrConditionFailed},
		{"hierarchy changed", 5, ErrConditionFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls []fenceRequest
			r := fenceRepo(t, &calls, func(fenceRequest) (int, string) { return 400, cancelledAt(6, tc.idx, false) })
			if err := r.StartAccountMerge(context.Background(), startJob(), startAncestors()); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestStartAccountMergeRejectsInvalidInput(t *testing.T) {
	var calls []fenceRequest
	r := fenceRepo(t, &calls, func(fenceRequest) (int, string) { return 200, `{}` })
	for name, mutate := range map[string]func(*model.AccountMergeJob, *[]AccountParentLink){
		"leased":        func(j *model.AccountMergeJob, _ *[]AccountParentLink) { j.LeaseOwner = "w" },
		"later step":    func(j *model.AccountMergeJob, _ *[]AccountParentLink) { j.Step = "events" },
		"unknown step":  func(j *model.AccountMergeJob, _ *[]AccountParentLink) { j.Step = "nope" },
		"same accounts": func(j *model.AccountMergeJob, _ *[]AccountParentLink) { j.TargetAccountID = "src" },
		"source in chain": func(_ *model.AccountMergeJob, a *[]AccountParentLink) {
			*a = []AccountParentLink{{"tgt", "src"}, {"src", ""}}
		},
		"chain wrong head": func(_ *model.AccountMergeJob, a *[]AccountParentLink) { *a = []AccountParentLink{{"root", ""}} },
		"incomplete chain": func(_ *model.AccountMergeJob, a *[]AccountParentLink) { *a = []AccountParentLink{{"tgt", "root"}} },
	} {
		job, ancestors := startJob(), startAncestors()
		mutate(job, &ancestors)
		if err := r.StartAccountMerge(context.Background(), job, ancestors); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if len(calls) != 0 {
		t.Fatalf("invalid start reached DynamoDB: %d calls", len(calls))
	}
}

func TestClaimMergeLease(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	var calls []fenceRequest
	r := fenceRepo(t, &calls, func(fenceRequest) (int, string) {
		return 200, `{"Attributes":{"sourceAccountId":{"S":"src"},"state":{"S":"running"},"leaseOwner":{"S":"req-2"},"leaseUntil":{"N":"1800000300"}}}`
	})
	job, err := r.ClaimMergeLease(context.Background(), "src", "req-2", now, 5*time.Minute)
	if err != nil || job.LeaseOwner != "req-2" || job.LeaseUntil != 1_800_000_300 {
		t.Fatalf("job=%+v err=%v", job, err)
	}
	body := calls[0].body
	if calls[0].target != "UpdateItem" || body["ReturnValues"] != "ALL_NEW" {
		t.Fatalf("not an ALL_NEW update: %+v", calls[0])
	}
	cond := resolvedCondition(body)
	for _, frag := range []string{"state", "leaseUntil", "leaseOwner"} {
		if !strings.Contains(cond, frag) {
			t.Errorf("claim condition %q missing %s", cond, frag)
		}
	}
	values := body["ExpressionAttributeValues"].(map[string]any)
	var seconds []string
	for _, v := range values {
		if n, ok := v.(map[string]any)["N"].(string); ok {
			seconds = append(seconds, n)
		}
	}
	if strings.Join(seconds, ",") != "1800000000,1800000300" && strings.Join(seconds, ",") != "1800000300,1800000000" {
		t.Errorf("lease must be Unix seconds (now and now+ttl), got %v", seconds)
	}

	held := fenceRepo(t, &calls, func(fenceRequest) (int, string) {
		return 400, `{"__type":"com.amazonaws.dynamodb.v20120810#ConditionalCheckFailedException","message":"failed"}`
	})
	if _, err := held.ClaimMergeLease(context.Background(), "src", "req-3", now, 5*time.Minute); !errors.Is(err, ErrMergeLeaseHeld) {
		t.Fatalf("err = %v, want ErrMergeLeaseHeld", err)
	}
	if _, err := r.ClaimMergeLease(context.Background(), "src", "", now, time.Minute); err == nil {
		t.Fatal("accepted empty owner")
	}
}

func leasedJob() *model.AccountMergeJob {
	j := startJob()
	j.LeaseOwner, j.LeaseUntil = "req-1", 1_800_000_300
	return j
}

func TestSaveMergeProgress(t *testing.T) {
	var calls []fenceRequest
	r := fenceRepo(t, &calls, func(fenceRequest) (int, string) { return 200, `{}` })
	job := leasedJob()
	if err := r.SaveMergeProgress(context.Background(), job, "members", "u5", map[string]int{"members.moved": 2}); err != nil {
		t.Fatal(err)
	}
	cond := resolvedCondition(calls[0].body)
	for _, frag := range []string{"state", "leaseOwner", "step"} {
		if !strings.Contains(cond, frag) {
			t.Errorf("progress condition %q missing %s", cond, frag)
		}
	}
	if job.Cursor != "u5" || job.Counts["members.moved"] != 2 {
		t.Fatalf("job not updated: %+v", job)
	}
	if err := r.SaveMergeProgress(context.Background(), job, "children", "", map[string]int{"members.moved": 2}); err != nil {
		t.Fatal(err)
	}
	if job.Step != "children" || job.Cursor != "" {
		t.Fatalf("step change kept cursor: %+v", job)
	}
	update, _ := calls[1].body["UpdateExpression"].(string)
	if !strings.Contains(update, "REMOVE") {
		t.Errorf("step change must remove the cursor: %s", update)
	}

	n := len(calls)
	for name, call := range map[string]func() error{
		"cursor carried into a new step": func() error { return r.SaveMergeProgress(context.Background(), job, "projects", "x", nil) },
		"step rewound":                   func() error { return r.SaveMergeProgress(context.Background(), job, "members", "", nil) },
		"unknown step":                   func() error { return r.SaveMergeProgress(context.Background(), job, "nope", "", nil) },
		"negative count": func() error {
			return r.SaveMergeProgress(context.Background(), job, "children", "", map[string]int{"x": -1})
		},
	} {
		if err := call(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if len(calls) != n {
		t.Fatal("invalid progress reached DynamoDB")
	}

	lost := fenceRepo(t, &calls, func(fenceRequest) (int, string) {
		return 400, `{"__type":"com.amazonaws.dynamodb.v20120810#ConditionalCheckFailedException","message":"failed"}`
	})
	if err := lost.SaveMergeProgress(context.Background(), leasedJob(), "members", "u9", nil); !errors.Is(err, ErrMergeFenceLost) {
		t.Fatalf("err = %v, want ErrMergeFenceLost", err)
	}
}

func TestFailMergeJob(t *testing.T) {
	var calls []fenceRequest
	r := fenceRepo(t, &calls, func(fenceRequest) (int, string) { return 200, `{}` })
	job := leasedJob()
	if err := r.FailMergeJob(context.Background(), job, "REQUESTER_REVOKED"); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0].target != "UpdateItem" {
		t.Fatalf("fail must touch only the job row: %+v", calls)
	}
	if got := mergeJobOpKey(calls[0].body); got != "ACCOUNT#src|MERGE#job" {
		t.Fatalf("fail key %s", got)
	}
	update, _ := calls[0].body["UpdateExpression"].(string)
	if !strings.Contains(update, "REMOVE") {
		t.Errorf("fail must release the lease: %s", update)
	}
	if !strings.Contains(resolvedCondition(calls[0].body), "leaseOwner") {
		t.Error("fail must be lease-conditioned")
	}
	if job.State != model.MergeStateFailed || job.ErrorCode != "REQUESTER_REVOKED" || job.LeaseOwner != "" {
		t.Fatalf("job = %+v", job)
	}
	if err := r.FailMergeJob(context.Background(), leasedJob(), "bad code"); err == nil {
		t.Fatal("accepted malformed code")
	}
}

func TestFinishAccountMerge(t *testing.T) {
	var calls []fenceRequest
	r := fenceRepo(t, &calls, func(fenceRequest) (int, string) { return 200, `{}` })
	job := leasedJob()
	job.Step = "finish"
	if err := r.FinishAccountMerge(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	items := fenceItems(t, calls[0])
	wantKeys := []string{"ACCOUNT#src|MERGE#job", "ACCOUNT#src|META", "ACCOUNT#src|MEMBER#owner-1", "ACCOUNT#tgt|MEMBER#owner-1", "ACCOUNT#tgt|META"}
	if len(items) != len(wantKeys) {
		t.Fatalf("items = %d", len(items))
	}
	for i, want := range wantKeys {
		if got := mergeJobOpKey(items[i]); got != want {
			t.Errorf("item %d key %s, want %s", i, got, want)
		}
	}
	source := items[1]
	names := source["ExpressionAttributeNames"].(map[string]any)
	joined := ""
	for _, n := range names {
		joined += n.(string) + ","
	}
	for _, field := range []string{"mergingInto", "mergedInto", "mergedAt"} {
		if !strings.Contains(joined, field) {
			t.Errorf("source update missing %s", field)
		}
	}
	if u, _ := items[4]["UpdateExpression"].(string); !strings.HasPrefix(u, "ADD ") {
		t.Errorf("target must ADD -1 incomingMerges: %s", u)
	}
	if !strings.Contains(resolvedCondition(items[4]), "incomingMerges >") {
		t.Error("decrement must keep incomingMerges non-negative")
	}
	if job.State != model.MergeStateDone || job.FinishedAt == nil || job.LeaseOwner != "" {
		t.Fatalf("job = %+v", job)
	}

	for _, tc := range []struct {
		idx  int
		want error
	}{{0, ErrMergeFenceLost}, {1, ErrMergeFenceLost}, {2, ErrMergeFenceLost}, {3, ErrMergeFenceLost}, {4, ErrConditionFailed}} {
		failing := fenceRepo(t, &calls, func(fenceRequest) (int, string) { return 400, cancelledAt(5, tc.idx, false) })
		j := leasedJob()
		j.Step = "finish"
		if err := failing.FinishAccountMerge(context.Background(), j); !errors.Is(err, tc.want) {
			t.Errorf("idx %d: err = %v, want %v", tc.idx, err, tc.want)
		}
	}
	early := leasedJob()
	if err := r.FinishAccountMerge(context.Background(), early); err == nil {
		t.Fatal("finished a job before its finish step")
	}
}

func TestGetAccountMergeJob(t *testing.T) {
	var calls []fenceRequest
	r := fenceRepo(t, &calls, func(fenceRequest) (int, string) {
		return 200, `{"Item":{"PK":{"S":"ACCOUNT#src"},"SK":{"S":"MERGE#job"},"state":{"S":"failed"},"step":{"S":"events"},"errorCode":{"S":"X"}}}`
	})
	job, err := r.GetAccountMergeJob(context.Background(), "src")
	if err != nil || job == nil || job.State != "failed" || job.Step != "events" {
		t.Fatalf("job=%+v err=%v", job, err)
	}
	if calls[0].body["ConsistentRead"] != true {
		t.Fatal("job read must be strongly consistent")
	}
	empty := fenceRepo(t, &calls, func(fenceRequest) (int, string) { return 200, `{}` })
	if job, err := empty.GetAccountMergeJob(context.Background(), "src"); err != nil || job != nil {
		t.Fatalf("absent job: %+v %v", job, err)
	}
}
