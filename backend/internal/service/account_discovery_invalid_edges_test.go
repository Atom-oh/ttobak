package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"testing"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

func TestAccountDiscoveryCompletesWithIndependentDirectGrantsOnInvalidEdges(t *testing.T) {
	t.Setenv(accountDescendantReadsEnv, "1")
	for _, scenario := range []string{"cycle", "depth"} {
		t.Run(scenario, func(t *testing.T) {
			r := boundedDiscoveryFixture(0)
			r.hierarchy.accounts = map[string]*model.Account{}
			r.hierarchy.members = map[string]*model.AccountMember{}
			required := []string{"a", "b"}
			parents := map[string]string{"a": "b", "b": "a"}
			if scenario == "depth" {
				parents = map[string]string{"a": ""}
				parent := "a"
				for depth := 1; depth <= repository.AccountHierarchyMaxAncestors+1; depth++ {
					id := fmt.Sprintf("node-%03d", depth)
					parents[id] = parent
					parent = id
				}
				required = []string{"a", parent}
			}
			for id, parent := range parents {
				r.hierarchy.accounts[id] = &model.Account{PK: "ACCOUNT#" + id, SK: "META",
					AccountID: id, ParentAccountID: parent, EntityType: model.EntityTypeAccount}
			}
			for _, id := range required {
				r.hierarchy.members[memberKey(id, "reader")] = &model.AccountMember{
					PK: "ACCOUNT#" + id, SK: "MEMBER#reader", AccountID: id, UserID: "reader",
					EntityType: model.EntityTypeAccountMember, Role: model.RoleAM}
			}
			r.accountIndex = required
			c := &accountDiscoveryCursor{}
			seen, cursors := map[string]bool{}, map[string]bool{}
			for step := 0; ; step++ {
				ids, done, err := nextReadableAccountCandidate(context.Background(), r, "reader", c)
				if err != nil {
					t.Fatalf("independent direct pagination failed: %v", err)
				}
				for _, id := range ids {
					seen[id] = true
				}
				if done {
					break
				}
				raw, _ := json.Marshal(c)
				if cursors[string(raw)] || step > 300 {
					t.Fatal("invalid hierarchy edge prevented discovery completion")
				}
				cursors[string(raw)] = true
			}
			for _, id := range required {
				if !seen[id] {
					t.Fatalf("invalid hierarchy hid direct grant %s", id)
				}
			}
			service := newAccountServiceWithRepo(&boundedAccountListRepo{r.hierarchy.mockAccountRepo, r})
			q := url.Values{"pageSize": {"1"}}
			seen = map[string]bool{}
			for pageNumber := 0; ; pageNumber++ {
				page, err := service.ListAccountsPage(context.Background(), "reader", q)
				if err != nil {
					t.Fatal(err)
				}
				for _, row := range page.Accounts {
					seen[row.AccountID] = true
				}
				if page.NextCursor == "" {
					break
				}
				if pageNumber > 100 {
					t.Fatal("paged Account API did not complete invalid hierarchy")
				}
				q.Set("cursor", page.NextCursor)
			}
			for _, id := range required {
				if !seen[id] {
					t.Fatalf("Account API hid direct grant %s", id)
				}
			}
		})
	}
}
