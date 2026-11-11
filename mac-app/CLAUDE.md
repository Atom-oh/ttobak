# macOS native module

Tauri 2/Rust wraps the remote TTOBAK SPA and supplies native capture: a Core Audio
process tap of system audio mixed with the default microphone (macOS 14.2+). Auth,
presigning, upload-complete and retry orchestration remain in the SPA. Rust owns
finished WAV transport, not Cognito tokens. See ADR-024.

## Build and validation

Run in `mac-app/` on macOS:

```bash
npm ci
npm run dev
npm run build:signed
(cd src-tauri && cargo fmt --check && cargo clippy --all-targets -- -D warnings && cargo test)
```

No CI covers this module. Plain `npm run build` does not apply the required audio
entitlements through the project's signing script; use build:signed, or sign after
building. `scripts/sign.sh` signs with `TTOBAK_SIGN_IDENTITY`, else a local
self-signed "TTOBAK Dev" code-signing certificate when the keychain has one, else
ad-hoc. A certificate keeps TCC grants across rebuilds; an ad-hoc signature changes
per build and invalidates them, so re-grant and test a short recording. Launch the
app through Finder or `open`: TCC attributes a binary started from a terminal to
that terminal. Current codesign --deep is accepted for ad-hoc signing;
real Developer ID notarization needs explicit nested signing.

Core Audio capture is macOS-gated. Linux can build Tauri only with its native GUI
dependencies; on hosts lacking those, a scratch crate can test Tauri-free portions
of error.rs/mix.rs/audio.rs/leftover.rs/power.rs. That does not validate IOKit or
capture. Keep pure helpers (mix.rs) outside macOS-only modules. A Linux host with
the `aarch64-apple-darwin` target can type-check and clippy the macOS module with
`cargo check --target aarch64-apple-darwin` if `CC_aarch64_apple_darwin` points to a
stub compiler for objc2's build script; that proves types, not runtime behavior.

## Wire contract

