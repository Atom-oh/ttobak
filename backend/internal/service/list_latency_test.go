package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// latencyMeetingRepo injects a fixed delay into every read the list paths
// issue and records call counts and the peak number of overlapping calls.
// It measures request shape (sequential round trips), not DynamoDB itself.
type latencyMeetingRepo struct {
	*mockMeetingRepo
	delay    time.Duration
	mu       sync.Mutex
	calls    map[string]int
	inFlight int64
	peak     int64

	// Optional fault injection, keyed by meeting or account ID.
	slow          map[string]time.Duration // extra delay for any read naming the key
	meetingErr    map[string]error         // GetMeeting
	shareErr      map[string]error         // GetShare
	accountErr    map[string]error         // GetAccount
	memberErrAt   map[string]map[int]error // GetMember: nth call (1-based) for an account fails
	memberGoneAt  map[string]int           // GetMember: nil from the nth call for an account on
	memberCalls   map[string]int
	canceledReads int
}

func newLatencyMeetingRepo(base *mockMeetingRepo, delay time.Duration) *latencyMeetingRepo {
	return &latencyMeetingRepo{mockMeetingRepo: base, delay: delay, calls: map[string]int{}}
}

func (r *latencyMeetingRepo) wait(ctx context.Context, op string, key ...string) error {
	r.mu.Lock()
	r.calls[op]++
	delay := r.delay
	for _, k := range key {
		delay += r.slow[k]
	}
	r.mu.Unlock()
	now := atomic.AddInt64(&r.inFlight, 1)
	defer atomic.AddInt64(&r.inFlight, -1)
	for {
		peak := atomic.LoadInt64(&r.peak)
		if now <= peak || atomic.CompareAndSwapInt64(&r.peak, peak, now) {
			break
		}
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		r.mu.Lock()
		r.canceledReads++
		r.mu.Unlock()
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (r *latencyMeetingRepo) total() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, c := range r.calls {
		n += c
	}
	return n
}

func (r *latencyMeetingRepo) summary() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	ops := make([]string, 0, len(r.calls))
	for op := range r.calls {
		ops = append(ops, op)
	}
	sort.Strings(ops)
	parts := make([]string, 0, len(ops))
	for _, op := range ops {
		parts = append(parts, fmt.Sprintf("%s=%d", op, r.calls[op]))
	}
	return strings.Join(parts, " ")
}

func (r *latencyMeetingRepo) ListMeetings(ctx context.Context, p repository.ListMeetingsParams) (*repository.ListMeetingsResult, error) {
	if err := r.wait(ctx, "ListMeetings"); err != nil {
		return nil, err
	}
	return r.mockMeetingRepo.ListMeetings(ctx, p)
}

func (r *latencyMeetingRepo) BatchGetMeetings(ctx context.Context, keys []repository.MeetingKey) ([]*model.Meeting, error) {
	if err := r.wait(ctx, "BatchGetMeetings"); err != nil {
		return nil, err
	}
	return r.mockMeetingRepo.BatchGetMeetings(ctx, keys)
}

func (r *latencyMeetingRepo) GetMeeting(ctx context.Context, owner, id string) (*model.Meeting, error) {
	if err := r.wait(ctx, "GetMeeting", id); err != nil {
		return nil, err
	}
	if err := r.meetingErr[id]; err != nil {
		return nil, err
	}
	return r.mockMeetingRepo.GetMeeting(ctx, owner, id)
}

func (r *latencyMeetingRepo) GetMember(ctx context.Context, account, user string) (*model.AccountMember, error) {
	if err := r.wait(ctx, "GetMember", account); err != nil {
		return nil, err
	}
	r.mu.Lock()
	if r.memberCalls == nil {
		r.memberCalls = map[string]int{}
	}
	r.memberCalls[account]++
	n := r.memberCalls[account]
	err := r.memberErrAt[account][n]
	gone := r.memberGoneAt[account] > 0 && n >= r.memberGoneAt[account]
	r.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if gone {
		return nil, nil
	}
	return r.mockMeetingRepo.GetMember(ctx, account, user)
}

