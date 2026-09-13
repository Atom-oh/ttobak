# Historical implementation record: AgentCore news-search migration

- Original plan date: 2026-07-12.
- Historical proposal, not an execution checklist or current review mandate. Source pointers checked 2026-09-13.

## Scope and rationale

Replace Google News/site RSS search in the news crawler and research agent with the AgentCore Gateway Web Search connector, retaining summarization, deduplication, and storage. Discovery supplied snippets rather than scraped full articles; explicit `customUrls` retained a separate fetched-content path. No RSS fallback was planned.

The design placed an IAM-authenticated Gateway in `us-east-1`, called cross-region with SigV4 service name `bedrock-agentcore` and MCP `tools/call`. At that time the Gateway used an L2 construct and the connector target an L1 construct because the installed L2 lacked the required factory. The plan recorded inspecting CDK 2.261.0 and distinguishing the manually created research-runtime role from the Lambda worker role; those were dated findings, not today's deployment evidence.

Separate crawler/research artifacts kept independent transport code. Gateway URL/region configuration and ARN-scoped invocation permissions were to reach both consumers; source URL and publication date remained in stored output.

## Risks, validation, and result record

The draft collapsed Gateway errors and zero hits into empty results and overclaimed that snippets were always usable. Current failure signaling and content/relevance gates supersede those sketches. Cross-region permissions must apply to the runtime that actually calls the Gateway; granting the worker alone is insufficient.

Intended checks covered connector types, cross-region synthesis, mocked transport/error/result tests, custom-URL regressions, runtime IAM inspection, one crawl, and stored source metadata. Every task box was unchecked; proposed deployment commands and expected results did not record completion.

## Current references

- [Gateway definitions](../../../infra/lib/web-search-gateway-stack.ts), [IAM](../../../infra/lib/ai-stack.ts), [news crawler](../../../backend/python/crawler/news_crawler.py), [research tools](../../../backend/python/research-agent/tools.py).
- [ADR-021](../../decisions/ADR-021-crawler-pipeline-repair-and-service-autodiscovery.md) adds tech-service discovery and ingestion repairs; [ADR-026](../../decisions/ADR-026-insights-relevance-gate-and-curation.md) adds relevance filtering; [ADR-028](../../decisions/ADR-028-qa-web-search-and-proactive-question-search.md) adds the third transport copy in QA and documents egress/logging/quota constraints. Current HTTPS and redirect restrictions are not optional merely because the early templates omitted them.
