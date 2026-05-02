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
- **WAV (16-bit PCM, 48 kHz, stereo) on disk, then upload through the SPA.**
  Avoids duplicating the presigned-upload flow in Rust.

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
    audio.rs          # SCStream → hound WAV writer (cfg(target_os = "macos"))
    error.rs          # AppError
```

## Tauri commands

| Command                  | Args                | Returns                                      |
|--------------------------|---------------------|----------------------------------------------|
| `start_recording`        | `meeting_id: string`| `{ temp_path }`                              |
| `stop_recording`         | —                   | `{ temp_path, duration_ms, byte_size }`      |
| `recording_status`       | —                   | `{ recording, temp_path?, elapsed_ms }`      |
| `read_recording_bytes`   | `path: string`      | WAV bytes via IPC binary (ArrayBuffer)       |
| `cleanup_recording`      | `path: string`      | deletes temp WAV after upload                |

The frontend integration PR adds an `isDesktop` (`__TAURI_INTERNALS__`) check
to `frontend/src/lib/` and routes the record button through `invoke()` when
running inside Tauri.

## Pitfalls

- ScreenCaptureKit needs an `SCDisplay` even when capturing audio only.
  `audio.rs::macos::Backend::start` picks the first display from
  `SCShareableContent::current()`.
- `excludes_current_process_audio = true` keeps the app's own UI sounds out of
  the recording. Don't disable this without a reason.
- The `screencapturekit` crate's API has been moving across versions.
  Pinned to `1` in Cargo.toml; if the build breaks after a `cargo update`,
  the only call sites that need adjustment are inside `audio.rs::macos`.

## Definitely NOT in this module

- Auth / token storage (lives in the SPA)
- Upload protocol (lives in `frontend/src/lib/upload.ts` via presigned URLs)
- Transcription / summarization (Lambda pipeline, unchanged)
