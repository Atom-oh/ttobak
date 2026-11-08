package service

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// projectListCountingRepo counts ListMyProjects' reads by the DynamoDB
// operation the production repository issues for each method.
type projectListCountingRepo struct {
	*mockProjectRepo

	mu                                      sync.Mutex
	query, getItem, batchGet                int
	getProject, getProjectMember, getMember int
	getAccount, accountBatches              int
	discovery                               [3]int // query, getItem, batchGet at the first candidate batch
	inCandidates                            bool
	memberErr, accountErr, projectErr       map[string]error
	beforeAccountBatch                      func(call int) // called unlocked
}

func (r *projectListCountingRepo) count(op *int, method *int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	*op++
	if method != nil {
		*method++
	}
}

func (r *projectListCountingRepo) totals() [3]int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return [3]int{r.query, r.getItem, r.batchGet}
}

func (r *projectListCountingRepo) ListProjectsForUser(ctx context.Context, user string) ([]model.Project, error) {
	r.count(&r.query, nil)
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.mockProjectRepo.ListProjectsForUser(ctx, user)
}

func (r *projectListCountingRepo) ListAccountsForUser(ctx context.Context, user string) ([]model.AccountMember, error) {
	r.count(&r.query, nil)
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.mockProjectRepo.ListAccountsForUser(ctx, user)
}

func (r *projectListCountingRepo) ListProjectRefsForAccount(ctx context.Context, account string) ([]model.ProjectRef, error) {
	r.count(&r.query, nil)
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.mockProjectRepo.ListProjectRefsForAccount(ctx, account)
}

