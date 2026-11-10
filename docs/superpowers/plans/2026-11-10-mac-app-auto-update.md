# Mac App Install and Auto-Update Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

Historical plan record (2026-11-10); the ADR and runbook it produces become the
current references. Backticked English UI labels here are descriptors; the exact
Korean strings live in `update.rs`, `update_prompt.rs` and `mac-app/src/update-prompt.html`.

**Goal:** Install the TTOBAK Mac app with one `curl | sh` command and announce
tagged releases in-app with an Update/Later (Korean labels) popup and an app-menu item, never
interrupting a recording.

**Architecture:** `tauri-plugin-updater` (Rust API only) checks a static
`latest.json` on the existing CloudFront site under `/mac/`. A pure policy module
decides "busy" and "should prompt"; `update.rs` orchestrates check/install and
the app menu; a bundled local popup window answers through one command. A
release script on the owner's Mac tags, builds, re-signs, minisign-signs and
uploads. The web Settings page shows the install command.

**Tech Stack:** Tauri 2 / Rust (`tauri-plugin-updater` 2, `semver` 1), POSIX sh,
bash release script with a Python manifest helper, Next.js 16 / React 19.

**Spec:** `docs/superpowers/specs/2026-11-10-mac-app-auto-update-design.md`

## Global Constraints

- Distribution: self-signed "TTOBAK Dev" certificate only (leaf SHA-1
  `C1D1D726FA434621D65DED0261A722AA70924A26`); release refuses ad-hoc or any other identity.
- Target `aarch64-apple-darwin` only; macOS ≥ 14.2.
- Hosting paths: `mac/install.sh`, `mac/latest.json` (`no-cache,max-age=0,must-revalidate`),
  `mac/TTOBAK_<version>_aarch64.app.tar.gz` and `.sig` (`public,max-age=31536000,immutable`).
- Endpoint `https://ttobak.atomai.click/mac/latest.json`; manifest platform key `darwin-aarch64`
  with `url`, `signature`, `sha256`.
- Tag format `mac-v<semver>`; tag version must equal `tauri.conf.json` and `Cargo.toml` version.
- Update commands are never granted to the remote `https://ttobak.atomai.click` capability;
  `capabilities/default.json` must not contain any `updater:` permission.
- No install while busy; never `kill` the app from the installer.
- Korean UI copy: buttons `Update` / `Later`; menu `check-for-updates` / `update-available (vX.Y.Z)`.
- Repository docs and commits in English. Mac changes have no CI: run
  `(cd mac-app/src-tauri && cargo fmt --check && cargo clippy --all-targets -- -D warnings && cargo test)`.
- No new API route, origin, IAM role or bucket; bucket/distribution come from CloudFormation
  exports `TtobakSiteBucketName` / `TtobakDistributionId`.

## Review Focus

1. Manifest unreachable, HTML, malformed or for another platform → silent no-op on auto check,
   clear message on manual check; never a crash or a bogus popup (Task 2).
2. SPA never reported a phase (page not loaded) while native is idle → not busy; native
   recording with stale SPA "idle" → busy (Task 1).
3. User clicks Update, then a recording starts before download finishes → install refused
   after download, update stays available (Task 2).
4. Old installed app (no `updateBusy` in socket status) → installer asks the user to quit
   TTOBAK and rerun; never quits it blindly (Task 4).
5. Re-running the release after an upload failure for an already-pushed tag → resumes upload
   of the same build, no second tag, no overwrite of a different archive (Task 5).

---

### Task 1: Busy and prompt policy, upload tracking, socket `updateBusy`

**Files:**
- Create: `mac-app/src-tauri/src/update_policy.rs` (pure, no Tauri imports)
- Modify: `mac-app/src-tauri/src/upload.rs` (in-flight counter), `mac-app/src-tauri/src/lib.rs`
  (`RecorderState` field, `mod`), `mac-app/src-tauri/src/control.rs` (`status`)
- Modify: `mac-app/src-tauri/Cargo.toml` (`semver = "1"`)

