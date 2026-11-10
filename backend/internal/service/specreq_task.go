package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

const (
	specReqTaskRetention    = 90 * 24 * time.Hour
	specReqTaskMaxPageLimit = 20
	specReqAmazonSuffix     = "@amazon.com"
)

// SpecReqTaskStore is the persistence surface of the task service.
type SpecReqTaskStore interface {
	CreateSpecReqTask(context.Context, repository.SpecReqTaskRow, int64) error
	GetSpecReqTask(context.Context, string, string) (*repository.SpecReqTaskRow, error)
	ListSpecReqTasks(context.Context, string, string, int32) ([]repository.SpecReqTaskRow, *string, error)
	UpdateSpecReqTask(context.Context, repository.SpecReqTaskRow, int64) error
	// SpecReqTaskRowCursor returns the cursor that resumes a page of the given
	// size right after row; it lets the service trim an oversized page.
	SpecReqTaskRowCursor(userID string, limit int32, row repository.SpecReqTaskRow) (string, error)
}

// SpecReqEventReader reads one canonical calendar occurrence row.
type SpecReqEventReader interface {
	GetCalendarEventRow(ctx context.Context, userID, sourceID, eventID string) (*repository.CalendarEventRow, error)
}

// SpecReqTaskService manages private per-occurrence SpecReq tasks.
type SpecReqTaskService struct {
	store    SpecReqTaskStore
	events   SpecReqEventReader
	cipher   *CalendarCipher
	accounts CalendarAccountReader
	calendar *CalendarService
	now      func() time.Time
}

func NewSpecReqTaskService(store SpecReqTaskStore, events SpecReqEventReader, cipher *CalendarCipher, accounts CalendarAccountReader) *SpecReqTaskService {
	return &SpecReqTaskService{
		store: store, events: events, cipher: cipher, accounts: accounts,
		calendar: NewCalendarService(nil, cipher, accounts), now: time.Now,
	}
}

// loadAccount returns the account when the user can read it.
func (svc *SpecReqTaskService) loadAccount(ctx context.Context, userID, accountID string) (*model.AccountResponse, error) {
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
	return account, nil
}

// checkCounterpart requires an SA or SSA member of the account with a resolved
// user ID and an Amazon address, other than the caller.
func checkSpecReqCounterpart(account *model.AccountResponse, userID, counterpartID string) error {
	if counterpartID == "" || counterpartID == userID {
		return ErrInvalidInput
	}
	for _, member := range account.Members {
		if member.Pending || member.UserID == "" || member.UserID != counterpartID {
			continue
		}
		email := strings.ToLower(strings.TrimSpace(member.Email))
		if (member.Role == model.RoleSA || member.Role == model.RoleSSA) &&
			strings.HasSuffix(email, specReqAmazonSuffix) && len(email) > len(specReqAmazonSuffix) {
			return nil
		}
		return ErrInvalidInput
	}
	return ErrInvalidInput
}

func (svc *SpecReqTaskService) checkAccountAndCounterpart(ctx context.Context, userID, accountID, counterpartID string) error {
	if accountID == "" {
		return nil
	}
	account, err := svc.loadAccount(ctx, userID, accountID)
	if err != nil {
		return err
	}
	if counterpartID == "" {
		return nil
	}
	return checkSpecReqCounterpart(account, userID, counterpartID)
}

