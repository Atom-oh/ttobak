# TTOBAK - API Specification

> Backend REST API detailed specification

## Base URL

```
Production: https://{cloudfront-domain}/api
Local Dev:  http://localhost:8080/api
```

## Authentication

Every API request requires a Cognito JWT.

- Lambda@Edge verifies the JWT on the CloudFront Viewer Request
- API Gateway HTTP API: Lambda invoked directly after Lambda@Edge passes
- API Gateway WebSocket API: Cognito Authorizer verifies at `$connect`
- The backend Lambda extracts `sub` (userId) from the request context
- Direct frontend calls use `Authorization: Bearer {idToken}`

## Download URL shape (ADR-027)

All download URLs the API returns (`downloadUrl`, `previewUrl`, `audioUrl`/`audioUrls[]`, attachment `url`, the public-share 302 Location) are CloudFront-signed URLs:

```
https://{domain}/media/{s3Key}?Expires=...&Signature=...&Key-Pair-Id=...
```

TTL semantics match the previous S3 presign scheme (1 hour default, 5 minutes for public shares). Uploads (`uploadUrl`, PUT) still use raw S3 presigned URLs. If the backend can't read the CloudFront signing key (local dev, etc.), it falls back to an S3 presigned GET URL.

## Endpoints

### Health Check

```
GET /api/health
Response: 200 OK
{
  "status": "ok",
  "timestamp": "2026-03-05T12:00:00Z"
}
```

---

### Meetings

#### List Meetings

```
GET /api/meetings?tab={all|shared}&cursor={lastKey}&limit={20}

Response: 200 OK
{
  "meetings": [
    {
      "meetingId": "uuid",
      "title": "Product Strategy Sync",
      "date": "2026-03-05T10:00:00Z",
      "status": "done",           // recording | transcribing | summarizing | done | error
      "summary": "AI summary preview (first 200 chars)...",
      "participants": ["Alice", "Bob"],
      "tags": ["Internal"],
      "sentiment": "positive",    // positive | neutral | negative, omitted until analyzed
      "duration": 1830,           // total audio length in seconds, omitted when unknown
      "isShared": false,          // true if this is a shared meeting
      "sharedBy": null,           // owner email if shared
      "permission": null,         // "read" | "edit" if shared
      "createdAt": "2026-03-05T10:00:00Z",
      "updatedAt": "2026-03-05T11:30:00Z"
    }
  ],
  "nextCursor": "base64-encoded-lastEvaluatedKey or null"
}
```

#### Create Meeting

```
POST /api/meetings
Request:
{
  "title": "New Meeting",
  "date": "2026-03-05T10:00:00Z",
  "participants": ["Alice", "Bob"]
}

Response: 201 Created
{
  "meetingId": "uuid",
  "title": "New Meeting",
  "date": "2026-03-05T10:00:00Z",
  "status": "recording",
  "participants": ["Alice", "Bob"],
  "content": "",
  "createdAt": "2026-03-05T10:00:00Z"
}
```

#### Get Meeting Detail

```
GET /api/meetings/{meetingId}

Response: 200 OK
{
  "meetingId": "uuid",
  "userId": "owner-uuid",
  "title": "Product Strategy Sync",
  "date": "2026-03-05T10:00:00Z",
  "status": "done",
  "participants": ["Alice", "Bob", "Charlie"],
  "content": "# Meeting Notes\n\n## Agenda\n...",     // Markdown
  "liveSummary": "## Live Summary\n...",              // Markdown incl. mermaid, built during recording (omitted if never saved)
  "transcriptA": "Full text of Transcribe result...",
  "transcriptB": "Full text of Nova 2 Sonic result...",
  "selectedTranscript": "A",                    // "A" | "B" | null
  "audioKey": "audio/user-uuid/meeting-uuid.webm",
  "notionPageId": "1a2b3c4d-5e6f-7a8b-9c0d-1e2f3a4b5c6d", // owner only, omitted for shared users; set once exported to Notion, re-export updates this page in place
  "permission": "owner",                        // "owner" | "read" | "edit"
  "attachments": [
    {
      "attachmentId": "uuid",
      "originalKey": "images/user-uuid/photo1.jpg",
      "processedKey": "processed/user-uuid/photo1-mermaid.md",
      "type": "diagram",                        // photo | screenshot | diagram | whiteboard
      "status": "done",                         // uploaded | processing | done
      "description": "System architecture diagram",
      "processedContent": "```mermaid\ngraph TD\n...\n```"
    }
  ],
  "shares": [                                   // Only visible to owner
    {
      "userId": "shared-user-uuid",
      "email": "bob@example.com",
      "permission": "read"
    }
  ],
  "createdAt": "2026-03-05T10:00:00Z",
  "updatedAt": "2026-03-05T11:30:00Z"
}

Error: 403 Forbidden (if not owner and not shared)
Error: 404 Not Found
```

#### Update Meeting

```
PUT /api/meetings/{meetingId}
Request:
{
  "title": "Updated Title",                     // optional
  "content": "# Updated markdown...",           // optional
  "notes": "In-meeting notes...",               // optional, see semantics below
  "liveSummary": "## Live Summary\n...",        // optional, same omit-vs-empty semantics as notes
  "selectedTranscript": "B",                    // optional
  "participants": ["Alice", "Bob", "David"],    // optional
  "status": "done"                              // optional
}

Response: 200 OK
{ "meetingId": "uuid", "updatedAt": "..." }

Error: 403 Forbidden (shared users with "read" permission cannot edit)
```

> `content` must be **Markdown**, not HTML. The web editor (TipTap) edits in HTML but converts back to Markdown before saving, because the summary is consumed as Markdown downstream (Notion/Obsidian export). Exporters also normalize any stray HTML to Markdown as a safety net for legacy records.

> `notes` and `liveSummary` are the only fields with omit-vs-explicit-empty semantics: omitting the key entirely leaves the stored value untouched, while sending an explicit `""` clears it. Every other field in this request follows the older "empty/omitted string means don't touch this field" convention (a plain `string`, not a pointer) — so e.g. sending `"title": ""` does NOT clear the title, it's treated the same as omitting it. `liveSummary` is the markdown (incl. mermaid) summary built incrementally during recording — the frontend sends it at save time (both the normal and retry update paths, when present), and the summarize pipeline feeds it into final-summary generation as prior context. Capped server-side at 32,000 characters (`400 BAD_REQUEST` beyond that).

#### Delete Meeting

```
DELETE /api/meetings/{meetingId}

Response: 204 No Content
Error: 403 Forbidden (only owner can delete)
```

---

### Accounts

An Account (customer) is a first-class entity shared by a team. Its creator automatically becomes the `owner` member, and only the owner can add members. Membership (role: owner/AM/TAM/SSA/SA/SA Manager/AM Manager — the assignable list is `model.AssignableRoles`) is the access control mechanism. All endpoints require auth.

#### List Accounts (my accounts)

```
GET /api/accounts

Response: 200 OK
{
  "accounts": [
    {
      "accountId": "uuid",
      "name": "Acme Bank",
      "role": "owner"            // owner | AM | TAM | SSA | SA | SA Manager | AM Manager
    }
  ]
}
```

Returns only accounts I'm a member of (GSI1 reverse lookup).

#### Create Account

```
POST /api/accounts
Request:
{
  "name": "Acme Bank",
  "aliases": ["Acme Financial"], // optional, tag-alias mapping
  "domains": ["acmebank.com"],   // optional
  "industry": "Finance"          // optional
}

Response: 201 Created
{
  "accountId": "uuid",
  "name": "Acme Bank",
  "aliases": ["Acme Financial"],
  "domains": ["acmebank.com"],
  "industry": "Finance",
  "ownerUserId": "owner-uuid",
  "members": [
    { "userId": "owner-uuid", "email": "owner@example.com", "role": "owner" }
  ],
  "createdAt": "2026-05-30T10:00:00Z"
}

Error: 400 Bad Request (empty name)
```

The creator automatically becomes an `owner` member.

#### Get Account Detail

```
GET /api/accounts/{accountId}

Response: 200 OK
{
  "accountId": "uuid",
  "name": "Acme Bank",
  "aliases": ["Acme Financial"],
  "domains": ["acmebank.com"],
  "industry": "Finance",
  "ownerUserId": "owner-uuid",
  "members": [
    { "userId": "owner-uuid", "email": "owner@example.com", "role": "owner" },
    { "userId": "tam-uuid", "email": "tam@example.com", "role": "TAM" }
  ],
  "createdAt": "2026-05-30T10:00:00Z"
}

Error: 403 Forbidden (not a member)
Error: 404 Not Found (account doesn't exist)
```

#### Add Member (any account member — ADR-034)

```
POST /api/accounts/{accountId}/members
Request:
{
  "email": "tam@example.com",   // a registered OR invited-but-not-yet-logged-in user's email
  "role": "TAM"                 // AM | TAM | SSA | SA | SA Manager | AM Manager (owner can't be assigned)
}

Response: 201 Created
{
  "userId": "tam-uuid",
  "email": "tam@example.com",
  "role": "TAM"
}

// If email belongs to a Cognito user who has been invited (admin-created)
// but never completed a first login, the grant is queued instead of
// rejected -- it materializes into a real membership on that email's next
// ListMeetings/CreateMeeting call after logging in (see PendingShare in
// backend/internal/model). The queued grant is not listed anywhere
// (revoke by re-submitting the same email, below, not by finding it in a
// list) and is un-claimable after a 30-day TTL enforced synchronously in
// application code; DynamoDB's own table TTL sweep (scoped to a distinct
// `pendingShareExpiresAt` attribute, not QA's `TTL`) later physically
// reclaims rows nobody ever revoked or claimed -- see PendingShare's
// doc comment.
Response: 201 Created
{
  "email": "tam@example.com",
  "role": "TAM",
  "pending": true                 // userId omitted -- not yet known
}

Error: 403 Forbidden (not a member of this account -- owner status is not required, ADR-034)
Error: 404 Not Found (email has never been invited at all)
Error: 400 Bad Request (already a member, or invalid role)
```

> Since ADR-034, any existing member of the account may add another -- not just the owner. The role allowlist (`owner` excluded) is unchanged, so no member can grant owner-level standing this way.

