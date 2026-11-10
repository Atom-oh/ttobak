package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ttobak/backend/internal/model"
)

// Missed-item rules. The pure functions below take already-authorized owner
// data; the service gathers it and rechecks every account join against the
// owner's currently readable accounts.

const (
	MissedKindLoggingGap     = "logging_gap"
	MissedKindSilentCustomer = "silent_customer"

	TouchpointCalendar = "calendar"
	TouchpointMeeting  = "meeting"
	TouchpointSFDC     = "sfdc"

	// MissedMaxItems bounds the response, newest first.
	MissedMaxItems = 500

	performanceMissedCalendarMaxPages = 200
	performanceMissedMeetingMaxPages  = 200
	performanceMissedMeetingChunk     = 100
	performanceMissedMinLabelRunes    = 2
	performanceMissedLocation         = "Asia/Seoul"
)

// ErrPerformanceSourcesUnavailable reports that the calendar or meeting reader
// is not wired. SFDC data alone would invent silent customers.
var ErrPerformanceSourcesUnavailable = errors.New("performance missed sources are not configured")

// ErrPerformanceReadBound reports that a source exceeded the missed-item read
// bound. Partial data would invent silent customers, so the request fails.
var ErrPerformanceReadBound = errors.New("performance source exceeds read bound")

// DefaultMissedConfig holds the spec thresholds: silent after 60 days, and a
// logging-gap window of plus or minus 3 days.
var DefaultMissedConfig = MissedConfig{SilentDays: 60, GapDays: 3}

// performanceInternalDomains never attribute an event by organizer domain.
var performanceInternalDomains = []string{"amazon.com", "amazon.co.kr"}

type Period struct {
	From, To time.Time
}

type MissedConfig struct {
	SilentDays int
	GapDays    int
}

// Touchpoint is one contact with an attributed account. SFDCAccountID is set
// when the source names the SFDC account (always for SFDC tasks); calendar and
// meeting touchpoints know only the TTOBAK account.
type Touchpoint struct {
	AccountID     string
	SFDCAccountID string
	Kind          string
	RefID         string
	At            time.Time
}

// MissedItem is the wire item.
type MissedItem = model.PerformanceMissedItem

// MissedInputs are the owner's attributed touchpoints, assignments and SFDC
// mappings. Mappings must already be limited to currently readable accounts.
type MissedInputs struct {
	Touchpoints []Touchpoint
	Assignments []model.SFDCAssignment
	Mappings    map[string]string // SFDC account ID -> account ID
}

// MissedAccount is a currently readable account used for attribution.
type MissedAccount struct {
	ID      string
	Name    string
	Aliases []string
	Domains []string
}

// PeriodFor returns the period in loc: week is ISO Monday through Sunday,
// quarter is the calendar quarter, and year is January 1 through now.
func PeriodFor(kind string, now time.Time, loc *time.Location) (Period, error) {
	local := now.In(loc)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	switch kind {
	case "week":
		from := day.AddDate(0, 0, -((int(local.Weekday()) + 6) % 7))
		return Period{From: from, To: from.AddDate(0, 0, 7).Add(-time.Nanosecond)}, nil
	case "quarter":
		from := time.Date(local.Year(), ((local.Month()-1)/3)*3+1, 1, 0, 0, 0, 0, loc)
		return Period{From: from, To: from.AddDate(0, 3, 0).Add(-time.Nanosecond)}, nil
	case "year":
		return Period{From: time.Date(local.Year(), time.January, 1, 0, 0, 0, 0, loc), To: local}, nil
	}
	return Period{}, ErrInvalidInput
}

// missedDay is the civil day number of t in loc.
func missedDay(t time.Time, loc *time.Location) int {
	local := t.In(loc)
	return int(time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC).Unix() / 86400)
}

func inPeriod(p Period, at time.Time) bool {
	return !at.Before(p.From) && !at.After(p.To)
}

