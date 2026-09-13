# Knowledge index bootstrap and activation

Activation configuration checked: 2026-09-13. `infra/bin/infra.ts` selects
`knowledgeIndexingMode='all'` and `knowledgeIndexScheduleEnabled=true`.
Hold the activation PR until the manual snapshot acceptance in step 3 and
current-source QA deployment/verification in step 4 are complete. Record their
actual deployment and acceptance references before merging. Source configuration
does not prove that a deployment has occurred.

The manual producer prerequisite has a completed
[2026-09-13 acceptance archive](../research/evaluations/2026-09-13-manual-kb-bootstrap/README.md):
private PDF and shared DOCX creation, replacement, current-byte retrieval,
deletion and fixture-version cleanup were observed against the deployed worker.
This archive does not establish authenticated Q&A or canonical-source acceptance.
The current consumer package has also been deployed, but its remaining runtime
acceptance must finish before this activation can merge.

## Readiness checkpoint — 2026-09-13

| Gate | Recorded evidence | State |
|---|---|---|
| Manual producer snapshots/recall/replacement/deletion | Linked acceptance archive, including exact fixture-version cleanup | Complete |
| Current QA package deployment | Commit `8959419e1fee826c5debad91aad1a05b42b95866`; infrastructure run `34752539144` and frontend run `34752539147` succeeded; 24 QA runtime files verified against source | Complete for that deployment |
| Public WS restoration | Public `/ws` and authenticated connection verified; operator receipt `ws-restore-connect-8959419-system-python.json` | Transport readiness only |
| Current-source public answers/history | Earlier notes REST timed out, a manual follow-up lost provenance, and WS returned an empty answer. New post-fix acceptance is separate from those preserved failures | Incomplete |
| Canonical `all` activation and CRUD/backfill | This draft selects the future deployment configuration | Not deployed by this record |

The QA package receipt is `qa-deployment-8959419e1fee-verified.json`.
These operator-held records identify the observation; they are not invented
canonical acceptance results. Refresh exact package/transport evidence for the
deployment actually used by acceptance. Do not relabel older build receipts.

The planned REST acceptance path is the asynchronous job API in PR242:
verify its deployed JWT routes, queue mapping and worker, then perform public
job acceptance while `qaAsyncJobs=false`. UI activation is a separate later
change. PR242 is not an indexing code dependency, but its transport addresses
the known long synchronous REST delivery failure. An equivalent successful
bounded REST proof may satisfy the consumer gate; WS-only results cannot waive
the REST portion.

Before merge, use existing manual-only fixtures to verify private/shared
retrieval, complete source details, same-session follow-ups, source replacement/
deletion and grant revocation. Verify notes/attachments and current live-input
handling through the deployed consumer. Coordinate both transports' V1 checks
before changing shared fixtures to V2 or deleting them. Keep unknown/failed
attempts and use explicitly separate repair lineages.

Canonical vector backfill and file-backed DocHub create/edit/delete require
`all`; they are post-activation checks in step 6, not circular pre-merge gates.
The existing meeting, personal-note and account-note fixtures predate stream
activation and must be found by reconciliation. A current-text fallback alone
does not establish index synchronization.

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
