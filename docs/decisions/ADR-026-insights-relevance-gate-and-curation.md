# ADR-026: Insight relevance filtering and manual curation

- Status: Accepted; extends [ADR-021](ADR-021-crawler-pipeline-repair-and-service-autodiscovery.md).
- Original decision date: Not recorded.
- Code checked: 2026-09-13.

## Original decision and rationale

Search results were ingested without checking whether they concerned the configured customer. Common names, incidental mentions, and competitor coverage produced irrelevant insights. Add a relevance verdict to the existing summary call, rather than another model call or lexical matching alone, and provide a separate destructive curation operation.

## Current behavior

- `_summarize_and_tag` returns a boolean verdict and confidence alongside the briefing. Search-derived articles must be relevant and meet `RELEVANCE_THRESHOLD` (default 0.7). Malformed/model-failure results fail closed; invalid threshold configuration falls back to the default.
- Explicit `customUrls` bypass the relevance verdict, but still pass fetch/content safety checks and require a usable briefing. This is an intentional ingest request, not permission to accept failed summaries.
- `DELETE /api/insights/{sourceId}/{docHash}` requires the source owner or a backend-verified admin. Subscription is insufficient because users can self-subscribe. Missing owner metadata denies non-admin deletion; synthetic tech sources without a source record remain unsupported by this route.
- Deletion removes S3 first, then metadata, preserving a retry path if S3 deletion fails. Missing stored keys use the news/tech fallback shapes. The handler requests a best-effort KB sync; sync failure is logged, and vectors can remain until ingestion reconciles them.
- `insights-rescore.py` is dry-run by default; destructive use requires `--run --yes` and an explicit bucket. It skips unscorable documents and custom ingests, including legacy URLs matched against current source configuration. Scoring uses stored title/summary, not the original article.
- `insights-backfill-owner.py` reports missing owners without assigning them. Current subscriber order cannot establish original ownership; an admin must use external evidence before granting destructive rights.

## Tradeoffs and accepted residual risks

A combined model call avoids another request but can correlate summary and relevance mistakes. Borderline relevant articles may be excluded. Manual deletion is not permanent suppression: removing metadata also removes the URL dedup marker, so a later crawl can re-ingest the article if it passes again. Tombstones/suppression markers are not implemented.

Legacy sources remain admin-only for deletion until ownership is established; this protects existing subscribers rather than guessing a creator. KB deletion lag and unsupported tech-document curation remain explicit limitations. Unsubscribing a source does not imply content deletion.

## Evidence

- [News processing](../../backend/python/crawler/news_crawler.py), [relevance/custom-ingest tests](../../backend/python/crawler/test_crawlers.py).
- [Deletion service](../../backend/internal/service/insights.go), [handler sync](../../backend/internal/handler/insights.go), [service tests](../../backend/internal/service/insights_test.go), [handler tests](../../backend/internal/handler/insights_test.go).
- [Rescore tool](../../scripts/insights-rescore.py), [owner report](../../scripts/insights-backfill-owner.py).
