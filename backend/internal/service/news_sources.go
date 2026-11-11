package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// newsSourceSubscriptions reads the caller's per-user crawler subscriptions.
type newsSourceSubscriptions interface {
	ListUserSubscriptions(ctx context.Context, userID string) ([]model.CrawlerSubscription, error)
	GetSource(ctx context.Context, sourceID string) (*model.CrawlerSource, error)
}

// newsSourceMemberships is the membership reverse lookup (GSI1). Its rows
// are candidates only; each account is re-authorized before use.
type newsSourceMemberships interface {
	ListAccountsForUser(ctx context.Context, userID string) ([]model.AccountMember, error)
}

const (
	// maxNewsSourceAccounts and maxNewsSourceSubscriptions bound ListAll's
	// reads; anything beyond them is left out and reported as truncated.
	maxNewsSourceAccounts      = 200
	maxNewsSourceSubscriptions = 500
)

// SetNewsSourceDirectory injects the readers ListAll needs.
func (s *AccountNewsService) SetNewsSourceDirectory(subscriptions newsSourceSubscriptions, memberships newsSourceMemberships) {
	s.subscriptions, s.memberships = subscriptions, memberships
}

func newsSourceList(values []string) []string {
	return append([]string{}, values...)
}

func newsSourceItem(src *model.CrawlerSource, kind, accountID, accountName string) model.NewsSourceItem {
	return model.NewsSourceItem{
		SourceID: src.SourceID, SourceName: boundedOriginName(src.SourceName), Kind: kind,
		AccountID: accountID, AccountName: boundedOriginName(accountName),
		NewsQueries: newsSourceList(src.NewsQueries), Aliases: newsSourceList(src.Aliases),
		CustomUrls: newsSourceList(src.CustomUrls), NewsSources: newsSourceList(src.NewsSources),
		Status: src.Status, LastCrawledAt: src.LastCrawledAt, DocumentCount: src.DocumentCount,
		// Every listed source is editable: account sources by any direct
		// member, common ones through the caller's own subscription.
		CanEdit: true,
	}
}

// ListAll returns the caller's news sources: account-owned sources of every
// account the caller is a current direct member of (by account name, then
// source name), then the subscribed common sources (by name). Operator and
// account-owned sources never appear as common. Undecodable CONFIG rows are
// skipped. At most maxNewsSourceAccounts accounts and
// maxNewsSourceSubscriptions subscriptions are read.
func (s *AccountNewsService) ListAll(ctx context.Context, userID string) (*model.NewsSourcesResponse, error) {
	if s.subscriptions == nil || s.memberships == nil {
		return nil, fmt.Errorf("news source directory is unavailable")
	}
	if validateAccountHierarchyIDs(userID, "") != nil {
		return nil, ErrInvalidInput
	}
	resp := &model.NewsSourcesResponse{Sources: []model.NewsSourceItem{}}
	memberships, err := s.memberships.ListAccountsForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	accountIDs := []string{}
	for _, member := range memberships {
		id := member.AccountID
		if id == "" {
			id = strings.TrimPrefix(member.PK, model.PrefixAccount)
		}
		if id != "" && !seen[id] {
			seen[id] = true
			accountIDs = append(accountIDs, id)
		}
	}
	sort.Strings(accountIDs)
	if len(accountIDs) > maxNewsSourceAccounts {
		accountIDs, resp.Truncated = accountIDs[:maxNewsSourceAccounts], true
	}
	accountItems := []model.NewsSourceItem{}
	for _, accountID := range accountIDs {
		account, err := s.authorize(ctx, accountID, userID)
		if errors.Is(err, ErrForbidden) || errors.Is(err, ErrInvalidInput) {
			continue // stale index row: no longer a current direct member
		}
		if err != nil {
			return nil, err
		}
		sources, err := s.ownedSources(ctx, accountID, true)
		if err != nil {
			return nil, err
		}
		for i := range sources {
			accountItems = append(accountItems, newsSourceItem(&sources[i], model.NewsSourceKindAccount, accountID, account.Name))
		}
	}
	sort.SliceStable(accountItems, func(i, j int) bool {
		a, b := accountItems[i], accountItems[j]
		if a.AccountName != b.AccountName {
			return a.AccountName < b.AccountName
		}
		if a.AccountID != b.AccountID {
			return a.AccountID < b.AccountID
		}
		if a.SourceName != b.SourceName {
			return a.SourceName < b.SourceName
		}
		return a.SourceID < b.SourceID
	})

	subs, err := s.subscriptions.ListUserSubscriptions(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to list subscriptions: %w", err)
	}
	if len(subs) > maxNewsSourceSubscriptions {
		subs, resp.Truncated = subs[:maxNewsSourceSubscriptions], true
	}
	commonItems := []model.NewsSourceItem{}
	for _, sub := range subs {
		// An acct_ ID is account-owned by construction, even when its CONFIG
		// lacks accountId; a stray subscription never lists it as common.
		if sub.SourceID == "" || strings.HasPrefix(sub.SourceID, accountNewsSourcePrefix) {
			continue
		}
		src, err := s.subscriptions.GetSource(ctx, sub.SourceID)
		if errors.Is(err, repository.ErrInvalidSource) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("failed to get source: %w", err)
		}
		if src == nil || userWriteRefused(src) {
			continue
		}
		if src.SourceID == "" {
			src.SourceID = sub.SourceID
		}
		if src.SourceID != sub.SourceID {
			continue
		}
		commonItems = append(commonItems, newsSourceItem(src, model.NewsSourceKindCommon, "", ""))
	}
	sort.SliceStable(commonItems, func(i, j int) bool {
		if commonItems[i].SourceName != commonItems[j].SourceName {
			return commonItems[i].SourceName < commonItems[j].SourceName
		}
		return commonItems[i].SourceID < commonItems[j].SourceID
	})
	resp.Sources = append(append(resp.Sources, accountItems...), commonItems...)
	return resp, nil
}

