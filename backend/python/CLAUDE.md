# Python Lambdas Module

Python Lambda functions for specialized AI workloads.

## Structure
- `qa/` — Bedrock RAG Q&A Lambda (Converse API + KB Retrieve, WebSocket streaming)
- `crawler/` — Step Functions pipeline: orchestrator, news-crawler, tech-crawler, ingest-trigger
- `research-agent/` — Strands Agents SDK agent for Deep Research (AgentCore Runtime)
- `research-tools/` — Tool Lambdas for Bedrock Agent (save-report, fetch-page)

## Conventions
- Runtime: Python 3.12 (Lambda managed runtime)
- Dependencies: `requirements.txt` per Lambda, stdlib + boto3 preferred
- No external HTTP libraries — use `urllib.request` for crawling
- Bedrock calls use `converse()` API (not `invoke_model()`)
- HTML parsing via stdlib `HTMLParser` (no BeautifulSoup dependency)
- Crawler filters: block paywalled URLs, require 100+ chars body text
