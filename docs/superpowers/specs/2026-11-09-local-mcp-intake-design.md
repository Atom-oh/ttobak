# Local MCP intake for the Mac app — design

Status: historical design proposal (2026-11-09), awaiting approval. This is not a record of
implementation. Decisions from 2026-11-09 are in §11; calendar output facts are
in §3.1. Reviewed source: `origin/main` at `96623c3d`. This is project 3, "Local intake",
of the account ontology roadmap (see `feat/account-signals-flow`,
`docs/superpowers/specs/2026-11-08-account-signals-and-flow-design.md`).
That roadmap's data-residency decision applies without change.

## 1. Intent

The user's Mac already runs authenticated internal MCP servers in three AI hosts.
These servers cover the calendar, SFDC, SpecReq and internal docs. TTOBAK should
stay current with the relevant parts of that data. It must do this without TTOBAK
cloud holding provider credentials, and without a second copy of each host's
configuration. The Mac app should:

1. discover which local MCP servers exist (Claude Code, Codex, Kiro);
2. let the user opt a few into named, fixed TTOBAK connectors;
3. keep TTOBAK updated on a schedule, with visible freshness and failure state.

## 2. Goals, non-goals, anti-goals

**Goals**

- G1. List local MCP servers from `~/.claude.json`, `~/.codex/config.toml` and
  `~/.kiro/settings/mcp.json`. Show only the name, host, transport, whether the
  command resolves and whether the entry is disabled.
- G2. Fixed connectors, each bound to exactly one selected server:
  - **Calendar**: `aws-outlook-mcp` (decided, §11) into the existing private
    calendar API.
  - **SFDC opportunities**: `aws-sentral-mcp` into Projects. Only the
    opportunity ID, name and stage go to the cloud.
  - **SpecReq**: `spec-req-mcp`. ID, title, status, account and a bounded
    summary go to the cloud (§11).
- G3. An unattended schedule that survives app restarts and runs without the
  window open. It does nothing while the Mac sleeps. It supports "Run now".
- G4. Per-connector status in Settings: last success, last error class (for
  example `AUTH_EXPIRED_MIDWAY`, `TTOBAK_LOGIN_REQUIRED`, `PARSE_FAILED` or
  `SERVER_START_FAILED`), counts and next run.
- G5. An optional, opt-in AI "review" job (option B) that produces *proposals*:
  account mapping suggestions and link candidates. A human applies them in TTOBAK.

**Non-goals**

- A generic MCP proxy, or "call any tool from TTOBAK".
- Running internal-docs servers (builder-mcp, aws-knowledge-mcp-server,
  aws-documentation-mcp-server) on a schedule. These are query tools, not feeds
  (§4.4).
- Highspot. No Highspot server is configured in any host, so there is nothing to
  import.
- Slack or mail bodies (excluded by the roadmap residency rule).
- Automating `mwinit` or any provider login.
- Editing the user's host config files.

**Anti-goals (failure even if it works)**

- TTOBAK copying, persisting, logging or uploading host config `env`, `headers`,
  `args`, tokens or cookies.
- A remote SPA page able to start arbitrary local processes or MCP tool calls.
  This would turn an XSS on `ttobak.atomai.click` into local code execution.
- Unattended AI runs that write into shared TTOBAK records based on their own
  judgment, or that hold provider write tools (for example
  `aws-sentral-mcp__update_opportunity`, `slack-mcp__post_message`).
- Deletions inferred from absence (the calendar runbook already forbids this).
- Silently stale data that shows "synced" when the source failed.

## 3. Facts observed on this Mac (names and shapes only; no values read out)

