package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ttobak/backend/internal/model"
)

// Performance data is owner-private: every method scopes to the verified
// caller's own USER# partition and takes no other user identifier.

const (
	// MaxPerformanceAssignments caps assignments per owner and mapping list
	// items; repository.PerformanceListRowLimit must be one larger.
	MaxPerformanceAssignments = 500
	performanceMaxMutations   = 20
	performanceMaxNameBytes   = 200
	performanceMaxSubject     = 300
	performanceMaxLabelBytes  = 64
	performanceDateLayout     = "2006-01-02"
	performanceMaxRevision    = 1<<62 - 1
)

var (
	performanceUserPattern    = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	performanceIDPattern      = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	performanceAccountPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	performanceSources        = []string{model.PerformanceSourceSFDCAssignments, model.PerformanceSourceSFDCTasks}
	performancePeriods        = map[string]bool{"week": true, "quarter": true, "year": true}
)

type PerformanceStore interface {
	ApplyPerformanceBatch(ctx context.Context, userID, source string, req model.PerformanceSyncRequest, digest string) (*model.PerformanceSyncReceipt, error)
	// ReplayPerformanceBatch returns the stored receipt for an identical,
	// already-applied batch, nil when none exists, or ErrConditionFailed when
	// the batch ID was reused with a different digest.
	ReplayPerformanceBatch(ctx context.Context, userID, source, batchID, digest string) (*model.PerformanceSyncReceipt, error)
	ListPerformanceSources(ctx context.Context, userID string) ([]model.PerformanceSourceStatus, error)
	ListPerformanceAssignments(ctx context.Context, userID string) ([]model.SFDCAssignment, error)
	// GetAccountMappings returns the mappings for exactly the given SFDC IDs.
	GetAccountMappings(ctx context.Context, userID string, sfdcAccountIDs []string) (map[string]model.AccountMapping, error)
	PutAccountMapping(ctx context.Context, userID string, mapping model.AccountMapping, onlyIfNotManual bool) error
	DeleteAccountMappingIfAutomatic(ctx context.Context, userID, sfdcAccountID string) error
	GetPerformanceGoals(ctx context.Context, userID string, year int) (*model.PerformanceGoals, error)
	PutPerformanceGoals(ctx context.Context, userID string, goals model.PerformanceGoals) error
}

// PerformanceAccountReader is the AccountService read check used to confirm
// the caller can currently read a mapping target.
type PerformanceAccountReader interface {
	GetAccount(ctx context.Context, userID, accountID string) (*model.AccountResponse, error)
}

type PerformanceService struct {
	store    PerformanceStore
	accounts PerformanceAccountReader
	now      func() time.Time
}

func NewPerformanceService(store PerformanceStore, accounts PerformanceAccountReader) *PerformanceService {
	return &PerformanceService{store: store, accounts: accounts, now: time.Now}
}

func validPerformanceSourceName(source string) bool {
	return source == model.PerformanceSourceSFDCAssignments || source == model.PerformanceSourceSFDCTasks
}

// Sync validates and normalizes one batch, computes its payload digest, and
// applies it. The store replays an identical batch and rejects a reused batch
// ID with a different digest or a stale revision with ErrConditionFailed.
func (svc *PerformanceService) Sync(ctx context.Context, userID, source string, request model.PerformanceSyncRequest) (*model.PerformanceSyncReceipt, error) {
	if !performanceUserPattern.MatchString(userID) {
		return nil, ErrForbidden
	}
	if !validPerformanceSourceName(source) {
		return nil, ErrInvalidInput
	}
	normalized, err := svc.normalizePerformanceRequest(source, request)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return nil, ErrInvalidInput
	}
	sum := sha256.Sum256(encoded)
	digest := hex.EncodeToString(sum[:])
	// Replay first: the activity window depends on the clock, and an identical
	// retry of an applied batch must return its receipt after a year boundary.
	replayed, err := svc.store.ReplayPerformanceBatch(ctx, userID, source, normalized.BatchID, digest)
	if err != nil {
		return nil, err
	}
	if replayed != nil {
		return replayed, nil
	}
	if err := svc.checkPerformanceActivityWindow(normalized); err != nil {
		return nil, err
	}
	if source == model.PerformanceSourceSFDCAssignments {
		if err := svc.checkAssignmentCap(ctx, userID, normalized); err != nil {
			return nil, err
		}
	}
	receipt, err := svc.store.ApplyPerformanceBatch(ctx, userID, source, normalized, digest)
	if err != nil {
		return nil, err
	}
	if source == model.PerformanceSourceSFDCAssignments && normalized.Complete && !receipt.Replayed {
		// The sync is committed; an auto-mapping failure must not undo it.
		if err := svc.autoMapAssignments(ctx, userID, removedAssignmentIDs(normalized)); err != nil {
			log.Printf("performance auto mapping failed: %v", err)
		}
	}
	return receipt, nil
}

