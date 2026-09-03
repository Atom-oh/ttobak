# Mac App Module

Tauri 2 + Rust desktop wrapper that adds native macOS system-audio capture to the TTOBAK SPA. Implements **Sub-project 2** of [ADR-006](../docs/decisions/ADR-006-tab-audio-capture-and-tauri-mac-app.md).

## Build only on macOS

The app itself runs only on macOS (ScreenCaptureKit). `screencapturekit` is macOS-target-gated in `Cargo.toml`, so `cargo check`/`cargo test`/`cargo clippy` DO build on Linux as long as Tauri's own native deps are present (webkit2gtk-4.1, gtk3, dbus dev packages — Amazon Linux 2023 has no webkit2gtk, so there only the Tauri-free modules `error.rs`/`audio.rs`/`leftover.rs` can be tested, via a scratch crate that `#[path]`-includes them). **No CI**: built and signed locally on a developer Mac (ad-hoc signing needs the Apple toolchain), so run the lint/test block below yourself before pushing.

```bash
cd mac-app
npm install
npm run dev          # tauri dev — opens window at https://ttobak.atomai.click
npm run build:signed # release + codesign --entitlements (see "Critical: signing" below)
```

## Test & lint (any platform with Tauri's native deps; no CI runs these)

```bash
cd mac-app/src-tauri
cargo fmt --check
cargo clippy --all-targets -- -D warnings
cargo test            # pure helpers: interleave_planes, StartGuard, leftover scan/containment, upload streaming
```

## Critical: signing

**`npm run build` alone produces a broken bundle.** Tauri 2's default ad-hoc signing skips `codesign --entitlements`, so `Entitlements.plist` ships but is never applied. Symptom: app launches, never prompts for mic/screen recording, `navigator.mediaDevices` is undefined inside WKWebView, recording fails.

Fix: always use `npm run build:signed` (or `npm run sign` after `npm run build`) — runs `scripts/sign.sh`: `codesign --force --deep --sign - --entitlements Entitlements.plist <app>`, then verifies `audio-input` is embedded.

If permissions still don't prompt after a signed build, the TCC cache is stuck on a prior denial: `tccutil reset Microphone click.atomai.ttobak.mac` (plus `Camera`, `ScreenCapture`).

## Architecture decisions

