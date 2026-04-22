# AgentCore Runtime Migration Design

## Overview

Migrate the Deep Research Agent from Bedrock Agents (classic CfnAgent + Action Groups) to Amazon Bedrock AgentCore Runtime + Gateway + Tools. AgentCore is now GA in ap-northeast-2.

This simplifies the architecture by replacing the CfnAgent/ActionGroup/Lambda chain with a single Python agent deployed directly to AgentCore Runtime, where tools are Python functions inside the agent code (no separate Lambda needed).

## Current Architecture (Classic)

```
Go API Lambda
  ↓ bedrockagentruntime.InvokeAgent
CfnAgent (ttobak-deep-research)
  ├─ Action Group: research-tools → Lambda (save_report)
  └─ Action Group: web-tools → Lambda (fetch_page)
```

**Problems:**
- CfnAgent system prompt is the only control surface (no custom logic)
- Tools require separate Lambda functions with action group wiring
- Agent can't directly access S3/DynamoDB — needs Lambda intermediaries
- Limited observability into agent reasoning
- No built-in auth for direct client invocation

## Target Architecture (AgentCore)

```
Go API Lambda (or direct client via Gateway)
  ↓ HTTP POST to AgentCore Runtime endpoint
AgentCore Runtime (Python agent)
  ├─ Strands Agents SDK
  ├─ Model: Claude Sonnet 4.6
  ├─ Tools (Python functions, no Lambda):
  │   ├─ web_search() — urllib + HTML parsing
  │   ├─ fetch_page() — urllib + BeautifulSoup-like parsing
  │   ├─ save_report() — boto3 S3 + DynamoDB direct
  │   └─ search_kb() — boto3 bedrock-agent-runtime.retrieve
  └─ 8-phase research pipeline (Python control flow)
      ↓
  S3: shared/research/{id}.md
  DynamoDB: RESEARCH#{id} status=done
```

### AgentCore Gateway (optional, future)

```
Frontend → AgentCore Gateway (Cognito JWT auth)
  ↓
AgentCore Runtime
```

Gateway enables direct frontend-to-agent communication without Go API Lambda intermediary. This is a future enhancement — initially we keep the Go API as the intermediary.

## Migration Scope

### Remove (Classic)
- `infra/lib/research-agent-stack.ts` — CfnAgent, CfnAgentAlias, tool Lambdas, agent IAM role
- `backend/python/research-tools/save_report.py` — replaced by agent-internal function
- `backend/python/research-tools/fetch_page.py` — replaced by agent-internal function
- `RESEARCH_AGENT_ID` / `RESEARCH_AGENT_ALIAS_ID` env vars from API Lambda

### Add (AgentCore)
- `backend/python/research-agent/agent.py` — complete agent with Strands SDK
- `backend/python/research-agent/tools.py` — tool functions (web_search, fetch_page, save_report, search_kb)
- `backend/python/research-agent/pyproject.toml` — dependencies
- `backend/python/research-agent/agentcore/agentcore.json` — AgentCore config
- AgentCore Runtime deployment via `agentcore deploy` CLI
- Updated Go service to invoke AgentCore Runtime endpoint via HTTP

