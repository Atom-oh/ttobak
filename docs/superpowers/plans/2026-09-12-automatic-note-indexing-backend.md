# Automatic indexing backend contract

This worker slice uses the existing S3 data-source full ingestion path. It does not enable stream/scheduled delivery; current-source retrieval guards and the required runtime configuration/IAM must ship before activation.

- `cmd/kb` accepts DynamoDB stream records (canonical keys only, partial-batch
  failures) and scheduled/internal `tick` or `sync` requests.
- Required environment: `TABLE_NAME`, `BUCKET_NAME` (assets), `KB_BUCKET_NAME`,
  `KB_ID`, `DATA_SOURCE_ID`. IDs come only from runtime configuration.
- Resource jobs live at `KBINDEX#JOBS / sha256(sourcePK + NUL + sourceSK)`.
  Source classes: `meeting`, `personalDocument`, `accountDocument`.
- States: PENDING, PREPARING, WAITING_SYNC, INDEXED, DELETED, WAITING_SOURCE,
  FAILED. Stream delivery reads the current source revision before queueing; duplicate same-revision events preserve active preparation. It never trusts old event images.
- A version-CAS coordinator at `KBINDEX#CONTROL / STATE` freezes a batch through
  EXPORTING → PREPARED → RUNNING → FINALIZING. Persist its idempotency token before
  StartIngestionJob. Conflicts and unknown submissions retain the same token.
- One coordinator lease serializes publication and cleanup. A 20-minute lease
  outlives Lambda's maximum execution duration; processing uses a shorter context
  budget. Uncertain S3 writes retain this lease rather than immediately allowing
  another publisher. Every worker transition also checks version/run/lease.
- Immutable projections use `canonical/v1/{kind}/{resourceHash}/{runId}/`.
  Persist planned object keys first; enumerate the resource prefix to recover
  partial/obsolete projections and remove legacy `meetings/{owner}/{id}.md`
  exports before syncing. No unrelated crawler/upload prefixes are deleted.
- Metadata: `resourceKind`, `resourceId`, `sourcePK`, `sourceSK`, `sourceRevision`,
  `indexRunId`, `indexSchema: canonical-v1`, `sourceObjects` (JSON string).
  Metadata is discovery/provenance, never authorization.
- Source revisions bind canonical projection fields and S3 ETag/version/size.
  Reads use pinned versions plus If-Match. INDEXED/DELETED requires a successful
  provider job, fresh object/source checks, and a conditional source+job write.
- Reconciliation pages canonical records, known jobs (including deleted sources),
  and legacy exports, persisting cursors so missed streams/backfill make progress.
- PPTX/PPT waits for PDF preview metadata `source-etag` and, when supplied by S3,
  `source-version-id` matching its current original object. Converter changes
  belong to the host; an unbound legacy preview is not proof of current content.

Implement model/repository/provider/source/worker modules and synthetic tests for
reordering, crash recovery, lost responses, source deletion, byte revisions,
cleanup, competing full syncs, HTTP serialization and command dispatch. QA,
status API/UI and CDK event/IAM wiring remain host-owned.

## Cross-language source revision

SHA-256 (lowercase hex) over consecutive UTF-8 strings, each encoded as decimal
UTF-8 byte length, `:`, then the bytes, with no separator after the value:

1. `canonical-v1`, source PK, source SK, outcome (`INDEXED`, `DELETED`, or
   `WAITING_SOURCE`).
2. For each present projection field sorted by name: `field`, field name, type
   (`string` or `null`), value (empty string for null). Missing attributes are
   absent, not null. `IndexSourceFields` defines the exact per-kind field list.
3. For every source object sorted by key: `object`, key, ETag exactly as returned
   by S3, version ID (or empty), decimal size, `true`/`false` missing flag,
   `source-etag` metadata (or empty), `source-version-id` metadata (or empty).

`sourceObjects` metadata carries these object bindings as a JSON string. Only
the two converter binding metadata fields participate. Empty/missing S3 objects
are distinguishable; GET uses both version and If-Match when reading the bytes.
Each binding has `key`, `etag`, `size`, optional `versionId`, optional `missing`,
and optional `metadata` containing those converter binding fields. Published
sidecars always hash outcome `INDEXED`; WAITING_SOURCE and DELETED publish no
files. Output filenames are `meeting.md`, `document.md`, or `file.{extension}`
for the supported file formats.

