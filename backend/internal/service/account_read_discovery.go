package service

import (
	"context"
	"sort"

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
