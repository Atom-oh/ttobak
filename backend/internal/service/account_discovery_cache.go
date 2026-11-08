package service

import (
	"context"
	"sync"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// discoveryReadCache memoizes canonical account and membership reads within one
// discovery page. Traversal revisits the same ancestry for every child; reading
// it once per request does not change what a request observes, because a page's
// returned rows are rechecked against the uncached repository before return.
// It is never shared across requests and never caches errors.
type discoveryReadCache struct {
	hierarchy accountReadHierarchyRepo
	members   accountReadMembershipRepo
	children  accountReadChildrenRepo
	nav       accountReadNavigationRepo
	// batch is nil when the repository has no consistent batch primitive;
	// priming is then a no-op and reads stay per item.
	batch accountRowBatchReader

	mu       sync.Mutex
	accounts map[string]*model.Account
	grants   map[[2]string]*model.AccountMember
}

// newDiscoveryReadCache wraps only repositories with every discovery capability.
// Narrower repositories keep their exact capability set, since traversal selects
// behavior by type assertion.
func newDiscoveryReadCache(repo accountReadMembershipRepo) accountReadMembershipRepo {
	hierarchy, ok1 := repo.(accountReadHierarchyRepo)
	children, ok2 := repo.(accountReadChildrenRepo)
	nav, ok3 := repo.(accountReadNavigationRepo)
	if !ok1 || !ok2 || !ok3 {
		return repo
	}
	batch, _ := repo.(accountRowBatchReader)
	return &discoveryReadCache{
		hierarchy: hierarchy, members: repo, children: children, nav: nav, batch: batch,
		accounts: map[string]*model.Account{}, grants: map[[2]string]*model.AccountMember{},
	}
}

func (c *discoveryReadCache) GetAccount(ctx context.Context, id string) (*model.Account, error) {
	c.mu.Lock()
	a, ok := c.accounts[id]
	c.mu.Unlock()
	if ok {
		return cloneAccount(a), nil
	}
	a, err := c.hierarchy.GetAccount(ctx, id)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.accounts[id] = cloneAccount(a)
	c.mu.Unlock()
	return a, nil
}

func (c *discoveryReadCache) GetMember(ctx context.Context, accountID, userID string) (*model.AccountMember, error) {
	key := [2]string{accountID, userID}
	c.mu.Lock()
	m, ok := c.grants[key]
	c.mu.Unlock()
	if ok {
		return cloneMember(m), nil
	}
	m, err := c.members.GetMember(ctx, accountID, userID)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.grants[key] = cloneMember(m)
	c.mu.Unlock()
	return m, nil
}

func (c *discoveryReadCache) ListChildAccountIDsPage(ctx context.Context, parent, cursor string, limit int32) ([]string, *string, error) {
	return c.children.ListChildAccountIDsPage(ctx, parent, cursor, limit)
}

func (c *discoveryReadCache) QueryNavigationCandidates(ctx context.Context, user, kind, account, after string, limit int) (*repository.NavigationCandidatePage, error) {
	return c.nav.QueryNavigationCandidates(ctx, user, kind, account, after, limit)
}

func cloneAccount(a *model.Account) *model.Account {
	if a == nil {
		return nil
	}
	cp := *a
	cp.Aliases, cp.Domains = cloneStrings(a.Aliases), cloneStrings(a.Domains)
	return &cp
}

// cloneStrings keeps nil and empty distinct: revisions hash the JSON form.
func cloneStrings(s []string) []string {
	if s == nil {
		return nil
	}
	return append(make([]string, 0, len(s)), s...)
}

func cloneMember(m *model.AccountMember) *model.AccountMember {
	if m == nil {
		return nil
	}
	cp := *m
	return &cp
}