| Command | Contract |
|---|---|
| start_recording | meeting_id, input_device_uid?; returns temp_path, optional warnings (e.g. no microphone), input {uid, name, reason requested/meeting/default, app?} |
| list_input_devices | input-capable non-virtual devices {uid, name, channels, is_default, used_by meeting apps} (ADR-061) |
| stop_recording | returns temp_path, duration_ms, byte_size, stop_timed_out, optional warnings (silent source, dropped buffers) |
| recording_status | path; recording, temp_path, elapsed_ms, finalizing_for_path, capture_lost (a failed mic switch stopped capture), optional warnings once that path finalized (covers stops that timed out) |
| upload_recording | path, uploadUrl, contentType; returns HTTP status code |
| cleanup_recording | Validated inactive/finalized path; removes WAV and adopted entry |
| release_recording_power | Releases only the path's idle-sleep protection, preserving recovery data |
| list_leftover_recordings | Startup-adopted inactive WAVs, newest first; not current-session recordings |
| control_ready | info {loggedIn, mounted?}; SPA bridge readiness (mounted:false on unload clears it) |
| control_reply | requestId, result {ok, data?/error{code,message}}; answers one native-control-request |
| control_report_state | state object (phase, meetingId, error), at most 16 KiB; returned by socket status |
| meeting_detection_get | returns supported, enabled, autoStop capability, active detection (ADR-061) |
| meeting_detection_set_enabled | enabled; persisted in the app config directory, off clears the announcement |
| meeting_detection_respond | detectionId, action dismiss/start(title)/stop; start/stop return the record page's control reply |
| update_prompt_choose | action `install`/`later`, version (the shown one; a stale version reloads the popup and acts on nothing); local update popup only (`update-prompt` capability, never remote); returns `busy` or a short Korean error message |
| caption_settings_get | returns {settings {profile, region, extraLanguage, windows}, profiles (names only), fonts} (ADR-069) |
| caption_settings_set | profile?, region, extraLanguage ja/zh/null; profile must be in the local AWS profile list; choosing or changing the profile or region first shows a native confirmation sheet from Rust (cancel: `confirmation-declined`, settings unchanged; for 30 s after a cancel such a change returns `confirmation-cooldown` with no sheet; a call while one runs: `invalid-input`); clears cached AWS clients |
| caption_translate | text, sourceLang, targetLang, isFinal, context[]; returns translatedText, engine llm/translate; SigV4 credentials only from the selected profile, never the default credentials chain (other SDK settings still load via `aws_config::defaults`); Bedrock is pinned to SigV4, so `AWS_BEARER_TOKEN_BEDROCK` is ignored (`bedrock_config`); upstream throttling returns `rate-limited`; only while that language's overlay window is open; 600 requests and 600,000 input bytes (text plus context) per sliding minute; text at most 9,000 bytes, context at most five items |
| caption_overlay_open | lang ko/en/ja/zh (ja/zh only as the extra language); opens or reuses the window, returns its saved layout |
| caption_overlay_close | lang; closes the window and saves it closed |
| caption_overlay_set_style | lang, style {fontFamily, fontSize 16-160, fontColor, outlineColor, outlineWidth 0-12, backgroundOpacity 0-1}; saved and sent to the window |
| caption_overlay_push | lang, kind entry/clear/preview, id (at most 64 bytes), order, text (at most 4,000 bytes; empty text on an entry removes that line), isFinal; a closed window ignores it; `clear` orders the window out (a transparent window still takes clicks), a non-empty entry or preview orders it back in without taking focus; `clear` also makes the window ignore the pointer; pushes to one window are serialised (`PushLanes`), other windows are independent |
| caption_overlay_idle | idle; overlay windows only (`caption-overlay.json`, never the remote SPA); sets only the calling `caption-<lang>` window to let clicks through while it shows no caption, and to take them again once it does; sync, main thread, no lock |
| intake_get_state | returns {adapter {present, updateAvailable null/bool}, nodePath, nodeMissing, schedule {kind off/hourly/daily/twiceDaily, times}, running run/setup/login/scheduleOff/null, lastResult (last `intake://finished` payload), status (`~/.ttobak/intake/status.json`, at most 256 KiB), selection [{connector, server, host, disabled}] (names only), launchAgentInstalled (the CLI LaunchAgent plist exists; the UI warns when the app schedule is also on), progress (last 200 lines)} (ADR-065, `intake_runner.rs`) |
| intake_set_schedule / intake_set_node_path | schedule (typed); path (absolute executable regular file named `node`, else `invalid-node-path`); stored in app config `intake.json` |
| intake_run_now / intake_setup / intake_login | start one background CLI run (setup: acceptPrivateCalendar, skip [connector]); `RUN_IN_PROGRESS` while one runs, `ADAPTER_UPDATING` during an adapter update; progress as `intake://progress` {line}, end as `intake://finished` {action, exitCode, result, code}; 20-minute timeout SIGTERMs the process group, then SIGKILLs it after a 3 s grace even when the CLI leader already exited |
| intake_cancel / intake_launch_agent_off | cancel returns whether a run was cancelled; launch-agent-off runs `intake schedule off` in the same exclusive run slot (killed on quit) and returns its JSON or code |
| intake_check_adapter / intake_update_adapter | compare or install `https://ttobak.atomai.click/mcp/ttobak-mcp.mjs` (no redirects, at most 8 MiB, must contain `intake setup`) against `~/.ttobak/server.mjs`, keeping one `server.mjs.bak` (a link to the replaced file; `server.mjs` changes only by an atomic rename); only `intake_update_adapter` is refused while a run is going (`intake_check_adapter` is allowed) |

Intake commands spawn node with argv only (no shell; the login PATH is captured once with `$SHELL -l -c`), a cleared environment (HOME, LANG, PATH) and their own process group, group-killed on timeout, cancel and app exit (a `shutting_down` flag stops any later spawn). The scheduler runs due slots and catches up at launch and about 45 s after a wake (`native-system-wake` or a wall-clock jump); a `PARTIAL_FAILURE` run satisfies its slot only when a connector succeeded.

Caption command errors carry a fixed `code`: profile-missing, credentials-expired,
model-access, rate-limited, invalid-input, invalid-output, timeout, aws-error,
save-failed, window-error, confirmation-declined, confirmation-cooldown, unavailable (no
app config directory; recording is unaffected). No capability grants a `dialog:` permission.

Socket `status` also returns `updateBusy` (`control::app_busy`), which `scripts/install.sh` reads.

