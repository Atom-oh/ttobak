package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

type MeetingCatalogRepository interface {
	GetShare(context.Context, string, string) (*model.Share, error)
	GetMember(context.Context, string, string) (*model.AccountMember, error)
	QueryMeetingCatalog(context.Context, string, string, string, string, int) (*repository.MeetingCatalogPage, error)
	QueryNavigationCandidates(context.Context, string, string, string, string, int) (*repository.NavigationCandidatePage, error)
	GetMeetingCatalogMetadata(context.Context, string, string) (*model.Meeting, error)
}
type MeetingCatalogService struct{ repo MeetingCatalogRepository }

func NewMeetingCatalogService(repo MeetingCatalogRepository) *MeetingCatalogService {
	return &MeetingCatalogService{repo}
}

type MeetingCatalogIdentity struct {
	UserID, Email, Name string
	EmailVerified       bool
}
type meetingCatalogOptions struct {
	tab, cursor     string
	limit, pageSize int
	accounts        []string
}
type meetingCatalogAnchor struct {
	Owner    string `json:"o"`
	ID       string `json:"i"`
	Via      string `json:"v"`
	Account  string `json:"a"`
	Revision string `json:"r"`
}
type meetingCatalogCursor struct {
	Version  int                  `json:"v"`
	Binding  string               `json:"b"`
	Phase    string               `json:"p"`
	After    string               `json:"k"`
	Offset   int                  `json:"o"`
	Revision string               `json:"r"`
	Account  string               `json:"a"`
	Grant    string               `json:"g"`
	NavAfter string               `json:"n"`
	NavDone  bool                 `json:"d"`
	Index    int                  `json:"i"`
	Anchor   meetingCatalogAnchor `json:"h"`
}

