# Live input and empty-search history

This inactive helper extends the source-history foundation. The public handler
wiring is in PR221; this prerequisite does not activate the strict QA consumer.

`remember_client_input(state, user_id, meeting_id, text)` records an authenticated
request-input receipt containing the user, meeting scope and text digest. It
does not store another copy of the transcript and does not attest saved-source
bytes. Client live windows are historical user input, like earlier questions.
They may grow, move to a later tail window, or be corrected; the next model call
must receive the latest request input and give it priority over earlier live
input. This is necessary because LiveQAPanel's WebSocket context is a rolling
UTF-8 window, whereas the HTTP fallback can contain the full current transcript.

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
