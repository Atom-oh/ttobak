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
| Accounts | AccountsClient, AccountDetailClient: hierarchy, member management, meetings/research/documents |
| Projects | Project views: explicit members, linked accounts/meetings/research and computed insights |
| QA/research | LiveQAPanel and research views: suggestions, streaming, citations, follow-ups |
| Simulator | SimCard: extract, confirm/edit, compare options, poll result |
| Settings | Integration settings, domain administration, admin user management, personal AI connector approvals; the custom dictionary is its own `/dictionary` page |

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

In the Mac app, `MeetingDetectionBanner` (layout level) offers start/dismiss when
the app detects a meeting, and after that meeting ends during a native recording
counts down 30 seconds before stopping and uploading, with keep recording and stop
now actions. The native waveform is drawn on a -60..-10 dBFS scale. Nothing records without the user's confirmation; the
start reuses the native control path to the record page. Settings shows the
`MeetingDetectionSettings` toggle only in an app build that supports detection
(ADR-061). In System Audio mode, `NativeMicSelector` offers automatic (follow the
meeting app's microphone) or a specific device, remembers the choice per viewer,
and the recording status names the microphone in use. With an app build that
supports it (ADR-061, #621), the record page also reacts to native events: the
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

Render accessible accounts as a collapsible hierarchy. An inaccessible parent
makes a visible child a display root; do not fetch the hidden parent's name.
Creation can select a parent. Only the child owner sees the parent editor; server
validation still enforces parent membership, cycles and concurrent ancestry changes.
Exclude self/known descendants from the picker and show save errors.

Meeting account selection supports multiple IDs. Expand selected visible groups
client-side and retain normalized selection across pagination; reset cursor on
change. Empty pages with nextCursor can still have later matches. Hierarchy and
classification never grant access to private content (ADR-036).

Activated ancestor access displays a read-only label and its granting Account
(ADR-058); published meetings are the exception and show edit and management
controls (not delete or audio upload) to every member (ADR-016 2026-11-06
amendment). Otherwise suppress inherited-only mutation controls and keep invitation,
publication, upload/link and dictionary selectors restricted to direct
membership. Billing is the ADR-060 exception: the BFF's authorized entries allow
direct and ancestor team members to view and upload. Its COT selector describes
the required Current State/COH workbooks and COT360 HTML, supporting both three-
and five-file ZIPs. Preserve independently granted editing controls. Account discovery
consumes bounded pages with explicit cursor-loop and size errors.

All current account members may add members/change assignable non-owner roles.
Removal/pending-invite revocation stay owner-only (ADR-034). UI
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

A **people** panel follows the flow (ADR-067, `components/account/AccountPeople.tsx`,
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
one (with the mail domain), or drop the group; undecided groups drop. The same
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
to creation; generic member operations never assign Owner (ADR-055).

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
email and the hourly invitation counter apply (ADR-054).

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

The same panel shows editors a note-language select (automatic, Korean,
English). A change saves `noteLanguage` through the meeting update and then
offers a regenerate-in-this-language action, which sends the existing empty
re-summary request. The select is
disabled while a batch summary or re-summary is running, because a mid-run change
discards that run as `SOURCE_CHANGED`. Save failures show an inline alert and
keep the previous value.

Deploy attachment, saved-summary and index-status APIs before this UI.

Owners can share current completed notes and Insights from Account connection
without replacing the output when the API reports `canShareToAccount`. The share
action approves disclosure of that canonical result to the selected team;
source-only regeneration remains optional. Recording Account discovery completes
independently of preparation documents, refreshes on authenticated membership
changes, and distinguishes loading, empty results and retryable errors.

Protected account readers see an explicit owner-sharing-update status while
retaining raw notes/transcript access. Owners can confirm sharing-safe regeneration
when the API reports it available. The confirmation explains the current-source
inputs, omitted predecessor/prior generated evidence, replacement of existing
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
sends no vocabulary on live streams: no API names one yet (ADR-008 item 5).
`lib/liveVocabulary.ts` would send at most one name per `LanguageOptions`
language; with a name, a vocabulary BadRequest before the first stream event
retries once without it, and any vocabulary rejection drops it for the recording
and shows an informational "recognizing without the custom dictionary" line.

## SA activity

`/performance` (`app/performance/page.tsx`, `components/performance/`, pure
helpers and wire types in `lib/performance.ts`, client `performanceApi` in
`lib/api.ts`) is the owner's own page: every request reads only the caller's
rows ([ADR-068](decisions/ADR-068-sa-performance-data-residency.md)) and there is
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
  items render with a notice and a neutral placeholder name, never the raw ID,
  and the mapping queue shows an error with a retry instead of "no accounts".
  A positive `unmappedCount` is shown as an excluded count, never guessed.
- Mapping queue (`MappingQueue`): unmapped SFDC assignments, each with a plain
  account select (hierarchy paths from `lib/accountTree.ts`) and a connect
  button. `403` reads as "cannot read this account", `404` as "not your
  assigned SFDC account"; a saved mapping reloads the queue and the missed list.
- Source freshness: one line per `sourceFreshness` entry, "last collected" in
  Asia/Seoul time; a never-completed source shows `ttobak-mcp intake sfdc` or
  `ttobak-mcp intake calendar`.

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
