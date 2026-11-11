# Mac app: local intake settings and the SA performance report

- Status: historical design record; approved by the owner on 2026-11-11.
- UI copy: backticked camelCase names are keys in
  `docs/superpowers/plans/2026-11-11-mac-intake-ui-copy.json`.
- Follows: the [SA performance dashboard design](2026-11-10-sa-performance-dashboard-design.md)
  and `ttobak-mcp intake setup|run|schedule` (PR #824).
- Decisions touched: ADR-003 (local MCP intake), ADR-068 (SA performance data
  residency), ADR-017 (Mac settings surface precedent). Part B needs a new ADR.

## Intent

The owner, an AWS SA, wants the SA Activity dashboard to stay fresh without
typing commands, and wants the full written performance report that the
`sa-performance-report` skill produces today in Claude Code, inside TTOBAK.

On 2026-11-11 the dashboard looked empty. The cause was that the Mac's
`~/.ttobak/server.mjs` was an old bundle and SFDC had never been collected.
That is the failure this design removes: collection and its state must be
visible and one click away.

Two parts, delivered in order:

- **Part A, local intake in the Mac app.** Settings, schedule, per-source
  status, a one-click setup and a [Collect now] (`collectNow`) button.
- **Part B, the SA performance report.** The Mac app runs the owner's own
  Claude Code CLI headless with the `sa-performance-report` skill, and stores
  the result in TTOBAK as an owner-private report rendered on `/performance`.

## Owner decisions (2026-11-11)

- The button lives both in Settings and on the SA Activity dashboard.
- Setup is also a button in the app, not only a terminal command.
- A run missed while the app was closed runs at launch. The LaunchAgent stays
  optional.
- The report reuses the skill as is ("as is"), run through the Claude CLI
  (Claude Agent SDK style) rather than Strands.
- Auth is whatever the user's own Claude Code CLI is configured with. Each
  user has Bedrock or an Enterprise login; TTOBAK never holds a model key.
- Earlier, still binding: mail bodies never stored, titles OK; calendar bodies
  never stored; cloud processing OK; the self-evaluation is visible only to
  the owner (ADR-068).

## Part A: local intake in the Mac app

### Goals

1. **Settings section** `sectionTitle`, shown only inside the Mac app (same
   pattern as `MeetingDetectionSettings`), with:
   - state: not set up / set up, the adapter version found, and the selected
     connectors with server and host;
   - schedule: off, hourly, daily (time), twice daily (two times) (`scheduleOptions`);
   - per source (calendar, SpecReq tasks, SFDC assignments, SFDC tasks): last
     run time, last success time, result code in Korean, and counts;
   - buttons: [Set up] (`connectSetup`) (setup), [Collect now] (`collectNow`) (run);
   - the last run's Korean progress lines while a run is going.
2. **Dashboard control** on `/performance`: next to the data-freshness line,
   [Collect now] (`collectNow`) with the same states. On success the page reloads its data.
   In a plain browser it shows the freshness only, plus `browserHint`.
3. **Setup button** runs `intake setup --noninteractive` with:
   - a consent checkbox for private calendar items, which maps to
     `--accept-unknown-sensitivity`. Stored consent is reused as the CLI
     already does;
   - per-connector skip toggles, which map to `--skip`;
   - no `--schedule`; the app owns scheduling.
   The JSON result is shown per connector (`connectorSelected` / `connectorSkipped` + reason).
4. **Run button and schedule** run `intake run --noninteractive`.
5. **Catch-up.** At launch, and on wake from sleep, if the most recent
   scheduled slot passed with no successful run since, run once.
6. **Adapter install/update.** If `~/.ttobak/server.mjs` is missing or older
   than the bundle served at `/mcp/ttobak-mcp.mjs`, the section offers
   [Update] (`update`). It downloads over HTTPS from the app's own origin, writes
   atomically, and keeps one `.bak`. This is the exact step that was missing
   on 2026-11-11.

### Behavior and errors

- `AUTH_EXPIRED_MIDWAY`: show `authExpiredMidway` A scheduled run retries at the
  next slot only. There are no notifications or retries in a loop.
- TTOBAK login missing or expired for the adapter: `--noninteractive` never
  opens a browser. The section shows [TTOBAK login] (`ttobakLogin`), which runs the
  adapter's login once on that click (a user action, so a browser may open).
- `PARTIAL_FAILURE` shows which source failed; others show their result.
- Only one intake process at a time: the app holds a lock file under
  `~/.ttobak/intake/`. A second request while running returns `runInProgress`. If the LaunchAgent is installed and the app schedule is on, the
  section warns and offers to turn the LaunchAgent off (`intake schedule
  off`).
- A run has a timeout (builder's value, at least 15 minutes) and is killed
  with its process group on timeout or app quit.

### Constraints

- **Process spawn.** Rust spawns with argv, never a shell, as ADR-003 requires.
  The node path is resolved once from a fixed candidate list (Homebrew,
  `/usr/local/bin`, nvm current) or a path the user picks, and stored in app
  settings. GUI apps do not inherit the shell PATH.
- **No secrets cross IPC.** The WebView gets only the CLI's JSON results and
  the Korean stderr progress lines. Host command, args and env values are
  never sent, as the CLI already guarantees.
- **New Tauri commands** must be listed in `build.rs` `COMMANDS` and granted
  in `capabilities/default.json` `remote` (the window loads the remote SPA).
  The remote SPA cannot pass arbitrary argv: commands take typed options only
  (schedule enum, times, consent bool, skip set).
- The schedule is stored by the app (app settings file), not by the web.
- Status comes from `~/.ttobak/intake/status.json` and the last run's JSON,
  read by Rust and returned as a typed object.

## Part B: the SA performance report

### Goals

1. **Generate.** A [Make report] (`makeReport`) button on `/performance` (Mac app only)
   with a period: this week, this quarter, this year, or custom start/end (`periodOptions`). The owner said
   reports are for weekly review, quarterly evaluation and year-end
   evaluation.
2. **Run the skill as is.** The Mac app spawns the owner's `claude` CLI in
   print (headless) mode with:
   - the `sa-performance-report` skill installed in the owner's Claude
     config, unchanged;
   - the owner's own MCP servers from their Claude config;
   - a prompt that names the alias, the period and the highlights (below),
     and says: follow the skill's collection, counting rules, cross-checks and
     "do not include" (`skillSections.doNotInclude`), but instead of its steps 5 and 6 (HTML template and
     Artifact), return one JSON object matching the TTOBAK report schema;
   - a read-only tool allowlist: the read tools the skill's
     `references/data-sources.md` uses, Read, Grep/Glob, and Bash limited to
     `python3 -I <skill>/scripts/*`. Every write tool of every server is
     denied (for example SFDC create/update, Slack post, Outlook write
     enablement, Artifact publish).
3. **Highlights.** The skill asks the user for projects, initiatives, Slack
   channels and programs to feature. Headless runs cannot ask, so the
   generate dialog has those fields, prefilled from the last report.
4. **Store.** The adapter uploads the JSON to a new owner-private route and
   the report appears in a report tab (`reportTab`) on `/performance`, newest first, one
   per period key. Regenerating a period replaces it, keeping the previous
   one until the new upload succeeds.
5. **Render.** The web renders the schema natively with the dashboard's
   components: header summary and ledger tiles, the skill's sections in the
   order of `references/report-structure.md`, actions (`skillSections.actions`) with severity, and
   data basis (`skillSections.dataBasis`). No model HTML is rendered.
6. **Progress.** While running, the button shows elapsed time and the CLI's
   streamed step lines (tool names and counts only). It can be cancelled.

### Report schema (contract)

Plain JSON, versioned `schemaVersion: 1`, bounded: at most 256 KB encoded,
text fields plain text (no HTML, no Markdown links), at most 40 sections, at
most 200 action items. Fields:

- `period {start, end, label}`, `alias`, `generatedAt`, `model` (as reported by
  the CLI);
- `summary` (one paragraph), `ledger[]` of `{label, value, unit, note?}`;
- `sections[]` of `{id, title, lede, paragraphs[], tables[]?, cards[]?}`,
  where `tables` are `{columns[], rows[][]}` of strings/numbers;
- `actions[]` of `{severity: hi|mid|lo, title, detail, source}`;
- `dataBasis[]` of `{source, query, exclusions, estimates?}`.

The server validates the schema and size. The web treats every string as
text.

### Auth

- TTOBAK never stores a model key and never calls a model for this. The CLI
  uses the owner's configuration: Bedrock, an Enterprise or Team login, or an
  API key, exactly as the owner's terminal `claude` would.
- The app spawns the CLI with a minimal environment (HOME, PATH to node and
  the claude binary, LANG). It does not import shell profile variables. On
  this Mac the shell sets Bedrock variables and the `claude` shell function
  removes them; spawning the binary without profile env matches that
  function. Settings show which mode the CLI reports, and let the user add
  `CLAUDE_CODE_USE_BEDROCK=1` and `AWS_PROFILE` explicitly if they want
  Bedrock.
- If the CLI is missing or not logged in, show the exact terminal step
  (`claude` then `/login`) instead of trying to log in from the app.

### Residency (new ADR, amends ADR-068)

- The stored report is model-composed text derived from SFDC, SpecReq, TFC
  Hub, calendar titles and counts, and Slack. It is owner-private
  (`USER#{id}` / `PERFREPORT#{periodKey}`), default table encryption as in
  ADR-068, TTL year+2 like goals.
- The prompt and schema forbid: mail bodies, calendar bodies, DM or group DM
  content and names, customer message originals (topic summaries only),
  candidates, 1:1 partners, HR data. The server cannot verify absence; the
  ADR records that this rests on the skill's rules and the prompt, and that
  the owner reviews the report.
- Owner decision (2026-11-11) on report fields, which widens ADR-068 for
  this report only:
  - **allowed:** exact opportunity amounts; one- or two-line topic summaries
    of customer questions from shared channels, never the original text;
    names of SpecReq requesters and of colleagues (SA, AM) on the work;
  - **not stored:** Slack thread permalinks. The report has no URL fields,
    and the server rejects `slack.com` links in any text field.
  - The dashboard rows keep the ADR-068 limits unchanged.
- Raw MCP results stay on the Mac in the CLI's working directory, which the
  runner deletes after the run.
- Never visible to other users, including Account members. Not indexed into
  the KB or QA.

## Non-goals

- Running anything model-based on the TTOBAK servers for this.
- Collecting new raw sources into TTOBAK beyond what Part A already syncs.
- Editing the skill or forking it into TTOBAK.
- Sharing reports with a team (owner said: later, partially).
- Windows or a non-Mac runner.

## Anti-goals

- The report silently invents numbers. Numbers come from the skill's
  scripts and tool results; the prompt says so and `dataBasis` must cite them.
- Any write to SFDC, Slack, Outlook or SpecReq during a report run.
- A background run opening a browser login.
- The remote SPA being able to make the app run an arbitrary command.

## Designer's calls (2026-11-11, after the builder check)

- **Read-only enforcement.** The CLI runs with an explicit allowlist and
  `dontAsk`-style denial of everything else, so the rule is enforced by the
  CLI, not only by the prompt. `scripts/mcp_stdio.py` is not allowed: it can
  start any server and call any tool, and a headless run loads every
  configured server at start, so it is not needed. Sub-agents (the Agent
  tool) are allowed because the skill delegates Outlook and Slack to them;
  they run under the same session permissions. The allowlist test asserts
  that no write tool and no `mcp_stdio.py` invocation is permitted.
- **Missing inputs.** A run never stops to ask.
  - If `config/customers.<alias>.json` is missing, the runner writes a
    temporary mapping in its working directory from the synced SFDC
    assignments and TTOBAK Account mappings. It points the prompt at that
    file and leaves the skill directory untouched.
  - A highlighted project not found in the data becomes an `actions` item.
  - A server that does not connect becomes a `dataBasis` gap.
  - The report is stored with `partial: true` and the missing sources listed.
  - A run with no SFDC data at all fails instead of storing.
- **Period keys.** `W2026-46` (ISO week), `Q2026-4`, `Y2026`, custom
  `C2026-03-01_2026-06-30`. Keys accumulate. Regenerating the same key
  replaces it, for example "this year" generated in November and again in
  December. TTL is two years after the period's end year.
- **Spawn environment.**
  - PATH is the login shell's PATH, captured once by running the user's
    login shell for `PATH` only and stored in app settings. The user can edit
    it. No other profile variable is imported.
  - Each MCP server's own `env` from the Claude config still applies, because
    the CLI passes it.
  - The settings section shows the servers the CLI reports as connected
    before a run.
- **Catch-up.** It works per slot. A slot is satisfied when a run finished
  without a fatal code. `PARTIAL_FAILURE` satisfies it, and the failed
  source is shown and retried at the next slot. `AUTH_EXPIRED_MIDWAY` does
  not satisfy it. A launch or wake retries an unsatisfied slot at most once.
- **Highlights** are part of schema v1:
  `highlights {projects[], initiatives[], channels[], programs[]}`. The
  dialog prefills them from the newest report of the same period type.

## Left to the builder

- UI composition of the settings section, the dashboard control and the
  report tab.
- Whether Part B uses `claude -p --output-format stream-json` directly or the
  Agent SDK with `pathToClaudeCodeExecutable`; both spawn the owner's CLI.
  Prefer the plain CLI if it is enough.
- How the JSON result is extracted and validated on the Mac before upload.
- Catch-up details on wake, and the run timeout value.
- Whether the adapter (`ttobak-mcp report upload`) or Rust uploads the
  report; prefer the adapter, which already holds the TTOBAK login.

## Verification

- Unit: schedule slot math and catch-up; argv building from typed options;
  lock handling; status parsing; report schema validation (server and web),
  size caps, and rejection of HTML in text fields; the tool allowlist builder
  denies write tools.
- Rust: `cargo fmt --check`, `cargo clippy -D warnings`, `cargo test`. There
  is no Mac CI; report that limit.
- Manual on the owner's Mac: setup button from a fresh `~/.ttobak/intake`,
  [Collect now] (`collectNow`) from the dashboard, an expired Midway session, a weekly report
  for this week, then a year report, and confirm in the stored report that no
  mail body, DM content or customer message original appears.
