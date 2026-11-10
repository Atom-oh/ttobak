package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/aws/aws-lambda-go/lambdacontext"
	"github.com/aws/aws-sdk-go-v2/aws"
	dbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	ebtypes "github.com/aws/aws-sdk-go-v2/service/eventbridge/types"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

const (
	// mergeLeaseTTL is how long one claim or renewal holds the job.
	mergeLeaseTTL = 5 * time.Minute
	// mergeLeaseRenewAfter is how old a claim may get before the worker
	// renews it ahead of the next mover call.
	mergeLeaseRenewAfter = time.Minute
	// mergeDeadlineReserve is how long before the invocation deadline the
	// worker stops, releases the lease and publishes a continuation.
	mergeDeadlineReserve = 3 * time.Minute
	// mergeStepAttempts bounds consecutive failed attempts of one step call
	// before the job fails.
	mergeStepAttempts = 4
	mergeRetryBackoff = 250 * time.Millisecond
	// mergeCallMargin ends each mover call this long before the invocation
	// deadline and the lease expiry.
	mergeCallMargin = 30 * time.Second
	// mergeReleaseTimeout bounds the best-effort lease release after an
	// error.
	mergeReleaseTimeout = 5 * time.Second

	// MergeErrorConflict is the job error code when a step kept failing on
	// concurrent changes (condition failures, transaction conflicts or a
	// revoked summary approval) after its bounded retries.
	MergeErrorConflict = "MERGE_CONFLICT"
	// MergeErrorStepFailed is the job error code when a step kept failing
	// for any other reason after its bounded retries.
	MergeErrorStepFailed = "STEP_FAILED"
	// MergeErrorDispatchFailed is the job error code when start could not
	// publish the worker event; restarting the merge resumes it.
	MergeErrorDispatchFailed = "DISPATCH_FAILED"
)

// accountMergeWorkerRepo lists the lease, progress and hierarchy writers the
// worker uses beyond the start/status job store and the sealed movers.
type accountMergeWorkerRepo interface {
	SaveMergeProgress(ctx context.Context, job *model.AccountMergeJob, step, cursor string, counts map[string]int) error
	ReleaseMergeLease(ctx context.Context, job *model.AccountMergeJob) error
	FinishAccountMerge(ctx context.Context, job *model.AccountMergeJob) error

	ReparentAccountForMerge(ctx context.Context, job *model.AccountMergeJob, childID string, intoAncestors []repository.AccountParentLink) error
	ListProjectRefsForAccount(ctx context.Context, accountID string) ([]model.ProjectRef, error)
	MoveProjectAccountLinkForMerge(ctx context.Context, job *model.AccountMergeJob, projectID string) error
	ListResearchRefsForAccount(ctx context.Context, accountID string) ([]model.ResearchRef, error)
	MoveResearchAccountLinkForMerge(ctx context.Context, job *model.AccountMergeJob, researchID string) error
	MergeAccountMetadata(ctx context.Context, job *model.AccountMergeJob, into *model.Account, aliases, domains []string) error
}

// mergeStepFunc runs one bounded mover call for the job's current step.
type mergeStepFunc func(fence *mergeFence, job *model.AccountMergeJob) (mergeMoveResult, error)

// SetWorkerRepo wires the worker's lease, progress and hierarchy writers.
func (s *AccountMergeService) SetWorkerRepo(worker accountMergeWorkerRepo) { s.worker = worker }

// SetMergePublisher wires the AccountMergeRequested publisher used by start,
// resume and the worker's continuation.
func (s *AccountMergeService) SetMergePublisher(fn func(context.Context, model.AccountMergeRequested) error) {
	s.publishMerge = fn
}

// SetPeopleResolvePublisher wires the publisher the worker uses to rerun the
// people resolver for the target after a finished merge.
func (s *AccountMergeService) SetPeopleResolvePublisher(fn func(context.Context, model.PeopleResolveRequested) error) {
	s.publishPeople = fn
}

