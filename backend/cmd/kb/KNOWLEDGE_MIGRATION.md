# Existing KB binary migration

Extends [ADR-038](../../../docs/decisions/ADR-038-canonical-note-indexing.md) using
the existing full S3 sync coordinator; no direct ingestion or fabricated DOC#
source rows. Original upload/list/delete behavior and visibility are preserved.

Code checked: 2026-09-13. The worker is implemented and this activation
configuration selects `all` with its schedule retained. `qa/handler.py` registers
strict source readers in REST and streaming paths. Configuration and registration
do not establish deployed acceptance; the
[bootstrap runbook](../../../docs/runbooks/knowledge-index-bootstrap.md) and
[QA rollout](../../../docs/runbooks/qa-current-source-rollout.md) record deployment
readiness and the remaining public acceptance gates.

## Source and snapshot contract

Private `kb/{owner}/{filename}` and authenticated `shared/**` originals are read
with exact ETag/version binding. The worker never rewrites/deletes them. New
snapshots use `manual-kb/v1/` and `shared-kb/v1/`, immutable conditional PUTs, and
provenance sidecars. Exact paths, fields and revision framing are in the
[Go contract](../../internal/service/INDEX_SOURCE_CONTRACT.md) and
[existing QA reader contract](../../python/qa/SOURCE_CONTRACT.md).
The registered `manual_kb.py` reader supports both schemas and requires verified
snapshots for binary evidence. Adding metadata to old chunks is not current-byte
proof.
Vectors: `backend/internal/service/testdata/knowledge-revisions.json`.

Each scheduled tick advances one bounded page per original prefix. Jobs retain
source keys to recover deletion after listing stops finding an object. Source
reads, current HEAD checks, run/lease CAS and uncertain-write freezes bind each
publication. Transient errors preserve validated snapshots; the coordinator
tracks retries/backoff and per-document status. Partial full-sync success
requires `INDEXED` for current snapshots and `NOT_FOUND` for removed
snapshots/original vectors, followed by fresh source checks.

PDF, DOC, DOCX, XLS and XLSX are copied as bytes. PPT/PPTX, empty and oversized
sources have explicit failure states; the 50 MiB limit is not a filename fallback.
Content-Type is copied from the original when present and checked for read races.

## Rollout and operations

Required env: TABLE_NAME, BUCKET_NAME, KB_BUCKET_NAME, KB_ID, DATA_SOURCE_ID,
`INDEXING_MODE=manual-only|all`. Mode is validated before AWS client initialization.
Follow the [bootstrap runbook](../../../docs/runbooks/knowledge-index-bootstrap.md):

1. Initially deploy with schedule/stream off: manual-only, bootstrap IAM and
   1,024 MiB/12 minutes. Explicitly enable the schedule after verification.
   This bootstrap stage precedes the current all-mode activation configuration.
2. Verify private/shared snapshots and deployed synthetic recall. Canonical
   sources/jobs and canonical/legacy meeting exports remain untouched.
3. Deploy and verify the registered strict QA runtime, including public
   answer/provenance/history acceptance in both transports.
4. After those gates pass, deploy all-mode and canonical permissions/delivery.
   No job edits or ad-hoc global tick invocation substitute for this order.

Mode is durable: downgrade and manual-only resumption of a canonical batch fail
before mutation. Restore `all` after mistaken downgrade; old-QA rollback after
canonical cleanup requires legacy re-export. Verify deployed IAM matches the
mode: originals need read only, and manual-only cannot access canonical rows
or retire meeting exports. `GetKnowledgeBaseDocuments` uses the configured KB
permission; it does not grant original object deletion.

Verify the out-of-band data source includes both snapshot prefixes, bucket
versioning/encryption and runtime sizing. Originals and snapshots can coexist in
that source; QA merges candidates and requires current bindings for binary facts.
Synthetic tests do not establish public QA acceptance or deployed canonical
indexing.

Implementation: `main.go`, `service/indexing.go`, `index_rollout.go`,
`index_knowledge_source.go`, `index_knowledge_provider.go` and `index_provider.go`.
