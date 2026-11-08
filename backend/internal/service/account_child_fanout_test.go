package service

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

const fanoutUser = "reader"

// childFanoutRepo serves a fixed account tree. Child pages are keyed by
// parent and page index (the cursor is the decimal index). It records the
// first-touch order of every account and membership read, which is the order
// in which the walk validated and added accounts, and the critical path of
// child-page reads: a call's depth is one more than the deepest call that had
// completed when it started, so sequential calls form a chain.
type childFanoutRepo struct {
	accounts map[string]*model.Account
	direct   map[string]bool
	pages    map[string][][]string
	pageErr  map[string]error   // "parent#page"
	nextOf   map[string]*string // "parent#page" overrides the continuation
	endless  map[string]bool    // parents whose pages never end
	delay    time.Duration

	mu           sync.Mutex
	childCalls   int
	completedMax int
	maxDepth     int
	inFlight     int
	peak         int
	memberSeen   map[string]bool
	memberOrder  []string
	accountSeen  map[string]bool
	accountOrder []string
}

func newChildFanoutRepo() *childFanoutRepo {
	return &childFanoutRepo{
		accounts: map[string]*model.Account{}, direct: map[string]bool{}, pages: map[string][][]string{},
		pageErr: map[string]error{}, nextOf: map[string]*string{}, endless: map[string]bool{},
		memberSeen: map[string]bool{}, accountSeen: map[string]bool{},
	}
}

func (r *childFanoutRepo) account(id, parent string) {
	r.accounts[id] = &model.Account{PK: model.PrefixAccount + id, SK: model.SKAccountMeta, EntityType: model.EntityTypeAccount, AccountID: id, ParentAccountID: parent}
}

// root adds a directly held account; tree adds canonical children under it.
func (r *childFanoutRepo) root(id string) {
	r.account(id, "")
	r.direct[id] = true
}

func (r *childFanoutRepo) child(parent string, ids ...string) {
	for _, id := range ids {
		r.account(id, parent)
	}
	r.pages[parent] = append(r.pages[parent], ids)
}

func (r *childFanoutRepo) GetAccount(_ context.Context, id string) (*model.Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.accountSeen[id] {
		r.accountSeen[id] = true
		r.accountOrder = append(r.accountOrder, id)
	}
	return cloneAccount(r.accounts[id]), nil
}

func (r *childFanoutRepo) GetMember(_ context.Context, accountID, userID string) (*model.AccountMember, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.memberSeen[accountID] {
		r.memberSeen[accountID] = true
		r.memberOrder = append(r.memberOrder, accountID)
	}
	return r.member(accountID, userID), nil
}

func (r *childFanoutRepo) member(accountID, userID string) *model.AccountMember {
	if !r.direct[accountID] || userID != fanoutUser {
		return nil
	}
	return &model.AccountMember{PK: model.PrefixAccount + accountID, SK: model.PrefixMember + userID,
		EntityType: model.EntityTypeAccountMember, AccountID: accountID, UserID: userID, Role: model.RoleSA}
}

func (r *childFanoutRepo) ListChildAccountIDsPage(ctx context.Context, parent, cursor string, limit int32) ([]string, *string, error) {
	r.mu.Lock()
	r.childCalls++
	depth := r.completedMax + 1
	r.maxDepth = max(r.maxDepth, depth)
	r.inFlight++
	r.peak = max(r.peak, r.inFlight)
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.inFlight--
		r.completedMax = max(r.completedMax, depth)
		r.mu.Unlock()
	}()
	if r.delay > 0 {
		select {
		case <-time.After(r.delay):
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
	}
	if limit != 100 {
		return nil, nil, fmt.Errorf("unexpected limit %d", limit)
	}
	page := 0
	if cursor != "" {
		n, err := strconv.Atoi(cursor)
		if err != nil {
			return nil, nil, err
		}
		page = n
	}
	key := parent + "#" + strconv.Itoa(page)
	if err := r.pageErr[key]; err != nil {
		return nil, nil, err
	}
	if next, ok := r.nextOf[key]; ok {
		return r.pageIDs(parent, page), next, nil
	}
	if r.endless[parent] {
		next := strconv.Itoa(page + 1)
		return []string{}, &next, nil
	}
	ids := r.pageIDs(parent, page)
	if page+1 < len(r.pages[parent]) {
		next := strconv.Itoa(page + 1)
		return ids, &next, nil
	}
	return ids, nil, nil
}

func (r *childFanoutRepo) pageIDs(parent string, page int) []string {
	if page < len(r.pages[parent]) {
		return append([]string{}, r.pages[parent][page]...)
	}
	return []string{}
}

