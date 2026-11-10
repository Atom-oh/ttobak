package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

var (
	// ErrMergeJobRunning means the source already has a running merge job.
	ErrMergeJobRunning = errors.New("account merge already running")
	// ErrAccountMergeConflict means an account, membership or the hierarchy
	// changed between the start checks and the start transaction.
	ErrAccountMergeConflict = errors.New("account merge start conflict")
)

// MergeErrorRequesterRevoked is the job error code when the requester lost
// Owner of the source or membership of the target mid-job.
const MergeErrorRequesterRevoked = "REQUESTER_REVOKED"

// accountMergeJobRepo lists the merge job store calls start, status and
// fence-loss handling use.
type accountMergeJobRepo interface {
	GetAccountMergeJob(ctx context.Context, sourceID string) (*model.AccountMergeJob, error)
	StartAccountMerge(ctx context.Context, job *model.AccountMergeJob, intoAncestors []repository.AccountParentLink) error
	ResumeAccountMerge(ctx context.Context, job *model.AccountMergeJob, intoAncestors []repository.AccountParentLink) error
	FailMergeJob(ctx context.Context, job *model.AccountMergeJob, code string) error
}

// SetJobRepo wires the merge job store.
func (s *AccountMergeService) SetJobRepo(jobs accountMergeJobRepo) { s.jobs = jobs }

// Start begins merging fromID into req.IntoAccountID, or restarts a failed
// job for the same target from its saved step. Every eligibility rule is
// checked here and again inside the start transaction. A source with unmoved
// categories requires AcceptUnmoved. Before the contacts step has run, the
// source and target contacts together must fit the dedupe index. The job is
// created unleased; the worker claims it.
func (s *AccountMergeService) Start(ctx context.Context, requesterID, fromID string, req *model.StartAccountMergeRequest) (*model.AccountMergeJobDTO, error) {
	if s.jobs == nil {
		return nil, fmt.Errorf("account merge job store is not configured")
	}
	if req == nil || requesterID == "" || req.IntoAccountID == "" {
		return nil, ErrInvalidInput
	}
	intoID := req.IntoAccountID
	if err := validateAccountHierarchyIDs(fromID, intoID); err != nil || fromID == intoID {
		return nil, ErrInvalidInput
	}
	existing, err := s.jobs.GetAccountMergeJob(ctx, fromID)
	if err != nil {
		return nil, err
	}
	resumeInto := ""
	if existing != nil && (existing.State == model.MergeStateRunning || existing.State == model.MergeStateFailed) {
		resumeInto = existing.TargetAccountID
	}
	subjects, err := s.authorize(ctx, requesterID, fromID, intoID, resumeInto)
	if err != nil {
		return nil, err
	}
	if existing != nil && existing.State == model.MergeStateRunning {
		return nil, ErrMergeJobRunning
	}
	resume := existing != nil && existing.State == model.MergeStateFailed && existing.TargetAccountID == intoID
	unmoved, err := s.unmovedCategories(ctx, fromID)
	if err != nil {
		return nil, err
	}
	for _, u := range unmoved {
		if u.Present && !req.AcceptUnmoved {
			return nil, ErrInvalidInput
		}
	}
	if !resume || model.MergeStepIndex(existing.Step) <= model.MergeStepIndex("contacts") {
		if err := s.checkContactIndexFits(ctx, fromID, intoID); err != nil {
			return nil, err
		}
	}
	now := time.Now().UTC()
	job := &model.AccountMergeJob{SourceAccountID: fromID, TargetAccountID: intoID, RequesterUserID: requesterID,
		State: model.MergeStateRunning, Step: model.MergeSteps[0], AcceptUnmoved: req.AcceptUnmoved,
		Counts: map[string]int{}, StartedAt: now, UpdatedAt: now}
	if resume {
		job.Step, job.Cursor, job.Counts, job.StartedAt = existing.Step, existing.Cursor, existing.Counts, existing.StartedAt
	}
	ancestors := append([]repository.AccountParentLink{{AccountID: intoID, ParentAccountID: subjects.Into.ParentAccountID}}, subjects.IntoAncestors...)
	write := s.jobs.StartAccountMerge
	if resume {
		write = s.jobs.ResumeAccountMerge
	}
	if err := write(ctx, job, ancestors); err != nil {
		switch {
		case errors.Is(err, repository.ErrMergeJobExists):
			return nil, ErrMergeJobRunning
		case errors.Is(err, repository.ErrConditionFailed):
			return nil, ErrAccountMergeConflict
		}
		return nil, err
	}
	return job.DTO(), nil
}

