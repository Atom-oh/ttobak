# Current-source QA reader contract

The prepared REST/WebSocket handler uses these current-source readers and history
checks. Code readiness is not deployed acceptance; follow
[the rollout](../../../docs/runbooks/qa-current-source-rollout.md) before cutover.

## Reader responsibilities

| Module | Contract |
|---|---|
| `document_context.py` | Authorize personal, direct-share and exact account documents before retaining contents. |
| `source_revision.py` | Bind canonical identity, exact present fields and S3 metadata; missing/null/empty stay distinct. Go/QA fixtures pin revisions. |
| `source_context.py` | Fresh metadata authorization before bounded S3 reads pinned to ETag/version. |
| `attachment_context.py` | Authorize ATTACH/ATTEXT; verify immutable result identity, original ETag, limits and continuation. Partial/retained results stay explicit, without audio timestamps. |
| `indexed_retrieval.py` | Enumerate authorized identities, use current saved text, and require canonical revision/S3 bindings for binary excerpts. Legacy meeting exports supply identities only. |
| `manual_kb.py` | Validate private/shared binary snapshots and current originals; unbound old chunks are not evidence. Missing snapshots are pending; read errors propagate. |
| `source_access.py` | Compose injected readers/callbacks into source contexts/search with dependencies; create no AWS clients. |
| `source_tools.py` | Define/format document, attachment and legacy-text tools for both transports. |
| `session_provenance.py` | Recheck every dependency before replay; require an explicit replayable marker and known provenance version. |

The runtime preserves the authenticated tool/error boundary and exposes only
allowlisted public source fields. `BUCKET_NAME`, `KB_BUCKET_NAME`
and source-read permissions must match the worker. Private sources remain
owner-only; `shared/` is authenticated-global, not a private tenant partition.
Never move private uploads there. Snapshot availability cannot replace current
authorization, and a pending-only consumer cannot replace existing binary recall.

## Candidate selection and current legacy text

Duplicate canonical/manual identities keep their highest provider score.
Verified/current text precedes metadata-only pending results. With a limit of
at least two, reserve one slot for a saved-body match when available, retaining
up to `limit - 1` semantic text results; remaining slots may hold more saved
matches. Limit one preserves available semantic body evidence. A title-only
match never displaces body evidence.

Saved fallback matches have score zero. They require all of the first 20
whitespace-separated, case-folded query terms across saved title/notes/content/
action items; the reserved body slot excludes title. This literal fallback
covers some ingestion delay, not general natural-language recall.

Legacy `.md`, `.txt`, `.html` and `.csv` under `kb/{caller}/` or authenticated
`shared/` are read from current scoped objects. Meeting exports never use this
path. Reads cap source size at 50 MiB, verify ETag/version before and after,
and retain at most 6,000 characters with partial coverage explicit.

Provider excerpts are usable only when their exact text occurs in current
bytes; duplicate URI hits are checked by descending relevance. Otherwise use
a current literal-query excerpt with the mismatch explicit. `coverage` gives
source-relative offsets and total length, retaining matches beyond the file's
introduction. Search formatting caps the excerpt at 2,400 characters and
recomputes coverage/provenance from what the model actually receives.

`get_legacy_text_detail(uri, offset=0, sourceRevision?)` returns up to 6,000
current characters. Continuations require the prior revision; changes require
restart. `nextOffset` exists only when text remains, and an offset above zero
is partial even on the final page. The tool never reads binary or
foreign-private files.

## Immutable binary snapshots

Supported original formats are PDF, DOC, DOCX, XLS and XLSX. PPT/PPTX have an
explicit unsupported state until a suitable conversion path exists. Private
original keys are flat `kb/{owner}/{filename}`; shared keys may nest beneath
`shared/`. Snapshots use lowercase extensions:

- `manual-kb/v1/{owner}/{resourceId}/{revision}/{run}/document{ext}`
- `shared-kb/v1/{resourceId}/{revision}/{run}/document{ext}`

`resourceId` is SHA-256 of the exact UTF-8 original key. `revision` hashes
byte-length-prefixed UTF-8 values in order: schema, bucket, key, exact ETag,
version ID (empty if absent), decimal source size. `run` matches the UUID
validator `source_revision.RUN_ID`. Fixtures are
`testdata/knowledge-revisions.json` and `manual-kb-revisions.json`.

Required metadata: `indexSchema`, `resourceKind`, `resourceId`, `sourceRevision`,
`indexRunId`, `sourceBucket`, `sourceKey`, `sourceETag`, `sourceVersionId` and
numeric `sourceSize`. Private schema/kind are `manual-kb-v1`/`manualKbDocument`
with `ownerId`; shared uses `shared-kb-v1`/`sharedKbDocument` with
`visibility=authenticated-shared` and forbids `ownerId`. URI, metadata and
current original must agree. Adding fresh metadata to old chunks is insufficient.

File states are `pending` (`VERIFIED_SNAPSHOT_UNAVAILABLE`), `ready`, `empty`
(`EMPTY_FILE`) and `failed`, including `UNSUPPORTED_FILE` or
`SOURCE_TOO_LARGE` above 50 MiB. Ready excerpts are still partial and capped at
6,000 characters before tool formatting.

Verify the external S3 data source includes `canonical/v1/`, `manual-kb/v1/`
and `shared-kb/v1/`. Metadata must support filters on `sourcePK`, `sourceSK`,
`indexSchema`, `ownerId`, `visibility`, `resourceId` and `sourceRevision`.
Checked-in configuration is not proof of the live source or successful ingestion.

## Session proof and verification

Dependencies identify canonical PK/SK plus revision, `legacyURI`, `manualKey`
or `sharedKey`. Attachments additionally bind an ID; `*` denotes the separately
hashed inventory. Revalidate each source and authorization before replay and
subsequent model use. Saved-text results without index objects use
`ttobak://source/{resourceHash}`.

Provenance version 1 accepts exact integer/SDK Decimal representations, rejecting
bools, floats, strings, unknown versions and untracked histories. Changed or
unavailable dependencies invalidate the entire history, including assistant
paraphrases. Read-tool proof and mutation receipts follow
[TOOL_HISTORY_CONTRACT.md](TOOL_HISTORY_CONTRACT.md). User/meeting-bound live input
and reexecuted empty searches follow [REQUEST_HISTORY_CONTRACT.md](REQUEST_HISTORY_CONTRACT.md).
Both transports pass the current meeting scope, reset per-call source coverage,
and use the latest client input. Capacity limits keep valid current results,
report `toolHistoryCoverage`, and prevent replay of untracked results.

From this directory, `python3 -m unittest test_handler -v` loads these suites.
Tests use synthetic table/S3 responses and real boto3 serialization without live
AWS/model calls; public REST/streaming acceptance remains separate.