| Host | File (mode) | Relevant servers | Transport |
|---|---|---|---|
| Claude Code 2.1.319 | `~/.claude.json` (0600), top-level `mcpServers`; 0 of 8 projects have project-scoped `mcpServers` | aws-outlook-mcp, amazon-meetings-mcp, aws-sentral-mcp, spec-req-mcp, aws-knowledge-mcp-server, ttobak | stdio except aws-knowledge (http) |
| Codex 0.159.3 | `~/.codex/config.toml` (**0644**), `[mcp_servers.*]` | the same plus builder-mcp, aws-documentation-mcp-server (`uvx`), outlook-mac | stdio; aws-knowledge and notion `url` |
| Kiro CLI 3.1.0 | `~/.kiro/settings/mcp.json` (0600), `mcpServers` | aws-outlook, amazon-meetings, aws-sentral, spec-req, aws-knowledge | stdio; aws-knowledge `url`; `disabled` and `autoApprove` keys present |

Quirks that matter:

- **Relative commands.** Most `command` values are bare names. They resolve via
  the interactive shell `PATH` (`~/.toolbox/bin`, `~/.aim/mcp-servers`,
  `~/.local/bin`). A Finder-launched app or a launchd job gets a minimal `PATH`
  and will not find them. The same server may be absolute in one host and
  relative in another.
- **Same name, different definitions.** A server can have different args and env
  in each host, and only Kiro has `disabled`. Each server name is therefore an
  `(host, name)` pair. The user picks the host whose definition works. The
  default choice is the host where the command resolves and the entry is enabled.
- **The `ttobak` entry carries TTOBAK_* env** (3 keys) in Claude and Codex.
  These are nonsecret deployment settings, but Codex's file is world-readable.
  Settings should mention it. TTOBAK must not change it.
- **Codex has an `otel` table, and the shell profile sets an OTel exporter** to a
  remote collector for claude/codex. Scheduled AI runs (option B) may export
  prompts or tool output there. This needs checking before B is enabled (§9).
- **Claude is a zsh function** that unsets Bedrock-related variables before
  calling `~/.local/bin/claude`. launchd does not run zsh functions. A scheduled
  B job must reproduce that environment explicitly, or it will pick a different
  provider and billing path.
- **Midway-backed servers** (aws-outlook, amazon-meetings, aws-sentral,
  spec-req, builder) depend on `~/.midway/cookie`, which expires (commonly
  within about a day; verify locally). spec-req-mcp failed to connect in this
  session. Recovery requires a human (`mwinit` with PIN and security key).
- **Tool output format is unknown.** `aws-outlook-mcp__calendar_view` takes
  `MM-DD-YYYY` dates and a day, week or month view. `amazon-meetings-mcp__list_meetings`
  documents meeting IDs, ISO 8601 times, a recurring flag and a `Canceled`
  response status. Neither documents a stable per-occurrence ID or JSON
  `structuredContent`. `aws-sentral-mcp__search_opportunities` documents a
  cursor-paged JSON-like `resultRecords` with an `ownershipFilter`
  (`associated`), stage enums and `lastStageChangeDate`. Before writing parsers,
  check real output shapes with the user's consent (plan Phase 0).

### 3.1 Calendar output observed (2026-11-09, user consent, structure only)

Two identical `aws-outlook-mcp__calendar_view` calls (one week, primary
calendar) were compared. No content was recorded.

- The result is a JSON array in text content. Each item has `meetingId`,
  `meetingChangeKey`, `subject`, `start`, `end` (ISO 8601 with offset),
  `location`, `status` (Free/Busy/Tentative), `categories`, `organizer`
  (`name`, `email`), `isCanceled`, `isRecurring`, `isAllDay`, `response` and
  reminder fields. Recurring items also carry `seriesId`.
- Both calls returned the same items with identical `meetingId` and
  `meetingChangeKey` values.
- Recurring occurrences have a distinct `meetingId` per date. The ID embeds
  the occurrence date and shares the series' `seriesId`. Single events use a
  plain item ID. So `meetingId` is a per-occurrence ID, and `meetingChangeKey`
  can detect changes.
- Multi-day events that overlap the window are returned, including one that
  ends exactly at the window start. The runner must clip by the half-open
  window.
