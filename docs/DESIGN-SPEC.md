# UI reference

Describe implemented UI, not the historical HTML mockups. Source components and
`frontend/src/app/globals.css` own exact markup/tokens; this guide keeps behavioral
constraints and navigation without duplicating entire JSX examples.

## Design system

Tailwind 4 uses `@custom-variant dark` with the `.dark` class, not OS preference
alone. CSS tokens share names across themes and are exposed through `@theme inline`.

| Token | Light | Dark |
|---|---|---|
| primary | #3211d4 | #8b85f7 |
| accent | #7c3aed | #a78bfa |
| surface-lowest | #ffffff | #101014 |
| surface | #f8fafc | #131318 |
| surface-container | #f1f5f9 | #1c1c22 |
| text-main | #0f172a | #e7e7ec |
| text-secondary | #64748b | #b3b8c2 |

Background-light/background-dark remain separate tokens. Material Symbols
Outlined supply icons. Legacy glass/glow/neon class names remain compatibility
hooks with flat/no-glow styling; their presence is not a request to restore neon.
Typography and component dimensions come from CSS/layout components, not DESIGN.md.
Body and headline utilities share an installed system font stack in both themes;
their font choice must not depend on download timing or the browser font cache.
Material Symbols is bundled as a WOFF2 subset inside the app CSS, with fixed icon
boxes, so names such as `record_voice_over` never flash while an external font
loads. The subset preserves fill variants and optical sizes 20–24 at weight 400.
After adding icons, run `npm run icons:update` in `frontend` to refresh the subset,
upstream text catalog, manifest and public Apache-2.0 license. Production
builds run `icons:check` offline to check source coverage and asset integrity.
The scanner includes known icon names in source literals, including helpers and
lookup tables; constructed names need explicit literals. The license ships at
`/licenses/material-symbols.txt`.

`useTheme` owns interactive theme toggling and watches the root class. Its fixed
initial dark value matches static prerendering; mount-time synchronization is
intentional hydration handling. `layout.tsx` applies the pre-hydration preference.
Mermaid/Shiki consume JS theme values through the hook; keep Mermaid's explicit
light/dark palettes aligned when changing CSS tokens. Purely styled components
use CSS variants.

## Screens and components

| Area | Current responsibilities / code pointers |
|---|---|
| Authentication | auth/LoginForm, ForgotPasswordForm, AuthProvider: login, NEW_PASSWORD_REQUIRED, reset; no self-signup |
| App layout | layout/Sidebar, MobileNav, DesktopHeader, AppLayout: responsive navigation, theme and active route |
| Meeting list | MeetingList: owned/shared/team streams, search, account selection and opaque pagination |
| Recording | RecordButton, app/record/page, useRecordingSession, usePostRecording, PostRecordingBanner |
| Meeting detail | MeetingDetailClient, meeting/ components: note, actions, transcript, attachments, account/project links |
| Markdown | MarkdownRenderer, MermaidBlock, CodeBlock, DataTable, DiagramLightbox |
| Documents | Personal document hub, editor/preview, share dialogs and public document viewer |
| Accounts | AccountsClient, AccountDetailClient: hierarchy, member management, meetings/research/documents |
| Projects | Project views: explicit members, linked accounts/meetings/research and computed insights |
| QA/research | LiveQAPanel and research views: suggestions, streaming, citations, follow-ups |
| Simulator | SimCard: extract, confirm/edit, compare options, poll result |
| Settings | Integration settings, dictionary/domain administration, admin user management |

Resolve exact paths under `frontend/src/` rather than treating this table as an
exhaustive file inventory. New static pages need the CloudFront knownPages mapping.

## Recording and recovery

Browser modes use microphone/tab MediaStreams; the macOS system-audio mode uses
native recording and PCM events. Finished native WAVs remain on disk and upload
through Rust. `usePostRecording` accepts a tagged native-path/browser-Blob payload.
Never load a finished native recording into the WebView just to upload it.

Capture/processing state and upload failures remain visible through the post-record
banner; a failed upload must remain retryable. Progress timeouts measure 60 seconds
without progress, not total file-transfer duration. Delete native temporary files
only after upload-complete succeeds.

Browser stop releases tracks and closes AudioContext. Mobile caption failure must
not stop recording. Wake-lock/reconnect/watchdog mitigation is implemented, with
manual gesture-backed recovery when automatic resume fails. Call resumeAudio and
manualStallRecovery synchronously inside the gesture before awaiting anything.
Browser network loss suspends live-caption transport without stopping MediaRecorder.
For the Transcribe preference, an `online` event retries a recorded outage, including
after desktop Web Speech fallback; pause defers recovery until resume. The warning
clears on the first recovered transcript. An unexpected response-stream end is a
recoverable caption error. Stopped sessions cannot reconnect after pending setup.
When Transcribe configuration is unavailable, desktop recovery retains Web Speech;
mobile still blocks its competing microphone capture. Leaving the recording route
releases the caption manager and its network listeners.

