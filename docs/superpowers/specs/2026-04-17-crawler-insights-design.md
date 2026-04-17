# Crawler & Insights Feature Design

## Overview

Enhance Ttobak's AI capabilities for AWS Solutions Architects by adding automated crawling of AWS documentation and customer news/articles into the shared Knowledge Base. Crawled content is browsable via a new Insights page (News + Tech tabs) and enriches QA RAG responses.

## Requirements

- Crawl AWS documentation per customer's tech stack (service-specific docs)
- Crawl customer news from configurable sources (Naver News, Google News, tech media, custom URLs)
- Periodic automatic crawling via EventBridge schedule (daily)
- Shared KB for crawled content (all SAs access), personal KB for meeting docs (existing)
- Deduplication: same customer registered by multiple SAs triggers only one crawl
- Settings UI for managing crawler sources (add/edit/remove customers, select AWS services, configure news sources)
- Insights page with News and Tech tabs for browsing crawled content
- Lambda + Step Functions architecture (serverless, failure isolation)

## Data Model (DynamoDB — ttobak-main, single-table)

### Crawler Source (system-level, one per customer)

```
PK: CRAWLER#<sourceId>
SK: CONFIG
Attributes:
  sourceName: string          # "하나은행"
  sourceId: string            # normalized key "hanabank"
  subscribers: string[]       # [userId1, userId2]
  awsServices: string[]       # union of all subscribers' selections
  newsQueries: string[]       # search keywords ["하나은행", "하나금융그룹"]
  customUrls: string[]        # user-provided RSS/URLs
  schedule: string            # "daily"
  lastCrawledAt: string       # ISO 8601
  status: string              # "idle" | "crawling" | "error"
  documentCount: number
```

### User Subscription (per-SA)

```
PK: USER#<userId>
SK: CRAWL_SUB#<sourceId>
Attributes:
  awsServices: string[]       # this user's selected services
  newsSources: string[]       # ["naver", "google", "zdnet"]
  customUrls: string[]        # this user's custom URLs
  addedAt: string
```

### Crawled Document Metadata

```
PK: CRAWLER#<sourceId>
SK: DOC#<docHash>
Attributes:
  type: string                # "news" | "tech"
  title: string
  url: string                 # original source URL
  source: string              # "ZDNet Korea", "AWS Docs", etc.
  summary: string             # LLM-generated 2-3 line summary
  awsServices: string[]       # relevant services (tech type)
  s3Key: string               # S3 path to full content
  crawledAt: string
  inKB: boolean               # true after KB ingestion
```

### Crawling History

```
PK: CRAWLER#<sourceId>
SK: HISTORY#<timestamp>
Attributes:
  docsAdded: number
  docsUpdated: number
  errors: string[]
  duration: number            # seconds
```

## Deduplication Strategy

### Source-Level Dedup

When an SA adds "하나은행":
1. Normalize name to sourceId: `normalize("하나은행")` → `"hanabank"`
2. Check if `CRAWLER#hanabank` exists
3. If exists: add userId to `subscribers`, merge `awsServices` as union
4. If not: create new `CRAWLER#hanabank` record

### Document-Level Dedup

```python
doc_hash = sha256(f"{source_type}:{url}").hexdigest()[:16]
```

Same URL always produces same hash → same S3 key → overwrite on re-crawl.

### Crawling Lock

Step Functions sets `status: "crawling"` with a DynamoDB conditional update (`status = "idle"`). If already crawling, the source is skipped.

### Unsubscribe

SA removes a source → removed from `subscribers`. When last subscriber leaves → `status: "disabled"`, data retained.

## Crawler Architecture

### Step Functions Workflow

```
EventBridge (cron: 0 19 * * ? *)     # KST 04:00 daily
  │
  ▼
TtobakCrawlerWorkflow (Step Functions)
  │
  ├─ 1. ListActiveSources Lambda
  │     Scan CRAWLER#* where status != "disabled"
  │     Output: array of source configs
  │
  ├─ 2. Map (parallel, per source)
  │     ├─ 2a. AcquireLock
  │     │     Conditional update: status idle → crawling
  │     │
  │     ├─ 2b. Parallel
  │     │     ├─ TechCrawler Lambda
  │     │     │   Per-service: fetch key pages (What Is, Best Practices, FAQ)
  │     │     │   Use search_aws_docs API for URL discovery
  │     │     │   Change detection via ETag comparison
  │     │     │   LLM summary via Haiku (cost-efficient)
  │     │     │   Write: S3 shared/aws-docs/{service}/{hash}.md
  │     │     │   Write: DynamoDB DOC# metadata
  │     │     │
  │     │     └─ NewsCrawler Lambda
  │     │         Naver News API + Google News RSS + custom URLs
  │     │         Article extraction via newspaper3k / BeautifulSoup
  │     │         LLM summary via Haiku
  │     │         Dedup: skip if DOC# already exists for URL hash
  │     │         Write: S3 shared/news/{sourceId}/{hash}.md
  │     │         Write: DynamoDB DOC# metadata
  │     │
  │     ├─ 2c. ReleaseLock
  │     │     status → idle, update lastCrawledAt, documentCount
  │     │
  │     └─ 2d. Catch → ErrorHandler
  │           status → error, log details
  │
  └─ 3. TriggerIngestion Lambda
        Bedrock StartIngestionJob for shared/ prefix
```

### Lambda Specifications

