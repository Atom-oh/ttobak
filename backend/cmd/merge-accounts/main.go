// merge-accounts is a manually operated CLI (not a Lambda) that merges one
// account (-from) into another (-into) as a named requester. It runs the
// same service paths as POST /api/accounts/{id}/merge: the requester must own
// the source (MEMBER role and META owner) and be a direct member of the
// target, and every eligibility rule, the contact index bound and the
// accept-unmoved requirement apply exactly as they do for the API. The CLI
// never bypasses them.
//
// -status prints the source's job state, step, error code and counts to a
// direct member of the source or target, as GET /api/accounts/{id}/merge.
//
// -verify is a read-only scan for after the merge: it skips the preview (a
// merged source is ineligible) and counts rows still naming -from. The
// requester must be a direct member of -into.
//
// -apply defaults to false: a dry run prints the merge preview counts and,
// with -scan-users, the counts of user-partition rows the CLI would rewrite.
// Nothing is written, not even a lease. With -apply the CLI starts the job
// (or restarts a failed one from its saved step, or takes over a running job
// whose lease expired), rewrites the user-partition rows under a CLI-held
// job lease when -scan-users is set, then runs the merge worker in-process
// until the job is done or failed. The lease owner is cli-<host>-<pid>.
//
// -scan-users scans the table (bounded parallel segments, at most
// -max-pages pages each) for rows only the CLI can reach: SpecReq tasks and
// calendar events classified to the source (accountId A to B, and
// shareAccountId and candidate account IDs inside the sealed event payload),
// and link-only meetings (moved by the same writer the worker uses, which
// never publishes them). Published meetings are left to the worker. A row
// that fails to move is counted in errors; the worker is then not started,
// so a rerun resumes from the same state.
//
// Output is JSON counts and the job state only: no names, titles, mention
// text, calendar fields or contact fields. Errors print a code only.
//
// Requires TABLE_NAME (no fallback), KMS_KEY_ID for sealed contacts, events
// and calendar rows (with -apply), and standard AWS credentials/region.
//
// Usage:
//
//	TABLE_NAME=ttobak-main KMS_KEY_ID=... go run ./cmd/merge-accounts -from A -into B -requester USER_ID [-scan-users] [-accept-unmoved] [-apply]
//	TABLE_NAME=ttobak-main go run ./cmd/merge-accounts -from A -requester USER_ID -status
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-lambda-go/lambdacontext"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/sealing"
	"github.com/ttobak/backend/internal/service"
)

const (
	defaultMaxPages = 100000
	scanSegments    = 4
	// workerWindow is the deadline given to each in-process worker run; the
	// worker hands off before it and the loop starts the next run.
	workerWindow = 15 * time.Minute
	// maxWorkerRuns and maxIdleRuns bound the in-process worker loop.
	maxWorkerRuns = 5000
	maxIdleRuns   = 120
	idlePause     = 5 * time.Second
	// leaseTTL and leaseRenewAfter govern the CLI-held lease during the scan.
	leaseTTL        = 5 * time.Minute
	leaseRenewAfter = time.Minute
	// rowMoveTimeout bounds one row's decrypt, re-seal and write, well inside
	// leaseTTL minus leaseRenewAfter.
	rowMoveTimeout = 30 * time.Second
)

var accountID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

type options struct {
	From, Into, Requester string
	Apply, AcceptUnmoved  bool
	ScanUsers, Status     bool
	Verify                bool
	MaxPages              int
}

var (
	errFromIntoRequired = errors.New("-from and -into are required and must differ")
	errRequesterNeeded  = errors.New("-requester is required")
	errInvalidMaxPages  = errors.New("-max-pages must be > 0")
	errNoProgress       = errors.New("merge worker made no progress; the job lease may be held elsewhere")
	errVerifyReadOnly   = errors.New("-verify is read-only and excludes -apply and -status")
	errScanIncomplete   = errors.New("user row scan left rows behind; worker not started")
)

