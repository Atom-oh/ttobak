package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// newsSourceSubscriptions reads the caller's per-user crawler subscriptions.
type newsSourceSubscriptions interface {
	ListUserSubscriptionsBounded(ctx context.Context, userID string, limit int) ([]model.CrawlerSubscription, bool, error)
	GetSource(ctx context.Context, sourceID string) (*model.CrawlerSource, error)
}

// newsSourceMemberships is the membership reverse lookup (GSI1). Its rows
// are candidates only; each account is re-authorized before use.
type newsSourceMemberships interface {
	ListAccountsForUser(ctx context.Context, userID string) ([]model.AccountMember, error)
}

const (
	// maxNewsSourceAccounts and maxNewsSourceSubscriptions bound ListAll's
	// reads (at most 200 x 10 account sources plus 500 common ones); anything
	// beyond them is left out and reported as truncated.
	maxNewsSourceAccounts      = 200
	maxNewsSourceSubscriptions = 500
	defaultNewsSourcePage      = 50
	maxNewsSourcePage          = 100
	maxNewsSourceCursor        = 8192
	newsSourceListTimeout      = 20 * time.Second

	// Item bounds. encoding/json writes at most 6 bytes per code point
	// (\u00XX, \u2028), so a string cut to n code points serializes to at most
	// 6n+2 bytes. Each list is cut to its count, per-string code points and a
	// measured serialized byte budget; any cut sets listsTruncated. Every
	// budget holds at least one worst-case string (URL: 6,002 <= 6,144).
	maxNewsSourceIDRunes     = 128
	maxNewsSourceStatusRunes = 32
	maxNewsSourceTimeRunes   = 64
	maxNewsSourceQueryBytes  = 8192
	maxNewsSourceAliasBytes  = 4096
	maxNewsSourceOriginBytes = 2048 // newsSources
	maxNewsSourceURLBytes    = 6144
	maxNewsSourceURLRunes    = 1000
	maxNewsSourceURLs        = 20

	// maxNewsSourceItemBytes bounds one serialized item: the four lists
	// (8192+4096+2048+6144 = 20,480) plus sourceName and accountName
	// (2 x 1,202), sourceId (770), accountId (130), status (194),
	// lastCrawledAt (386), documentCount (20), kind (9), two booleans and
	// keys/punctuation (under 300): 24,693 < 25,000. A page of 100 items plus
	// nextCursor (8,192) and the envelope is under maxNewsSourcePageBytes;
	// even escaped again inside the API Gateway proxy body (at most 2x) it
	// stays under the 6 MB Lambda response limit.
	maxNewsSourceItemBytes = 25000
	maxNewsSourcePageBytes = maxNewsSourcePage*maxNewsSourceItemBytes + 20000
)

// SetNewsSourceDirectory injects the readers ListAll needs.
func (s *AccountNewsService) SetNewsSourceDirectory(subscriptions newsSourceSubscriptions, memberships newsSourceMemberships) {
	s.subscriptions, s.memberships = subscriptions, memberships
}

// boundedNewsText removes invalid UTF-8 and cuts to maxRunes code points.
func boundedNewsText(value string, maxRunes int) (string, bool) {
	out := truncateRunes(strings.ToValidUTF8(value, ""), maxRunes)
	return out, out != value
}

// boundedNewsList keeps at most maxCount strings of at most maxRunes code
// points whose JSON array encoding stays within maxBytes.
func boundedNewsList(values []string, maxCount, maxRunes, maxBytes int) ([]string, bool) {
	out, size, truncated := []string{}, 2, false
	for i, value := range values {
		text, cut := boundedNewsText(value, maxRunes)
		encoded, err := json.Marshal(text)
		if i == maxCount || err != nil || size+len(encoded)+1 > maxBytes {
			return out, true
		}
		out, size, truncated = append(out, text), size+len(encoded)+1, truncated || cut
	}
	return out, truncated
}

