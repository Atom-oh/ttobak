package repository

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
)

func chainOf(n int) []AccountParentLink {
	links := make([]AccountParentLink, n)
	for i := range links {
		links[i] = AccountParentLink{AccountID: fmt.Sprintf("anc-%d", i)}
		if i+1 < n {
			links[i].ParentAccountID = fmt.Sprintf("anc-%d", i+1)
		}
	}
	links[0].AccountID = "tgt"
	return links
}

func itemKey(op map[string]any) string {
	key, _ := op["Key"].(map[string]any)
	if key == nil {
		item, _ := op["Item"].(map[string]any)
		key = item
	}
	pk, _ := key["PK"].(map[string]any)
	sk, _ := key["SK"].(map[string]any)
	return fmt.Sprint(pk["S"], "|", sk["S"])
}

func TestReparentAccountForMergeTransaction(t *testing.T) {
	var calls []fenceRequest
	r := fenceRepo(t, &calls, func(fenceRequest) (int, string) { return 200, `{}` })
	chain := chainOf(3)
	if err := r.ReparentAccountForMerge(context.Background(), mergeRowsJob(), "child", chain); err != nil {
		t.Fatal(err)
	}
	items := fenceItems(t, calls[0])
	// child update + 5 fence + delete + put + 2 ancestors above the target
	if len(items) != 1+mergeFenceCheckCount+2+2 {
		t.Fatalf("got %d items", len(items))
	}
	seen := map[string]bool{}
	for _, it := range items {
		k := itemKey(it)
		if seen[k] {
			t.Errorf("item %s touched twice", k)
		}
		seen[k] = true
	}
	if c := resolvedCondition(items[0]); !strings.Contains(c, "parentAccountId = ") || !strings.Contains(c, "mergingInto") {
		t.Errorf("child condition %q", c)
	}
	if itemKey(items[0]) != "ACCOUNT#child|META" {
		t.Errorf("mover item %s", itemKey(items[0]))
	}
	if c := resolvedCondition(items[5]); !strings.Contains(c, "mergedInto") || !strings.Contains(c, "parentAccountId") {
		t.Errorf("target check must carry open and parent: %s", c)
	}
	if itemKey(items[6]) != "ACCOUNT#src|CHILDREF#child" || itemKey(items[7]) != "ACCOUNT#tgt|CHILDREF#child" {
		t.Errorf("child refs %s %s", itemKey(items[6]), itemKey(items[7]))
	}
	if c := resolvedCondition(items[8]); !strings.Contains(c, "parentAccountId") || strings.Contains(c, "mergingInto") {
		t.Errorf("ancestor check %q", c)
	}
}

func TestReparentAccountForMergeFullChain(t *testing.T) {
	var calls []fenceRequest
	r := fenceRepo(t, &calls, func(fenceRequest) (int, string) { return 200, `{}` })
	if err := r.ReparentAccountForMerge(context.Background(), mergeRowsJob(), "child", chainOf(AccountHierarchyMaxAncestors)); err != nil {
		t.Fatal(err)
	}
	if n := len(fenceItems(t, calls[0])); n != 71 {
		t.Fatalf("64 ancestors: want 71 items, got %d", n)
	}
	if err := r.ReparentAccountForMerge(context.Background(), mergeRowsJob(), "child", chainOf(AccountHierarchyMaxAncestors+1)); err == nil {
		t.Error("65 ancestors must be rejected")
	}
	cyc := chainOf(2)
	cyc[1].ParentAccountID = "child"
	if err := r.ReparentAccountForMerge(context.Background(), mergeRowsJob(), "child", cyc); err == nil {
		t.Error("target below the child must be rejected")
	}
	if len(calls) != 1 {
		t.Errorf("invalid input reached DynamoDB: %d calls", len(calls))
	}
}