// Update edits an account-owned source's name, queries or aliases. It uses
// Create's validation; the source must belong to the account by both its ref
// and its CONFIG accountId, otherwise ErrNotFound (as for Delete). The write
// is one fenced transaction (repository.UpdateAccountNewsSource).
func (s *AccountNewsService) Update(ctx context.Context, userID, accountID, sourceID string, req *model.UpdateAccountNewsSourceRequest) (*model.NewsSourceItem, error) {
	if req == nil || !validNewsSourceID(sourceID) {
		return nil, ErrInvalidInput
	}
	fields := repository.AccountNewsSourceFields{}
	if req.SourceName != nil {
		names, err := cleanNewsTerms([]string{*req.SourceName}, 1, maxAccountNewsNameRunes, true)
		if err != nil || len(names) != 1 {
			return nil, ErrInvalidInput
		}
		fields.SourceName = &names[0]
	}
	if req.NewsQueries != nil {
		queries, err := cleanNewsTerms(*req.NewsQueries, maxAccountNewsQueries, maxAccountNewsQueryRunes, true)
		if err != nil || len(queries) == 0 {
			return nil, ErrInvalidInput
		}
		fields.NewsQueries = &queries
	}
	if req.Aliases != nil {
		aliases, err := validateAliases(*req.Aliases)
		if err != nil {
			return nil, err
		}
		fields.Aliases = &aliases
	}
	if fields.SourceName == nil && fields.NewsQueries == nil && fields.Aliases == nil {
		return nil, ErrInvalidInput
	}
	account, err := s.authorize(ctx, accountID, userID)
	if err != nil {
		return nil, err
	}
	refs, err := s.repo.ListAccountNewsSources(ctx, accountID)
	if err != nil {
		return nil, err
	}
	linked := false
	for _, ref := range refs {
		linked = linked || ref.SourceID == sourceID
	}
	if !linked {
		return nil, ErrNotFound
	}
	src, err := s.crawler.GetSource(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	if src == nil || src.SourceID != sourceID || src.AccountID != accountID || src.Status == "disabled" || src.GlobalNewsSource {
		return nil, ErrNotFound
	}
	err = s.repo.UpdateAccountNewsSource(ctx, accountID, userID, sourceID, fields)
	switch {
	case errors.Is(err, repository.ErrAccountNewsAccess):
		return nil, ErrForbidden // membership revoked before the commit
	case errors.Is(err, repository.ErrConditionFailed):
		return nil, ErrNotFound // deleted or reassigned before the commit
	case err != nil:
		return nil, err
	}
	if fields.SourceName != nil {
		src.SourceName = *fields.SourceName
	}
	if fields.NewsQueries != nil {
		src.NewsQueries = *fields.NewsQueries
	}
	if fields.Aliases != nil {
		src.Aliases = *fields.Aliases
	}
	item := newsSourceItem(src, model.NewsSourceKindAccount, accountID, account.Name)
	return &item, nil
}