- There is no IANA time zone field, only offsets. The runner maps to the Mac's
  zone.
- `organizer.email` is often an Exchange X.500 DN, not an SMTP address. It cannot
  map attendees to accounts.
- `location` often contains join URLs with passcodes. It must be stripped
  (runbook rule).
- **Gaps that block calendar acceptance:**
  - No sensitivity or private flag was observed. The runbook requires
    private/confidential exclusion. Phase 1 must confirm, with a known private
    test event, whether the server masks or flags private items. If not, a
    different tool or reader is needed.
  - It is unverified whether a rescheduled occurrence keeps its `meetingId`.
    The date-encoded form suggests it uses the original start, but this is not
    proven. Phase 1 tests it with a moved test occurrence.
  - Whether cancelled occurrences are returned with `isCanceled: true`, or just
    disappear, is unverified. Absence must never mean removal.

## 4. Option analysis per data source

Option definitions:

- **A**: deterministic connector code acts as the MCP client of the selected
  server and calls fixed read-only tools with fixed arguments. It maps results
  to TTOBAK API calls.
- **B**: a scheduled headless AI host run (`claude -p`, `codex exec`,
  `kiro-cli chat --no-interactive`) with its own MCPs plus the ttobak MCP,
  driven by a prompt.

### 4.1 Calendar (aws-outlook-mcp / amazon-meetings-mcp)

| Criterion | A | B |
|---|---|---|
| Feasibility | High if one server returns stable occurrence IDs. The full contract already exists (runbook, `ttobak_sync_calendar_events`). | Possible, but the runbook's journal, revision, 20-mutation batch and checkpoint rules are hard to get right repeatedly with a prompt. |
| Determinism | Exact: same input, same batch, identical retry. | No. Drift in ID choice, removals and pagination is likely. Duplicates, or missed cancellations, cannot be audited. |
| Auth expiry | Detects the Midway failure class and stops cleanly. | The model may "work around" it, report success, or hallucinate empty data. An empty calendar must never count as deletion evidence. |
| Cost | Negligible; every 10 minutes is fine. | Tokens per run; a 10-minute cadence is costly. |
| Failure visibility | Typed error, persisted status. | Free text; needs a structured-output schema and still fails softly. |

**Verdict: A.** This matches the earlier hybrid recommendation.

### 4.2 SFDC opportunities (aws-sentral-mcp)

The earlier hybrid put SFDC under B because "opportunity to account mapping is
judgment". I disagree with that. Split the work in two:

- *Mapping* an SFDC account to a TTOBAK account is a rare, one-time human
  decision per customer. Store it as an explicit link (`sfdcAccountId` on the
  TTOBAK account, or a mapping row). An AI may *suggest* it, interactively or
  through the G5 proposal job, but it is applied by a member.
