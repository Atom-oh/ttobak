package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

const calendarSourceID = "b7aa27f9-9878-4f52-9b54-a3b73a941d31"
const calendarBatchID = "90ec2d25-a32e-43a5-96a9-e203859b7d19"

var calendarNow = time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)

type calendarStoreFake struct {
	saved        *repository.CalendarBatchReceipt
	batches      []repository.CalendarWriteBatch
	receiptReads int
	rows         *repository.CalendarRowPage
	sources      *model.CalendarSourcePage
	users        []string
	query        model.CalendarQuery
	sourceLimit  int32
	applyError   error
}

func (fake *calendarStoreFake) GetCalendarBatchReceipt(_ context.Context, userID, sourceID, batchID string) (*repository.CalendarBatchReceipt, error) {
	fake.receiptReads++
	if fake.saved != nil && fake.saved.UserID == userID && fake.saved.SourceID == sourceID && fake.saved.Receipt.BatchID == batchID {
		return fake.saved, nil
	}
	return nil, nil
}

func (fake *calendarStoreFake) ApplyCalendarBatch(_ context.Context, userID, sourceID string, batch repository.CalendarWriteBatch) (*model.CalendarSyncReceipt, error) {
	if fake.applyError != nil {
		return nil, fake.applyError
	}
	fake.batches = append(fake.batches, batch)
	fake.saved = &repository.CalendarBatchReceipt{
		UserID: userID, SourceID: sourceID, PayloadDigest: batch.PayloadDigest, Receipt: batch.Receipt, ExpiresAt: batch.ExpiresAt,
	}
	return &batch.Receipt, nil
}

func (fake *calendarStoreFake) ListCalendarEventRows(_ context.Context, userID string, query model.CalendarQuery) (*repository.CalendarRowPage, error) {
	fake.users = append(fake.users, userID)
	fake.query = query
	if fake.rows != nil {
		return fake.rows, nil
	}
	return &repository.CalendarRowPage{Rows: []repository.CalendarEventRow{}}, nil
}

func (fake *calendarStoreFake) ListCalendarSources(_ context.Context, userID, cursor string, limit int32) (*model.CalendarSourcePage, error) {
	fake.users = append(fake.users, userID)
	fake.sourceLimit = limit
	if fake.sources != nil {
		return fake.sources, nil
	}
	return &model.CalendarSourcePage{Sources: []model.CalendarSourceStatus{}}, nil
}

type calendarAccountsFake struct {
	denied map[string]error
	calls  []string
}

func (fake *calendarAccountsFake) GetAccount(_ context.Context, userID, accountID string) (*model.AccountResponse, error) {
	fake.calls = append(fake.calls, userID+"/"+accountID)
	if err := fake.denied[accountID]; err != nil {
		return nil, err
	}
	return &model.AccountResponse{}, nil
}

func calendarFixture() (*CalendarService, *calendarStoreFake, *calendarKMSFake, *calendarAccountsFake) {
	store := &calendarStoreFake{}
	sdk := &calendarKMSFake{}
	accounts := &calendarAccountsFake{denied: map[string]error{}}
	svc := NewCalendarService(store, NewCalendarCipher(sdk, "configured-key"), accounts)
	svc.now = func() time.Time { return calendarNow }
	return svc, store, sdk, accounts
}

func calendarRequest() model.CalendarSyncRequest {
	return model.CalendarSyncRequest{
		BatchID: calendarBatchID, WindowStart: calendarNow.Add(-7 * 24 * time.Hour), WindowEnd: calendarNow.Add(30 * 24 * time.Hour),
		Mutations: []model.CalendarMutation{{Op: "upsert", Event: &model.CalendarEventInput{
			ExternalID: "series/occurrence-1", Title: "Synthetic private title", StartsAt: calendarNow, EndsAt: calendarNow.Add(time.Hour),
			TimeZone: "Asia/Seoul", Location: "Synthetic private location", AccountID: "account-1",
		}}},
	}
}