// AccountMergePublisher publishes one AccountMergeRequested event.
func AccountMergePublisher(client actionEventClient) func(context.Context, model.AccountMergeRequested) error {
	return func(ctx context.Context, event model.AccountMergeRequested) error {
		if validateAccountHierarchyIDs(event.SourceAccountID, "") != nil {
			return ErrInvalidInput
		}
		detail, err := json.Marshal(event)
		if err != nil {
			return err
		}
		out, err := client.PutEvents(ctx, &eventbridge.PutEventsInput{Entries: []ebtypes.PutEventsRequestEntry{{
			EventBusName: aws.String("default"), Source: aws.String("ttobak.analysis"),
			DetailType: aws.String("AccountMergeRequested"), Detail: aws.String(string(detail)),
		}}})
		if err == nil && (out == nil || out.FailedEntryCount != 0 || len(out.Entries) != 1 || aws.ToString(out.Entries[0].EventId) == "") {
			err = errors.New("account merge event was not accepted")
		}
		return err
	}
}

// dispatchMerge publishes the worker event for a just started or resumed
// job. When publication fails the job would stay running with nobody to run
// it, so it is failed with DISPATCH_FAILED (restart resumes it) unless a
// worker already holds it.
func (s *AccountMergeService) dispatchMerge(ctx context.Context, job *model.AccountMergeJob) error {
	err := s.publishMerge(ctx, model.AccountMergeRequested{SourceAccountID: job.SourceAccountID})
	if err == nil {
		return nil
	}
	claimed, claimErr := s.jobs.ClaimMergeLease(ctx, job.SourceAccountID, s.workerID(ctx), s.clock(), time.Minute)
	if claimErr == nil {
		if failErr := s.jobs.FailMergeJob(ctx, claimed, MergeErrorDispatchFailed); failErr == nil {
			job.State, job.ErrorCode = claimed.State, claimed.ErrorCode
		}
	}
	return fmt.Errorf("dispatch account merge: %w", err)
}

func (s *AccountMergeService) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *AccountMergeService) pause(ctx context.Context, d time.Duration) error {
	if s.sleep != nil {
		return s.sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// workerID is unique per call: the Lambda request ID (when present) plus a
// random suffix, so two deliveries never share a lease owner.
func (s *AccountMergeService) workerID(ctx context.Context) string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	id := hex.EncodeToString(b[:])
	if lc, ok := lambdacontext.FromContext(ctx); ok && lc.AwsRequestID != "" {
		id = lc.AwsRequestID + "/" + id
	}
	return id
}

// mergeStep returns the mover for step, or nil for the finish step.
func (s *AccountMergeService) mergeStep(step string) mergeStepFunc {
	if s.steps != nil {
		return s.steps[step]
	}
	switch step {
	case "members":
		return s.moveMembers
	case "children":
		return s.moveChildren
	case "projects":
		return s.moveProjects
	case "research":
		return s.moveResearch
	case "contacts":
		return s.moveContacts
	case "events":
		return s.moveEvents
	case "meetings":
		return s.moveMeetings
	case "metadata":
		return s.moveMetadata
	}
	return nil
}

// isTransactionConflict reports whether a transaction was cancelled because
// a concurrent transaction touched one of its items.
func isTransactionConflict(err error) bool {
	var cancelled *dbtypes.TransactionCanceledException
	if errors.As(err, &cancelled) {
		for _, reason := range cancelled.CancellationReasons {
			if aws.ToString(reason.Code) == "TransactionConflict" {
				return true
			}
		}
	}
	var conflict *dbtypes.TransactionConflictException
	return errors.As(err, &conflict)
}

// mergeRetryable reports whether err is a concurrent change a fresh attempt
// can get past.
func mergeRetryable(err error) bool {
	return errors.Is(err, repository.ErrConditionFailed) || errors.Is(err, repository.ErrSummaryAccess) ||
		errors.Is(err, repository.ErrAccountMerging)
}

// ProcessAccountMergeRequested runs the source's merge job. It claims a
// five-minute lease under a per-call worker ID (a job leased elsewhere, or
// no longer running, is a no-op), then runs model.MergeSteps from the saved
// step. Each mover call is bounded by the mover itself; after it the step's
// counts and cursor are saved, and a finished step advances with an empty
// cursor, so events run only after contacts reported Done. A failing call
// is retried a bounded number of times and then fails the job with a code;
// a lost fence fails the job only when the requester lost authority. Three
// minutes before deadline the worker releases the lease and publishes a
// continuation. At the finish step the job and tombstone are written and a
// people resolve run is requested for the target.
func (s *AccountMergeService) ProcessAccountMergeRequested(ctx context.Context, e model.AccountMergeRequested, deadline time.Time) error {
	if s.jobs == nil || s.worker == nil || s.mover == nil || s.publishMerge == nil || s.publishPeople == nil {
		return fmt.Errorf("account merge worker is not configured")
	}
	if validateAccountHierarchyIDs(e.SourceAccountID, "") != nil {
		// A retry cannot make the event valid; acknowledge it.
		log.Printf("account merge: invalid source id in event, ignored")
		return nil
	}
	owner := s.workerID(ctx)
	claimedAt := s.clock()
	job, err := s.jobs.ClaimMergeLease(ctx, e.SourceAccountID, owner, claimedAt, mergeLeaseTTL)
	if errors.Is(err, repository.ErrMergeLeaseHeld) {
		log.Printf("account merge: source=%s lease held elsewhere or job not running", e.SourceAccountID)
		return nil
	}
	if err != nil {
		return err
	}
	attempts := 0
	for {
		now := s.clock()
		if !now.Add(mergeDeadlineReserve).Before(deadline) {
			return s.continueMerge(ctx, job)
		}
		if now.Sub(claimedAt) >= mergeLeaseRenewAfter {
			renewed, err := s.jobs.ClaimMergeLease(ctx, job.SourceAccountID, owner, now, mergeLeaseTTL)
			switch {
			case errors.Is(err, repository.ErrMergeLeaseHeld):
				return s.mergeFenceLost(ctx, job)
			case err != nil:
				if stop, ret := s.retryMergeStep(ctx, job, &attempts, err); stop {
					return ret
				}
				continue
			}
			job.LeaseUntil, claimedAt = renewed.LeaseUntil, now
		}

		done, err := s.runBoundedMergeStep(ctx, job, s.moverCallBudget(now, deadline))
		if done {
			return err
		}
		if err == nil {
			attempts = 0
			continue
		}
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			// The call ran out of its time bound, not the job: hand off
			// without counting a failed attempt. Movers are idempotent, so
			// the next worker repeats the unsaved part.
			log.Printf("account merge: source=%s step=%s call time bound reached", job.SourceAccountID, job.Step)
			return s.continueMerge(ctx, job)
		}
		if stop, ret := s.retryMergeStep(ctx, job, &attempts, err); stop {
			return ret
		}
	}
}

