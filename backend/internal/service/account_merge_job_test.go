package service

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// fakeMergeJobRepo records start, resume and failure writes.
type fakeMergeJobRepo struct {
	job       *model.AccountMergeJob
	writeErr  error
	failErr   error
	started   []*model.AccountMergeJob
	resumed   []*model.AccountMergeJob
	ancestors []repository.AccountParentLink
	failed    []string
}

func (f *fakeMergeJobRepo) GetAccountMergeJob(_ context.Context, _ string) (*model.AccountMergeJob, error) {
	if f.job == nil {
		return nil, nil
	}
	copied := *f.job
	return &copied, nil
}
func (f *fakeMergeJobRepo) StartAccountMerge(_ context.Context, job *model.AccountMergeJob, a []repository.AccountParentLink) error {
	if f.writeErr != nil {
		return f.writeErr
	}
	f.started, f.ancestors = append(f.started, job), a
	return nil
}
func (f *fakeMergeJobRepo) ResumeAccountMerge(_ context.Context, job *model.AccountMergeJob, a []repository.AccountParentLink) error {
	if f.writeErr != nil {
		return f.writeErr
	}
	f.resumed, f.ancestors = append(f.resumed, job), a
	return nil
}
func (f *fakeMergeJobRepo) FailMergeJob(_ context.Context, _ *model.AccountMergeJob, code string) error {
	if f.failErr != nil {
		return f.failErr
	}
	f.failed = append(f.failed, code)
	return nil
}

// startFixture: u1 owns "a" and is a direct member of "b", whose parent is "p".
func startFixture() (*fakeMergeRepo, *fakeMergeJobRepo, *AccountMergeService) {
	repo, svc := mergeFixture()
	repo.addAccount("p", "")
	repo.accounts["b"].ParentAccountID = "p"
	jobs := &fakeMergeJobRepo{}
	svc.SetJobRepo(jobs)
	return repo, jobs, svc
}

