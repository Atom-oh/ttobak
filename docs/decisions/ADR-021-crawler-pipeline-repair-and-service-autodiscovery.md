# ADR-021: Crawler ingestion repair and service discovery

- Status: Accepted; extended by [ADR-026](ADR-026-insights-relevance-gate-and-curation.md).
- Original decision date: Not recorded. Original incident: ingestion stopped after 2026-05-28 for roughly seven weeks.
- Code checked: 2026-09-13; live ingestion and historical backfill were not rechecked.

## Original decision and rationale

Repair a pipeline that wrote crawled S3 objects while reporting successful workflows without updating the Knowledge Base. Placeholder IDs, wrapped branch outputs, and exceptions returned as normal `ERROR` results jointly hid the failure. Reuse the existing AgentCore search connector for new-service discovery instead of requiring every service to be registered manually.

## Current behavior

- `knowledge-stack.ts` reads KB/DataSource IDs from CDK context, falling back to `BJJLVLFTOR` / `3AVMMT3RF3`, not `PENDING`. The original hardcoding decision is now context-overridable; these values are configuration, not evidence that those resources currently exist.
- `CrawlTechDocs` emits its Lambda payload; the news Map emits a list. `ParallelCrawl` stores `[techResult, [newsResult, ...]]` in `crawlResults`, which `TriggerIngestion` passes directly to the Lambda. Direct/test calls may use `crawlerResults`.
- The ingestion trigger rejects missing/`PENDING` configuration even on a zero-document run. It flattens one list level, skips when nothing was added/updated, and raises on start-job failure. A successful start is not proof that ingestion completed.
- The tech crawler reuses `news_crawler`'s SigV4 client for per-service searches and one discovery pass. Valid service slugs are merged into `CRAWLER#__auto__/CONFIG`, capped at five additions per run and 30 stored services while preserving existing entries.
- The orchestrator excludes synthetic `__` sources from customer-news fan-out. News and tech runs record crawl history/status; per-article errors remain reportable without necessarily failing the whole workflow.

## Tradeoffs and operational constraint

Discovery adds model/search calls and accepts unreviewed model-selected service slugs. Validation and caps limit volume, not relevance or truth; ADR-026 provides the news relevance gate. Synthetic configuration and metadata writes are not a guarantee of successful KB ingestion.

The KB/DataSource resources remain out-of-band with commented Phase 2 definitions. Preserve the explicit prohibition on deploying `TtobakKnowledgeStack` while its staged teardown is unresolved. Deploy changed stacks with `--exclusively` in dependency order, never `cdk deploy --all`. CDK source/synthesis alone cannot prove deployed IDs, backlog recovery, or nightly health.

## Evidence

- [Ingestion trigger](../../backend/python/crawler/ingest_trigger.py), [orchestrator](../../backend/python/crawler/orchestrator.py), [tech crawler](../../backend/python/crawler/tech_crawler.py), [news crawler](../../backend/python/crawler/news_crawler.py).
- [Crawler tests](../../backend/python/crawler/test_crawlers.py): ingestion configuration/payloads, discovery caps, relevance, and history.
- [Crawler workflow](../../infra/lib/crawler-stack.ts), [KB configuration](../../infra/lib/knowledge-stack.ts), [deployment sequence](../../.github/workflows/deploy-infra.yml).
