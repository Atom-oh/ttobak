# ADR-024: Native WAV upload and system-audio captions

- Status: Accepted; amends ADR-006 by moving WAV transport from the SPA to Rust. Auth, presigning, and upload-complete notification remain in the SPA.
- Original decision date: Not recorded. Addenda: 2026-07-31 (PR #145), 2026-08-29 and 2026-09-02 (PR #169).
- Code checked: 2026-09-13; installed Mac binary behavior was not tested.

## Original decision and rationale

A roughly 35-minute, 401 MB WAV crashed the remote WebView when `read_recording_bytes` returned it through Tauri IPC as a giant JavaScript array. Native capture was not the hung component. Separately, the remote origin lacked event capability access, suppressing the waveform. Native streaming fixes transport without replacing the existing authenticated upload workflow.

## Current behavior and invariants

- `read_recording_bytes` is removed. `upload_recording` streams disk-to-S3 with an explicit `Content-Length`; complete WAVs must never cross the WebView bridge. Small base64 PCM chunks **do** cross it: 16 kHz mono, about 64 ms per chunk, for best-effort captions through `startNative`/`pushChunk`.
- Upload URL validation requires HTTPS and the **exact** `EXPECTED_BUCKET_HOST`: `ttobak-assets-180294183052.s3.ap-northeast-2.amazonaws.com`. A generic AWS-domain suffix would admit another tenant's bucket. Local paths must pass containment and the recording whitelist.
- The remote capability is restricted to the application origin. System-audio capture excludes the user's microphone and has no Web Speech fallback; caption failure must not discard the recording.
- `begin_start` reserves the recorder through an RAII `StartGuard`. Start/stop release the recorder lock before blocking ScreenCaptureKit calls. Stop removes the handle and inserts its canonical path into the finalizing set in the **same critical section**.
- Stop returns `stop_timed_out` after ten seconds while the blocking task continues. Finalization occurs if/when that task returns; a timeout does not certify a finished WAV. Upload checks the path-specific finalization state, and the SPA polls `finalizing_for_path` rather than a global flag. Lexical containment precedes canonicalization in the status helper.
- WAV checkpoints flush about every five seconds. Graceful exit attempts finalization; SIGKILL cannot. Upload uses a 60-second no-read-progress timeout and a separate 180-second full-body-response deadline, not an unbounded wait or a fixed whole-upload timeout.
- The SPA retains pending audio until upload-complete notification succeeds. `putDone` retries notification without another PUT. Each native retry gets a fresh presign; offline/backoff waiting shares a 45-minute budget, which does not preempt an already-running PUT. Generation checks prevent abandoned flows from updating newer UI state, not from undoing S3 writes already made.

## Dated amendments and accepted residual risks

**2026-07-31:** Add an empty-path command preflight before recording and network-aware retries after an old binary failed only at upload time. The preflight is an error-message heuristic, backed by a Rust contract test; it is not full binary/API compatibility validation.

**2026-08-29 / 2026-09-02:** Adopt regular leftover WAVs on startup, excluding symlinks; best-effort delete files whose known age is at least 48 hours. Unknown/future mtimes may be adopted, and retention is not a continuous sweep. `/record` lists only startup-adopted paths; each upload/delete requires file-specific confirmation naming the cross-account caveat.

Leftovers are scoped to the OS user's temporary directory, **not Cognito identity**. Another Cognito user sharing that OS account can confirm access to someone else's leftover. Confirmation mitigates but does not close this accepted risk. There is also no single-instance guard; a second instance may adopt an active first instance's WAV. Startup zero-callback detection, durable storage outside temporary directories, and identity-bound leftovers remain follow-ups. Upload retry is whole-file, not byte-range resume.

## Evidence

- [Commands/state](../../mac-app/src-tauri/src/lib.rs), [capture/RAII tests](../../mac-app/src-tauri/src/audio.rs), [upload/host/deadline tests](../../mac-app/src-tauri/src/upload.rs), [leftover tests](../../mac-app/src-tauri/src/leftover.rs).
- [Remote capability](../../mac-app/src-tauri/capabilities/default.json), [preflight/retry](../../frontend/src/lib/tauri.ts), [upload lifecycle](../../frontend/src/hooks/usePostRecording.ts), [confirmation UI](../../frontend/src/app/record/page.tsx).

The Mac module has no CI coverage. Source inspection does not substitute for the documented local macOS format, clippy, and test checks when changing native code.
