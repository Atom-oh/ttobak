# ADR-001: System Audio Capture for Remote Meetings

- Status: Superseded by [ADR-006](ADR-006-tab-audio-capture-and-tauri-mac-app.md).
- Decision date: Not recorded in the original ADR.
- Implementation checked: 2026-09-13.

## Context and original decision

Microphone-only recording missed remote participants played through headphones.
The original decision proposed capturing display/tab audio with `getDisplayMedia`
and mixing it with microphone input through Web Audio. A user-configured virtual
audio device was the fallback. This avoided separate conferencing-platform
integrations and reused the existing recording/upload pipeline.

## Current implementation

ADR-006 replaced the universal browser-capture proposal with browser tab capture
and a Tauri macOS wrapper for native system audio. `RecordButton` selects either
the microphone or the selected tab stream; the tab branch does **not** acquire and
mix a separate microphone stream. It stops the display video tracks and rejects a
selection that supplies no audio track.

Native system capture uses ScreenCaptureKit. Finished recordings upload directly
from disk in Rust, as specified by
[ADR-024](ADR-024-mac-app-native-streaming-upload-and-system-audio-captions.md).
The old browser support matrix and mixing sketch were proposals, not current
compatibility guarantees or implementation requirements.

## Rationale, alternatives, and tradeoffs

Browser capture minimized initial implementation work. Virtual loopback devices
required manual installation and routing; platform SDKs required separate
integrations; a desktop client added native maintenance. ADR-006 later accepted
that desktop cost to cover macOS conferencing applications.

Capture still requires user permission and depends on the selected source.
Tab recording can omit the local speaker when that voice is absent from the tab's
output. System capture can include unrelated application sounds. A combined audio
track does not provide independent participant channels.

## Evidence

- [RecordButton.tsx](../../frontend/src/components/RecordButton.tsx): microphone,
  tab, and native recording branches.
- [device.ts](../../frontend/src/lib/device.ts): `supportsTabAudioCapture`.
- [audio.rs](../../mac-app/src-tauri/src/audio.rs): native recorder.
