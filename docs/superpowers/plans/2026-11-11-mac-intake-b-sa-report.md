# SA performance report via the owner's Claude CLI (Part B) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

- Status: historical implementation plan (2026-11-11), approved by the owner.

**Goal:** The Mac app generates a period report by running the owner's `claude` CLI headless with the unchanged `sa-performance-report` skill. The validated JSON is stored owner-private in TTOBAK and rendered in a report tab (`reportTab`) on `/performance`.

**Architecture:** The Go backend adds `PERFREPORT#{periodKey}` rows with strict schema validation. The adapter adds `ttobak-mcp report run`. It builds a temporary MCP config, a read-only allowlist and the prompt, spawns `claude -p`, validates the final JSON and uploads it. Rust spawns that command through the Part A runner. The web renders the schema natively.

**Tech Stack:** Go 1.27.3 (chi, DynamoDB expression builder), TypeScript (mcp-server), Rust/Tauri 2, Next.js 16.

**Spec:** `docs/superpowers/specs/2026-11-11-mac-intake-settings-and-sa-report-design.md` (Part B, Designer's calls, Residency).

**Depends on:** Part A merged. Task 3 reuses `intake_runner.rs` spawn, progress events and cancel.

## Global Constraints

- Exact Korean UI copy lives in `docs/superpowers/plans/2026-11-11-mac-intake-ui-copy.json`; backticked camelCase names in this plan are its keys.

- Schema `schemaVersion: 1`; at most 256 KB encoded; at most 40 sections and 200 actions; text fields plain text.
- The server rejects any `<` followed by a letter or `/`, any `](`, and any `slack.com` substring in any string. There are no URL fields.
- Report content may hold exact opportunity amounts, topic summaries of customer questions (never originals), and requester/colleague names. It must not hold mail or calendar bodies, DM content or names, candidates, 1:1 partners, HR data, or Slack permalinks.
- Owner-private: `PK=USER#{id}`, `SK=PERFREPORT#{periodKey}`. Never readable by others, and never in KB or QA. TTL `pendingShareExpiresAt` = Jan 1 of (period end year + 3) 00:00 UTC, which is two full years after the end year.
- Period keys: `^W\d{4}-(0[1-9]|[1-4]\d|5[0-3])$`, `^Q\d{4}-[1-4]$`, `^Y\d{4}$`, `^C\d{4}-\d{2}-\d{2}_\d{4}-\d{2}-\d{2}$` (start ≤ end, at most 400 days).
- The run never writes to SFDC, Slack, Outlook or SpecReq. The CLI enforces this through `--permission-mode dontAsk`, `--allowedTools` and `--strict-mcp-config`, not the prompt alone.
- TTOBAK never stores a model key. The CLI's own configuration decides auth.
- Responses use `Cache-Control: private, no-store` like the other performance routes.

## Review Focus

1. The user's `~/.claude/settings.json` allows `mcp__aws-sentral-mcp__*`. The run must still deny SFDC writes → load no user permission rules (`--setting-sources` without `user`) and pass an explicit allowlist. (Task 2 test `run_args_exclude_user_settings_and_writes`.)
2. The model returns prose around the JSON or a truncated object → the run fails with `REPORT_INVALID` and uploads nothing. (Task 2 test `invalid_output_is_not_uploaded`.)
3. A text field contains `<script>` or a Slack permalink → rejected by both the adapter and the server. (Task 1 `TestValidateReportRejectsMarkupAndSlackLinks`, Task 2.)
4. Regenerating a period while the old report exists → the old report stays visible until the new PUT succeeds. (Task 1 `TestPutReportReplaces`.)
5. `claude` is not installed or not logged in → `CLAUDE_CLI_MISSING` / `CLAUDE_CLI_AUTH`, with the exact terminal step in Korean. (Task 2, Task 4 labels.)

---

### Task 1: Backend report rows and routes, ADR

**Files:**
- Modify: `backend/internal/model/performance.go` (prefix, types, key and validation)
- Modify: `backend/internal/repository/performance.go` (+ interface), `backend/internal/service/performance.go`, `backend/internal/handler/performance.go`, `backend/cmd/api/main.go`
- Test: the matching `*_test.go` files
- Create: `docs/decisions/ADR-074-sa-performance-report.md` (check `docs/decisions/` and open PRs for the next free number first), and update `docs/decisions/README.md`, `docs/API-SPEC.md` and ADR-068 (a "See ADR-074" line)

**Interfaces:**
- Produces:
  - `model.PrefixPerformanceReport = "PERFREPORT#"`
  - `model.PerformanceReport{ SchemaVersion int; PeriodKey string; Period {Start, End, Label string}; Alias, GeneratedAt, Model string; Partial bool; MissingSources []string; Highlights {Projects, Initiatives, Channels, Programs []string}; Summary string; Ledger []{Label, Value, Unit, Note string}; Sections []{ID, Title, Lede string; Paragraphs []string; Tables []{Columns []string; Rows [][]string}; Cards []{Title, Body string}}; Actions []{Severity, Title, Detail, Source string}; DataBasis []{Source, Query, Exclusions, Estimates string} }`. Table cells are strings; the adapter stringifies numbers.
  - `model.ValidatePerformanceReport(key string, r *PerformanceReport) error`, wrapping `service.ErrInvalidInput`-style sentinels the existing performance validation uses. Per-field caps: summary and paragraph at most 4000 chars, titles at most 200, table at most 50 columns × 500 rows, highlights at most 20 per list.
  - Routes:
    - `PUT /api/performance/reports/{periodKey}`, body = report, 256 KB body cap; returns `{periodKey, generatedAt}`;
    - `GET /api/performance/reports` lists `{periodKey, label, generatedAt, partial}`, newest first, paginated through the repository;
    - `GET /api/performance/reports/{periodKey}` returns the full report;
    - `DELETE /api/performance/reports/{periodKey}`.

- [ ] **Step 1: Write failing tests** `TestPerformanceReportKeyPatterns` (each key form, a reversed custom range, a range over 400 days), `TestValidateReportRejectsMarkupAndSlackLinks`, `TestValidateReportCaps`, `TestPutReportReplaces` (conditional-free overwrite is fine for an owner singleton; assert TTL value), `TestGetReportHidesExpired`, `TestReportRoutesOwnerOnly` (a different user ID gets 404), `TestReportResponsesNoStore`.
- [ ] **Step 2: Run** `(cd backend && go test ./... -count=1)` with the scratchpad toolchain (`GOFLAGS=-mod=mod GOPROXY=off GOSUMDB=off`) — FAIL.
- [ ] **Step 3: Implement.** The repository uses the expression builder. Listing queries `begins_with(SK, "PERFREPORT#")` and projects summary fields only.
- [ ] **Step 4: Write the ADR.**
  - Record the fields allowed and forbidden, and that absence of forbidden content rests on the skill rules, the prompt and server pattern checks; the owner reviews the report.
  - The temporary MCP config file on the Mac is 0600 and deleted after the run. It is the one place server config is copied (ADR-003 otherwise references). Record that as an accepted exception.
  - Run `go test`, `go vet`, the arm64 build and `python3 scripts/docs/check_docs.py` — PASS.
- [ ] **Step 5: Commit** `feat(performance): owner-private SA performance reports`.

### Task 2: Adapter `ttobak-mcp report run`

**Files:**
- Create: `mcp-server/src/report/run.ts`, `mcp-server/src/report/schema.ts` (mirror of the Go validation; one JSON Schema object also passed to `--json-schema`), `mcp-server/src/report/claude.ts` (binary lookup, args, stream parsing), `mcp-server/src/report/prompt.ts`, `mcp-server/src/report/tools.ts` (read-only allowlist)
- Modify: `mcp-server/src/index.ts` (dispatch `report`), `mcp-server/src/api.ts` (`putPerformanceReport`, `listPerformanceReports`, `getPerformanceReport`)
- Test: `mcp-server/test/report-*.test.mjs` with a fake `claude` script fixture that prints stream-json

**Interfaces:**
- Consumes: Task 1 routes; `signIn` from `intake/run.ts` (export it if needed); `RUN_IN_PROGRESS` lock from Part A (a separate `report.lock`).
- Produces CLI: `ttobak-mcp report run --period week|quarter|year|custom [--start YYYY-MM-DD --end YYYY-MM-DD] [--highlights-file <path>] [--noninteractive]`. It prints one JSON line, `{ok, periodKey, partial, missingSources}`, or exits 2 with `{ok:false, code}`. Codes: `CLAUDE_CLI_MISSING`, `CLAUDE_CLI_AUTH`, `SKILL_MISSING`, `REPORT_INVALID`, `REPORT_NO_SFDC`, `REPORT_TIMEOUT`, `RUN_IN_PROGRESS`, `TTOBAK_LOGIN_REQUIRED`, `TTOBAK_UNAVAILABLE`.
- Stderr: Korean progress lines, plus `toolCallLine` per tool use (names only, never inputs or results).

- [ ] **Step 1: Write failing tests:**
  - `period_keys_from_dates` (ISO week at year boundaries, quarter, year);
  - `run_args_exclude_user_settings_and_writes`: args include `-p`, `--output-format stream-json`, `--permission-mode dontAsk`, `--strict-mcp-config`, `--mcp-config <tmp>`, `--setting-sources` without `user`, `--json-schema`, and `--allowedTools` containing only names from `tools.ts`. No allowed name matches `/(create|update|add|remove|delete|post|send|inject|upsert|submit|enable_write|edit|archive|cancel|schedule)/`, and no Bash rule allows `mcp_stdio.py`;
  - `mcp_config_has_only_needed_servers_and_mode_0600`, and `tmp_dir_removed_after_run` on success, failure and timeout;
  - `customers_file_generated_when_missing`: built from `api.performanceSources()` assignments into the run's temp dir. The skill dir is untouched;
  - `invalid_output_is_not_uploaded`;
  - `markup_rejected_before_upload`;
  - `no_sfdc_data_fails_with_REPORT_NO_SFDC`;
  - `partial_report_lists_missing_servers`;
  - `progress_never_prints_tool_inputs`.
- [ ] **Step 2: Run** `(cd mcp-server && npm run build && node --test test/report-*.test.mjs)` — FAIL.
- [ ] **Step 3: Implement.**
  - Find `claude`: `$TTOBAK_CLAUDE_BIN`, then `~/.local/bin/claude`, `/opt/homebrew/bin/claude`, `/usr/local/bin/claude`, then `~/.claude/local/claude`.
  - Find the skill dir: `~/.claude/skills/sa-performance-report`, then plugin caches matching `*/skills/sa-performance-report/SKILL.md`. The newest mtime wins; otherwise `SKILL_MISSING`.
  - Build the temp MCP config from `~/.claude.json` top-level `mcpServers`, copying only `aws-sentral-mcp`, `aws-outlook-mcp`, `slack-mcp`, `spec-req-mcp`, `aws-tfc-hub-mcp` and `builder-mcp` when present.
  - The allowlist in `tools.ts` holds the read tools the skill's `references/data-sources.md` names, as `mcp__<server>__<tool>`. It also allows `Read`, `Glob`, `Grep`, `Agent`, `Write(<tmp>/**)`, and `Bash(python3 -I <skill>/scripts/calendar_count.py:*)`, `…/extract_tool_json.py:*` and `…/slack_threads.py:*`.
  - Use `--add-dir <skill>` and run with `cwd` = the temp dir.
  - The prompt is in `prompt.ts`, in Korean. It tells the model to read `<skill>/SKILL.md` and follow its collection, counting rules, cross-checks and "do not include" (`skillSections.doNotInclude`), but to return the JSON object instead of steps 5–6. It also passes alias, period, highlights and the customers file path, says never to ask questions, and says to record gaps in `dataBasis` and `missingSources`.
  - Spawn `claude` with the argv array and a minimal env: `HOME`, `PATH`, `LANG`, and the model-auth keys.
    - Model-auth keys come from the user's `~/.claude/settings.json` `env`. User settings are not loaded as a setting source, so these keys are carried over explicitly. Only `CLAUDE_CODE_USE_BEDROCK`, `CLAUDE_CODE_USE_VERTEX`, `AWS_PROFILE`, `AWS_REGION`, `ANTHROPIC_BASE_URL`, `ANTHROPIC_MODEL` and `ANTHROPIC_DEFAULT_{OPUS,SONNET,HAIKU}_MODEL` are carried.
    - The app's report settings, passed in via `TTOBAK_REPORT_USE_BEDROCK` and `TTOBAK_REPORT_AWS_PROFILE`, override those keys.
    - Test `auth_env_is_allowlisted` asserts that no other variable is passed.
    - Timeout 45 minutes.
  - Take the `result` event's structured output, validate it with `schema.ts`, sign in and PUT.
  - Map an auth failure in the stream (`authentication_failed`, "not logged in", or an API 401) to `CLAUDE_CLI_AUTH`.
- [ ] **Step 4: Run** `(cd mcp-server && npm test)` and regenerate the bundle as in Part A Task 1 — PASS.
- [ ] **Step 5: Commit** `feat(mcp-report): generate the SA report with the owner's Claude CLI`.

### Task 3: Rust report command

**Files:** `mac-app/src-tauri/src/intake_runner.rs` (or `report_runner.rs` if the runner exposes a reusable spawn), `intake_plan.rs` (argv), `build.rs`, `capabilities/default.json`, `lib.rs`.

**Interfaces:**
- Consumes: Part A spawn, events and settings.
- Produces:
  - `report_generate(period: "week"|"quarter"|"year"|"custom", start: Option<String>, end: Option<String>, highlights: Highlights)`. Highlights are written to a 0600 temp file passed as `--highlights-file`; each list holds at most 20 items of 200 chars.
  - `report_cancel()` and `report_settings_get/set { useBedrock: bool, awsProfile: Option<String> }`. The profile is validated with `aws_profiles::profile_names`.
  - Events `report://progress` and `report://finished`, shaped like Part A's.
- Ruling: report runs and intake runs may overlap. They use different locks.

- [ ] **Step 1: Write failing tests** `report_argv_maps_period_and_dates` (rejects bad dates and end < start), `highlights_are_capped`, `bedrock_env_only_when_enabled`, `aws_profile_must_exist`.
- [ ] **Step 2–4:** Implement; run the full Rust checks — PASS.
- [ ] **Step 5: Commit** `feat(mac-report): generate SA reports from the app`.

### Task 4: Web report tab

**Files:**
- Create: `frontend/src/lib/performanceReport.ts` + tests (`reportSummaryRows`, `reportCodeLabel`, `periodLabel`), `frontend/src/components/performance/ReportList.tsx`, `ReportView.tsx`, `ReportGenerateDialog.tsx`
- Modify: `frontend/src/app/performance/page.tsx` (tabs `dashboardTab` / `reportTab`), `frontend/src/lib/tauri.ts`, the performance API client in `frontend/src/lib/` (where `/api/performance/*` calls already live)

**Interfaces:**
- Consumes: Task 1 routes; Task 3 commands and events.
- Rendering: every string goes through React text nodes only. Sections follow the stored order; `actions` are grouped hi/mid/lo; a `partial` badge shows the missing sources; `dataBasis` is shown collapsed at the end.
- The dialog has the period (`periodOptions`) and four highlight lists, prefilled from the newest report of the same period type. It shows elapsed time, progress lines and [Cancel] (`cancel`). It is Mac app only; a browser shows the list and the view, without generation.
- `reportCodeLabel` gives Korean copy from `2026-11-11-mac-intake-ui-copy.json`: `CLAUDE_CLI_MISSING` → `claudeCliMissing`, `CLAUDE_CLI_AUTH` → `claudeCliAuth`, `SKILL_MISSING` → `skillMissing`, `REPORT_NO_SFDC` → `reportNoSfdc`, `REPORT_INVALID` → `reportInvalid`.

- [ ] **Step 1: Write failing unit tests** for the helpers.
- [ ] **Step 2–4:** Implement; run `(cd frontend && npm run lint && npm run test:unit && npm run build)` — PASS.
- [ ] **Step 5: Commit** `feat(web): SA performance report tab`.

### Task 5: Docs and manual acceptance record

**Files:** `docs/runbooks/local-mcp-intake.md` (a report section: prerequisites, auth modes, troubleshooting codes), `docs/DESIGN-SPEC.md`, `docs/architecture.md` (Mac → owner CLI → TTOBAK flow).

- [ ] **Step 1:** Edit; run `python3 scripts/docs/check_docs.py` — PASS.
- [ ] **Step 2: Commit** `docs: SA performance report`.

## Delivery

PR B1 = Task 1 (backend + ADR). PR B2 = Task 2 (adapter; bundle excluded from the size check). PR B3 = Tasks 3–5. Each PR opens against main after the previous one merges. Manual acceptance on the owner's Mac: a weekly report for this week, then a year report; check the stored JSON for forbidden content.
