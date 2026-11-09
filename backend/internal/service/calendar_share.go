package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
	"golang.org/x/text/unicode/norm"
)

const (
	calendarShareDirectoryMax = 200
	calendarSharedByMax       = 5
	calendarSharerEmailMax    = 320

	calendarModeAuto    = "auto"
	calendarModeAccount = "account"
	calendarModeHidden  = "hidden"

	calendarStateShared = "shared"
	calendarStateVeto   = "veto"
)

// CalendarMembership is the direct account-membership reader used for sharing.
// GetMember non-nil is the only membership test; ancestor and Billing access never count.
// GetAccount reads account metadata (name, aliases, domains) only, never authorization.
type CalendarMembership interface {
	GetMember(ctx context.Context, accountID, userID string) (*model.AccountMember, error)
	ListAccountMembers(ctx context.Context, accountID string) ([]model.AccountMember, error)
	ListAccountsForUser(ctx context.Context, userID string) ([]model.AccountMember, error)
	GetAccount(ctx context.Context, accountID string) (*model.Account, error)
}

// calendarMeetingKey identifies one meeting within an account across sharers:
// hex SHA-256 of accountID|startsAt|endsAt|normalized title.
func calendarMeetingKey(accountID string, startsAt, endsAt time.Time, title string) string {
	identity := accountID + "|" + startsAt.UTC().Format(time.RFC3339Nano) + "|" + endsAt.UTC().Format(time.RFC3339Nano) + "|" +
		strings.Join(strings.Fields(strings.ToLower(norm.NFKC.String(title))), " ")
	digest := sha256.Sum256([]byte(identity))
	return hex.EncodeToString(digest[:])
}

// calendarSharePayload is the only plaintext a share row carries.
type calendarSharePayload struct {
	Title     string    `json:"title"`
	StartsAt  time.Time `json:"startsAt"`
	EndsAt    time.Time `json:"endsAt"`
	TimeZone  string    `json:"timeZone"`
	IsAllDay  bool      `json:"isAllDay"`
	StartDate string    `json:"startDate,omitempty"`
	EndDate   string    `json:"endDate,omitempty"`
	Location  string    `json:"location,omitempty"`
}

func calendarSharePlaintext(input model.CalendarEventInput) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	payload := calendarSharePayload{
		Title: input.Title, StartsAt: input.StartsAt.UTC(), EndsAt: input.EndsAt.UTC(), TimeZone: input.TimeZone,
		IsAllDay: input.IsAllDay, StartDate: input.StartDate, EndDate: input.EndDate, Location: input.Location,
	}
	if encoder.Encode(payload) != nil || buffer.Len() > 4096 {
		return nil, ErrInvalidInput
	}
	return buffer.Bytes(), nil
}

// calendarShareDecision is the desired account row for one occurrence; empty accountID means none.
type calendarShareDecision struct {
	accountID string
	state     string
	// requested marks an account taken from the caller's sync input: a non-member gets 403.
	// Accounts derived from stored state (override, recorded shares) are dropped instead.
	requested bool
	// extraVeto is a second account to veto (hidden mode only); always derived.
	extraVeto string
}

// decideCalendarShare applies, in order: hidden override, Private, account override, automatic
// match. Private vetoes the recorded account (else the override account) and never shares. recorded lists the accounts the stored event row holds (its share, then its extra veto).
// Hidden vetoes the automatic account and the account where the event is recorded (shared or
// vetoed), at most two.
func decideCalendarShare(input model.CalendarEventInput, override *repository.CalendarOverrideRow, recorded []string, fromRequest bool) calendarShareDecision {
	switch {
	case override != nil && override.Mode == calendarModeHidden:
		var accounts []string
		requested := false
		if input.ShareAccountID != "" {
			accounts, requested = append(accounts, input.ShareAccountID), fromRequest
		}
		for _, account := range recorded {
			if len(accounts) < 2 && (len(accounts) == 0 || accounts[0] != account) {
				accounts = append(accounts, account)
			}
		}
		decision := calendarShareDecision{state: calendarStateVeto, requested: requested}
		if len(accounts) > 0 {
			decision.accountID = accounts[0]
		} else {
			decision.state = ""
		}
		if len(accounts) > 1 {
			decision.extraVeto = accounts[1]
		}
		return decision
	case input.Private:
		// Private beats an account override (spec G4): never share, veto where recorded.
		if len(recorded) > 0 {
			return calendarShareDecision{accountID: recorded[0], state: calendarStateVeto}
		}
		if override != nil && override.Mode == calendarModeAccount {
			return calendarShareDecision{accountID: override.AccountID, state: calendarStateVeto}
		}
		return calendarShareDecision{}
	case override != nil && override.Mode == calendarModeAccount:
		return calendarShareDecision{accountID: override.AccountID, state: calendarStateShared}
	case input.ShareAccountID != "":
		return calendarShareDecision{accountID: input.ShareAccountID, state: calendarStateShared, requested: fromRequest}
	}
	return calendarShareDecision{}
}

