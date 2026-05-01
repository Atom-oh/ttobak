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
|                       cleanup_recording(path)              |
+------------------------------------------------------------+
```

Auth is delegated to the existing SPA inside the WebView — no token storage in
Rust, no duplicate OAuth flow. The app is, conceptually, "Ttobak SPA + native
audio capture command".

## System requirements

| Requirement | Minimum |
|---|---|
| macOS | 13.0 (Ventura) |
| Xcode CLT | `xcode-select --install` |
| Rust | stable (latest) |
| Node.js | 18+ |
| Internet | required — WebView loads the live SPA |

## Quick start

```bash
# 1. Clone & checkout
git clone git@github.com:Atom-oh/ttobak.git && cd ttobak
git checkout fix/remove-section-splitting   # or main once merged

# 2. Install Rust (skip if already installed)
curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh
rustup target add aarch64-apple-darwin x86_64-apple-darwin

# 3. Install dependencies & run dev mode
cd mac-app
npm install
npm run dev          # opens a Tauri window at https://ttobak.atomai.click
```

The first time the app records, macOS will prompt for **Screen Recording** and
**Microphone** permissions. You must grant Screen Recording — that's the
ScreenCaptureKit gate, regardless of the fact that we only consume audio.

## Build a `.app` / `.dmg`

```bash
cd mac-app
npm run build
# Result: src-tauri/target/release/bundle/{macos/Ttobak.app, dmg/Ttobak_0.1.0_*.dmg}
```

For a quick local debug build (faster, larger):

```bash
npm run build:debug
```

### Running an unsigned build

Without code signing, macOS Gatekeeper will block the app. To run locally:

```bash
# Option 1: Remove quarantine attribute
xattr -cr /path/to/Ttobak.app

# Option 2: Allow in System Settings
#   System Settings → Privacy & Security → "Ttobak was blocked" → Open Anyway
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

## Frontend integration

The SPA detects Tauri via `isTauri()` (`frontend/src/lib/tauri.ts`) and routes
the record button through native commands:

```ts
// frontend/src/lib/tauri.ts
export const isTauri = () => "__TAURI_INTERNALS__" in window;
export function startNativeRecording(meetingId: string): Promise<StartResponse>;
export function stopNativeRecording(): Promise<StopResponse>;
export function readRecordingBytes(path: string): Promise<ArrayBuffer>;
export function cleanupRecording(path: string): Promise<void>;
```

`RecordButton` picks `startNativeRecording` over `getUserMedia` when
`audioSource === 'system'` (only shown when `isTauri()` is true).

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
