package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

const teamMeetingCursorPrefix = "team:"

// Team references form a second paginated stream after owned/individual
// shares. The cursor holds discovery state, never an authorization grant:
// every account is checked against current membership on each request.
type teamMeetingCursor struct {
	UserID        string   `json:"user"`
	Tab           string   `json:"tab"`
	AccountID     string   `json:"account,omitempty"`
	Accounts      []string `json:"accounts"`
	Index         int      `json:"index"`
	RefCursor     string   `json:"refs,omitempty"`
	RegularCursor string   `json:"regular,omitempty"`
}

func encodeTeamMeetingCursor(cursor teamMeetingCursor) (*string, error) {
	data, err := json.Marshal(cursor)
	if err != nil {
		return nil, fmt.Errorf("encode team meeting cursor: %w", err)
	}
	encoded := teamMeetingCursorPrefix + base64.RawURLEncoding.EncodeToString(data)
	return &encoded, nil
}

func decodeTeamMeetingCursor(encoded, userID, tab, accountID string) (teamMeetingCursor, error) {
	var cursor teamMeetingCursor
	data, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(encoded, teamMeetingCursorPrefix))
	if err != nil {
		return cursor, ErrInvalidInput
	}
	if err := json.Unmarshal(data, &cursor); err != nil {
		return cursor, ErrInvalidInput
	}
	if cursor.UserID != userID || cursor.Tab != tab || cursor.AccountID != accountID || cursor.Index < 0 || cursor.Index > len(cursor.Accounts) {
		return cursor, ErrInvalidInput
	}
	return cursor, nil
}

func (s *MeetingService) newTeamMeetingCursor(ctx context.Context, userID, tab, accountID string, joinedAccountIDs []string) (teamMeetingCursor, error) {
	cursor := teamMeetingCursor{UserID: userID, Tab: tab, AccountID: accountID}
	if accountID != "" {
		cursor.Accounts = []string{accountID}
		return cursor, nil
	}
	memberships, err := s.repo.ListAccountsForUser(ctx, userID)
	if err != nil {
		return cursor, fmt.Errorf("list team memberships: %w", err)
	}
	ids := make(map[string]bool)
	for _, membership := range memberships {
		ids[membership.AccountID] = true
	}
	// The handler just materialized these memberships in this request.
	// Include them even while the reverse GSI has not caught up.
	for _, id := range joinedAccountIDs {
		ids[id] = true
	}
	delete(ids, "")
	for id := range ids {
		cursor.Accounts = append(cursor.Accounts, id)
	}
	sort.Strings(cursor.Accounts)
	return cursor, nil
}

// listTeamSharedMeetings fills at most limit remaining slots using bounded
// reference pages. Existing individual shares are left to the regular stream,
// preserving direct edit permissions and avoiding duplicate team grants.
func (s *MeetingService) listTeamSharedMeetings(ctx context.Context, userID, tab, accountID string, limit int32, encodedCursor string, joinedAccountIDs []string) (*model.MeetingListResponse, error) {
	var cursor teamMeetingCursor
	var err error
	if encodedCursor == "" {
		cursor, err = s.newTeamMeetingCursor(ctx, userID, tab, accountID, joinedAccountIDs)
	} else {
		cursor, err = decodeTeamMeetingCursor(encodedCursor, userID, tab, accountID)
	}
	if err != nil {
		return nil, err
	}
	response := &model.MeetingListResponse{Meetings: []model.MeetingListItem{}}
	listed := make(map[string]bool)
	const maxRefPages = 25
	for page := 0; page < maxRefPages && cursor.Index < len(cursor.Accounts) && int32(len(response.Meetings)) < limit; page++ {
		id := cursor.Accounts[cursor.Index]
		if id == "" || (accountID != "" && id != accountID) {
			cursor.Index++
			cursor.RefCursor = ""
			continue
		}
		member, err := s.repo.GetMember(ctx, id, userID)
		if err != nil {
			return nil, fmt.Errorf("check team membership: %w", err)
		}
		if member == nil {
			cursor.Index++
			cursor.RefCursor = ""
			continue
		}
		refs, next, err := s.repo.ListMeetingRefsForAccountPage(ctx, id, cursor.RefCursor, limit-int32(len(response.Meetings)))
		if err != nil {
			return nil, fmt.Errorf("list team meeting references: %w", err)
		}
		if next == nil {
			cursor.Index++
			cursor.RefCursor = ""
		} else {
			cursor.RefCursor = *next
		}
		keys := make([]repository.MeetingKey, 0, len(refs))
		seenKeys := make(map[repository.MeetingKey]bool)
		for _, ref := range refs {
			if listed[ref.MeetingID] || ref.MeetingID == "" || ref.OwnerUserID == "" {
				continue
			}
			key := repository.MeetingKey{OwnerID: ref.OwnerUserID, MeetingID: ref.MeetingID}
			if !seenKeys[key] {
				keys = append(keys, key)
				seenKeys[key] = true
			}
		}
		if len(keys) == 0 {
			continue
		}
		meetings, err := s.repo.BatchGetMeetings(ctx, keys)
		if err != nil {
			return nil, fmt.Errorf("load team meetings: %w", err)
		}
		byID := make(map[string]*model.Meeting, len(meetings))
		for _, meeting := range meetings {
			if meeting != nil {
				byID[meeting.MeetingID] = meeting
			}
		}
		for _, key := range keys {
			meeting := byID[key.MeetingID]
			// A reference is an index, not a grant or trusted title.
			if meeting == nil || listed[meeting.MeetingID] || meeting.UserID == userID || !meeting.SharedToAccount || meeting.AccountID != id {
				continue
			}
			share, err := s.repo.GetShare(ctx, userID, meeting.MeetingID)
			if err != nil {
				return nil, fmt.Errorf("resolve existing meeting share: %w", err)
			}
			if share != nil {
				continue
			} // covered by the owned/individual-share stream
			permission := model.PermissionRead
			response.Meetings = append(response.Meetings, model.ToMeetingListItem(meeting, true, nil, &permission))
			listed[meeting.MeetingID] = true
		}
	}
	if cursor.Index < len(cursor.Accounts) {
		response.NextCursor, err = encodeTeamMeetingCursor(cursor)
		if err != nil {
			return nil, err
		}
	}
	return response, nil
}
