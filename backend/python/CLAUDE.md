# Python Lambdas Module

Python Lambda functions for specialized AI workloads.

## Structure
- `qa/` — Bedrock RAG Q&A Lambda (Converse API + KB Retrieve, WebSocket streaming)
- `crawler/` — Step Functions pipeline: orchestrator, news-crawler, tech-crawler, ingest-trigger
- `research-agent/` — AgentCore Runtime container (FastAPI + Strands Agents)
- `research-tools/` — Tool Lambdas for Bedrock Agent (save-report, fetch-page)

## Conventions
- Runtime: Python 3.12 (Lambda managed runtime)
- Dependencies: `requirements.txt` per Lambda, stdlib + boto3 preferred
- No external HTTP libraries — use `urllib.request` for crawling
- Bedrock calls use `converse()` API (not `invoke_model()`)
- HTML parsing via stdlib `HTMLParser` (no BeautifulSoup dependency)
- Crawler filters: block paywalled URLs, require 100+ chars body text

## AgentCore Research Agent (Container)
- **Pattern**: FastAPI + uvicorn (official AWS docs pattern, NOT bedrock-agentcore SDK)
- **Base image**: `ghcr.io/astral-sh/uv:python3.11-bookworm-slim` (ARM64)
- **Contract**: POST `/invocations` + GET `/ping` on port 8080
- **DO NOT use** `bedrock-agentcore>=1.6` SDK in containers — it boots a full Starlette ASGI stack that exceeds the 30s health check timeout
- **Build**: CICD pushes to main → self-hosted runner builds ARM64 natively (no cross-compile)
- **Deploy**: ECR push → `update-agent-runtime` with containerConfiguration
- **Long tasks**: Research runs in background thread; `/ping` returns `HealthyBusy` to keep session alive
