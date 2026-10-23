# API reference

Current route/contract map, verified against `backend/cmd/api/main.go`, Go
handlers/models/services, `backend/python/qa/handler.py`, and GatewayStack.
This document describes repository code; it does not certify deployed behavior.

## Transport and authentication

Use the CloudFront application domain with `/api`. The Go entry point is Lambda,
not a standalone HTTP server listening on localhost:8080. The chi integration
uses HTTP API payload 1.0; Python QA uses 2.0.

API Gateway uses a Cognito JWT authorizer for authenticated HTTP routes; edge and
backend validation add their own checks. Go `RequireAdmin` checks verified token
groups. `GET /api/public/docs/{token}` is the one explicit unauthenticated HTTP
API registration: it validates the stored public bearer token. Health and
allowed-domain handlers sit outside Go's Auth group, but that does not exempt
them from the upstream API Gateway/CloudFront route policy. Do not infer external
public access from a handler comment alone.

Downloads are signed CloudFront `/media/{s3Key}` URLs, usually valid for one hour;
public-document downloads use five minutes. Signing-key read failure falls back
to S3 GET presigns. PUT uploads continue to use signed S3 URLs.

Application request logs contain the registered route pattern, allowlisted HTTP
method, response status/size, elapsed time and a hash of the request ID. They
exclude actual paths, queries, headers and bodies, including public-share tokens
in unmatched or encoded URLs. Public-share cleanup failures also omit tokens
and provider error text. Existing logs and previously exposed links require
separate operational review; changing the logger does not revoke a public link.

The staged connector API consumer (ADR-053) is a service-IAM Lambda invocation,
not an additional HTTP route. Its top-level `ttobak.connector.invoke.v1` frame
binds the operation, request ID and exact argument bytes to a signed delegation.
When `CONNECTOR_SIGNING_KEY_ARN` is configured, the consumer loads that exact
public key and reads current client/user grants before and after one of the eight
bounded API read operations. Only the verified in-process principal reaches
the existing router; caller headers and nested HTTP bodies cannot create it.
Response bodies are capped at 14,000 bytes. Empty key configuration denies the
private consumer. Its only mutation is personal note creation as described below.
No OAuth issuance or IAM invocation permission is installed by the consumer code.

The private QA companion accepts only the signed `qa.readonlyAsk` operation,
whose argument DTO names `POST /api/qa/readonly-ask`; that path is not registered
as an HTTP route. It permits a question and optional meeting ID, resolves current
authorized source text, and makes one bounded model call without tools or
session persistence. Source access and client/user grants are checked again
before delivery. Argument bytes are capped at 32,000, question bytes at 4,096,
combined transcript, notes and serialized verified attachment context at 24,000,
and the serialized private result at 14,000. Attachment evidence preserves
partial, retained and unavailable status, with page/slide/paragraph/line citations.
The existing HTTP detection boundary now uses the same atomic current-grant
reader. Private key configuration remains empty until the reviewed KMS/IAM and
OAuth deployment; the Python package must include the pinned ARM64 cryptography
dependency before that activation.

The staged MCP HTTP private transport signs ten bounded API reads,
`qa.readonlyAsk` and personal note creation. It derives identity and scopes from the verified request,
checks atomic current grants before signing and before delivery, and invokes the
configured API/QA Lambda through service IAM. It forwards no bearer token and
never retries a rejected private request through the legacy HTTP transport.
`TTOBAK_CONNECTOR_PRIVATE_BRIDGE` defaults off; deployment permissions and
provider/consumer acceptance remain separate activation requirements.

## Contracts that require care

### Meeting catalog and vault pages

`GET /api/meeting-catalog` lists owned, directly shared and account-shared
meetings through bounded candidate queries. It accepts `tab=all|shared`,
`limit=1..20`, optional comma-separated `accountIds`, `pageSize=1..8000`
Unicode code points and opaque `cursor` (at most 2,048 characters).
Account filters classify accessible meetings; they never grant access.
Reads neither materialize invitations nor create or update user profiles.
Account-derived previews apply summary privacy restrictions, including the
`SHARING_REGEN_REQUIRED` marker; independent owner/direct grants retain access.

Each response is at most 10,000 encoded JSON bytes. Reassemble `dataJson`
fragments for the same revision before parsing the complete group. Discovery
pages contain no records. Groups are current individually, not a collection-wide
snapshot; different grant paths can overlap, so union by `meetingId`. Current
grants and metadata are checked again before publication and continuation.

`GET /api/vault/reading` defaults to `section=manifest`; `section=file` requires
the opaque `fileId` returned by the manifest. Both accept `pageSize` and `cursor`
with the same bounds. The manifest covers owned meetings and personal/account
documents without the legacy 300-meeting cap. It does not hydrate transcripts.
File pages expose exact Markdown fragments or explicit `non_text` entries for
uploaded documents, never presigned links or public-share tokens. Paths include
stable record IDs and sanitized Unicode names. Requested transcript spill reads
are authorized, validated and capped at 1 MiB; excessive sources return
`SOURCE_TOO_LARGE` without file text.

Vault cursors bind the current source, grant, file identity, account path and
attachment group. Source or continuation mismatches return 409 `STALE_CURSOR`;
lost access or a deleted source can instead return 403 or 404. Restart without
the old cursor and obtain a fresh manifest identity when needed.
Both routes use `Cache-Control: no-store`. MCP forwards their opaque pages and
enforces a separate 32,000-byte wire/result cap without a legacy fallback.
The existing web list and vault export endpoints retain their separate contracts.

### Private connector notes

The signed `api.document.putDocument` operation accepts exactly
`{ title, markdown, docType: "note", idempotencyKey }` at the private bridge's
`POST /api/documents` DTO. This reuses the existing route identity; ordinary HTTP
document writes keep their existing behavior. The operation requires OAuth
read/write scopes, enabled client policy and a current matching write approval.
The server chooses the verified user's personal document partition.
Account destinations, uploads, paths and other mutations are unavailable.

Title and markdown limits are 512 and 60,000 UTF-8 bytes after trimming;
the complete body is capped at 65,536 bytes. The retry key is 1–128 ASCII letters,
digits, underscores or hyphens and must match the signed request ID. A transaction
pins client policy and approval generations while creating the note and a
30-day receipt. Each fresh creation uses a new random document UUID; a retry
uses the identity stored in its receipt. An expired receipt that still exists
conflicts. After TTL removes it, reusing the key creates a separate document, so
orphaned shares of a deleted note cannot authorize the new note.
Retry with the same key and exact content after an uncertain
result. A replay returns the same document ID without changing later human edits;
different bytes conflict, and a deleted document is never recreated by replay.
Receipts confirm creation rather than returning current document content.
There is no automatic create retry. Revocation after a successful transaction can
suppress the response; it does not undo the saved note.

### Connector settings

The first-party settings UI uses `GET /api/connectors` to read at most the two
server-configured connector profiles and the current user's own approval.
Disabled client policy and blocked approval rows remain visible here; this
management view never grants access. Missing rollout configuration returns an
empty list, while failed or malformed stored lookups return an explicit error.

`PUT /api/connectors/approval` accepts only `{ profile, action, expectedGeneration }`.
Profiles are `read` or `write`; actions are `allow_read`, `approve_write`, `revoke`
or `block`. The server chooses the client, verified user, capabilities and one-hour
expiry. Writing can be approved only for the configured write profile. The
transaction pins the observed client policy generation, resource audience and
issuer, approval generation and audit receipt. Revocation preserves a prior
permanent block and remains possible after global client disablement.

`PUT /api/settings/connector-policy` additionally requires verified admin
membership and accepts `{ profile, enabled, expectedGeneration }`. Maxima come
from the server-owned profile (`read`, or `read` plus `write`). These routes are
not delegable through MCP, including for an approved writing connector. Bodies
are capped at 1,024 bytes; unknown fields and trailing JSON are rejected.
Generation conflicts return 409. An ambiguous write must be reconciled by
reloading state before another action; the UI does not blindly retry writes.

### Session bootstrap and project invitations

Authenticated `POST /api/session/bootstrap` takes optional `{ projectCursor }`
(maximum 1,024 body bytes); identity comes only from verified JWT claims. It
returns `emailVerified`, `pendingGrants`, `retryPending`, `projectCursor`,
`projectInvitationsEnabled`, up to 100 `joinedAccountIds`, and the current
page's `joinedProjectIds`. `GET /api/projects?joinedProjectIds=...` accepts up to
100 UUID hints; each requires canonical project access and never grants access. Project work is
limited to 25 canonical rows and five seconds per call. Failed pages retain their
cursor. Profile initialization errors fail the request; invitation side effects
surface retry state without blocking unrelated data. Unverified users cannot
consume grants. The companion client initializes once and refreshes after later
pages rather than interrupting an active recording.

An unverified caller with matching project invitations retains the input page
cursor until verification succeeds. Retry that cursor with refreshed verified
claims; a page containing only unrelated identities can advance normally.

Keep queued writers disabled until the consumer and client are deployed and verified.

Owner-only `POST /api/projects/{projectId}/members` accepts `{ email, allowPending? }`
(maximum 2,048 body bytes; email 254 bytes). It resolves current Cognito identity:
unknown users return 409 `INVITATION_REQUIRED`, disabled users 409 `USER_DISABLED`.
User creation/mail remains the separate admin-only settings invitation action.
Legacy clients can atomically add a registered verified user only if no pending
grant exists. Queueing requires `allowPending: true`; unsupported clients get
409 `CLIENT_UPGRADE_REQUIRED`. Queued writes require `PROJECT_INVITATIONS_ENABLED=true`; absent/other values fail closed
with 503 `INVITATIONS_PAUSED`. A queued response has `pending: true`, email,
verification state and empty userId. A recreated identity never receives the old
profile's user ID. Registered-member addition is idempotent.

Owner-only `GET /api/projects/{projectId}/members/pending?cursor=...` returns
`{ members: [{ email, expiresAt }], nextCursor? }`. Each page scans at most 25
reverse rows and revalidates canonical grants; empty pages may have continuation.
`DELETE /api/projects/{projectId}/members/pending` accepts `{ email }` and returns
204 after deletion, or 409 for missing/stale invitations or existing membership.
Already-joined users use member removal; email aliases do not prove old recipients.
A project/sub latest-invitation marker invalidates prior email-specific grants.
Member removal and legacy direct addition retire it atomically; cleanup conflicts
remain retryable. A durable control row also gates queue/claim transactions and bootstrap
capabilities; a paused fence returns `INVITATIONS_PAUSED` and clears page
continuation. Project user queues are isolated from old readers; transactional rows, TTL and
the required incompatible-API rollback drain are specified in ADR-044.

Admin user summaries include `emailVerified`. Password reset requires an enabled,
CONFIRMED user with verified email; missing verification returns 409
`EMAIL_VERIFICATION_REQUIRED`. Disabled mail actions return 409 `USER_DISABLED`.
An existing RESET_REQUIRED user completes or requests a code through the Cognito
self-service flow rather than invoking the CONFIRMED-only admin reset again.
Authenticated email verification uses Cognito SDK operations and refreshes JWT
claims before bootstrap is retried; the app never silently sets verification.

### Billing Insight hand-off

Authenticated `POST /api/billing-insight/handoff` lets a signed-in web user open
the separate AWS Billing Insight companion dashboard without a second sign-in.
It takes no body (bodies over 1,024 bytes return 400) and reads identity only
from verified middleware claims. Responses always carry `Cache-Control: no-store`.

- 404 `NOT_FOUND`: `BILLING_INSIGHT_URL` is unset or not an https origin
  (host only, optional trailing `/`, no query or fragment); the feature is off.
