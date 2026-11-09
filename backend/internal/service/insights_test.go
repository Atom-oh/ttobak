package service

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/ttobak/backend/internal/model"
)

// mockS3GetDeleter is a minimal s3GetDeleter for DeleteDocument happy-path
// tests -- *s3.Client is a concrete type with no test double, so exercising
// the S3-then-DynamoDB ordering, the 2-key fallback, and metadata
// preservation on S3 failure was previously impossible without a live AWS
// connection (see s3GetDeleter's doc comment in insights.go).
type mockS3GetDeleter struct {
	deletedKeys []string
	deleteErr   error
}

func (m *mockS3GetDeleter) GetObject(_ context.Context, _ *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	return nil, errors.New("mockS3GetDeleter.GetObject not implemented")
}

func (m *mockS3GetDeleter) DeleteObject(_ context.Context, params *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	if m.deleteErr != nil {
		return nil, m.deleteErr
	}
	m.deletedKeys = append(m.deletedKeys, aws.ToString(params.Key))
	return &s3.DeleteObjectOutput{}, nil
}

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
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestDeleteDocument_NonOwnerSubscriberForbidden(t *testing.T) {
	repo := newMockCrawlerRepo()
	// someone-else has self-subscribed (AddSource allows this unconditionally)
	// but is not the owner -- must still be forbidden from deleting.
	repo.sources["hanabank"] = &model.CrawlerSource{SourceID: "hanabank", OwnerID: "owner-1", Subscribers: []string{"owner-1", "someone-else"}}
	svc := &InsightsService{repo: repo}
	ctx := context.Background()

	err := svc.DeleteDocument(ctx, "someone-else", false, "hanabank", "doc1")
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("expected ErrForbidden, got %v", err)
	}
}

func TestDeleteDocument_OwnerButDocMissingNotFound(t *testing.T) {
	repo := newMockCrawlerRepo()
	repo.sources["hanabank"] = &model.CrawlerSource{SourceID: "hanabank", OwnerID: "user-1", Subscribers: []string{"user-1"}}
	svc := &InsightsService{repo: repo}
	ctx := context.Background()

	// user-1 IS the owner (passes the permission check) but the
	// requested doc doesn't exist -- must not fall through to S3.
	err := svc.DeleteDocument(ctx, "user-1", false, "hanabank", "no-such-doc")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestDeleteDocument_AdminBypassesOwnerCheck(t *testing.T) {
	repo := newMockCrawlerRepo()
	repo.sources["hanabank"] = &model.CrawlerSource{SourceID: "hanabank", OwnerID: "owner-1", Subscribers: []string{"owner-1"}}
	svc := &InsightsService{repo: repo}
	ctx := context.Background()

	// An admin who is NOT the owner must get past the permission check
	// (proven by getting ErrNotFound for the missing doc, not ErrForbidden).
	err := svc.DeleteDocument(ctx, "admin-1", true, "hanabank", "no-such-doc")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound (admin bypasses owner check), got %v", err)
	}
}

func TestDeleteDocument_EmptyUserIDForbidden(t *testing.T) {
	repo := newMockCrawlerRepo()
	// Legacy source with no backfilled OwnerID -- if an empty userID were
	// allowed to reach the OwnerID comparison, "" == "" would wrongly pass.
	repo.sources["hanabank"] = &model.CrawlerSource{SourceID: "hanabank", Subscribers: []string{"someone"}}
	svc := &InsightsService{repo: repo}
	ctx := context.Background()

	err := svc.DeleteDocument(ctx, "", false, "hanabank", "doc1")
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("expected ErrForbidden for empty userID, got %v", err)
	}
}

func TestDeleteDocument_LegacySourceWithoutOwnerIDDeniesNonAdmin(t *testing.T) {
	repo := newMockCrawlerRepo()
	// A source created before OwnerID existed -- OwnerID == "". Even its
	// own subscriber must not be able to delete; only admin can, until
	// scripts/insights-backfill-owner.py backfills OwnerID.
	repo.sources["hanabank"] = &model.CrawlerSource{SourceID: "hanabank", Subscribers: []string{"subscriber-1"}}
	svc := &InsightsService{repo: repo}
	ctx := context.Background()

	err := svc.DeleteDocument(ctx, "subscriber-1", false, "hanabank", "doc1")
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("expected ErrForbidden for legacy source without OwnerID, got %v", err)
	}
}

