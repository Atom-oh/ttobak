# Knowledge index bootstrap and activation

Activation configuration checked: 2026-09-13. `infra/bin/infra.ts` selects
`knowledgeIndexingMode='all'` and `knowledgeIndexScheduleEnabled=true`.
The named preactivation public checks below are complete as of 2026-09-13.
Complete normal current-HEAD review/CI checks, then use the existing conditional
merge and release workflow. Record actual deployment and acceptance references;
source configuration does not prove that a deployment has occurred.

The manual producer prerequisite has a completed
[2026-09-13 acceptance archive](../research/evaluations/2026-09-13-manual-kb-bootstrap/README.md):
private PDF and shared DOCX creation, replacement, current-byte retrieval,
deletion and fixture-version cleanup were observed against the deployed worker.
Authenticated notes, manual-binary retrieval, history invalidation, provider
deletion and old-version cleanup are recorded separately in the completed
[public QA evidence archive](../research/evaluations/2026-09-13-public-qa/README.md).
Neither archive establishes canonical indexing acceptance. The additional
attachment and live-input qualification has its own current-run status below.

## Readiness checkpoint — 2026-09-13

| Gate | Recorded evidence | State |
|---|---|---|
| Manual producer snapshots/recall/replacement/deletion | Linked acceptance archive, including exact fixture-version cleanup | Complete |
| Current QA package deployment | At 13:05 UTC, 25 runtime Python files matched `921c2b3e0e0b5c97ab684a2adfe9a1691bcfbe2a`; Active/Successful, Python 3.12, 300-second timeout | Verified package readiness |
| Public notes/manual-binary/history and transport checks | Linked public QA archive: notes freshness and grant revocation, private/shared V1/V2, current attribution, REST/WS behavior and source-change rejection | PASS for the recorded scenarios |
| Provider deletion and binary old-version cleanup | Linked public QA archive, with exact NOT_FOUND and version-cleanup receipts | Complete |
| Additional PDF attachment qualification | Current-run `attachment-pdf-results.json`: real PDF partial coverage, page continuation, foreign 404, cited QA and retained retry result with `current=false` while running | PASS for the recorded attachment case |
| Neutral live-input qualification | Current-run `live-context-neutral-results.json`: current live input and saved notes remain distinct; corrected latest-only input suppresses old values while preserving label and saved-source provenance | PASS for the recorded live case |
| Named preactivation public qualification | Completed archive plus the current-run PDF attachment and neutral live-input cases, verified against the stated deployments | Complete as of 2026-09-13 |
| Old validation-run closeout | Closed run `317daf7a9b4ee21344e4`, archived `final-cleanup.json`: users, scoped rows, assets versions and local credentials cleaned | Complete |
| New canonical canary preparation | Five genuine public-API fixtures prepared at 13:40 UTC while indexing remained manual-only | Prepared; not evidence of indexing |
| Canonical `all` activation and CRUD/backfill | Deployment follows completed qualification and normal release checks; canonical acceptance follows activation | Pending |

The current package receipt is
`/tmp/ttobak-qa-latest-deployed-code-check.json`, observed at
`2026-09-13T13:05:10.142245+00:00`, with code SHA-256
`ohQu+FP7YTLbm0sjCRXsw2AzujzhAv/fg94xhQfwxhQ=`.
This does not rebind older acceptance to the newer package. Preserve each
receipt's actual deployment, timestamp and source hashes. Async UI activation
has not occurred; `qaAsyncJobs` remains false.

Additional qualification used run `qa-validation-b39053f454239d733c70`, located
through `/tmp/ttobak-public-qa-validation-path.txt`. Its attachment and neutral
live-input cases passed on 2026-09-13 against the latest verified `921c2b3`
runtime. The old run is archived through
`/tmp/ttobak-public-qa-validation-closed-317daf7a9b4ee21344e4.txt`.
Its earlier cleanup-pending entry is historical; the subsequent closeout receipt
records completed cleanup. Do not reuse its deleted fixtures for backfill.

Historical failures remain evidence: earlier synchronous notes REST timeout,
missing manual follow-up provenance, an empty WS answer and the initial
private-V2 refusal. Preserve those records alongside the separately identified
corrected passes; do not relabel or erase a failed lineage.

The current run's initial `live-context-results.json` also remains a diagnostic
failure. It asked for a nonexistent rollout-codename field while the fixture's
actual field was `Marker`, and used an age-labeled `LIVE_OLD` code. The corrected
`live-context-neutral-results.json` uses the real `Marker` field and neutral
`DEPLOY` codes. Multiple inputs changed; these observations do not establish a
single cause for the initial failure.

The authenticated async job API is wired and its public results are covered in
the archive; UI activation remains a separate change. Preserve both REST and WS
qualification. For additional cases, coordinate both transports' V1 checks before
overwriting or deleting shared fixtures, and keep unknown/failed attempts in
separate lineages.