- 401 `UNAUTHORIZED`: no caller sub. 403 `FORBIDDEN`: no email claim (for
  example access tokens or the connector bridge) or `email_verified` is false.
- 500 `INTERNAL_ERROR`: the conditional write failed, including a hash collision.
- 200 `{ "url": "<origin>/login/ttobak?code=<code>" }`.

The code is 32 `crypto/rand` bytes in unpadded base64url (43 characters). The API
conditionally puts (`attribute_not_exists(PK)`) one `ttobak-main` row:
`PK = HANDOFF#<lowercase hex sha256(code)>`, `SK = HANDOFF`, `target =
"billing-insight"`, `sub`, `email` and `pendingShareExpiresAt = now + 60` seconds.
Only the hash is stored and the row has no GSI keys. Opening the link does not
sign anyone in by itself (a code minted for one account could be sent to someone
else): the companion first reads the row and shows whose account it is, and the
user confirms with a same-origin POST. Only then does it redeem the code by
deleting that row (condition `target = billing-insight`, returning the old item),
reject expired rows in code, and start its own session when the sub belongs to a
registered account. A browser already signed in to the companion as the same sub
skips the confirmation. Redemption is therefore single use and the code is valid
for at most 60 seconds, confirmation included. The connector inventory marks the operation
write-capable and non-delegable, so connector tokens cannot mint codes. The web
client validates the returned URL shape before navigating (`src/lib/billingInsight.ts`).

### Meetings and pagination

`GET /api/meetings` accepts `tab=all|shared`, `limit`, opaque `cursor`, and optional
comma-separated `accountIds` (at most 100 distinct IDs). Legacy `accountId` remains
supported; supplying both forms, invalid IDs or a mismatched cursor returns 400.
An empty selection is unfiltered. Preserve normalized selection/tab/caller across
continuation requests; restart pagination when filters change.

`joinedAccountIds` accepts at most 100 normalized discovery hints on the first
page only. Omit it with `cursor`; the continuation already carries those hints.
These are not filters or grants: current canonical membership is rechecked before
any team content is returned. The browser retains hints for 60 seconds, bound to
the current token's user ID, to bridge reverse-index propagation.

The service lists owned, direct-shared and inherited account-team meetings. These
streams retain their own ordering; there is no global chronological merge. A
non-null nextCursor can accompany an empty page. Shared scanning is bounded to
25 pages per request. Read-time checks validate current membership and canonical
meeting account/sharedToAccount state; account classification/hierarchy alone
never grants a private meeting. Direct share permissions take precedence.

Clients expand selected visible account groups to accessible descendants and send
the resulting IDs. The API filters explicit IDs, without inheriting parent access.
Accounts added after team publication can discover existing shared meetings;
pending invitees do so only after verified materialization.

Create/update request types live in `backend/internal/model/request.go`; entity
fields live in `backend/internal/model/meeting.go`.
`POST /api/meetings` optionally accepts `notes` (at most 32,000 Unicode code
points) and `accountId`. Notes validation and current account membership checks
precede meeting creation; both fields persist in the initial meeting write.
The meeting still starts as `recording`, and classification does not share it.
The create response includes `preparationApplied: true` only when nonempty
requested preparation was saved. Create and detail responses advertise
`supportsNotesComparison: true` and `supportsPrivateAccountLink: true`.
`supportsNotesComparison` denotes revision-aware notes fencing, not merely text
comparison. Create/detail responses include `notesRevision`: a server-generated
UUID for new meetings and `""` for legacy rows that have no revision.
During a mixed-version rollout, clients retain preparation locally and require
these capabilities before retrying notes or private classification. Never send
an unguarded preparation update to an older server. Capability flags describe
supported semantics; the existing authorization checks still apply.

`PUT /api/meetings/{meetingId}` optionally accepts `expectedNotes` alongside
`notes` for a notes-only compare-and-set update. Both strings are limited to
32,000 Unicode code points; `notes` must be present, and other mutation fields
are rejected. Empty `expectedNotes` matches absent legacy notes or an explicitly
empty string. Owner/edit authorization is unchanged. A failed database comparison
returns HTTP 409 with `error.code: "CONFLICT"`; retain the local draft and reload.
Omitting `expectedNotes` keeps the legacy update behavior.
An optional `expectedNotesRevision` additionally matches the exact stored notes
version in the same atomic condition. It requires both `notes` and `expectedNotes`,
accepts at most 128 bytes, and empty matches absent or explicitly empty legacy
revisions. A text or version mismatch returns 409. Every notes mutation, including
legacy/unconditional writes and same-text fences, writes a fresh server-generated
UUID in `notesRevision`; successful notes updates return that exact revision.
Meeting creation and every notes-changing write use one SDK wire attempt:
automatic retries must not republish a previously observable revision after a
newer fence. Ambiguous failures remain errors requiring readback. Unrelated
partial updates retain their configured SDK retry policy.
Non-notes updates preserve the version and return the version observed during
authorization. `notesRevision` is response-only: clients cannot assign it
(unknown request fields are ignored). Clients recovering an uncertain request
must fence even a same-text revert using both expected text and revision; equality
of the text alone does not prove an older in-flight request cannot still commit.
Recording autosave and final notes share one serialized comparison writer.
Audio-upload retries do not resend acknowledged notes. An uncertain write is
read back without silently adopting another editor's value; conflicts require
explicit comparison. Size and save errors retain the editable draft and audio.

`PUT /api/meetings/{meetingId}` also accepts `noteLanguage` with owner/edit
permission. Allowed values are exactly `""` (automatic), `"ko"` and `"en"`; any
other value, including case variants, returns 400 `BAD_REQUEST`. An explicit `""`
returns the meeting to automatic; an omitted field preserves the stored value,
and an unchanged value is not written. It cannot be combined with an
`expectedNotes` notes-only update. Meeting detail always returns `noteLanguage`
(`""` for legacy rows). Batch and saved-source summaries map it to a fixed prompt
directive (`noteLanguageDirective`); automatic keeps the historical "Korean unless
the transcript is entirely English" rule. Both summary captures pin the field in
their source conditions, so a change during a run fails that run with
`SOURCE_CHANGED` and keeps the prior summary.

GetMeeting resolves S3 transcript spills and returns the active note/transcript
state, attachments and optional simRun. A/B selection and edited text must remain
the source of truth: old timestamped segments cannot override the selected text.
Detail also returns `accountId` when set, `sharedToAccount`, and owner-only
`projectIds`. `fieldInsights` is an array of stored `MeetingInsight` objects,
including evidence only inside this meeting-authorized response.
`fieldInsightsFreshness` is always `"unknown"`: legacy extraction does not bind
results to the current source. Malformed arrays or invalid entries surface
`fieldInsightsError: "INVALID_INSIGHTS"`; usable entries can still be returned.
The projection allows at most 50 items and 64 KiB of encoded array JSON. Text
and evidence are capped at 2,000 code points each, implication/nextAction at
1,000 each, ID at 128, timestamp marker at 64, and entities at 20 entries of 128
code points each. Any size/count shortening sets `fieldInsightsTruncated: true`.
These bounds cover the insight projection, not the entire legacy detail body.
Saved notes are user input, distinct from generated content. Stuck transcription
or summarization is reconciled after 60 minutes; summarize retry eligibility is a
separate 20-minute conditional claim, not a retry scheduler.

Meeting detail includes `transcriptQuality` for the effective selected A/B text.
The status records refinement and speaker provenance, degradation, optional
diarization metadata and available/partial/unavailable timing. Legacy processing
status is `unknown`; text writes advance only that variant's server-managed
revision. Human API edits explicitly invalidate earlier attestation as `edited`.
Legacy machine publication with text plus segments records unknown processing
with fresh byte fingerprints; its full-text-matched timing stays usable before
strict writer activation. A text-only machine write cannot borrow sibling timing. Text and segment fingerprints
must match the current selected source before its processing metadata supplies
timing. Equal words alone cannot make a known untimed B variant borrow A's cache.
These checks describe structural/source identity, not semantic accuracy or a
verified person's name.

Detail presentation preserves canonical `transcriptA` and `transcriptB` exactly.
Wholly timed, full-text-matched segments retain the existing `transcription`
array. If any verified segment is `untimed: true`, the complete array is returned
only as additive `transcriptionV2`; legacy `transcription` is absent so older
clients show their existing complete canonical-text fallback. Current clients use
`transcriptionV2 ?? transcription ?? []`. Unknown times are explicit, never zero
timestamps. Stale or unavailable timing uses plain text. Existing transcript
updates containing only `transcriptA` remain supported; no new client revision or
projected display text is required.

Bounded transcript reads retain untimed speech without start/end time metadata.
Time-range requests include only verified timed segments, report
`unlocatedCodePoints`, and use `completenessScope: verified_timed_segments` when
some speech cannot be located. Summary/simulator anchors and KB exports likewise
cannot invent times for untimed speech.

The repository provides typed ASR snapshot, atomic transcript/summary-claim and
presence-and-bytes content-fence primitives. The claimed-summary entry point
checks ownership before model evidence and can end an owned batch as `done` with
`HUMAN_CONTENT_PRESERVED` without replacing human content or recording fresh
model-success coverage. Workers now publish transcript/quality and an owned
summary claim atomically. A human transcript edit during refinement discards old
output and starts from the current saved source; a competing audio/processing run
owns its own delivery. Fresh claims reset the two-retry budget; resumed claims
retain it and verify ownership again before model evidence. Reader deployment
must precede writer deployment; drain older writers before relying on revisions.
Legacy fixed-key S3 checks are preflight checks rather than an atomic S3/DynamoDB
fence. Code installation does not prove deployed acceptance.

Whisper refinement requires a complete `end_turn` response, strict segment schema,
finite source-bound times and each input `sourceIds` index exactly once in order.
Missing/duplicate/foreign IDs, incomplete output or invalid schema falls back to
raw speech for that chunk. Blank segments are counted and omitted; rounded
zero-length segments widen by 0.01 seconds. Other invalid times retain every word
as explicitly untimed speech alongside valid timed speech. Raw transcript JSON
is capped at 16 MiB, each chunk at 2,048 segments/128 KiB of text, and responses
have bounded decoding. Structural coverage does not attest semantic accuracy.

Conditional transcript/speaker edits publish new immutable
`transcripts/{meetingId}/{field}.{32-lowercase-hex}.txt` references only with a
matching database update. Conflicts return 409; ambiguous database failures retain
new objects because the write may have committed. Existing referenced objects
remain intact. Go and QA readers accept strictly validated legacy and versioned
keys; deploy compatible readers before writers (ADR-037).

`PUT /api/meetings/{meetingId}/speakers` permits owners, direct read/edit
recipients and current members of the published account team to correct speaker
names. This does not grant general content/notes editing or re-diarization.
Access is rechecked on each request; revoked publication/membership cannot rely
on cached account shares. The JSON body is capped at 128 KiB and `speakerMap` at
100 entries; labels must identify existing speakers and names must contain
1–100 characters without control characters. Invalid mappings return 400,
oversized bodies 413, inaccessible meetings 404 and concurrent writes 409.
Read recipients can replace anonymous labels in prose; custom names update only
structured speaker attribution, complete bracket-only transcript speaker lines,
and exact action-item assignees. Repeated corrections remain available, including
legacy meetings whose previous names are recorded in `speakerMap`. Custom-name
occurrences in ordinary prose remain unchanged for readers; owner/editor
custom-name prose replacement remains available. Partial corrections retain
unaffected current speaker names without accumulating historical aliases.
Restricted account views never write their hidden content/action fields.
Unchanged derived fields are also omitted, preserving safe summary clearance
for attribution-only corrections. Action-item updates target JSON text/assignee
fields and preserve task IDs, completion and other metadata.
See ADR-051.

