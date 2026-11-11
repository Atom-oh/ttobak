# API reference

Current route/contract map, verified against `backend/cmd/api/main.go`, Go
handlers/models/services, `backend/python/qa/handler.py`, and GatewayStack.
This document describes repository code; it does not certify deployed behavior.

## Private calendar

The caller's private calendar is separate from meetings. These authenticated
Go routes are implemented in `backend/internal/handler/calendar.go`, with
validation/encryption in `backend/internal/service/calendar.go` and wire types
in `backend/internal/model/calendar.go`. They use the existing CloudFront API
and verified caller identity, never a payload `userId`. They are not delegated
connector operations; generic and private HTTP MCP both exclude calendar tools.
Local stdio exports exactly `ttobak_sync_calendar_events`,
`ttobak_list_calendar_events`, and `ttobak_calendar_sync_status`.
No endpoint fetches Outlook, schedules work, reconciles a provider or creates a
meeting. See the [local reader runbook](runbooks/local-outlook-calendar-sync.md).

| Route | Request | Success |
|---|---|---|
| `POST /api/calendar/sources/{sourceId}/sync` | Nonzero UUID path; JSON `batchId`, `expectedRevision`, `windowStart`, `windowEnd`, `complete`, `mutations` | `200` receipt: `batchId`, `revision`, `upserted`, `removed`, `completed` |
| `GET /api/calendar/events` | Required `from`, `to`; optional `accountId`, `limit`, `cursor` | `{events:[...],nextCursor:string|null}` |
| `GET /api/calendar/sources` | Optional `limit`, `cursor` | `{sources:[...],nextCursor:string|null}` |

Sync JSON is limited to 64 KiB. Unknown, duplicate or incorrectly cased fields,
null values, invalid types and unsupported query parameters are rejected.
`batchId` is a nonzero UUID; `expectedRevision` is a nonnegative integer matching
the source's current revision (zero for a new source). The local MCP adapter
additionally requires safe JavaScript revisions, with input maximum
`Number.MAX_SAFE_INTEGER - 1`. Window and event intervals use RFC3339 instants,
have positive duration and are at most 93 elapsed days. An upsert whose
`accountId` names a merging or merged account rejects the batch with 409
`ACCOUNT_MERGING` (ADR-072); reads still return events on a merge tombstone.

Each batch has at most 20 distinct occurrence IDs, with only these mutations:

- `{"op":"upsert","event":{...}}`: required `externalId`, `title`, `startsAt`,
  `endsAt`, `timeZone`; optional `isAllDay`, `startDate`, `endDate`, `location`,
  `organizerDomain`, `accountId`, `shareAccountId`, `shareEvidence`, `candidateAccountIds`,
  `private`, `seriesKey`. IDs/titles are nonblank and at most 512 UTF-8 bytes; location is
  at most 256 and timeZone at most 128. Time zones must be IANA/UTC, not `Local`.
  All-day events require date-only start and exclusive end matching local
  midnight instants; timed events omit those dates. The encoded event payload
  must fit 4096 bytes. Account IDs match `[A-Za-z0-9_-]{1,128}` and require current
  caller access. `organizerDomain` is only the lowercase organizer email domain
  (`^[a-z0-9.-]{1,253}$`); the organizer's name and local part are never sent, and
  `GET /api/calendar/events` returns it when present. Body, attendees, provider
  credentials and arbitrary metadata are not accepted. The request decoder stays
  strict, so an API that predates `organizerDomain` rejects it with 400; deploy
  the API before a runner that sends it. The sealed event plaintext is decoded
  tolerantly of unknown fields. An API that predates `organizerDomain` decodes it
  strictly without that field and fails to read events sealed with it until they
  are re-synced without it or expire; a strict reader that already declares the
  field reads them.
- `{"op":"remove","externalId":"..."}`: explicit removal of that source's
  occurrence. The same ID/text bounds apply. No absence-based deletion occurs,
  including for empty `mutations` with `complete:true`.

Source/event identity is stable across reschedules. The transaction changes
events/time indexes, source revision and receipt together. Identical normalized
batches replay their receipt within its seven-day horizon; a changed payload or
stale source revision returns `409 CONFLICT`. Receipt counts are accepted
mutation counts. On ambiguous delivery retry the identical request, not a new
batch. On a definitive 409 refetch source status and reconcile before new work;
never blindly rebase old mutations. Provider checkpoint ownership is local.

Event responses contain `eventId`, `sourceId`, `provider:"outlook"`, `title`,
`startsAt`, `endsAt`, `timeZone`, `isAllDay`, and optional date/location/`organizerDomain`/account
fields; they omit `externalId`. Instants are normalized to UTC. Event reads use
overlap with the half-open requested window and default/cap at 50 rows.
Source reads default/cap at 20 (query limits 1–50 are accepted, capped to 20).
Sources contain `sourceId`, `provider`, `revision`, `lastAttemptAt`, and optional
`lastCompletedAt`, `lastCompletedRevision`, `completedWindowStart`,
`completedWindowEnd`. A partial accepted batch advances revision while preserving
the last completion. A missing completion revision is unknown; a lower one
means partial progress even if timestamps match. Completion is the caller's
report, not verified provider freshness or evidence of a successful local poll.

Follow actual opaque `nextCursor` values, including empty pages, with unchanged
query scope/limit until null. Cursors cap at 4096 bytes; responses cap at
14,000 encoded bytes and use `Cache-Control: private, no-store`. The MCP adapter
also caps original calendar HTTP bytes at 14,000 and its serialized tool result
at 32,000, returning errors rather than truncating receipts or continuations.
An Account association classifies private data; it grants no sharing. Personal
reads omit a revoked association; an inaccessible Account-filtered read is denied.

Account-sharing fields are validated and stored encrypted with the
event: `shareAccountId` and each of at most five distinct `candidateAccountIds`
use the Account ID format and differ from each other; `shareEvidence` holds up to
three distinct kinds (`title`, `location`, `domain`) and requires
`shareAccountId`; `private` excludes both; `seriesKey` is 64 lowercase hex.
Sync writes a share row only when the user is a direct member of
`shareAccountId`; otherwise it returns `403 FORBIDDEN`. A `Private` event or a
hidden owner override writes a veto row for the matched or previously shared
account instead. Owner
event views report `sharingMode` (`auto`, `account` or `hidden`),
`candidateAccountIds` and `private`; `shareAccountId` and `shareEvidence` appear
only once a stored share is granted.

Rollback boundary: API builds older than this contract reject a stored event
carrying any of these fields as corrupt, failing every event page that contains
it. Do not roll the API Lambda back past this contract once a client has sent
sharing fields.

Errors use `{error:{code,message}}`: `400 BAD_REQUEST` for invalid input/cursor,
`401 UNAUTHORIZED` for missing authentication, `403 FORBIDDEN` for denied access,
`404 NOT_FOUND` for missing resources, `409 CONFLICT` for revision/batch conflict,
`413 BAD_REQUEST` for oversized sync bodies, `413 CALENDAR_BATCH_TOO_LARGE` when a
batch's planned share writes exceed one 100-item transaction (resend fewer
mutations; the intake runner sends at most 12), and `503 INTERNAL_ERROR` when
calendar encryption is unavailable. Other failures use `500 INTERNAL_ERROR`.
Sensitive event payloads use context-bound encryption; event retention is
scheduled at 90 days after event end and receipts at seven days. TTL is
asynchronous, not a deletion SLA. No source-delete endpoint is implemented.
Repository code and synthetic fixtures do not certify deployed/provider
acceptance or the Calendar 5 persisted integration.

## SA performance

Owner-only SA performance data ([ADR-068](decisions/ADR-068-sa-performance-data-residency.md)).
Routes are in `backend/internal/handler/performance.go`, validation in
`backend/internal/service/performance.go`, and wire types in
`backend/internal/model/performance.go`. Every route reads or writes only the
verified JWT caller's own `USER#` partition; no path, query or body field names a
user. No account-membership, admin or Billing read path reaches these rows, and
there is no share action. They are not delegated connector operations. The
server never calls SFDC or other internal MCP servers; only the owner's Mac
runner collects and syncs.

| Route | Request | Success |
|---|---|---|
| `POST /api/performance/sources/{source}/sync` | `source` is `sfdc-assignments` or `sfdc-tasks`; JSON `batchId`, `expectedRevision`, `complete`, `mutations` | `200` receipt: `revision`, `completed`, `upserted`, `removed` |
| `GET /api/performance/sources` | none | `{sources:[{source,revision,lastAttemptAt?,lastCompletedAt?}]}`, one entry per source; never-synced sources report revision 0 |
| `GET /api/performance/goals?year=YYYY` | `year` within previous/current/next year | `{year,text,updatedAt}`; empty text when unset |
| `PUT /api/performance/goals?year=YYYY` | JSON `{text}`, at most 8,000 UTF-8 bytes | saved goals |
| `GET /api/performance/mappings` | none | `{items:[{sfdcAccountId,name,role,mapped,accountId?,manual,reason?,updatedAt?}],truncated}` sorted by SFDC ID; at most 500 items, `truncated:true` when more assignments exist |
| `PUT /api/performance/mappings/{sfdcAccountId}` | JSON `{accountId}` | saved mapping with `manual:true` |
| `GET /api/performance/missed` | optional `period` = `week` (default), `quarter` or `year`; any other value is `400` | `{items:[{kind,accountId,sfdcAccountId?,refKind?,refId?,at?,daysSince}],unmappedCount,sourceFreshness:[{source,lastCompletedAt?}],calendarCoverageStart}` |

