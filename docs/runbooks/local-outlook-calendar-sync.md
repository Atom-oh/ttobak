# Local Outlook calendar sync

This guide describes the implemented calendar API and local MCP protocol.
It does not install an Outlook reader, obtain consent, or activate a scheduler.
No reader, host or OS has been selected or tested for this installation.
The in-app prerequisites are always available at `/settings#outlook-sync`.
Full persisted stdio → Go → DynamoDB acceptance belongs to Calendar 5;
synthetic protocol fixtures do not establish provider or unattended acceptance.

## Prerequisites and boundaries

- Use a user-owned local host with both a read-only Outlook reader and the
  TTOBAK **stdio** adapter. Complete each service's own login locally.
  Calendar tools are unavailable over both generic and private HTTP MCP;
  a write-client approval does not enable them. Existing client/auth setup stays
  as documented in the [MCP guide](../../mcp-server/README.md).
- Choose the reader and verify its official documentation, executable path,
  foreground invocation, calendar pagination, occurrence expansion, cancellation
  handling and authentication renewal. The documented Graph v1 event-delta
  permission is delegated `Calendars.Read`; verify the selected reader's actual
  read-only consent. Do not request write consent for calendar reading.
- Never copy an Outlook password, browser token or credential file into a
  prompt, command argument or TTOBAK. Provider tokens, full delta links,
  checkpoints and mailbox-to-source mappings stay in protected local storage.
  Exclude them and private calendar content from host, scheduler and TTOBAK logs.
  An AI host's own data handling needs user review before it sees calendar data.
- The authenticated TTOBAK API, calendar encryption key and current account
  access must work before ingestion. Missing encryption configuration fails
  closed. This task does not verify a deployed key or API.

## Local reader contract

Keep one stable random, nonzero UUID `sourceId` per TTOBAK deployment/user,
Outlook mailbox and selected calendar. The local mapping, imported-ID ledger,
pending batches and provider checkpoint must be isolated by that scope.
Never substitute an email address for `sourceId`, reuse another user's mapping,
or generate a new source on every run. User identity comes only from TTOBAK auth.

Use the provider's case-sensitive immutable **occurrence** ID as `externalId`.
With Graph, send `Prefer: IdType="ImmutableId"` on every relevant request,
including continuations. IDs stay stable within a mailbox; archive moves and
export/reimport are exceptions requiring explicit reconciliation. Never derive
identity from the title, current start time or a series master ID. Import
individual recurring occurrences and exceptions; rescheduling retains the ID.
Do not claim arbitrary-calendar delta support: the cited Graph v1 delta route
documents the primary calendar's `calendarView`. Verify other calendar support
in the chosen reader.

Normalize locally before calling TTOBAK:

| Field or operation | Contract |
|---|---|
| `externalId`, `title` | Nonblank UTF-8, each at most 512 bytes; preserve ID case and bytes. |
| `startsAt`, `endsAt` | RFC3339 instants with offsets; positive interval, at most 93 elapsed days. |
| `timeZone` | IANA zone or `UTC`, at most 128 UTF-8 bytes; map provider/Windows zones locally, never `Local`. |
| `isAllDay` | Optional boolean, default false. If true, require `startDate` and exclusive `endDate` (`YYYY-MM-DD`), matching local midnight instants in `timeZone`; a DST day may be 23 or 25 hours. Omit dates for timed events. |
| `location` | Optional, at most 256 UTF-8 bytes; omit join URLs and sensitive details. |
| `accountId` | Optional explicit user-approved TTOBAK Account mapping, `[A-Za-z0-9_-]{1,128}`. Verify current access; omit if unmapped. Classification never shares the private event. |
| Upsert | `{"op":"upsert","event":{...}}` with only the fields above. |
| Removal | `{"op":"remove","externalId":"exact previously tracked occurrence ID"}`. No event body. |

Strip bodies, attendees, attachments, meeting/join URLs and other provider fields.
Exclude `private` and `confidential` events by default locally, and apply the
user's other exclusion rules. A newly `private`, `confidential` or otherwise
excluded event that was previously imported needs an explicit removal of its
tracked occurrence ID; merely skipping its new payload leaves the old data in
TTOBAK. Do not forward sensitivity flags or replacement titles for excluded
events. A cancellation removes only the corresponding tracked occurrence,
not an entire series.