// batchedChildFanoutRepo adds the navigation and batch primitives, so the walk
// runs through the request-scoped discoveryReadCache as in production.
type batchedChildFanoutRepo struct{ *childFanoutRepo }

func (r *batchedChildFanoutRepo) QueryNavigationCandidates(context.Context, string, string, string, string, int) (*repository.NavigationCandidatePage, error) {
	return &repository.NavigationCandidatePage{IDs: []string{}}, nil
}

func (r *batchedChildFanoutRepo) BatchGetAccountRows(_ context.Context, keys []repository.AccountRowKey) (*repository.AccountRows, error) {
	out := &repository.AccountRows{Accounts: map[string]*model.Account{}, Members: map[repository.AccountRowKey]*model.AccountMember{}}
	for _, key := range keys {
		if key.UserID == "" {
			if a := r.accounts[key.AccountID]; a != nil {
				out.Accounts[key.AccountID] = cloneAccount(a)
			}
		} else if m := r.member(key.AccountID, key.UserID); m != nil {
			out.Members[key] = m
		}
	}
	return out, nil
}

// walkResult is everything observable about one listReadableAccountIDs call.
type walkResult struct {
	ids          []string
	err          error
	childCalls   int
	maxDepth     int
	peak         int
	memberOrder  []string
	accountOrder []string
}

func runChildWalk(t *testing.T, build func() *childFanoutRepo, hints []string, fanout int, batched bool) walkResult {
	t.Helper()
	r := build()
	var repo accountReadMembershipRepo = r
	if batched {
		repo = &batchedChildFanoutRepo{r}
	}
	ids, err := listReadableAccountIDsWith(context.Background(), repo, fanoutUser, hints, fanout)
	hinted := map[string]bool{}
	for _, h := range hints {
		hinted[h] = true
	}
	// Hint candidates are prefetched concurrently before the walk and grants
	// are rechecked concurrently after it; first touches of every other ID
	// happen inside the walk, in its validation and add order.
	filter := func(order []string) []string {
		out := []string{}
		for _, id := range order {
			if !hinted[id] {
				out = append(out, id)
			}
		}
		return out
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return walkResult{ids: ids, err: err, childCalls: r.childCalls, maxDepth: r.maxDepth, peak: r.peak,
		memberOrder: filter(r.memberOrder), accountOrder: filter(r.accountOrder)}
}

func numberedIDs(prefix string, n int) []string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("%s-%03d", prefix, i)
	}
	return ids
}

// wideRoots is the production shape: n directly held accounts, no children.
func wideRoots(n int) (func() *childFanoutRepo, []string) {
	ids := numberedIDs("root", n)
	return func() *childFanoutRepo {
		r := newChildFanoutRepo()
		for _, id := range ids {
			r.root(id)
		}
		return r
	}, ids
}

func TestChildDiscoveryCriticalPath(t *testing.T) {
	t.Setenv(accountDescendantReadsEnv, "1")
	flat, flatHints := wideRoots(30)
	tree := func() *childFanoutRepo {
		r := newChildFanoutRepo()
		r.root("root")
		r.child("root", numberedIDs("kid", 29)...)
		return r
	}
	cases := []struct {
		name                      string
		build                     func() *childFanoutRepo
		hints                     []string
		calls, seqDepth, maxDepth int
	}{
		// 30 readable accounts: one child page each, 4 rounds of 8 instead of 30.
		{"30 direct roots", flat, flatHints, 30, 30, 5},
		// Two levels: 1 root page, then 29 child pages in 4 rounds.
		{"root with 29 children", tree, []string{"root"}, 30, 30, 6},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			build := func() *childFanoutRepo { r := tc.build(); r.delay = 5 * time.Millisecond; return r }
			started := time.Now()
			seq := runChildWalk(t, build, tc.hints, 0, false)
			seqTime := time.Since(started)
			started = time.Now()
			par := runChildWalk(t, build, tc.hints, listReadConcurrency, false)
			parTime := time.Since(started)
			if seq.err != nil || par.err != nil || !reflect.DeepEqual(seq.ids, par.ids) || len(par.ids) != 30 {
				t.Fatalf("seq=%v/%v par=%v/%v", seq.ids, seq.err, par.ids, par.err)
			}
			if seq.childCalls != tc.calls || par.childCalls != tc.calls {
				t.Fatalf("child page queries seq=%d par=%d, want %d", seq.childCalls, par.childCalls, tc.calls)
			}
			if seq.maxDepth != tc.seqDepth || seq.peak != 1 {
				t.Fatalf("baseline depth=%d peak=%d, want %d/1", seq.maxDepth, seq.peak, tc.seqDepth)
			}
			if par.maxDepth > tc.maxDepth || par.peak > listReadConcurrency {
				t.Fatalf("fan-out depth=%d peak=%d, want <=%d/<=%d", par.maxDepth, par.peak, tc.maxDepth, listReadConcurrency)
			}
			t.Logf("child page depth %d -> %d (peak %d); wall %v -> %v", seq.maxDepth, par.maxDepth, par.peak, seqTime, parTime)
		})
	}
}

