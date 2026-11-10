package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

const (
	srUser        = "user-1"
	srCounterpart = "user-2"
	srAccount     = "account-1"
	srExternalID  = "series/occurrence-1"
	srSpecReqA    = "11111111-1111-4111-8111-111111111111"
	srSpecReqB    = "22222222-2222-4222-8222-222222222222"
)

type specReqStoreFake struct {
	rows      map[string]repository.SpecReqTaskRow
	created   []repository.SpecReqTaskRow
	updates   []repository.SpecReqTaskRow
	revisions []int64
	updateErr error
	createErr error
}

func (fake *specReqStoreFake) CreateSpecReqTask(_ context.Context, row repository.SpecReqTaskRow, revision int64) error {
	if fake.createErr != nil {
		return fake.createErr
	}
	fake.created = append(fake.created, row)
	fake.revisions = append(fake.revisions, revision)
	fake.rows[row.EventID] = row
	return nil
}

func (fake *specReqStoreFake) GetSpecReqTask(_ context.Context, _, eventID string) (*repository.SpecReqTaskRow, error) {
	if row, ok := fake.rows[eventID]; ok {
		return &row, nil
	}
	return nil, nil
}

func (fake *specReqStoreFake) ListSpecReqTasks(_ context.Context, _, _ string, limit int32) ([]repository.SpecReqTaskRow, *string, error) {
	rows := []repository.SpecReqTaskRow{}
	for index := 0; index < int(limit); index++ {
		if row, ok := fake.rows[fmt.Sprintf("event-%02d", index)]; ok {
			rows = append(rows, row)
		}
	}
	next := "page-2"
	return rows, &next, nil
}

func (fake *specReqStoreFake) UpdateSpecReqTask(_ context.Context, row repository.SpecReqTaskRow, expected int64) error {
	if fake.updateErr != nil {
		return fake.updateErr
	}
	fake.updates = append(fake.updates, row)
	row.Version = expected + 1
	fake.rows[row.EventID] = row
	return nil
}

func (fake *specReqStoreFake) SpecReqTaskRowCursor(_ string, _ int32, row repository.SpecReqTaskRow) (string, error) {
	return "after-" + row.EventID, nil
}

type specReqEventsFake struct {
	rows map[string]*repository.CalendarEventRow
}

func (fake *specReqEventsFake) GetCalendarEventRow(_ context.Context, _, _, eventID string) (*repository.CalendarEventRow, error) {
	return fake.rows[eventID], nil
}

type specReqAccountsFake struct {
	accounts map[string]*model.AccountResponse
	denied   map[string]error
}

func (fake *specReqAccountsFake) GetAccount(_ context.Context, _, accountID string) (*model.AccountResponse, error) {
	if err := fake.denied[accountID]; err != nil {
		return nil, err
	}
	if account := fake.accounts[accountID]; account != nil {
		return account, nil
	}
	return nil, ErrNotFound
}

type specReqFixture struct {
	svc      *SpecReqTaskService
	store    *specReqStoreFake
	events   *specReqEventsFake
	accounts *specReqAccountsFake
	kms      *calendarKMSFake
	eventID  string
}

func newSpecReqFixture(test *testing.T) *specReqFixture {
	test.Helper()
	kms := &calendarKMSFake{}
	cipher := NewCalendarCipher(kms, "configured-key")
	fixture := &specReqFixture{
		store:  &specReqStoreFake{rows: map[string]repository.SpecReqTaskRow{}},
		events: &specReqEventsFake{rows: map[string]*repository.CalendarEventRow{}},
		accounts: &specReqAccountsFake{denied: map[string]error{}, accounts: map[string]*model.AccountResponse{srAccount: {
			AccountID: srAccount, Members: []model.AccountMemberDTO{
				{UserID: srUser, Email: "me@amazon.com", Role: model.RoleSA},
				{UserID: srCounterpart, Email: "peer@amazon.com", Role: model.RoleSSA},
				{UserID: "user-am", Email: "am@amazon.com", Role: model.RoleAM},
				{UserID: "user-ext", Email: "ext@example.com", Role: model.RoleSA},
				{Email: "pending@amazon.com", Role: model.RoleSA, Pending: true},
			},
		}}},
		kms:     kms,
		eventID: calendarEventID(calendarSourceID, srExternalID),
	}
	fixture.svc = NewSpecReqTaskService(fixture.store, fixture.events, cipher, fixture.accounts)
	fixture.svc.now = func() time.Time { return calendarNow }
	input := model.CalendarEventInput{
		ExternalID: srExternalID, Title: "Synthetic meeting", StartsAt: calendarNow, EndsAt: calendarNow.Add(time.Hour),
		TimeZone: "Asia/Seoul", AccountID: srAccount,
	}
	payload, err := marshalCalendarPayload(input)
	if err != nil {
		test.Fatal(err)
	}
	ciphertext, err := cipher.Encrypt(context.Background(), srUser, calendarSourceID, fixture.eventID, payload)
	if err != nil {
		test.Fatal(err)
	}
	fixture.events.rows[fixture.eventID] = &repository.CalendarEventRow{
		PK: model.PrefixUser + srUser, SK: model.CalendarEventSortKey(calendarSourceID, fixture.eventID), UserID: srUser,
		SourceID: calendarSourceID, EventID: fixture.eventID, StartsAt: input.StartsAt, EndsAt: input.EndsAt,
		AccountID: srAccount, Ciphertext: ciphertext, Revision: 7,
	}
	return fixture
}

