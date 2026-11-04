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
)

// latencyProjectRepo adds a fixed delay, call counts, peak overlap and
// per-key fault injection to the reads ListMyProjects issues.
type latencyProjectRepo struct {
	*mockProjectRepo
	delay      time.Duration
	mu         sync.Mutex
	calls      int
	inFlight   int64
	peak       int64
	refsErr    map[string]error // ListProjectRefsForAccount by account
	projectErr map[string]error // GetProject by project
	slow       map[string]time.Duration
}

func (r *latencyProjectRepo) wait(ctx context.Context, key string) error {
	r.mu.Lock()
	r.calls++
	delay := r.delay + r.slow[key]
	r.mu.Unlock()
	now := atomic.AddInt64(&r.inFlight, 1)
	defer atomic.AddInt64(&r.inFlight, -1)
	for p := atomic.LoadInt64(&r.peak); now > p && !atomic.CompareAndSwapInt64(&r.peak, p, now); p = atomic.LoadInt64(&r.peak) {
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(delay):
		return nil
	}
}

func (r *latencyProjectRepo) GetProject(ctx context.Context, id string) (*model.Project, error) {
	if err := r.wait(ctx, id); err != nil {
		return nil, err
	}
	if err := r.projectErr[id]; err != nil {
		return nil, err
	}
	return r.mockProjectRepo.GetProject(ctx, id)
}

func (r *latencyProjectRepo) GetProjectMember(ctx context.Context, projectID, userID string) (*model.ProjectMember, error) {
	if err := r.wait(ctx, projectID); err != nil {
		return nil, err
	}
	return r.mockProjectRepo.GetProjectMember(ctx, projectID, userID)
}

func (r *latencyProjectRepo) ListProjectsForUser(ctx context.Context, userID string) ([]model.Project, error) {
	if err := r.wait(ctx, ""); err != nil {
		return nil, err
	}
	return r.mockProjectRepo.ListProjectsForUser(ctx, userID)
}

func (r *latencyProjectRepo) ListAccountsForUser(ctx context.Context, userID string) ([]model.AccountMember, error) {
	if err := r.wait(ctx, ""); err != nil {
		return nil, err
	}
	return r.mockProjectRepo.ListAccountsForUser(ctx, userID)
}

func (r *latencyProjectRepo) ListProjectRefsForAccount(ctx context.Context, accountID string) ([]model.ProjectRef, error) {
	if err := r.wait(ctx, accountID); err != nil {
		return nil, err
	}
	if err := r.refsErr[accountID]; err != nil {
		return nil, err
	}
	return r.mockProjectRepo.ListProjectRefsForAccount(ctx, accountID)
}

func (r *latencyProjectRepo) GetMember(ctx context.Context, accountID, userID string) (*model.AccountMember, error) {
	if err := r.wait(ctx, accountID); err != nil {
		return nil, err
	}
	return r.mockProjectRepo.GetMember(ctx, accountID, userID)
}

func (r *latencyProjectRepo) GetAccount(ctx context.Context, accountID string) (*model.Account, error) {
	if err := r.wait(ctx, accountID); err != nil {
		return nil, err
	}
	return r.mockProjectRepo.GetAccount(ctx, accountID)
}

// inheritedProjectsFixture: carol owns one project and reaches twelve more
// through three accounts (four each). One extra ref is stale (unlinked).
func inheritedProjectsFixture(delay time.Duration) *latencyProjectRepo {
	base := newMockProjectRepo()
	seedProject(base, "own", "carol")
	for a := 1; a <= 3; a++ {
		account := fmt.Sprintf("acc-%d", a)
		addProjectAccountMember(base, account, "carol")
		base.accounts[account] = &model.Account{AccountID: account}
		for i := 0; i < 4; i++ {
			id := fmt.Sprintf("p-%d-%d", a, i)
			seedProject(base, id, "owner").AccountIDs = []string{account}
			base.projectRefs[account] = append(base.projectRefs[account], model.ProjectRef{ProjectID: id, AccountID: account})
		}
	}
	seedProject(base, "stale", "owner")
	base.projectRefs["acc-2"] = append(base.projectRefs["acc-2"], model.ProjectRef{ProjectID: "stale", AccountID: "acc-2"})
	return &latencyProjectRepo{mockProjectRepo: base, delay: delay, slow: map[string]time.Duration{}}
}

