package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/go-chi/chi/v5"
	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/service"
)

// ---------------------------------------------------------------------------
// mock repo – implements service.CrawlerRepo (exported alias of crawlerRepo)
// ---------------------------------------------------------------------------

type mockCrawlerRepo struct {
	sources       map[string]*model.CrawlerSource
	subscriptions map[string]*model.CrawlerSubscription // key: "userID|sourceID"
	history       map[string][]model.CrawlHistory
	documents     map[string][]model.CrawledDocument
	allDocuments  []model.CrawledDocument
}

func newMockCrawlerRepo() *mockCrawlerRepo {
	return &mockCrawlerRepo{
		sources:       make(map[string]*model.CrawlerSource),
		subscriptions: make(map[string]*model.CrawlerSubscription),
		history:       make(map[string][]model.CrawlHistory),
		documents:     make(map[string][]model.CrawledDocument),
	}
}

func subKey(userID, sourceID string) string { return userID + "|" + sourceID }

func (m *mockCrawlerRepo) GetSource(_ context.Context, sourceID string) (*model.CrawlerSource, error) {
	src, ok := m.sources[sourceID]
	if !ok {
		return nil, nil
	}
	cp := *src
	cp.Subscribers = append([]string(nil), src.Subscribers...)
	cp.AWSServices = append([]string(nil), src.AWSServices...)
	cp.NewsQueries = append([]string(nil), src.NewsQueries...)
	cp.CustomUrls = append([]string(nil), src.CustomUrls...)
	return &cp, nil
}

func (m *mockCrawlerRepo) PutSource(_ context.Context, source *model.CrawlerSource) error {
	cp := *source
	cp.Subscribers = append([]string(nil), source.Subscribers...)
	cp.AWSServices = append([]string(nil), source.AWSServices...)
	cp.NewsQueries = append([]string(nil), source.NewsQueries...)
	cp.CustomUrls = append([]string(nil), source.CustomUrls...)
	m.sources[source.SourceID] = &cp
	return nil
}

func (m *mockCrawlerRepo) GetSubscription(_ context.Context, userID, sourceID string) (*model.CrawlerSubscription, error) {
	sub, ok := m.subscriptions[subKey(userID, sourceID)]
	if !ok {
		return nil, nil
	}
	cp := *sub
	return &cp, nil
}

func (m *mockCrawlerRepo) PutSubscription(_ context.Context, userID string, sub *model.CrawlerSubscription) error {
	cp := *sub
	m.subscriptions[subKey(userID, sub.SourceID)] = &cp
	return nil
}

func (m *mockCrawlerRepo) DeleteSubscription(_ context.Context, userID, sourceID string) error {
	delete(m.subscriptions, subKey(userID, sourceID))
	return nil
}

func (m *mockCrawlerRepo) ListUserSubscriptions(_ context.Context, userID string) ([]model.CrawlerSubscription, error) {
	var subs []model.CrawlerSubscription
	prefix := userID + "|"
	for k, v := range m.subscriptions {
		if len(k) > len(prefix) && k[:len(prefix)] == prefix {
			subs = append(subs, *v)
		}
	}
	return subs, nil
}

func (m *mockCrawlerRepo) ListHistory(_ context.Context, sourceID string, limit int32) ([]model.CrawlHistory, error) {
	h := m.history[sourceID]
	if limit > 0 && int32(len(h)) > limit {
		h = h[:limit]
	}
	return h, nil
}

func (m *mockCrawlerRepo) ListDocuments(_ context.Context, sourceID, docType string, limit int32, _ map[string]types.AttributeValue) ([]model.CrawledDocument, map[string]types.AttributeValue, int, error) {
	docs := m.documents[sourceID]
	if docType != "" {
		var filtered []model.CrawledDocument
		for _, d := range docs {
			if d.Type == docType {
				filtered = append(filtered, d)
			}
		}
		docs = filtered
	}
	if limit > 0 && int32(len(docs)) > limit {
		docs = docs[:limit]
	}
	return docs, nil, len(docs), nil
}

func (m *mockCrawlerRepo) ListAllDocumentsByType(_ context.Context, docType string, limit int32, page int) ([]model.CrawledDocument, int, error) {
	var docs []model.CrawledDocument
	for _, d := range m.allDocuments {
		if docType == "" || d.Type == docType {
			docs = append(docs, d)
		}
	}
	total := len(docs)
	start := int(limit) * page
	if start > total {
		return []model.CrawledDocument{}, total, nil
	}
	end := start + int(limit)
	if end > total {
		end = total
	}
	return docs[start:end], total, nil
}

func (m *mockCrawlerRepo) GetDocument(_ context.Context, sourceID, docHash string) (*model.CrawledDocument, error) {
	for _, d := range m.documents[sourceID] {
		if d.DocHash == docHash {
			return &d, nil
		}
	}
	return nil, nil
}

func (m *mockCrawlerRepo) NormalizeSourceID(name string) string { return name }

// ---------------------------------------------------------------------------
// test helpers
// ---------------------------------------------------------------------------

const testUserID = "test-user-123"

