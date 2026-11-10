package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"

	"github.com/ttobak/backend/internal/model"
)

const directAccountPageCandidates = 25

// directAccountListRow deliberately uses exact membership rather than the
// inherited read resolver: recording preparation requires direct edit access.
// The navigation index is discovery only; current canonical rows grant access.
func directAccountListRow(ctx context.Context, reads accountReads, user, id string) (*model.AccountSummary, string, error) {
	member, err := reads.GetMember(ctx, id, user)
	if err != nil {
		return nil, "", err
	}
	grant := &accountReadGrant{AccountID: id, Member: member}
	if !validAccountReadIdentity(grant, id, user) {
		return nil, "", nil
	}
	account, err := reads.GetAccount(ctx, id)
	if err != nil {
		return nil, "", err
	}
	if account == nil {
		return nil, "", nil
	}
	if account.AccountID != id {
		return nil, "", ErrReadingData
	}
	// A merged source is a tombstone: hide it like the unpaged list does.
	if account.MergedInto != "" {
		return nil, "", nil
	}
	row := &model.AccountSummary{
		AccountID: id, ParentAccountID: account.ParentAccountID, Name: account.Name,
		Role: member.Role, AccessPermission: model.PermissionEdit, MergingInto: account.MergingInto,
	}
	return row, readingHash([]interface{}{account, grant.fingerprint()}), nil
}

func (s *AccountService) listDirectAccountsPage(ctx context.Context, user string, size int, rawCursor string) (*AccountListPage, error) {
	binding := readingHash([]interface{}{"account-direct-list-v1", user, size})
	c := accountListCursor{Version: 3, Binding: binding}
	if rawCursor != "" {
		data, err := base64.RawURLEncoding.Strict().DecodeString(rawCursor)
		if err != nil || json.Unmarshal(data, &c) != nil || c.Version != 3 || c.Discovery != nil ||
			!readingID.MatchString(c.After) || (c.Anchor != "" && !readingID.MatchString(c.Anchor)) ||
			(c.Anchor == "" && c.Revision != "") {
			return nil, ErrInvalidReadingCursor
		}
		canonical, _ := json.Marshal(c)
		if !bytes.Equal(data, canonical) {
			return nil, ErrInvalidReadingCursor
		}
		if c.Binding != binding {
			return nil, ErrStaleReadingCursor
		}
	}
	// Each phase reads its rows through a fresh view primed by one batch read
	// (exact memberships, no ancestry) instead of two GetItems per row.
	var reads accountReads = s.repo
	view := func(ids ...string) error {
		var err error
		reads, err = primedAccountReads(ctx, s.repo, user, ids, false)
		return err
	}
	recheck := func(id, revision string) error {
		if id == "" {
			return nil
		}
		row, current, err := directAccountListRow(ctx, reads, user, id)
		if err != nil {
			return err
		}
		if row == nil || current != revision {
			return ErrStaleReadingCursor
		}
		return nil
	}
	if c.Anchor != "" {
		if err := view(c.Anchor); err != nil {
			return nil, err
		}
	}
	if err := recheck(c.Anchor, c.Revision); err != nil {
		return nil, err
	}
	nav, ok := s.repo.(accountReadNavigationRepo)
	if !ok {
		return nil, ErrReadingData
	}
	limit := min(size, directAccountPageCandidates)
	candidates, err := nav.QueryNavigationCandidates(ctx, user, "accounts", "", c.After, limit)
	if err != nil {
		return nil, err
	}
	if candidates == nil || len(candidates.IDs) > limit {
		return nil, ErrReadingData
	}
	previous := c.After
	primed := []string{}
	for _, id := range candidates.IDs {
		if !readingID.MatchString(id) || id <= previous {
			break
		}
		previous = id
		primed = append(primed, id)
	}
	if err := view(primed...); err != nil {
		return nil, err
	}
	previous = c.After
	page := &AccountListPage{Accounts: []model.AccountSummary{}}
	observed := [][2]string{}
	for _, id := range candidates.IDs {
		if !readingID.MatchString(id) || id <= previous {
			return nil, ErrReadingData
		}
		previous = id
		row, revision, err := directAccountListRow(ctx, reads, user, id)
		if err != nil {
			return nil, err
		}
		if row == nil {
			continue
		}
		page.Accounts = append(page.Accounts, *row)
		observed = append(observed, [2]string{id, revision})
	}
	if next := candidates.NextID; next != "" &&
		(!readingID.MatchString(next) || next <= c.After || next < previous) {
		return nil, ErrReadingData
	}
	rechecked := make([]string, 0, len(observed)+1)
	for _, receipt := range observed {
		rechecked = append(rechecked, receipt[0])
	}
	if len(observed) == 0 && c.Anchor != "" {
		rechecked = append(rechecked, c.Anchor)
	}
	if len(rechecked) != 0 {
		if err := view(rechecked...); err != nil {
			return nil, err
		}
	}
	for _, receipt := range observed {
		if err := recheck(receipt[0], receipt[1]); err != nil {
			return nil, err
		}
	}
	// Empty/stale index pages still bind the last visible account's revision.
	if len(observed) == 0 {
		if err := recheck(c.Anchor, c.Revision); err != nil {
			return nil, err
		}
	} else {
		last := observed[len(observed)-1]
		c.Anchor, c.Revision = last[0], last[1]
	}
	if candidates.NextID != "" {
		c.After = candidates.NextID
		raw, _ := json.Marshal(c)
		page.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
		if len(page.NextCursor) > 2048 {
			return nil, ErrReadingData
		}
	}
	return page, nil
}
