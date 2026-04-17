# Crawler & Insights Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add automated AWS docs + customer news crawling into a shared KB, with Insights UI for browsing and Settings UI for managing crawler sources.

**Architecture:** New CrawlerStack (Step Functions + 4 Python Lambdas) for daily crawling. Go backend gains Crawler Settings + Insights REST endpoints. Frontend gets an Insights page (News/Tech tabs) and Settings crawler source section. QA Lambda KB filter is updated to include `shared/` prefix.

**Tech Stack:** CDK TypeScript, Go (chi router), Python 3.12 (crawler Lambdas), React/Next.js 16, DynamoDB single-table, S3, Step Functions, EventBridge, Bedrock (Haiku for summarization, KB for ingestion)

---

## Phase 1: Data Model & Backend API

### Task 1: DynamoDB Model Types

**Files:**
- Modify: `backend/internal/model/meeting.go` (add key prefixes)
- Modify: `backend/internal/model/request.go` (add request/response types)

- [ ] **Step 1: Add key prefixes to meeting.go**

Add after line 127 (`PrefixProfile`):

```go
PrefixCrawler    = "CRAWLER#"
PrefixCrawlSub   = "CRAWL_SUB#"
PrefixDoc        = "DOC#"
PrefixHistory    = "HISTORY#"
PrefixConfig     = "CONFIG"
```

- [ ] **Step 2: Add model structs to meeting.go**

Add after the existing entity types:

```go
type CrawlerSource struct {
	SourceID      string   `dynamodbav:"sourceId" json:"sourceId"`
	SourceName    string   `dynamodbav:"sourceName" json:"sourceName"`
	Subscribers   []string `dynamodbav:"subscribers" json:"subscribers"`
	AWSServices   []string `dynamodbav:"awsServices" json:"awsServices"`
	NewsQueries   []string `dynamodbav:"newsQueries" json:"newsQueries"`
	CustomUrls    []string `dynamodbav:"customUrls" json:"customUrls"`
	Schedule      string   `dynamodbav:"schedule" json:"schedule"`
	LastCrawledAt string   `dynamodbav:"lastCrawledAt" json:"lastCrawledAt"`
	Status        string   `dynamodbav:"status" json:"status"`
	DocumentCount int      `dynamodbav:"documentCount" json:"documentCount"`
}

type CrawlerSubscription struct {
	SourceID    string   `dynamodbav:"sourceId" json:"sourceId"`
	AWSServices []string `dynamodbav:"awsServices" json:"awsServices"`
	NewsSources []string `dynamodbav:"newsSources" json:"newsSources"`
	CustomUrls  []string `dynamodbav:"customUrls" json:"customUrls"`
	AddedAt     string   `dynamodbav:"addedAt" json:"addedAt"`
}

type CrawledDocument struct {
	DocHash     string   `dynamodbav:"docHash" json:"docHash"`
	Type        string   `dynamodbav:"type" json:"type"`
	Title       string   `dynamodbav:"title" json:"title"`
	URL         string   `dynamodbav:"url" json:"url"`
	Source      string   `dynamodbav:"source" json:"source"`
	Summary     string   `dynamodbav:"summary" json:"summary"`
	AWSServices []string `dynamodbav:"awsServices,omitempty" json:"awsServices,omitempty"`
	S3Key       string   `dynamodbav:"s3Key" json:"s3Key"`
	CrawledAt   string   `dynamodbav:"crawledAt" json:"crawledAt"`
	InKB        bool     `dynamodbav:"inKB" json:"inKB"`
}

type CrawlHistory struct {
	Timestamp   string   `dynamodbav:"timestamp" json:"timestamp"`
	DocsAdded   int      `dynamodbav:"docsAdded" json:"docsAdded"`
	DocsUpdated int      `dynamodbav:"docsUpdated" json:"docsUpdated"`
	Errors      []string `dynamodbav:"errors" json:"errors"`
	Duration    int      `dynamodbav:"duration" json:"duration"`
}
```

- [ ] **Step 3: Add request/response types to request.go**

Add after the existing KB types:

```go
// Crawler Settings
type AddCrawlerSourceRequest struct {
	SourceName  string   `json:"sourceName"`
	AWSServices []string `json:"awsServices"`
	NewsSources []string `json:"newsSources"`
	CustomUrls  []string `json:"customUrls,omitempty"`
	NewsQueries []string `json:"newsQueries,omitempty"`
}

type UpdateCrawlerSourceRequest struct {
	AWSServices []string `json:"awsServices"`
	NewsSources []string `json:"newsSources"`
	CustomUrls  []string `json:"customUrls,omitempty"`
}

type CrawlerSourceResponse struct {
	Source       CrawlerSource       `json:"source"`
	Subscription CrawlerSubscription `json:"subscription"`
}

type CrawlerSourcesResponse struct {
	Sources []CrawlerSourceResponse `json:"sources"`
}

type CrawlHistoryResponse struct {
	History []CrawlHistory `json:"history"`
}

// Insights
type InsightsRequest struct {
	Type    string `json:"type"`
	Source  string `json:"source,omitempty"`
	Service string `json:"service,omitempty"`
	Page    int    `json:"page"`
	Limit   int    `json:"limit"`
}

type InsightsResponse struct {
	Documents  []CrawledDocument `json:"documents"`
	TotalCount int               `json:"totalCount"`
	Page       int               `json:"page"`
	Limit      int               `json:"limit"`
}
```

- [ ] **Step 4: Commit**

```bash
git add backend/internal/model/
git commit -m "feat(model): add crawler, subscription, and insights DynamoDB types"
```

---

### Task 2: Crawler Repository

**Files:**
- Create: `backend/internal/repository/crawler.go`

- [ ] **Step 1: Create crawler repository**