// ComputeMissed returns logging gaps and silent customers, newest first and at
// most MissedMaxItems. A logging gap is a calendar or meeting touchpoint in
// the period on an SFDC-mapped account with no SFDC touchpoint on that account
// within GapDays. A silent customer is an assigned, mapped account whose last
// touchpoint at or before p.To is more than SilentDays old.
func ComputeMissed(p Period, in MissedInputs, cfg MissedConfig) []MissedItem {
	loc := p.To.Location()
	end := missedDay(p.To, loc)
	sfdcByAccount := make(map[string]string)
	for sfdcID, accountID := range in.Mappings {
		if accountID == "" {
			continue
		}
		if prior, found := sfdcByAccount[accountID]; !found || sfdcID < prior {
			sfdcByAccount[accountID] = sfdcID
		}
	}
	// Task days are kept per account and per SFDC account. A touchpoint that
	// names its SFDC account needs a task on that same SFDC account. One that
	// knows only the TTOBAK account (calendar, meeting) is satisfied by a task
	// on any SFDC account mapped to it, because no finer attribution exists.
	taskDays := make(map[string][]int)
	sfdcTaskDays := make(map[string][]int)
	for _, touch := range in.Touchpoints {
		if touch.Kind == TouchpointSFDC {
			day := missedDay(touch.At, loc)
			taskDays[touch.AccountID] = append(taskDays[touch.AccountID], day)
			if touch.SFDCAccountID != "" {
				sfdcTaskDays[touch.SFDCAccountID] = append(sfdcTaskDays[touch.SFDCAccountID], day)
			}
		}
	}
	type ranked struct {
		item MissedItem
		at   time.Time
	}
	var items []ranked
	for _, touch := range in.Touchpoints {
		if touch.Kind != TouchpointCalendar && touch.Kind != TouchpointMeeting || !inPeriod(p, touch.At) {
			continue
		}
		sfdcID, mapped := sfdcByAccount[touch.AccountID]
		candidates := taskDays[touch.AccountID]
		if touch.SFDCAccountID != "" {
			sfdcID, mapped = touch.SFDCAccountID, in.Mappings[touch.SFDCAccountID] == touch.AccountID
			candidates = sfdcTaskDays[touch.SFDCAccountID]
		}
		if !mapped {
			continue
		}
		day := missedDay(touch.At, loc)
		logged := false
		for _, taskDay := range candidates {
			if taskDay >= day-cfg.GapDays && taskDay <= day+cfg.GapDays {
				logged = true
				break
			}
		}
		if logged {
			continue
		}
		items = append(items, ranked{at: touch.At, item: MissedItem{
			Kind: MissedKindLoggingGap, AccountID: touch.AccountID, SFDCAccountID: sfdcID,
			RefKind: touch.Kind, RefID: touch.RefID, At: touch.At.UTC().Format(time.RFC3339), DaysSince: end - day,
		}})
	}
	last := make(map[string]Touchpoint)
	for _, touch := range in.Touchpoints {
		if touch.At.After(p.To) {
			continue
		}
		if prior, found := last[touch.AccountID]; !found || touch.At.After(prior.At) ||
			touch.At.Equal(prior.At) && touch.Kind+touch.RefID < prior.Kind+prior.RefID {
			last[touch.AccountID] = touch
		}
	}
	seen := make(map[string]bool)
	for _, assignment := range in.Assignments {
		accountID := in.Mappings[assignment.SFDCAccountID]
		if accountID == "" || seen[assignment.SFDCAccountID] {
			continue
		}
		seen[assignment.SFDCAccountID] = true
		touch, found := last[accountID]
		item := MissedItem{Kind: MissedKindSilentCustomer, AccountID: accountID, SFDCAccountID: assignment.SFDCAccountID, DaysSince: -1}
		if found {
			days := end - missedDay(touch.At, loc)
			if days <= cfg.SilentDays {
				continue
			}
			item.RefKind, item.RefID, item.At, item.DaysSince = touch.Kind, touch.RefID, touch.At.UTC().Format(time.RFC3339), days
		}
		items = append(items, ranked{item: item, at: touch.At})
	}
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.item.At == "" || b.item.At == "" {
			if (a.item.At == "") != (b.item.At == "") {
				return b.item.At == ""
			}
		} else if !a.at.Equal(b.at) {
			return a.at.After(b.at)
		}
		if a.item.Kind != b.item.Kind {
			return a.item.Kind < b.item.Kind
		}
		if a.item.AccountID != b.item.AccountID {
			return a.item.AccountID < b.item.AccountID
		}
		if a.item.SFDCAccountID != b.item.SFDCAccountID {
			return a.item.SFDCAccountID < b.item.SFDCAccountID
		}
		return a.item.RefKind+a.item.RefID < b.item.RefKind+b.item.RefID
	})
	if len(items) > MissedMaxItems {
		items = items[:MissedMaxItems]
	}
	out := make([]MissedItem, len(items))
	for index := range items {
		out[index] = items[index].item
	}
	return out
}

