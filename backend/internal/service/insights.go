package service

import (
	"context"
	"fmt"
	"io"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

type docCache struct {
	mu      sync.RWMutex
	entries map[string]cacheEntry
}

type cacheEntry struct {
	docs      []model.CrawledDocument
	total     int
	expiresAt time.Time
}

var scanCache = &docCache{entries: make(map[string]cacheEntry)}

const cacheTTL = 30 * time.Second

func (c *docCache) get(key string) ([]model.CrawledDocument, int, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.entries[key]
	if !ok || time.Now().After(e.expiresAt) {
		return nil, 0, false
	}
	return e.docs, e.total, true
}

func (c *docCache) set(key string, docs []model.CrawledDocument, total int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = cacheEntry{docs: docs, total: total, expiresAt: time.Now().Add(cacheTTL)}
}

func (c *docCache) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]cacheEntry)
}

// s3GetDeleter is the S3 capability InsightsService needs: reading a
// document's full content (GetDocumentDetail) and cleaning up its object on
// destructive delete (DeleteDocument). Matches *s3.Client's methods directly
// (same convention as AccountService's s3ObjectDeleter) so the real client
// satisfies it without wrapping, and a test can inject a mock implementing
// just these two methods to exercise DeleteDocument's S3-then-DynamoDB happy
// path -- which a concrete *s3.Client field could not do without a live AWS
// connection.
type s3GetDeleter interface {
	GetObject(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	DeleteObject(ctx context.Context, params *s3.DeleteObjectInput, optFns ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
}

// InsightsService handles document listing and insights for crawled content.
type InsightsService struct {
	repo         crawlerRepo
	s3           s3GetDeleter
	kbBucketName string
}

// NewInsightsService creates a new InsightsService.
func NewInsightsService(repo *repository.CrawlerRepository, s3Client *s3.Client, kbBucketName string) *InsightsService {
	return &InsightsService{repo: repo, s3: s3Client, kbBucketName: kbBucketName}
}

// GetDocumentDetail reads metadata from DynamoDB and full content from S3.
func (s *InsightsService) GetDocumentDetail(ctx context.Context, sourceID, docHash string) (*model.InsightDetailResponse, error) {
	meta, err := s.repo.GetDocument(ctx, sourceID, docHash)
	if err != nil {
		return nil, fmt.Errorf("failed to get document metadata: %w", err)
	}

	// Read content from S3 — prefer stored s3Key from metadata
	var s3Key string
	if meta != nil && meta.S3Key != "" {
		s3Key = meta.S3Key
	} else {
		s3Key = fmt.Sprintf("shared/news/%s/%s.md", sourceID, docHash)
	}

	result, err := s.s3.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.kbBucketName),
		Key:    aws.String(s3Key),
	})
	if err != nil {
		// Fallback paths
		fallbacks := []string{
			fmt.Sprintf("shared/news/%s/%s.md", sourceID, docHash),
			fmt.Sprintf("shared/aws-docs/%s/%s.md", sourceID, docHash),
		}
		for _, fb := range fallbacks {
			if fb == s3Key {
				continue
			}
			result, err = s.s3.GetObject(ctx, &s3.GetObjectInput{
				Bucket: aws.String(s.kbBucketName),
				Key:    aws.String(fb),
			})
			if err == nil {
				break
			}
		}
		if err != nil {
			return nil, fmt.Errorf("document not found: %w", err)
		}
	}
	defer result.Body.Close()

	body, err := io.ReadAll(result.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read content: %w", err)
	}

	resp := &model.InsightDetailResponse{Content: string(body)}
	if meta != nil {
		resp.CrawledDocument = *meta
	} else {
		resp.CrawledDocument = model.CrawledDocument{
			DocHash:  docHash,
			SourceID: sourceID,
		}
	}
	return resp, nil
}