func calendarScopeKey(input model.CalendarEventInput, eventID string) string {
	if input.SeriesKey != "" {
		return input.SeriesKey
	}
	return eventID
}

// calendarGrantedShareKey is the share key the stored event row records as held, if any, and
// the accounts the row records (its share or veto, then its extra veto).
func calendarGrantedShareKey(old *repository.CalendarEventRow, userID string) (*repository.ShareKey, []string) {
	if old == nil || old.ShareState != calendarStateShared && old.ShareState != calendarStateVeto ||
		!calendarIdentifier.MatchString(old.ShareAccountID) || !calendarIdentifier.MatchString(old.MeetingKey) {
		return nil, nil
	}
	recorded := []string{old.ShareAccountID}
	if old.ShareState == calendarStateVeto && calendarIdentifier.MatchString(old.ExtraVetoAccountID) && old.ExtraVetoAccountID != old.ShareAccountID {
		recorded = append(recorded, old.ExtraVetoAccountID)
	}
	return &repository.ShareKey{AccountID: old.ShareAccountID, SK: model.CalendarShareSortKey(old.StartsAt, old.MeetingKey, userID)}, recorded
}

type calendarShareLookups struct {
	members   map[string]*model.AccountMember
	overrides map[string]*repository.CalendarOverrideRow
}

func newCalendarShareLookups() *calendarShareLookups {
	return &calendarShareLookups{members: map[string]*model.AccountMember{}, overrides: map[string]*repository.CalendarOverrideRow{}}
}

func (svc *CalendarService) directMember(ctx context.Context, lookups *calendarShareLookups, accountID, userID string) (*model.AccountMember, error) {
	if member, checked := lookups.members[accountID]; checked {
		return member, nil
	}
	if svc.membership == nil {
		return nil, nil
	}
	member, err := svc.membership.GetMember(ctx, accountID, userID)
	if err != nil {
		return nil, err
	}
	if member != nil && (member.UserID != userID || member.AccountID != "" && member.AccountID != accountID) {
		member = nil
	}
	lookups.members[accountID] = member
	return member, nil
}

func (svc *CalendarService) calendarOverride(ctx context.Context, lookups *calendarShareLookups, userID, sourceID, scopeKey string) (*repository.CalendarOverrideRow, error) {
	if override, checked := lookups.overrides[scopeKey]; checked {
		return override, nil
	}
	override, err := svc.store.GetCalendarOverride(ctx, userID, sourceID, scopeKey)
	if err != nil {
		return nil, err
	}
	lookups.overrides[scopeKey] = override
	return override, nil
}

// resolveCalendarShare checks membership for the decided accounts. A requested non-member
// account is ErrForbidden; a derived one is dropped (its stored row is then removed), and an
// extra veto whose primary was dropped becomes the primary.
func (svc *CalendarService) resolveCalendarShare(ctx context.Context, lookups *calendarShareLookups, userID string, decision calendarShareDecision) (calendarShareDecision, *model.AccountMember, *model.AccountMember, error) {
	if decision.accountID == "" {
		return calendarShareDecision{}, nil, nil, nil
	}
	member, err := svc.directMember(ctx, lookups, decision.accountID, userID)
	if err != nil {
		return calendarShareDecision{}, nil, nil, err
	}
	var extraMember *model.AccountMember
	if decision.extraVeto != "" {
		if extraMember, err = svc.directMember(ctx, lookups, decision.extraVeto, userID); err != nil {
			return calendarShareDecision{}, nil, nil, err
		}
		if extraMember == nil {
			decision.extraVeto = ""
		}
	}
	if member == nil {
		if decision.requested {
			return calendarShareDecision{}, nil, nil, ErrForbidden
		}
		if decision.extraVeto == "" {
			return calendarShareDecision{}, nil, nil, nil
		}
		decision.accountID, decision.extraVeto, member, extraMember = decision.extraVeto, "", extraMember, nil
	}
	return decision, member, extraMember, nil
}

