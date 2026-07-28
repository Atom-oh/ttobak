# Deep Research Agent for KB — Design Spec

## Overview

Add a Deep Research Agent powered by Amazon Bedrock AgentCore Runtime that performs multi-source web research on user-specified topics, generates citation-backed reports, and stores them in the shared Knowledge Base. Users trigger research from the Insights page (new "Research" tab), monitor progress, and read completed reports inline with full markdown rendering.

Inspired by [claude-deep-research-skill](https://github.com/199-biotechnologies/claude-deep-research-skill) but deployed as a managed AgentCore agent instead of a local Claude Code skill.

## Requirements

- "New Research" button on Insights page triggers a deep research job
- User specifies topic and research mode (Quick/Standard/Deep)
- AgentCore Runtime agent executes the research pipeline asynchronously (5-45 min)
- Agent performs: web search → evidence collection → triangulation → synthesis → critique → report generation
- Final report (markdown) stored in S3 KB bucket and ingested into Bedrock KB
- Research metadata stored in DynamoDB for listing/status tracking
- Research tab on Insights page shows all research jobs (running/done/error)
- Completed reports viewable inline with full markdown rendering (same as news articles)
- QA Lambda can reference research reports via KB RAG (shared/ prefix already included)
- No new frontend pages — extends existing Insights page with a third tab

## Architecture

### Request Flow

```
User → Insights Research tab → "New Research" → topic + mode
    ↓
POST /api/research
    ↓
Go API Lambda:
  1. Create RESEARCH#{id} in DynamoDB (status: running)
  2. InvokeAgent (AgentCore Runtime, async)
  3. Return 202 {researchId, status: running}
    ↓
AgentCore Deep Research Agent (async, 5-45 min):
  Phase 1: SCOPE — analyze topic, generate search queries
  Phase 2: PLAN — design report structure (sections, angles)
  Phase 3: RETRIEVE — parallel web searches (5-10 queries)
  Phase 4: TRIANGULATE — cross-verify claims across sources
  Phase 5: SYNTHESIZE — draft report with citations
  Phase 6: CRITIQUE — self-review, loop back to Phase 3 if gaps found
  Phase 7: REFINE — final polish, ensure citation coverage
  Phase 8: PACKAGE — generate markdown report
    ↓
Agent writes results:
  → S3: shared/research/{researchId}.md
  → DynamoDB: update RESEARCH#{id} status=done, metadata
  → Bedrock KB ingestion trigger (optional)
```

### Component Diagram

```
┌─────────────────────────────────────────────────────┐
│                  Frontend (Insights)                  │
│  [News] [Tech] [Research]                            │
│                                                       │
│  Research tab:                                        │
│  ┌─────────────────────────────────────────┐         │
│  │ [+ New Research]                         │         │
│  │                                          │         │
│  │ ┌──────────────────────────────────────┐│         │
│  │ │ EKS Best Practices 2026      [Done]  ││         │
│  │ │ 12 sources · 3,200 words · Apr 22    ││         │
│  │ ├──────────────────────────────────────┤│         │
│  │ │ 하나은행 클라우드 전략       [Running]  ││         │
│  │ │ Started 3 min ago...                 ││         │
│  │ └──────────────────────────────────────┘│         │
│  └─────────────────────────────────────────┘         │
└──────────────────────┬──────────────────────────────┘
                       │
                 POST /api/research
                 GET  /api/research
                 GET  /api/research/{id}
                       │
┌──────────────────────▼──────────────────────────────┐
│                   Go API Lambda                       │
│  ResearchHandler → ResearchService → DynamoDB         │
│                         ↓                             │
│              InvokeAgent (AgentCore)                   │
└──────────────────────┬──────────────────────────────┘
                       │
┌──────────────────────▼──────────────────────────────┐
│              AgentCore Runtime                         │
│                                                       │
│  Deep Research Agent                                  │
│  ├─ Model: Claude Sonnet 4.6                         │
│  ├─ Tools:                                            │
│  │   ├─ web_search — multi-query web search           │
│  │   ├─ save_report — S3 PutObject + DynamoDB update  │
│  │   └─ search_kb — existing KB for prior research    │
│  ├─ Memory: session (persists across phases)          │
│  └─ System prompt: research methodology pipeline      │
└──────────────────────┬──────────────────────────────┘
                       │
              ┌────────▼────────┐
              │   S3 KB Bucket   │
              │ shared/research/ │
              └────────┬────────┘
                       │
              ┌────────▼────────┐
              │   Bedrock KB     │
              │  (RAG ingestion) │
              └─────────────────┘
```

## Data Model (DynamoDB — ttobak-main)

### Research Job

```
PK: RESEARCH#{researchId}
SK: CONFIG
Attributes:
  researchId: string         # UUID
  userId: string             # requesting user
  topic: string              # "EKS Best Practices 2026"
  mode: string               # "quick" | "standard" | "deep"
  status: string             # "running" | "done" | "error"
  createdAt: string          # ISO 8601
  completedAt: string        # ISO 8601 (when done)
  s3Key: string              # "shared/research/{researchId}.md"
  sourceCount: number        # number of sources cited
  wordCount: number          # report word count
  summary: string            # executive summary (200-400 words)
  errorMessage: string       # error details (if status=error)
```

### User Research Index

```
PK: USER#{userId}
SK: RESEARCH#{researchId}
Attributes:
  topic: string
  status: string
  createdAt: string
```

## API Endpoints (Go — ttobak-api)

### POST /api/research

Create a new research job and trigger AgentCore agent.

Request:
```json
{
  "topic": "EKS Best Practices for Financial Services 2026",
  "mode": "standard"
}
```

Response (202):
```json
{
  "researchId": "uuid-...",
  "topic": "EKS Best Practices for Financial Services 2026",
  "mode": "standard",
  "status": "running",
  "createdAt": "2026-04-22T..."
}
```

### GET /api/research

List user's research jobs (newest first).

Response:
```json
{
  "research": [
    {
      "researchId": "uuid-...",
      "topic": "...",
      "mode": "standard",
      "status": "done",
      "createdAt": "...",
      "completedAt": "...",
      "sourceCount": 12,
      "wordCount": 3200,
      "summary": "..."
    }
  ]
}
```

### GET /api/research/{researchId}

Get research detail including full S3 content.

Response:
```json
{
  "researchId": "...",
  "topic": "...",
  "status": "done",
  "content": "# EKS Best Practices...\n\n## Executive Summary\n...",
  "sourceCount": 12,
  "wordCount": 3200,
  "summary": "...",
  "createdAt": "...",
  "completedAt": "..."
}
```

### DELETE /api/research/{researchId}

Delete research job and S3 content.

## AgentCore Agent Configuration

### Agent Definition

```python
agent_config = {
    "agentName": "ttobak-deep-research",
    "foundationModel": "anthropic.claude-sonnet-4-6-v1:0",
    "instruction": RESEARCH_SYSTEM_PROMPT,  # multi-phase pipeline instructions
    "idleSessionTTLInSeconds": 3600,
    "agentResourceRoleArn": "arn:aws:iam::...:role/ttobak-research-agent-role",
}
```

### System Prompt (Research Methodology)

The agent's system prompt encodes the 8-phase research pipeline:

1. **SCOPE**: Analyze the topic. Identify 3-5 research angles. Generate 5-10 search queries.
2. **PLAN**: Design report structure with 4-6 sections. Define what evidence each section needs.
3. **RETRIEVE**: Execute web searches in parallel. Collect 10+ sources. Extract key findings with citations.
4. **TRIANGULATE**: Cross-verify claims. Flag contradictions. Score source credibility.
5. **SYNTHESIZE**: Draft each section (600-2000 words). Ensure every major claim has 3+ sources.
6. **CRITIQUE**: Self-review. Check for gaps, unsupported claims, bias. If critical gaps found, loop back to RETRIEVE with delta queries.
7. **REFINE**: Polish prose. Ensure executive summary captures key insights. Verify all citations.
8. **PACKAGE**: Generate final markdown. Call save_report tool to write to S3 and update DynamoDB.

### Agent Tools

**1. web_search**
- Description: Search the web for information on a topic
- Implementation: Bedrock agent built-in web search, or Lambda-backed tool calling search APIs
- Input: `{ query: string, maxResults: number }`
- Output: `{ results: [{ title, url, snippet }] }`

**2. fetch_page**
- Description: Fetch and extract text content from a URL
- Implementation: Lambda function with HTML parsing (same pattern as news_crawler)
- Input: `{ url: string }`
- Output: `{ content: string, title: string }`

**3. save_report**
- Description: Save the final research report to S3 and update status in DynamoDB
- Implementation: Lambda function
- Input: `{ researchId, content, summary, sourceCount, wordCount }`
- Output: `{ status: "saved", s3Key: string }`

**4. search_kb**
- Description: Search existing KB for prior research on related topics
- Implementation: Bedrock KB retrieve (same as QA Lambda)
- Input: `{ query: string }`
- Output: `{ results: [{ text, uri, score }] }`

### IAM Role

```
ttobak-research-agent-role:
  - bedrock:InvokeModel (Sonnet 4.6)
  - s3:PutObject (KB bucket, shared/research/ prefix)
  - dynamodb:UpdateItem (ttobak-main, RESEARCH# items)
  - bedrock:Retrieve (KB retrieval)
  - lambda:InvokeFunction (tool Lambdas)
```

## Frontend Changes

### Insights Page — Research Tab

Add third tab to InsightsList component:

```
[News]  [Tech]  [Research]
```

Research tab content:
- "New Research" button (top right) → modal with topic input + mode selector
- Card list: topic, status badge (Running/Done/Error), source count, word count, date
- Running jobs: show elapsed time, animated spinner
- Polling: refresh status every 10 seconds for running jobs
- Click card → navigate to `/insights/research/{researchId}` detail page

### New Research Modal

```
┌─────────────────────────────────────────┐
│ New Research                             │
│                                          │
│ Topic                                    │
│ ┌──────────────────────────────────────┐│
│ │ EKS Best Practices for Financial...  ││
│ └──────────────────────────────────────┘│
│                                          │
│ Mode                                     │
│ [Quick 2-5min] [Standard 5-10min] [Deep] │
│                                          │
│           [Cancel]  [Start Research]     │
└─────────────────────────────────────────┘
```

### Research Detail Page

Same pattern as `/insights/[sourceId]/[docHash]`:
- Header card: topic, mode badge, status, source count, word count, date
- Content: ReactMarkdown with remarkGfm + rehypeRaw + rehypeSanitize
- Uses existing InsightDetailClient pattern with usePathname for static export

Route: `/insights/research/{researchId}` — CloudFront SPA router rewrite needed.

## CDK Changes

### AgentCore Resources (new or extend AiStack)

- Bedrock Agent definition (agentName, model, instruction, tools)
- Agent action groups (tool Lambda functions)
- Agent alias for invocation
- IAM role for agent execution
- Tool Lambda functions (fetch_page, save_report)

### Alternatives if AgentCore is not yet GA in ap-northeast-2

If AgentCore Runtime is not available in the Seoul region:
- **Fallback**: Use Bedrock Agents (classic) with action groups
- **Or**: Implement as a Step Functions workflow calling Bedrock Converse API iteratively (similar to the crawler pattern but with multi-round tool use)

## Research Modes

| Mode | Phases | Duration | Sources | Best For |
|------|--------|----------|---------|----------|
| Quick | 3 (Scope→Retrieve→Package) | 2-5 min | 5-8 | Quick overview |
| Standard | 6 (+ Plan, Triangulate, Synthesize) | 5-10 min | 8-12 | Most research |
| Deep | 8 (+ Critique, Refine) | 10-20 min | 12-20 | Critical decisions |

## S3 Key Structure

```
ttobak-kb-{ACCOUNT_ID}/
  shared/research/{researchId}.md    # NEW: research reports
  shared/news/{sourceId}/{hash}.md   # existing: news articles
  shared/aws-docs/{svc}/{hash}.md    # existing: tech docs
```

## QA Integration

No changes needed. The QA Lambda already searches `shared/` prefix via the KB filter update (ADR-004). Research reports stored under `shared/research/` will automatically be included in RAG retrieval.

## Cost Estimates

| Resource | Per Research Job | Monthly (10 jobs) |
|----------|-----------------|-------------------|
| Sonnet 4.6 (agent reasoning) | ~$0.50-2.00 | ~$5-20 |
| Web search API calls | ~$0.01-0.05 | ~$0.10-0.50 |
| AgentCore Runtime | ~$0.10-0.50 | ~$1-5 |
| S3 storage | negligible | < $0.01 |
| KB ingestion | ~$0.10 | ~$1 |
| **Total** | **~$0.70-2.50** | **~$7-25** |

## File Changes

### New Files
- `backend/internal/handler/research.go` — HTTP handlers
- `backend/internal/service/research.go` — business logic + AgentCore invocation
- `backend/internal/model/research.go` — DynamoDB types (or add to existing model files)
- `infra/lib/research-agent-stack.ts` — AgentCore CDK resources (or extend existing stack)
- Tool Lambda(s) for AgentCore action groups

### Modified Files
- `backend/cmd/api/main.go` — register research routes
- `frontend/src/components/InsightsList.tsx` — add Research tab
- `frontend/src/lib/api.ts` — add researchApi
- `frontend/src/types/meeting.ts` — add Research types
- `infra/lib/frontend-stack.ts` — CloudFront SPA router for research detail route
- `infra/bin/infra.ts` — wire new stack (if separate)

### Backend Changes
- Go API: 4 new endpoints (POST/GET/GET/{id}/DELETE)
- AgentCore: agent definition + 3-4 tool Lambdas

### No Changes
- Existing QA Lambda (already searches shared/ prefix)
- Existing crawler infrastructure
- Existing Insights detail page (reused for research reports)

## Out of Scope

- QA-triggered research ("deep research 해줘" in chat) — future enhancement
- Auto-research from crawler keywords — future enhancement
- PDF/HTML export of research reports — future enhancement
- Research sharing between users — all research is visible to all users (shared KB)
- UltraDeep mode (20-45 min, 8+ phases) — start with Quick/Standard/Deep
