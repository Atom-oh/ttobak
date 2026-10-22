# Knowledge-grade news release

Historical staging snapshot on 2026-10-21, before daily activation: the private
workflow is deployed for one isolated canary.
Publication, charged usage, authenticated HTTP/WebSocket current-source QA,
source-change/disable rejection, and rendered settings/history passed production
acceptance. Scheduled recovery and the 986-item retained-legacy archive, including
a complete second reconciliation, also passed. Daily scheduling and `all` mode
are implemented but their production activation remains pending. The deployment
default remains off.
[ADR-052](../decisions/ADR-052-conditional-news-publication.md) owns publication
and accounting semantics.

## Runtime contract

| Handler | Input | Behavior |
|---|---|---|
| `news_runtime.discover_handler` | Exact `sourceId`, `runId` object | Strongly reads CONFIG, starts the run, discovers bounded candidates, rechecks source revision |
| `news_runtime.worker_handler` | One discovered candidate | Reserves maximum model cost, fetches/validates sources, conditionally publishes immutable results |
| `news_runtime.finalize_handler` | Exact `discovery`, `outcomes` object | Atomically writes final accounting/history; callable after disabling writers |
| `news_recovery.handler` | Empty object | Recovers expired runs using a leased scan cursor; callable after disabling writers |
| `news_schedule.handler` | `scheduledAt`, optionally `expectedSourcesSha256` | Lists current source identities with bounded strong pagination; pins the original UTC day and optionally the exact replay set; no writes or model calls |

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

`infra/lib/news-publication.ts` adds `ttobak-news-publication` and its
`ttobak-news-daily` parent to CrawlerStack. The legacy daily workflow remains
installed with news writes disabled. `NewsPipelineMode` permits `off`, `canary`
and `all`; CloudFormation rejects canary mode without a source ID and rejects
all mode unless legacy writes are off. See the dated activation contract below.

Discovery, worker, finalization, recovery and scheduling each have a separate Lambda role
and retained 30-day log group. DynamoDB item/transaction operations are confined
to `CRAWLER#*` keys on the existing table. Recovery and source scheduling receive
exact-table Scan. Scheduling also has an IAM projection attribute allowlist;
recovery bounds its projection in the request, without an IAM attribute condition.
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
  --expected-legacy-count 986 \
  --legacy-writer-code-sha256 "$REVIEWED_LEGACY_CODE_SHA256" \
  --archive "$PERSISTENT_NEWS_ARCHIVE"
# Repeat the same command with --apply to archive; rerun identically to resume.
```

Selection uses bounded, strongly consistent table pages, not an eventually
consistent GSI as authorization or completeness evidence. It selects the canonical
`type: news` field and declared `GSI4PK: DOC#news` identities; malformed legacy
identities fail validation. Bind the independently inventoried positive count
with `--expected-legacy-count` (986 in the 2026-10-21 inventory). A zero or changed
count cannot complete; investigate drift before starting a new archive.
The command permits
at most 10,000 news identities, 1,000 scan pages, 2 MiB per body, 1 MiB per typed
metadata object, and one hour per CLI invocation. Run on POSIX in the main thread;
a process alarm bounds streaming network reads as well as the cooperative work
checks. Capture checkpoints are resumable. Final reconciliation restarts from the
first entry on every invocation and must finish in that invocation; it never trusts
a previously verified prefix. It rechecks every local archive blob, canonical
row/object and source configuration, then scans the selected identities again. Changed, missing, corrupt or unreadable data stops completion.
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

