# Local MCP intake (calendar)

This runbook covers the Phase 1 calendar runner from
[ADR-065](../decisions/ADR-065-local-mcp-intake.md). It is a command line in
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
| `intake status` | Shows the selection (names only) and the last run result and counts. |

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
    `~/.ttobak/intake/calendar/<sourceId>/state.json` before sending.
  - An ambiguous failure keeps it for an identical retry on the next run.
  - A 4xx drops it. A 409 also blocks later runs until `--reconcile`.
  - The last batch of a run is `complete:true`.
- **Errors.**
  - `AUTH_EXPIRED_MIDWAY`: run `mwinit -o`.
  - Also `SERVER_START_FAILED`, `SCHEMA_CHANGED`, `PARSE_FAILED`, `LOCKED`
    (another run holds `calendar.lock`) and `TTOBAK_*`.

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
