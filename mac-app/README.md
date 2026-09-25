# TTOBAK for macOS

Tauri 2 wrapper around the existing TTOBAK web app, adding native capture of
system audio (Core Audio process tap) mixed with the default microphone, on macOS
14.2 or later. Login remains in the web app. The mix is recorded to a WAV on disk;
Rust streams finished uploads directly to a signed S3 URL. Small PCM events provide
live captions through the existing browser Transcribe client.

## Build

Use macOS with the Apple/Rust/Tauri toolchains:

```bash
npm ci
npm run dev
npm run build:signed
```

Use build:signed so microphone/camera entitlements are applied. The first
recording asks for Microphone and System Audio Recording permission; a new ad-hoc
build may require granting them again (`tccutil reset Microphone click.atomai.ttobak.mac`
and `tccutil reset AudioCapture click.atomai.ttobak.mac`). On macOS 14.2/14.3,
confirm the System Audio Recording prompt appears (ADR-046). Test a short recording with headphones after
installation: both your voice and the other participants' must be audible. This
module is built/tested locally and has no CI coverage.

```bash
(cd src-tauri && cargo fmt --check && cargo clippy --all-targets -- -D warnings && cargo test)
```

## MCP control

A same-user process, typically the stdio TTOBAK MCP server, can start and stop
recordings through the app's local socket
(`~/Library/Application Support/ttobak/control.sock`). The app must be open and
signed in; the window comes forward and a notification names each MCP-started
recording. See [ADR-046](../docs/decisions/ADR-046-mac-core-audio-tap-and-microphone.md).

## Recovery and limits

Wait for upload completion before closing the lid. Idle-sleep prevention does not
block explicit/lid-close sleep. Upload failures retain the WAV for retry; delete
only after the application's upload-complete step succeeds.

Startup recovers regular WAVs and best-effort removes files with a known age of
at least 48 hours. Unreadable/future timestamps may still be adopted; this is not
a hard retention cutoff. These leftovers belong to the
macOS user directory, not a specific TTOBAK login. Confirm the file before uploading
or deleting it on a shared Mac. Force Quit may leave only the last flushed audio;
graceful exit has a separate finalization path.

Native mode records system audio and the default microphone together. Without an
input device it records system audio only and says so in the start warning; a
silent source (usually a denied permission) is reported when recording stops.
Browser microphone recording remains a separate mode.
Real Developer ID distribution/notarization is not established by ad-hoc signing.

See [developer guidance](CLAUDE.md),
[ADR-024](../docs/decisions/ADR-024-mac-app-native-streaming-upload-and-system-audio-captions.md),
[ADR-046](../docs/decisions/ADR-046-mac-core-audio-tap-and-microphone.md),
and the actual remote URL/capabilities in src-tauri configuration.