Current accepted API reader is Lambda version 226, package SHA-256
`cvS71Y1jqkbWaQ9QdpQH/PmpysSYOAy+Z2SLWlL28GM=` (AWS base64 form).
QA package SHA-256 is `wqMtWGmvWbh1V4LMuL1fBKVeG/SU0JIOnFye232POcA=`.
The QA role required a narrowly scoped `news/v1/*` read grant; the earlier
HTTP/WebSocket HeadObject 403 attempts are failed tests, not acceptance.
Private detailed receipts remain in the dated release recovery archive.
The abandoned zero-attempt discovery run retained its original admission deadline
and was finalized by the scheduled recovery at 08:10:07 UTC. Its
`PARTIAL / RECOVERED_WITHOUT_OUTCOMES` history has zero attempts, charge and
documents. No manual recovery invocation was used; the prior publication charge
was unchanged. The observed delay included cursor traversal and two interfering
main deployments, which were subsequently paused.

## Retained-legacy acceptance, 2026-10-21

The reviewed legacy-off package SHA-256 (AWS base64)
`LjTwOgeFmcZVszYfGsUUGhS+Yf56EtD85tlUfz/MURQ=` was deployed with
`NewsLegacyWrites=off`, native publication restricted to the isolated canary,
and recovery/tech/KB schedules preserved. All writer timeout-plus-30-second
drain windows had ended at 08:44:23 UTC before capture started.
The archive completed with 986 legacy rows, one protected v1 row, zero model
calls and zero production writes. A second identical invocation independently
reconciled all local blobs and current production identities again.

Manifest SHA-256:
`d40a2f8c6cb3089f3315eec4a751d853feb8da4f7e731dcc1e3f00271b9e6c7d`.
The private persistent archive is
`.worktree-archive/2026-10-21/news-legacy-retained`, outside disposable worktrees.
Retain it for at least 400 days after acceptance; original rows and S3 objects
remain in production. The successful replay is necessary archival evidence,
not evidence that normal daily scheduling has been activated.

## Daily activation contract

CloudFormation permits `NewsPipelineMode=all` only with `NewsLegacyWrites=off`;
both native writers check the same exact flag before working. The daily rule is
enabled only in `all` mode and runs at 19:10 UTC (04:10 Korea time). Recovery
remains enabled in every mode. Manual private invocation in canary mode selects
only the configured canary source.

The scheduler strongly scans canonical source configuration with 100 evaluated
items per page, a 1,000-page limit and a deadline checked before each request.
It excludes disabled/internal sources and validates source identities. A malformed
identity becomes an `INVALID_SCHEDULE_SOURCE` entry containing only the SHA256 of
its canonical partition key. The parent records that source's failure and continues
healthy sources; it never renames or publishes the invalid source. Rejected entries
count toward the same 30-source limit. Duplicate rows, incomplete scans, capacity
overflow and the 64,000-byte output limit still reject the whole dispatch without
silently truncating coverage. Each child validates its current source configuration; oversized
subscription unions fail that source while later sources still run. The parent
reports failure after processing all dispatched sources.

One daily workflow processes sources sequentially, respecting the existing
single-discovery concurrency. Thirty children at the 45-minute child timeout
fit within the 23-hour parent timeout. Source/day execution names and run IDs
are deterministic from the original UTC schedule day, including on later replay.
The child uses its execution name as the actual durable run ID; the scheduler's
`daily-YYYYMMDD` field does not override that identity. Duplicate starts cannot
create additional paid runs. The
optimized nested integration reports `StepFunctions.ExecutionAlreadyExistsException`;
the duplicate catch accepts that observed name and the older suffix-free name.
A failed child is recorded without skipping later sources, and the parent fails
after all selected sources have been attempted. Successful task execution alone
is insufficient: a finalized `COMPLETED` or `PARTIAL` receipt must have the exact
empty `errors` array and boolean `usageComplete=true`. Nonempty errors such as
`SEARCH_FAILED`, incomplete usage and unknown outputs fail that source after
durable finalization. Warning-only partial results and valid empty results remain
successful. The short `FINALIZED` result and duplicate-start catch mean reuse of
an existing execution, not proof that its historical crawl succeeded. Inspect the
original history and child/parent failure or timeout alarms on replay.

### Daily admission retention and bounded replay