// buildCalendarShares builds the share (or veto) row and the extra veto row of a decision.
func (svc *CalendarService) buildCalendarShares(ctx context.Context, userID, sourceID, eventID string, input model.CalendarEventInput, decision calendarShareDecision, member, extraMember *model.AccountMember) (*repository.CalendarShareRow, *repository.CalendarShareRow, error) {
	share, err := svc.buildCalendarShare(ctx, userID, sourceID, eventID, input, decision, member)
	if err != nil || decision.extraVeto == "" {
		return share, nil, err
	}
	extra, err := svc.buildCalendarShare(ctx, userID, sourceID, eventID, input, calendarShareDecision{accountID: decision.extraVeto, state: calendarStateVeto}, extraMember)
	return share, extra, err
}

// buildCalendarShare builds the share or veto row for one occurrence; nil for no share.
func (svc *CalendarService) buildCalendarShare(ctx context.Context, userID, sourceID, eventID string, input model.CalendarEventInput, decision calendarShareDecision, member *model.AccountMember) (*repository.CalendarShareRow, error) {
	if decision.accountID == "" {
		return nil, nil
	}
	meetingKey := calendarMeetingKey(decision.accountID, input.StartsAt, input.EndsAt, input.Title)
	share := &repository.CalendarShareRow{
		PK: model.PrefixAccount + decision.accountID, SK: model.CalendarShareSortKey(input.StartsAt, meetingKey, userID),
		AccountID: decision.accountID, MeetingKey: meetingKey, UserID: userID, SourceID: sourceID, EventID: eventID,
		StartsAt: input.StartsAt.UTC(), EndsAt: input.EndsAt.UTC(), State: decision.state,
		ExpiresAt: input.EndsAt.Add(90 * 24 * time.Hour).Unix(),
	}
	if member != nil && utf8.ValidString(member.Email) && len(member.Email) <= calendarSharerEmailMax {
		share.SharerEmail = member.Email
	}
	if decision.state == calendarStateShared {
		plaintext, err := calendarSharePlaintext(input)
		if err != nil {
			return nil, err
		}
		share.Ciphertext, err = svc.cipher.EncryptShare(ctx, decision.accountID, meetingKey, userID, plaintext)
		if err != nil {
			return nil, err
		}
	}
	return share, nil
}

// calendarSyncShare is the share plan for one upsert in a sync batch, with the row version it was planned from.
type calendarSyncShare struct {
	decision    calendarShareDecision
	member      *model.AccountMember
	extraMember *model.AccountMember
	previous    *repository.ShareKey
	expected    repository.CalendarRowVersion
}

// planCalendarSyncShares reads each upsert's stored row and override and decides its share.
// Every membership check completes before any encryption.
func (svc *CalendarService) planCalendarSyncShares(ctx context.Context, userID, sourceID string, mutations []model.CalendarMutation) ([]calendarSyncShare, error) {
	lookups := newCalendarShareLookups()
	plans := make([]calendarSyncShare, len(mutations))
	for index, mutation := range mutations {
		if mutation.Op != "upsert" {
			continue
		}
		input := *mutation.Event
		eventID := calendarEventID(sourceID, input.ExternalID)
		old, err := svc.store.GetCalendarEventRow(ctx, userID, sourceID, eventID)
		if err != nil {
			return nil, err
		}
		if old != nil && (old.UserID != userID || old.SourceID != sourceID || old.EventID != eventID) {
			return nil, repository.ErrCalendarCorruptData
		}
		previous, recorded := calendarGrantedShareKey(old, userID)
		override, err := svc.calendarOverride(ctx, lookups, userID, sourceID, calendarScopeKey(input, eventID))
		if err != nil {
			return nil, err
		}
		decision, member, extraMember, err := svc.resolveCalendarShare(ctx, lookups, userID, decideCalendarShare(input, override, recorded, true))
		if err != nil {
			return nil, err
		}
		plans[index] = calendarSyncShare{decision: decision, member: member, extraMember: extraMember, previous: previous,
			expected: repository.CalendarRowVersionOf(old)}
	}
	return plans, nil
}

