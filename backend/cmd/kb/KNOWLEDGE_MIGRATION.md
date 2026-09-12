# Existing KB binary migration

Extends the PR205 coordinator; no second ingestion mechanism or canonical
document rows are created. Original `kb/{owner}/{filename}` and `shared/**`
objects, upload/list/delete routes, and visibility semantics stay unchanged.

## Discovery and lifecycle

Each scheduled tick visits one bounded S3 page from `kb/` and `shared/`, saving
`manualCursor` and `sharedCursor` beside existing coordinator cursors. Empty or
filtered pages retain their continuation tokens. Metadata sidecars and plain
text are excluded from binary migration; QA already hydrates legacy text.

PDF, DOC, DOCX, XLS and XLSX bytes are copied to new immutable objects.
PPT/PPTX are recorded as `FAILED / UNSUPPORTED_FILE`, because the existing
default KB parser does not index these formats. Empty and oversized sources
also have explicit durable failures. Originals are never removed or rewritten.

Jobs share `KBINDEX#JOBS`, its four-entry rotation, revision coalescing, version/
run/lease conditions, and exponential failure cooldown (one to thirty minutes).
Private object identities use `USER#{owner} / KBFILE#{resourceId}`; shared
identities use `KB#SHARED / KBFILE#{resourceId}`. These are job identities only,
not canonical DynamoDB source records. `resource.sourceKey` retains the exact
original S3 key, so known jobs detect deletion even after catalog enumeration
can no longer find the source.

Read the original with `IfMatch` plus `VersionId` when supplied by S3; verify
actual bytes/ETag/version/content type, then recheck the current original.
Persist planned output keys before any PUT. Metadata and data use
`IfNoneMatch="*"` and no SDK retries. Uncertain uploads retain the coordinator
lease so late writes cannot race successor cleanup. All old/partial objects
under the resource's snapshot prefix are removed before its full sync.
The merged PR205 changing-source isolation applies to both S3 source classes:
definitive source changes clean up and back off without blocking stable batch
members. An uncertain-write marker takes precedence over any wrapped changed
or condition-failed cause, retaining the freeze.

The same frozen batch/client token drives `StartIngestionJob`, including manual
or crawler conflicts and lost responses. INDEXED/DELETED requires successful
terminal ingestion, a fresh original-source revision, matching inventory, and
the job CAS. S3-only sources deliberately do not use a fabricated missing
DynamoDB row as source authority. As with existing S3-backed canonical files,
S3 and DynamoDB are not one transaction; QA must recheck original bindings
before using retrieved chunks, and status reads revalidate freshness.

## Private snapshot contract

`manual-kb/v1/{ownerId}/{resourceId}/{sourceRevision}/{indexRunId}/document.{ext}`

Adjacent `.metadata.json` contains `metadataAttributes`:

- `indexSchema: manual-kb-v1`
- `resourceKind: manualKbDocument`
- `ownerId`, `resourceId`, `sourceRevision`, `indexRunId`
- `sourceBucket`, `sourceKey`, `sourceETag`, `sourceVersionId`, `sourceSize`

This matches the QA `MANUAL_KB_COMPATIBILITY.md` contract exactly. `resourceId`
is lowercase SHA-256 of the exact UTF-8 original key. Revision is lowercase
SHA-256 over UTF-8 strings framed as decimal byte length, `:`, then bytes:
schema, configured KB bucket, source key, exact ETag, version ID (or empty),
decimal source size. No filename, URI or key normalization is performed.

## Shared snapshot contract

`shared-kb/v1/{resourceId}/{sourceRevision}/{indexRunId}/document.{ext}`

Shared snapshots use the same revision framing and common source attributes,
with `indexSchema: shared-kb-v1`, `resourceKind: sharedKbDocument` and
`visibility: authenticated-shared`. They never carry a fabricated `ownerId`.
The source must be under `shared/`; a `kb/{owner}/...` key cannot be relabeled
shared. This preserves the baseline QA filter's authenticated access to the
existing `shared/` dataset. It does not create public S3 access, anonymous
routes, or account-membership inheritance.

QA needs a corresponding shared-schema consumer with the same current-byte
checks. Keep the unified QA consumer held until both schemas are supported.
Private/shared golden revision vectors are in
`internal/service/testdata/knowledge-revisions.json`.

## Runtime/IAM handoff

No new environment variables: use the existing TABLE_NAME, BUCKET_NAME,
KB_BUCKET_NAME, KB_ID and DATA_SOURCE_ID. No resource IDs are hardcoded.

On the configured KB bucket, extend the worker's permissions with:

- `s3:GetObject` and `s3:GetObjectVersion` for `kb/*` and `shared/*`.
- `s3:ListBucket` on the exact configured KB bucket with a ResourceAccount
  condition. Effective ListBucket permission is needed for HEAD to distinguish
  absence from AccessDenied; a prefix-only condition may not apply to HEAD.
  Runtime listing methods still allow only the defined source/catalog and
  snapshot prefixes.
- `s3:PutObject` and `s3:DeleteObject` only for the two new snapshot prefixes.

No writes/deletes to original source prefixes are required. Existing table and
Bedrock Start/Get/ListIngestionJob permissions suffice. The existing scheduled
tick recovers old uploads, overwrites, deletions and missed events; no new
producer notification is required.

Tests are synthetic and do not establish deployed retrieval quality. Verify
the full Go suite, vet, ARM64 worker build, and deployment ordering before
releasing the held QA consumer.
