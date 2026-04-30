# Ttobak Mac App

Native macOS desktop app for **system audio capture** (Zoom, Teams, etc.) using
[ScreenCaptureKit](https://developer.apple.com/documentation/screencapturekit).
Implements **Sub-project 2** of [ADR-006](../docs/decisions/ADR-006-tab-audio-capture-and-tauri-mac-app.md).

> Status: **scaffold / MVP in progress.** Builds and runs only on macOS 13+.
> Linux/Windows builds are not supported and will fail at link time.

---

## Why a desktop app

Browser `getDisplayMedia` can capture audio from a *Chrome tab* (used for Google
Meet) but cannot reach **desktop app** audio — Zoom and Teams run outside the
browser, and macOS does not let browser pages tap system audio without a
virtual driver. ScreenCaptureKit (macOS 13+) is the official, sandboxable API
for this. Tauri wraps the existing Ttobak SPA in a native window and exposes
native commands for audio capture.

## Architecture

```
+------------------------------------------------------------+
|  Ttobak Mac App (Tauri 2)                                  |
|                                                            |
|  +-- WKWebView --> https://ttobak.atomai.click             |
|  |   (existing Next.js SPA, full Cognito SPA login)        |
|  |   detects window.__TAURI_INTERNALS__                    |
|  |   --> shows "System Audio" recording option             |
|  |                                                         |
|  +-- Rust core (src-tauri/src/)                            |
|      |                                                     |
|      +-- audio.rs   ScreenCaptureKit SCStream              |
|      |              audio output --> hound WAV writer      |
|      +-- lib.rs     Tauri commands:                        |
|                       start_recording(meeting_id)          |
|                       stop_recording()                     |
|                       recording_status()                   |
|                       read_recording_bytes(path)           |
+------------------------------------------------------------+
```

Auth is delegated to the existing SPA inside the WebView — no token storage in
Rust, no duplicate OAuth flow. The app is, conceptually, "Ttobak SPA + native
audio capture command".

## Prerequisites (Mac dev box)

```bash
# Rust toolchain
curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh
rustup target add aarch64-apple-darwin x86_64-apple-darwin

# Tauri CLI (via npm)
cd mac-app
npm install
```

Xcode Command Line Tools must be installed:

```bash
xcode-select --install
```

## Develop

```bash
cd mac-app
npm run dev          # opens a Tauri window pointed at https://ttobak.atomai.click
```

The first time the app records, macOS will prompt for **Screen Recording** and
**Microphone** permissions. You must grant Screen Recording — that's the
ScreenCaptureKit gate, regardless of the fact that we only consume audio.

## Build a `.app` / `.dmg`

```bash
cd mac-app
npm run build                   # release, universal binary if both targets installed
# Result: src-tauri/target/release/bundle/{macos/Ttobak.app, dmg/Ttobak_0.1.0_*.dmg}
```

For a quick local debug build (faster, larger):

```bash
npm run build:debug
```

### Code signing & notarization

The scaffold ships unsigned. For distribution:

1. Set `bundle.macOS.signingIdentity` in `tauri.conf.json` to your Developer ID
   Application certificate name.
2. Provide an Apple ID + app-specific password as env vars (see
   [Tauri signing docs](https://tauri.app/v2/distribute/sign/macos/)) and run
   `npm run build` — Tauri handles `codesign` + `notarytool` automatically.

## Capabilities & entitlements

- `Info.plist` declares `NSMicrophoneUsageDescription` and
  `NSScreenCaptureUsageDescription` strings — both are required by macOS to
  show the permission prompts.
- `Entitlements.plist` enables `device.audio-input` and disables the App
  Sandbox (ScreenCaptureKit + arbitrary file system writes for recordings).

## Frontend integration (separate PR)

The existing SPA needs a small change to use native capture when running inside
Tauri:

```ts
// frontend/src/lib/desktop.ts (proposed)
export const isDesktop = typeof window !== "undefined"
  && "__TAURI_INTERNALS__" in window;

export async function startNativeRecording(meetingId: string) {
  const { invoke } = await import("@tauri-apps/api/core");
  return invoke<{ tempPath: string }>("start_recording", { meetingId });
}
```

The recording component picks `startNativeRecording` over `getUserMedia` when
`isDesktop` is true.

## Project layout

```
mac-app/
├── package.json              # Tauri CLI wrapper
├── src/                      # Fallback HTML loaded if frontendDist is used
│   └── index.html
└── src-tauri/
    ├── Cargo.toml
    ├── tauri.conf.json
    ├── build.rs
    ├── Info.plist
    ├── Entitlements.plist
    ├── capabilities/default.json
    └── src/
        ├── main.rs
        ├── lib.rs            # Tauri commands + state
        ├── audio.rs          # ScreenCaptureKit recorder
        └── error.rs          # AppError
```

## Known TODOs (Phase 2+)

- **Mic capture + system mix**: currently only the system-audio output is
  captured. Mixing the local mic via AVAudioEngine is Phase 2.
- **Offline queue**: ADR-006 specifies an offline-first recording mode that
  syncs to S3 when connectivity returns. Not yet implemented.
- **In-window upload bridge**: `read_recording_bytes` returns the raw WAV; the
  frontend should chunk-upload via the existing presigned URL flow. The SPA
  side of this is the integration PR mentioned above.
- **Auto-update**: Tauri Updater plugin is not wired up — releases are manual
  for now.