- *Syncing* opportunity ID, name and stage for mapped accounts is fully
  deterministic: `search_opportunities` with `accountId` EXACT_MATCH (or
  `ownershipFilter: associated` with the user's alias), paged by cursor. It is
  an upsert keyed by `sfdcOpptyId`.

B cannot respect the residency boundary as reliably. An AI with
`fetch_opportunity_details` in context could write amounts or contacts into
notes or descriptions. A enforces the allowed field set in code.

**Verdict: A for sync. Human mapping, optionally with B-generated proposals.**

### 4.3 SpecReq (spec-req-mcp)

Its tools could not be listed because the server failed to connect. It is most
likely structured records (request ID, customer, status, owner, dates), which
again suits A. What may live in the cloud is undecided: SpecReq text can
describe customer internals. **Verdict: A after schema discovery and a residency
decision.** Default to ID, title, status, link and account mapping only, as with
SFDC.

### 4.4 Internal docs (builder-mcp, aws-knowledge, aws-documentation) and Highspot

These are search and read tools with no change feed, so there is nothing
deterministic to poll. A scheduled AI that "finds relevant docs per account" and
uploads them would copy internal or confidential content into team-shared account
KBs (ADR-020, roadmap access rules). It would also give an unattended agent broad
read access. Its value is low and its risk high. **Verdict: neither A nor B as a
scheduled intake.** Use these servers on demand in the user's interactive host
(prep, QA), where the user sees what is pulled. The existing
`ttobak_put_document` / `ttobak_kb_upload` can store what the user chooses to
keep. Re-evaluate Highspot when a server exists.

### 4.5 Where B still earns its place

B is useful where output is a *proposal*, not a write:

- unmapped SFDC accounts: proposed TTOBAK account matches with evidence;
- calendar events without an `accountId`: proposed account by attendee domain;
- "this news or event may relate to opportunity X" candidates, which the
  roadmap's G7 manual linking then applies.

Proposals land in a local file, or in a future authenticated "suggestions" API
with an explicit pending state. They never land directly in shared relations.
B is also the fallback if a source server's output turns out to be free text that
A cannot parse robustly. That fallback must still go through a strict output
schema (`--json-schema` / `--output-schema`). The deterministic writer then
validates and applies the result; the AI never calls TTOBAK write tools itself.

### 4.6 Summary

| Source | Recommendation | Cadence |
|---|---|---|
| Calendar | A | 10 min, no overlap (matches runbook) |
| SFDC opportunities | A (after human account mapping) | 1–4 h |
| SpecReq | A after Phase 0 schema and residency decision | 1–4 h |
| Docs / Highspot | No scheduled intake; interactive only | — |
| Mapping/link suggestions | B, opt-in, proposals only | Daily |

So the recommendation is **"A by default, B only for proposals"**. It is not a
data-source split between A and B.

## 5. Recommended architecture

```
Settings (remote SPA, Tauri only)
   │  fixed Tauri commands: local_intake_list / _get / _set / _run_now / _status
   ▼
Mac app (Rust)  ── native confirmation dialogs; writes LaunchAgent plist
   │  spawns with fixed argv, bounded stdout
   ▼
ttobak-intake runner (Node, same package as ~/.ttobak/server.mjs)
   ├─ reads host config at run time (in memory only)
   ├─ MCP client (@modelcontextprotocol/sdk Client + StdioClientTransport)
   │     → aws-outlook-mcp / amazon-meetings-mcp / aws-sentral-mcp / spec-req-mcp
   ├─ connector: fixed tool names + argument builders + strict parsers
   ├─ local journal/ledger/checkpoint (~/.ttobak/intake/, 0700/0600)
   └─ TTOBAK API client (existing api.ts/auth.ts token cache) → CloudFront API
launchd LaunchAgent → runs the runner on an interval (also when app closed)
```

Why the runner is Node in `mcp-server`, not Rust:

- Rust must not own Cognito tokens (mac-app CLAUDE.md, ADR-024). The Node
  package already owns verified stdio login, the refresh cache under
  `~/.ttobak/tokens/` and the calendar validators (`src/calendar.ts`).
- The MCP TypeScript SDK already ships a client and stdio transport. Rust would
  need a new MCP client dependency.
- Parsers and connectors get `node:test` coverage in CI. Mac Rust code has no CI.
- The runner works without the Mac app (CLI and dry run), which helps Phase 1.

Rust's role is small: run the runner with fixed subcommands, show native
consent dialogs, manage the LaunchAgent plist, and relay bounded status JSON to
the SPA.

### 5.1 Config references, not copies

The selection file `~/.ttobak/intake/selection.json` (0600) stores only
`{connector, host, serverName, enabled, cadence, options}`. Options are
non-secret, such as the calendar choice, exclusions, alias or account mappings.
At each run the runner re-reads the named host file, takes that one server
entry, and spawns it with its own `command`, `args` and `env`, held in memory
only. Nothing from the entry is persisted, logged, sent to TTOBAK or shown in the
UI beyond name and transport. Changes to the host config therefore take effect
on the next run. A removed entry yields `SERVER_NOT_CONFIGURED`.

### 5.2 Parsing

- Claude: JSON. Read top-level `mcpServers`. Also enumerate
  `projects[path].mcpServers`, which is currently empty on this Mac. Show
  project-scoped entries labeled with a hashed or shortened project label, not a
  full path. Repo-level `.mcp.json` files are out of scope because they need the
  host's per-project approval state. Entries are `type: stdio|http|sse` with
  `command/args/env` or `url/headers`.
- Codex: TOML `[mcp_servers.<name>]` with `command/args/env/env_vars/cwd/
  enabled/startup_timeout_sec/tool_timeout_sec/enabled_tools` or `url`
  (+ `bearer_token_env_var`, `http_headers`). Honor `enabled = false`. Use a
  strict, bounded TOML parser (`smol-toml` or similar; size cap 1 MiB) and
  ignore unknown keys.
- Kiro: JSON `mcpServers`, with `command/args/env` or `url`, `disabled`,
  `autoApprove`. Honor `disabled: true`. Workspace `.kiro/settings/mcp.json` is
  out of scope.
- Every parser: reject symlinked or non-user-owned files, cap size, catch
  errors per host (one broken file must not hide the others) and never echo
  values in errors. Name validation is `^[A-Za-z0-9_.-]{1,64}$`.
- Command resolution: if relative, resolve against a `PATH` captured once from
  the user's login shell at setup (`zsh -lc 'printf %s "$PATH"'`), shown and
  approved in Settings, and stored in `selection.json` (directories are
  non-secret). Never `sh -c` the command line; spawn argv directly.
- Remote (`url`) servers: v1 supports stdio only. HTTP servers such as
  aws-knowledge with headers need their own token handling and are not
  needed for the chosen connectors.

### 5.3 Connector contract (A)

Each connector is code, not configuration:

- a fixed set of read-only tool names and an argument builder (no free-form
  arguments from the UI);
- a strict parser from tool result (prefer `structuredContent`, else JSON text,
  else fail with `PARSE_FAILED`) to a normalized local record;
- a field allowlist for what crosses to TTOBAK;
- error classification: Midway or auth patterns, start failure, timeout,
  parse failure, TTOBAK 401, 409 or 5xx.

Before calling, the runner checks `tools/list` for the expected tool name and
input-schema shape. A mismatch yields `SCHEMA_CHANGED`, with no writes.

Calendar follows `docs/runbooks/local-outlook-calendar-sync.md` verbatim:
stable `sourceId`, immutable occurrence `externalId`, private and confidential
exclusion, explicit removals only, a durable journal before send, identical retry
and a checkpoint after the final `complete:true`. A lock file prevents overlap.

SFDC: for each mapped account, page `search_opportunities` and project to
`{sfdcOpptyId, name, stage}`. Then call a new idempotent
upsert (§7). Closed opportunities that disappear from a filter are not deleted;
the stage reflects closure.

### 5.4 Scheduling on macOS

None of the three CLIs has a local scheduler (checked with `--help`):

- **Claude Code**: `-p/--print` for headless runs, plus `--mcp-config`,
  `--strict-mcp-config`, `--allowedTools/--disallowedTools`,
  `--permission-mode dontAsk`, `--permission-prompts none`, `--max-budget-usd`,
  `--json-schema`, `--output-format json`, `--no-session-persistence`, `--bare`
  and `--restricted`. `--bg` runs a background session but not on a timer.
  `/loop` is session-scoped. Cloud routines (`schedule`) run remotely and cannot
  reach local stdio MCPs or the Midway cookie.
- **Codex**: `codex exec` with `--ephemeral`, `-s read-only`,
  `--output-schema`, `--json`, `-o`, `-c key=value` overrides (for example
  disabling unselected `mcp_servers`) and `--skip-git-repo-check`. Codex Cloud
  is remote. Whether MCP tool calls need approval in exec mode, and which config
  key disables that prompt, must be verified.
- **Kiro CLI**: `chat --no-interactive`, `--trust-tools=<list>`, `--agent`
  (custom agent JSON with its own tool and MCP set), `--require-mcp-startup`
  (exit code 3 if a server fails, which is useful for failure visibility),
  `--output-format stream-json` and `--model/--effort`. `--cloud` is remote.

So option B's "register a job in Claude Code, Codex or Kiro" in practice means
**TTOBAK writes a launchd LaunchAgent that invokes the CLI headlessly**. Both A
and B therefore use launchd:

| Mechanism | Pros | Cons |
|---|---|---|
| launchd LaunchAgent (`~/Library/LaunchAgents/click.atomai.ttobak.intake.plist`, `StartInterval`) | Runs with the app closed, survives reboot, coalesces missed runs after sleep, `launchctl` status | Minimal env (PATH), must be removed on uninstall or opt-out, and macOS 13+ shows a "background item added" notice |
| In-app timer (Tokio) | Simple, visible, stops when the app quits | Only while the app runs; the app has no tray or menu-bar mode today |
| `SMAppService` agent | Apple-managed login item | Needs a bundled helper and real signing; ad-hoc builds are fragile |

Recommend a launchd LaunchAgent (`launchctl bootstrap gui/$UID`). The plist
contains only the runner path, fixed subcommand, `ProgramArguments`, a
non-secret `PATH`, `StandardOutPath` / `StandardErrorPath` under
`~/Library/Logs/ttobak/` and a nice value. It holds no env secrets and no
prompts. The in-app "Run now" spawns the same runner once. The runner's lock
file prevents overlap between launchd and "Run now".

## 6. Option B job design (proposals only)

- Fixed, versioned prompt templates live in the runner package. The SPA cannot
  supply prompt text.
- Generated per-host restriction:
  - Claude: `--strict-mcp-config` is attractive, but it needs `--mcp-config`
    with server definitions, which would mean copying env. Instead, run with the
    user's config and a tight `--allowedTools` list of read-only tools. Use
    `--permission-prompts none`, `--max-budget-usd`, `--no-session-persistence`
    and `--json-schema`, plus explicit unset of the Bedrock variables (mirroring
    the zsh wrapper) or the user's chosen provider env.
  - Codex: `exec --ephemeral -s read-only --output-schema`, plus `-c` overrides
    that disable every non-selected `mcp_servers.*`, and `enabled_tools` limited
    to read tools.
  - Kiro: a generated agent JSON (`~/.kiro/agents/ttobak-intake.json`) listing
    only the selected MCP servers and read tools, with `--trust-tools` limited
    to that list and `--require-mcp-startup`. Writing into `~/.kiro/agents/` is
    a host-config change, so it needs explicit consent. Prefer an agent path
    outside it if `--agent` accepts a path (verify).
- The TTOBAK MCP is **not** given to the B run. The output is structured JSON
  that the deterministic runner validates and stores as proposals. This removes
  the main prompt-injection impact: SFDC or SpecReq text cannot make the agent
  write to TTOBAK or call provider write tools.
- Each run records host, model, duration and the cost reported by the host, with
  no content.

## 7. TTOBAK API / MCP gaps

| Need | Exists? | Gap |
|---|---|---|
| Private calendar sync | Yes (`/api/calendar/*`, stdio tools) | Runner uses the API client directly; no change. Check `provider:"outlook"` naming for Amazon Meetings. |
| Account ↔ SFDC account mapping | No | Add `sfdcAccountIds` (String Set) on Account META, or a `SFDCMAP#` relation row with a transactional reverse index (`SFDCACCT#{id}` → accountId) for lookup. Member-only write, conditional update. |
| Opportunity upsert by `sfdcOpptyId` | Partial: Project has `sfdcOpptyId`, `stage`; create and update exist | Idempotent `PUT /api/accounts/{accountId}/opportunities/{sfdcOpptyId}` (or a project upsert) with a uniqueness row `SFDCOPP#{id}`. It writes only `name`, `stage`, `stageChangedAt`; rejects other fields; uses conditional updates. A new project links to the account transactionally. |
| Stage history / days in stage | No (roadmap project 1 notes the same gap) | Append `STAGEHIST#` rows on stage change; shared with project 1. |
| SpecReq records | No | After Phase 0: `ACCOUNT#…/SPECREQ#{id}` with an allowlisted field set, idempotent upsert, member read. |
| Proposals | No | v1 local file shown in Settings; later authenticated `SUGGESTION#` rows with explicit accept/dismiss. |
| Intake freshness for others | No | Optional `INTAKE#{connector}` status per user (last success time, counts only) so the account page can say "SFDC synced 2h ago". |
| MCP tools | Calendar tools stdio-only | New opportunity and SpecReq write tools should stay stdio-only like calendar (not delegated connector operations), unless separately reviewed under ADR-003. |

Every new route is authenticated, under CloudFront, with bounded bodies (≤64 KiB),
strict decoding, sentinel errors and `{error:{code,message}}`. Update
`docs/API-SPEC.md`, and add an ADR (local intake trust boundary + residency).

## 8. Settings UI (Mac app only; hidden in the browser)

Add a section `/settings#local-intake` next to `MeetingDetectionSettings`, using
the same `isTauri()` gating pattern:

1. **Discovered servers**: a table of host, server name, transport,
   "command found" and "disabled in host". No args, env or URLs. Per-host parse
   errors are shown as a short code. Includes a refresh button.
2. **Connectors**: a card per connector (Calendar, SFDC opportunities,
   SpecReq), each with:
   - a source picker limited to compatible `(host, server)` pairs;
   - options (calendar choice and exclusions; SFDC alias and account mappings);
   - a cadence;
   - an enable toggle, which raises a **native** confirmation dialog stating what
     is read, what goes to TTOBAK cloud and that the job runs in the background;
   - Dry run (shows counts and sample normalized titles locally, writes nothing),
     Run now, and Disable.
3. **Status**: last run, result class, counts, next run, and a "Midway expired —
   run `mwinit -o`" hint. Includes a button that opens the log folder (logs
   contain no content).