func TestDeleteDocument_AdminAllowedOnLegacySourceWithoutOwnerID(t *testing.T) {
	repo := newMockCrawlerRepo()
	repo.sources["hanabank"] = &model.CrawlerSource{SourceID: "hanabank", Subscribers: []string{"subscriber-1"}}
	svc := &InsightsService{repo: repo}
	ctx := context.Background()

	err := svc.DeleteDocument(ctx, "admin-1", true, "hanabank", "no-such-doc")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound (admin allowed even on legacy source), got %v", err)
	}
}

// TestDeleteDocument_HappyPath_DeletesS3ThenRepo covers the destructive
// delete happy path that every other DeleteDocument test above deliberately
// stops short of (they all return before touching S3): S3Key present ->
// exactly that key is deleted, then the DynamoDB metadata is removed.
func TestDeleteDocument_HappyPath_DeletesS3ThenRepo(t *testing.T) {
	repo := newMockCrawlerRepo()
	repo.sources["hanabank"] = &model.CrawlerSource{SourceID: "hanabank", OwnerID: "user-1", Subscribers: []string{"user-1"}}
	repo.documents["hanabank"] = []model.CrawledDocument{
		{DocHash: "doc1", SourceID: "hanabank", S3Key: "shared/news/hanabank/doc1.md"},
	}
	mockS3 := &mockS3GetDeleter{}
	svc := &InsightsService{repo: repo, s3: mockS3, kbBucketName: "kb-bucket"}
	ctx := context.Background()

	if err := svc.DeleteDocument(ctx, "user-1", false, "hanabank", "doc1"); err != nil {
		t.Fatalf("DeleteDocument returned error: %v", err)
	}
	if len(mockS3.deletedKeys) != 1 || mockS3.deletedKeys[0] != "shared/news/hanabank/doc1.md" {
		t.Errorf("expected exactly the stored S3Key to be deleted, got %v", mockS3.deletedKeys)
	}
	remaining, err := repo.GetDocument(ctx, "hanabank", "doc1")
	if err != nil {
		t.Fatalf("GetDocument after delete returned error: %v", err)
	}
	if remaining != nil {
		t.Error("expected document metadata to be removed from repo")
	}
}

// TestDeleteDocument_HappyPath_NoS3KeyDeletesBothFallbackKeys covers older
// documents without a stored S3Key: both candidate keys must be deleted
// unconditionally (idempotent, per the code comment) rather than probed.
func TestDeleteDocument_HappyPath_NoS3KeyDeletesBothFallbackKeys(t *testing.T) {
	repo := newMockCrawlerRepo()
	repo.sources["hanabank"] = &model.CrawlerSource{SourceID: "hanabank", OwnerID: "user-1", Subscribers: []string{"user-1"}}
	repo.documents["hanabank"] = []model.CrawledDocument{
		{DocHash: "doc1", SourceID: "hanabank"}, // no S3Key
	}
	mockS3 := &mockS3GetDeleter{}
	svc := &InsightsService{repo: repo, s3: mockS3, kbBucketName: "kb-bucket"}
	ctx := context.Background()

	if err := svc.DeleteDocument(ctx, "user-1", false, "hanabank", "doc1"); err != nil {
		t.Fatalf("DeleteDocument returned error: %v", err)
	}
	if len(mockS3.deletedKeys) != 2 {
		t.Errorf("expected both fallback keys deleted, got %v", mockS3.deletedKeys)
	}
}