#### Revoke Pending Member Invite (owner only)

```
DELETE /api/accounts/{accountId}/members/pending?email={email}

Cancels a queued PendingShare account invite before the target has ever
logged in (no userId exists yet, so this can't go through DELETE
.../members/{userId}). A DeleteItem on an already-gone/never-existed row
that never resolves to a live member is a silent no-op -- "revoked" and
"there was nothing to revoke" both return 204. If the row is gone because
MaterializePendingShares won the race (the invitee logged in and claimed
the grant first), this returns 409 instead of a silent success -- the
caller would otherwise believe access was revoked while it's still live.

Response: 204 No Content
Error: 403 Forbidden (not the owner)
Error: 400 Bad Request (missing email query parameter)
Error: 409 Conflict (already claimed by materialize -- remove via Members instead)
```

#### Update Member Role (any account member — ADR-034)

```
PUT /api/accounts/{accountId}/members/{userId}
Request:
{
  "role": "AM"                  // AM | TAM | SSA | SA | SA Manager | AM Manager (cannot change to owner)
}

Response: 200 OK
{
  "userId": "tam-uuid",
  "email": "tam@example.com",
  "role": "AM"
}

Error: 403 Forbidden (not a member of this account -- owner status is not required, ADR-034)
Error: 404 Not Found (member doesn't exist)
Error: 400 Bad Request (invalid role, or target is the owner)
```

> Since ADR-034, any existing member may change another member's role -- not just the owner. The owner's own role can never be changed via this path, and the role allowlist still excludes `owner`.

#### Remove Member (owner only)

```
DELETE /api/accounts/{accountId}/members/{userId}[?force=true]

Response: 204 No Content (membership deleted and Share cleanup for all meetings fully succeeded)
Response: 200 OK (force=true; membership deleted, but Share cleanup failed for some meetings, or an ambiguous untagged Share was found)
{
  "removed": true,
  "cleanupFailedForMeetings": ["meeting-id-1", "meeting-id-2"],
  "ambiguousUntaggedMeetingIDs": ["meeting-id-3"]
}

Error: 403 Forbidden (not the owner)
Error: 404 Not Found (member doesn't exist)
Error: 400 Bad Request (owner can't be removed)
Error: 400 Bad Request (called without force=true, and the target holds at least one Share on an account-linked meeting whose origin isn't tagged "account" — i.e. ambiguous. Membership is NOT deleted; retry with ?force=true, which returns the same body as the 200 case above)
Error: 500 Internal Server Error (failed to list meetings to check for cleanup — membership is untouched, safe to retry)
```

> Unlike Add Member and Update Member Role (opened to any member, ADR-034), removal deliberately stays owner-only: it's destructive (revokes access, can cascade into meeting-share cleanup below) where a bad add/role-change is cheap to reverse.
>
> Removing membership immediately blocks new access to meetings that have no per-user Share record. For meetings that do have a Share record, the same `RemoveMember` request does a best-effort cleanup across the account's meeting refs, but only reclaims Shares tagged `origin=="account"`. This isn't atomic with the membership delete, and doesn't touch direct Shares the owner granted separately.
>
> **`force` parameter (fail-closed default)**: a Share whose `Origin != "account"` (effectively `Origin==""`) could be either an owner-granted direct grant or a legacy account-share predating the `Origin` field — the system can't tell which (see [ADR-023](decisions/ADR-023-share-origin-provenance-and-legacy-migration.md)). Without `force`, `RemoveMember` refuses the membership delete outright (400, membership untouched) the moment the target holds any such ambiguous Share — it does not fail-open by deleting membership first and reporting ambiguity after the fact. `?force=true` skips this precheck, deletes membership, leaves ambiguous Shares alone, and reports them in `ambiguousUntaggedMeetingIDs`. A lookup error during the precheck (including transient DynamoDB errors) returns 500 and leaves membership intact (safe to retry) — the precheck itself is a security gate, so tolerating transient errors here would reopen the gap it closes. This check only applies to meetings with `SharedToAccount == true`; link-only meetings (`AccountID` set, `SharedToAccount` false) are unaffected.
>
> **Meeting-list lookup failure**: the `ListMeetingRefsForAccount` call used to determine cleanup targets runs *before* the membership delete — if it fails, membership stays intact and the caller gets 500, so the same request is safely retryable.
>
> **A cleanup failure never leaves access behind**: per-meeting cleanup failures surface in the response's `cleanupFailedForMeetings`, but cleanup isn't the only access control. Every read path re-verifies current account membership live rather than trusting an `origin=="account"` Share row: meeting detail (`checkAccess`), meeting list (`ListMeetings`), KB Q&A (`KnowledgeService.Ask`, currently unused/unwired but kept consistent for future reuse), and the Python QA Lambda (`_list_shared_meetings`). The Python path caches only the immutable identifiers of which meetings are shared (`SHARED_MEETINGS_CACHE_TTL_SECONDS`, default 300s) — live membership/origin/`sharedToAccount` are re-checked on every call, so removal takes effect on the next QA request regardless of that TTL. The KB search cache (`KB_CACHE_TTL_SECONDS`, default 600s) stores an access signature alongside cached results and treats an access change as a cache miss. So a stale Share row alone can't restore access even if cleanup itself failed; `cleanupFailedForMeetings` is a useful signal for tidying stale rows, not a security requirement.
>
> **Where this guarantee doesn't apply**: legacy shares predating the `Origin` field (`origin==""`) are indistinguishable from direct grants and are trusted unconditionally until backfilled via the CLI below.
>
> **Known limitation & remediation**: Share records created by `share-account` before this fix has no origin tag and is treated as a direct grant, so `RemoveMember`'s cleanup can't auto-reclaim it. Since removal without `force` is now blocked in that case, silent lingering access is now confined to cases where the owner explicitly passes `force=true` — `ambiguousUntaggedMeetingIDs` in the response identifies affected meetings, though as a coarse signal (a meeting also appears there if the removed member holds a separate direct Share, regardless of whether a legacy account-share actually exists). `backend/cmd/backfill-share-origin` (operator-run per `--account-id`, dry-run by default, `--apply` to commit) retroactively tags such records with `origin=account` so they become subject to cleanup. This CLI can't distinguish an ambiguous candidate automatically (same meeting shared both via account and directly) — untrustworthy candidates must be excluded via `--exclude userId1:meetingId1,...` before `--apply`, or a direct grant risks being mistagged as account-origin and later auto-reclaimed. It enumerates by meeting rather than current membership, so it can also tag legacy shares for users already removed — running backfill before removing a member is still the friction-free path. One case it can never tag: a meeting later un-shared from this account or re-shared to another — that share is reported `ORPHANED` and left for the meeting owner to clean up manually via `RevokeShare`. Full design background: [ADR-023](decisions/ADR-023-share-origin-provenance-and-legacy-migration.md).

#### List Account Meetings (shared meetings — members only)

```
GET /api/accounts/{accountId}/meetings

Response: 200 OK
{
  "meetings": [
    {
      "meetingId": "uuid",
      "ownerUserId": "owner-uuid",
      "title": "ROSA Review",
      "date": "2026-05-30T10:00:00Z"
    }
  ]
}

Error: 403 Forbidden (not a member)
Error: 404 Not Found (account doesn't exist)
```

#### List Account Insights (raw insight material — members only)

Returns the 8 insight types extracted from meetings and fanned out to the Account's partition. `from`/`to` are optional RFC3339 time-range filters; `types` is an optional comma-separated type filter — both applied client-side in the service layer (§6.3).

8 insight types: `trend`, `need`, `competitive`, `risk`, `opportunity`, `tech`, `stakeholder`, `action`

```
GET /api/accounts/{accountId}/insights?from=<RFC3339>&to=<RFC3339>&types=risk,opportunity

Response: 200 OK
{
  "insights": [
    {
      "type": "risk",
      "text": "PoC schedule may slip by 2 months",
      "implication": "Risk that PoC results won't be ready before Q3 renewal negotiation",
      "nextAction": "TAM to confirm infra approval status this week",
      "sourceType": "meeting",
      "sourceId": "meeting-uuid",
      "occurredAt": "2026-05-12T09:00:00Z",
      "tsMarker": "[TS:120]",
      "entities": ["PoC"]
    }
  ]
}
```

`implication`/`nextAction` are optional fields, populated when `ExtractInsights` (Bedrock Haiku) generates structured reasoning alongside the insight — earlier versions only had `type`/`text`. `ExtractInsights` also generates `evidence` (a near-verbatim quote), but it is **deliberately excluded** from this fanned-out response, to avoid exposing raw quotes to account/project members without access to the source meeting (`BuildAccountInsights`, `meeting.go`). `evidence` is only exposed via the meeting's own `Insights` JSON, to users with meeting access. `Project.Insights` (`GET /api/projects/{projectId}/insights`, `GET /api/projects/{projectId}/brief`) shares the same schema (frontend type `FieldInsight`; backend `MeetingInsight`/`ProjectInsightDTO`) and the same fan-out policy.

```
Error: 400 Bad Request (invalid from/to — not RFC3339)
Error: 403 Forbidden (not a member)
Error: 404 Not Found (account doesn't exist)
```

#### Get Account Brief (bundled raw material — members only)

Bundles one account's raw material (metadata + insights by type + shared meetings) into a single call — the "batch consumption" endpoint used by a local agent preparing SFDC/SIFT/2by2/Player Card material. Composes the existing `GetAccount`+`ListAccountMeetings`+`ListAccountInsights` in the service layer, inheriting the same member gate. `from`/`to`/`types` filters behave the same as the insights endpoint.

```
GET /api/accounts/{accountId}/brief?from=<RFC3339>&to=<RFC3339>&types=risk,opportunity

Response: 200 OK
{
  "account": { "accountId": "acc-uuid", "name": "Acme Bank", "members": [ ... ], ... },
  "insightsByType": {
    "risk": [ { "type": "risk", "text": "...", "occurredAt": "2026-05-12T09:00:00Z", ... } ],
    "opportunity": [ ... ]
  },
  "meetings": [ { "meetingId": "meeting-uuid", "title": "ROSA PoC", "ownerUserId": "...", "date": "2026-05-12T09:00:00Z" } ]
}

Error: 400 Bad Request (invalid from/to — not RFC3339)
Error: 403 Forbidden (not a member)
Error: 404 Not Found (account doesn't exist)
```

