# Browser Tab Audio Capture Design

> Historical design record. Original date: 2026-04-20 (filename); no original status
> was recorded. This was browser sub-project 1, not the native Mac application design.

## Goal and boundaries

Let users record a browser meeting's tab audio through the existing recording,
upload, and live-caption pipeline. The design proposed a Microphone/Tab Audio
selector, capability-based hiding, and clean handling of picker cancellation,
missing audio tracks, and sharing that ends externally. Desktop-app system audio,
screen video, and tab/microphone mixing were outside this sub-project.

## Proposed interaction and data flow

Tab mode requested display capture with audio and a minimal video track, then
stopped the video tracks. The audio stream supplied MediaRecorder and Transcribe
Streaming. The microphone device selector was hidden in tab mode; the UI explained
the picker and showed the selected capture state. Ending capture stopped recording
and entered the ordinary post-recording upload flow.

Reusing downstream processing minimized frontend changes and required no new
backend capture endpoint. The original sketch used Web Audio routing and fixed
codec/checkpoint details; those were not additional requirements for an equivalent
stream path. Its browser/version list was an intended test matrix, not a current
support guarantee.

## Risks and validation intent

The source contradicted itself: it correctly said the local microphone was absent
from tab output, then claimed mixing was unnecessary because that microphone was
included. The latter claim was incorrect. Capturing remote voices does not guarantee
capture of the local speaker, and a selected surface may supply no audio at all.

The intended checks covered cancellation without an error, no-audio feedback,
external capture stop, microphone regression, unsupported/mobile UI, and keeping
recording alive when captions fail. Background recording was an expectation to
verify, not an unconditional browser guarantee.

## Current evidence and successors

[RecordButton](../../../frontend/src/components/RecordButton.tsx) implements separate
mic/tab/native branches. Its tab branch does not mix a second microphone stream;
it stops video tracks, checks for audio, and handles track termination.
[device.ts](../../../frontend/src/lib/device.ts) supplies capability detection.
[ADR-006](../../decisions/ADR-006-tab-audio-capture-and-tauri-mac-app.md) and
[ADR-024](../../decisions/ADR-024-mac-app-native-streaming-upload-and-system-audio-captions.md)
cover the native alternative; [ADR-030](../../decisions/ADR-030-mobile-live-captions-never-sacrifice-recording.md)
protects recording when live captions fail. This historical record does not prove
any browser/device combination was tested today.

Current references: [documentation map](../../README.md) and
[architecture](../../architecture.md).
