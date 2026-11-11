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
lookup tables; constructed names need explicit literals. Non-icon wire values
that equal an icon name (currently `event`) are listed in the scanner's
`NON_ICON_LITERALS` allowlist. The license ships at
`/licenses/material-symbols.txt`.

`useTheme` owns interactive theme toggling and watches the root class. Its fixed
initial dark value matches static prerendering; mount-time synchronization is
intentional hydration handling. `layout.tsx` applies the pre-hydration preference.
Mermaid/Shiki consume JS theme values through the hook; keep Mermaid's explicit
light/dark palettes aligned when changing CSS tokens. Purely styled components
use CSS variants.

## Workspace sizing

`AppLayout` uses the dynamic viewport height and a shrinking main column beside
the 256px desktop sidebar. Its mobile main area reserves the same height and
safe-area inset as `MobileNav`; chat inputs and independently scrolling detail
panes must stay above that navigation.

`globals.css` owns `--workspace-gutter` (16px below 1024px, 24px above it) and
`.workspace-page` / `.workspace-inline-gutter`. Workspace list, dashboard and
detail wrappers fill the available width instead of using a global reading cap.
Use `.workspace-card-grid` for adaptive project cards and
`.workspace-settings-grid` for independent settings sections. Administration
tables span all settings columns. Article prose and the document editor keep a
76ch reading width inside the full-width page; focused login/profile forms and
upload/empty-state controls keep their purpose-specific constraints.

The sidebar keeps its actions separate from its scrollable navigation. Long
breadcrumbs truncate inside the header without displacing search and actions.
See the [2026-11-11 design review](reviews/2026-11-11-workspace-design-review.md)
for viewport evidence and remaining design recommendations.

## Screens and components

