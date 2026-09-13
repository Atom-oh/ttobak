# Historical implementation record: Browser tab audio capture

- Original plan date: 2026-04-20.
- Historical proposal, not an execution checklist or current review mandate. Source pointers checked 2026-09-13.

## Scope and design

Add a browser-tab source for remote-meeting audio through `getDisplayMedia`, reusing MediaRecorder, checkpoints, upload, and the Transcribe Streaming path. The initial source selector had `mic`/`tab`; it hid microphone selection/preview in tab mode and displayed the selected tab's label. No backend changes were proposed.

Capability detection targeted desktop Chrome/Edge. Picker cancellation returned to idle, a stream without audio was rejected, unused video tracks were stopped, and externally ending tab sharing stopped recording. `SttManager` and the recording hook already accepted a supplied `MediaStream`, avoiding a second capture subsystem.

## Risks and intended validation

The supplied stream contract does not make every speech provider source-agnostic: Web Speech can open its own microphone. Browser support and the user's share-audio selection remain prerequisites. Intended manual cases included successful tab capture/captions/upload, picker cancellation, missing audio, external stop-sharing, microphone-mode regression, and hiding unsupported choices. Frontend builds were planned; the checklist was unchecked and no completed checks or deployment were recorded.

## Current references

- [Capture implementation](../../../frontend/src/components/RecordButton.tsx), [capability checks](../../../frontend/src/lib/device.ts), [source UI](../../../frontend/src/app/record/page.tsx), [STT manager](../../../frontend/src/lib/sttManager.ts).
- [ADR-006](../../decisions/ADR-006-tab-audio-capture-and-tauri-mac-app.md) separates browser-tab and native system capture. [ADR-024](../../decisions/ADR-024-mac-app-native-streaming-upload-and-system-audio-captions.md) adds native WAV transport/PCM captions; [ADR-030](../../decisions/ADR-030-mobile-live-captions-never-sacrifice-recording.md) governs mobile fallback and recovery. Those later paths do not follow this original two-option UI literally.
