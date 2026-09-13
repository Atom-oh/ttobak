# Historical proposal record: Knowledge-grade news pipeline

- Original plan date: 2026-08-09; preservation note dated 2026-09-11.
- Historical proposal, not an execution checklist or current review mandate. Source comparison: 2026-09-13.
- Original design reference: `8e86b39`, [knowledge-enrichment design](../specs/2026-08-07-knowledge-enrichment-pipeline-design.md). The plan's ADR-030 suggestion conflicts with the existing mobile-caption ADR.

## Proposed scope and rationale

Replace snippet summaries with fully fetched, evidence-backed syntheses. Retain AgentCore search for discovery but split news work into discovery, enrichment worker, and finalizer Lambdas, supported by pure fetch/validation/rendering modules. Step Functions would map sources and candidate groups, isolate failures, and emit bounded status/reason records without article bodies or plaintext queries.

Discovery used public source configuration and aliases, at most 12 queries, seven-day freshness with a 30-day fallback only when the first pass found no candidates, normalized URL deduplication, and near-duplicate event groups. Grouping required title-token similarity of at least 0.75, a shared organization alias, and dates within seven days. Explicit custom URLs bypassed freshness/relevance only, not fetching or quality checks.

## Proposed content and publication contract

- Fetch lead plus up to two corroborating sources with public-address/redirect validation, at most five redirects and three transient-error attempts. Planned limits were ten-second network timeouts, 2 MiB compressed/4 MiB decoded responses, and 30,000 normalized characters. Paywalls, JavaScript shells, and bodies below 800 characters/four blocks were rejected.
- Use the recorded Opus 5 profile in `us-west-2`, with one repair attempt. The proposed gate required a substantial summary, at least three facts and two evidence-backed facts, context/impact, and follow-up questions/actions. Evidence had to match fetched text; numeric claims needed support. Excerpts were capped at 180 characters each and 1,000 total.
- Claim canonical document identity for 30 minutes using run ownership, then write private structured JSON under `knowledge-artifacts/news/`, canonical Markdown under `shared/news/`, and conditional published metadata last. Raw article bodies were not intended to persist. Existing published schema-v1 items skipped model work; controlled failures released claims.
- Publish schema/quality/source-count metadata, hide legacy news until migrated, delete canonical and private objects before metadata, and keep tech documents unchanged. UI work included substantive cards, source counts, canonical article reading, and theme-aware Mermaid controls with strict rendering security.

## Proposed operations and risks

The plan budgeted 20 candidates, $2 per source run, $25 monthly per source, and a pause at 120%; its $5/$25 per-million-token estimates were historical planning assumptions, not current pricing. Proposed EMF metrics, alarms, schedule DLQ, retry policies, and new-month recovery were not established operational guarantees. Semantic/model quality, alias ambiguity, SSRF, incomplete writes, migration deletion, cost-estimate error, and claim concurrency needed explicit validation.

A resumable dry-run migration would refetch old news, preserve it on infrastructure errors, delete deterministic rejects only with explicit apply, retain a manifest, and reconcile the KB. The planned rollout kept schedules paused through canary/migration and avoided the staged KnowledgeStack teardown. The stated 24-hour same-region RPO/RTO was a design target, not measured recovery evidence.

## Current disposition and validation record

The preservation note explicitly said committing the plan did not complete or deploy it; all task boxes were unchecked. Intended suites covered pure policies, mocked workers, publication order, budgets, migration, Go authorization/API fields, frontend layout, CDK wiring, and a later source canary. No executed results were recorded.

The proposed `news_pipeline`, `article_fetcher`, `news_enrichment`, discovery/worker/finalizer modules, schema-v1 visibility filter, and migration script are absent from reviewed source. Current [news crawler](../../../backend/python/crawler/news_crawler.py), [Insights service](../../../backend/internal/service/insights.go), and [four-Lambda workflow](../../../infra/lib/crawler-stack.ts) still implement the earlier architecture. [ADR-021](../../decisions/ADR-021-crawler-pipeline-repair-and-service-autodiscovery.md) and [ADR-026](../../decisions/ADR-026-insights-relevance-gate-and-curation.md) describe that behavior; this proposal must not make reviewers demand unimplemented worker files or assume stronger content guarantees.