func (svc *SpecReqTaskService) Create(ctx context.Context, userID string, req model.CreateSpecReqTaskRequest) (*model.SpecReqTaskView, error) {
	if !calendarIdentifier.MatchString(userID) {
		return nil, ErrForbidden
	}
	if err := validateSpecReqCreate(req); err != nil {
		return nil, err
	}
	eventID := calendarEventID(req.SourceID, req.ExternalID)
	event, err := svc.events.GetCalendarEventRow(ctx, userID, req.SourceID, eventID)
	if err != nil {
		return nil, err
	}
	if event == nil {
		return nil, ErrNotFound
	}
	if err := svc.checkAccountAndCounterpart(ctx, userID, req.AccountID, req.CounterpartUserID); err != nil {
		return nil, err
	}
	existing, err := svc.store.GetSpecReqTask(ctx, userID, eventID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return nil, ErrConflict
	}
	now := svc.now().UTC()
	row := repository.SpecReqTaskRow{
		UserID: userID, EventID: eventID, SourceID: req.SourceID, AccountID: req.AccountID, Path: req.Path,
		State: req.State, CounterpartUserID: req.CounterpartUserID,
		OccurrenceStartsAt: event.StartsAt.UTC(), OccurrenceEndsAt: event.EndsAt.UTC(),
		CreatedAt: now, UpdatedAt: now, StateChangedAt: now, Version: 1,
		ExpiresAt: event.EndsAt.Add(specReqTaskRetention).Unix(),
	}
	if err := svc.store.CreateSpecReqTask(ctx, row, event.Revision); err != nil {
		switch {
		case errors.Is(err, repository.ErrSpecReqEventGone):
			return nil, ErrNotFound
		case errors.Is(err, repository.ErrConditionFailed):
			return nil, ErrConflict
		}
		return nil, err
	}
	return svc.buildView(ctx, userID, row, map[string]bool{})
}

func (svc *SpecReqTaskService) Get(ctx context.Context, userID, eventID string) (*model.SpecReqTaskView, error) {
	if !calendarIdentifier.MatchString(userID) {
		return nil, ErrForbidden
	}
	if !calendarIdentifier.MatchString(eventID) {
		return nil, ErrInvalidInput
	}
	row, err := svc.store.GetSpecReqTask(ctx, userID, eventID)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, ErrNotFound
	}
	return svc.buildView(ctx, userID, *row, map[string]bool{})
}

func (svc *SpecReqTaskService) List(ctx context.Context, userID, cursor string, limit int32) (*model.SpecReqTaskPage, error) {
	if !calendarIdentifier.MatchString(userID) {
		return nil, ErrForbidden
	}
	if limit < 0 || limit > specReqTaskMaxPageLimit || len(cursor) > 4096 {
		return nil, ErrInvalidInput
	}
	if limit == 0 {
		limit = specReqTaskMaxPageLimit
	}
	rows, next, err := svc.store.ListSpecReqTasks(ctx, userID, cursor, limit)
	if errors.Is(err, repository.ErrInvalidSpecReqCursor) {
		return nil, ErrInvalidInput
	}
	if err != nil {
		return nil, err
	}
	if len(rows) > int(limit) {
		return nil, repository.ErrCalendarCorruptData
	}
	access := map[string]bool{}
	page := &model.SpecReqTaskPage{Tasks: make([]model.SpecReqTaskView, 0, len(rows)), NextCursor: next}
	for _, row := range rows {
		view, err := svc.buildView(ctx, userID, row, access)
		if err != nil {
			return nil, err
		}
		page.Tasks = append(page.Tasks, *view)
	}
	for !calendarResultFits(page) {
		retained := len(page.Tasks) - 1
		if retained <= 0 {
			return nil, repository.ErrCalendarCorruptData
		}
		resume, err := svc.store.SpecReqTaskRowCursor(userID, limit, rows[retained-1])
		if err != nil || resume == "" {
			return nil, repository.ErrCalendarCorruptData
		}
		page.Tasks = page.Tasks[:retained]
		page.NextCursor = &resume
	}
	return page, nil
}

