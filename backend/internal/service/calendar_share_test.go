package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

func TestNormalizeCalendarEventSharing(t *testing.T) {
	hexKey := strings.Repeat("ab", 32)
	base := func(mutate func(*model.CalendarEventInput)) model.CalendarEventInput {
		event := model.CalendarEventInput{
			ExternalID: "ext-1", Title: "Synthetic", TimeZone: "UTC",
			StartsAt: time.Date(2026, 11, 9, 1, 0, 0, 0, time.UTC),
			EndsAt:   time.Date(2026, 11, 9, 2, 0, 0, 0, time.UTC),
		}
		mutate(&event)
		return event
	}
	tests := []struct {
		name     string
		mutate   func(*model.CalendarEventInput)
		wantErr  bool
		evidence []string
		cands    []string
	}{
		{"valid share sorted", func(e *model.CalendarEventInput) {
			e.ShareAccountID, e.ShareEvidence = "acc_1", []string{"title", "domain"}
			e.CandidateAccountIDs, e.SeriesKey = []string{"acc_3", "acc_2"}, hexKey
		}, false, []string{"domain", "title"}, []string{"acc_2", "acc_3"}},
		{"empty slices become nil", func(e *model.CalendarEventInput) {
			e.ShareEvidence, e.CandidateAccountIDs = []string{}, []string{}
		}, false, nil, nil},
		{"old payload", func(e *model.CalendarEventInput) {}, false, nil, nil},
		{"private alone", func(e *model.CalendarEventInput) { e.Private = true }, false, nil, nil},
		{"evidence without account", func(e *model.CalendarEventInput) { e.ShareEvidence = []string{"title"} }, true, nil, nil},
		{"unknown evidence", func(e *model.CalendarEventInput) {
			e.ShareAccountID, e.ShareEvidence = "acc_1", []string{"body"}
		}, true, nil, nil},
		{"duplicate evidence", func(e *model.CalendarEventInput) {
			e.ShareAccountID, e.ShareEvidence = "acc_1", []string{"title", "title"}
		}, true, nil, nil},
		{"six candidates", func(e *model.CalendarEventInput) {
			e.CandidateAccountIDs = []string{"a1", "a2", "a3", "a4", "a5", "a6"}
		}, true, nil, nil},
		{"duplicate candidates", func(e *model.CalendarEventInput) { e.CandidateAccountIDs = []string{"a1", "a1"} }, true, nil, nil},
		{"bad candidate id", func(e *model.CalendarEventInput) { e.CandidateAccountIDs = []string{"bad id"} }, true, nil, nil},
		{"bad share id", func(e *model.CalendarEventInput) { e.ShareAccountID = "bad id" }, true, nil, nil},
		{"private with share", func(e *model.CalendarEventInput) { e.Private, e.ShareAccountID = true, "acc_1" }, true, nil, nil},
		{"private with candidates", func(e *model.CalendarEventInput) {
			e.Private, e.CandidateAccountIDs = true, []string{"a1"}
		}, true, nil, nil},
		{"uppercase series key", func(e *model.CalendarEventInput) { e.SeriesKey = "ABC" }, true, nil, nil},
		{"uppercase hex series key", func(e *model.CalendarEventInput) { e.SeriesKey = strings.Repeat("AB", 32) }, true, nil, nil},
		{"share equals candidate", func(e *model.CalendarEventInput) {
			e.ShareAccountID, e.CandidateAccountIDs = "acc_1", []string{"acc_1"}
		}, true, nil, nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := normalizeCalendarEvent(base(test.mutate))
			if test.wantErr {
				if !errors.Is(err, ErrInvalidInput) {
					t.Fatalf("error = %v, want ErrInvalidInput", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got.ShareEvidence, test.evidence) || !reflect.DeepEqual(got.CandidateAccountIDs, test.cands) {
				t.Fatalf("evidence=%#v candidates=%#v", got.ShareEvidence, got.CandidateAccountIDs)
			}
		})
	}
}

var calendarTestSeriesKey = strings.Repeat("5e", 32)

func calendarShareRequest(mutate func(*model.CalendarEventInput)) model.CalendarSyncRequest {
	event := model.CalendarEventInput{
		ExternalID: "series/occurrence-1", Title: "Hana EKS Review", StartsAt: calendarNow, EndsAt: calendarNow.Add(time.Hour),
		TimeZone: "Asia/Seoul", Location: "Synthetic room", SeriesKey: calendarTestSeriesKey,
	}
	if mutate != nil {
		mutate(&event)
	}
	return model.CalendarSyncRequest{
		BatchID: calendarBatchID, WindowStart: calendarNow.Add(-7 * 24 * time.Hour), WindowEnd: calendarNow.Add(30 * 24 * time.Hour),
		Mutations: []model.CalendarMutation{{Op: "upsert", Event: &event}},
	}
}

func calendarNextBatch(request model.CalendarSyncRequest, revision int64) model.CalendarSyncRequest {
	request.BatchID = fmt.Sprintf("90ec2d25-a32e-43a5-96a9-e203859b7d%02d", revision)
	request.ExpectedRevision = revision
	return request
}

func TestCalendarMeetingKeyNormalizes(test *testing.T) {
	start, end := calendarNow, calendarNow.Add(time.Hour)
	key := calendarMeetingKey("acc_1", start, end, "  Hana  EKS Review ")
	if len(key) != 64 || strings.Trim(key, "0123456789abcdef") != "" {
		test.Fatalf("meetingKey must be 64 lowercase hex: %q", key)
	}
	for _, same := range []string{"hana eks review", "HANA\tEKS\n Review", "\uff28\uff41\uff4e\uff41 EKS Review"} {
		if got := calendarMeetingKey("acc_1", start.In(time.FixedZone("KST", 9*3600)), end, same); got != key {
			test.Fatalf("%q must normalize to the same key", same)
		}
	}
	for name, other := range map[string]string{
		"endsAt":  calendarMeetingKey("acc_1", start, end.Add(time.Minute), "hana eks review"),
		"startAt": calendarMeetingKey("acc_1", start.Add(time.Minute), end, "hana eks review"),
		"account": calendarMeetingKey("acc_2", start, end, "hana eks review"),
		"title":   calendarMeetingKey("acc_1", start, end, "hana eks review 2"),
	} {
		if other == key {
			test.Fatalf("different %s must change the key", name)
		}
	}
}

func TestSyncShareRequiresDirectMember(test *testing.T) {
	svc, store, sdk, accounts, membership := calendarShareFixture()
	request := calendarShareRequest(func(event *model.CalendarEventInput) {
		event.ShareAccountID, event.ShareEvidence, event.CandidateAccountIDs = "acc_1", []string{"title"}, []string{"acc_9"}
	})
	// GetAccount (inherited/ancestor/Billing access) admits acc_1, but there is no direct membership.
	if _, err := svc.Sync(context.Background(), "owner", calendarSourceID, request); !errors.Is(err, ErrForbidden) {
		test.Fatalf("inherited-only access must be forbidden: %v", err)
	}
	if len(store.batches) != 0 || len(store.shareStates) != 0 || len(accounts.calls) != 0 || sdk.encryptCalls != 0 {
		test.Fatal("a forbidden share must write nothing")
	}
	membership.addMember("acc_1", "owner", "owner@example.com")
	encryptBefore := sdk.encryptCalls
	if _, err := svc.Sync(context.Background(), "owner", calendarSourceID, request); err != nil {
		test.Fatal(err)
	}
	if sdk.encryptCalls != encryptBefore+2 {
		test.Fatalf("event and share ciphertexts expected, got %d", sdk.encryptCalls-encryptBefore)
	}
	mutation := store.batches[0].Mutations[0]
	share, row := mutation.Share, mutation.Event
	wantKey := calendarMeetingKey("acc_1", calendarNow, calendarNow.Add(time.Hour), "Hana EKS Review")
	if share == nil || share.State != "shared" || share.AccountID != "acc_1" || share.MeetingKey != wantKey || share.UserID != "owner" ||
		share.SourceID != calendarSourceID || share.EventID != mutation.EventID || !share.StartsAt.Equal(row.StartsAt) ||
		!share.EndsAt.Equal(row.EndsAt) || share.ExpiresAt != row.ExpiresAt || share.SharerEmail != "owner@example.com" ||
		share.PK != "ACCOUNT#acc_1" || share.SK != model.CalendarShareSortKey(calendarNow, wantKey, "owner") {
		test.Fatalf("share row: %+v", share)
	}
	if row.ShareAccountID != "acc_1" || row.MeetingKey != wantKey || row.ShareState != "shared" || row.SeriesKey != calendarTestSeriesKey {
		test.Fatalf("event row must mirror the share: %+v", row)
	}
	if mutation.PreviousShareKey != nil {
		test.Fatal("a first share has no previous key")
	}
	if strings.Contains(share.Ciphertext, "Hana") {
		test.Fatal("share ciphertext leaked plaintext")
	}
	plaintext, err := svc.cipher.DecryptShare(context.Background(), "acc_1", wantKey, "owner", share.Ciphertext)
	if err != nil {
		test.Fatal(err)
	}
	var fields map[string]any
	if json.Unmarshal(plaintext, &fields) != nil {
		test.Fatal("share plaintext is not JSON")
	}
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if !reflect.DeepEqual(keys, []string{"endsAt", "isAllDay", "location", "startsAt", "timeZone", "title"}) ||
		strings.Contains(string(plaintext), "acc_9") || strings.Contains(string(plaintext), "series/") {
		test.Fatalf("share plaintext must hold only display fields: %s", plaintext)
	}
	if context := sdk.contexts[len(sdk.contexts)-1]; !reflect.DeepEqual(context, map[string]string{
		"purpose": "ttobak-calendar-share-v1", "accountId": "acc_1", "meetingKey": wantKey, "userId": "owner"}) {
		test.Fatalf("share encryption context: %v", context)
	}
	if _, err := svc.cipher.DecryptShare(context.Background(), "acc_2", wantKey, "owner", share.Ciphertext); err == nil {
		test.Fatal("share ciphertext must be bound to its account")
	}
}

func TestSyncOverrideHiddenBeatsMatch(test *testing.T) {
	svc, store, _, _, membership := calendarShareFixture()
	membership.addMember("acc_1", "owner", "owner@example.com")
	store.overrides = map[string]*repository.CalendarOverrideRow{
		"owner/" + calendarSourceID + "/" + calendarTestSeriesKey: {Mode: "hidden"},
	}
	request := calendarShareRequest(func(event *model.CalendarEventInput) {
		event.ShareAccountID, event.ShareEvidence = "acc_1", []string{"domain"}
	})
	if _, err := svc.Sync(context.Background(), "owner", calendarSourceID, request); err != nil {
		test.Fatal(err)
	}
	mutation := store.batches[0].Mutations[0]
	if mutation.Share == nil || mutation.Share.State != "veto" || mutation.Share.AccountID != "acc_1" || mutation.Share.Ciphertext != "" ||
		mutation.Event.ShareState != "veto" || mutation.Event.ShareAccountID != "acc_1" {
		test.Fatalf("hidden override must write a veto for the matched account: %+v", mutation.Share)
	}
	// An account override beats the automatic match.
	store.overrides["owner/"+calendarSourceID+"/"+calendarTestSeriesKey] = &repository.CalendarOverrideRow{Mode: "account", AccountID: "acc_2"}
	membership.addMember("acc_2", "owner", "owner@example.com")
	if _, err := svc.Sync(context.Background(), "owner", calendarSourceID, calendarNextBatch(request, 1)); err != nil {
		test.Fatal(err)
	}
	mutation = store.batches[1].Mutations[0]
	if mutation.Share == nil || mutation.Share.State != "shared" || mutation.Share.AccountID != "acc_2" {
		test.Fatalf("account override must share to its account: %+v", mutation.Share)
	}
	if mutation.PreviousShareKey == nil || mutation.PreviousShareKey.AccountID != "acc_1" ||
		mutation.PreviousShareKey.SK != model.CalendarShareSortKey(calendarNow, calendarMeetingKey("acc_1", calendarNow, calendarNow.Add(time.Hour), "Hana EKS Review"), "owner") {
		test.Fatalf("previous key must be the granted veto: %+v", mutation.PreviousShareKey)
	}
}

func TestSyncPrivateWritesVetoForPreviouslySharedAccount(test *testing.T) {
	svc, store, _, _, membership := calendarShareFixture()
	membership.addMember("acc_1", "owner", "owner@example.com")
	shared := calendarShareRequest(func(event *model.CalendarEventInput) { event.ShareAccountID = "acc_1" })
	if _, err := svc.Sync(context.Background(), "owner", calendarSourceID, shared); err != nil {
		test.Fatal(err)
	}
	private := calendarNextBatch(calendarShareRequest(func(event *model.CalendarEventInput) {
		event.Private, event.Title = true, "Renamed private"
	}), 1)
	if _, err := svc.Sync(context.Background(), "owner", calendarSourceID, private); err != nil {
		test.Fatal(err)
	}
	mutation := store.batches[1].Mutations[0]
	if mutation.Share == nil || mutation.Share.State != "veto" || mutation.Share.AccountID != "acc_1" ||
		mutation.Share.MeetingKey != calendarMeetingKey("acc_1", calendarNow, calendarNow.Add(time.Hour), "Renamed private") {
		test.Fatalf("private must veto the previously shared account: %+v", mutation.Share)
	}
	oldKey := calendarMeetingKey("acc_1", calendarNow, calendarNow.Add(time.Hour), "Hana EKS Review")
	if mutation.PreviousShareKey == nil || mutation.PreviousShareKey.SK != model.CalendarShareSortKey(calendarNow, oldKey, "owner") {
		test.Fatalf("previous shared key must be passed: %+v", mutation.PreviousShareKey)
	}
	// A private event that was never shared writes nothing to any account.
	svc, store, _, _, _ = calendarShareFixture()
	if _, err := svc.Sync(context.Background(), "owner", calendarSourceID, calendarShareRequest(func(event *model.CalendarEventInput) { event.Private = true })); err != nil {
		test.Fatal(err)
	}
	if mutation := store.batches[0].Mutations[0]; mutation.Share != nil || mutation.PreviousShareKey != nil || mutation.Event.ShareState != "" {
		test.Fatalf("unshared private event must stay unshared: %+v", mutation)
	}
}

func TestSyncRetriesOnceOnShareStateChange(test *testing.T) {
	svc, store, _, _, membership := calendarShareFixture()
	membership.addMember("acc_1", "owner", "owner@example.com")
	request := calendarShareRequest(func(event *model.CalendarEventInput) { event.ShareAccountID = "acc_1" })
	store.applyErrors = []error{repository.ErrCalendarShareStateChanged}
	receipt, err := svc.Sync(context.Background(), "owner", calendarSourceID, request)
	if err != nil || receipt == nil || len(store.batches) != 2 || store.rowReads != 2 {
		test.Fatalf("one recompute and retry expected: %v batches=%d reads=%d", err, len(store.batches), store.rowReads)
	}
	svc, store, _, _, membership = calendarShareFixture()
	membership.addMember("acc_1", "owner", "owner@example.com")
	store.applyErrors = []error{repository.ErrCalendarShareStateChanged, repository.ErrCalendarShareStateChanged}
	if _, err := svc.Sync(context.Background(), "owner", calendarSourceID, request); !errors.Is(err, repository.ErrConditionFailed) || len(store.batches) != 2 {
		test.Fatalf("a second share-state change must be a conflict: %v batches=%d", err, len(store.batches))
	}
}

func calendarStoredShareEvent(test *testing.T, svc *CalendarService, store *calendarStoreFake, mutate func(*model.CalendarEventInput)) string {
	test.Helper()
	if _, err := svc.Sync(context.Background(), "owner", calendarSourceID, calendarShareRequest(mutate)); err != nil {
		test.Fatal(err)
	}
	return store.batches[len(store.batches)-1].Mutations[0].EventID
}

func TestSetEventSharingSeriesScope(test *testing.T) {
	svc, store, _, _, membership := calendarShareFixture()
	membership.addMember("acc_1", "owner", "owner@example.com")
	membership.addMember("acc_2", "owner", "owner@example.com")
	eventID := calendarStoredShareEvent(test, svc, store, func(event *model.CalendarEventInput) {
		event.ShareAccountID, event.ShareEvidence = "acc_1", []string{"title"}
	})
	event, err := svc.SetEventSharing(context.Background(), "owner", calendarSourceID, eventID, model.CalendarSharingRequest{Mode: "account", AccountID: "acc_2"})
	if err != nil {
		test.Fatal(err)
	}
	if !reflect.DeepEqual(store.overrideWrites, []string{"put " + calendarTestSeriesKey + " account acc_2"}) {
		test.Fatalf("override must be stored under the series key: %v", store.overrideWrites)
	}
	call := store.shareStates[0]
	if call.EventID != eventID || call.Share == nil || call.Share.State != "shared" || call.Share.AccountID != "acc_2" || call.Share.Ciphertext == "" ||
		call.Previous == nil || call.Previous.AccountID != "acc_1" {
		test.Fatalf("occurrence must be shared to the chosen account: %+v", call)
	}
	if event.SharingMode != "account" || event.ShareAccountID != "acc_2" || event.EventID != eventID {
		test.Fatalf("owner view after account mode: %+v", event)
	}
	event, err = svc.SetEventSharing(context.Background(), "owner", calendarSourceID, eventID, model.CalendarSharingRequest{Mode: "hidden"})
	if err != nil || event.SharingMode != "hidden" {
		test.Fatalf("hidden: %+v %v", event, err)
	}
	if call := store.shareStates[1]; call.Share == nil || call.Share.State != "veto" || call.Share.AccountID != "acc_1" || call.Previous == nil || call.Previous.AccountID != "acc_2" ||
		call.ExtraVeto == nil || call.ExtraVeto.AccountID != "acc_2" {
		test.Fatalf("hidden must veto the automatic and the visible account: %+v", call)
	}
	event, err = svc.SetEventSharing(context.Background(), "owner", calendarSourceID, eventID, model.CalendarSharingRequest{Mode: "auto"})
	if err != nil || event.SharingMode != "auto" || event.ShareAccountID != "acc_1" || !reflect.DeepEqual(event.ShareEvidence, []string{"title"}) {
		test.Fatalf("auto: %+v %v", event, err)
	}
	if store.overrideWrites[2] != "delete "+calendarTestSeriesKey {
		test.Fatalf("auto must clear the override: %v", store.overrideWrites)
	}
	if call := store.shareStates[2]; call.Share == nil || call.Share.State != "shared" || call.Share.AccountID != "acc_1" {
		test.Fatalf("auto must restore the automatic share: %+v", call)
	}
	// Without a series key the scope is the occurrence; a non-member account is forbidden before any write.
	svc, store, _, _, membership = calendarShareFixture()
	eventID = calendarStoredShareEvent(test, svc, store, func(event *model.CalendarEventInput) { event.SeriesKey = "" })
	if _, err := svc.SetEventSharing(context.Background(), "owner", calendarSourceID, eventID, model.CalendarSharingRequest{Mode: "account", AccountID: "acc_3"}); !errors.Is(err, ErrForbidden) {
		test.Fatalf("non-member account: %v", err)
	}
	if len(store.overrideWrites) != 0 || len(store.shareStates) != 0 {
		test.Fatal("forbidden sharing must not write")
	}
	membership.addMember("acc_3", "owner", "owner@example.com")
	if _, err := svc.SetEventSharing(context.Background(), "owner", calendarSourceID, eventID, model.CalendarSharingRequest{Mode: "account", AccountID: "acc_3"}); err != nil {
		test.Fatal(err)
	}
	if store.overrideWrites[0] != "put "+eventID+" account acc_3" {
		test.Fatalf("occurrence scope expected: %v", store.overrideWrites)
	}
	for _, bad := range []model.CalendarSharingRequest{{Mode: "auto", AccountID: "acc_3"}, {Mode: "hidden", AccountID: "acc_3"}, {Mode: "account"}, {Mode: "public"}} {
		if _, err := svc.SetEventSharing(context.Background(), "owner", calendarSourceID, eventID, bad); !errors.Is(err, ErrInvalidInput) {
			test.Fatalf("%+v must be invalid: %v", bad, err)
		}
	}
	if _, err := svc.SetEventSharing(context.Background(), "owner", calendarSourceID, strings.Repeat("f", 64), model.CalendarSharingRequest{Mode: "hidden"}); !errors.Is(err, ErrNotFound) {
		test.Fatalf("missing event: %v", err)
	}
}

func TestSetEventSharingRetriesOnceOnShareStateChange(test *testing.T) {
	svc, store, _, _, membership := calendarShareFixture()
	membership.addMember("acc_1", "owner", "owner@example.com")
	eventID := calendarStoredShareEvent(test, svc, store, func(event *model.CalendarEventInput) { event.ShareAccountID = "acc_1" })
	store.stateErrors = []error{repository.ErrCalendarShareStateChanged}
	if _, err := svc.SetEventSharing(context.Background(), "owner", calendarSourceID, eventID, model.CalendarSharingRequest{Mode: "hidden"}); err != nil || len(store.shareStates) != 2 {
		test.Fatalf("one retry expected: %v calls=%d", err, len(store.shareStates))
	}
	store.stateErrors = []error{repository.ErrCalendarShareStateChanged, repository.ErrCalendarShareStateChanged}
	if _, err := svc.SetEventSharing(context.Background(), "owner", calendarSourceID, eventID, model.CalendarSharingRequest{Mode: "hidden"}); !errors.Is(err, repository.ErrConditionFailed) {
		test.Fatalf("second change must be a conflict: %v", err)
	}
}

// A concurrent hidden call replaces the override between this call's plan and its retry;
// the retry must follow the stored override, never this request's stale one.
func TestSetEventSharingRetryRereadsOverride(test *testing.T) {
	svc, store, _, _, membership := calendarShareFixture()
	membership.addMember("acc_1", "owner", "owner@example.com")
	eventID := calendarStoredShareEvent(test, svc, store, func(event *model.CalendarEventInput) {})
	store.stateErrors = []error{repository.ErrCalendarShareStateChanged}
	store.onOverrideRead = func() {
		for _, row := range store.overrides {
			row.Mode, row.AccountID = "hidden", ""
		}
	}
	event, err := svc.SetEventSharing(context.Background(), "owner", calendarSourceID, eventID, model.CalendarSharingRequest{Mode: "account", AccountID: "acc_1"})
	if err != nil || len(store.shareStates) != 2 {
		test.Fatalf("one retry expected: %v calls=%d", err, len(store.shareStates))
	}
	if first := store.shareStates[0]; first.Share == nil || first.Share.State != "shared" {
		test.Fatalf("first attempt plans the request's override: %+v", first)
	}
	if retry := store.shareStates[1]; retry.Share != nil && retry.Share.State == "shared" {
		test.Fatalf("retry must not share against the stored hidden override: %+v", retry.Share)
	}
	if event.SharingMode != "hidden" {
		test.Fatalf("owner view must report the stored override: %+v", event)
	}
}

func TestListEventsOwnerViewIncludesSharing(test *testing.T) {
	svc, store, _, _, membership := calendarShareFixture()
	membership.addMember("acc_1", "owner", "owner@example.com")
	request := calendarShareRequest(func(event *model.CalendarEventInput) {
		event.ShareAccountID, event.ShareEvidence, event.CandidateAccountIDs = "acc_1", []string{"location", "title"}, []string{"acc_7"}
	})
	calendarStoredEvents(test, svc, store, request)
	page, err := svc.ListEvents(context.Background(), "owner", model.CalendarQuery{From: calendarNow, To: calendarNow.Add(24 * time.Hour)})
	if err != nil || len(page.Events) != 1 {
		test.Fatalf("%+v %v", page, err)
	}
	event := page.Events[0]
	if event.ShareAccountID != "acc_1" || !reflect.DeepEqual(event.ShareEvidence, []string{"location", "title"}) ||
		!reflect.DeepEqual(event.CandidateAccountIDs, []string{"acc_7"}) || event.Private || event.SharingMode != "auto" {
		test.Fatalf("owner view sharing fields: %+v", event)
	}
	store.overrides = map[string]*repository.CalendarOverrideRow{"owner/" + calendarSourceID + "/" + calendarTestSeriesKey: {Mode: "hidden"}}
	page, err = svc.ListEvents(context.Background(), "owner", model.CalendarQuery{From: calendarNow, To: calendarNow.Add(24 * time.Hour)})
	if err != nil || page.Events[0].SharingMode != "hidden" {
		test.Fatalf("override mode must be shown: %+v %v", page, err)
	}
}

func TestSyncStalePlanRetriesAndVetoSurvives(test *testing.T) {
	svc, store, _, _, membership := calendarShareFixture()
	membership.addMember("acc_1", "owner", "owner@example.com")
	request := calendarShareRequest(func(event *model.CalendarEventInput) { event.ShareAccountID = "acc_1" })
	eventID := calendarStoredShareEvent(test, svc, store, func(event *model.CalendarEventInput) { event.ShareAccountID = "acc_1" })
	// The user hides the occurrence after the run read the row and override, before its batch commits.
	store.onOverrideRead = func() {
		if _, err := svc.SetEventSharing(context.Background(), "owner", calendarSourceID, eventID, model.CalendarSharingRequest{Mode: "hidden"}); err != nil {
			test.Fatal(err)
		}
	}
	if _, err := svc.Sync(context.Background(), "owner", calendarSourceID, calendarNextBatch(request, 1)); err != nil {
		test.Fatal(err)
	}
	if len(store.batches) != 3 {
		test.Fatalf("the stale batch must be re-planned once: %d batches", len(store.batches))
	}
	stale, retried := store.batches[1].Mutations[0], store.batches[2].Mutations[0]
	if stale.Share == nil || stale.Share.State != "shared" || stale.Expected == nil || stale.Expected.ShareVersion != 0 {
		test.Fatalf("first attempt planned from the old read: %+v", stale)
	}
	if retried.Share == nil || retried.Share.State != "veto" || retried.Share.AccountID != "acc_1" || retried.Expected == nil || retried.Expected.ShareVersion != 1 {
		test.Fatalf("retry must re-plan from the hidden state: %+v", retried)
	}
	if row := store.stored[calendarSourceID+"/"+eventID]; row.ShareState != "veto" {
		test.Fatalf("the veto must survive the run: %+v", row)
	}
}

func TestSetEventSharingStalePlanUsesNewMeetingKey(test *testing.T) {
	svc, store, _, _, membership := calendarShareFixture()
	membership.addMember("acc_1", "owner", "owner@example.com")
	eventID := calendarStoredShareEvent(test, svc, store, func(event *model.CalendarEventInput) { event.ShareAccountID = "acc_1" })
	// The run renames the meeting after SetEventSharing read the row.
	store.onRowRead = func() {
		renamed := calendarNextBatch(calendarShareRequest(func(event *model.CalendarEventInput) {
			event.ShareAccountID, event.Title = "acc_1", "Renamed review"
		}), 1)
		if _, err := svc.Sync(context.Background(), "owner", calendarSourceID, renamed); err != nil {
			test.Fatal(err)
		}
	}
	if _, err := svc.SetEventSharing(context.Background(), "owner", calendarSourceID, eventID, model.CalendarSharingRequest{Mode: "hidden"}); err != nil {
		test.Fatal(err)
	}
	if len(store.shareStates) != 2 {
		test.Fatalf("one stale attempt and one retry expected: %d", len(store.shareStates))
	}
	wantKey := calendarMeetingKey("acc_1", calendarNow, calendarNow.Add(time.Hour), "Renamed review")
	if last := store.shareStates[1]; last.Share == nil || last.Share.State != "veto" || last.Share.MeetingKey != wantKey || last.Expected.Revision != 2 {
		test.Fatalf("retry must veto the current meetingKey: %+v", last)
	}
}

func TestHiddenVetoesAutomaticAndVisibleAccount(test *testing.T) {
	svc, store, _, _, membership := calendarShareFixture()
	membership.addMember("acc_1", "owner", "owner@example.com")
	membership.addMember("acc_2", "owner", "owner@example.com")
	request := calendarShareRequest(func(event *model.CalendarEventInput) { event.ShareAccountID = "acc_1" })
	eventID := calendarStoredShareEvent(test, svc, store, func(event *model.CalendarEventInput) { event.ShareAccountID = "acc_1" })
	if _, err := svc.SetEventSharing(context.Background(), "owner", calendarSourceID, eventID, model.CalendarSharingRequest{Mode: "account", AccountID: "acc_2"}); err != nil {
		test.Fatal(err)
	}
	if _, err := svc.SetEventSharing(context.Background(), "owner", calendarSourceID, eventID, model.CalendarSharingRequest{Mode: "hidden"}); err != nil {
		test.Fatal(err)
	}
	keyFor := func(account string) string {
		return calendarMeetingKey(account, calendarNow, calendarNow.Add(time.Hour), "Hana EKS Review")
	}
	hide := store.shareStates[1]
	if hide.Share == nil || hide.Share.State != "veto" || hide.Share.AccountID != "acc_1" || hide.Share.MeetingKey != keyFor("acc_1") ||
		hide.ExtraVeto == nil || hide.ExtraVeto.State != "veto" || hide.ExtraVeto.AccountID != "acc_2" || hide.ExtraVeto.MeetingKey != keyFor("acc_2") {
		test.Fatalf("hidden must veto the automatic and the visible account: %+v", hide)
	}
	// The next run keeps both vetoes.
	if _, err := svc.Sync(context.Background(), "owner", calendarSourceID, calendarNextBatch(request, 1)); err != nil {
		test.Fatal(err)
	}
	mutation := store.batches[len(store.batches)-1].Mutations[0]
	if mutation.Share == nil || mutation.Share.State != "veto" || mutation.Share.AccountID != "acc_1" ||
		mutation.ExtraVeto == nil || mutation.ExtraVeto.AccountID != "acc_2" ||
		mutation.Event.ExtraVetoAccountID != "acc_2" || mutation.Event.ExtraVetoMeetingKey != keyFor("acc_2") {
		test.Fatalf("a later run must keep both vetoes: %+v", mutation)
	}
	// Same account: only one veto.
	if _, err := svc.SetEventSharing(context.Background(), "owner", calendarSourceID, eventID, model.CalendarSharingRequest{Mode: "auto"}); err != nil {
		test.Fatal(err)
	}
	if last := store.shareStates[len(store.shareStates)-1]; last.Share == nil || last.Share.State != "shared" || last.ExtraVeto != nil {
		test.Fatalf("auto must clear the extra veto: %+v", last)
	}
	if _, err := svc.SetEventSharing(context.Background(), "owner", calendarSourceID, eventID, model.CalendarSharingRequest{Mode: "hidden"}); err != nil {
		test.Fatal(err)
	}
	if last := store.shareStates[len(store.shareStates)-1]; last.Share == nil || last.Share.AccountID != "acc_1" || last.ExtraVeto != nil {
		test.Fatalf("one account needs one veto: %+v", last)
	}
}

func TestDecideCalendarSharePrivateBeatsAccountOverride(test *testing.T) {
	account := &repository.CalendarOverrideRow{Mode: "account", AccountID: "acc_2"}
	hidden := &repository.CalendarOverrideRow{Mode: "hidden"}
	cases := []struct {
		name     string
		input    model.CalendarEventInput
		override *repository.CalendarOverrideRow
		recorded []string
		want     calendarShareDecision
	}{
		{"private with account override vetoes recorded share", model.CalendarEventInput{Private: true, ShareAccountID: "acc_1"}, account, []string{"acc_2"},
			calendarShareDecision{accountID: "acc_2", state: "veto"}},
		{"private with account override vetoes recorded other account", model.CalendarEventInput{Private: true}, account, []string{"acc_3"},
			calendarShareDecision{accountID: "acc_3", state: "veto"}},
		{"private with account override and nothing recorded vetoes override account", model.CalendarEventInput{Private: true}, account, nil,
			calendarShareDecision{accountID: "acc_2", state: "veto"}},
		{"private alone vetoes recorded", model.CalendarEventInput{Private: true, ShareAccountID: "acc_1"}, nil, []string{"acc_1"},
			calendarShareDecision{accountID: "acc_1", state: "veto"}},
		{"private alone with nothing recorded writes nothing", model.CalendarEventInput{Private: true, ShareAccountID: "acc_1"}, nil, nil,
			calendarShareDecision{}},
		{"account override without private shares", model.CalendarEventInput{ShareAccountID: "acc_1"}, account, []string{"acc_1"},
			calendarShareDecision{accountID: "acc_2", state: "shared"}},
		{"hidden still beats private", model.CalendarEventInput{Private: true, ShareAccountID: "acc_1"}, hidden, nil,
			calendarShareDecision{accountID: "acc_1", state: "veto", requested: true}},
	}
	for _, tc := range cases {
		if got := decideCalendarShare(tc.input, tc.override, tc.recorded, true); got != tc.want {
			test.Errorf("%s: got %+v want %+v", tc.name, got, tc.want)
		}
	}
}

func TestCalendarOwnerEventUsesStoredShareState(test *testing.T) {
	input := model.CalendarEventInput{Title: "Review", ShareAccountID: "acc_1", ShareEvidence: []string{"domain"}, CandidateAccountIDs: []string{"acc_7"}}
	hidden := &repository.CalendarOverrideRow{Mode: "hidden"}
	account := &repository.CalendarOverrideRow{Mode: "account", AccountID: "acc_2"}
	cases := []struct {
		name         string
		row          repository.CalendarEventRow
		override     *repository.CalendarOverrideRow
		wantAccount  string
		wantEvidence []string
		wantMode     string
	}{
		{"normal shared", repository.CalendarEventRow{ShareState: "shared", ShareAccountID: "acc_1", MeetingKey: "mk"}, nil, "acc_1", []string{"domain"}, "auto"},
		{"hidden after auto match", repository.CalendarEventRow{ShareState: "veto", ShareAccountID: "acc_1", MeetingKey: "mk"}, hidden, "", nil, "hidden"},
		{"share lost to key collision", repository.CalendarEventRow{}, nil, "", nil, "auto"},
		{"manual account has no evidence", repository.CalendarEventRow{ShareState: "shared", ShareAccountID: "acc_2", MeetingKey: "mk"}, account, "acc_2", nil, "account"},
		{"account override not yet applied", repository.CalendarEventRow{}, account, "", nil, "account"},
	}
	for _, tc := range cases {
		event := calendarOwnerEvent(tc.row, input, tc.override)
		if event.ShareAccountID != tc.wantAccount || !reflect.DeepEqual(event.ShareEvidence, tc.wantEvidence) || event.SharingMode != tc.wantMode ||
			!reflect.DeepEqual(event.CandidateAccountIDs, []string{"acc_7"}) {
			test.Errorf("%s: %+v", tc.name, event)
		}
	}
}
