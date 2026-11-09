# Local MCP intake (calendar, SpecReq tasks and SFDC)

This runbook covers the Phase 1 calendar runner and its meeting SpecReq task
step from [ADR-065](../decisions/ADR-065-local-mcp-intake.md). It is a command line in
the TTOBAK stdio adapter, `ttobak-mcp intake`. The Mac Settings UI does not
exist yet. Acceptance on the user's Mac is pending; nothing here proves it.

The runner implements the [calendar sync runbook](local-outlook-calendar-sync.md)
contract for one reader, `aws-outlook-mcp` `calendar_view`. It also matches each
event to an account for account-shared calendars (see
[Account matching](#account-matching)). The
[SFDC connector](#sfdc-connector) reads `aws-sentral-mcp` for the owner-only
SA performance data of [ADR-068](../decisions/ADR-068-sa-performance-data-residency.md).

## Quick start: `ttobak-mcp intake setup`, then `ttobak-mcp intake run`

Run with the stdio adapter (`~/.ttobak/server.mjs`) or `node mcp-server/dist/index.js`.
Each command prints one JSON line and exits 0, or 2 with `{ok:false, code}`.
`setup` writes Korean prompts and progress to stderr only.

```text
intake setup [--yes] [--noninteractive] [--accept-unknown-sensitivity] [--skip <connector>]...
  [--schedule off|hourly|daily@HH:MM|twice-daily@HH:MM,HH:MM]
intake run [--dry-run] [--noninteractive] [--only calendar,sfdc]
intake schedule off|hourly|daily@HH:MM|twice-daily@HH:MM,HH:MM
```

`intake setup`:

1. Lists the host servers (as `intake hosts` does) and probes only the known
   name per connector: calendar `aws-outlook-mcp`, sfdc `aws-sentral-mcp`,
   meetings `amazon-meetings-mcp`, specreq `spec-req-mcp`, slack `slack-mcp`.
   When one name exists in several hosts, Claude wins over Codex, then Kiro;
   interactive mode asks only then. `--yes` takes the default.
2. Verifies each candidate exactly as `intake select` does and stores it. No
   candidate, `--skip` or a failed check marks the connector `skipped` with a
   code (`SERVER_NOT_CONFIGURED`, `SKIPPED_BY_USER`, `SCHEMA_CHANGED`, ...),
   and setup continues. Setup is the source of truth: a skipped connector is
   disabled in the saved selection (`disabled: true`), so `intake run`
   matches the report. The entry keeps its `sourceId` and ledger, so selecting
   the same server again (setup or `intake select`) re-enables it without
   duplicate TTOBAK copies; a different server gets a new source.
   SpecReq is always selected with auto-ack off (`autoAck: false`); turn it
   on deliberately with `intake select specreq ... --auto-ack on`.
3. Calendar needs consent that private items reach the private TTOBAK
   calendar (see [Behavior](#behavior)): a yes/no question, or
   `--accept-unknown-sensitivity` with `--yes`. Consent stored by an earlier
   selection of the same server is reused (`consentReused: true`), also with
   `--yes` alone. Otherwise it is skipped with
   `SENSITIVITY_UNKNOWN`.
4. `AUTH_EXPIRED_MIDWAY` during a check, dry run or sync stops setup: run
   `mwinit -o` and rerun. A stop during the checks leaves the saved selection
   as it was before setup.
5. Dry-runs the calendar and SFDC connectors selected now and reports the
   counts. Then it asks whether to run the first sync (`--yes` runs it).
   Setup is a human command, so the first sync may open the TTOBAK login, even
   with `--yes`; `--noninteractive` (implies `--yes`) never prompts or opens it.
6. Writes the LaunchAgent only with `--schedule` or an interactive answer
   (see [Schedule](#schedule)).

The JSON line is `{ok, connectors: {<name>: {status, code?, server?, host?}},
dryRun: {<connector>: {result, counts}}, synced, schedule, sync?}`. A failed
dry run or first sync exits 2 with the run's code and `synced: false`; the
selections stay saved and no schedule is installed.

`intake run` runs the selected connectors in order, calendar (with its task
step) then SFDC, with one shared TTOBAK sign-in. The JSON line is
`{ok, results: {calendar: {result, counts, tasks?}, sfdc: {result, counts}}}`.
It exits 0 only when every attempted connector succeeded. Otherwise it exits 2
with `PARTIAL_FAILURE`; `AUTH_EXPIRED_MIDWAY` anywhere, including the
calendar's task step (`tasks.failures`), stops the run before later
connectors, because they share Midway, and is the code. Setup's first sync
follows the same rule. Disabled selections are skipped. `--noninteractive` never prompts and
never opens a browser; the LaunchAgent uses it.

## Advanced: manual commands

| Command | Effect |
|---|---|
| `intake hosts` | Lists servers in `~/.claude.json`, `~/.codex/config.toml` and `~/.kiro/settings/mcp.json`: name, transport, disabled, command found. Never values. |
| `intake select calendar --host <claude\|codex\|kiro> --server <name> [--accept-unknown-sensitivity] [--own-domain <domain>]...` | Starts that server once and checks the `calendar_view` schema. It stores the reference, the current terminal's `PATH` directories and the TTOBAK endpoints: `TTOBAK_*` env first, else the four endpoint keys of the host's own `ttobak` entry (no other values are read). `--own-domain` is repeatable (default `amazon.com`, at most 20); those domains never match an account. |
| `intake calendar --dry-run [--preview]` | Reads the window, plans mutations and prints counts. Its only TTOBAK call is the share directory read with a stored session, never a browser login; without one the directory counts as unavailable. `--preview` also prints titles locally. |
| `intake calendar [--noninteractive] [--reconcile]` | Syncs. `--noninteractive` fails with `TTOBAK_LOGIN_REQUIRED` instead of opening a browser. `--reconcile` adopts the server revision after a 409. |
| `intake select meetings\|specreq\|slack --host <claude\|codex\|kiro> --server <name>` | Starts that server once and checks every allowlisted tool schema. It stores only the reference and `PATH` directories; TTOBAK endpoints still come from the calendar selection or `TTOBAK_*` env. |
| `intake select specreq ... --auto-ack on\|off` | `on` (default) lets the runner acknowledge an eligible assignment; `off` queues an `ack_needed` notice instead. |
| `intake status` | Shows the selection (names only) and the last calendar and task results and counts. |
| `intake run ...` | Runs every selected sync connector; see [Quick start](#quick-start-ttobak-mcp-intake-setup-then-ttobak-mcp-intake-run). |

The window is the past 7 days through the next 30 days in the Mac's time zone,
read as 7-day `calendar_view` requests.

## Behavior

- **Identity.** `meetingId` (per occurrence) is the `externalId`. One
  `sourceId` is kept per selected server name.
- **Content.** The title, times, all-day dates, location and the organizer's
  lowercase email domain (`organizerDomain`) go to TTOBAK. Locations have join
  URLs and passcodes removed. The organizer's name and email local part,
  attendees, body, categories and response are dropped.
  Recurring items also send `seriesKey`, the hex SHA-256 of `sourceId|seriesId`.
- **Exclusions.** Items flagged `Private` or `Confidential` are excluded, and
  removed if they were tracked. Events longer than 93 days are skipped. Real
  runs fail with `SENSITIVITY_UNKNOWN` when any item lacks a sensitivity flag,
  unless the selection was made with `--accept-unknown-sensitivity`.
- **Private.** An item with the Outlook category `Private` (any case), or
  whose joined `get_meeting` says `isPrivate: true`, is sent with
  `private: true` and no share fields. If it was shared, this re-sends it and
  the server records a veto.
- **Removals.** Only a cancelled or excluded *tracked* occurrence is removed.
  Absence is reported as `unresolved` and never deleted.
- **Delivery.**
  - Each batch (at most 12 mutations, so that share-bearing upserts stay
    within the server's 100-item transaction, and about 60 KB or less) is journaled in
    `~/.ttobak/intake/calendar/<sourceId>/<scope>/state.json` before sending.
    `<scope>` is a digest of the TTOBAK deployment and the signed-in user, so
    a batch built for one account is never sent after signing in to another;
    each account keeps its own ledger and pending batch. A dry run plans
    against the ledger of the last synced account.
  - An ambiguous failure keeps it for an identical retry on the next run.
  - A 4xx drops it. A 409 also blocks later runs until `--reconcile`.
  - The last batch of a run is `complete:true`.
- **Counts.** Besides the plan counts: `shared`, `candidates` and `private`
  (events in the window, sent or unchanged), `attendeesUnavailable` and
  `directoryUnavailable` (boolean).
- **Errors.**
  - `AUTH_EXPIRED_MIDWAY`: run `mwinit -o`.
  - Also `SERVER_START_FAILED`, `SCHEMA_CHANGED`, `PARSE_FAILED`, `LOCKED`
    (another run holds `calendar.lock`) and `TTOBAK_*`. A lock whose process
    is gone is replaced by one run at a time; if the PID was reused by an
    unrelated process, delete `~/.ttobak/intake/calendar.lock` by hand.

## Deploy order and rollback

- Deploy the API before any runner that sends `organizerDomain`. The calendar
  request decoder is strict, so an API that predates `organizerDomain` rejects
  such batches with 400 and every calendar run fails until the API is updated.
- Rolling the API back to a release that predates `organizerDomain` after a
  calendar sync breaks reading the newly sealed events: those readers decode the
  sealed plaintext strictly without that field and report it as corrupt data, so
  `GET /api/calendar/events` returns 500 for pages holding those events. Reading
  recovers only after a re-sync rewrites them without the field (an older runner,
  and only within the intake window), after they expire, or after the API is
  redeployed with `organizerDomain` support. A rollback to a strict reader that
  already declares `organizerDomain` still reads them.
- Re-running `intake calendar` backfills `organizerDomain` only for events in
  the intake window (past 7 days through the next 30), not the full year.

## Account matching

- **Directory.** Each run reads `GET /api/calendar/share-directory` once
  (accounts the user directly belongs to: ID, name, aliases, domains). If the
  read fails, `directoryUnavailable` is `true` and every event keeps the share
  fields recorded in the ledger; unchanged events are not re-sent, so no share
  is removed because of a missing directory.
- **Matching.** The deterministic matcher (`intake/match.ts`) compares title
  and location with names and aliases, and attendee and organizer domains with
  account domains, excluding own domains. Exactly one account sends
  `shareAccountId` with `shareEvidence` (`title`, `location`, `domain`; never
  matched text). Several accounts send up to five `candidateAccountIds`. No
  match sends neither, which unshares a previously shared event. Fingerprints
  cover these fields, so a match or category change re-sends the event.
- **Meeting facts.** When the `meetings` connector is selected, each event that
  may be shared is joined to its meeting exactly like the task step (one
  `list_meetings` per run, then `get_meeting` with the recorded SpecReq login
  address, else the TTOBAK email). Only the address domains and `isPrivate` are
  kept; a payload without `isPrivate` or any attendee or organizer field is no
  answer. Facts are cached in `calendar/<sourceId>/meeting-facts.json` by
  `meetingId` and `meetingChangeKey`, pruned with the ledger. At most 25
  `get_meeting` reads run per sync, and three consecutive failures stop them.
  A lookup without an answer counts in `attendeesUnavailable` and keeps a
  previous private flag or a previous share that rested on domain evidence.
  Without the connector, matching uses title, location and organizer domain.

## SpecReq task step

A synced (non-dry) `intake calendar` run also runs the task step when both
`meetings` and `specreq` are selected; `slack` is optional (without it, request
notices say to copy the text from TTOBAK). It runs under the same
`calendar.lock`, and its result never changes the calendar exit code. The JSON
line adds `tasks: {result, counts, failures?}`; `status.json` keeps the same
under `tasks`. `failures` maps a step to a code, and any failure makes the
result `TASKS_PARTIAL` while independent steps still run.

**Who "me" is.** The SpecReq login (`check_user_is_tam` → `employee_login`)
is the authoritative Amazon identity; the TTOBAK sign-in email may be any
address (for example a personal one). Me is the TTOBAK token `sub` plus the
addresses {TTOBAK email, `<login>@amazon.com`}, lowercased. My account
membership and role are matched by TTOBAK user ID, never by email; I am
excluded from attendees by both addresses and never become a counterpart; the
meetings provider gets `<login>@amazon.com`; requester/resolver checks and the
Slack request signature use the login. The first confirmed login is recorded in
`tasks/<scope>/identity.json` (scope = deployment + TTOBAK user).
`intake select specreq` prints the detected `login` (or `identityError`), never
an email.

Steps per run (see `mcp-server/src/intake/tasks.ts`):

1. `load`: every task page (at most 50 pages, 1,000 tasks) and the window's
   TTOBAK events. Past the cap, `failures.load` is `PARSE_FAILED` and
   `counts.skipped` is 3: `mine`, `theirs` and `refresh` do not run (a hidden
   row could make a link ambiguous), while `close`, the read-only `specreq`
   identity check, `classify`, `drafts` and `notices` still run.
2. `close`: tasks of removed occurrences are closed.
3. `specreq`: `check_user_is_tam` returns my login; it must equal the login
   recorded for this TTOBAK account (recorded on the first success). It runs
   before `classify` (also past the task cap) because classification needs my
   Amazon address. If SpecReq is unreachable, `classify` uses the recorded
   login; on a mismatch, only the TTOBAK email.
4. `classify`: new occurrences and `needs_*` tasks with an account. One
   `list_meetings`, at most 25 `get_meeting`, then the deterministic classifier.
   Only a customer meeting creates a task: at least one attendee or the
   organizer must be outside `@amazon.com`. An internal-only meeting, a list
   over its cap, an unknown attendee list or a private meeting creates no task
   (not even `needs_account`). Addresses stay in memory.
5. `mine` and `theirs`: exact linking by opportunity, date range and
   requester; for theirs, the single acknowledge (auto-ack on) or an
   `ack_needed` notice (off). Unsynced ack ledger entries are written to
   TTOBAK first, decided by `get_request`, never by a second acknowledge.
6. `refresh`: at most 25 linked tasks, least recently refreshed first, re-read
   with `get_request`. The refresh time is stamped before the read, so a read
   that keeps failing still rotates. An `ack_failed` task is refreshed only
   when I am still the request's resolver.
7. `drafts`: one unsent Slack draft per `request_needed` task.
8. `notices`: one line per (task, state) in `~/.ttobak/intake/notify.jsonl`.
   While Slack is selected, a `request_needed` notice waits for the draft
   outcome, but at most 60 minutes after the task first waited; then it goes
   out with the copy-from-TTOBAK text.

Every transition sends the last seen `version`. A 409 re-reads the task once
and resends only if the change is still valid, so a web pick is never
overwritten (`counts.conflicts`).

Local task state: `tasks/<scope>/seen.json` (evaluated, pending, closing,
last-refreshed and first-waited occurrence IDs), `tasks/<scope>/identity.json`
(the recorded SpecReq login), `specreq/<scope>/ack-ledger.json` (permanent, one entry per
request ID), `slack/<scope>/drafts.json` and `notify/<scope>/sent.json`
(outcomes and keys only).

### Recovery

- `IDENTITY_MISMATCH` (`failures.specreq`): SpecReq returned no valid login,
  or a login other than the one recorded for this TTOBAK account (an Amazon
  account switch). No SpecReq linking, acknowledge or refresh runs. Sign the
  SpecReq server back in as the recorded login; if the switch is intended,
  delete `~/.ttobak/intake/tasks/<scope>/identity.json` and rerun. A non-Amazon
  TTOBAK email is not a mismatch.
- `ack_failed` on a task (`ACK_REJECTED` or `ACK_UNCONFIRMED`): the runner will
  not acknowledge that request again. Acknowledge it in SpecReq by hand; a
  later run reads `WORKING` and moves the task to `acked`. Never edit or delete
  the ack ledger to force a retry.
- A `theirs` failure with a `TTOBAK_*` code after an acknowledge: the ledger
  keeps the outcome with `cloudSynced: false` and the next run writes it.

### Known limits

- **Cross-device re-ack window.** The ack ledger is per device. The task row
  is the cross-device record, so a second device skips a row that is already
  `acked`, `ack_failed` or `resolved`, and every acknowledge first reads the
  request and needs `NEXT_UP`. Between one device's refused or unconfirmed
  acknowledge (the request stays `NEXT_UP`) and its cloud write of `ack_failed`
  (delayed by a crash or a `TTOBAK_*` failure), or during two concurrent runs,
  a second device sees an unchanged row and can acknowledge the same request
  once more. Run the task step on one device only.
- **Task cap.** Past 1,000 tasks no SpecReq linking, acknowledge or refresh
  runs (see `load`). Closed rows count toward the cap.
- **Attendees are read once.** An occurrence is classified on its first
  evaluation. Later attendee changes are not re-evaluated, so an occurrence
  classified as skip stays skipped.
- **External means not `@amazon.com`.** Any other address, including
  affiliates and resource mailboxes, counts as an external attendee and makes
  the occurrence a customer meeting.

## Acceptance on the Mac (required before scheduling)

1. Sign in once interactively: a first non-dry run without `--noninteractive`
   opens the TTOBAK login. `intake calendar --dry-run` never opens a login; it
   reads the share directory only when a TTOBAK session exists, and otherwise
   reports `directoryUnavailable: true`.
2. `intake select calendar --host claude --server aws-outlook-mcp`, optionally
   `intake select meetings --host claude --server amazon-meetings-mcp`, then
   `intake calendar --dry-run`. Check that the counts are plausible, including
   `shared`, `candidates`, `private` and `attendeesUnavailable`.
3. Create synthetic test events and verify each case:
   - a **private** event: is it hidden by the server, or does it carry a
     sensitivity flag? Observed 2026-11-09: neither; it arrives like a public
     event. `--accept-unknown-sensitivity` therefore accepts that private
     items reach the private TTOBAK calendar;
   - an event whose title contains an account alias, one with an attendee
     from an account domain only, one matching two accounts, a `Private`
     category added after sharing, and a title edited after sharing;
   - a **moved occurrence** of a recurring event keeps its `meetingId`;
   - a **cancelled occurrence** arrives with `isCanceled: true`, or disappears
     (then it stays `unresolved`);
   - an all-day event.
4. Run `intake calendar` and compare with `ttobak_list_calendar_events`.
   Re-run and confirm that the mutations are empty.
5. Record the date and results in ADR-066's status line.

### Task step acceptance

1. Select the three task connectors, starting with `--auto-ack off`:
   `intake select meetings --host claude --server amazon-meetings-mcp`,
   `intake select specreq --host claude --server spec-req-mcp --auto-ack off`,
   `intake select slack --host claude --server slack-mcp`.
2. Create a synthetic customer meeting on a TTOBAK account with another SA
   member invited. Run `intake calendar` and check the task state on the
   meeting card, the Slack draft in that DM (unsent) and one
   `request_needed` line in `notify.jsonl`. Run again: no new line, no second
   draft. Cancel the meeting and confirm the task closes.
3. With the user present, take one real assignment: after the counterpart
   issues the SpecReq, run with `--auto-ack off` and check the `ack_needed`
   notice. Then select `--auto-ack on`, rerun, and confirm exactly one
   `acknowledge_request` (SpecReq shows `WORKING`, the task `acked`).
4. Confirm `status` and the log hold codes and counts only.
5. Record the date and results in ADR-065's status line.

### Mac notification acceptance

The Mac app (`intake_notify.rs`) polls `~/.ttobak/intake/notify.jsonl` at launch
and every 60 seconds. It reads the file only when it is a regular, non-symlink
file owned by you, not writable by others, and at most 1 MiB. It skips malformed
or partial lines, `v` other than 1, keys or paths that do not match the 64-hex
event ID, entries older than 24 hours or more than 5 minutes ahead, and over-long
text. It dedupes on `key` in `intake-notified.json` (app data directory, 0600,
pruned after 48 hours) and posts at most 5 per cycle. Notification delivery has
not been verified on a Mac; ADR-061 found app notifications unreliable for the
meeting prompt. Check by hand with the packaged app running:

1. Append one synthetic line with a fresh 64-hex ID and a current `createdAt`
   (the format is in the header of `mcp-server/src/intake/notify.ts`) and keep the file at
   mode 0600. Within about a minute exactly one notification appears. Append the
   same line again: no second notification.
2. Clicking the notification only activates the app; it does not navigate. The
   task is on the meetings page's task strip.
3. Make the file a symlink: nothing posts and the log says the queue is
   unreadable. `chmod 666` it: nothing posts and the log says it was ignored.
4. Record the observed behaviour (delivered or not, whether the click activates
   the app) in ADR-065's status line.

Notifications are informational: the app never navigates because of one, so it
cannot discard unsaved input. The task strip on the meetings page shows the task.

## Schedule

Schedule only after the acceptance above. `intake setup --schedule <value>`
(or a yes answer in interactive setup) writes
`~/Library/LaunchAgents/click.atomai.ttobak.intake.plist`, boots out any old
job and runs `launchctl bootstrap gui/<uid>` with argv, never a shell.
Turn it off with `intake schedule off`, which boots the job out and deletes
the plist. `intake schedule <value>` only writes or removes the plist and
reloads the job; it starts no server and runs no sync. Setup's `--schedule`
calls the same step at the end. No plist is written without one of these. A launchctl failure is
`SCHEDULE_FAILED`.

| Value | launchd key |
|---|---|
| `hourly` | `StartInterval` 3600 |
| `daily@HH:MM` | one `StartCalendarInterval` entry |
| `twice-daily@HH:MM,HH:MM` | two `StartCalendarInterval` entries |

The arguments are fixed and contain no secrets: the absolute node executable
that ran setup, the absolute path of the running CLI script, then
`intake run --noninteractive`. Keep the CLI at that path (for example
`~/.ttobak/server.mjs`); rerun `intake setup --schedule ...` after moving or
reinstalling the CLI. The plist built by
`buildLaunchAgentPlist` in `mcp-server/src/intake/schedule.ts` looks like:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>click.atomai.ttobak.intake</string>
  <key>ProgramArguments</key><array><string>/ABSOLUTE/PATH/TO/node</string><string>/Users/YOU/.ttobak/server.mjs</string><string>intake</string><string>run</string><string>--noninteractive</string></array>
  <key>StartCalendarInterval</key><array><dict><key>Hour</key><integer>9</integer><key>Minute</key><integer>0</integer></dict></array>
  <key>StandardOutPath</key><string>/Users/YOU/Library/Logs/ttobak/intake.log</string>
  <key>StandardErrorPath</key><string>/Users/YOU/Library/Logs/ttobak/intake.err</string>
  <key>Nice</key><integer>10</integer>
</dict></plist>
```

A hand-installed job with the same label (the earlier 10-minute
`intake calendar` template) is replaced. To remove a job by hand, run
`launchctl bootout gui/$(id -u)/click.atomai.ttobak.intake` and delete the
plist. The log holds codes and counts only. Delete `~/.ttobak/intake/` to
forget the local ledger. Calendar copies stay until removed in TTOBAK; SFDC
assignments are reconciled (see below).

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
  A capped, cursorless, short (`totalCount`) or malformed page, or a record ID
  repeated across pages, fails with `PARSE_FAILED`; a changed schema with `SCHEMA_CHANGED`. Every read finishes
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
- **Stale assignments.** On `--reconcile` or a fresh assignments ledger (new
  Mac, deleted state, new `sourceId`), the SFDC IDs of
  `GET /api/performance/mappings` absent from the snapshot are removed; tasks
  keep their TTL. A malformed listing fails with `TTOBAK_UNAVAILABLE`, a
  truncated one with `ASSIGNMENT_CAP_EXCEEDED`.
- **Cap.** Removals precede upserts, so no batch exceeds the 500-assignment
  cap; a larger snapshot fails with `ASSIGNMENT_CAP_EXCEEDED` before sending.

Acceptance on the Mac is pending: run `intake sfdc --dry-run`, compare the
counts with AWSentral, then run `intake sfdc` and re-run to confirm empty
batches. The scheduled `intake run` includes SFDC whenever it is selected, so
schedule only after that, or pass `--skip sfdc` to setup, which disables
SFDC and keeps its ledger.