Bodies are limited to 64 KiB of strict JSON: unknown, duplicate or null fields,
trailing data and unsupported query parameters are rejected. A sync batch has at
most 20 mutations with distinct IDs. `batchId` is a nonzero UUID (normalized to
lowercase); `expectedRevision` is the source's current revision (zero when new).

- Assignment upsert (`sfdc-assignments` only): `sfdcAccountId`, `name` (at most
  200 bytes) and `role` (at most 64 bytes). An owner keeps at most 500
  assignments: a batch raising the stored count above 500 (current plus new IDs
  minus removes) returns `400 BAD_REQUEST`; updates and removes always pass.
  The check is read-then-write, so concurrent batches may overshoot by 20 each.
- Task upsert (`sfdc-tasks` only): `taskId`, optional `sfdcAccountId` (an
  accountless task is kept and stays unmapped), `activityDate`, `type`,
  `subject` (at most 300 bytes), `status`, optional `specReqId`. Type and
  status are at most 64 bytes. `activityDate` is a real `YYYY-MM-DD` date from
  January 1 of the previous UTC year through one year after today.
  Descriptions and comments are not accepted.
- Remove: `{"op":"remove","externalId":"..."}` for that source's SFDC account
  or task ID.

The mappings list reads at most 501 assignment rows (no cursor), returns the
first 500 (bounded to 500 items), and looks up their mappings by key, so unrelated
retained mapping rows never hide a mapping.

IDs match `[A-Za-z0-9_-]{1,64}`. The service hashes the normalized request
(SHA-256 of its canonical JSON); an identical batch replays its receipt within
seven days, and a reused `batchId` with a different payload or a stale revision
returns `409 CONFLICT`.

A mapping write requires that the caller can currently read the target Account
(`AccountService.GetAccount`; otherwise `403 FORBIDDEN`) and that the SFDC
account is in the caller's own assignments (otherwise `404 NOT_FOUND`). Manual
mappings always win over automatic ones. Unmapped assignments are listed with
`mapped:false`, never guessed. `manual` tells a manual mapping from an automatic
one; putting the same `accountId` confirms an automatic mapping as manual. The
list rechecks every stored mapping against the caller's currently readable
accounts (`ListReadableAccountCandidates`, the same set as `/missed`): a target
the caller can no longer read is reported as `mapped:false` with
`reason:"unreadable"` and no `accountId`. A reader that cannot list readable
accounts fails the request with `500`.

Missed items (`backend/internal/service/performance_missed.go`) are computed at
read time from the caller's own SFDC rows, calendar events (`CalendarService`,
decrypted, owner only) and meetings linked to an account that the caller owns or
received through a share row (`MeetingService.ListMeetingsForAccounts`). The
owned read (tab `all`) and the shared read (tab `shared`) each read their stage
in full, because neither is ordered by meeting date, and filter by date in the
service. Neither read enters the account-team stage. Every SFDC mapping, manual or
automatic, is rechecked against the caller's currently readable accounts;
account names, aliases and domains come only from those accounts.

- Periods use `Asia/Seoul`: `week` is ISO Monday through Sunday, `quarter` the
  calendar quarter, `year` January 1 through now. The evaluated end is capped
  at now.
- A calendar event is attributed by its own readable account link, else a
  unique organizer-domain match (exact or a parent domain containing a dot;
  `amazon.com` and `amazon.co.kr` never match), else a title containing exactly
  one account's name or alias as a whole token sequence. Boundaries are the
  string edges or a rune that is neither a letter nor a digit, so `SK` does not
  match "task". A task maps through its SFDC account.
- `logging_gap`: a calendar or meeting touchpoint in the period, on an account
  with an SFDC mapping, with no SFDC task within ±3 Seoul days. A touchpoint
  that names its SFDC account needs a task on that same SFDC account. A calendar
  or meeting touchpoint knows only the Account, so a task on any SFDC account
  mapped to it closes the gap.
- `silent_customer`: an assigned account (any role) with a readable mapping
  whose last touchpoint is more than 60 days before the period end. With no
  touchpoint in the lookback window, `at` is empty and `daysSince` is `-1`.
- `unmappedCount` counts in-period tasks without a readable mapping and
  in-period calendar events that match no account. They are never guessed.
- At most 500 items, newest `at` first. The calendar read is bounded to 200
  pages and the meeting read to 200; exceeding a bound fails the request rather
  than returning partial results. An API without the calendar and meeting
  readers wired also fails with `500`. `sourceFreshness` lists `sfdc-assignments`,
  `sfdc-tasks` and `calendar` (the latest completed calendar source).
- `calendarCoverageStart` is the earliest start (RFC3339 UTC) among the calendar
  events read for the request, or an empty string when none were read.
  Calendar rows expire 90 days after they end and the runner re-reads only its
  intake window, so calendar-based gaps cover only events from that time on.

Residency: rows use default table encryption without envelope sealing (an
ADR-068 accepted exception). Tasks and goals expire at 00:00 UTC on January 1
two years after their own year through `pendingShareExpiresAt`; batch receipts
expire after seven days. Errors use `{error:{code,message}}` with `400`, `401`,
`403`, `404`, `409`, `413` and `500`, as for the private calendar. Responses use
`Cache-Control: private, no-store`.

## SpecReq tasks

Private per-occurrence task rows for calendar events, implemented in
`backend/internal/handler/specreq_task.go` and
`backend/internal/service/specreq_task.go` (rules in `specreq_task_rules.go`),
with wire types in `backend/internal/model/specreq_task.go`. They use the verified
caller identity only, are not delegated connector operations (`api.specReqTask.list`,
`.get`, `.create`, `.transition` are all `delegationAllowed: false`) and are
absent from generic and private HTTP MCP tools.

| Route | Request | Success |
|---|---|---|
| `GET /api/specreq-tasks` | Optional `limit` 1-20, `cursor` (at most 4096 bytes) | `200` `{tasks:[...],nextCursor?}` |
| `GET /api/specreq-tasks/{eventId}` | `eventId` is 64 lowercase hex | `200` task view |
| `POST /api/specreq-tasks` | JSON `sourceId`, `externalId`, `state`; optional `accountId`, `path`, `counterpartUserId` | `201` task view |
| `POST /api/specreq-tasks/{eventId}/transitions` | JSON `op`, `version`; optional fields per `op` (transition table: `op` names in `model/specreq_task.go`, rules in `specreq_task_rules.go`) | `200` task view |

A task view holds the task fields (`eventId`, `sourceId`, `accountId`, `path`,
`state`, `counterpartUserId`, `specReqId`, `specReqTitle`, `specReqStatus`,
`failureCode`, occurrence and state timestamps, `version`) plus an optional
`event` summary joined from the calendar row. `accountId` is omitted when the
caller can no longer read that Account. Transitions are conditioned on `version`.
Bodies are limited to 4 KiB; unknown fields, trailing data and unsupported query
parameters are rejected, and `eventId` must match `^[0-9a-f]{64}$`.