func calendarSharingMode(override *repository.CalendarOverrideRow) string {
	if override == nil {
		return calendarModeAuto
	}
	return override.Mode
}

// calendarOwnerEvent is the owner's view of one stored event, including sharing state. The
// shared account comes from the stored row (the share the repository granted), never from the
// payload: a vetoed, unshared or collision-dropped event reports no shareAccountId.
func calendarOwnerEvent(row repository.CalendarEventRow, input model.CalendarEventInput, override *repository.CalendarOverrideRow) model.CalendarEvent {
	event := model.CalendarEvent{
		EventID: row.EventID, SourceID: row.SourceID, Provider: "outlook",
		Title: input.Title, StartsAt: input.StartsAt, EndsAt: input.EndsAt, TimeZone: input.TimeZone,
		IsAllDay: input.IsAllDay, StartDate: input.StartDate, EndDate: input.EndDate, Location: input.Location, OrganizerDomain: input.OrganizerDomain, AccountID: input.AccountID,
		CandidateAccountIDs: input.CandidateAccountIDs, Private: input.Private, SharingMode: calendarSharingMode(override),
	}
	if row.ShareState == calendarStateShared && calendarIdentifier.MatchString(row.ShareAccountID) && calendarIdentifier.MatchString(row.MeetingKey) {
		event.ShareAccountID = row.ShareAccountID
		if row.ShareAccountID == input.ShareAccountID {
			// Match evidence belongs to the automatic account only; a manual assignment has none.
			event.ShareEvidence = input.ShareEvidence
		}
	}
	return event
}

func (svc *CalendarService) readCalendarEvent(ctx context.Context, userID, sourceID, eventID string) (*repository.CalendarEventRow, model.CalendarEventInput, error) {
	row, err := svc.store.GetCalendarEventRow(ctx, userID, sourceID, eventID)
	if err != nil {
		return nil, model.CalendarEventInput{}, err
	}
	if row == nil {
		return nil, model.CalendarEventInput{}, ErrNotFound
	}
	input, err := svc.decryptCalendarInput(ctx, userID, *row)
	if err != nil {
		return nil, model.CalendarEventInput{}, err
	}
	return row, input, nil
}

func validCalendarSharingRequest(request model.CalendarSharingRequest) bool {
	switch request.Mode {
	case calendarModeAuto, calendarModeHidden:
		return request.AccountID == ""
	case calendarModeAccount:
		return calendarIdentifier.MatchString(request.AccountID)
	}
	return false
}