func validateOptions(o options) error {
	if o.Status {
		// A status read names only the source; -into comes from the job.
		if !accountID.MatchString(o.From) {
			return errFromIntoRequired
		}
	} else if !accountID.MatchString(o.From) || !accountID.MatchString(o.Into) || o.From == o.Into {
		return errFromIntoRequired
	}
	if !accountID.MatchString(o.Requester) {
		return errRequesterNeeded
	}
	if o.MaxPages <= 0 {
		return errInvalidMaxPages
	}
	if o.Verify && (o.Apply || o.Status) {
		return errVerifyReadOnly
	}
	return nil
}

// mergeService is the part of service.AccountMergeService the CLI drives.
type mergeService interface {
	Status(ctx context.Context, userID, fromID string) (*model.AccountMergeJobDTO, error)
	Preview(ctx context.Context, requesterID, fromID, intoID string) (*model.AccountMergePreview, error)
	Start(ctx context.Context, requesterID, fromID string, req *model.StartAccountMergeRequest) (*model.AccountMergeJobDTO, error)
	ProcessAccountMergeRequested(ctx context.Context, e model.AccountMergeRequested, deadline time.Time) error
}

type memberReader interface {
	GetMember(ctx context.Context, accountID, userID string) (*model.AccountMember, error)
}

type jobStore interface {
	GetAccountMergeJob(ctx context.Context, sourceID string) (*model.AccountMergeJob, error)
	ClaimMergeLease(ctx context.Context, sourceID, owner string, now time.Time, ttl time.Duration) (*model.AccountMergeJob, error)
	ReleaseMergeLease(ctx context.Context, job *model.AccountMergeJob) error
	FailMergeJob(ctx context.Context, job *model.AccountMergeJob, code string) error
}

type userRowStore interface {
	ScanMergeUserRowsSegmentPage(ctx context.Context, accountID string, segment, segments int, cursor string) (*repository.MergeUserRowsPage, error)
	MoveSpecReqTaskRowForMerge(ctx context.Context, job *model.AccountMergeJob, row repository.MergeUserRow) error
	MoveCalendarEventRowForMerge(ctx context.Context, job *model.AccountMergeJob, row repository.MergeUserRow, ciphertext string) error
	MoveMeetingForAccountMerge(ctx context.Context, job *model.AccountMergeJob, ownerID, meetingID string, ref *model.MeetingRef) error
}

type payloadCipher interface {
	Decrypt(ctx context.Context, userID, sourceID, eventID, ciphertext string) ([]byte, error)
	Encrypt(ctx context.Context, userID, sourceID, eventID string, plaintext []byte) (string, error)
}

type deps struct {
	svc     mergeService
	jobs    jobStore
	members memberReader
	users   userRowStore
	cipher  payloadCipher
	// owner is the lease owner, cli-<host>-<pid>.
	owner string
	now   func() time.Time
	pause func(context.Context, time.Duration) error
}

type result struct {
	Mode                  string         `json:"mode"`
	From                  string         `json:"from"`
	Into                  string         `json:"into"`
	State                 string         `json:"state,omitempty"`
	Step                  string         `json:"step,omitempty"`
	ErrorCode             string         `json:"errorCode,omitempty"`
	Counts                map[string]int `json:"counts,omitempty"`
	UserRows              map[string]int `json:"userRows,omitempty"`
	UnmovedPresent        []string       `json:"unmovedPresent,omitempty"`
	RequiresAcceptUnmoved bool           `json:"requiresAcceptUnmoved,omitempty"`
	// NewAudience is ADR-072's disclosure: target members absent from the
	// source and whether the target has ancestors. Tell members before -apply.
	NewAudience *model.MergeAudience `json:"newAudience,omitempty"`
}

