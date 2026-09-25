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
silent system audio or microphone (typically a denied permission), dropped
buffers, an unidentified own process, or an unreadable sample rate. The record
page shows them; zero callbacks and zero samples remain hard failures. The
worker emits events only while its recording generation is current, and a
failed stop or IOProc destroy leaks the callback context rather than freeing
memory a late callback could reach. The minimum macOS
version becomes 14.2, and ScreenCaptureKit and its Screen Recording usage
string are removed.

## Local control channel

A stdio MCP server (or any same-user process) can start and stop an app
recording through `~/Library/Application Support/ttobak/control.sock`:
- The directory is 0700 and the socket 0600. The peer uid must equal the app's
  uid, and a browser cannot reach a Unix socket.
- Each connection carries one JSON request line (at most 16 KiB, 5-second read
  timeout, at most 8 concurrent connections) and gets one JSON response line.
- A stale socket is removed only after a failed connect probe; a non-socket
  path is never deleted.

Rust gains no meeting or auth state:
- `start`/`stop` go to the signed-in SPA as a `native-control-request` event.
- The SPA answers through `control_reply` and reports readiness
  (`control_ready`) and its phase (`control_report_state`). Replies and state
  are validated and bounded before being forwarded.
- `status` combines that phase with the native recorder snapshot.

A start reuses the record page's normal native path (draft meeting, capture,
captions). It succeeds once capture runs, focuses the window, and posts a
notification. A stop uploads by default through the notes-skip path. Busy,
signed-out and not-ready states are explicit errors; a timeout is reported as
an unknown outcome.

## Verification and limits

Mixing and resampling are unit tested on Linux. The macOS module
type-checks against the objc2 Core Audio bindings, but only a signed macOS
build validates the permission prompts, the device topology and the audio.
Check a headphone call records both voices, a denied permission produces the
silence warning, and removing the input device falls back to system audio only.
Also confirm macOS 14.2/14.3 show the System Audio Recording prompt for
`NSAudioCaptureUsageDescription`; if they do not, raise the minimum version to
the first release that does.

A default input device changed mid-recording is not followed. Bluetooth
headsets used as input may switch to their lower-quality hands-free profile.

## References

- [Capture backend](../../mac-app/src-tauri/src/audio.rs)
- [Mixing and resampling](../../mac-app/src-tauri/src/mix.rs)
- [ADR-024](ADR-024-mac-app-native-streaming-upload-and-system-audio-captions.md)