func srCreateRequest() model.CreateSpecReqTaskRequest {
	return model.CreateSpecReqTaskRequest{
		SourceID: calendarSourceID, ExternalID: srExternalID, AccountID: srAccount,
		Path: model.SpecReqPathMine, State: model.SpecReqStateIssueNeeded,
	}
}

func (fixture *specReqFixture) seed(mutate func(*repository.SpecReqTaskRow)) repository.SpecReqTaskRow {
	row := repository.SpecReqTaskRow{
		PK: model.PrefixUser + srUser, SK: model.SpecReqTaskSortKey(fixture.eventID), UserID: srUser, EventID: fixture.eventID,
		SourceID: calendarSourceID, AccountID: srAccount, Path: model.SpecReqPathMine, State: model.SpecReqStateIssueNeeded,
		OccurrenceStartsAt: calendarNow, OccurrenceEndsAt: calendarNow.Add(time.Hour), CreatedAt: calendarNow,
		UpdatedAt: calendarNow, StateChangedAt: calendarNow, Version: 3, EntityType: "SPECREQ_TASK",
	}
	if mutate != nil {
		mutate(&row)
	}
	fixture.store.rows[row.EventID] = row
	return row
}

func TestSpecReqCreate_DerivesEventIDAndTTL(test *testing.T) {
	fixture := newSpecReqFixture(test)
	view, err := fixture.svc.Create(context.Background(), srUser, srCreateRequest())
	if err != nil {
		test.Fatal(err)
	}
	if len(fixture.store.created) != 1 {
		test.Fatalf("created rows = %d", len(fixture.store.created))
	}
	row := fixture.store.created[0]
	if row.EventID != calendarEventID(calendarSourceID, srExternalID) || view.EventID != row.EventID {
		test.Fatalf("event ID = %q view %q", row.EventID, view.EventID)
	}
	if want := calendarNow.Add(time.Hour).Add(90 * 24 * time.Hour).Unix(); row.ExpiresAt != want {
		test.Fatalf("ttl = %d, want %d", row.ExpiresAt, want)
	}
	if row.Version != 1 || fixture.store.revisions[0] != 7 || !row.OccurrenceEndsAt.Equal(calendarNow.Add(time.Hour)) {
		test.Fatalf("row = %+v revisions = %v", row, fixture.store.revisions)
	}
	if view.Event == nil || view.Event.Title != "Synthetic meeting" {
		test.Fatalf("view event = %+v", view.Event)
	}
}

func TestSpecReqCreate_MissingEventNotFound(test *testing.T) {
	fixture := newSpecReqFixture(test)
	request := srCreateRequest()
	request.ExternalID = "other"
	if _, err := fixture.svc.Create(context.Background(), srUser, request); !errors.Is(err, ErrNotFound) {
		test.Fatalf("error = %v", err)
	}
	if len(fixture.store.created) != 0 {
		test.Fatal("row written for a missing event")
	}
	fixture.store.createErr = repository.ErrSpecReqEventGone
	if _, err := fixture.svc.Create(context.Background(), srUser, srCreateRequest()); !errors.Is(err, ErrNotFound) {
		test.Fatalf("gone event error = %v", err)
	}
}

