package service

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
)

type reconcileQuery struct {
	cursor string
	limit  int32
}

type reconcilePagesRepo struct {
	*indexMemory
	order          []model.IndexResource
	queries        []reconcileQuery
	sourceLimits   []int32
	scanSources    bool
	queryFailureAt int
	queryError     error
	cancelAt       int
	cancel         context.CancelFunc
	sourceFailure  error
	oversizedPage  bool
}

func (r *reconcilePagesRepo) ScanIndexSources(ctx context.Context, cursor string, limit int32) ([]model.IndexResource, string, error) {
	r.sourceLimits = append(r.sourceLimits, limit)
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if !r.scanSources {
		return nil, cursor, nil
	}
	return r.indexMemory.ScanIndexSources(ctx, cursor, limit)
}

func (r *reconcilePagesRepo) RequestIndexResource(ctx context.Context, key model.IndexResource, revision string, now int64) error {
	if r.sourceFailure != nil {
		return r.sourceFailure
	}
	return r.indexMemory.RequestIndexResource(ctx, key, revision, now)
}

func (r *reconcilePagesRepo) ListIndexJobs(ctx context.Context, cursor string, limit int32) ([]model.IndexJob, string, error) {
	r.queries = append(r.queries, reconcileQuery{cursor, limit})
	if r.cancelAt == len(r.queries) {
		r.cancel()
	}
	if r.queryFailureAt == len(r.queries) {
		return nil, "", r.queryError
	}
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if r.order == nil {
		return r.indexMemory.ListIndexJobs(ctx, cursor, limit)
	}
	start, _ := strconv.Atoi(cursor)
	pageLimit := int(limit)
	if r.oversizedPage {
		pageLimit++
	}
	end := min(start+pageLimit, len(r.order))
	var jobs []model.IndexJob
	for _, key := range r.order[start:end] {
		jobs = append(jobs, *indexClone(r.jobs[key.Hash()]))
	}
	next := ""
	if end < len(r.order) {
		next = strconv.Itoa(end)
	}
	return jobs, next, nil
}

func addReconcileJob(t *testing.T, s *IndexingService, r *reconcilePagesRepo, state string) model.IndexResource {
	t.Helper()
	key := addIndexMeeting(r.indexMemory, fmt.Sprintf("ordered-%02d", len(r.order)), "saved source")
	source, err := s.ReadSource(context.Background(), key, false)
	if err != nil {
		t.Fatal(err)
	}
	r.jobs[key.Hash()] = &model.IndexJob{
		Resource: key, State: state, Revision: source.Revision, DesiredRevision: source.Revision, Version: 1,
	}
	if state == model.IndexFailed {
		r.jobs[key.Hash()].RetryAfter = s.now().Add(time.Hour).UnixMilli()
	}
	r.order = append(r.order, key)
	return key
}

func TestIndexReconcileSourceScanDiscoversHundredBeforeCursorAdvance(t *testing.T) {
	s, memory, _, _, _ := newIndexTest()
	r := &reconcilePagesRepo{indexMemory: memory, scanSources: true}
	s.repo = r
	for i := 0; i < 105; i++ {
		addIndexMeeting(memory, fmt.Sprintf("source-%03d", i), "saved source")
	}
	if _, err := s.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r.sourceLimits, []int32{100}) || len(memory.jobs) != 100 ||
		memory.control.SourceCursor != "100" || len(memory.control.Batch) != 4 {
		t.Fatalf("scan did not discover a bounded hundred: limits=%v jobs=%d cursor=%q batch=%d",
			r.sourceLimits, len(memory.jobs), memory.control.SourceCursor, len(memory.control.Batch))
	}
}

