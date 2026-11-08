# Meeting Workspace and Local Outlook Sync

Status: historical proposal recorded on 2026-11-07, pending plan review; not a
current implementation claim. The user approved
the HTML visual direction and requested local Outlook-to-TTOBAK MCP synchronization
on 2026-11-07. This document is not evidence of implementation or deployment.

Reviewed source: `origin/main` at
`dd3f2f7479f366695eb70fb2c1beccf58a4847ca`.
Approved visual reference:
`.superpowers/brainstorm/3192558-1794008007/content/meeting-workspace-v1.html`.
The visual reference is a local, ignored artifact, not a production dependency.

## Outcome

- Home prioritizes the next Outlook meeting, a seven-day agenda, account insights,
  and recent meeting records.
- Meetings uses compact, readable rows, account grouping, explicit date/time
  ordering, filters, and progressive loading.
- Accounts provides customer selection, overview, meeting history, and a distinct
  insights tab without removing projects, documents, billing, or team management.
- Local software reads Outlook with the user's permission and writes normalized
  events through TTOBAK's stdio MCP. TTOBAK does not scrape Outlook on its servers.
- The in-app MCP guide explains recurring local execution, first-run checks,
  changes/cancellations, retries, and freshness. Installing MCP alone does not
  start a recurring job.

## Delivery Boundaries

Plan A delivers the workspace redesign using existing authorized data. Upcoming
sections initially show a truthful local-sync setup state; no invented meetings,
counts, or demo dates enter production.

Plan B independently delivers authenticated calendar APIs, local-only MCP tools,
and the guide, then connects calendar data to Plan A. It introduces no dependency
on the separate contacts/import PRs. An Outlook connector and recurring task on
the user's computer require local configuration; this is not a bundled Outlook
connector, background cloud crawler, or automatically configured local scheduler.

The dirty original checkout must remain intact. Implementation should use a
user-approved isolated worktree based on current main, without silently creating
a branch, committing, opening a PR, merging, or deploying.

## Global Constraints

- Preserve existing authorization, explicit sharing, recording, uploads, and navigation.
- Account association classifies an item; it never grants another user access.
- Keep Outlook credentials and provider checkpoints local; never send them to TTOBAK.
- Use existing authenticated APIs and CloudFront; add no public application origin.
- Keep new calendar tools stdio-only; do not expand the remote private MCP bridge.
- Keep calendar events separate from Meeting status, transcripts, notes, and STT.
- Use existing dependencies and visual tokens; add no UI framework or external font.
- Write new documentation in concise English; retain the application's Korean UI copy.
- No commit, PR, merge, or deployment without a separate user request.

## Workspace Design

### Routes and navigation

Use `/` for the authenticated home dashboard and `/meetings` for meeting history.
Preserve the existing unauthenticated home/login flow. Redirect legacy `/?q=...`
searches to `/meetings?q=...`, retaining other search parameters. Update desktop
search, mobile navigation, active-state tests, and CloudFront's static-page
allowlist together. Preserve Assistant, knowledge, account, project, document,
recording, settings, and runtime-gated billing entry points.

Keep account deep links compatible with the existing static dynamic-route
scheme. Desktop account selection sits beside account details; mobile uses a
compact selector. Reuse account discovery/cache and invalidation behavior.

### Meeting rows and ordering

Rows show title, account, actual meeting date/time, processing status, and a short
plain-text preview. Never render a complete Markdown document to hide it with
CSS. Preview processing examines at most 2,048 code points and returns at most
180 code points; render as text, never HTML. Notes/detail rendering is unchanged.

Recorded meetings sort by parsed `date`, then `meetingId` for stable ties; invalid
dates go last. Support newest/oldest and grouped/flat views. Group labels and
date boundaries use the viewer's timezone. Undated meetings remain visible.

The existing API combines differently paginated owned/shared/team streams.
This release sorts, searches, and status/date-filters the **loaded result set**,
explicitly labeled with its loaded count whenever continuation exists. It must
not describe page-local ordering/search as a complete global result. Account
scope remains server-filtered. Do not silently download all history. A globally
ordered cross-share catalog is separate backend work, not a UI-only promise.