// moverCallBudget bounds one mover call (and its progress save): it ends
// mergeCallMargin before the invocation deadline and within the lease TTL
// less that margin, so a call never outlives the lease it writes under.
func (s *AccountMergeService) moverCallBudget(now, deadline time.Time) time.Duration {
	if s.callBudget > 0 {
		return s.callBudget
	}
	return min(deadline.Sub(now)-mergeCallMargin, mergeLeaseTTL-mergeCallMargin)
}

// runBoundedMergeStep runs runMergeStep under a fresh fence whose context
// ends after budget.
func (s *AccountMergeService) runBoundedMergeStep(ctx context.Context, job *model.AccountMergeJob, budget time.Duration) (bool, error) {
	callCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	fence := newMergeFence(callCtx)
	defer fence.cancel(nil)
	return s.runMergeStep(fence, job)
}

// retryMergeStep decides what a failed step call (or lease renewal) means:
// a lost fence goes to handleMergeFenceLost, a terminal mover error fails the
// job with its code, and anything else is retried after a short pause until
// mergeStepAttempts consecutive failures fail the job. stop reports that the
// invocation ends with ret.
func (s *AccountMergeService) retryMergeStep(ctx context.Context, job *model.AccountMergeJob, attempts *int, err error) (stop bool, ret error) {
	switch {
	case errors.Is(err, ErrMergeFenceLost):
		return true, s.mergeFenceLost(ctx, job)
	case ctx.Err() != nil:
		s.releaseQuietly(ctx, job)
		return true, ctx.Err()
	case MergeFailureCode(err) != "":
		return true, s.failMerge(ctx, job, MergeFailureCode(err), err)
	}
	*attempts++
	if *attempts >= mergeStepAttempts {
		code := MergeErrorStepFailed
		if mergeRetryable(err) || isTransactionConflict(err) {
			code = MergeErrorConflict
		}
		return true, s.failMerge(ctx, job, code, err)
	}
	log.Printf("account merge: source=%s step=%s attempt=%d retrying", job.SourceAccountID, job.Step, *attempts)
	if err := s.pause(ctx, time.Duration(*attempts)*mergeRetryBackoff); err != nil {
		s.releaseQuietly(ctx, job)
		return true, err
	}
	return false, nil
}

