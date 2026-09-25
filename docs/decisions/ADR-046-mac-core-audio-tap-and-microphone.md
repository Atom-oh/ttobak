# ADR-046: Mac Core Audio tap capture with microphone mix

- Status: Accepted for implementation; native capture needs macOS verification.
- Decision date: 2026-09-25.
- Supersedes: ADR-006's ScreenCaptureKit capture engine and ADR-024's
  "system-audio capture excludes the user's microphone". ADR-024's transport,
  finalization and leftover rules remain in force.

## Context

ScreenCaptureKit needs Screen Recording permission and a display even for
audio-only capture. It also records only other participants: with headphones,
the user's own voice was missing from Zoom, Teams and Meet recordings.
macOS 14.2 added Core Audio process taps, which need only the narrower
"System Audio Recording" permission.

## Decision

On macOS 14.2 or later, the Mac app records system audio and the microphone in
one private aggregate device:
- The tap is a stereo global tap that excludes the app's own process.
- The default input device is the clock-owning sub-device.
- The tap is attached with drift compensation.
- With no input device, the default output device clocks a system-audio-only
  recording, and start returns a warning.

The IOProc input list holds sub-device streams first and tap streams after
them. `ChannelLayout` records this order, and the pure `mix.rs` functions split
the channels, average the microphone into both channels of the system stereo
signal, soft-clip, and resample captions to 16 kHz with phase carried across
buffers.

The IOProc runs on Core Audio's real-time thread. It only mixes and enqueues
into a bounded channel; a worker owns WAV writes, five-second checkpoints and
Tauri events. A full queue drops buffers, and the number dropped is reported
as a stop warning. The WAV rate is the aggregate's nominal rate.

Teardown order is fixed:
1. `AudioDeviceStop`, which waits for any in-flight IOProc.
2. Destroy the IOProc, then the aggregate, then the tap.
3. Close the channel, join the worker, and finalize the WAV.

Start and stop responses gain an additive `warnings` field: no microphone,
silent system audio or microphone (typically a denied permission), and dropped
buffers. Zero callbacks and zero samples remain hard failures. The minimum macOS
version becomes 14.2, and ScreenCaptureKit and its Screen Recording usage
string are removed.

## Verification and limits

Mixing and resampling are unit tested on Linux. The macOS module
type-checks against the objc2 Core Audio bindings, but only a signed macOS
build validates the permission prompts, the device topology and the audio.
Check a headphone call records both voices, a denied permission produces the
silence warning, and removing the input device falls back to system audio only.

A default input device changed mid-recording is not followed. Bluetooth
headsets used as input may switch to their lower-quality hands-free profile.

## References

- [Capture backend](../../mac-app/src-tauri/src/audio.rs)
- [Mixing and resampling](../../mac-app/src-tauri/src/mix.rs)
- [ADR-024](ADR-024-mac-app-native-streaming-upload-and-system-audio-captions.md)
