package service

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// countingAccountListRepo counts the per-item GetItem-equivalent reads of a
// full-capability account repository and has no batch primitive.
type countingAccountListRepo struct {
	*boundedAccountListRepo
	mu                     sync.Mutex
	getAccounts, getMember int
}

func (r *countingAccountListRepo) GetAccount(ctx context.Context, id string) (*model.Account, error) {
	// The shared fixture's own counters are unsynchronized; serialize reads.
	r.mu.Lock()
	defer r.mu.Unlock()
	r.getAccounts++
	return r.boundedAccountListRepo.GetAccount(ctx, id)
}

func (r *countingAccountListRepo) GetMember(ctx context.Context, id, user string) (*model.AccountMember, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.getMember++
	return r.boundedAccountListRepo.GetMember(ctx, id, user)
}

func (r *countingAccountListRepo) getItems() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.getAccounts + r.getMember
}

// batchAccountListRepo adds BatchGetAccountRows over the same rows. requests
// counts the 100-key BatchGetItem requests the repository would issue.
type batchAccountListRepo struct {
	*countingAccountListRepo
	calls, requests int
	err             error
	before          func(call int)
}

func (r *batchAccountListRepo) BatchGetAccountRows(ctx context.Context, keys []repository.AccountRowKey) (*repository.AccountRows, error) {
	r.mu.Lock()
	r.calls++
	r.requests += (len(keys) + 99) / 100
	call := r.calls
	r.mu.Unlock()
	if r.before != nil {
		r.before(call)
	}
	if r.err != nil {
		return nil, r.err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := &repository.AccountRows{Accounts: map[string]*model.Account{}, Members: map[repository.AccountRowKey]*model.AccountMember{}}
	base := r.countingAccountListRepo.boundedAccountListRepo
	for _, key := range keys {
		if key.UserID == "" {
			a, err := base.GetAccount(ctx, key.AccountID)
			if err != nil {
				return nil, err
			}
			if a != nil {
				out.Accounts[key.AccountID] = a
			}
			continue
		}
		m, err := base.GetMember(ctx, key.AccountID, key.UserID)
		if err != nil {
			return nil, err
		}
		if m != nil {
			out.Members[key] = m
		}
	}
	return out, nil
}

func batchMember(id string) *model.AccountMember {
	return &model.AccountMember{PK: "ACCOUNT#" + id, SK: "MEMBER#reader", AccountID: id, UserID: "reader",
		EntityType: model.EntityTypeAccountMember, Role: model.RoleAM}
}

// batchListFixture: "parent" (direct owner) has `children` inherited
// descendants; child-0001 also holds an independent direct grant; "gone" is a
// stale index row whose membership was removed; "sibling" is unrelated;
// child-0002 is a malformed (non-canonical) child edge that must stay hidden.
func batchListFixture(children int) *countingAccountListRepo {
	r := boundedDiscoveryFixture(children)
	r.accountIndex = []string{"child-0001", "gone", "parent"}
	r.hierarchy.members[memberKey("child-0001", "reader")] = batchMember("child-0001")
	r.hierarchy.accounts["gone"] = &model.Account{PK: "ACCOUNT#gone", SK: "META", AccountID: "gone", EntityType: model.EntityTypeAccount}
	// Forty more direct grants on descendants make the direct scope span two
	// 25-candidate pages, exercising its continuation.
	for i := 100; i < 140 && i < children; i++ {
		id := fmt.Sprintf("child-%04d", i)
		r.accountIndex = append(r.accountIndex, id)
		r.hierarchy.members[memberKey(id, "reader")] = batchMember(id)
	}
	if a := r.hierarchy.accounts["child-0002"]; a != nil {
		a.EntityType = "NOT_ACCOUNT"
	}
	return &countingAccountListRepo{boundedAccountListRepo: &boundedAccountListRepo{r.hierarchy.mockAccountRepo, r}}
}

func listAllAccountPages(t *testing.T, s *AccountService, q url.Values) ([]*AccountListPage, error) {
	t.Helper()
	pages := []*AccountListPage{}
	for n := 0; n < 100; n++ {
		page, err := s.ListAccountsPage(context.Background(), "reader", q)
		if err != nil {
			return pages, err
		}
		pages = append(pages, page)
		if page.NextCursor == "" {
			return pages, nil
		}
		next := url.Values{"pageSize": q["pageSize"], "cursor": {page.NextCursor}}
		if scope := q.Get("scope"); scope != "" {
			next.Set("scope", scope)
		}
		q = next
	}
	t.Fatal("account listing never completed")
	return nil, nil
}

func TestAccountListingBatchReadsMatchPerItemReads(t *testing.T) {
	cases := []struct {
		name     string
		flag     string
		children int
		run      func(*testing.T, *AccountService) (interface{}, error)
	}{
		{"default listing inherited tree", "1", 150, func(t *testing.T, s *AccountService) (interface{}, error) {
			return listAllAccountPages(t, s, url.Values{"pageSize": {"100"}})
		}},
		{"direct scope", "1", 150, func(t *testing.T, s *AccountService) (interface{}, error) {
			pages, err := listAllAccountPages(t, s, url.Values{"pageSize": {"100"}, "scope": {"direct"}})
			if err == nil && len(pages) < 2 {
				t.Fatalf("direct scope continuation not exercised: %d page(s)", len(pages))
			}
			return pages, err
		}},
		{"direct scope flag off", "0", 10, func(t *testing.T, s *AccountService) (interface{}, error) {
			return listAllAccountPages(t, s, url.Values{"pageSize": {"100"}, "scope": {"direct"}})
		}},
		{"collection listing inherited tree", "1", 150, func(t *testing.T, s *AccountService) (interface{}, error) {
			return s.ListAccounts(context.Background(), "reader")
		}},
		{"collection listing flag off", "0", 150, func(t *testing.T, s *AccountService) (interface{}, error) {
			return s.ListAccounts(context.Background(), "reader")
		}},
		{"empty listing", "1", 0, func(t *testing.T, s *AccountService) (interface{}, error) {
			return s.ListAccounts(context.Background(), "nobody")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(accountDescendantReadsEnv, tc.flag)
			perItem := batchListFixture(tc.children)
			want, wantErr := tc.run(t, newAccountServiceWithRepo(perItem))
			batched := &batchAccountListRepo{countingAccountListRepo: batchListFixture(tc.children)}
			got, gotErr := tc.run(t, newAccountServiceWithRepo(batched))
			if !errors.Is(gotErr, wantErr) || !reflect.DeepEqual(got, want) {
				t.Fatalf("batched result differs:\n got %+v / %v\nwant %+v / %v", got, gotErr, want, wantErr)
			}
			t.Logf("GetItem %d -> GetItem %d + BatchGetItem %d (%d calls)",
				perItem.getItems(), batched.getItems(), batched.requests, batched.calls)
			if tc.children > 0 && batched.getItems()+batched.requests >= perItem.getItems() {
				t.Fatalf("batching did not reduce reads: %d+%d vs %d", batched.getItems(), batched.requests, perItem.getItems())
			}
		})
	}
}

func TestAccountListingBatchReadAuthorization(t *testing.T) {
	t.Setenv(accountDescendantReadsEnv, "1")
	r := &batchAccountListRepo{countingAccountListRepo: batchListFixture(150)}
	accounts, err := newAccountServiceWithRepo(r).ListAccounts(context.Background(), "reader")
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]model.AccountSummary{}
	for _, a := range accounts {
		byID[a.AccountID] = a
	}
	if len(accounts) != 150 {
		t.Fatalf("want parent + 149 canonical children, got %d", len(accounts))
	}
	if a := byID["parent"]; a.AccessPermission != model.PermissionEdit || a.InheritedFromAccountID != "" {
		t.Fatalf("direct grant: %+v", a)
	}
	if a := byID["child-0001"]; a.AccessPermission != model.PermissionEdit || a.Role != model.RoleAM {
		t.Fatalf("independent direct grant on a descendant: %+v", a)
	}
	if a := byID["child-0149"]; a.AccessPermission != model.PermissionRead || a.InheritedFromAccountID != "parent" || a.Role != "" {
		t.Fatalf("inherited grant: %+v", a)
	}
	for _, hidden := range []string{"gone", "sibling", "child-0002"} {
		if _, ok := byID[hidden]; ok {
			t.Fatalf("%s surfaced without a current grant", hidden)
		}
	}
}

