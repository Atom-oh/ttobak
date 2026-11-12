package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

type accountNewsRepo interface {
	GetAccount(context.Context, string) (*model.Account, error)
	GetMember(context.Context, string, string) (*model.AccountMember, error)
	CreateAccountNewsSource(ctx context.Context, src *model.CrawlerSource, ref *model.AccountNewsSourceRef, observedCount, limit int) error
	ListAccountNewsSources(context.Context, string) ([]model.AccountNewsSourceRef, error)
	DeleteAccountNewsSource(context.Context, string, string, string) error
	UpdateAccountNewsSource(ctx context.Context, accountID, userID, sourceID string, fields repository.AccountNewsSourceFields) error
	ReconcileAccountNewsSourceCount(ctx context.Context, accountID string) (int, error)
	GetAccountNewsSourceCount(ctx context.Context, accountID string) (count int, exists bool, err error)
	ClaimAccountNewsCollect(ctx context.Context, accountID, userID, sourceID, requestID string, requestedAt, cutoff time.Time) error
	ReleaseAccountNewsCollect(ctx context.Context, sourceID, requestID string) error
}

type accountNewsCrawler interface {
	GetSource(ctx context.Context, sourceID string) (*model.CrawlerSource, error)
	// GetSourceConsistent is GetSource with a strongly consistent read.
	GetSourceConsistent(ctx context.Context, sourceID string) (*model.CrawlerSource, error)
	ListDocuments(ctx context.Context, sourceID, docType string, limit int32, lastKey map[string]types.AttributeValue) ([]model.CrawledDocument, map[string]types.AttributeValue, int, error)
}

const (
	// maxAccountNewsSources bounds scheduled crawl cost per account. The ref
	// count below is a fast pre-check; the create transaction enforces the cap
	// atomically through the account's source counter row.
	maxAccountNewsSources = 10

	// Limits match the crawler's source-revision bounds (newsSourceRevision).
	maxAccountNewsQueries     = 32
	maxAccountNewsQueryRunes  = 200
	maxAccountNewsNameRunes   = 200
	maxAccountNewsCustomURLs  = 20
	accountNewsSourceStatus   = "idle" // the status AddSource gives new sources
	accountNewsSourceSchedule = "daily"
	accountNewsReadBatch      = 200
)

// AccountNewsService manages account-owned news sources for direct account
// members. Every account source has its own new ID; it never merges into an
// existing per-user source (unlike CrawlerService.AddSource).
type AccountNewsService struct {
	repo     accountNewsRepo
	crawler  accountNewsCrawler
	insights *InsightsService
	now      func() time.Time
	randHex  func() (string, error)
	// subscriptions and memberships back ListAll; nil fails it closed.
	subscriptions newsSourceSubscriptions
	memberships   newsSourceMemberships
	// publishCollect sends NewsCollectRequested; nil fails CollectNow closed.
	publishCollect func(context.Context, model.NewsCollectRequested) error
	// collectAdmission mirrors the starter's refusals; nil fails CollectNow closed.
	collectAdmission *NewsCollectAdmission
}

func NewAccountNewsService(repo accountNewsRepo, crawler accountNewsCrawler, insights *InsightsService) *AccountNewsService {
	return &AccountNewsService{repo: repo, crawler: crawler, insights: insights, now: time.Now, randHex: randomHex8}
}

func randomHex8() (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (s *AccountNewsService) authorize(ctx context.Context, accountID, userID string) (*model.Account, error) {
	if validateAccountHierarchyIDs(accountID, "") != nil || validateAccountHierarchyIDs(userID, "") != nil {
		return nil, ErrInvalidInput
	}
	account, err := s.repo.GetAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if account == nil {
		return nil, ErrForbidden
	}
	member, err := s.repo.GetMember(ctx, accountID, userID)
	if err != nil {
		return nil, err
	}
	if member == nil {
		return nil, ErrForbidden
	}
	return account, nil
}

// cleanNewsTerms trims, deduplicates case-insensitively and bounds terms.
// strict rejects bad input; otherwise bad items are skipped and the list is
// truncated (used for defaults derived from account metadata).
func cleanNewsTerms(values []string, maxCount, maxRunes int, strict bool) ([]string, error) {
	seen := map[string]bool{}
	out := []string{}
	for _, raw := range values {
		v := strings.TrimSpace(raw)
		ok := v != "" && utf8.ValidString(v) && utf8.RuneCountInString(v) <= maxRunes
		for _, r := range v {
			if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
				ok = false
			}
		}
		if !ok {
			if strict {
				return nil, ErrInvalidInput
			}
			continue
		}
		key := strings.ToLower(v)
		if seen[key] {
			continue
		}
		if len(out) == maxCount {
			if strict {
				return nil, ErrInvalidInput
			}
			break
		}
		seen[key] = true
		out = append(out, v)
	}
	return out, nil
}

// accountNewsSourcePrefix starts every account-owned crawler source ID.
const accountNewsSourcePrefix = "acct_"

func accountNewsSourceID(accountID, suffix string) (string, error) {
	id := accountNewsSourcePrefix + accountID + "_" + suffix
	if !validNewsSourceID(id) {
		return "", ErrInvalidInput
	}
	return id, nil
}

