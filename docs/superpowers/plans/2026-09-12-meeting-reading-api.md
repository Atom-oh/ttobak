# Bounded meeting reading API

**Goal:** Make long meetings readable before the buffered Lambda response limit,
including notes-only reads that perform no transcript S3 hydration.

**Contract:** Authenticated `GET /api/meetings/{meetingId}/reading`.
Queries are `kind=meeting|transcript` (default meeting), `pageSize` (default 4000,
1–8000 Unicode code points), and optional opaque `cursor` (at most 2048 characters).
Meeting kind accepts `section=notes|summary|actionItems` (default notes).
Transcript kind accepts `source=selected|A|B` (default selected), and paired finite
`startTime`/`endTime` with `0 <= startTime < endTime`. Reject unknown/repeated
query keys and invalid combinations before storage reads.

Return the inner JSON object currently produced by `meetingReading` or
`transcriptReading` in the `work/note-mcp-reading` prototype. Preserve provenance,
preview truncation flags, action analysis state (unknown for legacy absence),
`page` offsets/completeness/continuation, and exact source text. Cursors are opaque
to clients; bind them to meeting/kind/section or source/time range and content
revision. Reject changed revisions instead of joining incompatible pages.

Limit serialized API JSON to 14,000 bytes including its trailing newline. This
leaves room for the MCP JSON string wrapper and JSON-RPC envelope below 32,000
bytes. Cap chunks at 50 and metadata/preview lengths as in the prototype.
Return fewer than `pageSize` points when needed to fit; never split UTF-8/surrogate
sequences or silently omit text. Full action-item JSON is independently paged.

Reuse `MeetingService.checkAccess`, current source selection, and verified
segment policy. Use a read-only repository view that skips transcript hydration
for authorization/notes, without changing existing default readers. For
transcripts, hydrate only the chosen text and candidate segments after access
is established. Never expose stored S3 references. Provide time ranges only when
the entire candidate transcript matches and exact raw-text windows can be
established; otherwise expose text pages without invented timings.

Use the existing action-analysis description path for its small metadata row,
without calling its full meeting response method (which hydrates transcripts).
Recheck ownership/sharing/account membership on every continuation.

## Implementation and validation

- [x] Add repository metadata view, service pagination and authenticated handler.
- [x] Wire the route into the existing API; no new public route or IAM grant.
- [x] Test original oversized meeting: 4 MiB A + 3 MiB B with tiny notes.
- [x] Prove notes-only requests make zero S3 calls and API Gateway v1 responses remain bounded.
- [x] Test long Korean/emoji reconstruction, segment windows, unchanged and stale cursors.
- [x] Test unauthorized/revoked shares, wrong source and malformed/repeated options.
- [x] Preserve complete action JSON and distinguish unknown/failed/empty success.
- [x] Run Go tests/vet/ARM64 build; document the API contract.
- [ ] Point MCP tools at this endpoint, bound HTTP reading responses, and remove production client-side paging.
- [ ] Test actual MCP protocol routing/continuations/errors and standalone bundle parity.

Publish the API after the action-analysis prerequisites have merged, then publish
the MCP adapter. Latest-head AI review, CI and deployment evidence remain required.

## Backend evidence (2026-09-12)

The SDK-backed handler test first reproduced a 7,340,309-byte API Gateway v1
body using the old detail handler. The new endpoint passes the same 4 MiB A +
3 MiB B fixture with no notes-time S3 reads and a body <=14,000 bytes including
newline. Tests also bound the serialized API Gateway and MCP envelopes.

The metadata view leaves the original repository's hydration flag unchanged.
GSI-discovered metadata is reread consistently from the primary key before access
decisions. Action status uses `Describe`, followed by fresh metadata/access reads
so a newly succeeded state cannot be paired with older items. Stored-item
normalization matches the existing action-items endpoint.

`go test ./... -count=1` and `go vet ./...` pass. Linux/ARM64 builds of `cmd/api`
and `cmd/summarize` with `-tags lambda.norpc` pass. Runs used the requested Go
binary/cache/tmp settings. Coverage includes revoked direct/account grants,
stale GSI rows, forbidden S3 references, broken S3 with readable notes, malformed
cursor syntax before storage reads, Unicode/huge-segment paging, time windows,
and complete action JSON with preserved completion and lifecycle status.

No contract change was needed. The API's smaller byte budget can shorten the
action preview; its complete JSON remains independently paged. The MCP steps
above belong to the separate adapter task and are not marked verified here.
No commit, push, infrastructure change, or deployment was performed.

Follow-up: API and summarize action-lifecycle construction now share a metadata
factory for repository reads and MeetingService authorization, retaining real
conditional writes. A processing guard independently rejects blank summaries
before the real extractor can fall back to a stored transcript reference.
SDK-backed regression tests cover polling, retry, processing, completion writes,
and the inline worker with zero S3 reads.

Full action JSON uses object maps preserving unknown/nested stored properties;
only legacy ID/completion fields are normalized. Preview omission flags and
extension-only cursor invalidation are covered by end-to-end reconstruction.
The host's frontend polling-timer change is outside this implementation's scope
and remains untouched.