func TestChildDiscoveryMatchesSequentialWalk(t *testing.T) {
	t.Setenv(accountDescendantReadsEnv, "1")
	errA, errB := errors.New("child page a"), errors.New("child page b")
	loop := "1"
	empty := ""
	cases := []struct {
		name  string
		build func() *childFanoutRepo
		hints []string
		want  error // nil: success
	}{
		{"multi-level multi-page tree", func() *childFanoutRepo {
			r := newChildFanoutRepo()
			r.root("root")
			r.child("root", "b", "a")
			r.child("root", "d", "c")
			r.child("a", "a2", "a1")
			r.child("c", "c1")
			r.child("c", "c2")
			r.child("b", numberedIDs("b", 100)...)
			r.child("b", "b-tail")
			r.child("a1", "a1x")
			return r
		}, []string{"root"}, nil},
		{"cycles, duplicates and non-canonical children", func() *childFanoutRepo {
			r := newChildFanoutRepo()
			r.root("root")
			r.root("other")
			for id, parent := range map[string]string{"x": "root", "y": "root", "z": "x", "w": "z", "o1": "other"} {
				r.account(id, parent)
			}
			// Raw index pages: duplicates within and across pages and parents,
			// back edges to ancestors and roots, a self edge, a child listed
			// under the wrong parent and one with no META row.
			r.pages["root"] = [][]string{{"x", "y", "x", "root"}, {"y", "other", "z"}}
			r.pages["x"] = [][]string{{"z", "root", "x"}}
			r.pages["y"] = [][]string{{"z", "x", "ghost"}}
			r.pages["z"] = [][]string{{"w"}}
			r.pages["w"] = [][]string{{"z", "root", "w"}}
			r.pages["other"] = [][]string{{"o1"}}
			r.pages["o1"] = [][]string{{"other", "o1", "x"}}
			return r
		}, []string{"root", "other"}, nil},
		{"directly held descendant is not revisited", func() *childFanoutRepo {
			r := newChildFanoutRepo()
			r.root("root")
			r.child("root", "held", "plain")
			r.direct["held"] = true
			r.child("held", "deep")
			return r
		}, []string{"held", "root"}, nil},
		{"first error in level order wins", func() *childFanoutRepo {
			r := newChildFanoutRepo()
			for _, id := range numberedIDs("r", 12) {
				r.root(id)
			}
			r.pageErr["r-009#0"] = errB
			r.pageErr["r-004#0"] = errA
			return r
		}, numberedIDs("r", 12), errA},
		{"earlier parent's malformed child beats later page error", func() *childFanoutRepo {
			r := newChildFanoutRepo()
			for _, id := range numberedIDs("r", 6) {
				r.root(id)
			}
			r.pages["r-001"] = [][]string{{"bad/id"}}
			r.pageErr["r-003#0"] = errA
			return r
		}, numberedIDs("r", 6), ErrReadingData},
		{"earlier continuation error beats later first page error", func() *childFanoutRepo {
			r := newChildFanoutRepo()
			for _, id := range numberedIDs("r", 6) {
				r.root(id)
			}
			r.child("r-000", "k0")
			r.child("r-000", "k1")
			r.pageErr["r-000#1"] = errB
			r.pageErr["r-002#0"] = errA
			return r
		}, numberedIDs("r", 6), errB},
		{"error in the middle of a deeper level", func() *childFanoutRepo {
			r := newChildFanoutRepo()
			r.root("root")
			r.child("root", "a", "b", "c", "d")
			r.child("a", "a1")
			r.pageErr["c#0"] = errA
			r.pageErr["d#0"] = errB
			return r
		}, []string{"root"}, errA},
		{"oversized child page", func() *childFanoutRepo {
			r := newChildFanoutRepo()
			r.root("root")
			r.root("later")
			r.pages["root"] = [][]string{numberedIDs("k", 101)}
			r.pageErr["later#0"] = errA
			return r
		}, []string{"root", "later"}, ErrReadingData},
		{"readable account limit", func() *childFanoutRepo {
			r := newChildFanoutRepo()
			r.root("root")
			ids := numberedIDs("k", maxReadableAccounts)
			for i := 0; i < len(ids); i += 100 {
				r.child("root", ids[i:i+100]...)
			}
			return r
		}, []string{"root"}, ErrReadingData},
		{"readable account limit exactly met", func() *childFanoutRepo {
			r := newChildFanoutRepo()
			r.root("root")
			ids := numberedIDs("k", maxReadableAccounts-1)
			for i := 0; i < len(ids); i += 100 {
				r.child("root", ids[i:min(i+100, len(ids))]...)
			}
			return r
		}, []string{"root"}, nil},
		{"page limit", func() *childFanoutRepo {
			r := newChildFanoutRepo()
			r.root("root")
			r.root("ok")
			r.endless["root"] = true
			return r
		}, []string{"root", "ok"}, ErrReadingData},
		{"repeated cursor", func() *childFanoutRepo {
			r := newChildFanoutRepo()
			r.root("root")
			r.child("root", "k0")
			r.nextOf["root#0"] = &loop
			r.nextOf["root#1"] = &loop
			return r
		}, []string{"root"}, ErrReadingData},
		{"cursor revisits an earlier cursor", func() *childFanoutRepo {
			r := newChildFanoutRepo()
			r.root("root")
			two := "2"
			r.nextOf["root#0"] = &loop
			r.nextOf["root#1"] = &two
			r.nextOf["root#2"] = &loop
			return r
		}, []string{"root"}, ErrReadingData},
		{"empty continuation", func() *childFanoutRepo {
			r := newChildFanoutRepo()
			r.root("root")
			r.nextOf["root#0"] = &empty
			return r
		}, []string{"root"}, ErrReadingData},
	}
	for _, tc := range cases {
		for _, batched := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/batched=%v", tc.name, batched), func(t *testing.T) {
				seq := runChildWalk(t, tc.build, tc.hints, 0, batched)
				par := runChildWalk(t, tc.build, tc.hints, listReadConcurrency, batched)
				if tc.want == nil && seq.err != nil {
					t.Fatalf("reference walk failed: %v", seq.err)
				}
				if tc.want != nil && !errors.Is(seq.err, tc.want) {
					t.Fatalf("reference err = %v, want %v", seq.err, tc.want)
				}
				if seq.err != par.err || !reflect.DeepEqual(seq.ids, par.ids) {
					t.Fatalf("result differs: seq=%v/%v par=%v/%v", seq.ids, seq.err, par.ids, par.err)
				}
				if !batched {
					// Membership first touches are the add (seen) order and account
					// first touches the child validation order.
					if !reflect.DeepEqual(seq.memberOrder, par.memberOrder) || !reflect.DeepEqual(seq.accountOrder, par.accountOrder) {
						t.Fatalf("walk order differs:\nseq members %v\npar members %v\nseq accounts %v\npar accounts %v",
							seq.memberOrder, par.memberOrder, seq.accountOrder, par.accountOrder)
					}
				}
				if tc.want == nil && seq.childCalls != par.childCalls {
					t.Fatalf("child page queries seq=%d par=%d", seq.childCalls, par.childCalls)
				}
			})
		}
	}
}