func (r *latencyMeetingRepo) GetAccount(ctx context.Context, account string) (*model.Account, error) {
	if err := r.wait(ctx, "GetAccount", account); err != nil {
		return nil, err
	}
	if err := r.accountErr[account]; err != nil {
		return nil, err
	}
	return r.mockMeetingRepo.GetAccount(ctx, account)
}

func (r *latencyMeetingRepo) GetShare(ctx context.Context, to, id string) (*model.Share, error) {
	if err := r.wait(ctx, "GetShare", id); err != nil {
		return nil, err
	}
	if err := r.shareErr[id]; err != nil {
		return nil, err
	}
	return r.mockMeetingRepo.GetShare(ctx, to, id)
}

func (r *latencyMeetingRepo) ListAccountsForUser(ctx context.Context, user string) ([]model.AccountMember, error) {
	if err := r.wait(ctx, "ListAccountsForUser"); err != nil {
		return nil, err
	}
	return r.mockMeetingRepo.ListAccountsForUser(ctx, user)
}

func (r *latencyMeetingRepo) ListMeetingRefsForAccountPage(ctx context.Context, account, cursor string, limit int32) ([]model.MeetingRef, *string, error) {
	if err := r.wait(ctx, "ListMeetingRefsForAccountPage"); err != nil {
		return nil, nil, err
	}
	return r.mockMeetingRepo.ListMeetingRefsForAccountPage(ctx, account, cursor, limit)
}

// sharedTabFixture: 20 account-origin shares spread over three accounts in a
// fixed repository order, the caller a direct member of every account.
func sharedTabFixture() (*mockMeetingRepo, []model.Share) {
	repo := newMockMeetingRepo()
	accounts := []string{"acc-1", "acc-2", "acc-3"}
	for _, a := range accounts {
		repo.addMember(a, "member", model.RoleSA)
	}
	shares := []model.Share{}
	for i := 0; i < 20; i++ {
		id := fmt.Sprintf("m-%02d", i)
		account := accounts[i%len(accounts)]
		repo.addMeeting(&model.Meeting{MeetingID: id, UserID: "owner", Title: id, Status: model.StatusDone, AccountID: account, SharedToAccount: true})
		share := model.Share{MeetingID: id, OwnerID: "owner", SharedToID: "member", Permission: model.PermissionRead, Origin: model.ShareOriginAccount}
		repo.shares[shareKey("member", id)] = &share
		shares = append(shares, share)
	}
	repo.listMeetingsFn = func(repository.ListMeetingsParams) (*repository.ListMeetingsResult, error) {
		return &repository.ListMeetingsResult{Shares: append([]model.Share(nil), shares...)}, nil
	}
	return repo, shares
}

// teamTabFixture: no individual rows; fifteen team meetings referenced by
// three accounts (five each) that the caller belongs to.
func teamTabFixture() *mockMeetingRepo {
	repo := newMockMeetingRepo()
	for a := 1; a <= 3; a++ {
		account := fmt.Sprintf("acc-%d", a)
		repo.addMember(account, "member", model.RoleSA)
		for i := 0; i < 5; i++ {
			id := fmt.Sprintf("t-%d-%d", a, i)
			repo.addMeeting(&model.Meeting{MeetingID: id, UserID: "owner", Title: id, Status: model.StatusDone, AccountID: account, SharedToAccount: true})
			repo.meetingRefs[account] = append(repo.meetingRefs[account], model.MeetingRef{AccountID: account, MeetingID: id, OwnerUserID: "owner"})
		}
	}
	repo.listMeetingsFn = func(repository.ListMeetingsParams) (*repository.ListMeetingsResult, error) {
		return &repository.ListMeetingsResult{}, nil
	}
	return repo
}

