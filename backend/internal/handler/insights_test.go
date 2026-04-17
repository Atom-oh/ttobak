package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/service"
)

// setupInsightsRouter creates a chi router wired to the given mock repo.
func setupInsightsRouter(repo *mockCrawlerRepo) http.Handler {
	insightsSvc := service.NewInsightsServiceWithRepo(repo)
	h := NewInsightsHandler(insightsSvc)

	r := chi.NewRouter()
	r.Use(withUserContext)
	r.Get("/api/insights", h.ListInsights)
	return r
}

func TestListInsights_200_News(t *testing.T) {
	repo := newMockCrawlerRepo()
	repo.allDocuments = []model.CrawledDocument{
		{DocHash: "d1", Type: "news", Title: "Breaking news", Source: "reuters"},
		{DocHash: "d2", Type: "news", Title: "Market update", Source: "reuters"},
		{DocHash: "d3", Type: "blog", Title: "Tech blog", Source: "medium"},
	}

	router := setupInsightsRouter(repo)
	req := httptest.NewRequest(http.MethodGet, "/api/insights?type=news", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp model.InsightsResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(resp.Documents) != 2 {
		t.Errorf("expected 2 news documents, got %d", len(resp.Documents))
	}
	for _, doc := range resp.Documents {
		if doc.Type != "news" {
			t.Errorf("expected type 'news', got %q", doc.Type)
		}
	}
}

func TestListInsights_200_DefaultType(t *testing.T) {
	repo := newMockCrawlerRepo()
	repo.allDocuments = []model.CrawledDocument{
		{DocHash: "d1", Type: "blog", Title: "Blog post", Source: "medium"},
		{DocHash: "d2", Type: "announcement", Title: "Announcement", Source: "aws"},
	}

	router := setupInsightsRouter(repo)
	// No type parameter — handler passes empty string to service, which defaults to "blog"
	req := httptest.NewRequest(http.MethodGet, "/api/insights", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp model.InsightsResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	// Default type is "blog" in InsightsService.scanAll
	if len(resp.Documents) != 1 {
		t.Errorf("expected 1 document (default type=blog), got %d", len(resp.Documents))
	}
	if len(resp.Documents) > 0 && resp.Documents[0].Type != "blog" {
		t.Errorf("expected type 'blog', got %q", resp.Documents[0].Type)
	}
}

func TestListInsights_Pagination(t *testing.T) {
	repo := newMockCrawlerRepo()
	// Seed 10 tech documents
	for i := 0; i < 10; i++ {
		repo.allDocuments = append(repo.allDocuments, model.CrawledDocument{
			DocHash: "d" + string(rune('0'+i)),
			Type:    "tech",
			Title:   "Tech post",
			Source:  "src",
		})
	}

	router := setupInsightsRouter(repo)
	req := httptest.NewRequest(http.MethodGet, "/api/insights?type=tech&page=2&limit=5", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp model.InsightsResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.Page != 2 {
		t.Errorf("expected page 2, got %d", resp.Page)
	}
	if resp.Limit != 5 {
		t.Errorf("expected limit 5, got %d", resp.Limit)
	}
	if len(resp.Documents) != 5 {
		t.Errorf("expected 5 documents on page 2, got %d", len(resp.Documents))
	}
	if resp.TotalCount != 10 {
		t.Errorf("expected totalCount 10, got %d", resp.TotalCount)
	}
}