**Interfaces:**
- Produces:
  - `pub fn is_busy(native_recording: bool, finalizing: usize, uploads_in_flight: usize, spa_phase: Option<&str>) -> bool`
    — busy if native recording, any finalizing path, any upload in flight, or SPA phase is
    one of `starting|recording|notes|creating|saving|uploading|redirecting`. `None`, `idle`,
    `error` and unknown strings are not busy by themselves.
  - `pub fn is_newer(current: &str, candidate: &str) -> bool` (semver; invalid → false)
  - `pub fn should_prompt(current: &str, available: &str, declined: Option<&str>) -> bool`
    — newer than current and not equal to the declined version (a newer-than-declined version prompts).
  - `RecorderState.uploads_in_flight: Arc<AtomicUsize>`, incremented for the whole
    `upload_recording` call by an RAII guard (decrement on every exit path).
  - `pub fn app_busy(app: &AppHandle) -> bool` in `control.rs` (reads recorder snapshot,
    `finalizing.len()`, the uploads counter and `last_state["phase"]`, then calls `is_busy`;
    the policy module stays pure).
  - Socket `status` data gains top-level `"updateBusy": bool` = `app_busy`.

- [ ] **Step 1: Write failing tests in `update_policy.rs`**

```rust
#[test] fn idle_everywhere_is_not_busy() { assert!(!is_busy(false, 0, 0, None)); assert!(!is_busy(false, 0, 0, Some("idle"))); assert!(!is_busy(false, 0, 0, Some("error"))); }
#[test] fn native_work_is_busy_even_if_spa_says_idle() { assert!(is_busy(true, 0, 0, Some("idle"))); assert!(is_busy(false, 1, 0, None)); assert!(is_busy(false, 0, 1, Some("idle"))); }
#[test] fn spa_phases_are_busy() { for p in ["starting","recording","notes","creating","saving","uploading","redirecting"] { assert!(is_busy(false, 0, 0, Some(p)), "{p}"); } }
#[test] fn version_rules() { assert!(is_newer("0.1.0", "0.2.0")); assert!(!is_newer("0.2.0", "0.2.0")); assert!(!is_newer("0.2.0", "0.1.9")); assert!(!is_newer("0.1.0", "garbage")); }
#[test] fn declined_version_is_not_prompted_but_newer_is() { assert!(should_prompt("0.1.0", "0.2.0", None)); assert!(!should_prompt("0.1.0", "0.2.0", Some("0.2.0"))); assert!(should_prompt("0.1.0", "0.3.0", Some("0.2.0"))); assert!(!should_prompt("0.2.0", "0.2.0", None)); }
```

- [ ] **Step 2:** `cd mac-app/src-tauri && cargo test update_policy` → FAIL (functions missing).
- [ ] **Step 3:** Implement the three functions; add `uploads_in_flight` and its guard in
  `upload_recording`; add `app_busy` and `updateBusy` to `control::status`.
- [ ] **Step 4:** Add a unit test in `upload.rs` (or `lib.rs`) that the RAII guard restores
  the counter on early return (construct the guard directly). Run
  `cargo fmt --check && cargo clippy --all-targets -- -D warnings && cargo test` → PASS.
- [ ] **Step 5:** Commit `feat(mac): update busy policy, upload tracking and socket updateBusy`.

### Task 2: Updater plugin, update orchestration and app menu

**Files:**
- Create: `mac-app/src-tauri/src/update.rs`
- Modify: `mac-app/src-tauri/Cargo.toml` (`tauri-plugin-updater = "2"`),
  `mac-app/src-tauri/tauri.conf.json` (`plugins.updater.endpoints`
  `["https://ttobak.atomai.click/mac/latest.json"]`, `plugins.updater.pubkey`), `lib.rs`
  (plugin init, `.manage(update::UpdateState)`, menu, setup start)

**Interfaces:**
- Consumes: Task 1 `is_busy`/`should_prompt`/`app_busy`.
- Produces:
  - `pub struct UpdateState` holding the available update (version, notes) and the declined
    version; declined version persisted as `update.json` `{ "declinedVersion": "x.y.z" }` in
    `app_config_dir` (same pattern as `meeting_detect.rs` `SETTINGS_FILE`).
  - `pub fn start(app: AppHandle)` — check on launch and every 6 hours; if `should_prompt`
    and not busy, call `update_prompt::offer` (Task 3); if busy, re-evaluate every 30 seconds
    until idle (no popup while busy).
  - `pub async fn check_now(app: &AppHandle, manual: bool) -> CheckOutcome` with
    `enum CheckOutcome { UpToDate, Available { version: String, notes: String }, Failed }`.
    Manual check shows a native dialog/notification for `UpToDate`/`Failed`; manual
    `Available` opens the popup even for a declined version.
  - `pub async fn install(app: &AppHandle) -> Result<(), InstallError>` with
    `enum InstallError { Busy, NoUpdate, Download(String) }`: refuse if `app_busy` before
    download, download+verify, re-check `app_busy` before installing (refuse → keep update
    available), then install and `app.restart()`.
  - `pub fn decline(app: &AppHandle)` — store the available version as declined.
  - App menu: build `Menu::default(app)` and insert into the app submenu, after "About", one
    item id `update` whose text is `check-for-updates` or `update-available (v{version})`;
    `pub fn refresh_menu(app: &AppHandle)` updates it. Clicking runs `check_now(manual=true)`
    or opens the popup when an update is known.
