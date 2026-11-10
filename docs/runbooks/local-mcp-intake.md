# Local MCP intake (calendar and SpecReq tasks)

This runbook covers the Phase 1 calendar runner and its meeting SpecReq task
step from [ADR-065](../decisions/ADR-065-local-mcp-intake.md). It is a command line in
the TTOBAK stdio adapter, `ttobak-mcp intake`. The Mac Settings UI does not
exist yet. Acceptance on the user's Mac is pending; nothing here proves it.

The runner implements the [calendar sync runbook](local-outlook-calendar-sync.md)
contract for one reader, `aws-outlook-mcp` `calendar_view`.

## Commands

Run with the stdio adapter (`~/.ttobak/server.mjs`) or `node mcp-server/dist/index.js`.
Each command prints one JSON line and exits 0, or 2 with `{ok:false, code}`.

| Command | Effect |
|---|---|
| `intake hosts` | Lists servers in `~/.claude.json`, `~/.codex/config.toml` and `~/.kiro/settings/mcp.json`: name, transport, disabled, command found. Never values. |
| `intake select calendar --host <claude\|codex\|kiro> --server <name> [--accept-unknown-sensitivity]` | Starts that server once and checks the `calendar_view` schema. It stores the reference, the current terminal's `PATH` directories and the TTOBAK endpoints: `TTOBAK_*` env first, else the four endpoint keys of the host's own `ttobak` entry (no other values are read). |
| `intake calendar --dry-run [--preview]` | Reads the window, plans mutations and prints counts. It makes no TTOBAK calls. `--preview` also prints titles locally. |
| `intake calendar [--noninteractive] [--reconcile]` | Syncs. `--noninteractive` fails with `TTOBAK_LOGIN_REQUIRED` instead of opening a browser. `--reconcile` adopts the server revision after a 409. |
| `intake select meetings\|specreq\|slack --host <claude\|codex\|kiro> --server <name>` | Starts that server once and checks every allowlisted tool schema. It stores only the reference and `PATH` directories; TTOBAK endpoints still come from the calendar selection or `TTOBAK_*` env. |
| `intake select specreq ... --auto-ack on\|off` | `on` (default) lets the runner acknowledge an eligible assignment; `off` queues an `ack_needed` notice instead. |
| `intake status` | Shows the selection (names only) and the last calendar and task results and counts. |

The window is the past 7 days through the next 30 days in the Mac's time zone,
read as 7-day `calendar_view` requests.

## Behavior

- **Identity.** `meetingId` (per occurrence) is the `externalId`. One
  `sourceId` is kept per selected server name.
- **Content.** The title, times, all-day dates and location go to TTOBAK.
  Locations have join URLs and passcodes removed. Organizer, attendees, body,
  categories and response are dropped.
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

## SpecReq task step

A synced (non-dry) `intake calendar` run also runs the task step when both
`meetings` and `specreq` are selected; `slack` is optional (without it, request
notices say to copy the text from TTOBAK). It runs under the same
`calendar.lock`, and its result never changes the calendar exit code. The JSON
line adds `tasks: {result, counts, failures?}`; `status.json` keeps the same
under `tasks`. `failures` maps a step to a code, and any failure makes the
result `TASKS_PARTIAL` while independent steps still run.

Steps per run (see `mcp-server/src/intake/tasks.ts`):

1. `load`: every task page (at most 10) and the window's TTOBAK events.
2. `close`: tasks of removed occurrences are closed.
3. `classify`: new occurrences and `needs_*` tasks with an account. One
   `list_meetings`, at most 25 `get_meeting`, then the deterministic classifier.
   A list over its cap, an unknown attendee list or a private meeting creates
   no task.
4. `specreq`: `check_user_is_tam` must equal the signed-in `@amazon.com` alias.
5. `mine` and `theirs`: exact linking by opportunity, date range and
   requester; for theirs, the single acknowledge (auto-ack on) or an
   `ack_needed` notice (off). Unsynced ack ledger entries are written to
   TTOBAK first, decided by `get_request`, never by a second acknowledge.
6. `refresh`: at most 25 linked tasks re-read with `get_request`.
7. `drafts`: one unsent Slack draft per `request_needed` task.
8. `notices`: one line per (task, state) in `~/.ttobak/intake/notify.jsonl`.

Every transition sends the last seen `version`. A 409 re-reads the task once
and resends only if the change is still valid, so a web pick is never
overwritten (`counts.conflicts`).

Local task state: `tasks/<scope>/seen.json` (evaluated, pending and closing
occurrence IDs), `specreq/<scope>/ack-ledger.json` (permanent, one entry per
request ID), `slack/<scope>/drafts.json` and `notify/<scope>/sent.json`
(outcomes and keys only).

### Recovery

- `IDENTITY_MISMATCH` (`failures.specreq`): the SpecReq server is signed in as
  someone other than the TTOBAK user, or TTOBAK's email is not
  `@amazon.com`. No SpecReq step runs. Fix the provider login or sign in to
  TTOBAK with the matching account, then rerun.
- `ack_failed` on a task (`ACK_REJECTED` or `ACK_UNCONFIRMED`): the runner will
  not acknowledge that request again. Acknowledge it in SpecReq by hand; a
  later run reads `WORKING` and moves the task to `acked`. Never edit or delete
  the ack ledger to force a retry.
- A `theirs` failure with a `TTOBAK_*` code after an acknowledge: the ledger
  keeps the outcome with `cloudSynced: false` and the next run writes it.

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