Privacy: rows never hold attendee addresses, mail, Slack data or SpecReq bodies;
`specReqTitle` is stored encrypted. Rows expire 90 days after the occurrence ends; a transition into a terminal state (`closed`, `resolved`, or `issued` on the `mine` path) of a task with no linked SpecReq caps the expiry at the later of 14 days from that transition and 45 days after the occurrence ends (so the row outlives the runner's matching horizon) and never extends it; linked tasks keep the full retention so the request stays reserved. All responses use `Cache-Control: no-store`.
Errors use `{error:{code,message}}`: `400 BAD_REQUEST` (also `413` for oversized
bodies), `401`, `403 FORBIDDEN`, `404 NOT_FOUND`, `409 CONFLICT` for a version or
duplicate conflict, `503 INTERNAL_ERROR` when encryption is unavailable, and
`500 INTERNAL_ERROR` otherwise.

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

The connector API consumer (ADR-053) is a service-IAM Lambda invocation,
not an additional HTTP route. Its top-level `ttobak.connector.invoke.v1` frame
binds the operation, request ID and exact argument bytes to a signed delegation.
When `CONNECTOR_SIGNING_KEY_ARN` is configured, the consumer loads that exact
public key and reads current client/user grants before and after one of the
bounded API read operations (the eight original read families plus the account
contact list and account event list/get). Only the verified in-process principal reaches
the existing router; caller headers and nested HTTP bodies cannot create it.
Response bodies are capped at 14,000 bytes. Empty key configuration denies the
private consumer. Its mutations are personal note creation, account contact
creation and account event creation, each with its own atomic executor, as
described below.

Delegated contact creation signs `api.accountContacts.create` for `POST
/api/accounts/{accountId}/contacts` with the body `{contact, idempotencyKey}`;
`idempotencyKey` must equal the signed request ID and `contact` takes only the
contact fields above, as strings, without nulls, duplicates or server-owned
fields. The executor checks direct membership and a current read+write approval,
then writes the sealed contact and a receipt
`USER#{userId}/CONNECTOR_MUTATION#{clientId}#{requestId}` in one transaction
with the policy, approval, account and member checks. The receipt holds only the
operation, account, contact ID and revision, approval generation, creation time
and a SHA-256 of the account and exact request body bytes; it has no expiry, so a
retry with the same key and bytes stays idempotent. Responses are
`{contactId, replayed?}` (200); 400 `BAD_REQUEST`, 403 `CAPABILITY_DENIED`, 409
`CONNECTOR_CONFLICT` (same key with different bytes, or a replayed contact that
changed or disappeared), 503 `CONTACTS_UNAVAILABLE` (no key; nothing written)
and 503 `CONNECTOR_WRITE_UNCONFIRMED` (outcome unknown; retry the same key and
bytes). A first create on a merging or merged account returns 409
`ACCOUNT_MERGING` (nothing written); a replay whose receipt exists on such an
account returns 409 `CONNECTOR_REPLAY_ACCOUNT_MERGING` (created before the merge
began; do not recreate). `?replay=1` never creates: it returns an existing receipt or 404
`RECEIPT_NOT_FOUND`.
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

The MCP HTTP private transport signs thirteen bounded API reads (including the
account contact list and account event list/get), `qa.readonlyAsk`, personal
note creation, account contact creation and account event creation (with
`?replay=1` probes for retry recovery). It derives identity and scopes from the verified request,
checks atomic current grants before signing and before delivery, and invokes the
configured API/QA Lambda through service IAM. It forwards no bearer token and
never retries a rejected private request through the legacy HTTP transport.
`TTOBAK_CONNECTOR_PRIVATE_BRIDGE` defaults off; deployment permissions and
provider/consumer acceptance remain separate activation requirements. The
[dated connector record](runbooks/connector-activation-2026-10-21.md) binds
actual deployed source and artifacts without changing these endpoint contracts.

## Contracts that require care

### Meeting catalog and vault pages

`GET /api/meeting-catalog` lists owned, directly shared and account-shared
meetings through bounded candidate queries. It accepts `tab=all|shared`,
`limit=1..20`, optional comma-separated `accountIds`, `pageSize=1..8000`
Unicode code points and opaque `cursor` (at most 2,048 characters).
Account filters classify accessible meetings; they never grant access.
Reads neither materialize invitations nor create or update user profiles.
Account-team readers see published meetings' current derived previews, as
owners do (ADR-049 2026-11-06 amendment); unpublished meetings are not listed.

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
TTL removal is asynchronous and best-effort; expiry does not imply that the
receipt has already been removed, and an expired retained receipt still conflicts.
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
The checked-in configuration leaves it off: the companion was retired on
2026-11-04, so `BILLING_INSIGHT_URL` is unset and the route returns 404.
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

### Billing service

The Billing routes under `/api/billing/*` are served by the separate Billing BFF
(`services/billing/bff`, a Lambda Web Function) through CloudFront, not by the Go
API, so they are not part of the Go route inventory above. Identity comes only
from `Authorization: Bearer <JWT>`, verified by the BFF (issuer, signature,
expiry and client allowlist; see
[ADR-056](decisions/ADR-056-billing-service-origin-and-identity.md)). Access
uses canonical direct membership or validated ancestor membership (ADR-060);
pending rows grant nothing. Billing uploads and POSTs work only on the custom domain
(`PUBLIC_URL`), not on the raw CloudFront domain.

| Route | Notes |
|---|---|
| `GET /api/billing/accounts` | Accounts the caller is a member of, with the current version of each source. |
| `GET /api/billing/data/{id}.json` | One dataset. Optional `acct` and `v` (both required together) select one published version of one account. |
| `GET /api/billing/accounts/{acct}/reading` | Bounded metadata, CUR/COT dataset or Markdown report for MCP; current team access is checked before and after reading. |
| `GET /api/billing/accounts/{acct}/report` | Markdown report. `source=cot\|cur` is required, `v` is optional. |
| `POST /api/billing/accounts/{acct}/uploads` | Create an upload. |
| `POST /api/billing/accounts/{acct}/uploads/{id}/complete` | Complete an upload. |
| `GET /api/billing/accounts/{acct}/uploads` | Upload history. |
| `GET /api/billing/accounts/{acct}/uploads/{id}` | A single upload. |
| `POST /api/billing/accounts/{acct}/uploads/{id}/discard` | Discard an upload. |
| `POST /api/billing/accounts/{acct}/current` | Change the current version. |
| `POST /api/billing/accounts/{acct}/rerun-agents` | Re-run the agents on a version. |
| `POST /api/billing/accounts/known` | Record a known account. |

A missing or invalid token returns 401 with
`{ "error": { "code": "UNAUTHORIZED", "message": "..." } }`. Every other error
(including 404, 500 and 503) has a Billing-specific JSON body:
`{ "error": "<string>" }`, except the bounded reading route, which uses the
standard `{ "error": { "code": "...", "message": "..." } }` shape. Every response carries
`Cache-Control: private, no-store`. POST requests require a JSON content type,
`X-TB: 1` and a same-origin `Origin`, otherwise they return 403.

The reading route alone also accepts dedicated connector access tokens with the
exact MCP resource audience and read scope. It resolves canonical client policy
and user approval before and after reading; blocked clients cannot publish a
result. Existing browser authentication remains supported. Connector tokens do
not authorize Billing catalog, uploads or reruns.

Use `section=metadata|dataset|report`; content requires `source=cot|cur`, and
`dataset` is required only for dataset reads. Optional `versionId` selects a
published upload; omission selects the current source. `pageSize` is 1000–8000
UTF-8 bytes (default 4000), and `cursor` is at most 2048 characters. Unknown
parameters are rejected. A supported dataset may have no published file (404).
Metadata returns `dataJson`; dataset and report pages return `dataJson` and
`markdown` respectively, plus source revision, byte offsets and
`page.complete/nextCursor`. Concatenate same-revision fragments before parsing
JSON. API JSON is capped at 14,000 encoded bytes; S3 sources at 48 MiB. Current
pointer/upload reads are strongly consistent. Source or selection changes return
409: restart without a cursor. Missing or unauthorized accounts return 404;
oversized sources return 413. No presigned URLs are returned.

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
never grants a private meeting. A direct edit share keeps edit; a direct read
share yields to live team edit on a published meeting (ADR-016 amendment).

Clients expand selected visible account groups to accessible descendants and send
the resulting IDs. With ADR-058 activated, canonical ancestor membership gives
read-only access to explicitly published descendant content, except meetings:
every direct or inherited member gets `edit` on a published meeting (ADR-016
2026-11-06 amendment). Account filters remain classification. Independent
direct edit grants retain their permissions; a direct read grant yields to
team edit.
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

`PUT /api/meetings/{meetingId}` rejects `content`, `liveSummary` or `status`
writes from a restricted summary projection with 409 `SHARING_REGEN_REQUIRED`;
members of the published account no longer receive such projections.

`PUT /api/meetings/{meetingId}` also accepts `noteLanguage` with owner/edit
permission. Allowed values are exactly `""` (automatic), `"ko"` and `"en"`; any
other value, including case variants, returns 400 `BAD_REQUEST`. An explicit `""`
returns the meeting to automatic; an omitted field preserves the stored value,
and an unchanged value is not written. It cannot be combined with an
`expectedNotes` notes-only update. Meeting detail always returns `noteLanguage`
(`""` for legacy rows). The field is stored and accepted but no longer
affects generation: Korean final notes always use the Korean directive
(`noteLanguageDirective`, ADR-070). Both summary captures still pin the field in
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

`POST /api/kb/upload` `{fileName, fileType, accountId?}` returns `{uploadUrl,
key, expiresIn, headers?}`. An empty `accountId` is a personal file and the
request/response are unchanged (no `headers`). A non-empty `accountId` must have
no `#` or `/` (else 400) and the caller must be a member of that Account (else
403 `FORBIDDEN`; membership is read consistently). The presigned PUT then signs
object metadata `account-id`, and `headers` is `{"x-amz-meta-account-id":
"{accountId}"}`: the client must send exactly those headers (plus
`Content-Type`) or S3 rejects the upload. Before returning the URL it creates
the account relation row `ACCOUNT#{accountId}` / `KBREF#{sha256(key)}` (absent
only); if that write fails the request fails with 500. A verified connector
delegation also needs the `share` capability for a non-empty `accountId` (else
403 `CAPABILITY_DENIED`); personal and direct user uploads are unchanged. The web upload dialog requires the
user to choose an Account or the personal (no Account) option, which the UI labels in Korean, before any file is sent.

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

Meeting management (ADR-016 2026-11-06 amendment; for inherited members it
supersedes ADR-058's invitation/linking restriction for meetings only): the
creator or a live direct or inherited member of the account a meeting is
published to may call the share, revoke, pending-revoke, account,
share-account, link, image/file upload, recover, rediarize, audio-crop, simulator
and project-link routes; audio upload and delete stay creator-only. Detail returns
`canManage`, and managers receive `shares` (with `sharedByEmail` for
member-created shares) and `projectIds`. Writes keep the creator's partition and
prefixes; strangers get 404. A member's account or share-account call must
target the account the meeting is currently published to (403 otherwise; 409 if
it was unpublished concurrently) and never mints the owner approval; member
email shares pass the same `SUMMARY_SHARING_UNSAFE` gate.
`summarySharing.canShareToAccount` offers the approval path only to the creator
and team republication only to the creator or direct members of the meeting's
account. `canRecoverRecording` is offered to managers; a member's recovery still
requires the 10-minute quiet checkpoint. Account-link condition failures return
409 `CONFLICT`. A member's project link or unlink requires the project to be
linked to that account. Shared list items include `ownerUserId` (the creator).

`GET/POST /api/accounts/{accountId}/contacts`, `PUT/DELETE
/api/accounts/{accountId}/contacts/{contactId}`, and `GET
/api/accounts/{accountId}/speaker-team` require current **direct** membership;
account ancestry, meeting links and shares grant no access. Account and member
records are read strongly consistently before and after reads; every write
transaction checks both records and conditionally creates or requires the contact.
Account and membership deletion/revocation during a transaction prevents the write.

The contact response contains exactly `contactId`, `revision`, `name`, `company`, `title`,
`department`, `email`, `address`, `workPhone`, `fax`, `phone`,
`cardRegisteredAt`, `cardBook`, `group`, and `notes`. POST creates a new UUID
and returns 201 with the contact. PUT requires the observed `revision`, replaces
the editable fields and returns 200 with a new revision; DELETE requires the
observed `revision` query parameter and returns 204. A stale revision returns
409 so concurrent changes are not overwritten. A successful POST or PUT queues
one people resolve request for the account unless it carries
`?peopleResolve=defer`, the only query parameter they accept (any other query
is 400). `name` must contain non-whitespace text. All fields are
UTF-8 strings: `address` is at most 1,024 bytes, `notes` at most 8,192 bytes,
and the other fields at most 256 bytes each. POST/PUT accept a single JSON object
at most 64 KiB, with at most 12,000 bytes in the serialized contact; omitted optional fields become empty strings.

Contacts listing accepts `limit=1..25` (default 25) and an opaque `cursor`, and
returns `{contacts, nextCursor}` with `nextCursor` omitted at the end. A cursor
is bound to its account; every page reauthorizes the caller. Pagination is not a
snapshot: concurrent changes can affect subsequent pages. The team response is
`{members:[{userId,name,email,role}],nextCursor}`; the name uses the profile name where
available and otherwise the member's email. Team pages accept the same limit and
cursor parameters and recheck the requester's access. Invalid input returns
400, denied access 403, a missing contact on PUT/DELETE 404, a stale revision 409, and unexpected
errors a generic 500. Responses are not cached and contact content is not logged.

Contact fields are KMS envelope-encrypted per row before storage and bound to
the account and contact (ADR-063); without a configured key, contact routes
return 503 `CONTACTS_UNAVAILABLE`; delegated connector creates seal the row the
same way and return that 503 before any write. Contact rows have no TTL and remain until a
direct member deletes them. Point-in-time recovery can retain deleted, still
encrypted data within the table's recovery window.

`GET/POST /api/accounts/{accountId}/events` and `GET/PUT/DELETE
/api/accounts/{accountId}/events/{eventId}` manage account events (schedules)
with the same **direct** membership rule and transactional access checks as
contacts. An event has `eventId`, `revision`, `title` (1-256 bytes), `startAt`
and optional `endAt` (RFC 3339, normalized to UTC seconds; at most 31 days
apart), `allDay`, `location` (512 bytes), `attendees` (at most 50 of
`{contactId?, name, email?}`; a `contactId` must name a contact in the same
account), `notes` (8,192 bytes), `createdBy`, `createdAt` and `updatedAt`; the
serialized event is at most 12,000 bytes on every path, so one event always fits
a private bridge read.
POST returns 201; PUT requires the observed `revision` and DELETE its
`revision` query parameter; a stale revision returns 409. Clients cannot set
the author or timestamps.

`GET/POST /api/accounts/{accountId}/news-sources` and `DELETE
/api/accounts/{accountId}/news-sources/{sourceId}` manage account-owned news
sources for **direct** members. Each source is a new crawler CONFIG row
`acct_{accountId}_{8 hex}` carrying `accountId`, plus an `ACCOUNT#{accountId}` /
`NEWSSRC#{sourceId}` ref, written in one transaction that also checks the
account and the caller's membership. POST takes `sourceName` (1-200 code
points), optional `newsQueries` (at most 32) and `aliases` (at most 20), and
optional `copyFromSourceId`, which copies queries, aliases and custom URLs from
a per-user source the caller subscribes to and never changes that source;
operator-curated, unknown or unsubscribed sources return 400. Default queries
are the account name plus its aliases; at most 10 sources per account, enforced
by an `ACCOUNT#{accountId}` / `NEWSSRC_COUNT` counter row (`sourceCount`, seeded
from the existing refs when absent, plus a `sourceRev` revision that every counter
write advances). The create transaction increments it only below the cap and the
delete transaction decrements it, conditioned on the count and revision it read.
Reconcile sets the counter to the strongly consistent ref count, conditioned on
the revision read before listing. A create runs it in both directions: before the
create transaction when the counter is below the observed ref count (left low by
a writer that predates the counter), and once more with a retry when the refs show
room but the counter is at the cap. No account-delete path exists, so the
row has no deletion counterpart. GET
returns `{ "sources": [...] }`, omitting refs whose CONFIG no longer names the
account or is disabled. DELETE removes the ref and sets the CONFIG `status` to
`disabled` (so scheduling stops) in one transaction; returns 204, or 404 for an
unknown source. Per-user crawler routes (subscribe/AddSource, update,
unsubscribe) refuse account-owned sources like operator-curated ones, in the
service and in the `attribute_not_exists(accountId)` write condition.
`DELETE /api/insights/{sourceId}/{docHash}` on an account-owned source's
document is allowed only for admins or a current direct member of that account
(read per request); the stored source creator grants nothing. A non-admin
delete is fenced before any object is removed: versioned news in the unpublish
transaction, legacy documents by a claim transaction that checks the CONFIG
`accountId`, the account and the caller's direct `MEMBER#` row. Membership is
fenced at that claim (or unpublish) commit: a membership revoked before it
returns 403; the object and metadata removal that follows is not re-fenced.

`GET/POST/DELETE /api/accounts/{accountId}/links` manage typed account links
for **direct** members. Allowed pairs are news→project, tech→project,
event→project and news→meeting. News and tech IDs are `{sourceId}:{docHash}`
(tech uses `__tech__`), event IDs are event UUIDs and meeting IDs are
`{ownerUserId}:{meetingId}`. A link is an `ACCOUNT#{accountId}` /
`LINK#{fromKind}#{fromId}#{toKind}#{toId}` row plus a `LINKTO#` reverse row,
written in one transaction that also checks the project still lists the
account (`accountIds`) or the meeting is still published to it, and that the
account and the acting member's direct `MEMBER#` row still exist (the automatic
linker has no member row and checks only the account). Delete transactions
check the same account and member rows. POST takes
`{fromKind, fromId, toKind, toId}`; the news must come from one of the
account's current sources, tech docs must exist under `__tech__`, and events
must exist in the account. It returns 201 with the new link, or with the
existing link for the same pair; endpoint failures return 400. GET returns
`{ "links": [...], "nextCursor"? }` (100 rows read per page, opaque `cursor`),
omitting rows whose project left the account, whose news source is no longer
the account's, or whose meeting is no longer published, so a page can be short
while a cursor remains. Each link shows `origin` (`manual` or `auto`),
`createdBy`, `createdAt` and, for auto links, `evidence`. DELETE takes the four
endpoint fields as query parameters, is allowed for any direct member, returns
204 or 404, and for an `auto` link also writes a `LINKDENY#` row so the
automatic linker never re-creates that pair. None of the three routes is
connector-delegable; the list carries meeting-derived evidence. People edges
(below) live in the `LINK#meeting#` range and never appear on these routes.