- Pubkey: until the owner generates the key (Task 8), `tauri.conf.json` carries the literal
  placeholder `"REPLACE_WITH_TTOBAK_UPDATER_PUBKEY"`; `update::start` logs and does nothing
  when the configured pubkey equals it. The release script refuses it (Task 5).

- [ ] **Step 1:** Unit tests for pure helpers in `update.rs`: `menu_label(None) == "check-for-updates"`,
  `menu_label(Some("0.3.0")) == "update-available (v0.3.0)"`; `declined` JSON round-trip and
  malformed/missing `update.json` → `None`; a manifest with `"sha256"` extra field parses via
  the plugin's response type (or document that it is ignored, verified by a test using the
  plugin's deserializer if public; otherwise a manual acceptance check).
  Also `outcome_for(manual, result)` (pure): a check error (unreachable, HTML, malformed,
  missing `darwin-aarch64`) maps to `Failed`, and on an automatic check `Failed` produces
  no popup and no dialog; tests cover both `manual` values (Review Focus 1).
- [ ] **Step 2:** `cargo test update::` → FAIL.
- [ ] **Step 3:** Implement; use only the plugin's Rust API (`app.updater()?.check()`,
  `Update::download`, `Update::install`). Do not add any `updater:` permission to any
  capability file.
- [ ] **Step 4:** Add to `command_acl_tests` (existing in `lib.rs`): `capabilities/default.json`
  contains no permission starting with `updater:`. Run fmt/clippy/test → PASS.
- [ ] **Step 5:** Commit `feat(mac): in-app update checks, install guard and app menu item`.

### Task 3: Update popup window

**Files:**
- Create: `mac-app/src-tauri/src/update_prompt.rs`, `mac-app/src/update-prompt.html`,
  `mac-app/src-tauri/capabilities/update-prompt.json`
- Modify: `mac-app/src-tauri/build.rs` (`COMMANDS` += `update_prompt_choose`), `lib.rs`
  (`invoke_handler`), command ACL tests

**Interfaces:**
- Consumes: Task 2 `install`, `decline`, `UpdateState`.
- Produces:
  - `pub fn offer(app: &AppHandle, version: &str, notes: &str)` — local window label
    `update-prompt`, URL `update-prompt.html?version=<v>&notes=<percent-encoded, ≤ 500 chars>`,
    modelled on `meeting_prompt.rs` (`page_url`, centered, always on top, focused).
  - `#[tauri::command] pub async fn update_prompt_choose(action: String, app: AppHandle) -> Result<String, String>`
    — `action` is `install` or `later`; `later` → `decline`, close; `install` → close and
    run `install`; `Busy` → returns `"busy"` and the page shows
    `retry-after-recording notice`; unknown action → `Err`.
  - Capability `update-prompt`: windows `["update-prompt"]`, permissions
    `["allow-update-prompt-choose"]`, no `remote`. `default.json` must NOT get
    `allow-update-prompt-choose`.
- Page copy: title `new-version title`, notes paragraph, buttons `Update` (primary)
  and `Later`. Text inserted with `textContent`, never `innerHTML`.

- [ ] **Step 1:** Tests: `page_url("0.3.0", "note")` percent-encodes and truncates notes to
  500 chars; ACL test — `update_prompt_choose` is in `COMMANDS`, granted only by
  `update-prompt.json`, absent from `default.json`; `update-prompt.html` contains no
  `innerHTML`.
- [ ] **Step 2:** `cargo test` → FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** fmt/clippy/test → PASS.
- [ ] **Step 5:** Commit `feat(mac): update popup window`.

### Task 4: Installer `install.sh`

**Files:**
- Create: `mac-app/scripts/install.sh`, `mac-app/scripts/test_install.py` (stdlib unittest)

