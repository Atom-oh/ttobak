package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// crawlerRepo defines the repository methods used by CrawlerService and InsightsService.
type crawlerRepo interface {
	GetSource(ctx context.Context, sourceID string) (*model.CrawlerSource, error)
	PutSource(ctx context.Context, source *model.CrawlerSource) error
	PutSourceIfAbsent(ctx context.Context, source *model.CrawlerSource) error
	UpdateSourceSubscription(ctx context.Context, sourceID string, expected *model.CrawlerSource, subscribers, awsServices, newsQueries, newsSources, customUrls []string) error
	GetSubscription(ctx context.Context, userID, sourceID string) (*model.CrawlerSubscription, error)
	PutSubscription(ctx context.Context, userID string, sub *model.CrawlerSubscription) error
	DeleteSubscription(ctx context.Context, userID, sourceID string) error
	ListUserSubscriptions(ctx context.Context, userID string) ([]model.CrawlerSubscription, error)
	ListHistory(ctx context.Context, sourceID string, limit int32) ([]model.CrawlHistory, error)
	GetDocument(ctx context.Context, sourceID, docHash string) (*model.CrawledDocument, error)
	DeleteDocument(ctx context.Context, sourceID, docHash string) error
	ListDocuments(ctx context.Context, sourceID, docType string, limit int32, lastKey map[string]types.AttributeValue) ([]model.CrawledDocument, map[string]types.AttributeValue, int, error)
	ListAllDocumentsByType(ctx context.Context, docType string, limit int32, page int) ([]model.CrawledDocument, int, error)
	NormalizeSourceID(name string) string
}

// CrawlerService handles crawler source management and subscription logic.
type CrawlerService struct {
	repo crawlerRepo
}

// NewCrawlerService creates a new CrawlerService.
func NewCrawlerService(repo *repository.CrawlerRepository) *CrawlerService {
	return &CrawlerService{repo: repo}
}

