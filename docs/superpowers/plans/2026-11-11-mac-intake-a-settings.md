# Mac local intake settings (Part A) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

- Status: historical implementation plan (2026-11-11), approved by the owner.

**Goal:** The Mac app sets up, schedules and runs `ttobak-mcp intake`, shows per-source status, and exposes [Set up] (`connectSetup`) / [Collect now] (`collectNow`) in Settings and on `/performance`.

**Architecture:** The CLI stays the only collector. It gains `intake login` and a run lock. Rust gets a pure module for schedule math and argv building, plus a runner module that spawns `node ~/.ttobak/server.mjs intake ...` (argv, no shell), streams stderr lines as events, runs the schedule and catch-up, and updates the adapter. The web calls typed Tauri commands.

**Tech Stack:** TypeScript (mcp-server, node:test), Rust/Tauri 2 (tokio), Next.js 16/React 19.

**Spec:** `docs/superpowers/specs/2026-11-11-mac-intake-settings-and-sa-report-design.md` (Part A, Designer's calls: spawn environment, catch-up).

## Global Constraints

- Exact Korean UI copy lives in `docs/superpowers/plans/2026-11-11-mac-intake-ui-copy.json`; backticked camelCase names in this plan are its keys.

- Spawn with argv, never a shell; never print or forward host command/args/env values (ADR-065).
- `--noninteractive` for every scheduled or button run, except the explicit [TTOBAK login] (`ttobakLogin`) click.
- The remote SPA passes typed options only, never argv strings or paths.
- Every new command is in `mac-app/src-tauri/build.rs` `COMMANDS` and granted in `capabilities/default.json` `remote`.
- Korean UI copy; English code and docs. Literal UTF-8.
- Adapter download only from `https://ttobak.atomai.click/mcp/ttobak-mcp.mjs`, atomic write, one `server.mjs.bak`.
- Rust checks: `cargo fmt --check && cargo clippy --all-targets -- -D warnings && cargo test`. No Mac CI; say so in the PR.

## Review Focus

1. Node not found because the GUI app has no shell PATH → state `nodeMissing` with a path picker, not a silent failure. (Task 2 test `resolve_node_prefers_stored_then_candidates`.)
2. App launched hours after a missed daily slot → exactly one catch-up run; launching twice does not run twice. (Task 2 test `catch_up_runs_once_per_slot`.)
3. LaunchAgent and app schedule fire together → the second process exits `RUN_IN_PROGRESS`, the UI shows `runInProgress`. (Task 1 test `run_lock_rejects_second_run`, Task 3 handling.)
4. App quit mid-run → the child process group is killed; the lock is stale-detected next time by PID. (Task 1 test `stale_lock_is_reclaimed`.)
5. Midway expired at 08:30 → slot unsatisfied, one retry at next launch/wake, no loop. (Task 2 test `auth_expired_does_not_satisfy_slot`.)

---

### Task 1: CLI `intake login` and run lock

**Files:**
- Modify: `mcp-server/src/intake/run.ts` (USAGE, dispatch, `runConnectors`, setup entry)
- Create: `mcp-server/src/intake/lock.ts`
- Modify: `mcp-server/src/intake/errors.ts` (add `RUN_IN_PROGRESS`)
- Test: `mcp-server/test/intake-lock.test.mjs`, extend `mcp-server/test/intake-setup.test.mjs`
- Docs: `docs/runbooks/local-mcp-intake.md`

**Interfaces:**
- Produces: `acquireRunLock(dir: string, now?: () => number): Promise<{ release(): Promise<void> }>` throwing `IntakeError('RUN_IN_PROGRESS')`; lock file `~/.ttobak/intake/run.lock` containing `{pid, startedAt}`.
- Produces: `ttobak-mcp intake login` prints `{ok:true, email}` after an interactive Cognito login (browser allowed); exits 2 `{ok:false, code}` otherwise.

- [ ] **Step 1: Write failing tests** `run_lock_rejects_second_run` (second acquire throws `RUN_IN_PROGRESS`), `stale_lock_is_reclaimed` (lock with a dead PID is replaced), `released_lock_allows_next_run`, `setup_and_run_hold_the_lock` (a held lock makes `intake run` and `intake setup` exit 2 with `RUN_IN_PROGRESS` and touch no state), `login_prints_email` using the existing fake auth factory.
- [ ] **Step 2: Run** `(cd mcp-server && npm run build && node --test test/intake-lock.test.mjs test/intake-setup.test.mjs)` — expect FAIL.
- [ ] **Step 3: Implement.** Create with `open(path, 'wx')`; on `EEXIST` read the PID and `process.kill(pid, 0)`; `ESRCH` means stale → replace. `run`, `setup`, `calendar`, `sfdc` acquire it; `status`, `hosts`, `schedule`, `login` do not. `login` calls `signIn` with `noninteractive=false` and no dry run.
- [ ] **Step 4: Run** `(cd mcp-server && npm test)` — intake tests pass (known env failures listed in the runbook are allowed). Regenerate the bundle: `npm run bundle && cp dist/ttobak-mcp.mjs ../frontend/public/mcp/ttobak-mcp.mjs && npm run test:bundle`.
- [ ] **Step 5: Commit** `feat(mcp-intake): add intake login and a run lock`.

### Task 2: Rust pure intake logic

**Files:**
- Create: `mac-app/src-tauri/src/intake_plan.rs` (no Tauri imports)
- Modify: `mac-app/src-tauri/src/lib.rs` (`mod intake_plan;`)

**Interfaces:**
- Produces:
  - `enum Schedule { Off, Hourly, Daily(HhMm), TwiceDaily(HhMm, HhMm) }` with serde `{kind:"off"|"hourly"|"daily"|"twiceDaily", times:["HH:MM",...]}`; `Schedule::parse_times` rejects invalid or duplicate times.
  - `fn latest_slot_at_or_before(schedule: &Schedule, now_local: NaiveLikeMinutes) -> Option<i64>` and `fn next_slot_after(...) -> Option<i64>`, epoch minutes in local time. Use the local UTC offset passed in as minutes. The module adds no chrono dependency if `time` math by hand stays small; otherwise use `chrono` (builder's call; note it in the PR).
  - `struct SlotState { last_satisfied_slot: Option<i64>, last_attempted_slot: Option<i64> }`.
  - `fn should_catch_up(schedule, state: &SlotState, now) -> Option<i64>` returns the slot to run.
  - `fn record_result(state: &mut SlotState, slot: i64, code: Option<&str>)` where `None` or `PARTIAL_FAILURE` satisfies; `AUTH_EXPIRED_MIDWAY` and others only mark attempted.
  - `enum IntakeAction { Run, Setup { accept_private_calendar: bool, skip: BTreeSet<Connector> }, Login, Status, ScheduleOff }` and `fn argv(action: &IntakeAction, server_mjs: &Path) -> Vec<OsString>`. It always adds `--noninteractive` except for `Login`, `Status` and `ScheduleOff` (`intake schedule off`, which removes the LaunchAgent), never adds `--schedule`, and adds `--accept-unknown-sensitivity` only when the flag is true.
  - `fn resolve_node(stored: Option<&Path>, candidates: &[PathBuf], is_exec: impl Fn(&Path)->bool) -> Option<PathBuf>`; candidates are `/opt/homebrew/bin/node`, `/usr/local/bin/node`, then `~/.nvm/versions/node/*/bin/node`, newest first.

- [ ] **Step 1: Write failing tests** in the module: `daily_slot_math` (08:30 at 09:00 → today 08:30; at 08:00 → yesterday 08:30), `twice_daily_slot_math`, `hourly_slot_math`, `off_has_no_slots`, `catch_up_runs_once_per_slot`, `auth_expired_does_not_satisfy_slot` (attempted once → no second catch-up for the same slot), `partial_failure_satisfies_slot`, `argv_run_is_noninteractive`, `argv_setup_maps_consent_and_skip`, `argv_never_contains_schedule`, `resolve_node_prefers_stored_then_candidates`, `schedule_rejects_bad_times` ("24:00", "8:3", duplicates).
- [ ] **Step 2: Run** `(cd mac-app/src-tauri && cargo test intake_plan)` — FAIL.
- [ ] **Step 3: Implement** to pass.
- [ ] **Step 4: Run** the full Rust checks from Global Constraints — PASS.
- [ ] **Step 5: Commit** `feat(mac-intake): add schedule, catch-up and argv planning`.

### Task 3: Rust runner, scheduler and Tauri commands

**Files:**
- Create: `mac-app/src-tauri/src/intake_runner.rs`
- Modify: `mac-app/src-tauri/src/lib.rs` (handler list, `setup` start), `mac-app/src-tauri/build.rs`, `mac-app/src-tauri/capabilities/default.json`, `mac-app/src-tauri/src/sleep.rs` (wake hook) if it exposes one; otherwise poll wall-clock jumps every 60 s.

**Interfaces:**
- Consumes: Task 2 types; Task 1 CLI JSON and `RUN_IN_PROGRESS`.
- Produces commands (all `async`, errors as `String` codes):
  - `intake_get_state() -> IntakeState` with `{ adapter: {present, updateAvailable: Option<bool>}, nodePath: Option<String>, nodeMissing: bool, schedule, running: Option<"run"|"setup"|"login">, lastResult: Option<serde_json::Value>, status: serde_json::Value /* ~/.ttobak/intake/status.json, capped 256 KiB */, selection: Vec<{connector, server, host, disabled}>, launchAgentInstalled: bool }`. The selection exposes names only, never pathDirs or env.
  - `intake_set_schedule(schedule: Schedule)`, `intake_set_node_path(path: String)` (must be an executable file named `node`).
  - `intake_launch_agent_off()`.
  - `intake_run_now()`, `intake_setup(acceptPrivateCalendar: bool, skip: Vec<String>)`, `intake_login()`, `intake_cancel()`.
  - `intake_check_adapter()` / `intake_update_adapter()`.
  - Events: `intake://progress` `{line: String}` (stderr line, max 300 chars, 200 lines per run kept), `intake://finished` `{action, exitCode, result: Value | null, code: Option<String>}`.
- App settings: `app_config_dir()/intake.json` `{schedule, nodePath, loginPath, slotState}`, written atomically like caption settings.

- [ ] **Step 1: Write failing tests** for the testable parts: `progress_lines_are_capped`, `stdout_json_is_last_line` (parse the final JSON line, ignore anything else), `selection_view_drops_paths`, `adapter_update_requires_exact_origin` (URL constant check), `node_path_validation_rejects_non_node`.
- [ ] **Step 2: Run** `cargo test intake_runner` — FAIL.
- [ ] **Step 3: Implement.**
  - Spawn with `tokio::process::Command` in a new process group (`process_group(0)`), `env_clear()` then `HOME`, `LANG=ko_KR.UTF-8`, and `PATH` = stored login PATH. The login PATH is captured once with `$SHELL -l -c 'printf %s "$PATH"'`; this is the only shell use and runs no user input. Without it, use `/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin`.
  - Timeout 20 minutes; kill the group on timeout, cancel and app exit.
  - One run at a time in-process; the CLI lock covers other processes.
  - Scheduler: a tokio loop wakes at `next_slot_after`, and on launch and wake asks `should_catch_up`; results go through `record_result`.
  - Adapter: GET the URL with reqwest (rustls), cap at 8 MiB, require it to contain `intake setup`; compare bytes to the local file. Update writes `server.mjs.tmp`, renames the old file to `server.mjs.bak`, then renames the new one into place.
  - `launchAgentInstalled` checks `~/Library/LaunchAgents/click.atomai.ttobak.intake.plist` exists.
- [ ] **Step 4: Run** the full Rust checks — PASS. Then `(cd mac-app && npm run build:signed)` or `scripts/sign.sh` if DMG bundling fails, and note the result.
- [ ] **Step 5: Commit** `feat(mac-intake): run, schedule and update local intake from the app`.

### Task 4: Web settings section and dashboard control

**Files:**
- Modify: `frontend/src/lib/tauri.ts` (typed wrappers, `isCommandNotFound` fallback → `null`)
- Create: `frontend/src/lib/intake.ts` (pure view helpers), `frontend/src/lib/intake.test.mjs` or the project's existing unit-test layout (check `npm run test:unit`)
- Create: `frontend/src/components/IntakeSettings.tsx`, `frontend/src/components/performance/IntakeRunControl.tsx`
- Modify: `frontend/src/app/settings/page.tsx` (place next to `MeetingDetectionSettings`), `frontend/src/app/performance/page.tsx` (control next to freshness; reload data on `intake://finished` success)

**Interfaces:**
- Consumes: Task 3 commands and events.
- Produces: `intakeCodeLabel(code: string): string` (Korean copy from `2026-11-11-mac-intake-ui-copy.json`: `AUTH_EXPIRED_MIDWAY` → `authExpiredMidway`, `TTOBAK_LOGIN_REQUIRED` → `ttobakLoginRequired`, `RUN_IN_PROGRESS` → `runInProgress`, `PARTIAL_FAILURE` → `partialFailure`, `SENSITIVITY_UNKNOWN` → `sensitivityUnknown`; unknown → the code itself); `sourceRows(status, selection): Array<{source, label, lastRunAt, lastSuccessAt, result, counts}>`; `scheduleLabel(schedule)`.

- [ ] **Step 1: Write failing unit tests** for the three helpers, including unknown codes and an empty status.
- [ ] **Step 2: Run** `(cd frontend && npm run test:unit)` — FAIL.
- [ ] **Step 3: Implement** helpers and components.
  - Settings shows: adapter state + [Update] (`update`); node missing + path input; selection table; schedule select (`scheduleOptions` + time inputs); per-source rows; [Set up] (`connectSetup`) (opens a panel with the private-calendar consent checkbox and per-connector skip toggles); [Collect now] (`collectNow`); [TTOBAK login] (`ttobakLogin`) when the last code is `TTOBAK_LOGIN_REQUIRED`; a LaunchAgent warning when both are on, with a button that runs `IntakeAction::ScheduleOff` through a command `intake_launch_agent_off()` (add it to Task 3's list).
  - Progress lines appear in a collapsible log while a run is going.
  - The dashboard control uses the same wrapper. In a browser it shows only `browserHint`.
- [ ] **Step 4: Run** `(cd frontend && npm run lint && npm run test:unit && npm run build)` — PASS.
- [ ] **Step 5: Commit** `feat(web): Mac intake settings and dashboard collect button`.

### Task 5: Docs

**Files:** `docs/runbooks/local-mcp-intake.md` (Mac app section first: install/update, setup button, schedule, catch-up, lock), `docs/decisions/ADR-065-local-mcp-intake.md` (one bullet: the app owns the schedule; the LaunchAgent stays optional; one lock across both), `docs/DESIGN-SPEC.md` (settings section, dashboard control), `mac-app/` module docs if any list commands.

- [ ] **Step 1:** Edit; run `python3 scripts/docs/check_docs.py` — PASS.
- [ ] **Step 2: Commit** `docs: Mac app intake settings`.

## Delivery

Two PRs to keep each under the 95,000-byte review cap (bundle excluded): PR A1 = Tasks 1–3, PR A2 = Tasks 4–5. Open A2 against main only after A1 merges.
