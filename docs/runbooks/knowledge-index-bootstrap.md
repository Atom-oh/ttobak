# Knowledge index bootstrap and activation

Activation configuration checked: 2026-09-13. `infra/bin/infra.ts` selects
`knowledgeIndexingMode='all'` and `knowledgeIndexScheduleEnabled=true`.
Hold the activation PR until the manual snapshot acceptance in step 3 and
current-source QA deployment/verification in step 4 are complete. Record their
actual deployment and acceptance references before merging. Source configuration
does not prove that a deployment has occurred.

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
4. Deploy the complete strict QA runtime with source-read permissions and both
   bucket variables. Verify private/shared snapshots, current authorization,
   revision changes and whole-history invalidation. Follow the
   [QA rollout](qa-current-source-rollout.md); helpers alone are insufficient.
5. Enable `all` in a reviewed activation, adding canonical source reads,
   condition checks, stream grants and the mapping together. Manual batches
   retain their ingestion token and finish before canonical backfill.
   Job/control partitions remain the only DynamoDB write targets.
6. Verify canonical create/edit/delete/revoke, reconciliation and DLQ/failure
   handling. Provider failure must not appear as empty successful search.

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