func TestCalendarSyncValidatesEntireBatchBeforeAnyIO(test *testing.T) {
	cases := []struct {
		name   string
		change func(*model.CalendarSyncRequest)
	}{
		{"21 events", func(request *model.CalendarSyncRequest) {
			for index := 1; index < 21; index++ {
				event := *request.Mutations[0].Event
				event.ExternalID = fmt.Sprint(index)
				request.Mutations = append(request.Mutations, model.CalendarMutation{Op: "upsert", Event: &event})
			}
		}},
		{"duplicate occurrence", func(request *model.CalendarSyncRequest) {
			request.Mutations = append(request.Mutations, request.Mutations[0])
		}},
		{"upsert remove duplicate", func(request *model.CalendarSyncRequest) {
			request.Mutations = append(request.Mutations, model.CalendarMutation{Op: "remove", ExternalID: request.Mutations[0].Event.ExternalID})
		}},
		{"title bytes", func(request *model.CalendarSyncRequest) {
			request.Mutations[0].Event.Title = strings.Repeat("한", 171)
		}},
		{"external bytes", func(request *model.CalendarSyncRequest) {
			request.Mutations[0].Event.ExternalID = strings.Repeat("x", 513)
		}},
		{"location bytes", func(request *model.CalendarSyncRequest) {
			request.Mutations[0].Event.Location = strings.Repeat("x", 257)
		}},
		{"invalid UTF8", func(request *model.CalendarSyncRequest) { request.Mutations[0].Event.Title = string([]byte{0xff}) }},
		{"blank title", func(request *model.CalendarSyncRequest) { request.Mutations[0].Event.Title = " \n" }},
		{"empty external ID", func(request *model.CalendarSyncRequest) { request.Mutations[0].Event.ExternalID = "" }},
		{"reversed time", func(request *model.CalendarSyncRequest) {
			request.Mutations[0].Event.EndsAt = calendarNow.Add(-time.Hour)
		}},
		{"equal time", func(request *model.CalendarSyncRequest) { request.Mutations[0].Event.EndsAt = calendarNow }},
		{"event too long", func(request *model.CalendarSyncRequest) {
			request.Mutations[0].Event.EndsAt = calendarNow.Add(93*24*time.Hour + time.Nanosecond)
		}},
		{"window too long", func(request *model.CalendarSyncRequest) {
			request.WindowEnd = request.WindowStart.Add(93*24*time.Hour + time.Nanosecond)
		}},
		{"empty time", func(request *model.CalendarSyncRequest) { request.WindowStart = time.Time{} }},
		{"unknown zone", func(request *model.CalendarSyncRequest) { request.Mutations[0].Event.TimeZone = "Korea Standard Time" }},
		{"host local zone", func(request *model.CalendarSyncRequest) { request.Mutations[0].Event.TimeZone = "Local" }},
		{"missing zone", func(request *model.CalendarSyncRequest) { request.Mutations[0].Event.TimeZone = "" }},
		{"negative revision", func(request *model.CalendarSyncRequest) { request.ExpectedRevision = -1 }},
		{"overflow revision", func(request *model.CalendarSyncRequest) { request.ExpectedRevision = math.MaxInt64 }},
		{"invalid batch ID", func(request *model.CalendarSyncRequest) { request.BatchID = "batch" }},
		{"unsupported op", func(request *model.CalendarSyncRequest) { request.Mutations[0].Op = "cancel" }},
		{"remove with event", func(request *model.CalendarSyncRequest) {
			request.Mutations[0].Op = "remove"
			request.Mutations[0].ExternalID = "occurrence"
		}},
		{"upsert with remove ID", func(request *model.CalendarSyncRequest) { request.Mutations[0].ExternalID = "occurrence" }},
		{"timed with date", func(request *model.CalendarSyncRequest) { request.Mutations[0].Event.StartDate = "2026-11-07" }},
		{"all day without dates", func(request *model.CalendarSyncRequest) { request.Mutations[0].Event.IsAllDay = true }},
		{"account path", func(request *model.CalendarSyncRequest) { request.Mutations[0].Event.AccountID = "../account" }},
		{"encoded payload exceeds KMS", func(request *model.CalendarSyncRequest) {
			request.Mutations[0].Event.ExternalID = strings.Repeat("\x01", 512)
			request.Mutations[0].Event.Title = strings.Repeat("\x02", 512)
		}},
	}
	for _, scenario := range cases {
		test.Run(scenario.name, func(test *testing.T) {
			svc, store, sdk, accounts := calendarFixture()
			request := calendarRequest()
			scenario.change(&request)
			if _, err := svc.Sync(context.Background(), "owner", calendarSourceID, request); !errors.Is(err, ErrInvalidInput) {
				test.Fatalf("invalid batch accepted: %v", err)
			}
			if store.receiptReads != 0 || len(store.batches) != 0 || sdk.encryptCalls != 0 || len(accounts.calls) != 0 {
				test.Fatal("pure validation must reject the whole batch before any I/O")
			}
		})
	}
}

