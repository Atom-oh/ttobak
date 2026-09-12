# MCP bounded meeting reading adapter

Goal: let external agents read saved notes first and continue long transcripts
without fetching an oversized full meeting through buffered Lambda responses.

Scope: MCP source, tests, documentation and the reproducibly built public bundle.
The backend reading API is implemented separately under
`docs/superpowers/plans/2026-09-12-meeting-reading-api.md` and was deployed before this adapter release.

## Shared API and tool contract

- Preserve `ttobak_get_meeting(meetingId, section?, cursor?, pageSize?)` with
  default `section=notes`; summary and action-item JSON remain explicit sections.
- Preserve `ttobak_read_transcript(meetingId, source?, cursor?, pageSize?,
  startTime?, endTime?)` with default `source=selected`.
- Both tools call only authenticated `GET /api/meetings/{id}/reading` with
  `kind=meeting|transcript`, `pageSize`, optional opaque `cursor`, and applicable
  `section` or `source/startTime/endTime` queries. Missing routes and API errors
  are tool errors; never fall back to the full meeting endpoint.
- Validate IDs, option names/types, page sizes 1–8000 (default 4000), nonempty
  cursors up to 2048 characters, and paired finite time bounds before HTTP.
  URL-encode query values without interpreting cursor contents.
- The API returns the inner JSON previously carried in MCP `content[0].text`.
  Preserve its source revision, provenance, offsets, text, action-item preview,
  analysis status, and continuation metadata. Absent analysis metadata remains
  `unknown`; empty items never imply successful extraction.
- The backend owns source selection, Unicode/time windows, segment verification,
  pagination, byte-aware page sizing, and cursor/revision validation. The MCP
  adapter performs no source hashing, cursor decoding/encoding, or text slicing.
- The API contract caps its JSON at 14,000 bytes including the trailing newline.
  Independently cap HTTP reading responses at 32,000 original bytes before
  buffering/JSON parsing; reject oversized Content-Length or streaming bodies,
  destroy response/request streams, and never return partial oversized data.
- Validate response identity and basic page shape, then wrap the JSON as MCP
  text content. Reject a final serialized tool result above 32,000 bytes instead
  of truncating a server page or inventing a continuation.
- Keep API failures, interrupted responses, stale cursors, revoked access and
  malformed responses explicit. Every continuation performs a fresh API request.

## Regression coverage

Use real stdio MCP clients with synthetic credential/HTTPS boundaries. Fixtures
are fixed backend page snapshots, not a second pagination implementation.

- Exact new route and default/explicit query values; no full-meeting fallback.
- Opaque cursor forwarding, server-issued revisions, source/time-range queries,
  and unchanged metadata/provenance, including partial segment timing.
- Notes, summary, action JSON fragments, completion flags, and unknown/failed/
  succeeded analysis semantics.
- Small notes requests where the underlying raw transcript variants exceed 6 MiB.
- Fresh 401/403/404 denials and server range/cursor errors on continuation.
- Header/stream/UTF-8 byte-limit aborts before parsing; no oversized text forwarded.
- Final JSON-wrapper expansion limit; interrupted stream errors without killing
  the MCP session; malformed arguments rejected before an API call.

Run the full MCP suite, the same adapter protocol tests against the standalone
bundle, and two byte-identical bundle builds. Backend tests separately prove
actual source pagination and bounded API Gateway v1 responses.

## Delivery order

Merge and deploy the bounded API after its prerequisites, then release this MCP
adapter. This PR includes the reviewed reproducible bundle at
`frontend/public/mcp/ttobak-mcp.mjs`; `test-mcp.yml` verifies it matches the source
build with `cmp`. No IAM change is needed.

## Verification

The adapted protocol tests first reproduced the old full-meeting requests
(10 failures, 2 passes). The first adapter run passed all 12 protocol tests.
Final `npm test` passed: **38 compiled-module/protocol tests**, then **12 standalone-bundle protocol tests**.
TypeScript compilation passed; two consecutive esbuild outputs were byte-identical.
Bundle SHA-256: `e5d46fe38019b6dd881110ac30a8efdfdd24589ee1b42f4a3ee7c193052fd4bb` (766711 bytes).
The full-suite rerun also verifies explicit fixture-trace synchronization; tests do not assume
that child stderr arrives before the MCP stdout response.
Protocol-test HTTP transports and credentials were synthetic. The public bundle
was copied and compared byte-for-byte, and the frontend static build passed.
The prerequisite API deployment succeeded; adapter deployment remains a post-merge verification.
