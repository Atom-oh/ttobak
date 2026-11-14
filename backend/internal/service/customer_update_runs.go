package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"regexp"
	"sort"
	"time"

	"github.com/aws/smithy-go"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// Run budgets and bounds (contracts/customer-updates/run-v1.json).
const (
	maxRunUsersPerTick     = 25
	maxPendingMarksRead    = 1000
	maxRunItems            = 30
	maxDayDrafts           = 20
	maxAccountDayDrafts    = customerUpdateDailyDrafts
	maxRunDraftsBudget     = 30
	maxRunResearchPairs    = 10
	maxItemAttempts        = 3
	runLeaseDuration       = 30 * time.Minute
	runStartMinRemaining   = 60 * time.Second
	maxRunAccountBytes     = 8000
	defaultMaxPayloadBytes = 1 << 20
	maxRunDraftLinksStored = 5
	maxRunWhyRunes         = 400
	maxRunSourceTitleRunes = 300
	maxDraftFallbackOpptys = 10
	maxStoredCodeBytes     = 200
	// DefaultResearchPairsPerDay is the per-user per-KST-day research cap
	// (CUSTOMER_UPDATE_RESEARCH_PAIRS_PER_DAY); 0 starts no runs.
	DefaultResearchPairsPerDay = 30
	maxItemAge                 = 72 * time.Hour
	failedBuildHold            = time.Hour
)

// Decision outcome codes written by the Go finalize step into the stored
// decision's plaintext discardReason (decisions are not exposed by any API).
// With outcome "discarded":
//   - out_of_scope: the account is no longer in scope for the user (InScope
//     false now, or the block-list/opportunity read denied membership).
//   - item_missing: the leased inbox item row no longer exists.
//   - draft_missing: the DEC row said drafted but the run has no DRAFT row.
//   - encoding: a body, why, title or list entry is not valid UTF-8.
//   - links_invalid: more than 5 links, or a link outside fetchedUrls (valid
//     public https URLs only) plus the item URL.
//   - check codes of contracts/customer-updates/draft-checks-v1.json for the
//     body, in their fixed order, then why_invalid (why empty, over 400 code
//     points, or failing the body checks other than link_missing and
//     no_meeting_offer), joined by commas.
//
// With outcome "deferred" (the item is held until the next KST day):
//   - day_budget: the user already has 20 drafts today.
//   - account_day_budget: the account already has 3 drafts today.
//
// A runtime DEC row deferred for account_budget or draft_budget also holds its
// item until the next KST day; one deferred for research_budget leaves it new
// (attempts unchanged).
//
// A runtime-written discardReason is stored only when it is a short code
// ([a-z0-9_:,]*), else "invalid".
const (
	discardOutOfScope     = "out_of_scope"
	discardItemMissing    = "item_missing"
	discardDraftMissing   = "draft_missing"
	discardEncoding       = "encoding"
	discardLinksInvalid   = "links_invalid"
	discardWhyInvalid     = "why_invalid"
	deferDayBudget        = "day_budget"
	deferAccountDayBudget = "account_day_budget"

	runtimeAccountBudget  = "account_budget"
	runtimeDraftBudget    = "draft_budget"
	runtimeResearchBudget = "research_budget"
)