| Area | Current responsibilities / code pointers |
|---|---|
| Authentication | auth/LoginForm, ForgotPasswordForm, AuthProvider: login, NEW_PASSWORD_REQUIRED, reset; no self-signup |
| App layout | layout/Sidebar, MobileNav, DesktopHeader, AppLayout: responsive navigation, theme and active route. Menu groups come from `lib/navigation.ts`: Assistant on top; Work (Meetings, Accounts, Projects, Docs, SA Activity `/performance`); Knowledge (Library `/kb`, Deep Research `/insights?tab=research`, News & Updates `/insights`, Custom Dictionary `/dictionary`); Customer Analytics (Billing); Settings. Mobile keeps five bottom tabs; the Knowledge tab opens the grouped menu sheet. `/files` redirects to `/kb?source=meeting` (Library's meeting-attachment source). layout/BillingInsightButton is the Customer Analytics › Billing item (sidebar, menu sheet and profile menu, only with runtime `billingInsight: true`): it opens a blank tab synchronously, then loads the validated hand-off URL, closing it and showing an inline alert on failure. With runtime `billingService: true` the same entry is a plain link to `/billing`, which hosts the Billing dashboard in a same-origin iframe (`/billing-app/index.html`, `upload.html` for `?view=upload`) and gives it tokens through `window.ttobakBilling` (`lib/billingBridge.ts`) |
| Meeting list | MeetingList: owned/shared/team streams, search, account selection, category filter and opaque pagination. The grouped view shows account and unassigned groups in first-seen order, then one Internal section with Team / 1:1 / Training / Other subgroups (`lib/meetingCategory.ts`) |
| Recording | RecordButton, app/record/page, useRecordingSession, usePostRecording, PostRecordingBanner, record/RecordingLinkControls: customer and project links stay editable during browser and native recording; they apply to an existing draft immediately, otherwise when the meeting is created at save (`lib/recordingLinks.ts`). Keeping a recording on the device stores its pending project additions and removals as deltas; a restore applies them to the meeting's current `projectIds` (links made elsewhere meanwhile stay), runs them before upload and then clears them from the copy. A project whose link or unlink request is still in flight is saved as possibly changed. After an audio upload completes (recording or file), the page returns home with an uploaded-meeting reconciliation hint; transcription and notes continue on the server. Meeting cards badge `transcribing`/`summarizing` and `error`. The home list refreshes the current filtered first page every 15 s while a listed meeting is processing or the uploaded meeting has not appeared with a post-recording status, only while visible and for at most 30 minutes (`lib/meetingStatus.ts`). An initially missing uploaded meeting is admitted when it appears; a stale recording status is shown as transcribing while reconciling. Processing meetings on later loaded pages still need manual reload. |
| Meeting detail | MeetingDetailClient, meeting/ components: note, actions, transcript, attachments, account/project links. AccountSection gives the creator a meeting-category control; an internal meeting hides account linking, and Internal is disabled while an account is linked |
| Markdown | MarkdownRenderer, MermaidBlock, CodeBlock, DataTable, DiagramLightbox |
| Documents | Personal document hub, editor/preview, share dialogs and public document viewer |
| Accounts | AccountsClient, AccountDetailClient: hierarchy, member management, meetings/research/documents. The members table shows an owner-only "Transfer owner" button on each non-owner row with a `userId` (never on pending rows or for AM/inherited viewers); it confirms with the target email and the note that the previous owner becomes SA, calls `POST /api/accounts/{id}/owner-transfer`, reloads, and maps 409 to a refresh-and-retry message. The Account Owner also sees a "merge into another Account" action (`AccountMergeDialog`, ADR-022): it picks a target from directly editable Accounts (never the source), calls `GET merge-preview`, and shows move counts (`-1` means capped), the categories that do not move (billing is marked as not checkable), the CLI-only categories (labelled as rows an operator moves with the CLI after the merge) and who gains access. Start stays disabled until the unmoved-items checkbox is checked and never fires unconfirmed. Progress polls `GET merge` every 5 s (doubling after failed polls, 30 s cap) and stops on `done`, `failed` or `aborted`; failure codes map to plain Korean copy, a failed or stalled job offers retry (`POST merge` again to resume), and an operator-aborted job shows that moved items stay on the target, with no retry. A merging Account (`mergingInto`) shows a read-only banner, hides write controls and keeps a progress link for the Owner; a tombstone (`mergedInto`) stays read-only with a banner linking to the target, so its unmoved categories remain reachable; writes answered with 409 `ACCOUNT_MERGING` show a merge-specific message. |
| Projects | Project views: explicit members, linked accounts/meetings/research and computed insights |
| QA/research | LiveQAPanel and research views: suggestions, streaming, citations, follow-ups |
| Simulator | SimCard: extract, confirm/edit, compare options, poll result |
| Settings | Mac app install (`MacAppInstall`, section `#mac-app`), integration settings, domain administration, admin user management, personal AI connector approvals; the custom dictionary is its own `/dictionary` page |

Resolve exact paths under `frontend/src/` rather than treating this table as an
exhaustive file inventory. New static pages need the CloudFront knownPages mapping.

Recording `SAPreparation` requests `scope=direct` customer pages, independently
of personal preparation documents. It excludes inherited read-only customers
because selecting a recording customer creates a private account link.
`RecordingLinkControls` in the recording header uses the same direct, editable
customer catalog plus accessible projects. A customer already on the server
meeting can be replaced but not removed (no unlink API). A failed immediate link
is shown inline and retried at save. Project skip drops pending additions but
preserves requested removals; unconfirmed removals still stop upload, with local
retention available. Account skip permits upload without waiting on an outstanding
private-link request, but does not revoke that request. Restoring a browser
recording resets prior association choices and queues; only the restored meeting's
authorized account is shown, never another recording's projects.
`PreparationReferences` mounts reference catalogs on the first expansion of
its details section, then retains panel state on collapse so question drafts
and answers survive reopening. The picker continues browsing inherited customers.
During an independent frontend/API rollout, an initial unsupported-option 400
uses the existing listing and its edit filter. Authorization failures and later
continuation failures never trigger that compatibility fallback.

`TranscriptSection` shows source-bound refinement and speaker status beside the
current transcript. Missing legacy attestation and human edits are explicit;
structural validation does not promise semantic accuracy or real-world identity.
Untimed speech remains visible with an unavailable-time label beside valid timed
speech. Grouping follows both speaker and timing availability; unknown times
never become zero-time links. Blank/untimed counts describe available metadata.

When meeting detail reports `supportsMeetingTranslation`, the AI notes card and
`TranscriptSection` show a translation select (original, Korean, English;
`MeetingTranslation.tsx`). It is hidden when the flag is off or while editing.
Translated notes render through the same sanitized `MarkdownRenderer` with the
same citation resolution; translated transcript segments render as React text with
the source speaker and time. Translations stay in component memory, are discarded
when the source text changes, and are labelled as unsaved machine translation.
Further pages load with an explicit next-part button; untranslated lines, daily
limit and changed-source errors are shown inline.

Meeting detail and `SpeakerMapEditor` prefer `transcriptionV2` when supplied.
Mixed/untimed arrays use that additive field while old clients receive no legacy
segment array and retain complete plain text. Wholly timed responses keep the
legacy array without duplication. Speaker-map/rediarization refreshes explicitly
clear omitted versioned data; a pending local transcript edit preserves its
paired source and segments. Saving changed A text clears old quality/segments
until the detail refresh supplies current data. Canonical A/B bytes and existing
text-only updates remain intact. Strict worker output uses these readers; legacy processing output remains
unknown. Raw chunk fallback and unknown speaker provenance remain explicit.

## Recording and recovery

Browser modes use microphone/tab MediaStreams; the macOS system-audio mode uses
native recording and PCM events. Finished native WAVs remain on disk and upload
through Rust. `usePostRecording` accepts a tagged native-path/browser-Blob payload.
Never load a finished native recording into the WebView just to upload it.

Settings' Mac app section (`MacAppInstall.tsx`, `lib/macRelease.ts`) shows the
copyable `curl -fsSL https://ttobak.atomai.click/mac/install.sh | sh` command and,
when `/mac/latest.json` is readable, the published version; the version line is
hidden on any failure. The Mac app's own update UI is native, not the SPA: the
TTOBAK app menu has a check-for-updates item (a manual check reports up-to-date or
failure by notification, and opens the popup when an update exists) that becomes an
update-available item with the version once an update is known. The small local popup
(`update-prompt.html`, ADR-019) has Update and Later buttons, shows a static
downloading status (not progress) after Update, stays open until install succeeds, and
asks the user to retry after the recording when work is in progress. Copy is Korean; see
`update.rs` and the page for exact strings. No popup appears while recording, finalizing or
uploading.

In the Mac app, `MeetingDetectionBanner` (layout level) offers start/dismiss when
the app detects a meeting, and after that meeting ends during a native recording
counts down 30 seconds before stopping and uploading, with keep recording and stop
now actions. The native waveform is drawn on a -60..-10 dBFS scale. Nothing records without the user's confirmation; the
start reuses the native control path to the record page. Settings shows the
`MeetingDetectionSettings` toggle only in an app build that supports detection
(ADR-017). Settings also shows `IntakeSettings` (Mac local intake, ADR-003) only in
an app build with the intake commands: adapter state and update, node path when
missing, selected servers, schedule (off, hourly, daily, twice daily) with a
warning when the CLI LaunchAgent is also installed, per-source status, setup with
private-calendar consent and skip toggles, run now, login, cancel and a live
progress log. A failed state read shows a one-line error. 
In Mic mode, `MicSelector` remembers the chosen device; when that device no
longer exists (`OverconstrainedError`/`NotFoundError`, for example after an app
rebuild), `lib/micStream.ts` opens the default microphone, the saved choice is
cleared and an amber notice asks the user to reselect.
In System Audio mode, `NativeMicSelector` offers automatic (follow the
meeting app's microphone) or a specific device, remembers the choice per viewer,
and the recording status names the microphone in use. With an app build that
supports it (ADR-017, #621), the record page also reacts to native events: the
picker stays available during a recording only when the start response reports
`can_switch_input` (also for a start without a microphone); a failed switch shows
Korean guidance with the app's reason, and says so when `recording_status`
reports `capture_lost`; `native-mic-muted` shows that the meeting app's mute keeps
the user's voice out of the recording (leaving the meeting reopens the
microphone); and `native-system-wake` retries
an upload that failed across sleep. Builds without these never show them.

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

Live interpretation is opt-in from recording controls visible on desktop and mobile.
Its default language choice is bidirectional Korean/English: Korean stays in the
left column and English in the right, with the recognized original labeled.
Desktop places interpretation above the summary/notes panels; mobile exposes an
interpretation tab. Single-target translation remains available, including Korean.
Each utterance appears immediately, before its translation finishes. Partial
(draft) translation is off by default; a draft-translation checkbox labelled
as higher cost (`RecordingConfig`), shown only
while interpretation is on, opts in. Drafts then refresh on a 500ms throttle and
are explicitly provisional; turning drafts off cancels the pending draft without
an error. Final recognition clears provisional output and triggers a separate
authoritative translation, requested as `quality: high` with up to two earlier
final source texts (2,000 bytes) frozen when the final is queued; the browser
aborts after 10s. Each completed line carries an engine badge from the response (Korean labels in
`ENGINE_BADGES`, `TranslationView.tsx`): AI (interpreter), machine (Amazon Translate
requested) or machine-fallback (AI was requested but Translate answered: fallback
or hourly downgrade). Finals made only of Korean or English hesitation sounds and
their repeats (`FILLER_TOKEN`, e.g. uh, um, er, hmm) are labelled as skipped without
a request; short Korean answers such as yes and numbers are still translated.
`?debugCaptions=1` on the record page shows the share of retained finals with at
most two words (fragment measurement only; nothing is merged). Response order never
changes utterance order. Language/enable changes discard the previous translation
view and cancel pending work. Failures retain recognized text and show an error.
The view retains at most 200 utterances and caps each displayed original at 9,000
UTF-16 code units, marking truncated originals. This display cap is separate from
the 9,000-byte translation request limit; the full transcript remains separate.
Auto-scroll follows new captions only while the reader remains near the bottom.
A polite live region announces completed translations, not provisional updates.
Single-target speech already in the target language uses one original column.
In the bilingual ko-en mode, a Japanese or Chinese source renders as the original plus a
Korean translation (`TranslationView`). `translationSourceLanguage` labels `zh` and `ja`.

Mac app caption windows (ADR-018) are separate from this card. In the Mac app only,
the record page shows `CaptionOverlayPanel` (state from `useCaptionOverlay`, wired in
`record/page.tsx`): an AWS profile select (from the local profile names), a region
select (`ap-northeast-2`, the default, and `us-east-1`; a saved region outside that list
stays as an extra option; the model is a global inference profile, so the region only
picks the request endpoint and the Amazon Translate region), and a third-language select (none, Japanese, Chinese; disabled while recording,
with a note that recognition language changes only before recording). Each allowed
language (Korean, English, the extra language) has an open/close toggle, disabled until
a profile is chosen. Each open window gets a style editor: font, size (16-160), font and
outline colors, outline width (0-12) and background opacity. Controls live in this
panel, never on the projected window. Hovering a caption window shows a dashed
outline, a grip-glyph move handle at the top centre and a diagonal resize grip at the
bottom-right corner (minimum 320x80); they fade out within about 200 ms when the
pointer leaves, and the whole window stays draggable. A note says Zoom shows the overlay only when the
whole screen is shared, not a single window. Choosing or changing the profile or region raises a
native Mac confirmation; if the user cancels, the panel shows a Korean "change cancelled" message
and re-reads the saved settings so the selects show them again. Sample captions appear only while the settings section is expanded, or for about 4 seconds after an explicit open with it collapsed; windows reopened at page load, recording start and recording stop leave the windows hidden, and expanding or collapsing never touches them while recording.
Failures show a Korean message
(`captionErrorMessage`) with a retry button that also resets once-only error reporting.
A failed translation removes that line from the window (an empty-text push), so no stale
draft stays projected; a translation longer than the 4,000-byte push cap shows its end. The panel renders nothing outside the Mac app or when the app
build lacks the caption commands. The panel is collapsed to one row by default (title, per-language toggles, a settings disclosure button); the profile, region, third-language, style and Zoom controls expand on demand, while the error line and the profile-missing note always show. The choice is stored in `localStorage` (`ttobak-caption-panel-expanded`). On desktop, the record page's right Q&A/reference aside can be hidden with a chevron button and reopened from a button on the right edge (`ttobak-record-side-panel`, default open); it is hidden with CSS so Q&A history survives, and asking from a reference reopens it.

While recording on desktop, a layout menu at the right of the meeting title shows or hides each card with checkboxes: translation (only when enabled), live summary, notes, live transcript and the Q&A/reference aside. The column fills the viewport but keeps a floor per visible row (`recordColumnHeight` in `frontend/src/lib/recordLayoutPrefs.ts`), so on a short window the column grows and the page scrolls instead of squeezing a card. The aside checkbox drives the same preference as its chevron buttons. Card choices are stored as one JSON value (`ttobak-record-cards`, default all shown; corrupt values fall back to defaults) and the pure rules live in `lib/recordLayoutPrefs.ts`. Hidden cards stay mounted and are hidden with CSS, so typed notes and live subscriptions survive. The visible rows share a bounded column (viewport height minus 8rem, at least 28rem): translation, summary/notes and an expanded transcript each take an equal share, a lone summary or notes card spans the full width, and a collapsed transcript is a header strip that gives its height back. The transcript starts collapsed as before, expands when re-shown from the menu, and is always expanded when it is the only row. The last visible main card cannot be unchecked. Mobile tabs are unchanged.

`useCaptionOverlay` runs one `CaptionOverlayController` (one `LiveTranslation` engine per
open window, interims on) over the local AWS profile, never `/api/translate`. The record
page passes every live segment to it (`onSegment`) and `useRecordingSession` reads the
extra recognition language through a getter (`getExtraLanguage`) when a recording starts.
Opening a window while idle shows preview text; when a recording stops the windows are
cleared and the engines are rebuilt so the next recording's segment IDs cannot collide.
Closing a window stops its engine first. Each window shows only payloads tagged with its
own language, and once a line is final later drafts for it are ignored.
Desktop Web
Speech fallback remains Korean-configured and the interpretation UI explains
that mixed-language recognition requires the Transcribe Streaming connection.
These controls do not establish a measured speech-accuracy or end-to-end latency
guarantee. Their regression scripts run with every `frontend/scripts/*.test.mjs`
suite through `npm run test:unit` in `frontend/`; lint and build do not run them.

Browser microphone/tab capture also stores each delivered audio chunk in IndexedDB,
partitioned by Cognito user and a random recording ID. A Web Lock keeps a live or
pending recording from being recovered in another tab. The displayed saved time
advances only after the audio transaction commits; storage failure does not stop
capture or prevent an in-memory upload. Periodic server checkpoints use the latest
draft ID and serialize cumulative writes, coalescing pending snapshots.

After stop, "keep on device / upload later" commits final notes and returns home.
The recording page lists the signed-in user's local recordings, with restore,
download and explicit deletion. Restore reuses the draft meeting when available;
an acknowledged upload is cleaned up without resetting its processing status.
Local copies remain until successful upload acknowledgement or explicit deletion.
Closing a lid does not guarantee a final callback or network upload: recovery is
limited to committed chunks, and browser data deletion/eviction can remove them.
An expired empty recording still offers server-checkpoint recovery; a missing
checkpoint directs the user to local recordings instead of suggesting deletion.

Summary timestamp links scroll to the transcript turn and, when that turn has a
start time inside the playing source, seek the meeting audio there and play.
Browser-recorded WebM can lack Duration/Cues; the player then uses the meeting's
server duration for single-file audio. Multi-part meetings play the first part,
so merged-timeline times beyond its known length only scroll.

When the API reports crop support, owners of single-file recordings can choose a start/end time, preview the range,
and create a private cropped meeting. Cropping retains the original meeting and
copies saved notes/participants, but not attachments, relationships, shares, human
summary edits or derived analyses. The new recording is transcribed and summarized
from the selected bytes. Its audio cannot be replaced in place; crop another copy
to change the range. Limits are 2 GiB source audio, a six-hour selected range, and
an end time within the first 24 hours. Failures link back to the original.

Crash-leftover native WAVs are not Cognito-scoped. Show the existing caveat and
require per-file confirmation for upload/delete; retain the 48-hour cleanup policy.
This confirmation is a mitigation, not an ownership binding (ADR-024).

## Sign-in, invitations and recovery

AuthProvider completes initial profile bootstrap before mounting page content.
Transient failures get three bounded backoff attempts and explicit retry/logout.
Only a missing bootstrap route may fall back to the older authenticated meeting
initializer; new project invitations stay disabled until server capabilities are
available; disabled invitations alone show no top banner, which appears only for
pending grants, retries or a project continuation. Subsequent grant pages and verification refresh do not unmount active
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

Render accessible accounts as a collapsible hierarchy. On `/accounts` and
`/accounts/{id}` that hierarchy is the customer selector: a left tree in the app
nav style (`AccountWorkspaceSelector` → `AccountHierarchyList`), sorted by Korean
name order, children nested under their group, the selection's group expanded,
and no separate flat list. Rows omit role badges; inherited read-only rows show
a labelled icon. The implicit default (first row) stays selected across parent
saves, and the `/accounts/{id}` tree reloads after one. Mobile uses a select in
the same order. An inaccessible parent
makes a visible child a display root; do not fetch the hidden parent's name.
Creation can select a parent. Only the child owner sees the parent editor; server
validation still enforces parent membership, cycles and concurrent ancestry changes.
Exclude self/known descendants from the picker and show save errors.

Meeting account selection supports multiple IDs. Expand selected visible groups
client-side and retain normalized selection across pagination; reset cursor on
change. Empty pages with nextCursor can still have later matches. Hierarchy and
classification never grant access to private content (ADR-015, hierarchy).

Activated ancestor access displays a read-only label and its granting Account
(ADR-015, descendant reads); published meetings are the exception and show edit and management
controls (not delete or audio upload) to every member (ADR-016 2026-11-06
amendment). Otherwise suppress inherited-only mutation controls and keep invitation,
publication, upload/link and dictionary selectors restricted to direct
membership. Billing is the ADR-012 exception: the BFF's authorized entries allow
direct and ancestor team members to view and upload. Its COT selector describes
the required Current State/COH workbooks and COT360 HTML, supporting both three-
and five-file ZIPs. Preserve independently granted editing controls. Account discovery
consumes bounded pages with explicit cursor-loop and size errors.

All current account members may add members/change assignable non-owner roles.
Removal/pending-invite revocation stay owner-only (ADR-015, member permissions). UI
visibility is not the authorization boundary.

`accountApi.list` keeps the signed-in user's Account list in memory for one
minute per token subject and scope (`src/lib/accountListCache.ts`). Every page
request asserts that subject, and a load is discarded if the subject changed.
Concurrent callers share one load, and the Account list paints the last loaded
list before an expired entry reloads. Account mutations, session grant bootstrap, sign-out and session
expiry drop it. It is never persisted and does not authorize reads.

Direct members also get an Account **Events** tab (labelled in Korean in the UI): upcoming events by
default and past events (descending, bounded at the time the list opened so its
cursor stays valid), with create, edit and delete. Attendees come from the
account's contacts or free text; times are entered in the viewer's zone and sent
as UTC RFC 3339; limits mirror the API (`src/lib/accountEvents.ts`), and deletes
confirm with the shared dialog. Inherited readers do not see the tab.

For direct members the general tab starts with **AWS signals** and then the
**engagement flow**, above members and contacts (`components/account/`). Signals
show the account's recent news (with a new-opportunity-candidate badge, linked
opportunities and unlink) or an explicit no-source / no-recent-news state with
account news-source management, plus opportunity stage, days in stage
("unknown" when absent), stall reasons and recent need/opportunity/risk
insights. The flow shows activity → response → opportunity → result per
opportunity, stalled results in the error tone, auto links with an "auto" badge
whose hover title is the evidence, unlink on stored event links, and an
unassigned row whose events offer "link to opportunity". A collapsed
**link management** panel below the flow pages every stored link (manual and
auto, auto evidence as a plain-text title) with unlink; it is the one place to
remove tech, event, older news and auto news→meeting links. The link action also
appears on Events tab rows, which show their existing project links with unlink
(from one bounded links scan, not per-row calls), and on News & Updates document
cards (choosing a direct account first). Server truncation flags show a partial-links notice. Evidence,
needs and stall details render as plain text. Every link or news-source mutation
reloads signals and flow. Korean copy lives in `src/lib/accountOverview.ts`. A collapsed
**relationship graph** renders the graph route client-side with Mermaid under
`securityLevel: 'strict'`; labels are stripped of directives and backticks.
Person edges carry the Korean role label (dotted when auto), and a note gives
the direct member count because only connected members are drawn.

A **people** panel follows the flow (ADR-021 people edges, `components/account/AccountPeople.tsx`,
copy in `src/lib/accountPeople.ts`). It lists resolved members and contacts with
meeting counts and a role summary, then unresolved names with their meeting
count. "Link" opens a member/contact picker fed by the speaker-team and
contact loaders and resolves the whole group; "not a person" ignores the text
account-wide. Lists page with "more", reload after each action and show a
"more remain" note when a resolve leaves rows. In a meeting published to an
account, choosing a speaker-name suggestion also binds that label to the member
or contact after the names save, but only while the saved name still equals the
picked name. A failed binding shows a non-blocking notice and keeps the free
text; existing speaker bindings are listed with unbind. The
**KB** tab pages each kind (upload, meeting, news, tech) with the real index state,
the uploader (a departed member shows as a former member), download for direct
members and untag for the uploader or an owner.

Direct members manage a shared customer contact list on the Account's general
tab. CSV and XLSX imports parse locally, map Remember and common Korean/English
headers, show unmapped columns and a selectable preview, and report each row.
Existing and earlier imported rows are checked for matching email or phone;
shared office numbers can cause skips, and concurrent imports can race. Import is
scoped to the currently selected Account and authenticated user. Manual entry,
editing and deletion remain available.

Settings also has a cross-account contact import (`BulkContactImport`,
`src/lib/contactBulkImport.ts`). One Remember CSV/XLSX file is grouped by
normalized company name (legal forms, case and spacing ignored), or by company
mail domain when the company is blank. A group whose name or alias matches one
directly joined Account imports there automatically; otherwise a matching
company domain is used. Matching requires complete account metadata and considers
all nonpersonal domains in a company group; conflicting account matches require
an explicit choice regardless of row order. Similar names (containment, shared head or bigram
similarity) and unmatched groups ask the user to pick an Account, create a new
one (with the mail domain), or drop the group; undecided groups drop. A leading
"KEB " brand prefix is ignored. When a non-exact group has exactly one strong
candidate (one normalized name contains the other with at least two characters,
similarity of at least 0.8, or a unique domain match), that Account is
preselected with a "suggested" badge and can be changed; ambiguous or weak candidates
still drop. Ancestor/descendant Accounts readable through the hierarchy are also
matched, but contacts can only be added to directly joined Accounts, so they are
shown with a direct-members-only note, disabled and never preselected. The same
person is the same email, or the same name with a shared mobile or work phone
when emails do not differ; a shared office line alone never matches. A match
fills only empty fields; differing values are listed as change candidates and
applied one at a time only while the stored value is still the one shown.
A failed change-candidate response is an unconfirmed outcome, not proof of rejection;
the user verifies the stored value before retrying.
Retries within the current import page skip confirmed saved rows. Failed rows are excluded by default because
an unsuccessful response may follow a committed write. The user checks the
Account's contacts in another tab, keeps the import page open, and explicitly
selects only still-unsaved rows to retry. Each failed attempt clears those retry
selections. Tracking is component-local: reload, navigation or file reselection
resets it, so reimporting a name-only saved contact can create a duplicate.
A created account becomes the
group's target, and any account-bound write result locks that target so a partially
processed group cannot split across Accounts. Implicit drop decisions also persist
across retries. Account aliases are not edited, because no alias update route exists.

Initial Account creation can select another Owner by colleague or email.
Show an unchecked "Keep me as a regular member (AM)" checkbox for this choice.
Checked retains AM membership; unchecked hides and clears extra member selections,
shows a creation receipt and stays on the Account list. Separate explicit
meeting/document shares remain possible without Account membership. Reuse one
creation UUID after a failed request, rotate it for edited input and reject stale
results after an authentication change. Initial Owner assignment remains exclusive
to creation; generic member operations never assign Owner (ADR-015, initial Owner).

An initial pending Owner invitation exposes an explicit resend action on detail
and the creator's receipt. Members or the recorded creator within thirty days can
request delivery only to the current canonical Owner; no Account access is
created. The server rechecks actual password status/identity at resend time.

Account creation and the detail page use the authorized add-member API directly
(`lib/accountInvite.ts`). A current member can invite a missing user: the server
requests Cognito email delivery and queues identity-bound membership. Reusing an
existing invited user queues membership without sending another email. The
pending notice distinguishes grant queuing from mail delivery and offers an
explicit "resend invitation" action to members; cancellation remains owner-only.
New-user invitation and resend confirmations
acknowledge a mail request, not inbox delivery. The configured invitation includes
the login link and temporary password. Domain allowlisting, verified requester
email and the hourly invitation counter apply (ADR-015, invitations).

Account document sharing creates a copy. Email sharing is a read-only reference,
with no permission toggle. A public link is separately revocable. previewUrl is
for converted PDF; downloadUrl remains the original file. Do not combine these
flows into one ambiguous share model.

## Reading and editing

Saved notes and generated content are separate. Respect active A/B transcript
selection and source freshness when editing/displaying diarized segments. Explain
processing/error states instead of hiding them behind a permanent spinner.
Speaker-name edits retain entered values and show inline save errors, including
409 concurrent changes. Direct read/edit recipients and current shared-account
members can correct names. The editor explains that read recipients update speaker
attribution and assignees while retaining custom names in ordinary prose and hidden
summaries. Previous mapping values keep the editor available for repeated
corrections on legacy meetings without segments. Names are limited to 100 characters.
Re-diarization remains owner-only and is hidden from shared recipients.
When the meeting is linked to an Account, the speaker editor may offer team names
and customer contacts to current direct members. Suggestions are loaded through
separate Account-authorized requests, scoped to the current Account, user and
membership revision; denied access leaves manual naming available. Selecting a
suggestion writes the same plain-text speaker mapping as manual entry.
Saved notes are reference input, not proof of spoken
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

Crawler settings accept public company aliases shared by source subscribers.
Explain their use in external news searches and persistence after unsubscribing.
Saved aliases have a remove control for the source owner; other subscribers can
cancel their unsaved additions. Saving sends explicit removals separately from
additions, preserving unrelated concurrent contributions.
Enforce the 20-name and 100-code-point limits when adding names and
surface server validation failures. History displays actual publication run
status, attempts, exclusions and exact recorded cost; incomplete usage shows
that the amount includes conservative estimates. Legacy history remains readable
without invented counters or costs. Omit unavailable counters rather than
displaying zero, preserve recorded numeric zero, and explain missing outcomes
for recovered runs.

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

The re-summary panel has no note-language select or regenerate-in-language button;
generated notes are Korean. The AI Summary card adds a Korean/English segmented
toggle once an English note exists. The page reads `GET .../notes/en` on every visit, so a
pending or failed first run shows its badge and retry even with no stored note. The
English tab is a read-only `MarkdownRenderer` using the Korean view's citation
resolver. The selected tab is per-meeting component state and is not persisted.
Editors see a generate action (no note yet), a regenerate action (existing note)
or a retry action (failed run); readers see none. Badges mark generating, failed and
source-changed (`stale`) states, and polling reuses the re-summary interval and
bound, stopping on unmount or meeting change. A failed run keeps the previous
English note visible. Request errors map `MEETING_BUSY`, `SOURCE_NOT_READY`,
`NO_SUMMARY_SOURCE`, `SHARING_REGEN_REQUIRED` and `OUTPUT_TOO_LARGE` to Korean text.

Deploy attachment, saved-summary and index-status APIs before this UI.

Owners can share current completed notes and Insights from Account connection
without replacing the output when the API reports `canShareToAccount`. The share
action approves disclosure of that canonical result to the selected team;
source-only regeneration remains optional. Recording Account discovery completes
independently of preparation documents, refreshes on authenticated membership
changes, and distinguishes loading, empty results and retryable errors.

Protected account readers see an explicit owner-sharing-update status while
retaining raw notes/transcript access. Linked previous meetings in the header and picker are plain navigation; the copy
does not promise carried-over context.

Owners see a plain "regenerate notes" action only when the API reports that
regeneration is required and available. Its copy says the note was made the old
way and cannot be shared individually, and that regenerating also recreates action
items and insights; the UI has no "sharing-safe" wording. The confirmation explains
the current-source inputs (saved notes, selected transcript, extracted documents),
that prior generated results are not used, replacement of existing
analysis/simulator output, failure preservation and task-completion continuity.
Unsaved edits and active work disable the operation. Loading its verified result
also clears obsolete generated fields from the displayed meeting.

## MCP client setup guides

Settings' AI connector panel shows only configured read/write profiles. Users can
block a connection, restore read access, approve personal-note writing for one hour
or revoke writing. Verified administrators also see global enable/disable controls;
the server enforces that role independently. Failed writes trigger a fresh state
read and a visible warning instead of a blind retry. No configured profiles means
the panel is absent; lookup failure remains visible. Writing expiry refreshes
every 30 seconds and remains enforced by the server.

Settings' MCP guide has separate selectors for Claude Code, Codex, Kiro Crew,
Amazon Quick and Kiro CLI. Claude/Codex/Crew/Kiro CLI have local stdio and remote
HTTP instructions; Quick is HTTP-only. Crew uses its Integrations dashboard and
the documented remote-header authentication path, without promising native OAuth
login/refresh. A shared Crew must not share one user's private credentials.

Guide commands use the current HTTPS site origin, runtime Cognito client/pool
configuration and bounded, timed public OIDC discovery. They never read or copy
session tokens. The optional runtime `mcp.url` is accepted only for the same-site
HTTPS `/api/mcp` resource. Its presence is described as a configured address, not
proof of a successful client connection. Missing, loading and failed configuration
are distinct; commands requiring an unknown HTTP URL cannot be copied.
The guide does not invent an active endpoint from the site origin.
HTTP instructions follow runtime `mcp.privateBridgeEnabled`: `false` keeps the
first-party client and `openid email profile`; `true` uses the dedicated read
client and adds the `{mcp.url}/read` scope. A missing mode or invalid, duplicate or
first-party dedicated client ID shows HTTP setup as unavailable; stdio keeps the
first-party client.

Local setup documents the Node runtime, download, client registration, browser
login and verification. JSON examples require an explicit absolute file path;
shell examples quote deployment values. HTTP instructions cover client-specific
OAuth callbacks, Crew's resource-bound access-token header, and Quick's user OAuth,
Sync and 60-second operation limit. Existing approval settings are preserved.
Copy controls report success/failure; client/transport controls work with keyboard,
mobile and light/dark themes. Notes-first reads are distinguished from generated
summary and transcript requests.

## Personal dictionary

Settings' dictionary (`CustomDictionary.tsx`, `DictionaryTable.tsx`; copy in those
files and `lib/dictionary.ts`) uses only per-term routes. Columns: display form,
recognized-as aliases, type, source (manual, automatic, or a source meeting link
once the API returns it). Filters all / automatic / manual / suggested. Actions:
edit (case, spacing or `- . _ ·` only), keep (`aliasMode=always`), delete (toast:
never re-added automatically) and add on suggestions. Writes send `version`;
conflicts and 404 reload the list. An open editor diffs and versions against the
row snapshot taken when editing began; polled responses older than a mutation are
dropped. Duplicates use `normalizeTermKey`, checked against Go's
`backend/internal/service/testdata/term-key/vectors.json` by `npm run test:unit`
(CI runs it only when `frontend/**` changes; the Go half runs with the backend). Legacy terms without a row cannot be edited
while `migrationPending`; terms that refuse migration are marked and can be deleted
through the legacy DELETE-by-phrase, which retries migration. That delete rewrites
the whole blob without a revision guard, so the UI runs one at a time, reload
included (`LEGACY_DELETE_KEY`). KO/EN badges show in use, preparing,
"updating (current vocabulary in use)", failure codes in plain Korean, and off;
the page polls every 10 seconds only during a build.