// TestDeleteDocument_S3FailurePreservesMetadata covers the S3-before-
// DynamoDB ordering's whole point: if the S3 delete fails, the DynamoDB
// metadata must survive so the delete is safely retryable, not orphan the
// object with no metadata left pointing at it.
func TestDeleteDocument_S3FailurePreservesMetadata(t *testing.T) {
	repo := newMockCrawlerRepo()
	repo.sources["hanabank"] = &model.CrawlerSource{SourceID: "hanabank", OwnerID: "user-1", Subscribers: []string{"user-1"}}
	repo.documents["hanabank"] = []model.CrawledDocument{
		{DocHash: "doc1", SourceID: "hanabank", S3Key: "shared/news/hanabank/doc1.md"},
	}
	mockS3 := &mockS3GetDeleter{deleteErr: errors.New("s3 unavailable")}
	svc := &InsightsService{repo: repo, s3: mockS3, kbBucketName: "kb-bucket"}
	ctx := context.Background()

	if err := svc.DeleteDocument(ctx, "user-1", false, "hanabank", "doc1"); err == nil {
		t.Fatal("expected an error when the S3 delete fails")
	}
	remaining, err := repo.GetDocument(ctx, "hanabank", "doc1")
	if err != nil {
		t.Fatalf("GetDocument returned error: %v", err)
	}
	if remaining == nil {
		t.Error("expected document metadata to survive an S3 delete failure")
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
	for i := range repo.allDocuments {
		repo.allDocuments[i].SourceID = "hanabank"
	}
	svc.news = legacyNewsRows(t, "hanabank", repo.allDocuments)

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
	svc.news = legacyNewsRows(t, "hanabank", repo.documents["hanabank"])

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

func manyNewsDocs(n int, sourceID string) []model.CrawledDocument {
	docs := make([]model.CrawledDocument, n)
	for i := range docs {
		docs[i] = model.CrawledDocument{
			DocHash: fmt.Sprintf("%016x", i), Type: "news", Title: fmt.Sprintf("Article %d", i),
			Source: sourceID, SourceID: sourceID, CrawledAt: int64(1000 + i),
		}
	}
	return docs
}

func TestListInsights_CrossSourceNewestBeyond500(t *testing.T) {
	scanCache.clear()
	repo := newMockCrawlerRepo()
	repo.allDocuments = manyNewsDocs(600, "hanabank")
	svc := &InsightsService{repo: repo}
	svc.news = legacyNewsRows(t, "hanabank", repo.allDocuments)

	resp, err := svc.ListInsights(context.Background(), "news", "", "", nil, "date", 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Documents) == 0 || resp.Documents[0].DocHash != fmt.Sprintf("%016x", 599) {
		t.Fatalf("newest document beyond index 500 not first: %+v", resp.Documents)
	}
	if len(repo.scanLimits) != 1 || repo.scanLimits[0] > 0 {
		t.Fatalf("expected one all-pages scan (limit <= 0), got %v", repo.scanLimits)
	}
}

func TestListInsights_WithSourceNewestBeyond500(t *testing.T) {
	scanCache.clear()
	repo := newMockCrawlerRepo()
	repo.documents["hanabank"] = manyNewsDocs(600, "hanabank")
	svc := &InsightsService{repo: repo}
	svc.news = legacyNewsRows(t, "hanabank", repo.documents["hanabank"])

	resp, err := svc.ListInsights(context.Background(), "news", "hanabank", "", nil, "date", 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Documents) == 0 || resp.Documents[0].DocHash != fmt.Sprintf("%016x", 599) {
		t.Fatalf("newest document beyond index 500 not first: %+v", resp.Documents)
	}
	if len(repo.listLimits) != 1 || repo.listLimits[0] > 0 {
		t.Fatalf("expected one all-pages list (limit <= 0), got %v", repo.listLimits)
	}
}

func techServiceDocs() []model.CrawledDocument {
	return []model.CrawledDocument{
		{DocHash: "t1", Type: "tech", Title: "EKS 1", SourceID: "s1", CrawledAt: 1, AWSServices: []string{"Amazon EKS"}},
		{DocHash: "t2", Type: "tech", Title: "Lambda 1", SourceID: "s2", CrawledAt: 2, AWSServices: []string{"AWS Lambda"}},
		{DocHash: "t3", Type: "tech", Title: "EKS 2", SourceID: "s3", CrawledAt: 3, AWSServices: []string{"Amazon EKS"}},
	}
}

func TestListInsights_CrossSourceServiceFilter(t *testing.T) {
	scanCache.clear()
	repo := newMockCrawlerRepo()
	repo.allDocuments = techServiceDocs()
	svc := &InsightsService{repo: repo}

	resp, err := svc.ListInsights(context.Background(), "tech", "", "Amazon EKS", nil, "date", 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Documents) != 2 || resp.TotalCount != 2 {
		t.Fatalf("expected 2 EKS docs, got %d (total %d)", len(resp.Documents), resp.TotalCount)
	}
	for _, d := range resp.Documents {
		if d.DocHash == "t2" {
			t.Fatalf("non-EKS doc leaked: %+v", d)
		}
	}
	// The facet keeps showing alternatives while a service filter is active.
	if len(resp.Services) != 2 {
		t.Fatalf("services facet should ignore the service filter, got %v", resp.Services)
	}
}

func TestListInsights_ReturnsServicesFacet(t *testing.T) {
	scanCache.clear()
	repo := newMockCrawlerRepo()
	repo.allDocuments = techServiceDocs()
	svc := &InsightsService{repo: repo}

	resp, err := svc.ListInsights(context.Background(), "tech", "", "", nil, "date", 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Documents) != 1 {
		t.Fatalf("expected one doc on page 1, got %d", len(resp.Documents))
	}
	want := []string{"Amazon EKS", "AWS Lambda"}
	if !reflect.DeepEqual(resp.Services, want) {
		t.Fatalf("services = %v, want %v", resp.Services, want)
	}
}

func TestListInsights_ReturnsSourceIDsFacet(t *testing.T) {
	for _, source := range []string{"", "s1"} {
		scanCache.clear()
		repo := newMockCrawlerRepo()
		repo.allDocuments = techServiceDocs()
		repo.documents["s1"] = []model.CrawledDocument{repo.allDocuments[0]}
		svc := &InsightsService{repo: repo}

		resp, err := svc.ListInsights(context.Background(), "tech", source, "", nil, "date", 1, 1)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"s1", "s2", "s3"}
		if !reflect.DeepEqual(resp.SourceIDs, want) {
			t.Fatalf("source=%q sourceIds = %v, want %v", source, resp.SourceIDs, want)
		}
	}
}

// insightsMembers is a direct-membership reader for account-source deletes.
type insightsMembers struct {
	members map[string]bool // accountID + "/" + userID
	calls   int
	err     error
}

func (m *insightsMembers) GetMember(_ context.Context, accountID, userID string) (*model.AccountMember, error) {
	m.calls++
	if m.err != nil {
		return nil, m.err
	}
	if !m.members[accountID+"/"+userID] {
		return nil, nil
	}
	return &model.AccountMember{UserID: userID}, nil
}

func accountSourceDeleteFixture(members *insightsMembers) (*InsightsService, *mockCrawlerRepo, *mockS3GetDeleter) {
	repo := newMockCrawlerRepo()
	repo.sources["acct_acct-1_0123abcd"] = &model.CrawlerSource{
		SourceID: "acct_acct-1_0123abcd", OwnerID: "creator", AccountID: "acct-1",
	}
	repo.documents["acct_acct-1_0123abcd"] = []model.CrawledDocument{
		{DocHash: "doc1", SourceID: "acct_acct-1_0123abcd", S3Key: "shared/news/acct_acct-1_0123abcd/doc1.md"},
	}
	mockS3 := &mockS3GetDeleter{}
	svc := &InsightsService{repo: repo, s3: mockS3, kbBucketName: "kb-bucket"}
	if members != nil {
		svc.SetAccountMemberReader(members)
	}
	return svc, repo, mockS3
}

// A source creator removed from the account keeps OwnerID on the CONFIG row;
// that must not let them delete the account's news.
func TestDeleteDocument_AccountSourceFormerCreatorForbidden(t *testing.T) {
	for _, tc := range []struct {
		name    string
		members *insightsMembers
	}{
		{name: "not a current member", members: &insightsMembers{members: map[string]bool{}}},
		{name: "no membership reader fails closed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, repo, mockS3 := accountSourceDeleteFixture(tc.members)
			err := svc.DeleteDocument(context.Background(), "creator", false, "acct_acct-1_0123abcd", "doc1")
			if !errors.Is(err, ErrForbidden) {
				t.Fatalf("expected ErrForbidden, got %v", err)
			}
			if len(mockS3.deletedKeys) != 0 {
				t.Fatalf("S3 object deleted for a former creator: %v", mockS3.deletedKeys)
			}
			if doc, _ := repo.GetDocument(context.Background(), "acct_acct-1_0123abcd", "doc1"); doc == nil {
				t.Fatal("document metadata deleted for a former creator")
			}
			if tc.members != nil && tc.members.calls != 1 {
				t.Fatalf("membership must be read per request, got %d reads", tc.members.calls)
			}
		})
	}
}