finalizing_for_path is deliberately a new per-path field, not the older global
finalizing flag. Frontend wire compatibility checks rely on that distinction.
Events are native-audio-level (RMS), native-upload-progress (loaded/total), and
native-pcm-chunk (16kHz mono PCM for captions); native-control-request
({requestId, action start|stop, params, origin mcp|detection}) goes to the main window only.
native-meeting-detected/native-meeting-ended ({detectionId, kind, label, browser,
recording}) also go to the main window only; native-meeting-answered (same shape)
closes the banner after the floating prompt window (`meeting_prompt.rs`, local
`meeting-prompt.html`, `meeting_prompt_choose` only) answered it.
Caption overlay (ADR-069): `caption-update` ({lang, kind, id, order, text, isFinal}) and
`caption-style` (style fields plus lang) are sent with `emit_to` to the matching
`caption-<lang>` window. A JS listener with the default target receives every emit, so the
page listens with target `{kind: 'WebviewWindow', label: 'caption-<lang>'}` and also drops
payloads whose `lang` is not its own. The page loads
bundled `caption-overlay.html` under the listen-only `capabilities/caption-overlay.json`
(plus window dragging, `allow-set-size` and `allow-caption-overlay-idle`; no emit, no
other command). An idle page (no caption line) reports idle, so its transparent window
passes clicks through and shows no hover chrome until text returns. Hover chrome
(outline, move handle, resize grip) is invisible without a pointer; the grip resizes
via page `setSize` because tao has no macOS `startResizeDragging`. Credentials stay in Rust;
only profile names cross IPC. The event ACL has no per-event scope, so the overlay could
listen to broadcast events such as native-pcm-chunk (accepted, ADR-069). Overlay windows
use level 1000, all Spaces and full-screen-auxiliary behavior, are destroyed (keeping
their saved `open` flags) when the main window is destroyed, and need
`macOSPrivateApi` for transparency. `interpreter.rs` mirrors
`backend/internal/service/translate.go`; keep them aligned.
native-mic-muted ({muted, app Zoom|Google Meet, reason muted}) reports
the mute gate; a silent system tap while other apps play posts a notification; native-system-wake follows a system wake. Remote event capability grants must
match the actual loaded origin; do not add speculative origins. Tauri checks the
ACL for every command called from the remote SPA: each command must be listed in
`build.rs` COMMANDS and its `allow-*` permission granted in
`capabilities/default.json` (`command_acl_tests` enforces both).

## Invariants

- Finished WAV data streams disk-to-S3 in Rust; no read_recording_bytes/bulk IPC
  response. A former 400MB JS array delivery crashed JavaScriptCore. Small live
  PCM events intentionally cross IPC and reuse browser Transcribe Streaming;
  native mode has no Web Speech microphone fallback.
- Pin EXPECTED_BUCKET_HOST exactly; an amazonaws.com suffix would admit another
  customer's bucket. Validate path/recording membership server-side.
- upload.rs bounds no-progress streaming at 60 seconds, then separately bounds
  waiting for the response after full send at 180 seconds. Do not replace this
  with a fixed total upload timeout or an unbounded final phase.
- Never hold recorder locks across blocking Core Audio FFI. StartGuard's
  RAII drop clears abandoned starts; do not drop it while retaining the same
  non-reentrant lock.
- stop_recording marks the path finalizing inside the same critical section as
  taking its handle. upload_recording rejects active/finalizing paths. Status
  checks contain(path), not whether any recording is finalizing.
- recording_status checks lexical containment before canonicalization and checks
  resolved containment afterward. Out-of-tree status queries must not become
  file-existence probes.
- Aggregate input lists sub-device (microphone) streams first and tap streams
  after them; map channels per buffer's mNumberChannels, never assume one layout.
  The tap excludes the app's own sound. No display is required.
- The IOProc is real-time: mix and enqueue only. The worker owns disk writes and
  events. Teardown order: AudioDeviceStop, destroy IOProc, aggregate, tap, then
  close the queue and join the worker before finalizing the WAV.
- Without a requested device, capture uses the input device a meeting app is
  capturing from (Core Audio process `Devices`, input scope), else the system
  default; `input_select.rs` holds the rules. Virtual/aggregate devices remain
  selectable (a virtual noise-suppression microphone can be the real input); the
  picker hides them unless they are the default or a meeting app's input.
- The automatic stop after a meeting ends may only stop, once, for the ended
  detection it counted for; a resumed meeting (`native-meeting-resumed`) clears
  the bound recording so a pending stop is rejected.
