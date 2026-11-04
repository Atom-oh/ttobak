package service

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

func directAccountListFixture(count int) (*AccountService, *boundedDiscoveryRepo) {
	r := boundedDiscoveryFixture(1001)
	r.accountIndex = nil
	for i := 0; i < count; i++ {
		id := fmt.Sprintf("direct-%03d", i)
		r.accountIndex = append(r.accountIndex, id)
		r.hierarchy.accounts[id] = &model.Account{
			PK: "ACCOUNT#" + id, SK: "META", AccountID: id,
			Name: id, ParentAccountID: "parent", EntityType: model.EntityTypeAccount,
		}
		r.hierarchy.members[memberKey(id, "reader")] = &model.AccountMember{
			PK: "ACCOUNT#" + id, SK: "MEMBER#reader", AccountID: id, UserID: "reader",
			EntityType: model.EntityTypeAccountMember, Role: model.RoleOwner,
		}
	}
	return newAccountServiceWithRepo(&boundedAccountListRepo{r.hierarchy.mockAccountRepo, r}), r
}

func TestDirectAccountListBatchesMembershipsWithoutTraversingDescendants(t *testing.T) {
	for _, flag := range []string{"0", "1"} {
		t.Run(flag, func(t *testing.T) {
			t.Setenv(accountDescendantReadsEnv, flag)
			s, r := directAccountListFixture(12)
			page, err := s.ListAccountsPage(context.Background(), "reader", url.Values{
				"scope": {"direct"}, "pageSize": {"100"},
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(page.Accounts) != 12 || page.NextCursor != "" {
				t.Fatalf("twelve direct customers must fit one response: %+v", page)
			}
			if r.queries != 1 || r.childQueries != 0 || r.accountReads > 24 {
				t.Fatalf("direct picker performed descendant work: index=%d children=%d metadata=%d",
					r.queries, r.childQueries, r.accountReads)
			}
			for i, a := range page.Accounts {
				if a.AccountID != fmt.Sprintf("direct-%03d", i) || a.AccessPermission != model.PermissionEdit ||
					a.InheritedFromAccountID != "" {
					t.Fatalf("unexpected direct customer: %+v", a)
				}
			}
		})
	}
}

func TestDirectAccountListBoundsPagesAndPreservesEveryMembership(t *testing.T) {
	t.Setenv(accountDescendantReadsEnv, "1")
	s, r := directAccountListFixture(40)
	q := url.Values{"scope": {"direct"}, "pageSize": {"100"}}
	var ids []string
	for n := 0; ; n++ {
		before := r.queries
		page, err := s.ListAccountsPage(context.Background(), "reader", q)
		if err != nil {
			t.Fatal(err)
		}
		if r.queries-before != 1 || len(page.Accounts) > 25 || r.childQueries != 0 {
			t.Fatal("direct page exceeded its fixed candidate budget")
		}
		for _, a := range page.Accounts {
			ids = append(ids, a.AccountID)
		}
		if page.NextCursor == "" {
			break
		}
		if n >= 1 {
			t.Fatal("forty direct memberships must require only two requests")
		}
		q.Set("cursor", page.NextCursor)
	}
	if len(ids) != 40 {
		t.Fatalf("lost memberships: %v", ids)
	}
	for i, id := range ids {
		if id != fmt.Sprintf("direct-%03d", i) {
			t.Fatalf("duplicate or unordered membership: %v", ids)
		}
	}
}

func TestDirectAccountListOmitsStaleIndexWithoutInheritingEditAccess(t *testing.T) {
	t.Setenv(accountDescendantReadsEnv, "1")
	s, r := directAccountListFixture(3)
	delete(r.hierarchy.members, memberKey("direct-000", "reader"))
	// A stale GSI candidate remains readable through its parent, but is not
	// directly editable. Even an empty direct page must retain index progress.
	q := url.Values{"scope": {"direct"}, "pageSize": {"1"}}
	first, err := s.ListAccountsPage(context.Background(), "reader", q)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Accounts) != 0 || first.NextCursor == "" {
		t.Fatalf("stale candidate lost continuation or became editable: %+v", first)
	}
	q.Set("cursor", first.NextCursor)
	second, err := s.ListAccountsPage(context.Background(), "reader", q)
	if err != nil || len(second.Accounts) != 1 || second.Accounts[0].AccountID != "direct-001" {
		t.Fatalf("stale candidate prevented later valid membership: %+v %v", second, err)
	}
	if r.childQueries != 0 {
		t.Fatal("direct listing traversed descendants")
	}
}

func TestDirectAccountListRechecksEarlierRowsBeforePublication(t *testing.T) {
	t.Setenv(accountDescendantReadsEnv, "1")
	for _, change := range []string{"revoke", "role", "metadata"} {
		t.Run(change, func(t *testing.T) {
			s, r := directAccountListFixture(2)
			r.afterAccount = func(id string) {
				if id != "direct-001" {
					return
				}
				r.afterAccount = nil
				switch change {
				case "revoke":
					delete(r.hierarchy.members, memberKey("direct-000", "reader"))
				case "role":
					r.hierarchy.members[memberKey("direct-000", "reader")].Role = model.RoleAM
				case "metadata":
					r.hierarchy.accounts["direct-000"].Name = "Changed"
				}
			}
			_, err := s.ListAccountsPage(context.Background(), "reader", url.Values{
				"scope": {"direct"}, "pageSize": {"100"},
			})
			if !errors.Is(err, ErrStaleReadingCursor) {
				t.Fatalf("published a row that changed during listing: %v", err)
			}
		})
	}
}

func TestDirectAccountListContinuationIsBoundToScopeUserSizeAndCurrentGrant(t *testing.T) {
	t.Setenv(accountDescendantReadsEnv, "1")
	s, r := directAccountListFixture(3)
	q := url.Values{"scope": {"direct"}, "pageSize": {"1"}}
	first, err := s.ListAccountsPage(context.Background(), "reader", q)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, user, scope, size string
	}{
		{"user", "other", "direct", "1"},
		{"scope", "reader", "all", "1"},
		{"size", "reader", "direct", "2"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := s.ListAccountsPage(context.Background(), c.user, url.Values{
				"scope": {c.scope}, "pageSize": {c.size}, "cursor": {first.NextCursor},
			})
			if err == nil {
				t.Fatal("continuation replayed outside its original selection")
			}
		})
	}
	delete(r.hierarchy.members, memberKey("direct-000", "reader"))
	q.Set("cursor", first.NextCursor)
	if _, err := s.ListAccountsPage(context.Background(), "reader", q); !errors.Is(err, ErrStaleReadingCursor) {
		t.Fatalf("revoked anchor remained valid: %v", err)
	}
}

func BenchmarkRecordingAccountListRequests(b *testing.B) {
	for _, scope := range []string{"all", "direct"} {
		b.Run(scope, func(b *testing.B) {
			b.Setenv(accountDescendantReadsEnv, "1")
			totalPages := 0
			for i := 0; i < b.N; i++ {
				s, _ := directAccountListFixture(12)
				q := url.Values{"scope": {scope}, "pageSize": {"100"}}
				for pages := 0; ; pages++ {
					page, err := s.ListAccountsPage(context.Background(), "reader", q)
					if err != nil {
						b.Fatal(err)
					}
					totalPages++
					if page.NextCursor == "" {
						break
					}
					if pages >= 1000 {
						b.Fatal("listing did not complete")
					}
					q.Set("cursor", page.NextCursor)
				}
			}
			b.ReportMetric(float64(totalPages)/float64(b.N), "http-requests/op")
		})
	}
}