func performanceInternalDomain(domain string) bool {
	for _, internal := range performanceInternalDomains {
		if domain == internal || strings.HasSuffix(domain, "."+internal) {
			return true
		}
	}
	return false
}

// uniqueAccount returns the only account in ids, or "".
func uniqueAccount(ids map[string]bool) string {
	if len(ids) != 1 {
		return ""
	}
	for id := range ids {
		return id
	}
	return ""
}

// CalendarTouchpoints attributes events to readable accounts: an event's own
// readable account link first, then a unique organizer-domain match (exact or
// subdomain, never an internal domain), then a title containing exactly one
// account's name or alias. Unattributed events in the period are counted and
// never guessed. Events after p.To are ignored.
func CalendarTouchpoints(p Period, events []model.CalendarEvent, accounts []MissedAccount) ([]Touchpoint, int) {
	readable := make(map[string]bool, len(accounts))
	domains := make(map[string]map[string]bool)
	type label struct{ folded, id string }
	var labels []label
	for _, account := range accounts {
		readable[account.ID] = true
		for _, domain := range account.Domains {
			domain = strings.Trim(strings.ToLower(strings.TrimSpace(domain)), ".")
			if domain == "" || performanceInternalDomain(domain) {
				continue
			}
			if domains[domain] == nil {
				domains[domain] = make(map[string]bool)
			}
			domains[domain][account.ID] = true
		}
		for _, name := range append([]string{account.Name}, account.Aliases...) {
			if folded := NormalizePersonName(name); utf8.RuneCountInString(folded) >= performanceMissedMinLabelRunes {
				labels = append(labels, label{folded: folded, id: account.ID})
			}
		}
	}
	var touchpoints []Touchpoint
	unmapped := 0
	for _, event := range events {
		if event.StartsAt.After(p.To) {
			continue
		}
		accountID := ""
		if event.AccountID != "" && readable[event.AccountID] {
			accountID = event.AccountID
		}
		if domain := strings.ToLower(event.OrganizerDomain); accountID == "" && domain != "" && !performanceInternalDomain(domain) {
			matched := make(map[string]bool)
			// Walk parent domains, but never a bare suffix with no dot.
			for candidate := domain; strings.Contains(candidate, "."); {
				for id := range domains[candidate] {
					matched[id] = true
				}
				dot := strings.IndexByte(candidate, '.')
				if dot < 0 {
					break
				}
				candidate = candidate[dot+1:]
			}
			accountID = uniqueAccount(matched)
		}
		if accountID == "" {
			title := NormalizePersonName(event.Title)
			matched := make(map[string]bool)
			for _, candidate := range labels {
				if containsToken(title, candidate.folded) {
					matched[candidate.id] = true
				}
			}
			accountID = uniqueAccount(matched)
		}
		if accountID == "" {
			if inPeriod(p, event.StartsAt) {
				unmapped++
			}
			continue
		}
		touchpoints = append(touchpoints, Touchpoint{AccountID: accountID, Kind: TouchpointCalendar, RefID: event.EventID, At: event.StartsAt})
	}
	return touchpoints, unmapped
}

func performanceWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// containsToken reports whether token occurs in text as a whole token
// sequence: each occurrence must start at the beginning of text or after a
// rune that is not a letter or digit, and end at the end of text or before
// one. Both inputs are already folded. "sk" does not match "task"; Korean
// labels follow the same rule.
func containsToken(text, token string) bool {
	if token == "" {
		return false
	}
	for offset := 0; offset < len(text); {
		index := strings.Index(text[offset:], token)
		if index < 0 {
			return false
		}
		start := offset + index
		end := start + len(token)
		before, _ := utf8.DecodeLastRuneInString(text[:start])
		after, _ := utf8.DecodeRuneInString(text[end:])
		if (start == 0 || !performanceWordRune(before)) && (end == len(text) || !performanceWordRune(after)) {
			return true
		}
		_, size := utf8.DecodeRuneInString(text[start:])
		offset = start + size
	}
	return false
}