The daily EventBridge target forwards its original `time` as `scheduledAt`.
Active scheduling requires exactly `YYYY-MM-DDTHH:MM:SSZ`, no future timestamp,
and an age of at most 14 days. Unknown input fields fail before scanning.
Current pricing and activation checks still use current time. Only the daily
target has six delivery retries with a two-hour event age; the recovery target
keeps its separate empty input and zero retries.

Failed admission is retained for 14 days in the standard
`ttobak-news-daily-delivery-dlq` queue. The queue and rotating customer-managed
KMS key have retain policies. The queue grants only the exact daily rule
`sqs:SendMessage`; the dedicated key grants the EventBridge service
`kms:GenerateDataKey` and `kms:Decrypt` for this account. The stack outputs both
ARNs. Alarms cover `AWS/Events` `FailedInvocations`,
`InvocationsFailedToBeSentToDlq`, and visible queue messages. Metrics are
best-effort signals, not complete delivery accounting.

Before manual recovery, bind the exact queue, rule and target ARNs, retained
message attributes and original timestamp. Inspect the original parent/child
executions, current source identities/configuration revisions and ledger counters.
Never replace the original timestamp with replay time. Recovery intended to do
no new model work must verify that every expected deterministic child name already
exists in the exact publication state machine and is terminal.

Supply `expectedSourcesSha256` as 64 lowercase hexadecimal characters alongside
`scheduledAt`. The scheduler hashes its actual scanned result before returning
any entries. `sources_sha256` sorts every source/error entry by its JSON encoding,
then hashes UTF-8 JSON of `{mode,sources}` with sorted object keys,
`ensure_ascii=False`, compact comma/colon separators and no non-finite numbers.
All identities, execution names, day fields and rejection entries participate.
Addition, removal or identity change fails with `SCHEDULE_SOURCES_CHANGED` before
any child starts. This guards the scan after operator preflight; a preflight
snapshot alone does not prevent a newly added source from starting work.

Record one uniquely named parent execution intent and its exact input. On an
ambiguous response, reconcile that execution name before deciding anything else;
do not automatically retry. Verify all original child names and accounting
counters after completion. Delete only the bound DLQ message after its effect is
confirmed; retain it on uncertainty. Do not bulk-drain, recreate source identities,
clear ledger rows or change concurrency to force a replay.

This repair's source tests and declared resources do not prove deployed recovery.
After a targeted, exclusive CrawlerStack deployment, record actual service-role,
queue/key, alarm and original-day replay checks. A same-day replay using already
completed children must leave attempts, documents and charges unchanged. Actual
EventBridge failure-to-DLQ delivery needs separate bounded service acceptance;
do not label an operator-written queue fixture as proof of that service path.

Before enabling all sources, verify the deployed scheduler role's actual scan,
nested-workflow IAM, canary selection, duplicate execution behavior and failure
isolation, then record normal source counts and the enabled rule. Keep the
publication, recovery and archive evidence above bound to the release.

## Rollback and retention

Rollback disables new writers first and leaves readers, finalization and recovery
available. Do not delete immutable objects or ledger rows to free a budget.
Deploy changed stacks individually with `--exclusively`; never deploy
KnowledgeStack as a dependency or run an S3 sync with `--delete`.


### Daily canary execution evidence (2026-10-21)

The deployed scheduler's own role completed its strongly consistent projected
Scan and selected exactly the configured canary; the daily rule remained disabled
and recovery enabled. The first real daily parent and nested child succeeded at
09:21 UTC with zero queries, model attempts, documents and charge. This verifies
nested IAM and orchestration with empty synthetic search inputs, separately from
the previously accepted article publication. A second parent exposed the actual
`StepFunctions.ExecutionAlreadyExistsException` name and failed under the original
catch. The corrected catch requires a fresh duplicate-only acceptance run; do not
count the original successful child as a second publication. The synthetic source's
original configuration was conditionally restored after both parents and their
known child were confirmed terminal. All-source activation remains pending.
