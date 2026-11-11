package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

func calendarTestShareRow(test *testing.T, svc *CalendarService, accountID, userID, state, title string, start time.Time) repository.CalendarShareRow {
	test.Helper()
	end := start.Add(time.Hour)
	meetingKey := calendarMeetingKey(accountID, start, end, title)
	row := repository.CalendarShareRow{
		PK: model.PrefixAccount + accountID, SK: model.CalendarShareSortKey(start, meetingKey, userID), AccountID: accountID,
		MeetingKey: meetingKey, UserID: userID, SourceID: calendarSourceID, EventID: strings.Repeat("e", 64),
		StartsAt: start, EndsAt: end, State: state, ExpiresAt: end.Add(90 * 24 * time.Hour).Unix(),
	}
	if state == "veto" {
		row.VetoEventIDs = []string{calendarSourceID + "#" + row.EventID}
		return row
	}
	plaintext, err := calendarSharePlaintext(model.CalendarEventInput{Title: title, StartsAt: start, EndsAt: end, TimeZone: "Asia/Seoul", Location: "Room " + userID})
	if err != nil {
		test.Fatal(err)
	}
	row.Ciphertext, err = svc.cipher.EncryptShare(context.Background(), accountID, meetingKey, userID, plaintext)
	if err != nil {
		test.Fatal(err)
	}
	return row
}

func calendarAccountQuery() model.CalendarQuery {
	return model.CalendarQuery{From: calendarNow, To: calendarNow.Add(24 * time.Hour)}
}

func TestListAccountCalendarVetoHidesGroup(test *testing.T) {
	svc, store, _, _, membership := calendarShareFixture()
	for _, user := range []string{"u1", "u2", "viewer"} {
		membership.addMember("acc_1", user, user+"@example.com")
	}
	start := calendarNow.Add(time.Hour)
	store.sharePage = &repository.CalendarSharePage{Groups: [][]repository.CalendarShareRow{{
		calendarTestShareRow(test, svc, "acc_1", "u1", "shared", "Hana EKS Review", start),
		calendarTestShareRow(test, svc, "acc_1", "u2", "veto", "Hana EKS Review", start),
	}}}
	page, err := svc.ListAccountCalendar(context.Background(), "viewer", "acc_1", calendarAccountQuery())
	if err != nil || len(page.Events) != 0 {
		test.Fatalf("one veto must hide the meeting: %+v %v", page, err)
	}
	store.sharePage.Groups = append(store.sharePage.Groups, []repository.CalendarShareRow{
		calendarTestShareRow(test, svc, "acc_1", "u1", "shared", "Other meeting", start),
		calendarTestShareRow(test, svc, "acc_1", "u2", "shared", "Other meeting", start),
	})
	page, err = svc.ListAccountCalendar(context.Background(), "viewer", "acc_1", calendarAccountQuery())
	if err != nil || len(page.Events) != 1 {
		test.Fatalf("unvetoed meeting expected once: %+v %v", page, err)
	}
	event := page.Events[0]
	if event.Title != "Other meeting" || event.Location != "Room u1" || !reflect.DeepEqual(event.SharedBy, []string{"u1@example.com", "u2@example.com"}) ||
		event.SharedByMe || event.MeetingKey != store.sharePage.Groups[1][0].MeetingKey || !event.StartsAt.Equal(start) {
		test.Fatalf("deduplicated meeting: %+v", event)
	}
	if store.shareQueries[len(store.shareQueries)-1] != "acc_1" {
		test.Fatal("share read must use the requested account")
	}
}

func TestListAccountCalendarDropsRemovedSharer(test *testing.T) {
	svc, store, _, _, membership := calendarShareFixture()
	membership.addMember("acc_1", "u1", "u1@example.com")
	start := calendarNow.Add(time.Hour)
	store.sharePage = &repository.CalendarSharePage{Groups: [][]repository.CalendarShareRow{
		{calendarTestShareRow(test, svc, "acc_1", "removed", "shared", "Removed only", start)},
		{
			calendarTestShareRow(test, svc, "acc_1", "removed", "shared", "Mixed", start),
			calendarTestShareRow(test, svc, "acc_1", "u1", "shared", "Mixed", start),
		},
	}}
	page, err := svc.ListAccountCalendar(context.Background(), "u1", "acc_1", calendarAccountQuery())
	if err != nil || len(page.Events) != 1 {
		test.Fatalf("removed sharer's rows must be dropped: %+v %v", page, err)
	}
	if event := page.Events[0]; event.Title != "Mixed" || event.Location != "Room u1" || !event.SharedByMe ||
		!reflect.DeepEqual(event.SharedBy, []string{"u1@example.com"}) {
		test.Fatalf("only current members may supply or be named: %+v", event)
	}
	encoded, _ := json.Marshal(page)
	if strings.Contains(string(encoded), "removed") {
		test.Fatalf("removed sharer leaked: %s", encoded)
	}
}