#### Update Research (rename display title)

`Research.Title` is a user-editable display label, defaulting to `Topic` (the original research prompt) at creation. `Topic` itself is permanently immutable — the agent pipeline (research-worker, research-agent) acts on `Topic`, so a rename must never look like it changed what was researched. `AccountResearchRef`/`ProjectResearchRef` (denormalized link snapshots) still carry only `Topic`, not `Title` — a rename does not propagate to those.

```
PUT /api/research/{researchId}
{ "title": "새 제목" }

Response: 200 OK
{ "researchId": "r-uuid" }

Error: 400 Bad Request (title empty or longer than 200 chars)
Error: 403 Forbidden (not the owner)
Error: 404 Not Found (research doesn't exist)
```

#### List Account Research (linked research — members only)

Read path for research tasks linked to an Account (`POST /api/research/{researchId}/accounts`, `DELETE /api/research/{researchId}/accounts/{accountId}` — the rest of research CRUD is existing functionality not yet documented here). On link, `accountIds` is updated with an atomic DynamoDB String Set `ADD`/`DELETE`, so concurrent link requests don't race. Reads re-verify each result's `accountIds` actually contains the target accountId (fail-closed) — a failed reverse-index cleanup after unlinking never leaks a stale entry into the list.

```
GET /api/accounts/{accountId}/research

Response: 200 OK
{ "research": [ { "researchId": "r-uuid", "topic": "...", "summary": "...", "status": "done", "ownerUserId": "...", "createdAt": "..." } ] }

Error: 403 Forbidden (not a member)
```

#### Put Account Document (ingest a local document — members only)

Stores a locally-authored document (email notes, calendar, prep notes, etc.) into an Account as inline markdown (≤300KB), so non-Obsidian teammates can read it in TTOBAK. A loop-guard rejects any TTOBAK-originated document (identified by `ttobak_id` frontmatter).

`docType` is a free string (existing values like `"prep"`/`"reference"` and the Document Hub v2 UI's `"note"`/`"blog"`/`"slide"` all share this field; the server does no enum validation). Wikilinks in `markdown` (`[[Doc Name]]`, `[[Doc Name|Alias]]`, `[[Doc Name#Section]]`) are parsed on save into a normalized title list stored in `links` (data source for a future graph view). Slides (PPTX/PDF) pass `fileKey` instead of `markdown` (an S3 key from a presigned PUT, must be prefixed `docs/{my userId}/`) — this put call itself is the upload-complete record; there's no separate `/api/upload/complete` step.

```
POST /api/accounts/{accountId}/documents
{ "title": "Email notes", "markdown": "# Prep\n\n[[Acme Bank]] meeting prep...", "docType": "prep", "path": "Accounts/Acme Bank/prep.md" }
{ "title": "Slide Deck", "docType": "slide", "fileKey": "docs/user-uuid/1234567890_deck.pdf", "fileName": "deck.pdf", "mimeType": "application/pdf", "fileSize": 123456 }

Response: 201 Created
{ "docId": "doc-uuid", "title": "Email notes", "docType": "prep", "path": "Accounts/Acme Bank/prep.md", "links": ["Acme Bank"], "sourceUserId": "user-uuid", "createdAt": "2026-05-30T09:00:00Z", "updatedAt": "2026-05-30T09:00:00Z" }

Error: 400 Bad Request (missing title, neither markdown nor fileKey, or markdown >300KB)
Error: 400 Bad Request (TTOBAK-originated — loop guard)
Error: 403 Forbidden (not a member, or fileKey doesn't have my userId prefix)
Error: 404 Not Found (account doesn't exist)
```

#### List Account Documents (ingested documents — members only)

```
GET /api/accounts/{accountId}/documents?docType=prep

Response: 200 OK
{ "documents": [ { "docId": "doc-uuid", "title": "Email notes", "docType": "prep", "path": "...", "sourceUserId": "...", "createdAt": "2026-05-30T09:00:00Z", "updatedAt": "2026-05-30T09:00:00Z" } ] }
```

`links`/`fileName` appear only when non-empty (`omitempty`) — a document with no wikilinks or file omits the field entirely (not an empty array/`null`).

```
Error: 403 Forbidden (not a member)
Error: 404 Not Found (account doesn't exist)
```

The list omits `content` (fetch the full body via Get).

#### Get Account Document (full content — members only)

```
GET /api/accounts/{accountId}/documents/{docId}

Response: 200 OK
{ "docId": "doc-uuid", "title": "Email notes", "docType": "prep", "path": "...", "links": ["Acme Bank"], "sourceUserId": "...", "createdAt": "2026-05-30T09:00:00Z", "updatedAt": "2026-05-30T09:00:00Z", "content": "# Prep\n\n[[Acme Bank]] meeting prep..." }
```

A slide (a document with `fileName`) has empty `content` and a populated `downloadUrl` (the original file, 1h TTL). PPTX/PPT additionally gets `previewUrl` (PDF sidecar, present only once conversion finishes — ADR-022); `downloadUrl` always points at the original, never the sidecar. Both fields are omitted entirely when absent.

```
Error: 403 Forbidden (not a member)
Error: 404 Not Found (document doesn't exist)
```

#### Update / Delete Account Document (members only)

Update follows field-level "omit to preserve" semantics — `docId`/`sourceUserId`/`createdAt` are always preserved. `title` is required (rejected if empty); `docType`/`path` keep their existing value if omitted, or get replaced if sent. `markdown` uses `*string`: omitting the JSON key preserves the body, sending an explicit empty string clears it. Omitting both `markdown` and `fileKey` preserves whichever is currently set (no longer an error); sending both is rejected (a doc is note/blog OR slide, never both). A non-empty `markdown` re-runs link parsing and the loop guard; a changed non-empty `fileKey` re-verifies ownership (my userId prefix). Slide→note conversion: send only `markdown` (clears the file fields). Note→slide: send only `fileKey` (clears the body).

```
PUT /api/accounts/{accountId}/documents/{docId}
{ "title": "Email notes v2", "markdown": "# Prep v2\n..." }

Response: 200 OK
{ "docId": "doc-uuid", "title": "Email notes v2", ... }

DELETE /api/accounts/{accountId}/documents/{docId}
Response: 204 No Content

Error: 400 Bad Request (missing title, both markdown and fileKey set, markdown >300KB, or TTOBAK-originated)
Error: 403 Forbidden (not a member, or fileKey doesn't have my userId prefix)
Error: 404 Not Found (document doesn't exist)
```

#### Personal Documents (not account-scoped — owner only)

Personal notes/blogs/slides for `ttobak_ask`/Document Hub v2. Stored under `PK: USER#{my userId}`, so ownership is inherent in the key and no account-membership check is needed. Request/response schemas match Account documents (just without the accountId path segment).

```
POST   /api/documents                 { "title": "...", "markdown": "...", "docType": "note" }
GET    /api/documents?docType=note    → { "documents": [ AccountDocumentDTO, ... ] }
GET    /api/documents/{docId}         → AccountDocumentDetail
PUT    /api/documents/{docId}         { "title": "...", "markdown": "..." }
DELETE /api/documents/{docId}         → 204 No Content

Error: 400 Bad Request / 404 Not Found — same semantics as the Account document endpoints above
(no membership check, so no "not a member" 403; a foreign fileKey still gets 403 —
the PK proves ownership, fileKey-prefix validation is a separate check)
```

#### Slide Upload (presigned URL for documents)

The existing presigned upload endpoint (`POST /api/upload/presigned`) gained a `"doc"` category. `fileType` only allows `application/pdf` or PowerPoint MIME types (`application/vnd.openxmlformats-officedocument.presentationml.presentation`, `application/vnd.ms-powerpoint`). `meetingId` isn't needed (documents aren't tied to a meeting). The S3 key is `docs/{my userId}/{timestamp}_{fileName}`, passed straight through as `fileKey` to Put Document.

```
POST /api/upload/presigned
{ "fileName": "deck.pdf", "fileType": "application/pdf", "category": "doc" }

Response: 200 OK
{ "uploadUrl": "https://...presigned-put-url...", "key": "docs/user-uuid/1234567890_deck.pdf", "expiresIn": 3600 }

Error: 400 Bad Request (fileType is not pdf/PowerPoint MIME)
```

PUT the file directly to `uploadUrl`, then pass the response's `key` as `fileKey` to Put Document — there's no `/api/upload/complete` call.

#### Slide Preview (PPTX → PDF conversion, ADR-022)

A PPTX/PPT uploaded under the `docs/` prefix triggers a separate container Lambda (`cmd/convert-doc`) via an EventBridge S3 event, which generates a PDF sidecar with headless LibreOffice (deterministic key, no DynamoDB write — the document record may not exist yet). In Get Account/Personal Document responses, `downloadUrl` **always** points at the original file; for a PPTX/PPT, `previewUrl` (the PDF sidecar) is populated separately once conversion finishes — `downloadUrl` never switches to the sidecar. `previewUrl` is omitted while conversion is still in progress (poll and re-fetch). There's no separate public REST endpoint for this. This "`downloadUrl` is never the sidecar" rule is about **JSON field names** — the Public Share Link's `GET /api/public/docs/{token}` below is a 302 redirect rather than a field, and its redirect target deliberately goes to the sidecar when one exists (an unauthenticated visitor wants a preview, so unlike everywhere else, sidecar-if-present takes priority there, falling back to the original otherwise).

#### Share Document to Account (personal document → team copy)

Shares a personal document with an Account's team. Works for both slides and notes (no slide-only check in code — markdown documents copy their body as-is). A slide is **copied**, not referenced — an S3 `CopyObject` to a new key (`docs/{my userId}/{ms}_{randomId}_{fileName}`) backs a new `AccountDocumentDTO` (the original isn't overwritten, so later edits to it don't affect the shared copy). This looks like a different key layout from Slide Upload's `docs/{my userId}/{timestamp}_{fileName}`, but it's the same rule — upload uses `{timestamp}_{fileName}`, share-copy inserts a `generateID()` to avoid collisions (`{ms}_{randomId}_{fileName}`); both keep the `docs/{userId}/` prefix and preserve the filename.

