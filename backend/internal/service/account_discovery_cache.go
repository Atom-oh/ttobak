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
	childPos map[string]windowPos
	navPos   map[string]windowPos
	// user is the caller whose rows prime() reads; child windows prime for it.
	user string
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
		childPos: map[string]windowPos{}, navPos: map[string]windowPos{},
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
	if limit != 1 || c.batch == nil {
		return c.children.ListChildAccountIDsPage(ctx, parent, cursor, limit)
	}
	key := parent + "\x00" + cursor
	c.mu.Lock()
	pos, ok := c.childPos[key]
	c.mu.Unlock()
	if !ok {
		ids, next, err := c.children.ListChildAccountIDsPage(ctx, parent, cursor, discoveryChildWindow)
		if err != nil {
			return nil, nil, err
		}
		if len(ids) > discoveryChildWindow {
			return nil, nil, ErrReadingData
		}
		w := &idWindow{ids: append([]string{}, ids...), next: next}
		c.mu.Lock()
		// A node revisited in the same request (a directly held descendant
		// is listed under its root and as its own root) reuses its window.
		c.childPos[key] = windowPos{w, 0}
		for j, id := range w.ids {
			// After the last ID of a truncated window the index must be read
			// again; every other position is answered from this window.
			if j == len(w.ids)-1 && next != nil {
				break
			}
			if after, err := repository.AccountChildCursorAfter(parent, id); err == nil {
				c.childPos[parent+"\x00"+after] = windowPos{w, j + 1}
			}
		}
		user := c.user
		c.mu.Unlock()
		// One batch read primes the rows every step in this window reads.
		if user != "" {
			if err := c.prime(ctx, user, w.ids, true); err != nil {
				return nil, nil, err
			}
		}
		pos = windowPos{w, 0}
	}
	if pos.i >= len(pos.w.ids) {
		// Only an empty window or the end of a complete one reaches here.
		if len(pos.w.ids) == 0 {
			return []string{}, pos.w.next, nil
		}
		return []string{}, nil, nil
	}
	id := pos.w.ids[pos.i]
	if pos.i == len(pos.w.ids)-1 {
		// The window's own read decides whether more children follow, so a
		// complete window ends without a further empty step.
		return []string{id}, pos.w.next, nil
	}
	after, err := repository.AccountChildCursorAfter(parent, id)
	if err != nil {
		return nil, nil, ErrReadingData
	}
	return []string{id}, &after, nil
}

func (c *discoveryReadCache) QueryNavigationCandidates(ctx context.Context, user, kind, account, after string, limit int) (*repository.NavigationCandidatePage, error) {
	if limit != 1 || kind != "accounts" || account != "" || c.batch == nil {
		return c.nav.QueryNavigationCandidates(ctx, user, kind, account, after, limit)
	}
	key := user + "\x00" + after
	c.mu.Lock()
	pos, ok := c.navPos[key]
	c.mu.Unlock()
	if !ok {
		page, err := c.nav.QueryNavigationCandidates(ctx, user, kind, account, after, discoveryNavWindow)
		if err != nil {
			return nil, err
		}
		if page == nil || len(page.IDs) > discoveryNavWindow {
			return nil, ErrReadingData
		}
		previous := after
		for _, id := range page.IDs {
			if id <= previous {
				return nil, ErrReadingData
			}
			previous = id
		}
		var next *string
		if page.NextID != "" {
			n := page.NextID
			next = &n
		}
		w := &idWindow{ids: append([]string{}, page.IDs...), next: next}
		c.mu.Lock()
		for j, id := range w.ids {
			if j == len(w.ids)-1 && next != nil {
				break
			}
			c.navPos[user+"\x00"+id] = windowPos{w, j + 1}
		}
		c.mu.Unlock()
		if err := c.prime(ctx, user, w.ids, true); err != nil {
			return nil, err
		}
		pos = windowPos{w, 0}
	}
	if pos.i >= len(pos.w.ids) {
		if len(pos.w.ids) == 0 && pos.w.next != nil {
			return &repository.NavigationCandidatePage{IDs: []string{}, NextID: *pos.w.next}, nil
		}
		return &repository.NavigationCandidatePage{IDs: []string{}}, nil
	}
	// A one-ID index page reports its key as the continuation, except at the
	// end of a complete window, which ends without a further empty step.
	id := pos.w.ids[pos.i]
	if pos.i == len(pos.w.ids)-1 && pos.w.next == nil {
		return &repository.NavigationCandidatePage{IDs: []string{id}}, nil
	}
	return &repository.NavigationCandidatePage{IDs: []string{id}, NextID: id}, nil
}

// Discovery reads one candidate per step. With the batched read view, each
// index or child query instead reads a wider window once per request and
// answers the following one-ID steps from it, returning exactly the IDs and
// continuations the one-ID queries would; an index snapshot only selects
// candidates, and every returned row is still rechecked against current rows.
const (
	discoveryChildWindow = 100
	discoveryNavWindow   = 50
)

type idWindow struct {
	ids  []string
	next *string
}

type windowPos struct {
	w *idWindow
	i int
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