```go
package repository

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
)

type CrawlerRepository struct {
	client    *dynamodb.Client
	tableName string
}

func NewCrawlerRepository(client *dynamodb.Client, tableName string) *CrawlerRepository {
	return &CrawlerRepository{client: client, tableName: tableName}
}

func normalizeSourceID(name string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	var b strings.Builder
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func (r *CrawlerRepository) GetSource(ctx context.Context, sourceID string) (*model.CrawlerSource, error) {
	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: &r.tableName,
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: model.PrefixCrawler + sourceID},
			"SK": &types.AttributeValueMemberS{Value: model.PrefixConfig},
		},
	})
	if err != nil {
		return nil, err
	}
	if result.Item == nil {
		return nil, nil
	}
	var source model.CrawlerSource
	if err := attributevalue.UnmarshalMap(result.Item, &source); err != nil {
		return nil, err
	}
	return &source, nil
}

func (r *CrawlerRepository) PutSource(ctx context.Context, source *model.CrawlerSource) error {
	item, err := attributevalue.MarshalMap(source)
	if err != nil {
		return err
	}
	item["PK"] = &types.AttributeValueMemberS{Value: model.PrefixCrawler + source.SourceID}
	item["SK"] = &types.AttributeValueMemberS{Value: model.PrefixConfig}
	_, err = r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: &r.tableName,
		Item:      item,
	})
	return err
}

func (r *CrawlerRepository) GetSubscription(ctx context.Context, userID, sourceID string) (*model.CrawlerSubscription, error) {
	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: &r.tableName,
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: model.PrefixUser + userID},
			"SK": &types.AttributeValueMemberS{Value: model.PrefixCrawlSub + sourceID},
		},
	})
	if err != nil {
		return nil, err
	}
	if result.Item == nil {
		return nil, nil
	}
	var sub model.CrawlerSubscription
	if err := attributevalue.UnmarshalMap(result.Item, &sub); err != nil {
		return nil, err
	}
	return &sub, nil
}

func (r *CrawlerRepository) PutSubscription(ctx context.Context, userID string, sub *model.CrawlerSubscription) error {
	item, err := attributevalue.MarshalMap(sub)
	if err != nil {
		return err
	}
	item["PK"] = &types.AttributeValueMemberS{Value: model.PrefixUser + userID}
	item["SK"] = &types.AttributeValueMemberS{Value: model.PrefixCrawlSub + sub.SourceID}
	_, err = r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: &r.tableName,
		Item:      item,
	})
	return err
}

func (r *CrawlerRepository) DeleteSubscription(ctx context.Context, userID, sourceID string) error {
	_, err := r.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: &r.tableName,
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: model.PrefixUser + userID},
			"SK": &types.AttributeValueMemberS{Value: model.PrefixCrawlSub + sourceID},
		},
	})
	return err
}

func (r *CrawlerRepository) ListUserSubscriptions(ctx context.Context, userID string) ([]model.CrawlerSubscription, error) {
	keyEx := expression.Key("PK").Equal(expression.Value(model.PrefixUser + userID)).
		And(expression.Key("SK").BeginsWith(expression.Value(model.PrefixCrawlSub)))
	expr, _ := expression.NewBuilder().WithKeyCondition(keyEx).Build()

	result, err := r.client.Query(ctx, &dynamodb.QueryInput{
		TableName:                 &r.tableName,
		KeyConditionExpression:    expr.KeyCondition(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
	})
	if err != nil {
		return nil, err
	}
	var subs []model.CrawlerSubscription
	if err := attributevalue.UnmarshalListOfMaps(result.Items, &subs); err != nil {
		return nil, err
	}
	return subs, nil
}

func (r *CrawlerRepository) ListDocuments(ctx context.Context, sourceID, docType string, limit int, lastKey map[string]types.AttributeValue) ([]model.CrawledDocument, map[string]types.AttributeValue, int, error) {
	keyEx := expression.Key("PK").Equal(expression.Value(model.PrefixCrawler + sourceID)).
		And(expression.Key("SK").BeginsWith(expression.Value(model.PrefixDoc)))

	builder := expression.NewBuilder().WithKeyCondition(keyEx)
	if docType != "" {
		builder = builder.WithFilter(expression.Name("type").Equal(expression.Value(docType)))
	}
	expr, _ := builder.Build()

	input := &dynamodb.QueryInput{
		TableName:                 &r.tableName,
		KeyConditionExpression:    expr.KeyCondition(),
		FilterExpression:          expr.Filter(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
		Limit:                     aws.Int32(int32(limit)),
		ScanIndexForward:          aws.Bool(false),
	}
	if lastKey != nil {
		input.ExclusiveStartKey = lastKey
	}

	result, err := r.client.Query(ctx, input)
	if err != nil {
		return nil, nil, 0, err
	}
	var docs []model.CrawledDocument
	if err := attributevalue.UnmarshalListOfMaps(result.Items, &docs); err != nil {
		return nil, nil, 0, err
	}
	return docs, result.LastEvaluatedKey, int(result.Count), nil
}

func (r *CrawlerRepository) ListAllDocumentsByType(ctx context.Context, docType string, limit, page int) ([]model.CrawledDocument, int, error) {
	filt := expression.Name("type").Equal(expression.Value(docType)).
		And(expression.Key("PK").BeginsWith(expression.Value(model.PrefixCrawler)))
	// For cross-source queries, scan with filter (acceptable for low-volume shared data)
	filterEx := expression.Name("type").Equal(expression.Value(docType))
	expr, _ := expression.NewBuilder().WithFilter(filterEx).Build()

	var allDocs []model.CrawledDocument
	var lastKey map[string]types.AttributeValue

	for {
		input := &dynamodb.ScanInput{
			TableName:                 &r.tableName,
			FilterExpression:          expr.Filter(),
			ExpressionAttributeNames:  expr.Names(),
			ExpressionAttributeValues: expr.Values(),
			ExclusiveStartKey:         lastKey,
		}
		result, err := r.client.Scan(ctx, input)
		if err != nil {
			return nil, 0, err
		}
		var docs []model.CrawledDocument
		attributevalue.UnmarshalListOfMaps(result.Items, &docs)
		allDocs = append(allDocs, docs...)
		lastKey = result.LastEvaluatedKey
		if lastKey == nil {
			break
		}
	}

	total := len(allDocs)
	start := (page - 1) * limit
	if start >= total {
		return []model.CrawledDocument{}, total, nil
	}
	end := start + limit
	if end > total {
		end = total
	}
	return allDocs[start:end], total, nil
}

func (r *CrawlerRepository) ListHistory(ctx context.Context, sourceID string, limit int) ([]model.CrawlHistory, error) {
	keyEx := expression.Key("PK").Equal(expression.Value(model.PrefixCrawler + sourceID)).
		And(expression.Key("SK").BeginsWith(expression.Value(model.PrefixHistory)))
	expr, _ := expression.NewBuilder().WithKeyCondition(keyEx).Build()

	result, err := r.client.Query(ctx, &dynamodb.QueryInput{
		TableName:                 &r.tableName,
		KeyConditionExpression:    expr.KeyCondition(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
		Limit:                     aws.Int32(int32(limit)),
		ScanIndexForward:          aws.Bool(false),
	})
	if err != nil {
		return nil, err
	}
	var history []model.CrawlHistory
	if err := attributevalue.UnmarshalListOfMaps(result.Items, &history); err != nil {
		return nil, err
	}
	return history, nil
}

func (r *CrawlerRepository) NormalizeSourceID(name string) string {
	return normalizeSourceID(name)
}
```

- [ ] **Step 2: Build and verify**

```bash
cd backend && /usr/local/go/bin/go build ./...
```

- [ ] **Step 3: Commit**

```bash
git add backend/internal/repository/crawler.go
git commit -m "feat(repo): add crawler DynamoDB repository"
```

---

### Task 3: Crawler Service

**Files:**
- Create: `backend/internal/service/crawler.go`

- [ ] **Step 1: Create crawler service**