func TestStartAccountMergeWritesFreshJob(t *testing.T) {
	_, jobs, svc := startFixture()
	dto, err := svc.Start(context.Background(), "u1", "a", &model.StartAccountMergeRequest{IntoAccountID: "b", AcceptUnmoved: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs.started) != 1 || len(jobs.resumed) != 0 {
		t.Fatalf("started=%d resumed=%d", len(jobs.started), len(jobs.resumed))
	}
	job := jobs.started[0]
	if job.SourceAccountID != "a" || job.TargetAccountID != "b" || job.RequesterUserID != "u1" ||
		job.State != model.MergeStateRunning || job.Step != "members" || job.Cursor != "" || !job.AcceptUnmoved || job.LeaseOwner != "" {
		t.Fatalf("job = %+v", job)
	}
	want := []repository.AccountParentLink{{AccountID: "b", ParentAccountID: "p"}, {AccountID: "p"}}
	if !reflect.DeepEqual(jobs.ancestors, want) {
		t.Fatalf("ancestors = %+v, want %+v", jobs.ancestors, want)
	}
	if dto.FromAccountID != "a" || dto.IntoAccountID != "b" || dto.State != model.MergeStateRunning || dto.Counts == nil {
		t.Fatalf("dto = %+v", dto)
	}
}

func TestStartRequiresAcceptUnmoved(t *testing.T) {
	// billing is always reported present, so a start without the flag fails.
	_, jobs, svc := startFixture()
	_, err := svc.Start(context.Background(), "u1", "a", &model.StartAccountMergeRequest{IntoAccountID: "b"})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
	if len(jobs.started)+len(jobs.resumed) != 0 {
		t.Fatal("start wrote without acceptUnmoved")
	}
}

func TestStartRejectsInvalidRequests(t *testing.T) {
	_, jobs, svc := startFixture()
	for _, tc := range []struct {
		name, requester, from string
		req                   *model.StartAccountMergeRequest
		want                  error
	}{
		{"nil body", "u1", "a", nil, ErrInvalidInput},
		{"no target", "u1", "a", &model.StartAccountMergeRequest{AcceptUnmoved: true}, ErrInvalidInput},
		{"self", "u1", "a", &model.StartAccountMergeRequest{IntoAccountID: "a", AcceptUnmoved: true}, ErrInvalidInput},
		{"malformed", "u1", "a", &model.StartAccountMergeRequest{IntoAccountID: "b#x", AcceptUnmoved: true}, ErrInvalidInput},
		{"stranger", "u9", "a", &model.StartAccountMergeRequest{IntoAccountID: "b", AcceptUnmoved: true}, ErrForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := svc.Start(context.Background(), tc.requester, tc.from, tc.req); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
	if len(jobs.started) != 0 {
		t.Fatal("invalid start wrote a job")
	}
}

func TestStartRejectsLargeContactIndex(t *testing.T) {
	for _, tc := range []struct {
		name       string
		from, into int
		want       error
	}{
		{"fits exactly", 4000, 6000, nil},
		{"sum too large", 4000, 6001, ErrMergeContactIndexTooLarge},
		{"source over cap", 10001, 0, ErrMergeContactIndexTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, jobs, svc := startFixture()
			repo.contacts["a"], repo.contacts["b"] = tc.from, tc.into
			_, err := svc.Start(context.Background(), "u1", "a", &model.StartAccountMergeRequest{IntoAccountID: "b", AcceptUnmoved: true})
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if tc.want != nil && len(jobs.started) != 0 {
				t.Fatal("oversized merge started")
			}
		})
	}
}

func TestStartResumesFailedJob(t *testing.T) {
	repo, jobs, svc := startFixture()
	repo.accounts["a"].MergingInto = "b"
	// Past the contacts step the contact cap no longer applies.
	repo.contacts["b"] = 20000
	jobs.job = &model.AccountMergeJob{SourceAccountID: "a", TargetAccountID: "b", RequesterUserID: "u1",
		State: model.MergeStateFailed, Step: "meetings", Cursor: "owner:m9", ErrorCode: MergeErrorRequesterRevoked,
		Counts: map[string]int{"members.moved": 3}}
	dto, err := svc.Start(context.Background(), "u1", "a", &model.StartAccountMergeRequest{IntoAccountID: "b", AcceptUnmoved: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs.resumed) != 1 || len(jobs.started) != 0 {
		t.Fatalf("started=%d resumed=%d", len(jobs.started), len(jobs.resumed))
	}
	job := jobs.resumed[0]
	if job.Step != "meetings" || job.Cursor != "owner:m9" || job.ErrorCode != "" || job.State != model.MergeStateRunning ||
		job.Counts["members.moved"] != 3 || job.LeaseOwner != "" {
		t.Fatalf("resumed job = %+v", job)
	}
	if dto.Step != "meetings" || dto.ErrorCode != "" {
		t.Fatalf("dto = %+v", dto)
	}

	// A failed job for another target cannot be redirected.
	repo.addAccount("c", "")
	repo.addMember("c", "u1", model.RoleSA)
	if _, err := svc.Start(context.Background(), "u1", "a", &model.StartAccountMergeRequest{IntoAccountID: "c", AcceptUnmoved: true}); !errors.Is(err, ErrAccountMergeIneligible) {
		t.Fatalf("redirect err = %v, want ErrAccountMergeIneligible", err)
	}

	// A resume before the contacts step still checks the contact cap.
	jobs.job.Step = "contacts"
	if _, err := svc.Start(context.Background(), "u1", "a", &model.StartAccountMergeRequest{IntoAccountID: "b", AcceptUnmoved: true}); !errors.Is(err, ErrMergeContactIndexTooLarge) {
		t.Fatalf("contacts resume err = %v", err)
	}
}

func TestStartRejectsRunningJob(t *testing.T) {
	repo, jobs, svc := startFixture()
	repo.accounts["a"].MergingInto = "b"
	jobs.job = &model.AccountMergeJob{SourceAccountID: "a", TargetAccountID: "b", RequesterUserID: "u1", State: model.MergeStateRunning, Step: "contacts"}
	req := &model.StartAccountMergeRequest{IntoAccountID: "b", AcceptUnmoved: true}
	if _, err := svc.Start(context.Background(), "u1", "a", req); !errors.Is(err, ErrMergeJobRunning) {
		t.Fatalf("err = %v, want ErrMergeJobRunning", err)
	}
	if len(jobs.started)+len(jobs.resumed) != 0 {
		t.Fatal("running job was overwritten")
	}

	// A job that appeared between the read and the transaction is also a running merge.
	repo2, jobs2, svc2 := startFixture()
	_ = repo2
	jobs2.writeErr = fmt.Errorf("wrapped: %w", repository.ErrMergeJobExists)
	if _, err := svc2.Start(context.Background(), "u1", "a", req); !errors.Is(err, ErrMergeJobRunning) {
		t.Fatalf("race err = %v, want ErrMergeJobRunning", err)
	}
	jobs2.writeErr = fmt.Errorf("wrapped: %w", repository.ErrConditionFailed)
	if _, err := svc2.Start(context.Background(), "u1", "a", req); !errors.Is(err, ErrAccountMergeConflict) {
		t.Fatalf("conflict err = %v, want ErrAccountMergeConflict", err)
	}
}

func TestStatusVisibility(t *testing.T) {
	repo, jobs, svc := startFixture()
	repo.addMember("a", "am", model.RoleAM)
	repo.addMember("b", "bm", model.RoleSA)
	repo.addAccount("c", "")
	repo.addMember("c", "stranger", model.RoleOwner)
	ctx := context.Background()

	if _, err := svc.Status(ctx, "am", "a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("source member without job: %v, want ErrNotFound", err)
	}
	if _, err := svc.Status(ctx, "bm", "a"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("target member without job: %v, want ErrForbidden", err)
	}

	jobs.job = &model.AccountMergeJob{SourceAccountID: "a", TargetAccountID: "b", RequesterUserID: "u1", State: model.MergeStateRunning,
		Step: "events", Cursor: "EVENT#secret", LeaseOwner: "req-1", Counts: map[string]int{"contacts.moved": 4}}
	for _, tc := range []struct {
		user string
		want error
	}{{"am", nil}, {"bm", nil}, {"u1", nil}, {"stranger", ErrForbidden}, {"", ErrInvalidInput}} {
		t.Run(tc.user, func(t *testing.T) {
			dto, err := svc.Status(ctx, tc.user, "a")
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if tc.want == nil && (dto.Step != "events" || dto.Counts["contacts.moved"] != 4 || dto.IntoAccountID != "b") {
				t.Fatalf("dto = %+v", dto)
			}
		})
	}
	if _, err := svc.Status(ctx, "am", "a#b"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("malformed id: %v", err)
	}
}

func TestMergeRequesterRevokedFailsJob(t *testing.T) {
	for _, tc := range []struct {
		name     string
		revoke   func(r *fakeMergeRepo)
		failErr  error
		wantFail bool
	}{
		{"left target", func(r *fakeMergeRepo) { delete(r.members["b"], "u1") }, nil, true},
		{"lost owner role", func(r *fakeMergeRepo) { r.members["a"]["u1"].Role = model.RoleSA }, nil, true},
		{"lost META owner", func(r *fakeMergeRepo) { r.accounts["a"].OwnerUserID = "u2" }, nil, true},
		{"lease moved elsewhere", func(*fakeMergeRepo) {}, nil, false},
		{"revoked but lease also gone", func(r *fakeMergeRepo) { delete(r.members["b"], "u1") }, repository.ErrMergeFenceLost, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, jobs, svc := startFixture()
			repo.accounts["a"].MergingInto = "b"
			jobs.failErr = tc.failErr
			tc.revoke(repo)
			// A mover transaction fails on the fence ...
			mover := newFakeMoverRepo()
			mover.add("a", "u2", model.RoleSA)
			mover.putErr = repository.ErrMergeFenceLost
			svc.SetMoverRepo(mover)
			job := &model.AccountMergeJob{SourceAccountID: "a", TargetAccountID: "b", RequesterUserID: "u1",
				State: model.MergeStateRunning, Step: "members", LeaseOwner: "w1"}
			res, err := svc.moveMembers(newMergeFence(context.Background()), job)
			if !errors.Is(err, ErrMergeFenceLost) || res.Cursor != "" || len(mover.puts) != 0 {
				t.Fatalf("mover: res=%+v err=%v puts=%v", res, err, mover.puts)
			}
			// ... and the worker's fence-loss handling decides the job's fate.
			failed, err := svc.handleMergeFenceLost(context.Background(), job)
			if err != nil {
				t.Fatal(err)
			}
			if failed != tc.wantFail {
				t.Fatalf("failed = %v, want %v", failed, tc.wantFail)
			}
			if tc.wantFail && !reflect.DeepEqual(jobs.failed, []string{MergeErrorRequesterRevoked}) {
				t.Fatalf("fail codes = %v", jobs.failed)
			}
			if !tc.wantFail && len(jobs.failed) != 0 {
				t.Fatalf("unexpected failure write: %v", jobs.failed)
			}
		})
	}
}

func TestAddMergeMoveCounts(t *testing.T) {
	counts := map[string]int{"contacts.moved": 2}
	addMergeMoveCounts(counts, "contacts", mergeMoveResult{Moved: 3, Unreadable: 1, KeptSeparate: 2, Skipped: 4, Resumed: 5})
	addMergeMoveCounts(counts, "events", mergeMoveResult{})
	want := map[string]int{"contacts.moved": 5, "contacts.unreadable": 1, "contacts.keptSeparate": 2, "contacts.skipped": 4, "contacts.resumed": 5}
	if !reflect.DeepEqual(counts, want) {
		t.Fatalf("counts = %v, want %v", counts, want)
	}
}

func TestAccountMergeJobDTOHidesLease(t *testing.T) {
	job := &model.AccountMergeJob{SourceAccountID: "a", TargetAccountID: "b", LeaseOwner: "req-1", LeaseUntil: 99, Cursor: "c",
		Counts: map[string]int{"x.moved": 1}}
	dto := job.DTO()
	dto.Counts["x.moved"] = 7
	if job.Counts["x.moved"] != 1 {
		t.Fatal("DTO shares the job's counts map")
	}
	v := reflect.ValueOf(*dto)
	for _, hidden := range []string{"LeaseOwner", "LeaseUntil", "Cursor", "RequesterUserID"} {
		if v.FieldByName(hidden).IsValid() {
			t.Fatalf("DTO exposes %s", hidden)
		}
	}
}