The recording page calls the dictionary GET once (a builder poll opportunity) but
sends no vocabulary on live streams: no API names one yet (ADR-008 rollout check 5).
`lib/liveVocabulary.ts` would send at most one name per `LanguageOptions`
language; with a name, a vocabulary BadRequest before the first stream event
retries once without it, and any vocabulary rejection drops it for the recording
and shows an informational "recognizing without the custom dictionary" line.

## SA activity

`/performance` (`app/performance/page.tsx`, `components/performance/`, pure
helpers and wire types in `lib/performance.ts`, client `performanceApi` in
`lib/api.ts`) is the owner's own page: every request reads only the caller's
rows ([ADR-023](decisions/ADR-023-sa-performance-data-residency.md)) and there is
no share control. The Work menu entry is SA Activity. A segmented
period control (this week / quarter / this year, default this week) drives
`GET /api/performance/missed`. Sections, in order:

- Scorecard placeholder card: the AI scorecard arrives with plan 2; for now it
  holds `GoalsEditor`, the current Asia/Seoul year's goals with a UTF-8 byte
  counter that disables saving above 8,000 bytes. The editor stays read-only
  until the GET succeeds (unset goals are a 200 with empty text); a failed load
  shows an error and a retry, so a save never overwrites unseen goals.
- What I missed (`MissedList`): two groups, SFDC logging gaps and silent
  customers, each newest `at` first. A meeting touchpoint links to
  `/meeting/{id}`; calendar gaps and silent customers link to `/accounts/{id}`.
  Account names come from the readable account list. If that list fails, the
  items render with a notice, a neutral placeholder name (never the raw ID) and
  a retry, and the mapping queue shows the same error and retry above its list
  or empty message. A period change also retries a failed account list, and a
  failed missed read or mapping-list read shows a retry. A calendar-coverage note states the earliest calendar event read (`calendarCoverageStart`, or that no events were read) and qualifies the logging-gap empty state. A positive `unmappedCount` is shown as an
  excluded count, never guessed; the notice says only SFDC-account activity can
  be fixed in the queue.
