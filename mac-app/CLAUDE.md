# macOS native module

Tauri 2/Rust wraps the remote TTOBAK SPA and supplies native capture: a Core Audio
process tap of system audio mixed with the default microphone (macOS 14.2+). Auth,
presigning, upload-complete and retry orchestration remain in the SPA. Rust owns
finished WAV transport, not Cognito tokens. See ADR-006/024/046.

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
building. Re-grant TCC permissions if a rebuilt ad-hoc signature invalidates them,
then test a short recording. Current codesign --deep is accepted for ad-hoc signing;
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
| switch_recording_input | input_device_uid?; switches the running recording's microphone and returns the new input; refuses a device that cannot run at the WAV rate (ADR-061) |
| stop_recording | returns temp_path, duration_ms, byte_size, stop_timed_out, optional warnings (silent source, dropped buffers) |
| recording_status | path; recording, temp_path, elapsed_ms, finalizing_for_path, optional warnings once that path finalized (covers stops that timed out) |
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

finalizing_for_path is deliberately a new per-path field, not the older global
finalizing flag. Frontend wire compatibility checks rely on that distinction.
Events are native-audio-level (RMS), native-upload-progress (loaded/total), and
native-pcm-chunk (16kHz mono PCM for captions); native-control-request
({requestId, action start|stop, params, origin mcp|detection}) goes to the main window only.
native-meeting-detected/native-meeting-ended ({detectionId, kind, label, browser,
recording}) also go to the main window only.
native-mic-muted ({muted, app, reason muted|meeting_ended}) reports the Zoom mute
gate; native-system-wake follows a system wake. Remote event capability grants must
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
- The Zoom mute gate (`zoom_mute.rs`) reads Zoom through Accessibility off the
  IOProc thread and only flips an atomic the IOProc reads; never call AX or
  log from the IOProc. A microphone switch holds the backend's resource lock
  across its Core Audio calls, never the recorder lock.
- `sleep.rs` must always answer IOKit's will-sleep notice (IOAllowPowerChange)
  within its deadline, recording or not.
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