```
POST /api/documents/{docId}/share-account
{ "accountId": "acc-uuid" }

Response: 201 Created
{ "docId": "new-doc-uuid", "title": "Slide Deck", "docType": "slide", "fileName": "deck.pdf", ... }  (AccountDocumentDTO)

Error: 400 Bad Request (missing accountId)
Error: 403 Forbidden (not the document owner, or not a member of accountId)
Error: 404 Not Found (document doesn't exist)
```

#### Share Document with a User (personal document → one person, by reference, read-only)

Shares a personal document with exactly one other user by email. Unlike Share to Account (which copies the S3 object), this shares **by reference** — only one copy exists, the recipient always sees the owner's current edits, and revoking makes it disappear immediately. Always **read-only**: the repository hardcodes `permission` to `read` and the request body has no `permission` field at all (unlike meeting sharing). Only the owner can issue/revoke/list shares; a non-owner caller gets 404 rather than 403, since the document isn't in their partition (fail-closed, doesn't reveal whether the doc exists).

On the recipient's side, `GET /api/documents` (list) and `GET /api/documents/{docId}` (detail) both include a `sharedBy` field (owner's email) — the frontend uses it as the read-only indicator (`ShareButton`'s `readOnly` prop). The recipient's detail response deliberately omits `publicShareToken` (issuing/revoking a public link is owner-only). If the owner deletes the document, share records aren't cascaded but are silently skipped on list.

Internally these share records use dedicated DynamoDB prefixes (`SHAREDDOC#` / `DOCSHARE_TO#`, `EntityType=DOC_SHARE`) distinct from meeting/research sharing — reusing `SHARED#` would mix document shares into the shared-meetings list (which reads via `begins_with(SK, "SHARED#")`) and corrupt its pagination (ADR-029).

```
POST /api/documents/{docId}/share
{ "email": "teammate@example.com" }

Response: 201 Created
{ "sharedWith": { "userId": "user-uuid", "email": "teammate@example.com", "permission": "read" } }

GET /api/documents/{docId}/shares
Response: 200 OK
{ "shares": [ { "userId": "user-uuid", "email": "teammate@example.com", "permission": "read", "sharedAt": "..." } ] }

DELETE /api/documents/{docId}/share/{userId}
Response: 204 No Content

Error: 400 Bad Request (missing email, or sharing with yourself)
Error: 404 Not Found (document doesn't exist, caller isn't the owner — 404 instead of 403 to hide
                       existence — or no user with that email)
```

#### Public Share Link (unauthenticated public link for a personal file document)

Issues a 128-bit random token (`crypto/rand`) for unauthenticated access. Only allowed for documents with a `fileKey` (slides/PDFs — markdown notes are excluded). Only the authenticated owner can issue/revoke, but the link itself (`GET /api/public/docs/{token}`) is registered under the CloudFront `/api/public/*` behavior, skipping both the API Gateway JWT authorizer and the Lambda@Edge JWT check — unlike every other route under `/api/*`, which passes through both layers (see [ADR-022](decisions/ADR-022-slide-preview-conversion-and-public-share-links.md)). The handler never returns document content directly — it always 302-redirects to a signed GET URL (`https://{domain}/media/...`, ADR-027 — or the PDF sidecar if one exists). Issuing is atomic across concurrent requests (`SetPublicShareTokenIfAbsent` conditional write), so a double-click can't mint two tokens and orphan one.

```
POST /api/documents/{docId}/public-share
Response: 200 OK
{ "token": "8f2c...128-bit-random..." }

DELETE /api/documents/{docId}/public-share
Response: 204 No Content

GET /api/public/docs/{token}   (no auth header)
Response: 302 Found → Location: <signed GET URL, 5-minute TTL (https://{domain}/media/...)>

Error: 400 Bad Request (target document has no fileKey — markdown notes can't be publicly shared)
Error: 403 Forbidden (not the document owner — issue/revoke only)
Error: 404 Not Found (document doesn't exist, or token revoked/expired)
```

This route's signed URL uses a 5-minute TTL (`PublicShareURLTTL`), shorter than the 1-hour default elsewhere — a deliberate shortening (ADR-022) to narrow the window a URL stays live after revocation. 5 minutes is still not zero, so revocation isn't instantly airtight — a known, accepted limitation.

#### Export Vault (Obsidian markdown export)

Renders the caller's own meetings and documents as Obsidian-friendly markdown (YAML frontmatter) and returns them as a file list; an MCP client writes each file into a local vault.

- Meetings: `Accounts/{name}/` (account-shared) or `_Private/Meetings/` (private)
- Documents with a markdown body (slides excluded): `Accounts/{name}/Docs/` (by account membership) or `_Private/Docs/` (personal). Frontmatter includes `doc_type`, `links`, `ttobak_id` (ADR-020) — re-ingestion applies the same loop guard as ADR-017.

```
GET /api/vault/export

Response: 200 OK
{ "files": [
  { "path": "Accounts/Acme Bank/2026-05-12 ROSA Review.md", "markdown": "---\naccount: \"[[Acme Bank]]\"\n...\n---\n\n# ROSA Review\n..." },
  { "path": "Accounts/Acme Bank/Docs/Meeting Prep.md", "markdown": "---\ndoc_type: note\nttobak_id: doc-uuid\n---\n\nPrep content..." }
] }

Error: 403 Forbidden
```

#### Link Meeting to Account (classification only — owner+member only)

```
POST /api/meetings/{meetingId}/account
Request:
{
  "accountId": "acc-uuid"
}

Response: 200 OK
{
  "accountId": "acc-uuid"
}

Error: 403 Forbidden (not owner, or not a member of that account)
Error: 404 Not Found (meeting doesn't exist)
```

#### Share Meeting to Account (team share — owner+member only)

Shares a meeting with an Account's team: sets `accountId`+`sharedToAccount`, grants a `read` Share to every account member except the owner, and adds a MeetingRef to the Account partition.

```
POST /api/meetings/{meetingId}/share-account
Request:
{
  "accountId": "acc-uuid"
}

Response: 200 OK
{
  "accountId": "acc-uuid",
  "sharedWith": 2          // number of members granted read access (excluding owner)
}

Error: 403 Forbidden (not owner, or not a member of that account)
Error: 404 Not Found (meeting doesn't exist)
```

---

### Projects

A Project (SFDC Opportunity) is a first-class entity that groups meeting notes, research, and insights by sales opportunity. Unlike Account, it's a **many-to-many graph** — it can link to multiple accounts at once (e.g. a partner plus the end customer). It reuses the same graph-reference pattern as Research↔Account linking (string set + reverse-index item + fail-closed reverification) — see ADR-025 for the data model.

**Access** is hybrid: project owner, a directly-invited member (`POST .../members`), or **a member of any linked Account**. Linking an Account to a project auto-extends viewing access to that Account's whole team (no separate invite needed). SFDC integration is metadata-only (`sfdcOpptyId`/`sfdcUrl`) — an external MCP client (SFDC MCP → `ttobak_create_project`) is responsible for the real SFDC data; there's no server-side SFDC API integration.

#### List My Projects

```
GET /api/projects

Response: 200 OK
{ "projects": [ { "projectId": "uuid", "name": "...", "stage": "...", "sfdcOpptyId": "..." } ] }
```

Returns projects where I'm owner, a direct member, or a member of a linked Account (owner index + GSI1 member reverse-lookup + my Account memberships cross-referenced against each candidate — all three canonically reverified, mirroring `requireProjectAccess`'s hybrid access check). The same projects are also discoverable via `GET /api/accounts/{accountId}/projects` — the two are alternative discovery paths.

#### Create Project

```
POST /api/projects
Request:
{
  "name": "Acme Bank Cloud Migration",
  "description": "...",          // optional
  "sfdcOpptyId": "006XX...",      // optional
  "sfdcUrl": "https://...",       // optional
  "stage": "Negotiation"          // optional
}

Response: 201 Created
{
  "projectId": "uuid", "name": "...", "description": "...",
  "sfdcOpptyId": "006XX...", "sfdcUrl": "https://...", "stage": "Negotiation",
  "ownerUserId": "owner-uuid", "accountIds": [], "members": [],
  "createdAt": "2026-07-21T00:00:00Z", "updatedAt": "2026-07-21T00:00:00Z"
}

Error: 400 Bad Request (empty name)
```

#### Get / Update / Delete Project

```
GET /api/projects/{projectId}
PUT /api/projects/{projectId}      (owner only, same fields as Create)
DELETE /api/projects/{projectId}   (owner only)

Error: 403 Forbidden — GET: not owner/direct member/linked-Account member
Error: 403 Forbidden — PUT/DELETE: not owner (direct/linked-Account membership doesn't count)
Error: 404 Not Found
Error: 400 Bad Request (DELETE: rejected while any account/meeting/research/member
       relation still exists — all must be unlinked first, to avoid orphaned relations)
```

#### Members (owner only)

```
POST   /api/projects/{projectId}/members
Request: { "email": "user@example.com" }
Response: 201 Created — { "userId": "uuid", "email": "user@example.com" }
Error: 400 Bad Request (already a member) · 404 Not Found (no user with that email)

DELETE /api/projects/{projectId}/members/{userId}
Response: 204 No Content
```

Unlike Account, membership has no role distinction — it's binary (owner, or member).

#### Link / Unlink Account

```
POST   /api/projects/{projectId}/accounts        (owner only, must be a member of the target Account)
Request: { "accountId": "uuid" }
Response: 200 OK — { "accountIds": ["uuid", ...] }
Error: 403 Forbidden (not owner, or not a member of the target Account)

DELETE /api/projects/{projectId}/accounts/{accountId}   (owner only)
Response: 204 No Content
```

Both link and unlink atomically update `Project.accountIds` (String Set) and the reverse index (`ACCOUNT#{accountId}/PROJECTREF#{projectId}`) via a single `TransactWriteItems` call (ADR-025). Unlinking only requires project ownership, not current membership in that Account — otherwise an owner removed from the Account could never unlink it.