- Mapping queue (`MappingQueue`): unmapped SFDC assignments, each with a plain
  account select (hierarchy paths from `lib/accountTree.ts`) and a connect
  button. An assignment whose mapped account is no longer readable (the API's
  `reason:"unreadable"`, or an account absent from the loaded readable list)
  is listed with that reason. Below them, automatic mappings preselect their
  account with a confirm button (saving makes the mapping manual) that becomes
  "change" when another account is selected. Manual mappings are not listed.
  `403` reads as "cannot read this account", `404` as "not your
  assigned SFDC account"; a saved mapping reloads the queue and the missed list.
- Source freshness: the `IntakeRunControl` collect button sits here (Mac app
  only; a browser shows a hint, an older app build shows nothing, a failed state
  read shows a one-line error). A run that ends with success or `PARTIAL_FAILURE`
  reloads the missed list, mappings and accounts. One line per `sourceFreshness` entry, "last collected" in
  Asia/Seoul time; a never-completed source shows `ttobak-mcp intake sfdc` or
  `ttobak-mcp intake calendar`.

Two tabs sit under the header: Dashboard (the sections above, with the period
control) and Report (`ReportTab`, [ADR-023](decisions/ADR-023-sa-performance-data-residency.md);
helpers in `lib/performanceReport.ts`). The report tab lists the owner's stored
reports newest first (`ReportList`, a partial badge per row) and renders the
selected one natively from its JSON (`ReportView`): every string is a React text
node, with no HTML, Markdown or links built from content. The view shows the
period, generation time and model, a partial badge naming the missing sources,
the summary, ledger tiles, sections in stored order (lede, paragraphs, tables,
cards), actions grouped high/medium/low, and the data basis collapsed at the end.
Delete uses an inline confirm (not a browser dialog) and reloads the list.