Provider tombstones can refer to items outside the requested window. Check the
local tracked scope and provider evidence before emitting a removal; do not
blindly forward every tombstone. Missing pages, empty responses, revoked access,
filtered results and moving window edges are never deletion evidence. Use a
confirmed cancellation, explicit exclusion or verified item lookup to reconcile
tracked items, including items moved out of the window. If the reader cannot
establish this evidence, retain the item and report reconciliation as unresolved.

## Dry run and manual acceptance

1. Start with a fixed run window covering the past seven days and next 30 days.
   Compute boundaries in the user's chosen zone and convert to instants.
   Follow every provider page, including empty continuation pages. Exclude
   `private` and `confidential` events by default locally. Validate all normalized
   fields and explicit removals using each previously tracked occurrence ID
   without any TTOBAK writes.
2. Confirm selected calendars, exclusions and Account mappings with the user.
   An inaccessible or malformed page is a failed run, not an empty calendar.
3. Read `ttobak_calendar_sync_status` through every `nextCursor` until null.
   Use the matching source's `revision`; only a verified absent new source starts
   at zero. A failed status read is not absence.
4. Send bounded batches with `ttobak_sync_calendar_events`, then inspect
   `ttobak_list_calendar_events` and status. Follow actual `nextCursor` values,
   including empty pages, with unchanged query parameters/limits. Restart stale
   or invalid cursors without treating a restart as a complete read.
5. Manually verify initial import, identical retry, reschedule without duplicate,
   cancellation, private reclassification, confidential reclassification, two
   recurring instances with one cancelled, an all-day DST case and failure on a
   later provider page. Verify explicit removals of each reclassified instance's
   tracked occurrence ID without uploading replacement titles or sensitivity
   fields. Check counts and source revisions. Do this with user-approved
   synthetic data before enabling a repeating task.

List results expose `eventId`, not the provider's `externalId`; keep the local
imported-ID ledger for removals. Losing that ledger requires user reconciliation,
not guesses from titles/times or creation of a replacement source.

## Batches and checkpoints