The `/api/accounts/{accountId}/people` routes serve ADR-067 people edges for
**direct** members, checked on every request (not connector-delegable;
`Cache-Control: no-store`; 400 uses `BAD_REQUEST`, an unreadable contact
directory 503 `CONTACTS_UNAVAILABLE`). Meeting IDs are `{ownerUserId}:{meetingId}`,
and only meetings currently published to the account count:
- `GET /people?cursor=` returns `{people, nextCursor?, truncated}`: at most 200
  members then contacts, `{kind, id, label, meetings, roles}` with role counts
  over visible meetings. `GET /people/mentions?cursor=` returns `{groups,
  nextCursor?, truncated}`: at most 100 unresolved groups `{key, text, meetings,
  roles}` where `key` is 32 lowercase hex. Cursors are opaque base64url.
- `POST /people/mentions/{mentionKey}/resolve` takes `{kind: member|contact,
  id}`, writes manual edges for the visible rows and returns `{written,
  remaining}`; `remaining` means more rows are left for another call.
  `POST .../ignore` returns 204 and hides the text account-wide (no undo route).
- `GET /people/edges?meetingId=` returns `{edges, labels}` for one meeting;
  `labels` is keyed `{kind}:{id}` and holds only readable targets. `POST
  /people/edges` takes `{meetingId, toKind, toId, role, sourceRef?}` and returns
  201 with a manual edge; a `speaker` edge needs `sourceRef` to be a speaker-map
  label or a default diarization label (`spk_N` or the Korean speaker letter form). `DELETE /people/edges` takes the four
  key fields as query parameters, returns 204, and for an auto edge writes a
  deny row.
- `POST /people/resolve` (no body, no query) returns 202 `{accepted: true}` and queues one resolve
  request for the account. Bulk and CSV contact imports write rows with
  `?peopleResolve=defer` and call it once per account with saved rows; a failed
  call is shown with a retry.
- Under the summary guard, assignee and insight mention text and evidence are
  blank for teammates who do not own the meeting.

Project stages are a fixed list (Prospect, Qualified, Technical Validation,
Business Validation, Committed, Launched, Closed Lost; matched case-insensitively,
empty means unset). Create and `PUT /api/projects/{projectId}` reject other values
as invalid input. A stage change sets `stageChangedAt` and appends an immutable
`STAGEHIST#` row with the update; legacy values outside the list read as unmapped
and projects without a change time show an unknown stage age. Project list rows
carry `stageChangedAt` (when recorded) and `stageMapped`, like the detail
response. See [ADR-064](decisions/ADR-064-account-relationship-links.md).