func TestListAccountCalendarRequiresDirectMember(test *testing.T) {
	svc, store, _, accounts, membership := calendarShareFixture()
	membership.addMember("acc_1", "someone", "someone@example.com")
	// accounts (GetAccount) admits ancestors and Billing; it must not grant the shared read.
	if _, err := svc.ListAccountCalendar(context.Background(), "billing-reader", "acc_1", calendarAccountQuery()); !errors.Is(err, ErrForbidden) {
		test.Fatalf("non-direct member must be forbidden: %v", err)
	}
	if len(store.shareQueries) != 0 || len(accounts.calls) != 0 {
		test.Fatal("no share read or GetAccount may happen for a non-member")
	}
	query := calendarAccountQuery()
	query.AccountID = "acc_2"
	if _, err := svc.ListAccountCalendar(context.Background(), "someone", "acc_1", query); !errors.Is(err, ErrInvalidInput) {
		test.Fatalf("mismatched query account: %v", err)
	}
	if _, err := svc.ListAccountCalendar(context.Background(), "someone", "../acc", calendarAccountQuery()); !errors.Is(err, ErrInvalidInput) {
		test.Fatalf("bad account id: %v", err)
	}
}

func TestShareDirectoryDirectOnly(test *testing.T) {
	svc, _, _, accounts, membership := calendarShareFixture()
	membership.memberships = map[string][]model.AccountMember{"owner": {
		{AccountID: "acc_b", UserID: "owner"}, {AccountID: "acc_a", UserID: "owner"},
		{AccountID: "acc_x", UserID: "someone-else"}, {AccountID: "acc_gone", UserID: "owner"},
		{AccountID: "acc_stale", UserID: "owner"}, {AccountID: "acc_merged", UserID: "owner"},
	}}
	// Canonical membership rows: acc_stale is only a stale GSI1 entry.
	membership.members = map[string][]model.AccountMember{
		"acc_a": {{AccountID: "acc_a", UserID: "owner"}}, "acc_b": {{AccountID: "acc_b", UserID: "owner"}},
		"acc_gone": {{AccountID: "acc_gone", UserID: "owner"}}, "acc_merged": {{AccountID: "acc_merged", UserID: "owner"}},
	}
	membership.accounts = map[string]*model.Account{
		"acc_a":      {AccountID: "acc_a", Name: "Hana Bank", Aliases: []string{"HNB"}, Domains: []string{"hanabank.com"}},
		"acc_b":      {AccountID: "acc_b", Name: "Beta"},
		"acc_x":      {AccountID: "acc_x", Name: "Not mine"},
		"acc_stale":  {AccountID: "acc_stale", Name: "Removed from me"},
		"acc_merged": {AccountID: "acc_merged", Name: "Tombstone", MergedInto: "acc_a"},
	}
	directory, err := svc.ShareDirectory(context.Background(), "owner")
	if err != nil {
		test.Fatal(err)
	}
	want := []model.CalendarShareDirectoryAccount{
		{AccountID: "acc_a", Name: "Hana Bank", Aliases: []string{"HNB"}, Domains: []string{"hanabank.com"}},
		{AccountID: "acc_b", Name: "Beta", Aliases: []string{}, Domains: []string{}},
	}
	if !reflect.DeepEqual(directory.Accounts, want) || len(accounts.calls) != 0 {
		test.Fatalf("directory must hold direct memberships only, sorted: %+v", directory.Accounts)
	}
	encoded, _ := json.Marshal(directory)
	if !strings.Contains(string(encoded), `"aliases":[]`) {
		test.Fatalf("empty lists must encode as []: %s", encoded)
	}
	membership.memberships["owner"] = nil
	membership.accounts = map[string]*model.Account{}
	for index := 0; index < 205; index++ {
		id := fmt.Sprintf("acc_%03d", 204-index)
		membership.memberships["owner"] = append(membership.memberships["owner"], model.AccountMember{AccountID: id, UserID: "owner"})
		membership.members[id] = []model.AccountMember{{AccountID: id, UserID: "owner"}}
		membership.accounts[id] = &model.Account{AccountID: id, Name: id}
	}
	directory, err = svc.ShareDirectory(context.Background(), "owner")
	if err != nil || len(directory.Accounts) != 200 || directory.Accounts[0].AccountID != "acc_000" || directory.Accounts[199].AccountID != "acc_199" {
		test.Fatalf("directory must cap at 200 sorted accounts: %d %v", len(directory.Accounts), err)
	}
	if _, err := svc.ShareDirectory(context.Background(), ""); !errors.Is(err, ErrForbidden) {
		test.Fatalf("anonymous directory: %v", err)
	}
}

