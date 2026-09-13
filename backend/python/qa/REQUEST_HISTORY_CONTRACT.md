# Live input and empty-search history

For handler registration and deployment acceptance status, see
[SOURCE_CONTRACT.md](SOURCE_CONTRACT.md).

Named binary searches add an optional normalized `sourceKeys` list to the
`emptySearch` value. Its fingerprint includes that selection, and replay checks
the same keys, query, count and user. Unscoped receipts keep their original shape;
older readers discard the extended shape safely. Selection validation and
visibility rules are defined in `manual_kb.selected_source_keys`.

`remember_client_input(state, user_id, meeting_id, text)` records an authenticated
request-input receipt containing the user, meeting scope and text digest. It
does not store another copy of the transcript and does not attest saved-source
bytes. Client live windows are historical user input, like earlier questions.
They may grow, move to a later tail window, or be corrected; the next model call
must receive the latest request input and give it priority over earlier live
input. This is necessary because LiveQAPanel's WebSocket context is a rolling
UTF-8 window, whereas the HTTP fallback can contain the full current transcript.

`current_input.request_user_message` adds separate server-generated metadata to
each user turn in sync REST, WebSocket and async execution. It records current
client-input presence, meeting scope, context kind and an opaque comparison digest,
without another raw-text copy or new logging. New receipts use schema version 2
and omit character counts. Persisted version 1 receipts remain readable, including
their validated legacy count field, without resetting or rewriting conversation
history. Guidance prohibits unsolicited hash/accounting narration and invented
input-size comparisons: changed digests do not establish equal or different lengths.
The system excerpt is rebuilt for the latest request; stored receipts describe
only their own user turn. A changed
digest can mean growth, a rolling window or a correction, not that an earlier
assistant answer was wrong. Unchanged input is explicit. Older sessions retain
their valid dialogue with `prior_unrecorded`; the helper never rewrites earlier
turns or interprets a receipt-shaped question as server metadata.

These prompt receipts neither authorize retrieval nor replace the dependency
receipts above. They are not saved-source verification. Missing current client
input cannot inherit presence from an old receipt, and all saved-source/grant
checks still apply. The input policy preserves conversation labels and prohibits
backdating a current draft to earlier input. It does not rewrite model output.
Local tests inspect real request construction using recorded synthetic payloads
and placeholder model replies; semantic improvement requires separate real
three-turn acceptance after reviewed deployment.

Receipts restore only for the same authenticated user and meeting scope. An
unscoped receipt requires an unscoped request. They never authorize a DynamoDB
or S3 read. All independently recorded server-source dependencies remain
mandatory: meeting/notes changes, deletion, grant revocation or file replacement
discard the entire old conversation, including assistant paraphrases. Receipt
handling does not classify `search_transcript` as a public tool.

`remember_empty_search(state, user_id, query, count)` is called only after an
actual successful empty search. During restore and each later model round,
`SourceAccess._source_is_current` reexecutes that query for the current user.
New matching sources or grants invalidate the old empty result; read failures
also invalidate it. Failed, skipped or untracked private reads receive no empty
proof. Counts use the same effective cap as SourceAccess, accept SDK Decimal
integers, and reject booleans/fractional values.

Empty-search tracking is bounded to eight dependencies and 4096 UTF-8 bytes per
query; the normal 128-dependency session budget also applies. Bookkeeping limits
set `replayable=False` and `toolHistoryCoverage`, retaining the current result.
The replay validator enforces the empty-search budget before provider calls.
The cumulative merge enforces the same budget across separate tool calls:
the ninth distinct empty result remains available, retains eight proofs, and
marks history nonreplayable with `DEPENDENCY_LIMIT`. Restore never partially
merges a history whose combined proofs exceed the budget.

`build_tool_context(..., source_access=..., history=..., create_research=...,
check_research_limit=..., check_web_search_limit=...)` holds the existing shared
callback wiring. It pins each private callback to the current user, collects
dependencies independently per call, records successful empty reads and keeps
successful research receipts. Both transports reset `sourceReadRecorded` before
each execution and call `track_tool_history` afterward.

Runtime wiring must pass the current request's `requestMeetingId` to
`SourceAccess._source_is_current(..., request_meeting_id=...)` during load and
validation. `clientInputReceived` is request-local; it is not restored from an
old session. Never drop a failed server-source check while retaining a client
receipt, and never replay a creation/mutation.

Validation: `cd backend/python/qa && python3 -m unittest test_handler -v`.
Tests use actual SDK serialization and current readers/tool executors with
synthetic storage/provider responses; no AWS/model calls or new framework.
