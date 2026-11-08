package service

import (
	"context"
	"errors"
	"sort"

	"github.com/ttobak/backend/internal/fanout"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// listReadConcurrency bounds the independent DynamoDB reads a single list
// request keeps in flight. Results are always replayed in the original order.
const listReadConcurrency = 8

// prefetched holds one read result per slot. Only slots that a sequential
// loop would certainly have reached (below and at fanout.Ordered's fatal
// index) are marked ready; any other slot falls back to a live read, so a
// result canceled by an unrelated failure is never replayed.
type prefetched[T any] struct {
	ready []bool
	value []T
	err   []error
}

func newPrefetched[T any](n int) *prefetched[T] {
	return &prefetched[T]{ready: make([]bool, n), value: make([]T, n), err: make([]error, n)}
}

// markReady records which of the slots (given in issue order) completed
// before or at the fatal index returned by fanout.Ordered.
func (p *prefetched[T]) markReady(slots []int, stop int) {
	for k, slot := range slots {
		if k > stop {
			break
		}
		p.ready[slot] = true
	}
}

// accountSharePrefetch issues, concurrently, the reads the shared-meeting
// replay in listMeetingsFilteredPage would otherwise make one at a time: the
// canonical re-read of every account-origin share and the first membership
// lookup per account. It never decides access itself.
type accountSharePrefetch struct {
	meetings *prefetched[*model.Meeting]
	grants   map[string]prefetchedGrant
}

type prefetchedGrant struct {
	grant *accountReadGrant
	err   error
}

func (s *MeetingService) prefetchAccountShareReads(ctx context.Context, shares []model.Share, meetingMap map[string]*model.Meeting, userID string, membershipErrorsFatal bool) *accountSharePrefetch {
	pre := &accountSharePrefetch{meetings: newPrefetched[*model.Meeting](len(shares)), grants: map[string]prefetchedGrant{}}
	reread := make([]int, 0, len(shares))
	for i, share := range shares {
		if _, ok := meetingMap[share.MeetingID]; ok && share.Origin == model.ShareOriginAccount {
			reread = append(reread, i)
		}
	}
	stop := fanout.Ordered(ctx, len(reread), listReadConcurrency, func(ctx context.Context, k int) error {
		i := reread[k]
		m, err := s.currentListMeeting(ctx, shares[i].OwnerID, shares[i].MeetingID)
		pre.meetings.value[i], pre.meetings.err[i] = m, err
		return err
	})
	pre.meetings.markReady(reread, stop)

	// The replay looks up membership once per account, at the first share
	// whose canonical meeting is account-shared. Only shares before the first
	// failed re-read can reach that lookup.
	accounts := []string{}
	seen := map[string]bool{}
	for _, i := range reread[:min(stop, len(reread))] {
		m := pre.meetings.value[i]
		if m != nil && m.SharedToAccount && m.AccountID != "" && !seen[m.AccountID] {
			seen[m.AccountID] = true
			accounts = append(accounts, m.AccountID)
		}
	}
	results := make([]prefetchedGrant, len(accounts))
	stop = fanout.Ordered(ctx, len(accounts), listReadConcurrency, func(ctx context.Context, k int) error {
		grant, err := resolveAccountReadGrant(ctx, s.repo, accounts[k], userID)
		results[k] = prefetchedGrant{grant: grant, err: err}
		if membershipErrorsFatal {
			return err
		}
		return nil
	})
	for k := 0; k < len(accounts) && k <= stop; k++ {
		pre.grants[accounts[k]] = results[k]
	}
	return pre
}

func (p *accountSharePrefetch) current(ctx context.Context, s *MeetingService, i int, share model.Share) (*model.Meeting, error) {
	if p.meetings.ready[i] {
		return p.meetings.value[i], p.meetings.err[i]
	}
	return s.currentListMeeting(ctx, share.OwnerID, share.MeetingID)
}

// grant returns the prefetched first lookup for an account exactly once.
// The replay caches successes, so a second lookup only follows a transient
// error and must be a fresh read, as before.
func (p *accountSharePrefetch) grant(ctx context.Context, repo accountReadMembershipRepo, accountID, userID string) (*accountReadGrant, error) {
	if g, ok := p.grants[accountID]; ok {
		delete(p.grants, accountID)
		return g.grant, g.err
	}
	return resolveAccountReadGrant(ctx, repo, accountID, userID)
}

var errAccountReadGrantChanged = errors.New("account read grant changed")

// revalidateAccountReadGrants rechecks every non-nil cached grant after a
// list was assembled. Any lookup error is returned; any grant that no longer
// matches returns invalid. Map iteration made the sequential loop's choice
// between several failures arbitrary; this checks accounts in sorted order
// and reports the first failure in that order.
func revalidateAccountReadGrants(ctx context.Context, repo accountReadMembershipRepo, grants map[string]*accountReadGrant, userID string, invalid error) error {
	ids := make([]string, 0, len(grants))
	for id, grant := range grants {
		if grant != nil {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	errs := make([]error, len(ids))
	stop := fanout.Ordered(ctx, len(ids), listReadConcurrency, func(ctx context.Context, k int) error {
		valid, err := revalidateAccountReadGrant(ctx, repo, grants[ids[k]], userID)
		if err == nil && !valid {
			err = errAccountReadGrantChanged
		}
		errs[k] = err
		return err
	})
	if stop >= len(ids) {
		return nil
	}
	if errors.Is(errs[stop], errAccountReadGrantChanged) {
		return invalid
	}
	return errs[stop]
}

// teamPagePrefetch issues one team reference page's canonical meeting
// re-reads and existing-share lookups concurrently. The replay in
// listTeamSharedMeetingsForFilter still applies every check in key order.
type teamPagePrefetch struct {
	meetings *prefetched[*model.Meeting]
	shares   *prefetched[*model.Share]
}

func (s *MeetingService) prefetchTeamPageReads(ctx context.Context, keys []repository.MeetingKey, byID map[string]*model.Meeting, listed map[string]bool, userID, accountID string) *teamPagePrefetch {
	pre := &teamPagePrefetch{meetings: newPrefetched[*model.Meeting](len(keys)), shares: newPrefetched[*model.Share](len(keys))}
	reread := make([]int, 0, len(keys))
	for k, key := range keys {
		if byID[key.MeetingID] != nil {
			reread = append(reread, k)
		}
	}
	stop := fanout.Ordered(ctx, len(reread), listReadConcurrency, func(ctx context.Context, j int) error {
		k := reread[j]
		m, err := s.currentListMeeting(ctx, keys[k].OwnerID, keys[k].MeetingID)
		pre.meetings.value[k], pre.meetings.err[k] = m, err
		return err
	})
	pre.meetings.markReady(reread, stop)

	// A key reaches the share lookup when its canonical meeting passes the
	// replay's filters. The first such key per meeting is reached regardless
	// of earlier outcomes (only appends mark a meeting listed); later
	// duplicates depend on those outcomes and stay live reads.
	lookups := []int{}
	seen := map[string]bool{}
	for _, k := range reread[:min(stop, len(reread))] {
		m := pre.meetings.value[k]
		if m == nil || listed[m.MeetingID] || seen[m.MeetingID] || m.UserID == userID || !m.SharedToAccount || m.AccountID != accountID {
			continue
		}
		seen[m.MeetingID] = true
		lookups = append(lookups, k)
	}
	stop = fanout.Ordered(ctx, len(lookups), listReadConcurrency, func(ctx context.Context, j int) error {
		k := lookups[j]
		share, err := s.repo.GetShare(ctx, userID, pre.meetings.value[k].MeetingID)
		pre.shares.value[k], pre.shares.err[k] = share, err
		return err
	})
	pre.shares.markReady(lookups, stop)
	return pre
}

func (p *teamPagePrefetch) current(ctx context.Context, s *MeetingService, k int, key repository.MeetingKey) (*model.Meeting, error) {
	if p.meetings.ready[k] {
		return p.meetings.value[k], p.meetings.err[k]
	}
	return s.currentListMeeting(ctx, key.OwnerID, key.MeetingID)
}

func (p *teamPagePrefetch) share(ctx context.Context, s *MeetingService, k int, userID, meetingID string) (*model.Share, error) {
	if p.shares.ready[k] {
		p.shares.ready[k] = false
		return p.shares.value[k], p.shares.err[k]
	}
	return s.repo.GetShare(ctx, userID, meetingID)
}

// readableCandidatePrefetch holds the grant and account reads that
// listReadableAccountIDs makes for each distinct discovery candidate.
type readableCandidatePrefetch struct {
	repo    accountReadMembershipRepo
	userID  string
	results map[string]readableCandidate
}

type readableCandidate struct {
	grant   *accountReadGrant
	account *model.Account
	err     error
}

// prefetchReadableCandidates reads, concurrently, every distinct candidate
// the sequential add loop is certain to reach: it stops at the first
// malformed identifier and at the distinct-candidate bound, where that loop
// returns before reading.
func prefetchReadableCandidates(ctx context.Context, repo accountReadMembershipRepo, userID string, candidateIDs []string) *readableCandidatePrefetch {
	pre := &readableCandidatePrefetch{repo: repo, userID: userID, results: map[string]readableCandidate{}}
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
	results := make([]readableCandidate, len(ids))
	stop := fanout.Ordered(ctx, len(ids), listReadConcurrency, func(ctx context.Context, k int) error {
		results[k] = readCandidate(ctx, repo, ids[k], userID)
		return results[k].err
	})
	for k := 0; k < len(ids) && k <= stop; k++ {
		pre.results[ids[k]] = results[k]
	}
	return pre
}

// readCandidate makes exactly the reads add() made inline: the grant, then
// the account only for a granted candidate on a hierarchy-capable repository.
func readCandidate(ctx context.Context, repo accountReadMembershipRepo, id, userID string) readableCandidate {
	g, err := resolveAccountReadGrant(ctx, repo, id, userID)
	if err != nil || g == nil {
		return readableCandidate{err: err}
	}
	out := readableCandidate{grant: g}
	if hierarchy, ok := repo.(accountReadHierarchyRepo); ok {
		account, err := hierarchy.GetAccount(ctx, id)
		if err != nil {
			return readableCandidate{err: err}
		}
		// add() only checks presence and AccountID; keep just that so a
		// prefetched candidate never retains the full account item.
		if account != nil {
			out.account = &model.Account{AccountID: account.AccountID}
		}
	}
	return out
}

// read returns a prefetched candidate once; descendants discovered later
// (and anything not prefetched) are read live, in order, as before.
func (p *readableCandidatePrefetch) read(ctx context.Context, id string) (*accountReadGrant, *model.Account, error) {
	result, ok := p.results[id]
	if ok {
		delete(p.results, id)
	} else {
		result = readCandidate(ctx, p.repo, id, p.userID)
	}
	return result.grant, result.account, result.err
}