// SetEventSharing stores the owner's manual sharing mode for the event's series (or the
// occurrence without one) and applies it to this occurrence at once. Other occurrences
// follow at their next sync. A repeated concurrent share change is ErrConditionFailed.
func (svc *CalendarService) SetEventSharing(ctx context.Context, userID, sourceID, eventID string, request model.CalendarSharingRequest) (*model.CalendarEvent, error) {
	if !calendarIdentifier.MatchString(userID) {
		return nil, ErrForbidden
	}
	normalizedSource, err := normalizeCalendarUUID(sourceID)
	if err != nil || !calendarSeriesKey.MatchString(eventID) || !validCalendarSharingRequest(request) {
		return nil, ErrInvalidInput
	}
	if !svc.cipher.configured() {
		return nil, ErrCalendarEncryption
	}
	lookups := newCalendarShareLookups()
	if request.Mode == calendarModeAccount {
		member, err := svc.directMember(ctx, lookups, request.AccountID, userID)
		if err != nil {
			return nil, err
		}
		if member == nil {
			return nil, ErrForbidden
		}
	}
	row, input, err := svc.readCalendarEvent(ctx, userID, normalizedSource, eventID)
	if err != nil {
		return nil, err
	}
	scopeKey := calendarScopeKey(input, eventID)
	var override *repository.CalendarOverrideRow
	if request.Mode == calendarModeAuto {
		err = svc.store.DeleteCalendarOverride(ctx, userID, normalizedSource, scopeKey)
	} else {
		override = &repository.CalendarOverrideRow{Mode: request.Mode, AccountID: request.AccountID}
		err = svc.store.PutCalendarOverride(ctx, userID, normalizedSource, scopeKey, *override)
	}
	if err != nil {
		return nil, err
	}
	for attempt := 0; ; attempt++ {
		// The change carries the row version this plan was read from; a newer row fails it.
		previous, recorded := calendarGrantedShareKey(row, userID)
		decision, member, extraMember, err := svc.resolveCalendarShare(ctx, lookups, userID, decideCalendarShare(input, override, recorded, false))
		if err != nil {
			return nil, err
		}
		share, extra, err := svc.buildCalendarShares(ctx, userID, normalizedSource, eventID, input, decision, member, extraMember)
		if err != nil {
			return nil, err
		}
		err = svc.store.SetCalendarShareState(ctx, userID, normalizedSource, eventID, repository.CalendarShareChange{
			Expected: repository.CalendarRowVersionOf(row), Share: share, ExtraVeto: extra, Previous: previous})
		if errors.Is(err, repository.ErrCalendarShareStateChanged) {
			if attempt == 0 {
				if row, input, err = svc.readCalendarEvent(ctx, userID, normalizedSource, eventID); err != nil {
					return nil, err
				}
				continue
			}
			return nil, errors.Join(repository.ErrConditionFailed, err)
		}
		if errors.Is(err, repository.ErrCalendarEventNotFound) {
			return nil, ErrNotFound
		}
		if err != nil {
			return nil, err
		}
		break
	}
	row, input, err = svc.readCalendarEvent(ctx, userID, normalizedSource, eventID)
	if err != nil {
		return nil, err
	}
	event := calendarOwnerEvent(*row, input, override)
	return &event, nil
}

// ListAccountCalendar returns the account's shared meetings for a direct member: one entry
// per meetingKey, rows of non-members dropped, and any veto hiding the whole meeting.
func (svc *CalendarService) ListAccountCalendar(ctx context.Context, userID, accountID string, query model.CalendarQuery) (*model.AccountCalendarPage, error) {
	if !calendarIdentifier.MatchString(userID) {
		return nil, ErrForbidden
	}
	if !calendarIdentifier.MatchString(accountID) || !validCalendarInterval(query.From, query.To) || query.Limit < 0 || query.Limit > 50 ||
		len(query.Cursor) > 4096 || query.AccountID != "" && query.AccountID != accountID {
		return nil, ErrInvalidInput
	}
	query.From, query.To, query.AccountID = query.From.UTC(), query.To.UTC(), ""
	if query.Limit == 0 {
		query.Limit = 50
	}
	member, err := svc.directMember(ctx, newCalendarShareLookups(), accountID, userID)
	if err != nil {
		return nil, err
	}
	if member == nil {
		return nil, ErrForbidden
	}
	if !svc.cipher.configured() {
		return nil, ErrCalendarEncryption
	}
	members, err := svc.membership.ListAccountMembers(ctx, accountID)
	if err != nil {
		return nil, err
	}
	current := make(map[string]string, len(members))
	for _, candidate := range members {
		if candidate.UserID != "" && (candidate.AccountID == "" || candidate.AccountID == accountID) {
			current[candidate.UserID] = candidate.Email
		}
	}
	if _, ok := current[userID]; !ok {
		return nil, ErrForbidden
	}
	rows, err := svc.store.ListCalendarShareRows(ctx, accountID, query)
	if err != nil {
		return nil, err
	}
	if rows == nil {
		return nil, repository.ErrCalendarCorruptData
	}
	page := &model.AccountCalendarPage{Events: make([]model.AccountCalendarEvent, 0, len(rows.Groups)), NextCursor: rows.NextCursor}
	var groupOf []int
	attempted, decrypted := 0, 0
	for groupIndex, group := range rows.Groups {
		var shared []repository.CalendarShareRow
		vetoed := false
		for _, row := range group {
			if _, ok := current[row.UserID]; !ok || row.AccountID != accountID {
				continue
			}
			switch row.State {
			case calendarStateVeto:
				vetoed = true
			case calendarStateShared:
				shared = append(shared, row)
			}
		}
		if vetoed || len(shared) == 0 {
			continue
		}
		var event *model.AccountCalendarEvent
		seen := make(map[string]bool)
		sharedBy := []string{}
		sharedByMe := false
		for _, row := range shared {
			if !seen[row.UserID] {
				seen[row.UserID] = true
				if email := current[row.UserID]; email != "" && len(sharedBy) < calendarSharedByMax {
					sharedBy = append(sharedBy, email)
				}
			}
			sharedByMe = sharedByMe || row.UserID == userID
			if event == nil {
				attempted++
				if decoded, ok := svc.decryptCalendarShare(ctx, accountID, row); ok {
					decrypted++
					event = &decoded
				}
			}
		}
		if event == nil {
			continue
		}
		event.SharedBy, event.SharedByMe = sharedBy, sharedByMe
		page.Events = append(page.Events, *event)
		groupOf = append(groupOf, groupIndex)
	}
	if attempted > 0 && decrypted == 0 {
		return nil, ErrCalendarEncryption
	}
	for !calendarResultFits(page) {
		retained := len(page.Events) - 1
		if retained <= 0 || len(rows.GroupCursors) != len(rows.Groups) || rows.GroupCursors[groupOf[retained-1]] == "" {
			return nil, repository.ErrCalendarCorruptData
		}
		page.Events = page.Events[:retained]
		cursor := rows.GroupCursors[groupOf[retained-1]]
		page.NextCursor = &cursor
	}
	return page, nil
}