// SFDCTouchpoints attributes tasks through the readable mappings. A task with
// no SFDC account, or one whose account has no readable mapping, is counted
// when its date falls in the period and is never guessed. Task dates are
// midnight in the period's location.
func SFDCTouchpoints(p Period, tasks []model.SFDCTask, mappings map[string]string) ([]Touchpoint, int) {
	loc := p.To.Location()
	var touchpoints []Touchpoint
	unmapped := 0
	for _, task := range tasks {
		at, err := time.ParseInLocation(performanceDateLayout, task.ActivityDate, loc)
		if err != nil {
			continue
		}
		accountID := ""
		if task.SFDCAccountID != "" {
			accountID = mappings[task.SFDCAccountID]
		}
		if accountID == "" {
			if missedDay(at, loc) >= missedDay(p.From, loc) && missedDay(at, loc) <= missedDay(p.To, loc) {
				unmapped++
			}
			continue
		}
		touchpoints = append(touchpoints, Touchpoint{AccountID: accountID, SFDCAccountID: task.SFDCAccountID, Kind: TouchpointSFDC, RefID: task.TaskID, At: at})
	}
	return touchpoints, unmapped
}

// ---- service wiring ----

// PerformanceCalendarReader is CalendarService: owner-only, decrypted events.
type PerformanceCalendarReader interface {
	ListEvents(ctx context.Context, userID string, query model.CalendarQuery) (*model.CalendarEventPage, error)
	ListSources(ctx context.Context, userID, cursor string, limit int32) (*model.CalendarSourcePage, error)
}

// PerformanceMeetingLister is MeetingService's account-filtered meeting list.
type PerformanceMeetingLister interface {
	ListMeetingsForAccounts(ctx context.Context, userID, tab, encoded string, accountIDs []string, limit int32, joinedAccountIDs ...string) (*model.MeetingListResponse, error)
}

// SetMissedSources wires the calendar and meeting readers. Without them
// ListMissed fails with ErrPerformanceSourcesUnavailable.
func (svc *PerformanceService) SetMissedSources(calendar PerformanceCalendarReader, meetings PerformanceMeetingLister) {
	svc.calendar, svc.meetings = calendar, meetings
}

// ListMissed computes the owner's missed items. Period is week, quarter or
// year (week when empty). The evaluated end is capped at now, so scheduled
// future events are not touchpoints.
func (svc *PerformanceService) ListMissed(ctx context.Context, userID, period string) (*model.PerformanceMissedList, error) {
	if !performanceUserPattern.MatchString(userID) {
		return nil, ErrForbidden
	}
	if period == "" {
		period = "week"
	}
	if !performancePeriods[period] {
		return nil, ErrInvalidInput
	}
	if svc.calendar == nil || svc.meetings == nil {
		return nil, ErrPerformanceSourcesUnavailable
	}
	loc, err := time.LoadLocation(performanceMissedLocation)
	if err != nil {
		return nil, err
	}
	now := svc.now().In(loc)
	p, err := PeriodFor(period, now, loc)
	if err != nil {
		return nil, err
	}
	if p.To.After(now) {
		p.To = now
	}
	cfg := DefaultMissedConfig
	lookback := p.From
	if silent := p.To.AddDate(0, 0, -(cfg.SilentDays + 1)); silent.Before(lookback) {
		lookback = silent
	}
	lookback = time.Date(lookback.Year(), lookback.Month(), lookback.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, -cfg.GapDays)

	accounts, err := svc.missedReadableAccounts(ctx, userID)
	if err != nil {
		return nil, err
	}
	readable := make(map[string]bool, len(accounts))
	for _, account := range accounts {
		readable[account.ID] = true
	}
	stored, err := svc.store.ListAccountMappings(ctx, userID)
	if err != nil {
		return nil, err
	}
	// Recheck every mapping, manual or automatic, against current readability.
	mappings := make(map[string]string, len(stored))
	mappedAccounts := make(map[string]bool)
	for _, mapping := range stored {
		if mapping.SFDCAccountID != "" && readable[mapping.AccountID] {
			mappings[mapping.SFDCAccountID] = mapping.AccountID
			mappedAccounts[mapping.AccountID] = true
		}
	}
	assignments, err := svc.store.ListPerformanceAssignments(ctx, userID)
	if err != nil {
		return nil, err
	}
	tasks, err := svc.store.ListPerformanceTasks(ctx, userID, lookback.Format(performanceDateLayout),
		p.To.AddDate(0, 0, cfg.GapDays).Format(performanceDateLayout))
	if err != nil {
		return nil, err
	}
	touchpoints, unmapped := SFDCTouchpoints(p, tasks, mappings)

	events, err := svc.missedCalendarEvents(ctx, userID, lookback, p.To)
	if err != nil {
		return nil, err
	}
	calendarTouchpoints, unmappedEvents := CalendarTouchpoints(p, events, accounts)
	touchpoints = append(touchpoints, calendarTouchpoints...)
	unmapped += unmappedEvents

	meetingTouchpoints, err := svc.missedMeetingTouchpoints(ctx, userID, mappedAccounts, lookback, p.To)
	if err != nil {
		return nil, err
	}
	touchpoints = append(touchpoints, meetingTouchpoints...)

	freshness, err := svc.missedFreshness(ctx, userID)
	if err != nil {
		return nil, err
	}
	items := ComputeMissed(p, MissedInputs{Touchpoints: touchpoints, Assignments: assignments, Mappings: mappings}, cfg)
	if items == nil {
		items = []MissedItem{}
	}
	return &model.PerformanceMissedList{Items: items, UnmappedCount: unmapped, SourceFreshness: freshness,
		CalendarCoverageStart: calendarCoverageStart(events)}, nil
}