func TestListAccountCalendarTrimsAtGroupCursor(test *testing.T) {
	svc, store, _, _, membership := calendarShareFixture()
	membership.addMember("acc_1", "u1", "u1@example.com")
	store.sharePage = &repository.CalendarSharePage{NextCursor: nil}
	for index := 0; index < 30; index++ {
		title := fmt.Sprintf("%03d %s", index, strings.Repeat("긴", 160))
		store.sharePage.Groups = append(store.sharePage.Groups, []repository.CalendarShareRow{
			calendarTestShareRow(test, svc, "acc_1", "u1", "shared", title, calendarNow.Add(time.Duration(index)*time.Minute)),
		})
		store.sharePage.GroupCursors = append(store.sharePage.GroupCursors, fmt.Sprintf("group-%d", index))
	}
	page, err := svc.ListAccountCalendar(context.Background(), "u1", "acc_1", calendarAccountQuery())
	if err != nil || len(page.Events) == 0 || len(page.Events) == 30 || page.NextCursor == nil ||
		*page.NextCursor != fmt.Sprintf("group-%d", len(page.Events)-1) || !calendarResultFits(page) {
		test.Fatalf("page must be trimmed at a group cursor: %d %v %v", len(page.Events), page.NextCursor, err)
	}
	store.sharePage.GroupCursors = nil
	if _, err := svc.ListAccountCalendar(context.Background(), "u1", "acc_1", calendarAccountQuery()); !errors.Is(err, repository.ErrCalendarCorruptData) {
		test.Fatalf("trimming without group cursors must fail closed: %v", err)
	}
}

func TestListAccountCalendarUndecryptableRows(test *testing.T) {
	svc, store, _, _, membership := calendarShareFixture()
	membership.addMember("acc_1", "u1", "u1@example.com")
	membership.addMember("acc_1", "u2", "u2@example.com")
	start := calendarNow.Add(time.Hour)
	broken := calendarTestShareRow(test, svc, "acc_1", "u1", "shared", "Mixed", start)
	broken.Ciphertext = calendarTestShareRow(test, svc, "acc_1", "u1", "shared", "Other", start).Ciphertext
	store.sharePage = &repository.CalendarSharePage{Groups: [][]repository.CalendarShareRow{{
		broken, calendarTestShareRow(test, svc, "acc_1", "u2", "shared", "Mixed", start),
	}}}
	page, err := svc.ListAccountCalendar(context.Background(), "u1", "acc_1", calendarAccountQuery())
	if err != nil || len(page.Events) != 1 || page.Events[0].Location != "Room u2" || !page.Events[0].SharedByMe {
		test.Fatalf("the first decryptable row must be used: %+v %v", page, err)
	}
	store.sharePage.Groups[0] = store.sharePage.Groups[0][:1]
	if _, err := svc.ListAccountCalendar(context.Background(), "u1", "acc_1", calendarAccountQuery()); !errors.Is(err, ErrCalendarEncryption) {
		test.Fatalf("no decryptable row at all must not look like an empty calendar: %v", err)
	}
	// One undecryptable meeting next to a readable one fails the page closed too.
	store.sharePage.Groups = append(store.sharePage.Groups, []repository.CalendarShareRow{
		calendarTestShareRow(test, svc, "acc_1", "u2", "shared", "Fine", start.Add(time.Hour))})
	if _, err := svc.ListAccountCalendar(context.Background(), "u1", "acc_1", calendarAccountQuery()); !errors.Is(err, ErrCalendarEncryption) {
		test.Fatalf("a partly undecryptable page must not be returned as complete: %v", err)
	}
}