4. **AI proposals (optional, off)**: a host picker (Claude/Codex/Kiro) and a
   daily cadence. Shows a consent text naming that the chosen AI provider will
   see SFDC, calendar and SpecReq data, and the OTel/telemetry caveat. Lists the
   proposals with "open in TTOBAK to apply".

Korean UI copy, English docs. Use existing tokens and components.

## 9. Security and privacy

- **Trust boundary.** The remote SPA can call Tauri commands. New commands take
  only enum or fixed-shape arguments: connector ID, `(host, serverName)` chosen
  from the discovered list, cadence enum and option fields validated in Rust and
  again in the runner. There is no command that takes a tool name, prompt, argv,
  path or env. Enabling a connector, enabling B and installing the LaunchAgent
  each need a Rust-rendered native dialog, so remote content cannot silently
  enable background execution. Add each command to `build.rs` COMMANDS and
  `capabilities/default.json` (enforced by `command_acl_tests`).
- **No secret handling.** Server entries are read at run time, in memory, and
  passed to the child only. No env, header, args, URL, token or cookie value is
  written to disk by TTOBAK, logged, put in a plist, sent over IPC to the SPA or
  sent to the cloud. Errors and logs use codes, counts and server names. Unit
  tests assert that a sentinel secret placed in fixture env/args/headers never
  appears in any output, status file, plist or log line.
