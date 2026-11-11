package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"sort"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

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
				if decoded, ok := svc.decryptCalendarShare(ctx, accountID, row); ok {
					event = &decoded
				}
			}
		}
		// An eligible meeting with no decryptable row (a KMS failure or a bad
		// ciphertext) fails the page closed instead of being skipped silently.
		if event == nil {
			return nil, ErrCalendarEncryption
		}
		event.SharedBy, event.SharedByMe = sharedBy, sharedByMe
		page.Events = append(page.Events, *event)
		groupOf = append(groupOf, groupIndex)
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
	lookups := newCalendarShareLookups()
	for _, accountID := range accountIDs {
		if len(directory.Accounts) == calendarShareDirectoryMax {
			break
		}
		// GSI1 is eventually consistent: revalidate the canonical membership
		// row before reading account metadata, as ListAccounts does.
		member, err := svc.directMember(ctx, lookups, accountID, userID)
		if err != nil {
			return nil, err
		}
		if member == nil {
			continue
		}
		account, err := svc.membership.GetAccount(ctx, accountID)
		if err != nil {
			return nil, err
		}
		if account == nil || account.AccountID != "" && account.AccountID != accountID || account.MergedInto != "" {
			continue
		}
		directory.Accounts = append(directory.Accounts, model.CalendarShareDirectoryAccount{
			AccountID: accountID, Name: account.Name,
			Aliases: append([]string{}, account.Aliases...), Domains: append([]string{}, account.Domains...),
		})
	}
	return directory, nil
}
