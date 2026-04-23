# Deep Research Agent Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a Deep Research Agent that performs multi-source web research, generates citation-backed reports, and stores them in the shared KB. Accessible via a new "Research" tab on the Insights page.

**Architecture:** Bedrock Agents (classic) with action groups for tool execution. Go API Lambda handles CRUD + triggers agent invocation asynchronously. Agent uses Claude Sonnet 4.6 with web_search, fetch_page, save_report, and search_kb tools implemented as Lambda-backed action groups. Frontend extends Insights with a Research tab, New Research modal, and detail page.

**Tech Stack:** Go (chi router), CDK TypeScript, Bedrock Agents API, Python 3.12 (tool Lambdas), Next.js 16, React, Tailwind v4

---

## Phase 1: Data Model & Backend API

### Task 1: DynamoDB Model Types

**Files:**
- Modify: `backend/internal/model/meeting.go` (add prefix)
- Modify: `backend/internal/model/request.go` (add types)

- [ ] **Step 1: Add prefix constant to meeting.go**

Add after existing prefixes (around line 183):

```go
PrefixResearch = "RESEARCH#"
```

- [ ] **Step 2: Add Research model struct to meeting.go**

Add after existing entity types:

```go
type Research struct {
	ResearchID   string `dynamodbav:"researchId" json:"researchId"`
	UserID       string `dynamodbav:"userId" json:"userId"`
	Topic        string `dynamodbav:"topic" json:"topic"`
	Mode         string `dynamodbav:"mode" json:"mode"`
	Status       string `dynamodbav:"status" json:"status"`
	CreatedAt    string `dynamodbav:"createdAt" json:"createdAt"`
	CompletedAt  string `dynamodbav:"completedAt,omitempty" json:"completedAt,omitempty"`
	S3Key        string `dynamodbav:"s3Key,omitempty" json:"s3Key,omitempty"`
	SourceCount  int    `dynamodbav:"sourceCount,omitempty" json:"sourceCount,omitempty"`
	WordCount    int    `dynamodbav:"wordCount,omitempty" json:"wordCount,omitempty"`
	Summary      string `dynamodbav:"summary,omitempty" json:"summary,omitempty"`
	ErrorMessage string `dynamodbav:"errorMessage,omitempty" json:"errorMessage,omitempty"`
}
```

- [ ] **Step 3: Add request/response types to request.go**

Add after existing types:

```go
type CreateResearchRequest struct {
	Topic string `json:"topic"`
	Mode  string `json:"mode"`
}

type ResearchResponse struct {
	Research
	Content string `json:"content,omitempty"`
}

type ResearchListResponse struct {
	Research []Research `json:"research"`
}
```

- [ ] **Step 4: Build to verify**

```bash
cd /home/ec2-user/ttobak/backend && /usr/local/go/bin/go build ./...
```

- [ ] **Step 5: Commit**

```bash
cd /home/ec2-user/ttobak && git add backend/internal/model/
git commit -m "feat(model): add Research DynamoDB types and API request/response types

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: Research Repository

**Files:**
- Create: `backend/internal/repository/research.go`

- [ ] **Step 1: Create research repository**

Follow the crawler repository pattern. Methods needed:

1. `NewResearchRepository(client *dynamodb.Client, tableName string) *ResearchRepository`
2. `CreateResearch(ctx, *model.Research) error` — PK: RESEARCH#{id}, SK: CONFIG + PK: USER#{userId}, SK: RESEARCH#{id}
3. `GetResearch(ctx, researchId) (*model.Research, error)` — PK: RESEARCH#{id}, SK: CONFIG
4. `UpdateResearchStatus(ctx, researchId, status, fields map[string]interface{}) error` — partial update
5. `ListUserResearch(ctx, userId) ([]model.Research, error)` — Query PK=USER#{userId}, SK begins_with RESEARCH#, then fetch each
6. `DeleteResearch(ctx, researchId, userId) error` — delete both RESEARCH# and USER# records

Use expression builder for all DynamoDB operations. Use private wrapper struct `researchItem` embedding `model.Research` with PK/SK/EntityType.

- [ ] **Step 2: Build to verify**

```bash
cd /home/ec2-user/ttobak/backend && /usr/local/go/bin/go build ./...
```

- [ ] **Step 3: Commit**

```bash
cd /home/ec2-user/ttobak && git add backend/internal/repository/research.go
git commit -m "feat(repo): add research DynamoDB repository

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: Research Service