#### Link / Unlink Meeting, Research

```
POST   /api/projects/{projectId}/meetings          Request: { "meetingId": "uuid" }
DELETE /api/projects/{projectId}/meetings/{meetingId}
POST   /api/projects/{projectId}/research          Request: { "researchId": "uuid" }
DELETE /api/projects/{projectId}/research/{researchId}

Error: 404 Not Found — Link(POST) Meeting: target meeting isn't owned by the caller (looked up
       only in the caller's own partition, so someone else's meeting is indistinguishable from
       nonexistent) or the meeting/project doesn't exist
Error: 403 Forbidden — Link(POST) Research: target research isn't owned by the caller
       (research lookup itself isn't owner-gated, so ownership is checked explicitly)
Error: 403 Forbidden — Link(POST), either case: passes the ownership check above but still lacks
       project access (owner/direct member/linked-Account member)
Error: 403 Forbidden — Unlink(DELETE): caller is neither the target's owner nor the project owner (see asymmetry below)
Error: 404 Not Found (research/project doesn't exist)
```

`Meeting.projectIds`/`Research.projectIds` link the same way — String Set + atomic `TransactWriteItems`. **Linking** requires being the target meeting/research's owner, but **unlinking** only requires being that owner *or* the **project owner** — this asymmetry (ADR-025) prevents a deadlock where a member who linked something, then got removed via `RemoveMember`, could no longer unlink it (having lost project access), and the project owner couldn't either (not owning the meeting/research). This is independent of `SharedToAccount` (the account-sharing gate) — a meeting's title/insights, once linked to a project, are visible to everyone with project access regardless (ADR-025), a separate sharing channel that deliberately bypasses `SharedToAccount`.

#### List Project Meetings / Research

```
GET /api/projects/{projectId}/meetings
Response: 200 OK — { "meetings": [ { "meetingId", "ownerUserId", "title", "date" } ] }

GET /api/projects/{projectId}/research
Response: 200 OK — { "research": [ { "researchId", "topic", "summary", "status", "ownerUserId", "createdAt" } ] }

Error: 403 Forbidden (no project access)
```

Both lists reverify each reverse-index candidate against the canonical `projectIds` set (fail-closed) — a stale ref left behind by a failure outside the link/unlink transaction (e.g. the underlying meeting deleted through some other path) never surfaces in results. Also deduplicated by `meetingId` (defends against ADR-025's mutable-Date reference SK issue).

#### Get Project Insights

```
GET /api/projects/{projectId}/insights?from=RFC3339&to=RFC3339&types=risk,tech

Response: 200 OK
{ "insights": [ { "type": "risk", "text": "...", "sourceId": "meeting-uuid", "occurredAt": "...", "tsMarker": "[TS:120]", "entities": [] } ] }

Error: 400 Bad Request (from/to not RFC3339, or invalid insight type)
Error: 403 Forbidden (no project access)
```

**Aggregated at read time, never persisted** — parses linked meetings' `Insights` JSON on every call, so a re-summarized meeting's updated insights show up on the next fetch automatically (unlike Account insights, which snapshot at share time — there's no sync-drift possible here at all).

#### Get Project Brief

```
GET /api/projects/{projectId}/brief?from=RFC3339&to=RFC3339&types=risk,tech

Response: 200 OK
{
  "project": { ... ProjectResponse ... },
  "insightsByType": { "risk": [...], "tech": [...] },
  "meetings": [...],
  "research": [...]
}
```

A convenience endpoint bundling Get Project + List Meetings + List Research + Get Insights in one call.

#### List Account's Projects

```
GET /api/accounts/{accountId}/projects   (members of that account only)

Response: 200 OK
{ "projects": [ { "projectId", "name", "stage", "sfdcOpptyId" } ] }
```

The Account-side read path for "projects linked to this account" (same pattern as Research's `GET /api/accounts/{accountId}/research`).

---

### Sharing

#### Share Meeting

```
POST /api/meetings/{meetingId}/share
Request:
{
  "email": "bob@example.com",
  "permission": "read"          // "read" | "edit"
}

Response: 200 OK
{
  "sharedWith": {
    "userId": "uuid",
    "email": "bob@example.com",
    "permission": "read"
  }
}

// email is invited (Cognito account exists) but has never logged in yet --
// queued as a PendingShare instead of a real Share row; materializes on
// that email's next ListMeetings/CreateMeeting call after logging in
// (not listed anywhere -- revoke by re-submitting the same email, below,
// not by finding it in a list -- and un-claimable after 30 days via a
// synchronous application-code TTL check; DynamoDB's own table TTL sweep,
// scoped to a distinct `pendingShareExpiresAt` attribute so it can't touch
// QA's unrelated `TTL`-named rows, later physically reclaims rows nobody
// ever revoked or claimed).
Response: 200 OK
{
  "sharedWith": {
    "email": "bob@example.com",
    "permission": "read",
    "pending": true                // userId omitted -- not yet known
  }
}

Error: 403 Forbidden (only owner can share)
Error: 404 User not found (email has never been invited at all)
```

#### Revoke Share

```
DELETE /api/meetings/{meetingId}/share/{userId}

Response: 204 No Content
Error: 403 Forbidden (only owner can revoke)
```

#### Revoke Pending Share Invite (owner only)

```
DELETE /api/meetings/{meetingId}/share/pending?email={email}

Cancels a queued PendingShare meeting invite before the target has ever
logged in (no userId exists yet, so this can't go through DELETE
.../share/{userId}). A DeleteItem on an already-gone/never-existed row
that never resolves to a live share is a silent no-op -- "revoked" and
"there was nothing to revoke" both return 204. If the row is gone because
MaterializePendingShares won the race (the invitee logged in and claimed
the grant first), this returns 409 instead of a silent success -- the
caller would otherwise believe access was revoked while it's still live.

Response: 204 No Content
Error: 403 Forbidden (not the owner)
Error: 404 Not Found (meeting doesn't exist)
Error: 400 Bad Request (missing email query parameter)
Error: 409 Conflict (already claimed by materialize -- revoke direct access instead)
```

#### Search Users (for sharing)

```
GET /api/users/search?q={email-prefix}

Response: 200 OK
{
  "users": [
    {
      "userId": "uuid",
      "email": "bob@example.com",
      "name": "Bob Kim"
    }
  ]
}
```

---

### Upload

#### Get Presigned URL

```
POST /api/upload/presigned
Request:
{
  "fileName": "recording.webm",
  "fileType": "audio/webm",         // audio/webm | audio/mp4 | audio/x-m4a | image/jpeg | image/png
  "category": "audio"               // "audio" | "image"
}

Response: 200 OK
{
  "uploadUrl": "https://s3.amazonaws.com/bucket/...",
  "key": "audio/user-uuid/meeting-uuid/recording.webm",
  "expiresIn": 3600
}
```

#### Notify Upload Complete

```
POST /api/upload/complete
Request:
{
  "meetingId": "uuid",
  "key": "audio/user-uuid/meeting-uuid/recording.webm",
  "category": "audio"               // "audio" | "image"
}

Response: 200 OK
{
  "status": "processing"
}
```

---

### Real-time Translation (REST)

> Current implementation: real-time transcription/translation uses the Browser Speech API + REST calls, not WebSocket.

#### Translate Text

```
POST /api/translate
Request:
{
  "text": "Text to translate",
  "sourceLang": "ko",
  "targetLang": "en"
}

Response: 200 OK
{
  "translatedText": "Text to translate",
  "sourceLang": "ko",
  "targetLang": "en"
}
```

#### Live Summary (called every ~200 words)

```
POST /api/summarize-live
Request:
{
  "meetingId": "client-meeting-id",
  "text": "Full transcript text so far...",
  "previousSummary": "Previous summary (optional)"
}

Response: 200 OK
{
  "summary": "Summary so far..."
}
```

---

### STT Results

#### Select Transcript

```
PUT /api/meetings/{meetingId}/transcript
Request:
{
  "selected": "A"                   // "A" | "B"
}

Response: 200 OK
```

#### Re-diarize (re-analyze with a corrected speaker count, ADR-019)

When acoustic diarization (pyannote) detects fewer speakers than actually present, re-runs the same audio with a user-supplied speaker-count hint. Whisper-transcribed meetings only (AWS Transcribe fallback meetings have no acoustic diarization to redo), single-part audio only (v1 scope — multi-part would need per-part ECS re-trigger + resetting `AudioPartsReady`). Rather than calling ECS `RunTask` directly, it `CopyObject`s the existing audio to a new key (`audio/{userId}/{meetingId}/rediarize_{uuid}_...`), reusing the existing EventBridge S3 event → `ttobak-transcribe` pipeline — no new IAM permission needed on the `api` Lambda.

```
POST /api/meetings/{meetingId}/rediarize
{ "speakerCount": 6 }                 // 2-20

Response: 200 OK
{ "meetingId": "uuid", "status": "transcribing" }

Error: 400 Bad Request (speakerCount out of 2-20 range, non-whisper meeting, multi-part audio,
                         no audio, or meeting already processing)
Error: 403 Forbidden (not my meeting)
Error: 404 Not Found (meeting doesn't exist)
```

On call, immediately clears the meeting's `speakerMap` (re-analysis re-numbers `spk_N` from scratch, so old name mappings are meaningless) and resets `status` to `transcribing`, storing the requested `speakerCount` in `Meeting.DiarizationSpeakerHint` — `cmd/transcribe/main.go` uses this as pyannote's `max_speakers` hint instead of `len(Participants)`. This hint is **sticky**: once set, it applies to future re-transcriptions too (instead of the registered participant count). Clearing `speakerMap` is a conditional write (`UpdateMeetingFieldsIfMatch`) gated on the freshly-read current `status` — a double call only lets one succeed, the other gets 400 (`meeting is already being processed`). A `CopyObject` failure (including ambiguous SDK errors) has no dedicated recovery — it's left to the existing 60-minute stuck-transcribing auto-expiry (`GetMeeting` handler), since a separate rollback write could race with the re-trigger pipeline's own state transition.

#### Cost/sizing simulator (ADR-033, AgentCore Code Interpreter)

Extracts quantitative requirements (users, TPS, data volume, SLO...) from a done meeting, lets the user confirm/correct them, then runs a real Python computation in AgentCore Code Interpreter comparing 2-3 architecture options (TCO, chart PNGs, markdown report). `SimRun` is a singleton per meeting (`SIMRUN` sort key) — a fresh extraction overwrites any prior draft/result; running is gated behind `PutSimRunIfNotRunning`'s conditional write (no two concurrent runs per meeting). The generated code never receives the meeting transcript — only the server-validated requirements/options JSON — so a transcript can influence extracted *values* but never the *code itself* (see ADR-033's trust-boundary section).

```
POST /api/meetings/{meetingId}/sim/extract

Response: 200 OK
{
  "simRunId": "uuid", "status": "extracted",
  "requirements": [
    { "key": "monthlyActiveUsers", "label": "월간 활성 사용자", "value": "100000",
      "required": true, "source": "extracted", "evidence": "transcript://seg-12" }
  ],
  "createdAt": "...", "updatedAt": "..."
}

Error: 400 Bad Request (meeting not done yet)
Error: 403 Forbidden (not my meeting)
Error: 404 Not Found (meeting doesn't exist)
```

```
POST /api/meetings/{meetingId}/sim
{
  "requirements": [ { "key": "monthlyActiveUsers", "label": "...", "value": "100000",
                       "required": true, "source": "user" } ],
  "options": [ { "name": "서버리스", "description": "Lambda + API Gateway" },
               { "name": "컨테이너", "description": "ECS Fargate" } ]   // 2-3 required
}

Response: 202 Accepted
{ "simRunId": "uuid", "status": "queued", ... }

Error: 400 Bad Request (unknown requirement key, value out of range/not in allowlist,
                         missing required value, wrong option count 2-3, meeting not
                         done, or a simulation is already running for this meeting)
Error: 403 Forbidden (not my meeting)
Error: 404 Not Found (meeting doesn't exist)
```

Every field is re-validated server-side against a fixed allowlist (`AllowedSimRequirementKeys`) regardless of what the confirm form submits — the form is a UX gate, not the trust boundary. Async hand-off to `ttobak-sim` (`InvocationType=Event`); the frontend polls `GET /api/meetings/{meetingId}` for `simRun.status` (see below), not a new WebSocket channel — this is a 1-3 minute job, not a token stream. A `queued`/`running` run older than 20 minutes is reported as `error` at read time (mirrors the existing 60-minute `isStuck` reconciliation for transcribing/summarizing meetings) without being persisted that way.

`GetMeeting`'s response gains a `simRun` field (same shape as the extract response, plus `charts: [{key, url}]` with presigned CloudFront URLs, `reportMarkdown`, `codeKey`, `priceSnapshotAt`, `errorMessage` once `status` reaches `done`/`error`). Generated chart PNGs land under the existing `images/` prefix and the report/code/price-snapshot under `files/` (both already in the OAC allowlist — no new CloudFront behavior needed, see ADR-027's "Download URLs" note above and ADR-033).

---

### WebSocket (API Gateway) — not implemented

> **현재 상태**: 실시간 전사는 클라이언트에서 처리하며, 기본 엔진은 AWS Transcribe Streaming (`@aws-sdk/client-transcribe-streaming`, 브라우저→AWS 직결) — Browser Web Speech API (`BrowserSpeechRecognition`)는 Transcribe Streaming이 설정되지 않았거나 실패했을 때만 쓰이는 폴백이다. 모바일(iOS/iPadOS/Android)에서 마이크/탭 스트림으로 녹음 중일 때는 mic 트랙 충돌 위험 때문에 이 폴백이 막혀 있고(`SttManager.fallbackToWebSpeech`, ADR-030), 사용자가 데스크톱에서 명시적으로 Browser를 선택한 경우는 (모바일이 아니므로) 계속 지원된다. 번역/요약은 REST API 호출. WebSocket 기반 Nova Sonic 스트리밍은 v2 목표.

WebSocket API for real-time transcription and translation.

```
Endpoint: wss://{apigw-domain}/realtime

Connection: $connect with Authorization header (Cognito JWT)

Client → Server Messages:

1. Start Session
{
  "action": "start",
  "meetingId": "uuid",
  "language": "ko-KR",              // source language
  "targetLangs": ["en-US", "ja-JP"] // optional translation targets
}

2. Audio Chunk
{
  "action": "audio",
  "data": "base64-encoded-audio-chunk"
}

3. Stop Session
{
  "action": "stop"
}

Server → Client Messages:

1. Transcript Result
{
  "type": "transcript",
  "text": "Transcribed text",
  "isFinal": true,                  // false for interim results
  "timestamp": "2026-03-05T10:00:00Z",
  "speaker": "Speaker 1"            // optional speaker diarization
}

2. Translation Result
{
  "type": "translation",
  "text": "Translated text",
  "targetLang": "en-US",
  "timestamp": "2026-03-05T10:00:00Z"
}

3. Error
{
  "type": "error",
  "code": "STREAMING_ERROR",
  "message": "Nova Sonic connection failed"
}
```

---

### Q&A (Knowledge Base RAG)

#### Ask Question

```
POST /api/meetings/{meetingId}/ask
Request:
{
  "question": "What deadline was decided in this meeting?",
  "includeKB": true                 // true: also search the global KB, false: this meeting only
}

Response: 200 OK
{
  "answer": "The deadline was set for March 15.",
  "sources": [
    {
      "type": "meeting",            // "meeting" | "kb"
      "meetingId": "uuid",
      "title": "Product Strategy Sync",
      "excerpt": "...confirmed the deadline as March 15...",
      "relevanceScore": 0.95
    },
    {
      "type": "kb",
      "fileId": "uuid",
      "fileName": "project-timeline.pdf",
      "excerpt": "...Phase 2 deadline: March 15...",
      "relevanceScore": 0.82
    }
  ],
  "questionId": "uuid"
}
```

#### Agentic Q&A (Python QA Lambda)

`POST /api/qa/ask`, `POST /api/qa/meeting/{meetingId}`, WebSocket `ask_live` — a Bedrock Converse agentic loop. Available tools: `search_knowledge_base`, `search_aws_docs`, `search_transcript`, `get_aws_recommendation`, `search_web`, `list_meetings`, `get_meeting_detail`, `start_research`, and account tools. The streaming path (`ask_live`) includes the same live transcript tail (2000 chars) in the system prompt as the non-streaming path. Conversation continuity: history per `sessionId` is stored in DynamoDB (7-day TTL), so follow-up questions in the same session carry prior Q&A context.

**`search_web` data-transmission notice**: this tool makes a cross-region SigV4 call to the us-east-1 AgentCore Web Search Gateway, and the model-composed search query (up to 200 chars, which may include keywords derived from meeting conversation) **is sent to an external web search provider**. This happens **on the manual question path too**, not just proactive auto-fire — the opt-in toggle below only gates auto-fired questions. The manual path's mitigation is query-construction constraints in the system prompt/tool description (no customer/attendee names, internal codenames, or meeting figures — generalized keywords only), plus an injection guard that never treats transcript text as an instruction. Query text is never logged in plaintext — both `web_search.py`'s own logs and the agentic loop's tool-call log (`redact_tool_input_for_log`) keep only a hash + length. If `WEB_SEARCH_GATEWAY_URL` is unset, the tool stays exposed but calling it returns a "web search not configured" failure to the model (consumes one tool round, isn't fully disabled). A server-side per-user hourly rate limit applies (`WEB_SEARCH_HOURLY_LIMIT`, default 30/h; checked before the gateway call, so a capped call consumes no external quota and returns a distinct limit-reached message to the model). It is an abuse brake, not a security boundary: it fails open on DynamoDB errors, is a tumbling-hour window (a burst straddling the hour boundary can reach ~2× the limit), and applies to the qa Lambda's authenticated agentic paths only (crawler/research-agent are system-triggered and unmetered by design).

