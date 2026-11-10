# ADR-065: Local MCP intake

- Status: Accepted; Phase 1 (calendar runner, CLI only) implemented. Calendar
  acceptance on the user's Mac is pending (see the runbook).
- Decision date: 2026-11-09.
- Design: [local MCP intake design](../superpowers/specs/2026-11-09-local-mcp-intake-design.md).
- Related: ADR-045 (stdio-only calendar tools), ADR-046 (Mac control path).

## Context

The user's Mac already runs authenticated internal MCP servers (calendar, SFDC,
SpecReq) in Claude Code, Codex and Kiro. Their credentials are Midway cookies
and host-specific environment entries. TTOBAK should stay current with a few
of these sources without holding provider credentials and without copying host
configuration.

## Decision

- **Deterministic connectors.** A local runner (`ttobak-mcp intake`, in
  `mcp-server/src/intake/`) is an MCP client of one selected server per
  connector. It calls fixed read tools with fixed arguments, parses results
  strictly and writes through the user's existing TTOBAK API token. AI hosts
  never write to TTOBAK on a schedule. A later optional AI job may produce
  proposals only.
- **References, not copies.** The selection stores a host and server name, the
  approved `PATH` directories and non-secret TTOBAK endpoints. Each run
  re-reads that one host entry and spawns it with argv, never a shell. Command,
  arguments and environment stay in memory. They are never written to disk,
  printed, logged or sent to TTOBAK.
- **Calendar.** `aws-outlook-mcp` `calendar_view` is the only calendar source.
  It follows the [calendar sync runbook](../runbooks/local-outlook-calendar-sync.md):
  - occurrence `meetingId` as `externalId`;
  - join links stripped from locations;
  - private and confidential items excluded when flagged;
  - removals only for cancelled or excluded tracked occurrences;
  - a journaled batch before each send, identical retry on ambiguous delivery,
    and `complete:true` last;
  - a 409 stops the runner until `--reconcile`.
- **Residency.** Calendar stays private. A later SFDC connector stores only the
  opportunity ID, name and stage, for accounts a member mapped. A later SpecReq
  connector stores ID, title, status, account and a summary of at most 1,000 bytes.
- **Writes stay stdio/local.** No new HTTP MCP tool or public route is added.
- **Scheduling** uses a user-installed launchd LaunchAgent that runs
  `intake calendar --noninteractive`. Background runs never open a browser
  login.

## Consequences

- Local state lives in `~/.ttobak/intake/` (0700 directory, 0600 files): the
  selection, per-source ledger, pending batch, status and a run lock.
- Midway expiry surfaces as `AUTH_EXPIRED_MIDWAY` and needs a human `mwinit`.
- The calendar source exposes no sensitivity flag. On 2026-11-09 a synthetic
  event marked Private in Outlook arrived from `calendar_view` with the same
  fields as a public one, and no `aws-outlook-mcp` calendar tool mentions
  sensitivity. Private items are therefore neither hidden nor flagged. Real runs
  fail closed with `SENSITIVITY_UNKNOWN`; `--accept-unknown-sensitivity` means
  accepting that private items reach the user's private TTOBAK calendar. Dry
  runs report the count.
- Codex TOML is read by a bounded in-repo parser (`toml.ts`); no new dependency.
- The Mac Settings UI and app-managed schedule are Phase 2.