func projectIDs(projects []model.ProjectSummary) []string {
	ids := make([]string, 0, len(projects))
	for _, p := range projects {
		ids = append(ids, p.ProjectID)
	}
	return ids
}

func TestListMyProjectsLatencyHarness(t *testing.T) {
	const delay = 10 * time.Millisecond
	repo := inheritedProjectsFixture(delay)
	start := time.Now()
	projects, err := newProjectServiceWithRepo(repo).ListMyProjects(context.Background(), "carol")
	elapsed := time.Since(start)
	t.Logf("delay=%v elapsed=%v calls=%d peak=%d", delay, elapsed.Round(time.Millisecond), repo.calls, atomic.LoadInt64(&repo.peak))
	if err != nil {
		t.Fatal(err)
	}
	got := projectIDs(projects)
	sorted := append([]string(nil), got[1:]...)
	sort.Strings(sorted)
	if len(got) != 13 || got[0] != "own" || strings.Join(sorted, ",") != "p-1-0,p-1-1,p-1-2,p-1-3,p-2-0,p-2-1,p-2-2,p-2-3,p-3-0,p-3-1,p-3-2,p-3-3" {
		t.Fatalf("projects = %v", got)
	}
	if peak := atomic.LoadInt64(&repo.peak); peak > listReadConcurrency || peak < 2 {
		t.Fatalf("peak overlapping reads %d, want 2..%d", peak, listReadConcurrency)
	}
}

func TestListMyProjectsConcurrentDiscovery(t *testing.T) {
	t.Run("inherited projects follow account then ref order", func(t *testing.T) {
		repo := inheritedProjectsFixture(time.Millisecond)
		for a := 1; a <= 3; a++ {
			for i := 0; i < 4; i++ {
				repo.slow[fmt.Sprintf("p-%d-%d", a, i)] = time.Duration(4-i) * 3 * time.Millisecond
			}
		}
		projects, err := newProjectServiceWithRepo(repo).ListMyProjects(context.Background(), "carol")
		if err != nil {
			t.Fatal(err)
		}
		want := "own,p-1-0,p-1-1,p-1-2,p-1-3,p-2-0,p-2-1,p-2-2,p-2-3,p-3-0,p-3-1,p-3-2,p-3-3"
		if got := strings.Join(projectIDs(projects), ","); got != want {
			t.Fatalf("got %s\nwant %s", got, want)
		}
	})
	t.Run("reference listing error is returned", func(t *testing.T) {
		repo := inheritedProjectsFixture(time.Millisecond)
		boom := errors.New("refs failed")
		repo.refsErr = map[string]error{"acc-2": boom}
		if _, err := newProjectServiceWithRepo(repo).ListMyProjects(context.Background(), "carol"); !errors.Is(err, boom) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("first candidate error in discovery order", func(t *testing.T) {
		repo := inheritedProjectsFixture(time.Millisecond)
		errFirst, errLater := errors.New("p-1-2 read"), errors.New("p-3-1 read")
		repo.projectErr = map[string]error{"p-1-2": errFirst, "p-3-1": errLater}
		repo.slow["p-1-2"] = 30 * time.Millisecond
		if _, err := newProjectServiceWithRepo(repo).ListMyProjects(context.Background(), "carol"); !errors.Is(err, errFirst) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("membership no longer granting access skips only that project", func(t *testing.T) {
		repo := inheritedProjectsFixture(time.Millisecond)
		repo.projects["p-3-0"].AccountIDs = []string{"acc-9"} // canonical link moved away
		projects, err := newProjectServiceWithRepo(repo).ListMyProjects(context.Background(), "carol")
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range projectIDs(projects) {
			if id == "p-3-0" || id == "stale" {
				t.Fatalf("unlinked project %s surfaced: %v", id, projectIDs(projects))
			}
		}
		if len(projects) != 12 {
			t.Fatalf("projects = %v", projectIDs(projects))
		}
	})
}