`GET /api/kb/files` retains its `{files: [...]}` contract and follows all S3
continuation pages under the caller's `kb/{userId}/` prefix. Any page failure
fails the request rather than returning a silently incomplete successful list.
This is storage pagination; the HTTP response remains a complete file list.

`DELETE /api/kb/files/{fileId}` deletes the one object under that prefix whose
final path segment equals `fileId` exactly, searching every listing page. An ID
with a path separator or `..` returns 400, no match 404, and several objects with
the same name 409 (nothing is deleted). Server errors return a generic message.

### Meeting PDF export

Authenticated `POST /api/meetings/{meetingId}/export` with `{"format":"pdf"}`
returns a `.pdf` filename and five-minute signed download URL, not text under a
PDF label. Owners and currently authorized recipients export their current
authorized view. Source and access are rechecked before storage and before the
URL is returned; changes return 409 or an inaccessible-resource response.

The PDF embeds the licensed Pretendard font and contains the title, date,
participants, saved notes, HTML-to-text summary and selected A/B transcript.
Input is limited to 2MiB of UTF-8 text, 200 pages and 8MiB of PDF output; the request
body is capped at 4KiB. Limits return 413, unsupported glyphs 422, and generation,
storage or signing failures return a generic error. No successful export silently
drops unsupported text. Markdown export remains available.

Temporary artifacts use fresh `files/{callerId}/{meetingId}/export_{uuid}.pdf`
keys, the existing private bucket/download signer and an attachment disposition.
The `ttobak-artifact=meeting-pdf-export` tag limits the seven-day lifecycle rule
to these exports. Current versions become eligible after seven days; the
noncurrent seven-day clock starts when a version is superseded. A body can remain
after current-version expiration until that separate window and asynchronous
cleanup finish. This does not extend the download URL or delete canonical records.
Deploy and verify the StorageStack lifecycle rule before claiming retention is
active. Meeting deletion or grant revocation does not immediately revoke an
already issued five-minute URL.

### Bounded meeting reading

Bounded `section=notes` pages include the exact `notesRevision` (empty for legacy
rows) from a strong metadata read without transcript S3 hydration. Their existing
`revision` fingerprint and opaque cursor bind both the text and notes version, so
even a same-text fence invalidates an older continuation with 409 `STALE_CURSOR`.
Owner and shared-detail reads also use a strongly consistent canonical `GetItem`.
Meeting-ID discovery projects only owner/meeting IDs from GSI3, then rereads the
primary key; stale GSI text, revisions and publication flags are never authoritative.

`GET /api/meetings/{meetingId}/reading` rechecks owner/direct-share/current-account
access on every page and returns `Cache-Control: no-store`.

| Query | Contract |
|---|---|
| kind | `meeting` (default) or `transcript` |
| pageSize | Integer 1–8000 Unicode code points; default 4000 |
| cursor | Opaque continuation, at most 2048 characters |
| section | Meeting only: `notes` (default), `summary`, `actionItems` |
| source | Transcript only: `selected` (default), `A`, `B` |
| startTime, endTime | Transcript only; both finite seconds, `0 <= startTime < endTime` |

Unknown/repeated keys and incompatible options return 400 before storage reads.
Keep meeting/kind/section or source/time range unchanged across continuations;
pageSize may change. The inner JSON response is at most **14,000 bytes including
the trailing newline**, with at most 50 transcript chunks. This bounds transport,
not the memory required to load and validate the selected source.

Meeting pages return exact `notes`, `content`, or `actionItemsJson`, identifying
metadata, `availableCodePoints`, `revision`, `readingHints`, and `page`. Join all
actionItemsJson pages before parsing; a page may end inside a JSON string.
Normalized legacy IDs/completion flags and stored extension fields survive the
full section. `actionItems` is only a bounded preview: inspect
`actionItemsPreview.available/complete/totalItems/metadataTruncated` and
`actionItemsAnalysis`, never infer successful extraction from an empty preview.
Metadata truncation is explicit; extension-only changes invalidate continuation.

Transcript pages return source/selection/provenance, exact `chunks[].text`, and
zero-based code-point offsets with exclusive ends. Only segments verified against
the effective selected text receive original speaker/timing metadata. Unselected
or unmatched variants use raw text without borrowed timestamps. A selected empty
variant may fall back to the other available variant; explicit A/B never does.
Time windows select whole verified segments overlapping `[startTime,endTime)`;
partial chunks retain `timingScope: whole_segment`, not inferred word times.

`page` reports unit, start/end offsets, whole-source totalCodePoints, requested-span
matchingCodePoints, complete and nextCursor. Consume all preceding pages before
claiming completeness; time-window completeness covers only that window. Cursors
bind current content and provenance. A source change returns 409 `STALE_CURSOR`
and requires restarting; malformed cursors return 400 `INVALID_CURSOR`.
Other 400 codes include `INVALID_ARGUMENT`, `NO_TRANSCRIPT`, and
`TIME_RANGE_UNAVAILABLE`; denied/missing meetings return 403/404 and read failures
500 `READING_UNAVAILABLE`. Failures never fall back to cached text.

Notes/summary/action-item reads use metadata views without S3 transcript hydration.
Transcript reads hydrate only the chosen field and eligible segment candidates
after authorization. Storage references are never returned. Source:
`handler/meeting_reading.go`, `service/meeting_reading*.go`; MCP usage:
[adapter README](../mcp-server/README.md).

HTTP MCP (ADR-045) is served by the `ttobak-mcp` Lambda, not the Go API:
`ANY /api/mcp` (JWT authorizer; POST JSON-RPC, stateless, other methods 405) and
the unauthenticated static discovery documents
`GET /.well-known/oauth-protected-resource` and
`GET /.well-known/oauth-protected-resource/api/mcp`. Tokens must be Cognito access
tokens with `aud` equal to `https://<site>/api/mcp`; see the adapter README.

### Action item analysis

GET `.../action-items` permits meeting readers; POST `.../action-items/retry` and
PUT `.../action-items/{itemId}` require owner/edit access. GET/PUT return
`{actionItems, analysis}` with 200; retry returns that shape with 202 and requires
a done meeting with a nonblank saved summary. PUT requires explicit
`{"completed":true}` or `{"completed":false}`.

Analysis is `unknown|queued|running|succeeded|failed`. Only succeeded plus an
empty array means no tasks. Missing legacy metadata is unknown; expired leases
become failed/`INTERRUPTED`. Detail/reading status failures show
unknown/`STATUS_UNAVAILABLE`; changed successful source shows failed/`SOURCE_CHANGED`.
Errors use fixed codes, never raw model responses.

The separate `MEETING#{id}/ANALYSIS#actionItems` row binds run, source and lease.
Retry uses `ttobak.analysis` / `ActionItemsRequested`; the summarize worker also
uses this service inline. Result and success commit together only if run, saved
summary and prior items still match. Failures retain prior items; unchanged tasks
keep IDs and human completion. Metadata reads avoid transcript hydration and
there is no transcript fallback when the summary is missing. Conflicts return
409, denied writes 403 and missing meetings/items 404.

### Accounts and projects

`POST /api/meetings/{meetingId}/account` is a private classification operation:
the meeting owner must currently belong to the target account. One partial
update sets `accountId` and `sharedToAccount: false` together, including when
relinking an already team-shared meeting to the same or a different account.
Notes and independent direct shares are preserved. Account-origin share rows
remain subject to current meeting publication/membership checks; an explicit
`POST /api/meetings/{meetingId}/share-account` is required to publish to a team.
`POST /api/meetings/{meetingId}/share` (direct or pending invite) and
`share-account` map `SUMMARY_SHARING_UNSAFE` to 409, a concurrent meeting change
to 409 `CONFLICT` and a lapsed owner access or account membership to 403 when the
service reports them; other failures stay 500. Which grant paths can raise them
is defined by the summary-privacy writer state in INFRA-SPEC.
The meeting-detail UI uses that publication endpoint directly for its single
link-and-share action: no preliminary private link request is needed. Existing
private links and recording preparation remain private until explicit publication.

Account meeting lists and meeting-sourced insights, including the registered QA
account insight/brief tools, revalidate the exact
owner/meeting identity and current `accountId`/`sharedToAccount` using a strongly
consistent, metadata-only primary-key read. They never authorize from an eventual
GSI copy or hydrate transcript objects for this check. Missing/deleted, unpublished,
repointed, or mismatched source/projection rows are omitted even when old account
projection rows remain. Storage or decode failures fail the request rather than
returning an empty or partial success. The account brief uses the same readers.
Explicit account-owned `news` and `ingest` insight sources retain their member-only
visibility; missing or unknown source types do not bypass meeting validation.
Malformed legacy projection identities are omitted with a successful response,
not treated as grants. Deploy the guarded QA consumers before enabling private
relinking in the API; code checks alone do not establish live revocation.

Account creation creates owner membership atomically. Optional parentAccountId
requires current membership in the parent. Parent updates require child ownership
and new-parent membership; the required field uses an empty string to detach,
not omitted/null. Self/cyclic/deep ancestry (over 64 nodes) is rejected;
transactional ancestry checks and retries protect concurrent moves. Conflicts
return 409. Hierarchy is organization metadata, never authorization inheritance.

Any current account member can add members or change assignable roles; `owner`
is never assignable. Adding an email with no profile and no Cognito user returns
404 `INVITATION_REQUIRED`; an invited-but-unregistered email returns
`pending: true`. Creating the Cognito user remains admin-only. Removal and pending-invite revocation remain owner-only
(ADR-034). Project permissions are separate: owner/direct member/member of a
linked account. The service list unions those access paths; project deletion
rejects remaining relationships. Canonical sets and reverse references change in
one transaction. See account/project handlers and services for each mutation gate.

### Staged summary-sharing privacy

Meeting detail adds `summarySharing`: `canCreateGrant`,
`derivedReadRestricted`, `requiresRegeneration` and `canRegenerate`. These
describe the current source view. Grant/publication and provenance writers are
installed behind `SUMMARY_PRIVACY_WRITER_ENABLED=1`; any other value disables
writer activation. `canRegenerate` requires activated writers, ownership and a
terminal meeting state. Source wiring does not establish deployed acceptance.

For a protected account membership, unknown/private meeting derivation withholds
summary, tasks, insights, tags, sentiment and live draft context. Parent-owned
notes and transcript remain available through their authorized bounded reads.
Existing independent direct grants and owner access retain their scope.
Restricted account insight/brief requests return 409 `SHARING_REGEN_REQUIRED`;
they do not claim that there are no insights.

Copied account insights must match the canonical summary generation and insight
revision. Readers recheck publication and membership before returning a result.
Stamped memberships are always protected. Enforcement for unmarked memberships
requires `SUMMARY_PRIVACY_READER_ENABLED=1` and the fixed UTC
`SUMMARY_PRIVACY_LEGACY_MEMBER_CUTOFF`; missing/invalid eligibility denies derived
access after activation. The installation value `0` preserves unmarked legacy
behavior and is not evidence that R1 or historical disclosure has been fixed.

Activated direct/pending/materialized grants fence the canonical parent and
advance `authorizationRevision`. Account publication additionally checks current
owner membership in the same transaction, even for an account with no recipients.
New/re-added memberships receive a server-minted time and privacy marker; role
changes preserve them. Unknown/private derivation cannot create a new grant.
Ordinary summary regeneration and content/live-draft edits retain private or
unknown ancestry. Saved notes remain independent human input.

### Documents and shares

Personal document writes and share mutations isolate the owner's partition; a
non-owner cannot mutate the source. GetUserDocument first checks the caller's
partition, then resolves an authorized read-only share through the owner's row.
An inaccessible document returns 404. An account share creates an independent
S3 copy; an email share references the original and is read-only.
Document shares use SHAREDDOC#/DOCSHARE_TO#, not meeting SHARED# keys. Deleting an
owner document can leave share rows; list reads skip missing targets.

