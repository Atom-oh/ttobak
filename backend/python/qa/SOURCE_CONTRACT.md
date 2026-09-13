# Current-source QA reader contract

Code checked: 2026-09-13. The handler registers these helpers in both transports.
Package deployment and public consumer acceptance are separate checks. This
activation revision selects canonical `all` delivery but remains held until the
[rollout](../../../docs/runbooks/qa-current-source-rollout.md) acceptance gate is
met. See the bootstrap runbook's dated readiness record; configuration or package
verification alone does not establish successful answers and current provenance.

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
| `source_tools.py` | Define/format document, attachment and legacy-text tools for authenticated consumers. |
| `session_provenance.py` | Recheck every dependency before replay; require an explicit replayable marker and known provenance version. |

Runtime integration must preserve the authenticated tool/error boundary and
expose only allowlisted public source fields. `BUCKET_NAME`, `KB_BUCKET_NAME`
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
Consumers must pass the current meeting scope, reset per-call coverage and use
the latest client input. At cumulative capacity, `tool_context` retains the
current read-time-authorized result, reports `DEPENDENCY_LIMIT`, and marks
history nonreplayable; it does not claim atomic freshness after that read.

From this directory, `python3 -m unittest test_handler -v` loads these suites.
Tests use synthetic table/S3 responses and real boto3 serialization without live
AWS/model calls; public REST/streaming acceptance remains separate.

[Manual bootstrap evidence](../../../docs/research/evaluations/2026-09-13-manual-kb-bootstrap/README.md)
records producer checks, not public QA deployment or model quality.

Source checks are point-in-time; model generation and stream delivery are not
atomic with later source changes. Client live input is not saved-source proof.

### Follow-up source details

Both transports retain server-produced provenance when a valid follow-up uses
history without new tool calls. `history_details.py` stores only allowlisted
metadata, never source bodies, in a separate `SESSION#{userId}#{sessionId}` /
`SOURCE_DETAILS` row. Its SHA-256 binding covers the user, session, exact serialized
messages, dependencies and detail payload. The existing message item does not
grow. Metadata has a 64-KiB JSON budget and seven-day `pendingShareExpiresAt`
retention, checked on read as well as using the table's active TTL attribute.

Restore first revalidates all source/grant/read-tool dependencies, checks the
binding and source identities, then revalidates before exposing history/details.
Changed or revoked sources discard both. Returned `provenanceScope:
validated_history` means attribution from still-valid conversation evidence,
not a new retrieval or a claim that every listed source supports the new answer.
No mutation or model call is replayed for attribution.

Explicit chat deletion removes the chat-list metadata, `MESSAGES` and
`SOURCE_DETAILS` in one DynamoDB transaction. Missing companion rows are harmless
for older sessions. This does not cancel an already-running QA invocation or add
a session tombstone; existing in-flight work can still finish afterward.

Older valid sessions and missing, expired, mismatched, unavailable or oversized
metadata retain their validated conversation. Details fall back to safe dependency
identities with `provenanceScope: legacy_identity`, without inventing excerpts,
page locations or current titles. Research creation receipts instead use
`history_receipt`; read-only tool dependencies provide identity-only attribution.
Identity-only scope remains explicit across later saves. Inventory-only attachment
receipts validate file additions/removals and are not content citations. Equivalent
fresh attribution replaces historical attribution without duplicate cards.
This does not make untracked legacy history
replayable. Attachment attempt/result/locations are typed and allowlisted; title
limits also apply to restored metadata. A restored-detail budget of 320 KiB
returns an explicit `legacy_identity_unavailable` / `DETAIL_LIMIT` marker if even
identity attribution exceeds it. That marker replaces the attribution list while
preserving valid dialogue. Existing final transport limits still apply.

## Completion and delivery