func TestCalendarSyncNormalizesRetryAndKeepsCiphertextPrivate(test *testing.T) {
	svc, store, sdk, accounts := calendarFixture()
	request := calendarRequest()
	second := *request.Mutations[0].Event
	second.ExternalID = "series/occurrence-2"
	request.Mutations = append(request.Mutations, model.CalendarMutation{Op: "upsert", Event: &second})
	first, err := svc.Sync(context.Background(), "owner", strings.ToUpper(calendarSourceID), request)
	if err != nil || first.Revision != 1 || first.Upserted != 2 || first.Completed {
		test.Fatalf("first sync: %+v, %v", first, err)
	}
	batch := store.batches[0]
	if len(batch.PayloadDigest) != 64 || batch.ExpiresAt != calendarNow.Add(7*24*time.Hour).Unix() {
		test.Fatal("digest or receipt expiry missing")
	}
	for _, mutation := range batch.Mutations {
		row := mutation.Event
		encoded, _ := json.Marshal(row)
		if strings.Contains(string(encoded), "Synthetic") || strings.Contains(string(encoded), "series/") ||
			row.UserID != "owner" || row.SourceID != calendarSourceID || row.Revision != 1 ||
			row.ExpiresAt != row.EndsAt.Add(90*24*time.Hour).Unix() {
			test.Fatal("stored event leaked text or lost identity/expiry")
		}
	}
	if batch.Mutations[0].EventID == batch.Mutations[1].EventID {
		test.Fatal("recurring occurrences collided")
	}
	request.Mutations[0], request.Mutations[1] = request.Mutations[1], request.Mutations[0]
	request.WindowStart = request.WindowStart.In(time.FixedZone("offset", 3600))
	request.Mutations[0].Event.StartsAt = request.Mutations[0].Event.StartsAt.In(time.FixedZone("offset", 3600))
	sdk.failure = errors.New("KMS unavailable during retry")
	accounts.denied["account-1"] = ErrForbidden
	accountChecks := len(accounts.calls)
	retry, err := svc.Sync(context.Background(), "owner", calendarSourceID, request)
	if err != nil || !reflect.DeepEqual(first, retry) || sdk.encryptCalls != 2 || len(store.batches) != 1 || len(accounts.calls) != accountChecks {
		test.Fatalf("logical retry must reuse acknowledgement without encryption: %v", err)
	}
	request.Mutations[0].Event.Title = "Changed"
	if _, err := svc.Sync(context.Background(), "owner", calendarSourceID, request); !errors.Is(err, repository.ErrConditionFailed) {
		test.Fatalf("changed batch did not conflict: %v", err)
	}
}

