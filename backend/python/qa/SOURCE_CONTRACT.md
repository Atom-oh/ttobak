# Current-source reader foundation

The REST and WebSocket QA handlers use these modules for current-source reads,
retrieval, document tools and source-bound conversation history.

- `document_context.py` authorizes personal/direct-share/exact account document
  reads before retaining source contents.
- `source_revision.py` pins canonical identity and hashes the exact present
  fields plus S3 bindings. Missing and null remain distinct; test vectors match
  the Go worker when its fixture is present.
- `source_context.py` performs fresh metadata authorization before S3, validates
  source identities, and bounds reads pinned to ETag/version.
- `attachment_context.py` reads authorized `ATTACH#`/`ATTEXT#` records and verifies
  immutable result identity, original ETag, byte limits and continuation.
  Partial/retained results remain explicit and never get audio timestamps.
- `indexed_retrieval.py` discovers all authorized identity pages, consumes
  current saved text, and accepts indexed file excerpts only with the current
  canonical revision and S3 bindings. Legacy meeting exports supply identities,
  never meeting text. Saved keyword matches cover ingestion delay.
- `manual_kb.py` verifies private `manual-kb-v1` and authenticated-shared
  `shared-kb-v1` snapshots. Original `kb/{owner}/...` and `shared/**` binary
  chunks need a new immutable copy; adding current metadata to an old URI is
  insufficient. Missing snapshots return explicit pending status. Source
  read failures propagate instead of returning empty success.
- `session_provenance.py` keeps source dependencies beside model messages.
  Restore requires current authorization/revisions for every dependency and
  an explicit replayable marker. Both integer version 1 before persistence and
  Decimal version 1 after a boto3 resource read are accepted; booleans, floats,
  strings, unknown versions and untracked histories are rejected.
- `source_access.py` composes these readers into current-source search and
  meeting/document/attachment contexts, with source dependencies attached.
  Its constructor receives readers and callbacks; it creates no AWS clients.
- `source_tools.py` defines and formats the three document/attachment tools.
  Both authenticated tool loops register these definitions and preserve the
  existing tool-error boundary.

The existing handler-suite command also loads these contract suites:

```bash
python3 -m unittest test_handler -v
```

Tests use synthetic table/S3 responses, including the real boto3 attribute
serializer/deserializer, without live AWS/model calls. The helper tests are
loaded by the same command together with the REST/WebSocket integration tests.

The runtime requires the separately deployed source-read IAM and
`KB_BUCKET_NAME`. Public details are constructed from explicit source fields.
Private/shared binary snapshots must be produced before adopting
the strict consumer; pending-only migration is not a substitute for existing
file answerability. Documented snapshot support is PDF, DOC, DOCX, XLS and XLSX.
PPT/PPTX remain visible with an unsupported-file state until a supported
conversion path exists. Producer deployment, actual recall and session
revalidation through the public endpoints remain separate acceptance work.

## Candidate selection and legacy text

Duplicate canonical/manual identities retain their highest provider score.
Verified or current text precedes metadata-only pending results. For limits
of two or more, literal saved body matches reserve at least one result slot
while retaining up to `limit - 1` semantic text results; remaining slots can
hold additional saved matches. A limit of one preserves an available semantic
body result. The reserved candidate must match all query terms in
notes/content/action items, excluding the title. A title-only match, including
one with unrelated nonempty content, never displaces available body evidence.
These fallback matches have score zero, not fabricated provider confidence.
They require all of the first 20 whitespace-separated, case-folded query terms
to occur in the saved title/notes/content/action items; this is not a Korean
natural-language recall guarantee. The semantic index remains necessary.

Legacy text under `kb/{caller}/` or authenticated-global `shared/`, with
`.md`, `.txt`, `.html` or `.csv` suffix, is read from the current scoped object.
Meeting exports never enter that path. Reads verify ETag/version before and
after consumption; results retain at most 6,000 characters and indicate
partial coverage. Source reads are capped at 50 MiB. `shared/` is an existing
authenticated-global namespace for shared ingestion, not a tenant-private
partition; a producer must never move private uploads there.

## Binary snapshot producer contract

Private original keys are flat `kb/{owner}/{filename}` keys. Shared original
keys may be nested below `shared/`. Binary snapshots use lowercase original
extensions:

- `manual-kb/v1/{owner}/{resourceId}/{revision}/{run}/document{ext}`
- `shared-kb/v1/{resourceId}/{revision}/{run}/document{ext}`

`resourceId` is SHA-256 of the exact UTF-8 original key. `revision` hashes
UTF-8 byte-length-prefixed values in this order: schema, source bucket, source
key, exact ETag, version ID (empty if absent), decimal source size.
`testdata/knowledge-revisions.json` and `manual-kb-revisions.json` contain
producer vectors. A run is the UUID checked by `source_revision.RUN_ID`.

Required metadata is `indexSchema`, `resourceKind`, `resourceId`,
`sourceRevision`, `indexRunId`, `sourceBucket`, `sourceKey`, `sourceETag`,
`sourceVersionId`, and numeric `sourceSize`. Private snapshots also require
`ownerId`; shared snapshots require `visibility: authenticated-shared` and
forbid `ownerId`. URI, metadata and the current original must agree.

File states are `pending`, `ready`, `empty`, and `failed`. Pending means
`VERIFIED_SNAPSHOT_UNAVAILABLE`; empty means `EMPTY_FILE`; failures include
`UNSUPPORTED_FILE` (PPT/PPTX) and `SOURCE_TOO_LARGE` (over 50 MiB). A ready
excerpt is still partial, limited to 6,000 characters before tool formatting.
Old unbound binary chunks are not evidence.

The S3 data source must include `canonical/v1/`, `manual-kb/v1/` and
`shared-kb/v1/`; its current whole-bucket configuration already does.
Sidecar metadata must be filterable for `sourcePK`, `sourceSK`, `indexSchema`,
`ownerId`, `visibility`, `resourceId` and `sourceRevision`.

Session dependencies use canonical PK/SK with a source revision, `legacyURI`,
`manualKey`, or `sharedKey`; attachment dependencies additionally bind an
attachment ID (`*` means the separately hashed inventory). Every form is
revalidated against its current authorization and source before replay.
Saved-text results without an index object use
`ttobak://source/{resourceHash}` as their identity URI.