QA must retain the exact present DynamoDB projection fields separately from its
display DTO: do not normalize null/missing to empty text, parse/reformat dates,
or substitute hydrated transcript text when hashing. `read_current_document`
currently returns a normalized display DTO, so its return value alone is not
the revision input. Fresh authorization must precede retaining these fields.
`IndexSourceRevision` is exported for backend status callers. QA must combine
fresh authorization and source checks with the revision, never trust index
metadata or the saved job state as a grant.

Literal vectors (including Korean, emoji, null, missing files and opaque S3
version IDs) are in `backend/internal/service/testdata/index-revisions.json`.
Expected hashes were independently calculated with Python stdlib, then checked
against Go. QA can consume the same vectors without importing the worker.

Exact projection field allowlists (include present fields only):

- Meeting: `meetingId`, `userId`, `title`, `date`, `status`, `notes`, `content`,
  `selectedTranscript`, `transcriptA`, `transcriptB`, `actionItems`, `updatedAt`.
- Personal/account document: `docId`, `accountId`, `sourceUserId`, `title`,
  `docType`, `content`, `fileKey`, `fileName`, `mimeType`, `updatedAt`.

Provider references: AWS [supported formats and 50 MB file limit](https://docs.aws.amazon.com/bedrock/latest/userguide/knowledge-base-ds.html),
[S3 metadata sidecars and 10 KB metadata limit](https://docs.aws.amazon.com/bedrock/latest/userguide/s3-data-source-connector.html),
and [StartIngestionJob idempotency](https://docs.aws.amazon.com/bedrock/latest/APIReference/API_agent_StartIngestionJob.html).

## Integration handoff

- `NewIndexingService(repo, objects, ingestion, assetsBucket)` exposes `Enqueue`,
  `Tick`, `ReadSource` and `Status`. `Status` needs canonical authorization in its
  caller, and revalidates source/object bindings and projection inventory before
  returning INDEXED. Preserve a current failed-source error even if that source
  failed before a new export revision could be recorded.
- Configure the existing KB Lambda with a 12-minute timeout and sufficient
  memory for bounded 50 MB file reads (1 GB recommended). The worker uses a
  10-minute processing context and a 20-minute crash-recovery lease. Existing
  30-second/256 MB settings are insufficient for this workload.
- Enable canonical-key DynamoDB stream delivery with `ReportBatchItemFailures`,
  including REMOVE events, and a scheduled tick. Give the role table
  GetItem/Query/Scan/UpdateItem/TransactWriteItems and stream-consumer permissions;
  assets GetObject/GetObjectVersion under transcripts, docs and docs-pdf; KB
  ListBucket for canonical/v1 and meetings, PutObject under canonical/v1 and
  DeleteObject under canonical/v1 and meetings; and scoped Bedrock
  StartIngestionJob/GetIngestionJob/ListIngestionJobs.
- The first tick may wait for an existing manual/crawler sync. No worker export
  mutation starts while that sync is known active. A concurrent start conflict
  after export retains the frozen generation/token until its own sync is accepted.
- Live retrieval must authorize canonical sources and validate current revisions.
  S3 heads and DynamoDB transactions are not one cross-service atomic read;
  retrieval must reject stale file chunks even if an object changes just after
  the worker's final freshness check. This is why stored INDEXED is not a grant.
- PPTX/PPT preview binding requires a separate converter change. Unbound previews
  remain WAITING_SOURCE. No converter, QA, frontend or infra changes are included.
- Deployed ingestion/recall and real source edit/delete/revoke evidence remain
  host-owned prerequisites; synthetic tests alone do not establish those results.

Review split: model/repository/provider files and their tests form a standalone
foundation (no source service or worker dependency). A second patch can add
`index_source*`, `indexing*` service files, revision fixture, `cmd/kb`, and this
plan. Keep all regression tests when splitting to satisfy review size limits.

Validation uses home-backed GOTMPDIR/TMPDIR and the shared Go cache. Full
`go test ./... -count=1`, `go vet ./...`, and a Linux ARM64 `lambda.norpc` KB build
are required. The indexing suite covers stream filtering, source/CAS races,
coordinator/run leases, accepted replies lost across both provider and DynamoDB,
partial uploads, cleanup/deletion/recreation, external sync conflicts, failed
ingestion, retry cooldowns, paginated backfill, raw byte projection and revision
binding, metadata bounds, and actual SDK HTTP serialization. No live AWS calls.

Validation on main plus this worker patch uses the commands above. The new stream-delivery regression proves a duplicate leaves the active preparation/version intact, while editing saved notes queues a new revision. The deliberately bad version failed before the fix. The opt-in real-model evaluation is not run by these unit tests.
