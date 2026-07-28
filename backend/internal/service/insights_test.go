package service

import (
	"context"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

func TestListInsights_WithSourceFilter(t *testing.T) {
	scanCache.clear()
	repo := newMockCrawlerRepo()
	svc := &InsightsService{repo: repo}
	ctx := context.Background()

	// Seed documents for a specific source
	repo.documents["aws-blog"] = []model.CrawledDocument{
		{DocHash: "doc1", Type: "blog", Title: "Lambda Update", Source: "aws-blog", AWSServices: []string{"lambda"}},
		{DocHash: "doc2", Type: "blog", Title: "S3 News", Source: "aws-blog", AWSServices: []string{"s3"}},
		{DocHash: "doc3", Type: "announcement", Title: "EC2 Launch", Source: "aws-blog", AWSServices: []string{"ec2"}},
	}

	resp, err := svc.ListInsights(ctx, "blog", "aws-blog", "", nil, "", 1, 20)
	if err != nil {
		t.Fatalf("ListInsights returned error: %v", err)
	}

	// Should return only blog-type documents from aws-blog
	if len(resp.Documents) != 2 {
		t.Errorf("expected 2 blog documents, got %d", len(resp.Documents))
	}

	if resp.Page != 1 {
		t.Errorf("expected page 1, got %d", resp.Page)
	}
	if resp.Limit != 20 {
		t.Errorf("expected limit 20, got %d", resp.Limit)
	}
}

func TestListInsights_WithSourceAndServiceFilter(t *testing.T) {
	scanCache.clear()
	repo := newMockCrawlerRepo()
	svc := &InsightsService{repo: repo}
	ctx := context.Background()

	repo.documents["aws-blog"] = []model.CrawledDocument{
		{DocHash: "doc1", Type: "blog", Title: "Lambda Update", Source: "aws-blog", AWSServices: []string{"lambda"}},
		{DocHash: "doc2", Type: "blog", Title: "S3 News", Source: "aws-blog", AWSServices: []string{"s3"}},
		{DocHash: "doc3", Type: "blog", Title: "Lambda Tips", Source: "aws-blog", AWSServices: []string{"lambda", "dynamodb"}},
	}

	resp, err := svc.ListInsights(ctx, "blog", "aws-blog", "lambda", nil, "", 1, 20)
	if err != nil {
		t.Fatalf("ListInsights returned error: %v", err)
	}

	// Should return only blog docs that mention lambda
	if len(resp.Documents) != 2 {
		t.Errorf("expected 2 lambda blog documents, got %d", len(resp.Documents))
	}

	for _, doc := range resp.Documents {
		hasLambda := false
		for _, svc := range doc.AWSServices {
			if svc == "lambda" {
				hasLambda = true
				break
			}
		}
		if !hasLambda {
			t.Errorf("document %q should have lambda in awsServices, got %v", doc.Title, doc.AWSServices)
		}
	}
}

func TestListInsights_CrossSourceByType(t *testing.T) {
	scanCache.clear()
	repo := newMockCrawlerRepo()
	svc := &InsightsService{repo: repo}
	ctx := context.Background()

	// Seed documents across sources using allDocuments (for scan)
	repo.allDocuments = []model.CrawledDocument{
		{DocHash: "doc1", Type: "blog", Title: "Lambda Update", Source: "aws-blog"},
		{DocHash: "doc2", Type: "announcement", Title: "EC2 Launch", Source: "aws-blog"},
		{DocHash: "doc3", Type: "blog", Title: "Cloud Trends", Source: "techcrunch"},
		{DocHash: "doc4", Type: "blog", Title: "AI News", Source: "techcrunch"},
	}

	// No source filter => scans all documents by type
	resp, err := svc.ListInsights(ctx, "blog", "", "", nil, "", 1, 20)
	if err != nil {
		t.Fatalf("ListInsights returned error: %v", err)
	}

	// Should return 3 blog documents across sources
	if len(resp.Documents) != 3 {
		t.Errorf("expected 3 blog documents across sources, got %d", len(resp.Documents))
	}

	if resp.TotalCount != 3 {
		t.Errorf("expected totalCount 3, got %d", resp.TotalCount)
	}
}

func TestListInsights_CrossSource_DefaultType(t *testing.T) {
	scanCache.clear()
	repo := newMockCrawlerRepo()
	svc := &InsightsService{repo: repo}
	ctx := context.Background()

	repo.allDocuments = []model.CrawledDocument{
		{DocHash: "doc1", Type: "blog", Title: "Post 1", Source: "src1"},
		{DocHash: "doc2", Type: "announcement", Title: "Post 2", Source: "src1"},
	}

	// Empty type => defaults to "blog" in scanAll
	resp, err := svc.ListInsights(ctx, "", "", "", nil, "", 1, 20)
	if err != nil {
		t.Fatalf("ListInsights returned error: %v", err)
	}

	// Should return only blog type (default)
	if len(resp.Documents) != 1 {
		t.Errorf("expected 1 blog document (default type), got %d", len(resp.Documents))
	}
}

// DeleteDocument authorization -- these cases all return before touching S3
// (source/doc-existence and permission checks run first), so they don't
// need an s3.Client. The actual S3 DeleteObject + repo delete on the happy
// path isn't covered here, matching this file's existing scope (S3 I/O
// isn't unit-tested for GetDocumentDetail either).

func TestDeleteDocument_UnknownSourceNotFound(t *testing.T) {
	repo := newMockCrawlerRepo()
	svc := &InsightsService{repo: repo}
	ctx := context.Background()

	err := svc.DeleteDocument(ctx, "user-1", false, "no-such-source", "doc1")
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestDeleteDocument_NonSubscriberForbidden(t *testing.T) {
	repo := newMockCrawlerRepo()
	repo.sources["hanabank"] = &model.CrawlerSource{SourceID: "hanabank", Subscribers: []string{"owner-1"}}
	svc := &InsightsService{repo: repo}
	ctx := context.Background()

	err := svc.DeleteDocument(ctx, "someone-else", false, "hanabank", "doc1")
	if err != ErrForbidden {
		t.Errorf("expected ErrForbidden, got %v", err)
	}
}

func TestDeleteDocument_SubscriberButDocMissingNotFound(t *testing.T) {
	repo := newMockCrawlerRepo()
	repo.sources["hanabank"] = &model.CrawlerSource{SourceID: "hanabank", Subscribers: []string{"user-1"}}
	svc := &InsightsService{repo: repo}
	ctx := context.Background()

	// user-1 IS a subscriber (passes the permission check) but the
	// requested doc doesn't exist -- must not fall through to S3.
	err := svc.DeleteDocument(ctx, "user-1", false, "hanabank", "no-such-doc")
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestDeleteDocument_AdminBypassesSubscriberCheck(t *testing.T) {
	repo := newMockCrawlerRepo()
	repo.sources["hanabank"] = &model.CrawlerSource{SourceID: "hanabank", Subscribers: []string{"owner-1"}}
	svc := &InsightsService{repo: repo}
	ctx := context.Background()

	// An admin who is NOT a subscriber must get past the permission check
	// (proven by getting ErrNotFound for the missing doc, not ErrForbidden).
	err := svc.DeleteDocument(ctx, "admin-1", true, "hanabank", "no-such-doc")
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound (admin bypasses subscriber check), got %v", err)
	}
}

func TestListInsights_PaginationDefaults(t *testing.T) {
	scanCache.clear()
	repo := newMockCrawlerRepo()
	svc := &InsightsService{repo: repo}
	ctx := context.Background()

	repo.allDocuments = []model.CrawledDocument{
		{DocHash: "doc1", Type: "blog", Title: "Post 1", Source: "src1"},
	}

	// page=0 and limit=0 should default to page=1, limit=20
	resp, err := svc.ListInsights(ctx, "blog", "", "", nil, "", 0, 0)
	if err != nil {
		t.Fatalf("ListInsights returned error: %v", err)
	}

	if resp.Page != 1 {
		t.Errorf("expected page default 1, got %d", resp.Page)
	}
	if resp.Limit != 20 {
		t.Errorf("expected limit default 20, got %d", resp.Limit)
	}
}

func TestListInsights_PaginationDefaults_Negative(t *testing.T) {
	scanCache.clear()
	repo := newMockCrawlerRepo()
	svc := &InsightsService{repo: repo}
	ctx := context.Background()

	repo.allDocuments = []model.CrawledDocument{}

	// Negative values should also default
	resp, err := svc.ListInsights(ctx, "blog", "", "", nil, "", -5, -10)
	if err != nil {
		t.Fatalf("ListInsights returned error: %v", err)
	}

	if resp.Page != 1 {
		t.Errorf("expected page default 1 for negative input, got %d", resp.Page)
	}
	if resp.Limit != 20 {
		t.Errorf("expected limit default 20 for negative input, got %d", resp.Limit)
	}
}

func TestListInsights_LimitCap(t *testing.T) {
	scanCache.clear()
	repo := newMockCrawlerRepo()
	svc := &InsightsService{repo: repo}
	ctx := context.Background()

	repo.allDocuments = []model.CrawledDocument{}

	// limit > 50 should be capped to 50
	resp, err := svc.ListInsights(ctx, "blog", "", "", nil, "", 1, 100)
	if err != nil {
		t.Fatalf("ListInsights returned error: %v", err)
	}

	if resp.Limit != 50 {
		t.Errorf("expected limit capped to 50, got %d", resp.Limit)
	}
}

func TestListInsights_LimitCapBoundary(t *testing.T) {
	scanCache.clear()
	repo := newMockCrawlerRepo()
	svc := &InsightsService{repo: repo}
	ctx := context.Background()

	repo.allDocuments = []model.CrawledDocument{}

	// limit = 50 should stay at 50 (boundary)
	resp, err := svc.ListInsights(ctx, "blog", "", "", nil, "", 1, 50)
	if err != nil {
		t.Fatalf("ListInsights returned error: %v", err)
	}

	if resp.Limit != 50 {
		t.Errorf("expected limit 50, got %d", resp.Limit)
	}

	// limit = 51 should be capped
	resp, err = svc.ListInsights(ctx, "blog", "", "", nil, "", 1, 51)
	if err != nil {
		t.Fatalf("ListInsights returned error: %v", err)
	}

	if resp.Limit != 50 {
		t.Errorf("expected limit capped to 50, got %d", resp.Limit)
	}
}

func TestListInsights_Pagination(t *testing.T) {
	scanCache.clear()
	repo := newMockCrawlerRepo()
	svc := &InsightsService{repo: repo}
	ctx := context.Background()

	// Seed 5 documents
	repo.allDocuments = make([]model.CrawledDocument, 5)
	for i := 0; i < 5; i++ {
		repo.allDocuments[i] = model.CrawledDocument{
			DocHash: "doc" + string(rune('1'+i)),
			Type:    "blog",
			Title:   "Post " + string(rune('1'+i)),
			Source:  "src1",
		}
	}

	// Page 1, limit 2 => first 2 docs
	resp, err := svc.ListInsights(ctx, "blog", "", "", nil, "", 1, 2)
	if err != nil {
		t.Fatalf("ListInsights page 1 returned error: %v", err)
	}

	if len(resp.Documents) != 2 {
		t.Errorf("expected 2 documents on page 1, got %d", len(resp.Documents))
	}
	if resp.TotalCount != 5 {
		t.Errorf("expected totalCount 5, got %d", resp.TotalCount)
	}

	// Page 3, limit 2 => last 1 doc
	resp, err = svc.ListInsights(ctx, "blog", "", "", nil, "", 3, 2)
	if err != nil {
		t.Fatalf("ListInsights page 3 returned error: %v", err)
	}

	if len(resp.Documents) != 1 {
		t.Errorf("expected 1 document on page 3, got %d", len(resp.Documents))
	}

	// Page 4, limit 2 => 0 docs (past the end)
	resp, err = svc.ListInsights(ctx, "blog", "", "", nil, "", 4, 2)
	if err != nil {
		t.Fatalf("ListInsights page 4 returned error: %v", err)
	}

	if len(resp.Documents) != 0 {
		t.Errorf("expected 0 documents on page 4, got %d", len(resp.Documents))
	}
}

func TestListInsights_EmptyResult(t *testing.T) {
	scanCache.clear()
	repo := newMockCrawlerRepo()
	svc := &InsightsService{repo: repo}
	ctx := context.Background()

	resp, err := svc.ListInsights(ctx, "blog", "nonexistent", "", nil, "", 1, 20)
	if err != nil {
		t.Fatalf("ListInsights returned error: %v", err)
	}

	if len(resp.Documents) != 0 {
		t.Errorf("expected 0 documents, got %d", len(resp.Documents))
	}
}

// TestListInsights_SortsBeforePaginating_CrossSource is a regression test
// for the bug reported as "new news isn't showing up" in production:
// scanAll used to slice the DynamoDB Scan's arbitrary result order into a
// page BEFORE sorting by crawledAt, so a genuinely new document could land
// outside page 1 purely by chance and never surface. allDocuments here is
// seeded in an order that does NOT match recency (oldest-first, reversed
// from crawledAt descending) to reproduce that arbitrary Scan ordering.
func TestListInsights_SortsBeforePaginating_CrossSource(t *testing.T) {
	scanCache.clear()
	repo := newMockCrawlerRepo()
	svc := &InsightsService{repo: repo}
	ctx := context.Background()

	// Seeded oldest -> newest (the opposite of what "newest" sort should
	// return), simulating an unordered Scan result.
	repo.allDocuments = []model.CrawledDocument{
		{DocHash: "old1", Type: "news", Title: "Old Article 1", CrawledAt: 100},
		{DocHash: "old2", Type: "news", Title: "Old Article 2", CrawledAt: 200},
		{DocHash: "old3", Type: "news", Title: "Old Article 3", CrawledAt: 300},
		{DocHash: "new1", Type: "news", Title: "Brand New Article", CrawledAt: 999},
	}

	resp, err := svc.ListInsights(ctx, "news", "", "", nil, "", 1, 2)
	if err != nil {
		t.Fatalf("ListInsights returned error: %v", err)
	}
	if len(resp.Documents) != 2 {
		t.Fatalf("expected 2 documents on page 1, got %d", len(resp.Documents))
	}
	if resp.Documents[0].DocHash != "new1" {
		t.Errorf("expected the newest document (new1) first on page 1, got %q -- pagination is truncating before sorting", resp.Documents[0].DocHash)
	}
	if resp.Documents[1].DocHash != "old3" {
		t.Errorf("expected old3 second (next-newest by crawledAt), got %q", resp.Documents[1].DocHash)
	}
}

// TestListInsights_SortsBeforePaginating_WithSource is the same regression,
// scoped to a single source (listBySource path, which queries by SK/docHash
// order -- also unrelated to crawledAt).
func TestListInsights_SortsBeforePaginating_WithSource(t *testing.T) {
	scanCache.clear()
	repo := newMockCrawlerRepo()
	svc := &InsightsService{repo: repo}
	ctx := context.Background()

	repo.documents["hanabank"] = []model.CrawledDocument{
		{DocHash: "old1", Type: "news", Title: "Old Article 1", Source: "hanabank", CrawledAt: 100},
		{DocHash: "old2", Type: "news", Title: "Old Article 2", Source: "hanabank", CrawledAt: 200},
		{DocHash: "old3", Type: "news", Title: "Old Article 3", Source: "hanabank", CrawledAt: 300},
		{DocHash: "new1", Type: "news", Title: "Brand New Article", Source: "hanabank", CrawledAt: 999},
	}

	resp, err := svc.ListInsights(ctx, "news", "hanabank", "", nil, "", 1, 2)
	if err != nil {
		t.Fatalf("ListInsights returned error: %v", err)
	}
	if len(resp.Documents) != 2 {
		t.Fatalf("expected 2 documents on page 1, got %d", len(resp.Documents))
	}
	if resp.Documents[0].DocHash != "new1" {
		t.Errorf("expected the newest document (new1) first on page 1, got %q -- pagination is truncating before sorting", resp.Documents[0].DocHash)
	}
}