// decryptCalendarShare opens one shared row and checks that it still hashes to its meetingKey.
func (svc *CalendarService) decryptCalendarShare(ctx context.Context, accountID string, row repository.CalendarShareRow) (model.AccountCalendarEvent, bool) {
	plaintext, err := svc.cipher.DecryptShare(ctx, accountID, row.MeetingKey, row.UserID, row.Ciphertext)
	if err != nil {
		return model.AccountCalendarEvent{}, false
	}
	var payload calendarSharePayload
	decoder := json.NewDecoder(bytes.NewReader(plaintext))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&payload) != nil || decoder.Decode(new(any)) != io.EOF ||
		!payload.StartsAt.Equal(row.StartsAt) || !payload.EndsAt.Equal(row.EndsAt) ||
		calendarMeetingKey(accountID, payload.StartsAt, payload.EndsAt, payload.Title) != row.MeetingKey {
		return model.AccountCalendarEvent{}, false
	}
	return model.AccountCalendarEvent{
		MeetingKey: row.MeetingKey, Title: payload.Title, StartsAt: payload.StartsAt.UTC(), EndsAt: payload.EndsAt.UTC(),
		TimeZone: payload.TimeZone, IsAllDay: payload.IsAllDay, StartDate: payload.StartDate, EndDate: payload.EndDate, Location: payload.Location,
	}, true
}

// ShareDirectory lists the caller's direct-membership accounts (at most 200, by accountId)
// with the names, aliases and domains the intake matcher uses.
func (svc *CalendarService) ShareDirectory(ctx context.Context, userID string) (*model.CalendarShareDirectory, error) {
	if !calendarIdentifier.MatchString(userID) {
		return nil, ErrForbidden
	}
	directory := &model.CalendarShareDirectory{Accounts: []model.CalendarShareDirectoryAccount{}}
	if svc.membership == nil {
		return directory, nil
	}
	memberships, err := svc.membership.ListAccountsForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	var accountIDs []string
	for _, membership := range memberships {
		if membership.UserID != userID || !calendarIdentifier.MatchString(membership.AccountID) || seen[membership.AccountID] {
			continue
		}
		seen[membership.AccountID] = true
		accountIDs = append(accountIDs, membership.AccountID)
	}
	sort.Strings(accountIDs)
	for _, accountID := range accountIDs {
		if len(directory.Accounts) == calendarShareDirectoryMax {
			break
		}
		account, err := svc.membership.GetAccount(ctx, accountID)
		if err != nil {
			return nil, err
		}
		if account == nil || account.AccountID != "" && account.AccountID != accountID {
			continue
		}
		directory.Accounts = append(directory.Accounts, model.CalendarShareDirectoryAccount{
			AccountID: accountID, Name: account.Name,
			Aliases: append([]string{}, account.Aliases...), Domains: append([]string{}, account.Domains...),
		})
	}
	return directory, nil
}