func TestCalendarSyncAuthorizationAndFailClosed(test *testing.T) {
	for _, scenario := range []string{"anonymous", "invalid user", "invalid source", "inaccessible account", "missing account", "account transient", "missing key", "KMS failure", "expired receipt", "foreign receipt"} {
		test.Run(scenario, func(test *testing.T) {
			svc, store, sdk, accounts := calendarFixture()
			userID, sourceID := "owner", calendarSourceID
			expected := ErrForbidden
			switch scenario {
			case "anonymous":
				userID = ""
			case "invalid user":
				userID = "../owner"
			case "invalid source":
				sourceID = "../source"
				expected = ErrInvalidInput
			case "inaccessible account":
				accounts.denied["account-1"] = ErrForbidden
			case "missing account":
				accounts.denied["account-1"] = ErrNotFound
			case "account transient":
				expected = errors.New("account lookup unavailable")
				accounts.denied["account-1"] = expected
			case "missing key":
				svc.cipher = NewCalendarCipher(sdk, "")
				expected = ErrCalendarEncryption
			case "KMS failure":
				sdk.failure = errors.New("private title in upstream error")
				expected = ErrCalendarEncryption
			case "expired receipt", "foreign receipt":
				if _, err := svc.Sync(context.Background(), userID, sourceID, calendarRequest()); err != nil {
					test.Fatal(err)
				}
				if scenario == "expired receipt" {
					store.saved.ExpiresAt = calendarNow.Unix()
				} else {
					userID = "another-member"
				}
				store.batches = nil
				store.applyError = repository.ErrConditionFailed
				expected = repository.ErrConditionFailed
			}
			if _, err := svc.Sync(context.Background(), userID, sourceID, calendarRequest()); !errors.Is(err, expected) {
				test.Fatalf("authorization/failure error = %v; want %v", err, expected)
			}
			if len(store.batches) != 0 {
				test.Fatal("rejected batch wrote state")
			}
		})
	}
}