// checkAssignmentCap rejects a batch that takes the stored count (current plus
// new IDs minus removes) over MaxPerformanceAssignments. Updates and removes
// always pass. Read-then-write is not atomic; concurrent batches overshoot by
// at most performanceMaxMutations each, and reads are bounded independently.
func (svc *PerformanceService) checkAssignmentCap(ctx context.Context, userID string, req model.PerformanceSyncRequest) error {
	current, err := svc.store.ListPerformanceAssignments(ctx, userID)
	if err != nil {
		return err
	}
	stored := make(map[string]bool, len(current))
	for _, assignment := range current {
		stored[assignment.SFDCAccountID] = true
	}
	count := len(stored)
	for _, mutation := range req.Mutations {
		switch {
		case mutation.Op == "remove" && stored[mutation.ExternalID]:
			delete(stored, mutation.ExternalID)
			count--
		case mutation.Op == "upsert" && mutation.Assignment != nil && !stored[mutation.Assignment.SFDCAccountID]:
			stored[mutation.Assignment.SFDCAccountID] = true
			count++
		}
	}
	if count > MaxPerformanceAssignments && count > len(current) {
		return ErrInvalidInput
	}
	return nil
}

func (svc *PerformanceService) normalizePerformanceRequest(source string, request model.PerformanceSyncRequest) (model.PerformanceSyncRequest, error) {
	batchID, err := normalizeCalendarUUID(request.BatchID)
	if err != nil {
		return model.PerformanceSyncRequest{}, err
	}
	if request.ExpectedRevision < 0 || request.ExpectedRevision > performanceMaxRevision ||
		len(request.Mutations) > performanceMaxMutations {
		return model.PerformanceSyncRequest{}, ErrInvalidInput
	}
	normalized := model.PerformanceSyncRequest{
		BatchID: batchID, ExpectedRevision: request.ExpectedRevision, Complete: request.Complete,
		Mutations: make([]model.PerformanceMutation, 0, len(request.Mutations)),
	}
	for _, mutation := range request.Mutations {
		switch mutation.Op {
		case "remove":
			if mutation.Assignment != nil || mutation.Task != nil || !performanceIDPattern.MatchString(mutation.ExternalID) {
				return model.PerformanceSyncRequest{}, ErrInvalidInput
			}
			normalized.Mutations = append(normalized.Mutations, model.PerformanceMutation{Op: "remove", ExternalID: mutation.ExternalID})
		case "upsert":
			if mutation.ExternalID != "" {
				return model.PerformanceSyncRequest{}, ErrInvalidInput
			}
			next, err := normalizePerformanceUpsert(source, mutation)
			if err != nil {
				return model.PerformanceSyncRequest{}, err
			}
			normalized.Mutations = append(normalized.Mutations, next)
		default:
			return model.PerformanceSyncRequest{}, ErrInvalidInput
		}
	}
	return normalized, nil
}

// performanceActivityWindow is January 1 of the previous year (UTC) through
// one year after today, both inclusive, as YYYY-MM-DD strings.
func (svc *PerformanceService) performanceActivityWindow() (string, string) {
	now := svc.now().UTC()
	earliest := time.Date(now.Year()-1, time.January, 1, 0, 0, 0, 0, time.UTC)
	return earliest.Format(performanceDateLayout), now.AddDate(1, 0, 0).Format(performanceDateLayout)
}

// checkPerformanceActivityWindow applies the clock-dependent activity-date
// window to a normalized batch. It runs only for batches not yet applied.
func (svc *PerformanceService) checkPerformanceActivityWindow(req model.PerformanceSyncRequest) error {
	earliest, latest := svc.performanceActivityWindow()
	for _, mutation := range req.Mutations {
		if mutation.Task != nil && (mutation.Task.ActivityDate < earliest || mutation.Task.ActivityDate > latest) {
			return ErrInvalidInput
		}
	}
	return nil
}