// calendarCoverageStart is the earliest event start among the events read, as
// RFC3339 UTC, or empty when there are none.
func calendarCoverageStart(events []model.CalendarEvent) string {
	var earliest time.Time
	for _, event := range events {
		if earliest.IsZero() || event.StartsAt.Before(earliest) {
			earliest = event.StartsAt
		}
	}
	if earliest.IsZero() {
		return ""
	}
	return earliest.UTC().Format(time.RFC3339)
}

// missedReadableAccounts lists only accounts the owner can read now; names,
// aliases and domains come from nowhere else.
func (svc *PerformanceService) missedReadableAccounts(ctx context.Context, userID string) ([]MissedAccount, error) {
	source, ok := svc.accounts.(PerformanceAccountCandidates)
	if !ok {
		return nil, nil
	}
	candidates, err := source.ListReadableAccountCandidates(ctx, userID)
	if err != nil {
		return nil, err
	}
	accounts := make([]MissedAccount, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.ID == "" {
			continue
		}
		accounts = append(accounts, MissedAccount{ID: candidate.ID, Name: candidate.Name, Aliases: candidate.Aliases, Domains: candidate.Domains})
	}
	return accounts, nil
}

// missedCalendarEvents pages the owner's events in contiguous windows of at
// most the calendar query limit.
func (svc *PerformanceService) missedCalendarEvents(ctx context.Context, userID string, from, to time.Time) ([]model.CalendarEvent, error) {
	if !to.After(from) {
		return nil, nil
	}
	seen := make(map[string]bool)
	var events []model.CalendarEvent
	pages := 0
	for start := from; start.Before(to); {
		end := start.Add(calendarMaxDuration)
		if end.After(to) {
			end = to
		}
		cursor := ""
		for {
			pages++
			if pages > performanceMissedCalendarMaxPages {
				return nil, fmt.Errorf("calendar events: %w", ErrPerformanceReadBound)
			}
			page, err := svc.calendar.ListEvents(ctx, userID, model.CalendarQuery{From: start, To: end, Limit: 50, Cursor: cursor})
			if err != nil {
				return nil, err
			}
			for _, event := range page.Events {
				if !seen[event.EventID] {
					seen[event.EventID] = true
					events = append(events, event)
				}
			}
			if page.NextCursor == nil || *page.NextCursor == "" {
				break
			}
			cursor = *page.NextCursor
		}
		start = end
	}
	return events, nil
}