func TestDeleteDocument_AccountSourceCurrentMemberAllowed(t *testing.T) {
	members := &insightsMembers{members: map[string]bool{"acct-1/teammate": true}}
	svc, repo, mockS3 := accountSourceDeleteFixture(members)
	if err := svc.DeleteDocument(context.Background(), "teammate", false, "acct_acct-1_0123abcd", "doc1"); err != nil {
		t.Fatalf("current member delete failed: %v", err)
	}
	if members.calls != 1 {
		t.Fatalf("expected one membership read, got %d", members.calls)
	}
	if len(mockS3.deletedKeys) != 1 || mockS3.deletedKeys[0] != "shared/news/acct_acct-1_0123abcd/doc1.md" {
		t.Fatalf("unexpected S3 deletes: %v", mockS3.deletedKeys)
	}
	if doc, _ := repo.GetDocument(context.Background(), "acct_acct-1_0123abcd", "doc1"); doc != nil {
		t.Fatal("document metadata was not removed")
	}

	// A membership read failure is surfaced, never treated as a grant.
	members.err = errors.New("dynamodb unavailable")
	svc2, _, s3b := accountSourceDeleteFixture(members)
	if err := svc2.DeleteDocument(context.Background(), "teammate", false, "acct_acct-1_0123abcd", "doc1"); err == nil || errors.Is(err, ErrForbidden) || len(s3b.deletedKeys) != 0 {
		t.Fatalf("membership read error must fail the delete without side effects: %v %v", err, s3b.deletedKeys)
	}
}

