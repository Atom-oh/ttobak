package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

type AccountReadingRepository interface {
	accountRepo
	QueryAccountReading(context.Context, string, string, string) (*repository.AccountReadingPage, error)
}
type AccountReadingService struct{ repo AccountReadingRepository }

func NewAccountReadingService(repo AccountReadingRepository) *AccountReadingService {
	return &AccountReadingService{repo: repo}
}

type sectionReadingOptions struct {
	section, cursor string
	pageSize        int
	from, to        time.Time
	types           []string
}
type sectionReadingCursor struct {
	Version  int    `json:"v"`
	Binding  string `json:"b"`
	Phase    string `json:"p"`
	After    string `json:"k"`
	Offset   int    `json:"o"`
	Revision string `json:"r"`
}

func compactReadingHash(value interface{}) string {
	data, _ := json.Marshal(value)
	sum := sha256.Sum256(data)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func accountReadingGrant(member *model.AccountMember) string {
	// The privacy epoch is intentionally absent from member JSON, but it is
	// part of the authorization grant and must invalidate continuation.
	return readingHash([]interface{}{member, member.SummaryPrivacyVersion})
}

func parseSectionReading(q url.Values) (sectionReadingOptions, error) {
	return parseSectionReadingFor(q, accountReadingPhases)
}

func parseSectionReadingFor(q url.Values, phases func(string) []string) (sectionReadingOptions, error) {
	o := sectionReadingOptions{section: "detail", pageSize: 4000, types: []string{}}
	for key, values := range q {
		if len(values) != 1 {
			return o, ErrInvalidReadingOptions
		}
		value := values[0]
		switch key {
		case "section":
			o.section = value
		case "cursor":
			if value == "" || len(value) > 2048 {
				return o, ErrInvalidReadingCursor
			}
			o.cursor = value
		case "pageSize":
			n, err := strconv.Atoi(value)
			if err != nil || n < 1 || n > 8000 {
				return o, ErrInvalidReadingOptions
			}
			o.pageSize = n
		case "from", "to":
			t, err := time.Parse(time.RFC3339, value)
			if err != nil {
				return o, ErrInvalidReadingOptions
			}
			if key == "from" {
				o.from = t.UTC()
			} else {
				o.to = t.UTC()
			}
		case "types":
			seen := map[string]bool{}
			for _, kind := range strings.Split(value, ",") {
				if !model.IsValidInsightType(kind) {
					return o, ErrInvalidReadingOptions
				}
				if !seen[kind] {
					o.types = append(o.types, kind)
					seen[kind] = true
				}
			}
			sort.Strings(o.types)
		default:
			return o, ErrInvalidReadingOptions
		}
	}
	if !o.from.IsZero() && !o.to.IsZero() && o.to.Before(o.from) {
		return o, ErrInvalidReadingOptions
	}
	if len(phases(o.section)) == 0 {
		return o, ErrInvalidReadingOptions
	}
	if o.section != "brief" && o.section != "insights" &&
		(!o.from.IsZero() || !o.to.IsZero() || len(o.types) != 0) {
		return o, ErrInvalidReadingOptions
	}
	return o, nil
}

func accountReadingPhases(section string) []string {
	switch section {
	case "detail":
		return []string{"metadata", "members"}
	case "brief":
		return []string{"metadata", "members", "meetings", "insights"}
	case "metadata", "members", "meetings", "insights":
		return []string{section}
	}
	return nil
}

func decodeSectionReadingCursor(raw string) (sectionReadingCursor, error) {
	var c sectionReadingCursor
	data, err := base64.RawURLEncoding.Strict().DecodeString(raw)
	if err != nil || json.Unmarshal(data, &c) != nil || c.Version != 1 || c.Offset < 0 {
		return c, ErrInvalidReadingCursor
	}
	canonical, _ := json.Marshal(c)
	if !bytes.Equal(data, canonical) || (c.Offset > 0 && c.Revision == "") {
		return c, ErrInvalidReadingCursor
	}
	return c, nil
}

// Existing account authorization/publication rules run against this bounded
// source window. No helper can silently enumerate the full collection.
type accountReadingView struct {
	accountRepo
	accountID string
	page      *repository.AccountReadingPage
}

func (v *accountReadingView) ListAccountMembers(_ context.Context, id string) ([]model.AccountMember, error) {
	if id != v.accountID {
		return nil, ErrForbidden
	}
	return v.page.Members, nil
}
func (v *accountReadingView) ListMeetingRefsForAccount(_ context.Context, id string) ([]model.MeetingRef, error) {
	if id != v.accountID {
		return nil, ErrForbidden
	}
	return v.page.Meetings, nil
}
func (v *accountReadingView) ListInsightsForAccount(_ context.Context, id string) ([]model.AccountInsight, error) {
	if id != v.accountID {
		return nil, ErrForbidden
	}
	return v.page.Insights, nil
}

func (s *AccountReadingService) payload(ctx context.Context, user, accountID, phase, after string, o sectionReadingOptions) (string, string, error) {
	page := &repository.AccountReadingPage{}
	var err error
	if phase != "metadata" {
		page, err = s.repo.QueryAccountReading(ctx, accountID, phase, after)
		if err != nil {
			return "", "", err
		}
		if page == nil || len(page.Members)+len(page.Meetings)+len(page.Insights) > 10 {
			return "", "", ErrReadingData
		}
	}
	view := &accountReadingView{accountRepo: s.repo, accountID: accountID, page: page}
	access := &AccountService{repo: view}
	var value interface{}
	switch phase {
	case "metadata", "members":
		account, err := access.GetAccount(ctx, user, accountID)
		if err != nil {
			return "", "", err
		}
		if phase == "members" {
			value = account.Members
		} else {
			encoded, err := json.Marshal(account)
			if err != nil {
				return "", "", err
			}
			var metadata map[string]interface{}
			if json.Unmarshal(encoded, &metadata) != nil {
				return "", "", ErrReadingData
			}
			delete(metadata, "members")
			value = metadata
		}
	case "meetings":
		value, err = access.ListAccountMeetings(ctx, user, accountID)
	case "insights":
		value, err = access.ListAccountInsights(ctx, user, accountID, o.from, o.to, o.types)
	}
	if err != nil {
		return "", "", err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", "", err
	}
	return string(encoded), page.NextKey, nil
}

func (s *AccountReadingService) Read(ctx context.Context, user, accountID string, q url.Values) ([]byte, error) {
	if user == "" {
		return nil, ErrForbidden
	}
	if !readingID.MatchString(accountID) {
		return nil, ErrInvalidReadingOptions
	}
	o, err := parseSectionReading(q)
	if err != nil {
		return nil, err
	}
	member, err := s.repo.GetMember(ctx, accountID, user)
	if err != nil {
		return nil, err
	}
	if member == nil || member.AccountID != accountID || member.UserID != user ||
		member.PK != model.PrefixAccount+accountID || member.SK != model.PrefixMember+user ||
		member.EntityType != model.EntityTypeAccountMember {
		return nil, ErrForbidden
	}
	account, err := s.repo.GetAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if account == nil {
		return nil, ErrNotFound
	}
	if account.AccountID != accountID || account.PK != model.PrefixAccount+accountID ||
		account.SK != model.SKAccountMeta || account.EntityType != model.EntityTypeAccount {
		return nil, ErrReadingData
	}
	accountRevision := readingHash(account)
	grant := accountReadingGrant(member)
	phases := accountReadingPhases(o.section)
	binding := compactReadingHash([]interface{}{"account-reading-v1", user, accountID, o.section,
		o.from, o.to, o.types, grant, accountRevision})
	c := sectionReadingCursor{Version: 1, Binding: binding, Phase: phases[0]}
	if o.cursor != "" {
		c, err = decodeSectionReadingCursor(o.cursor)
		if err != nil {
			return nil, err
		}
		if c.Binding != binding {
			return nil, ErrStaleReadingCursor
		}
	}
	phaseIndex := -1
	for i, phase := range phases {
		if c.Phase == phase {
			phaseIndex = i
		}
	}
	if phaseIndex < 0 {
		return nil, ErrInvalidReadingCursor
	}
	after, err := base64.RawURLEncoding.Strict().DecodeString(c.After)
	if err != nil || !utf8.Valid(after) || len(after) > 1024 ||
		(c.Phase == "metadata" && len(after) != 0) ||
		(c.Phase != "metadata" && len(after) != 0 && !strings.HasPrefix(string(after), repository.AccountReadingPrefix(c.Phase))) {
		return nil, ErrInvalidReadingCursor
	}
	text, nextKey, err := s.payload(ctx, user, accountID, c.Phase, string(after), o)
	if err != nil {
		return nil, err
	}
	group := compactReadingHash([]interface{}{c.Phase, string(after), nextKey, text, grant})
	if c.Revision != "" && c.Revision != group {
		return nil, ErrStaleReadingCursor
	}
	innerBinding := readingHash([]string{binding, group})
	inner := ReadingOptions{PageSize: o.pageSize, MaxBytes: 10000}
	if c.Offset > 0 {
		raw, _ := json.Marshal(readingCursor{1, innerBinding, c.Offset})
		inner.Cursor = base64.RawURLEncoding.EncodeToString(raw)
	}
	revisionBytes, _ := base64.RawURLEncoding.DecodeString(group)
	revision := hex.EncodeToString(revisionBytes)
	windows := []readingWindow{{end: utf8.RuneCountInString(text)}}
	var renderError error
	body, err := paginateReading(text, windows, inner, innerBinding, func(chunks []readingChunk, fragment readingObject) readingObject {
		renderError = nil
		part := ""
		for _, chunk := range chunks {
			part += chunk.Text
		}
		next := c
		done := fragment["complete"].(bool)
		complete := done && nextKey == "" && phaseIndex == len(phases)-1
		var cursor interface{}
		if !complete {
			if !done {
				next.Offset, next.Revision = fragment["endOffset"].(int), group
			} else if nextKey != "" {
				next.After, next.Offset, next.Revision = base64.RawURLEncoding.EncodeToString([]byte(nextKey)), 0, ""
			} else {
				next.Phase, next.After, next.Offset, next.Revision = phases[phaseIndex+1], "", 0, ""
			}
			raw, _ := json.Marshal(next)
			encoded := base64.RawURLEncoding.EncodeToString(raw)
			if len(encoded) > 2048 {
				renderError = ErrReadingData
			}
			cursor = encoded
		}
		delete(fragment, "nextCursor")
		return readingObject{"kind": "account", "accountId": accountID, "section": o.section, "phase": c.Phase,
			"consistency": "current_group",
			"revision":    revision, "dataJson": part, "fragment": fragment,
			"page": readingObject{"complete": complete, "nextCursor": cursor}}
	})
	if err != nil {
		return nil, err
	}
	if renderError != nil {
		return nil, renderError
	}
	current, err := s.repo.GetMember(ctx, accountID, user)
	if err != nil {
		return nil, err
	}
	if current == nil || accountReadingGrant(current) != grant {
		return nil, ErrStaleReadingCursor
	}
	fresh, freshNext, err := s.payload(ctx, user, accountID, c.Phase, string(after), o)
	if err != nil {
		if errors.Is(err, ErrForbidden) || errors.Is(err, ErrNotFound) {
			return nil, ErrStaleReadingCursor
		}
		return nil, err
	}
	if fresh != text || freshNext != nextKey {
		return nil, ErrStaleReadingCursor
	}
	latest, err := s.repo.GetAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if latest == nil || readingHash(latest) != accountRevision {
		return nil, ErrStaleReadingCursor
	}
	current, err = s.repo.GetMember(ctx, accountID, user)
	if err != nil {
		return nil, err
	}
	if current == nil || accountReadingGrant(current) != grant {
		return nil, ErrStaleReadingCursor
	}
	return body, nil
}
