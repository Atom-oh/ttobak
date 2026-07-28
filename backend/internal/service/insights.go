package service

import (
	"context"
	"fmt"
	"io"
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

// InsightsService handles document listing and insights for crawled content.
type InsightsService struct {
	repo         crawlerRepo
	s3Client     *s3.Client
	kbBucketName string
}

// NewInsightsService creates a new InsightsService.
func NewInsightsService(repo *repository.CrawlerRepository, s3Client *s3.Client, kbBucketName string) *InsightsService {
	return &InsightsService{repo: repo, s3Client: s3Client, kbBucketName: kbBucketName}
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

	result, err := s.s3Client.GetObject(ctx, &s3.GetObjectInput{
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
			result, err = s.s3Client.GetObject(ctx, &s3.GetObjectInput{
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
// KB object). Authorization is scoped to the source's subscribers or an
// admin -- unlike GetDocumentContent's open read (insights are shared
// substrate by design), a mutating route must not inherit that open
// posture. The KB vector store is NOT purged here: Bedrock Knowledge Base
// only reconciles deletions on an ingestion job. The handler
// (InsightsHandler.DeleteDocument) triggers one, best-effort, right after
// this call succeeds -- a stale vector can still surface in Q&A until that
// job completes, or if it fails, until the next daily crawl/manual sync.
func (s *InsightsService) DeleteDocument(ctx context.Context, userID string, isAdmin bool, sourceID, docHash string) error {
	source, err := s.repo.GetSource(ctx, sourceID)
	if err != nil {
		return fmt.Errorf("failed to get source: %w", err)
	}
	if source == nil {
		return ErrNotFound
	}
	if !isAdmin && !contains(source.Subscribers, userID) {
		return ErrForbidden
	}

	doc, err := s.repo.GetDocument(ctx, sourceID, docHash)
	if err != nil {
		return fmt.Errorf("failed to get document: %w", err)
	}
	if doc == nil {
		return ErrNotFound
	}

	s3Key := doc.S3Key
	if s3Key == "" {
		s3Key = fmt.Sprintf("shared/news/%s/%s.md", sourceID, docHash)
	}
	if _, err := s.s3Client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.kbBucketName),
		Key:    aws.String(s3Key),
	}); err != nil {
		return fmt.Errorf("failed to delete KB object: %w", err)
	}

	if err := s.repo.DeleteDocument(ctx, sourceID, docHash); err != nil {
		return fmt.Errorf("failed to delete document: %w", err)
	}

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