// withUserContext injects a user ID into the request context, simulating
// the Auth middleware that would normally be applied.
func withUserContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), middleware.UserIDKey, testUserID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// setupCrawlerRouter creates a chi router wired to the given mock repo.
func setupCrawlerRouter(repo *mockCrawlerRepo) http.Handler {
	crawlerSvc := service.NewCrawlerServiceWithRepo(repo)
	h := NewCrawlerHandler(crawlerSvc)

	r := chi.NewRouter()
	r.Use(withUserContext)
	r.Route("/api/crawler/sources", func(r chi.Router) {
		r.Get("/", h.ListSources)
		r.Post("/", h.AddSource)
		r.Put("/{sourceId}", h.UpdateSource)
		r.Delete("/{sourceId}", h.Unsubscribe)
		r.Get("/{sourceId}/history", h.GetHistory)
	})
	return r
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestListSources_200(t *testing.T) {
	repo := newMockCrawlerRepo()
	// Seed a source and subscription for the test user
	repo.sources["hanabank"] = &model.CrawlerSource{
		SourceID:    "hanabank",
		SourceName:  "Hana Bank",
		Subscribers: []string{testUserID},
		AWSServices: []string{"lambda"},
		Schedule:    "daily",
		Status:      "idle",
	}
	repo.subscriptions[subKey(testUserID, "hanabank")] = &model.CrawlerSubscription{
		SourceID:    "hanabank",
		AWSServices: []string{"lambda"},
		AddedAt:     "2026-04-15T00:00:00Z",
	}

	router := setupCrawlerRouter(repo)
	req := httptest.NewRequest(http.MethodGet, "/api/crawler/sources", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var body model.CrawlerSourcesResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(body.Sources) != 1 {
		t.Fatalf("expected 1 source, got %d", len(body.Sources))
	}
	if body.Sources[0].Source.SourceID != "hanabank" {
		t.Errorf("expected sourceID 'hanabank', got %q", body.Sources[0].Source.SourceID)
	}
}

func TestAddSource_201(t *testing.T) {
	repo := newMockCrawlerRepo()
	router := setupCrawlerRouter(repo)

	payload := model.AddCrawlerSourceRequest{
		SourceName:  "AWS Blog",
		AWSServices: []string{"lambda", "s3"},
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/api/crawler/sources", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp model.CrawlerSourceResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Source.SourceID != "aws-blog" {
		t.Errorf("expected sourceID 'aws-blog', got %q", resp.Source.SourceID)
	}
	if resp.Source.Status != "idle" {
		t.Errorf("expected status 'idle', got %q", resp.Source.Status)
	}
}

func TestAddSource_400_MissingName(t *testing.T) {
	repo := newMockCrawlerRepo()
	router := setupCrawlerRouter(repo)

	// sourceName is empty
	payload := model.AddCrawlerSourceRequest{
		SourceName: "",
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/api/crawler/sources", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify error body contains BAD_REQUEST code
	var errResp map[string]map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&errResp); err != nil {
		t.Fatalf("failed to decode error response: %v", err)
	}
	if errResp["error"]["code"] != "BAD_REQUEST" {
		t.Errorf("expected error code BAD_REQUEST, got %q", errResp["error"]["code"])
	}
}

func TestUpdateSource_200(t *testing.T) {
	repo := newMockCrawlerRepo()
	// Seed source + subscription so UpdateSource succeeds
	repo.sources["hanabank"] = &model.CrawlerSource{
		SourceID:    "hanabank",
		SourceName:  "Hana Bank",
		Subscribers: []string{testUserID},
		AWSServices: []string{"lambda"},
		Schedule:    "daily",
		Status:      "idle",
	}
	repo.subscriptions[subKey(testUserID, "hanabank")] = &model.CrawlerSubscription{
		SourceID:    "hanabank",
		AWSServices: []string{"lambda"},
		AddedAt:     "2026-04-15T00:00:00Z",
	}

	router := setupCrawlerRouter(repo)

	payload := model.UpdateCrawlerSourceRequest{
		AWSServices: []string{"lambda", "s3"},
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPut, "/api/crawler/sources/hanabank", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp["status"] != "updated" {
		t.Errorf("expected status 'updated', got %q", resp["status"])
	}
}

func TestUnsubscribe_204(t *testing.T) {
	repo := newMockCrawlerRepo()
	repo.sources["hanabank"] = &model.CrawlerSource{
		SourceID:    "hanabank",
		SourceName:  "Hana Bank",
		Subscribers: []string{testUserID},
		AWSServices: []string{"lambda"},
		Schedule:    "daily",
		Status:      "idle",
	}
	repo.subscriptions[subKey(testUserID, "hanabank")] = &model.CrawlerSubscription{
		SourceID:    "hanabank",
		AWSServices: []string{"lambda"},
	}

	router := setupCrawlerRouter(repo)
	req := httptest.NewRequest(http.MethodDelete, "/api/crawler/sources/hanabank", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify subscription was actually deleted
	if _, ok := repo.subscriptions[subKey(testUserID, "hanabank")]; ok {
		t.Error("expected subscription to be deleted from repo")
	}
}

func TestGetHistory_200(t *testing.T) {
	repo := newMockCrawlerRepo()
	repo.history["hanabank"] = []model.CrawlHistory{
		{Timestamp: "2026-04-15T10:00:00Z", DocsAdded: 5, Duration: 120},
		{Timestamp: "2026-04-14T10:00:00Z", DocsAdded: 3, Duration: 90},
	}

	router := setupCrawlerRouter(repo)
	req := httptest.NewRequest(http.MethodGet, "/api/crawler/sources/hanabank/history", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp model.CrawlHistoryResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(resp.History) != 2 {
		t.Errorf("expected 2 history entries, got %d", len(resp.History))
	}
}