// TestListMeetingsLatencyHarness reports simulated wall-clock time, call
// counts and peak overlap for representative pages. Run with -v to read it.
// Only the result shape and the concurrency bound are asserted; timings are
// informational.
func TestListMeetingsLatencyHarness(t *testing.T) {
	const delay = 10 * time.Millisecond
	for _, tc := range []struct {
		name  string
		build func() *mockMeetingRepo
		tab   string
		want  int
	}{
		{"shared tab, 20 account-origin shares over 3 accounts", func() *mockMeetingRepo { r, _ := sharedTabFixture(); return r }, "shared", 20},
		{"all tab, 15 team meetings over 3 accounts", teamTabFixture, "", 15},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newLatencyMeetingRepo(tc.build(), delay)
			svc := NewMeetingServiceForTest(repo)
			start := time.Now()
			resp, err := svc.ListMeetings(context.Background(), "member", tc.tab, "", "", 20)
			elapsed := time.Since(start)
			t.Logf("delay=%v elapsed=%v calls=%d peak=%d [%s]", delay, elapsed.Round(time.Millisecond), repo.total(), atomic.LoadInt64(&repo.peak), repo.summary())
			if err != nil {
				t.Fatal(err)
			}
			if len(resp.Meetings) != tc.want {
				t.Fatalf("meetings = %d, want %d", len(resp.Meetings), tc.want)
			}
			if peak := atomic.LoadInt64(&repo.peak); peak > listReadConcurrency || peak < 2 {
				t.Fatalf("peak overlapping reads %d, want 2..%d", peak, listReadConcurrency)
			}
		})
	}
}

func meetingIDs(items []model.MeetingListItem) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.MeetingID)
	}
	return ids
}

// mixedShareFixture interleaves direct and account-origin shares over three
// accounts, with per-read jitter so completion order differs from share order.
func mixedShareFixture() (*latencyMeetingRepo, []string) {
	base, shares := sharedTabFixture()
	for i := range shares {
		if i%4 == 1 {
			shares[i].Origin = ""
			shares[i].Permission = model.PermissionEdit
		}
	}
	base.listMeetingsFn = func(repository.ListMeetingsParams) (*repository.ListMeetingsResult, error) {
		return &repository.ListMeetingsResult{Shares: append([]model.Share(nil), shares...)}, nil
	}
	repo := newLatencyMeetingRepo(base, time.Millisecond)
	repo.slow = map[string]time.Duration{}
	want := []string{}
	for i, share := range shares {
		repo.slow[share.MeetingID] = time.Duration(len(shares)-i) * time.Millisecond
		want = append(want, share.MeetingID)
	}
	return repo, want
}

func TestListMeetingsConcurrentSharedReadsKeepShareOrder(t *testing.T) {
	repo, want := mixedShareFixture()
	resp, err := NewMeetingServiceForTest(repo).ListMeetings(context.Background(), "member", "shared", "", "", 20)
	if err != nil {
		t.Fatal(err)
	}
	if got := meetingIDs(resp.Meetings); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("order changed:\n got %v\nwant %v", got, want)
	}
	direct := map[string]bool{"m-01": true, "m-05": true, "m-09": true, "m-13": true, "m-17": true}
	for _, item := range resp.Meetings {
		want := model.PermissionRead
		if direct[item.MeetingID] {
			want = model.PermissionEdit
		}
		if item.Permission == nil || *item.Permission != want {
			t.Fatalf("%s permission %v, want %s", item.MeetingID, item.Permission, want)
		}
	}
	// One canonical re-read per account-origin share (15) and one listing plus
	// one revalidation membership read per account remain.
	if got := repo.calls["GetMeeting"]; got != 15 {
		t.Fatalf("GetMeeting calls = %d, want 15", got)
	}
}

func TestListMeetingsConcurrentReReadReturnsFirstErrorInShareOrder(t *testing.T) {
	repo, _ := mixedShareFixture()
	errFirst, errLater := errors.New("first re-read failed"), errors.New("later re-read failed")
	repo.meetingErr = map[string]error{"m-04": errFirst, "m-14": errLater}
	repo.slow["m-04"] = 40 * time.Millisecond // fails last in time, first in order
	repo.slow["m-14"] = 0
	_, err := NewMeetingServiceForTest(repo).ListMeetings(context.Background(), "member", "shared", "", "", 20)
	if !errors.Is(err, errFirst) {
		t.Fatalf("err = %v, want the first share-order failure", err)
	}
	// A sequential loop reads acc-1 (m-00) and acc-3 (m-02) before it fails
	// at m-04; acc-2 first appears at the failing share and is never read.
	if repo.memberCalls["acc-1"] != 1 || repo.memberCalls["acc-3"] != 1 || repo.memberCalls["acc-2"] != 0 {
		t.Fatalf("membership reads differ from the sequential loop: %v", repo.memberCalls)
	}
}