// AddSource adds a new crawler source or subscribes the user to an existing one.
// If the source already exists, it adds the user as a subscriber and merges awsServices.
// If not, it creates a new source with status="idle".
func (s *CrawlerService) AddSource(ctx context.Context, userID string, req *model.AddCrawlerSourceRequest) (*model.CrawlerSourceResponse, error) {
	sourceID := normalizeSourceID(req.SourceName)

	// Retry once: a concurrent AddSource for the same not-yet-existing
	// sourceId could otherwise let PutSource's unconditional overwrite make
	// the later writer OwnerID (this PR's destructive-delete gate) on a
	// source the earlier caller believes they created. GetSource + a
	// conditional create closes that -- on ConditionalCheckFailedException
	// (someone else created it in between), re-fetch and fall into the
	// existing-source update path instead of retrying the create.
	source, err := s.repo.GetSource(ctx, sourceID)
	if err != nil {
		return nil, fmt.Errorf("failed to get source: %w", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	justCreated := false

	if source == nil {
		newSource := &model.CrawlerSource{
			SourceID:    sourceID,
			SourceName:  req.SourceName,
			OwnerID:     userID,
			Subscribers: []string{userID},
			AWSServices: req.AWSServices,
			NewsQueries: req.NewsQueries,
			NewsSources: req.NewsSources,
			CustomUrls:  req.CustomUrls,
			Schedule:    "daily",
			Status:      "idle",
		}
		if err := s.repo.PutSourceIfAbsent(ctx, newSource); err == nil {
			source = newSource
			justCreated = true
		} else if errors.Is(err, repository.ErrConditionFailed) {
			// Someone else created it between our GetSource and PutSourceIfAbsent
			// -- fall into the existing-source update path below instead of
			// treating their create as ours.
			source, err = s.repo.GetSource(ctx, sourceID)
			if err != nil {
				return nil, fmt.Errorf("failed to get source after create race: %w", err)
			}
			if source == nil {
				return nil, fmt.Errorf("source %s vanished after concurrent create", sourceID)
			}
		} else {
			return nil, fmt.Errorf("failed to create source: %w", err)
		}
	}

	// Skip the merge below when we just created the source: its fields
	// already match req exactly.
	//
	// The merge itself used to be a whole-item PutSource using this
	// request's local `source` value -- if a concurrent AddSource for a
	// DIFFERENT user (or an admin's ownerId backfill) updated the item in
	// the window between the read above and that write, the whole item
	// would be overwritten with this call's stale snapshot, losing the
	// other write entirely (and since OwnerID is this feature's
	// destructive-delete gate, silently erasing an admin's manual backfill
	// is the same class of bug, not just a lost subscriber). Replaced with
	// a partial UpdateItem (UpdateSourceSubscription) that only ever
	// touches the five subscription-merge fields -- structurally incapable
	// of overwriting ownerId -- guarded by a condition asserting those five
	// fields are still what this call read, with a bounded retry (re-read,
	// re-merge, re-attempt) on a concurrent change instead of overwriting it.
	if !justCreated {
		const maxMergeAttempts = 3
		for attempt := 1; ; attempt++ {
			subscribers := source.Subscribers
			if !contains(subscribers, userID) {
				subscribers = append(append([]string{}, subscribers...), userID)
			}
			awsServices := union(source.AWSServices, req.AWSServices)
			newsQueries := union(source.NewsQueries, req.NewsQueries)
			newsSources := union(source.NewsSources, req.NewsSources)
			customUrls := union(source.CustomUrls, req.CustomUrls)

			err := s.repo.UpdateSourceSubscription(ctx, sourceID, source, subscribers, awsServices, newsQueries, newsSources, customUrls)
			if err == nil {
				source.Subscribers = subscribers
				source.AWSServices = awsServices
				source.NewsQueries = newsQueries
				source.NewsSources = newsSources
				source.CustomUrls = customUrls
				break
			}
			if !errors.Is(err, repository.ErrConditionFailed) {
				return nil, fmt.Errorf("failed to update source: %w", err)
			}
			if attempt >= maxMergeAttempts {
				return nil, fmt.Errorf("failed to update source: subscription fields changed concurrently %d times, giving up", maxMergeAttempts)
			}
			source, err = s.repo.GetSource(ctx, sourceID)
			if err != nil {
				return nil, fmt.Errorf("failed to re-read source after merge race: %w", err)
			}
			if source == nil {
				return nil, fmt.Errorf("source %s vanished during merge retry", sourceID)
			}
		}
	}

	// Create or update user subscription
	sub := &model.CrawlerSubscription{
		SourceID:    sourceID,
		AWSServices: req.AWSServices,
		NewsSources: req.NewsSources,
		NewsQueries: req.NewsQueries,
		CustomUrls:  req.CustomUrls,
		AddedAt:     now,
	}
	if err := s.repo.PutSubscription(ctx, userID, sub); err != nil {
		return nil, fmt.Errorf("failed to create subscription: %w", err)
	}

	return &model.CrawlerSourceResponse{
		Source:       *source,
		Subscription: *sub,
	}, nil
}

// ListSources lists all crawler sources the user is subscribed to.
func (s *CrawlerService) ListSources(ctx context.Context, userID string) (*model.CrawlerSourcesResponse, error) {
	subs, err := s.repo.ListUserSubscriptions(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to list subscriptions: %w", err)
	}

	sources := make([]model.CrawlerSourceResponse, 0, len(subs))
	for _, sub := range subs {
		source, err := s.repo.GetSource(ctx, sub.SourceID)
		if err != nil {
			return nil, fmt.Errorf("failed to get source %s: %w", sub.SourceID, err)
		}
		if source == nil {
			// Source was deleted but subscription remains — skip
			continue
		}
		sources = append(sources, model.CrawlerSourceResponse{
			Source:       *source,
			Subscription: sub,
		})
	}

	return &model.CrawlerSourcesResponse{Sources: sources}, nil
}

// UpdateSource updates the user's subscription and rebuilds the source's union from all subscribers.
func (s *CrawlerService) UpdateSource(ctx context.Context, userID, sourceID string, req *model.UpdateCrawlerSourceRequest) error {
	// Update the user's subscription
	sub, err := s.repo.GetSubscription(ctx, userID, sourceID)
	if err != nil {
		return fmt.Errorf("failed to get subscription: %w", err)
	}
	if sub == nil {
		return ErrNotFound
	}

	sub.AWSServices = req.AWSServices
	sub.NewsSources = req.NewsSources
	sub.NewsQueries = req.NewsQueries
	sub.CustomUrls = req.CustomUrls

	if err := s.repo.PutSubscription(ctx, userID, sub); err != nil {
		return fmt.Errorf("failed to update subscription: %w", err)
	}

	// Rebuild source union from all subscribers
	return s.rebuildSourceUnion(ctx, sourceID)
}

// Unsubscribe removes a user from a crawler source.
// If the user is the last subscriber, the source status is set to "disabled".
func (s *CrawlerService) Unsubscribe(ctx context.Context, userID, sourceID string) error {
	// Delete the subscription
	if err := s.repo.DeleteSubscription(ctx, userID, sourceID); err != nil {
		return fmt.Errorf("failed to delete subscription: %w", err)
	}

	// Update the source
	source, err := s.repo.GetSource(ctx, sourceID)
	if err != nil {
		return fmt.Errorf("failed to get source: %w", err)
	}
	if source == nil {
		return nil // Source already gone
	}

	source.Subscribers = remove(source.Subscribers, userID)

	if len(source.Subscribers) == 0 {
		source.Status = "disabled"
		source.AWSServices = nil
		source.NewsSources = nil
		source.NewsQueries = nil
		source.CustomUrls = nil
		if err := s.repo.PutSource(ctx, source); err != nil {
			return fmt.Errorf("failed to update source: %w", err)
		}
		return nil
	}

	// Save updated subscribers list, then rebuild union from remaining
	if err := s.repo.PutSource(ctx, source); err != nil {
		return fmt.Errorf("failed to update source: %w", err)
	}
	return s.rebuildSourceUnion(ctx, sourceID)
}

// GetHistory retrieves crawl history for a source.
func (s *CrawlerService) GetHistory(ctx context.Context, sourceID string) (*model.CrawlHistoryResponse, error) {
	history, err := s.repo.ListHistory(ctx, sourceID, 50)
	if err != nil {
		return nil, fmt.Errorf("failed to list history: %w", err)
	}
	return &model.CrawlHistoryResponse{History: history}, nil
}

// rebuildSourceUnion fetches all subscriptions for a source and rebuilds
// the source's awsServices, newsQueries, and customUrls as unions.
func (s *CrawlerService) rebuildSourceUnion(ctx context.Context, sourceID string) error {
	source, err := s.repo.GetSource(ctx, sourceID)
	if err != nil {
		return fmt.Errorf("failed to get source: %w", err)
	}
	if source == nil {
		return nil
	}

	var allAWS, allNewsSources, allNewsQueries, allURLs []string
	for _, uid := range source.Subscribers {
		sub, err := s.repo.GetSubscription(ctx, uid, sourceID)
		if err != nil {
			return fmt.Errorf("failed to get subscription for %s: %w", uid, err)
		}
		if sub == nil {
			continue
		}
		allAWS = union(allAWS, sub.AWSServices)
		allNewsSources = union(allNewsSources, sub.NewsSources)
		allNewsQueries = union(allNewsQueries, sub.NewsQueries)
		allURLs = union(allURLs, sub.CustomUrls)
	}

	source.AWSServices = allAWS
	source.NewsSources = allNewsSources
	source.NewsQueries = allNewsQueries
	source.CustomUrls = allURLs

	if err := s.repo.PutSource(ctx, source); err != nil {
		return fmt.Errorf("failed to update source union: %w", err)
	}
	return nil
}

// normalizeSourceID converts a human-readable source name to a stable ID.
// Keeps unicode letters (Korean, etc.), ASCII digits, and hyphens.
// e.g. "AWS Blog" -> "aws-blog", "하나금융그룹" -> "하나금융그룹"
func normalizeSourceID(name string) string {
	id := strings.ToLower(strings.TrimSpace(name))
	id = strings.ReplaceAll(id, " ", "-")
	var b strings.Builder
	for _, r := range id {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// contains checks if a string slice contains a value.
func contains(slice []string, val string) bool {
	for _, s := range slice {
		if s == val {
			return true
		}
	}
	return false
}

// union returns the union of two string slices (no duplicates).
func union(a, b []string) []string {
	seen := make(map[string]struct{}, len(a))
	for _, v := range a {
		seen[v] = struct{}{}
	}
	result := make([]string, 0, len(a)+len(b))
	result = append(result, a...)
	for _, v := range b {
		if _, ok := seen[v]; !ok {
			result = append(result, v)
			seen[v] = struct{}{}
		}
	}
	return result
}

// remove returns a new slice with the specified value removed.
func remove(slice []string, val string) []string {
	result := make([]string, 0, len(slice))
	for _, s := range slice {
		if s != val {
			result = append(result, s)
		}
	}
	return result
}
