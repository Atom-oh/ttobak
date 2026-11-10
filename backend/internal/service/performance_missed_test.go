package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
)

func missedSeoul(test *testing.T) *time.Location {
	test.Helper()
	loc, err := time.LoadLocation("Asia/Seoul")
	if err != nil {
		test.Fatal(err)
	}
	return loc
}

func missedAt(loc *time.Location, month time.Month, day, hour int) time.Time {
	return time.Date(2026, month, day, hour, 0, 0, 0, loc)
}

var missedConfig = MissedConfig{SilentDays: 60, GapDays: 3}

func missedKinds(items []MissedItem, kind string) []MissedItem {
	var out []MissedItem
	for _, item := range items {
		if item.Kind == kind {
			out = append(out, item)
		}
	}
	return out
}

func TestPeriodForKinds(test *testing.T) {
	loc := missedSeoul(test)
	now := time.Date(2026, time.November, 10, 9, 30, 0, 0, loc) // Tuesday
	cases := []struct {
		kind     string
		from, to time.Time
	}{
		{"week", time.Date(2026, 11, 9, 0, 0, 0, 0, loc), time.Date(2026, 11, 16, 0, 0, 0, 0, loc).Add(-time.Nanosecond)},
		{"quarter", time.Date(2026, 10, 1, 0, 0, 0, 0, loc), time.Date(2027, 1, 1, 0, 0, 0, 0, loc).Add(-time.Nanosecond)},
		{"year", time.Date(2026, 1, 1, 0, 0, 0, 0, loc), now},
	}
	for _, tc := range cases {
		period, err := PeriodFor(tc.kind, now, loc)
		if err != nil || !period.From.Equal(tc.from) || !period.To.Equal(tc.to) {
			test.Fatalf("%s: %+v %v", tc.kind, period, err)
		}
	}
	// A Sunday still belongs to the week that began the previous Monday.
	sunday, _ := PeriodFor("week", time.Date(2026, 11, 15, 23, 0, 0, 0, loc), loc)
	if !sunday.From.Equal(time.Date(2026, 11, 9, 0, 0, 0, 0, loc)) {
		test.Fatalf("sunday week: %+v", sunday)
	}
	// The Seoul date decides the period, not the UTC date.
	utcMonday := time.Date(2026, 11, 8, 16, 0, 0, 0, time.UTC) // Monday 01:00 in Seoul
	monday, _ := PeriodFor("week", utcMonday, loc)
	if !monday.From.Equal(time.Date(2026, 11, 9, 0, 0, 0, 0, loc)) {
		test.Fatalf("seoul monday: %+v", monday)
	}
	for _, bad := range []string{"", "month", "WEEK"} {
		if _, err := PeriodFor(bad, now, loc); !errors.Is(err, ErrInvalidInput) {
			test.Fatalf("%q: %v", bad, err)
		}
	}
}

func TestLoggingGapWhenNoTaskWithinWindow(test *testing.T) {
	loc := missedSeoul(test)
	period := Period{From: missedAt(loc, 3, 1, 0), To: missedAt(loc, 3, 31, 0)}
	inputs := MissedInputs{
		Mappings: map[string]string{"S1": "acct-1"},
		Touchpoints: []Touchpoint{
			{AccountID: "acct-1", Kind: TouchpointCalendar, RefID: "ev-1", At: missedAt(loc, 3, 10, 15)},
			{AccountID: "acct-1", Kind: TouchpointSFDC, RefID: "T1", At: missedAt(loc, 3, 14, 0)},
		},
	}
	gaps := missedKinds(ComputeMissed(period, inputs, missedConfig), MissedKindLoggingGap)
	if len(gaps) != 1 || gaps[0].AccountID != "acct-1" || gaps[0].SFDCAccountID != "S1" ||
		gaps[0].RefKind != TouchpointCalendar || gaps[0].RefID != "ev-1" || gaps[0].DaysSince != 21 ||
		gaps[0].At != missedAt(loc, 3, 10, 15).UTC().Format(time.RFC3339) {
		test.Fatalf("gaps: %+v", gaps)
	}
	// A meeting touchpoint is checked the same way; a task three days before matches.
	inputs.Touchpoints = append(inputs.Touchpoints,
		Touchpoint{AccountID: "acct-1", Kind: TouchpointMeeting, RefID: "m-1", At: missedAt(loc, 3, 20, 10)},
		Touchpoint{AccountID: "acct-1", Kind: TouchpointSFDC, RefID: "T2", At: missedAt(loc, 3, 17, 0)},
	)
	gaps = missedKinds(ComputeMissed(period, inputs, missedConfig), MissedKindLoggingGap)
	if len(gaps) != 1 || gaps[0].RefID != "ev-1" {
		test.Fatalf("meeting within window: %+v", gaps)
	}
}