func TestListMeetingsConcurrentTransientMembershipErrorSkipsOnlyThatMeeting(t *testing.T) {
	transient := errors.New("transient GetMember")
	t.Run("unfiltered list skips one meeting and retries the account", func(t *testing.T) {
		repo, want := mixedShareFixture()
		// acc-2's first lookup fails. m-01 is a direct share, so the first
		// acc-2 account-origin share is m-04; m-07 retries acc-2 afresh.
		repo.memberErrAt = map[string]map[int]error{"acc-2": {1: transient}}
		resp, err := NewMeetingServiceForTest(repo).ListMeetings(context.Background(), "member", "shared", "", "", 20)
		if err != nil {
			t.Fatal(err)
		}
		expected := []string{}
		for _, id := range want {
			if id != "m-04" {
				expected = append(expected, id)
			}
		}
		if got := meetingIDs(resp.Meetings); strings.Join(got, ",") != strings.Join(expected, ",") {
			t.Fatalf("got %v\nwant %v", got, expected)
		}
	})
	t.Run("account-filtered list returns the error", func(t *testing.T) {
		repo, _ := mixedShareFixture()
		repo.memberErrAt = map[string]map[int]error{"acc-2": {1: transient}}
		_, err := NewMeetingServiceForTest(repo).ListMeetingsForAccounts(context.Background(), "member", "shared", "", []string{"acc-1", "acc-2", "acc-3"}, 20)
		if !errors.Is(err, transient) || !strings.Contains(err.Error(), "check shared meeting membership") {
			t.Fatalf("err = %v, want wrapped membership error", err)
		}
	})
}

func TestListMeetingsConcurrentRevalidationFailures(t *testing.T) {
	t.Run("changed grant is forbidden", func(t *testing.T) {
		repo, _ := mixedShareFixture()
		repo.memberGoneAt = map[string]int{"acc-3": 2} // removed after the listing lookup
		_, err := NewMeetingServiceForTest(repo).ListMeetings(context.Background(), "member", "shared", "", "", 20)
		if !errors.Is(err, ErrForbidden) {
			t.Fatalf("err = %v, want ErrForbidden", err)
		}
	})
	t.Run("revalidation read error is returned", func(t *testing.T) {
		repo, _ := mixedShareFixture()
		boom := errors.New("revalidation read failed")
		repo.memberErrAt = map[string]map[int]error{"acc-1": {2: boom}}
		_, err := NewMeetingServiceForTest(repo).ListMeetings(context.Background(), "member", "shared", "", "", 20)
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v, want the revalidation error", err)
		}
	})
}