func TestCalendarSyncBoundariesAllDayAndIdentity(test *testing.T) {
	for _, scenario := range []string{"20 events", "maximum fields", "93 days", "spring DST", "fall DST", "exclusive end mismatch", "empty completion", "explicit cancellation"} {
		test.Run(scenario, func(test *testing.T) {
			svc, store, sdk, _ := calendarFixture()
			request := calendarRequest()
			event := request.Mutations[0].Event
			switch scenario {
			case "20 events":
				for index := 1; index < 20; index++ {
					copyEvent := *event
					copyEvent.ExternalID = fmt.Sprint(index)
					request.Mutations = append(request.Mutations, model.CalendarMutation{Op: "upsert", Event: &copyEvent})
				}
			case "maximum fields":
				event.ExternalID, event.Title, event.Location = strings.Repeat("i", 512), strings.Repeat("한", 170)+"ab", strings.Repeat("x", 256)
			case "93 days":
				event.EndsAt = event.StartsAt.Add(93 * 24 * time.Hour)
				request.WindowEnd = request.WindowStart.Add(93 * 24 * time.Hour)
			case "spring DST", "fall DST", "exclusive end mismatch":
				event.IsAllDay, event.TimeZone = true, "America/New_York"
				event.StartDate, event.EndDate = "2026-03-08", "2026-03-09"
				event.StartsAt = time.Date(2026, 3, 8, 5, 0, 0, 0, time.UTC)
				event.EndsAt = time.Date(2026, 3, 9, 4, 0, 0, 0, time.UTC)
				if scenario == "fall DST" {
					event.StartDate, event.EndDate = "2026-11-01", "2026-11-02"
					event.StartsAt = time.Date(2026, 11, 1, 4, 0, 0, 0, time.UTC)
					event.EndsAt = time.Date(2026, 11, 2, 5, 0, 0, 0, time.UTC)
				}
				if scenario == "exclusive end mismatch" {
					event.EndsAt = event.EndsAt.Add(time.Hour)
				}
			case "empty completion":
				request.Complete, request.Mutations = true, nil
			case "explicit cancellation":
				request.Mutations = []model.CalendarMutation{{Op: "remove", ExternalID: event.ExternalID}}
			}
			receipt, err := svc.Sync(context.Background(), "owner", calendarSourceID, request)
			if scenario == "exclusive end mismatch" {
				if !errors.Is(err, ErrInvalidInput) || len(store.batches) != 0 {
					test.Fatal("all-day end must match exclusive local midnight")
				}
				return
			}
			if err != nil || receipt == nil {
				test.Fatalf("valid boundary rejected: %v", err)
			}
			for _, plaintext := range sdk.plaintexts {
				if len(plaintext) > 4096 {
					test.Fatal("JSON encoding exceeded the KMS plaintext limit")
				}
			}
			if scenario == "explicit cancellation" && (receipt.Removed != 1 || sdk.encryptCalls != 0) {
				test.Fatal("cancellation must be an explicit removal")
			}
			if scenario == "empty completion" && (!receipt.Completed || len(store.batches[0].Mutations) != 0) {
				test.Fatal("empty completion must preserve explicit progress")
			}
		})
	}
	svc, store, _, _ := calendarFixture()
	request := calendarRequest()
	if _, err := svc.Sync(context.Background(), "owner", calendarSourceID, request); err != nil {
		test.Fatal(err)
	}
	originalID := store.batches[0].Mutations[0].EventID
	request.BatchID = "dc93f9a3-3595-4871-a183-087fa81c5106"
	request.ExpectedRevision = 1
	request.Mutations[0].Event.Title = "Renamed"
	request.Mutations[0].Event.StartsAt = calendarNow.Add(time.Hour)
	request.Mutations[0].Event.EndsAt = calendarNow.Add(2 * time.Hour)
	if _, err := svc.Sync(context.Background(), "owner", calendarSourceID, request); err != nil || store.batches[1].Mutations[0].EventID != originalID {
		test.Fatalf("reschedule changed occurrence identity: %v", err)
	}
}

func calendarStoredEvents(test *testing.T, svc *CalendarService, store *calendarStoreFake, request model.CalendarSyncRequest) {
	test.Helper()
	if _, err := svc.Sync(context.Background(), "owner", calendarSourceID, request); err != nil {
		test.Fatal(err)
	}
	store.rows = &repository.CalendarRowPage{}
	for index, mutation := range store.batches[len(store.batches)-1].Mutations {
		if mutation.Event == nil {
			continue
		}
		row := *mutation.Event
		row.PK, row.SK = "USER#owner", model.CalendarEventSortKey(row.SourceID, row.EventID)
		store.rows.Rows = append(store.rows.Rows, row)
		store.rows.RowCursors = append(store.rows.RowCursors, fmt.Sprintf("opaque-row-%d", index))
	}
}

