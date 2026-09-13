# Historical implementation record: Crawler and Insights foundation

- Original plan date: 2026-04-17.
- Historical proposal, not an execution checklist or current review mandate. Source pointers checked 2026-09-13.

## Scope and rationale

Introduce a shared AWS-technical/customer-news corpus, an Insights browser with News/Tech tabs, and crawler-source settings. The proposed Step Functions pipeline used four Python Lambdas: orchestrator, tech crawler, news crawler, and ingestion trigger, scheduled daily through EventBridge.

DynamoDB source configuration, user subscriptions, document metadata, and crawl-history rows supported source deduplication and unioned subscription settings. Go repository/service/handler layers exposed source management and insight queries; the frontend added source/service filters, pagination, status/history, and navigation. QA retrieval was to include shared material alongside user-owned meetings.

## Risks, validation, and result record

The early news implementation used Google News/site RSS, and its copied IAM, query, and deployment templates were proposals rather than safe defaults. Expanding retrieval must preserve per-user authorization for private meetings; shared crawler material does not make all meeting prefixes public. Unsubscribing was distinct from deleting collected content.

Intended validation covered Go builds, frontend build, CDK synthesis, authenticated API checks, a workflow run, and inspection of stored documents/history. All task boxes were unchecked; no completed deployment or pipeline run was recorded. The old blanket stack-deploy command is not retained: current policy excludes the staged KnowledgeStack teardown and uses individual `--exclusively` deployments.

## Current references

- [Crawler workflow](../../../infra/lib/crawler-stack.ts), [crawler service](../../../backend/internal/service/crawler.go), [source repository](../../../backend/internal/repository/crawler.go), [QA retrieval](../../../backend/python/qa/handler.py).
- [ADR-004](../../decisions/ADR-004-crawler-insights-for-sa-knowledge-base.md) records the foundation; [ADR-021](../../decisions/ADR-021-crawler-pipeline-repair-and-service-autodiscovery.md) corrects ingestion payload/configuration handling; [ADR-026](../../decisions/ADR-026-insights-relevance-gate-and-curation.md) adds relevance and curation. The July Gateway-search plan supersedes the RSS news-search proposal.