func TestDeleteDocument_PersonalSourceOwnerUnchanged(t *testing.T) {
	members := &insightsMembers{members: map[string]bool{}}
	for _, tc := range []struct {
		name    string
		user    string
		admin   bool
		wantErr error
	}{
		{name: "owner deletes", user: "user-1"},
		{name: "non-owner forbidden", user: "someone-else", wantErr: ErrForbidden},
		{name: "admin deletes", user: "admin-1", admin: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newMockCrawlerRepo()
			repo.sources["hanabank"] = &model.CrawlerSource{SourceID: "hanabank", OwnerID: "user-1", Subscribers: []string{"user-1"}}
			repo.documents["hanabank"] = []model.CrawledDocument{{DocHash: "doc1", SourceID: "hanabank", S3Key: "shared/news/hanabank/doc1.md"}}
			svc := &InsightsService{repo: repo, s3: &mockS3GetDeleter{}, kbBucketName: "kb-bucket"}
			svc.SetAccountMemberReader(members)
			err := svc.DeleteDocument(context.Background(), tc.user, tc.admin, "hanabank", "doc1")
			if tc.wantErr == nil && err != nil || tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("expected %v, got %v", tc.wantErr, err)
			}
		})
	}
	if members.calls != 0 {
		t.Fatalf("personal sources must not consult account membership, got %d reads", members.calls)
	}
}