func TestNoGapWhenTaskWithinWindow(test *testing.T) {
	loc := missedSeoul(test)
	period := Period{From: missedAt(loc, 3, 1, 0), To: missedAt(loc, 3, 31, 0)}
	inputs := MissedInputs{
		Mappings: map[string]string{"S1": "acct-1"},
		Touchpoints: []Touchpoint{
			{AccountID: "acct-1", Kind: TouchpointCalendar, RefID: "ev-1", At: missedAt(loc, 3, 10, 15)},
			{AccountID: "acct-1", Kind: TouchpointSFDC, RefID: "T1", At: missedAt(loc, 3, 12, 0)},
		},
	}
	if gaps := missedKinds(ComputeMissed(period, inputs, missedConfig), MissedKindLoggingGap); len(gaps) != 0 {
		test.Fatalf("gaps: %+v", gaps)
	}
	// A task on another account does not cover the touchpoint.
	inputs.Touchpoints[1].AccountID = "acct-2"
	if gaps := missedKinds(ComputeMissed(period, inputs, missedConfig), MissedKindLoggingGap); len(gaps) != 1 {
		test.Fatalf("other-account task: %+v", gaps)
	}
	// A touchpoint outside the period is not reported.
	inputs.Touchpoints[0].At = missedAt(loc, 2, 20, 10)
	if gaps := missedKinds(ComputeMissed(period, inputs, missedConfig), MissedKindLoggingGap); len(gaps) != 0 {
		test.Fatalf("outside period: %+v", gaps)
	}
	// An account with no SFDC mapping has no logging gap.
	inputs.Touchpoints[0].At = missedAt(loc, 3, 10, 15)
	inputs.Mappings = map[string]string{}
	if gaps := missedKinds(ComputeMissed(period, inputs, missedConfig), MissedKindLoggingGap); len(gaps) != 0 {
		test.Fatalf("unmapped account: %+v", gaps)
	}
}

func TestSilentCustomerAfter60Days(test *testing.T) {
	loc := missedSeoul(test)
	end := missedAt(loc, 6, 30, 18)
	period := Period{From: missedAt(loc, 6, 29, 0), To: end}
	inputs := MissedInputs{
		Assignments: []model.SFDCAssignment{
			{SFDCAccountID: "S1", Name: "Old", Role: "REGISTRY"},
			{SFDCAccountID: "S2", Name: "Recent", Role: "activity"},
			{SFDCAccountID: "S3", Name: "Never", Role: "activity"},
			{SFDCAccountID: "S4", Name: "Unmapped", Role: "REGISTRY"},
		},
		Mappings: map[string]string{"S1": "acct-1", "S2": "acct-2", "S3": "acct-3"},
		Touchpoints: []Touchpoint{
			{AccountID: "acct-1", Kind: TouchpointMeeting, RefID: "m-1", At: end.AddDate(0, 0, -61)},
			{AccountID: "acct-2", Kind: TouchpointSFDC, RefID: "T2", At: end.AddDate(0, 0, -60)},
			// A future touchpoint does not end silence.
			{AccountID: "acct-1", Kind: TouchpointSFDC, RefID: "T9", At: end.AddDate(0, 0, 2)},
		},
	}
	silent := missedKinds(ComputeMissed(period, inputs, missedConfig), MissedKindSilentCustomer)
	if len(silent) != 2 {
		test.Fatalf("silent: %+v", silent)
	}
	if silent[0].SFDCAccountID != "S1" || silent[0].AccountID != "acct-1" || silent[0].DaysSince != 61 ||
		silent[0].RefKind != TouchpointMeeting || silent[0].RefID != "m-1" {
		test.Fatalf("61 days: %+v", silent[0])
	}
	if silent[1].SFDCAccountID != "S3" || silent[1].DaysSince != -1 || silent[1].At != "" || silent[1].RefID != "" {
		test.Fatalf("never touched: %+v", silent[1])
	}
}

func TestUnmappedNeverGuessed(test *testing.T) {
	loc := missedSeoul(test)
	period := Period{From: missedAt(loc, 3, 1, 0), To: missedAt(loc, 3, 31, 0)}
	accounts := []MissedAccount{{ID: "acct-1", Name: "Acme", Domains: []string{"acme.com"}}}
	events := []model.CalendarEvent{
		{EventID: "ev-1", Title: "Weekly sync", OrganizerDomain: "unknown.io", StartsAt: missedAt(loc, 3, 10, 10)},
		{EventID: "ev-2", Title: "Weekly sync", OrganizerDomain: "unknown.io", StartsAt: missedAt(loc, 2, 10, 10)},
	}
	touchpoints, unmapped := CalendarTouchpoints(period, events, accounts)
	if len(touchpoints) != 0 || unmapped != 1 {
		test.Fatalf("touchpoints %+v unmapped %d", touchpoints, unmapped)
	}
	tasks := []model.SFDCTask{
		{TaskID: "T1", ActivityDate: "2026-03-05"},
		{TaskID: "T2", SFDCAccountID: "S9", ActivityDate: "2026-03-06"},
		{TaskID: "T3", SFDCAccountID: "S1", ActivityDate: "2026-03-07"},
		{TaskID: "T4", ActivityDate: "2026-01-07"},
	}
	sfdc, unmappedTasks := SFDCTouchpoints(period, tasks, map[string]string{"S1": "acct-1"})
	if len(sfdc) != 1 || sfdc[0].RefID != "T3" || sfdc[0].AccountID != "acct-1" || unmappedTasks != 2 ||
		!sfdc[0].At.Equal(time.Date(2026, 3, 7, 0, 0, 0, 0, loc)) {
		test.Fatalf("sfdc %+v unmapped %d", sfdc, unmappedTasks)
	}
	items := ComputeMissed(period, MissedInputs{Touchpoints: touchpoints, Mappings: map[string]string{"S1": "acct-1"}}, missedConfig)
	if len(items) != 0 {
		test.Fatalf("items: %+v", items)
	}
}