func TestAccountListingBatchReadFailsClosed(t *testing.T) {
	t.Setenv(accountDescendantReadsEnv, "1")
	boom := errors.New("unprocessed keys remain")
	for _, q := range []url.Values{nil, {"pageSize": {"100"}}, {"pageSize": {"100"}, "scope": {"direct"}}} {
		r := &batchAccountListRepo{countingAccountListRepo: batchListFixture(5), err: boom}
		s := newAccountServiceWithRepo(r)
		var err error
		if q == nil {
			_, err = s.ListAccounts(context.Background(), "reader")
		} else {
			_, err = s.ListAccountsPage(context.Background(), "reader", q)
		}
		if !errors.Is(err, boom) {
			t.Fatalf("%v: batch failure must fail the listing, got %v", q, err)
		}
	}
}

// The final recheck reads current rows: a membership removed after assembly
// is detected exactly as the per-item rechecks detected it.
func TestAccountListingBatchRecheckReadsCurrentRows(t *testing.T) {
	t.Setenv(accountDescendantReadsEnv, "1")
	for _, tc := range []struct {
		name string
		run  func(*AccountService) error
		want error
	}{
		{"collection", func(s *AccountService) error {
			_, err := s.ListAccounts(context.Background(), "reader")
			return err
		}, ErrForbidden},
		{"default page", func(s *AccountService) error {
			_, err := s.ListAccountsPage(context.Background(), "reader", url.Values{"pageSize": {"100"}})
			return err
		}, ErrStaleReadingCursor},
		{"direct page", func(s *AccountService) error {
			_, err := s.ListAccountsPage(context.Background(), "reader", url.Values{"pageSize": {"100"}, "scope": {"direct"}})
			return err
		}, ErrStaleReadingCursor},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dry := &batchAccountListRepo{countingAccountListRepo: batchListFixture(5)}
			if err := tc.run(newAccountServiceWithRepo(dry)); err != nil {
				t.Fatal(err)
			}
			r := &batchAccountListRepo{countingAccountListRepo: batchListFixture(5)}
			r.before = func(call int) {
				if call == dry.calls {
					delete(r.boundedAccountListRepo.discovery.hierarchy.members, memberKey("child-0001", "reader"))
				}
			}
			if err := tc.run(newAccountServiceWithRepo(r)); !errors.Is(err, tc.want) {
				t.Fatalf("removed membership before the recheck: got %v, want %v", err, tc.want)
			}
		})
	}
}

