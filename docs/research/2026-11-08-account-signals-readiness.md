# Account signals readiness record

Status: historical. Recorded 2026-11-09 before any code from the account signals
and flow branch was deployed. It is dated evidence, not a current configuration
reference or an acceptance result.

## Deployed news configuration (read-only, account ap-northeast-2)

| Item | Observed value | Source |
|---|---|---|
| `NewsPipelineMode` | `all` | CloudFormation `TtobakCrawlerStack` parameter |
| `NewsCanarySourceId` | `aws-news-canary-20261021-84681d3c` | CloudFormation `TtobakCrawlerStack` parameter |
| `NEWS_LEGACY_WRITES` | `off` | Lambda environment of `ttobak-news-schedule`, `ttobak-news-worker`, `ttobak-crawler-news`, `ttobak-news-discover`, `ttobak-news-recover`, `ttobak-news-finalize` |
| KB data source inclusion prefixes | none (`null`); the whole bucket is in scope, so `news/v1/` is not filtered out | Bedrock KB `BJJLVLFTOR`, data source `3AVMMT3RF3` |

None of this branch's code is deployed; these values describe production before
the branch. Legacy writes are off and the pipeline mode is `all`, so account news
sources depend on the knowledge-grade pipeline rather than the legacy path. This
record does not show that account-source news is being produced or accepted.

## Rollout order

1. Reader and API code first, so every consumer tolerates the new rows.
2. Then the summarize Lambda with the auto-link hook.
3. Then the account KB backfill (`backend/cmd/backfill-account-kb`): a dry run
   first (`-dry-run` defaults to true), then the real backfill, which requires
   explicit user authorization.
4. Enabling account-source news production follows the
   [news release runbook](../runbooks/knowledge-grade-news-release.md); its gates
   are not bypassed.
5. Deploy changed stacks individually with `--exclusively`, in dependency order.
   This record performs no deployment.

Design and decisions: [ADR-064](../decisions/ADR-064-account-relationship-links.md).
