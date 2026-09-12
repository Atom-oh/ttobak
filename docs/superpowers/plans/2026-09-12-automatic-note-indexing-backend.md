# Canonical indexing contract

No automatic trigger is enabled; invocation details are in API-SPEC §6.
Required env: `TABLE_NAME`, `BUCKET_NAME` (assets), `KB_BUCKET_NAME`, `KB_ID`,
`DATA_SOURCE_ID`; the latter two are bare ten-character alphanumeric IDs.

Jobs use `KBINDEX#JOBS`; the version/lease coordinator is `KBINDEX#CONTROL/STATE`.
It serializes immutable publication and full sync, retaining the token and
20-minute lease after uncertain writes. Four-job pages preserve fairness.
Changing members are deferred; failed jobs back off 1–30 minutes. Unknown
revisions preserve cooldown; proven edits reset it.
Transient source/HEAD/GET errors preserve projections and source revisions;
preparation retries without cleanup, and finalization retains the pending member.
Confirmed missing/invalid sources and changing sources use separate cleanup paths.

## Projection and revision

Immutable data: `canonical/v1/{kind}/{resourceHash}/{runId}/{file}`; adjacent
`.metadata.json` sidecars. Kinds: meeting, personalDocument, accountDocument.
Files: `meeting.md`, `document.md`, `file.{extension}`. Metadata attributes:
`indexSchema=canonical-v1`, `resourceKind`, `resourceId`, `sourcePK`, `sourceSK`,
`sourceRevision`, `indexRunId`, `sourceObjects` (JSON string). Sidecars are limited
to 10,000 bytes. Metadata is provenance, never authorization.

Revision is lowercase SHA-256 over UTF-8 strings framed as decimal byte length,
colon, bytes (no trailing separator):

1. `canonical-v1`, PK, SK, outcome (`INDEXED`, `DELETED`, `WAITING_SOURCE`).
2. Each present field sorted by name: `field`, name, type (`string`/`null`), value
   (empty for null). Missing fields are omitted, not normalized.
3. Each object sorted by key: `object`, key, exact ETag, version ID or empty,
   decimal size, `true`/`false` missing flag, `source-etag` metadata or empty,
   `source-version-id` metadata or empty.

Fields: meetings use `meetingId,userId,title,date,status,notes,content,
selectedTranscript,transcriptA,transcriptB,actionItems,updatedAt`; documents use
`docId,accountId,sourceUserId,title,docType,content,fileKey,fileName,mimeType,updatedAt`.
`sourceObjects` carries `key,etag,size` and optional `versionId,missing,metadata`.
Only the two converter binding metadata fields participate. Vectors: `backend/internal/service/testdata/index-revisions.json`.

Read bytes with version/If-Match. Completion requires successful ingestion,
fresh source/object checks and a source+job conditional write. Paginated scans
recover missed events, source deletion and stale/partial projections. Cleanup
is limited to that resource's canonical prefix and exact legacy meeting exports.
PPTX/PPT needs a preview bound to the current original. Confirm PR204 converter
deployment and regenerate unbound previews. Account file ownership follows the
current key and validated write path; SourceUserID remains the original creator.

A partially failed full sync is resolved per immutable document with
`GetKnowledgeBaseDocuments`: current objects require `INDEXED`; removed objects
require `NOT_FOUND`. `removedKeys` is persisted before deletion and retained across
interrupted cleanup. Partial indexing is not success. Provider read errors/pending
states retain the member for retry while other verified members can finalize.
Fresh source/byte reads and the source CAS still follow the provider check.

## Activation and rollback

1. Deploy and verify QA canonical metadata filters and current-source authorization.
   Old QA recognizes only legacy `meetings/{owner}/` URIs and cannot find canonical
   objects. Verify current edits, new terms, deletion and revocation.
2. Configure KB Lambda for 12 minutes/1024 MiB. Grant table GetItem/Query/Scan/
   UpdateItem/ConditionCheckItem; assets GetObject/GetObjectVersion plus ListBucket
   for missing-object detection; KB ListBucket, canonical Put/Delete and exact
   legacy meeting cleanup; scoped Bedrock Start/Get/ListIngestionJobs and
   GetKnowledgeBaseDocuments on the configured knowledge base.
3. Enable canonical-key stream delivery with partial-batch responses, bounded
   retries/DLQ and scheduled reconciliation. Current CDK has no trigger and still
   uses 30 seconds/256 MiB, so configuration must precede activation.

Legacy cleanup makes rollback to old QA require re-export (ADR-038).

Validation: `/usr/local/go/bin/go test ./... -count=1`,
`/usr/local/go/bin/go vet ./...`, and
`GOOS=linux GOARCH=arm64 /usr/local/go/bin/go build -tags lambda.norpc -o cmd/kb/bootstrap ./cmd/kb`.