`GET /api/accounts/{accountId}/signals` returns the account page's AWS signals
for **direct** members (membership is read on every call; no query parameters;
`Cache-Control: no-store`; not connector-delegable, like the insights route).
The response is `{ newsState, news, opportunities, insights, restrictedCount,
linksTruncated }`, at most 64 KiB, with text fields capped at 280 code points.
Links are read per displayed endpoint (`LINK#news#{newsId}#project#` and
`LINKTO#project#{projectId}#event#`, at most 200 rows each) and revalidated;
`linksTruncated` reports that a bound was hit, in which case linked projects,
`candidate` and `NO_UPCOMING` may be incomplete:
- `newsState` is `OK`, `NO_SOURCE` (the account has no current news source) or
  `NO_RECENT_NEWS`. `news` holds at most 10 current items crawled in the last
  30 days by the account's sources, ordered by `qualityScore` then crawl time.
  Each item has `id` (`{sourceId}:{docHash}`), `linkedProjectIds` (projects
  that still list the account) and `candidate` (unlinked, has opportunities and
  `qualityScore >= 0.75`); the flag is display-only and never written.
- `opportunities` lists at most 20 projects that still list the account, with
  `stage`, `stageMapped`, `daysInStage` (absent when the change time is
  unknown), `terminal` and `stall` reasons. Launched and Closed Lost never
  stall. `STAGE_STALE`: stage unchanged for at least 21 days. `OPEN_ACTIONS`:
  an open saved action item at least 14 days old, measured from its meeting's
  date, counting only meetings that list the project and are currently
  published to the account. `NO_UPCOMING`: no account event linked to the
  project (event→project link) starts after now; per-user calendar entries
  are not considered.
- `insights` are the 10 newest `need`, `opportunity` and `risk` account
  insights, with `restrictedCount` as in the insights route.