#### Detect Questions (live question detection + proactive-search flag)

```
POST /api/qa/detect-questions
Request:
{
  "transcript": "Recent conversation...",
  "summary": "Current meeting summary (optional)",
  "previousQuestions": ["Already-suggested question"]
}

Response: 200 OK
{
  "questions": ["When does EKS 1.31 support end?", "Which team will own the migration?"],
  "proactive": ["When does EKS 1.31 support end?"]   // a subset of `questions` — ones a search can
                                                      // fact-check immediately. The frontend
                                                      // (LiveQAPanel) auto-fires one per batch to
                                                      // surface an answer ahead of time.
}
```

Proactive auto-fire is **opt-in, default off**: since a question derived from meeting conversation could reach external web search without explicit user action, it only fires for users who've enabled LiveQAPanel's "Proactive Search" toggle (localStorage `ttobak.proactiveSearchEnabled`). When off, `proactive` questions still show up, just as ordinary suggestion chips.

---

### Knowledge Base

#### Upload KB File (Get Presigned URL)

```
POST /api/kb/upload
Request:
{
  "fileName": "project-spec.pdf",
  "fileType": "application/pdf",    // pdf | md | pptx | docx
  "fileSize": 1048576               // bytes
}

Response: 200 OK
{
  "uploadUrl": "https://s3.amazonaws.com/...",
  "fileId": "uuid",
  "key": "kb/{userId}/{fileId}/project-spec.pdf",
  "expiresIn": 3600
}
```

#### Sync KB Index

```
POST /api/kb/sync
Request: (no body — always a full-data-source sync)

Response: 200 OK
{
  "status": "started",
  "jobId": "bedrock-ingestion-job-id",
  "message": "Knowledge Base sync started"
}

// When the API Lambda lacks KB_ID/KB_DATASOURCE_ID (env not deployed):
Response: 200 OK
{
  "status": "skipped",
  "message": "Knowledge Base not configured"
}
```

#### List KB Files