**Files:**
- Create: `backend/internal/service/research.go`

- [ ] **Step 1: Create research service**

```go
package service

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagentruntime"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
	"io"
)

type ResearchService struct {
	repo         *repository.ResearchRepository
	agentClient  *bedrockagentruntime.Client
	s3Client     *s3.Client
	kbBucketName string
	agentId      string
	agentAliasId string
}

func NewResearchService(
	repo *repository.ResearchRepository,
	agentClient *bedrockagentruntime.Client,
	s3Client *s3.Client,
	kbBucketName, agentId, agentAliasId string,
) *ResearchService {
	return &ResearchService{
		repo: repo, agentClient: agentClient, s3Client: s3Client,
		kbBucketName: kbBucketName, agentId: agentId, agentAliasId: agentAliasId,
	}
}
```

Methods:
1. `CreateResearch(ctx, userId, *model.CreateResearchRequest) (*model.Research, error)`:
   - Generate UUID
   - Create research record (status: "running")
   - If agentClient is configured, invoke Bedrock Agent async with topic+mode+researchId
   - If agentClient is nil, set status to "error" with message "Agent not configured"
   - Return research record

2. `ListResearch(ctx, userId) (*model.ResearchListResponse, error)`:
   - Query user's research records from repo
   - Return sorted by createdAt desc

3. `GetResearchDetail(ctx, researchId) (*model.ResearchResponse, error)`:
   - Get research metadata from DynamoDB
   - If status == "done", read S3 content from s3Key
   - Return metadata + content

4. `DeleteResearch(ctx, researchId, userId) error`:
   - Delete from DynamoDB
   - Delete S3 object if s3Key exists

- [ ] **Step 2: Build to verify**

```bash
cd /home/ec2-user/ttobak/backend && /usr/local/go/bin/go build ./...
```

- [ ] **Step 3: Commit**

```bash
cd /home/ec2-user/ttobak && git add backend/internal/service/research.go
git commit -m "feat(service): add research service with Bedrock Agent invocation

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: Research Handler + Route Registration

**Files:**
- Create: `backend/internal/handler/research.go`
- Modify: `backend/cmd/api/main.go`

- [ ] **Step 1: Create research handler**

4 methods following the crawler handler pattern:
1. `CreateResearch(w, r)` — POST, decode `model.CreateResearchRequest`, validate topic not empty, return 202
2. `ListResearch(w, r)` — GET, return user's research list
3. `GetResearchDetail(w, r)` — GET, `chi.URLParam(r, "researchId")`, return metadata + content
4. `DeleteResearch(w, r)` — DELETE, return 204

Validate `researchId` for path traversal (no `..` or `/`) in GetResearchDetail and DeleteResearch.

- [ ] **Step 2: Wire into main.go**

Add after existing route registrations:

```go
// Research
researchRepo := repository.NewResearchRepository(dynamoClient, tableName)
researchService := service.NewResearchService(researchRepo, agentRuntimeClient, s3Client, kbBucketName, agentId, agentAliasId)
researchHandler := handler.NewResearchHandler(researchService)
```

Register routes in the auth group:

```go
r.Post("/api/research", researchHandler.CreateResearch)
r.Get("/api/research", researchHandler.ListResearch)
r.Get("/api/research/{researchId}", researchHandler.GetResearchDetail)
r.Delete("/api/research/{researchId}", researchHandler.DeleteResearch)
```

Add env vars for agent config:

```go
agentId := os.Getenv("RESEARCH_AGENT_ID")
agentAliasId := os.Getenv("RESEARCH_AGENT_ALIAS_ID")
```

Create `bedrockagentruntime.Client`:

```go
var agentRuntimeClient *bedrockagentruntime.Client
if agentId != "" {
    agentRuntimeClient = bedrockagentruntime.NewFromConfig(cfg)
}
```

- [ ] **Step 3: Build API Lambda**

```bash
cd /home/ec2-user/ttobak/backend && /usr/local/go/bin/go build ./... && GOOS=linux GOARCH=arm64 /usr/local/go/bin/go build -tags lambda.norpc -o cmd/api/bootstrap ./cmd/api
```

- [ ] **Step 4: Commit**

```bash
cd /home/ec2-user/ttobak && git add backend/internal/handler/research.go backend/cmd/api/main.go
git commit -m "feat(api): add research CRUD endpoints with Bedrock Agent trigger

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