func TestSpecReqCreate_DuplicateConflict(test *testing.T) {
	fixture := newSpecReqFixture(test)
	fixture.seed(nil)
	if _, err := fixture.svc.Create(context.Background(), srUser, srCreateRequest()); !errors.Is(err, ErrConflict) {
		test.Fatalf("existing row error = %v", err)
	}
	fixture.store.rows = map[string]repository.SpecReqTaskRow{}
	fixture.store.createErr = errors.Join(repository.ErrConditionFailed, errors.New("raced"))
	if _, err := fixture.svc.Create(context.Background(), srUser, srCreateRequest()); !errors.Is(err, ErrConflict) {
		test.Fatalf("raced create error = %v", err)
	}
}

func TestSpecReqCreate_CounterpartRules(test *testing.T) {
	for _, scenario := range []struct {
		name        string
		counterpart string
		ok          bool
	}{
		{"role AM", "user-am", false},
		{"pending member without userId", "pending@amazon.com", false},
		{"non-amazon email", "user-ext", false},
		{"self", srUser, false},
		{"not a member", "user-9", false},
		{"valid SSA", srCounterpart, true},
	} {
		test.Run(scenario.name, func(test *testing.T) {
			fixture := newSpecReqFixture(test)
			request := srCreateRequest()
			request.Path, request.State, request.CounterpartUserID = model.SpecReqPathTheirs, model.SpecReqStateRequestNeeded, scenario.counterpart
			_, err := fixture.svc.Create(context.Background(), srUser, request)
			if scenario.ok != (err == nil) {
				test.Fatalf("error = %v, want ok=%v", err, scenario.ok)
			}
			if !scenario.ok && (!errors.Is(err, ErrInvalidInput) || len(fixture.store.created) != 0) {
				test.Fatalf("error = %v, created = %d", err, len(fixture.store.created))
			}
		})
	}
}

func TestSpecReqCreate_ForeignAccountForbidden(test *testing.T) {
	for _, denial := range []error{ErrForbidden, ErrNotFound} {
		fixture := newSpecReqFixture(test)
		fixture.accounts.denied[srAccount] = denial
		if _, err := fixture.svc.Create(context.Background(), srUser, srCreateRequest()); !errors.Is(err, ErrForbidden) {
			test.Fatalf("denial %v: error = %v", denial, err)
		}
		if len(fixture.store.created) != 0 {
			test.Fatal("row written for a foreign account")
		}
	}
}

func TestSpecReqTransition_StaleVersionConflict(test *testing.T) {
	fixture := newSpecReqFixture(test)
	fixture.seed(nil)
	change := model.SpecReqTaskTransition{Op: model.SpecReqOpClose, Version: 2}
	if _, err := fixture.svc.Transition(context.Background(), srUser, fixture.eventID, change); !errors.Is(err, ErrConflict) {
		test.Fatalf("stale version error = %v", err)
	}
	if len(fixture.store.updates) != 0 {
		test.Fatal("stale version wrote")
	}
	fixture.store.updateErr = errors.Join(repository.ErrConditionFailed, errors.New("raced"))
	change.Version = 3
	if _, err := fixture.svc.Transition(context.Background(), srUser, fixture.eventID, change); !errors.Is(err, ErrConflict) {
		test.Fatalf("condition failure error = %v", err)
	}
}

func TestSpecReqTransition_PreviousStateComesFromStoredRow(test *testing.T) {
	fixture := newSpecReqFixture(test)
	fixture.seed(nil)
	view, err := fixture.svc.Transition(context.Background(), srUser, fixture.eventID, model.SpecReqTaskTransition{Op: model.SpecReqOpClose, Version: 3})
	if err != nil {
		test.Fatal(err)
	}
	if got := fixture.store.updates[0]; got.PreviousState != model.SpecReqStateIssueNeeded || got.State != model.SpecReqStateClosed {
		test.Fatalf("update = %+v", got)
	}
	if view.State != model.SpecReqStateClosed || view.Version != 4 {
		test.Fatalf("view = %+v", view.SpecReqTask)
	}
}