`GET /api/accounts/{accountId}/flow` returns the account's engagement flow for
**direct** members (same access, query and caching rules as signals; not
connector-delegable because it carries meeting-derived text). The response is
`{ rows, unassigned, truncated }`, at most 64 KiB, with text capped at 280 code
points. Items are placed only through stored links; nothing is inferred:
- `rows` holds at most 20 projects that still list the account (up to 100
  project refs are read), nonterminal first, then by `stageChangedAt`, newest
  first. Each row has `projectId`, `activity` (account events linked by an
  event→project link that still exist, at most 5 newest shown earliest first,
  with the link's `origin` and, for auto links, `evidence`), `response`
  (meetings currently published to the account whose `projectIds` list the
  project, at most 5 newest shown earliest first, each with the newest readable
  `need` insight text), `oppty` (the signals opportunity item), `history`
  (at most 10 newest stage changes `{projectId, from, to, changedBy,
  changedAt}`) and `result` `{state, stall}`. `state` is `TERMINAL`,
  `UNKNOWN` (unmapped stage, or no stage change time and no history),
  `STALLED` (any stall reason) or `ACTIVE`.
- `unassigned` holds, newest first and at most 5 each, account events among the
  50 newest with no link to a project still in the account (read per event
  through `LINK#event#{eventId}#project#`, at most 20 rows) and published
  meetings among the 50 newest account meeting refs that list no such project.
- Items are `{kind: event|meeting, id, title, date, origin?, evidence?, need?,
  people?}`; meeting IDs are `{ownerUserId}:{meetingId}`. `people` holds at most
  5 `{kind: member|contact, id, label, role}`: readable attendee contacts for
  events, member and contact people edges for meetings (assignees left out under
  the summary guard).
- `truncated` reports that a read bound or the size bound was hit; rows or
  items may then be missing, and unassigned items may have links that were not
  read.

`GET /api/accounts/{accountId}/graph` returns the account's relationship graph
for **direct** members (same access, query and caching rules as signals; not
connector-delegable). The response is `{ nodes, edges, truncated, memberCount }`,
at most 64 KiB. Nodes are `{id, kind, label}` with `id` `{kind}:{id}` (meetings
`{ownerUserId}:{meetingId}`, news/tech `{sourceId}:{docHash}`), `kind`
`account|project|meeting|event|news|tech|member|contact` and `label` capped at
60 code points. Edges are `{from, to, kind, origin?, role?}` with `kind`
`project_account|meeting_project|meeting_account|link|attends|member_of|person`;
`origin` (`manual`/`auto`) is set only on `link` and `person` edges and `role`
only on `person` edges (meeting to member, contact, account or project). Every node comes from a
revalidated relation; nothing is inferred:
- projects that still list the account (up to 100 project refs); meetings
  currently published to the account among the 100 newest account meeting
  refs, with `meeting_project` edges to listed projects still in the account;
  the 50 newest account events.
- `LINK#` rows (at most 1,000 read). A row yields nodes and an edge only when
  both endpoints revalidate: project still lists the account, news from one of
  the account's current sources and tech documents not `DELETED`, event still
  exists, meeting currently published to the account. A link never makes an
  unpublished meeting visible. Rows whose missing endpoint nodes would exceed
  the node cap are skipped without endpoint reads, and link rows spend at most
  200 distinct uncached endpoint lookups (project, meeting publication, event,
  document); skipped rows set `truncated`. At the node cap boundary a
  revalidated from node can appear without its link edge.
- direct members labelled `{email} ({role})`, as the account detail shows, now
  only when a people edge connects them; `memberCount` counts all direct
  members; event attendees whose `contactId` names an existing account contact, as
  `attends` edges labelled with the contact name. At most 200 distinct contacts
  are read, and none once the node cap is full; skipped attendees set
  `truncated`. Meeting project expansion stops reading projects once the node
  cap is full.
- people edges of meeting nodes already present (no extra meeting reads):
  members must still be direct members, contacts must open, account targets
  need the viewer's read grant and project targets must already be nodes.
- At most 150 nodes and 300 edges. `truncated` reports that a node, edge, read
  or size bound was hit, or that a single contact or linked event could not be
  opened and was skipped. A missing record key or a storage failure still
  fails the call.

`GET /api/accounts/{accountId}/kb?kind=upload|meeting|news|tech&cursor=` lists
one kind of the account's KB material for **direct** members, checked on every
call (not connector-delegable; `Cache-Control: no-store`). `kind` is required;
other query keys are 400. The response is `{items, nextCursor?, truncated?}` with at most
25 items; `cursor` is opaque and bound to the kind. Items are `{kind, id, title,
date, uploadedBy?, indexState, canDownload}`; `title` is capped at 200 code
points and no S3 key or bucket name is returned.
- `upload`: `KBREF#` rows of the account. A ref is listed only when its
  `kb/{owner}/{file}` object exists and its signed `account-id` metadata still
  names the account, so abandoned presigns and deleted files never show.
  Uploads stay listed after the uploader leaves. `id` is the ref ID (64
  lowercase hex) and `canDownload` is true.
- `meeting`: meetings currently published to the account, newest first; `id` is
  `{ownerUserId}:{meetingId}`.
- `news`: current news of the account's sources crawled in the last 90 days; `id`
  is `{sourceId}:{docHash}`.
- `tech`: tech documents linked (`LINK#tech#…#project#…`, at most 200 rows read)
  to projects that still list the account, excluding `DELETED` documents; `id` is
  `__tech__:{docHash}`. `truncated: true` reports that more link rows exist
  than were read, so items may be missing.
- `indexState` comes from the index job record only: `INDEXED`, `PENDING`
  (`PENDING`, `PREPARING`, `WAITING_SYNC`, `WAITING_SOURCE`), `FAILED`, or
  `UNTRACKED` when no job exists or the source has no index identity (for example
  `news/v1/` keys). Items whose job is `DELETED` are omitted, so pages may be
  short while `nextCursor` is set. The crawler `inKB` flag is never used.

`GET /api/accounts/{accountId}/kb/{refId}/download` returns `{url}` (`Cache-Control:
no-store`), a 5-minute presigned S3 GET that downloads as an attachment, for any
direct member. `refId` must be 64 lowercase hex (else 400); the ref must belong to
the account, name a `kb/` key whose hash is the ref ID, and its object must exist
and still carry the account metadata (else 404). Not connector-delegable.

`DELETE /api/accounts/{accountId}/kb/{refId}` removes the account ref (204). Only
the uploader (while a direct member) or an account `owner` may untag (else 403);
a missing ref is 404. The S3 object is never deleted; it stays in the uploader's
personal KB. Untag deletes the ref and writes an `ACCOUNT#{accountId}/KBUNTAG#{refId}`
tombstone in one transaction; every ref create requires the tombstone to be
absent. A new upload gets a new timestamped key and so a new ref ID. QA
retrieval scope is unchanged by refs. Not connector-delegable.
Existing tagged uploads are backfilled with `backend/cmd/backfill-account-kb`
(`-dry-run` defaults to true; prints counts only). Apply mode requires
`-before`, an RFC 3339 time set to when account KB refs were first deployed;
only objects modified strictly before it are backfilled and later ones are
counted as `after_cutoff`. Refs untagged before tombstones existed left no
`KBUNTAG#` row, so this cutoff is what keeps them from being re-shared. A rerun
skips existing refs and never recreates a ref untagged since tombstones
(counted as `untagged`).

Listing accepts `from`, `to` (RFC 3339; events starting in `[from, to)`),
`order=asc|desc`, `limit=1..25` and an opaque `cursor` that binds the account,
order and resolved range. Without `from`, ascending lists start now (upcoming)
and descending lists cover all past times. Responses are `{events,
nextCursor}` and are not cached. Event fields except `startAt` are sealed like
contacts (ADR-063); without a key the routes return 503 `EVENTS_UNAVAILABLE`.
Delegated MCP clients create events through the private bridge like contacts:
`{event, idempotencyKey}` with strict decoding (no unknown, server-owned or
null fields), direct membership and current read+write approval pinned in the
same transaction as the event, its reference row and an arguments-bound receipt.
`?replay=1` only replays an existing receipt. The receipt
(`CONNECTOR_MUTATION#{clientId}#{requestId}`) holds the operation, account,
event ID, event sort key and revision, approval generation, creation time and a
SHA-256 of the account and exact request body bytes, with no expiry. Responses
are `{eventId, replayed?}` (200); 400 `BAD_REQUEST`, 403 `CAPABILITY_DENIED`,
404 `RECEIPT_NOT_FOUND` (replay probe only), 409 `CONNECTOR_CONFLICT` (same key
with different bytes, or the replayed event moved, changed or disappeared), 503
`EVENTS_UNAVAILABLE` (no key; nothing written) and 503
`CONNECTOR_WRITE_UNCONFIRMED` (retry the same key and bytes). A merging or
merged account returns 409 `ACCOUNT_MERGING` for a first create and 409
`CONNECTOR_REPLAY_ACCOUNT_MERGING` for a replay whose receipt exists; the replay
probe still returns 404 when no receipt exists. A membership,
policy or approval change between the pre-checks and the transaction also
surfaces as 409. Attendee contact IDs are resolved only after membership and
approval checks and only for a fresh create, so a replay returns its receipt
even if a referenced contact was deleted since. Event list and get are available bridge reads.
Events have no TTL and remain until a direct member deletes them.

`PUT /api/meetings/{meetingId}/category` sets the creator's internal meeting
category: `{"category": "internal_team" | "internal_1on1" | "internal_training" | "internal_other" | ""}`
(empty clears it). Only the creator may call it (403 for other readers, 404 when
the meeting is not visible). It is a conditional update on the meeting having no
`accountId`; an account-linked meeting returns 409 `CONFLICT`. Category and account
link are mutually exclusive: every write that sets a non-empty `accountId`
(link, share-account, member publication) also removes `meetingCategory`, and
`POST /api/meetings` rejects `accountId` together with `meetingCategory` (400).
List items, detail and create responses expose `meetingCategory` when set.

`POST /api/meetings/{meetingId}/account` is a private classification operation:
the caller (creator or managing member) must directly belong to the target account. One partial
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

Account creation accepts optional `ownerEmail`, `keepCreatorAsMember` and a UUID
`requestId` in a single JSON object capped at 16 KiB. Without another Owner,
the creator remains Owner. A different Owner is resolved to the current Cognito
sub; missing identities use ADR-054's invitation gates. Create META, the canonical
Owner and optional creator AM atomically. `keepCreatorAsMember: false` creates no
creator membership or read grant. The response returns the actual `ownerUserId`,
initial members, `creatorMember`, `ownerInvitationPending` (an initial invitation
hint) and `ownerInvitationRequested` (delivery requested, not inbox confirmation).
Same-creator/same-input `requestId` retries return a bounded creation receipt,
never later private Account fields or restored membership; changed input or a
foreign creator returns 409. Legacy callers can omit `requestId`.

`POST /api/accounts/{accountId}/owner-invitation` requires verified email and a
single empty JSON object capped at 1,024 bytes. Current members or the exact
recorded creator within thirty days may resend only the canonical initial Owner
invitation. Recheck the current Cognito email/sub, pending password status,
allowlist and shared hourly counter. Confirmed users return 409; identity or
delivery dependency failures return 503. Non-members gain no Account read access.
The operation's connector inventory disables delegation (ADR-055).

`POST /api/accounts/{accountId}/owner-transfer` requires verified email and a body
`{"targetUserId"}` capped at 1,024 bytes with unknown fields rejected. Only the
current Owner (both the MEMBER role and META `ownerUserId`) may transfer, to an
existing direct non-owner member whom Cognito `AdminGetUser` reports Enabled,
CONFIRMED and `email_verified`. The previous Owner becomes SA in one three-item transaction that also
clears the initial-owner invitation fields and records `originalOwnerUserId` for
creation receipts. Member role updates and removals that race a transfer onto the
new Owner return 409. Responses: 200 with `accountId`,
`ownerUserId`, `previousOwnerUserId`, `previousOwnerRole`; 400 invalid or self
target, pending, disabled or unverified identity; 403 non-owner; 404 missing account or
non-member target; 409 concurrent change; 503 Cognito unwired or lookup failure.
It is the only path that grants Owner; its connector inventory disables delegation.

`GET /api/accounts/{accountId}/merge-preview?into={id}` is a read-only inventory of
merging the path Account into `into` (ADR-072). The requester must own the source
(MEMBER role and META owner) and be a direct member of the target. Response:
`fromAccountId`, `intoAccountId`, `counts` (`members`, `meetings`, `projects`,
`research`, `contacts`, `events`, `children`; `-1` when a bounded read cap is
exceeded; members, projects, research, contacts and events are counted by sort key only,
without reading or decrypting rows, up to 10,000), `unmoved` (`newsSources`, `kb`, `documents`, `calendarShares`,
`dictionary`, `billing`, each with `present`; `billing` also carries `unverifiable: true` because its data is outside the main table and is always reported present, so `requiresAcceptUnmoved` is always true), `cliOnly`, `newAudience`
(`intoMembersNotInFrom`, `intoHasAncestors`) and `requiresAcceptUnmoved`. Errors:
400 missing `into`, same or malformed IDs; 403 requester is not the source Owner or a
target direct member; 404 missing Account; 409 `MERGE_INELIGIBLE` with the reason
(tombstone or merge in progress, target inside the source subtree, hierarchy too deep
or large). The connector inventory disables delegation.

`POST /api/accounts/{accountId}/merge` starts merging the path Account into
`intoAccountId` (ADR-072). Body `{"intoAccountId","acceptUnmoved"}` is capped at
1,024 bytes with unknown fields rejected, and the email must be verified. It
re-checks the preview's eligibility rules, requires `acceptUnmoved: true` when any
unmoved category is present (billing always is), and rejects a merge whose source
and target contacts together exceed 10,000 before anything moves. One transaction
writes the `MERGE#job` row (state `running`, step `members`), sets the source META
`mergingInto`, increments the target's `incomingMerges`, and rechecks ownership,
target membership, both Accounts' open state and the target's ancestor chain. A
`running` job for the same target whose worker lease is absent or expired is handed
to a new worker event (202; a live lease is 409 `MERGE_RUNNING`). A
`failed` job for the same target restarts from its saved step (the source stays
fenced after a failure; nothing is rolled back). After the write the API publishes
`AccountMergeRequested` for the summarize worker; when that publication fails the
job is marked `failed` with `DISPATCH_FAILED` (restart resumes it) and the call
returns 500. Responses: 202 with the job view;
400 invalid body or unaccepted unmoved data; 403 unverified email, non-owner or
non-member; 404 missing Account; 409 `MERGE_RUNNING`, `CONTACT_INDEX_TOO_LARGE`,
`MERGE_INELIGIBLE` with the reason, or `CONFLICT` when an Account changed (or a
concurrent transaction conflicted) during the start.

`GET /api/accounts/{accountId}/merge` returns the job view to a direct member of the
source or of the job's target: `fromAccountId`, `intoAccountId`, `state`
(`running`, `failed`, `done`, or `aborted` after an operator abort), `step`, `errorCode` (`REQUESTER_REVOKED`,
`CONTACT_INDEX_TOO_LARGE`, `MERGE_CONFLICT`, `STEP_FAILED` or `DISPATCH_FAILED`), `counts` keyed
`{step}.{moved|skipped|resumed|unreadable|keptSeparate}`, `startedAt`, `updatedAt`,
`finishedAt` and `abortedAt`. Lease and cursor state are not exposed. 404 when a source member
asks and no job exists; 403 otherwise. Both routes disable connector delegation.

Account writes are fenced while an Account merges (ADR-072). Writers that check the
Account META row also require `mergingInto` and `mergedInto` to be absent in the same
transaction: member add, removal and role change, owner transfer, parent changes
(child and new parent), contact and event writes, link create and delete, project and
research linking, Account document creation (including sharing a personal document
to the Account), the Account KB upload presign, a member's team audio-crop copy, and
meeting classification or publication into the Account (a write that only names the
stored Account reads it first and stays bound to it). Calendar sync rejects an event
classified to a merging or merged Account with the same 409. Notes and other
meeting writes carry no Account check. A rejected write returns 409 `ACCOUNT_MERGING`
instead of the generic conflict. A pending Account grant for a merging Account stays
queued and is redirected to the merge target after the merge finishes.

Optional parentAccountId requires the creator's current membership in the parent
before mail and transactionally before creation. Parent updates require child ownership
and new-parent membership; the required field uses an empty string to detach,
not omitted/null. Self/cyclic/deep ancestry (over 64 nodes) is rejected;
transactional ancestry checks and retries protect concurrent moves. Conflicts
return 409. Canonical child references change in the same transaction. ADR-058
adds read-only descendant access when `ACCOUNT_DESCENDANT_READS_ENABLED=1`;
private meeting links, mutations and Billing continue to require direct grants.

`GET /api/accounts` retains `{accounts: [...]}` for callers without pagination.
Optional `pageSize=1..100` and opaque `cursor` return `{accounts, nextCursor}`.
Discovery validates canonical ancestry and membership, including empty advancing
pages. Each paged request stops after 256 discovery steps (one index or child
query each) or two seconds of traversal, reads ancestry once per request, and
rechecks every returned row against current grants before responding. With descendant
reads enabled, responses declare `overlapPossible: true, unionKey: "accountId"`:
independent direct roots can overlap descendant traversal. Clients combine those
declared pages by Account ID, replacing a duplicate with its current response
and permissions. Undeclared duplicates remain invalid. Cursorless compatibility
collection fails explicitly beyond 1,000 Accounts; paged clients must finish
continuation or report their page limit, never silently return a partial list.
Account/doc/project responses may include `accessPermission: "read"` and
`inheritedFromAccountId` for ancestor-only access; no synthetic member is created.

`scope=direct` selects the bounded direct-membership listing used by recording
preparation. It scans at most 25 index candidates in one query per page, then
rereads canonical membership and account metadata; inherited grants never enter
this scope. Every returned row is revalidated before publication. Empty pages
retain continuation for stale index candidates. Its cursor binds user, scope,
requested page size, candidate position and the last returned revision; it is
not interchangeable with the default/`scope=all` discovery cursor.

Any current account member can add members or change assignable roles; `owner`
is never assignable. Member addition requires a verified requester email and a
single JSON body capped at 1,024 bytes. A missing Cognito identity is invited and
queued, returning `pending: true, invited: true`. An existing invited identity is
queued with `pending: true` without resending mail. Optional `resendInvitation:
true` explicitly requests resend only for a `FORCE_CHANGE_PASSWORD` identity.
New invitations and resends enforce the current domain allowlist and 20 attempts
per caller per UTC hour; counter failure returns 503 and limit exhaustion 429.
`INVITATION_GRANT_FAILED` (500) records requested delivery followed by queue
failure. Account materialization transactionally revalidates current inviter
membership, the invited sub, expiry and pending-row version. Removal and
pending-invite revocation remain owner-only (ADR-034/054).
Project permissions are separate: owner/direct member/member of a
linked account. The service list unions those access paths; project deletion
rejects remaining relationships. Canonical sets and reverse references change in
one transaction. See account/project handlers and services for each mutation gate.

### Staged summary-sharing privacy

Meeting detail adds `summarySharing`: `canCreateGrant`, `canShareToAccount`,
`derivedReadRestricted`, `requiresRegeneration` and `canRegenerate`. These
describe the current source view. Grant/publication and provenance writers are
installed behind `SUMMARY_PRIVACY_WRITER_ENABLED=1`; any other value disables
writer activation. `canRegenerate` requires activated writers, ownership and a
terminal meeting state. Source wiring does not establish deployed acceptance.

With `SUMMARY_OWNER_ACCOUNT_SHARE_ENABLED=1` and privacy writers enabled,
owner-only `POST /api/meetings/{meetingId}/share-account` can disclose current
completed private/unknown notes without regeneration. The canonical publication
and server-managed `summaryAccountApproval` are atomic with the current source
and direct owner-membership fence. Existing output and summary/action identities
are preserved; absent legacy Insight revisions are minted to reject stale copies.
Generic requests cannot assign approval. Protected Account readers accept only a
current owner/audience/generation/hash-bound approval. Content/live writes, summary
publication and audience changes invalidate it even during writer rollback.
`canCreateGrant` retains direct-email/pending safe-source semantics; see ADR-059.

For a protected account membership, unapproved unknown/private meeting derivation withholds
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
hierarchy discovery includes validated descendant read grants under ADR-058.
Project pages visit ownership, direct
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
membership and the complete ancestor path for inherited grants. Fragment
continuation also pins the exact authorized source window
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

The server validates category, management access and traversal; meeting upload
keys use the creator's prefix even when a managing member uploads an image or file. Checkpoint filenames
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

Recover uses a saved progress object; a non-creator manager may recover only after
it has been unchanged for 10 minutes (409 `RECORDING_STILL_ACTIVE`). Audio presign
and completion are creator-only (403 `FORBIDDEN`) pending a member staging-prefix
design; managers upload images and files, with 5-minute presigned URLs. Rediarize accepts supported single-part
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

`GET /api/meetings/{meetingId}/audio` returns `audioUrl` (single file) or
`audioUrls` (ordered parts, empty slots skipped) as one-hour signed URLs. For a
WebM/Ogg key it signs the ADR-014 `audio-playback/` copy only when that copy's
`source-etag` and `source-version-id` equal the source's current HEAD; a missing,
stale or unreadable copy falls back to the original key. The response shape is
unchanged and the original remains the STT/crop/recovery input.

`POST /api/meetings/{meetingId}/audio/crop` accepts a bounded JSON object containing
`requestId` (UUID), `startSeconds` and `endSeconds` (integer seconds). The creator
or a managing account member may crop single-file `done`/`error` sources; the copy
belongs to the creator, and a member's copy is published to the same account. The server resolves the canonical
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
product names), or above 0.1 for an `en`, `ja` or `zh` target. An `en` target
also rejects a Han/kana letter share above 0.1; a `zh` target rejects output with no
Han letter and a `ja` target output with neither kana nor Han, with the same
Latin-only exception. Fallback reasons in metering are
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
WebSocket stream. Concurrent runs are conditionally rejected (400); a member whose
grant lapsed gets 403, an unpublished or changed meeting 409, and a sharing-restricted
summary 409 `SHARING_REGEN_REQUIRED`. Stale queued/running
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