// run executes one invocation. The result is always returned so the caller
// can print what was done even when err is set.
func run(ctx context.Context, d deps, o options) (result, error) {
	res := result{Mode: "dry-run", From: o.From, Into: o.Into}
	if err := validateOptions(o); err != nil {
		return res, err
	}
	if o.Status {
		res.Mode = "status"
		dto, err := d.svc.Status(ctx, o.Requester, o.From)
		if err != nil {
			return res, err
		}
		res.Into, res.State, res.Step, res.ErrorCode, res.Counts = dto.IntoAccountID, dto.State, dto.Step, dto.ErrorCode, dto.Counts
		return res, nil
	}
	if o.Verify {
		// Read-only check that the CLI-only rows no longer name the source.
		// It skips Preview, which refuses a merged source, and requires only
		// that the requester is a direct member of the target.
		res.Mode = "verify"
		member, err := d.members.GetMember(ctx, o.Into, o.Requester)
		if err != nil {
			return res, err
		}
		if member == nil || member.UserID != o.Requester {
			return res, service.ErrForbidden
		}
		counts, err := d.scanUserRows(ctx, o, nil)
		res.UserRows = counts
		return res, err
	}
	if !o.Apply {
		preview, err := d.svc.Preview(ctx, o.Requester, o.From, o.Into)
		if err != nil {
			return res, err
		}
		res.Counts, res.RequiresAcceptUnmoved = preview.Counts, preview.RequiresAcceptUnmoved
		audience := preview.NewAudience
		res.NewAudience = &audience
		for _, u := range preview.Unmoved {
			if u.Present {
				res.UnmovedPresent = append(res.UnmovedPresent, u.Category)
			}
		}
		if o.ScanUsers {
			counts, err := d.scanUserRows(ctx, o, nil)
			res.UserRows = counts
			return res, err
		}
		return res, nil
	}

	res.Mode = "apply"
	dto, err := d.svc.Start(ctx, o.Requester, o.From, &model.StartAccountMergeRequest{IntoAccountID: o.Into, AcceptUnmoved: o.AcceptUnmoved})
	if err != nil {
		return res, err
	}
	res.State, res.Step = dto.State, dto.Step
	if o.ScanUsers {
		counts, err := d.scanApply(ctx, o)
		res.UserRows = counts
		if err != nil {
			// Report the persisted state (failed/SCAN_INCOMPLETE), not Start's.
			if job, getErr := d.jobs.GetAccountMergeJob(ctx, o.From); getErr == nil && job != nil {
				res.State, res.Step, res.ErrorCode = job.State, job.Step, job.ErrorCode
			}
			return res, err
		}
		// The worker finalizes the job and a merged source refuses Start, so
		// a row left behind now could never be moved by a rerun.
		if counts["errors"] > 0 || skippedRows(counts) > 0 {
			return res, errScanIncomplete
		}
	}
	job, err := d.drive(ctx, o.From)
	if job != nil {
		res.State, res.Step, res.ErrorCode = job.State, job.Step, job.ErrorCode
		res.Counts = job.DTO().Counts
	}
	if err == nil && job != nil && job.State == model.MergeStateFailed {
		err = errors.New("merge job failed")
	}
	return res, err
}

// scanApplyPasses bounds the re-scans that pick up rows a concurrent edit
// made the CLI skip (its version condition failed).
const scanApplyPasses = 3

// scanIncompleteCode is the job error code that keeps a scan-incomplete job
// out of the worker until an -apply rerun restarts it.
const scanIncompleteCode = "SCAN_INCOMPLETE"

