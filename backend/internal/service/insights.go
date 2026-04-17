package service

import (
	"context"
	"fmt"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// InsightsService handles document listing and insights for crawled content.
type InsightsService struct {
	repo *repository.CrawlerRepository
}

// NewInsightsService creates a new InsightsService.
func NewInsightsService(repo *repository.CrawlerRepository) *InsightsService {
	return &InsightsService{repo: repo}
}

// ListInsights retrieves crawled documents with optional filtering by type, source, and service.
// Defaults: page=1, limit=20, max limit=50.
func (s *InsightsService) ListInsights(ctx context.Context, docType, source, service string, page, limit int) (*model.InsightsResponse, error) {
	// Apply defaults
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
		// Query a specific source's documents
		docs, totalCount, err = s.listBySource(ctx, source, docType, service, page, limit)
	} else {
		// Scan all documents by type
		docs, totalCount, err = s.scanAll(ctx, docType, page, limit)
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

// listBySource queries documents from a specific source with optional type/service filters.
func (s *InsightsService) listBySource(ctx context.Context, source, docType, service string, page, limit int) ([]model.CrawledDocument, int, error) {
	// Fetch documents from the source; use the repository's ListDocuments which supports
	// optional docType filtering. We fetch page*limit items and paginate in-memory.
	fetchLimit := int32(page * limit)

	docs, _, count, err := s.repo.ListDocuments(ctx, source, docType, fetchLimit, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to list documents for source %s: %w", source, err)
	}

	// Apply in-memory service filter if specified
	if service != "" {
		docs = filterByService(docs, service)
		count = len(docs)
	}

	// Apply pagination offset
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
func (s *InsightsService) scanAll(ctx context.Context, docType string, page, limit int) ([]model.CrawledDocument, int, error) {
	if docType == "" {
		docType = "blog" // default document type
	}

	// Use the repository's ListAllDocumentsByType which handles full-table scan + pagination.
	// The repo uses 0-based page offset, while our API uses 1-based page.
	docs, total, err := s.repo.ListAllDocumentsByType(ctx, docType, int32(limit), page-1)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to scan documents: %w", err)
	}

	return docs, total, nil
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