Generation is Mac app only (`ReportGenerateDialog`, an in-page dialog; a browser
shows a hint). It takes a period (this week, this quarter, this year, custom
start/end of at most 400 days, ending no more than two calendar years ago) and four highlight lists, one item per line,
prefilled from the newest stored report of the same period kind and checked
like the Mac runner before `report_generate`. An advanced section holds the
Bedrock toggle and AWS profile picker (`report_settings_get/set`). While a run
goes, the dialog shows elapsed time, progress lines and Cancel; closing it keeps
the run going. A successful `report://finished` reloads the list and opens the
new report; a failure shows the Korean label of its code (report labels, then the shared runner labels). The report tab stays
mounted (hidden) while the dashboard shows, so a finish event is not lost and is
handled when the user returns. A failed cancel shows its error inline.

## Verification

Run frontend lint/build for code changes and targeted browser checks for affected
interactions. Check light/dark, mobile/desktop, keyboard access, loading/empty/error,
permission differences and recovery paths where applicable. There is no frontend
test framework; `npm run test:unit` runs the `scripts/*.test.mjs` node:test files
(not part of lint/build). Documentation changes alone do not require a frontend rebuild.
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
changes. Notes editing requires server capability flags during mixed-version
rollout. Meeting-detail management (account, sharing, links, projects, file uploads,
crop, rediarize, recovery) follows the server `canManage` flag: the creator or a
live account-team member (ADR-016 2026-11-06 amendment). Delete stays
creator-only on desktop and mobile; member-created shares show the sharer. Account
management uses one explicit link-and-share button calling the existing team
publication endpoint. The label
and help text explain that the selected account team receives the meeting and
its extracted insights. An already published account offers a share refresh;
failures remain visible and retryable. Recording preparation and existing private
links are not published automatically. Recording notes share a comparison writer across autosave,
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