Authenticated `GET /api/documents/{docId}/reading` and
`GET /api/accounts/{accountId}/documents/{docId}/reading` return bounded current
document pages. `section=content` is the default; `metadata` omits text.
File-only `download` and `preview` selections request their respective links.
`pageSize` counts Unicode code points (1–8000, default 4000). `maxBytes` bounds
encoded JSON including its newline (8000–14000, default 10000), leaving room
for MCP text and structured representations together. Content is never silently
truncated: follow `page.nextCursor` until `complete=true` and `nextCursor=null`.
Page offsets and totals use `unicode_code_points`.

Every page reads present ownership/membership/share authorization and the current
document using consistent primary reads, and rechecks both before returning.
Cursors bind user, account scope, document, section, source revision and grant.
Changing any of those makes continuation fail; content changes invalidate it even
when the saved timestamp is unchanged. `STALE_CURSOR` returns 409 and requires a
fresh read. Invalid options/cursors return 400; inaccessible personal documents
return 404 and unauthorized account reads return 403.

Responses include `revision`, `scope`, `permission`, `mode`, `textStatus`,
`provenance`, explicit `metadataTruncated` fields, `content` and `page`.
File-backed documents return `mode=file`, `textStatus=not_available`, bounded file
metadata, authorized five-minute download/preview URLs and `textExtracted=false`.
The existing AccountService and UploadService paths revalidate file access and
sign the exact current key. Personal/shared keys must belong to the source
owner; account file keys remain authoritative after another editor replaces a
file. Public sharing tokens and raw `fileKey` fields are never returned.
Link failures are explicit `downloadStatus`/`previewStatus=unavailable`. URLs are
never truncated: oversized metadata previews shrink with `metadataTruncated`;
if both full URLs still exceed the page budget, the download remains and
`previewStatus=deferred` supplies `previewRead` for a separately authorized
`section=preview` read. Its `expectedRevision` rejects file replacement with
`STALE_CURSOR`; callers can also pin other related reads with that field.
These links do not claim extracted file text.

Authenticated `GET /api/document-catalog` provides bounded metadata pages for
MCP discovery. Omit `accountId` for personal and directly-shared documents; an
explicit account requires current membership. Preserve `accountId` and `docType`
while following the opaque `cursor`. `pageSize` is 1–50 (default 20); each call
issues at most one DynamoDB query and never loads document bodies. Encoded JSON
is capped at 10,000 bytes, including metadata previews and continuation.
Empty filtered pages can have another cursor and must not be treated as complete.

Rows revalidate their canonical metadata and current grants before publication.
Cursors bind the caller, selection, current account grant and last returned
item's metadata/grant. A changed anchor or revoked grant rejects continuation.
The list is live (`consistency=current_per_item`), not a collection-wide
snapshot. `metadataRevision` identifies metadata only; it is not a document-body
revision for `expectedRevision`. Read the selected document to obtain that
revision. `metadataTruncated` marks shortened field/link previews; body bytes,
raw file keys and public-share capabilities are excluded from catalog responses.

Authenticated `GET /api/kb/files/reading` pages the caller's private KB file
metadata. It accepts `pageSize` (1–50, default 20) and an opaque `cursor`, with
the same 10,000-byte encoded page budget. Each request uses one bounded S3 list
operation in the configured general-purpose bucket and checks current object
metadata before returning it. The cursor binds the user/bucket and last
consumed object's revision; changed/deleted anchors return `STALE_CURSOR`.
Names may have explicit preview truncation, but `fileId` is preserved exactly.
The list is live, not a collection-wide snapshot. No object body is downloaded,
and foreign-prefix objects fail closed.

Authenticated `GET /api/navigation/accounts` and `/api/navigation/projects`
provide bounded metadata catalogs (`pageSize` 1–50, default 20; opaque `cursor`;
10,000 encoded bytes). GSI candidates are revalidated against consistent
canonical membership/resource reads; indexes never grant access. Account
hierarchy remains classification only. Project pages visit ownership, direct
membership and linked-account membership. Deduplication uses only exact IDs
already emitted, never an inferred earlier path: membership/ref indexes can lag.
The cursor retains up to 32 emitted IDs while staying within 2048 characters.
When that bounded state fills, `overlapPossible=true` is explicit; combine
project results by the declared `unionKey=projectId`. No authorized project is
suppressed merely because another membership exists but its index is delayed.

Project account traversal is resumable. Each request issues at most two
bounded candidate queries and does not enumerate earlier-account memberships
to infer duplicates. Empty continuation pages remain possible. Continue until
`complete=true`; do not treat an empty page as completion. Canonical String Sets
are sorted for stable metadata revisions. Continuations revalidate
their last returned resource/grant and any in-progress account scope. These
are live catalogs, not collection-wide snapshots; concurrent changes can
require a fresh read.

Authenticated `GET /api/accounts/{accountId}/reading` returns full account data
without a monolithic brief response. `section=detail` (default) traverses
metadata and members; `brief` also traverses meetings and insights. Individual
metadata/members/meetings/insights selections are supported. The brief's
`from`/`to` RFC3339 and `types` filters apply to insights as before.

Each physical collection window contains at most ten source rows. The existing
AccountService authorization and canonical meeting-publication checks run on
that window; raw insight Evidence is excluded. Results are serialized as JSON
groups and split by Unicode code points (`pageSize` 1–8000, default 4000),
with a 10,000-byte encoded response cap. Concatenate `dataJson` only within the
same `phase`/`revision` until `fragment.complete=true`, then parse that complete
object (metadata) or array (other phases). Continue the opaque `page.nextCursor`
until `page.complete=true` to finish all groups/phases, including empty filtered
groups. Group fragments are never silently truncated.

Cursors bind the caller, account, selection/filters, account metadata and current
membership. Fragment continuation also pins the exact authorized source window
and its provider continuation. Membership, publication and payload are rechecked
before returning bytes. `consistency=current_group` describes a live collection
of individually pinned windows, not one atomic snapshot of every brief record.

Authenticated `GET /api/projects/{projectId}/reading` uses the same JSON-group
contract and limits. `detail` traverses metadata/members; `brief` traverses
metadata/members/meetings/research/insights. Each phase can also be selected
alone. Full descriptions, account IDs and member/meeting/insight text remain
available across fragments; research summaries retain their existing preview
semantics. Date/type filters apply only to brief/insights.

Project authorization checks owner/direct membership first, then at most 50
linked-account candidates per request. When more candidates remain without a
verified grant, `phase=authorization` and `authorizationPending=true` return
only identity/selection and an opaque continuation, with no project data.
Continue until authorized data or a denial; transient membership failures
retain the current scan window for retry. Once selected, the grant path is
pinned and revalidated on every page. Project metadata changes invalidate
continuation; set ordering alone does not. No account hint or cursor grants
access.

Project collections query at most ten consistent primary source rows per
window. Meeting/research bodies use bounded consistent primary reads and
canonical project-link checks; research owner mismatch, trash and stale links
are omitted. Raw insight Evidence remains suppressed. A changed source window
or grant fails continuation before returning bytes. Existing duplicate meeting
references can span windows: `overlapPossible=true` explicitly requires union
by `meetingId`, and replacing repeated complete insight groups by `sourceId`
while preserving all insights for that source. This is a live set of pinned
source windows, not a collection-wide snapshot.


PPT/PPTX uploads trigger convert-doc and produce a docs-pdf/ sidecar. previewUrl
points to the PDF while downloadUrl always points to the original. The converter
records the actual GET's source ETag/version, rechecks the original, and replaces
the observed preview conditionally. The preview URL path still checks existence;
canonical indexing/QA readers additionally validate the source binding. Legacy
unbound previews require regeneration. Public tokens
are conditionally minted and validated against the document's own PublicShareToken
on every read; revocation removes that grant. See ADR-022/027/029.

Pending meeting/account invitations bind the invited Cognito sub, require verified
email at materialization, and expire after 30 days. Revoking an already-materialized
grant returns 409 rather than falsely reporting revocation. The table sweeps
pendingShareExpiresAt; QA history's uppercase TTL field is not swept.

### Upload and recovery

The server validates category, ownership and traversal. Checkpoint filenames
recording_progress.webm/m4a/ogg intentionally overwrite a stable audio key; other
uploads receive unique names. Upload completion emits the appropriate custom
event. The bounded PDF/PPTX/DOCX/Markdown extractor and its asynchronous worker
now exist. The API wires AttachmentTextService into upload completion, status, retry
and bounded text routes. Summarization receives only verified current DOCUMENT text;
failed/unavailable documents produce coverage notices. QA reader activation is separate.
AudioUploader attempts KB promotion; the recording page also offers manual copy.
Neither copying nor preview conversion proves summary grounding.

The worker accepts `ttobak.upload` / `DocumentUploadCompleted` only for canonical
ATTACH#/ATTEXT# identities and a queued run. Owner and uploader may differ; event
key alone is not authority. Pinned bounded reads produce immutable
`files/{uploader}/{meetingId}/text/{attachmentId}/{runId}.json`. Parent/attachment,
run, lease and ETag checks guard publication. Failed attempts retain earlier
results; attempt status and retained-result completeness are separate. Locations
are document pages/slides/paragraphs/cells/lines, never audio timestamps.
See the [worker contract](../backend/python/document-extract/LAMBDA.md) and
[parser scope](../backend/python/document-extract/README.md).

Recover uses a saved progress object. Rediarize accepts supported single-part
Whisper meetings and a speaker-count hint. Use their handler/service contracts,
not a raw DynamoDB status reset or a fabricated AWS S3 event.

`canRecoverRecording` is owner-only and includes expired `error` drafts without
existing audio, transcript or summary. `recover` returns
`RECORDING_CHECKPOINT_MISSING` when no checkpoint exists, distinguishes storage
failures, and compares the observed meeting state before publishing its result.
The transcribe consumer retries a recovered-copy event until its canonical audio
key is bound within the configured async retry budget, and discards copies that
lost to a different upload. Recovery uses unique keys, deletes definitively
rejected copies and surfaces cleanup failures. Ambiguous writes retain their copy.

`POST /api/meetings/{meetingId}/audio/crop` accepts a bounded JSON object containing
`requestId` (UUID), `startSeconds` and `endSeconds` (integer seconds). Owner-only,
single-file `done`/`error` sources are eligible. The server resolves the canonical
audio key and pins its S3 ETag; clients cannot select arbitrary keys. Source size
is at most 2 GiB, range length at most six hours, and end at most 24 hours.
The 202 response contains the new `meetingId` and status. Reusing a request ID and
range returns the same copy; conflicting reuse returns 409. A queued event publish
failure can be retried with the same request ID. A failed worker requires a new
request from the original; there is no automatic Spot-task restart.

The source revision check and conditional copy creation are one transaction.
Copies retain notes and participants, without prior transcripts, summaries,
action items, attachments, account/project relations or shares. Detail exposes
`supportsAudioCrop`, `audioCrop` provenance/status and `duration`. Source storage
keys/ETags, worker claim IDs and candidate output keys are not exposed through
`audioCrop`. Expired queued copies are not dispatched again. Cropped audio
cannot be replaced or rediarized in place. Its worker claims once, reads the exact
authorized source with `IfMatch`, verifies the decoded range length, and publishes
only selected audio and its new transcript. Rejected publication does not emit a
transcript or recreate a deleted meeting. A run-bound heartbeat keeps long media
work current; loss or expiry of that claim fails the copy and prevents publication.
Definitively rejected audio is deleted, ambiguous bindings retain their object,
and cleanup failures surface.