// runMergeStep runs one mover call (or the finish) and saves its progress.
// done reports that the job finished.
func (s *AccountMergeService) runMergeStep(fence *mergeFence, job *model.AccountMergeJob) (done bool, err error) {
	ctx := fence.Context()
	if job.Step == model.MergeSteps[len(model.MergeSteps)-1] {
		if err := s.worker.FinishAccountMerge(ctx, job); err != nil {
			return false, fence.translate(err)
		}
		log.Printf("account merge: source=%s finished", job.SourceAccountID)
		if err := s.publishPeople(ctx, model.PeopleResolveRequested{AccountID: job.TargetAccountID, Reason: model.PeopleResolveReasonContacts}); err != nil {
			// The merge is complete; a redelivery would find it done, so the
			// failure is surfaced rather than retried.
			log.Printf("account merge: source=%s people resolve request failed", job.SourceAccountID)
			return true, fmt.Errorf("account merge finished but people resolve was not requested: %w", err)
		}
		return true, nil
	}
	mover := s.mergeStep(job.Step)
	if mover == nil {
		return false, fmt.Errorf("unknown merge step %q", job.Step)
	}
	result, err := mover(fence, job)
	if err != nil {
		return false, err
	}
	counts := make(map[string]int, len(job.Counts)+5)
	for k, v := range job.Counts {
		counts[k] = v
	}
	addMergeMoveCounts(counts, job.Step, result)
	step, cursor := job.Step, result.Cursor
	if result.Done {
		step, cursor = model.MergeSteps[model.MergeStepIndex(job.Step)+1], ""
	}
	if err := s.worker.SaveMergeProgress(ctx, job, step, cursor, counts); err != nil {
		return false, fence.translate(err)
	}
	return false, nil
}

// continueMerge hands the job to a fresh invocation: release the lease so
// the next delivery can claim it, then publish the continuation. A failed
// publication is returned so the current event is retried.
func (s *AccountMergeService) continueMerge(ctx context.Context, job *model.AccountMergeJob) error {
	if err := s.worker.ReleaseMergeLease(ctx, job); err != nil {
		if errors.Is(err, ErrMergeFenceLost) {
			return s.mergeFenceLost(ctx, job)
		}
		s.releaseQuietly(ctx, job)
		return err
	}
	log.Printf("account merge: source=%s step=%s continuing in a new invocation", job.SourceAccountID, job.Step)
	return s.publishMerge(ctx, model.AccountMergeRequested{SourceAccountID: job.SourceAccountID})
}

func (s *AccountMergeService) mergeFenceLost(ctx context.Context, job *model.AccountMergeJob) error {
	failed, err := s.handleMergeFenceLost(ctx, job)
	if err != nil {
		// Authority could not be read or the failure not written. If the
		// lease is still ours, free it so a redelivery or restart can run.
		s.releaseQuietly(ctx, job)
		return err
	}
	log.Printf("account merge: source=%s step=%s fence lost failed=%t", job.SourceAccountID, job.Step, failed)
	return nil
}

// failMerge fails the job with code. Error text may carry IDs or SDK detail,
// so only the code is logged.
func (s *AccountMergeService) failMerge(ctx context.Context, job *model.AccountMergeJob, code string, cause error) error {
	log.Printf("account merge: source=%s step=%s failed code=%s", job.SourceAccountID, job.Step, code)
	if err := s.jobs.FailMergeJob(ctx, job, code); err != nil {
		if errors.Is(err, ErrMergeFenceLost) {
			return nil
		}
		s.releaseQuietly(ctx, job)
		return errors.Join(err, cause)
	}
	return nil
}

// releaseQuietly frees job's lease after a non-terminal error so the
// Lambda retry, the next event or a restart can claim it at once. It runs on
// a fresh short context (the invocation's may be cancelled), is best effort,
// and logs only an outcome code.
func (s *AccountMergeService) releaseQuietly(ctx context.Context, job *model.AccountMergeJob) {
	if job == nil || job.LeaseOwner == "" {
		return
	}
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), mergeReleaseTimeout)
	defer cancel()
	if err := s.worker.ReleaseMergeLease(releaseCtx, job); err != nil {
		code := "error"
		if errors.Is(err, ErrMergeFenceLost) {
			code = "lease_moved"
		}
		log.Printf("account merge: source=%s lease release failed code=%s", job.SourceAccountID, code)
	}
}