**Interfaces:**
- Consumes: manifest format (Global Constraints); socket status `updateBusy` (Task 1).
- Environment overrides for tests only: `TTOBAK_INSTALL_BASE` (default
  `https://ttobak.atomai.click/mac`), `TTOBAK_INSTALL_DIR` (default `/Applications`).
- Behavior (POSIX sh, `set -eu`, no `sudo`, no `jq`; parse JSON with
  `/usr/bin/osascript -l JavaScript` or `/usr/bin/plutil`):
  1. Refuse non-Darwin, non-`arm64`, or `sw_vers -productVersion` < 14.2 (exit 2, message).
  2. Fetch `latest.json`; download `darwin-aarch64.url` to `mktemp -d`; verify `shasum -a 256`
     equals `sha256` (mismatch → exit 3, nothing installed).
  3. If an app with bundle id `click.atomai.ttobak.mac` is running: send
     `{"v":1,"id":"install","action":"status"}` to the control socket with `/usr/bin/nc -U`;
     `"updateBusy":true` → exit 4 with `rerun-after-recording notice`; `false` →
     `osascript -e 'tell application id "click.atomai.ttobak.mac" to quit'`, wait ≤ 30 s;
     socket missing or field absent → exit 5 asking the user to quit TTOBAK and rerun.
  4. Extract, move existing app to `TTOBAK.app.previous` in the install dir, move new app in,
     remove the previous copy only after success (restore it on failure); `xattr -cr`; `open`.
  5. `/Applications` not writable → exit 6 with a clear message.
- Never call `kill`.

- [ ] **Step 1:** Tests in `test_install.py` run `sh install.sh` with a temp `PATH` of stub
  `uname`, `sw_vers`, `curl`, `shasum`, `nc`, `osascript`, `pgrep`, `open`, `xattr`:
  `test_refuses_intel`, `test_refuses_old_macos`, `test_checksum_mismatch_installs_nothing`,
  `test_busy_app_refused`, `test_old_app_without_updateBusy_asks_to_quit` (exit 5, no quit
  sent), `test_idle_app_quit_then_replaced`, `test_failed_move_restores_previous`,
  `test_never_calls_kill` (stub `kill` records calls; asserts none).
- [ ] **Step 2:** `python3 -m unittest mac-app/scripts/test_install.py -v` → FAIL.
- [ ] **Step 3:** Implement `install.sh`.
- [ ] **Step 4:** Tests PASS; `sh -n mac-app/scripts/install.sh`.
- [ ] **Step 5:** Commit `feat(mac): curl installer`.

### Task 5: Release command

**Files:**
- Create: `mac-app/scripts/release.sh`, `mac-app/scripts/release_manifest.py`,
  `mac-app/scripts/test_release_manifest.py`
- Modify: `mac-app/package.json` (`"release": "bash scripts/release.sh"`),
  `scripts/test_publish_frontend.py` (assert `publish-frontend.sh` never deletes or targets `mac/`)

**Interfaces:**
- Consumes: Task 4 `install.sh`; `scripts/sign.sh`.
- `release_manifest.py` (pure CLI): `build --version V --notes N --url U --signature-file F --archive A`
  prints `latest.json` (adds `pub_date` UTC, `sha256`); `check-newer --published FILE --version V`
  exits 1 unless V > published version (missing file = OK).
- `npm run release -- "notes"` in `mac-app/`:
  1. Require clean tree, `HEAD == origin/main`, versions equal in `tauri.conf.json` and
     `Cargo.toml`, pubkey not the placeholder, `TTOBAK Dev` identity present.
  2. Fetch published `latest.json`; `check-newer`.
  3. Tag `mac-v<version>` handling: tag absent → build first; tag exists on `HEAD` (local or
     remote) → resume mode: rebuild is allowed only if the remote archive for that version is
     absent; if the remote archive exists, only re-upload `latest.json`/`install.sh` when the
     remote `.sig` matches the local one, else refuse. Tag on another commit → refuse.
  4. `npx tauri build --target aarch64-apple-darwin`, `bash scripts/sign.sh`; verify
     `codesign -d -r-` contains the leaf hash and the audio-input entitlement.
  5. `tar -czf` the re-signed `.app`; `npx tauri signer sign` (key from
     `TAURI_SIGNING_PRIVATE_KEY_PATH`/keychain-provided password, never echoed); manifest via
     `release_manifest.py build`.
  6. Create annotated tag with notes and push it; then `aws s3 cp` archive and `.sig`
     (immutable cache, `--if-none-match`-style refusal to overwrite: check `head-object`
     first), then `install.sh` and `latest.json` (no-cache); invalidate `/mac/latest.json`
     and `/mac/install.sh`. Bucket/distribution from `aws cloudformation list-exports`
     (`TtobakSiteBucketName`, `TtobakDistributionId`).

