package service

import (
	"context"

	"github.com/ttobak/backend/internal/repository"
)

// accountRowBatchReader is the consistent batched form of GetAccount/GetMember.
type accountRowBatchReader interface {
	BatchGetAccountRows(context.Context, []repository.AccountRowKey) (*repository.AccountRows, error)
}

// accountPrimeCallKeys bounds the keys handed to one repository batch call;
// the repository splits each call into concurrent 100-key requests.
const accountPrimeCallKeys = 1000

type accountReads interface {
	accountReadMembershipRepo
	accountReadHierarchyRepo
}

// batchedAccountReads returns a fresh request-scoped read view that can be
// primed with batch reads, or nil when repo lacks a capability. Only
// repositories with the full discovery capability set and a batch primitive
// are wrapped, so narrower repositories keep their exact per-item behavior.
// A fresh view reads current rows: callers create one per recheck phase.
func batchedAccountReads(repo accountReadMembershipRepo) *discoveryReadCache {
	if _, ok := repo.(accountRowBatchReader); !ok {
		return nil
	}
	cache, _ := newDiscoveryReadCache(repo).(*discoveryReadCache)
	return cache
}

// primedAccountReads returns a fresh view primed with ids, or repo itself when
// batching is unavailable. A priming failure is returned, never ignored.
func primedAccountReads(ctx context.Context, repo accountReadMembershipRepo, user string, ids []string, ancestry bool) (accountReads, error) {
	if view := batchedAccountReads(repo); view != nil {
		return view, view.prime(ctx, user, ids, ancestry)
	}
	reads, ok := repo.(accountReads)
	if !ok {
		return nil, ErrReadingData
	}
	return reads, nil
}

// primeAccountReads primes repo when it is a batch-capable request view.
func primeAccountReads(ctx context.Context, repo accountReadMembershipRepo, user string, ids []string, ancestry bool) error {
	if view, ok := repo.(*discoveryReadCache); ok {
		return view.prime(ctx, user, ids, ancestry)
	}
	return nil
}

// prime batch-reads the META row and the caller's exact MEMBER row for ids and,
// with ancestry, level by level for the ancestors resolveAccountReadGrant can
// visit: an id without a direct membership walks to its root. It only fills
// the request cache, never caches errors, and skips rows already cached. Every
// access decision is still made by the unchanged resolver over these rows;
// any row it was not primed with is read individually as before.
func (c *discoveryReadCache) prime(ctx context.Context, user string, ids []string, ancestry bool) error {
	if c.batch == nil || !readingID.MatchString(user) {
		return nil
	}
	ancestry = ancestry && descendantReadsEnabled()
	type entry struct {
		id      string
		walking bool
	}
	frontier := make([]entry, 0, len(ids))
	for _, id := range ids {
		frontier = append(frontier, entry{id: id})
	}
	read, followed := map[string]bool{}, map[string]bool{}
	for depth := 0; len(frontier) > 0 && depth <= repository.AccountHierarchyMaxAncestors+1; depth++ {
		keys := []repository.AccountRowKey{}
		c.mu.Lock()
		for _, e := range frontier {
			if read[e.id] || !readingID.MatchString(e.id) {
				continue
			}
			read[e.id] = true
			if _, ok := c.accounts[e.id]; !ok {
				keys = append(keys, repository.AccountRowKey{AccountID: e.id})
			}
			if _, ok := c.grants[[2]string{e.id, user}]; !ok {
				keys = append(keys, repository.AccountRowKey{AccountID: e.id, UserID: user})
			}
		}
		c.mu.Unlock()
		for start := 0; start < len(keys); start += accountPrimeCallKeys {
			chunk := keys[start:min(start+accountPrimeCallKeys, len(keys))]
			rows, err := c.batch.BatchGetAccountRows(ctx, chunk)
			if err != nil {
				return err
			}
			if rows == nil {
				return ErrReadingData
			}
			c.mu.Lock()
			for _, key := range chunk {
				if key.UserID == "" {
					c.accounts[key.AccountID] = cloneAccount(rows.Accounts[key.AccountID])
				} else {
					c.grants[[2]string{key.AccountID, key.UserID}] = cloneMember(rows.Members[key])
				}
			}
			c.mu.Unlock()
		}
		if !ancestry {
			return nil
		}
		next := []entry{}
		c.mu.Lock()
		for _, e := range frontier {
			a := c.accounts[e.id]
			if followed[e.id] || !readingID.MatchString(e.id) || !canonicalReadAccount(a, e.id) || a.ParentAccountID == "" {
				continue
			}
			// The resolver stops at a direct membership on the requested
			// account; once walking it records the path through to the root.
			if !e.walking && c.grants[[2]string{e.id, user}] != nil {
				continue
			}
			followed[e.id] = true
			next = append(next, entry{id: a.ParentAccountID, walking: true})
		}
		c.mu.Unlock()
		frontier = next
	}
	return nil
}

// grantAccountIDs lists, in order, the account IDs of non-nil grants.
func grantAccountIDs(grants []*accountReadGrant) []string {
	ids := make([]string, 0, len(grants))
	for _, grant := range grants {
		if grant != nil {
			ids = append(ids, grant.AccountID)
		}
	}
	return ids
}

// distinctCandidateIDs mirrors prefetchReadableCandidates' selection: distinct
// IDs in order, stopping at the first malformed ID or the 10,000 bound, where
// the sequential loop returns before reading.
func distinctCandidateIDs(candidateIDs []string) []string {
	ids := []string{}
	seen := map[string]bool{}
	for _, id := range candidateIDs {
		if !readingID.MatchString(id) || len(ids) >= 10000 {
			break
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids
}
