# ADR-030: Mobile captions must not interrupt recording

- Status: Accepted; PR #160 follows PR #153. Complements ADR-024's native-audio path.
- Decision date: 2026-08-19. Recovery details below reflect later code, without an independently recorded amendment date.
- Code checked: 2026-09-13; no physical-device verification in this documentation pass.

## Original decision and rationale

Moving iOS to normal MediaRecorder fixed a file-input path that opened the camera, but exposed Web Speech taking a second microphone capture and ending the recording track. Make AWS Transcribe Streaming the recording page's default and disable competing Web Speech capture on mobile. Losing captions is preferable to losing the recording.

## Current behavior and invariants

- The recording page defaults to Transcribe Streaming on all platforms. `hasMobileMicConflictRisk` uses iOS/iPadOS/Android device signals rather than window width; mobile Web Speech selection/fallback is blocked. Missing configuration or failure shows a caption warning while recording continues. Desktop explicit selection/fallback remains available.
- Mobile skips the idle microphone preview's extra `getUserMedia`. The streaming AudioContext uses its hardware-selected rate; the worklet downsamples to 16 kHz instead of forcing 48 kHz.
- `retryWithConfig` promotes a waiting browser recording when configuration arrives. While paused it stores configuration and waits for resume. Stopped sessions and explicit desktop Web Speech choices must not be silently restarted/promoted.
- Current recovery includes wake-lock reacquisition and audio-context resume attempts on visibility/focus return, a PCM stall watchdog for captions, and a separate waveform watchdog. `SttManager` gives one automatic reconnect grace per recording for streaming stall/error signals; it can defer reconnect until visible.
- Automatic recovery can still be blocked by browser user-activation policy. The manual retry invokes `resumeAudio()` and `manualStallRecovery()` synchronously from the button, **before any `await`**. Waveform-only recovery has its own button. Do not remove these paths as redundant with automatic recovery.
- Recovery messages clear on evidence: a reconnected caption session's first transcript, or waveform watchdog confirmation, rather than merely starting another AudioContext. Actual recorder/track failure is surfaced separately from optional caption failure.

## Tradeoffs and accepted residual risks

Mobile without working Transcribe Streaming has no Web Speech fallback. Audio contexts and browsers can still suspend, so uninterrupted captions are not guaranteed. Desktop Web Speech can send audio to the browser vendor; the default streaming path sends audio to AWS and changes streaming usage/cost. This decision adds no new IAM capability; it uses the configured authenticated identity-pool permission.

The original five changes did not by themselves solve screen-lock recovery; the implemented automatic and gesture-backed paths above are now authoritative. Frontend validation follows the repository's lint/build workflow, with device testing needed for browser-specific capture behavior.

## Evidence

- [Provider/config/reconnect](../../frontend/src/lib/sttManager.ts), [stream watchdog](../../frontend/src/lib/transcribeStreamingClient.ts), [device check](../../frontend/src/lib/device.ts), [worklet](../../frontend/public/pcm-processor.js).
- [Wake lock/waveform recovery](../../frontend/src/components/RecordButton.tsx), [caption lifecycle](../../frontend/src/hooks/useRecordingSession.ts), [defaults/manual buttons](../../frontend/src/app/record/page.tsx).
