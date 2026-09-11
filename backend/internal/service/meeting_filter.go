package service

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

type meetingAccountFilter struct {
	AccountID  string
	AccountIDs []string
}

func (f meetingAccountFilter) matches(id string) bool {
	if f.AccountID != "" {
		return f.AccountID == id
	}
	return len(f.AccountIDs) == 0 || slices.Contains(f.AccountIDs, id)
}

// ParseMeetingAccountIDs normalizes the public comma-separated OR selection.
func ParseMeetingAccountIDs(raw string) ([]string, error) {
	var ids []string
	if strings.TrimSpace(raw) != "" {
		ids = strings.Split(raw, ",")
	}
	normalized, err := repository.NormalizeMeetingAccountIDs(ids)
	if err != nil {
		return nil, ErrInvalidInput
	}
	return normalized, nil
}

const meetingFilterCursorPrefix = "meetings:v1:"

// The envelope binds all stages, including empty pages and discovery hints, to
// the same selection. It is pagination state, not a grant: membership and
// canonical meeting account IDs must still be checked after decoding.
type meetingFilterCursor struct {
	UserID     string   `json:"user"`
	Tab        string   `json:"tab"`
	AccountIDs []string `json:"-"` // restored from the normalized request
	Selection  string   `json:"selection"`
	Stage      string   `json:"stage"`
	Regular    string   `json:"regular,omitempty"`
	Team       string   `json:"team,omitempty"`
	Joined     []string `json:"joined,omitempty"`
}

func decodeMeetingFilterCursor(encoded, userID, tab string, ids []string) (meetingFilterCursor, error) {
	var c meetingFilterCursor
	if !strings.HasPrefix(encoded, meetingFilterCursorPrefix) || len(encoded) > 256*1024 {
		return c, ErrInvalidInput
	}
	b, err := base64.RawURLEncoding.Strict().DecodeString(strings.TrimPrefix(encoded, meetingFilterCursorPrefix))
	if err != nil {
		return c, ErrInvalidInput
	}
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return c, ErrInvalidInput
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return c, ErrInvalidInput
	}
	if c.UserID != userID || c.Tab != tab || c.Selection != meetingSelectionDigest(ids) {
		return c, ErrInvalidInput
	}
	c.AccountIDs = ids
	for _, id := range c.Joined {
		normalized, err := repository.NormalizeMeetingAccountIDs([]string{id})
		if err != nil || normalized[0] != id {
			return c, ErrInvalidInput
		}
	}
	switch c.Stage {
	case "owned", "shared":
		if c.Stage == "owned" && tab != "all" || c.Team != "" {
			return c, ErrInvalidInput
		}
		if err := repository.ValidateMeetingListCursor(c.Regular, userID, c.Stage); err != nil {
			return c, ErrInvalidInput
		}
	case "team":
		if c.Regular != "" || c.Team == "" || len(c.Joined) != 0 {
			return c, ErrInvalidInput
		}
		if _, err := decodeTeamMeetingCursorForFilter(c.Team, userID, tab, meetingAccountFilter{AccountIDs: ids}); err != nil {
			return c, err
		}
	default:
		return c, ErrInvalidInput
	}
	return c, nil
}

func encodeMeetingFilterCursor(c meetingFilterCursor) (*string, error) {
	c.Selection = meetingSelectionDigest(c.AccountIDs)
	b, err := json.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("encode meeting filter cursor: %w", err)
	}
	encoded := meetingFilterCursorPrefix + base64.RawURLEncoding.EncodeToString(b)
	return &encoded, nil
}

// IDs are normalized and cannot contain NUL, making this delimiter unambiguous.
// Hashing the selection avoids repeating up to 100 UUIDs in a request URL.
func meetingSelectionDigest(ids []string) string {
	sum := sha256.Sum256([]byte(strings.Join(ids, "\x00")))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// ListMeetingsForAccounts is the multi-filter entry point. A nil/empty selection
// means all accounts but still uses context-bound cursors. The legacy public
// ListMeetings signature and its established pagination format remain available.
func (s *MeetingService) ListMeetingsForAccounts(ctx context.Context, userID, tab, encoded string, accountIDs []string, limit int32, joinedAccountIDs ...string) (*model.MeetingListResponse, error) {
	ids, err := repository.NormalizeMeetingAccountIDs(accountIDs)
	if err != nil {
		return nil, ErrInvalidInput
	}
	if tab == "" {
		tab = "all"
	}
	if tab != "all" && tab != "shared" {
		return nil, ErrInvalidInput
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	c := meetingFilterCursor{UserID: userID, Tab: tab, AccountIDs: ids, Stage: "owned"}
	if tab == "shared" {
		c.Stage = "shared"
	}
	if encoded != "" {
		c, err = decodeMeetingFilterCursor(encoded, userID, tab, ids)
		if err != nil {
			return nil, err
		}
	}
	// Explicit selections already supply team discovery candidates. Retaining
	// duplicate first-login hints can push the continuation URL over its budget.
	// Unfiltered discovery still needs hints while the membership GSI catches up.
	if len(ids) > 0 {
		c.Joined = nil
	} else if c.Stage != "team" {
		c.Joined = mergeMeetingDiscoveryHints(c.Joined, joinedAccountIDs)
	}
	filter := meetingAccountFilter{AccountIDs: ids}
	response := &model.MeetingListResponse{Meetings: []model.MeetingListItem{}}
	const maxRegularPages = 25
	for page := 0; c.Stage != "team" && page < maxRegularPages && int32(len(response.Meetings)) < limit; page++ {
		queryTab := "all"
		if c.Stage == "shared" {
			queryTab = "shared"
		}
		part, err := s.listMeetingsFilteredPage(ctx, repository.ListMeetingsParams{
			UserID: userID, Tab: queryTab, Cursor: c.Regular, AccountIDs: ids,
			Limit: limit - int32(len(response.Meetings)),
		})
		if err != nil {
			return nil, err
		}
		response.Meetings = append(response.Meetings, part.Meetings...)
		if part.NextCursor != nil {
			c.Regular = *part.NextCursor
			// The owned repository already scans up to 25 sparse GSI pages.
			// Do not multiply that work bound in the service.
			if c.Stage == "owned" {
				break
			}
		} else {
			c.Regular = ""
			if c.Stage == "owned" {
				c.Stage = "shared"
			} else {
				c.Stage = "team"
			}
		}
	}
	if c.Stage == "team" {
		team, err := s.listTeamSharedMeetingsForFilter(ctx, userID, tab, filter, limit-int32(len(response.Meetings)), c.Team, c.Joined)
		if err != nil {
			return nil, err
		}
		response.Meetings = append(response.Meetings, team.Meetings...)
		if team.NextCursor == nil {
			return response, nil
		}
		c.Team, c.Joined = *team.NextCursor, nil
	}
	response.NextCursor, err = encodeMeetingFilterCursor(c)
	return response, err
}

func mergeMeetingDiscoveryHints(existing, joined []string) []string {
	ids := make(map[string]bool, len(existing)+len(joined))
	for _, candidates := range [][]string{existing, joined} {
		for _, id := range candidates {
			if normalized, err := repository.NormalizeMeetingAccountIDs([]string{id}); err == nil && normalized[0] == id {
				ids[id] = true
			}
		}
	}
	out := make([]string, 0, len(ids))
	for id := range ids {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}