// DeleteDocument removes a single crawled document (DynamoDB metadata + S3
// KB object). Authorization is scoped to the source's owner (whoever
// created/registered the source) or an admin -- NOT any subscriber, since
// AddSource lets any authenticated user self-subscribe to an existing
// source with no invite/approval step, which would otherwise make this
// destructive route trivially self-grantable to anyone. The KB vector
// store is NOT purged here: Bedrock Knowledge Base only reconciles
// deletions on an ingestion job. The handler (InsightsHandler.DeleteDocument)
// triggers one, best-effort, right after this call succeeds -- a stale
// vector can still surface in Q&A until that job completes, or if it
// fails, until the next daily crawl/manual sync.
func (s *InsightsService) DeleteDocument(ctx context.Context, userID string, isAdmin bool, sourceID, docHash string) error {
	// Defense-in-depth: an empty userID must never satisfy the owner check
	// below via an accidental "" == "" match against a legacy source that
	// hasn't been backfilled with an OwnerID yet (see OwnerID note below).
	// The normal auth path (Lambda@Edge + verified JWT) never hands us an
	// empty sub, but this destructive route shouldn't rely on that alone.
	if userID == "" {
		return ErrForbidden
	}

	source, err := s.repo.GetSource(ctx, sourceID)
	if err != nil {
		return fmt.Errorf("failed to get source: %w", err)
	}
	if source == nil {
		return ErrNotFound
	}
	// source.OwnerID is only populated for sources created after this field
	// was added (AddSource's new-source branch). A source created before
	// that rollout has OwnerID == "" -- explicitly deny non-admins rather
	// than let it fall through to an OwnerID != userID comparison, so a
	// legacy source is admin-only-deletable until backfilled (see
	// scripts/insights-backfill-owner.py), not silently unowned-by-anyone
	// or (worse) matchable by an empty caller ID.
	if !isAdmin && (source.OwnerID == "" || source.OwnerID != userID) {
		return ErrForbidden
	}

	doc, err := s.repo.GetDocument(ctx, sourceID, docHash)
	if err != nil {
		return fmt.Errorf("failed to get document: %w", err)
	}
	if doc == nil {
		return ErrNotFound
	}

	// S3 delete FIRST: if this fails, the DynamoDB metadata is still intact
	// and the delete is safely retryable. Deleting DynamoDB first would risk
	// the opposite -- metadata gone, so GetDocument returns nil on retry,
	// permanently orphaning the S3 object + KB vector with no API path left
	// to remove them.
	if doc.S3Key != "" {
		if _, err := s.s3.DeleteObject(ctx, &s3.DeleteObjectInput{
			Bucket: aws.String(s.kbBucketName),
			Key:    aws.String(doc.S3Key),
		}); err != nil {
			return fmt.Errorf("failed to delete KB object: %w", err)
		}
	} else {
		// Older documents without a stored S3Key can be shaped like either
		// crawler type (mirrors GetDocumentDetail's read-path fallback).
		// DeleteObject succeeds (no error) on a key that doesn't already
		// exist, so deleting both candidate keys unconditionally is
		// idempotent and correct -- unlike probing with HeadObject first,
		// which can't tell "not found" apart from throttling/403/network
		// errors and would silently skip the real object on any of those.
		for _, key := range []string{
			fmt.Sprintf("shared/news/%s/%s.md", sourceID, docHash),
			fmt.Sprintf("shared/aws-docs/%s/%s.md", sourceID, docHash),
		} {
			if _, err := s.s3.DeleteObject(ctx, &s3.DeleteObjectInput{
				Bucket: aws.String(s.kbBucketName),
				Key:    aws.String(key),
			}); err != nil {
				return fmt.Errorf("failed to delete KB object %s: %w", key, err)
			}
		}
	}

	if err := s.repo.DeleteDocument(ctx, sourceID, docHash); err != nil {
		return fmt.Errorf("failed to delete document: %w", err)
	}

	log.Printf("insights: document deleted userID=%s sourceID=%s docHash=%s", userID, sourceID, docHash)
	scanCache.clear()
	return nil
}

// ListInsights retrieves crawled documents with optional filtering by type, source, service, tags, and sort.
// sortBy: "newest" (default), "oldest", "title"
func (s *InsightsService) ListInsights(ctx context.Context, docType, source, service string, tags []string, sortBy string, page, limit int) (*model.InsightsResponse, error) {
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 20
	}
	if limit > 50 {
		limit = 50
	}

	var docs []model.CrawledDocument
	var totalCount int
	var err error

	if source != "" {
		docs, totalCount, err = s.listBySource(ctx, source, docType, service, tags, sortBy, page, limit)
	} else {
		docs, totalCount, err = s.scanAll(ctx, docType, tags, sortBy, page, limit)
	}
	if err != nil {
		return nil, err
	}

	return &model.InsightsResponse{
		Documents:  docs,
		TotalCount: totalCount,
		Page:       page,
		Limit:      limit,
	}, nil
}