func normalizePerformanceUpsert(source string, mutation model.PerformanceMutation) (model.PerformanceMutation, error) {
	switch source {
	case model.PerformanceSourceSFDCAssignments:
		assignment := mutation.Assignment
		if assignment == nil || mutation.Task != nil || !performanceIDPattern.MatchString(assignment.SFDCAccountID) ||
			!validPerformanceText(assignment.Name, performanceMaxNameBytes) || !validPerformanceText(assignment.Role, performanceMaxLabelBytes) {
			return model.PerformanceMutation{}, ErrInvalidInput
		}
		copied := *assignment
		return model.PerformanceMutation{Op: "upsert", Assignment: &copied}, nil
	case model.PerformanceSourceSFDCTasks:
		task := mutation.Task
		if task == nil || mutation.Assignment != nil || !performanceIDPattern.MatchString(task.TaskID) ||
			(task.SFDCAccountID != "" && !performanceIDPattern.MatchString(task.SFDCAccountID)) ||
			(task.SpecReqID != "" && !performanceIDPattern.MatchString(task.SpecReqID)) ||
			!validPerformanceText(task.Subject, performanceMaxSubject) || !validPerformanceText(task.Type, performanceMaxLabelBytes) ||
			!validPerformanceText(task.Status, performanceMaxLabelBytes) {
			return model.PerformanceMutation{}, ErrInvalidInput
		}
		date, err := time.Parse(performanceDateLayout, task.ActivityDate)
		if err != nil || date.Format(performanceDateLayout) != task.ActivityDate {
			return model.PerformanceMutation{}, ErrInvalidInput
		}
		copied := *task
		return model.PerformanceMutation{Op: "upsert", Task: &copied}, nil
	}
	return model.PerformanceMutation{}, ErrInvalidInput
}

func validPerformanceText(value string, maxBytes int) bool {
	return len(value) <= maxBytes && utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}

// ListSources reports every known source, including never-synced ones.
func (svc *PerformanceService) ListSources(ctx context.Context, userID string) (*model.PerformanceSourceList, error) {
	if !performanceUserPattern.MatchString(userID) {
		return nil, ErrForbidden
	}
	stored, err := svc.store.ListPerformanceSources(ctx, userID)
	if err != nil {
		return nil, err
	}
	bySource := make(map[string]model.PerformanceSourceStatus, len(stored))
	for _, status := range stored {
		bySource[status.Source] = status
	}
	list := &model.PerformanceSourceList{Sources: make([]model.PerformanceSourceStatus, 0, len(performanceSources))}
	for _, source := range performanceSources {
		status, found := bySource[source]
		if !found {
			status = model.PerformanceSourceStatus{Source: source}
		}
		list.Sources = append(list.Sources, status)
	}
	return list, nil
}

// validPerformanceGoalsYear keeps goals inside the retained window: the
// previous, current and next year.
func (svc *PerformanceService) validPerformanceGoalsYear(year int) bool {
	current := svc.now().UTC().Year()
	return year >= current-1 && year <= current+1
}

func (svc *PerformanceService) GetGoals(ctx context.Context, userID string, year int) (*model.PerformanceGoals, error) {
	if !performanceUserPattern.MatchString(userID) {
		return nil, ErrForbidden
	}
	if !svc.validPerformanceGoalsYear(year) {
		return nil, ErrInvalidInput
	}
	goals, err := svc.store.GetPerformanceGoals(ctx, userID, year)
	if err != nil {
		return nil, err
	}
	if goals == nil {
		return &model.PerformanceGoals{Year: year}, nil
	}
	return goals, nil
}

func (svc *PerformanceService) PutGoals(ctx context.Context, userID string, year int, text string) (*model.PerformanceGoals, error) {
	if !performanceUserPattern.MatchString(userID) {
		return nil, ErrForbidden
	}
	if !svc.validPerformanceGoalsYear(year) || !validPerformanceText(text, model.PerformanceGoalsMaxBytes) {
		return nil, ErrInvalidInput
	}
	goals := model.PerformanceGoals{Year: year, Text: text, UpdatedAt: svc.now().UTC().Format(time.RFC3339)}
	if err := svc.store.PutPerformanceGoals(ctx, userID, goals); err != nil {
		return nil, err
	}
	return &goals, nil
}