// Create makes a new account-owned source. With CopyFromSourceID it copies
// queries, aliases and custom URLs from a per-user source the caller
// subscribes to; the original source is only read.
func (s *AccountNewsService) Create(ctx context.Context, userID, accountID string, req *model.CreateAccountNewsSourceRequest) (*model.CrawlerSource, error) {
	if req == nil {
		return nil, ErrInvalidInput
	}
	names, err := cleanNewsTerms([]string{req.SourceName}, 1, maxAccountNewsNameRunes, true)
	if err != nil || len(names) != 1 {
		return nil, ErrInvalidInput
	}
	queries, err := cleanNewsTerms(req.NewsQueries, maxAccountNewsQueries, maxAccountNewsQueryRunes, true)
	if err != nil {
		return nil, err
	}
	aliases, err := validateAliases(req.Aliases)
	if err != nil {
		return nil, err
	}
	if req.CopyFromSourceID != "" && !validNewsSourceID(req.CopyFromSourceID) {
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
	if len(refs) >= maxAccountNewsSources {
		return nil, ErrInvalidInput
	}
	observed, err := s.repairLowCounter(ctx, accountID, len(refs))
	if err != nil {
		return nil, err
	}

	var customURLs []string
	if req.CopyFromSourceID != "" {
		original, err := s.crawler.GetSource(ctx, req.CopyFromSourceID)
		if err != nil {
			return nil, err
		}
		// Missing, operator-curated, account-owned and unsubscribed sources are
		// all the same rejection, so the response reveals nothing about other
		// users' or accounts' sources. Only per-user sources can be copied.
		if original == nil || original.GlobalNewsSource || original.AccountID != "" || !contains(original.Subscribers, userID) {
			return nil, ErrInvalidInput
		}
		if queries, err = cleanNewsTerms(append(append([]string{}, original.NewsQueries...), queries...), maxAccountNewsQueries, maxAccountNewsQueryRunes, true); err != nil {
			return nil, err
		}
		if aliases, err = validateAliases(append(append([]string{}, original.Aliases...), aliases...)); err != nil {
			return nil, err
		}
		if len(original.CustomUrls) > maxAccountNewsCustomURLs {
			return nil, ErrInvalidInput
		}
		customURLs = append([]string{}, original.CustomUrls...)
	} else if len(aliases) == 0 {
		aliases, _ = cleanNewsTerms(account.Aliases, maxAliases, maxAliasCodePoints, false)
	}
	if len(queries) == 0 {
		queries, _ = cleanNewsTerms(append([]string{account.Name}, account.Aliases...), maxAccountNewsQueries, maxAccountNewsQueryRunes, false)
	}
	if len(queries) == 0 {
		return nil, ErrInvalidInput
	}

	now := s.now().UTC()
	reconciled := false
	for collisions := 0; collisions < 2; {
		suffix, err := s.randHex()
		if err != nil {
			return nil, fmt.Errorf("generate source id: %w", err)
		}
		sourceID, err := accountNewsSourceID(accountID, suffix)
		if err != nil {
			return nil, err
		}
		src := &model.CrawlerSource{SourceID: sourceID, SourceName: names[0], OwnerID: userID, AccountID: accountID, Subscribers: []string{userID}}
		src.AWSServices, src.NewsSources, src.NewsQueries, src.CustomUrls, src.Aliases = []string{}, []string{}, queries, customURLs, aliases
		src.Schedule, src.Status = accountNewsSourceSchedule, accountNewsSourceStatus
		if src.CustomUrls == nil {
			src.CustomUrls = []string{}
		}
		ref := &model.AccountNewsSourceRef{AccountID: accountID, SourceID: sourceID, SourceName: names[0], CreatedBy: userID, CreatedAt: now}
		err = s.repo.CreateAccountNewsSource(ctx, src, ref, observed, maxAccountNewsSources)
		switch {
		case err == nil:
			return src, nil
		case errors.Is(err, repository.ErrAccountNewsAccess):
			return nil, ErrForbidden
		case errors.Is(err, repository.ErrAccountNewsSourceLimit):
			// The refs showed room but the counter is at the cap: either a
			// concurrent create won, or the counter drifted from the refs
			// (e.g. seeded while a pre-counter delete committed). Reconcile it
			// to the canonical ref count once, then retry.
			if reconciled {
				return nil, ErrInvalidInput
			}
			reconciled = true
			count, err := s.repo.ReconcileAccountNewsSourceCount(ctx, accountID)
			if err != nil {
				return nil, err
			}
			if count >= maxAccountNewsSources {
				return nil, ErrInvalidInput
			}
			observed = count
		case errors.Is(err, repository.ErrConditionFailed):
			collisions++ // ID collision: retry once with a fresh suffix
		default:
			return nil, err
		}
	}
	return nil, fmt.Errorf("account news source id collision")
}

// repairLowCounter reconciles a source counter that is below the observed
// ref count (left low by a writer that predates the counter) before the
// create, so the counter condition enforces the cap again. It returns the ref
// count to use; a reconciled count at the cap is ErrInvalidInput. A high
// counter is left to the limit-failure reconcile in Create.
func (s *AccountNewsService) repairLowCounter(ctx context.Context, accountID string, refs int) (int, error) {
	count, exists, err := s.repo.GetAccountNewsSourceCount(ctx, accountID)
	if err != nil {
		return 0, err
	}
	if !exists || count >= refs {
		return refs, nil
	}
	reconciled, err := s.repo.ReconcileAccountNewsSourceCount(ctx, accountID)
	if err != nil {
		return 0, err
	}
	if reconciled >= maxAccountNewsSources {
		return 0, ErrInvalidInput
	}
	return reconciled, nil
}

// accountSources returns the account's sources whose CONFIG still names the
// account and is not disabled. Refs alone never prove ownership.
func (s *AccountNewsService) accountSources(ctx context.Context, accountID string) ([]model.CrawlerSource, error) {
	return s.ownedSources(ctx, accountID, false)
}

// ownedSources is accountSources; with skipInvalid, a CONFIG row that does
// not decode (legacy scalar list values) is left out instead of failing the
// whole read.
func (s *AccountNewsService) ownedSources(ctx context.Context, accountID string, skipInvalid bool) ([]model.CrawlerSource, error) {
	refs, err := s.repo.ListAccountNewsSources(ctx, accountID)
	if err != nil {
		return nil, err
	}
	out := make([]model.CrawlerSource, 0, len(refs))
	for _, ref := range refs {
		if !validNewsSourceID(ref.SourceID) {
			continue
		}
		src, err := s.crawler.GetSource(ctx, ref.SourceID)
		if skipInvalid && errors.Is(err, repository.ErrInvalidSource) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if src == nil || src.AccountID != accountID || src.SourceID != ref.SourceID || src.Status == "disabled" || src.GlobalNewsSource {
			continue
		}
		out = append(out, *src)
	}
	return out, nil
}

// List returns the account's news sources, revalidating each CONFIG accountId.
func (s *AccountNewsService) List(ctx context.Context, userID, accountID string) ([]model.CrawlerSource, error) {
	if _, err := s.authorize(ctx, accountID, userID); err != nil {
		return nil, err
	}
	sources, err := s.accountSources(ctx, accountID)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	for i := range sources {
		sources[i].NextCollectAllowedAt = nextCollectAllowedAt(sources[i].LastCollectRequestedAt, now)
	}
	return sources, nil
}

// Delete removes the ref and disables the CONFIG so the scheduler stops
// crawling it. Unknown or foreign sources are ErrNotFound.
func (s *AccountNewsService) Delete(ctx context.Context, userID, accountID, sourceID string) error {
	if !validNewsSourceID(sourceID) {
		return ErrInvalidInput
	}
	if _, err := s.authorize(ctx, accountID, userID); err != nil {
		return err
	}
	err := s.repo.DeleteAccountNewsSource(ctx, accountID, userID, sourceID)
	if errors.Is(err, repository.ErrAccountNewsAccess) {
		// Membership was revoked between the check and the commit.
		return ErrForbidden
	}
	if errors.Is(err, repository.ErrConditionFailed) {
		return ErrNotFound
	}
	return err
}

// CurrentNews returns current-version news crawled in [from, to) by the
// account's sources, newest first. It performs no authorization; callers must
// authorize the account first.
func (s *AccountNewsService) CurrentNews(ctx context.Context, accountID string, from, to time.Time) ([]model.CrawledDocument, error) {
	if s.insights == nil {
		return nil, fmt.Errorf("current news reader is unavailable")
	}
	if !to.After(from) {
		return []model.CrawledDocument{}, nil
	}
	start, end := from.Unix(), to.Unix()
	inWindow := func(doc model.CrawledDocument) bool { return doc.CrawledAt >= start && doc.CrawledAt < end }
	sources, err := s.accountSources(ctx, accountID)
	if err != nil {
		return nil, err
	}
	candidates := []model.CrawledDocument{}
	for _, src := range sources {
		docs, _, _, err := s.crawler.ListDocuments(ctx, src.SourceID, "news", 0, nil)
		if err != nil {
			return nil, err
		}
		for _, doc := range docs {
			if doc.SourceID == "" {
				doc.SourceID = src.SourceID
			}
			if doc.SourceID == src.SourceID && inWindow(doc) {
				candidates = append(candidates, doc)
			}
		}
	}
	// Each document needs two current-row keys; ReadNewsItems takes at most
	// 1,000 keys per call, so re-read in bounded batches.
	out := make([]model.CrawledDocument, 0, len(candidates))
	for startAt := 0; startAt < len(candidates); startAt += accountNewsReadBatch {
		stop := min(startAt+accountNewsReadBatch, len(candidates))
		current, err := s.insights.currentNewsList(ctx, candidates[startAt:stop])
		if err != nil {
			return nil, err
		}
		// The current row may differ from the listed one; re-apply the window.
		for _, doc := range current {
			if inWindow(doc) {
				out = append(out, doc)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CrawledAt > out[j].CrawledAt })
	return out, nil
}