// meetingCursorStage peeks at the stage of a cursor that
// ListMeetingsForAccounts itself produced: owned, shared or team. It is used
// only to stop reading early; the service revalidates any cursor it receives.
func meetingCursorStage(encoded string) string {
	if !strings.HasPrefix(encoded, meetingFilterCursorPrefix) {
		return ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(encoded, meetingFilterCursorPrefix))
	if err != nil {
		return ""
	}
	var cursor struct {
		Stage string `json:"stage"`
	}
	if json.Unmarshal(raw, &cursor) != nil {
		return ""
	}
	return cursor.Stage
}

// missedMeetingTouchpoints lists meetings linked to the mapped readable
// accounts that the owner owns or that were shared to the owner through a
// share row. ListMeetingsForAccounts has no date bound, and neither stage is
// ordered by the meeting Date (the owned GSI1 sort key is the creation time),
// so both streams are read in full and filtered by Date here:
//   - owned: tab "all" while its cursor stays in the owned stage;
//   - shared: tab "shared" while its cursor stays in the shared stage.
//
// Neither read enters the account-team stage, and team items (shared with no
// share row) are dropped if a final page carries them. A bound hit fails
// closed with ErrPerformanceReadBound.
func (svc *PerformanceService) missedMeetingTouchpoints(ctx context.Context, userID string, accounts map[string]bool, from, to time.Time) ([]Touchpoint, error) {
	if len(accounts) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(accounts))
	for id := range accounts {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var touchpoints []Touchpoint
	seen := make(map[string]bool)
	pages := 0
	read := func(chunk []string, tab, stage string) error {
		cursor := ""
		for {
			pages++
			if pages > performanceMissedMeetingMaxPages {
				return fmt.Errorf("meetings: %w", ErrPerformanceReadBound)
			}
			page, err := svc.meetings.ListMeetingsForAccounts(ctx, userID, tab, cursor, chunk, 100)
			if err != nil {
				return err
			}
			for _, meeting := range page.Meetings {
				owned := !meeting.IsShared
				if stage == "owned" && !owned || stage == "shared" && meeting.SharedBy == nil {
					continue
				}
				at, err := time.Parse(time.RFC3339, meeting.Date)
				if err != nil || at.Before(from) || at.After(to) || !accounts[meeting.AccountID] || seen[meeting.MeetingID] {
					continue
				}
				seen[meeting.MeetingID] = true
				touchpoints = append(touchpoints, Touchpoint{AccountID: meeting.AccountID, Kind: TouchpointMeeting, RefID: meeting.MeetingID, At: at})
			}
			if page.NextCursor == nil || *page.NextCursor == "" || meetingCursorStage(*page.NextCursor) != stage {
				return nil
			}
			cursor = *page.NextCursor
		}
	}
	for offset := 0; offset < len(ids); offset += performanceMissedMeetingChunk {
		chunk := ids[offset:min(offset+performanceMissedMeetingChunk, len(ids))]
		if err := read(chunk, "all", "owned"); err != nil {
			return nil, err
		}
		if err := read(chunk, "shared", "shared"); err != nil {
			return nil, err
		}
	}
	return touchpoints, nil
}

func (svc *PerformanceService) missedFreshness(ctx context.Context, userID string) ([]model.PerformanceSourceFreshness, error) {
	sources, err := svc.ListSources(ctx, userID)
	if err != nil {
		return nil, err
	}
	freshness := make([]model.PerformanceSourceFreshness, 0, len(sources.Sources)+1)
	for _, source := range sources.Sources {
		freshness = append(freshness, model.PerformanceSourceFreshness{Source: source.Source, LastCompletedAt: source.LastCompletedAt})
	}
	calendar := model.PerformanceSourceFreshness{Source: "calendar"}
	if svc.calendar != nil {
		page, err := svc.calendar.ListSources(ctx, userID, "", 20)
		if err != nil {
			return nil, err
		}
		var latest time.Time
		for _, source := range page.Sources {
			if source.LastCompletedAt != nil && source.LastCompletedAt.After(latest) {
				latest = *source.LastCompletedAt
			}
		}
		if !latest.IsZero() {
			calendar.LastCompletedAt = latest.UTC().Format(time.RFC3339)
		}
	}
	return append(freshness, calendar), nil
}