### Live translation

`POST /api/translate` translates one live caption segment:
`{text, sourceLang?, targetLang, quality?, context?}`. `text` is at most 9,000 UTF-8
bytes. `sourceLang` is `auto` or one of `ko|en|ja|zh|es|fr|de` (default `auto`);
`targetLang` is one of the same codes and must differ from the source. The
client-only bilingual sentinel `ko-en` is resolved before the call. The body is
capped at 32KB; violations return 400.

`quality` defaults to `fast` (Amazon Translate only). `high` asks the interpreter
model configured by `BEDROCK_TRANSLATE_MODEL_ID` first, then falls back to Amazon
Translate on any model error, timeout (`TRANSLATE_HIGH_TIMEOUT_MS`, default 6000,
accepted 500–8000), truncated or rejected output, or when the model is unset or
the text exceeds 3,000 bytes. `validateInterpretation` rejects output that starts
with a translation label (English or Korean `Translation:` labels, `Here is the translation`…; see `interpreterMetaPrefixes`),
starts with `{` or echoes `current_segment`/`previous_segments`, is longer than
`4*len(text)+200` bytes, has a Hangul letter share below 0.3 for a `ko` target
(except at most four words that all occur in a Latin-only source, such as kept
product names), or above 0.1 for an `en` target. Fallback reasons in metering are
`unavailable`, `too-long`, `llm-error` (including timeout) and `invalid-output`.
The web client sends `high` with context for finals and `fast` for opt-in drafts.
`context` (at most two
earlier source segments, 2,000 bytes total) is reference data for `high` only and is
JSON-encoded separately from the current segment. The response is
`{translatedText, sourceLang, targetLang, quality, engine, limited}` where `engine` is
`llm` or `translate`; a caller can show the `fast` result and replace it with `high`.
Amazon Translate requests targeting `ko` set `Settings.Formality=FORMAL`; profanity
masking is not enabled. Failures return a generic 500 without segment text; the text
is never logged.

Cost guard: each authenticated user has fixed hourly UTC windows counted by
`WindowCounter` (`USER#{userId}/RATE#translate#{windowStart}` and
`RATE#translate-high#{windowStart}`, atomic `ADD count`, `pendingShareExpiresAt` =
window end + 2h). Every validated request counts toward `TRANSLATE_HOURLY_LIMIT`
(default 3000); above it the API returns 429 `RATE_LIMITED` with `Retry-After`
seconds and does not call a translator. `high` requests also count toward
`TRANSLATE_HIGH_HOURLY_LIMIT` (default 1500); above it the request is served by
`fast`, and the response has `quality: "fast"`, `limited: true`. A limit of 0
disables that check. Counter storage errors fail open: this is an abuse/cost brake,
not an access boundary. Rejected requests also count within their window. Each
success logs one metering JSON line
`{evt, q, engine, src, tgt, inBytes, outBytes, ms, limited, fallbackReason}`
without text or user ID. No route throttling is configured in `gateway-stack.ts`.

### Meeting translation

`POST /api/meetings/{meetingId}/translation` translates one bounded reading page of
a meeting's AI notes or selected transcript; nothing is persisted (`persisted:
false`, `Cache-Control: no-store`). Unless `MEETING_TRANSLATION_ENABLED=true` it
returns 404 `FEATURE_DISABLED` before identity or storage checks; meeting detail
exposes `supportsMeetingTranslation`.

The strict body `{part, target, cursor?}` is at most 4KB (413 above). Unknown
fields, trailing values, `part` other than `notes|transcript` or `target` other
than `ko|en` return 400 `BAD_REQUEST`; a cursor over 2048 characters or outside the
reading-cursor alphabet 400 `INVALID_CURSOR`, before storage reads. Pages of 4000
code points come only from `MeetingReadingService` (notes: `kind=meeting&section=
summary`; transcript: `kind=transcript&source=selected`), so access and cursor
errors match `/reading` (404, 409 `SHARING_REGEN_REQUIRED`, 409 `STALE_CURSOR`).
A non-final page is trimmed to its last line start (notes: outside a fence if any;
text transcript: a speaker header after a blank line, else a line not directly
after a header) or whole segment, re-issuing the reader's cursor; a page without
one is kept whole. A wrapper cursor carries an open notes fence (closed, as in
CommonMark, only by a bare run of its character at least as long) or the
text-transcript speaker and mid-run state. Pass `nextCursor` unchanged with the
same `part`; pages concatenate to the source.

Notes are translated per Markdown line: markers stay local; links, images, inline
code, HTML and URLs become atomic `⟦Ln⟧` placeholders; fenced code, table rules and
blank lines are not sent. Transcript text is sent without speaker labels or times,
reattached from the page (`segments[]`: `segmentId`, `speaker`, `startTime`/
`endTime` when timed, `partial`, `text`); text-mode `[speaker]` headers (page start
outside a run, or after a blank line) are not sent, other bracketed lines are
speech. Lines with at least 30% Hangul are Korean, Latin-only lines English,
code-mixed lines the non-target language; target-language or other-script lines
stay source. At most 12 same-language chunks of 3,000 bytes run with concurrency 4
in a 20 s budget through the `quality: high` interpreter path; a chunk changing
line count, placeholders or table pipes is retried once with Amazon Translate. A
line with any failed, over-cap or unrestorable piece keeps its whole source and
counts toward `untranslatedLines`. All chunks failing returns 502
`TRANSLATION_FAILED`; a response over 64KB 502 `TRANSLATION_TOO_LARGE`.

The response is `{meetingId, part, target, revision, mode?, notes? | segments?,
engines, untranslatedLines, nextCursor, complete, persisted}`. Bytes to be sent are
charged on attempt to the daily per-user `WindowCounter` bucket `meeting-translate`
(UTC day, TTL window end + 2h); above `MEETING_TRANSLATE_DAILY_BYTES` (default
2,000,000; 0 disables) the API returns 429 `RATE_LIMITED` with `Retry-After`.
Counter errors fail open. Each attempt after the page read, including 429 and 502,
logs one line `{evt: "meeting_translate", outcome, part, tgt, inBytes, outBytes,
chunks, failedChunks, untranslatedLines, ms}` without text or user ID.

### Simulator

`POST .../sim/extract` extracts requirements for a done meeting. `POST .../sim`
validates allowed keys/ranges and two or three architecture options, then returns
202 with a queued simRun. The frontend polls GetMeeting; there is no simulator
WebSocket stream. Concurrent runs are conditionally rejected. Stale queued/running
runs are persisted to error by GetSimRun, not merely displayed as error.

Only validated requirements/options enter codegen; option descriptions still
influence the prompt. The interpreter's empty IAM role and SANDBOX network are the
execution boundary. Worker updates require matching simRunId. Outputs stay under
images/ and files/; details are in ADR-033 and service/sim.go.

### Admin and settings

Invite-user and the six user-management routes use RequireAdmin. Delete/disable
protect self and last-admin targets, with a post-write race warning. Sign-out
revokes refresh tokens but issued locally validated JWTs survive until expiry.
PostAuthentication records lastLoginAt separately from PROFILE and fails open;
refresh-token authentication does not update it. New accounts use admin invites
and NEW_PASSWORD_REQUIRED, never self-signup. The domain allowlist is supplemental.
`isApprovedAdminInvite` exempts only `demo@atomai.click` (case-insensitive) on
`PreSignUp_AdminCreateUser`; it grants neither a domain-wide exception nor group
membership. RESEND trigger behavior remains unverified.

### Personal dictionary

Storage and migration: ADR-008's 2026-10-18 amendments. Routes act on the
caller's `USER#` partition; `termId` is the 32-hex `termKey` (other shapes 404).
`ValidateTermInput`: NFKC and trim of surrounding spaces; Cc/Cf (tabs and
newlines anywhere, including the edges) and line separators rejected;
`displayAs` 2-40 runes, at most 120 bytes; at most five 2-40 rune `heardAs`,
never blocklisted (`terms_blocklist.txt`) or the term's own key; `phrase` at most
256 runes. Errors are 400 `field: reason` and never echo input.

- `GET /api/settings/dictionary` migrates the legacy item best effort, runs
  `EnsureVocabularies` (see below), then returns legacy `terms`/`status`,
  `vocabularies`, `warnings?` plus `items` (non-tombstone rows: `termId`,
  `displayAs`, `phrase`, `heardAs`, `aliasMode`, `type`, `origin`,
  `state` `active|suggested`, `userEdited`, `version`, flags, `hits`, times).
  `migrationPending` means the legacy item is not yet migrated: either it is
  refused (distinct terms share a key, which a deduplicated legacy PUT resolves)
  or the best-effort migration failed and is retried later. Legacy `terms` list
  active rows of every origin, possibly without a `phrase`, and exclude
  suggestions. `items` is omitted when there are no rows. A term saved without
  `displayAs` lists `displayAs: ""` only while its `phrase` still names the
  term; after a v2 phrase edit it lists the stored `displayAs`, so an unchanged
  round trip keeps the same row.
- `POST .../terms` `{displayAs, phrase?, heardAs?, type?, aliasMode?}` (at most
  4 KB, no unknown fields or trailing data) creates a manual term or restores a
  tombstone as manual; `phrase` defaults to `displayAs`, and `""` keeps the term
  out of the vocabulary. 201 `{item, termId, state, status, warnings?}`; 409
  `TERM_EXISTS`, or `DICTIONARY_FULL` at 500 active manual terms. The cap is
  best effort (check, then write): concurrent creates can slightly exceed it.
- `PATCH .../terms/{termId}` requires `version` (stale: 409 `VERSION_CONFLICT`);
  optional `displayAs` (same key only), `phrase`, `heardAs`, `type`, `aliasMode`
  (`hint|always`, the keep action) and `state: "active"` (accept a suggestion).
  Every edit sets `userEditedAt`, blocking automatic writes; `origin` is kept.
- `DELETE .../terms/{termId}[?version=]` tombstones any origin; failed observation
  cleanup or pruning (beyond 2,000 tombstones, oldest first) is a `warnings` entry.
- Legacy `PUT` (at most 256 KB and 1,000 entries; `soundsLike` splits on commas)
  accepts the GET `terms` unchanged. Entries whose key belongs to a live auto or
  suggested row are passed through without validation and never written; if one
  differs from that row, or names a suggestion, the response carries a
  `warnings` entry. The remaining entries are manual terms (at most 500): new or
  changed ones are validated and creates or restores need a `phrase`; unchanged
  ones are kept as stored. Distinct terms sharing a key are 400. The PUT writes
  the manual terms to the legacy item, migrates, then reconciles **manual** rows
  only: create (restoring a tombstone), edit changed terms, hard-delete manual
  rows absent from the body. It never tombstones and never modifies auto or
  suggested rows. The PUT is not atomic across rows: each row write is
  version-conditioned, a failure part-way leaves earlier writes applied and
  skips the rebuild, and retrying the same body converges. It is last writer
  wins for the whole list: a manual term created (through v2 or an overlapping
  PUT) after the body's GET is absent from the body and is deleted without a
  conflict, and two overlapping PUTs with different bodies can each delete the
  other's terms.
- Legacy `DELETE .../term` `{phrase}` hard-deletes active manual matches (204,
  with no body, so rebuild status and warnings are not reported);
  auto or suggested matches are 409 `TERM_NOT_MANUAL`. An unmigratable legacy
  item keeps the pre-v2 update; v2 writes on it are 409
  `LEGACY_DICTIONARY_CONFLICT`. A manual row changed concurrently during a legacy
  PUT or DELETE is 409 `VERSION_CONFLICT`; 409 `CONFLICT` only reports a legacy
  item that kept changing during migration.