func TestIndexReconcileWalksIneligiblePagesToPending(t *testing.T) {
	s, memory, _, _, _ := newIndexTest()
	r := &reconcilePagesRepo{indexMemory: memory}
	s.repo = r
	for i := 0; i < 12; i++ {
		addReconcileJob(t, s, r, model.IndexFailed)
	}
	pending := addReconcileJob(t, s, r, model.IndexPending)
	for i := 0; i < 3; i++ {
		addReconcileJob(t, s, r, model.IndexFailed)
	}
	control := *memory.control
	batch, err := s.reconcile(context.Background(), &control)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch) != 1 || batch[0].Resource != pending || control.JobCursor != "" {
		t.Fatalf("pending job behind ineligible pages was not reached: batch=%v cursor=%q", batch, control.JobCursor)
	}
	want := []reconcileQuery{{"", 4}, {"4", 4}, {"8", 4}, {"12", 4}}
	if !reflect.DeepEqual(r.queries, want) {
		t.Fatalf("queries=%v, want %v", r.queries, want)
	}
}

func TestIndexReconcileCapacityLeavesNextEligibleJobForNextGeneration(t *testing.T) {
	s, memory, _, provider, _ := newIndexTest()
	r := &reconcilePagesRepo{indexMemory: memory}
	s.repo = r
	states := []string{model.IndexFailed, model.IndexPending, model.IndexFailed, model.IndexPending,
		model.IndexFailed, model.IndexPending, model.IndexPending, model.IndexPending}
	var pending []model.IndexResource
	for _, state := range states {
		key := addReconcileJob(t, s, r, state)
		if state == model.IndexPending {
			pending = append(pending, key)
		}
	}
	if _, err := s.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(memory.control.Batch) != 4 || memory.control.JobCursor != "7" {
		t.Fatalf("capacity/cursor mismatch: batch=%d cursor=%q", len(memory.control.Batch), memory.control.JobCursor)
	}
	if !reflect.DeepEqual(r.queries, []reconcileQuery{{"", 4}, {"4", 2}, {"6", 1}}) {
		t.Fatalf("query limits did not follow remaining capacity: %v", r.queries)
	}
	if memory.jobs[pending[4].Hash()].State != model.IndexPending {
		t.Fatal("unprocessed eligible job was consumed or skipped")
	}
	finishIndex(t, s, provider)
	r.queries = nil
	if _, err := s.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r.queries, []reconcileQuery{{"7", 4}}) || len(memory.control.Batch) != 1 ||
		memory.control.Batch[0].Resource != pending[4] {
		t.Fatalf("next generation did not resume exactly: queries=%v batch=%v", r.queries, memory.control.Batch)
	}
	finishIndex(t, s, provider)
	for _, key := range pending {
		if memory.jobs[key.Hash()].State != model.IndexIndexed {
			t.Fatal("pending work did not progress to indexed")
		}
	}
}

func TestIndexReconcileFourPageBoundaryRotatesToLaterPending(t *testing.T) {
	s, memory, _, provider, _ := newIndexTest()
	r := &reconcilePagesRepo{indexMemory: memory}
	s.repo = r
	for i := 0; i < 16; i++ {
		addReconcileJob(t, s, r, model.IndexFailed)
	}
	pending := addReconcileJob(t, s, r, model.IndexPending)
	if result, err := s.Tick(context.Background()); err != nil || result.Phase != "IDLE" {
		t.Fatalf("first bounded pass: result=%+v err=%v", result, err)
	}
	if len(r.queries) != 4 || memory.control.JobCursor != "16" || len(provider.tokens) != 0 {
		t.Fatalf("four-page bound failed: queries=%v cursor=%q", r.queries, memory.control.JobCursor)
	}
	r.queries = nil
	if _, err := s.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r.queries, []reconcileQuery{{"16", 4}}) {
		t.Fatalf("end-of-query wrapped or skipped: %v", r.queries)
	}
	finishIndex(t, s, provider)
	if memory.jobs[pending.Hash()].State != model.IndexIndexed {
		t.Fatal("later scheduled generation did not index pending source")
	}
}

