package service

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	dbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

type savedMergeProgress struct {
	step, cursor string
	counts       map[string]int
}

// fakeMergeWorker is the worker's lease, progress and hierarchy store. It
// keeps the job of the shared fakeMergeJobRepo in step with saves.
type fakeMergeWorker struct {
	jobs      *fakeMergeJobRepo
	saves     []savedMergeProgress
	saveErr   error
	released  int
	finished  int
	finishErr []error
	cleared   int

	reparentErr   map[string]error
	reparented    []string
	chains        [][]repository.AccountParentLink
	projects      []model.ProjectRef
	research      []model.ResearchRef
	movedProjects []string
	movedResearch []string
	metaWrites    [][2][]string
	metaErr       error
}

func (f *fakeMergeWorker) SaveMergeProgress(_ context.Context, job *model.AccountMergeJob, step, cursor string, counts map[string]int) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	if model.MergeStepIndex(step) < model.MergeStepIndex(job.Step) || (step != job.Step && cursor != "") {
		return errors.New("invalid progress")
	}
	f.saves = append(f.saves, savedMergeProgress{step, cursor, counts})
	job.Step, job.Cursor, job.Counts = step, cursor, counts
	if f.jobs.job != nil {
		f.jobs.job.Step, f.jobs.job.Cursor, f.jobs.job.Counts = step, cursor, counts
	}
	return nil
}
func (f *fakeMergeWorker) ReleaseMergeLease(_ context.Context, job *model.AccountMergeJob) error {
	f.released++
	job.LeaseOwner, job.LeaseUntil = "", 0
	if f.jobs.job != nil {
		f.jobs.job.LeaseOwner, f.jobs.job.LeaseUntil = "", 0
	}
	return nil
}
func (f *fakeMergeWorker) FinishAccountMerge(_ context.Context, job *model.AccountMergeJob) error {
	if len(f.finishErr) > 0 {
		err := f.finishErr[0]
		f.finishErr = f.finishErr[1:]
		if err != nil {
			return err
		}
	}
	f.finished++
	job.State, job.LeaseOwner, job.LeaseUntil, job.ResolvePending = model.MergeStateDone, "", 0, true
	if f.jobs.job != nil {
		f.jobs.job.State, f.jobs.job.LeaseOwner, f.jobs.job.LeaseUntil, f.jobs.job.ResolvePending = model.MergeStateDone, "", 0, true
	}
	return nil
}
func (f *fakeMergeWorker) ClearMergeResolvePending(context.Context, string) error {
	f.cleared++
	if f.jobs.job != nil && f.jobs.job.State == model.MergeStateDone {
		f.jobs.job.ResolvePending = false
	}
	return nil
}
func (f *fakeMergeWorker) ReparentAccountForMerge(_ context.Context, _ *model.AccountMergeJob, childID string, chain []repository.AccountParentLink) error {
	if err := f.reparentErr[childID]; err != nil {
		return err
	}
	f.reparented = append(f.reparented, childID)
	f.chains = append(f.chains, chain)
	return nil
}
func (f *fakeMergeWorker) ListProjectRefsForAccount(context.Context, string) ([]model.ProjectRef, error) {
	return f.projects, nil
}
func (f *fakeMergeWorker) MoveProjectAccountLinkForMerge(_ context.Context, _ *model.AccountMergeJob, id string) error {
	f.movedProjects = append(f.movedProjects, id)
	return nil
}
func (f *fakeMergeWorker) ListResearchRefsForAccount(context.Context, string) ([]model.ResearchRef, error) {
	return f.research, nil
}
func (f *fakeMergeWorker) MoveResearchAccountLinkForMerge(_ context.Context, _ *model.AccountMergeJob, id string) error {
	f.movedResearch = append(f.movedResearch, id)
	return nil
}
func (f *fakeMergeWorker) MergeAccountMetadata(_ context.Context, _ *model.AccountMergeJob, _ *model.Account, aliases, domains []string) error {
	if f.metaErr != nil {
		return f.metaErr
	}
	f.metaWrites = append(f.metaWrites, [2][]string{aliases, domains})
	return nil
}

// workerHarness wires a worker over the start fixture: u1 owns "a" (merging
// into "b") and is a member of "b"; the clock is fixed unless a step moves it.
type workerHarness struct {
	repo    *fakeMergeRepo
	jobs    *fakeMergeJobRepo
	worker  *fakeMergeWorker
	svc     *AccountMergeService
	now     time.Time
	merges  []model.AccountMergeRequested
	people  []model.PeopleResolveRequested
	calls   []string
	pauses  int
	peopErr error
}