func TestCalendarEventReadsEnforceOwnerAndCurrentAccountAccess(test *testing.T) {
	for _, scenario := range []string{"owner", "other member", "revoked unfiltered", "missing unfiltered", "revoked filtered", "transient unfiltered", "transient filtered", "ciphertext moved", "missing key", "anonymous", "corrupt plaintext"} {
		test.Run(scenario, func(test *testing.T) {
			svc, store, sdk, accounts := calendarFixture()
			calendarStoredEvents(test, svc, store, calendarRequest())
			query := model.CalendarQuery{From: calendarNow, To: calendarNow.Add(24 * time.Hour)}
			userID := "owner"
			var expected error
			switch scenario {
			case "other member":
				userID, query.AccountID = "another-member", "account-1"
				expected = repository.ErrCalendarCorruptData
			case "revoked unfiltered":
				accounts.denied["account-1"] = ErrForbidden
			case "missing unfiltered":
				accounts.denied["account-1"] = ErrNotFound
			case "revoked filtered":
				accounts.denied["account-1"], query.AccountID = ErrForbidden, "account-1"
				expected = ErrForbidden
			case "transient unfiltered", "transient filtered":
				expected = errors.New("synthetic membership outage")
				accounts.denied["account-1"] = expected
				if scenario == "transient filtered" {
					query.AccountID = "account-1"
				}
			case "ciphertext moved":
				store.rows.Rows[0].EventID = strings.Repeat("b", 64)
				store.rows.Rows[0].SK = model.CalendarEventSortKey(calendarSourceID, store.rows.Rows[0].EventID)
				expected = ErrCalendarEncryption
			case "missing key":
				svc.cipher = NewCalendarCipher(sdk, "")
				expected = ErrCalendarEncryption
			case "anonymous":
				userID = ""
				expected = ErrForbidden
			case "corrupt plaintext":
				row := &store.rows.Rows[0]
				row.Ciphertext, _ = svc.cipher.Encrypt(context.Background(), userID, row.SourceID, row.EventID, []byte(`{"title":"forged","userId":"other"}`))
				expected = repository.ErrCalendarCorruptData
			}
			page, err := svc.ListEvents(context.Background(), userID, query)
			if expected != nil {
				if !errors.Is(err, expected) || page != nil {
					test.Fatalf("private read returned data or wrong error: %v", err)
				}
				return
			}
			if err != nil || len(page.Events) != 1 || page.Events[0].Title != "Synthetic private title" {
				test.Fatalf("owner lost personal event: %+v, %v", page, err)
			}
			if (scenario == "revoked unfiltered" || scenario == "missing unfiltered") && page.Events[0].AccountID != "" {
				test.Fatal("revoked classification remained visible")
			}
			if store.rows.Rows[0].AccountID != "account-1" {
				test.Fatal("read mutated stored personal classification")
			}
		})
	}
}

func TestCalendarReadQueryValidationBeforeIO(test *testing.T) {
	for _, scenario := range []string{"93 days", "93 days plus", "reversed", "limit 51", "negative limit", "account path", "cursor length"} {
		test.Run(scenario, func(test *testing.T) {
			svc, store, _, _ := calendarFixture()
			query := model.CalendarQuery{From: calendarNow, To: calendarNow.Add(93 * 24 * time.Hour)}
			switch scenario {
			case "93 days plus":
				query.To = query.To.Add(time.Nanosecond)
			case "reversed":
				query.To = calendarNow
			case "limit 51":
				query.Limit = 51
			case "negative limit":
				query.Limit = -1
			case "account path":
				query.AccountID = "../other"
			case "cursor length":
				query.Cursor = strings.Repeat("a", 4097)
			}
			page, err := svc.ListEvents(context.Background(), "owner", query)
			if scenario == "93 days" {
				if err != nil || page.Events == nil || len(page.Events) != 0 {
					test.Fatalf("maximum query window rejected: %v", err)
				}
			} else if !errors.Is(err, ErrInvalidInput) || len(store.users) != 0 {
				test.Fatalf("invalid query performed IO: %v", err)
			}
		})
	}
}

