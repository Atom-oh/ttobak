# Knowledge-grade news release

Status on 2026-10-21: the private workflow is deployed for one isolated canary.
Publication, charged usage, authenticated HTTP/WebSocket current-source QA,
source-change/disable rejection, and rendered settings/history passed production
acceptance. Normal source activation, recovery completion and the retained-legacy
archive remain pending. The deployment default remains off.
[ADR-052](../decisions/ADR-052-conditional-news-publication.md) owns publication
and accounting semantics.

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
does not prove it. The CDK workflow replaces caller input with discovery output,
selects only four bounded identity/status fields from each of at most twenty
worker results, and removes candidate data before finalization. An infrastructure
test covers a full 192,000-byte discovery page plus maximum Unicode identities.
Python 3.12 Lambda uses literal UTF-8 JSON, as confirmed by the
[AWS runtime marshaller](https://github.com/aws/aws-lambda-python-runtime-interface-client/blob/main/awslambdaric/lambda_runtime_marshaller.py).

Writers require `NEWS_PIPELINE_MODE=canary|all`; the default is `off`.
Canary mode additionally requires the exact `NEWS_CANARY_SOURCE_ID`.
Do not configure `all` before the acceptance record below is complete.
`TABLE_NAME`, `KB_BUCKET_NAME`, `NEWS_ARTIFACT_BUCKET` and the US East
`WEB_SEARCH_GATEWAY_URL` are deployment configuration. SDK operations use
no application retries, finite connect/read timeouts and a monotonic invocation
deadline. Discovery search has its own 20-second total transport deadline.
SSE search returns when the matching response frame arrives, without waiting
for the server to close the stream. Each candidate also has a 64,000-byte limit.

The recovery checkpoint is `CRAWLER#__news_runtime__/RECOVERY`. It holds a
five-minute lease and a native DynamoDB scan cursor, never meeting text.
Its monotonic work budget is at most four minutes, including nested finalizer
SDK calls; cleanup has a separate margin inside the lease. The schedule target
must supply the constant empty object `{}`, not the default scheduled-event body.
Each tick examines at most ten pages of 100 evaluated items. The cursor advances
after every row on a page is handled; interrupted pages are replayed.
Mid-page `RUN_BUDGET` is an expected failed invocation: the next tick retries
that page without repeating settled charges. A malformed row requires repair.
Do not expire the checkpoint or restart every tick at the first page.
Schedule recovery frequently enough to sweep the table, and keep it enabled
through rollback. Live admissions and live worker leases are not reclaimed.
For a corrupt cursor or poison row, disable writers, pause the recovery schedule,
wait for its lease to expire, and preserve the checkpoint and offending row in
the private recovery manifest. Repair only the verified malformed fields; if
the cursor cannot be recovered, conditionally remove only `scanCursor` from the
observed unleased checkpoint, then resume recovery from the first page. Preserve
all run, attempt, receipt and accounting records; finalized receipts make replay
safe. Record the repair and restore the schedule before resuming writers.

## Private infrastructure

`infra/lib/news-publication.ts` adds `ttobak-news-publication` to CrawlerStack.
It does not replace the legacy daily workflow. `NewsPipelineMode` permits only
`off` and `canary`; CloudFormation rejects canary mode without a source ID.
All-source activation needs a later accepted configuration change.

Discovery, worker, finalization and recovery each have a separate Lambda role
and retained 30-day log group. DynamoDB item/transaction operations are confined
to `CRAWLER#*` keys on the existing table. Only recovery receives table Scan.
Only discovery can invoke the existing search gateway. Only the worker can
write `news/v1/*` and `knowledge-artifacts/news/v1/*` objects, invoke the verified
Opus 5 profile/model resources and count tokens on Mantle's default project.
The Mantle grant includes the exact model condition documented in the
[service authorization reference](https://docs.aws.amazon.com/service-authorization/latest/reference/list_bedrock-mantle.html).
There is no new application endpoint, function URL, bucket or public policy.

Discovery times out after ten minutes; each worker after fourteen minutes.
The Map runs one worker at a time so a settled reservation can free allowance
before the next candidate. The workflow has a 45-minute overall timeout; the
runtime's original 30-minute admission window still rejects late reservations.
Tasks have no implicit invocation retry. A caught worker invocation failure
becomes a bounded failed outcome; execution termination or an unfinished lease
leaves the run for scheduled recovery. Finalization does not pretend a live
attempt completed.

Recovery runs every five minutes with a two-minute Lambda timeout below its
five-minute checkpoint lease, including when writers are off. Alarms cover
workflow failures/timeouts and recovery errors; they have no notification target
in this stage. Execution history is private; workflow payload logging is disabled.
Inspect both workflow outcome and the durable run receipt during acceptance.

Synthesize and inspect changed-stack diffs before deployment. Adding the gateway
ARN reference can update the existing cross-region export: deploy the changed
WebSearchGatewayStack producer first when required, then CrawlerStack, each with
`--exclusively`. Preserve KnowledgeStack. Set `NewsPipelineMode=off` explicitly
for the first deployment and verify actual function environment values afterwards.

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
6. Complete the resumable retained-legacy archive below and dated acceptance before normal
   scheduling or `all` mode. Preserve the KB reconciliation schedule.

## Retained-legacy migration

Existing news remains explicitly legacy: retain every original row and object,
including old, unverified and incomplete material. Do not call it schema-v1
publication, infer a quality rejection from an old search date, or delete it.
Fresh articles use the independently accepted native publication workflow.
This policy migrates operation to the two supported reader formats; it does not
claim that all historical articles have been regenerated.

`scripts/news_legacy_archive.py` takes a resumable, private local archive outside
disposable worktrees. It preserves every DynamoDB attribute with its type,
exact bounded S3 bytes, ETag/version identity and current source configuration.
Files are content-addressed, written with fsync before atomic publication, and
checked against their hashes during reconciliation. Keep the archive in the
persistent recovery directory during worktree/cache cleanup. It is a host-local
recovery copy, not a replicated backup or an atomic whole-table snapshot.
The original production rows and objects remain in place.

Before running it, deploy `NewsLegacyWrites=off` to CrawlerStack and verify the
reviewed legacy Lambda code hash. The switch returns before search, generation,
object writes or history/status changes; unexpected flag values also stop work.
Wait at least the function timeout plus 30 seconds after each writer revision
changed. Both native writer functions must remain off or restricted to a canary
source outside the legacy selection. The archive checks these conditions and
pins the writer revision IDs, so a changed configuration cannot silently resume.
Tech crawling, KB ingestion and recovery remain enabled.

Run the following from the checkout that contains the reviewed archive tool.
Use the actual deployed legacy code SHA and a private persistent directory:

```bash
python3 scripts/news_legacy_archive.py \
  --account-id 180294183052 --region ap-northeast-2 --table ttobak-main \
  --source-bucket ttobak-kb-180294183052 \
  --legacy-writer-code-sha256 "$REVIEWED_LEGACY_CODE_SHA256" \
  --archive "$PERSISTENT_NEWS_ARCHIVE"
# Repeat the same command with --apply to archive; rerun identically to resume.
```

Selection uses bounded, strongly consistent table pages, not an eventually
consistent GSI as authorization or completeness evidence. The command permits
at most 10,000 news identities, 1,000 scan pages, 2 MiB per body, 1 MiB per typed
metadata object, and ten minutes per invocation. Capture and reconciliation
have separate resumable checkpoints. A final scan checks the selected identities;
every captured canonical row/object and every current source configuration must
still match. Changed, missing, corrupt or unreadable data stops completion.
Versioned, tombstoned and unknown-schema rows are protected and counted separately.
The command makes no production write, model call, data deletion or mode change.

Completion means a `COMPLETE_RETAINED_LEGACY` manifest with a verified SHA-256,
the full selected count and `RETAINED_LEGACY` dispositions. It is not established
by a partial archive, a zero-candidate run or a successful deployment. Record
its exact path/hash, source counts and replay result before normal activation.
Keep the archive through acceptance and for 400 days afterwards; any later
removal must target that dated archive explicitly and preserve required incident
evidence. Do not include private archived rows in a PR or CI artifact.

Validation requires Python 3.12, boto3 and moto:
`(cd scripts && python3 -m unittest test_news_legacy_archive -v)`.

## Recorded canary acceptance, 2026-10-21

The accepted canary published one official AWS DynamoDB article with two
hash-verified immutable objects, complete usage and a charged total of
USD 0.190025. A finalized discovery replay admitted no new candidate.
HTTP and WebSocket QA both read the current publication and returned its source.
Changing the source configuration and disabling it each produced an API 404
and no QA source; the WebSocket check reused the earlier conversation.
Restoring configuration restored the exact original publication bytes.
Desktop and 390-pixel mobile settings/history rendered without page errors or
horizontal overflow, including the published count and exact charge.

Minimum accepted API reader is Lambda version 223, package SHA-256
`xjeZrUN0Xp9m004ge9gBrj6kvhMHV7dmKcgtTJjp1Fk=` (AWS base64 form).
QA package SHA-256 is `wqMtWGmvWbh1V4LMuL1fBKVeG/SU0JIOnFye232POcA=`.
The QA role required a narrowly scoped `news/v1/*` read grant; the earlier
HTTP/WebSocket HeadObject 403 attempts are failed tests, not acceptance.
Private detailed receipts remain in the dated release recovery archive.
An abandoned zero-attempt discovery run is waiting for its real admission
deadline and scheduled recovery; recovery acceptance is not yet claimed.

## Rollback and retention

Rollback disables new writers first and leaves readers, finalization and recovery
available. Do not delete immutable objects or ledger rows to free a budget.
Deploy changed stacks individually with `--exclusively`; never deploy
KnowledgeStack as a dependency or run an S3 sync with `--delete`.