func TestTeamPageConcurrentReadsKeepOrderAndFirstError(t *testing.T) {
	build := func() *latencyMeetingRepo {
		repo := newLatencyMeetingRepo(teamTabFixture(), time.Millisecond)
		repo.slow = map[string]time.Duration{}
		for a := 1; a <= 3; a++ {
			for i := 0; i < 5; i++ {
				repo.slow[fmt.Sprintf("t-%d-%d", a, i)] = time.Duration(5-i) * 2 * time.Millisecond
			}
		}
		return repo
	}
	t.Run("order and existing share", func(t *testing.T) {
		repo := build()
		// An individual share for t-2-2 leaves it to the regular stream.
		repo.mockMeetingRepo.shares[shareKey("member", "t-2-2")] = &model.Share{MeetingID: "t-2-2", OwnerID: "owner", SharedToID: "member", Permission: model.PermissionEdit}
		resp, err := NewMeetingServiceForTest(repo).ListMeetings(context.Background(), "member", "", "", "", 20)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{}
		for a := 1; a <= 3; a++ {
			for i := 0; i < 5; i++ {
				if id := fmt.Sprintf("t-%d-%d", a, i); id != "t-2-2" {
					want = append(want, id)
				}
			}
		}
		if got := meetingIDs(resp.Meetings); strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("got %v\nwant %v", got, want)
		}
	})
	t.Run("first share lookup error in key order", func(t *testing.T) {
		repo := build()
		errFirst, errLater := errors.New("first share lookup"), errors.New("later share lookup")
		repo.shareErr = map[string]error{"t-1-1": errFirst, "t-1-3": errLater}
		repo.slow["t-1-1"] = 30 * time.Millisecond
		repo.slow["t-1-3"] = 0
		_, err := NewMeetingServiceForTest(repo).ListMeetings(context.Background(), "member", "", "", "", 20)
		if !errors.Is(err, errFirst) || !strings.Contains(err.Error(), "resolve existing meeting share") {
			t.Fatalf("err = %v, want the first key-order share error", err)
		}
	})
	t.Run("re-read error stops before share lookups", func(t *testing.T) {
		repo := build()
		boom := errors.New("re-read failed")
		repo.meetingErr = map[string]error{"t-1-0": boom}
		_, err := NewMeetingServiceForTest(repo).ListMeetings(context.Background(), "member", "", "", "", 20)
		if !errors.Is(err, boom) || repo.calls["GetShare"] != 0 {
			t.Fatalf("err = %v calls [%s]", err, repo.summary())
		}
	})
}

func TestReadableAccountDiscoveryConcurrentChecks(t *testing.T) {
	build := func() *latencyMeetingRepo {
		base := newMockMeetingRepo()
		for _, a := range []string{"acc-1", "acc-2", "acc-3", "acc-4"} {
			base.addMember(a, "member", model.RoleSA)
		}
		base.membershipIndexLag = true // candidates come only from ordered hints
		repo := newLatencyMeetingRepo(base, time.Millisecond)
		repo.slow = map[string]time.Duration{}
		return repo
	}
	hints := []string{"acc-1", "acc-2", "acc-3", "acc-4"}
	t.Run("sorted result", func(t *testing.T) {
		repo := build()
		ids, err := listReadableAccountIDs(context.Background(), repo, "member", []string{"acc-3", "acc-1", "acc-4", "acc-2", "acc-1"})
		if err != nil || strings.Join(ids, ",") != "acc-1,acc-2,acc-3,acc-4" {
			t.Fatalf("ids=%v err=%v", ids, err)
		}
		if repo.calls["GetMember"] != 8 || repo.calls["GetAccount"] != 4 {
			t.Fatalf("calls [%s]", repo.summary())
		}
	})
	t.Run("first account read error in candidate order", func(t *testing.T) {
		repo := build()
		errFirst, errLater := errors.New("acc-2 read"), errors.New("acc-4 read")
		repo.accountErr = map[string]error{"acc-2": errFirst, "acc-4": errLater}
		repo.slow["acc-2"] = 30 * time.Millisecond
		if _, err := listReadableAccountIDs(context.Background(), repo, "member", hints); !errors.Is(err, errFirst) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("malformed candidate stops before later reads", func(t *testing.T) {
		repo := build()
		if _, err := listReadableAccountIDs(context.Background(), repo, "member", []string{"acc-1", "bad/id", "acc-2"}); !errors.Is(err, ErrReadingData) {
			t.Fatalf("err = %v", err)
		}
		if repo.memberCalls["acc-2"] != 0 {
			t.Fatalf("read a candidate after the malformed one: [%s]", repo.summary())
		}
	})
	t.Run("changed grant is stale", func(t *testing.T) {
		repo := build()
		repo.memberGoneAt = map[string]int{"acc-3": 2}
		if _, err := listReadableAccountIDs(context.Background(), repo, "member", hints); !errors.Is(err, ErrStaleReadingCursor) {
			t.Fatalf("err = %v", err)
		}
	})
}
