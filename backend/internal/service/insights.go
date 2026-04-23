package service

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

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

// ListInsights retrieves crawled documents with optional filtering by type, source, service, and tags.
func (s *InsightsService) ListInsights(ctx context.Context, docType, source, service string, tags []string, page, limit int) (*model.InsightsResponse, error) {
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
		docs, totalCount, err = s.listBySource(ctx, source, docType, service, tags, page, limit)
	} else {
		docs, totalCount, err = s.scanAll(ctx, docType, tags, page, limit)
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
func (s *InsightsService) listBySource(ctx context.Context, source, docType, service string, tags []string, page, limit int) ([]model.CrawledDocument, int, error) {
	fetchLimit := int32(page * limit)

	docs, _, count, err := s.repo.ListDocuments(ctx, source, docType, fetchLimit, nil)
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
func (s *InsightsService) scanAll(ctx context.Context, docType string, tags []string, page, limit int) ([]model.CrawledDocument, int, error) {
	if docType == "" {
		docType = "blog"
	}

	docs, total, err := s.repo.ListAllDocumentsByType(ctx, docType, int32(limit), page-1)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to scan documents: %w", err)
	}

	if len(tags) > 0 {
		docs = filterByTags(docs, tags)
		total = len(docs)
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