---

## Phase 2: Bedrock Agent + Tool Lambdas

### Task 5: Tool Lambda — save_report

**Files:**
- Create: `backend/python/research-tools/save_report.py`
- Create: `backend/python/research-tools/requirements.txt`

- [ ] **Step 1: Create save_report Lambda**

This Lambda is called by the Bedrock Agent's action group when the research is complete.

```python
import json
import os
import logging
import boto3

logger = logging.getLogger()
logger.setLevel(logging.INFO)

TABLE_NAME = os.environ.get('TABLE_NAME', 'ttobak-main')
KB_BUCKET = os.environ.get('KB_BUCKET_NAME', '')

s3 = boto3.client('s3')
dynamodb = boto3.resource('dynamodb')
table = dynamodb.Table(TABLE_NAME)


def handler(event, context):
    """Bedrock Agent action group handler for save_report tool."""
    # Parse action group input
    action = event.get('actionGroup', '')
    function_name = event.get('function', '')
    parameters = {p['name']: p['value'] for p in event.get('parameters', [])}

    research_id = parameters.get('researchId', '')
    content = parameters.get('content', '')
    summary = parameters.get('summary', '')
    source_count = int(parameters.get('sourceCount', '0'))
    word_count = int(parameters.get('wordCount', '0'))

    if not research_id or not content:
        return action_response(event, 'error', 'researchId and content are required')

    s3_key = f'shared/research/{research_id}.md'

    # Write report to S3
    s3.put_object(
        Bucket=KB_BUCKET,
        Key=s3_key,
        Body=content.encode('utf-8'),
        ContentType='text/markdown; charset=utf-8',
    )
    logger.info(f'Saved report to s3://{KB_BUCKET}/{s3_key}')

    # Update DynamoDB status
    from datetime import datetime
    table.update_item(
        Key={'PK': f'RESEARCH#{research_id}', 'SK': 'CONFIG'},
        UpdateExpression='SET #s = :s, completedAt = :c, s3Key = :k, sourceCount = :sc, wordCount = :wc, summary = :sm',
        ExpressionAttributeNames={'#s': 'status'},
        ExpressionAttributeValues={
            ':s': 'done',
            ':c': datetime.utcnow().isoformat() + 'Z',
            ':k': s3_key,
            ':sc': source_count,
            ':wc': word_count,
            ':sm': summary[:1000],
        },
    )
    logger.info(f'Updated research {research_id} status to done')

    return action_response(event, 'saved', f's3://{KB_BUCKET}/{s3_key}')


def action_response(event, status, message):
    """Format response for Bedrock Agent action group."""
    return {
        'messageVersion': '1.0',
        'response': {
            'actionGroup': event.get('actionGroup', ''),
            'function': event.get('function', ''),
            'functionResponse': {
                'responseBody': {
                    'TEXT': {
                        'body': json.dumps({'status': status, 'message': message}),
                    }
                }
            }
        }
    }
```

- [ ] **Step 2: Create requirements.txt**

```
boto3
```

- [ ] **Step 3: Commit**

```bash
cd /home/ec2-user/ttobak && git add backend/python/research-tools/
git commit -m "feat(research): add save_report tool Lambda for Bedrock Agent

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

---

### Task 6: Tool Lambda — fetch_page

**Files:**
- Create: `backend/python/research-tools/fetch_page.py`

- [ ] **Step 1: Create fetch_page Lambda**

Reuses the HTML parsing pattern from news_crawler. Only allows http/https URLs.

```python
import json
import logging
from html.parser import HTMLParser
from urllib.request import Request, urlopen