func TestCalendarTouchpointByOrganizerDomainOrUniqueTitle(test *testing.T) {
	loc := missedSeoul(test)
	period := Period{From: missedAt(loc, 3, 1, 0), To: missedAt(loc, 3, 31, 0)}
	accounts := []MissedAccount{
		{ID: "acct-1", Name: "Acme", Domains: []string{"acme.com"}},
		{ID: "acct-2", Name: "Globex", Aliases: []string{"글로벡스"}},
		{ID: "acct-3", Name: "Initech"},
		{ID: "acct-4", Name: "Initrode", Domains: []string{"shared.com"}},
		{ID: "acct-5", Name: "Hooli", Domains: []string{"shared.com"}},
	}
	at := missedAt(loc, 3, 10, 10)
	cases := []struct {
		event   model.CalendarEvent
		account string
	}{
		{model.CalendarEvent{EventID: "domain", Title: "Sync", OrganizerDomain: "acme.com"}, "acct-1"},
		{model.CalendarEvent{EventID: "subdomain", Title: "Sync", OrganizerDomain: "eu.acme.com"}, "acct-1"},
		{model.CalendarEvent{EventID: "suffix-only", Title: "Sync", OrganizerDomain: "notacme.com"}, ""},
		{model.CalendarEvent{EventID: "alias", Title: "글로벡스 아키텍처 리뷰", OrganizerDomain: "gmail.com"}, "acct-2"},
		{model.CalendarEvent{EventID: "name", Title: "GLOBEX roadmap"}, "acct-2"},
		{model.CalendarEvent{EventID: "two-titles", Title: "Globex + Initech joint review"}, ""},
		{model.CalendarEvent{EventID: "ambiguous-domain", Title: "Sync", OrganizerDomain: "shared.com"}, ""},
		{model.CalendarEvent{EventID: "ambiguous-domain-title", Title: "Hooli sync", OrganizerDomain: "shared.com"}, "acct-5"},
		{model.CalendarEvent{EventID: "domain-beats-title", Title: "Initech intro", OrganizerDomain: "acme.com"}, "acct-1"},
		{model.CalendarEvent{EventID: "linked", Title: "Sync", AccountID: "acct-3"}, "acct-3"},
		{model.CalendarEvent{EventID: "linked-unreadable", Title: "Sync", AccountID: "acct-9"}, ""},
	}
	for _, tc := range cases {
		tc.event.StartsAt = at
		touchpoints, unmapped := CalendarTouchpoints(period, []model.CalendarEvent{tc.event}, accounts)
		if tc.account == "" {
			if len(touchpoints) != 0 || unmapped != 1 {
				test.Fatalf("%s: %+v %d", tc.event.EventID, touchpoints, unmapped)
			}
			continue
		}
		if len(touchpoints) != 1 || unmapped != 0 || touchpoints[0].AccountID != tc.account ||
			touchpoints[0].Kind != TouchpointCalendar || touchpoints[0].RefID != tc.event.EventID || !touchpoints[0].At.Equal(at) {
			test.Fatalf("%s: %+v %d", tc.event.EventID, touchpoints, unmapped)
		}
	}
}

func TestInternalOrganizerNeedsTitleMatch(test *testing.T) {
	loc := missedSeoul(test)
	period := Period{From: missedAt(loc, 3, 1, 0), To: missedAt(loc, 3, 31, 0)}
	// Even an account that lists the internal domain is never matched by it.
	accounts := []MissedAccount{{ID: "acct-1", Name: "Acme", Domains: []string{"amazon.com", "acme.com"}}}
	at := missedAt(loc, 3, 10, 10)
	internal := []model.CalendarEvent{
		{EventID: "ev-1", Title: "Internal prep", OrganizerDomain: "amazon.com", StartsAt: at},
		{EventID: "ev-2", Title: "Team sync", OrganizerDomain: "corp.amazon.com", StartsAt: at},
	}
	if touchpoints, unmapped := CalendarTouchpoints(period, internal, accounts); len(touchpoints) != 0 || unmapped != 2 {
		test.Fatalf("internal without title: %+v %d", touchpoints, unmapped)
	}
	titled := []model.CalendarEvent{{EventID: "ev-3", Title: "Acme 사전 준비", OrganizerDomain: "amazon.com", StartsAt: at}}
	touchpoints, unmapped := CalendarTouchpoints(period, titled, accounts)
	if len(touchpoints) != 1 || unmapped != 0 || touchpoints[0].AccountID != "acct-1" {
		test.Fatalf("internal with title: %+v %d", touchpoints, unmapped)
	}
}

