# Knowledge-grade news release

Status on 2026-10-21: private handlers and publication-aware readers are implemented.
This record does not claim infrastructure deployment, positive consumer acceptance,
or activation. [ADR-052](../decisions/ADR-052-conditional-news-publication.md) owns
publication and accounting semantics.

## Runtime contract

| Handler | Input | Behavior |
|---|---|---|
| `news_runtime.discover_handler` | Exact `sourceId`, `runId` object | Strongly reads CONFIG, starts the run, discovers bounded candidates, rechecks source revision |
| `news_runtime.worker_handler` | One discovered candidate | Reserves maximum model cost, fetches/validates sources, conditionally publishes immutable results |
| `news_runtime.finalize_handler` | Exact `discovery`, `outcomes` object | Atomically writes final accounting/history; callable after disabling writers |
| `news_recovery.handler` | Empty object | Recovers expired runs using a leased scan cursor; callable after disabling writers |

These functions require private IAM invocation. They must not be exposed through
an unauthenticated function URL or a new public application route.
Input JSON is capped at 224,000 UTF-8 bytes; output is capped at 200,000 bytes.
Discovery separately limits candidates to 192,000 bytes, reserving room for
bounded outcomes and the Step Functions envelope. Workflow wiring must verify
the combined state stays below the service limit; a Lambda output cap alone
does not prove it.

Writers require `NEWS_PIPELINE_MODE=canary|all`; the default is `off`.
Canary mode additionally requires the exact `NEWS_CANARY_SOURCE_ID`.
Do not configure `all` before the acceptance record below is complete.
`TABLE_NAME`, `KB_BUCKET_NAME`, `NEWS_ARTIFACT_BUCKET` and the US East
`WEB_SEARCH_GATEWAY_URL` are deployment configuration. SDK operations use
no application retries, finite connect/read timeouts and a monotonic invocation
deadline. Discovery search has its own 20-second total transport deadline.

The recovery checkpoint is `CRAWLER#__news_runtime__/RECOVERY`. It holds a
five-minute lease and a native DynamoDB scan cursor, never meeting text.
Each tick examines at most ten pages of 100 evaluated items. The cursor advances
after every row on a page is handled; interrupted pages are replayed.
Do not expire the checkpoint or restart every tick at the first page.
Schedule recovery frequently enough to sweep the table, and keep it enabled
through rollback. Live admissions and live worker leases are not reclaimed.

## Model price evidence

Verified with the AWS Price List `GetProducts` API on 2026-10-21:

| Attribute | Value |
|---|---|
| Service code | `AmazonBedrockFoundationModels` |
| Model / endpoint region | `global.anthropic.claude-opus-5` / `ap-northeast-2` |
| Tier | Standard Global on-demand, no prompt caching |
| Input SKU / USD per million tokens | `VJDX59EY55RJM47T` / `5` |
| Output SKU / USD per million tokens | `XG2YFPFFRJQFPCPT` / `25` |
| Offer effective date | `2026-09-01T00:00:00Z` |
| Retrieved catalog SHA-256 | `76d1f1b3295c9157851e6d9130daa2617bee02c5b6e3a9d19a89b2a557a1f591` |

Source: [AWS Bedrock pricing](https://aws.amazon.com/bedrock/pricing/).
The public `AmazonBedrock` service code does not contain these Opus 5 products;
use the Foundation Models code above.

Configure `NEWS_INPUT_USD_PER_MTOK=5`, `NEWS_OUTPUT_USD_PER_MTOK=25`,
`NEWS_PRICE_VERIFIED_ON=2026-10-21`, `NEWS_RUN_LIMIT_USD=2` and
`NEWS_MONTH_LIMIT_USD=25` only while those prices and limits remain intended.
The configured date must be no more than 90 days old and not in the future.
Refresh prices before that window expires; changing the date without checking
the model/tier/region is not verification. Two maximum calls reserve
`(200000 * 5 + 12000 * 25) / 1000000 = 1.30 USD` per article.
Actual complete usage settles that reservation; ambiguous usage retains it.
These are model-token budgets, not total AWS/search costs.

## Acceptance and rollback order

1. Verify no incompatible activated v1 ledger exists; preserve current legacy data
   and its recovery manifest.
2. Deploy current-publication readers before enabling any v1 writer. Check exact
   deployed code, configuration and source revision behavior.
3. Deploy the private workflow and recovery schedule with writers off. Verify
   Mantle/Converse, S3 and DynamoDB IAM from the actual function roles.
4. Enable one synthetic public-source canary. Record a published article, immutable
   objects, charged usage, run history and successful retry/recovery behavior.
5. Verify positive current-source QA over both HTTP and WebSocket, the actual
   rendered UI and revocation/source-change rejection. A connected socket, empty
   search result or successful deployment is not consumer acceptance.
6. Complete resumable legacy migration and dated acceptance before normal
   scheduling or `all` mode. Preserve the KB reconciliation schedule.

Rollback disables new writers first and leaves readers, finalization and recovery
available. Do not delete immutable objects or ledger rows to free a budget.
Deploy changed stacks individually with `--exclusively`; never deploy
KnowledgeStack as a dependency or run an S3 sync with `--delete`.