## SpecReq task strip

The workspace upcoming-meetings view and the Account page show a private
"SpecReq tasks" strip above the events (`SpecReqTasks`, `useSpecReqTasks`; copy in
`SPECREQ_TASK_COPY` and `SPECREQ_ACTION_COPY`). It uses the existing tokens from
`globals.css` (`surface`, `border-subtle`, `primary`, `text-*`). Rows list open
tasks for the signed-in user only; closed, resolved and issued tasks are hidden, a
valid `?task=` event ID highlights its row, and an unavailable API hides the strip.

`SpecReqTaskActions` offers one action set per state, each sending the loaded
row `version`; buttons disable while a transition is pending, and a 409 reloads the
row and shows reload copy.

| State / path | Actions |
|---|---|
| `request_needed` | Copy the Slack request (also shown in a read-only field) and "I sent it" (`markRequested`). |
| `needs_account` | Account picker over my accounts (`pickAccount`); waiting copy once an account is set. |
| `needs_owner` | Owner picker over the account's direct `SA`/`SSA` members with a user ID, excluding me and pending members (`pickOwner`). |
| mine `issue_needed`, theirs `requested` | SpecReq ID input accepting a UUID (`linkSpecReq`). |

A linked SpecReq shows its ID in a code element with a copy button; there is no known SpecReq page URL, so no link is rendered. When the account cannot be resolved, the copy button stays disabled and a hint explains why. The Slack
request text is built in the browser from cloud fields only (account name, event
title and time in the event zone, my display name) with `renderSlackRequest`; its
`SLACK_REQUEST_TEMPLATE` mirrors the runner's and `contracts/specreq/slack-request-v1.json`.
The web copy does not escape Slack control characters because the user pastes it.