Crash-leftover native WAVs are not Cognito-scoped. Show the existing caveat and
require per-file confirmation for upload/delete; retain the 48-hour cleanup policy.
This confirmation is a mitigation, not an ownership binding (ADR-024).

## Sign-in, invitations and recovery

AuthProvider completes initial profile bootstrap before mounting page content.
Transient failures get three bounded backoff attempts and explicit retry/logout.
Only a missing bootstrap route may fall back to the older authenticated meeting
initializer; new project invitations stay disabled until server capabilities are
available. Subsequent grant pages and verification refresh do not unmount active
recordings/forms: membership revisions refresh affected lists after success.
Project/account discovery hints remain identity-bound and are authorized on the
server. Project UUID hints persist across reloads in the current browser tab,
remain capped at 100, and do not expire while index discovery catches up. Storage
failure falls back to in-memory hints without blocking initialization. Account
hints retain their existing short lifetime. Automatic continuation is bounded
to three passes per interaction, then
an explicit continue/retry control remains available.
Unverified signed-in users get explicit send-code and verify-code controls. A
successful verification refreshes claims and retries pending membership grants.
If refreshed claims still show an unverified address, the UI reports that email
verification completed but sign-in is required to refresh claims. A different
current user cannot complete the original user's verification flow.
First-login and password-reset forms share the configured minimum-eight,
lowercase-letter and digit policy. The reset form also supports entering an
already-received code without requiring another email request. Never modify
password text while normalizing email input.

Project owners add existing users. An unregistered email gives admins an explicit
"send invitation and add to project" action; non-admin owners are directed to an
administrator. Mail creation and membership results are separate: if the mail
request succeeds but adding fails, preserve that partial success and retry member
addition without silently resending. Show pending members with paginated refresh
and cancellation. Admins can follow the existing settings resend path. Email
request acceptance is not inbox delivery.

User management distinguishes invitation pending, reset required, unverified,
unknown-schema and disabled states. Unknown readiness provides a refresh control. Do not offer an admin reset for RESET_REQUIRED, disabled or
unverified users. Existing account/member and sharing notices explicitly say that
queuing a grant does not send mail. The invitation template includes the login URL
and temporary password; no temporary password or code is shown in app diagnostics.
An unverified user who cannot sign in uses the
[operator recovery procedure](runbooks/unverified-account-recovery.md);
the application never silently verifies the address or changes credentials.

## Accounts, filters and sharing

Render accessible accounts as a collapsible hierarchy. An inaccessible parent
makes a visible child a display root; do not fetch the hidden parent's name.
Creation can select a parent. Only the child owner sees the parent editor; server
validation still enforces parent membership, cycles and concurrent ancestry changes.
Exclude self/known descendants from the picker and show save errors.

Meeting account selection supports multiple IDs. Expand selected visible groups
client-side and retain normalized selection across pagination; reset cursor on
change. Empty pages with nextCursor can still have later matches. Hierarchy and
classification never grant access to private content (ADR-036).

All current account members may add members/change assignable roles. Nobody can
assign owner. Removal/pending-invite revocation stay owner-only (ADR-034). UI
visibility is not the authorization boundary.

Adding an existing invited user queues account membership; it does not send an
email. The member picker says "add member", and the pending notice distinguishes
the queued grant from mail delivery. Admins get a link to
`/settings#user-management` for the explicit resend action; other members are
directed to an administrator. New-user invitation and resend confirmations
acknowledge a mail request, not inbox delivery. The configured invitation includes
the login link and temporary password.

Account document sharing creates a copy. Email sharing is a read-only reference,
with no permission toggle. A public link is separately revocable. previewUrl is
for converted PDF; downloadUrl remains the original file. Do not combine these
flows into one ambiguous share model.

## Reading and editing

Saved notes and generated content are separate. Respect active A/B transcript
selection and source freshness when editing/displaying diarized segments. Explain
processing/error states instead of hiding them behind a permanent spinner.
Speaker-name edits retain entered values and show inline save errors, including
409 concurrent changes. Saved notes are reference input, not proof of spoken
statements or agreed tasks; note-only evidence must not receive transcript links.

The action-item card distinguishes unknown legacy state, queued/running analysis,
failure and success. Only successful empty analysis means no tasks. Pending/failed
analysis retains earlier items. Editors can retry for a done meeting with a saved
summary and persist completion checkboxes; readers have no write controls.
Failures remain visible. One timer covers the one-minute summary-to-analysis
handoff and subsequent pending work even when the meeting is already done.
Manual refresh recovers failed status reads; changing meetings cancels stale
requests, and older polls cannot overwrite a completion save.