func (r *projectListCountingRepo) ListChildAccountIDsPage(_ context.Context, parent, _ string, _ int32) ([]string, *string, error) {
	r.count(&r.query, nil)
	r.mu.Lock()
	defer r.mu.Unlock()
	ids := []string{}
	for id, a := range r.accounts {
		if a.ParentAccountID == parent {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids, nil, nil
}

func (r *projectListCountingRepo) GetProject(ctx context.Context, id string) (*model.Project, error) {
	r.count(&r.getItem, &r.getProject)
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.projectErr[id]; err != nil {
		return nil, err
	}
	return r.mockProjectRepo.GetProject(ctx, id)
}

func (r *projectListCountingRepo) GetProjectMember(ctx context.Context, id, user string) (*model.ProjectMember, error) {
	r.count(&r.getItem, &r.getProjectMember)
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.mockProjectRepo.GetProjectMember(ctx, id, user)
}

func (r *projectListCountingRepo) GetMember(ctx context.Context, account, user string) (*model.AccountMember, error) {
	r.count(&r.getItem, &r.getMember)
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.memberErr[account]; err != nil {
		return nil, err
	}
	return r.mockProjectRepo.GetMember(ctx, account, user)
}

func (r *projectListCountingRepo) GetAccount(ctx context.Context, account string) (*model.Account, error) {
	r.count(&r.getItem, &r.getAccount)
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.accountErr[account]; err != nil {
		return nil, err
	}
	return r.mockProjectRepo.GetAccount(ctx, account)
}

// projectListBatchRepo adds the production repository's consistent batch
// primitives and account navigation index over the same rows; with them the
// repository has the full discovery capability set.
type projectListBatchRepo struct{ *projectListCountingRepo }

func (r *projectListBatchRepo) QueryNavigationCandidates(_ context.Context, user, kind, account, after string, limit int) (*repository.NavigationCandidatePage, error) {
	r.count(&r.query, nil)
	if kind != "accounts" || account != "" {
		return nil, errors.New("unexpected navigation kind")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	ids := []string{}
	for _, m := range r.accountMembers {
		if m.UserID == user && m.AccountID > after {
			ids = append(ids, m.AccountID)
		}
	}
	sort.Strings(ids)
	page := &repository.NavigationCandidatePage{IDs: ids}
	if len(ids) > limit {
		page.IDs, page.NextID = ids[:limit], ids[limit-1]
	}
	return page, nil
}

func (r *projectListBatchRepo) BatchGetAccountRows(ctx context.Context, keys []repository.AccountRowKey) (*repository.AccountRows, error) {
	r.mu.Lock()
	r.accountBatches++
	call, hook := r.accountBatches, r.beforeAccountBatch
	r.mu.Unlock()
	if hook != nil {
		hook(call)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.batchGet += (len(keys) + 99) / 100
	out := &repository.AccountRows{Accounts: map[string]*model.Account{}, Members: map[repository.AccountRowKey]*model.AccountMember{}}
	for _, key := range keys {
		if key.UserID == "" {
			if err := r.accountErr[key.AccountID]; err != nil {
				return nil, err
			}
			if a, _ := r.mockProjectRepo.GetAccount(ctx, key.AccountID); a != nil {
				out.Accounts[key.AccountID] = a
			}
		} else {
			if err := r.memberErr[key.AccountID]; err != nil {
				return nil, err
			}
			if m, _ := r.mockProjectRepo.GetMember(ctx, key.AccountID, key.UserID); m != nil {
				out.Members[key] = m
			}
		}
	}
	return out, nil
}

func (r *projectListBatchRepo) BatchGetProjectListRows(_ context.Context, ids []string) (map[string]*model.Project, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.inCandidates {
		r.inCandidates = true
		r.discovery = [3]int{r.query, r.getItem, r.batchGet}
	}
	r.batchGet += (len(ids) + 99) / 100
	out := map[string]*model.Project{}
	for _, id := range ids {
		if err := r.projectErr[id]; err != nil {
			return nil, err
		}
		if p := r.projects[id]; p != nil {
			out[id] = cloneProject(p)
		}
	}
	return out, nil
}

func treeMember(base *mockProjectRepo, id string) {
	base.accountMembers[projectAccountMemberKey(id, "reader")] = &model.AccountMember{PK: "ACCOUNT#" + id, SK: "MEMBER#reader",
		AccountID: id, UserID: "reader", Role: model.RoleOwner, EntityType: model.EntityTypeAccountMember}
}

// projectListTreeFixture: reader owns "own" and holds direct memberships on
// root and on four separate roots d1..d4. root has five children a1..a5, each
// with four children, so 30 accounts are readable (25 by inheritance). Twenty
// candidate projects p00..p19 are linked to leaves or to d accounts; p00, p03,
// p06, p09 and p18 are also linked first to the unreadable account x.
// "stale" has a reverse ref but its canonical link moved to x, and "deleted"
// has a reverse ref but no canonical row.
func projectListTreeFixture() *projectListCountingRepo {
	base := newMockProjectRepo()
	account := func(id, parent string) {
		base.accounts[id] = &model.Account{PK: "ACCOUNT#" + id, SK: "META", AccountID: id, ParentAccountID: parent, EntityType: model.EntityTypeAccount}
	}
	account("root", "")
	treeMember(base, "root")
	for i := 1; i <= 5; i++ {
		mid := fmt.Sprintf("a%d", i)
		account(mid, "root")
		for j := 1; j <= 4; j++ {
			account(fmt.Sprintf("a%d-%d", i, j), mid)
		}
	}
	for i := 1; i <= 4; i++ {
		account(fmt.Sprintf("d%d", i), "")
		treeMember(base, fmt.Sprintf("d%d", i))
	}
	account("x", "")
	seedProject(base, "own", "reader")
	// link records the canonical links and a reverse ref under the last one.
	link := func(id string, accounts ...string) {
		seedProject(base, id, "owner").AccountIDs = accounts
		ref := accounts[len(accounts)-1]
		base.projectRefs[ref] = append(base.projectRefs[ref], model.ProjectRef{ProjectID: id, AccountID: ref})
	}
	for n := 0; n < 20; n++ {
		id, leaf := fmt.Sprintf("p%02d", n), fmt.Sprintf("a%d-%d", n%5+1, n%4+1)
		switch {
		case n%5 == 4:
			link(id, fmt.Sprintf("d%d", n/5+1))
		case n%3 == 0:
			link(id, "x", leaf)
		default:
			link(id, leaf)
		}
	}
	seedProject(base, "stale", "owner").AccountIDs = []string{"x"}
	base.projectRefs["a1-1"] = append(base.projectRefs["a1-1"], model.ProjectRef{ProjectID: "stale"}, model.ProjectRef{ProjectID: "deleted"})
	return &projectListCountingRepo{mockProjectRepo: base}
}

func treeRepo(r *projectListCountingRepo, batched bool) projectRepo {
	if batched {
		return &projectListBatchRepo{r}
	}
	return r
}

// treeProjects lists the expected projects in discovery order: owned first,
// then by readable account in sorted order and reverse-ref order within it.
func treeProjects(keep func(id string) bool) string {
	byAccount := map[string][]string{}
	for n := 0; n < 20; n++ {
		account := fmt.Sprintf("a%d-%d", n%5+1, n%4+1)
		if n%5 == 4 {
			account = fmt.Sprintf("d%d", n/5+1)
		}
		byAccount[account] = append(byAccount[account], fmt.Sprintf("p%02d", n))
	}
	accounts := make([]string, 0, len(byAccount))
	for a := range byAccount {
		accounts = append(accounts, a)
	}
	sort.Strings(accounts)
	out := []string{"own"}
	for _, a := range accounts {
		for _, id := range byAccount[a] {
			if keep == nil || keep(id) {
				out = append(out, id)
			}
		}
	}
	return strings.Join(out, ",")
}

func inheritedTreeProject(id string) bool {
	var n int
	fmt.Sscanf(id, "p%02d", &n)
	return n%5 != 4
}

func TestListMyProjectsCandidateReadCounts(t *testing.T) {
	t.Setenv(accountDescendantReadsEnv, "1")
	for _, tc := range []struct {
		name    string
		batched bool
		// Upper bounds on the whole request's reads.
		maxQuery, maxGetItem, maxBatch int
	}{
		// Discovery (unchanged): 30 child listings + 30 reverse-ref queries +
		// project and account index queries, 8 BatchGetItem. Candidates: no
		// GetItem (was 286); one project batch, three prime levels and three
		// recheck levels. Before: Query 62, GetItem 286, BatchGetItem 9.
		{"batch-capable repository", true, 62, 0, 15},
		// Per item: each distinct account/ancestor row once per phase plus
		// one project row each, instead of every candidate rereading them.
		// Concurrent first misses may read a row twice. Before: Query 62,
		// GetItem 653 (345 of them discovery); after about 461.
		{"per-item repository", false, 62, 500, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := projectListTreeFixture()
			projects, err := newProjectServiceWithRepo(treeRepo(r, tc.batched)).ListMyProjects(context.Background(), "reader")
			if err != nil {
				t.Fatal(err)
			}
			if got, want := strings.Join(projectIDs(projects), ","), treeProjects(nil); got != want {
				t.Fatalf("projects = %s\nwant %s", got, want)
			}
			total, disc := r.totals(), r.discovery
			t.Logf("total Query %d GetItem %d BatchGetItem %d; discovery Query %d GetItem %d BatchGetItem %d; "+
				"after discovery GetItem %d BatchGetItem %d (GetProject %d GetProjectMember %d GetMember %d GetAccount %d)",
				total[0], total[1], total[2], disc[0], disc[1], disc[2], total[1]-disc[1], total[2]-disc[2],
				r.getProject, r.getProjectMember, r.getMember, r.getAccount)
			if total[0] > tc.maxQuery || total[1] > tc.maxGetItem || total[2] > tc.maxBatch {
				t.Fatalf("reads Query %d GetItem %d BatchGetItem %d exceed %d/%d/%d",
					total[0], total[1], total[2], tc.maxQuery, tc.maxGetItem, tc.maxBatch)
			}
		})
	}
}

func TestListMyProjectsCandidateAuthorization(t *testing.T) {
	t.Setenv(accountDescendantReadsEnv, "1")
	boom := errors.New("transient read failure")
	for _, tc := range []struct {
		name  string
		setup func(*projectListCountingRepo)
		want  string // expected order; empty when an error is expected
		err   error
	}{
		{"stale and deleted reverse refs stay excluded", nil, treeProjects(nil), nil},
		{"revoked canonical link excluded", func(r *projectListCountingRepo) {
			r.projects["p01"].AccountIDs = []string{"x"}
			r.projects["p03"].AccountIDs = []string{"x"}
		}, treeProjects(func(id string) bool { return id != "p01" && id != "p03" }), nil},
		{"ancestor membership removed excludes its subtree", func(r *projectListCountingRepo) {
			delete(r.accountMembers, projectAccountMemberKey("root", "reader"))
			treeMember(r.mockProjectRepo, "a2") // only a2's subtree stays inherited
		}, treeProjects(func(id string) bool {
			var n int
			fmt.Sscanf(id, "p%02d", &n)
			return !inheritedTreeProject(id) || n%5 == 1
		}), nil},
		{"mid-tree parent edge broken excludes its subtree", func(r *projectListCountingRepo) {
			r.accounts["a3"].ParentAccountID = ""
		}, treeProjects(func(id string) bool {
			var n int
			fmt.Sscanf(id, "p%02d", &n)
			return !inheritedTreeProject(id) || n%5 != 2
		}), nil},
		{"unreadable linked account read error fails closed", func(r *projectListCountingRepo) {
			r.memberErr = map[string]error{"x": boom}
		}, "", boom},
		{"ancestor row read error fails closed", func(r *projectListCountingRepo) {
			r.accountErr = map[string]error{"a4-2": boom}
		}, "", boom},
	} {
		for _, batched := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/batched=%v", tc.name, batched), func(t *testing.T) {
				r := projectListTreeFixture()
				if tc.setup != nil {
					tc.setup(r)
				}
				projects, err := newProjectServiceWithRepo(treeRepo(r, batched)).ListMyProjects(context.Background(), "reader")
				if tc.err != nil {
					if !errors.Is(err, tc.err) || projects != nil {
						t.Fatalf("got %v / %v, want error %v", projectIDs(projects), err, tc.err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if got := strings.Join(projectIDs(projects), ","); got != tc.want {
					t.Fatalf("projects = %s\nwant %s", got, tc.want)
				}
			})
		}
	}
}

// listCandidateAccess decides from the held canonical row: owner, a linked
// account grant, else a direct project membership, failing closed on errors.
func TestListCandidateAccess(t *testing.T) {
	t.Setenv(accountDescendantReadsEnv, "1")
	boom := errors.New("project member read failed")
	for _, tc := range []struct {
		name      string
		project   model.Project
		member    bool
		memberErr bool
		readable  bool
		grant     string
		err       error
	}{
		{"owner", model.Project{OwnerUserID: "reader", AccountIDs: []string{"x"}}, false, false, true, "", nil},
		{"inherited grant on a later link", model.Project{OwnerUserID: "o", AccountIDs: []string{"x", "a2-3"}}, false, false, true, "a2-3", nil},
		{"direct project member without a grant", model.Project{OwnerUserID: "o", AccountIDs: []string{"x"}}, true, false, true, "", nil},
		{"no access", model.Project{OwnerUserID: "o", AccountIDs: []string{"x", "missing"}}, false, false, false, "", nil},
		{"project member read error", model.Project{OwnerUserID: "o", AccountIDs: []string{"x"}}, false, true, false, "", boom},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := projectListTreeFixture()
			var repo projectRepo = r
			if tc.member {
				r.projectMembers[projectMemberKey("p", "reader")] = &model.ProjectMember{ProjectID: "p", UserID: "reader"}
			}
			if tc.memberErr {
				repo = &failingProjectMemberRepo{r, boom}
			}
			s := newProjectServiceWithRepo(repo)
			ok, grant, err := s.listCandidateAccess(context.Background(), newAccountRowMemo(repo), "reader", "p", &tc.project)
			if ok != tc.readable || !errors.Is(err, tc.err) || (grant == nil) != (tc.grant == "") || (grant != nil && grant.AccountID != tc.grant) {
				t.Fatalf("got %v %+v %v", ok, grant, err)
			}
			if tc.grant != "" && r.getProjectMember != 0 {
				t.Fatal("a granted candidate also read its project membership")
			}
		})
	}
}

type failingProjectMemberRepo struct {
	*projectListCountingRepo
	err error
}

func (r *failingProjectMemberRepo) GetProjectMember(context.Context, string, string) (*model.ProjectMember, error) {
	return nil, r.err
}

// The candidate memo serves one chunk; grants are then rechecked against a
// fresh view. A membership removed after verification but before the recheck
// still excludes every project it granted, and a recheck read failure fails
// the listing.
func TestListMyProjectsCandidateRecheckReadsCurrentRows(t *testing.T) {
	t.Setenv(accountDescendantReadsEnv, "1")
	dry := projectListTreeFixture()
	if _, err := newProjectServiceWithRepo(treeRepo(dry, true)).ListMyProjects(context.Background(), "reader"); err != nil {
		t.Fatal(err)
	}
	// The recheck primes the leaf, mid and root levels: the last three calls.
	recheck := dry.accountBatches - 2
	if recheck <= 3 {
		t.Fatalf("unexpected account batch sequence: %d calls", dry.accountBatches)
	}
	t.Run("membership removed before recheck", func(t *testing.T) {
		r := projectListTreeFixture()
		r.beforeAccountBatch = func(call int) {
			if call == recheck {
				r.mu.Lock()
				delete(r.accountMembers, projectAccountMemberKey("root", "reader"))
				r.mu.Unlock()
			}
		}
		projects, err := newProjectServiceWithRepo(treeRepo(r, true)).ListMyProjects(context.Background(), "reader")
		if err != nil {
			t.Fatal(err)
		}
		want := treeProjects(func(id string) bool { return !inheritedTreeProject(id) })
		if got := strings.Join(projectIDs(projects), ","); got != want {
			t.Fatalf("projects = %s\nwant %s", got, want)
		}
	})
	t.Run("recheck read failure", func(t *testing.T) {
		r := projectListTreeFixture()
		boom := errors.New("recheck read failed")
		r.beforeAccountBatch = func(call int) {
			if call == recheck {
				r.mu.Lock()
				r.accountErr = map[string]error{"a1": boom}
				r.mu.Unlock()
			}
		}
		if projects, err := newProjectServiceWithRepo(treeRepo(r, true)).ListMyProjects(context.Background(), "reader"); !errors.Is(err, boom) || projects != nil {
			t.Fatalf("got %v / %v", projectIDs(projects), err)
		}
	})
}

// A grant that changed between verification and recheck falls back to the
// uncached per-candidate check, so a still-readable candidate is kept.
func TestRecheckListCandidatesFallsBackOnChangedGrant(t *testing.T) {
	t.Setenv(accountDescendantReadsEnv, "1")
	r := projectListTreeFixture()
	s := newProjectServiceWithRepo(r)
	grant, err := resolveAccountReadGrant(context.Background(), r, "a1-1", "reader")
	if err != nil || grant == nil {
		t.Fatalf("grant: %v / %v", grant, err)
	}
	stale := *grant
	stale.InheritedFromAccountID = "a1" // no longer matches the current grant
	ids := []string{"p00", "p05", "p10"}
	r.projects["p10"].AccountIDs = []string{"x"} // not readable when rechecked
	grants := []*accountReadGrant{grant, &stale, &stale}
	readable := []bool{true, true, true}
	if err := s.recheckListCandidates(context.Background(), "reader", ids, grants, readable, len(ids), nil); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(readable, []bool{true, true, false}) {
		t.Fatalf("readable = %v", readable)
	}
	// requireProjectReadAccess reads the project twice per changed candidate.
	if r.getProject != 4 {
		t.Fatalf("fallback reads: GetProject %d, want the uncached path for exactly two candidates", r.getProject)
	}
}

// First error in discovery order, for verification and recheck failures alike.
func TestListMyProjectsCandidateErrorOrder(t *testing.T) {
	t.Setenv(accountDescendantReadsEnv, "1")
	first, later := errors.New("first candidate"), errors.New("later candidate")
	r := projectListTreeFixture()
	order := strings.Split(treeProjects(nil), ",")
	r.projectErr = map[string]error{order[3]: first, order[15]: later}
	if _, err := newProjectServiceWithRepo(r).ListMyProjects(context.Background(), "reader"); !errors.Is(err, first) {
		t.Fatalf("err = %v, want %v", err, first)
	}
	// A recheck failure on an earlier candidate's grant precedes a later
	// candidate's verification error.
	s := newProjectServiceWithRepo(projectListTreeFixture())
	grant := &accountReadGrant{AccountID: "a1-1", Member: &model.AccountMember{}}
	r2 := s.repo.(*projectListCountingRepo)
	r2.accountErr = map[string]error{"a1-1": first}
	err := s.recheckListCandidates(context.Background(), "reader", []string{"p00", "p05"},
		[]*accountReadGrant{grant, nil}, []bool{true, false}, 1, later)
	if !errors.Is(err, first) {
		t.Fatalf("err = %v, want recheck error first", err)
	}
	r2.accountErr = nil
	err = s.recheckListCandidates(context.Background(), "reader", []string{"p00", "p05"},
		[]*accountReadGrant{nil, nil}, []bool{true, false}, 1, later)
	if !errors.Is(err, later) {
		t.Fatalf("err = %v, want verification error", err)
	}
}

func TestListMyProjectsCandidateOrderMatchesPerItem(t *testing.T) {
	t.Setenv(accountDescendantReadsEnv, "1")
	want, err := newProjectServiceWithRepo(projectListTreeFixture()).ListMyProjects(context.Background(), "reader")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		got, err := newProjectServiceWithRepo(treeRepo(projectListTreeFixture(), true)).ListMyProjects(context.Background(), "reader")
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("run %d: batched %v / %v, per-item %v", i, projectIDs(got), err, projectIDs(want))
		}
	}
}
