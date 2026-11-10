# Local MCP intake (calendar and SFDC)

This runbook covers the Phase 1 calendar runner from
[ADR-065](../decisions/ADR-065-local-mcp-intake.md). It is a command line in
the TTOBAK stdio adapter, `ttobak-mcp intake`. The Mac Settings UI does not
exist yet. Acceptance on the user's Mac is pending; nothing here proves it.

The runner implements the [calendar sync runbook](local-outlook-calendar-sync.md)
contract for one reader, `aws-outlook-mcp` `calendar_view`. The
[SFDC connector](#sfdc-connector) reads `aws-sentral-mcp` for the owner-only
SA performance data of [ADR-068](../decisions/ADR-068-sa-performance-data-residency.md).

## Commands

Run with the stdio adapter (`~/.ttobak/server.mjs`) or `node mcp-server/dist/index.js`.
Each command prints one JSON line and exits 0, or 2 with `{ok:false, code}`.

| Command | Effect |
|---|---|
| `intake hosts` | Lists servers in `~/.claude.json`, `~/.codex/config.toml` and `~/.kiro/settings/mcp.json`: name, transport, disabled, command found. Never values. |
| `intake select calendar --host <claude\|codex\|kiro> --server <name> [--accept-unknown-sensitivity]` | Starts that server once and checks the `calendar_view` schema. It stores the reference, the current terminal's `PATH` directories and the TTOBAK endpoints: `TTOBAK_*` env first, else the four endpoint keys of the host's own `ttobak` entry (no other values are read). |
| `intake calendar --dry-run [--preview]` | Reads the window, plans mutations and prints counts. It makes no TTOBAK calls. `--preview` also prints titles locally. |
| `intake calendar [--noninteractive] [--reconcile]` | Syncs. `--noninteractive` fails with `TTOBAK_LOGIN_REQUIRED` instead of opening a browser. `--reconcile` adopts the server revision after a 409. |
| `intake status` | Shows the selection (names only) and the last run result and counts. |

The window is the past 7 days through the next 30 days in the Mac's time zone,
read as 7-day `calendar_view` requests.

## Behavior

- **Identity.** `meetingId` (per occurrence) is the `externalId`. One
  `sourceId` is kept per selected server name.
- **Content.** The title, times, all-day dates, location and the organizer's
  lowercase email domain (`organizerDomain`) go to TTOBAK. Locations have join
  URLs and passcodes removed. The organizer's name and email local part,
  attendees, body, categories and response are dropped.
- **Exclusions.** Items flagged `Private` or `Confidential` are excluded, and
  removed if they were tracked. Events longer than 93 days are skipped. Real
  runs fail with `SENSITIVITY_UNKNOWN` when any item lacks a sensitivity flag,
  unless the selection was made with `--accept-unknown-sensitivity`.
- **Removals.** Only a cancelled or excluded *tracked* occurrence is removed.
  Absence is reported as `unresolved` and never deleted.
- **Delivery.**
  - Each batch (at most 20 mutations, about 60 KB or less) is journaled in
    `~/.ttobak/intake/calendar/<sourceId>/<scope>/state.json` before sending.
    `<scope>` is a digest of the TTOBAK deployment and the signed-in user, so
    a batch built for one account is never sent after signing in to another;
    each account keeps its own ledger and pending batch. A dry run plans
    against the ledger of the last synced account.
  - An ambiguous failure keeps it for an identical retry on the next run.
  - A 4xx drops it. A 409 also blocks later runs until `--reconcile`.
  - The last batch of a run is `complete:true`.
- **Errors.**
  - `AUTH_EXPIRED_MIDWAY`: run `mwinit -o`.
  - Also `SERVER_START_FAILED`, `SCHEMA_CHANGED`, `PARSE_FAILED`, `LOCKED`
    (another run holds `calendar.lock`) and `TTOBAK_*`. A lock whose process
    is gone is replaced by one run at a time; if the PID was reused by an
    unrelated process, delete `~/.ttobak/intake/calendar.lock` by hand.

## Deploy order and rollback

- Deploy the API before any runner that sends `organizerDomain`. The calendar
  request decoder is strict, so an older API rejects such batches with 400 and
  every calendar run fails until the API is updated.
- Rolling the API back below this release after a calendar sync breaks reading
  the newly sealed events: older readers decode the sealed plaintext strictly
  and report `organizerDomain` as corrupt data, so `GET /api/calendar/events`
  returns 500 for pages holding those events. Reading recovers only after a
  re-sync rewrites them without the field (an older runner, and only within
  the intake window), after they expire, or after the API is redeployed at or
  above this release, whose sealed decoder tolerates unknown fields.
- Re-running `intake calendar` backfills `organizerDomain` only for events in
  the intake window (past 7 days through the next 30), not the full year.

## Acceptance on the Mac (required before scheduling)

1. Sign in once interactively: `intake calendar --dry-run` does not need TTOBAK;
   a first non-dry run without `--noninteractive` opens the TTOBAK login.
2. `intake select calendar --host claude --server aws-outlook-mcp`, then
   `intake calendar --dry-run`. Check that the counts are plausible.
3. Create synthetic test events and verify each case:
   - a **private** event: is it hidden by the server, or does it carry a
     sensitivity flag? Observed 2026-11-09: neither; it arrives like a public
     event. `--accept-unknown-sensitivity` therefore accepts that private
     items reach the private TTOBAK calendar;
   - a **moved occurrence** of a recurring event keeps its `meetingId`;
   - a **cancelled occurrence** arrives with `isCanceled: true`, or disappears
     (then it stays `unresolved`);
   - an all-day event.
4. Run `intake calendar` and compare with `ttobak_list_calendar_events`.
   Re-run and confirm that the mutations are empty.
5. Record the date and results in ADR-065's status line.

## Schedule (after acceptance)

Install a LaunchAgent at `~/Library/LaunchAgents/click.atomai.ttobak.intake.plist`.
It runs every 10 minutes, and its arguments are fixed and contain no secrets:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>click.atomai.ttobak.intake</string>
  <key>ProgramArguments</key><array>
    <string>/ABSOLUTE/PATH/TO/node</string>
    <string>/Users/YOU/.ttobak/server.mjs</string>
    <string>intake</string><string>calendar</string><string>--noninteractive</string>
  </array>
  <key>StartInterval</key><integer>600</integer>
  <key>StandardOutPath</key><string>/Users/YOU/Library/Logs/ttobak/intake.log</string>
  <key>StandardErrorPath</key><string>/Users/YOU/Library/Logs/ttobak/intake.err</string>
  <key>Nice</key><integer>10</integer>
</dict></plist>
```

Load it with `launchctl bootstrap gui/$(id -u) <plist>`, and remove it with
`launchctl bootout gui/$(id -u)/click.atomai.ttobak.intake`. The log holds
codes and counts only. Delete `~/.ttobak/intake/` to forget the local ledger.
The TTOBAK copies stay until removed in TTOBAK.

## SFDC connector

`intake select sfdc --host <claude|codex|kiro> --server aws-sentral-mcp` checks
the input schemas of `get_my_personal_details`, `get_registry_assignments` and
`search_tasks`, and keeps the calendar selection. `intake sfdc [--dry-run]
[--noninteractive] [--reconcile]` then reads and syncs; a dry run prints counts
only and makes no TTOBAK calls. It holds `sfdc.lock`.

- **Reads.** The alias and SFDC user ID come from `get_my_personal_details`.
  Registry assignments are requested at level L14 without a `hierarchyId`,
  because hierarchy IDs are versions, not years. Only `sales/YYYY-MM-DD`
  records of the newest returned version are kept; other hierarchies (for
  example Partner L14 sellers) are not SFDC accounts and are dropped. Tasks
  are the owner's (`ownerId`) with `activityDate` from January 1 of the
  previous year through one year after today. Both follow `cursor` pages of 500, at most 20 pages each.
  A capped, cursorless, short (`totalCount`) or malformed page fails with
  `PARSE_FAILED`; a changed schema with `SCHEMA_CHANGED`. Every read finishes
  before any batch is built, so `AUTH_EXPIRED_MIDWAY` never leaves a partial
  snapshot.
- **Content.** Tasks send only `taskId`, `sfdcAccountId` (empty when the task
  has no account), `activityDate` (`sa_Activity_Date__c`, else
  `activityDate`), `type` (`sa_Activity_Category__c`, else `type`), `subject`
  and `status`. No SpecReq ID is guessed from the subject. Descriptions,
  comments, owners, opportunities and other fields never leave the parser.
  Tasks dated outside January 1 of the previous year through today are
  skipped and counted.
- **Assignments.** Registry entries carry their `assignmentSource` as role.
  Every other account of an in-window task is added with role `activity` and
  the task's account name. A registry entry wins.
- **Snapshots.** Both reads are complete, so a tracked assignment or task that
  is absent from a later read is removed.
- **Delivery.** `sfdc-assignments` is sent first, then `sfdc-tasks`, through
  `POST /api/performance/sources/{source}/sync`. Each source keeps its own
  ledger, revision and pending batch at
  `~/.ttobak/intake/sfdc/<sourceId>/<scope>/<source>.json`. Batches, retries,
  4xx/409 handling and `complete:true` follow the calendar rules above. The
  receipt must carry revision+1 and matching counts. `--reconcile` adopts the
  revisions of `GET /api/performance/sources` and resends every current row.
- **Stale server assignments.** On `--reconcile`, and whenever the local
  assignments ledger is fresh (a new Mac, a deleted `~/.ttobak/intake`, or a new
  `sourceId` after selecting another server name), the runner also reads
  `GET /api/performance/mappings` and keeps only its SFDC account IDs. Every
  server assignment absent from the current snapshot is sent as a `remove`, so a
  lost ledger cannot leave false silent customers. Tasks are not listed; they
  keep their `pendingShareExpiresAt` TTL. A malformed listing fails with
  `TTOBAK_UNAVAILABLE` before any batch is built.

Acceptance on the Mac is pending: run `intake sfdc --dry-run`, compare the
counts with AWSentral, then run `intake sfdc` and re-run to confirm empty
batches. Add `intake sfdc --noninteractive` to the schedule only after that.