// scanApply claims the job lease once and holds it through every scan pass,
// re-scanning while rows were skipped and nothing failed. Moved counts
// accumulate across passes; skip counts come from the last pass.
//
// A clean scan returns with the lease still held: the in-process worker claims
// with the same owner, so no remote delivery can take the job between the scan
// and the drive. A scan that leaves rows behind fails the job with
// SCAN_INCOMPLETE instead of releasing it. A failed job is never processed by
// a delivery, so it cannot finish over stranded rows, and an -apply rerun
// restarts it from its saved step.
func (d deps) scanApply(ctx context.Context, o options) (map[string]int, error) {
	lease := &cliLease{d: d, source: o.From}
	if err := lease.renew(ctx); err != nil {
		return nil, err
	}
	total := map[string]int{}
	var err error
	for pass := 0; pass < scanApplyPasses; pass++ {
		var counts map[string]int
		counts, err = d.scanUserRows(ctx, o, lease)
		for key := range total {
			if strings.HasSuffix(key, "Skipped") {
				delete(total, key)
			}
		}
		for key, n := range counts {
			if strings.HasSuffix(key, "Moved") || strings.HasPrefix(key, "errors") || strings.HasSuffix(key, "Skipped") {
				total[key] += n
			} else {
				total[key] = n
			}
		}
		if err != nil || total["errors"] > 0 || skippedRows(counts) == 0 {
			break
		}
	}
	if err == nil && total["errors"] == 0 && skippedRows(total) == 0 {
		return total, nil
	}
	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	job := lease.current()
	if job == nil {
		if err == nil {
			err = errScanIncomplete
		}
		return total, err
	}
	// Fail the job even after a lost fence: FailMergeJob is conditioned on
	// this lease owner, so a real takeover refuses it, while a revocation under
	// our lease leaves a barrier instead of an unleased running job a retried
	// delivery would finish over the rows left behind.
	if ferr := d.jobs.FailMergeJob(stopCtx, job, scanIncompleteCode); ferr != nil && err == nil {
		err = ferr
	}
	if err == nil {
		err = errScanIncomplete
	}
	return total, err
}

func skippedRows(counts map[string]int) int {
	n := 0
	for key, v := range counts {
		if strings.HasSuffix(key, "Skipped") {
			n += v
		}
	}
	return n
}

// cliLease keeps the CLI's claim on the job fresh for the duration of a scan.
type cliLease struct {
	d       deps
	source  string
	mu      sync.Mutex
	job     *model.AccountMergeJob
	claimed time.Time
}

func (l *cliLease) current() *model.AccountMergeJob {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.job
}

func (l *cliLease) renew(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.d.now()
	if l.job != nil && now.Sub(l.claimed) < leaseRenewAfter {
		return nil
	}
	job, err := l.d.jobs.ClaimMergeLease(ctx, l.source, l.d.owner, now, leaseTTL)
	if err != nil {
		return err
	}
	l.job, l.claimed = job, now
	return nil
}

// scanUserRows scans the user partitions for rows classified to o.From. A nil
// lease is a dry run: rows are counted and nothing is read beyond the scan.
func (d deps) scanUserRows(ctx context.Context, o options, lease *cliLease) (map[string]int, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		mu     sync.Mutex
		counts = map[string]int{}
		failed error
		wg     sync.WaitGroup
	)
	add := func(key string, n int) {
		mu.Lock()
		counts[key] += n
		mu.Unlock()
	}
	for segment := 0; segment < scanSegments; segment++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := d.scanSegment(ctx, o, lease, segment, add); err != nil {
				mu.Lock()
				if failed == nil {
					failed = err
				}
				mu.Unlock()
				cancel()
			}
		}()
	}
	wg.Wait()
	return counts, failed
}

func (d deps) scanSegment(ctx context.Context, o options, lease *cliLease, segment int, add func(string, int)) error {
	cursor := ""
	for pages := 0; ; pages++ {
		if pages >= o.MaxPages {
			return errors.New("user scan page limit exceeded")
		}
		if lease != nil {
			if err := lease.renew(ctx); err != nil {
				return err
			}
		}
		page, err := d.users.ScanMergeUserRowsSegmentPage(ctx, o.From, segment, scanSegments, cursor)
		if err != nil {
			return err
		}
		add("invalid", page.Invalid)
		for _, row := range page.Rows {
			if err := d.handleRow(ctx, o, lease, row, add); err != nil {
				return err
			}
		}
		if page.NextCursor == "" {
			return nil
		}
		cursor = page.NextCursor
	}
}