// listBySource queries documents from a specific source with optional type/service/tags filters.
// Fetches a generous bound (500, matching scanAll's precedent) rather than
// page*limit: repo.ListDocuments orders by SK (a docHash), which has no
// relationship to crawledAt, so truncating at the DynamoDB query layer
// before sorting would return an arbitrary subset that "newest" sorting can
// only reorder locally -- silently hiding genuinely new documents that
// happened to land outside that arbitrary slice.
func (s *InsightsService) listBySource(ctx context.Context, source, docType, service string, tags []string, sortBy string, page, limit int) ([]model.CrawledDocument, int, error) {
	docs, _, count, err := s.repo.ListDocuments(ctx, source, docType, 500, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to list documents for source %s: %w", source, err)
	}

	if service != "" {
		docs = filterByService(docs, service)
		count = len(docs)
	}

	if len(tags) > 0 {
		docs = filterByTags(docs, tags)
		count = len(docs)
	}

	sortDocuments(docs, sortBy)

	start := (page - 1) * limit
	if start >= len(docs) {
		return []model.CrawledDocument{}, count, nil
	}
	end := start + limit
	if end > len(docs) {
		end = len(docs)
	}
	return docs[start:end], count, nil
}

// scanAll scans all documents filtered by type with pagination.
// Results are cached for 30 seconds to avoid repeated full-table scans.
func (s *InsightsService) scanAll(ctx context.Context, docType string, tags []string, sortBy string, page, limit int) ([]model.CrawledDocument, int, error) {
	if docType == "" {
		docType = "blog"
	}

	cacheKey := "scan:" + docType
	allDocs, total, hit := scanCache.get(cacheKey)
	if !hit {
		var err error
		allDocs, total, err = s.repo.ListAllDocumentsByType(ctx, docType, 500, 0)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to scan documents: %w", err)
		}
		scanCache.set(cacheKey, allDocs, total)
	}

	docs := allDocs
	if len(tags) > 0 {
		docs = filterByTags(docs, tags)
		total = len(docs)
	}

	// Copy before sorting: allDocs may be the cached slice shared across
	// concurrent requests with different sortBy values -- sorting in place
	// would race and leave the cache holding whichever order won last.
	//
	// Sorting BEFORE slicing for pagination matters just as much: allDocs
	// comes from a raw DynamoDB Scan with no chronological guarantee (see
	// ListAllDocumentsByType), so slicing first would return an arbitrary
	// subset that "newest" sorting can only reorder locally -- this was the
	// actual bug (new articles ARE crawled daily, per CrawlHistory, but
	// rarely surfaced because they landed outside whatever arbitrary page-1
	// slice the Scan order produced).
	sorted := make([]model.CrawledDocument, len(docs))
	copy(sorted, docs)
	sortDocuments(sorted, sortBy)
	docs = sorted

	start := (page - 1) * limit
	if start >= len(docs) {
		return []model.CrawledDocument{}, total, nil
	}
	end := start + limit
	if end > len(docs) {
		end = len(docs)
	}
	return docs[start:end], total, nil
}

// filterByService filters documents that contain the specified AWS service.
func filterByService(docs []model.CrawledDocument, service string) []model.CrawledDocument {
	var filtered []model.CrawledDocument
	for _, doc := range docs {
		for _, svc := range doc.AWSServices {
			if svc == service {
				filtered = append(filtered, doc)
				break
			}
		}
	}
	return filtered
}

// filterByTags filters documents that contain ALL specified tags (case-insensitive).
func filterByTags(docs []model.CrawledDocument, tags []string) []model.CrawledDocument {
	lowerTags := make([]string, len(tags))
	for i, t := range tags {
		lowerTags[i] = strings.ToLower(t)
	}

	var filtered []model.CrawledDocument
	for _, doc := range docs {
		if matchesTags(doc.Tags, lowerTags) {
			filtered = append(filtered, doc)
		}
	}
	return filtered
}

func matchesTags(docTags []string, filterTags []string) bool {
	docTagSet := make(map[string]bool, len(docTags))
	for _, t := range docTags {
		docTagSet[strings.ToLower(t)] = true
	}
	for _, ft := range filterTags {
		if !docTagSet[ft] {
			return false
		}
	}
	return true
}

// sortDocuments sorts documents in-place by the specified field.
func sortDocuments(docs []model.CrawledDocument, sortBy string) {
	switch sortBy {
	case "oldest":
		sort.Slice(docs, func(i, j int) bool {
			return docs[i].CrawledAt < docs[j].CrawledAt
		})
	case "title":
		sort.Slice(docs, func(i, j int) bool {
			return docs[i].Title < docs[j].Title
		})
	default: // "newest" or empty
		sort.Slice(docs, func(i, j int) bool {
			return docs[i].CrawledAt > docs[j].CrawledAt
		})
	}
}