- **No config exfiltration.** The cloud never receives the server list or the
  selection (the optional status row carries connector ID and counts only).
- **Content minimization.** Calendar: runbook field set; bodies, attendees and
  join URLs stripped; private and confidential excluded. SFDC: ID, name, stage
  only. SpecReq: ID, title, status, account, bounded summary. Logs never contain titles.
  Dry-run previews stay in the local UI.
- **Least privilege.** Connectors call only named read tools. Write-capable tools
  of source servers are never invoked by A and are excluded from B's tool
  allowlist. `aws-outlook-mcp__enable_write_tools` is never called.
- **Local storage.** `~/.ttobak/intake/` uses mode 0700/0600 with the same
  ownership and symlink checks as `~/.ttobak/tokens/`. It holds the journal,
  ledger and checkpoints, which are private calendar metadata. Retention: prune
  journals after acknowledgment; the ledger lives as long as the source.
- **Consent.** Explicit, per connector, revocable. Disabling removes the
  LaunchAgent (if no connector remains) and keeps the local ledger, with a
  separate "forget local data" action. B consent separately names the AI
  provider, telemetry and cost.
- **Auth expiry.** The runner never prompts or opens a browser in background
  mode. On a TTOBAK refresh failure it reports `TTOBAK_LOGIN_REQUIRED`; on a
  Midway failure, `AUTH_EXPIRED_MIDWAY`. It backs off until the next scheduled
  run and sends one native notification per state change.