Storage and migration: ADR-008. Routes act on the
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
- Vocabularies (ADR-008 A/B builder): the GET and every
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
  `VocabularyNames` on its ko-KR/en-US stream (ADR-008 rollout check 5).
  The builder replaces the earlier in-place rebuild of the derived ko-KR
  vocabulary, including its pending-build deferral warning and bounded
  conflict recomputation; that path no longer exists.

### Layered dictionary

Routes under `/api/settings/dictionary/layers/{layer}` act on one layer:
`personal` (the caller's `USER#` partition), `common` (`DICT#COMMON`, every
signed-in user) or `account:{accountId}` (`ACCOUNT#{accountId}`, members only).
Any other `layer` is 400. An Account layer first checks the caller's
`MEMBER#` row (403 `FORBIDDEN` when absent); every Account write also carries
a membership ConditionCheck in its transaction, so a membership removed after
that check is still 403. `termId` is the 32-hex `termKey`, validated as in the
personal routes, and request bodies have the same bounds and validation.
Term items (here and in the personal GET `items`) gain `layer` and `sources` (`[{kind, ref, at}]`, newest first, at
most 10, `kind` `meeting|kb|research|news`; `ref` is empty on Account and
Common items, which name other users' content); Common items also carry
`updatedBy`. These routes do not rebuild vocabularies, except personal writes
(which behave as the personal routes above).

- `GET .../layers/{layer}/terms` → 200 `{items}` (non-tombstone rows, oldest
  first; the personal layer hides unmigrated legacy copies like the GET above).
- `GET .../layers/common/terms?deleted=1` → 200 `{items: [{termId, displayAs,
  phrase, type, deletedAt, deletedBy}]}`: the newest 50 Common tombstones,
  newest deletion first. The display fields are the term as it was before its latest audited
  change (the stored tombstone is cleared); a tombstone with no audited
  earlier state cannot be restored and is omitted. Restore through the log
  routes below. `deleted=1` on any other layer, or any other value, is 400.
- `POST .../layers/{layer}/terms` `{displayAs, phrase?, heardAs?, type?,
  aliasMode?}` creates a manual term in the personal or an Account layer (201,
  same 409s as `POST .../terms`). `common` is 400: Common terms come from
  automatic sources only.
- `PATCH .../layers/{layer}/terms/{termId}` takes the personal PATCH body
  (`version` required; stale 409 `VERSION_CONFLICT`). 200 `{item, termId, state}`.
- `DELETE .../layers/{layer}/terms/{termId}[?version=]` tombstones the term.
  200 `{termId, state: "deleted", warnings?}`; observation cleanup and
  tombstone pruning are best effort, and a failure is a `warnings` entry.
- Common audit (L2): every Common PATCH, DELETE and restore records
  `updatedBy`/`updatedAt` on the term and writes a `DICTLOG#{termKey}#{at}` row
  `{actor, action update|delete|restore, before, after, at}` in the same
  transaction. A term keeps at most 20 rows: its oldest row (the create or
  first system row) is pinned and the newest 19 others are kept; the rest are
  trimmed best effort after each write. Account and personal writes are not
  audited. A user-edited Common term still feeds vocabulary and the Whisper
  prompt but not the transcript-correction glossary.
- `GET .../layers/{layer}/terms/{termId}/log` → 200 `{items: [{logId, termId,
  actor, action, at, before?, after?}]}` newest first, `before`/`after` being
  term items. Only `common` has a log; other layers are 404.
- `POST .../layers/common/terms/{termId}/log/{logId}/restore` puts the term
  back as it was before that change (`before`), whether it is now live or a
  tombstone, and audits the restore as a new row. `logId` is the row's `at`
  (`2006-01-02T15:04:05.000000Z` form, other shapes 404). 200 `{item, termId,
  state}`; unknown log row or another layer 404; a row with no `before` 400;
  a concurrent change 409 `VERSION_CONFLICT`.

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
PDF/DOC/DOCX/XLS/XLSX snapshot support differs from attachment extraction.
PPTX originals are indexed as a worker-generated `document.md` projection
(slide titles, body text, tables and speaker notes) bound to the original's
revision; malformed, encrypted or limit-exceeding decks fail with `PPTX_*`
codes. Legacy `.ppt` fails `LEGACY_PPT_UNSUPPORTED` (convert to `.pptx`);
empty or over-50-MiB originals have explicit failure states.

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
| POST | `/api/calendar/sources/{sourceId}/sync` | `calendarHandler.Sync` |
| GET | `/api/calendar/sources` | `calendarHandler.ListSources` |
| GET | `/api/calendar/events` | `calendarHandler.ListEvents` |
| POST | `/api/performance/sources/{source}/sync` | `performanceHandler.Sync` |
| GET | `/api/performance/sources` | `performanceHandler.ListSources` |
| GET | `/api/performance/goals` | `performanceHandler.GetGoals` |
| PUT | `/api/performance/goals` | `performanceHandler.PutGoals` |
| GET | `/api/performance/mappings` | `performanceHandler.ListMappings` |
| PUT | `/api/performance/mappings/{sfdcAccountId}` | `performanceHandler.PutMapping` |
| GET | `/api/performance/missed` | `performanceHandler.ListMissed` |
| GET | `/api/specreq-tasks` | `specReqTaskHandler.List` |
| GET | `/api/specreq-tasks/{eventId}` | `specReqTaskHandler.Get` |
| POST | `/api/specreq-tasks` | `specReqTaskHandler.Create` |
| POST | `/api/specreq-tasks/{eventId}/transitions` | `specReqTaskHandler.Transition` |
| GET | `/api/accounts` | `accountHandler.ListAccounts` |
| GET | `/api/navigation/accounts` | `accountCatalogHandler.Get` |
| POST | `/api/accounts` | `accountHandler.CreateAccount` |
| POST | `/api/accounts/{accountId}/owner-invitation` | `accountHandler.ResendOwnerInvitation` |
| POST | `/api/accounts/{accountId}/owner-transfer` | `accountHandler.TransferOwner` |
| GET | `/api/accounts/{accountId}/merge-preview` | `accountMergeHandler.Preview` |
| POST | `/api/accounts/{accountId}/merge` | `accountMergeHandler.Start` |
| GET | `/api/accounts/{accountId}/merge` | `accountMergeHandler.Status` |
| GET | `/api/accounts/{accountId}` | `accountHandler.GetAccount` |
| GET | `/api/accounts/{accountId}/reading` | `accountReadingHandler.Get` |
| GET | `/api/accounts/{accountId}/contacts` | `accountContactsHandler.List` |
| POST | `/api/accounts/{accountId}/contacts` | `accountContactsHandler.Create` |
| PUT | `/api/accounts/{accountId}/contacts/{contactId}` | `accountContactsHandler.Update` |
| DELETE | `/api/accounts/{accountId}/contacts/{contactId}` | `accountContactsHandler.Delete` |
| GET | `/api/accounts/{accountId}/speaker-team` | `accountContactsHandler.SpeakerTeam` |
| GET | `/api/accounts/{accountId}/events` | `accountEventsHandler.List` |
| POST | `/api/accounts/{accountId}/events` | `accountEventsHandler.Create` |
| GET | `/api/accounts/{accountId}/events/{eventId}` | `accountEventsHandler.Get` |
| PUT | `/api/accounts/{accountId}/events/{eventId}` | `accountEventsHandler.Update` |
| DELETE | `/api/accounts/{accountId}/events/{eventId}` | `accountEventsHandler.Delete` |
| GET | `/api/accounts/{accountId}/news-sources` | `accountNewsHandler.List` |
| POST | `/api/accounts/{accountId}/news-sources` | `accountNewsHandler.Create` |
| DELETE | `/api/accounts/{accountId}/news-sources/{sourceId}` | `accountNewsHandler.Delete` |
| GET | `/api/accounts/{accountId}/links` | `accountLinksHandler.List` |
| POST | `/api/accounts/{accountId}/links` | `accountLinksHandler.Create` |
| DELETE | `/api/accounts/{accountId}/links` | `accountLinksHandler.Delete` |
| GET | `/api/accounts/{accountId}/people` | `accountPeopleHandler.List` |
| GET | `/api/accounts/{accountId}/people/mentions` | `accountPeopleHandler.Mentions` |
| POST | `/api/accounts/{accountId}/people/mentions/{mentionKey}/resolve` | `accountPeopleHandler.ResolveMention` |
| POST | `/api/accounts/{accountId}/people/mentions/{mentionKey}/ignore` | `accountPeopleHandler.IgnoreMention` |
| GET | `/api/accounts/{accountId}/people/edges` | `accountPeopleHandler.Edges` |
| POST | `/api/accounts/{accountId}/people/edges` | `accountPeopleHandler.Bind` |
| DELETE | `/api/accounts/{accountId}/people/edges` | `accountPeopleHandler.Unbind` |
| POST | `/api/accounts/{accountId}/people/resolve` | `accountPeopleHandler.Resolve` |
| GET | `/api/accounts/{accountId}/signals` | `accountOverviewHandler.Signals` |
| GET | `/api/accounts/{accountId}/flow` | `accountOverviewHandler.Flow` |
| GET | `/api/accounts/{accountId}/graph` | `accountOverviewHandler.Graph` |
| GET | `/api/accounts/{accountId}/kb` | `accountKBHandler.List` |
| GET | `/api/accounts/{accountId}/kb/{refId}/download` | `accountKBHandler.Download` |
| DELETE | `/api/accounts/{accountId}/kb/{refId}` | `accountKBHandler.Untag` |
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
| PUT | `/api/meetings/{meetingId}/category` | `meetingHandler.SetCategory` |
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
| GET | `/api/meetings/{meetingId}/notes/en` | `resummaryHandler.GetEnglish` |
| POST | `/api/meetings/{meetingId}/notes/en` | `resummaryHandler.RequestEnglish` |
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
| GET | `/api/settings/dictionary/layers/{layer}/terms` | `dictHandler.ListLayerTerms` |
| POST | `/api/settings/dictionary/layers/{layer}/terms` | `dictHandler.CreateLayerTerm` |
| PATCH | `/api/settings/dictionary/layers/{layer}/terms/{termId}` | `dictHandler.UpdateLayerTerm` |
| DELETE | `/api/settings/dictionary/layers/{layer}/terms/{termId}` | `dictHandler.DeleteLayerTerm` |
| GET | `/api/settings/dictionary/layers/{layer}/terms/{termId}/log` | `dictHandler.ListTermLog` |
| POST | `/api/settings/dictionary/layers/{layer}/terms/{termId}/log/{logId}/restore` | `dictHandler.RestoreTerm` |
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
is part of the conditional write (`BindMeetingAudioKey`). Audio, image and file keys
must be exactly `{audio|images|files}/{callerUserId}/{meetingId}/{name}`, the shape
the presign route mints; any other key returns 400 before a write or image event.

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
| GET | `/api/meetings/{meetingId}/notes/en` | Current meeting readers; `200` English note and status |
| POST | `/api/meetings/{meetingId}/notes/en` | Owner/edit; empty body or `{}` only; `202` with status |

Response: `{status, runId?, errorCode?, leaseUntil?, updatedAt?, resultHash?, sharingSafe?}`.
Both routes reject query strings with 400. POST accepts no caller source fields
and caps its body at 1 KiB.
Status is `unknown`, `queued`, `running`, `succeeded`, or `failed`; lease is epoch
milliseconds and updatedAt is RFC3339. An active run is reused. Expired work is
persisted as `failed/INTERRUPTED`. A successful resultHash is the SHA-256 of the
saved summary text.

The explicit owner `sharingSafe` mode uses the selected saved transcript, saved
notes, verified current DOCUMENT extraction and the owner's own image analyses.
It excludes prior generated and preview evidence; no mode reads linked
predecessors (ADR-070). It is the owner-only reset; ordinary human edits keep
`safe` provenance and need no reset. Mode is persisted and pinned through delivery.
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
verified document extraction, and writes Korean notes; the POST body never selects a language and the stored `noteLanguage`
does not change it (ADR-070). It does not rerun STT or refinement and does not
import linked-meeting context. Unavailable documents are omitted with notices
when trusted notes/transcript remain. If no other source exists, pending/failed/
missing document text returns `409 SOURCE_NOT_READY`; active transcription returns `409 MEETING_BUSY`;
absent source returns `409 NO_SUMMARY_SOURCE`. Synchronous source/capture limits
return 413 and publish failures return 503. After a 202, source conflicts and
output limits are reported through failed status; oversized output uses
`OUTPUT_TOO_LARGE`. Raw source/model text is
never included in error responses.

### English meeting note

`GET`/`POST /api/meetings/{meetingId}/notes/en` manage a second, English-only note
(`contentEn`) beside the Korean note. Both routes are authenticated, reject query
strings with 400, and POST caps its body at 1 KiB and rejects any field (no
`sharingSafe`, text or language). Errors and limits match re-summary: 403, 404,
`409 MEETING_BUSY`, `409 NO_SUMMARY_SOURCE`, `409 SOURCE_NOT_READY`, 413, 503.
POST returns `409 SHARING_REGEN_REQUIRED` while the meeting is read-restricted
(its Korean note awaits the owner's regeneration); that restriction does not
block GET.

The run uses the same `SummaryRequested` event with target `en` and its own
`ANALYSIS#summaryEn` state, so it neither blocks nor is blocked by a Korean run.
It requires a `done` meeting and the same sources as re-summary (selected
transcript, saved notes, verified DOCUMENT text, image analyses). It never reads
the Korean note or a prior English note, and writes only `contentEn` with its
source hash and `contentEnGeneratedAt`: tasks, insights, tags, sentiment, live
draft, privacy provenance and simulator/action state are untouched. Failures keep
the previous English note; an oversized result fails as `OUTPUT_TOO_LARGE`.

`GET` returns `200` with the status fields of the re-summary response plus
`content` (omitted until a note exists), `generatedAt` (RFC3339) and `stale`.
`stale` is true when the transcript selection or revision, saved notes or
verified documents changed since generation (or the source hash was never
stored); it is not set by Korean note or live-draft edits. Freshness is checked
after the caller's access: authorization and not-found outcomes remain errors,
while any other freshness-capture failure (for example more than 20 attachments
or the check bound) still returns `200` with the stored content and
`stale: true`, never a fresh guess. Meeting detail adds only
`hasEnglishNote` (presence of the note); it never inlines `contentEn`.

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