func TestMissedItemsNewestFirstAndBounded(test *testing.T) {
	loc := missedSeoul(test)
	period := Period{From: missedAt(loc, 1, 1, 0), To: missedAt(loc, 12, 31, 0)}
	inputs := MissedInputs{Mappings: map[string]string{"S1": "acct-1"}}
	start := missedAt(loc, 1, 1, 9)
	for index := 0; index < 600; index++ {
		inputs.Touchpoints = append(inputs.Touchpoints, Touchpoint{
			AccountID: "acct-1", Kind: TouchpointCalendar, RefID: "ev", At: start.Add(time.Duration(index) * 12 * time.Hour),
		})
	}
	items := ComputeMissed(period, inputs, missedConfig)
	if len(items) != MissedMaxItems {
		test.Fatalf("items: %d", len(items))
	}
	for index := 1; index < len(items); index++ {
		if items[index-1].At < items[index].At {
			test.Fatalf("order at %d: %s < %s", index, items[index-1].At, items[index].At)
		}
	}
}

// ---- service wiring ----

type missedCalendarFake struct {
	events  []model.CalendarEvent
	queries []model.CalendarQuery
	users   []string
	sources *model.CalendarSourcePage
	endless bool // every page has a next cursor
}

func (fake *missedCalendarFake) ListEvents(_ context.Context, userID string, query model.CalendarQuery) (*model.CalendarEventPage, error) {
	fake.users = append(fake.users, userID)
	fake.queries = append(fake.queries, query)
	if !validCalendarInterval(query.From, query.To) {
		return nil, ErrInvalidInput
	}
	page := &model.CalendarEventPage{Events: []model.CalendarEvent{}}
	if fake.endless {
		cursor := "more"
		page.NextCursor = &cursor
		return page, nil
	}
	matched := 0
	skip := 0
	if query.Cursor != "" {
		skip = 1
	}
	for _, event := range fake.events {
		if event.StartsAt.Before(query.From) || !event.StartsAt.Before(query.To) {
			continue
		}
		matched++
		if matched <= skip {
			continue
		}
		if len(page.Events) == 1 { // one event per page exercises paging
			cursor := "next"
			page.NextCursor = &cursor
			break
		}
		page.Events = append(page.Events, event)
	}
	return page, nil
}

func (fake *missedCalendarFake) ListSources(_ context.Context, userID, _ string, _ int32) (*model.CalendarSourcePage, error) {
	fake.users = append(fake.users, userID)
	if fake.sources == nil {
		return &model.CalendarSourcePage{Sources: []model.CalendarSourceStatus{}}, nil
	}
	return fake.sources, nil
}

// missedMeetingsFake mirrors ListMeetingsForAccounts' stages: tab "all" reads
// owned -> shared -> team, tab "shared" reads shared -> team. Owned items are
// !IsShared, shared items carry SharedBy, team items have neither. Each call
// returns one stage page; the cursor names the next stage like the real one.
type missedMeetingsFake struct {
	meetings   []model.MeetingListItem
	pageSize   int
	endless    bool // the owned stage never ends and stays recent
	recent     time.Time
	selections [][]string
	tabs       []string
	stages     []string // stage served by each call
	users      []string
}

func missedMeetingCursor(stage string, offset int) *string {
	raw, _ := json.Marshal(map[string]any{"stage": stage, "offset": offset})
	cursor := meetingFilterCursorPrefix + base64.RawURLEncoding.EncodeToString(raw)
	return &cursor
}