Show continuation independently from an empty filtered page. Preserve loaded rows
on a failed "load more"; distinguish refresh, initial load, and genuine empty
states. Deduplicate by meeting ID and reject obsolete account/user responses.
Upcoming events are a separate date-ordered section, not fake recording rows.

### Home and account sections

Home renders bounded sections independently. Recent records use the existing
meeting list. Insights use the existing authorized account insights API for the
selected account, defaulting to the first available account; do not fan out over
every customer or imply a complete organization-wide summary.

Account tabs are overview, meetings, insights, projects, documents, and
runtime-gated billing. Keep existing URL tab behavior. Load secondary data only
when needed, with independent loading, retry, empty, and error states; an insights
failure must not hide meeting history.

Preserve all existing insight kinds and unknown-type fallback. Show evidence,
next action, occurrence date, and source links only where provided and supported
by existing authorized source routing. Do not invent AI summaries or scorecards.

Preserve current account sharing and inherited-read restrictions. Account meeting
history remains the existing shared-history API, not an enumeration of colleagues'
private account-linked meetings. Upcoming account schedules are explicitly labeled
as the signed-in viewer's personal Outlook events.

### Rendering acceptance

Verify 360, 390, 768, 1024, and 1440 CSS-pixel widths, light/dark themes, keyboard
navigation, focus visibility, and no horizontal page overflow. Bound mounted
meeting rows to a 100-row display window with previous/next controls over already
loaded data. Keep a separate explicit action for fetching another server page.
No unbounded automatic fetch loop or mandatory heavy animation.

Measure the same mocked 200-meeting fixture before and after in the same browser.
Record list-render elapsed time and DOM size; describe only measured improvements,
not production latency claims. No private meeting text in screenshots or logs.

## Calendar Contract

### Ownership and minimum data

Calendar events belong to the authenticated user. Linking `accountId` requires
current account access but never shares an event. Another account member cannot
read these events by knowing an ID or using the account filter.

Use separate `CalendarEvent` records: provider `outlook`, locally assigned stable
`sourceId`, stable provider instance identity, title, start/end, timezone,
all-day dates when applicable, optional location, and optional account ID.
Do not import bodies, attachments, attendee emails, join URLs/passcodes, or private/
confidential events by default. If a previously imported event becomes excluded,
the local sync submits an explicit removal.

Do not create recording meetings, start recording, invoke STT, or overwrite saved
notes during synchronization. Outlook data is untrusted text, never instructions
or a URL for the backend to fetch.

### Bounded sync API

New authenticated routes:

- `POST /api/calendar/sources/{sourceId}/sync`
- `GET /api/calendar/sources`
- `GET /api/calendar/events`

`CalendarSyncRequest` contains `batchId`, `expectedRevision`, `windowStart`,
`windowEnd`, `complete`, and `mutations`. A mutation is an upsert with an event or
an explicit remove with `externalId`. Source identity comes from the route and
user identity only from the verified session.

Bounds: 20 distinct event identities per batch, 64 KiB original JSON request,
14,000 encoded bytes per API response, and 32,000 bytes per MCP result. Source and
batch IDs are UUIDs; external IDs are at most 512 UTF-8 bytes; titles at most
512 bytes; locations at most 256 bytes; query windows and event duration at most
93 days.
Reject unknown fields, duplicate identities, invalid times, and invalid account
assignments before any mutation. Limit list pages to 50, preserving opaque
continuation even after bounded filtering produces an empty page.

Upserts carry UTC `startsAt`/`endsAt` plus an IANA timezone. All-day events also
carry local `startDate` and exclusive `endDate`, consistent with those boundaries.
Normalize provider timezone names locally; reject an unknown mapping rather than
silently treating it as UTC. Validate recurrence at the occurrence level rather
than importing only series masters.

### Identity, concurrency, and lifecycle

Derive an opaque event ID from source ID and immutable provider occurrence ID;
titles or start times are not identity. Keep canonical owner-scoped rows and
chronological reference rows in the existing table. Transactionally replace
canonical data, old/new time references, source revision, and batch receipt.
Revalidate references against canonical rows before returning data.

Each source has a monotonically increasing revision. Accept a batch only when its
`expectedRevision` matches. First create expects zero. Repeating an acknowledged
batch ID and normalized payload returns the saved receipt; reusing the ID with
different contents returns conflict. Any conflict rejects the whole batch.
An ambiguous transport failure is retried with the **same** batch ID and revision,
never rebound to the newest revision.

