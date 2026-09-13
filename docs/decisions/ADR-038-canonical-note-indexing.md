# ADR-038: Canonical note indexing

- Status: Accepted, including the private/shared bootstrap extension, 2026-09-12.
- Code checked: 2026-09-13. `infra/bin/infra.ts` selects `manual-only` with its
  one-minute schedule enabled. Canonical stream delivery requires `all`;
  strict QA runtime wiring remains separate. These are source/configuration
  observations, not deployed acceptance.

## Context and decision

Manual meeting exports miss edits and deletion. Direct ingestion would create
a second writer beside the existing S3 sync. Use the same S3 data source,
immutable projections and a coordinator that coalesces full syncs.

Canonical sources are meetings and personal/account documents. Durable jobs
bind saved fields and source object ETags/versions to a revision and lease.
Completion requires successful ingestion, exact document status, immutable
projection inventory and fresh source checks. Paginated reconciliation
recovers missed events and deletion; changing or failed sources back off
without holding other jobs.

Private `kb/{owner}/{filename}` and authenticated-shared `shared/**` originals
also receive immutable `manual-kb/v1/` and `shared-kb/v1/` snapshots, without
inventing canonical document rows. Preserve owner isolation and authenticated
sharing. Metadata alone cannot prove that old binary chunks match current bytes.

## Activation order

1. Prepare the mode-aware worker with delivery off, then explicitly enable
   `manual-only` snapshot production with its restricted permissions.
   The checked-in configuration has reached this schedule-enablement stage.
2. Verify immutable private/shared snapshots and current-byte recall while
   retaining originals and legacy meeting exports.
3. Deploy and verify the complete strict QA consumer, including source
   permissions, snapshot checks and conversation-history revalidation.
4. Enable `all` with canonical permissions, stream delivery and reconciliation.
   Existing manual batches finish under their original ingestion token.

Mode is durable: `all → manual-only` and a manual-only start over a canonical
batch are rejected. Restore `all` after an accidental downgrade; never edit
coordinator state or invoke ad-hoc global ticks to bypass the guard.

## Consequences

Search is eventually consistent and adds request/cleanup cost. An indexed
status never replaces current authorization or source checks. Old QA recognizes
legacy `meetings/{owner}/` exports; retiring them before strict QA loses recall.
After retirement, old-QA rollback requires explicit re-export. Stopping the
worker does not restore deleted exports or justify serving stale chunks.

## Evidence

- [Worker](../../backend/cmd/kb/main.go),
  [coordinator](../../backend/internal/service/indexing.go),
  [mode guard](../../backend/internal/service/index_rollout.go).
- [Source/provider contract](../../backend/internal/service/INDEX_SOURCE_CONTRACT.md),
  [migration contract](../../backend/cmd/kb/KNOWLEDGE_MIGRATION.md).
- [Bootstrap runbook](../runbooks/knowledge-index-bootstrap.md),
  [QA rollout](../runbooks/qa-current-source-rollout.md).