func (fake *missedMeetingsFake) ListMeetingsForAccounts(_ context.Context, userID, tab, encoded string, accountIDs []string, _ int32, _ ...string) (*model.MeetingListResponse, error) {
	fake.users = append(fake.users, userID)
	fake.tabs = append(fake.tabs, tab)
	fake.selections = append(fake.selections, append([]string(nil), accountIDs...))
	stage, offset := "owned", 0
	if tab == "shared" {
		stage = "shared"
	} else if tab != "all" {
		return nil, ErrInvalidInput
	}
	if encoded != "" {
		raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(encoded, meetingFilterCursorPrefix))
		var cursor struct {
			Stage  string `json:"stage"`
			Offset int    `json:"offset"`
		}
		if err != nil || json.Unmarshal(raw, &cursor) != nil {
			return nil, ErrInvalidInput
		}
		stage, offset = cursor.Stage, cursor.Offset
	}
	fake.stages = append(fake.stages, stage)
	response := &model.MeetingListResponse{Meetings: []model.MeetingListItem{}}
	if fake.endless && stage == "owned" {
		response.Meetings = append(response.Meetings, model.MeetingListItem{
			MeetingID: fmt.Sprintf("m-%d", offset), AccountID: accountIDs[0], Date: fake.recent.Format(time.RFC3339),
		})
		response.NextCursor = missedMeetingCursor("owned", offset+1)
		return response, nil
	}
	selected := map[string]bool{}
	for _, id := range accountIDs {
		selected[id] = true
	}
	var items []model.MeetingListItem
	for _, meeting := range fake.meetings {
		kind := "owned"
		if meeting.IsShared && meeting.SharedBy != nil {
			kind = "shared"
		} else if meeting.IsShared {
			kind = "team"
		}
		if kind == stage && selected[meeting.AccountID] {
			items = append(items, meeting)
		}
	}
	size := fake.pageSize
	if size == 0 {
		size = 100
	}
	end := min(offset+size, len(items))
	response.Meetings = append(response.Meetings, items[min(offset, end):end]...)
	switch {
	case end < len(items):
		response.NextCursor = missedMeetingCursor(stage, end)
	case stage == "owned":
		response.NextCursor = missedMeetingCursor("shared", 0)
	case stage == "shared":
		response.NextCursor = missedMeetingCursor("team", 0)
	}
	return response, nil
}

type missedTaskStore struct {
	*performanceStoreFake
	tasks    []model.SFDCTask
	taskFrom string
	taskTo   string
}

func (store *missedTaskStore) ListPerformanceTasks(_ context.Context, userID, from, to string) ([]model.SFDCTask, error) {
	store.taskFrom, store.taskTo = from, to
	if userID != "owner" {
		return nil, nil
	}
	return store.tasks, nil
}

func TestListMissedJoinsOnlyCurrentlyReadableAccounts(test *testing.T) {
	loc := missedSeoul(test)
	now := time.Date(2026, 11, 10, 9, 0, 0, 0, loc)
	store := &missedTaskStore{performanceStoreFake: &performanceStoreFake{
		assignments: map[string][]model.SFDCAssignment{"owner": {
			{SFDCAccountID: "S1", Name: "Acme", Role: "REGISTRY"},
			{SFDCAccountID: "S2", Name: "Gone", Role: "activity"},
			{SFDCAccountID: "S3", Name: "Quiet", Role: "activity"},
		}},
		mappings: map[string][]model.AccountMapping{"owner": {
			{SFDCAccountID: "S1", AccountID: "acct-1"},
			{SFDCAccountID: "S2", AccountID: "acct-gone", Manual: true},
			{SFDCAccountID: "S3", AccountID: "acct-3"},
		}},
		statuses: map[string][]model.PerformanceSourceStatus{"owner": {
			{Source: model.PerformanceSourceSFDCTasks, Revision: 2, LastCompletedAt: "2026-11-09T01:00:00Z"},
		}},
	}, tasks: []model.SFDCTask{
		{TaskID: "T1", SFDCAccountID: "S3", ActivityDate: "2026-11-10"},
		{TaskID: "T2", SFDCAccountID: "S2", ActivityDate: "2026-11-10"},
		{TaskID: "T3", ActivityDate: "2026-11-10"},
	}}
	accounts := &performanceCandidatesFake{candidates: []NamedCandidate{
		{Kind: "account", ID: "acct-1", Name: "Acme", Domains: []string{"acme.com"}},
		{Kind: "account", ID: "acct-3", Name: "Quiet Corp"},
	}}
	completed := time.Date(2026, 11, 8, 2, 0, 0, 0, time.UTC)
	calendar := &missedCalendarFake{
		events: []model.CalendarEvent{
			{EventID: "ev-acme", Title: "Sync", OrganizerDomain: "acme.com", StartsAt: now.Add(-24 * time.Hour)},
			{EventID: "ev-gone", Title: "Gone review", OrganizerDomain: "gone.com", StartsAt: now.Add(-25 * time.Hour)},
			{EventID: "ev-future", Title: "Sync", OrganizerDomain: "acme.com", StartsAt: now.Add(48 * time.Hour)},
		},
		sources: &model.CalendarSourcePage{Sources: []model.CalendarSourceStatus{{SourceID: "src", LastCompletedAt: &completed}}},
	}
	meetings := &missedMeetingsFake{meetings: []model.MeetingListItem{
		{MeetingID: "m-own", AccountID: "acct-1", Date: now.Add(-2 * time.Hour).Format(time.RFC3339)},
		{MeetingID: "m-team", AccountID: "acct-1", Date: now.Add(-3 * time.Hour).Format(time.RFC3339), IsShared: true},
	}}
	svc := NewPerformanceService(store, accounts)
	svc.now = func() time.Time { return now }
	svc.SetMissedSources(calendar, meetings)

	list, err := svc.ListMissed(test.Context(), "owner", "week")
	if err != nil {
		test.Fatal(err)
	}
	// ev-acme and m-own are gaps on acct-1 (S1); acct-3 has a task today; the
	// team-stream meeting, the future event and the unreadable mapping are ignored.
	if len(list.Items) != 2 || list.Items[0].RefID != "m-own" || list.Items[1].RefID != "ev-acme" {
		test.Fatalf("items: %+v", list.Items)
	}
	for _, item := range list.Items {
		if item.Kind != MissedKindLoggingGap || item.AccountID != "acct-1" || item.SFDCAccountID != "S1" {
			test.Fatalf("item: %+v", item)
		}
	}
	// T2 maps to an unreadable account and T3 has no account; ev-gone matches nothing.
	if list.UnmappedCount != 3 {
		test.Fatalf("unmapped: %d", list.UnmappedCount)
	}
	// One owned read (tab all) and one shared read; neither enters the team stage.
	if len(meetings.selections) != 2 || meetings.tabs[0] != "all" || meetings.tabs[1] != "shared" {
		test.Fatalf("meeting reads: %v %v", meetings.tabs, meetings.selections)
	}
	for _, selection := range meetings.selections {
		if len(selection) != 2 || selection[0] != "acct-1" || selection[1] != "acct-3" {
			test.Fatalf("meeting selection: %v", meetings.selections)
		}
	}
	for _, user := range append(calendar.users, meetings.users...) {
		if user != "owner" {
			test.Fatalf("read another user's data: %s", user)
		}
	}
	for _, query := range calendar.queries {
		if query.AccountID != "" || query.To.After(now) {
			test.Fatalf("calendar query: %+v", query)
		}
	}
	freshness := map[string]string{}
	for _, source := range list.SourceFreshness {
		freshness[source.Source] = source.LastCompletedAt
	}
	if len(list.SourceFreshness) != 3 || freshness[model.PerformanceSourceSFDCTasks] != "2026-11-09T01:00:00Z" ||
		freshness[model.PerformanceSourceSFDCAssignments] != "" || freshness["calendar"] != "2026-11-08T02:00:00Z" {
		test.Fatalf("freshness: %+v", list.SourceFreshness)
	}
}