```go
package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

type CrawlerService struct {
	repo *repository.CrawlerRepository
}

func NewCrawlerService(repo *repository.CrawlerRepository) *CrawlerService {
	return &CrawlerService{repo: repo}
}

func (s *CrawlerService) AddSource(ctx context.Context, userID string, req *model.AddCrawlerSourceRequest) (*model.CrawlerSourceResponse, error) {
	sourceID := s.repo.NormalizeSourceID(req.SourceName)
	if sourceID == "" {
		return nil, ErrBadRequest
	}

	existing, err := s.repo.GetSource(ctx, sourceID)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC().Format(time.RFC3339)
	newsQueries := req.NewsQueries
	if len(newsQueries) == 0 {
		newsQueries = []string{req.SourceName}
	}

	if existing != nil {
		if !contains(existing.Subscribers, userID) {
			existing.Subscribers = append(existing.Subscribers, userID)
		}
		existing.AWSServices = union(existing.AWSServices, req.AWSServices)
		existing.NewsQueries = union(existing.NewsQueries, newsQueries)
		existing.CustomUrls = union(existing.CustomUrls, req.CustomUrls)
		if err := s.repo.PutSource(ctx, existing); err != nil {
			return nil, err
		}
	} else {
		existing = &model.CrawlerSource{
			SourceID:      sourceID,
			SourceName:    req.SourceName,
			Subscribers:   []string{userID},
			AWSServices:   req.AWSServices,
			NewsQueries:   newsQueries,
			CustomUrls:    req.CustomUrls,
			Schedule:      "daily",
			Status:        "idle",
			DocumentCount: 0,
		}
		if err := s.repo.PutSource(ctx, existing); err != nil {
			return nil, err
		}
	}

	sub := &model.CrawlerSubscription{
		SourceID:    sourceID,
		AWSServices: req.AWSServices,
		NewsSources: req.NewsSources,
		CustomUrls:  req.CustomUrls,
		AddedAt:     now,
	}
	if err := s.repo.PutSubscription(ctx, userID, sub); err != nil {
		return nil, err
	}

	return &model.CrawlerSourceResponse{Source: *existing, Subscription: *sub}, nil
}

func (s *CrawlerService) ListSources(ctx context.Context, userID string) (*model.CrawlerSourcesResponse, error) {
	subs, err := s.repo.ListUserSubscriptions(ctx, userID)
	if err != nil {
		return nil, err
	}

	var sources []model.CrawlerSourceResponse
	for _, sub := range subs {
		source, err := s.repo.GetSource(ctx, sub.SourceID)
		if err != nil || source == nil {
			continue
		}
		sources = append(sources, model.CrawlerSourceResponse{Source: *source, Subscription: sub})
	}
	return &model.CrawlerSourcesResponse{Sources: sources}, nil
}

func (s *CrawlerService) UpdateSource(ctx context.Context, userID, sourceID string, req *model.UpdateCrawlerSourceRequest) error {
	sub, err := s.repo.GetSubscription(ctx, userID, sourceID)
	if err != nil {
		return err
	}
	if sub == nil {
		return ErrNotFound
	}

	sub.AWSServices = req.AWSServices
	sub.NewsSources = req.NewsSources
	sub.CustomUrls = req.CustomUrls
	if err := s.repo.PutSubscription(ctx, userID, sub); err != nil {
		return err
	}

	return s.rebuildSourceUnion(ctx, sourceID)
}

func (s *CrawlerService) Unsubscribe(ctx context.Context, userID, sourceID string) error {
	if err := s.repo.DeleteSubscription(ctx, userID, sourceID); err != nil {
		return err
	}

	source, err := s.repo.GetSource(ctx, sourceID)
	if err != nil || source == nil {
		return err
	}

	source.Subscribers = remove(source.Subscribers, userID)
	if len(source.Subscribers) == 0 {
		source.Status = "disabled"
	}
	return s.repo.PutSource(ctx, source)
}

func (s *CrawlerService) GetHistory(ctx context.Context, sourceID string) (*model.CrawlHistoryResponse, error) {
	history, err := s.repo.ListHistory(ctx, sourceID, 20)
	if err != nil {
		return nil, err
	}
	return &model.CrawlHistoryResponse{History: history}, nil
}

func (s *CrawlerService) rebuildSourceUnion(ctx context.Context, sourceID string) error {
	source, err := s.repo.GetSource(ctx, sourceID)
	if err != nil || source == nil {
		return err
	}

	var allServices, allUrls []string
	for _, uid := range source.Subscribers {
		sub, err := s.repo.GetSubscription(ctx, uid, sourceID)
		if err != nil || sub == nil {
			continue
		}
		allServices = union(allServices, sub.AWSServices)
		allUrls = union(allUrls, sub.CustomUrls)
	}
	source.AWSServices = allServices
	source.CustomUrls = allUrls
	return s.repo.PutSource(ctx, source)
}

var ErrBadRequest = fmt.Errorf("bad request")

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

func union(a, b []string) []string {
	set := make(map[string]bool)
	for _, s := range a {
		set[strings.TrimSpace(s)] = true
	}
	for _, s := range b {
		set[strings.TrimSpace(s)] = true
	}
	var result []string
	for s := range set {
		if s != "" {
			result = append(result, s)
		}
	}
	return result
}

func remove(slice []string, item string) []string {
	var result []string
	for _, s := range slice {
		if s != item {
			result = append(result, s)
		}
	}
	return result
}
```

- [ ] **Step 2: Build and verify**

```bash
cd backend && /usr/local/go/bin/go build ./...
```

- [ ] **Step 3: Commit**

```bash
git add backend/internal/service/crawler.go
git commit -m "feat(service): add crawler service with source management and dedup"
```

---

### Task 4: Insights Service

**Files:**
- Create: `backend/internal/service/insights.go`

- [ ] **Step 1: Create insights service**

```go
package service

import (
	"context"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

type InsightsService struct {
	repo *repository.CrawlerRepository
}

func NewInsightsService(repo *repository.CrawlerRepository) *InsightsService {
	return &InsightsService{repo: repo}
}

func (s *InsightsService) ListInsights(ctx context.Context, docType string, source string, service string, page, limit int) (*model.InsightsResponse, error) {
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 50 {
		limit = 20
	}

	if source != "" {
		docs, _, count, err := s.repo.ListDocuments(ctx, source, docType, limit, nil)
		if err != nil {
			return nil, err
		}
		return &model.InsightsResponse{Documents: docs, TotalCount: count, Page: page, Limit: limit}, nil
	}

	docs, total, err := s.repo.ListAllDocumentsByType(ctx, docType, limit, page)
	if err != nil {
		return nil, err
	}
	return &model.InsightsResponse{Documents: docs, TotalCount: total, Page: page, Limit: limit}, nil
}
```

- [ ] **Step 2: Build and verify**

```bash
cd backend && /usr/local/go/bin/go build ./...
```

- [ ] **Step 3: Commit**

```bash
git add backend/internal/service/insights.go
git commit -m "feat(service): add insights service for browsing crawled documents"
```

---

### Task 5: Crawler & Insights Handlers + Route Registration

**Files:**
- Create: `backend/internal/handler/crawler.go`
- Create: `backend/internal/handler/insights.go`
- Modify: `backend/cmd/api/main.go` (add routes)

- [ ] **Step 1: Create crawler handler**