func TestChildDiscoveryOrderingFixture(t *testing.T) {
	t.Setenv(accountDescendantReadsEnv, "1")
	build := func() *childFanoutRepo {
		r := newChildFanoutRepo()
		r.root("root")
		r.child("root", "b", "a")
		r.child("root", "c")
		r.child("b", "b2", "b1")
		r.child("a", "a1")
		return r
	}
	got := runChildWalk(t, build, []string{"root"}, listReadConcurrency, false)
	if got.err != nil || strings.Join(got.ids, ",") != "a,a1,b,b1,b2,c,root" {
		t.Fatalf("ids=%v err=%v", got.ids, got.err)
	}
	// BFS add order: level 1 in page order, then each parent's children in
	// parent order, regardless of which child page returned first.
	if strings.Join(got.memberOrder, ",") != "b,a,c,b2,b1,a1" {
		t.Fatalf("add order = %v", got.memberOrder)
	}
}

func TestChildDiscoveryRequiresDescendantReads(t *testing.T) {
	t.Setenv(accountDescendantReadsEnv, "")
	build := func() *childFanoutRepo {
		r := newChildFanoutRepo()
		r.root("root")
		r.child("root", "kid")
		return r
	}
	got := runChildWalk(t, build, []string{"root"}, listReadConcurrency, true)
	if got.err != nil || strings.Join(got.ids, ",") != "root" || got.childCalls != 0 {
		t.Fatalf("ids=%v err=%v child calls=%d", got.ids, got.err, got.childCalls)
	}
}