### Keep (No Change)
- DynamoDB schema (RESEARCH# records)
- Go API endpoints (POST/GET/DELETE /api/research)
- Frontend (InsightsList Research tab, detail page)
- S3 key structure (shared/research/{id}.md)
- QA Lambda KB filter (shared/ prefix)

## Agent Code (`agent.py`)

```python
from bedrock_agentcore.runtime import BedrockAgentCoreApp
from strands import Agent
from strands.models.bedrock import BedrockModel
from tools import web_search, fetch_page, save_report, search_kb

app = BedrockAgentCoreApp()

RESEARCH_SYSTEM_PROMPT = """You are a Deep Research Agent for Ttobak...
[8-phase pipeline instructions — same as current CfnAgent instruction]
"""

model = BedrockModel(
    model_id="anthropic.claude-sonnet-4-6-v1:0",
    region_name="ap-northeast-2",
)

agent = Agent(
    model=model,
    system_prompt=RESEARCH_SYSTEM_PROMPT,
    tools=[web_search, fetch_page, save_report, search_kb],
)

@app.entrypoint
def handle(payload):
    topic = payload.get("topic", "")
    mode = payload.get("mode", "standard")
    research_id = payload.get("researchId", "")
    
    prompt = f"Research topic: {topic}\nMode: {mode}\nResearch ID: {research_id}"
    result = agent(prompt)
    
    return {"result": str(result)}
```

## Tools (`tools.py`)

All tools are Python functions with `@tool` decorator from Strands SDK. They run inside the AgentCore Runtime microVM with direct boto3 access.

```python
from strands.tools import tool
import boto3
import json
import hashlib
from urllib.request import Request, urlopen
from html.parser import HTMLParser

s3 = boto3.client('s3')
dynamodb = boto3.resource('dynamodb')
table = dynamodb.Table(os.environ.get('TABLE_NAME', 'ttobak-main'))
KB_BUCKET = os.environ.get('KB_BUCKET_NAME', 'ttobak-kb-180294183052')

@tool
def web_search(query: str, max_results: int = 10) -> str:
    """Search the web using Google News RSS or AWS docs search API."""
    # Google News RSS for Korean queries
    # AWS docs search API for technical queries
    ...

@tool  
def fetch_page(url: str) -> str:
    """Fetch and extract text content from a web page."""
    # Only http/https (SSRF prevention)
    # HTMLParser text extraction
    ...

@tool
def save_report(research_id: str, content: str, summary: str, source_count: int, word_count: int) -> str:
    """Save completed research report to S3 and update DynamoDB status."""
    s3_key = f"shared/research/{research_id}.md"
    s3.put_object(Bucket=KB_BUCKET, Key=s3_key, Body=content.encode('utf-8'))
    table.update_item(
        Key={'PK': f'RESEARCH#{research_id}', 'SK': 'CONFIG'},
        UpdateExpression='SET #s = :s, completedAt = :c, s3Key = :k, sourceCount = :sc, wordCount = :wc, summary = :sm',
        ...
    )
    return json.dumps({"status": "saved", "s3Key": s3_key})

@tool
def search_kb(query: str) -> str:
    """Search existing KB for prior research on related topics."""
    bedrock_agent = boto3.client('bedrock-agent-runtime')
    resp = bedrock_agent.retrieve(knowledgeBaseId=KB_ID, retrievalQuery={'text': query})
    ...
```

## Go Service Changes

Replace `bedrockagentruntime.InvokeAgent` with HTTP POST to AgentCore Runtime:

```go
// Before (classic):
output, err := s.agentClient.InvokeAgent(ctx, &bedrockagentruntime.InvokeAgentInput{
    AgentId: aws.String(s.agentId),
    AgentAliasId: aws.String(s.agentAliasId),
    SessionId: aws.String(research.ResearchID),
    InputText: aws.String(prompt),
})

// After (AgentCore):
payload := map[string]string{
    "topic": research.Topic,
    "mode": research.Mode,
    "researchId": research.ResearchID,
}
body, _ := json.Marshal(payload)
req, _ := http.NewRequest("POST", s.agentCoreEndpoint+"/invocations", bytes.NewReader(body))
req.Header.Set("Content-Type", "application/json")
// Sign with SigV4 for AgentCore Runtime auth
resp, err := s.httpClient.Do(req)
```

The `agentCoreEndpoint` is the AgentCore Runtime endpoint URL, provided after `agentcore deploy`.

## Deployment

### AgentCore CLI Deployment

```bash
cd backend/python/research-agent
agentcore create  # or manual setup
agentcore deploy  # packages + deploys to Runtime
```

The deployment produces:
- AgentCore Runtime endpoint URL
- Agent name/ID for management

### CDK Changes

- Remove `research-agent-stack.ts` (CfnAgent resources)
- Or replace with a Custom Resource that runs `agentcore deploy`
- Add AgentCore Runtime endpoint URL as env var to API Lambda
- Keep IAM role for AgentCore Runtime execution

### Environment Variables

| Variable | Current | After Migration |
|----------|---------|-----------------|
| `RESEARCH_AGENT_ID` | Bedrock Agent ID | Remove |
| `RESEARCH_AGENT_ALIAS_ID` | Agent alias ID | Remove |
| `AGENTCORE_ENDPOINT` | N/A | AgentCore Runtime URL |

## AgentCore Gateway (Phase 2)

After the Runtime migration is stable, add AgentCore Gateway for direct client access:

1. Create Gateway with Cognito JWT authorizer
2. Frontend can invoke research agent directly via Gateway (bypass Go API for streaming)
3. Enables real-time streaming of agent reasoning to the UI

This is out of scope for the initial migration.

## Benefits

| Aspect | Classic | AgentCore |
|--------|---------|-----------|
| Execution time | Lambda 15min (action groups) | Up to 8 hours |
| Tools | Separate Lambdas + Action Groups | Python functions inline |
| Observability | CloudWatch only | Built-in agent tracing |
| Deployment | CDK CfnAgent | `agentcore deploy` CLI |
| Session | Stateless per invoke | Stateful microVM sessions |
| Code control | System prompt only | Full Python logic |
| Dependencies | 5 CDK resources | 1 agentcore deploy |

## File Changes

### New
- `backend/python/research-agent/agent.py`
- `backend/python/research-agent/tools.py`
- `backend/python/research-agent/pyproject.toml`
- `backend/python/research-agent/agentcore/agentcore.json`

### Modified
- `backend/internal/service/research.go` — HTTP POST instead of InvokeAgent
- `backend/cmd/api/main.go` — replace agent client with HTTP client + endpoint env var
- `infra/lib/research-agent-stack.ts` — remove CfnAgent, add AgentCore IAM
- `infra/bin/infra.ts` — update stack wiring

### Removed
- `backend/python/research-tools/save_report.py` (moved into agent)
- `backend/python/research-tools/fetch_page.py` (moved into agent)
- `backend/python/research-tools/requirements.txt`

### No Changes
- Frontend (InsightsList, Research detail page)
- DynamoDB schema
- S3 key structure
- QA Lambda

## Out of Scope

- AgentCore Gateway (Phase 2 — direct frontend-to-agent)
- AgentCore Memory (use DynamoDB for now)
- Multi-agent collaboration (A2A protocol)
- WebSocket bidirectional streaming to frontend