```go
package handler

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/service"
)

type CrawlerHandler struct {
	crawlerService *service.CrawlerService
}

func NewCrawlerHandler(crawlerService *service.CrawlerService) *CrawlerHandler {
	return &CrawlerHandler{crawlerService: crawlerService}
}

func (h *CrawlerHandler) ListSources(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)

	result, err := h.crawlerService.ListSources(ctx, userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *CrawlerHandler) AddSource(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)

	var req model.AddCrawlerSourceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "invalid request body")
		return
	}
	if req.SourceName == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "sourceName is required")
		return
	}

	result, err := h.crawlerService.AddSource(ctx, userID, &req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

func (h *CrawlerHandler) UpdateSource(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	sourceID := chi.URLParam(r, "sourceId")

	var req model.UpdateCrawlerSourceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "invalid request body")
		return
	}

	if err := h.crawlerService.UpdateSource(ctx, userID, sourceID, &req); err != nil {
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (h *CrawlerHandler) Unsubscribe(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	sourceID := chi.URLParam(r, "sourceId")

	if err := h.crawlerService.Unsubscribe(ctx, userID, sourceID); err != nil {
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *CrawlerHandler) GetHistory(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sourceID := chi.URLParam(r, "sourceId")

	result, err := h.crawlerService.GetHistory(ctx, sourceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}
```

- [ ] **Step 2: Create insights handler**

```go
package handler

import (
	"net/http"
	"strconv"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/service"
)

type InsightsHandler struct {
	insightsService *service.InsightsService
}

func NewInsightsHandler(insightsService *service.InsightsService) *InsightsHandler {
	return &InsightsHandler{insightsService: insightsService}
}

func (h *InsightsHandler) ListInsights(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	docType := r.URL.Query().Get("type")
	source := r.URL.Query().Get("source")
	svc := r.URL.Query().Get("service")
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))

	if docType == "" {
		docType = "news"
	}
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 20
	}

	result, err := h.insightsService.ListInsights(ctx, docType, source, svc, page, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}
```

- [ ] **Step 3: Register routes in main.go**

Add inside the `r.Group(func(r chi.Router) { r.Use(middleware.Auth) ... })` block, after the KB routes:

```go
// Crawler settings
r.Get("/api/crawler/sources", crawlerHandler.ListSources)
r.Post("/api/crawler/sources", crawlerHandler.AddSource)
r.Put("/api/crawler/sources/{sourceId}", crawlerHandler.UpdateSource)
r.Delete("/api/crawler/sources/{sourceId}", crawlerHandler.Unsubscribe)
r.Get("/api/crawler/sources/{sourceId}/history", crawlerHandler.GetHistory)

// Insights
r.Get("/api/insights", insightsHandler.ListInsights)
```

Also add the repository, service, and handler initialization in the `init()` function:

```go
crawlerRepo := repository.NewCrawlerRepository(dynamoClient, tableName)
crawlerService := service.NewCrawlerService(crawlerRepo)
insightsService := service.NewInsightsService(crawlerRepo)
crawlerHandler := handler.NewCrawlerHandler(crawlerService)
insightsHandler := handler.NewInsightsHandler(insightsService)
```

- [ ] **Step 4: Build API Lambda**

```bash
cd backend && GOOS=linux GOARCH=arm64 /usr/local/go/bin/go build -tags lambda.norpc -o cmd/api/bootstrap ./cmd/api
```

- [ ] **Step 5: Commit**

```bash
git add backend/internal/handler/crawler.go backend/internal/handler/insights.go backend/cmd/api/main.go
git commit -m "feat(api): add crawler settings and insights REST endpoints"
```

---

## Phase 2: QA Lambda KB Filter Update

### Task 6: Update QA Retrieval Filter

**Files:**
- Modify: `backend/python/qa/handler.py` (line ~179-185)

- [ ] **Step 1: Update retrieve_from_kb filter**

Replace the current user-scoping filter in `retrieve_from_kb()`:

```python
# Before (line ~179-185):
if user_id:
    retrieval_config['vectorSearchConfiguration']['filter'] = {
        'stringContains': {
            'key': 'x-amz-bedrock-kb-source-uri',
            'value': f'kb/{user_id}/'
        }
    }

# After:
if user_id:
    retrieval_config['vectorSearchConfiguration']['filter'] = {
        'orAll': [
            {'stringContains': {'key': 'x-amz-bedrock-kb-source-uri', 'value': f'kb/{user_id}/'}},
            {'stringContains': {'key': 'x-amz-bedrock-kb-source-uri', 'value': f'meetings/{user_id}/'}},
            {'stringContains': {'key': 'x-amz-bedrock-kb-source-uri', 'value': 'shared/'}},
        ]
    }
```

- [ ] **Step 2: Commit**

```bash
git add backend/python/qa/handler.py
git commit -m "feat(qa): expand KB retrieval filter to include shared/ and meetings/ prefixes"
```

---

## Phase 3: CDK Infrastructure — CrawlerStack

### Task 7: Add Crawler IAM Role to AiStack

**Files:**
- Modify: `infra/lib/ai-stack.ts` (add crawler role)

- [ ] **Step 1: Add crawlerRole property**

In `AiStack` class, add a new public property:

```typescript
public readonly crawlerRole: iam.Role;
```

- [ ] **Step 2: Create the role in the constructor**

After the existing roles, add:

```typescript
this.crawlerRole = createLambdaRole('TtobakCrawlerRole', 'ttobak-crawler-role', 'Role for crawler Lambda functions');
props.table.grantReadWriteData(this.crawlerRole);
props.kbBucket.grantReadWrite(this.crawlerRole);
this.crawlerRole.addToPolicy(new iam.PolicyStatement({
  sid: 'BedrockHaikuForSummarization',
  effect: iam.Effect.ALLOW,
  actions: ['bedrock:InvokeModel'],
  resources: [
    `arn:aws:bedrock:*::foundation-model/anthropic.claude-haiku-*`,
    `arn:aws:bedrock:*:${cdk.Aws.ACCOUNT_ID}:inference-profile/*claude-haiku*`,
  ],
}));
this.crawlerRole.addToPolicy(new iam.PolicyStatement({
  sid: 'BedrockKBIngestion',
  effect: iam.Effect.ALLOW,
  actions: ['bedrock:StartIngestionJob'],
  resources: ['*'],
}));
this.crawlerRole.addToPolicy(new iam.PolicyStatement({
  sid: 'StepFunctionsExecution',
  effect: iam.Effect.ALLOW,
  actions: ['states:StartExecution'],
  resources: ['*'],
}));
```

- [ ] **Step 3: Add CfnOutput and export**

```typescript
new cdk.CfnOutput(this, 'CrawlerRoleArn', {
  value: this.crawlerRole.roleArn,
  exportName: 'TtobakCrawlerRoleArn',
});
```

Also export via `ExportsOutput` pattern matching existing exports.

- [ ] **Step 4: Commit**

```bash
git add infra/lib/ai-stack.ts
git commit -m "feat(infra): add crawler IAM role to AiStack"
```

---

### Task 8: Create CrawlerStack

**Files:**
- Create: `infra/lib/crawler-stack.ts`
- Modify: `infra/bin/infra.ts` (wire stack)

- [ ] **Step 1: Create crawler-stack.ts**

```typescript
import * as cdk from 'aws-cdk-lib';
import * as lambda from 'aws-cdk-lib/aws-lambda';
import * as iam from 'aws-cdk-lib/aws-iam';
import * as s3 from 'aws-cdk-lib/aws-s3';
import * as dynamodb from 'aws-cdk-lib/aws-dynamodb';
import * as sfn from 'aws-cdk-lib/aws-stepfunctions';
import * as tasks from 'aws-cdk-lib/aws-stepfunctions-tasks';
import * as events from 'aws-cdk-lib/aws-events';
import * as eventsTargets from 'aws-cdk-lib/aws-events-targets';
import { Construct } from 'constructs';

export interface CrawlerStackProps extends cdk.StackProps {
  crawlerRole: iam.IRole;
  table: dynamodb.ITable;
  kbBucket: s3.IBucket;
  knowledgeBaseId?: string;
  dataSourceId?: string;
}

export class CrawlerStack extends cdk.Stack {
  constructor(scope: Construct, id: string, props: CrawlerStackProps) {
    super(scope, id, props);

    const commonEnv = {
      TABLE_NAME: props.table.tableName,
      KB_BUCKET_NAME: props.kbBucket.bucketName,
      KB_ID: props.knowledgeBaseId || '',
      DATA_SOURCE_ID: props.dataSourceId || '',
      HAIKU_MODEL_ID: 'global.anthropic.claude-haiku-4-5-20251001-v1:0',
    };

    // Orchestrator Lambda
    const orchestrator = new lambda.Function(this, 'OrchestratorFunction', {
      functionName: 'ttobak-crawler-orchestrator',
      runtime: lambda.Runtime.PYTHON_3_12,
      architecture: lambda.Architecture.ARM_64,
      handler: 'orchestrator.handler',
      code: lambda.Code.fromAsset('../backend/python/crawler'),
      role: props.crawlerRole as iam.Role,
      environment: { TABLE_NAME: commonEnv.TABLE_NAME },
      timeout: cdk.Duration.seconds(30),
      memorySize: 256,
    });

    // Tech Crawler Lambda
    const techCrawler = new lambda.Function(this, 'TechCrawlerFunction', {
      functionName: 'ttobak-crawler-tech',
      runtime: lambda.Runtime.PYTHON_3_12,
      architecture: lambda.Architecture.ARM_64,
      handler: 'tech_crawler.handler',
      code: lambda.Code.fromAsset('../backend/python/crawler'),
      role: props.crawlerRole as iam.Role,
      environment: commonEnv,
      timeout: cdk.Duration.minutes(5),
      memorySize: 512,
    });

    // News Crawler Lambda
    const newsCrawler = new lambda.Function(this, 'NewsCrawlerFunction', {
      functionName: 'ttobak-crawler-news',
      runtime: lambda.Runtime.PYTHON_3_12,
      architecture: lambda.Architecture.ARM_64,
      handler: 'news_crawler.handler',
      code: lambda.Code.fromAsset('../backend/python/crawler'),
      role: props.crawlerRole as iam.Role,
      environment: commonEnv,
      timeout: cdk.Duration.minutes(5),
      memorySize: 512,
    });

    // Ingestion Trigger Lambda
    const ingestTrigger = new lambda.Function(this, 'IngestTriggerFunction', {
      functionName: 'ttobak-crawler-ingest',
      runtime: lambda.Runtime.PYTHON_3_12,
      architecture: lambda.Architecture.ARM_64,
      handler: 'ingest_trigger.handler',
      code: lambda.Code.fromAsset('../backend/python/crawler'),
      role: props.crawlerRole as iam.Role,
      environment: {
        KB_ID: commonEnv.KB_ID,
        DATA_SOURCE_ID: commonEnv.DATA_SOURCE_ID,
      },
      timeout: cdk.Duration.seconds(30),
      memorySize: 256,
    });

    // Step Functions Workflow
    const listSources = new tasks.LambdaInvoke(this, 'ListActiveSources', {
      lambdaFunction: orchestrator,
      outputPath: '$.Payload',
    });

    const acquireLock = new sfn.Pass(this, 'AcquireLock');
    const releaseLock = new sfn.Pass(this, 'ReleaseLock');

    const crawlTech = new tasks.LambdaInvoke(this, 'CrawlTechDocs', {
      lambdaFunction: techCrawler,
      outputPath: '$.Payload',
    });

    const crawlNews = new tasks.LambdaInvoke(this, 'CrawlNews', {
      lambdaFunction: newsCrawler,
      outputPath: '$.Payload',
    });

    const parallelCrawl = new sfn.Parallel(this, 'ParallelCrawl')
      .branch(crawlTech)
      .branch(crawlNews);

    const perSourceFlow = acquireLock
      .next(parallelCrawl)
      .next(releaseLock);

    const mapSources = new sfn.Map(this, 'MapSources', {
      maxConcurrency: 5,
      itemsPath: '$.sources',
    }).itemProcessor(perSourceFlow);

    const triggerIngestion = new tasks.LambdaInvoke(this, 'TriggerIngestion', {
      lambdaFunction: ingestTrigger,
      outputPath: '$.Payload',
    });

    const definition = listSources
      .next(mapSources)
      .next(triggerIngestion);

    const stateMachine = new sfn.StateMachine(this, 'CrawlerWorkflow', {
      stateMachineName: 'ttobak-crawler-workflow',
      definitionBody: sfn.DefinitionBody.fromChainable(definition),
      timeout: cdk.Duration.minutes(30),
    });

    // Daily schedule (KST 04:00 = UTC 19:00)
    new events.Rule(this, 'DailyCrawlSchedule', {
      ruleName: 'ttobak-crawler-daily',
      schedule: events.Schedule.cron({ hour: '19', minute: '0' }),
      targets: [new eventsTargets.SfnStateMachine(stateMachine)],
    });

    // Outputs
    new cdk.CfnOutput(this, 'StateMachineArn', {
      value: stateMachine.stateMachineArn,
      exportName: 'TtobakCrawlerStateMachineArn',
    });
  }
}
```

- [ ] **Step 2: Wire into infra.ts**

Add import and stack creation after GatewayStack, before FrontendStack:

```typescript
import { CrawlerStack } from '../lib/crawler-stack';

// After gatewayStack definition:
const crawlerStack = new CrawlerStack(app, 'TtobakCrawlerStack', {
  env,
  description: 'Ttobak AI Meeting Assistant - Crawler (Step Functions + Lambda)',
  crawlerRole: aiStack.crawlerRole,
  table: storageStack.table,
  kbBucket: knowledgeStack.kbBucket,
  knowledgeBaseId: knowledgeStack.knowledgeBaseId,
  dataSourceId: knowledgeStack.dataSourceId,
});
crawlerStack.addDependency(aiStack);
crawlerStack.addDependency(storageStack);
crawlerStack.addDependency(knowledgeStack);
```

- [ ] **Step 3: CDK synth to validate**

```bash
cd infra && npx cdk synth TtobakCrawlerStack 2>&1 | tail -20
```

- [ ] **Step 4: Commit**

```bash
git add infra/lib/crawler-stack.ts infra/bin/infra.ts
git commit -m "feat(infra): add CrawlerStack with Step Functions, 4 Lambdas, daily schedule"
```

---

## Phase 4: Python Crawler Lambdas

### Task 9: Crawler Lambda Code

**Files:**
- Create: `backend/python/crawler/orchestrator.py`
- Create: `backend/python/crawler/tech_crawler.py`
- Create: `backend/python/crawler/news_crawler.py`
- Create: `backend/python/crawler/ingest_trigger.py`
- Create: `backend/python/crawler/requirements.txt`

- [ ] **Step 1: Create orchestrator.py**

```python
import os
import boto3

TABLE_NAME = os.environ.get('TABLE_NAME', 'ttobak-main')
dynamodb = boto3.resource('dynamodb')
table = dynamodb.Table(TABLE_NAME)


def handler(event, context):
    result = table.scan(
        FilterExpression='begins_with(PK, :prefix) AND SK = :config AND #s <> :disabled',
        ExpressionAttributeNames={'#s': 'status'},
        ExpressionAttributeValues={
            ':prefix': 'CRAWLER#',
            ':config': 'CONFIG',
            ':disabled': 'disabled',
        },
        ProjectionExpression='sourceId, sourceName, awsServices, newsQueries, customUrls, #s',
    )
    sources = result.get('Items', [])
    return {'sources': sources}
```

- [ ] **Step 2: Create tech_crawler.py**

```python
import hashlib
import json
import os
import logging
import urllib.request
import urllib.parse

import boto3

logger = logging.getLogger()
logger.setLevel(logging.INFO)

TABLE_NAME = os.environ.get('TABLE_NAME', 'ttobak-main')
KB_BUCKET = os.environ.get('KB_BUCKET_NAME', '')
HAIKU_MODEL = os.environ.get('HAIKU_MODEL_ID', 'global.anthropic.claude-haiku-4-5-20251001-v1:0')

s3 = boto3.client('s3')
dynamodb = boto3.resource('dynamodb')
table = dynamodb.Table(TABLE_NAME)
bedrock = boto3.client('bedrock-runtime')

AWS_DOCS_SEARCH_URL = 'https://proxy.search.docs.aws.com/search'
SERVICE_PAGE_TYPES = ['what-is', 'getting-started', 'best-practices', 'faq']


def handler(event, context):
    source_id = event.get('sourceId', '')
    aws_services = event.get('awsServices', [])
    docs_added = 0
    docs_updated = 0
    errors = []

    for svc in aws_services:
        try:
            urls = discover_docs(svc)
            for url in urls[:10]:
                added = process_doc(source_id, svc, url)
                if added == 'added':
                    docs_added += 1
                elif added == 'updated':
                    docs_updated += 1
        except Exception as e:
            logger.error(f'Error crawling {svc}: {e}')
            errors.append(f'{svc}: {str(e)}')

    return {'docsAdded': docs_added, 'docsUpdated': docs_updated, 'errors': errors}


def discover_docs(service_name):
    try:
        params = urllib.parse.urlencode({
            'searchQuery': f'{service_name} best practices getting started',
            'locale': 'en_us',
            'size': 10,
        })
        req = urllib.request.Request(f'{AWS_DOCS_SEARCH_URL}?{params}')
        with urllib.request.urlopen(req, timeout=10) as resp:
            data = json.loads(resp.read())
        return [hit.get('href', '') for hit in data.get('hits', []) if hit.get('href')]
    except Exception as e:
        logger.warning(f'Doc discovery failed for {service_name}: {e}')
        return []


def process_doc(source_id, service_name, url):
    doc_hash = hashlib.sha256(f'tech:{url}'.encode()).hexdigest()[:16]
    s3_key = f'shared/aws-docs/{service_name.lower()}/{doc_hash}.md'

    existing = table.get_item(
        Key={'PK': f'CRAWLER#{source_id}', 'SK': f'DOC#{doc_hash}'},
    ).get('Item')
    if existing:
        return 'skipped'

    try:
        req = urllib.request.Request(url, headers={'User-Agent': 'Ttobak-Crawler/1.0'})
        with urllib.request.urlopen(req, timeout=15) as resp:
            html = resp.read().decode('utf-8', errors='ignore')
    except Exception:
        return 'error'

    from html.parser import HTMLParser
    class TextExtractor(HTMLParser):
        def __init__(self):
            super().__init__()
            self.text = []
            self.skip = False
        def handle_starttag(self, tag, attrs):
            if tag in ('script', 'style', 'nav', 'header', 'footer'):
                self.skip = True
        def handle_endtag(self, tag):
            if tag in ('script', 'style', 'nav', 'header', 'footer'):
                self.skip = False
        def handle_data(self, data):
            if not self.skip:
                self.text.append(data.strip())

    parser = TextExtractor()
    parser.feed(html)
    text = '\n'.join(t for t in parser.text if t)[:8000]

    if len(text) < 100:
        return 'skipped'

    title = url.split('/')[-1].replace('.html', '').replace('-', ' ').title()
    summary = summarize_with_haiku(text[:4000], title)

    content = f'# {title}\n\nSource: {url}\nService: {service_name}\n\n{text}'
    s3.put_object(Bucket=KB_BUCKET, Key=s3_key, Body=content.encode('utf-8'), ContentType='text/markdown')

    table.put_item(Item={
        'PK': f'CRAWLER#{source_id}',
        'SK': f'DOC#{doc_hash}',
        'type': 'tech',
        'title': title,
        'url': url,
        'source': 'AWS Docs',
        'summary': summary,
        'awsServices': [service_name],
        's3Key': s3_key,
        'crawledAt': __import__('datetime').datetime.utcnow().isoformat() + 'Z',
        'inKB': True,
    })
    return 'added'


def summarize_with_haiku(text, title):
    try:
        resp = bedrock.converse(
            modelId=HAIKU_MODEL,
            messages=[{'role': 'user', 'content': [{'text': f'Summarize this AWS documentation in 2-3 sentences in Korean:\n\nTitle: {title}\n\n{text}'}]}],
            inferenceConfig={'maxTokens': 200},
        )
        return resp['output']['message']['content'][0]['text']
    except Exception as e:
        logger.warning(f'Summarization failed: {e}')
        return text[:200]
```

- [ ] **Step 3: Create news_crawler.py**

```python
import hashlib
import json
import os
import logging
import urllib.request
import urllib.parse
import xml.etree.ElementTree as ET
from datetime import datetime

import boto3

logger = logging.getLogger()
logger.setLevel(logging.INFO)

TABLE_NAME = os.environ.get('TABLE_NAME', 'ttobak-main')
KB_BUCKET = os.environ.get('KB_BUCKET_NAME', '')
HAIKU_MODEL = os.environ.get('HAIKU_MODEL_ID', 'global.anthropic.claude-haiku-4-5-20251001-v1:0')
NAVER_CLIENT_ID = os.environ.get('NAVER_CLIENT_ID', '')
NAVER_CLIENT_SECRET = os.environ.get('NAVER_CLIENT_SECRET', '')

s3 = boto3.client('s3')
dynamodb = boto3.resource('dynamodb')
table = dynamodb.Table(TABLE_NAME)
bedrock = boto3.client('bedrock-runtime')


def handler(event, context):
    source_id = event.get('sourceId', '')
    news_queries = event.get('newsQueries', [])
    custom_urls = event.get('customUrls', [])
    docs_added = 0
    errors = []

    for query in news_queries:
        try:
            articles = search_google_news(query)
            for article in articles[:5]:
                if process_article(source_id, article):
                    docs_added += 1
        except Exception as e:
            logger.error(f'News search failed for {query}: {e}')
            errors.append(str(e))

    for url in custom_urls:
        try:
            articles = fetch_rss(url)
            for article in articles[:5]:
                if process_article(source_id, article):
                    docs_added += 1
        except Exception as e:
            logger.error(f'Custom URL failed {url}: {e}')
            errors.append(str(e))

    return {'docsAdded': docs_added, 'errors': errors}


def search_google_news(query):
    encoded = urllib.parse.quote(query)
    url = f'https://news.google.com/rss/search?q={encoded}&hl=ko&gl=KR&ceid=KR:ko'
    return fetch_rss(url)


def fetch_rss(url):
    try:
        req = urllib.request.Request(url, headers={'User-Agent': 'Ttobak-Crawler/1.0'})
        with urllib.request.urlopen(req, timeout=10) as resp:
            xml_data = resp.read()
        root = ET.fromstring(xml_data)
        articles = []
        for item in root.iter('item'):
            title = item.findtext('title', '')
            link = item.findtext('link', '')
            pub_date = item.findtext('pubDate', '')
            source = item.findtext('source', 'Unknown')
            if title and link:
                articles.append({
                    'title': title,
                    'url': link,
                    'source': source,
                    'pubDate': pub_date,
                })
        return articles
    except Exception as e:
        logger.warning(f'RSS fetch failed for {url}: {e}')
        return []


def process_article(source_id, article):
    url = article['url']
    doc_hash = hashlib.sha256(f"news:{url}".encode()).hexdigest()[:16]

    existing = table.get_item(
        Key={'PK': f'CRAWLER#{source_id}', 'SK': f'DOC#{doc_hash}'},
    ).get('Item')
    if existing:
        return False

    try:
        req = urllib.request.Request(url, headers={'User-Agent': 'Ttobak-Crawler/1.0'})
        with urllib.request.urlopen(req, timeout=10) as resp:
            html = resp.read().decode('utf-8', errors='ignore')
    except Exception:
        return False

    from html.parser import HTMLParser
    class ParagraphExtractor(HTMLParser):
        def __init__(self):
            super().__init__()
            self.in_p = False
            self.paragraphs = []
        def handle_starttag(self, tag, attrs):
            if tag == 'p':
                self.in_p = True
                self.paragraphs.append('')
        def handle_endtag(self, tag):
            if tag == 'p':
                self.in_p = False
        def handle_data(self, data):
            if self.in_p and self.paragraphs:
                self.paragraphs[-1] += data.strip() + ' '

    parser = ParagraphExtractor()
    parser.feed(html)
    body = '\n\n'.join(p.strip() for p in parser.paragraphs if len(p.strip()) > 30)[:6000]

    if len(body) < 50:
        body = article['title']

    summary = summarize_article(article['title'], body)
    s3_key = f'shared/news/{source_id}/{doc_hash}.md'

    content = f"# {article['title']}\n\nSource: {article.get('source', 'Unknown')}\nURL: {url}\nDate: {article.get('pubDate', '')}\n\n{body}"
    s3.put_object(Bucket=KB_BUCKET, Key=s3_key, Body=content.encode('utf-8'), ContentType='text/markdown')

    table.put_item(Item={
        'PK': f'CRAWLER#{source_id}',
        'SK': f'DOC#{doc_hash}',
        'type': 'news',
        'title': article['title'],
        'url': url,
        'source': article.get('source', 'Unknown'),
        'summary': summary,
        's3Key': s3_key,
        'crawledAt': datetime.utcnow().isoformat() + 'Z',
        'inKB': True,
    })
    return True


def summarize_article(title, body):
    try:
        resp = bedrock.converse(
            modelId=HAIKU_MODEL,
            messages=[{'role': 'user', 'content': [{'text': f'Summarize this news article in 2-3 sentences in Korean:\n\nTitle: {title}\n\n{body[:3000]}'}]}],
            inferenceConfig={'maxTokens': 200},
        )
        return resp['output']['message']['content'][0]['text']
    except Exception as e:
        logger.warning(f'Summary failed: {e}')
        return body[:200]
```

- [ ] **Step 4: Create ingest_trigger.py**

```python
import os
import logging
import boto3

logger = logging.getLogger()
logger.setLevel(logging.INFO)

KB_ID = os.environ.get('KB_ID', '')
DATA_SOURCE_ID = os.environ.get('DATA_SOURCE_ID', '')

bedrock_agent = boto3.client('bedrock-agent')


def handler(event, context):
    if not KB_ID or not DATA_SOURCE_ID:
        logger.warning('KB_ID or DATA_SOURCE_ID not set, skipping ingestion')
        return {'status': 'skipped', 'reason': 'KB not configured'}

    try:
        resp = bedrock_agent.start_ingestion_job(
            knowledgeBaseId=KB_ID,
            dataSourceId=DATA_SOURCE_ID,
        )
        job_id = resp['ingestionJob']['ingestionJobId']
        logger.info(f'Started ingestion job: {job_id}')
        return {'status': 'started', 'jobId': job_id}
    except Exception as e:
        logger.error(f'Ingestion trigger failed: {e}')
        return {'status': 'error', 'error': str(e)}
```

- [ ] **Step 5: Create requirements.txt**

```
boto3
```

- [ ] **Step 6: Commit**

```bash
git add backend/python/crawler/
git commit -m "feat(crawler): add orchestrator, tech/news crawlers, and ingestion trigger Lambdas"
```

---

## Phase 5: Frontend — Insights Page

### Task 10: TypeScript Types & API Client

**Files:**
- Modify: `frontend/src/types/meeting.ts` (add types)
- Modify: `frontend/src/lib/api.ts` (add API clients)

- [ ] **Step 1: Add types to meeting.ts**

Add after existing types:

```typescript
export interface CrawlerSource {
  sourceId: string;
  sourceName: string;
  subscribers: string[];
  awsServices: string[];
  newsQueries: string[];
  customUrls: string[];
  schedule: string;
  lastCrawledAt: string;
  status: string;
  documentCount: number;
}

export interface CrawlerSubscription {
  sourceId: string;
  awsServices: string[];
  newsSources: string[];
  customUrls: string[];
  addedAt: string;
}

export interface CrawlerSourceResponse {
  source: CrawlerSource;
  subscription: CrawlerSubscription;
}

export interface CrawledDocument {
  docHash: string;
  type: 'news' | 'tech';
  title: string;
  url: string;
  source: string;
  summary: string;
  awsServices?: string[];
  s3Key: string;
  crawledAt: string;
  inKB: boolean;
}

export interface CrawlHistory {
  timestamp: string;
  docsAdded: number;
  docsUpdated: number;
  errors: string[];
  duration: number;
}
```

- [ ] **Step 2: Add API clients to api.ts**

Add after existing API objects:

```typescript
export const crawlerApi = {
  listSources: () =>
    api.get<{ sources: import('@/types/meeting').CrawlerSourceResponse[] }>('/api/crawler/sources'),
  addSource: (data: {
    sourceName: string;
    awsServices: string[];
    newsSources: string[];
    customUrls?: string[];
    newsQueries?: string[];
  }) => api.post<import('@/types/meeting').CrawlerSourceResponse>('/api/crawler/sources', data),
  updateSource: (sourceId: string, data: {
    awsServices: string[];
    newsSources: string[];
    customUrls?: string[];
  }) => api.put<{ status: string }>(`/api/crawler/sources/${sourceId}`, data),
  unsubscribe: (sourceId: string) =>
    api.delete(`/api/crawler/sources/${sourceId}`),
  getHistory: (sourceId: string) =>
    api.get<{ history: import('@/types/meeting').CrawlHistory[] }>(`/api/crawler/sources/${sourceId}/history`),
};

export const insightsApi = {
  list: (params: { type: string; source?: string; service?: string; page?: number; limit?: number }) => {
    const q = new URLSearchParams();
    q.set('type', params.type);
    if (params.source) q.set('source', params.source);
    if (params.service) q.set('service', params.service);
    q.set('page', String(params.page || 1));
    q.set('limit', String(params.limit || 20));
    return api.get<{
      documents: import('@/types/meeting').CrawledDocument[];
      totalCount: number;
      page: number;
      limit: number;
    }>(`/api/insights?${q.toString()}`);
  },
};
```

- [ ] **Step 3: Commit**

```bash
git add frontend/src/types/meeting.ts frontend/src/lib/api.ts
git commit -m "feat(frontend): add crawler and insights TypeScript types and API clients"
```

---

### Task 11: Navigation — Add Insights Link

**Files:**
- Modify: `frontend/src/components/layout/Sidebar.tsx` (line ~10)
- Modify: `frontend/src/components/layout/MobileNav.tsx` (line ~9)

- [ ] **Step 1: Add to Sidebar mainNav array**

Insert before the Settings entry:

```typescript
{ href: '/insights', icon: 'insights', label: 'Insights' },
```

- [ ] **Step 2: Add to MobileNav navItems**

Replace the Files entry with Insights (keep 4 items for mobile):

```typescript
{ href: '/insights', icon: 'insights', label: 'Insights' },
```

- [ ] **Step 3: Commit**

```bash
git add frontend/src/components/layout/Sidebar.tsx frontend/src/components/layout/MobileNav.tsx
git commit -m "feat(nav): add Insights link to sidebar and mobile navigation"
```

---

### Task 12: Insights Page Component

**Files:**
- Create: `frontend/src/app/insights/page.tsx`
- Create: `frontend/src/components/InsightsList.tsx`

- [ ] **Step 1: Create the page wrapper**

Create `frontend/src/app/insights/page.tsx` following the settings page pattern (`AppLayout`, auth guard, mobile header).

- [ ] **Step 2: Create InsightsList component**

Create `frontend/src/components/InsightsList.tsx` with:
- Two tabs (News / Tech) using `useState` for active tab
- `useEffect` + `insightsApi.list()` for data fetching
- Filter dropdowns (source for News, service for Tech)
- Card list with: title, source badge, date, summary, `[Open]` external link, `[KB+]` button
- Pagination controls
- Loading skeleton and empty states
- Follow `KBFileList.tsx` patterns for styling (glass-panel cards, dark mode tokens, Material Symbols)

- [ ] **Step 3: Build and verify**

```bash
cd frontend && npm run build
```

- [ ] **Step 4: Commit**

```bash
git add frontend/src/app/insights/ frontend/src/components/InsightsList.tsx
git commit -m "feat(frontend): add Insights page with News and Tech tabs"
```

---

### Task 13: Settings — Crawler Sources Section

**Files:**
- Create: `frontend/src/components/CrawlerSettings.tsx`
- Modify: `frontend/src/app/settings/page.tsx` (add section)

- [ ] **Step 1: Create CrawlerSettings component**

Create `frontend/src/components/CrawlerSettings.tsx` with:
- Source list: cards with customer name, AWS services tags, news sources, status badge, last crawled, doc count
- Add Source modal: customer name input (with "Already tracked" indicator), AWS service tag selector (popular presets + free input), news source checkboxes, custom URL inputs, schedule dropdown
- Per-source menu (⋮): Edit, View History, Crawl Now, Unsubscribe
- Follow `IntegrationSettings.tsx` card pattern (glass-panel, loading/saving states)

- [ ] **Step 2: Add to settings page**

Import and render `<CrawlerSettings />` in a new section between Integrations and Developer Tools:

```tsx
<section className="lg:pb-8 lg:border-b lg:border-slate-200 dark:lg:border-white/10">
  <h3 className="section-header mb-4">Crawler Sources</h3>
  <CrawlerSettings />
</section>
```

- [ ] **Step 3: Build and verify**

```bash
cd frontend && npm run build
```

- [ ] **Step 4: Commit**

```bash
git add frontend/src/components/CrawlerSettings.tsx frontend/src/app/settings/page.tsx
git commit -m "feat(settings): add Crawler Sources section with add/edit/unsubscribe"
```

---

## Phase 6: Deploy & Verify

### Task 14: Build, Deploy, and Validate

- [ ] **Step 1: Build all Go Lambda binaries**

```bash
cd backend && for dir in cmd/api cmd/transcribe cmd/summarize cmd/process-image cmd/kb; do
  GOOS=linux GOARCH=arm64 /usr/local/go/bin/go build -tags lambda.norpc -o $dir/bootstrap ./$dir
done
```

- [ ] **Step 2: Build frontend**

```bash
cd frontend && npm run build
```

- [ ] **Step 3: CDK synth all stacks**

```bash
cd infra && npx cdk synth
```

- [ ] **Step 4: CDK deploy**

```bash
cd infra && npx cdk deploy --all
```

- [ ] **Step 5: Deploy frontend**

```bash
aws s3 sync frontend/out/ s3://ttobak-site-180294183052-ap-northeast-2/ --delete
aws cloudfront create-invalidation --distribution-id E3IFMH57E9UTB5 --paths "/*"
```

- [ ] **Step 6: Verify API endpoints**

```bash
# Test crawler sources endpoint (requires auth token)
curl -H "Authorization: Bearer $TOKEN" https://ttobak.atomai.click/api/crawler/sources
curl -H "Authorization: Bearer $TOKEN" https://ttobak.atomai.click/api/insights?type=news
```

- [ ] **Step 7: Verify Step Functions in AWS Console**

Check that `ttobak-crawler-workflow` state machine exists and the daily EventBridge rule is active.

- [ ] **Step 8: Final commit**

```bash
git add -A
git commit -m "feat: crawler & insights — complete deployment"
```
