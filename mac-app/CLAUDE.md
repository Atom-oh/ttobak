# macOS native module

Tauri 2/Rust wraps the remote TTOBAK SPA and supplies ScreenCaptureKit system-audio
capture. Auth, presigning, upload-complete and retry orchestration remain in the
SPA. Rust owns finished WAV transport, not Cognito tokens. See ADR-006/024.

## Build and validation

Run in `mac-app/` on macOS:

```bash
npm ci
npm run dev
npm run build:signed
(cd src-tauri && cargo fmt --check && cargo clippy --all-targets -- -D warnings && cargo test)
```

No CI covers this module. Plain `npm run build` does not apply the required audio
entitlements through the project's signing script; use build:signed, or sign after
building. Re-grant TCC permissions if a rebuilt ad-hoc signature invalidates them,
then test a short recording. Current codesign --deep is accepted for ad-hoc signing;
real Developer ID notarization needs explicit nested signing.

ScreenCaptureKit is macOS-gated. Linux can build Tauri only with its native GUI
dependencies; on hosts lacking those, a scratch crate can test Tauri-free portions
of error.rs/audio.rs/leftover.rs/power.rs. That does not validate IOKit or capture.
Keep pure helpers such as interleave_planes outside macOS-only modules.

## Wire contract

| Command | Contract |
|---|---|
| start_recording | meeting_id; returns temp_path |
| stop_recording | returns temp_path, duration_ms, byte_size, stop_timed_out |
| recording_status | path; recording, temp_path, elapsed_ms, finalizing_for_path |
| upload_recording | path, uploadUrl, contentType; returns HTTP status code |
| cleanup_recording | Validated inactive/finalized path; removes WAV and adopted entry |
| release_recording_power | Releases only the path's idle-sleep protection, preserving recovery data |
| list_leftover_recordings | Startup-adopted inactive WAVs, newest first; not current-session recordings |

finalizing_for_path is deliberately a new per-path field, not the older global
finalizing flag. Frontend wire compatibility checks rely on that distinction.
Events are native-audio-level (RMS), native-upload-progress (loaded/total), and
native-pcm-chunk (16kHz mono PCM for captions). Remote event capability grants must
match the actual loaded origin; do not add speculative origins.

## Invariants

- Finished WAV data streams disk-to-S3 in Rust; no read_recording_bytes/bulk IPC
  response. A former 400MB JS array delivery crashed JavaScriptCore. Small live
  PCM events intentionally cross IPC and reuse browser Transcribe Streaming;
  native mode has no Web Speech microphone fallback.
- Pin EXPECTED_BUCKET_HOST exactly; an amazonaws.com suffix would admit another
  customer's bucket. Validate path/recording membership server-side.
- upload.rs bounds no-progress streaming at 60 seconds, then separately bounds
  waiting for the response after full send at 180 seconds. Do not replace this
  with a fixed total upload timeout or an unbounded final phase.
- Never hold recorder locks across blocking ScreenCaptureKit FFI. StartGuard's
  RAII drop clears abandoned starts; do not drop it while retaining the same
  non-reentrant lock.
- stop_recording marks the path finalizing inside the same critical section as
  taking its handle. upload_recording rejects active/finalizing paths. Status
  checks contain(path), not whether any recording is finalizing.
- recording_status checks lexical containment before canonicalization and checks
  resolved containment afterward. Out-of-tree status queries must not become
  file-existence probes.
- Detect/interleave planar and interleaved buffers; do not assume the layout from
  one device. System audio excludes the app's own sound. Capture needs a display
  even when recording audio only; source-specific logic stays in audio.rs.
- Graceful exit finalizes capture. Force Quit/SIGKILL cannot run that callback;
  periodic WAV flush checkpoints and startup recovery limit damage instead.

## Leftovers and power

Startup adopts regular WAV files under allowed_dir(), rejects symlinks, and purges
files whose known age is at least 48 hours at startup (not continuously).
Unreadable/future modification times may be adopted. Both recorded_paths and
adopted_paths are needed. The UI lists only adopted leftovers and requires per-file
confirmation naming the cross-account caveat. Files are scoped to a macOS user,
not the Cognito user who recorded them; account binding remains an accepted gap.

Idle-sleep assertions are keyed by canonical path under shared ownership. One
recording's cleanup must not release another's protection. Failed uploads preserve
retry data; cleanup releases protection before deletion, while release_recording_power
allows navigation/reset without deleting the WAV. Each upload attempt also owns an
RAII assertion, including startup-adopted files. Invalid paths do not clear state.

Protection covers idle system sleep, not lid-close, explicit sleep or low battery;
display sleep is allowed. Validate pmset assertions during recording, pending upload,
recovered upload and cleanup on real macOS. Late callbacks after page unmount must
release their own pending protection without corrupting another recording.

Tauri config CSP does not protect the remote SPA response; use the served policy.
Auth and tokens remain in the SPA. Source pointers: src-tauri/src/lib.rs, audio.rs,
upload.rs, leftover.rs, power.rs; frontend src/lib/tauri.ts and recording hooks.
