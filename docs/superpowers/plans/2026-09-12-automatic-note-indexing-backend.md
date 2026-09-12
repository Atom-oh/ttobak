# Automatic indexing backend contract

`cmd/kb` accepts canonical DynamoDB notifications and internal `tick`/`sync`
requests. This slice enables no automatic trigger. Runtime configuration is
`TABLE_NAME`, `BUCKET_NAME` (assets), `KB_BUCKET_NAME`, `KB_ID`, `DATA_SOURCE_ID`.

Jobs use `KBINDEX#JOBS / sha256(sourcePK + NUL + sourceSK)`; the version/lease
coordinator is `KBINDEX#CONTROL/STATE`. Its 20-minute lease covers the maximum
invocation and is retained after uncertain writes. Persist the ingestion token
before submission and keep it across unknown/conflicting replies.

Sources are meetings and personal/account documents. Immutable projections use
`canonical/v1/{kind}/{resourceHash}/{runId}/`; clean obsolete/partial generations
and exact legacy meeting exports before sync. Completion requires a successful
provider job, fresh source/object checks and a conditional write (ADR-038).

Paginated reconciliation rotates four-job pages. Failures back off 1–30 minutes;
unknown revisions preserve cooldown, while a proven new revision resets it.
Permanent source defects become durable failed jobs, not blocked stream records.
QA, status UI and activation are separate integration steps described below.

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

Activation order is required for recall as well as authorization: deploy and
verify QA filters that accept `canonical/v1/` metadata and reauthorize current
sources first; configure the worker's environment/IAM/12-minute/1-GB budget
second; enable stream/scheduled delivery last. The old QA URI filter accepts
only `meetings/{owner}/` exports and cannot discover canonical projections.
The worker deletes those legacy exports, so enabling it first loses meeting
recall. Rolling QA back afterward requires re-exporting legacy objects; stopping
the worker alone does not restore them. Preserve compatible readers on rollback.

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
  GetItem/Query/Scan/UpdateItem/ConditionCheckItem and stream-consumer permissions;
  assets GetObject/GetObjectVersion under transcripts, docs and docs-pdf plus
  ListBucket to distinguish missing sources/previews from forbidden reads; KB
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
- Confirm deployment of the source-binding converter from PR204 before backfill.
  Unbound previews remain WAITING_SOURCE until regenerated. No converter, QA,
  frontend or infra changes are included in this worker slice.
- Deployed ingestion/recall and real source edit/delete/revoke evidence remain
  host-owned prerequisites; synthetic tests alone do not establish those results.

Validation: run `/usr/local/go/bin/go test ./... -count=1`,
`/usr/local/go/bin/go vet ./...`, and a Linux ARM64 `lambda.norpc` KB build.
Tests use synthetic SDK responses; deployed recall is a separate activation gate.