- Vocabularies (ADR-008 A/B builder amendment): the GET and every
  recognition-affecting manual write (one interval bypass each) call
  `EnsureVocabularies`, which polls, flips or starts the personal ko-KR build
  (en-US is deferred and reports `NONE`); failures are `warnings` and the
  active vocabulary keeps serving.
  `vocabularies: {ko, en}` gives `{status, activeReady, failureCode?, termCount}`
  per language (`status` `READY|BUILDING|FAILED|QUOTA_EXCEEDED|NONE` is the
  latest build; `failureCode` `PROVIDER|QUOTA`). Legacy `status` is `PENDING`
  while a ko-KR build is in flight (so the settings UI keeps polling the GET,
  which flips it), otherwise `READY` whenever the active vocabulary is ready.
  No response carries a
  vocabulary name yet: the live client would send a ko-KR-only
  `VocabularyNames` on its ko-KR/en-US stream (ADR-008 acceptance item 5).
  The builder replaces the earlier in-place rebuild of the derived ko-KR
  vocabulary, including its pending-build deferral warning and bounded
  conflict recomputation; that path no longer exists.

### Index status and automatic indexing rollout

Authenticated GET index-status routes cover meetings, personal/shared documents,
and account documents. Current access is checked before foreign source reads.
Responses expose only `{state,errorCode?,updatedAt?}` with `no-store`; states are
`UNTRACKED`, `PENDING`, `PREPARING`, `WAITING_SYNC`, `WAITING_SOURCE`, `INDEXED`,
`FAILED`, `DELETED`. Unavailable configuration/reads return 503 `INDEX_UNAVAILABLE`,
never successful indexing. UNTRACKED is not proof that no legacy vector exists.

The canonical worker can index current USER#/MEETING#, USER#/DOC#, and
ACCOUNT#/DOC# sources into immutable `canonical/v1/` projections. Revisions bind
present fields and exact S3 ETag/version/size/preview provenance. Notifications
are identities to reread, not source snapshots. Full S3 ingestion is coalesced;
job acceptance is not success. Per-document status, projection inventory and fresh
source/conditional checks determine completion. There is no direct ingestion.

The activation configuration selects `INDEXING_MODE=all` with its one-minute
schedule enabled, adding canonical stream delivery and permissions after the
recorded consumer qualification. Configuration alone is not deployment evidence.
The preceding manual-only stage bootstraps
private `kb/{owner}/...` and authenticated-global `shared/**` originals into
`manual-kb/v1/` and `shared-kb/v1/` snapshots without altering originals or
canonical/legacy meeting exports. Private/shared visibility must remain distinct.
PDF/DOC/DOCX/XLS/XLSX snapshot support differs from attachment extraction;
PPT/PPTX, empty or over-50-MiB originals have explicit failure states.

Rollout: deploy the mode-aware worker with delivery off; verify restricted
manual-only IAM/configuration, then enable its schedule; verify snapshots and
synthetic recall; deploy/verify strict current-source QA; finally enable all-mode
canonical delivery. Durable mode rejects an all-to-manual-only downgrade; restore
all after a mistaken downgrade. Legacy QA rollback after canonical cleanup needs
re-export. Existing `/api/kb/*` routes remain in the API Lambda; `cmd/kb` accepts
stream/schedule/tick envelopes, not API proxy requests. Follow the
[bootstrap runbook](runbooks/knowledge-index-bootstrap.md) and
[source/provider contract](../backend/internal/service/INDEX_SOURCE_CONTRACT.md).

## Go route inventory

Method/path registration is authoritative; the handler column identifies the
entry point for exact request validation, response types and service permissions.
The Go inventory in this section comes from `backend/cmd/api/main.go`.

<!-- BEGIN GO ROUTES -->
| Method | Path | Handler |
|---|---|---|
| GET | `/api/health` | `healthHandler.Health` |
| GET | `/api/auth/allowed-domains` | `settingsHandler.GetAllowedDomains` |
| GET | `/api/public/docs/{token}` | `documentHandler.PublicGetDoc` |
| POST | `/api/session/bootstrap` | `sessionHandler.Bootstrap` |
| POST | `/api/billing-insight/handoff` | `billingInsightHandler.CreateHandoff` |
| GET | `/api/accounts` | `accountHandler.ListAccounts` |
| GET | `/api/navigation/accounts` | `accountCatalogHandler.Get` |
| POST | `/api/accounts` | `accountHandler.CreateAccount` |
| GET | `/api/accounts/{accountId}` | `accountHandler.GetAccount` |
| GET | `/api/accounts/{accountId}/reading` | `accountReadingHandler.Get` |
| PUT | `/api/accounts/{accountId}/parent` | `accountHandler.UpdateAccountParent` |
| POST | `/api/accounts/{accountId}/members` | `accountHandler.AddMember` |
| DELETE | `/api/accounts/{accountId}/members/pending` | `accountHandler.RevokePendingMember` |
| PUT | `/api/accounts/{accountId}/members/{userId}` | `accountHandler.UpdateMemberRole` |
| DELETE | `/api/accounts/{accountId}/members/{userId}` | `accountHandler.RemoveMember` |
| GET | `/api/accounts/{accountId}/meetings` | `accountHandler.ListAccountMeetings` |
| GET | `/api/accounts/{accountId}/insights` | `accountHandler.ListAccountInsights` |
| GET | `/api/accounts/{accountId}/brief` | `accountHandler.GetAccountBrief` |
| GET | `/api/accounts/{accountId}/research` | `researchHandler.ListAccountResearch` |
| GET | `/api/accounts/{accountId}/projects` | `projectHandler.ListAccountProjects` |
| POST | `/api/accounts/{accountId}/documents` | `accountHandler.PutDocument` |
| GET | `/api/accounts/{accountId}/documents` | `accountHandler.ListDocuments` |
| GET | `/api/accounts/{accountId}/documents/{docId}` | `accountHandler.GetDocument` |
| GET | `/api/accounts/{accountId}/documents/{docId}/reading` | `documentReadingHandler.Get` |
| GET | `/api/accounts/{accountId}/documents/{docId}/index-status` | `indexStatusHandler.AccountDocument` |
| PUT | `/api/accounts/{accountId}/documents/{docId}` | `accountHandler.UpdateDocument` |
| DELETE | `/api/accounts/{accountId}/documents/{docId}` | `accountHandler.DeleteDocument` |
| POST | `/api/documents` | `documentHandler.PutDocument` |
| GET | `/api/documents` | `documentHandler.ListDocuments` |
| GET | `/api/document-catalog` | `documentCatalogHandler.Get` |
| GET | `/api/documents/{docId}` | `documentHandler.GetDocument` |
| GET | `/api/documents/{docId}/reading` | `documentReadingHandler.Get` |
| GET | `/api/documents/{docId}/index-status` | `indexStatusHandler.PersonalDocument` |
| PUT | `/api/documents/{docId}` | `documentHandler.UpdateDocument` |
| DELETE | `/api/documents/{docId}` | `documentHandler.DeleteDocument` |
| POST | `/api/documents/{docId}/share-account` | `documentHandler.ShareToAccount` |
| POST | `/api/documents/{docId}/share` | `documentHandler.ShareWithUser` |
| GET | `/api/documents/{docId}/shares` | `documentHandler.ListShares` |
| DELETE | `/api/documents/{docId}/share/{userId}` | `documentHandler.RevokeShare` |
| POST | `/api/documents/{docId}/public-share` | `documentHandler.CreatePublicShare` |
| DELETE | `/api/documents/{docId}/public-share` | `documentHandler.RevokePublicShare` |
| GET | `/api/vault/export` | `vaultHandler.ExportVault` |
| GET | `/api/vault/reading` | `vaultReadingHandler.Get` |
| POST | `/api/meetings/{meetingId}/account` | `meetingHandler.LinkToAccount` |
| POST | `/api/meetings/{meetingId}/share-account` | `shareHandler.ShareToAccount` |
| GET | `/api/meetings` | `meetingHandler.ListMeetings` |
| GET | `/api/meeting-catalog` | `meetingCatalogHandler.Get` |
| POST | `/api/meetings` | `meetingHandler.CreateMeeting` |
| GET | `/api/meetings/{meetingId}` | `meetingHandler.GetMeeting` |
| GET | `/api/meetings/{meetingId}/reading` | `readingHandler.Get` |
| POST | `/api/meetings/{meetingId}/translation` | `meetingTranslationHandler.Post` |
| GET | `/api/meetings/{meetingId}/action-items` | `actionItemsHandler.Get` |
| GET | `/api/meetings/{meetingId}/resummary` | `resummaryHandler.Get` |
| POST | `/api/meetings/{meetingId}/resummary` | `resummaryHandler.Request` |
| GET | `/api/meetings/{meetingId}/attachments/{attachmentId}/text/status` | `attachmentTextHandler.Status` |
| POST | `/api/meetings/{meetingId}/attachments/{attachmentId}/text/retry` | `attachmentTextHandler.Retry` |
| GET | `/api/meetings/{meetingId}/attachments/{attachmentId}/text` | `attachmentTextHandler.Read` |
| GET | `/api/meetings/{meetingId}/index-status` | `indexStatusHandler.Meeting` |
| POST | `/api/meetings/{meetingId}/action-items/retry` | `actionItemsHandler.Retry` |
| PUT | `/api/meetings/{meetingId}/action-items/{itemId}` | `actionItemsHandler.SetCompleted` |
| PUT | `/api/meetings/{meetingId}` | `meetingHandler.UpdateMeeting` |
| DELETE | `/api/meetings/{meetingId}` | `meetingHandler.DeleteMeeting` |
| GET | `/api/meetings/{meetingId}/audio` | `meetingHandler.GetAudioURL` |
| POST | `/api/meetings/{meetingId}/audio/crop` | `meetingHandler.CropAudio` |
| POST | `/api/meetings/{meetingId}/recover` | `meetingHandler.RecoverMeeting` |
| POST | `/api/meetings/{meetingId}/rediarize` | `meetingHandler.RediarizeMeeting` |
| POST | `/api/meetings/{meetingId}/sim/extract` | `simHandler.ExtractRequirements` |
| POST | `/api/meetings/{meetingId}/sim` | `simHandler.CreateSimulation` |
| PUT | `/api/meetings/{meetingId}/transcript` | `meetingHandler.SelectTranscript` |
| PUT | `/api/meetings/{meetingId}/speakers` | `meetingHandler.UpdateSpeakers` |
| POST | `/api/meetings/{meetingId}/link` | `meetingHandler.LinkMeetings` |
| POST | `/api/meetings/{meetingId}/share` | `shareHandler.ShareMeeting` |
| DELETE | `/api/meetings/{meetingId}/share/pending` | `shareHandler.RevokePendingShare` |
| DELETE | `/api/meetings/{meetingId}/share/{userId}` | `shareHandler.RevokeShare` |
| GET | `/api/users/search` | `shareHandler.SearchUsers` |
| POST | `/api/upload/presigned` | `uploadHandler.GetPresignedURL` |
| POST | `/api/upload/complete` | `uploadHandler.UploadComplete` |
| POST | `/api/kb/upload` | `kbHandler.GetPresignedURL` |
| POST | `/api/kb/sync` | `kbHandler.SyncKB` |
| POST | `/api/kb/copy-attachment` | `kbHandler.CopyAttachment` |
| GET | `/api/kb/files` | `kbHandler.ListFiles` |
| GET | `/api/kb/files/reading` | `kbHandler.ReadFiles` |
| DELETE | `/api/kb/files/{fileId}` | `kbHandler.DeleteFile` |
| POST | `/api/meetings/{meetingId}/export` | `exportHandler.ExportMeeting` |
| GET | `/api/meetings/{meetingId}/export/obsidian` | `exportHandler.ExportObsidian` |
| GET | `/api/settings/integrations` | `settingsHandler.GetIntegrations` |
| GET | `/api/connectors` | `connectorSettingsHandler.List` |
| PUT | `/api/connectors/approval` | `connectorSettingsHandler.UpdateApproval` |
| PUT | `/api/settings/integrations/notion` | `settingsHandler.SaveNotionKey` |
| DELETE | `/api/settings/integrations/notion` | `settingsHandler.DeleteNotionKey` |
| PUT | `/api/settings/allowed-domains` | `settingsHandler.SaveAllowedDomains` |
| PUT | `/api/settings/connector-policy` | `connectorSettingsHandler.UpdatePolicy` |
| POST | `/api/settings/invite-user` | `settingsHandler.InviteUser` |
| GET | `/api/settings/users` | `userAdminHandler.ListUsers` |
| DELETE | `/api/settings/users/{userId}` | `userAdminHandler.DeleteUser` |
| PUT | `/api/settings/users/{userId}/enable` | `userAdminHandler.EnableUser` |
| PUT | `/api/settings/users/{userId}/disable` | `userAdminHandler.DisableUser` |
| POST | `/api/settings/users/{userId}/resend-invite` | `userAdminHandler.ResendInvite` |
| POST | `/api/settings/users/{userId}/reset-password` | `userAdminHandler.ResetPassword` |
| GET | `/api/settings/dictionary` | `dictHandler.GetDictionary` |
| PUT | `/api/settings/dictionary` | `dictHandler.UpdateDictionary` |
| DELETE | `/api/settings/dictionary/term` | `dictHandler.DeleteTerm` |
| POST | `/api/settings/dictionary/terms` | `dictHandler.CreateTerm` |
| PATCH | `/api/settings/dictionary/terms/{termId}` | `dictHandler.UpdateTerm` |
| DELETE | `/api/settings/dictionary/terms/{termId}` | `dictHandler.DeleteDictTerm` |
| POST | `/api/translate` | `translateHandler.Translate` |
| POST | `/api/meetings/{meetingId}/summarize` | `summarizeLiveHandler.SummarizeLive` |
| GET | `/api/crawler/sources` | `crawlerHandler.ListSources` |
| POST | `/api/crawler/sources` | `crawlerHandler.AddSource` |
| PUT | `/api/crawler/sources/{sourceId}` | `crawlerHandler.UpdateSource` |
| DELETE | `/api/crawler/sources/{sourceId}` | `crawlerHandler.Unsubscribe` |
| GET | `/api/crawler/sources/{sourceId}/history` | `crawlerHandler.GetHistory` |
| GET | `/api/insights` | `insightsHandler.ListInsights` |
| GET | `/api/insights/{sourceId}/{docHash}` | `insightsHandler.GetDocumentContent` |
| DELETE | `/api/insights/{sourceId}/{docHash}` | `insightsHandler.DeleteDocument` |
| POST | `/api/research` | `researchHandler.CreateResearch` |
| GET | `/api/research` | `researchHandler.ListResearch` |
| GET | `/api/research/{researchId}` | `researchHandler.GetResearchDetail` |
| PUT | `/api/research/{researchId}` | `researchHandler.UpdateResearch` |
| DELETE | `/api/research/{researchId}` | `researchHandler.DeleteResearch` |
| POST | `/api/research/{researchId}/restore` | `researchHandler.RestoreResearch` |
| POST | `/api/research/{researchId}/export` | `researchHandler.ExportResearch` |
| POST | `/api/research/{researchId}/share` | `researchShareHandler.ShareResearch` |
| DELETE | `/api/research/{researchId}/share/{userId}` | `researchShareHandler.RevokeResearchShare` |
| POST | `/api/research/{researchId}/accounts` | `researchHandler.LinkAccount` |
| DELETE | `/api/research/{researchId}/accounts/{accountId}` | `researchHandler.UnlinkAccount` |
| POST | `/api/projects` | `projectHandler.CreateProject` |
| GET | `/api/projects` | `projectHandler.ListMyProjects` |
| GET | `/api/navigation/projects` | `projectCatalogHandler.Get` |
| GET | `/api/projects/{projectId}` | `projectHandler.GetProject` |
| GET | `/api/projects/{projectId}/reading` | `projectReadingHandler.Get` |
| PUT | `/api/projects/{projectId}` | `projectHandler.UpdateProject` |
| DELETE | `/api/projects/{projectId}` | `projectHandler.DeleteProject` |
| POST | `/api/projects/{projectId}/members` | `projectHandler.AddMember` |
| GET | `/api/projects/{projectId}/members/pending` | `projectHandler.ListPendingMembers` |
| DELETE | `/api/projects/{projectId}/members/pending` | `projectHandler.RevokePendingMember` |
| DELETE | `/api/projects/{projectId}/members/{userId}` | `projectHandler.RemoveMember` |
| POST | `/api/projects/{projectId}/accounts` | `projectHandler.LinkAccount` |
| DELETE | `/api/projects/{projectId}/accounts/{accountId}` | `projectHandler.UnlinkAccount` |
| POST | `/api/projects/{projectId}/meetings` | `projectHandler.LinkMeeting` |
| DELETE | `/api/projects/{projectId}/meetings/{meetingId}` | `projectHandler.UnlinkMeeting` |
| POST | `/api/projects/{projectId}/research` | `projectHandler.LinkResearch` |
| DELETE | `/api/projects/{projectId}/research/{researchId}` | `projectHandler.UnlinkResearch` |
| GET | `/api/projects/{projectId}/meetings` | `projectHandler.ListProjectMeetings` |
| GET | `/api/projects/{projectId}/research` | `projectHandler.ListProjectResearch` |
| GET | `/api/projects/{projectId}/insights` | `projectHandler.GetProjectInsights` |
| GET | `/api/projects/{projectId}/brief` | `projectHandler.GetProjectBrief` |
| GET | `/api/research/{researchId}/chat` | `researchChatHandler.ListMessages` |
| POST | `/api/research/{researchId}/chat` | `researchChatHandler.SendMessage` |
| GET | `/api/research/{researchId}/subpages` | `researchChatHandler.ListSubPages` |
| GET | `/api/chat/sessions` | `chatHandler.ListSessions` |
| DELETE | `/api/chat/sessions/{sessionId}` | `chatHandler.DeleteSession` |
<!-- END GO ROUTES -->

