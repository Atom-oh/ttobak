package service

import (
	"context"
	"net/url"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

// Paged account lists hide merge tombstones and flag in-flight merge sources,
// matching the unpaged list, so pickers cannot select a merged source.
func TestDirectAccountListHidesMergedAndFlagsMerging(t *testing.T) {
	t.Setenv(accountDescendantReadsEnv, "1")
	s, r := directAccountListFixture(3)
	r.hierarchy.accounts["direct-000"].MergedInto = "direct-002"
	r.hierarchy.accounts["direct-001"].MergingInto = "direct-002"
	page, err := s.ListAccountsPage(context.Background(), "reader", url.Values{"scope": {"direct"}, "pageSize": {"100"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Accounts) != 2 || page.NextCursor != "" ||
		page.Accounts[0].AccountID != "direct-001" || page.Accounts[0].MergingInto != "direct-002" ||
		page.Accounts[1].AccountID != "direct-002" || page.Accounts[1].MergingInto != "" {
		t.Fatalf("merged source listed or merging flag lost: %+v", page.Accounts)
	}
}

func TestDirectAccountListMergedCandidateKeepsContinuation(t *testing.T) {
	t.Setenv(accountDescendantReadsEnv, "1")
	s, r := directAccountListFixture(3)
	r.hierarchy.accounts["direct-000"].MergedInto = "direct-002"
	q := url.Values{"scope": {"direct"}, "pageSize": {"1"}}
	first, err := s.ListAccountsPage(context.Background(), "reader", q)
	if err != nil || len(first.Accounts) != 0 || first.NextCursor == "" {
		t.Fatalf("merged candidate lost continuation: %+v %v", first, err)
	}
	q.Set("cursor", first.NextCursor)
	second, err := s.ListAccountsPage(context.Background(), "reader", q)
	if err != nil || len(second.Accounts) != 1 || second.Accounts[0].AccountID != "direct-001" {
		t.Fatalf("merged candidate prevented the next membership: %+v %v", second, err)
	}
}

func TestDescendantAccountDiscoveryHidesMergedAndFlagsMerging(t *testing.T) {
	t.Setenv(accountDescendantReadsEnv, "1")
	r := boundedDiscoveryFixture(4)
	r.hierarchy.accounts["child-0001"].MergedInto = "child-0002"
	r.hierarchy.accounts["child-0003"].MergingInto = "child-0002"
	s := newAccountServiceWithRepo(&boundedAccountListRepo{r.hierarchy.mockAccountRepo, r})
	q := url.Values{"pageSize": {"1"}}
	seen := map[string]model.AccountSummary{}
	for n := 0; ; n++ {
		page, err := s.ListAccountsPage(context.Background(), "reader", q)
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range page.Accounts {
			seen[a.AccountID] = a
		}
		if page.NextCursor == "" {
			break
		}
		if n > 40 {
			t.Fatal("discovery did not finish")
		}
		q.Set("cursor", page.NextCursor)
	}
	if _, ok := seen["child-0001"]; ok {
		t.Fatalf("merged descendant listed: %+v", seen)
	}
	if len(seen) != 4 || seen["child-0003"].MergingInto != "child-0002" || seen["child-0000"].MergingInto != "" {
		t.Fatalf("descendant discovery lost rows or the merging flag: %+v", seen)
	}
}