The streaming loop accumulates ConverseStream text from its first `contentBlockDelta`; text does
not require `contentBlockStart`, which the [AWS event contract](https://docs.aws.amazon.com/bedrock/latest/userguide/conversation-inference.html)
uses for tools. Empty, unterminated, token-truncated or tool-budget-exhausted
responses emit an explicit `MODEL_STREAM_*` / `MODEL_TOOL_ROUND_LIMIT` error and
cannot produce a successful completion. Completed, source-validated tool rounds
are retained with an explicit interruption note on these failures, so an empty
model message cannot erase a completed creation receipt. This includes exceptions
while consuming the model stream, which is closed on success or failure.
No model/tool retry is performed automatically. These changes apply to WebSocket
`ask_live`; the non-streaming Converse loop is unchanged. If current-source
validation fails, private tool context is not saved.
Terminal model errors include `sessionContinuable`: true when no new message
write was needed or the completed-tool history write was acknowledged, false
when that write was unconfirmed. Clients close the failed socket in either case.
Chat keeps the session only for recognized model-error codes with a literal true
flag, preserving prior dialogue and execution receipts. Unknown failures,
timeouts and disconnects still isolate a new session. Live QA retains a proactive
question's claim on terminal model failure rather than automatically repeating
potentially completed work; it does not mark the failed answer successful.

Both tool loops validate tracked sources after the final model call and before
session persistence. REST handlers and the WebSocket completion path validate
again before final publication. Rejected current-source proof returns HTTP 409
`SOURCE_CHANGED`; an exception during validation returns HTTP 503
`SOURCE_UNAVAILABLE`. WebSocket errors carry the same safe code in `answer_error`.
These checks do not recall deltas already streamed or make storage/delivery atomic.

Public source titles are presentation-limited to 256 Unicode characters, at most
1024 UTF-8 bytes, with `titleTruncated:true` when shortened. Source identity,
revision, source content and result arrays are retained. Free-text tool-input
logging hashes `uri` as well as search/topic/name fields.

Each JSON `PostToConnection` payload uses a conservative 30,000-byte application
budget. AWS documents a [32-KB frame quota and 128-KB message quota](https://docs.aws.amazon.com/apigateway/latest/developerguide/apigateway-execution-service-websocket-limits-table.html);
messages above the frame quota require fragmentation, including `@connections`.
The [SDK operation](https://boto3.amazonaws.com/v1/documentation/api/latest/reference/services/apigatewaymanagementapi/client/post_to_connection.html)
takes bytes and a connection ID and exposes `PayloadTooLargeException`; it does
not expose a frame-fragmentation option. `ws_source_frames.py` therefore keeps
small completions unchanged and moves large attribution into separate complete
JSON `answer_sources` messages. Each carries `sourceBatchId`, zero-based
`sourceBatchIndex`, `sessionId`, `sources` and `sourceDetails`; the final
`answer_complete` carries the same batch ID and `sourceBatchCount` instead of
duplicating those arrays. Up to 64 attribution frames and 512 KiB of attribution
are allowed. All frames are built and size-checked before the first write.
The frontend `QASourceFrames` assembler validates sequence, session, batch/count
and aggregate size, and exposes the answer only with the complete original arrays.
Missing, duplicate, mixed-session or oversized attribution produces an explicit
error and closes that socket before the UI can submit another question, detaching
its message callback so late frames cannot fail the next request.
This is application framing, not silent source truncation; direct
WebSocket clients opt in with `sourceFramesVersion:1` on `ask_live`; the Go
relay forwards only that supported version. Older or unknown-version clients
retain the existing explicit size error instead of receiving incomplete arrays.

Oversized answer text, individual sources or aggregate attribution retain their
source arrays and produce a small
`answer_error` with `RESPONSE_TOO_LARGE`, never a truncated successful result.
SDK payload rejection takes the same path. Source-frame delivery failures are
terminal and cannot be followed by a successful completion. Other terminal delivery failures use
`DELIVERY_FAILED`; the async handler returns `delivery_failed` and logs a safe code.
An error-notification failure is not retried recursively or reported as `ok`.
Confirmed Gone returns `gone`; if the error cannot reach a disconnected or
unavailable client, that client may still time out. Ordinary transient delta or
heartbeat failures retain the existing best-effort behavior; completion must
actually be accepted by the Management API before the handler returns `ok`.

## Named binary KB sources

The model tool `search_knowledge_base` accepts optional `source_keys`: one to
five exact original binary keys, at most 4096 UTF-8 bytes combined, under the
current user's `kb/{userId}/` prefix or authenticated-global `shared/`.
`numberOfResults` must cover the selected sources. Invalid, foreign, empty or
malformed selections are rejected before source/provider reads.

Selection is an identity lookup, not a semantic filename query. Each original
is checked afresh, then only its exact resource/revision snapshot is retrieved.
The ordinary semantic search threshold remains 0.5; it does not discard an
explicitly selected, currently verified file because its name has a low score.
Missing files yield scoped empty-search proof; unindexed/unsupported files
retain explicit pending/failure metadata, never old unbound content. File text
remains a partial excerpt. Legacy text files use `get_legacy_text_detail`.

Empty-search receipts preserve and revalidate the same normalized selection.
Existing unscoped receipts remain compatible; older readers reject the new
scoped receipt shape rather than replaying it as a global search. Selection
keys are redacted in tool-input logs. No public route, IAM grant or source
visibility rule changes.