// handleRow counts a scanned row and, in apply mode, rewrites it. A fenced
// failure (lost lease, requester revoked, account merging) aborts the scan;
// a changed row is skipped; any other failure is counted and the scan goes on.
func (d deps) handleRow(ctx context.Context, o options, lease *cliLease, row repository.MergeUserRow, add func(string, int)) error {
	var (
		kind string
		move func(context.Context) error
	)
	switch row.Kind {
	case repository.MergeUserRowSpecReqTask:
		kind = "specreqTasks"
		move = func(ctx context.Context) error { return d.users.MoveSpecReqTaskRowForMerge(ctx, lease.current(), row) }
	case repository.MergeUserRowCalendar:
		kind = "calendarEvents"
		move = func(ctx context.Context) error { return d.moveCalendarEvent(ctx, o, lease.current(), row) }
	case repository.MergeUserRowMeeting:
		if row.SharedToAccount {
			// Published meetings move in the worker's meetings step.
			add("publishedMeetings", 1)
			return nil
		}
		kind = "linkOnlyMeetings"
		move = func(ctx context.Context) error {
			return d.users.MoveMeetingForAccountMerge(ctx, lease.current(), row.UserID, row.MeetingID, repository.MeetingRefForMergeRow(o.From, row))
		}
	default:
		add("invalid", 1)
		return nil
	}
	if lease == nil {
		add(kind, 1)
		return nil
	}
	// Renew before every row and bound the row's work, so a slow page (KMS
	// throttling, a dense segment) never outlives the lease mid-page.
	if err := lease.renew(ctx); err != nil {
		return err
	}
	rowCtx, cancel := context.WithTimeout(ctx, rowMoveTimeout)
	err := move(rowCtx)
	cancel()
	switch {
	case err == nil:
		add(kind+"Moved", 1)
	case errors.Is(err, repository.ErrMergeFenceLost), errors.Is(err, repository.ErrAccountMerging), ctx.Err() != nil:
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	case errors.Is(err, repository.ErrConditionFailed):
		add(kind+"Skipped", 1)
	default:
		add("errors", 1)
		add("errors."+failureReason(err), 1)
	}
	return nil
}

// reasonError tags a row failure with a fixed reason for the error counts.
type reasonError struct {
	reason string
	err    error
}

func (e *reasonError) Error() string { return e.reason + ": " + e.err.Error() }
func (e *reasonError) Unwrap() error { return e.err }

func tagReason(reason string, err error) error {
	if err == nil {
		return nil
	}
	return &reasonError{reason: reason, err: err}
}

func failureReason(err error) string {
	var tagged *reasonError
	if errors.As(err, &tagged) {
		return tagged.reason
	}
	return "write"
}

func (d deps) moveCalendarEvent(ctx context.Context, o options, job *model.AccountMergeJob, row repository.MergeUserRow) error {
	plaintext, err := d.cipher.Decrypt(ctx, row.UserID, row.SourceID, row.EventID, row.Ciphertext)
	if err != nil {
		return tagReason("decrypt", err)
	}
	rewritten, err := rewriteCalendarPayload(plaintext, o.From, o.Into)
	if err != nil {
		return tagReason("payload", err)
	}
	sealed, err := d.cipher.Encrypt(ctx, row.UserID, row.SourceID, row.EventID, rewritten)
	if err != nil {
		return tagReason("encrypt", err)
	}
	return d.users.MoveCalendarEventRowForMerge(ctx, job, row, sealed)
}

