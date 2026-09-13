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

## Contracts that require care

### Meetings and pagination

`GET /api/meetings` accepts `tab=all|shared`, `limit`, opaque `cursor`, and optional
comma-separated `accountIds` (at most 100 distinct IDs). Legacy `accountId` remains
supported; supplying both forms, invalid IDs or a mismatched cursor returns 400.
An empty selection is unfiltered. Preserve normalized selection/tab/caller across
continuation requests; restart pagination when filters change.

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
GetMeeting resolves S3 transcript spills and returns the active note/transcript
state, attachments and optional simRun. A/B selection and edited text must remain
the source of truth: old timestamped segments cannot override the selected text.
Saved notes are user input, distinct from generated content. Stuck transcription
or summarization is reconciled after 60 minutes; summarize retry eligibility is a
separate 20-minute conditional claim, not a retry scheduler.

Conditional transcript/speaker edits publish new immutable
`transcripts/{meetingId}/{field}.{32-lowercase-hex}.txt` references only with a
matching database update. Conflicts return 409; ambiguous database failures retain
new objects because the write may have committed. Existing referenced objects
remain intact. Go and QA readers accept strictly validated legacy and versioned
keys; deploy compatible readers before writers (ADR-037).

### Bounded meeting reading

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

Account creation creates owner membership atomically. Optional parentAccountId
requires current membership in the parent. Parent updates require child ownership
and new-parent membership; the required field uses an empty string to detach,
not omitted/null. Self/cyclic/deep ancestry (over 64 nodes) is rejected;
transactional ancestry checks and retries protect concurrent moves. Conflicts
return 409. Hierarchy is organization metadata, never authorization inheritance.

Any current account member can add members or change assignable roles; `owner`
is never assignable. Removal and pending-invite revocation remain owner-only
(ADR-034). Project permissions are separate: owner/direct member/member of a
linked account. The service list unions those access paths; project deletion
rejects remaining relationships. Canonical sets and reverse references change in
one transaction. See account/project handlers and services for each mutation gate.

### Documents and shares

Personal document writes and share mutations isolate the owner's partition; a
non-owner cannot mutate the source. GetUserDocument first checks the caller's
partition, then resolves an authorized read-only share through the owner's row.
An inaccessible document returns 404. An account share creates an independent
S3 copy; an email share references the original and is read-only.
Document shares use SHAREDDOC#/DOCSHARE_TO#, not meeting SHARED# keys. Deleting an
owner document can leave share rows; list reads skip missing targets.

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

The checked-in app selects `INDEXING_MODE=manual-only` **with its one-minute
schedule enabled**; canonical stream delivery and permissions require `all`.
This is configuration, not deployed-state evidence. Manual-only bootstraps
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
All rows below come from `backend/cmd/api/main.go`.

<!-- BEGIN GO ROUTES -->
| Method | Path | Handler |
|---|---|---|
| GET | `/api/health` | `healthHandler.Health` |
| GET | `/api/auth/allowed-domains` | `settingsHandler.GetAllowedDomains` |
| GET | `/api/public/docs/{token}` | `documentHandler.PublicGetDoc` |
| GET | `/api/accounts` | `accountHandler.ListAccounts` |
| POST | `/api/accounts` | `accountHandler.CreateAccount` |
| GET | `/api/accounts/{accountId}` | `accountHandler.GetAccount` |
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
| GET | `/api/accounts/{accountId}/documents/{docId}/index-status` | `indexStatusHandler.AccountDocument` |
| PUT | `/api/accounts/{accountId}/documents/{docId}` | `accountHandler.UpdateDocument` |
| DELETE | `/api/accounts/{accountId}/documents/{docId}` | `accountHandler.DeleteDocument` |
| POST | `/api/documents` | `documentHandler.PutDocument` |
| GET | `/api/documents` | `documentHandler.ListDocuments` |
| GET | `/api/documents/{docId}` | `documentHandler.GetDocument` |
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
| POST | `/api/meetings/{meetingId}/account` | `meetingHandler.LinkToAccount` |
| POST | `/api/meetings/{meetingId}/share-account` | `shareHandler.ShareToAccount` |
| GET | `/api/meetings` | `meetingHandler.ListMeetings` |
| POST | `/api/meetings` | `meetingHandler.CreateMeeting` |
| GET | `/api/meetings/{meetingId}` | `meetingHandler.GetMeeting` |
| GET | `/api/meetings/{meetingId}/reading` | `readingHandler.Get` |
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
| DELETE | `/api/kb/files/{fileId}` | `kbHandler.DeleteFile` |
| POST | `/api/meetings/{meetingId}/export` | `exportHandler.ExportMeeting` |
| GET | `/api/meetings/{meetingId}/export/obsidian` | `exportHandler.ExportObsidian` |
| GET | `/api/settings/integrations` | `settingsHandler.GetIntegrations` |
| PUT | `/api/settings/integrations/notion` | `settingsHandler.SaveNotionKey` |
| DELETE | `/api/settings/integrations/notion` | `settingsHandler.DeleteNotionKey` |
| PUT | `/api/settings/allowed-domains` | `settingsHandler.SaveAllowedDomains` |
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
| GET | `/api/projects/{projectId}` | `projectHandler.GetProject` |
| PUT | `/api/projects/{projectId}` | `projectHandler.UpdateProject` |
| DELETE | `/api/projects/{projectId}` | `projectHandler.DeleteProject` |
| POST | `/api/projects/{projectId}/members` | `projectHandler.AddMember` |
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

## Python QA and WebSocket

| Transport | Route/action | Implementation |
|---|---|---|
| HTTP POST | `/api/qa/ask` | Agentic general Q&A |
| HTTP POST | `/api/qa/meeting/{meetingId}` | Authorized meeting-context Q&A |
| HTTP POST | `/api/qa/detect-questions` | Suggested/proactive question detection |
| WebSocket | `$connect`, `$disconnect`, `$default` | Go websocket Lambda |
| WebSocket message | `ask_live` | Async invocation of Python QA, streamed replies |

WebSocket is implemented. `$connect` uses the dedicated Go Lambda authorizer and
query token; it is not a Cognito HTTP JWT-authorizer attachment. Clients use the
runtime-configured WebSocket endpoint. There is no current start/audio/stop
server-side transcription stream or separate connections table in this handler.
Live transcription runs in the browser using AWS Transcribe Streaming.

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
  exports provide identities only. Cached text/misses cannot replace fresh reads,
  and the strict runtime must write no new result cache.
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
| POST | `/api/meetings/{meetingId}/resummary` | Owner/edit; empty body or `{}`; `202` with status |

Response: `{status, runId?, errorCode?, leaseUntil?, updatedAt?, resultHash?}`.
Both routes reject query strings with 400. POST accepts no caller source fields
and caps its body at 1 KiB.
Status is `unknown`, `queued`, `running`, `succeeded`, or `failed`; lease is epoch
milliseconds and updatedAt is RFC3339. An active run is reused. Expired work is
persisted as `failed/INTERRUPTED`. A successful resultHash is the SHA-256 of the
saved summary text.

This differs from POST `/summarize`, which is live, caller-text summarization.
Re-summary reads saved notes/summary, the current selected transcript and
verified document extraction. It does not rerun STT or refinement and does not
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
