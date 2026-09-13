# Crawler and Insights Design

> Historical design record. Original date: 2026-04-17 (filename); no original status
> was recorded. The proposals below are not current requirements or proof of delivery.

## Goal and scope

Enrich an SA's meeting preparation and Q&A with customer news and AWS technical
material. The design combined daily ingestion, source subscription management,
shared knowledge, and a browsable Insights page. It excluded authenticated sites,
real-time alerts, per-source schedules, and a separate full-text search UI.

## Proposed design and rationale

A system-level crawler source represented one customer, while per-user subscriptions
held service/news preferences. Merging subscriber selections avoided repeated crawls.
Document URL hashes provided stable identities; unsubscribe retained documents and
disabled a source after its last subscriber left. Source normalization was intended
to prevent duplicates, not to prescribe a transliteration algorithm for names.

Four Python Lambdas covered source enumeration, technical crawling, news crawling,
and KB ingestion. Step Functions coordinated fan-out, proposed source locks, failure
isolation, and a daily 19:00 UTC schedule. Technical discovery combined official AWS
pages with change detection; news discovery proposed search APIs, RSS, and custom
URLs. The original per-source parallel graph, five-minute worker limits, Haiku
choice, and ETag approach were design details, not permanent contracts.

Metadata lived in `CRAWLER#` source/document/history rows and `CRAWL_SUB#`
subscriptions. Markdown used `shared/aws-docs/` and `shared/news/`; user uploads
and meeting exports retained their own prefixes. The QA filter would deliberately
include shared crawler content without exposing another user's private uploads.

News/Tech tabs offered cards, filtering, pagination, source links, and source
management with manual crawl/history actions. The original API inventory and
navigation sketch were proposals; use the current API reference for actual routes.

## Tradeoffs and validation intent

Serverless workers reused existing storage and isolated work but added orchestration,
external-source fragility, delayed freshness, and model/ingestion cost. Shared content
could be irrelevant to some users. The original $12-27/month estimate depended on
small daily volume and excluded any guarantee about current prices or deployment.
Validation was intended to cover deduplication, subscriptions, failed source isolation,
private/shared retrieval, and useful news/technical browsing.

## Current evidence and successors

[ADR-004](../../decisions/ADR-004-crawler-insights-for-sa-knowledge-base.md),
[ADR-021](../../decisions/ADR-021-crawler-pipeline-repair-and-service-autodiscovery.md),
and [ADR-026](../../decisions/ADR-026-insights-relevance-gate-and-curation.md) record
later decisions. Current [CrawlerStack](../../../infra/lib/crawler-stack.ts) runs
one aggregated technical branch beside a per-source news Map, with 14-minute crawl
workers. [News crawling](../../../backend/python/crawler/news_crawler.py) uses the
Web Search Gateway plus explicit custom URLs; official RSS remains in the separate
[technical crawler](../../../backend/python/crawler/tech_crawler.py). These are
source observations, not evidence of a successful production run.

See the [documentation map](../../README.md), [API](../../API-SPEC.md), and
[infrastructure reference](../../INFRA-SPEC.md) for current contracts.