Markdown surfaces sanitize rendered content and use consistent code/table/diagram
components. Diagram wheel scrolling normally scrolls the page; Ctrl/Command-wheel
zooms. Preserve touch scrolling at default scale, bounded zoom/pan and Escape/backdrop
close behavior in the lightbox. Inspect ZoomPanViewport for exact gestures/limits.

QA suggestions can trigger external search only under the existing manual or
opt-in proactive flows; the proactive toggle does not disable search for manual
questions. Show citation/source coverage and errors. Simulator execution begins
only after user confirmation, with server-side validation and a polled status.

## Search status and source details

DocDetailClient shows IndexStatus for personal/account documents. Unsaved changes
override any indexed badge. Pending status polls at five-second intervals for at
most 12 checks, then offers explicit refresh; unavailable/failed status never means
indexed. Deploy the index-status API before this UI. A status route or badge does
not prove deployed canonical indexing. This activation configuration selects
`all` after consumer qualification; deployed backfill and lifecycle outcomes are
verified separately.

REST, WebSocket, chat and QA components preserve optional `sourceDetails`, with
legacy `sources` fallback. Display human titles and caveats for partial evidence,
pending files and retained previous results. Derive existing-app links only from
validated canonical identities;
internal storage/partition keys are not display titles or navigable source links.
Document page/slide/paragraph positions are not meeting audio timestamps.

Meeting controls require deployed attachment-text, saved re-summary and index-status
APIs. Keep their frontend release dependent on that backend deployment. See the
[API contract](API-SPEC.md) for routes and the
[QA source contract](../backend/python/qa/SOURCE_CONTRACT.md) for wiring and acceptance status.

## Meeting attachment text and saved re-summary

Document cards expose extraction status, saved errors, authorized retry and a
bounded text viewer with parser-provided page/slide/paragraph locations.
Retained results, partial extraction, excerpts and documents missing from the
saved summary remain explicit.

The saved re-summary control requests asynchronous analysis without re-running
STT. Loading a completed summary verifies paginated text against its run/hash
and preserves unsaved edits. Polling is bounded and stale route responses are
ignored. Meeting search status refreshes after successful saves and never marks
unsaved text as indexed.
Speaker-save and re-diarization refreshes check the latest editor dirty state before
applying responses, preserving in-progress summary/transcript text and its selected
A/B source while accepting server updates for clean editors.
Summary saves run serially and coalesce queued edits to the latest draft.
Failures retain that draft and expose retry. Editing preserves canonical
attachment/transcript citation IDs; signed URLs and page anchors are display
targets only. Ordinary links and literal citation text keep their identity.

Deploy attachment, saved-summary and index-status APIs before this UI.

## Verification

Run frontend lint/build for code changes and targeted browser checks for affected
interactions. Check light/dark, mobile/desktop, keyboard access, loading/empty/error,
permission differences and recovery paths where applicable. No frontend unit-test
framework exists. Documentation changes alone do not require a frontend rebuild.
The generated `public/mcp/ttobak-mcp.mjs` download is excluded from frontend lint;
validate its authored Node source and bundle with `npm test` in `mcp-server`.

## SA preparation, references and follow-up

The recording setup accepts a customer and a preparation memo. Users may save a
private prep document and reload a prep/note/reference document before capture.
Starting capture carries initial notes and private account classification into
meeting creation. Reference selection never shares a meeting or sends a question
automatically. Uploaded audio follows the same preparation path.

The reference panel exposes personal KB, personal/shared documents, authorized
prior meetings, customer insights/research/documents, and previously crawled news.
It loads one catalogue at a time, shows bounded excerpts and partial/error states,
and discards stale responses after an account change. Selecting a source prepares
a question; it does not limit the existing QA tool scope. Explicit note insertion
keeps source links and evidence caveats. Personal KB links carry owner identity;
another viewer's same filename must not be presented as the original source.

Meeting detail has a notes editor, stored field insights and follow-up controls.
Notes compare both text and a server-issued revision on explicit save. A conflict preserves the draft;
refreshing the comparison baseline is a separate user action. Background refresh
must not silently advance a dirty editor's baseline. Dirty notes block re-summary.
Delayed detail responses cannot roll back acknowledged notes or association
changes. Editing and private relinking require server capability flags during
mixed-version rollout. Recording notes share a comparison writer across autosave,
finalization and upload retry. After an uncertain write, even reverting to the
previous text requires a version-changing save to fence late requests. Final
submission locks before the autosave flush. Failed or oversized notes remain
editable with the captured audio retained.
Legacy insight extraction has unknown freshness and is labeled accordingly.

Completed QA answers can be added with structured citations; pending/error output
cannot be saved as evidence. Added text is a draft until its destination saves.
Follow-up documents are private snapshots built from bounded saved note/summary
reads and current action items. Creating a document does not publish it to a team.
Project linking and account sharing remain explicit operations with server-side
permissions. Owner-only association controls are hidden from shared viewers.
