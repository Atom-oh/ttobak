# Attachment text Lambda parent

Install `requirements-lambda.txt` and use `handler.lambda_handler` on Python 3.12.
Only `BUCKET_NAME` and `TABLE_NAME` are required application configuration.
AWS region/credentials come from the execution environment. Clients initialize
lazily and are reused; importing parser modules never initializes AWS.
The child receives no parent environment or credentials.

The accepted event is `source=ttobak.upload`, `detail-type=DocumentUploadCompleted`,
with detail `{bucket,key,meetingId,ownerId,userId,attachmentId,runId}`. `userId`
is the uploader; `ownerId` identifies the meeting owner and need not equal it.
The configured bucket must match. Event `key` is ignored as source authority;
the queued row and canonical attachment must agree on the exact key.

Implementation sequence:

1. Strong metadata-only reads of `MEETING#mid / ATTEXT#attachmentId`,
   `USER#owner / MEETING#mid`, and **`MEETING#mid / ATTACH#attachmentId`**.
   Match the queued run, owner/uploader, active numeric lease, canonical
   attachment ID/meeting/uploader/originalKey, and parent meeting ID/owner.
   The source must be exactly `files/uploader/mid/basename`. No large meeting
   fields, transcript hydration, query, scan, or model calls are needed.
2. Atomically claim `queued → running`: a state CAS and parent/attachment
   condition checks. Conditions bind run, status, lease, source key and identity.
   A duplicate/stale run does not parse. Running leases are capped at 120 seconds
   and the invocation's remaining duration.
3. `HeadObject` checks actual size (20 MiB ceiling). `GetObject(IfMatch=ETag)`
   reads chunks under an actual-byte/deadline bound and closes the body on every
   path. Format comes from the canonical key extension, never filename-only
   extraction. Run the existing child with a reduced wall deadline and 4096 bytes
   reserved in its JSON limit for source provenance.
4. Recheck source size/ETag; write at most 1 MiB including newline to
   `files/uploader/mid/text/attachmentId/runId.json` using `IfNoneMatch="*"`.
   The JSON retains the parser schema and adds `source` with bucket, key, eTag,
   meetingId, ownerId, uploaderId, attachmentId and runId. Recheck ETag again
   before publication.
5. A terminal DynamoDB transaction checks parent + canonical attachment again
   and updates state only for this still-running, active lease/run/source.
   Success/partial sets `sourceETag,resultKey,unitCount,complete,status,errorCode`,
   clears the lease, and sets `updatedAt` as a UTC **RFC3339 string** (Go
   `time.Time` compatible). Partial results use `PARTIAL_EXTRACTION`.

Failure updates set **only** `status,errorCode,leaseUntil,updatedAt` and condition
on the expected run/status/lease/source/identities and row existence. Prior
`resultKey,sourceETag,unitCount,complete` remain untouched. Thus `complete=true`
on a failed current attempt can describe the retained earlier result, never
successful completion of the current run. Its JSON `source.runId` identifies it.
Parser failures retain their fixed parser codes. Parent failures use
`SOURCE_UNAVAILABLE`, `SOURCE_CHANGED`, `INVALID_SOURCE`, `SOURCE_TOO_LARGE`,
`RESULT_WRITE_FAILED`, `WORKER_FAILED`, or `TIMEOUT`. No raw text/exception logs.

DynamoDB read/write failures raise fixed `STATE_READ_FAILED` /
`STATE_WRITE_FAILED`; they are not reported as persisted extraction failure.
SDK retries are disabled (one attempt) so a write that succeeded before a lost
response cannot be retried and then misinterpreted as a definite CAS rejection.
No result is deleted on conditional/ambiguous completion. A duplicate delivery
of a running job is ignored; the host's expired-lease status/retry path must
replace interrupted jobs with a fresh run ID. Run IDs must never be reused.

The invocation deadline bounds parsing/IO initiation and leaves time for state
updates; SDK connect/read timeouts are 2/4 seconds. Deploy with enough parent+
child memory and time (at least 90 seconds recommended for these bounds).
Bucket-default encryption applies to result PUTs. Deployment owns private
networking, bucket encryption and narrowly scoped S3 read/write + DynamoDB
GetItem/UpdateItem/ConditionCheckItem permissions for transactional state writes. This module creates no resources.

S3 and DynamoDB cannot share a transaction: an overwrite after the final HEAD
can still race publication. The result always records the ETag it parsed.
Consumers requiring current-source freshness must revalidate it or enforce
immutable upload keys; a source overwrite needs a fresh queued run. Orphaned
immutable result objects may remain after deletion, lost leases, or uncertain
commits; lifecycle cleanup must not delete a result still referenced by state.
No `ExtractionCompleted` event is emitted; stream/index wiring belongs to the host.


## AWS primary references

- https://boto3.amazonaws.com/v1/documentation/api/latest/reference/services/s3/client/get_object.html
- https://boto3.amazonaws.com/v1/documentation/api/latest/reference/services/s3/client/put_object.html
- https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_TransactWriteItems.html

## Local verification and PR split

```bash
python -m pip install -r requirements-lambda.txt
python -m unittest -v
python -m pip check
```

All 49 tests pass on CPython 3.12.13 Linux/AArch64 (28 parser/child tests and
21 parent tests). The hyperlink rejection regression was observed before the
fix. Initial handler tests failed before implementation. Parent tests cover
owner/uploader separation, stale/duplicate delivery, source changes, deleted
parents/attachments, lease/run guards, conditional and ambiguous writes,
retained results, bounded streaming/output, partial results and deadlines.
A real child parse runs through botocore Stubber request validation. RFC3339
output also passed Go's actual DynamoDB `attributevalue.UnmarshalMap` into
`time.Time`. AWS calls are mocked; no customer data or deployed resources.

The parser foundation is a prerequisite. This worker/infra slice packages the parent and runtime dependencies and creates its internal event target. API queueing, summary/Q&A and UI consumers remain separate integration work.
