package service

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
)

// mockCrawlerRepo is an in-memory implementation of crawlerRepo for testing.
type mockCrawlerRepo struct {
	sources       map[string]*model.CrawlerSource       // sourceID -> source
	subscriptions map[string]*model.CrawlerSubscription  // "userID|sourceID" -> sub
	history       map[string][]model.CrawlHistory        // sourceID -> history entries
	documents     map[string][]model.CrawledDocument      // sourceID -> documents
	allDocuments  []model.CrawledDocument                 // all documents for scan
}

func newMockCrawlerRepo() *mockCrawlerRepo {
	return &mockCrawlerRepo{
		sources:       make(map[string]*model.CrawlerSource),
		subscriptions: make(map[string]*model.CrawlerSubscription),
		history:       make(map[string][]model.CrawlHistory),
		documents:     make(map[string][]model.CrawledDocument),
	}
}

func subKey(userID, sourceID string) string {
	return userID + "|" + sourceID
}

func (m *mockCrawlerRepo) GetSource(_ context.Context, sourceID string) (*model.CrawlerSource, error) {
	src, ok := m.sources[sourceID]
	if !ok {
		return nil, nil
	}
	// Return a copy to avoid aliasing issues
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
	for k, v := range m.subscriptions {
		// key format: "userID|sourceID"
		if len(k) > len(userID)+1 && k[:len(userID)+1] == userID+"|" {
			subs = append(subs, *v)
		}
	}
	return subs, nil
}