func parseMeetingCatalog(q url.Values) (meetingCatalogOptions, error) {
	o := meetingCatalogOptions{tab: "all", limit: 20, pageSize: 4000, accounts: []string{}}
	for key, values := range q {
		if len(values) != 1 {
			return o, ErrInvalidReadingOptions
		}
		value := values[0]
		switch key {
		case "tab":
			o.tab = value
		case "cursor":
			if value == "" || len(value) > 2048 {
				return o, ErrInvalidReadingCursor
			}
			o.cursor = value
		case "limit", "pageSize":
			n, err := strconv.Atoi(value)
			max := 20
			if key == "pageSize" {
				max = 8000
			}
			if err != nil || n < 1 || n > max {
				return o, ErrInvalidReadingOptions
			}
			if key == "limit" {
				o.limit = n
			} else {
				o.pageSize = n
			}
		case "accountIds":
			ids, err := ParseMeetingAccountIDs(value)
			if err != nil || len(ids) == 0 {
				return o, ErrInvalidReadingOptions
			}
			o.accounts = ids
		default:
			return o, ErrInvalidReadingOptions
		}
	}
	if o.tab != "all" && o.tab != "shared" {
		return o, ErrInvalidReadingOptions
	}
	return o, nil
}
func encodeMeetingCatalogCursor(c meetingCatalogCursor) (string, error) {
	raw, _ := json.Marshal(c)
	encoded := base64.RawURLEncoding.EncodeToString(raw)
	if len(encoded) > 2048 {
		return "", ErrReadingData
	}
	return encoded, nil
}
func regularMeetingPhase(tab string) string {
	if tab == "shared" {
		return "direct"
	}
	return "owned"
}
func (s *MeetingCatalogService) member(ctx context.Context, user, account string) (*model.AccountMember, error) {
	m, err := s.repo.GetMember(ctx, account, user)
	if err != nil {
		return nil, err
	}
	if m == nil {
		return nil, nil
	}
	if m.AccountID != account || m.UserID != user {
		return nil, ErrReadingData
	}
	return m, nil
}
func (s *MeetingCatalogService) item(ctx context.Context, user, tab, phase, account string, row repository.MeetingCatalogCandidate) (*model.MeetingListItem, string, string, error) {
	var share *model.Share
	var accountMember *model.AccountMember
	var err error
	owner := row.OwnerID
	if phase == "direct" {
		share, err = s.repo.GetShare(ctx, user, row.MeetingID)
		if err != nil {
			return nil, "", "", err
		}
		if share == nil {
			return nil, "", "", nil
		}
		if share.MeetingID != row.MeetingID || share.SharedToID != user || !readingID.MatchString(share.OwnerID) {
			return nil, "", "", ErrReadingData
		}
		owner = share.OwnerID
	}
	if !readingID.MatchString(owner) || !readingID.MatchString(row.MeetingID) {
		return nil, "", "", ErrReadingData
	}
	m, err := s.repo.GetMeetingCatalogMetadata(ctx, owner, row.MeetingID)
	if err != nil {
		return nil, "", "", err
	}
	if m == nil {
		return nil, "", "", nil
	}
	if m.UserID != owner || m.MeetingID != row.MeetingID {
		return nil, "", "", ErrReadingData
	}
	permission := model.PermissionRead
	var sharedBy *string
	grant := readingHash([]string{"owner", user})
	if phase == "owned" {
		if owner != user {
			return nil, "", "", ErrReadingData
		}
	} else if phase == "team" {
		if !m.SharedToAccount || m.AccountID != account {
			return nil, "", "", nil
		}
		member, err := s.member(ctx, user, account)
		if err != nil {
			return nil, "", "", err
		}
		if member == nil {
			return nil, "", "", nil
		}
		grant = accountReadingGrant(member)
		accountMember = member
		// Prefer a present direct/edit share without suppressing this actual emitted
		// path merely because another index might eventually contain the meeting.
		share, err = s.repo.GetShare(ctx, user, m.MeetingID)
		if err != nil {
			return nil, "", "", err
		}
		if share != nil && (share.OwnerID != owner || share.MeetingID != m.MeetingID || share.SharedToID != user) {
			return nil, "", "", ErrReadingData
		}
	}
	if share != nil {
		if share.Origin == model.ShareOriginAccount {
			if !m.SharedToAccount || m.AccountID == "" {
				return nil, "", "", nil
			}
			member, err := s.member(ctx, user, m.AccountID)
			if err != nil {
				return nil, "", "", err
			}
			if member == nil {
				return nil, "", "", nil
			}
			grant = readingHash([]interface{}{share, accountReadingGrant(member)})
			accountMember = member
		} else {
			grant = readingHash(share)
		}
		permission = share.Permission
		sharedBy = &share.OwnerEmail
	}
	if owner == user && tab == "shared" {
		return nil, "", "", nil
	}
	if owner != user && accountMember != nil && (share == nil || share.Origin == model.ShareOriginAccount) {
		m = projectAccountMeeting(m, accountMember)
	}
	item := model.ToMeetingListItem(m, owner != user, sharedBy, &permission)
	if owner == user {
		item.Permission = nil
		item.SharedBy = nil
	}
	item.Summary = truncateRunes(m.Content, 200)
	if utf8.RuneCountInString(m.Content) > 200 {
		item.Summary += "..."
	}
	return &item, owner, readingHash([]interface{}{item, grant}), nil
}
func (s *MeetingCatalogService) verifyAnchor(ctx context.Context, id MeetingCatalogIdentity, o meetingCatalogOptions, a meetingCatalogAnchor) error {
	if a.ID == "" {
		if a != (meetingCatalogAnchor{}) {
			return ErrInvalidReadingCursor
		}
		return nil
	}
	if !readingID.MatchString(a.ID) || !readingID.MatchString(a.Owner) || (a.Via != "owned" && a.Via != "direct" && a.Via != "team") || (a.Via == "team" && !readingID.MatchString(a.Account)) {
		return ErrInvalidReadingCursor
	}
	item, _, revision, err := s.item(ctx, id.UserID, o.tab, a.Via, a.Account, repository.MeetingCatalogCandidate{MeetingID: a.ID, OwnerID: a.Owner})
	if err != nil {
		return err
	}
	if item == nil || revision != a.Revision {
		return ErrStaleReadingCursor
	}
	return nil
}
func (s *MeetingCatalogService) payload(ctx context.Context, id MeetingCatalogIdentity, o meetingCatalogOptions, c meetingCatalogCursor, proof *string) (string, string, meetingCatalogAnchor, error) {
	page, err := s.repo.QueryMeetingCatalog(ctx, id.UserID, c.Phase, c.Account, c.After, o.limit)
	if errors.Is(err, repository.ErrInvalidMeetingCursor) {
		return "", "", c.Anchor, ErrInvalidReadingCursor
	}
	if err != nil {
		return "", "", c.Anchor, err
	}
	if page == nil || len(page.Rows) > o.limit {
		return "", "", c.Anchor, ErrReadingData
	}
	rows := []model.MeetingListItem{}
	proofs := []string{}
	anchor := c.Anchor
	for _, candidate := range page.Rows {
		item, owner, revision, err := s.item(ctx, id.UserID, o.tab, c.Phase, c.Account, candidate)
		if err != nil {
			return "", "", anchor, err
		}
		if item == nil || !(meetingAccountFilter{AccountIDs: o.accounts}).matches(item.AccountID) {
			continue
		}
		rows = append(rows, *item)
		proofs = append(proofs, revision)
		anchor = meetingCatalogAnchor{owner, item.MeetingID, c.Phase, c.Account, revision}
	}
	raw, err := json.Marshal(rows)
	if err != nil {
		return "", "", anchor, err
	}
	next := page.NextKey
	*proof = compactReadingHash(proofs)
	return string(raw), next, anchor, nil
}
func (s *MeetingCatalogService) discovery(ctx context.Context, id MeetingCatalogIdentity, o meetingCatalogOptions, c *meetingCatalogCursor) (bool, error) {
	if c.Phase != "accounts" {
		return false, ErrInvalidReadingCursor
	}
	account := ""
	if len(o.accounts) > 0 {
		if c.Index < 0 || c.Index > len(o.accounts) {
			return false, ErrInvalidReadingCursor
		}
		if c.Index == len(o.accounts) {
			return true, nil
		}
		account = o.accounts[c.Index]
		c.Index++
		c.NavDone = c.Index == len(o.accounts)
	} else {
		page, err := s.repo.QueryNavigationCandidates(ctx, id.UserID, "accounts", "", c.NavAfter, 1)
		if err != nil {
			return false, err
		}
		if page == nil || len(page.IDs) > 1 {
			return false, ErrReadingData
		}
		c.NavAfter, c.NavDone = page.NextID, page.NextID == ""
		if len(page.IDs) > 0 {
			account = page.IDs[0]
		}
	}
	if account != "" {
		m, err := s.member(ctx, id.UserID, account)
		if err != nil {
			return false, err
		}
		if m != nil {
			c.Account, c.Grant, c.Phase = account, accountReadingGrant(m), "team"
			return false, nil
		}
	}
	return c.NavDone, nil
}
func (s *MeetingCatalogService) Read(ctx context.Context, id MeetingCatalogIdentity, q url.Values) ([]byte, error) {
	if id.UserID == "" {
		return nil, ErrForbidden
	}
	o, err := parseMeetingCatalog(q)
	if err != nil {
		return nil, err
	}
	binding := compactReadingHash([]interface{}{"meeting-catalog-v1", id.UserID, strings.ToLower(id.Email), id.EmailVerified, o.tab, o.accounts, o.limit})
	c := meetingCatalogCursor{Version: 1, Binding: binding, Phase: regularMeetingPhase(o.tab)}
	if o.cursor != "" {
		raw, err := base64.RawURLEncoding.Strict().DecodeString(o.cursor)
		if err != nil || json.Unmarshal(raw, &c) != nil || c.Version != 1 || c.Offset < 0 || c.Index < 0 {
			return nil, ErrInvalidReadingCursor
		}
		canonical, _ := json.Marshal(c)
		if !bytes.Equal(raw, canonical) || (c.Offset > 0 && c.Revision == "") {
			return nil, ErrInvalidReadingCursor
		}
		if c.Binding != binding {
			return nil, ErrStaleReadingCursor
		}
	}

	if err := s.verifyAnchor(ctx, id, o, c.Anchor); err != nil {
		return nil, err
	}
	if c.Phase == "accounts" {
		if c.Offset != 0 || c.Revision != "" || c.After != "" {
			return nil, ErrInvalidReadingCursor
		}
		complete, err := s.discovery(ctx, id, o, &c)
		if err != nil {
			return nil, err
		}
		var cursor interface{}
		if !complete {
			cursor, err = encodeMeetingCatalogCursor(c)
			if err != nil {
				return nil, err
			}
		}
		return json.Marshal(readingObject{"kind": "meeting_catalog", "phase": "discovery", "discoveryPending": true, "page": readingObject{"complete": complete, "nextCursor": cursor}})
	}
	if c.Phase != "owned" && c.Phase != "direct" && c.Phase != "team" {
		return nil, ErrInvalidReadingCursor
	}
	if c.Phase == "owned" && o.tab == "shared" {
		return nil, ErrInvalidReadingCursor
	}
	checkGrant := func() error {
		if c.Phase != "team" {
			return nil
		}
		if !readingID.MatchString(c.Account) {
			return ErrInvalidReadingCursor
		}
		m, err := s.member(ctx, id.UserID, c.Account)
		if err != nil {
			return err
		}
		if m == nil || accountReadingGrant(m) != c.Grant {
			return ErrStaleReadingCursor
		}
		return nil
	}
	if err := checkGrant(); err != nil {
		return nil, err
	}
	var proof string
	text, nextKey, anchor, err := s.payload(ctx, id, o, c, &proof)
	if err != nil {
		return nil, err
	}
	group := compactReadingHash([]interface{}{c.Phase, c.After, nextKey, text, anchor, c.Grant, proof})
	if c.Revision != "" && c.Revision != group {
		return nil, ErrStaleReadingCursor
	}
	body, err := renderCollectionReading("meeting_catalog", c.Phase, text, binding, group, c.Offset, o.pageSize, readingObject{"overlapPossible": true, "unionKey": "meetingId"}, func(done bool, end int) (string, bool, error) {
		next := c
		next.Anchor = anchor
		if !done {
			next.Offset, next.Revision = end, group
		} else {
			next.After, next.Offset, next.Revision = nextKey, 0, ""
			if nextKey == "" {
				switch c.Phase {
				case "owned":
					next.Phase = "direct"
				case "direct":
					next.Phase = "accounts"
				case "team":
					next.Account, next.Grant = "", ""
					if c.NavDone {
						return "", true, nil
					} else {
						next.Phase = "accounts"
					}
				}
			}
		}
		encoded, err := encodeMeetingCatalogCursor(next)
		return encoded, false, err
	})
	if err != nil {
		return nil, err
	}
	var freshProof string
	fresh, freshNext, freshAnchor, err := s.payload(ctx, id, o, c, &freshProof)
	if err != nil {
		return nil, err
	}
	if fresh != text || freshNext != nextKey || freshAnchor != anchor || freshProof != proof {
		return nil, ErrStaleReadingCursor
	}
	if err := checkGrant(); err != nil {
		return nil, err
	}
	if err := s.verifyAnchor(ctx, id, o, anchor); err != nil {
		return nil, err
	}
	return body, nil
}