func newsSourceItem(src *model.CrawlerSource, kind, accountID, accountName string) model.NewsSourceItem {
	item := model.NewsSourceItem{
		SourceID: src.SourceID, SourceName: boundedOriginName(src.SourceName), Kind: kind,
		AccountID: accountID, AccountName: boundedOriginName(accountName), DocumentCount: src.DocumentCount,
		// Every listed source is editable: account sources by any direct
		// member, common ones through the caller's own subscription.
		CanEdit: true,
	}
	item.Status, _ = boundedNewsText(src.Status, maxNewsSourceStatusRunes)
	item.LastCrawledAt, _ = boundedNewsText(src.LastCrawledAt, maxNewsSourceTimeRunes)
	cut := make([]bool, 4)
	item.NewsQueries, cut[0] = boundedNewsList(src.NewsQueries, maxAccountNewsQueries, maxAccountNewsQueryRunes, maxNewsSourceQueryBytes)
	item.Aliases, cut[1] = boundedNewsList(src.Aliases, maxAliases, maxAliasCodePoints, maxNewsSourceAliasBytes)
	item.NewsSources, cut[2] = boundedNewsList(src.NewsSources, maxAliases, maxAliasCodePoints, maxNewsSourceOriginBytes)
	item.CustomUrls, cut[3] = boundedNewsList(src.CustomUrls, maxNewsSourceURLs, maxNewsSourceURLRunes, maxNewsSourceURLBytes)
	item.ListsTruncated = cut[0] || cut[1] || cut[2] || cut[3]
	return item
}

// newsSourceKey is the directory order: account kind first, by account name,
// account ID, source name and source ID; then common by source name and ID.
type newsSourceKey [5]string

func newsSourceKeyOf(item model.NewsSourceItem) newsSourceKey {
	rank := "0"
	if item.Kind == model.NewsSourceKindCommon {
		rank = "1"
	}
	return newsSourceKey{rank, item.AccountName, item.AccountID, item.SourceName, item.SourceID}
}

func (a newsSourceKey) less(b newsSourceKey) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// newsSourceCursor is the last returned item's key, bound to the caller. It
// only positions within the caller's own fresh directory.
type newsSourceCursor struct {
	V int           `json:"v"`
	U string        `json:"u"`
	K newsSourceKey `json:"k"`
}

func newsSourceCursorUser(userID string) string {
	sum := sha256.Sum256([]byte("news-sources-cursor\x00" + userID))
	return hex.EncodeToString(sum[:8])
}

func encodeNewsSourceCursor(userID string, key newsSourceKey) string {
	raw, _ := json.Marshal(newsSourceCursor{V: 1, U: newsSourceCursorUser(userID), K: key})
	return base64.RawURLEncoding.EncodeToString(raw)
}

// decodeNewsSourceCursor rejects malformed, oversized and foreign cursors.
func decodeNewsSourceCursor(userID, value string) (newsSourceKey, error) {
	var cursor newsSourceCursor
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if len(value) > maxNewsSourceCursor || err != nil {
		return newsSourceKey{}, ErrInvalidInput
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&cursor) != nil || decoder.More() || cursor.V != 1 || cursor.U != newsSourceCursorUser(userID) {
		return newsSourceKey{}, ErrInvalidInput
	}
	k := cursor.K
	limits := []int{1, maxOriginNameRunes, 128, maxOriginNameRunes, maxNewsSourceIDRunes}
	for i, part := range k {
		if !utf8.ValidString(part) || utf8.RuneCountInString(part) > limits[i] {
			return newsSourceKey{}, ErrInvalidInput
		}
	}
	if (k[0] != "0" && k[0] != "1") || k[4] == "" || (k[0] == "1" && (k[1] != "" || k[2] != "")) || (k[0] == "0" && k[2] == "") {
		return newsSourceKey{}, ErrInvalidInput
	}
	return k, nil
}