func newWorkerHarness(step string) *workerHarness {
	repo, jobs, svc := startFixture()
	repo.accounts["a"].MergingInto = "b"
	h := &workerHarness{repo: repo, jobs: jobs, svc: svc, now: time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)}
	jobs.job = &model.AccountMergeJob{SourceAccountID: "a", TargetAccountID: "b", RequesterUserID: "u1",
		State: model.MergeStateRunning, Step: step, Counts: map[string]int{}}
	h.worker = &fakeMergeWorker{jobs: jobs}
	svc.SetWorkerRepo(h.worker)
	svc.SetMoverRepo(newFakeMoverRepo())
	svc.SetMergePublisher(func(_ context.Context, e model.AccountMergeRequested) error {
		h.merges = append(h.merges, e)
		return nil
	})
	svc.SetPeopleResolvePublisher(func(_ context.Context, e model.PeopleResolveRequested) error {
		h.people = append(h.people, e)
		return h.peopErr
	})
	svc.now = func() time.Time { return h.now }
	svc.sleep = func(context.Context, time.Duration) error { h.pauses++; return nil }
	// Every mover step finishes in one call unless a test overrides it.
	svc.steps = map[string]mergeStepFunc{}
	for _, name := range model.MergeSteps[:len(model.MergeSteps)-1] {
		name := name
		svc.steps[name] = func(*mergeFence, *model.AccountMergeJob) (mergeMoveResult, error) {
			h.calls = append(h.calls, name)
			return mergeMoveResult{Moved: 1, Done: true}, nil
		}
	}
	return h
}

func (h *workerHarness) run() error {
	return h.svc.ProcessAccountMergeRequested(context.Background(), model.AccountMergeRequested{SourceAccountID: "a"}, h.now.Add(15*time.Minute))
}

func TestWorkerRunsStepsInOrder(t *testing.T) {
	h := newWorkerHarness("members")
	contactCalls := 0
	h.svc.steps["contacts"] = func(_ *mergeFence, job *model.AccountMergeJob) (mergeMoveResult, error) {
		h.calls = append(h.calls, "contacts")
		contactCalls++
		if contactCalls == 1 {
			if job.Cursor != "" {
				t.Errorf("contacts started with cursor %q", job.Cursor)
			}
			return mergeMoveResult{Moved: 500, Unreadable: 2, KeptSeparate: 1, Cursor: "c500"}, nil
		}
		if job.Cursor != "c500" {
			t.Errorf("contacts resumed at %q", job.Cursor)
		}
		return mergeMoveResult{Moved: 3, Skipped: 1, Done: true}, nil
	}
	h.svc.steps["events"] = func(_ *mergeFence, job *model.AccountMergeJob) (mergeMoveResult, error) {
		h.calls = append(h.calls, "events")
		if job.Cursor != "" || job.Counts["contacts.moved"] != 503 {
			t.Errorf("events ran before contacts finished: cursor=%q counts=%v", job.Cursor, job.Counts)
		}
		return mergeMoveResult{Moved: 4, Unreadable: 1, Done: true}, nil
	}
	if err := h.run(); err != nil {
		t.Fatal(err)
	}
	want := []string{"members", "children", "projects", "research", "contacts", "contacts", "events", "meetings", "metadata"}
	if !reflect.DeepEqual(h.calls, want) {
		t.Fatalf("calls = %v, want %v", h.calls, want)
	}
	if h.worker.finished != 1 {
		t.Fatalf("finished = %d", h.worker.finished)
	}
	var steps []string
	for _, s := range h.worker.saves {
		steps = append(steps, s.step+"@"+s.cursor)
	}
	wantSaves := []string{"children@", "projects@", "research@", "contacts@", "contacts@c500", "events@", "meetings@", "metadata@", "finish@"}
	if !reflect.DeepEqual(steps, wantSaves) {
		t.Fatalf("saves = %v, want %v", steps, wantSaves)
	}
	counts := h.worker.saves[len(h.worker.saves)-1].counts
	for key, n := range map[string]int{"members.moved": 1, "contacts.moved": 503, "contacts.unreadable": 2,
		"contacts.keptSeparate": 1, "contacts.skipped": 1, "events.moved": 4, "events.unreadable": 1, "metadata.moved": 1} {
		if counts[key] != n {
			t.Errorf("counts[%s] = %d, want %d (%v)", key, counts[key], n, counts)
		}
	}
	if len(h.jobs.claims) != 1 || len(h.merges) != 0 || len(h.jobs.failed) != 0 {
		t.Fatalf("claims=%v merges=%v failed=%v", h.jobs.claims, h.merges, h.jobs.failed)
	}
}