func TestListMissedSilentCustomerAndCalendarChunks(test *testing.T) {
	loc := missedSeoul(test)
	now := time.Date(2026, 11, 10, 9, 0, 0, 0, loc)
	store := &missedTaskStore{performanceStoreFake: &performanceStoreFake{
		assignments: map[string][]model.SFDCAssignment{"owner": {{SFDCAccountID: "S1", Name: "Acme", Role: "activity"}}},
		mappings:    map[string][]model.AccountMapping{"owner": {{SFDCAccountID: "S1", AccountID: "acct-1"}}},
	}, tasks: []model.SFDCTask{{TaskID: "T1", SFDCAccountID: "S1", ActivityDate: "2026-03-02"}}}
	accounts := &performanceCandidatesFake{candidates: []NamedCandidate{{Kind: "account", ID: "acct-1", Name: "Acme"}}}
	calendar := &missedCalendarFake{}
	svc := NewPerformanceService(store, accounts)
	svc.now = func() time.Time { return now }
	svc.SetMissedSources(calendar, &missedMeetingsFake{})

	list, err := svc.ListMissed(test.Context(), "owner", "year")
	if err != nil {
		test.Fatal(err)
	}
	if len(list.Items) != 1 || list.Items[0].Kind != MissedKindSilentCustomer || list.Items[0].SFDCAccountID != "S1" ||
		list.Items[0].RefKind != TouchpointSFDC || list.Items[0].RefID != "T1" || list.Items[0].DaysSince != 253 {
		test.Fatalf("items: %+v", list.Items)
	}
	// The year window (from three days before January 1) spans several calendar chunks.
	if len(calendar.queries) < 4 {
		test.Fatalf("calendar chunks: %d", len(calendar.queries))
	}
	if !calendar.queries[0].From.Equal(time.Date(2025, 12, 29, 0, 0, 0, 0, loc)) {
		test.Fatalf("first chunk: %+v", calendar.queries[0])
	}
	for index := 1; index < len(calendar.queries); index++ {
		if !calendar.queries[index].From.Equal(calendar.queries[index-1].To) {
			test.Fatalf("chunk gap at %d", index)
		}
	}
	if last := calendar.queries[len(calendar.queries)-1]; !last.To.Equal(now) {
		test.Fatalf("last chunk: %+v", last)
	}
	if store.taskFrom != "2025-12-29" || store.taskTo != "2026-11-13" {
		test.Fatalf("task window: %s..%s", store.taskFrom, store.taskTo)
	}
}