```
GET /api/kb/files?cursor={lastKey}&limit={20}

Response: 200 OK
{
  "files": [
    {
      "fileId": "uuid",
      "fileName": "project-spec.pdf",
      "fileType": "application/pdf",
      "fileSize": 1048576,
      "status": "indexed",          // uploading | indexing | indexed | error
      "createdAt": "2026-03-05T10:00:00Z",
      "updatedAt": "2026-03-05T10:05:00Z"
    }
  ],
  "nextCursor": "base64-encoded-lastEvaluatedKey or null"
}
```

#### Delete KB File

```
DELETE /api/kb/files/{fileId}

Response: 204 No Content
```

---

### Export

#### Export Meeting

```
POST /api/meetings/{meetingId}/export
Request:
{
  "format": "pdf"                   // "pdf" | "markdown" | "notion" | "obsidian"
}

Response (PDF/Markdown/Obsidian): 200 OK
{
  "url": "https://s3.presigned-url...",
  "fileName": "meeting-2026-03-05.pdf",
  "expiresIn": 3600
}

Response (Notion): 200 OK
{
  "notionPageId": "abc123",
  "notionUrl": "https://notion.so/abc123"
}

Error: 400 Bad Request (if Notion API key not configured)
{
  "error": {
    "code": "INTEGRATION_NOT_CONFIGURED",
    "message": "Notion API key not configured. Please add it in Settings."
  }
}
```

#### Get Obsidian Export (Direct Download)

```
GET /api/meetings/{meetingId}/export/obsidian

Response: 200 OK
{
  "filename": "Product-Strategy-Sync-2026-03-05.md",
  "content": "---\ntitle: Product Strategy Sync\ndate: 2026-03-05\nparticipants:\n  - Alice\n  - Bob\ntags:\n  - internal\n  - strategy\nstatus: done\nrelated:\n  - \"[[Weekly Team Standup 2026-03-04]]\"\n  - \"[[Q1 Planning 2026-02-28]]\"\n---\n\n# Product Strategy Sync\n\n## Summary\n...\n\n## Action Items\n- [ ] Task 1\n- [ ] Task 2\n\n## Backlinks\n- [[Weekly Team Standup 2026-03-04]]\n- [[Q1 Planning 2026-02-28]]\n"
}
```

**Obsidian Export Format:**
- YAML frontmatter: title, date, participants, tags, status, related
- `[[wikilinks]]` to other meetings by title for cross-referencing
- Backlinks section at the end for building knowledge graph in Obsidian vaults

---

### Integration Settings

#### Get Integration Settings

```
GET /api/settings/integrations

Response: 200 OK
{
  "notion": {
    "configured": true,
    "maskedKey": "ntn_****abcd",    // last 4 chars visible
    "parentPageId": "1a2b3c4d-5e6f-7a8b-9c0d-1e2f3a4b5c6d"
  }
}
```

Note: an empty/absent `parentPageId` on a configured integration means the connection predates the parent-page requirement and must be re-saved with a `parentPage` before exports will work.

#### Configure Notion Integration

Notion internal integrations can no longer create pages at the workspace root — a parent page or database that has been shared with the integration is required.

```
PUT /api/settings/integrations/notion
Request:
{
  "apiKey": "ntn_xxxxxxxxxxxx",
  "parentPage": "https://www.notion.so/My-Page-1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d"
}

Response: 200 OK
{
  "configured": true,
  "maskedKey": "ntn_****xxxx",
  "parentPageId": "1a2b3c4d-5e6f-7a8b-9c0d-1e2f3a4b5c6d"
}
```

**Errors:**
- `400 BAD_REQUEST` — `apiKey` missing or invalid format
- `400 BAD_REQUEST` — `"parentPage is required"` (missing)
- `400 BAD_REQUEST` — `"Invalid Notion page URL or ID"` (unparseable `parentPage`)
- `400 BAD_REQUEST` — `"Notion API key is invalid or has been revoked."` (`apiKey` itself rejected by Notion — 401)
- `400 BAD_REQUEST` — `"Notion page not found or not shared with the integration. Share the page with your integration (··· → Connections) and try again."` (page not found, or not shared with the integration — Notion returns 404 for both cases)

#### Remove Notion Integration

```
DELETE /api/settings/integrations/notion

Response: 204 No Content
```

#### Invite User (admin-only)

Creates a Cognito user with a system-generated temporary password. Cognito emails the invite directly (default template: username + temp password — no login link, since no `userInvitation` template is configured in `infra/lib/auth-stack.ts`) — no SES/templating on our side. The admin who invited the user is responsible for sharing the sign-in URL separately. The invitee's first sign-in returns a `NEW_PASSWORD_REQUIRED` challenge, handled client-side by `completeNewPassword()` in `frontend/src/lib/auth.ts`.

Requires the caller's JWT `cognito:groups` claim to contain `admins` (enforced by `middleware.RequireAdmin`, backed by JWKS-verified signature checking in `middleware.ParseVerifiedJWT`).

```
POST /api/settings/invite-user
Request:
{
  "email": "new.hire@amazon.com",
  "name": "New Hire",       // optional
  "admin": false             // optional, adds to "admins" group if true
}

Response: 201 Created
{
  "email": "new.hire@amazon.com",
  "invited": true,
  "addedToAdmins": false
}
```

**Errors:**
- `400 BAD_REQUEST` — email missing or invalid format
- `403 FORBIDDEN` — caller is not in the `admins` group
- `409 BAD_REQUEST` — a user with this email already exists

**Partial success:** if `admin: true` but adding the user to the `admins` group fails after the account was already created and invited, the response is still `201 Created` with `addedToAdmins: false` rather than an error — the invite itself succeeded.

#### Admin User Management (admin-only)

Backs the Settings page's "사용자 관리" panel. All six routes require the caller's JWT `cognito:groups` claim to contain `admins` (same `middleware.RequireAdmin` gate as Invite User above). Implemented in `service.UserAdminService` (`backend/internal/service/user_admin.go`) behind a Cognito-SDK-shaped interface, so these operations are unit-tested without a live AWS account.

```
GET /api/settings/users

Response: 200 OK
{
  "users": [
    {
      "userId": "04f86dfc-30b1-7059-896c-55801dacccda",  // Cognito Username == sub for this pool
      "email": "admin@example.com",
      "name": "Admin",
      "status": "CONFIRMED",           // Cognito UserStatus
      "enabled": true,
      "isAdmin": true,
      "createdAt": "2026-04-21T09:01:32Z",
      "lastLoginAt": "2026-08-15T02:11:04Z",  // null if never recorded (see dormancy note below)
      "dormant": false
    }
  ],
  "lastLoginUnavailable": false,  // true if the DynamoDB last-login join failed; users are still returned
  "truncated": false              // true if the pool exceeds the internal pagination safety cap (~1200 users)
}
```

Last-login tracking is written by a Cognito `PostAuthentication` Lambda trigger (`infra/lambda/post-authentication`) to a dedicated `USER#{userId}/LOGIN` DynamoDB item (never onto `USER#{userId}/PROFILE`, to avoid ever creating a stub profile missing its email-search GSI keys). Dormancy has three states, not two — `lastLoginAt` absent is **not** the same as dormant:
- `lastLoginAt` present and older than 90 days → `dormant: true`
- `lastLoginAt` absent and `status: "FORCE_CHANGE_PASSWORD"` → still awaiting first login, not dormant
- `lastLoginAt` absent and `status: "CONFIRMED"` → no record yet (e.g. pre-dates this feature, or last login was via a refresh token, which does not re-fire the trigger), not dormant

```
DELETE /api/settings/users/{userId}

Response: 200 OK
{ "userId": "...", "warning": "" }
```
Deletes the Cognito account only — DynamoDB data (meetings, documents) is preserved. Before deleting, the profile's `GSI2PK`/`GSI2SK` email-search keys are detached (not the whole item) so a later re-invite of the same email can't resolve back to the deleted user's dead ID. `AdminUserGlobalSignOut` runs **before** the delete (a deleted user can no longer be signed out) to close the window where an already-issued access/ID token would otherwise stay valid until it naturally expires.

```
PUT /api/settings/users/{userId}/enable
PUT /api/settings/users/{userId}/disable

Response: 200 OK
{ "userId": "...", "warning": "" }
```
Toggles the Cognito `Enabled` flag. Disable also calls `AdminUserGlobalSignOut` immediately afterward, for the same already-issued-token reason as delete.

```
POST /api/settings/users/{userId}/resend-invite

Response: 200 OK
{ "userId": "..." }
```
Only valid when the target's Cognito status is `FORCE_CHANGE_PASSWORD` (never completed first login) — re-sends the invite email with a fresh temporary password (`AdminCreateUser` with `MessageAction=RESEND`). `400 BAD_REQUEST` otherwise.

```
POST /api/settings/users/{userId}/reset-password

Response: 200 OK
{ "userId": "..." }
```
Only valid when the target's status is `CONFIRMED` — calls `AdminResetUserPassword`, which emails the user a reset code. The user completes the reset via the login screen's "비밀번호를 잊으셨나요?" flow (`ForgotPasswordForm.tsx`, using the previously-unwired `forgotPassword`/`confirmForgotPassword` in `lib/auth.ts`). Deliberately rejected with `400 BAD_REQUEST` for a `FORCE_CHANGE_PASSWORD` user — that state has no code-entry screen, so calling this on them would lock the account out instead of helping.

**Guards on delete/disable:** both reject with `400 BAD_REQUEST` if the target is the caller's own account, or the sole remaining member of the `admins` group (`"마지막 관리자 계정은 삭제할 수 없습니다..."`-style Korean message, since the frontend only surfaces the error `message`, not a code). A same-instant concurrent removal by two admins can theoretically both pass this check (TOCTOU); the service re-checks after acting and returns a `warning` field (not an error, since the primary action already succeeded) if the group is found empty. Recovery in that case is `aws cognito-idp admin-add-user-to-group` from an operator shell.

**Errors (all six routes):**
- `400 BAD_REQUEST` — self-delete/disable, last-admin delete/disable, or wrong status for resend-invite/reset-password
- `403 FORBIDDEN` — caller is not in the `admins` group
- `404 NOT_FOUND` — target user does not exist
- `500 INTERNAL_ERROR` — Cognito/DynamoDB failure

---

## Insights (Crawler)

