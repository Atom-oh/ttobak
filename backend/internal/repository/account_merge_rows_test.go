package repository

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

func mergeRowsJob() *model.AccountMergeJob {
	return &model.AccountMergeJob{SourceAccountID: "src", TargetAccountID: "tgt", RequesterUserID: "owner-1",
		State: model.MergeStateRunning, LeaseOwner: "worker-1"}
}

func mergedMember() *model.AccountMember {
	return &model.AccountMember{AccountID: "tgt", UserID: "u1", Role: model.RoleSA, Email: "u1@example.com"}
}

func TestPutMergedMemberShapeAndFence(t *testing.T) {
	var calls []fenceRequest
	r := fenceRepo(t, &calls, func(fenceRequest) (int, string) { return 200, `{}` })
	if err := r.PutMergedMember(context.Background(), mergeRowsJob(), mergedMember()); err != nil {
		t.Fatal(err)
	}
	items := fenceItems(t, calls[0])
	if len(items) != 1+mergeFenceCheckCount {
		t.Fatalf("want %d items, got %d", 1+mergeFenceCheckCount, len(items))
	}
	put := items[0]
	if c := resolvedCondition(put); !strings.Contains(c, "attribute_not_exists") {
		t.Errorf("put must be create-only: %s", c)
	}
	row := put["Item"].(map[string]any)
	for key, want := range map[string]string{"PK": "ACCOUNT#tgt", "SK": "MEMBER#u1", "GSI1PK": "USER#u1", "GSI1SK": "ACCOUNT#tgt"} {
		if got := row[key].(map[string]any)["S"]; got != want {
			t.Errorf("%s = %v, want %s", key, got, want)
		}
	}
	jobCond := resolvedCondition(items[1])
	for _, frag := range []string{"state", "leaseOwner"} {
		if !strings.Contains(jobCond, frag) {
			t.Errorf("job check %q missing %s", jobCond, frag)
		}
	}
	if c := resolvedCondition(items[2]); !strings.Contains(c, "mergingInto") || !strings.Contains(c, "ownerUserId") {
		t.Errorf("source check wrong: %s", c)
	}
	if c := resolvedCondition(items[3]); !strings.Contains(c, "role") {
		t.Errorf("requester owner check wrong: %s", c)
	}
	if c := resolvedCondition(items[5]); !strings.Contains(c, "mergedInto") {
		t.Errorf("target open check wrong: %s", c)
	}
	// The source META is mergingInto, so the open fence must never be applied to it.
	for _, it := range items {
		key, _ := it["Key"].(map[string]any)
		pk, _ := key["PK"].(map[string]any)
		sk, _ := key["SK"].(map[string]any)
		if pk["S"] == "ACCOUNT#src" && sk["S"] == "META" && strings.Contains(resolvedCondition(it), "attribute_not_exists(mergingInto)") {
			t.Errorf("source META carries the open fence")
		}
	}
}

func TestPutMergedMemberOutcomes(t *testing.T) {
	cases := []struct {
		name string
		idx  int
		mark bool
		want error
		ok   bool
	}{
		{name: "existing target member keeps its role", idx: 0, ok: true},
		{name: "job lease lost", idx: 1, want: ErrMergeFenceLost},
		{name: "source no longer merging", idx: 2, want: ErrMergeFenceLost},
		{name: "requester lost ownership", idx: 3, want: ErrMergeFenceLost},
		{name: "requester left target", idx: 4, want: ErrMergeFenceLost},
		{name: "target started merging", idx: 5, mark: true, want: ErrAccountMerging},
		{name: "target absent", idx: 5, want: ErrMergeFenceLost},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls []fenceRequest
			r := fenceRepo(t, &calls, func(fenceRequest) (int, string) { return 400, cancelledAt(1+mergeFenceCheckCount, tc.idx, tc.mark) })
			err := r.PutMergedMember(context.Background(), mergeRowsJob(), mergedMember())
			if tc.ok {
				if err != nil {
					t.Fatalf("want nil, got %v", err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
		})
	}
}

func TestPutMergedMemberRejectsInvalidInput(t *testing.T) {
	var calls []fenceRequest
	r := fenceRepo(t, &calls, func(fenceRequest) (int, string) { return 200, `{}` })
	bad := mergedMember()
	bad.AccountID = "other"
	if err := r.PutMergedMember(context.Background(), mergeRowsJob(), bad); err == nil {
		t.Error("member for a non-target account must be rejected")
	}
	noLease := mergeRowsJob()
	noLease.LeaseOwner = ""
	if err := r.PutMergedMember(context.Background(), noLease, mergedMember()); err == nil {
		t.Error("job without a lease owner must be rejected")
	}
	if len(calls) != 0 {
		t.Errorf("invalid input must not reach DynamoDB, got %d calls", len(calls))
	}
}
