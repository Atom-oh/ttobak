# Knowledge index bootstrap and activation

This preparation deploys with scheduled execution disabled. Enable it only
after the canonical coordinator and the private/shared migration worker,
including its required `INDEXING_MODE` validation, are merged and built.

`knowledgeIndexingMode` in `infra/bin/infra.ts` is the single deployment choice
passed to AiStack and GatewayStack. It starts as `manual-only`.
`knowledgeIndexScheduleEnabled` starts as `false`; changing it requires a
separate reviewed activation once the mode-aware worker is deployed.

## Manual snapshot bootstrap

The worker receives the actual table, assets bucket, KB bucket, KB ID and data
source ID. When explicitly enabled, the one-minute EventBridge schedule
submits `{"action":"tick"}`.
The Lambda has 1,024 MiB and a 12-minute timeout; the worker's shorter internal
deadline leaves cleanup time. Its conditional coordinator prevents overlapping
generations. EventBridge delivery has two retries, a five-minute event age and
an encrypted seven-day DLQ.

In `manual-only` mode:

- Canonical DynamoDB mapping and stream-read grants are absent.
- DynamoDB access is limited to `KBINDEX#JOBS` and `KBINDEX#CONTROL` partition
  keys; bootstrap cannot read canonical meeting/document rows.
- The worker may read private `kb/*` and authenticated-shared `shared/*`
  originals and publish/delete only `manual-kb/v1/*` and `shared-kb/v1/*`
  snapshots. It cannot delete original files or existing meeting exports.
- Canonical source reads, DynamoDB Scan, and canonical/meeting projection
  writes/deletes are absent from its role.
- The existing QA handler keeps using its existing sources while the worker
  automatically paginates and backfills the immutable snapshots.

The worker itself skips canonical scans, notifications and jobs in this mode.
It persists its applied mode and rejects `all → manual-only` or a manual-only
start over a canonical batch. Do not edit coordinator records to bypass that
guard and do not invoke ad-hoc global ticks.

Before bootstrap, use a strong read of `KBINDEX#CONTROL / STATE`, including
PK/SK, to check for an existing mode/batch. Record the current Lambda code hash,
mode, configuration and coordinator observation in deployment evidence.

## Verify and enable canonical indexing

1. Deploy the disabled preparation through AiStack, then GatewayStack, each
   with `--exclusively`. Never deploy KnowledgeStack or use `cdk deploy --all`.
2. After the mode-aware migration worker is deployed, verify
   `INDEXING_MODE=manual-only`, the intended KB/source IDs, the disabled
   schedule, absence of a canonical mapping/stream grants, and scoped snapshot
   permissions. Enable `knowledgeIndexScheduleEnabled` in a reviewed PR.
3. Observe normal scheduled processing of synthetic private/shared files. Verify new
   immutable snapshots reach INDEXED and contain the expected current-byte
   evidence. Original files and meeting exports must remain intact.
4. Deploy the complete current-source QA consumer with its bucket configuration,
   source-read permissions, private/shared snapshot verification and session
   revalidation. Verify overwrite, deletion and access revocation behavior.
5. Change `knowledgeIndexingMode` to `all` in a reviewed activation PR. This
   adds canonical read/condition-check and stream permissions and creates the
   DynamoDB mapping together with `INDEXING_MODE=all`. Existing manual batches
   retain their ingestion token and finish normally before canonical backfill.
   DynamoDB writes remain limited to the job/control partitions. Full mode
   also permits deletion of legacy `meetings/*` exports and writes/deletes of
   `canonical/v1/*` projections. The stream activates independently of the tick
   switch, so keep the schedule enabled for normal full-mode processing.
6. Verify canonical create/edit/delete/revoke behavior and DLQ/failure states.
   A provider failure must not be reported as an empty successful search.

The full-mode mapping uses the table's existing stream, starts at LATEST, and filters
canonical USER meeting/document and ACCOUNT document keys. It uses batch size
20, partial batch failures, bisection, three retries and a 23-hour record age.
The canonical catalogue scan covers pre-activation changes without stream
replay. Stream permissions and the DLQ policy belong to GatewayStack so no
reverse dependency on AiStack is introduced.
The function imports an immutable role reference; manage its grants through
AiStack's KB role and GatewayStack's explicit delivery policy, not direct
`grant*` calls on the function.

## Rollback

Before canonical activation, set `knowledgeIndexScheduleEnabled=false` in CDK
to pause bootstrap while
preserving original files and the existing QA path. After `all` has run, an
environment downgrade is not a legacy-source restore. Preserve the guarded
current-source consumer or perform an explicit reviewed legacy re-export;
never serve stale unbound chunks as an availability fallback.