// ListMappings returns up to MaxPerformanceAssignments assignments (lowest SFDC
// IDs first) with their mappings, and truncated=true when more exist.
// Assignments without a mapping are reported with mapped=false, never guessed.
func (svc *PerformanceService) ListMappings(ctx context.Context, userID string) (*model.PerformanceMappingList, error) {
	if !performanceUserPattern.MatchString(userID) {
		return nil, ErrForbidden
	}
	assignments, err := svc.store.ListPerformanceAssignments(ctx, userID)
	if err != nil {
		return nil, err
	}
	sort.Slice(assignments, func(i, j int) bool { return assignments[i].SFDCAccountID < assignments[j].SFDCAccountID })
	truncated := len(assignments) > MaxPerformanceAssignments
	if truncated {
		assignments = assignments[:MaxPerformanceAssignments]
	}
	ids := make([]string, len(assignments))
	for i, assignment := range assignments {
		ids[i] = assignment.SFDCAccountID
	}
	bySFDC, err := svc.store.GetAccountMappings(ctx, userID, ids)
	if err != nil {
		return nil, err
	}
	list := &model.PerformanceMappingList{Items: make([]model.PerformanceMappingItem, 0, len(assignments)), Truncated: truncated}
	for _, assignment := range assignments {
		item := model.PerformanceMappingItem{SFDCAccountID: assignment.SFDCAccountID, Name: assignment.Name, Role: assignment.Role}
		if mapping, found := bySFDC[assignment.SFDCAccountID]; found && mapping.AccountID != "" {
			item.Mapped, item.AccountID, item.Manual, item.UpdatedAt = true, mapping.AccountID, mapping.Manual, mapping.UpdatedAt
		}
		list.Items = append(list.Items, item)
	}
	sort.Slice(list.Items, func(i, j int) bool { return list.Items[i].SFDCAccountID < list.Items[j].SFDCAccountID })
	return list, nil
}

// PutMapping stores a manual mapping. The caller must currently be able to
// read the target account, and the SFDC account must be in the caller's own
// assignments.
func (svc *PerformanceService) PutMapping(ctx context.Context, userID, sfdcAccountID, accountID string) (*model.AccountMapping, error) {
	if !performanceUserPattern.MatchString(userID) {
		return nil, ErrForbidden
	}
	if !performanceIDPattern.MatchString(sfdcAccountID) || !performanceAccountPattern.MatchString(accountID) {
		return nil, ErrInvalidInput
	}
	if svc.accounts == nil {
		return nil, ErrForbidden
	}
	account, err := svc.accounts.GetAccount(ctx, userID, accountID)
	if errors.Is(err, ErrForbidden) || errors.Is(err, ErrNotFound) {
		return nil, ErrForbidden
	}
	if err != nil {
		return nil, err
	}
	if account == nil {
		return nil, ErrForbidden
	}
	assignments, err := svc.store.ListPerformanceAssignments(ctx, userID)
	if err != nil {
		return nil, err
	}
	assigned := false
	for _, assignment := range assignments {
		if assignment.SFDCAccountID == sfdcAccountID {
			assigned = true
			break
		}
	}
	if !assigned {
		return nil, ErrNotFound
	}
	mapping := model.AccountMapping{SFDCAccountID: sfdcAccountID, AccountID: accountID, Manual: true, UpdatedAt: svc.now().UTC().Format(time.RFC3339)}
	if err := svc.store.PutAccountMapping(ctx, userID, mapping, false); err != nil {
		return nil, err
	}
	return &mapping, nil
}

// ListMissed returns the owner's missed items for a period. The rules are not
// implemented yet, so the list is always empty.
func (svc *PerformanceService) ListMissed(_ context.Context, userID, period string) (*model.PerformanceMissedList, error) {
	if !performanceUserPattern.MatchString(userID) {
		return nil, ErrForbidden
	}
	if period != "" && !performancePeriods[period] {
		return nil, ErrInvalidInput
	}
	return &model.PerformanceMissedList{Items: []model.PerformanceMissedItem{}}, nil
}