func (m *mockCrawlerRepo) ListHistory(_ context.Context, sourceID string, limit int32) ([]model.CrawlHistory, error) {
	h := m.history[sourceID]
	if int32(len(h)) > limit && limit > 0 {
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

func (m *mockCrawlerRepo) NormalizeSourceID(name string) string {
	return name // unused by service (service uses its own normalizeSourceID)
}

// ---------- helpers ----------

func strSliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	set := make(map[string]struct{}, len(a))
	for _, v := range a {
		set[v] = struct{}{}
	}
	for _, v := range b {
		if _, ok := set[v]; !ok {
			return false
		}
	}
	return true
}

func strSliceContains(slice []string, val string) bool {
	for _, s := range slice {
		if s == val {
			return true
		}
	}
	return false
}

// ---------- Tests ----------

func TestAddSource_NewSource(t *testing.T) {
	repo := newMockCrawlerRepo()
	svc := &CrawlerService{repo: repo}
	ctx := context.Background()

	req := &model.AddCrawlerSourceRequest{
		SourceName:  "AWS Blog",
		AWSServices: []string{"lambda", "s3"},
		NewsQueries: []string{"serverless"},
	}

	resp, err := svc.AddSource(ctx, "user1", req)
	if err != nil {
		t.Fatalf("AddSource returned error: %v", err)
	}

	// sourceID should be normalized
	if resp.Source.SourceID != "aws-blog" {
		t.Errorf("expected sourceID 'aws-blog', got %q", resp.Source.SourceID)
	}

	// Source should have user1 as subscriber
	if !strSliceContains(resp.Source.Subscribers, "user1") {
		t.Errorf("expected user1 in subscribers, got %v", resp.Source.Subscribers)
	}

	// Status should be "idle"
	if resp.Source.Status != "idle" {
		t.Errorf("expected status 'idle', got %q", resp.Source.Status)
	}

	// Schedule should be "daily"
	if resp.Source.Schedule != "daily" {
		t.Errorf("expected schedule 'daily', got %q", resp.Source.Schedule)
	}

	// AWSServices should match
	if !strSliceEqual(resp.Source.AWSServices, []string{"lambda", "s3"}) {
		t.Errorf("expected awsServices [lambda, s3], got %v", resp.Source.AWSServices)
	}

	// Subscription should be created
	if resp.Subscription.SourceID != "aws-blog" {
		t.Errorf("expected subscription sourceID 'aws-blog', got %q", resp.Subscription.SourceID)
	}
	if resp.Subscription.AddedAt == "" {
		t.Error("expected subscription addedAt to be set")
	}

	// Verify stored in repo
	stored := repo.sources["aws-blog"]
	if stored == nil {
		t.Fatal("source not stored in repo")
	}
}

func TestAddSource_ExistingSource_NewSubscriber(t *testing.T) {
	repo := newMockCrawlerRepo()
	svc := &CrawlerService{repo: repo}
	ctx := context.Background()

	// Seed an existing source with user1
	repo.sources["aws-blog"] = &model.CrawlerSource{
		SourceID:    "aws-blog",
		SourceName:  "AWS Blog",
		Subscribers: []string{"user1"},
		AWSServices: []string{"lambda"},
		Schedule:    "daily",
		Status:      "idle",
	}
	repo.subscriptions[subKey("user1", "aws-blog")] = &model.CrawlerSubscription{
		SourceID:    "aws-blog",
		AWSServices: []string{"lambda"},
	}

	// user2 subscribes with additional services
	req := &model.AddCrawlerSourceRequest{
		SourceName:  "AWS Blog",
		AWSServices: []string{"lambda", "s3", "ec2"},
	}

	resp, err := svc.AddSource(ctx, "user2", req)
	if err != nil {
		t.Fatalf("AddSource returned error: %v", err)
	}

	// Both users should be subscribers
	if len(resp.Source.Subscribers) != 2 {
		t.Fatalf("expected 2 subscribers, got %d: %v", len(resp.Source.Subscribers), resp.Source.Subscribers)
	}
	if !strSliceContains(resp.Source.Subscribers, "user1") || !strSliceContains(resp.Source.Subscribers, "user2") {
		t.Errorf("expected both user1 and user2 in subscribers, got %v", resp.Source.Subscribers)
	}

	// AWSServices should be the union: lambda, s3, ec2
	if !strSliceEqual(resp.Source.AWSServices, []string{"lambda", "s3", "ec2"}) {
		t.Errorf("expected awsServices union [lambda, s3, ec2], got %v", resp.Source.AWSServices)
	}

	// user2's subscription should exist
	sub2 := repo.subscriptions[subKey("user2", "aws-blog")]
	if sub2 == nil {
		t.Fatal("user2 subscription not stored")
	}
}

func TestAddSource_ExistingSource_AlreadySubscribed(t *testing.T) {
	repo := newMockCrawlerRepo()
	svc := &CrawlerService{repo: repo}
	ctx := context.Background()

	// Seed existing source with user1 already subscribed
	repo.sources["aws-blog"] = &model.CrawlerSource{
		SourceID:    "aws-blog",
		SourceName:  "AWS Blog",
		Subscribers: []string{"user1"},
		AWSServices: []string{"lambda"},
		Schedule:    "daily",
		Status:      "idle",
	}
	repo.subscriptions[subKey("user1", "aws-blog")] = &model.CrawlerSubscription{
		SourceID:    "aws-blog",
		AWSServices: []string{"lambda"},
	}

	// user1 subscribes again (should not duplicate)
	req := &model.AddCrawlerSourceRequest{
		SourceName:  "AWS Blog",
		AWSServices: []string{"lambda", "s3"},
	}

	resp, err := svc.AddSource(ctx, "user1", req)
	if err != nil {
		t.Fatalf("AddSource returned error: %v", err)
	}

	// Should still have only 1 subscriber
	if len(resp.Source.Subscribers) != 1 {
		t.Errorf("expected 1 subscriber (no duplicate), got %d: %v", len(resp.Source.Subscribers), resp.Source.Subscribers)
	}

	// AWSServices should still be union: lambda, s3
	if !strSliceEqual(resp.Source.AWSServices, []string{"lambda", "s3"}) {
		t.Errorf("expected awsServices union [lambda, s3], got %v", resp.Source.AWSServices)
	}
}

func TestUpdateSource_UpdatesSubscriptionAndRebuildsUnion(t *testing.T) {
	repo := newMockCrawlerRepo()
	svc := &CrawlerService{repo: repo}
	ctx := context.Background()

	// Seed source with two subscribers
	repo.sources["aws-blog"] = &model.CrawlerSource{
		SourceID:    "aws-blog",
		SourceName:  "AWS Blog",
		Subscribers: []string{"user1", "user2"},
		AWSServices: []string{"lambda", "s3", "ec2"},
		Schedule:    "daily",
		Status:      "idle",
	}
	repo.subscriptions[subKey("user1", "aws-blog")] = &model.CrawlerSubscription{
		SourceID:    "aws-blog",
		AWSServices: []string{"lambda", "s3"},
	}
	repo.subscriptions[subKey("user2", "aws-blog")] = &model.CrawlerSubscription{
		SourceID:    "aws-blog",
		AWSServices: []string{"ec2"},
	}

	// user1 updates their subscription — removes s3, adds dynamodb
	req := &model.UpdateCrawlerSourceRequest{
		AWSServices: []string{"lambda", "dynamodb"},
	}

	err := svc.UpdateSource(ctx, "user1", "aws-blog", req)
	if err != nil {
		t.Fatalf("UpdateSource returned error: %v", err)
	}

	// user1's subscription should be updated
	sub1 := repo.subscriptions[subKey("user1", "aws-blog")]
	if !strSliceEqual(sub1.AWSServices, []string{"lambda", "dynamodb"}) {
		t.Errorf("expected user1 awsServices [lambda, dynamodb], got %v", sub1.AWSServices)
	}

	// Source union should be rebuilt: lambda, dynamodb (user1) + ec2 (user2)
	source := repo.sources["aws-blog"]
	if !strSliceEqual(source.AWSServices, []string{"lambda", "dynamodb", "ec2"}) {
		t.Errorf("expected source awsServices union [lambda, dynamodb, ec2], got %v", source.AWSServices)
	}
}

func TestUpdateSource_NotFound(t *testing.T) {
	repo := newMockCrawlerRepo()
	svc := &CrawlerService{repo: repo}
	ctx := context.Background()

	req := &model.UpdateCrawlerSourceRequest{
		AWSServices: []string{"lambda"},
	}

	err := svc.UpdateSource(ctx, "user1", "nonexistent", req)
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestUnsubscribe_LastSubscriber(t *testing.T) {
	repo := newMockCrawlerRepo()
	svc := &CrawlerService{repo: repo}
	ctx := context.Background()

	// Seed source with only user1
	repo.sources["aws-blog"] = &model.CrawlerSource{
		SourceID:    "aws-blog",
		SourceName:  "AWS Blog",
		Subscribers: []string{"user1"},
		AWSServices: []string{"lambda", "s3"},
		NewsQueries: []string{"serverless"},
		CustomUrls:  []string{"https://example.com"},
		Schedule:    "daily",
		Status:      "idle",
	}
	repo.subscriptions[subKey("user1", "aws-blog")] = &model.CrawlerSubscription{
		SourceID:    "aws-blog",
		AWSServices: []string{"lambda", "s3"},
	}

	err := svc.Unsubscribe(ctx, "user1", "aws-blog")
	if err != nil {
		t.Fatalf("Unsubscribe returned error: %v", err)
	}

	// Source should be "disabled"
	source := repo.sources["aws-blog"]
	if source.Status != "disabled" {
		t.Errorf("expected status 'disabled', got %q", source.Status)
	}

	// Subscribers should be empty
	if len(source.Subscribers) != 0 {
		t.Errorf("expected 0 subscribers, got %d", len(source.Subscribers))
	}

	// AWSServices, NewsQueries, CustomUrls should be nil/cleared
	if source.AWSServices != nil {
		t.Errorf("expected awsServices nil, got %v", source.AWSServices)
	}
	if source.NewsQueries != nil {
		t.Errorf("expected newsQueries nil, got %v", source.NewsQueries)
	}
	if source.CustomUrls != nil {
		t.Errorf("expected customUrls nil, got %v", source.CustomUrls)
	}

	// Subscription should be deleted
	if _, ok := repo.subscriptions[subKey("user1", "aws-blog")]; ok {
		t.Error("expected subscription to be deleted")
	}
}

func TestUnsubscribe_OtherSubscribersRemain(t *testing.T) {
	repo := newMockCrawlerRepo()
	svc := &CrawlerService{repo: repo}
	ctx := context.Background()

	// Seed source with two subscribers
	repo.sources["aws-blog"] = &model.CrawlerSource{
		SourceID:    "aws-blog",
		SourceName:  "AWS Blog",
		Subscribers: []string{"user1", "user2"},
		AWSServices: []string{"lambda", "s3", "ec2"},
		Schedule:    "daily",
		Status:      "idle",
	}
	repo.subscriptions[subKey("user1", "aws-blog")] = &model.CrawlerSubscription{
		SourceID:    "aws-blog",
		AWSServices: []string{"lambda", "s3"},
	}
	repo.subscriptions[subKey("user2", "aws-blog")] = &model.CrawlerSubscription{
		SourceID:    "aws-blog",
		AWSServices: []string{"ec2"},
	}

	err := svc.Unsubscribe(ctx, "user1", "aws-blog")
	if err != nil {
		t.Fatalf("Unsubscribe returned error: %v", err)
	}

	source := repo.sources["aws-blog"]

	// Status should NOT be "disabled"
	if source.Status == "disabled" {
		t.Error("expected status to remain active, got 'disabled'")
	}

	// Subscribers should only have user2
	if len(source.Subscribers) != 1 || source.Subscribers[0] != "user2" {
		t.Errorf("expected subscribers [user2], got %v", source.Subscribers)
	}

	// AWSServices should be rebuilt from remaining subscriber (user2 only has ec2)
	if !strSliceEqual(source.AWSServices, []string{"ec2"}) {
		t.Errorf("expected awsServices [ec2] after rebuild, got %v", source.AWSServices)
	}

	// user1's subscription should be deleted
	if _, ok := repo.subscriptions[subKey("user1", "aws-blog")]; ok {
		t.Error("expected user1 subscription to be deleted")
	}
}

func TestListSources_ReturnsSourcesWithSubscriptions(t *testing.T) {
	repo := newMockCrawlerRepo()
	svc := &CrawlerService{repo: repo}
	ctx := context.Background()

	// Seed two sources, user1 subscribed to both
	repo.sources["aws-blog"] = &model.CrawlerSource{
		SourceID:    "aws-blog",
		SourceName:  "AWS Blog",
		Subscribers: []string{"user1"},
		AWSServices: []string{"lambda"},
		Schedule:    "daily",
		Status:      "idle",
	}
	repo.sources["techcrunch"] = &model.CrawlerSource{
		SourceID:    "techcrunch",
		SourceName:  "TechCrunch",
		Subscribers: []string{"user1"},
		Schedule:    "daily",
		Status:      "idle",
	}
	repo.subscriptions[subKey("user1", "aws-blog")] = &model.CrawlerSubscription{
		SourceID:    "aws-blog",
		AWSServices: []string{"lambda"},
		AddedAt:     "2026-01-01T00:00:00Z",
	}
	repo.subscriptions[subKey("user1", "techcrunch")] = &model.CrawlerSubscription{
		SourceID: "techcrunch",
		AddedAt:  "2026-01-02T00:00:00Z",
	}

	resp, err := svc.ListSources(ctx, "user1")
	if err != nil {
		t.Fatalf("ListSources returned error: %v", err)
	}

	if len(resp.Sources) != 2 {
		t.Fatalf("expected 2 sources, got %d", len(resp.Sources))
	}

	// Verify each source has a corresponding subscription
	for _, sr := range resp.Sources {
		if sr.Source.SourceID == "" {
			t.Error("source has empty sourceID")
		}
		if sr.Subscription.SourceID == "" {
			t.Error("subscription has empty sourceID")
		}
		if sr.Source.SourceID != sr.Subscription.SourceID {
			t.Errorf("source and subscription sourceID mismatch: %q vs %q", sr.Source.SourceID, sr.Subscription.SourceID)
		}
	}
}

func TestListSources_SkipsMissingSource(t *testing.T) {
	repo := newMockCrawlerRepo()
	svc := &CrawlerService{repo: repo}
	ctx := context.Background()

	// user1 has a subscription but the source is gone
	repo.subscriptions[subKey("user1", "deleted-source")] = &model.CrawlerSubscription{
		SourceID: "deleted-source",
	}

	resp, err := svc.ListSources(ctx, "user1")
	if err != nil {
		t.Fatalf("ListSources returned error: %v", err)
	}

	// Should skip the orphaned subscription
	if len(resp.Sources) != 0 {
		t.Errorf("expected 0 sources (orphaned subscription skipped), got %d", len(resp.Sources))
	}
}

func TestGetHistory(t *testing.T) {
	repo := newMockCrawlerRepo()
	svc := &CrawlerService{repo: repo}
	ctx := context.Background()

	repo.history["aws-blog"] = []model.CrawlHistory{
		{Timestamp: "2026-04-15T10:00:00Z", DocsAdded: 5, Duration: 120},
		{Timestamp: "2026-04-14T10:00:00Z", DocsAdded: 3, Duration: 90},
	}

	resp, err := svc.GetHistory(ctx, "aws-blog")
	if err != nil {
		t.Fatalf("GetHistory returned error: %v", err)
	}

	if len(resp.History) != 2 {
		t.Errorf("expected 2 history entries, got %d", len(resp.History))
	}
}

// ---------- Unit tests for helper functions ----------

func TestNormalizeSourceID(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"AWS Blog", "aws-blog"},
		{"  TechCrunch  ", "techcrunch"},
		{"Hello World 123", "hello-world-123"},
		{"Special!@#Chars", "specialchars"},
	}

	for _, tt := range tests {
		got := normalizeSourceID(tt.input)
		if got != tt.expected {
			t.Errorf("normalizeSourceID(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestUnion(t *testing.T) {
	tests := []struct {
		a, b     []string
		expected []string
	}{
		{[]string{"a", "b"}, []string{"b", "c"}, []string{"a", "b", "c"}},
		{nil, []string{"a"}, []string{"a"}},
		{[]string{"a"}, nil, []string{"a"}},
		{nil, nil, []string{}},
	}

	for _, tt := range tests {
		got := union(tt.a, tt.b)
		if !strSliceEqual(got, tt.expected) {
			t.Errorf("union(%v, %v) = %v, want %v", tt.a, tt.b, got, tt.expected)
		}
	}
}

func TestContains(t *testing.T) {
	if !contains([]string{"a", "b", "c"}, "b") {
		t.Error("expected contains([a,b,c], b) = true")
	}
	if contains([]string{"a", "b", "c"}, "d") {
		t.Error("expected contains([a,b,c], d) = false")
	}
	if contains(nil, "a") {
		t.Error("expected contains(nil, a) = false")
	}
}

func TestRemove(t *testing.T) {
	got := remove([]string{"a", "b", "c"}, "b")
	if !strSliceEqual(got, []string{"a", "c"}) {
		t.Errorf("remove([a,b,c], b) = %v, want [a, c]", got)
	}

	got = remove([]string{"a"}, "a")
	if len(got) != 0 {
		t.Errorf("remove([a], a) = %v, want []", got)
	}

	got = remove([]string{"a", "b"}, "x")
	if !strSliceEqual(got, []string{"a", "b"}) {
		t.Errorf("remove([a,b], x) = %v, want [a, b]", got)
	}
}