- **No new public origin.** All writes go through the existing CloudFront API
  with the user's own token.
- **Shared visibility.** Calendar stays private. Opportunities and SpecReq rows
  are account-visible by design, so the user must understand that the stored
  fields (ID, name, stage) become visible to account members.

## 10. Risks

| Risk | Mitigation |
|---|---|
| MCP output is prose, or its schema changes | Phase 0 shape capture; `tools/list` schema check; strict parser; `SCHEMA_CHANGED` with no writes; B-as-parser fallback only with schema validation |
| No stable occurrence ID from the Outlook MCPs | Choose the server that has one; otherwise calendar intake is blocked (the runbook forbids title/time identity). `outlook-mac` (Codex) is a third candidate. |
| Midway expiry causes long silent gaps | Status badge plus notification; the account page freshness row |
| Duplicate calendar sources (both Outlook servers) | One calendar source per mailbox, enforced in the UI |
| Background item confusion or uninstall leftovers | Settings shows the LaunchAgent state; "Disable all" unloads and deletes it |
| B cost or provider leakage | Off by default, daily, budget cap, consent, no TTOBAK tools |

## 11. Decisions and open questions

Decided by the user on 2026-11-09:

1. **Calendar source: `aws-outlook-mcp`.** It is the only calendar connector in
   v1. One local capture was allowed; see §3.1 for the results and remaining
   gaps.
2. **SFDC scope: mapped accounts only.** Sync opportunities only for SFDC
   accounts that a member has mapped to a TTOBAK account. Only the ID, name and
   stage go to the cloud. `lastStageChangeDate` is not stored, and unmapped
   accounts get no proposals from this connector.
   - Designer's call: TTOBAK may record when it *observed* a stage change. This
     is TTOBAK's own timestamp, not the SFDC field.
3. **SpecReq: ID, title, status, account and a summary.** The summary is bounded
   (designer's call: at most 1,000 UTF-8 bytes) and the full request body is
   not stored. The summary becomes visible to account members, and the enable
   dialog says so.
   - Blocker: spec-req-mcp fails to connect until the user runs `mwinit -o`.
     The source of the summary (a provider field or a local excerpt) depends on
     the tool schema found in Phase 0.

Still open. These affect later phases, not Phase 1:

4. Is the disagreement acceptable: AI (B) produces proposals only and never
   writes, and internal docs/Highspot get no scheduled intake?
5. For B, which host and provider should be used, and is the configured OTel
   export acceptable for runs that contain SFDC and calendar data?
6. Is a launchd background item acceptable, or should jobs run only while the
   app is open?