| Lambda | Runtime | Timeout | Memory | Role |
|--------|---------|---------|--------|------|
| ttobak-crawler-orchestrator | Python 3.12 | 30s | 256MB | DynamoDB read |
| ttobak-crawler-tech | Python 3.12 | 5min | 512MB | S3 write, DynamoDB write, HTTPS egress, Bedrock Haiku |
| ttobak-crawler-news | Python 3.12 | 5min | 512MB | S3 write, DynamoDB write, HTTPS egress, Bedrock Haiku |
| ttobak-crawler-ingest | Python 3.12 | 30s | 256MB | Bedrock StartIngestionJob |

### Crawling Sources

**TechCrawler:**
- AWS official docs (`docs.aws.amazon.com/{service}/`)
- Per-service key pages: What Is, Getting Started, Best Practices, FAQ
- URL discovery via existing `search_aws_docs` public API
- Change detection: compare S3 ETag, only re-crawl changed docs

**NewsCrawler:**
- Naver News Search API (free tier: 25,000 requests/day)
- Google News RSS feed (unlimited)
- ZDNet Korea, IT Chosun via RSS
- Custom URLs: RSS auto-detect or HTML parsing (BeautifulSoup)
- Article body extraction: newspaper3k library

## S3 Key Structure (KB Bucket)

```
ttobak-kb-{ACCOUNT_ID}/
  kb/{userId}/                        # existing: user-uploaded docs
  meetings/{userId}/{meetingId}.md    # existing: auto-exported meeting docs
  shared/                             # NEW: crawler output
    aws-docs/{service}/{hash}.md      # AWS documentation
    news/{sourceId}/{hash}.md         # customer news/articles
```

## API Endpoints

### Crawler Settings (ttobak-api)

```
GET    /api/crawler/sources              # list my subscriptions
POST   /api/crawler/sources              # add/subscribe to source
PUT    /api/crawler/sources/{sourceId}   # update my settings
DELETE /api/crawler/sources/{sourceId}   # unsubscribe
GET    /api/crawler/sources/{sourceId}/history  # crawling history
POST   /api/crawler/sources/{sourceId}/crawl    # manual trigger
```

### Insights (ttobak-api)

```
GET    /api/insights?type=news&source={sourceId}&page=1&limit=20
GET    /api/insights?type=tech&service={service}&page=1&limit=20
GET    /api/insights/{docHash}
POST   /api/insights/{docHash}/add-to-kb
```

## Frontend

### Navigation Change

Sidebar: `Meetings | Files | Knowledge Base | Insights | Settings`

Mobile bottom nav: Add Insights icon (Material Symbol: `newspaper`).

### Insights Page (`/insights`)

Two tabs: **News** and **Tech**.

**News tab:**
- Card list: title, source name, date, customer tag, 2-3 line summary
- Filters: customer (dropdown), news source (multi-select)
- Actions per card: Open (external link), KB+ (add to KB if not already)
- Pagination

**Tech tab:**
- Card list: title, AWS service tag, doc type (Guide/Blog/FAQ), last crawled
- Filters: AWS service, doc type
- Actions: Open, KB+
- Pagination

### Settings — Crawler Sources Section

Added between Integrations and Developer Tools.

**Source list:** Cards showing customer name, AWS services, news sources, last crawled, doc count, status badge.

**Add Source modal:**
- Customer name input (shows "Already tracked by N SA(s)" if exists)
- AWS services: tag selector with popular presets + free input
- News sources: checkbox grid (Naver, Google, ZDNet, IT Chosun)
- Custom URLs: repeatable input field
- Schedule: dropdown (Daily)

**Source card menu (⋮):** Edit, View History, Crawl Now, Unsubscribe.

## QA Lambda Modification

Current KB retrieval filters by `kb/{userId}/` in source URI. Modify to also include `shared/` prefix:

```python
# Before
filter = {"contains": {"key": "x-amz-bedrock-kb-source-uri", "value": f"kb/{user_id}/"}}

# After
filter = {"orAll": [
    {"contains": {"key": "x-amz-bedrock-kb-source-uri", "value": f"kb/{user_id}/"}},
    {"contains": {"key": "x-amz-bedrock-kb-source-uri", "value": f"meetings/{user_id}/"}},
    {"contains": {"key": "x-amz-bedrock-kb-source-uri", "value": "shared/"}},
]}
```

## CDK Changes

### New Stack: CrawlerStack

Dependencies: StorageStack (table, bucket), KnowledgeStack (kbBucket)

Resources:
- 4 Lambda functions (Python 3.12, ARM64)
- 4 IAM roles (scoped per Lambda)
- Step Functions StateMachine (Standard, not Express)
- EventBridge Rule (daily cron)
- EventBridge Rule (manual trigger event pattern)

### Stack Dependency Order (updated)

```
Auth + Storage (parallel) → AI → Knowledge → EdgeAuth → Gateway → Crawler → Frontend
```

CrawlerStack depends on Gateway (for DynamoDB table and S3 buckets) and Knowledge (for KB bucket and ingestion).

## Cost Estimates

| Resource | Estimated Monthly Cost |
|----------|----------------------|
| Lambda (4 functions, daily runs) | ~$2-5 |
| Step Functions (30 executions/day) | ~$1 |
| Bedrock Haiku (summarization) | ~$5-15 (depends on doc volume) |
| Bedrock KB ingestion | ~$3-5 |
| S3 storage (shared docs) | < $1 |
| Naver API | Free (within limits) |
| **Total** | **~$12-27/month** |

## Out of Scope

- Real-time crawling (push-based / webhook)
- Full-text search within Insights (use QA for semantic search)
- Crawling authentication-required pages (IR systems behind login)
- Multi-language support (Korean-only for news, English for AWS docs)
- Custom crawl schedules per source (all sources run on same daily schedule)
