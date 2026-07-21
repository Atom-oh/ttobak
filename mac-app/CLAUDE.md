# Mac App Module

Tauri 2 + Rust desktop wrapper that adds native macOS system-audio capture to
the TTOBAK SPA. Implements **Sub-project 2** of [ADR-006](../docs/decisions/ADR-006-tab-audio-capture-and-tauri-mac-app.md).

## Build only on macOS

`screencapturekit`, `core-foundation`, `objc2-foundation` only build on Darwin.
Linux/Windows `cargo check` will fail at link time. **No CI** — the Mac app is
built and signed locally on a developer Mac (Tauri's ad-hoc signing requires
the Apple toolchain on macOS).

```bash
cd mac-app
npm install
npm run dev          # tauri dev — opens window at https://ttobak.atomai.click
npm run build:signed # release + codesign --entitlements (see "Critical: signing" below)
```

## Critical: signing

**`npm run build` alone produces a broken bundle.** Tauri 2's default ad-hoc
signing skips `codesign --entitlements`, so `Entitlements.plist` is shipped
but never applied to the binary. Symptom: app launches, never prompts for
mic / screen recording, `navigator.mediaDevices` is undefined inside WKWebView,
and the recording flow fails.

Always use `npm run build:signed` (or `npm run sign` after `npm run build`).
That runs `scripts/sign.sh` which does
`codesign --force --deep --sign - --entitlements Entitlements.plist <app>`
and verifies `audio-input` is embedded.

If permissions still don't prompt after a signed build, the macOS TCC cache
is sticking on a previous denial — `tccutil reset Microphone click.atomai.ttobak.mac`
(plus `Camera`, `ScreenCapture`).

## Architecture decisions

- **Auth is delegated to the WebView**, not duplicated in Rust. The Tauri
  window loads `https://ttobak.atomai.click` (the existing static SPA), which
  already handles Cognito SPA login. ADR-006 mentioned a system-browser OAuth
  PKCE flow (mirroring `mcp-server`); we deferred that because the WebView
  approach gives us free SSO and zero token-storage code.
- **System audio only for MVP.** Local mic capture + mixing is Phase 2 — the
  whole point of the desktop app is what the browser cannot do.
- **WAV (16-bit PCM, 48 kHz, stereo) on disk.** Presign, auth, and
  upload-complete notification stay in the SPA (ADR-006's original
  boundary), but as of ADR-024 the byte *transport* for a finished
  recording happens in Rust (`upload_recording`, `src/upload.rs`) — the WAV
  is streamed straight from disk to the presigned S3 URL the SPA hands it,
  never read into the WebView. See "Critical: don't ship WAV bytes through
  IPC" below for why.
- **Live captions in System Audio mode reuse the browser's Transcribe
  Streaming pipeline (ADR-024), not a second one.** The audio callback
  downsamples to 16kHz mono and emits `native-pcm-chunk` events; the
  frontend's `TranscribeStreamingSession.startNative()`/`pushChunk()` feed
  those into the same Cognito-credentialed WebSocket connection mic/tab
  modes already use. No Web Speech fallback exists in this mode — it needs
  a microphone that doesn't exist here.

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

| Command                  | Args                                              | Returns                                                |
|--------------------------|----------------------------------------------------|---------------------------------------------------------|
| `start_recording`        | `meeting_id: string`                                | `{ temp_path }`                                          |
| `stop_recording`         | —                                                   | `{ temp_path, duration_ms, byte_size, stop_timed_out }`  |
| `recording_status`       | —                                                   | `{ recording, temp_path?, elapsed_ms }` (no frontend callers today) |
| `upload_recording`       | `path: string, uploadUrl: string, contentType: string` | HTTP status code (u16) on success                     |
| `cleanup_recording`      | `path: string`                                      | deletes temp WAV after upload-complete is confirmed      |

`read_recording_bytes` (WAV bytes via IPC binary `ArrayBuffer`) used to
exist here and is deliberately gone (ADR-024) — see the pitfall below for
why it can never come back in this form.

**Events** (subscribed via `frontend/src/lib/tauri.ts`'s `onNative*`
helpers — require the `capabilities/*.json` `remote` grant below, or they
silently never arrive):
- `native-audio-level` — normalized 0–1 RMS, ~30 Hz, while recording.
- `native-upload-progress` — `{ loaded, total }` bytes, during
  `upload_recording`.
- `native-pcm-chunk` — base64-encoded 16kHz mono 16-bit PCM, ~64ms/chunk,
  while recording (feeds live captions).

The frontend integration PR adds an `isDesktop` (`__TAURI_INTERNALS__`) check
to `frontend/src/lib/` and routes the record button through `invoke()` when
running inside Tauri.

## Critical: don't ship WAV bytes through IPC

`read_recording_bytes` used to `std::fs::read` the entire recorded WAV and
return it as one IPC `Response`. On a real ~35-minute recording
(400,949,804 bytes) this crashed the app: because this window loads the
**remote** production SPA rather than a local `tauri://` origin, Tauri 2
delivers IPC binary responses to the WebView via `evaluateJavaScript` — as
one giant JS array literal. JavaScriptCore fatally asserted while
bytecode-compiling it, killing the WebContent process (the user saw this as
the whole app freezing after "end meeting"). See
[ADR-024](../docs/decisions/ADR-024-mac-app-native-streaming-upload-and-system-audio-captions.md).
Any future command that needs to move a recording's bytes somewhere must
stream them (file → network, or file → a bounded chunk size genuinely small
enough for `evaluateJavaScript`, like the ~2.7KB `native-pcm-chunk`
payloads) — never one bulk read-and-return.

## Pitfalls

- ScreenCaptureKit needs an `SCDisplay` even when capturing audio only.
  `audio.rs::macos::Backend::start` picks the first display from
  `SCShareableContent::current()`.
- `excludes_current_process_audio = true` keeps the app's own UI sounds out of
  the recording. Don't disable this without a reason.
- The `screencapturekit` crate's API has been moving across versions.
  Pinned to `1` in Cargo.toml; if the build breaks after a `cargo update`,
  the only call sites that need adjustment are inside `audio.rs::macos`.
- **`capabilities/*.json` needs an explicit `"remote"` grant for the
  production origin, or every `plugin:event|listen` silently no-ops while
  commands keep working.** This app's window loads
  `https://ttobak.atomai.click` (see `tauri.conf.json`), not a local
  `tauri://` origin. Tauri 2's ACL only matches a capability's default
  (local) context against `tauri://localhost`; without `remote: { urls:
  [...] }`, `plugin:event|listen` is rejected for this window while custom
  commands (which bypass the ACL entirely — this app has no `__app-acl__`
  manifest) keep working. This is exactly why `native-audio-level` events
  never reached the UI before ADR-024 — start/stop/read all worked, so
  nothing looked broken except a waveform that silently never moved.
- Rebuilding/re-signing (`npm run build:signed`) changes the ad-hoc
  signature, which can invalidate a previously-granted Screen Recording TCC
  permission — re-grant it and do a short test recording after any rebuild,
  before relying on it for a real meeting.

## Definitely NOT in this module

- Auth / token storage (lives in the SPA)
- Presign, upload-complete notification, and retry orchestration (live in
  `frontend/src/lib/upload.ts` / `frontend/src/hooks/usePostRecording.ts` —
  only the byte *transport* for a finished recording moved to Rust, see
  `src/upload.rs` and ADR-024)
- Transcription / summarization (Lambda pipeline, unchanged)