## Crawler aliases and publication history

Crawler source POST and PUT bodies are limited to 64 KiB of valid UTF-8 JSON.
Optional `aliases` add public company names to the shared source identity. Each
trimmed name contains 1–100 Unicode code points, without control or format
characters. Names are deduplicated case-insensitively, preserving the existing
spelling; the combined source is limited to 20 aliases. Conditional updates
re-read and enforce that combined limit after concurrent changes. Invalid
additions return 400 before changing the user's subscription.

Aliases are shared by all subscribers, may enter external news searches, and
remain after unsubscribing. PUT is additive: omitting aliases or sending an empty
list leaves existing aliases unchanged. Optional PUT `removeAliases` removes only
listed names and requires the current source owner; other subscribers receive 403.
Removals use the same name validation and case-insensitive comparison, free the
shared capacity, and preserve other names added concurrently. Both current owner
and the observed alias list guard the conditional write; retries recheck ownership.
The UI exposes saved-name deletion only to the owner. Existing subscribers may add
aliases; source ownership is unchanged. Subscription storage
and the shared alias update are separate operations: a storage failure is
reported, and retrying the same additions/removals is safe.

News publication history optionally includes `runId`, `status`, `queryCount`,
rejection/attempt counters, `workerOutcomes`, and `fetchWarnings`. `chargedUsd`
is an exact decimal string, including conservative reservations for unknown
usage; `usageComplete: false` distinguishes that case from a verified total.
Legacy history omits these diagnostics. These fields describe recorded runs,
not deployment or consumer acceptance.

## Python QA and WebSocket

HTTP QA routes are registered in `infra/lib/gateway-stack.ts` and handled by
`backend/python/qa/handler.py`. `backend/cmd/websocket/main.go` handles the
WebSocket connection and message routes.

| Transport | Route/action | Implementation |
|---|---|---|
| HTTP POST | `/api/qa/ask` | Agentic general Q&A |
| HTTP POST | `/api/qa/meeting/{meetingId}` | Authorized meeting-context Q&A |
| HTTP POST | `/api/qa/jobs` | User-bound idempotent asynchronous QA submission (202) |
| HTTP GET | `/api/qa/jobs/{jobId}` | Current-source-validated job status/result |
| HTTP POST | `/api/qa/detect-questions` | Suggested/proactive question detection |
| WebSocket | `$connect`, `$disconnect`, `$default` | Go websocket Lambda |
| WebSocket message | `ask_live` | Async invocation of Python QA, streamed replies |

The frontend selects jobs only when runtime `qaAsyncJobs` is exactly true;
the default remains synchronous and preserves `QAResponse`. Job activation is a
separate change after backend acceptance. The [async contract](../backend/python/qa/ASYNC_CONTRACT.md)
defines identity, proof/byte/deadline limits and unknown-outcome reconciliation.

WebSocket clients resolve runtime `wsUrl: "/ws"` against the current CloudFront
site and use `wss://` in production. No direct execute-api fallback is accepted.
CloudFront forwards the query token, rewrites the path to the existing production
stage, and injects a private origin proof. `$connect` requires that proof and a
verified Cognito JWT through the dedicated Go Lambda authorizer; it is not a
Cognito HTTP JWT-authorizer attachment. The public config contains no proof or
secret ARN. Invalid/missing config or a failed initial connection uses the existing
REST fallback. There is no current start/audio/stop
server-side transcription stream or separate connections table in this handler.
Live transcription runs in the browser using AWS Transcribe Streaming.
See [WS deployment and acceptance](runbooks/websocket-runtime.md); synth and mock
tests do not establish a live WebSocket connection.

**Legacy behavior before current-source cutover:** the QA handler uses a Converse
tool loop and current authorized meeting data. KB meeting hits are candidates;
access and saved notes/content are reread, including on cache hits. New terms
may wait for export/ingestion. This paragraph is not the current-source contract.
Both transports send bounded, separate untrusted transcript and saved-note
excerpts. Follow `get_meeting_detail`'s next offset, not an excerpt-relative
position; transcript read failures return errors rather than silently losing context.

### Current-source consumer contract

Helper installation alone does not activate this consumer contract. The
[source contract](../backend/python/qa/SOURCE_CONTRACT.md) states whether the
reviewed handler registers it. That status does not certify deployment; the
[manual bootstrap evidence](research/evaluations/2026-09-13-manual-kb-bootstrap/README.md)
records IAM producer/provider checks only. Public QA acceptance remains separate.

Live/HTTP requests pass current client input separately from saved notes. Live
input receipts bind user and meeting; growing, rolling and corrected windows
retain conversation while the latest input takes priority. They do not attest
saved-source bytes. Every recorded server source still requires current access
and revision; changes or revocation invalidate the complete derived history.

- Fresh source discovery revalidates canonical access, exact source revision and
  S3 bindings. Saved-text keyword matches supplement index lag; legacy meeting
  exports provide identities only. Cached text/misses cannot replace fresh reads.
  Source discovery adds no reusable query-result cache. Separately scoped async jobs
  retain bounded results and revalidate their proof on every result read.
- Private/manual and authenticated-shared binary evidence requires matching
  immutable snapshots; old unbound chunks cannot be relabeled as current. Legacy
  text is read from current scoped bytes; continuations bind its revision.
  Source failures are errors, and unavailable binaries expose pending/failed state.
