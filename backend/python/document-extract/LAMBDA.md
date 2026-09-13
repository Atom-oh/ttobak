# Attachment text Lambda parent

`handler.lambda_handler` runs on Python 3.12 with `requirements-lambda.txt`.
Application configuration is `BUCKET_NAME` and `TABLE_NAME`; AWS credentials
and region come from the runtime. Clients initialize lazily and are reused;
the parser child receives no parent environment or credentials.

Code checked: 2026-09-13. `DocumentExtraction` configures the worker and internal
event target, and `infra/bin/infra.ts` enables the construct. Go state/read and
QA reader foundations exist. The active upload/API, summary and QA paths do
not yet wire this complete flow; deployed acceptance remains separate.

## Event and publication contract

Accept `source=ttobak.upload`, `detail-type=DocumentUploadCompleted`, with detail
`{bucket,key,meetingId,ownerId,userId,attachmentId,runId}`. `userId` is the
uploader and may differ from the meeting `ownerId`. The bucket must match
configuration. Event `key` grants no authority: canonical attachment and queued
state must agree on the exact source.

1. Strong metadata-only reads load `MEETING#mid / ATTEXT#attachmentId`,
   `USER#owner / MEETING#mid` and `MEETING#mid / ATTACH#attachmentId`.
   Match run, identities, active numeric lease and canonical source key,
   exactly `files/uploader/mid/basename`. No transcript hydration, query,
   scan or model call is needed.
2. Transactionally claim `queued → running` with parent/attachment checks
   and run/status/lease/source/identity conditions. Stale/duplicate work does
   not parse. Running leases are capped at 120 seconds and remaining runtime.
3. HEAD checks size; `GetObject(IfMatch=ETag)` streams at most 20 MiB under
   byte/deadline bounds and closes the body on every path. Format comes from
   the canonical extension. The child gets a reduced wall deadline and 4096
   bytes reserved in its JSON budget for source provenance.
4. Recheck source size/ETag and conditionally PUT at most 1 MiB, including
   newline, to `files/uploader/mid/text/attachmentId/runId.json` with
   `IfNoneMatch="*"`. Add `source` fields: bucket, key, eTag, meetingId,
   ownerId, uploaderId, attachmentId and runId. Recheck ETag before completion.
5. A terminal transaction rechecks parent/attachment and the active
   run/lease/source. Success/partial sets
   `sourceETag,resultKey,unitCount,complete,status,errorCode`, clears the lease
   and writes UTC RFC3339 `updatedAt` compatible with Go `time.Time`.
   Partial output uses `PARTIAL_EXTRACTION`.

## Failure, retry and retention

Failure updates only `status,errorCode,leaseUntil,updatedAt`, conditioned on
row existence and the expected run/status/lease/source/identities. Previous
`resultKey,sourceETag,unitCount,complete` remain. Thus `complete=true` after a
failed attempt can describe retained earlier output, never current-run success;
its JSON `source.runId` identifies that output.

Parser codes remain fixed. Parent codes are `SOURCE_UNAVAILABLE`,
`SOURCE_CHANGED`, `INVALID_SOURCE`, `SOURCE_TOO_LARGE`, `RESULT_WRITE_FAILED`,
`WORKER_FAILED` and `TIMEOUT`; no raw text/exceptions are logged. State failures
raise `STATE_READ_FAILED`/`STATE_WRITE_FAILED` rather than claim a persisted
extraction failure.

SDK writes have one attempt, avoiding a lost successful response followed by
misleading conditional rejection. Do not delete results on conditional or
ambiguous completion. Duplicate running deliveries are ignored; the host's
expired-lease/retry path must queue a fresh run ID. Never reuse IDs.

S3 HEAD and DynamoDB completion are not atomic. Results record the parsed ETag;
consumers must revalidate current source bindings. Overwrites require a new
run. Lost leases, deletion and uncertain commits can leave immutable orphans;
cleanup must preserve referenced results. No `ExtractionCompleted` event is
emitted; index/stream integration belongs to the host.

## Deployment and checks

`infra/lib/document-extraction.ts` configures ARM64, 1,536 MiB, 90 seconds,
isolated subnets and HTTPS egress to S3/DynamoDB prefix lists. IAM permits
`files/*` reads, result-JSON PUTs and required table state operations. Lambda
async retries are zero; EventBridge delivery has three retries, five-minute
age and an encrypted seven-day DLQ. Verify actual routes/endpoints separately.

The parent reserves state-update time; SDK connect/read timeouts are 2/4
seconds. Bucket-default encryption applies. Resource limits/environment
stripping do not prevent same-UID RCE or replace tenant authorization.
See [ADR-039](../../../docs/decisions/ADR-039-meeting-document-extraction.md)
for retained object versions and the absence of result expiry.

From this directory:

```bash
python3 -m pip install -r requirements-lambda.txt
python3 -m unittest -v
python3 -m pip check
```

Tests cover parser limits, owner/uploader separation, stale runs, source changes,
parent/attachment deletion, leases, conditional/ambiguous writes, retained and
partial results, deadlines and RFC3339 state. AWS interactions use synthetic
responses/Stubber; these checks do not prove deployed behavior.