func TestCalendarEventPageTrimsOnlyAtRealRowContinuation(test *testing.T) {
	svc, store, _, _ := calendarFixture()
	request := calendarRequest()
	request.Mutations = nil
	for index := 0; index < 20; index++ {
		event := *calendarRequest().Mutations[0].Event
		event.ExternalID = fmt.Sprintf("occurrence-%02d", index)
		event.Title, event.Location = strings.Repeat("<", 512), strings.Repeat(">", 256)
		event.StartsAt, event.EndsAt = calendarNow.Add(time.Duration(index)*time.Hour), calendarNow.Add(time.Duration(index+1)*time.Hour)
		request.Mutations = append(request.Mutations, model.CalendarMutation{Op: "upsert", Event: &event})
	}
	calendarStoredEvents(test, svc, store, request)
	tail := "opaque-end-of-page"
	store.rows.NextCursor = &tail
	query := model.CalendarQuery{From: calendarNow, To: calendarNow.Add(24 * time.Hour), Limit: 50}
	page, err := svc.ListEvents(context.Background(), "owner", query)
	if err != nil {
		test.Fatal(err)
	}
	encoded, _ := json.Marshal(page)
	if len(encoded) > 14000 || len(page.Events) == 0 || len(page.Events) >= 20 || page.NextCursor == nil ||
		*page.NextCursor != store.rows.RowCursors[len(page.Events)-1] || *page.NextCursor == tail ||
		store.query.Limit != 50 {
		test.Fatalf("page dropped events or changed effective limit: count=%d bytes=%d cursor=%v", len(page.Events), len(encoded), page.NextCursor)
	}
	for _, event := range page.Events {
		if event.Title != strings.Repeat("<", 512) || event.Location != strings.Repeat(">", 256) {
			test.Fatal("size bound truncated private content")
		}
	}
	retained := len(page.Events)
	store.rows.Rows, store.rows.RowCursors = store.rows.Rows[retained:], store.rows.RowCursors[retained:]
	query.Cursor = *page.NextCursor
	resumed, err := svc.ListEvents(context.Background(), "owner", query)
	if err != nil || len(resumed.Events) == 0 || resumed.Events[0].StartsAt != calendarNow.Add(time.Duration(retained)*time.Hour) {
		test.Fatal("overflowing event missing on continuation")
	}
}

func TestCalendarSourcePagesAndEmptyEventContinuationsAreBounded(test *testing.T) {
	svc, store, _, _ := calendarFixture()
	next := "opaque-empty-page"
	store.rows = &repository.CalendarRowPage{Rows: []repository.CalendarEventRow{}, NextCursor: &next}
	events, err := svc.ListEvents(context.Background(), "owner", model.CalendarQuery{From: calendarNow, To: calendarNow.Add(time.Hour)})
	if err != nil || events.Events == nil || len(events.Events) != 0 || events.NextCursor == nil || *events.NextCursor != next {
		test.Fatal("filtered empty page lost its continuation")
	}
	completed := calendarNow.Add(-time.Hour)
	windowStart, windowEnd := calendarNow.Add(-7*24*time.Hour), calendarNow.Add(30*24*time.Hour)
	revision := int64(math.MaxInt64)
	store.sources = &model.CalendarSourcePage{Sources: []model.CalendarSourceStatus{}, NextCursor: &next}
	for index := 0; index < 20; index++ {
		store.sources.Sources = append(store.sources.Sources, model.CalendarSourceStatus{
			SourceID: fmt.Sprintf("b7aa27f9-9878-4f52-9b54-%012d", index), Provider: "outlook", Revision: revision,
			LastAttemptAt: calendarNow, LastCompletedAt: &completed, LastCompletedRevision: &revision,
			CompletedWindowStart: &windowStart, CompletedWindowEnd: &windowEnd,
		})
	}
	page, err := svc.ListSources(context.Background(), "owner", "", 50)
	if err != nil {
		test.Fatal(err)
	}
	encoded, _ := json.Marshal(page)
	if len(encoded) > 14000 || store.sourceLimit != 20 || page.NextCursor == nil || *page.NextCursor != next ||
		len(page.Sources) != 20 || page.Sources[0].LastCompletedRevision == nil {
		test.Fatalf("source status size/progress/continuation broken: bytes=%d limit=%d", len(encoded), store.sourceLimit)
	}
	for _, limit := range []int32{-1, 51} {
		if _, err := svc.ListSources(context.Background(), "owner", "", limit); !errors.Is(err, ErrInvalidInput) {
			test.Fatal("invalid source limit accepted")
		}
	}
	if _, err := svc.ListSources(context.Background(), "", "", 20); !errors.Is(err, ErrForbidden) {
		test.Fatal("anonymous source metadata disclosed")
	}
}