- Add optional `sourceDetails` while retaining `answer`, `sources`, `usedKB`,
  `usedDocs`, and `toolsUsed`. Explicit public fields describe identity, title,
  revision, evidence origin, partial/file-pending/migration status, and attachment
  attempt/result/location. Do not forward arbitrary provider metadata or invent
  audio timestamps for document positions. Public titles have a presentation bound
  and expose `titleTruncated:true` when shortened; source arrays remain complete.
- No-tool follow-ups restore source attribution only after live dependency
  validation. `provenanceScope: validated_history` identifies historical evidence,
  not a new retrieval. Valid older sessions retain dialogue with explicit
  `legacy_identity` references when detailed metadata is unavailable. Changed or
  revoked dependencies clear both history and attribution. Research creation
  receipts use `history_receipt`; inventory validation receipts are not citations.
  Equivalent fresh attribution replaces restored attribution. See the source
  contract for metadata/identity bounds and incomplete-attribution markers.
- Replay requires current access/revisions for every source plus matching
  fingerprints for supported read-only tools. Strict callbacks consume all pages,
  recheck exact membership/canonical references and attest complete reads.
  Changed, denied, failed or untracked dependencies invalidate the entire history,
  including assistant paraphrases; recorded dependencies must be checked before
  each model round and final output.
- Final validation runs before persistence and again before publication. HTTP
  source failures use `SOURCE_CHANGED` (409) or `SOURCE_UNAVAILABLE` (503);
  WebSocket failures use `answer_error`. Oversized/rejected completions report
  `RESPONSE_TOO_LARGE` or `DELIVERY_FAILED`, not a shortened successful result.
  Large source arrays arrive in bounded `answer_sources` frames, followed by
  `answer_complete` with matching `sourceBatchId` and `sourceBatchCount`. Clients
  validate and assemble the entire source batch before presenting completion.
  Small responses retain the existing one-frame shape.
  Clients request this protocol with `sourceFramesVersion:1` on `ask_live`.
  Without that opt-in, oversized responses retain the existing size error.
- WebSocket `ask_live` reports model stream failures regardless of the
  `sourceFramesVersion` opt-in. Empty or incomplete model streams report
  `MODEL_STREAM_EMPTY` or `MODEL_STREAM_INCOMPLETE`. Request/iteration failures
  report `MODEL_STREAM_UNAVAILABLE`; exhausting the tool
  round budget reports `MODEL_TOOL_ROUND_LIMIT`. These are failed responses,
  not successful empty answers or automatic retries. Source-validated completed
  tool rounds can be retained with an explicit interruption note for the next
  conversation turn, including when stream iteration raises.
  The error frame's `sessionContinuable` boolean states whether the client can
  retain the conversation after closing the socket. A missing session ID or a
  needed but unconfirmed history write sets it false. This signal does not
  authorize automatic retries.
  See the source contract's completion limits and non-atomic streaming caveat.
- `start_research` records a creation receipt only after one successful mutation.
  Never replay creation to validate history. Tracking overflow preserves the
  current result while marking history nonreplayable with explicit coverage.
  Unverifiable legacy sessions reset at cutover. Successful empty KB searches
  are reexecuted for the current user; new matches or failed reads invalidate
  their proofs. Failed/skipped private reads never become empty successes.
- `toolHistoryCoverage` is normally `[]`; `{tool, complete:false, reason}` reports
  `DEPENDENCY_LIMIT`, `RESULT_LIMIT` or `RECEIPT_UNAVAILABLE`. Capacity exhaustion
  keeps the current read-time-authorized result but prevents history replay.

Exact source fields, visibility, limits and integration APIs:
[source contract](../backend/python/qa/SOURCE_CONTRACT.md),
[tool history](../backend/python/qa/TOOL_HISTORY_CONTRACT.md),
[account reads](../backend/python/qa/ACCOUNT_READS_CONTRACT.md), and
[rollout](runbooks/qa-current-source-rollout.md).

Manual and opt-in proactive QA may send model-composed queries to the external
web-search provider through the us-east-1 Gateway. The UI toggle only gates the
proactive path. Query construction restrictions and hash-only logging mitigate,
but do not eliminate, that egress. The per-user hourly limit runs before the call
(default 30, 0 disables), deliberately fails open on storage errors, and does not
meter crawler/research. Unconfigured web search returns a tool error. Session
history has TTL attributes, but the current table TTL setting does not sweep them.

## Errors and implementation references

HTTP errors use `{ "error": { "code": "...", "message": "..." } }`. Common codes
include BAD_REQUEST, FORBIDDEN, NOT_FOUND and CONFLICT; inspect each handler for
its exact mapping. Services use sentinel errors and callers use errors.Is.

- Request/response and key types: `backend/internal/model/`.
- HTTP decoding/status mapping: `backend/internal/handler/`.
- Authorization/business rules: `backend/internal/service/`.
- Persistence, pagination, conditional writes: `backend/internal/repository/`.
- Transport routing/authorizers/events: `infra/lib/gateway-stack.ts`.
- Frontend client contracts: `frontend/src/lib/api.ts`.

## Meeting attachment text

Owner-only upload completion creates canonical metadata and requests extraction
for PDF/PPTX/DOCX/MD. A stored original remains HTTP 200 when unsupported format
or publication failure has durable `textExtraction` failure metadata. Failed-state
persistence errors remain errors. Retry still returns errors when work cannot queue.
`POST /api/upload/complete` uses fixed error messages: 400 invalid input, 403 denied,
404 missing meeting/source, 409 concurrent source change and 500 storage/event failure
without a durable failure state. Its success response remains `{"status":"processing"}`.
Single-file audio completion is idempotent per key: completing the key a meeting
already holds succeeds without changing its status, so a retry after a lost
response never moves a transcribed or finished meeting back to `transcribing`. A
different key still replaces the audio and restarts transcription. The key check
is part of the conditional write (`BindMeetingAudioKey`).

### GET /api/meetings/{meetingId}/attachments/{attachmentId}/text/status

Current meeting readers receive HTTP 200. No query fields are accepted.
Status also appears as `Attachment.textExtraction`:
`{status,runId?,errorCode?,leaseUntil?,updatedAt?,unitCount,complete,hasResult,needsResummary,summaryExcerpted}`.
States: unknown/queued/running/succeeded/partial/failed. Expiry is interrupted
failure; retained results never imply current success.

```json
{"status":"failed","errorCode":"PUBLISH_FAILED","unitCount":0,"complete":false,"hasResult":false,"needsResummary":false,"summaryExcerpted":false}
```

`STATUS_UNAVAILABLE` means status lookup failed; unknown/absence never implies success.

### POST /api/meetings/{meetingId}/attachments/{attachmentId}/text/retry

Owners/editors may retry supported documents, including legacy unknown state.
No query fields are accepted. HTTP 202 returns the same status shape, normally queued.
An unsupported retry returns 422 `UNSUPPORTED_FORMAT`; publication/state errors remain errors.

### GET /api/meetings/{meetingId}/attachments/{attachmentId}/text

Optional query: `pageSize=3000&cursor=...`. Unknown/duplicate query fields fail.
Text: `{analysis,current,source,format,scope,complete,warningCount,units,nextCursor?,pageComplete}`.
Units carry exact Unicode offsets and parser locations. pageSize is 1–6000,
response ≤14,000 bytes (`AttachmentTextPageLimit`) including newline, ≤50 units. Current auth/source/run/ETag
is revalidated; stale cursors conflict. Errors: 400 query, 403/404 access/source,
409 `CONFLICT` for stale source/cursor, 409 `TEXT_UNAVAILABLE` for unavailable
verified result text (including S3 HEAD/GET failures), and 422 `UNSUPPORTED_FORMAT`
for unsupported input. Other metadata/internal failures return 500. A page with
`current:false` is retained historical evidence.

Source conflicts preserve text and mark fresh-generation retry; unrelated metadata
changes do not invalidate generation. No source/model text appears in errors.
Deployment prerequisites and runtime acceptance are recorded in
[the rollout runbook](runbooks/meeting-document-release.md).

## Re-summary from saved sources

| Method | Path | Result |
| --- | --- | --- |
| GET | `/api/meetings/{meetingId}/resummary` | Current meeting readers; small status response |
| POST | `/api/meetings/{meetingId}/resummary` | Owner/edit; empty body or `{}`; owner-only `{sharingSafe:true}` with activated privacy writers; `202` with status |

Response: `{status, runId?, errorCode?, leaseUntil?, updatedAt?, resultHash?, sharingSafe?}`.
Both routes reject query strings with 400. POST accepts no caller source fields
and caps its body at 1 KiB.
Status is `unknown`, `queued`, `running`, `succeeded`, or `failed`; lease is epoch
milliseconds and updatedAt is RFC3339. An active run is reused. Expired work is
persisted as `failed/INTERRUPTED`. A successful resultHash is the SHA-256 of the
saved summary text.

The explicit owner `sharingSafe` mode uses the selected saved transcript, saved
notes and verified current DOCUMENT extraction. It excludes predecessor and prior
generated/image/preview evidence. Mode is persisted and pinned through delivery.
Success atomically publishes safe provenance, resets tasks/insights/tags/sentiment/
live draft, and deletes action-analysis and simulator state. Source/edit-grant CAS
preserves concurrent human edits; failures retain old content and provenance.
Task continuity stores only bounded hashes, IDs and completion, never old task
prose. Task and other model publications pin the generation/source they consumed;
account insight copies also fence canonical publication and insight revision.

This differs from POST `/summarize`, which is live, caller-text summarization.
That route requires owner or `edit` access to `{meetingId}` before any model call
(404 without access, 403 read-only) and caps the body at 1 MiB. Recorders send the
server draft meeting ID. A browser-only `meeting_<epoch ms>` ID is still accepted
without a meeting check during the frontend rollout; remove that allowance once the
updated frontend is deployed.
Re-summary reads saved notes/summary, the current selected transcript and
verified document extraction, and writes notes in the meeting's stored
`noteLanguage`; the POST body never selects a language. Change the language
through the meeting update first, then request a re-summary. It does not rerun STT or refinement and does not
import linked-meeting context. Unavailable documents are omitted with notices
when trusted notes/transcript remain. If no other source exists, pending/failed/
missing document text returns `409 SOURCE_NOT_READY`; active transcription returns `409 MEETING_BUSY`;
absent source returns `409 NO_SUMMARY_SOURCE`. Synchronous source/capture limits
return 413 and publish failures return 503. After a 202, source conflicts and
output limits are reported through failed status; oversized output uses
`OUTPUT_TOO_LARGE`. Raw source/model text is
never included in error responses.

State is separate at `MEETING#id / ANALYSIS#summary`. The worker revalidates the
requester's edit grant, source fields, attachment inventory and object ETags.
Summary/coverage and success are published atomically under source/run/lease
conditions. Concurrent human changes reject the generated result. Failure keeps
the previous summary. Publication uses one SDK attempt; condition/validation
rejections and canceled transactions clean up only newly created transcript
spills. Other database failures retain spills because publication may be
ambiguous; cleanup errors remain visible.
Inputs are limited to 20 attachments and 8 MiB per loaded
transcript field; document evidence is fairly excerpted within 64 KiB total.
Unprovided/partial evidence is explicitly marked.

Delivery requires host infrastructure to route
`source=ttobak.analysis, detail-type=SummaryRequested`, detail `{meetingId,runId}`,
to the existing summarize Lambda. The API uses its existing default-bus PutEvents
grant. Deploy that rule before enabling this action.

The separate frontend change is staged until these APIs are deployed. Its contract
polls metadata while pending and loads completed content through
`/api/meetings/{id}/reading?kind=meeting&section=summary`, checking page continuity,
current run and resultHash without replacing an unsaved editor draft.