func TestWorkerRepublishesBeforeDeadline(t *testing.T) {
	t.Run("no time left", func(t *testing.T) {
		h := newWorkerHarness("contacts")
		err := h.svc.ProcessAccountMergeRequested(context.Background(), model.AccountMergeRequested{SourceAccountID: "a"}, h.now.Add(3*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if len(h.calls) != 0 || h.worker.released != 1 || !reflect.DeepEqual(h.merges, []model.AccountMergeRequested{{SourceAccountID: "a"}}) {
			t.Fatalf("calls=%v released=%d merges=%v", h.calls, h.worker.released, h.merges)
		}
	})
	t.Run("stops mid-job and renews the lease meanwhile", func(t *testing.T) {
		h := newWorkerHarness("members")
		for _, name := range model.MergeSteps[:len(model.MergeSteps)-1] {
			name := name
			h.svc.steps[name] = func(*mergeFence, *model.AccountMergeJob) (mergeMoveResult, error) {
				h.calls = append(h.calls, name)
				h.now = h.now.Add(4 * time.Minute)
				return mergeMoveResult{Moved: 1, Done: true}, nil
			}
		}
		if err := h.run(); err != nil {
			t.Fatal(err)
		}
		// 15 min deadline, 3 min reserve, 4 min per call: three calls fit.
		if !reflect.DeepEqual(h.calls, []string{"members", "children", "projects"}) {
			t.Fatalf("calls = %v", h.calls)
		}
		if h.jobs.job.Step != "research" || h.worker.released != 1 || len(h.merges) != 1 || h.worker.finished != 0 {
			t.Fatalf("step=%s released=%d merges=%v finished=%d", h.jobs.job.Step, h.worker.released, h.merges, h.worker.finished)
		}
		if len(h.jobs.claims) != 3 || h.jobs.claims[1] != h.jobs.claims[0] {
			t.Fatalf("lease must be renewed by the same owner: %v", h.jobs.claims)
		}
		// The continuation claims the released lease under a new owner.
		h.calls = nil
		if err := h.run(); err != nil {
			t.Fatal(err)
		}
		if len(h.calls) == 0 || h.calls[0] != "research" || h.jobs.claims[3] == h.jobs.claims[0] {
			t.Fatalf("continuation calls=%v claims=%v", h.calls, h.jobs.claims)
		}
	})
	t.Run("failed continuation is returned for retry", func(t *testing.T) {
		h := newWorkerHarness("contacts")
		h.svc.SetMergePublisher(func(context.Context, model.AccountMergeRequested) error { return errors.New("bus down") })
		err := h.svc.ProcessAccountMergeRequested(context.Background(), model.AccountMergeRequested{SourceAccountID: "a"}, h.now)
		if err == nil || h.worker.released != 1 || h.jobs.job.LeaseOwner != "" {
			t.Fatalf("err=%v released=%d owner=%q", err, h.worker.released, h.jobs.job.LeaseOwner)
		}
	})
}

func TestWorkerLeaseHeldElsewhereNoops(t *testing.T) {
	for name, mutate := range map[string]func(*workerHarness){
		"live lease": func(h *workerHarness) {
			h.jobs.job.LeaseOwner, h.jobs.job.LeaseUntil = "other", h.now.Add(time.Minute).Unix()
		},
		"job done":    func(h *workerHarness) { h.jobs.job.State = model.MergeStateDone },
		"job failed":  func(h *workerHarness) { h.jobs.job.State = model.MergeStateFailed },
		"job missing": func(h *workerHarness) { h.jobs.job = nil },
	} {
		t.Run(name, func(t *testing.T) {
			h := newWorkerHarness("members")
			mutate(h)
			if err := h.run(); err != nil {
				t.Fatal(err)
			}
			if len(h.calls) != 0 || len(h.worker.saves) != 0 || h.worker.released != 0 || len(h.merges) != 0 || len(h.jobs.failed) != 0 {
				t.Fatalf("calls=%v saves=%v released=%d merges=%v failed=%v", h.calls, h.worker.saves, h.worker.released, h.merges, h.jobs.failed)
			}
		})
	}
	t.Run("expired lease is taken over", func(t *testing.T) {
		h := newWorkerHarness("metadata")
		h.jobs.job.LeaseOwner, h.jobs.job.LeaseUntil = "other", h.now.Add(-time.Second).Unix()
		if err := h.run(); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(h.calls, []string{"metadata"}) || h.jobs.claims[0] == "other" {
			t.Fatalf("calls=%v claims=%v", h.calls, h.jobs.claims)
		}
	})
}

func TestWorkerFinishEmitsPeopleResolve(t *testing.T) {
	h := newWorkerHarness("finish")
	h.worker.finishErr = []error{repository.ErrConditionFailed}
	if err := h.run(); err != nil {
		t.Fatal(err)
	}
	if h.worker.finished != 1 || h.pauses != 1 {
		t.Fatalf("finished=%d pauses=%d", h.worker.finished, h.pauses)
	}
	want := []model.PeopleResolveRequested{{AccountID: "b", Reason: model.PeopleResolveReasonContacts}}
	if !reflect.DeepEqual(h.people, want) || len(h.merges) != 0 {
		t.Fatalf("people=%v merges=%v", h.people, h.merges)
	}

	if h.worker.cleared != 1 || h.jobs.job.ResolvePending {
		t.Fatalf("a published request must clear resolvePending: cleared=%d", h.worker.cleared)
	}
	// A redelivery after a successful publish does nothing.
	if err := h.run(); err != nil || len(h.people) != 1 {
		t.Fatalf("redelivery republished: err=%v people=%v", err, h.people)
	}

	failing := newWorkerHarness("finish")
	failing.peopErr = errors.New("bus down")
	if err := failing.run(); err == nil || failing.worker.finished != 1 || len(failing.jobs.failed) != 0 {
		t.Fatalf("people publish failure must surface without failing the done job: err=%v failed=%v", err, failing.jobs.failed)
	}
	if failing.worker.cleared != 0 || !failing.jobs.job.ResolvePending {
		t.Fatal("a failed publish must keep resolvePending")
	}
	// The redelivered event finds the job done and republishes once.
	if err := failing.run(); err == nil || len(failing.people) != 2 {
		t.Fatalf("a failing republish must surface for retry: err=%v people=%d", err, len(failing.people))
	}
	failing.peopErr = nil
	if err := failing.run(); err != nil || len(failing.people) != 3 || failing.worker.cleared != 1 || failing.jobs.job.ResolvePending {
		t.Fatalf("redelivery must republish and clear: err=%v people=%d cleared=%d", err, len(failing.people), failing.worker.cleared)
	}
	if err := failing.run(); err != nil || len(failing.people) != 3 {
		t.Fatalf("a cleared flag must not republish: err=%v people=%d", err, len(failing.people))
	}
	if failing.worker.finished != 1 {
		t.Fatalf("redeliveries must not finish again: %d", failing.worker.finished)
	}
}

func TestWorkerBoundedRetry(t *testing.T) {
	conflict := &dbtypes.TransactionCanceledException{CancellationReasons: []dbtypes.CancellationReason{{Code: aws.String("TransactionConflict")}}}
	for _, tc := range []struct {
		name     string
		errs     []error
		wantFail []string
		wantDone bool
	}{
		{"condition failures then success", []error{repository.ErrConditionFailed, repository.ErrConditionFailed}, nil, true},
		{"condition failures exhaust", []error{repository.ErrConditionFailed, repository.ErrConditionFailed, repository.ErrConditionFailed, repository.ErrConditionFailed}, []string{MergeErrorConflict}, false},
		{"summary access exhausts", []error{repository.ErrSummaryAccess, repository.ErrSummaryAccess, repository.ErrSummaryAccess, repository.ErrSummaryAccess}, []string{MergeErrorConflict}, false},
		{"transaction conflicts exhaust", []error{conflict, conflict, conflict, conflict}, []string{MergeErrorConflict}, false},
		{"other errors exhaust", []error{errors.New("x"), errors.New("x"), errors.New("x"), errors.New("x")}, []string{MergeErrorStepFailed}, false},
		{"terminal failure fails at once", []error{ErrMergeContactIndexTooLarge}, []string{MergeErrorContactIndexTooLarge}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newWorkerHarness("meetings")
			calls := 0
			h.svc.steps["meetings"] = func(_ *mergeFence, job *model.AccountMergeJob) (mergeMoveResult, error) {
				calls++
				if job.Cursor != "" {
					t.Errorf("cursor advanced on error: %q", job.Cursor)
				}
				if calls <= len(tc.errs) {
					return mergeMoveResult{Cursor: job.Cursor}, tc.errs[calls-1]
				}
				return mergeMoveResult{Moved: 1, Done: true}, nil
			}
			if err := h.run(); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(h.jobs.failed, tc.wantFail) || (h.worker.finished == 1) != tc.wantDone {
				t.Fatalf("failed=%v finished=%d calls=%d", h.jobs.failed, h.worker.finished, calls)
			}
			if tc.wantFail != nil && calls > mergeStepAttempts {
				t.Fatalf("retried %d times", calls)
			}
		})
	}
}

func TestWorkerLeaseRenewalRetries(t *testing.T) {
	h := newWorkerHarness("research")
	h.jobs.claimErrs = []error{nil, errors.New("throttled")}
	for _, name := range []string{"research", "contacts"} {
		name := name
		h.svc.steps[name] = func(*mergeFence, *model.AccountMergeJob) (mergeMoveResult, error) {
			h.calls = append(h.calls, name)
			h.now = h.now.Add(2 * time.Minute)
			return mergeMoveResult{Done: true}, nil
		}
	}
	if err := h.run(); err != nil {
		t.Fatal(err)
	}
	if len(h.calls) < 2 || h.calls[1] != "contacts" || h.pauses != 1 || len(h.jobs.failed) != 0 {
		t.Fatalf("calls=%v pauses=%d failed=%v", h.calls, h.pauses, h.jobs.failed)
	}

	// A renewal that loses the lease stops without writing.
	lost := newWorkerHarness("research")
	lost.jobs.claimErrs = []error{nil, repository.ErrMergeLeaseHeld}
	lost.svc.steps["research"] = func(*mergeFence, *model.AccountMergeJob) (mergeMoveResult, error) {
		lost.calls = append(lost.calls, "research")
		lost.now = lost.now.Add(2 * time.Minute)
		return mergeMoveResult{Done: true}, nil
	}
	if err := lost.run(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(lost.calls, []string{"research"}) || len(lost.jobs.failed) != 0 || len(lost.merges) != 0 {
		t.Fatalf("calls=%v failed=%v merges=%v", lost.calls, lost.jobs.failed, lost.merges)
	}
}

func TestWorkerFenceLost(t *testing.T) {
	for _, tc := range []struct {
		name     string
		revoke   func(*fakeMergeRepo)
		wantFail []string
	}{
		{"requester revoked", func(r *fakeMergeRepo) { delete(r.members["b"], "u1") }, []string{MergeErrorRequesterRevoked}},
		{"lease taken over", func(*fakeMergeRepo) {}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newWorkerHarness("events")
			tc.revoke(h.repo)
			h.svc.steps["events"] = func(*mergeFence, *model.AccountMergeJob) (mergeMoveResult, error) {
				return mergeMoveResult{}, ErrMergeFenceLost
			}
			if err := h.run(); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(h.jobs.failed, tc.wantFail) || h.pauses != 0 || len(h.worker.saves) != 0 || len(h.merges) != 0 {
				t.Fatalf("failed=%v pauses=%d saves=%v merges=%v", h.jobs.failed, h.pauses, h.worker.saves, h.merges)
			}
		})
	}
}

func TestWorkerIDsUniquePerCall(t *testing.T) {
	h := newWorkerHarness("metadata")
	if err := h.run(); err != nil {
		t.Fatal(err)
	}
	h.jobs.job.State, h.jobs.job.Step = model.MergeStateRunning, "metadata"
	if err := h.run(); err != nil {
		t.Fatal(err)
	}
	if len(h.jobs.claims) != 2 || h.jobs.claims[0] == "" || h.jobs.claims[0] == h.jobs.claims[1] {
		t.Fatalf("claims = %v", h.jobs.claims)
	}
}

// The operator CLI continues from its own lease by claiming as exactly its owner.
func TestWorkerIDHonorsExplicitOwner(t *testing.T) {
	s := &AccountMergeService{}
	ctx := WithMergeWorkerOwner(context.Background(), "cli-host-1")
	if got := s.workerID(ctx); got != "cli-host-1" || MergeWorkerOwner(ctx) != "cli-host-1" {
		t.Fatalf("workerID = %q, want the explicit owner", got)
	}
	if got := s.workerID(context.Background()); got == "" || got == "cli-host-1" {
		t.Fatalf("workerID without an owner = %q, want a random ID", got)
	}
}

func TestStartPublishesAccountMergeRequested(t *testing.T) {
	repo, jobs, svc := startFixture()
	var got []model.AccountMergeRequested
	svc.SetMergePublisher(func(_ context.Context, e model.AccountMergeRequested) error { got = append(got, e); return nil })
	if _, err := svc.Start(context.Background(), "u1", "a", &model.StartAccountMergeRequest{IntoAccountID: "b", AcceptUnmoved: true}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []model.AccountMergeRequested{{SourceAccountID: "a"}}) {
		t.Fatalf("published %v", got)
	}

	// Resume publishes too.
	repo.accounts["a"].MergingInto = "b"
	jobs.job = &model.AccountMergeJob{SourceAccountID: "a", TargetAccountID: "b", RequesterUserID: "u1",
		State: model.MergeStateFailed, Step: "events"}
	got = nil
	if _, err := svc.Start(context.Background(), "u1", "a", &model.StartAccountMergeRequest{IntoAccountID: "b", AcceptUnmoved: true}); err != nil {
		t.Fatal(err)
	}
	if len(jobs.resumed) != 1 || len(got) != 1 {
		t.Fatalf("resumed=%d published=%v", len(jobs.resumed), got)
	}
}

func TestStartDispatchFailureFailsJob(t *testing.T) {
	_, jobs, svc := startFixture()
	svc.SetMergePublisher(func(context.Context, model.AccountMergeRequested) error { return errors.New("bus down") })
	// The fake start write does not store the job; model the stored row.
	jobs.job = nil
	stored := &model.AccountMergeJob{SourceAccountID: "a", TargetAccountID: "b", RequesterUserID: "u1", State: model.MergeStateRunning, Step: "members"}
	svc.SetJobRepo(&startStoresJob{fakeMergeJobRepo: jobs, stored: stored})
	if _, err := svc.Start(context.Background(), "u1", "a", &model.StartAccountMergeRequest{IntoAccountID: "b", AcceptUnmoved: true}); err == nil {
		t.Fatal("start succeeded without a worker event")
	}
	if !reflect.DeepEqual(jobs.failed, []string{MergeErrorDispatchFailed}) {
		t.Fatalf("failed = %v", jobs.failed)
	}
}

// startStoresJob makes the fake start write visible to later claims.
type startStoresJob struct {
	*fakeMergeJobRepo
	stored *model.AccountMergeJob
}

func (f *startStoresJob) StartAccountMerge(ctx context.Context, job *model.AccountMergeJob, a []repository.AccountParentLink) error {
	if err := f.fakeMergeJobRepo.StartAccountMerge(ctx, job, a); err != nil {
		return err
	}
	f.job = f.stored
	return nil
}

func TestAccountMergePublisherEnvelope(t *testing.T) {
	client := &peopleEventClient{}
	if err := AccountMergePublisher(client)(context.Background(), model.AccountMergeRequested{SourceAccountID: "a1"}); err != nil {
		t.Fatal(err)
	}
	entry := client.in.Entries[0]
	if aws.ToString(entry.Source) != "ttobak.analysis" || aws.ToString(entry.DetailType) != "AccountMergeRequested" || aws.ToString(entry.EventBusName) != "default" {
		t.Fatalf("entry %+v", entry)
	}
	var got model.AccountMergeRequested
	if err := json.Unmarshal([]byte(aws.ToString(entry.Detail)), &got); err != nil || got.SourceAccountID != "a1" {
		t.Fatalf("detail %s %v", aws.ToString(entry.Detail), err)
	}
	client.in = nil
	if err := AccountMergePublisher(client)(context.Background(), model.AccountMergeRequested{SourceAccountID: "a#1"}); err == nil || client.in != nil {
		t.Fatal("invalid event published")
	}
}

func TestStartRedispatchesStalledRunningJob(t *testing.T) {
	for _, tc := range []struct {
		name      string
		owner     string
		until     time.Duration // relative to the harness clock; 0 means no lease
		wantErr   error
		wantEvent bool
	}{
		{"no lease", "", 0, nil, true},
		{"expired lease", "w-dead", -time.Second, nil, true},
		{"live lease", "w-live", time.Minute, ErrMergeJobRunning, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newWorkerHarness("events")
			h.jobs.job.LeaseOwner = tc.owner
			if tc.until != 0 {
				h.jobs.job.LeaseUntil = h.now.Add(tc.until).Unix()
			}
			dto, err := h.svc.Start(context.Background(), "u1", "a", &model.StartAccountMergeRequest{IntoAccountID: "b", AcceptUnmoved: true})
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if (len(h.merges) == 1) != tc.wantEvent || len(h.jobs.started)+len(h.jobs.resumed) != 0 {
				t.Fatalf("merges=%v started=%d resumed=%d", h.merges, len(h.jobs.started), len(h.jobs.resumed))
			}
			if tc.wantErr == nil && (dto.Step != "events" || dto.State != model.MergeStateRunning) {
				t.Fatalf("dto = %+v", dto)
			}
		})
	}
}