Crawled news/tech documents (`CRAWLER#{sourceId}/DOC#{docHash}` items, distinct from the meeting-derived `ACCOUNT#{accountId}/INSIGHT#...` items under Accounts above). `GET /api/insights` lists/filters; `GET /api/insights/{sourceId}/{docHash}` returns full content.

```
DELETE /api/insights/{sourceId}/{docHash}

Response: 204 No Content
```

Manually curates a single crawled **news** document — e.g. a search result the relevance gate (`backend/python/crawler/news_crawler.py`) let through anyway, or one ingested before the gate existed. Deletes the S3 KB markdown object first, then the DynamoDB metadata item (order matters — see below), trying both the `shared/news/` and `shared/aws-docs/` key shapes when no `S3Key` is stored, mirroring the read path in `GetDocumentDetail`. Tech docs (`type === 'tech'`, stored under the synthetic `__tech__` source, which has no `CONFIG`/owner row) are NOT deletable through this route — `GetSource` 404s for them, and the frontend hides the delete button accordingly for now.

**Authorization:** caller must be the source's owner (`CrawlerSource.OwnerID`, the user who first created the source via `AddSource`) or an admin (`cognito:groups` contains `admins`) — NOT merely a subscriber. `AddSource` lets any authenticated user self-subscribe to an existing source with no invite/approval step, so gating on subscription alone would make this destructive route trivially self-grantable by anyone. A source created before this field existed has `OwnerID == ""` and stays denied to every non-admin explicitly (not merely by an accidental string mismatch), indefinitely — there is no automated backfill (see `scripts/insights-backfill-owner.py`, report-only, and ADR-026 for why); an admin can set `ownerId` by hand if the real creator is known out of band. An empty caller ID is also explicitly rejected (401) so it can never accidentally match an unbackfilled `OwnerID == ""`. This route does NOT inherit `GetDocumentContent`'s open-read posture (insights are shared substrate by design for reads; a mutating route is not). A successful delete is logged with `userID`/`sourceID`/`docHash` as an audit trail.

**Delete order:** S3 object(s) are deleted before the DynamoDB row. If S3 delete fails, metadata is untouched and the request is safely retryable; deleting DynamoDB first would risk the opposite outcome — metadata gone, `GetDocument` returns nil on retry, and the S3 object + KB vector become permanently unreachable via any API path.

**Errors:**
- `400 BAD_REQUEST` — missing/invalid `sourceId` or `docHash`
- `401 UNAUTHORIZED` — no authenticated caller
- `403 FORBIDDEN` — caller is not the source's owner and not an admin
- `404 NOT_FOUND` — source or document doesn't exist (includes every tech doc, and any source without a `CONFIG` row)
- `500 INTERNAL_ERROR` — S3 or DynamoDB delete failed; per the ordering above, a failure here always means DynamoDB metadata is still intact and the request can be retried

**KB vector caveat:** deleting the S3 object does not immediately evict it from the Bedrock Knowledge Base's vector index — that only reconciles on an ingestion job. `InsightsHandler.DeleteDocument` triggers one itself, best-effort, right after a successful delete (same `KBService.SyncKB` as `POST /api/kb/sync`) — a failure there is logged but does not turn the delete response into an error, so a deleted doc can still surface in Q&A RAG results until that job completes, or if it failed, until the next daily crawl/manual sync. `scripts/insights-rescore.py` (batch re-score + purge for existing docs ingested before the relevance gate) triggers one ingestion job itself after a purge run.

---

## Error Response Format

```json
{
  "error": {
    "code": "UNAUTHORIZED",
    "message": "Authentication required"
  }
}
```

| HTTP Status | Code | Description |
|-------------|------|-------------|
| 400 | BAD_REQUEST | Invalid request parameters |
| 401 | UNAUTHORIZED | Authentication required |
| 403 | FORBIDDEN | No permission (ownership/sharing) |
| 404 | NOT_FOUND | Resource doesn't exist |
| 500 | INTERNAL_ERROR | Internal server error |

---

## Lambda Functions

### 1. API Lambda (cmd/api)
- **Trigger**: API Gateway HTTP API
- **Role**: handles all REST API requests
- **Routing**: Chi Router
- **Env vars**: TABLE_NAME, BUCKET_NAME, COGNITO_USER_POOL_ID, KB_ID

### 2. Transcribe Lambda (cmd/transcribe)
- **Trigger**: S3 Event (audio/ prefix) via EventBridge
- **Role**: kicks off the STT A/B pipeline (offline recordings)
- **Steps**: (1) extract audio key from the S3 event → (2) call Transcribe StartTranscriptionJob (result A) → (3) call Nova 2 Sonic Bidirectional Streaming API (result B) → (4) store both results in DynamoDB → (5) update meeting status "transcribing" → "summarizing"
- **Env vars**: TABLE_NAME, BUCKET_NAME, OUTPUT_BUCKET

### 3. Summarize Lambda (cmd/summarize)
- **Trigger**: EventBridge — S3 `Object Created` on the `transcripts/` prefix, and the custom `AllPartsTranscribed` event for multi-part audio (not a DynamoDB Stream — see INFRA-SPEC.md and ADR-031)
- **Role**: summarizes the meeting via Bedrock Claude
- **Steps**: (1) load the selected transcript → (2) build attachment context: image analysis results (a diagram attachment is labeled "Attached Diagram" and its mermaid code passed as a trusted source) + document attachment (PPTX/PDF etc., body not extracted) filenames → (3) call Bedrock Claude Opus 5 — the note conditionally includes an `## Architecture Diagram` section (only when a diagram attachment's mermaid exists or the discussion is concretely architectural; otherwise the section is omitted) → (4) generate the structured markdown note (+ trailing `## Attached Images`/`## Attached Documents` sections using `attachment://{id}` links, resolved to presigned URLs by the frontend) → (5) save content to DynamoDB → (6) set status to "done"
- **Env vars**: TABLE_NAME, BEDROCK_MODEL_ID

### 4. Process Image Lambda (cmd/process-image)
- **Trigger**: S3 Event (images/ prefix) via EventBridge
- **Role**: image analysis + diagram regeneration
- **Steps**: (1) download image from S3 → (2) classify via Bedrock Claude Vision (architecture/table/whiteboard/photo) → (3) per-category processing: architecture → Mermaid diagram code, table → markdown table, whiteboard → extracted/structured text, photo → description text → (4) store results in S3 (processed/) + DynamoDB
- **Env vars**: TABLE_NAME, BUCKET_NAME, BEDROCK_MODEL_ID

### 5. WebSocket Lambda (cmd/realtime)
- **Trigger**: API Gateway WebSocket API ($connect, $disconnect, $default)
- **Role**: real-time transcription + translation streaming
- **Steps**: (1) $connect: verify Cognito JWT, store connection info in DynamoDB → (2) start: begin a Nova Sonic v2 streaming session → (3) audio: forward audio chunks to Nova Sonic → (4) receive Nova Sonic results → send transcript to client → (5) on translation request, real-time translate via Bedrock Claude → send translation → (6) stop/$disconnect: end session, save the full transcript
- **Env vars**: TABLE_NAME, CONNECTIONS_TABLE_NAME, NOVA_SONIC_MODEL_ID, BEDROCK_MODEL_ID

### 6. KB Lambda (cmd/kb)
- **Trigger**: S3 Event (kb/ prefix) via EventBridge + API Gateway (sync requests)
- **Role**: Knowledge Base file indexing
- **Steps**: (1) download file from S3 (pdf/md/pptx/docx) → (2) add/update the document in the Bedrock Knowledge Base → (3) update the OpenSearch Serverless index → (4) store indexing status in DynamoDB
- **Env vars**: TABLE_NAME, BUCKET_NAME, KB_ID, AOSS_ENDPOINT

### 7. Lambda@Edge (cmd/edge-auth, us-east-1)
- **Trigger**: CloudFront Viewer Request
- **Role**: Cognito JWT verification
- **Steps**: (1) extract JWT from the Authorization header → (2) verify signature via Cognito JWKS → (3) valid: pass the request through, add userId to a header → (4) invalid: 401 response or login redirect
- **Env vars**: COGNITO_USER_POOL_ID, COGNITO_REGION (deployed to us-east-1)

### 8. Convert-Doc Lambda (cmd/convert-doc, container image, ADR-022)
- **Trigger**: S3 Event (docs/ prefix, .ppt/.pptx only) via EventBridge
- **Role**: converts an uploaded PPTX/PPT to a PDF sidecar via headless LibreOffice (for in-browser preview, reusing the existing PDF `<iframe>` viewer)
- **Steps**: (1) download the PPTX/PPT from S3 → (2) run `soffice --headless --convert-to pdf` (with `AWS_*` env vars stripped before exec, to prevent credential leakage while parsing untrusted input) → (3) upload the PDF to a deterministic sidecar key (no DynamoDB write)
- **IAM**: scoped to `docs/*` read + `docs-pdf/*` write (narrower than other upload categories' bucket-wide `grantReadWrite`)
- **Env vars**: BUCKET_NAME (calls `log.Fatal` immediately at cold start if unset)

---

## DynamoDB Access Patterns

| Access Pattern | Key Condition | Filter |
|-----------|---------------|--------|
| My meeting list | PK=USER#{userId}, SK begins_with MEETING# | entityType=MEETING |
| My meetings by date | GSI1: PK=MEETING#{meetingId}, SK=USER#{userId} | - |
| Meeting detail | PK=USER#{userId}, SK=MEETING#{meetingId} | - |
| Shared-with-me list | PK=USER#{userId}, SK begins_with SHARED# | - |
| Attachment list | PK=MEETING#{meetingId}, SK begins_with ATTACH# | - |
| Share target list | GSI1: PK=MEETING#{meetingId}, SK begins_with SHARED# | - |
| Search users by email | GSI2: PK begins_with EMAIL#{emailPrefix} | - |
| User profile | PK=USER#{userId}, SK=PROFILE | - |

### Share-check logic (on meeting-detail access)
```
1. Look up PK=USER#{userId}, SK=MEETING#{meetingId} → owner, OK
2. On failure, look up PK=USER#{userId}, SK=SHARED#{meetingId} → shared, check permission
3. Both fail → 403 Forbidden
```