Canonical vector backfill and file-backed DocHub create/edit/delete require
`all`; they are post-activation checks in step 6, not circular pre-merge gates.
The five current-run canaries are a meeting, personal/account notes and
file-backed personal/account documents, recorded in `canonical-manifest.json`.
They predate stream activation and must be found by normal reconciliation after
`all` is active. Their public-API creation and current-text fallback do not prove
an indexed projection or successful provider ingestion.

## Bootstrap boundary

The worker requires `TABLE_NAME`, `BUCKET_NAME`, `KB_BUCKET_NAME`, `KB_ID`,
`DATA_SOURCE_ID` and `INDEXING_MODE=manual-only|all`. It validates mode before
initializing AWS clients. The one-minute schedule submits `{"action":"tick"}`;
Lambda has 1,024 MiB and a 12-minute timeout with a shorter worker deadline.
A conditional coordinator prevents overlapping generations. EventBridge has
two retries, a five-minute event age and an encrypted seven-day DLQ.

In `manual-only` mode:

- No canonical stream mapping, stream-read grants, source reads or table Scan.
  DynamoDB access is limited to `KBINDEX#JOBS` and `KBINDEX#CONTROL`.
- Read private `kb/*` and authenticated-shared `shared/*` originals; publish
  or delete only `manual-kb/v1/*` and `shared-kb/v1/*` snapshots.
- Preserve originals, canonical projections and legacy `meetings/*` exports.
  The active QA handler retains its existing retrieval path during bootstrap.
- Paginate original prefixes and skip canonical notifications/jobs/scans.
  Persist mode; reject `all → manual-only` or a manual-only start over a
  canonical batch before mutation.

Before changing delivery, strongly read `KBINDEX#CONTROL / STATE`, including
PK/SK, and record its mode/batch alongside the Lambda code hash and configuration.
Do not edit coordinator records or invoke ad-hoc global ticks to bypass guards.

## Deployment and acceptance

1. For initial preparation, deploy the mode-aware worker with delivery off,
   using `TtobakAiStack --exclusively`, then
   `TtobakGatewayStack --exclusively`. Never deploy KnowledgeStack or use `--all`.
2. Verify `manual-only`, intended buckets/KB/source IDs, scoped IAM and absent
   canonical delivery before enabling the schedule in a reviewed activation.
   Schedule enablement precedes the separate all-mode activation.
3. Observe scheduled synthetic private/shared uploads, overwrite and deletion.
   Require current immutable snapshots to reach `INDEXED`, preserve original
   files/meeting exports and verify actual recall. Confirm the external S3
   data source includes snapshot prefixes and supports the required metadata.
4. Verify the complete deployed strict QA runtime, source-read permissions and
   both bucket variables against the intended reviewed commit. Complete current
   private/shared answer/provenance, authorization, revision and whole-history
   acceptance in REST and streaming, using the readiness path above. Follow the
   [QA rollout](qa-current-source-rollout.md); package readiness alone is insufficient.
5. Enable `all` in a reviewed activation, adding canonical source reads,
   condition checks, stream grants and the mapping together. Manual batches
   retain their ingestion token and finish before canonical backfill.
   Job/control partitions remain the only DynamoDB write targets.
6. Verify canonical meeting/personal/account create/edit/delete/revoke,
   reconciliation, new-term recall and DLQ/failure handling. Include file-backed
   personal/account DocHub fixtures with byte-bound source/projection proofs.
   Require exact current provider INDEXED/NOT_FOUND observations; provider
   failure must not appear as empty successful search.

Full mode permits `canonical/v1/*` writes/deletes and retirement of legacy
`meetings/*` exports. Its existing-table stream mapping starts at LATEST,
filters USER meeting/document and ACCOUNT document keys, and uses batch size
20, partial failures, bisection, three retries and a 23-hour record age.
Paginated canonical scans cover changes predating activation. The stream runs
independently of the tick switch; keep the schedule enabled for reconciliation.

AiStack owns the KB role; GatewayStack owns explicit stream/DLQ delivery policy.
The function imports that role as immutable. Avoid direct function `grant*`
calls that introduce a reverse stack dependency.

## Rollback

Before canonical activation, setting the schedule flag false pauses bootstrap
without retiring originals or legacy exports. After `all` has run, restoring
`manual-only` is rejected; restore `all` after an accidental downgrade.
Stopping only the schedule does not disable the canonical stream. Preserve the
strict consumer, or perform a reviewed legacy re-export before old-QA rollback.
Never use stale unbound chunks as an availability fallback.

To pause after canonical activation, retain `INDEXING_MODE=all`, set
`knowledgeIndexScheduleEnabled=false`, and set `enabled: false` on the canonical
`DynamoEventSource` in `gateway-stack.ts` through a reviewed change. Deploy
`TtobakGatewayStack --exclusively` and verify both rule and mapping are disabled.
Existing invocations may finish; preserve coordinator state and snapshots.
Re-enable both delivery paths through a reviewed change after resolution.

See [migration details](../../backend/cmd/kb/KNOWLEDGE_MIGRATION.md) and
[ADR-038](../decisions/ADR-038-canonical-note-indexing.md).