var (
	updateRunIDPattern    = regexp.MustCompile(`^[0-9a-f]{32}$`)
	runDiscardCodePattern = regexp.MustCompile(`^[a-z0-9_:,]*$`)
	runBackendCodePattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,64}$`)
	kstZone               = time.FixedZone("KST", 9*3600)
)

// ErrRunNotAccepted means the agent runtime answered without accepting the
// run (400, 409 BUSY/RUN_EXISTS, 503 or an unexpected body). Wrapped with a
// code only, never with the response body.
var ErrRunNotAccepted = errors.New("customer update run not accepted")

// ErrRunOutcomeUnknown means the invoke may have reached the runtime and the
// run may be running (a timeout, a transport error, an unreadable answer).
var ErrRunOutcomeUnknown = errors.New("customer update run outcome unknown")

// RunNotAcceptedError is a definite answer from the runtime that is not an
// acceptance. Code is the runtime's error code (letters, digits and
// underscores only); Contract marks a payload rejection (HTTP 400 or 413 with
// a code other than BUSY, RUN_EXISTS or UNAVAILABLE), which is deterministic.
// It wraps ErrRunNotAccepted.
type RunNotAcceptedError struct {
	Code     string
	Contract bool
}

func (e *RunNotAcceptedError) Error() string { return ErrRunNotAccepted.Error() + ": " + e.Code }
func (e *RunNotAcceptedError) Unwrap() error { return ErrRunNotAccepted }

// AgentInvoker starts one agent run. runID is single-use: a failed call is
// never retried with the same id.
type AgentInvoker interface {
	Invoke(ctx context.Context, runID string, payload []byte) error
}

// updateRunsRepo is the repository surface of the run phases.
type updateRunsRepo interface {
	ListPendingUsers(ctx context.Context, limit int) ([]model.UpdatePendingMark, error)
	GetPendingCursor(ctx context.Context) (string, error)
	SetPendingCursor(ctx context.Context, lastUserID string) error
	ClearPending(ctx context.Context, mark model.UpdatePendingMark) error
	GetRun(ctx context.Context, userID string) (*model.UpdateRun, error)
	CreateRunIfAbsent(ctx context.Context, userID string, run *model.UpdateRun) error
	DeleteRunIfMatch(ctx context.Context, userID, runID string) error
	ReadRunRows(ctx context.Context, runID string) (*model.RunRows, error)
	DeleteRunRows(ctx context.Context, runID string) error
	ListEligibleItems(ctx context.Context, userID string, limit int) ([]model.UpdateItem, int, bool, error)
	GetItem(ctx context.Context, userID, urlHash string) (*model.UpdateItem, error)
	SetItemState(ctx context.Context, userID, urlHash, state string, attemptsDelta int) error
	SetItemHold(ctx context.Context, userID, urlHash string, until time.Time) error
	GetDay(ctx context.Context, userID, day string) (*model.UpdateDay, error)
	AddDayDraft(ctx context.Context, userID, day, accountID string, maxTotal, maxPerAccount int) error
	AddDayResearched(ctx context.Context, userID, day string, n int) error
	PutDecisionIfAbsent(ctx context.Context, userID string, d *model.UpdateDecision) error
	GetDecision(ctx context.Context, userID, urlHash, accountID string) (*model.UpdateDecision, error)
	ListDecidedAccounts(ctx context.Context, userID, urlHash string) ([]string, error)
	ReplaceOpenDecision(ctx context.Context, userID string, d *model.UpdateDecision) error
	SetDecisionOutcome(ctx context.Context, userID, urlHash, accountID, outcome, discardReason string) error
	GetDraft(ctx context.Context, userID, accountID, urlHash string) (*model.UpdateDraft, error)
	PutDraftIfAbsent(ctx context.Context, userID string, d *model.UpdateDraft) error
}

// updateRunsContext is CustomerUpdateContext as the run phases use it.
type updateRunsContext interface {
	BuildWithStats(ctx context.Context, userID string, dayCounts map[string]int) ([]AccountPack, BuildStats, error)
	InScope(ctx context.Context, userID, accountID string) (bool, error)
	BlockList(ctx context.Context, userID, accountID string) ([]string, error)
	OpenOpportunities(ctx context.Context, userID, accountID string) ([]AccountPackOpportunity, error)
}

// CustomerUpdateRunsDeps wires the run phases. A nil Invoker turns them off.
type CustomerUpdateRunsDeps struct {
	Repo    updateRunsRepo
	Context updateRunsContext
	Invoker AgentInvoker
}

// RunStats are counts only, safe to log. Oversized counts finalize attempts
// that met a run partition over the row bound (once per attempt).
// StartAmbiguous counts invokes whose outcome is unknown (the lease is kept).
// BuildFailedHeld counts users whose batch was held because an in-scope
// account's pack failed to build.
type RunStats struct {
	Users, Started, StartFailed, StartAmbiguous, Finalized, Expired, Held int
	Drafts, Discarded, Deferred, Ignored, OutOfScope                      int
	Malformed, Unreadable, Oversized, Leaked, ServicesUnavailable         int
	Errors, ItemErrors, BuildFailedHeld                                   int
}

// Enabled reports whether the run phases are on (an agent runtime is wired).
func (r *CustomerUpdateRuns) Enabled() bool { return r.deps.Invoker != nil }

// CustomerUpdateRuns starts agent runs for pending users and finalizes
// finished ones. The runtime is untrusted: every result row is checked
// against the run lease and re-validated before anything is written under
// the user's partition. Logs carry run ids, user-id hash prefixes, codes and
// counts only.
type CustomerUpdateRuns struct {
	deps            CustomerUpdateRunsDeps
	now             func() time.Time
	newRunID        func() (string, error)
	maxPayloadBytes int
	researchCap     int
}

// NewCustomerUpdateRuns creates the run phases; a nil now uses time.Now.
func NewCustomerUpdateRuns(deps CustomerUpdateRunsDeps, now func() time.Time) *CustomerUpdateRuns {
	if now == nil {
		now = time.Now
	}
	return &CustomerUpdateRuns{deps: deps, now: now, newRunID: randomRunID, maxPayloadBytes: defaultMaxPayloadBytes,
		researchCap: DefaultResearchPairsPerDay}
}

// SetResearchPairsPerDay sets the per-user per-KST-day research cap; 0 starts
// no runs, a negative value restores the default.
func (r *CustomerUpdateRuns) SetResearchPairsPerDay(n int) {
	if n < 0 {
		n = DefaultResearchPairsPerDay
	}
	r.researchCap = n
}

func randomRunID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// kstDay is the yyyymmdd day key in Korea Standard Time (fixed +09:00, no tzdata).
func kstDay(t time.Time) string { return t.In(kstZone).Format("20060102") }

// nextKSTDayStart is 00:00 KST of the day after t.
func nextKSTDayStart(t time.Time) time.Time {
	k := t.In(kstZone)
	return time.Date(k.Year(), k.Month(), k.Day()+1, 0, 0, 0, 0, kstZone)
}

func userHashPrefix(userID string) string {
	sum := sha256.Sum256([]byte(userID))
	return hex.EncodeToString(sum[:])[:12]
}

// runErrorClass names an error without its message: an AWS API error code,
// a not-accepted code, a context error, or the Go type.
func runErrorClass(err error) string {
	var api smithy.APIError
	var na *RunNotAcceptedError
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return "context"
	case errors.Is(err, ErrRunOutcomeUnknown):
		return "unknown"
	case errors.As(err, &na):
		return "not_accepted:" + na.Code
	case errors.Is(err, ErrRunNotAccepted):
		return "not_accepted"
	case errors.As(err, &api):
		return api.ErrorCode()
	case errors.Is(err, repository.ErrConditionFailed):
		return "condition_failed"
	}
	return fmt.Sprintf("%T", err)
}

func timeLeft(ctx context.Context, min time.Duration) bool {
	deadline, ok := ctx.Deadline()
	return !ok || time.Until(deadline) >= min
}

// rotatePending orders marks by user id and returns up to n of them, starting
// at the first id strictly greater than cursor (the last user served) and
// wrapping around, so marks that stay (holds, leases, caps) cannot starve the
// users after them.
func rotatePending(marks []model.UpdatePendingMark, cursor string, n int) []model.UpdatePendingMark {
	if len(marks) == 0 {
		return marks
	}
	sorted := append([]model.UpdatePendingMark(nil), marks...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].UserID < sorted[j].UserID })
	start := sort.Search(len(sorted), func(i int) bool { return sorted[i].UserID > cursor }) % len(sorted)
	out := make([]model.UpdatePendingMark, 0, min(n, len(sorted)))
	for i := 0; i < len(sorted) && len(out) < n; i++ {
		out = append(out, sorted[(start+i)%len(sorted)])
	}
	return out
}

// Tick reads up to 1,000 pending marks and finalizes and starts runs for at
// most 25 of them, resuming after the saved rotation cursor, stopping before
// a user when less than 60 seconds remain, and then saves the last user it
// served. It returns an error only when the pending list cannot be read; a
// cursor read failure starts from the first mark, and per-user and cursor
// write failures are logged and counted in Errors.
func (r *CustomerUpdateRuns) Tick(ctx context.Context) (RunStats, error) {
	var stats RunStats
	if r.deps.Invoker == nil {
		return stats, nil
	}
	all, err := r.deps.Repo.ListPendingUsers(ctx, maxPendingMarksRead)
	if err != nil {
		return stats, err
	}
	cursor, err := r.deps.Repo.GetPendingCursor(ctx)
	if err != nil {
		cursor = ""
		log.Printf("customer update pending cursor read failed err=%s", runErrorClass(err))
	}
	marks := rotatePending(all, cursor, maxRunUsersPerTick)
	last := ""
	for i, mark := range marks {
		if i == maxRunUsersPerTick || !timeLeft(ctx, runStartMinRemaining) {
			break
		}
		last = mark.UserID
		stats.Users++
		if runID, err := r.handleUser(ctx, mark, &stats); err != nil {
			stats.Errors++
			log.Printf("customer update run user failed user=%s run=%s err=%s", userHashPrefix(mark.UserID), runID, runErrorClass(err))
		}
	}
	if last != "" {
		if err := r.deps.Repo.SetPendingCursor(ctx, last); err != nil {
			stats.Errors++
			log.Printf("customer update pending cursor write failed err=%s", runErrorClass(err))
		}
	}
	return stats, nil
}

// handleUser finalizes the user's run, if any, then starts a new one when the
// lease is gone and time remains. It returns the run id it worked on.
func (r *CustomerUpdateRuns) handleUser(ctx context.Context, mark model.UpdatePendingMark, stats *RunStats) (string, error) {
	run, err := r.deps.Repo.GetRun(ctx, mark.UserID)
	if err != nil {
		return "", err
	}
	if run != nil {
		cleared, err := r.finalize(ctx, mark.UserID, run, stats)
		if err != nil || !cleared {
			return run.RunID, err
		}
	}
	if !timeLeft(ctx, runStartMinRemaining) {
		return "", nil
	}
	return r.start(ctx, mark, stats)
}

// setItem updates an item; a vanished item (TTL) is not an error.
func (r *CustomerUpdateRuns) setItem(ctx context.Context, userID, urlHash, state string, delta int) error {
	err := r.deps.Repo.SetItemState(ctx, userID, urlHash, state, delta)
	if errors.Is(err, repository.ErrConditionFailed) {
		return nil
	}
	return err
}

// holdItem keeps a new item out of runs until the next KST day.
func (r *CustomerUpdateRuns) holdItem(ctx context.Context, userID, urlHash string, now time.Time) error {
	return r.holdItemUntil(ctx, userID, urlHash, nextKSTDayStart(now))
}

// holdItemUntil keeps a new item out of runs until the given time.
func (r *CustomerUpdateRuns) holdItemUntil(ctx context.Context, userID, urlHash string, until time.Time) error {
	err := r.deps.Repo.SetItemHold(ctx, userID, urlHash, until)
	if errors.Is(err, repository.ErrConditionFailed) {
		return nil
	}
	return err
}