- [ ] **Step 1:** Tests: manifest fields/types and `darwin-aarch64` key; `sha256` matches the
  archive; `check-newer` rejects equal and lower, accepts higher and missing; publish-frontend
  test for `mac/`.
- [ ] **Step 2:** Run `python3 -m unittest mac-app/scripts/test_release_manifest.py -v` and
  `python3 -m unittest discover -s scripts -p 'test_publish_frontend.py' -v` → FAIL.
- [ ] **Step 3:** Implement; `bash -n mac-app/scripts/release.sh`.
- [ ] **Step 4:** Tests PASS.
- [ ] **Step 5:** Commit `feat(mac): one-command tagged release`.

### Task 6: Web "Mac app" section

**Files:**
- Create: `frontend/src/components/MacAppInstall.tsx`, `frontend/src/lib/macRelease.ts`,
  `frontend/scripts/mac-app-install.test.mjs`
- Modify: `frontend/src/app/settings/page.tsx` (section before Integrations, `id="mac-app"`)

**Interfaces:**
- `macRelease.ts`: `export const MAC_INSTALL_COMMAND = 'curl -fsSL https://ttobak.atomai.click/mac/install.sh | sh'`;
  `export function publishedVersion(manifest: unknown): string | null` (valid semver string
  in `version`, else null).
- Component: heading `Mac app`, one-line explanation (Apple Silicon, macOS 14.2+), the command
  in a code block with a copy button (same pattern as `McpGuide`), `current-version line` from
  `fetch('/mac/latest.json', { cache: 'no-store' })`; on failure hide the version line. No
  download button.

- [ ] **Step 1:** Tests: `publishedVersion({version:'0.2.0'}) === '0.2.0'`; `null` for
  `{}`, `{version:1}`, `'<html>'`, `{version:'x'}`; command constant exact.
- [ ] **Step 2:** `cd frontend && node --test scripts/mac-app-install.test.mjs` → FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** `npm run lint && npm run test:unit && npm run build` → PASS.
- [ ] **Step 5:** Commit `feat(web): Mac app install section`.

### Task 7: Documentation

**Files:**
- Create: `docs/decisions/ADR-071-mac-app-distribution-and-updates.md` (use the next free
  number at implementation time; ADR-066 is the account-shared calendar),
  `docs/runbooks/mac-app-release.md`
- Modify: `docs/README.md` (index), `docs/DESIGN-SPEC.md` (popup, menu, Settings section),
  `mac-app/CLAUDE.md` (wire contract rows for `update_prompt_choose`, socket `updateBusy`,
  updater invariants), `docs/architecture.md` if it lists Mac distribution.

- [ ] **Step 1:** ADR: decision, rationale (no quarantine via curl/updater; DR stability),
  consequences, Developer ID triggers, accepted risk (public archive, self-signed).
- [ ] **Step 2:** Runbook: one-time updater key setup (`npx tauri signer generate -w ~/.tauri/ttobak-updater.key`,
  pubkey into `tauri.conf.json` via PR, `.p12` and key backups), version-bump PR,
  `npm run release -- "notes"`, verification (`curl -sI` cache headers, manifest), restoring
  a previous `latest.json`, resume after failed upload, key loss and certificate expiry (2036-06-01).
- [ ] **Step 3:** `python3 scripts/docs/check_docs.py` → PASS.
- [ ] **Step 4:** Commit `docs: Mac app distribution ADR and release runbook`.

### Task 8: Owner setup and manual acceptance (owner, on real Macs)

- [ ] Generate the updater key; replace the placeholder pubkey through a PR; back up key and `.p12`.
- [ ] Version-bump PR to `0.2.0`; after merge `npm run release -- "first auto-update release"`.
- [ ] Spec Verification steps 1–6 (fresh install, Later + menu, busy refusal, permissions kept,
  tampered archive refused, teammate Mac). Record results in the ADR status line.