// rewriteCalendarPayload replaces the source account ID with the target in a
// decrypted calendar event payload: accountId and each candidate account ID.
// shareAccountId and its evidence are left as stored (the share row stays on
// the source). Candidates are deduplicated and drop the share account, then
// the result goes through the service's stored-payload normalization and size
// cap, so a payload that reads would reject is never produced. A payload that
// does not name the source as its account is rejected.
func rewriteCalendarPayload(plaintext []byte, from, to string) ([]byte, error) {
	var event model.CalendarEventInput
	decoder := json.NewDecoder(bytes.NewReader(plaintext))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&event) != nil || decoder.Decode(new(any)) != io.EOF || event.AccountID != from {
		return nil, errors.New("calendar payload does not match the source account")
	}
	event.AccountID = to
	if len(event.CandidateAccountIDs) > 0 {
		seen := map[string]bool{event.ShareAccountID: true}
		candidates := make([]string, 0, len(event.CandidateAccountIDs))
		for _, id := range event.CandidateAccountIDs {
			if id == from {
				id = to
			}
			if !seen[id] {
				seen[id] = true
				candidates = append(candidates, id)
			}
		}
		event.CandidateAccountIDs = candidates
	}
	return service.NormalizeCalendarPayload(event)
}

// drive runs the merge worker in-process until the job leaves the running
// state. The worker's continuation publisher is a no-op: this loop is the
// next invocation. Runs that move the job's progress reset the idle count; a
// job nothing advances (its lease held by another worker) ends the loop with
// errNoProgress after maxIdleRuns pauses.
func (d deps) drive(ctx context.Context, from string) (*model.AccountMergeJob, error) {
	// The worker claims as exactly the CLI owner, so it continues from a scan
	// lease the CLI still holds instead of being refused by it.
	runCtx := service.WithMergeWorkerOwner(lambdacontext.NewContext(ctx, &lambdacontext.LambdaContext{AwsRequestID: d.owner}), d.owner)
	var lastUpdate time.Time
	idle := 0
	for runs := 0; runs < maxWorkerRuns; runs++ {
		err := d.svc.ProcessAccountMergeRequested(runCtx, model.AccountMergeRequested{SourceAccountID: from}, d.now().Add(workerWindow))
		job, getErr := d.jobs.GetAccountMergeJob(ctx, from)
		if getErr != nil {
			return job, getErr
		}
		if job == nil {
			return nil, service.ErrNotFound
		}
		if job.State != model.MergeStateRunning || err != nil {
			return job, err
		}
		if job.UpdatedAt.Equal(lastUpdate) {
			if idle++; idle >= maxIdleRuns {
				return job, errNoProgress
			}
			if err := d.pause(ctx, idlePause); err != nil {
				return job, err
			}
		} else {
			idle = 0
		}
		lastUpdate = job.UpdatedAt
	}
	return nil, errNoProgress
}

// errorCode maps err to a fixed code; error text may carry IDs or SDK detail.
func errorCode(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, service.ErrForbidden):
		return "FORBIDDEN"
	case errors.Is(err, service.ErrNotFound):
		return "NOT_FOUND"
	case errors.Is(err, service.ErrInvalidInput), errors.Is(err, errFromIntoRequired), errors.Is(err, errRequesterNeeded), errors.Is(err, errInvalidMaxPages), errors.Is(err, errVerifyReadOnly):
		return "INVALID_INPUT"
	case errors.Is(err, service.ErrAccountMergeIneligible):
		return "INELIGIBLE"
	case errors.Is(err, service.ErrMergeJobRunning), errors.Is(err, repository.ErrMergeLeaseHeld):
		return "JOB_RUNNING"
	case errors.Is(err, service.ErrAccountMergeConflict):
		return "START_CONFLICT"
	case errors.Is(err, service.ErrMergeContactIndexTooLarge):
		return "CONTACT_INDEX_TOO_LARGE"
	case errors.Is(err, repository.ErrMergeFenceLost), errors.Is(err, repository.ErrAccountMerging):
		return "FENCE_LOST"
	case errors.Is(err, errScanIncomplete):
		return "SCAN_INCOMPLETE"
	case errors.Is(err, errNoProgress):
		return "NO_PROGRESS"
	}
	return "FAILED"
}