func TestListMissedRejectsBadInput(test *testing.T) {
	svc := performanceTestService(&performanceStoreFake{}, nil)
	if _, err := svc.ListMissed(test.Context(), "owner", "month"); !errors.Is(err, ErrInvalidInput) {
		test.Fatalf("bad period: %v", err)
	}
	if _, err := svc.ListMissed(test.Context(), "bad user", "week"); !errors.Is(err, ErrForbidden) {
		test.Fatalf("bad user: %v", err)
	}
}

func TestTitleMatchRequiresTokenBoundary(test *testing.T) {
	loc := missedSeoul(test)
	period := Period{From: missedAt(loc, 3, 1, 0), To: missedAt(loc, 3, 31, 0)}
	accounts := []MissedAccount{{ID: "acct-sk", Name: "SK"}, {ID: "acct-lg", Name: "LG"}, {ID: "acct-g", Name: "글로벡스"}}
	at := missedAt(loc, 3, 10, 10)
	cases := map[string]string{
		"Weekly task review": "",
		"SK 주간 미팅":           "acct-sk",
		"Algorithm study":    "",
		"[LG] kickoff":       "acct-lg",
		"sk/lg joint":        "",
		"글로벡스 리뷰":            "acct-g",
		"신글로벡스 리뷰":           "",
	}
	for title, want := range cases {
		events := []model.CalendarEvent{{EventID: "ev", Title: title, OrganizerDomain: "amazon.com", StartsAt: at}}
		touchpoints, unmapped := CalendarTouchpoints(period, events, accounts)
		got := ""
		if len(touchpoints) == 1 {
			got = touchpoints[0].AccountID
		}
		if got != want || (want == "") != (unmapped == 1) {
			test.Fatalf("%q: got %q (%d unmapped), want %q", title, got, unmapped, want)
		}
	}
	if !containsToken("sk sk", "sk") || containsToken("tasks", "sk") || containsToken("", "sk") || containsToken("sk", "") {
		test.Fatal("containsToken edge cases")
	}
}

func TestLoggingGapComparesSameSFDCAccountWhenKnown(test *testing.T) {
	loc := missedSeoul(test)
	period := Period{From: missedAt(loc, 3, 1, 0), To: missedAt(loc, 3, 31, 0)}
	// S1 and S2 both map to acct-1; the only task is on S1.
	base := MissedInputs{
		Mappings: map[string]string{"S1": "acct-1", "S2": "acct-1"},
		Touchpoints: []Touchpoint{
			{AccountID: "acct-1", SFDCAccountID: "S1", Kind: TouchpointSFDC, RefID: "T1", At: missedAt(loc, 3, 10, 0)},
		},
	}
	withTouch := func(touch Touchpoint) []MissedItem {
		inputs := base
		inputs.Touchpoints = append(append([]Touchpoint(nil), base.Touchpoints...), touch)
		return missedKinds(ComputeMissed(period, inputs, missedConfig), MissedKindLoggingGap)
	}
	// A touchpoint that names S2 is not covered by a task on S1.
	gaps := withTouch(Touchpoint{AccountID: "acct-1", SFDCAccountID: "S2", Kind: TouchpointMeeting, RefID: "m-s2", At: missedAt(loc, 3, 10, 9)})
	if len(gaps) != 1 || gaps[0].SFDCAccountID != "S2" || gaps[0].RefID != "m-s2" {
		test.Fatalf("named S2: %+v", gaps)
	}
	// A touchpoint that names S1 is covered.
	if gaps := withTouch(Touchpoint{AccountID: "acct-1", SFDCAccountID: "S1", Kind: TouchpointMeeting, RefID: "m-s1", At: missedAt(loc, 3, 10, 9)}); len(gaps) != 0 {
		test.Fatalf("named S1: %+v", gaps)
	}
	// A touchpoint that knows only the account is covered by a task on any mapped SFDC account.
	if gaps := withTouch(Touchpoint{AccountID: "acct-1", Kind: TouchpointCalendar, RefID: "ev", At: missedAt(loc, 3, 10, 9)}); len(gaps) != 0 {
		test.Fatalf("account only: %+v", gaps)
	}
	// A named SFDC account that does not map to the touchpoint's account is not a gap.
	if gaps := withTouch(Touchpoint{AccountID: "acct-1", SFDCAccountID: "S9", Kind: TouchpointMeeting, RefID: "m-s9", At: missedAt(loc, 3, 10, 9)}); len(gaps) != 0 {
		test.Fatalf("unmapped SFDC: %+v", gaps)
	}
}

func missedWiredService(test *testing.T, calendar PerformanceCalendarReader, meetings PerformanceMeetingLister) *PerformanceService {
	test.Helper()
	store := &missedTaskStore{performanceStoreFake: &performanceStoreFake{
		assignments: map[string][]model.SFDCAssignment{"owner": {{SFDCAccountID: "S1", Name: "Acme", Role: "REGISTRY"}}},
		mappings:    map[string][]model.AccountMapping{"owner": {{SFDCAccountID: "S1", AccountID: "acct-1"}}},
	}}
	svc := NewPerformanceService(store, &performanceCandidatesFake{candidates: []NamedCandidate{{Kind: "account", ID: "acct-1", Name: "Acme"}}})
	svc.now = func() time.Time { return time.Date(2026, 11, 10, 9, 0, 0, 0, missedSeoul(test)) }
	if calendar != nil || meetings != nil {
		svc.SetMissedSources(calendar, meetings)
	}
	return svc
}