Only explicit removals delete events. Never infer deletions from a failed page,
an incomplete snapshot, or a changed rolling window. A local connector without
provider tombstones may emit removals only after a complete, comparable snapshot
and confirmation that the previously tracked event was deleted or excluded.

`complete=true` means the local caller reports draining its provider pages and
receiving acknowledgements for earlier batches in that window. It alone updates
`lastCompletedAt`; it is not independently verified provider freshness. Empty
completion batches are allowed. Keep partial progress distinguishable from a
completed sync. Record `lastAttemptAt`, `revision`, and last completed window.

Encrypt the bounded sensitive payload with the configured KMS key, binding
encryption context to purpose, user, source, and event. Never fall back to
plaintext when encryption/configuration fails. Time references contain IDs and
timestamps, not titles/locations. Check existing key/IAM configuration before
enabling ingestion; do not claim the existing table is customer-key encrypted.

Use `pendingShareExpiresAt` for calendar TTLs: events and time references expire
90 days after event end; receipts expire seven days after acceptance. Hide expired
rows synchronously; source metadata retains only minimal identity/progress. A
receipt retry after expiry cannot bypass the expected revision fence.

### Read model and freshness

`CalendarEventPage` contains `events` and `nextCursor`; `CalendarSourcePage`
contains `sources` and `nextCursor`. Cursors bind caller and complete query scope.
Event queries accept `from`, `to`, optional `accountId`, `limit`, and `cursor`.
They query chronological references from `from` minus 93 days through `to`, then
retain events overlapping the requested half-open interval. This bounded
lookback includes ongoing events without a whole-history scan. They return
non-expired events in ascending start-time order, excluding cancellations.
Ongoing events remain visible until their exclusive end. An account-filtered
read requires current membership; an unfiltered personal read strips an account
association the viewer can no longer access rather than sharing or deleting the
personal event.

Home selects the next timed event and a seven-day agenda; all-day entries stay
in a separate labeled area. Meetings and Accounts reuse the same read API.
Display local date/time and timezone. Clear private caches on user or membership
change. Missing API/configuration, never synced, stale, failed request, and an
empty completed window are different states. More than 30 minutes since the last
reported completion is stale; show the timestamp, not a claim of live Outlook
connectivity. A stale event may still be displayed, clearly marked.

## Local MCP and Guide

Add stdio-only tools `ttobak_sync_calendar_events`,
`ttobak_list_calendar_events`, and `ttobak_calendar_sync_status`. Validate schemas
before authenticated API calls and return bounded structured results/receipts.
HTTP discovery excludes these tools, and HTTP invocation rejects them. Do not
broaden connector capabilities, file-path access, or unauthenticated routes.

Guide sequence:

1. Configure an approved local Outlook reader and the existing TTOBAK stdio MCP
   in the same MCP-capable host; authorize selected work calendars read-only.
2. Run a dry read and inspect the allowlisted normalized fields before upload.
3. Persist source IDs, acknowledged provider checkpoints, and an explicit
   calendar/account mapping locally. Unknown accounts remain unassigned.
4. Complete one manual sync of the previous seven days and next 30 days; inspect
   duplicate, reschedule, cancellation, private-event, and recurrence behavior.
5. Configure the local host or OS scheduler to run that tested command every ten
   minutes without overlapping runs. Provide clearly marked macOS launchd and
   Windows Task Scheduler templates, not fabricated host CLI commands.
6. Advance a provider checkpoint only after all corresponding writes are
   acknowledged. On source revision conflict, refetch/reconcile; on transient
   delivery failure retry the identical batch. Authentication failures require
   local reauthentication, not copying browser tokens.
7. Explain sleep/offline behavior, stale status, pause/resume, local credential
   protection, log redaction, and removal of the recurring task.

The guide includes a copyable host prompt and an example with synthetic data.
Verify current official connector/provider documentation when writing setup
instructions, including recurring instances, stable IDs, timezone normalization,
deletion delivery, consent, and checkpoint expiry. Do not name unsupported Outlook
MCP tools. Real Outlook acceptance requires the user's configured local reader;
fixture tests cannot establish it.
