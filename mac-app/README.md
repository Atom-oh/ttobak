# TTOBAK for macOS

Tauri 2 wrapper around the existing TTOBAK web app, adding native ScreenCaptureKit
system-audio capture. Login remains in the web app. System audio is recorded to a
WAV on disk; Rust streams finished uploads directly to a signed S3 URL. Small PCM
events provide live captions through the existing browser Transcribe client.

## Build

Use macOS with the Apple/Rust/Tauri toolchains:

```bash
npm ci
npm run dev
npm run build:signed
```

Use build:signed so microphone/camera entitlements are applied. A new ad-hoc build
may require re-granting Screen Recording permission. Test a short recording after
installation. This module is built/tested locally and has no CI coverage.

```bash
(cd src-tauri && cargo fmt --check && cargo clippy --all-targets -- -D warnings && cargo test)
```

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

System audio is the implemented native capture mode. Native microphone mixing is
not implied by the wrapper; browser microphone recording is a separate mode.
Real Developer ID distribution/notarization is not established by ad-hoc signing.

See [developer guidance](CLAUDE.md),
[ADR-024](../docs/decisions/ADR-024-mac-app-native-streaming-upload-and-system-audio-captions.md),
and the actual remote URL/capabilities in src-tauri configuration.
