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

Crash-leftover native WAVs are not Cognito-scoped. Show the existing caveat and
require per-file confirmation for upload/delete; retain the 48-hour cleanup policy.
This confirmation is a mitigation, not an ownership binding (ADR-024).

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
not mean canonical indexing has been activated: the app still selects manual-only
bootstrap.

REST, WebSocket, chat and QA components preserve optional `sourceDetails`, with
legacy `sources` fallback. Display human titles and caveats for partial evidence,
pending files and retained previous results. Derive existing-app links only from
validated canonical identities;
internal storage/partition keys are not display titles or navigable source links.
Document page/slide/paragraph positions are not meeting audio timestamps.

Structured source rendering is ready, but the strict QA handler wiring remains
staged. Attachment text and saved-summary routes are provided by the backend parents;
the meeting controls are implemented here and require those APIs deployed first. See the
[API contract](API-SPEC.md) and [QA source contract](../backend/python/qa/SOURCE_CONTRACT.md).

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

Deploy attachment, saved-summary and index-status APIs before this UI.


## Verification

Run frontend lint/build for code changes and targeted browser checks for affected
interactions. Check light/dark, mobile/desktop, keyboard access, loading/empty/error,
permission differences and recovery paths where applicable. No frontend unit-test
framework exists. Documentation changes alone do not require a frontend rebuild.