The actual tools are `ttobak_sync_calendar_events`,
`ttobak_list_calendar_events` and `ttobak_calendar_sync_status`.
See the [API contract](../API-SPEC.md#private-calendar) for bounds and errors.

Before sending, durably journal the exact `sourceId`, new UUID `batchId`,
`expectedRevision`, fixed `windowStart`/`windowEnd`, `complete` and `mutations`.
Send at most 20 distinct occurrence IDs per batch, within the 64 KiB request
limit. Do not send provider tokens, provider links, asserted user IDs or unknown
fields. A successful receipt contains exactly the contract fields `batchId`,
`revision`, `upserted`, `removed`, `completed` (future metadata may be preserved).
Counts describe accepted operations, not newly created or actually deleted rows.

After each validated acknowledgment, atomically record the receipt revision and
associated local imported-ID changes. Keep the pending request until this is
durable. Intermediate batches use `complete:false`. After all provider pages and
all required upserts/removals are acknowledged, send a final `complete:true`
batch, possibly with `mutations:[]`. Only its acknowledgment allows committing
the provider delta checkpoint for that completed scope. An empty completion
does not remove missing events. Keep provisional page progress separate from the
committed provider checkpoint; a crash must recover the identical pending batch.

Preserve the entire Graph `nextLink`/`deltaLink` and original range locally.
Do not edit a delta link to advance the rolling window or append unsupported
`select`/`filter`/`orderby`/`search` options. Advancing the past-seven/next-30-day
window requires a fresh view for the new range or a reader-documented equivalent,
fully enumerated before committing its checkpoint. A complete view still does
not authorize absence-based deletions.

- **Unconfirmed delivery:** network loss, interruption or an unusable receipt
  may follow a committed batch. Retry the identical batch ID, expected revision,
  source and full payload; do not invent a new ID or advance the checkpoint.
- **Definitive 409:** refetch paged source status and reconcile the local journal
  against current provider evidence and imported IDs. Do not retry indefinitely
  or attach old mutations to a newer revision. Only newly reconciled work gets a
  new batch ID. TTOBAK receipts have a seven-day replay horizon, separate from
  Outlook token lifetime; old ambiguous work may require manual reconciliation.
- **Other definitive errors:** fix validation, access or encryption prerequisites
  before proceeding. A rejected batch does not authorize checkpoint advancement.

## Synthetic wire example

These fixed IDs and dates are examples, not a live task or credentials.
MCP `ttobak_sync_calendar_events` arguments:

```json
{
  "sourceId": "12345678-1234-1234-abcd-123456789abc",
  "batchId": "87654321-4321-4321-abcd-cba987654321",
  "expectedRevision": 0,
  "windowStart": "2026-10-31T00:00:00Z",
  "windowEnd": "2026-12-07T00:00:00Z",
  "complete": false,
  "mutations": [{
    "op": "upsert",
    "event": {
      "externalId": "SyntheticOccurrence/AbC",
      "title": "Synthetic planning",
      "startsAt": "2026-11-09T09:00:00Z",
      "endsAt": "2026-11-09T10:00:00Z",
      "timeZone": "UTC"
    }
  }]
}
```

The adapter sends authenticated
`POST /api/calendar/sources/12345678-1234-1234-abcd-123456789abc/sync`
with this body; `sourceId` is a path parameter, and `userId` is never supplied:

```json
{
  "batchId": "87654321-4321-4321-abcd-cba987654321",
  "expectedRevision": 0,
  "windowStart": "2026-10-31T00:00:00Z",
  "windowEnd": "2026-12-07T00:00:00Z",
  "complete": false,
  "mutations": [{
    "op": "upsert",
    "event": {
      "externalId": "SyntheticOccurrence/AbC",
      "title": "Synthetic planning",
      "startsAt": "2026-11-09T09:00:00Z",
      "endsAt": "2026-11-09T10:00:00Z",
      "timeZone": "UTC"
    }
  }]
}
```

Expected first receipt in a new synthetic source:
`{"batchId":"87654321-4321-4321-abcd-cba987654321","revision":1,"upserted":1,"removed":0,"completed":false}`.
It acknowledges the batch, not provider freshness or full synchronization.

## User-local ten-minute task

MCP itself is not a scheduler. Select and test a local host before filling in
these placeholders:

| Placeholder | User must supply and test |
|---|---|
| `TESTED_FOREGROUND_HOST_COMMAND_AND_ARGUMENTS` | Exact documented local host invocation that completes one run and stays in the foreground. No Outlook MCP executable or flags are prescribed here. |
| `/ABSOLUTE/TESTED/PATH/foreground-sync-with-lock` | User-created executable wrapper containing that tested command and protected local configuration. |
| `C:\REPLACE_WITH_TESTED_PATH\foreground-sync-with-lock.cmd` | Windows equivalent, with a tested foreground host invocation. |

The wrapper must hold an OS process lock for the entire run, including provider
pagination, retries, acknowledgment and local checkpoint commit. Manual runs
must use the same lock. If locked, skip the new run; do not queue overlapping
work, background the host or release the lock before its child exits. A leftover
lock requires checking the owning process and pending journal before recovery.
Test two simultaneous invocations and a crash before scheduler activation.
Use protected local files for task instructions; no credentials in task arguments
or stdout/stderr. Scheduling requires the user's logged-in session and network.

### macOS LaunchAgent template

Replace the absolute wrapper path and save under the logged-in user's
`~/Library/LaunchAgents/click.atomai.ttobak.outlook-sync.plist`.
This template is not installed or loaded by TTOBAK. `ProgramArguments` is a
tokenized array, not a shell command; the wrapper supplies the tested host command.
The interval is 600 seconds. Use the chosen OS version's documented launchctl
procedure only after manual acceptance and lock testing.

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>click.atomai.ttobak.outlook-sync</string>
  <key>ProgramArguments</key>
  <array><string>/ABSOLUTE/TESTED/PATH/foreground-sync-with-lock</string></array>
  <key>StartInterval</key><integer>600</integer>
  <key>RunAtLoad</key><false/>
</dict>
</plist>
```

### Windows Task Scheduler template

For the logged-in, non-elevated user, create a ten-minute task only after the
wrapper and its shared lock pass manual tests. `/it` requires that user to be
logged on. Paths below are explicit placeholders; task creation does not validate
the executable. No password or administrator account is needed:

```bat
schtasks /create /tn "TTOBAK Outlook Sync" /sc minute /mo 10 /it /tr "C:\REPLACE_WITH_TESTED_PATH\foreground-sync-with-lock.cmd"
```

In Task Scheduler's task Settings, select **Do not start a new instance** when
the task is already running. Keep the wrapper's lock too, because manual runs
and separately registered tasks are outside that setting. Confirm foreground
execution, permissions and the next scheduled result in this user's session.
Do not describe either template as a working unattended installation.

## Copyable host instruction

```text
Help me configure local Outlook-to-TTOBAK sync. First identify my OS, local
Outlook reader and execution host. Verify their official docs and report the
tested absolute executable path and foreground command; do not invent an
Outlook MCP executable or flags. I must complete read-only provider consent
and the existing TTOBAK stdio login. HTTP cannot enable calendar tools.
Dry-run the past seven/next 30 days, following all pages and recurring
occurrences. Strip bodies, attendees and join URLs locally.
Exclude private and confidential events by default locally.
Use only my confirmed Account mapping. Preserve mailbox/user/calendar sourceId
and immutable occurrence externalId; explicitly remove the previously imported,
tracked occurrence externalId for cancellation, private reclassification or
confidential reclassification. Do not upload sensitivity fields or replacement
titles for excluded events. Missing or failed pages are not deletions.
Use ttobak_calendar_sync_status, ttobak_sync_calendar_events and
ttobak_list_calendar_events with their actual schemas and opaque continuations.
Journal each batch before sending. Acknowledge all pages and mutations before
final completion and provider checkpoint commit. Retry identical uncertain
batches; refetch status and reconcile definitive 409 conflicts.
After manual acceptance, show me a ten-minute user-local foreground task with
a shared no-overlap lock, stale/offline reporting, pause and uninstall steps.
Keep provider tokens, checkpoints and mailbox mappings local and out of logs.
Do not claim setup or unattended acceptance until I test it.
Do not treat guide or protocol checks as proof of the selected reader filtering
private or confidential events; verify that behavior separately.
```

## Failure, freshness, pause and uninstall

- Sleep, logout, offline periods and failed authentication interrupt progress.
  On return, resume the pending journal, verify status, and re-read provider
  changes before reporting success. Do not replay a queue of overlapping timer
  ticks. Reauthenticate through the selected local reader or TTOBAK login.
- Outlook delta state has a cache-dependent lifetime, **not a guaranteed seven
  days**. A 410/reset or `syncStateNotFound` requires a full provider resync.
  Keep old imported IDs for explicit reconciliation. An incomplete reset cannot
  commit a new checkpoint, complete the run or infer deletions.
- Compare `revision` with optional `lastCompletedRevision`, plus
  `lastCompletedAt`, `completedWindowStart`, `completedWindowEnd`. Missing
  completion revision is unknown; a lower completion revision is partial
  progress even if timestamps match. `lastAttemptAt` records accepted batch
  progress, not every local failed poll. Completion is caller-reported, not
  independently verified Outlook freshness. For this ten-minute task, treat
  more than two missed intervals as a local stale warning, not a server promise.
- Pause by disabling/unloading the user's scheduled task and waiting for any
  running foreground task to stop safely. Pausing does not delete imported data.
  To uninstall, remove the task/LaunchAgent after unloading it. If the user
  requests data removal, send explicit tracked-ID removals and acknowledge them
  **before** deleting the local mapping/journal. There is no source-delete or
  provider-reconciliation endpoint. Without the ID ledger, stop and arrange
  manual reconciliation. Retention/TTL is not an immediate-delete guarantee.
  Revoke the reader's local consent and use TTOBAK's existing local logout;
  preserve unrelated clients and profiles.

## Primary references and verification limits

Provider references were checked on 2026-11-07; no real account was accessed:

- [Immutable Outlook IDs](https://learn.microsoft.com/en-us/graph/outlook-immutable-id)
- [Event delta, Graph v1](https://learn.microsoft.com/en-us/graph/api/event-delta?view=graph-rest-1.0)
- [Calendar-view delta](https://learn.microsoft.com/en-us/graph/delta-query-events)
- [Delta reset and token duration](https://learn.microsoft.com/en-us/graph/delta-query-overview)
- [Apple user launch agents](https://developer.apple.com/library/archive/documentation/MacOSX/Conceptual/BPSystemStartup/Chapters/CreatingLaunchdJobs.html)
- [Windows task creation](https://learn.microsoft.com/en-us/windows-server/administration/windows-commands/schtasks-create)

`frontend/scripts/mcp-guide.test.mjs` checks the local prerequisites and stable
anchor under loading/error/HTTP states. `mcp-server/test/calendar.test.mjs`
exercises real stdio framing and API serialization against synthetic HTTPS
responses, including lifecycle state in a fixture and public bundle calls.
These guide and protocol checks do not verify the selected reader's private or
confidential filtering.
It does not run an Outlook reader, scheduler, Go service or DynamoDB. Calendar 5
owns persisted integration acceptance; selected-reader authentication, actual
calendar behavior and user-session scheduling require separate local acceptance.