func TestListMissedRequiresWiredSources(test *testing.T) {
	cases := map[string]*PerformanceService{
		"none":     missedWiredService(test, nil, nil),
		"calendar": missedWiredService(test, nil, &missedMeetingsFake{}),
		"meetings": missedWiredService(test, &missedCalendarFake{}, nil),
	}
	for name, svc := range cases {
		if _, err := svc.ListMissed(test.Context(), "owner", "week"); !errors.Is(err, ErrPerformanceSourcesUnavailable) {
			test.Fatalf("%s: %v", name, err)
		}
	}
	if _, err := missedWiredService(test, &missedCalendarFake{}, &missedMeetingsFake{}).ListMissed(test.Context(), "owner", "week"); err != nil {
		test.Fatalf("wired: %v", err)
	}
}

func TestListMissedReadBounds(test *testing.T) {
	now := time.Date(2026, 11, 10, 9, 0, 0, 0, missedSeoul(test))
	calendar := &missedCalendarFake{endless: true}
	if _, err := missedWiredService(test, calendar, &missedMeetingsFake{}).ListMissed(test.Context(), "owner", "week"); !errors.Is(err, ErrPerformanceReadBound) {
		test.Fatalf("calendar bound: %v", err)
	}
	if len(calendar.queries) != performanceMissedCalendarMaxPages {
		test.Fatalf("calendar pages: %d", len(calendar.queries))
	}
	meetings := &missedMeetingsFake{endless: true, recent: now.Add(-time.Hour)}
	if _, err := missedWiredService(test, &missedCalendarFake{}, meetings).ListMissed(test.Context(), "owner", "week"); !errors.Is(err, ErrPerformanceReadBound) {
		test.Fatalf("meeting bound: %v", err)
	}
	if performanceMissedMeetingMaxPages != 200 || len(meetings.tabs) != 200 {
		test.Fatalf("meeting pages: %d", len(meetings.tabs))
	}
}

func TestMissedMeetingReadSkipsTeamAndKeepsBackdated(test *testing.T) {
	now := time.Date(2026, 11, 10, 9, 0, 0, 0, missedSeoul(test))
	lookback := now.AddDate(0, 0, -64)
	owner := "owner@example.com"
	// Owned items arrive in creation order, not Date order: the newest-created
	// meeting is backdated outside the window, and an in-window meeting is on a
	// later page.
	meetings := &missedMeetingsFake{pageSize: 1, meetings: []model.MeetingListItem{
		{MeetingID: "own-backdated", AccountID: "acct-1", Date: now.AddDate(0, 0, -120).Format(time.RFC3339)},
		{MeetingID: "own-older", AccountID: "acct-1", Date: now.AddDate(0, 0, -200).Format(time.RFC3339)},
		{MeetingID: "own-in-window", AccountID: "acct-1", Date: now.AddDate(0, 0, -10).Format(time.RFC3339)},
		{MeetingID: "shared-old", AccountID: "acct-1", Date: now.AddDate(0, 0, -90).Format(time.RFC3339), IsShared: true, SharedBy: &owner},
		{MeetingID: "shared-new", AccountID: "acct-1", Date: now.AddDate(0, 0, -5).Format(time.RFC3339), IsShared: true, SharedBy: &owner},
		{MeetingID: "team-1", AccountID: "acct-1", Date: now.Add(-2 * time.Hour).Format(time.RFC3339), IsShared: true},
		{MeetingID: "team-2", AccountID: "acct-1", Date: now.Add(-3 * time.Hour).Format(time.RFC3339), IsShared: true},
	}}
	svc := missedWiredService(test, &missedCalendarFake{}, meetings)
	touchpoints, err := svc.missedMeetingTouchpoints(test.Context(), "owner", map[string]bool{"acct-1": true}, lookback, now)
	if err != nil {
		test.Fatal(err)
	}
	got := map[string]bool{}
	for _, touch := range touchpoints {
		got[touch.RefID] = true
	}
	if len(got) != 2 || !got["own-in-window"] || !got["shared-new"] {
		test.Fatalf("touchpoints: %+v", touchpoints)
	}
	// Owned: three one-item pages, read in full. Shared: two pages, ending when
	// the cursor reaches the team stage. No team page is requested.
	want := []string{"all", "all", "all", "shared", "shared"}
	if strings.Join(meetings.tabs, ",") != strings.Join(want, ",") {
		test.Fatalf("reads: %v", meetings.tabs)
	}
	for _, stage := range meetings.stages {
		if stage == "team" {
			test.Fatalf("team stage requested: %v", meetings.stages)
		}
	}
}