func TestReparentAccountForMergeOutcomes(t *testing.T) {
	n := 1 + mergeFenceCheckCount + 2
	chain := chainOf(1)
	for name, tc := range map[string]struct {
		idx    int
		marker bool
		parent string
		want   error
		ok     bool
	}{
		"already under target":   {idx: 0, parent: "tgt", ok: true},
		"child moved elsewhere":  {idx: 0, parent: "other", want: ErrConditionFailed},
		"lease lost":             {idx: 1, want: ErrMergeFenceLost},
		"target started merging": {idx: 5, marker: true, want: ErrAccountMerging},
	} {
		t.Run(name, func(t *testing.T) {
			var calls []fenceRequest
			r := fenceRepo(t, &calls, func(c fenceRequest) (int, string) {
				if c.target == "GetItem" {
					return 200, `{"Item":{"parentAccountId":{"S":"` + tc.parent + `"}}}`
				}
				return 400, cancelledAt(n, tc.idx, tc.marker)
			})
			err := r.ReparentAccountForMerge(context.Background(), mergeRowsJob(), "child", chain)
			if tc.ok && err != nil || !tc.ok && !errors.Is(err, tc.want) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func linkRepo(t *testing.T, calls *[]fenceRequest, ids string, failTx map[int]int) *DynamoDBRepository {
	tx := 0
	return fenceRepo(t, calls, func(c fenceRequest) (int, string) {
		switch c.target {
		case "GetItem":
			key := c.body["Key"].(map[string]any)
			if sk := key["SK"].(map[string]any)["S"].(string); strings.HasSuffix(sk, "REF#p1") || strings.HasSuffix(sk, "REF#r1") {
				return 200, `{"Item":{"PK":{"S":"ACCOUNT#src"},"SK":{"S":"` + sk + `"},"ownerUserId":{"S":"o1"},"name":{"S":"Proj"},"topic":{"S":"Topic"}}}`
			}
			return 200, `{"Item":{"accountIds":{"SS":[` + ids + `]},"ownerUserId":{"S":"o1"},"name":{"S":"Proj"},"topic":{"S":"Topic"}}}`
		}
		tx++
		if idx, ok := failTx[tx]; ok {
			return 400, cancelledAt(2+mergeFenceCheckCount-1, idx, false)
		}
		return 200, `{}`
	})
}

func TestMoveProjectLinkForMerge_AlreadyLinkedToB(t *testing.T) {
	var calls []fenceRequest
	r := linkRepo(t, &calls, `"src","tgt"`, nil)
	if err := r.MoveProjectAccountLinkForMerge(context.Background(), mergeRowsJob(), "p1"); err != nil {
		t.Fatal(err)
	}
	var txs [][]map[string]any
	for _, c := range calls {
		if c.target == "TransactWriteItems" {
			txs = append(txs, fenceItems(t, c))
		}
	}
	if len(txs) != 2 {
		t.Fatalf("want 2 transactions, got %d", len(txs))
	}
	add, del := txs[0], txs[1]
	if len(add) != 2+mergeFenceCheckCount || len(del) != 2+mergeFenceCheckCount {
		t.Fatalf("sizes %d %d", len(add), len(del))
	}
	if ue := add[0]["UpdateExpression"].(string); !strings.HasPrefix(ue, "ADD ") {
		t.Errorf("first update %q", ue)
	}
	if ue := del[0]["UpdateExpression"].(string); !strings.HasPrefix(ue, "DELETE ") {
		t.Errorf("second update %q", ue)
	}
	if c := resolvedCondition(del[0]); !strings.Contains(c, "contains") || strings.Count(c, "contains") != 2 {
		t.Errorf("delete must require both links: %s", c)
	}
	if itemKey(add[0]) != "PROJECT#p1|CONFIG" || itemKey(del[0]) != "PROJECT#p1|CONFIG" {
		t.Errorf("config keys %s %s", itemKey(add[0]), itemKey(del[0]))
	}
	last := len(add) - 1
	if itemKey(add[last]) != "ACCOUNT#tgt|PROJECTREF#p1" || itemKey(del[last]) != "ACCOUNT#src|PROJECTREF#p1" {
		t.Errorf("refs %s %s", itemKey(add[last]), itemKey(del[last]))
	}
	item := add[last]["Item"].(map[string]any)
	for k, want := range map[string]string{"accountId": "tgt", "projectId": "p1", "ownerUserId": "o1", "name": "Proj", "entityType": "PROJECT_REF"} {
		if got := item[k].(map[string]any)["S"]; got != want {
			t.Errorf("ref %s = %v, want %s", k, got, want)
		}
	}
}

func TestMoveResearchLinkForMerge_Idempotent(t *testing.T) {
	var calls []fenceRequest
	r := linkRepo(t, &calls, `"tgt"`, nil)
	if err := r.MoveResearchAccountLinkForMerge(context.Background(), mergeRowsJob(), "r1"); err != nil {
		t.Fatal(err)
	}
	for _, c := range calls {
		if c.target == "TransactWriteItems" {
			t.Fatal("source no longer linked: no write expected")
		}
	}
	// Source still linked: both steps run and the ref is built from the source ref.
	calls = nil
	r = linkRepo(t, &calls, `"src"`, nil)
	if err := r.MoveResearchAccountLinkForMerge(context.Background(), mergeRowsJob(), "r1"); err != nil {
		t.Fatal(err)
	}
	writes := 0
	for _, c := range calls {
		if c.target == "TransactWriteItems" {
			writes++
		}
	}
	if writes != 2 {
		t.Errorf("want 2 writes, got %d", writes)
	}
}

func TestMoveLinkForMergeOutcomes(t *testing.T) {
	ctx := context.Background()
	var calls []fenceRequest
	// Step 1 mover condition fails: the link moved away meanwhile.
	r := linkRepo(t, &calls, `"src"`, map[int]int{1: 0})
	if err := r.MoveProjectAccountLinkForMerge(ctx, mergeRowsJob(), "p1"); err != nil {
		t.Errorf("step 1 condition failure is already-moved: %v", err)
	}
	// Step 2 mover condition fails: the target link vanished; retry.
	r = linkRepo(t, &calls, `"src"`, map[int]int{2: 0})
	if err := r.MoveProjectAccountLinkForMerge(ctx, mergeRowsJob(), "p1"); !errors.Is(err, ErrConditionFailed) {
		t.Errorf("step 2 want ErrConditionFailed, got %v", err)
	}
	// Lost fence on step 1 stops before step 2.
	calls = nil
	r = linkRepo(t, &calls, `"src"`, map[int]int{1: 2})
	if err := r.MoveProjectAccountLinkForMerge(ctx, mergeRowsJob(), "p1"); !errors.Is(err, ErrMergeFenceLost) {
		t.Errorf("want ErrMergeFenceLost, got %v", err)
	}
	writes := 0
	for _, c := range calls {
		if c.target == "TransactWriteItems" {
			writes++
		}
	}
	if writes != 1 {
		t.Errorf("fence loss must stop after one write, got %d", writes)
	}
}

func TestMergeAccountMetadataUnion(t *testing.T) {
	into := &model.Account{AccountID: "tgt", Name: "LGE Corp", Aliases: []string{"LG Electronics"}, Domains: []string{"lge.com"}}
	src := &model.Account{Name: "LGE", Aliases: []string{"lg", "LGE ", "엘지전자", "LG"}, Domains: []string{"LGE.com ", "lg.com"}}
	got := MergedAccountAliases(into, src)
	want := []string{"LG Electronics", "LGE", "lg", "엘지전자"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("aliases %v, want %v", got, want)
	}
	if d := MergedAccountDomains(into, src); !reflect.DeepEqual(d, []string{"lge.com", "lg.com"}) {
		t.Errorf("domains %v", d)
	}
}

func TestMergeAccountMetadataWrite(t *testing.T) {
	observed := time.Unix(1700000000, 0).UTC()
	into := &model.Account{AccountID: "tgt", UpdatedAt: observed}
	var calls []fenceRequest
	r := fenceRepo(t, &calls, func(fenceRequest) (int, string) { return 200, `{}` })
	if err := r.MergeAccountMetadata(context.Background(), mergeRowsJob(), into, []string{"a", "엘지"}, nil); err != nil {
		t.Fatal(err)
	}
	items := fenceItems(t, calls[0])
	if len(items) != mergeFenceCheckCount {
		t.Fatalf("want %d items, got %d", mergeFenceCheckCount, len(items))
	}
	if c := resolvedCondition(items[0]); !strings.Contains(c, "updatedAt") || !strings.Contains(c, "mergedInto") {
		t.Errorf("CAS condition %q", c)
	}
	seen := map[string]bool{}
	for _, it := range items {
		if k := itemKey(it); seen[k] {
			t.Errorf("%s touched twice", k)
		} else {
			seen[k] = true
		}
	}
	if err := r.MergeAccountMetadata(context.Background(), mergeRowsJob(), &model.Account{AccountID: "other"}, nil, nil); err == nil {
		t.Error("non-target account must be rejected")
	}
	for name, tc := range map[string]struct {
		idx    int
		marker bool
		want   error
	}{
		"stale observation": {idx: 0, want: ErrConditionFailed},
		"lease lost":        {idx: 1, want: ErrMergeFenceLost},
		"target merging":    {idx: 0, marker: true, want: ErrAccountMerging},
	} {
		r := fenceRepo(t, &calls, func(fenceRequest) (int, string) { return 400, cancelledAt(mergeFenceCheckCount, tc.idx, tc.marker) })
		if err := r.MergeAccountMetadata(context.Background(), mergeRowsJob(), into, []string{"a"}, nil); !errors.Is(err, tc.want) {
			t.Errorf("%s: got %v", name, err)
		}
	}
}
