package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"sort"
	"strconv"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

const maxReadableAccounts = 1000

type accountReadUserListRepo interface {
	ListAccountsForUser(context.Context, string) ([]model.AccountMember, error)
}
type accountReadNavigationRepo interface {
	QueryNavigationCandidates(context.Context, string, string, string, string, int) (*repository.NavigationCandidatePage, error)
}
type accountReadChildrenRepo interface {
	ListChildAccountIDsPage(context.Context, string, string, int32) ([]string, *string, error)
}

// listReadableAccountIDs validates discovery rows against live canonical grants.
// The compatibility collection has an explicit 1,000-account limit; oversized
// trees fail instead of presenting a silently incomplete collection. Hints are
// additional candidate roots and never grant access.
func listReadableAccountIDs(ctx context.Context, repo accountReadMembershipRepo, userID string, hints []string) ([]string, error) {
	candidates := append([]string{}, hints...)
	if nav, ok := repo.(accountReadNavigationRepo); ok {
		after := ""
		for pages := 0; ; pages++ {
			if pages >= 10000 {
				return nil, ErrReadingData
			}
			page, err := nav.QueryNavigationCandidates(ctx, userID, "accounts", "", after, 1)
			if err != nil {
				return nil, err
			}
			if page == nil || len(page.IDs) > 1 {
				return nil, ErrReadingData
			}
			for _, id := range page.IDs {
				if !readingID.MatchString(id) || id <= after {
					return nil, ErrReadingData
				}
				candidates = append(candidates, id)
			}
			if page.NextID == "" {
				break
			}
			if !readingID.MatchString(page.NextID) || page.NextID <= after {
				return nil, ErrReadingData
			}
			after = page.NextID
		}
	} else if userList, ok := repo.(accountReadUserListRepo); ok {
		members, err := userList.ListAccountsForUser(ctx, userID)
		if err != nil {
			return nil, err
		}
		for _, m := range members {
			if m.UserID == userID {
				candidates = append(candidates, m.AccountID)
			}
		}
	}
	seen := map[string]bool{}
	grants := map[string]*accountReadGrant{}
	queue := []string{}
	add := func(id string) error {
		if !readingID.MatchString(id) {
			return ErrReadingData
		}
		if seen[id] {
			return nil
		}
		seen[id] = true
		if len(seen) > 10000 {
			return ErrReadingData
		}
		g, err := resolveAccountReadGrant(ctx, repo, id, userID)
		if err != nil {
			return err
		}
		if g == nil {
			return nil
		}
		if hierarchy, ok := repo.(accountReadHierarchyRepo); ok {
			a, err := hierarchy.GetAccount(ctx, id)
			if err != nil {
				return err
			}
			if a == nil {
				return nil
			}
			if a.AccountID != id {
				return ErrReadingData
			}
		}
		if len(grants) >= maxReadableAccounts {
			return ErrReadingData
		}
		grants[id] = g
		queue = append(queue, id)
		return nil
	}
	for _, id := range candidates {
		if err := add(id); err != nil {
			return nil, err
		}
	}
	children, canDiscover := repo.(accountReadChildrenRepo)
	hierarchy, canValidate := repo.(accountReadHierarchyRepo)
	if descendantReadsEnabled() && canDiscover && canValidate {
		for pos := 0; pos < len(queue); pos++ {
			parent := queue[pos]
			cursor := ""
			cursors := map[string]bool{}
			for pages := 0; ; pages++ {
				if pages >= 10000 {
					return nil, ErrReadingData
				}
				ids, next, err := children.ListChildAccountIDsPage(ctx, parent, cursor, 100)
				if err != nil {
					return nil, err
				}
				if len(ids) > 100 {
					return nil, ErrReadingData
				}
				for _, id := range ids {
					if !readingID.MatchString(id) {
						return nil, ErrReadingData
					}
					a, err := hierarchy.GetAccount(ctx, id)
					if err != nil {
						return nil, err
					}
					if !canonicalReadAccount(a, id) || a.ParentAccountID != parent {
						continue
					}
					if err := add(id); err != nil {
						return nil, err
					}
				}
				if next == nil {
					break
				}
				if *next == "" || len(*next) > 2048 || *next == cursor || cursors[*next] {
					return nil, ErrReadingData
				}
				cursors[*next] = true
				cursor = *next
			}
		}
	}
	ids := make([]string, 0, len(grants))
	for id, g := range grants {
		valid, err := revalidateAccountReadGrant(ctx, repo, g, userID)
		if err != nil {
			return nil, err
		}
		if !valid {
			return nil, ErrStaleReadingCursor
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

type AccountListPage struct {
	Accounts   []model.AccountSummary `json:"accounts"`
	NextCursor string                 `json:"nextCursor,omitempty"`
}

type accountListCursor struct {
	Version int    `json:"v"`
	Binding string `json:"b"`
	After   string `json:"a"`
}

func (s *AccountService) ListAccountsPage(ctx context.Context, user string, q url.Values) (*AccountListPage, error) {
	size := 50
	rawCursor := ""
	for key, values := range q {
		if len(values) != 1 {
			return nil, ErrInvalidReadingOptions
		}
		switch key {
		case "pageSize":
			n, err := strconv.Atoi(values[0])
			if err != nil || n < 1 || n > 100 {
				return nil, ErrInvalidReadingOptions
			}
			size = n
		case "cursor":
			rawCursor = values[0]
			if rawCursor == "" || len(rawCursor) > 2048 {
				return nil, ErrInvalidReadingCursor
			}
		default:
			return nil, ErrInvalidReadingOptions
		}
	}
	accounts, err := s.ListAccounts(ctx, user)
	if err != nil {
		return nil, err
	}
	fingerprints := []string{}
	for _, account := range accounts {
		g, err := s.requireReadGrant(ctx, user, account.AccountID)
		if err != nil {
			return nil, err
		}
		fingerprints = append(fingerprints, g.fingerprint())
	}
	binding := readingHash([]interface{}{"account-list-v1", user, accounts, fingerprints})
	cursor := accountListCursor{Version: 1, Binding: binding}
	if rawCursor != "" {
		data, err := base64.RawURLEncoding.Strict().DecodeString(rawCursor)
		if err != nil || json.Unmarshal(data, &cursor) != nil || cursor.Version != 1 || !readingID.MatchString(cursor.After) {
			return nil, ErrInvalidReadingCursor
		}
		canonical, _ := json.Marshal(cursor)
		if !bytes.Equal(data, canonical) {
			return nil, ErrInvalidReadingCursor
		}
		if cursor.Binding != binding {
			return nil, ErrStaleReadingCursor
		}
	}
	start := sort.Search(len(accounts), func(i int) bool { return accounts[i].AccountID > cursor.After })
	end := start + size
	if end > len(accounts) {
		end = len(accounts)
	}
	page := &AccountListPage{Accounts: accounts[start:end]}
	if end < len(accounts) {
		cursor.After = accounts[end-1].AccountID
		data, _ := json.Marshal(cursor)
		page.NextCursor = base64.RawURLEncoding.EncodeToString(data)
	}
	for i, account := range accounts {
		g, err := s.requireReadGrant(ctx, user, account.AccountID)
		if err != nil {
			return nil, err
		}
		if g.fingerprint() != fingerprints[i] {
			return nil, ErrStaleReadingCursor
		}
	}
	return page, nil
}

// queryReadableAccountCandidates preserves narrow legacy catalog repositories
// while using the same canonical descendant discovery when the rollout is on.
func queryReadableAccountCandidates(ctx context.Context, repo accountReadMembershipRepo, user, after string, limit int) (*repository.NavigationCandidatePage, error) {
	if !descendantReadsEnabled() {
		nav, ok := repo.(accountReadNavigationRepo)
		if !ok {
			return nil, ErrReadingData
		}
		return nav.QueryNavigationCandidates(ctx, user, "accounts", "", after, limit)
	}
	ids, err := listReadableAccountIDs(ctx, repo, user, nil)
	if err != nil {
		return nil, err
	}
	start := sort.SearchStrings(ids, after)
	for start < len(ids) && ids[start] <= after {
		start++
	}
	end := start + limit
	if end > len(ids) {
		end = len(ids)
	}
	page := &repository.NavigationCandidatePage{IDs: ids[start:end]}
	if end < len(ids) && end > start {
		page.NextID = ids[end-1]
	}
	return page, nil
}
