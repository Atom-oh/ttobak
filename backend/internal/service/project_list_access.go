package service

import (
	"context"
	"errors"

	"github.com/ttobak/backend/internal/fanout"
	"github.com/ttobak/backend/internal/model"
)

// accountRowMemo is a request-scoped memo of canonical account META and the
// caller's MEMBER rows, used by ListMyProjects' inherited-candidate checks.
// Every candidate resolves its grants over the same ancestry; reading those
// rows once per phase replaces the per-candidate GetMember/GetAccount reads.
// It exposes only the two row reads, so capability-based traversal never
// treats it as a discovery repository. With a batch primitive it can be
// primed; otherwise rows are read individually on first use. It is never
// shared across requests or phases and never caches errors (it reuses
// discoveryReadCache's row maps).
type accountRowMemo struct{ c *discoveryReadCache }

func newAccountRowMemo(repo projectRepo) accountRowMemo {
	batch, _ := repo.(accountRowBatchReader)
	return accountRowMemo{&discoveryReadCache{hierarchy: repo, members: repo, batch: batch,
		accounts: map[string]*model.Account{}, grants: map[[2]string]*model.AccountMember{},
		childPos: map[string]windowPos{}, navPos: map[string]windowPos{}}}
}

func (m accountRowMemo) GetMember(ctx context.Context, account, user string) (*model.AccountMember, error) {
	return m.c.GetMember(ctx, account, user)
}

func (m accountRowMemo) GetAccount(ctx context.Context, id string) (*model.Account, error) {
	return m.c.GetAccount(ctx, id)
}

// prime batch-reads ids and the ancestry resolveAccountReadGrant can visit;
// a no-op without a batch primitive. A failure is returned, never ignored.
func (m accountRowMemo) prime(ctx context.Context, user string, ids []string) error {
	return m.c.prime(ctx, user, ids, true)
}

// listCandidateAccess is requireProjectReadAccess's read decision for a
// canonical row the caller already holds: owner, any linked account's read
// grant (a direct account membership is such a grant), or a direct project
// membership. It reads the project row neither again nor twice, and resolves
// grants over reads. The returned grant, when non-nil, still requires the
// fresh recheck in recheckListCandidates before the candidate is listed.
// Mutations keep requireProjectAccess; nothing here grants write access.
func (s *ProjectService) listCandidateAccess(ctx context.Context, reads accountReadMembershipRepo, user, projectID string, project *model.Project) (bool, *accountReadGrant, error) {
	if project.OwnerUserID == user {
		return true, nil, nil
	}
	for _, account := range project.AccountIDs {
		grant, err := resolveAccountReadGrant(ctx, reads, account, user)
		if err != nil {
			return false, nil, err
		}
		if grant != nil {
			return true, grant, nil
		}
	}
	member, err := s.repo.GetProjectMember(ctx, projectID, user)
	if err != nil {
		return false, nil, err
	}
	return member != nil, nil, nil
}

// listChunkAccountIDs lists, once each, the well-formed linked accounts of
// the chunk's canonical rows that the caller does not own: the accounts
// listCandidateAccess resolves grants for.
func listChunkAccountIDs(user string, ids []string, canonical map[string]*model.Project) []string {
	out, seen := []string{}, map[string]bool{}
	for _, id := range ids {
		p := canonical[id]
		if p == nil || p.OwnerUserID == user {
			continue
		}
		for _, account := range p.AccountIDs {
			if readingID.MatchString(account) && !seen[account] {
				seen[account] = true
				out = append(out, account)
			}
		}
	}
	return out
}

// recheckListCandidates replays one chunk's verified candidates in discovery
// order. A candidate admitted through an account grant is listed only if that
// grant still resolves identically over a fresh view read after the whole
// chunk was verified: the same post-assembly recheck listReadableAccountIDs
// makes, replacing requireProjectReadAccess's immediate per-candidate
// revalidation. Each distinct grant is revalidated once; a changed grant
// falls back to the uncached requireProjectReadAccess for exactly the
// candidates that used it. stop is the chunk's first verification failure
// (len(ids) when none); the first error in discovery order is returned,
// a verification error included.
func (s *ProjectService) recheckListCandidates(ctx context.Context, user string, ids []string, grants []*accountReadGrant, readable []bool, stop int, verifyErr error) error {
	type grantKey struct{ account, fingerprint string }
	order := []grantKey{}
	byKey := map[grantKey]*accountReadGrant{}
	accounts, seenAccount := []string{}, map[string]bool{}
	for j := 0; j < stop; j++ {
		g := grants[j]
		if !readable[j] || g == nil {
			continue
		}
		key := grantKey{g.AccountID, g.fingerprint()}
		if _, ok := byKey[key]; !ok {
			byKey[key] = g
			order = append(order, key)
		}
		if !seenAccount[g.AccountID] {
			seenAccount[g.AccountID] = true
			accounts = append(accounts, g.AccountID)
		}
	}
	valid := map[grantKey]bool{}
	errs := map[grantKey]error{}
	checked := len(order)
	if len(order) > 0 {
		fresh := newAccountRowMemo(s.repo)
		if err := fresh.prime(ctx, user, accounts); err != nil {
			return err
		}
		results := make([]bool, len(order))
		resultErrs := make([]error, len(order))
		checked = fanout.Ordered(ctx, len(order), listReadConcurrency, func(ctx context.Context, i int) error {
			results[i], resultErrs[i] = revalidateAccountReadGrant(ctx, fresh, byKey[order[i]], user)
			return resultErrs[i]
		})
		// Grants are ordered by first use, so the replay below reaches the
		// first user of a failed grant before any user of an unchecked one.
		for i := 0; i < len(order) && i <= checked; i++ {
			valid[order[i]], errs[order[i]] = results[i], resultErrs[i]
		}
	}
	for j := 0; j < len(ids); j++ {
		if j == stop {
			return verifyErr
		}
		g := grants[j]
		if !readable[j] || g == nil {
			continue
		}
		key := grantKey{g.AccountID, g.fingerprint()}
		if err := errs[key]; err != nil {
			return err
		}
		if valid[key] {
			continue
		}
		// The grant changed after verification: decide this candidate
		// exactly as the per-candidate path did, from current rows.
		if _, _, err := s.requireProjectReadAccess(ctx, user, ids[j]); err != nil {
			if !errors.Is(err, ErrForbidden) && !errors.Is(err, ErrNotFound) {
				return err
			}
			readable[j] = false
		}
	}
	return nil
}