// checkContactIndexFits rejects a merge whose source and target contacts
// together exceed the contacts step's dedupe index, counting sort keys only.
func (s *AccountMergeService) checkContactIndexFits(ctx context.Context, fromID, intoID string) error {
	total := 0
	for _, id := range []string{fromID, intoID} {
		keys, over, err := s.repo.ListAccountPartitionKeys(ctx, id, mergeContactPrefix, mergeContactIndexCap)
		if err != nil {
			return err
		}
		total += len(keys)
		if over || total > mergeContactIndexCap {
			return ErrMergeContactIndexTooLarge
		}
	}
	return nil
}

// Status returns the source's merge job to a direct member of the source or
// of the job's target. A source member sees ErrNotFound when no job exists;
// anyone else sees ErrForbidden.
func (s *AccountMergeService) Status(ctx context.Context, userID, fromID string) (*model.AccountMergeJobDTO, error) {
	if s.jobs == nil {
		return nil, fmt.Errorf("account merge job store is not configured")
	}
	if userID == "" {
		return nil, ErrInvalidInput
	}
	if err := validateAccountHierarchyIDs(fromID, ""); err != nil {
		return nil, ErrInvalidInput
	}
	sourceMember, err := s.repo.GetMember(ctx, fromID, userID)
	if err != nil {
		return nil, err
	}
	job, err := s.jobs.GetAccountMergeJob(ctx, fromID)
	if err != nil {
		return nil, err
	}
	if sourceMember != nil && sourceMember.UserID == userID {
		if job == nil {
			return nil, ErrNotFound
		}
		return job.DTO(), nil
	}
	if job == nil || job.TargetAccountID == "" {
		return nil, ErrForbidden
	}
	targetMember, err := s.repo.GetMember(ctx, job.TargetAccountID, userID)
	if err != nil {
		return nil, err
	}
	if targetMember == nil || targetMember.UserID != userID {
		return nil, ErrForbidden
	}
	return job.DTO(), nil
}

// requesterAuthorized reports whether the job's requester still owns the
// source (META owner and MEMBER role) and is a direct target member.
func (s *AccountMergeService) requesterAuthorized(ctx context.Context, job *model.AccountMergeJob) (bool, error) {
	source, err := s.repo.GetAccount(ctx, job.SourceAccountID)
	if err != nil {
		return false, err
	}
	if source == nil || source.OwnerUserID != job.RequesterUserID {
		return false, nil
	}
	owner, err := s.repo.GetMember(ctx, job.SourceAccountID, job.RequesterUserID)
	if err != nil {
		return false, err
	}
	if owner == nil || owner.Role != model.RoleOwner {
		return false, nil
	}
	member, err := s.repo.GetMember(ctx, job.TargetAccountID, job.RequesterUserID)
	if err != nil {
		return false, err
	}
	return member != nil && member.UserID == job.RequesterUserID, nil
}

// handleMergeFenceLost decides what a mover's ErrMergeFenceLost means. When
// the requester lost Owner of the source or membership of the target, the
// job fails with REQUESTER_REVOKED and failed is true. Otherwise the lease
// moved to another worker (or the job already ended) and this worker stops
// without writing.
func (s *AccountMergeService) handleMergeFenceLost(ctx context.Context, job *model.AccountMergeJob) (failed bool, err error) {
	authorized, err := s.requesterAuthorized(ctx, job)
	if err != nil {
		return false, err
	}
	if authorized {
		return false, nil
	}
	if err := s.jobs.FailMergeJob(ctx, job, MergeErrorRequesterRevoked); err != nil {
		if errors.Is(err, repository.ErrMergeFenceLost) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// addMergeMoveCounts adds one mover run's outcome to the job counters under
// "{step}.{outcome}" keys; zero outcomes are not stored.
func addMergeMoveCounts(counts map[string]int, step string, r mergeMoveResult) {
	for outcome, n := range map[string]int{"moved": r.Moved, "skipped": r.Skipped, "resumed": r.Resumed,
		"unreadable": r.Unreadable, "keptSeparate": r.KeptSeparate} {
		if n > 0 {
			counts[step+"."+outcome] += n
		}
	}
}
