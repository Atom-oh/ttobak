# Knowledge index bootstrap and activation

Deploy this infrastructure only after the canonical coordinator and the
private/shared migration worker, including its required `INDEXING_MODE`
validation, are merged and built. The previous placeholder worker cannot
consume these scheduled events.

`knowledgeIndexingMode` in `infra/bin/infra.ts` is the single deployment choice
passed to AiStack and GatewayStack. It starts as `manual-only`.

## Manual snapshot bootstrap

The worker receives the actual table, assets bucket, KB bucket, KB ID and data
source ID. A one-minute EventBridge schedule submits `{"action":"tick"}`.
The Lambda has 1,024 MiB and a 12-minute timeout; the worker's shorter internal
deadline leaves cleanup time. Its conditional coordinator prevents overlapping
generations. EventBridge delivery has two retries, a five-minute event age and
an encrypted seven-day DLQ.

In `manual-only` mode:

- Canonical DynamoDB notifications remain disabled.
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
PK/SK, to check for an existing mode/batch. The 2026-09-12 preflight found no
record. The existing Lambda was Active/Successful at 30 seconds/256 MiB with
no KB/source/mode configuration; that was the inactive baseline, not evidence
that backfill had run.

## Verify and enable canonical indexing

1. Deploy AiStack, then GatewayStack, each with `--exclusively`. Never deploy
   KnowledgeStack or use `cdk deploy --all`.
2. Verify the worker has `INDEXING_MODE=manual-only`, the intended KB/source
   IDs, the schedule and disabled mapping, and the scoped snapshot permissions.
3. Observe scheduled processing of synthetic private/shared files. Verify new
   immutable snapshots reach INDEXED and contain the expected current-byte
   evidence. Original files and meeting exports must remain intact.
4. Deploy the complete current-source QA consumer with its bucket configuration,
   source-read permissions, private/shared snapshot verification and session
   revalidation. Verify overwrite, deletion and access revocation behavior.
5. Change `knowledgeIndexingMode` to `all` in a reviewed activation PR. This
   adds canonical source/projection permissions and enables the existing
   DynamoDB mapping together with `INDEXING_MODE=all`. Existing manual batches
   retain their ingestion token and finish normally before canonical backfill.
6. Verify canonical create/edit/delete/revoke behavior and DLQ/failure states.
   A provider failure must not be reported as an empty successful search.

The mapping uses the table's existing stream, starts at LATEST, and filters
canonical USER meeting/document and ACCOUNT document keys. It uses batch size
20, partial batch failures, bisection, three retries and a 23-hour record age.
The canonical catalogue scan covers pre-activation changes without stream
replay. Stream permissions and the DLQ policy belong to GatewayStack so no
reverse dependency on AiStack is introduced.

## Rollback

Before canonical activation, disable the schedule to pause bootstrap while
preserving original files and the existing QA path. After `all` has run, an
environment downgrade is not a legacy-source restore. Preserve the guarded
current-source consumer or perform an explicit reviewed legacy re-export;
never serve stale unbound chunks as an availability fallback.
