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
points to the PDF while downloadUrl always points to the original. Public tokens
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
event. Meeting file attachments are currently not content-extracted for summaries.
AudioUploader automatically attempts KB promotion for documents; the recording
page also offers manual copy. Copy/async ingestion does not guarantee parser
support or summary grounding. Images and PPT/PPTX document previews have distinct
processing paths.

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
| PUT | `/api/accounts/{accountId}/documents/{docId}` | `accountHandler.UpdateDocument` |
| DELETE | `/api/accounts/{accountId}/documents/{docId}` | `accountHandler.DeleteDocument` |
| POST | `/api/documents` | `documentHandler.PutDocument` |
| GET | `/api/documents` | `documentHandler.ListDocuments` |
| GET | `/api/documents/{docId}` | `documentHandler.GetDocument` |
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

QA uses a Converse tool loop and current authorized meeting data. Tools include
KB/AWS docs/web/transcript search, meeting detail/list, account operations and
research initiation; `qa/tools.py` is the exact roster. KB meeting hits are discovery
candidates: current access and current notes/content are rechecked, including on
cache hits. Revoked/deleted sources and citations are removed. New text may not
be discoverable until KB export/ingestion catches up; current reads are not reindexing.

Live/HTTP meeting contexts send separate bounded untrusted excerpts for transcript
and saved notes with coverage metadata. get_meeting_detail exposes continuation
information; follow its next offset, not an excerpt-relative index. Transcript
read failures return an error rather than silently losing context.

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