// batchProjectRepo serves BatchGetProjectListRows from the same project rows,
// keeping only repository.ProjectListRowFields as the projection does. events
// records batch calls ("B") and per-candidate membership reads ("V:<id>").
type batchProjectRepo struct {
	*latencyProjectRepo
	batchCalls, inBatch int
	batchErr            error
	events              []string
	chunks              [][]string
}

func (r *batchProjectRepo) BatchGetProjectListRows(_ context.Context, ids []string) (map[string]*model.Project, error) {
	r.mu.Lock()
	r.batchCalls += (len(ids) + 99) / 100
	r.inBatch++
	inBatch := r.inBatch
	r.events = append(r.events, "B")
	r.chunks = append(r.chunks, append([]string{}, ids...))
	r.mu.Unlock()
	defer func() { r.mu.Lock(); r.inBatch--; r.mu.Unlock() }()
	if inBatch > 1 || len(ids) > 100 {
		return nil, fmt.Errorf("overlapping or oversized candidate batch: %d ids", len(ids))
	}
	if r.batchErr != nil {
		return nil, r.batchErr
	}
	out := map[string]*model.Project{}
	for _, id := range ids {
		if id == "" {
			return nil, errors.New("empty project key")
		}
		if p := r.projects[id]; p != nil {
			out[id] = &model.Project{PK: p.PK, SK: p.SK, EntityType: p.EntityType, ProjectID: p.ProjectID, Name: p.Name,
				Stage: p.Stage, SfdcOpptyID: p.SfdcOpptyID, OwnerUserID: p.OwnerUserID, AccountIDs: append([]string{}, p.AccountIDs...)}
		}
	}
	return out, nil
}