func (svc *SpecReqTaskService) Transition(ctx context.Context, userID, eventID string, change model.SpecReqTaskTransition) (*model.SpecReqTaskView, error) {
	if !calendarIdentifier.MatchString(userID) {
		return nil, ErrForbidden
	}
	if !calendarIdentifier.MatchString(eventID) || change.Version < 1 {
		return nil, ErrInvalidInput
	}
	row, err := svc.store.GetSpecReqTask(ctx, userID, eventID)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, ErrNotFound
	}
	if change.Version != row.Version {
		return nil, ErrConflict
	}
	current := taskFromRow(*row)
	if change.Op == model.SpecReqOpLinkSpecReq && change.SpecReqID != "" &&
		current.SpecReqID != "" && current.SpecReqID != change.SpecReqID {
		return nil, ErrConflict
	}
	next, err := applySpecReqTransition(current, change)
	if err != nil {
		return nil, err
	}
	switch change.Op {
	case model.SpecReqOpClassify, model.SpecReqOpPickAccount, model.SpecReqOpPickOwner:
		if err := svc.checkAccountAndCounterpart(ctx, userID, next.AccountID, next.CounterpartUserID); err != nil {
			return nil, err
		}
	}
	updated := *row
	updated.PreviousState = row.State
	updated.State, updated.AccountID, updated.Path, updated.CounterpartUserID = next.State, next.AccountID, next.Path, next.CounterpartUserID
	updated.SpecReqID, updated.SpecReqStatus, updated.FailureCode = next.SpecReqID, next.SpecReqStatus, next.FailureCode
	if next.SpecReqTitle != "" {
		sealed, err := svc.cipher.SealTaskText(ctx, userID, row.SourceID, row.EventID, []byte(next.SpecReqTitle))
		if err != nil {
			return nil, err
		}
		updated.SpecReqTitleSealed = sealed
	}
	if err := svc.store.UpdateSpecReqTask(ctx, updated, change.Version); err != nil {
		if errors.Is(err, repository.ErrConditionFailed) {
			return nil, ErrConflict
		}
		return nil, err
	}
	saved, err := svc.store.GetSpecReqTask(ctx, userID, eventID)
	if err != nil {
		return nil, err
	}
	if saved == nil {
		return nil, ErrNotFound
	}
	return svc.buildView(ctx, userID, *saved, map[string]bool{})
}

// taskFromRow converts a stored row to the API task without the sealed title.
func taskFromRow(row repository.SpecReqTaskRow) model.SpecReqTask {
	return model.SpecReqTask{
		EventID: row.EventID, SourceID: row.SourceID, AccountID: row.AccountID, Path: row.Path, State: row.State,
		CounterpartUserID: row.CounterpartUserID, SpecReqID: row.SpecReqID, SpecReqStatus: row.SpecReqStatus,
		FailureCode: row.FailureCode, OccurrenceStartsAt: row.OccurrenceStartsAt, OccurrenceEndsAt: row.OccurrenceEndsAt,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, StateChangedAt: row.StateChangedAt, Version: row.Version,
	}
}

// buildView joins the unsealed title, the calendar summary and the account
// visibility onto a task. access caches per-account read access.
func (svc *SpecReqTaskService) buildView(ctx context.Context, userID string, row repository.SpecReqTaskRow, access map[string]bool) (*model.SpecReqTaskView, error) {
	view := &model.SpecReqTaskView{SpecReqTask: taskFromRow(row)}
	if row.SpecReqTitleSealed != "" {
		title, err := svc.cipher.OpenTaskText(ctx, userID, row.SourceID, row.EventID, row.SpecReqTitleSealed)
		if err != nil {
			return nil, err
		}
		view.SpecReqTitle = string(title)
	}
	if row.AccountID != "" {
		allowed, checked := access[row.AccountID]
		if !checked {
			_, err := svc.loadAccount(ctx, userID, row.AccountID)
			if err != nil && !errors.Is(err, ErrForbidden) {
				return nil, err
			}
			allowed = err == nil
			access[row.AccountID] = allowed
		}
		if !allowed {
			view.AccountID = ""
		}
	}
	eventRow, err := svc.events.GetCalendarEventRow(ctx, userID, row.SourceID, row.EventID)
	if err != nil && !errors.Is(err, repository.ErrCalendarCorruptData) {
		return nil, err
	}
	if err != nil || eventRow == nil {
		return view, nil
	}
	event, err := svc.calendar.decryptCalendarEvent(ctx, userID, *eventRow)
	if errors.Is(err, repository.ErrCalendarCorruptData) || errors.Is(err, ErrCalendarEncryption) {
		// A corrupt or undecryptable payload cannot be told apart from a KMS outage here;
		// the task stays usable without its event summary.
		return view, nil
	}
	if err != nil {
		return nil, err
	}
	view.Event = &model.SpecReqTaskEvent{
		Title: event.Title, StartsAt: event.StartsAt, EndsAt: event.EndsAt, TimeZone: event.TimeZone,
		IsAllDay: event.IsAllDay, StartDate: event.StartDate, EndDate: event.EndDate,
	}
	return view, nil
}