func TestSpecReqTransition_SealsTitle(test *testing.T) {
	fixture := newSpecReqFixture(test)
	fixture.seed(nil)
	const title = "Private SpecReq title"
	view, err := fixture.svc.Transition(context.Background(), srUser, fixture.eventID, model.SpecReqTaskTransition{
		Op: model.SpecReqOpLinkSpecReq, Version: 3, SpecReqID: srSpecReqA, SpecReqTitle: title,
	})
	if err != nil {
		test.Fatal(err)
	}
	stored := fixture.store.rows[fixture.eventID]
	if stored.SpecReqTitleSealed == "" || strings.Contains(fmt.Sprintf("%+v", stored), title) {
		test.Fatalf("stored row exposes the title: %+v", stored)
	}
	if view.SpecReqTitle != title || view.State != model.SpecReqStateIssued {
		test.Fatalf("view = %+v", view.SpecReqTask)
	}
}

func TestSpecReqTransition_LinkRejectsDifferentSpecReq(test *testing.T) {
	fixture := newSpecReqFixture(test)
	fixture.seed(func(row *repository.SpecReqTaskRow) {
		row.Path, row.State, row.CounterpartUserID, row.SpecReqID = model.SpecReqPathTheirs, model.SpecReqStateRequested, srCounterpart, srSpecReqA
	})
	_, err := fixture.svc.Transition(context.Background(), srUser, fixture.eventID, model.SpecReqTaskTransition{
		Op: model.SpecReqOpLinkSpecReq, Version: 3, SpecReqID: srSpecReqB,
	})
	if !errors.Is(err, ErrConflict) || len(fixture.store.updates) != 0 {
		test.Fatalf("error = %v, updates = %d", err, len(fixture.store.updates))
	}
	if _, err := fixture.svc.Transition(context.Background(), srUser, fixture.eventID, model.SpecReqTaskTransition{
		Op: model.SpecReqOpLinkSpecReq, Version: 3, SpecReqID: srSpecReqA,
	}); err != nil {
		test.Fatalf("same SpecReq must be idempotent: %v", err)
	}
}

func TestSpecReqTransition_RechecksCounterpartOnPick(test *testing.T) {
	fixture := newSpecReqFixture(test)
	fixture.seed(func(row *repository.SpecReqTaskRow) { row.State, row.Path = model.SpecReqStateNeedsOwner, "" })
	change := model.SpecReqTaskTransition{Op: model.SpecReqOpPickOwner, Version: 3, CounterpartUserID: "user-am"}
	if _, err := fixture.svc.Transition(context.Background(), srUser, fixture.eventID, change); !errors.Is(err, ErrInvalidInput) {
		test.Fatalf("AM counterpart error = %v", err)
	}
	change.CounterpartUserID = srCounterpart
	if _, err := fixture.svc.Transition(context.Background(), srUser, fixture.eventID, change); err != nil {
		test.Fatal(err)
	}
	if len(fixture.store.updates) != 1 {
		test.Fatalf("updates = %d", len(fixture.store.updates))
	}
}

func TestSpecReqList_BlanksRevokedAccountAndFits(test *testing.T) {
	fixture := newSpecReqFixture(test)
	for index := 0; index < 20; index++ {
		eventID := fmt.Sprintf("event-%02d", index)
		sealed, err := fixture.svc.cipher.SealTaskText(context.Background(), srUser, calendarSourceID, eventID, []byte(strings.Repeat("t", 500)))
		if err != nil {
			test.Fatal(err)
		}
		accountID := srAccount
		if index == 0 {
			accountID = "account-revoked"
		}
		fixture.seed(func(row *repository.SpecReqTaskRow) {
			row.EventID, row.SK, row.AccountID, row.SpecReqTitleSealed = eventID, model.SpecReqTaskSortKey(eventID), accountID, sealed
		})
	}
	fixture.accounts.denied["account-revoked"] = ErrForbidden
	page, err := fixture.svc.List(context.Background(), srUser, "", 20)
	if err != nil {
		test.Fatal(err)
	}
	if len(page.Tasks) == 0 || len(page.Tasks) >= 20 {
		test.Fatalf("tasks = %d, want a trimmed page", len(page.Tasks))
	}
	if !calendarResultFits(page) {
		test.Fatal("page exceeds the response budget")
	}
	if want := "after-" + page.Tasks[len(page.Tasks)-1].EventID; page.NextCursor == nil || *page.NextCursor != want {
		test.Fatalf("next cursor = %v, want %q", page.NextCursor, want)
	}
	if page.Tasks[0].AccountID != "" || page.Tasks[1].AccountID != srAccount {
		test.Fatalf("account IDs = %q, %q", page.Tasks[0].AccountID, page.Tasks[1].AccountID)
	}
}