- Meeting detection (`meeting.rs` rules, `meeting_detect.rs` probe) only offers a
  recording; confirmed starts use the control dispatch path, never
  `start_recording` directly. Core Audio polls run without recorder locks. The
  browser tab scripts (per-PID Scripting Bridge, bundle-ID AppleScript fallback)
  are fixed strings returning only meeting-URL prefixes, with the PID passed as an
  argument; never log tab URLs or widen the prefix list without updating
  `classify_meeting_url`.
- The Zoom/Meet mute gate (`zoom_mute.rs`, `meet_mute.rs`) reads the meeting
  UI through Accessibility off the IOProc thread and only flips an atomic the
  IOProc reads; the gate silences only while the followed meeting shows the user muted and reopens when that meeting ends; Meet never logs labels or URLs; never call AX or
  log from the IOProc.
- `sleep.rs` must always answer IOKit's will-sleep notice (IOAllowPowerChange)
  within its deadline, recording or not.
- Updates (`update.rs`, `update_policy.rs`, `update_prompt.rs`; ADR-073): update
  commands are never in the remote capability (`command_acl_tests` enforces it), and
  nothing installs or restarts while `app_busy`. Update checks are disabled while the
  `tauri.conf.json` updater pubkey is the placeholder. The archive download is bounded
  by 60 seconds of stalled progress, never a total duration (the check's request
  timeout is cleared from the found `Update`). Releases follow
  `docs/runbooks/mac-app-release.md`; sign only with the pinned "TTOBAK Dev" identity.
- Graceful exit finalizes capture. Force Quit/SIGKILL cannot run that callback;
  periodic WAV flush checkpoints and startup recovery limit damage instead.

## Leftovers and power

Startup adopts regular WAV files under allowed_dir(), rejects symlinks, and purges
files whose known age is at least 48 hours at startup (not continuously).
Unreadable/future modification times may be adopted. Both recorded_paths and
adopted_paths are needed. The UI lists only adopted leftovers and requires per-file
confirmation naming the cross-account caveat. Files are scoped to a macOS user,
not the Cognito user who recorded them; account binding remains an accepted gap.

Idle-sleep assertions are keyed by canonical path under shared ownership. One
recording's cleanup must not release another's protection. Failed uploads preserve
retry data; cleanup releases protection before deletion, while release_recording_power
allows navigation/reset without deleting the WAV. Each upload attempt also owns an
RAII assertion, including startup-adopted files. Invalid paths do not clear state.

`PowerAssertion` (idle-sleep only, held for the whole recording) covers idle system
sleep, not lid-close, explicit sleep or low battery; display sleep is allowed — this
is the accurate, existing scope statement (Apple QA1340) and stays true regardless
of the guard below.

`LidCloseGuard` (`PreventSystemSleep`) is IOKit's AC-power-only assertion type for
blocking lid-close sleep during a short, bounded operation — Apple's own guidance is
to never hold it for a whole open-ended recording, so it is scoped to exactly
`stop_recording`'s `stop_and_finalize` window (bounded by this command's own
lifetime, `STOP_CAPTURE_TIMEOUT` — NOT by the unbounded background finalize task if
that timeout is hit, see `lib.rs`) and `upload_recording`'s transfer window (bounded
by stalled progress, not total duration — a slow-but-progressing upload legitimately
holds it longer). Its actual effect on a real lid-close, and whether it does anything
at all on battery power, is UNVERIFIED — confirm with `pmset -g assertions` plus a
physical lid-close test on both AC and battery on real macOS before treating this as
a guarantee; if that test shows no effect (e.g. on battery), this doc needs
correcting, not the code. Closing the lid mid-meeting still suspends capture as
before, unaffected by this guard — only the finish-and-upload tail right after "end
meeting" is in scope for it at all. Validate `PowerAssertion` during recording,
pending upload, recovered upload and cleanup (its existing scope); validate
`LidCloseGuard` only during the finalize and transfer windows specifically — its
absence outside those two is correct, not a defect. Late callbacks after page
unmount must release their own pending protection without corrupting another
recording.

Tauri config CSP does not protect the remote SPA response; use the served policy.
Auth and tokens remain in the SPA. Source pointers: src-tauri/src/lib.rs, audio.rs,
upload.rs, leftover.rs, power.rs; frontend src/lib/tauri.ts and recording hooks.