func TestIndexReconcileCooldownAndPermanentFailureDoNotStarveLaterWork(t *testing.T) {
	s, memory, _, provider, now := newIndexTest()
	r := &reconcilePagesRepo{indexMemory: memory}
	s.repo = r
	for i := 0; i < 8; i++ {
		key := addReconcileJob(t, s, r, model.IndexFailed)
		memory.sources[key.Hash()].Fields["notes"] = 17 // Permanent source-format defect in cooldown.
	}
	bad := addReconcileJob(t, s, r, model.IndexFailed)
	memory.sources[bad.Hash()].Fields["notes"] = 17
	memory.jobs[bad.Hash()].RetryAfter = 0
	for i := 0; i < 7; i++ {
		addReconcileJob(t, s, r, model.IndexFailed)
	}
	good := addReconcileJob(t, s, r, model.IndexPending)
	for i := 0; i < 6 && memory.jobs[good.Hash()].State != model.IndexIndexed; i++ {
		provider.complete()
		if _, err := s.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if memory.jobs[good.Hash()].State != model.IndexIndexed ||
		memory.jobs[bad.Hash()].State != model.IndexFailed || memory.jobs[bad.Hash()].RetryAfter <= now.UnixMilli() {
		t.Fatalf("fair rotation failed: good=%s bad=%+v", memory.jobs[good.Hash()].State, memory.jobs[bad.Hash()])
	}
}

func TestIndexReconcileFailureDoesNotPublishPartialCursorOrBatch(t *testing.T) {
	for _, mode := range []string{"query", "cancel", "already-cancelled", "source-request"} {
		t.Run(mode, func(t *testing.T) {
			s, memory, _, provider, _ := newIndexTest()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			failure := errors.New("synthetic query outage")
			r := &reconcilePagesRepo{indexMemory: memory, queryError: failure, cancel: cancel}
			s.repo = r
			for i := 0; i < 8; i++ {
				state := model.IndexFailed
				if i == 0 && (mode == "query" || mode == "cancel") {
					state = model.IndexPending // A collected candidate must not be published on the later failure.
				}
				addReconcileJob(t, s, r, state)
			}
			switch mode {
			case "query":
				r.queryFailureAt = 2
			case "cancel":
				r.cancelAt = 2
				failure = context.Canceled
			case "already-cancelled":
				cancel()
				failure = context.Canceled
			case "source-request":
				r.scanSources, r.sourceFailure = true, failure
			}
			if _, err := s.Tick(ctx); !errors.Is(err, failure) {
				t.Fatalf("failure not surfaced: %v", err)
			}
			if memory.control.JobCursor != "" || memory.control.SourceCursor != "" ||
				len(memory.control.Batch) != 0 || memory.control.LeaseUntil != 0 || len(provider.tokens) != 0 {
				t.Fatalf("partial work published: %+v", memory.control)
			}
			want := 2
			if mode == "already-cancelled" || mode == "source-request" {
				want = 0
			}
			if len(r.queries) != want {
				t.Fatalf("query count=%d, want %d", len(r.queries), want)
			}
		})
	}
}

func TestIndexReconcileRejectsPageThatWouldSkipEligibleJobs(t *testing.T) {
	s, memory, _, provider, _ := newIndexTest()
	r := &reconcilePagesRepo{indexMemory: memory, oversizedPage: true}
	s.repo = r
	for i := 0; i < 5; i++ {
		addReconcileJob(t, s, r, model.IndexPending)
	}
	if _, err := s.Tick(context.Background()); !errors.Is(err, ErrIndexInvalid) {
		t.Fatalf("oversized repository page accepted: %v", err)
	}
	if memory.control.JobCursor != "" || len(memory.control.Batch) != 0 || len(provider.tokens) != 0 {
		t.Fatal("advanced past an eligible job that could not fit")
	}
}

func TestIndexReconcileStillWaitsForLeaseAndProvider(t *testing.T) {
	for _, mode := range []string{"lease", "provider", "running"} {
		t.Run(mode, func(t *testing.T) {
			s, memory, _, provider, _ := newIndexTest()
			r := &reconcilePagesRepo{indexMemory: memory}
			s.repo = r
			switch mode {
			case "lease":
				memory.control.LeaseUntil = s.now().Add(time.Minute).UnixMilli()
			case "provider":
				provider.busy = true
			case "running":
				memory.control.Phase, memory.control.ProviderJobID = "RUNNING", "existing"
				provider.jobs["existing"] = IndexProviderJob{ID: "existing", Status: "IN_PROGRESS"}
			}
			if _, err := s.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(r.queries) != 0 || len(r.sourceLimits) != 0 {
				t.Fatalf("reconciled through freeze: queries=%v scans=%v", r.queries, r.sourceLimits)
			}
		})
	}
}