func TestWorkerTimedOutThenResumes(t *testing.T) {
	h := newWorkerHarness("contacts")
	ctx, cancel := context.WithCancel(context.Background())
	h.svc.steps["contacts"] = func(_ *mergeFence, job *model.AccountMergeJob) (mergeMoveResult, error) {
		cancel() // the invocation ends mid-call
		return mergeMoveResult{Cursor: job.Cursor}, errors.New("operation error: context canceled")
	}
	err := h.svc.ProcessAccountMergeRequested(ctx, model.AccountMergeRequested{SourceAccountID: "a"}, h.now.Add(15*time.Minute))
	if !errors.Is(err, context.Canceled) || h.worker.released != 1 || h.jobs.job.LeaseOwner != "" || len(h.jobs.failed) != 0 {
		t.Fatalf("err=%v released=%d owner=%q failed=%v", err, h.worker.released, h.jobs.job.LeaseOwner, h.jobs.failed)
	}
	// The owner's restart hands the running job to a new worker event.
	if _, err := h.svc.Start(context.Background(), "u1", "a", &model.StartAccountMergeRequest{IntoAccountID: "b", AcceptUnmoved: true}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(h.merges, []model.AccountMergeRequested{{SourceAccountID: "a"}}) {
		t.Fatalf("merges = %v", h.merges)
	}
}

func TestWorkerCallTimeBoundHandsOff(t *testing.T) {
	h := newWorkerHarness("meetings")
	h.svc.callBudget = 20 * time.Millisecond
	h.svc.steps["meetings"] = func(fence *mergeFence, job *model.AccountMergeJob) (mergeMoveResult, error) {
		<-fence.Context().Done()
		return mergeMoveResult{Cursor: job.Cursor}, fence.Err()
	}
	if err := h.run(); err != nil {
		t.Fatal(err)
	}
	if h.worker.released != 1 || len(h.merges) != 1 || h.pauses != 0 || len(h.jobs.failed) != 0 || len(h.worker.saves) != 0 {
		t.Fatalf("released=%d merges=%v pauses=%d failed=%v saves=%v", h.worker.released, h.merges, h.pauses, h.jobs.failed, h.worker.saves)
	}
	// The computed bound ends 30 s before the deadline and within the lease.
	svc := &AccountMergeService{}
	now := time.Now()
	if got := svc.moverCallBudget(now, now.Add(4*time.Minute)); got != 3*time.Minute+30*time.Second {
		t.Fatalf("budget near deadline = %v", got)
	}
	if got := svc.moverCallBudget(now, now.Add(15*time.Minute)); got != mergeLeaseTTL-mergeCallMargin {
		t.Fatalf("budget capped by lease = %v", got)
	}
}

func TestWorkerInvalidSourceAcknowledged(t *testing.T) {
	h := newWorkerHarness("members")
	err := h.svc.ProcessAccountMergeRequested(context.Background(), model.AccountMergeRequested{SourceAccountID: "a#1"}, h.now.Add(15*time.Minute))
	if err != nil || len(h.jobs.claims) != 0 {
		t.Fatalf("err=%v claims=%v", err, h.jobs.claims)
	}
}