logger = logging.getLogger()
logger.setLevel(logging.INFO)


class TextExtractor(HTMLParser):
    CONTENT_TAGS = {'p', 'li', 'h1', 'h2', 'h3', 'h4', 'h5', 'h6', 'td', 'th', 'blockquote'}
    SKIP_TAGS = {'script', 'style', 'nav', 'footer', 'header', 'noscript'}

    def __init__(self):
        super().__init__()
        self.pieces = []
        self.in_content = False
        self.skip_depth = 0
        self.current = []

    def handle_starttag(self, tag, attrs):
        if tag in self.SKIP_TAGS:
            self.skip_depth += 1
        elif tag in self.CONTENT_TAGS and self.skip_depth == 0:
            self.in_content = True
            self.current = []

    def handle_endtag(self, tag):
        if tag in self.SKIP_TAGS:
            self.skip_depth = max(0, self.skip_depth - 1)
        elif tag in self.CONTENT_TAGS and self.in_content:
            text = ' '.join(self.current).strip()
            if text:
                self.pieces.append(text)
            self.in_content = False

    def handle_data(self, data):
        if self.in_content and self.skip_depth == 0:
            self.current.append(data.strip())

    def get_text(self):
        return '\n\n'.join(self.pieces)


def handler(event, context):
    parameters = {p['name']: p['value'] for p in event.get('parameters', [])}
    url = parameters.get('url', '')

    if not url.startswith(('http://', 'https://')):
        return action_response(event, '', 'Invalid URL scheme')

    try:
        req = Request(url, headers={'User-Agent': 'TtobakResearch/1.0'})
        with urlopen(req, timeout=15) as resp:
            charset = resp.headers.get_content_charset() or 'utf-8'
            html = resp.read().decode(charset, errors='replace')

        parser = TextExtractor()
        parser.feed(html)
        text = parser.get_text()[:8000]

        title_start = html.find('<title>')
        title_end = html.find('</title>')
        title = html[title_start + 7:title_end].strip() if title_start >= 0 and title_end > title_start else url

        return action_response(event, text, title)
    except Exception as e:
        logger.warning(f'Fetch failed for {url}: {e}')
        return action_response(event, f'Error fetching page: {str(e)}', '')


def action_response(event, content, title):
    return {
        'messageVersion': '1.0',
        'response': {
            'actionGroup': event.get('actionGroup', ''),
            'function': event.get('function', ''),
            'functionResponse': {
                'responseBody': {
                    'TEXT': {
                        'body': json.dumps({'content': content, 'title': title}),
                    }
                }
            }
        }
    }
```

- [ ] **Step 2: Commit**

```bash
cd /home/ec2-user/ttobak && git add backend/python/research-tools/fetch_page.py
git commit -m "feat(research): add fetch_page tool Lambda for web content extraction

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

---

### Task 7: CDK — Research Agent Stack

**Files:**
- Create: `infra/lib/research-agent-stack.ts`
- Modify: `infra/bin/infra.ts`

- [ ] **Step 1: Create research-agent-stack.ts**

CDK stack that creates:
1. IAM role for the Bedrock Agent (bedrock:InvokeModel, s3:PutObject, dynamodb:UpdateItem)
2. Two tool Lambda functions (save_report, fetch_page)
3. Bedrock Agent with system prompt encoding the 8-phase research pipeline
4. Agent action group with function definitions for: web_search (built-in), fetch_page, save_report, search_kb
5. Agent alias for invocation
6. Pass agentId and aliasId as env vars to the API Lambda (via GatewayStack props)

The system prompt should encode the research methodology:
- Phase 1-8 pipeline instructions
- Output format requirements (markdown with citations)
- Tool usage guidance (when to use which tool)
- Quality standards (10+ sources, 3+ per major claim)

- [ ] **Step 2: Wire into infra.ts**

Add after CrawlerStack, before FrontendStack:

```typescript
import { ResearchAgentStack } from '../lib/research-agent-stack';

const researchAgentStack = new ResearchAgentStack(app, 'TtobakResearchAgentStack', {
  env,
  description: 'Ttobak AI Meeting Assistant - Research Agent (Bedrock Agent)',
  table: storageStack.table,
  kbBucket: knowledgeStack.kbBucket,
  knowledgeBaseId: knowledgeStack.knowledgeBaseId,
});
researchAgentStack.addDependency(storageStack);
researchAgentStack.addDependency(knowledgeStack);
```

Pass agentId/aliasId to GatewayStack as props or CDK context.

- [ ] **Step 3: CDK synth to verify**

```bash
cd /home/ec2-user/ttobak/infra && npx cdk synth TtobakResearchAgentStack 2>&1 | tail -20
```

- [ ] **Step 4: Commit**

```bash
cd /home/ec2-user/ttobak && git add infra/lib/research-agent-stack.ts infra/bin/infra.ts
git commit -m "feat(infra): add ResearchAgentStack with Bedrock Agent + tool Lambdas

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

---

## Phase 3: Frontend

### Task 8: TypeScript Types & API Client

**Files:**
- Modify: `frontend/src/types/meeting.ts`
- Modify: `frontend/src/lib/api.ts`

- [ ] **Step 1: Add Research types to meeting.ts**

```typescript
export interface Research {
  researchId: string;
  userId?: string;
  topic: string;
  mode: 'quick' | 'standard' | 'deep';
  status: 'running' | 'done' | 'error';
  createdAt: string;
  completedAt?: string;
  s3Key?: string;
  sourceCount?: number;
  wordCount?: number;
  summary?: string;
  errorMessage?: string;
}

export interface ResearchDetail extends Research {
  content?: string;
}
```

- [ ] **Step 2: Add researchApi to api.ts**

```typescript
export const researchApi = {
  create: (data: { topic: string; mode: string }) =>
    api.post<Research>('/api/research', data),
  list: () =>
    api.get<{ research: Research[] }>('/api/research'),
  getDetail: (researchId: string) =>
    api.get<ResearchDetail>(`/api/research/${encodeURIComponent(researchId)}`),
  delete: (researchId: string) =>
    api.delete(`/api/research/${encodeURIComponent(researchId)}`),
};
```

- [ ] **Step 3: Build to verify**

```bash
cd /home/ec2-user/ttobak/frontend && npm run build 2>&1 | tail -5
```

- [ ] **Step 4: Commit**

```bash
cd /home/ec2-user/ttobak && git add frontend/src/types/meeting.ts frontend/src/lib/api.ts
git commit -m "feat(frontend): add Research types and API client

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

---

### Task 9: Insights Research Tab + New Research Modal

**Files:**
- Modify: `frontend/src/components/InsightsList.tsx`

- [ ] **Step 1: Add Research tab**

Extend the `TabType` to include `'research'`:
```typescript
type TabType = 'news' | 'tech' | 'research';
```

Add third tab button with `science` Material Symbol icon.

- [ ] **Step 2: Add Research tab content**

When `activeTab === 'research'`:
- Fetch from `researchApi.list()` instead of `insightsApi.list()`
- Show "New Research" button (top right)
- Card list: topic, mode badge (Quick/Standard/Deep), status badge (Running animated/Done green/Error red), source count, word count, date
- Running jobs: show elapsed time with animated spinner, poll every 10s via `setInterval`
- Click card → `router.push(/insights/research/${r.researchId})`

- [ ] **Step 3: Add New Research modal**

State: `showNewResearchModal`, `researchTopic`, `researchMode`, `creating`

Modal content:
- Topic textarea input
- Mode segment buttons: Quick (2-5min) / Standard (5-10min) / Deep (10-20min)
- Cancel / Start Research buttons
- On submit: `researchApi.create({ topic, mode })` → close modal → refresh list

- [ ] **Step 4: Build to verify**

```bash
cd /home/ec2-user/ttobak/frontend && npm run build 2>&1 | tail -5
```

- [ ] **Step 5: Commit**

