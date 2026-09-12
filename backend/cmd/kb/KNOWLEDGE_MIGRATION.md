# Existing KB binary migration

Extends [ADR-038](../../../docs/decisions/ADR-038-canonical-note-indexing.md) using
the existing full S3 sync coordinator; no direct ingestion or fabricated DOC#
source rows. Original upload/list/delete behavior and visibility are preserved.

## Source and snapshot contract

Private `kb/{owner}/{filename}` and authenticated `shared/**` originals are read
with exact ETag/version binding. The worker never rewrites/deletes them. New
snapshots use `manual-kb/v1/` and `shared-kb/v1/`, immutable conditional PUTs, and
provenance sidecars. Exact paths, fields and revision framing are in the
[Go contract](../../internal/service/INDEX_SOURCE_CONTRACT.md) and
[existing QA reader contract](../../python/qa/SOURCE_CONTRACT.md).
`manual_kb.py` already supports both schemas; strict runtime wiring (PR221) remains
held until snapshots are verified. Adding metadata to old chunks is not proof.
Vectors: `backend/internal/service/testdata/knowledge-revisions.json`.

Each scheduled tick advances one bounded page per original prefix. Jobs retain
source keys to recover deletion after listing stops finding an object. Source
reads, current HEAD checks, run/lease CAS and uncertain-write freezes bind each
publication. Transient errors preserve validated snapshots; retries/backoff and
per-document status follow PR205. Partial full-sync success requires INDEXED for
current snapshots and NOT_FOUND for removed snapshots/original vectors.

PDF, DOC, DOCX, XLS and XLSX are copied as bytes. PPT/PPTX, empty and oversized
sources have explicit failure states; the 50 MiB limit is not a filename fallback.
Content-Type is copied from the original when present and checked for read races.

## Rollout and operations

Required env: TABLE_NAME, BUCKET_NAME, KB_BUCKET_NAME, KB_ID, DATA_SOURCE_ID,
`INDEXING_MODE=manual-only|all`. Mode is validated before AWS client initialization.
Follow the same four stages in [INFRA-SPEC](../../../docs/INFRA-SPEC.md) and
[API-SPEC](../../../docs/API-SPEC.md):

1. Deploy worker with schedule/stream off; configure manual-only, bootstrap IAM,
   1024 MiB/12 minutes, then explicitly enable the schedule (no stream mapping).
2. Verify private/shared snapshots and deployed synthetic recall. Canonical
   sources/jobs and canonical/legacy meeting exports remain untouched.
3. Deploy/verify strict QA runtime using the existing reader foundations.
4. Enable all-mode and canonical permissions/delivery. No job edits or ad-hoc
   global tick invocation substitute for this order.

Mode is durable: downgrade and manual-only resumption of a canonical batch fail
before mutation. Restore all after mistaken downgrade; old-QA rollback after
canonical cleanup requires legacy re-export. Verify the prepared CDK narrowing
described in INFRA-SPEC is deployed; original prefixes need read only. GetKnowledgeBaseDocuments
uses the configured KB permission and does not grant original object deletion.

Verify the out-of-band data source includes both snapshot prefixes, bucket
versioning/encryption and runtime sizing. Originals and snapshots can coexist in
that source; QA merges candidates and requires current bindings for binary facts.
Tests are synthetic; deployed acceptance remains required before strict QA.