- Auth is delegated to the WebView (loads `https://ttobak.atomai.click`, reusing the SPA's Cognito login), not a native OAuth PKCE flow — free SSO, no token storage in Rust.
- System audio only for MVP; local mic capture + mixing is Phase 2.
- WAV (16-bit PCM, 48kHz, stereo) on disk. Presign/auth/upload-complete stay in the SPA; only the byte *transport* (ADR-024) is Rust — `upload_recording` (`src/upload.rs`) streams the WAV from disk straight to the presigned S3 URL, never through the WebView (see IPC pitfall below).
- Live captions in System Audio mode reuse the browser's Transcribe Streaming pipeline (ADR-024) instead of a second one: the audio callback downsamples to 16kHz mono and emits `native-pcm-chunk`, consumed by `TranscribeStreamingSession.startNative()`/`pushChunk()` over the same Cognito-credentialed WebSocket mic/tab modes use. No Web Speech fallback — there's no microphone in this mode.

## Layout

```
src/index.html        # offline fallback (Tauri normally loads the live SPA URL)
src-tauri/
  Cargo.toml          # screencapturekit 1.x, hound, tauri 2, parking_lot, tokio
  tauri.conf.json     # window points at ttobak.atomai.click
  Info.plist          # NSMicrophoneUsageDescription, NSCameraUsageDescription, NSScreenCaptureUsageDescription
  Entitlements.plist  # device.audio-input, device.camera, network.client; sandbox OFF
  src/
    main.rs           # entrypoint
    lib.rs            # Tauri command surface
    audio.rs          # SCStream → hound WAV writer + native-pcm-chunk downsampler (cfg(target_os = "macos"))
    upload.rs         # streaming upload_recording (reqwest, file → presigned S3 URL) + tests
    error.rs          # AppError
```

## Tauri commands

| Command             | Args                                                    | Returns                                                  |
|----------------------|----------------------------------------------------------|------------------------------------------------------------|
| `start_recording`   | `meeting_id: string`                                    | `{ temp_path }`                                            |
| `stop_recording`    | —                                                        | `{ temp_path, duration_ms, byte_size, stop_timed_out }`    |
| `recording_status`  | `path: string`                                          | `{ recording, temp_path: string \| null, elapsed_ms, finalizing_for_path: bool }` — `finalizing_for_path` is for `path` specifically, not "is anything finalizing" (polled by RecordButton, passing its own tempPath, after `stop_timed_out` to await background finalize). The field is deliberately NOT named `finalizing`: an earlier shipped build sent a global any-path bool under that name, so the SPA's version-skew fail-fast (`=== undefined`) only works because the per-path field has a name no older build ever sent — don't rename it back. A current build ALWAYS sends it; the TS type marks it optional only for wire-compat. `path` must be under `allowed_dir()` (checked lexically before any FS access) or the answer is `false` without a syscall |
| `upload_recording`  | `path: string, uploadUrl: string, contentType: string`  | HTTP status code (u16) on success                         |
| `cleanup_recording` | `path: string`                                          | deletes temp WAV after upload-complete is confirmed; also drops it from the adopted set |
| `list_leftover_recordings` | —                                                | `LeftoverRecording[]` `{ path, file_name, byte_size, modified_ms }`, newest first — ONLY files adopted at startup from a previous run (`RecorderState.adopted_paths`), never a path this session's `start_recording` created; skips active/finalizing paths and forgets vanished files. Consumed by `useLeftoverRecordings.ts` → `LeftoverRecordingsCard.tsx` on `/record`, which gates 업로드/삭제 behind a per-file confirm (ADR-024 cross-account caveat) |

`read_recording_bytes` (WAV bytes via IPC binary `ArrayBuffer`) is gone (ADR-024) and must never come back in this form — see the IPC pitfall below.

**Events** (via `frontend/src/lib/tauri.ts`'s `onNative*` helpers — require the `capabilities/*.json` `remote` grant below, or they silently never arrive):
- `native-audio-level` — normalized 0–1 RMS, ~30 Hz, while recording.
- `native-upload-progress` — `{ loaded, total }` bytes, during `upload_recording`.
- `native-pcm-chunk` — base64 16kHz mono 16-bit PCM, ~64ms/chunk, while recording (feeds live captions).

Frontend integration adds an `isDesktop` (`__TAURI_INTERNALS__`) check to `frontend/src/lib/`, routing the record button through `invoke()` inside Tauri.

## Critical: don't ship WAV bytes through IPC

`read_recording_bytes` used to `std::fs::read` the whole WAV and return it as one IPC `Response`. On a real ~35-minute recording (400,949,804 bytes) this crashed the app: this window loads the **remote** production SPA (not a local `tauri://` origin), so Tauri 2 delivers IPC binary responses via `evaluateJavaScript` as one giant JS array literal — which fatally crashed JavaScriptCore (app froze after "end meeting"). Details: [ADR-024](../docs/decisions/ADR-024-mac-app-native-streaming-upload-and-system-audio-captions.md).

**Rule**: any command moving recording bytes must stream them (file → network, or a chunk small enough for `evaluateJavaScript`, like the ~2.7KB `native-pcm-chunk` payloads) — never one bulk read-and-return.

## Pitfalls

- ScreenCaptureKit needs an `SCDisplay` even for audio-only capture — `audio.rs::macos::Backend::start` picks the first display from `SCShareableContent::current()`.
- `excludes_current_process_audio = true` keeps the app's own UI sounds out of the recording — don't disable without reason.
- `screencapturekit` crate API has shifted across versions; pinned to `1` in `Cargo.toml` — if a `cargo update` breaks the build, fix call sites in `audio.rs::macos` only.
- **`capabilities/*.json` needs an explicit `"remote"` grant for the production origin, or every `plugin:event|listen` silently no-ops** (custom commands still work — they bypass the ACL entirely). Tauri 2's ACL only matches a capability's default (local) context against `tauri://localhost`; this window loads `https://ttobak.atomai.click`, so it needs `remote: { urls: [...] }`. This is why `native-audio-level` never reached the UI pre-ADR-024 — nothing looked broken except a waveform that silently never moved. Keep `remote.urls` to exactly the origin(s) `tauri.conf.json`'s window actually loads — `native-pcm-chunk` carries raw meeting audio, so don't add a second origin as speculative "insurance" without a concrete need; that only widens who can receive it while adding nothing until it's actually used.
- Rebuilding/re-signing (`npm run build:signed`) changes the ad-hoc signature and can invalidate a previously-granted Screen Recording TCC permission — re-grant and do a short test recording after any rebuild.
- **`tauri.conf.json`'s `app.security.csp` is largely inert.** Tauri only injects that CSP into responses *it* serves; this window loads a remote origin (`app.windows[0].url`), so the real policy in effect is whatever CloudFront/the SPA sends for that origin, not this file. Don't rely on this config as an actual security boundary.
- **ScreenCaptureKit's audio buffer layout (interleaved vs. planar) is detected at runtime, not assumed** — `audio.rs::macos::did_output_sample_buffer` logs `buffer_count=`/`layout=` on the first callback of every recording specifically so this can be confirmed against real hardware; `interleave_planes` normalizes either layout to interleaved before the WAV writer or the caption downmix ever see it. If a `screencapturekit` version bump ever changes the delivered layout, that log line is where it shows up. **`interleave_planes` deliberately lives at `audio.rs`'s top level, NOT inside `mod macos`'s `#[cfg(target_os = "macos")]` gate** — it has no ScreenCaptureKit/FFI dependency, and this module has no CI, so gating it out on non-macOS would mean its regression tests (the planar-vs-interleaved bug, the empty-plane-erases-everything bug) could never run anywhere except a Mac. Any future pure helper here should follow the same rule: gate only what's actually platform-specific, not the function that happens to be called from platform-specific code.
- **`start_recording`/`stop_recording` never hold `RecorderState.recorder`'s lock across blocking ScreenCaptureKit FFI** (`SCShareableContent::get()`/`start_capture()`/`stop_capture()`, none of which have their own timeout) — both go through a reserve-then-release-the-lock-then-blocking-work-then-reacquire shape (`AudioRecorder::begin_start`/`install` for start, `take_handle` for stop) specifically so `recording_status` (a *sync*, main-thread command) never freezes behind a Screen Recording permission dialog or a wedged stop. Don't reintroduce blocking FFI inside a held lock here. **The start reservation is released by an RAII `StartGuard` (`audio.rs`), not by hand**: drop without `disarm()` → `cancel_start()`, so an early `?`, a panic, or a dropped command future can't leave `starting = true` and wedge every later start behind `AlreadyRunning`. Its `Drop` takes the recorder lock, and parking_lot is non-reentrant — never keep `state.recorder.lock()` alive across a point where the guard could drop; keep every lock in `start_recording` a temporary.
- **`upload_recording`'s stall watchdog has TWO deadlines, both bounded** (`upload.rs`): `STALL_TIMEOUT` (60s with no bytes read from disk) while streaming, then `RESPONSE_DEADLINE_AFTER_FULL_SEND` (180s) once the whole body is handed to hyper and the request is only awaiting S3's response. An earlier version exempted that second phase from any timeout, which hung the command forever on a connection that died right after the full send — `reqwest` here sets only a `connect_timeout`. Don't collapse these back into one, and don't make the second one unbounded. Both are parameters of `stream_file_to_url` so the tests can shorten them.
- **`RecorderState.finalizing` is a `Mutex<HashSet<PathBuf>>` keyed by recording path, not a single bool/counter** — a shared bool let an earlier, still-wedged-past-timeout stop's completion clear the flag out from under a *later* recording's in-flight finalize, so a status check could falsely report "done" while that later WAV was still being written. `upload_recording` also checks this set directly (plus whether the path is the currently-active recording) as a server-side backstop, independent of whatever the frontend's own poll does. `stop_recording` inserts into this set BEFORE releasing `RecorderState.recorder`'s lock (in the same critical section as `take_handle()`) — inserting only after releasing that lock leaves a TOCTOU window where `upload_recording` can observe `recording: false` and an empty `finalizing` set at once, against a WAV that hasn't actually finished being written. **`recording_status` must check this set PER-PATH (`contains(path)`), never `is_empty()`** — an any-path aggregate was tried first and reverted: it let an unrelated, still-wedged EARLIER recording keep a LATER recording's own already-finished finalize looking incomplete forever whenever the two overlap, which is worse than the original single-bool bug in exactly that scenario. `recording_status`'s `path` argument and `RecordButton.tsx`'s poll (which passes its own `tempPath`) exist specifically to keep this check-scoped-to-one-recording.
- **`lib.rs::run()`'s `.run(|app_handle, event| ...)` callback finalizes any in-progress recording on `RunEvent::Exit`** — this only covers a GRACEFUL quit (window closed, Cmd+Q); a SIGKILL/Force Quit tears the process down without running this callback at all, same as always, so it's the periodic 5s flush checkpoint (not this handler) that bounds data loss on that path. Without this handler, a graceful quit mid-recording still never calls `stop_capture`/`finalize_writer`, leaving the WAV's RIFF/data header unpatched beyond that last checkpoint.
- **`run()`'s `.setup()` adopts any leftover `*.wav` under `allowed_dir()` into `recorded_paths` AND `adopted_paths` on startup** (`leftover::scan_leftover_dir`, unit-tested without Tauri) — a crash or force-quit from a previous run otherwise leaves a file `validate_recording_path` rejects forever ("path was not created by this recording session"), and the `/record?mode=upload` recovery route the error messages point to doesn't go through the native commands at all. Adoption alone was NOT enough (PR #169 review): nothing exposed the adopted set and the SPA keeps its pending path only in memory, so the whitelist entry was dead state after a relaunch. `list_leftover_recordings` + the `/record` card (`LeftoverRecordingsCard.tsx`) close that; keep the command reporting the adopted-at-startup set only, never a path this session created. Adoption rejects symlinks (`entry.file_type().is_file()`, checked before `validate_recording_path`'s own containment check ever runs) and deletes (rather than adopts) anything older than `LEFTOVER_RECORDING_MAX_AGE` (48h), evaluated at launch only (best-effort, not a continuous sweep) — raw meeting audio is PII, and this directory is scoped to the macOS user, not a Cognito login, so the card's per-file confirm (naming that caveat) is mandatory before any upload/delete; see root `CLAUDE.md` Known Issues (Medium) for the residual.
- **`recording_status(path)` checks containment under `allowed_dir()` lexically BEFORE `canonicalize`** (`leftover::finalizing_for_path`) — `path` is WebView-controlled, and canonicalize succeeds only for existing files, so the previous unconditional call made this sync command a file-existence oracle for arbitrary paths. Out-of-tree paths answer `false` with no syscall; a symlink inside the dir that resolves outside is re-checked after canonicalize. This is deliberately lighter than `validate_recording_path` (no whitelist-membership requirement, never an error) because status must stay infallible for a path that is mid-finalize.

## Definitely NOT in this module

- Auth / token storage (lives in the SPA)
- Presign, upload-complete notification, and retry orchestration (`frontend/src/lib/upload.ts` / `frontend/src/hooks/usePostRecording.ts` — only the byte *transport* moved to Rust, see `src/upload.rs` and ADR-024)
- Transcription / summarization (Lambda pipeline, unchanged)