```bash
cd /home/ec2-user/ttobak && git add frontend/src/components/InsightsList.tsx
git commit -m "feat(insights): add Research tab with job list, polling, and New Research modal

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

---

### Task 10: Research Detail Page

**Files:**
- Create: `frontend/src/app/insights/research/[researchId]/page.tsx`
- Create: `frontend/src/app/insights/research/[researchId]/ResearchDetailClient.tsx`
- Modify: `infra/lib/frontend-stack.ts` (CloudFront SPA router)

- [ ] **Step 1: Create page.tsx (static export wrapper)**

```typescript
import ResearchDetailPage from './ResearchDetailClient';

export async function generateStaticParams() {
  return [{ researchId: '_' }];
}

export default async function Page(props: { params: Promise<{ researchId: string }> }) {
  await props.params;
  return <ResearchDetailPage />;
}
```

- [ ] **Step 2: Create ResearchDetailClient.tsx**

Follow the InsightDetailClient pattern:
- Use `usePathname()` (not `useParams`) for static export compatibility
- Extract researchId from pathname: `pathname.split('/insights/research/')[1]`
- Fetch `researchApi.getDetail(researchId)` on mount
- Header card: topic, mode badge, status badge, source count, word count, dates
- Content: `ReactMarkdown` with `remarkGfm` + `rehypeRaw` + `rehypeSanitize`
- Back button → `/insights` (with Research tab active)
- If status is "running": show spinner + "Research in progress..." message, poll every 10s

- [ ] **Step 3: Update CloudFront SPA router**

Add to `frontend-stack.ts` SPA router function:

```javascript
// Dynamic route: /insights/research/{researchId} → /insights/research/_
if (uri.match(/^\/insights\/research\/[^\/]+/) && !uri.match(/^\/insights\/research\/_/)) {
  uri = uri.replace(/^\/insights\/research\/[^\/\.]+/, '/insights/research/_');
  request.uri = uri;
}
```

Add `/insights/research/_` to the `knownPages` array.

- [ ] **Step 4: Build to verify**

```bash
cd /home/ec2-user/ttobak/frontend && npm run build 2>&1 | tail -5
```

- [ ] **Step 5: Commit**

```bash
cd /home/ec2-user/ttobak && git add frontend/src/app/insights/research/ infra/lib/frontend-stack.ts
git commit -m "feat(research): add research detail page with markdown rendering and polling

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

---

## Phase 4: Deploy & Test

### Task 11: Build, Deploy, and Validate

- [ ] **Step 1: Build all Go Lambda binaries**

```bash
cd /home/ec2-user/ttobak/backend && for dir in cmd/api cmd/transcribe cmd/summarize cmd/process-image cmd/kb; do
  GOOS=linux GOARCH=arm64 /usr/local/go/bin/go build -tags lambda.norpc -o $dir/bootstrap ./$dir
done
```

- [ ] **Step 2: Build frontend**

```bash
cd /home/ec2-user/ttobak/frontend && npm run build
```

- [ ] **Step 3: CDK deploy all**

```bash
cd /home/ec2-user/ttobak/infra && npx cdk deploy --all --require-approval never
```

- [ ] **Step 4: Deploy frontend**

```bash
aws s3 sync /home/ec2-user/ttobak/frontend/out/ s3://ttobak-site-180294183052-ap-northeast-2/ --delete
aws cloudfront create-invalidation --distribution-id E3IFMH57E9UTB5 --paths "/*"
```

- [ ] **Step 5: Test Research API**

```bash
# Create research (requires auth token)
curl -X POST https://ttobak.atomai.click/api/research \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"topic":"EKS Best Practices 2026","mode":"quick"}'

# List research
curl https://ttobak.atomai.click/api/research \
  -H "Authorization: Bearer $TOKEN"
```

- [ ] **Step 6: Test Insights Research tab**

Open https://ttobak.atomai.click/insights, switch to Research tab, click "New Research", enter a topic, verify job appears with "Running" status.

- [ ] **Step 7: Commit**

```bash
cd /home/ec2-user/ttobak && git add -A
git commit -m "feat: deploy deep research agent — API, tools, frontend

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```