func (r *batchProjectRepo) GetProjectMember(ctx context.Context, projectID, userID string) (*model.ProjectMember, error) {
	r.mu.Lock()
	r.events = append(r.events, "V:"+projectID)
	r.mu.Unlock()
	return r.latencyProjectRepo.GetProjectMember(ctx, projectID, userID)
}

func TestListMyProjectsBatchCandidateReadsMatchPerItemReads(t *testing.T) {
	fixture := func() *latencyProjectRepo {
		repo := inheritedProjectsFixture(0)
		repo.projects["p-3-0"].AccountIDs = []string{"acc-9"} // revoked link
		delete(repo.projects, "p-2-1")                        // deleted project, stale ref
		for i := 0; i < 220; i++ {                            // three candidate chunks
			id := fmt.Sprintf("q-%03d", i)
			p := seedProject(repo.mockProjectRepo, id, "owner")
			p.AccountIDs, p.Description = []string{"acc-1"}, strings.Repeat("large description ", 64)
			repo.projectRefs["acc-1"] = append(repo.projectRefs["acc-1"], model.ProjectRef{ProjectID: id, AccountID: "acc-1"})
		}
		// An empty legacy reference is skipped, as the per-item read did.
		repo.projectRefs["acc-3"] = append(repo.projectRefs["acc-3"], model.ProjectRef{AccountID: "acc-3"})
		return repo
	}
	perItem := fixture()
	want, wantErr := newProjectServiceWithRepo(perItem).ListMyProjects(context.Background(), "carol")
	batched := &batchProjectRepo{latencyProjectRepo: fixture()}
	got, gotErr := newProjectServiceWithRepo(batched).ListMyProjects(context.Background(), "carol")
	if wantErr != nil || gotErr != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("batched projects differ:\n got %v / %v\nwant %v / %v", projectIDs(got), gotErr, projectIDs(want), wantErr)
	}
	// Each chunk's rows are used only between its own read and the next
	// chunk's read: at most one chunk of projected rows is held at a time.
	chunkOf := map[string]int{}
	for n, ids := range batched.chunks {
		for _, id := range ids {
			chunkOf[id] = n
		}
	}
	current := -1
	for _, event := range batched.events {
		if event == "B" {
			current++
			continue
		}
		if n, ok := chunkOf[strings.TrimPrefix(event, "V:")]; ok && n != current {
			t.Fatalf("candidate %s of chunk %d verified while chunk %d was held", event, n, current)
		}
	}
	if batched.batchCalls != 3 || len(batched.chunks) != 3 || batched.calls >= perItem.calls {
		t.Fatalf("calls: per-item %d, batched %d + %d batch requests", perItem.calls, batched.calls, batched.batchCalls)
	}
	t.Logf("per-item calls %d -> %d + %d BatchGetItem", perItem.calls, batched.calls, batched.batchCalls)

	failing := &batchProjectRepo{latencyProjectRepo: fixture(), batchErr: errors.New("malformed project row")}
	if _, err := newProjectServiceWithRepo(failing).ListMyProjects(context.Background(), "carol"); !errors.Is(err, failing.batchErr) {
		t.Fatalf("batch failure must fail closed: %v", err)
	}
}
