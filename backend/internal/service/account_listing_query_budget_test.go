package service

import (
	"context"
	"fmt"
	"net/url"
	"reflect"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

// productionShapedAccountTree mirrors the production tree behind the
// GET /api/accounts regression: eleven roots and nineteen children, all held
// directly (each child is listed twice: under its root and as its own root),
// plus three inherited grandchildren.
func productionShapedAccountTree() *countingAccountListRepo {
	r := boundedDiscoveryFixture(0)
	add := func(id, parent string, direct bool) {
		r.hierarchy.accounts[id] = &model.Account{PK: "ACCOUNT#" + id, SK: "META", AccountID: id, Name: id,
			ParentAccountID: parent, EntityType: model.EntityTypeAccount}
		if direct {
			r.hierarchy.members[memberKey(id, "reader")] = batchMember(id)
			r.accountIndex = append(r.accountIndex, id)
		}
	}
	for i := 0; i < 11; i++ {
		add(fmt.Sprintf("r%02d", i), "", true)
	}
	for i := 0; i < 19; i++ {
		add(fmt.Sprintf("r%02d-c%02d", i%5, i), fmt.Sprintf("r%02d", i%5), true)
	}
	for i := 0; i < 3; i++ {
		add(fmt.Sprintf("r00-c00-g%d", i), "r00-c00", false)
	}
	return &countingAccountListRepo{boundedAccountListRepo: &boundedAccountListRepo{r.hierarchy.mockAccountRepo, r}}
}

type accountListingCost struct{ queries, getItems, batches int }

func listAccountsWithCost(t *testing.T, c *countingAccountListRepo, b *batchAccountListRepo, q url.Values) ([]*AccountListPage, []accountListingCost) {
	t.Helper()
	var s *AccountService
	if b != nil {
		s = newAccountServiceWithRepo(b)
	} else {
		s = newAccountServiceWithRepo(c)
	}
	d := c.boundedAccountListRepo.discovery
	pages, costs := []*AccountListPage{}, []accountListingCost{}
	for n := 0; n < 200; n++ {
		queries, getItems, batches := d.queries+d.childQueries, c.getItems(), 0
		if b != nil {
			batches = b.calls
		}
		page, err := s.ListAccountsPage(context.Background(), "reader", q)
		if err != nil {
			t.Fatal(err)
		}
		cost := accountListingCost{d.queries + d.childQueries - queries, c.getItems() - getItems, -batches}
		if b != nil {
			cost.batches += b.calls
		}
		pages, costs = append(pages, page), append(costs, cost)
		if page.NextCursor == "" {
			return pages, costs
		}
		q = url.Values{"pageSize": q["pageSize"], "scope": q["scope"], "cursor": {page.NextCursor}}
	}
	t.Fatal("listing never completed")
	return nil, nil
}

// Per-request DynamoDB work for the frontend's GET /api/accounts calls must
// stay at or below the pre-#646 Query count and well below its round trips.
// Base numbers were measured on 216acc7a with this fixture.
func TestAccountListingPerRequestQueryBudget(t *testing.T) {
	t.Setenv(accountDescendantReadsEnv, "1")
	cases := []struct {
		scope                    string
		baseQueries, baseGetItem []int
	}{
		{"all", []int{112}, []int{204}},
		{"direct", []int{1, 1}, []int{100, 26}},
	}
	for _, tc := range cases {
		t.Run(tc.scope, func(t *testing.T) {
			q := url.Values{"pageSize": {"100"}, "scope": {tc.scope}}
			perItem := productionShapedAccountTree()
			wantPages, baseCosts := listAccountsWithCost(t, perItem, nil, q)
			c := productionShapedAccountTree()
			gotPages, costs := listAccountsWithCost(t, c, &batchAccountListRepo{countingAccountListRepo: c}, q)
			if !reflect.DeepEqual(gotPages, wantPages) {
				t.Fatalf("batched listing differs from per-item listing")
			}
			if len(costs) != len(tc.baseQueries) {
				t.Fatalf("pages = %d, want %d", len(costs), len(tc.baseQueries))
			}
			for i, cost := range costs {
				if baseCosts[i].queries != tc.baseQueries[i] || baseCosts[i].getItems != tc.baseGetItem[i] {
					t.Fatalf("page %d per-item cost %+v no longer matches base %d/%d", i, baseCosts[i], tc.baseQueries[i], tc.baseGetItem[i])
				}
				trips := cost.queries + cost.getItems + cost.batches
				t.Logf("page %d: base Query %d + GetItem %d -> Query %d + GetItem %d + BatchGetItem %d",
					i, tc.baseQueries[i], tc.baseGetItem[i], cost.queries, cost.getItems, cost.batches)
				if cost.queries > tc.baseQueries[i] || trips*2 > tc.baseQueries[i]+tc.baseGetItem[i] {
					t.Fatalf("page %d exceeds the base budget: %+v", i, cost)
				}
			}
		})
	}
}

// More direct accounts than one index window, and more children than one
// child window, still list exactly the per-item pages and continuations.
func TestAccountListingWindowsMatchPerItemPages(t *testing.T) {
	t.Setenv(accountDescendantReadsEnv, "1")
	fixture := func() *countingAccountListRepo {
		c := productionShapedAccountTree()
		r := c.boundedAccountListRepo.discovery
		for i := 0; i < 120; i++ {
			id := fmt.Sprintf("s%03d", i)
			r.hierarchy.accounts[id] = &model.Account{PK: "ACCOUNT#" + id, SK: "META", AccountID: id, Name: id, EntityType: model.EntityTypeAccount}
			r.hierarchy.members[memberKey(id, "reader")] = batchMember(id)
			r.accountIndex = append(r.accountIndex, id)
		}
		for i := 0; i < 130; i++ {
			id := fmt.Sprintf("r01-w%03d", i)
			r.hierarchy.accounts[id] = &model.Account{PK: "ACCOUNT#" + id, SK: "META", AccountID: id, Name: id, ParentAccountID: "r01", EntityType: model.EntityTypeAccount}
		}
		return c
	}
	for _, size := range []string{"100", "7"} {
		q := url.Values{"pageSize": {size}, "scope": {"all"}}
		want, base := listAccountsWithCost(t, fixture(), nil, q)
		c := fixture()
		got, costs := listAccountsWithCost(t, c, &batchAccountListRepo{countingAccountListRepo: c}, q)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("pageSize %s: windowed listing differs from per-item listing", size)
		}
		var baseQ, gotQ int
		for i := range costs {
			baseQ, gotQ = baseQ+base[i].queries, gotQ+costs[i].queries
		}
		if gotQ > baseQ {
			t.Fatalf("pageSize %s: queries %d exceed per-item %d", size, gotQ, baseQ)
		}
	}
}