// ListAll returns one page of the caller's directory in newsSourceKey order.
// Each request rebuilds the whole bounded directory under a deadline, so pages
// are stable without server state; cursor names the last item returned.
func (s *AccountNewsService) ListAll(ctx context.Context, userID, cursor string, limit int) (*model.NewsSourcesResponse, error) {
	if s.subscriptions == nil || s.memberships == nil {
		return nil, fmt.Errorf("news source directory is unavailable")
	}
	if validateAccountHierarchyIDs(userID, "") != nil {
		return nil, ErrInvalidInput
	}
	if limit < 1 {
		limit = defaultNewsSourcePage
	}
	limit = min(limit, maxNewsSourcePage)
	var after *newsSourceKey
	if cursor != "" {
		key, err := decodeNewsSourceCursor(userID, cursor)
		if err != nil {
			return nil, err
		}
		after = &key
	}
	ctx, cancel := context.WithTimeout(ctx, newsSourceListTimeout)
	defer cancel()
	items, truncated, err := s.newsSourceDirectory(ctx, userID)
	if err != nil {
		return nil, err
	}
	start := 0
	if after != nil {
		start = sort.Search(len(items), func(i int) bool { return after.less(newsSourceKeyOf(items[i])) })
	}
	end := min(start+limit, len(items))
	resp := &model.NewsSourcesResponse{Sources: items[start:end], Truncated: truncated}
	if end < len(items) {
		resp.NextCursor = encodeNewsSourceCursor(userID, newsSourceKeyOf(items[end-1]))
	}
	return resp, nil
}

// newsSourceDirectory reads and sorts the caller's bounded directory: sources
// of accounts the caller is a current direct member of, then subscribed common
// sources (never operator or account-owned); undecodable rows are skipped.
func (s *AccountNewsService) newsSourceDirectory(ctx context.Context, userID string) ([]model.NewsSourceItem, bool, error) {
	truncated := false
	memberships, err := s.memberships.ListAccountsForUser(ctx, userID)
	if err != nil {
		return nil, false, err
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
		accountIDs, truncated = accountIDs[:maxNewsSourceAccounts], true
	}
	items := []model.NewsSourceItem{}
	for _, accountID := range accountIDs {
		account, err := s.authorize(ctx, accountID, userID)
		if errors.Is(err, ErrForbidden) || errors.Is(err, ErrInvalidInput) {
			continue // stale index row: no longer a current direct member
		}
		if err != nil {
			return nil, false, err
		}
		sources, err := s.ownedSources(ctx, accountID, true)
		if err != nil {
			return nil, false, err
		}
		for i := range sources {
			items = append(items, newsSourceItem(&sources[i], model.NewsSourceKindAccount, accountID, account.Name))
		}
	}
	subs, more, err := s.subscriptions.ListUserSubscriptionsBounded(ctx, userID, maxNewsSourceSubscriptions)
	if err != nil {
		return nil, false, fmt.Errorf("failed to list subscriptions: %w", err)
	}
	truncated = truncated || more
	for _, sub := range subs {
		// An acct_ ID is account-owned by construction, even when its CONFIG
		// lacks accountId; a stray subscription never lists it as common.
		// An ID too long or invalid to return unchanged is skipped.
		if sub.SourceID == "" || strings.HasPrefix(sub.SourceID, accountNewsSourcePrefix) ||
			!utf8.ValidString(sub.SourceID) || utf8.RuneCountInString(sub.SourceID) > maxNewsSourceIDRunes {
			continue
		}
		src, err := s.subscriptions.GetSource(ctx, sub.SourceID)
		if errors.Is(err, repository.ErrInvalidSource) {
			continue
		}
		if err != nil {
			return nil, false, fmt.Errorf("failed to get source: %w", err)
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
		items = append(items, newsSourceItem(src, model.NewsSourceKindCommon, "", ""))
	}
	sort.SliceStable(items, func(i, j int) bool { return newsSourceKeyOf(items[i]).less(newsSourceKeyOf(items[j])) })
	return items, truncated, nil
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