func writeResult(w io.Writer, r result) {
	if r.UnmovedPresent != nil {
		sort.Strings(r.UnmovedPresent)
	}
	raw, _ := json.Marshal(r)
	fmt.Fprintln(w, string(raw))
}

var unsafeOwner = regexp.MustCompile(`[^A-Za-z0-9_.-]`)

// leaseOwner returns cli-<host>-<pid>.
func leaseOwner(host string, pid int) string {
	return fmt.Sprintf("cli-%s-%d", unsafeOwner.ReplaceAllString(host, "_"), pid)
}

func main() {
	o := options{}
	flag.StringVar(&o.From, "from", "", "source account ID, merged into -into and left as a tombstone")
	flag.StringVar(&o.Into, "into", "", "target account ID")
	flag.StringVar(&o.Requester, "requester", "", "user ID of the requester: Owner of -from and a direct member of -into")
	flag.BoolVar(&o.Apply, "apply", false, "start or resume the merge (default: print counts only)")
	flag.BoolVar(&o.AcceptUnmoved, "accept-unmoved", false, "accept the categories v1 leaves on the source")
	flag.BoolVar(&o.ScanUsers, "scan-users", false, "also rewrite SpecReq tasks, calendar events and link-only meetings")
	flag.BoolVar(&o.Status, "status", false, "print the source's merge job state and counts (needs -from and -requester only)")
	flag.BoolVar(&o.Verify, "verify", false, "read-only: count CLI-only rows still naming -from; needs -from, -into and -requester (a direct member of -into); usable after the merge is done")
	flag.IntVar(&o.MaxPages, "max-pages", defaultMaxPages, "page limit per user scan segment")
	flag.Parse()
	if err := validateOptions(o); err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		os.Exit(1)
	}
	tableName, keyID := os.Getenv("TABLE_NAME"), os.Getenv("KMS_KEY_ID")
	if tableName == "" || (o.Apply && keyID == "") {
		fmt.Fprintln(os.Stderr, "ERROR: TABLE_NAME is required, and KMS_KEY_ID with -apply")
		os.Exit(1)
	}
	ctx := context.Background()
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERROR: load AWS config")
		os.Exit(1)
	}
	// The merge worker re-seals moved contacts and events with the target's
	// binding, so it uses the sealing repository view (as the summarize worker).
	repo := repository.NewDynamoDBRepository(dynamodb.NewFromConfig(cfg), tableName).MetadataView()
	kmsClient := kms.NewFromConfig(cfg)
	if o.Apply {
		sealer := sealing.NewEnvelope(kmsClient, keyID)
		if sealer == nil {
			fmt.Fprintln(os.Stderr, "ERROR: record sealer unavailable")
			os.Exit(1)
		}
		repo.SetRecordSealer(sealer)
	}
	svc := service.NewAccountMergeService(repo, nil)
	svc.SetJobRepo(repo)
	svc.SetMoverRepo(repo)
	svc.SetWorkerRepo(repo)
	// This process is the worker: start and the worker's continuation need no event.
	svc.SetMergePublisher(func(context.Context, model.AccountMergeRequested) error { return nil })
	svc.SetPeopleResolvePublisher(service.PeopleResolvePublisher(eventbridge.NewFromConfig(cfg)))
	host, _ := os.Hostname()
	d := deps{svc: svc, jobs: repo, members: repo, users: repo, cipher: service.NewCalendarCipher(kmsClient, keyID),
		owner: leaseOwner(host, os.Getpid()), now: time.Now, pause: pause}
	res, err := run(ctx, d, o)
	res.ErrorCode = firstNonEmpty(res.ErrorCode, errorCode(err))
	writeResult(os.Stdout, res)
	if err != nil {
		// Codes only: messages may carry IDs from wrapped SDK errors.
		fmt.Fprintln(os.Stderr, "ERROR: merge failed:", errorCode(err))
		os.Exit(1)
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func pause(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
