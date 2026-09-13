# ADR-004: Shared Crawled Knowledge and Insights

- Status: Accepted; pipeline repairs and curation extend this decision in
  [ADR-021](ADR-021-crawler-pipeline-repair-and-service-autodiscovery.md) and
  [ADR-026](ADR-026-insights-relevance-gate-and-curation.md).
- Decision date: Not recorded; original design specification dated 2026-04-17.
- Implementation checked: 2026-09-13.

## Context and decision

Meeting summaries and manual uploads alone lacked AWS technical guidance and
customer news for account preparation. Use a dedicated scheduled crawler pipeline,
shared source registration with subscriptions, and a shared KB prefix to avoid
crawling the same material separately for each user.

Step Functions orchestrates four focused Lambdas: source orchestration, technical
documentation, customer news, and KB ingestion. Crawled material is stored under
`shared/aws-docs/` and `shared/news/`; authenticated Insights browsing and Q&A can
consume this intentionally shared content.

## Current implementation

- The orchestrator aggregates AWS services for one technical crawl, parallel to
  a per-source news Map. It does not run a separate technical crawl for every
  subscriber. The daily schedule is 19:00 UTC.
- Technical/news Lambda timeouts are 14 minutes; the state-machine timeout is
  30 minutes. The original five-minute limit is obsolete.
- News discovery uses the AgentCore Web Search Gateway plus direct custom-URL
  fetching. Legacy RSS helpers/comments are not the current handler path. AWS
  service autodiscovery and result handling follow ADR-021.
- The configured crawler summary model is
  `global.anthropic.claude-sonnet-5`, superseding the Sonnet 4.6 addendum.
- Ingestion validates KB configuration, skips zero-change runs, and raises
  configuration or ingestion-start failures so Step Functions can fail visibly.
  It does not treat an error-status payload as successful ingestion.
- Insights supports filtered lists and detail reading; deep research is a
  separate pipeline. Markdown uses the sanitized renderer in ADR-010.

## Alternatives, consequences, and risks

Scheduled containers offered longer processing windows but added container
operations. Extending the request-serving API coupled crawling to interactive
requests. Separate Lambdas and Step Functions provided clearer failure boundaries
and reusable ingestion.

Tradeoffs remain orchestration maintenance, external search/site changes, parsing
failures, model cost, and delay between publication and scheduled ingestion.
Shared crawler knowledge is visible across users by design; it is not an
account-private store. Relevance filters reduce noise but do not guarantee source
accuracy. Original dollar estimates are historical, not a current budget.

## Evidence

- [crawler-stack.ts](../../infra/lib/crawler-stack.ts): scheduling, fan-out,
  timeouts, and model configuration.
- [crawler](../../backend/python/crawler): orchestrator, crawlers,
  `ingest_trigger.py`, and `test_crawlers.py`.
- [insights.go](../../backend/internal/service/insights.go): browsing and filters.
- [qa/handler.py](../../backend/python/qa/handler.py): retrieval visibility.
