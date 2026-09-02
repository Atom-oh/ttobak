//! TTOBAK Mac App library entry point.
//!
//! Tauri commands exposed to the WebView:
//! - `start_recording(meeting_id)` — begin system-audio capture into a temp WAV
//! - `stop_recording()` — stop capture, finalize the WAV, return its path
//! - `upload_recording(path, upload_url, content_type)` — stream the WAV
//!   straight from disk to a presigned S3 URL (bulk audio bytes never cross
//!   the IPC bridge to the WebView — see `upload.rs` module docs)
//! - `cleanup_recording(path)` — delete a temp WAV file and revoke whitelist entry
//! - `recording_status(path)` — current capture state for the UI, plus
//!   whether `path` specifically is still being finalized
//! - `list_leftover_recordings()` — temp WAVs adopted at startup from a
//!   previous run (crash / force quit), so the SPA can offer to upload or
//!   delete them; see `leftover.rs` and `RecorderState::adopted_paths`
//!
//! `read_recording_bytes` (WAV bytes via IPC binary response) used to exist
//! here and has been deliberately removed: on a real ~35-minute recording it
//! read a 400,949,804-byte WAV into memory and shipped it through Tauri's IPC
//! bridge, which — because this app's WebView loads the remote production SPA
//! rather than a local `tauri://` origin — gets delivered via
//! `evaluateJavaScript` as one giant JS array literal. JavaScriptCore fatally
//! asserted while bytecode-compiling it, killing the WebContent process
//! (surfacing to the user as the whole app freezing). See
//! `docs/decisions/ADR-024-mac-app-native-streaming-upload-and-system-audio-captions.md`.

mod audio;
mod error;
mod leftover;
mod upload;

use std::collections::HashSet;
use std::path::PathBuf;
use std::sync::Arc;
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use parking_lot::Mutex;
use serde::Serialize;
use tauri::{AppHandle, Manager, State};

use crate::audio::{AudioRecorder, StartGuard};
use crate::error::AppError;

/// How long `stop_recording` waits for ScreenCaptureKit's `stop_capture` to
/// return before giving up and reporting `stop_timed_out: true`. The
/// underlying FFI call has no timeout of its own (it blocks on a plain
/// `Condvar`), so without this a wedged stream previously could have hung
/// the command's promise forever.
const STOP_CAPTURE_TIMEOUT: Duration = Duration::from_secs(10);

/// A leftover temp WAV found at startup (see `run()`'s `.setup()`) older
/// than this is deleted outright rather than adopted into `recorded_paths`.
/// Raw meeting audio is PII; a crash this old is treated as abandoned, not
/// recoverable, so it shouldn't accumulate indefinitely in this app's temp
/// directory across every future launch.
const LEFTOVER_RECORDING_MAX_AGE: Duration = Duration::from_secs(48 * 3600);

pub struct RecorderState {
    pub recorder: Mutex<AudioRecorder>,
    pub recorded_paths: Mutex<HashSet<PathBuf>>,
    /// The subset of `recorded_paths` that was adopted at startup from a
    /// PREVIOUS run (see `run()`'s `.setup()` and `leftover.rs`) rather than
    /// created by a `start_recording` in this one. `list_leftover_recordings`
    /// reports exactly this set so the SPA can surface "a recording from an
    /// earlier session is still here" — and ONLY this set: a path this
    /// session created is already known to the SPA (returned from
    /// `start_recording`) and must never be offered as a leftover while its
    /// own upload may be in flight. Kept as a parallel set (invariant:
    /// `adopted_paths ⊆ recorded_paths`) rather than changing
    /// `recorded_paths`' type, because `validate_recording_path` and
    /// `upload_recording` consume the plain whitelist set by reference.
    /// `cleanup_recording` removes from both.
    pub adopted_paths: Mutex<HashSet<PathBuf>>,
    /// Paths of recordings whose `stop_and_finalize` is currently running —
    /// including any that outlived `STOP_CAPTURE_TIMEOUT` and kept going in
    /// the background. `recording_status(path)` reports whether THAT
    /// specific path is in this set as `finalizing`, so the frontend can
    /// wait for its own WAV to actually be finalized after a
    /// `stop_timed_out: true` response: `recording` alone flips false the
    /// moment `take_handle()` empties the recorder, long before the
    /// background finalize is done. `upload_recording` also checks this
    /// directly (by path) as a server-side backstop against uploading a
    /// file that's still being written.
    ///
    /// Deliberately a per-path set, not a single `AtomicBool`: a bool shared
    /// across every recording would let an *earlier* recording's
    /// wedged-past-timeout finalize (which unconditionally cleared it when
    /// it eventually returned) stomp on a *later* recording's
    /// still-in-flight finalize, making status checks falsely report "done"
    /// while that later WAV was still being written — the exact
    /// silent-truncation bug this field exists to prevent. A set is immune
    /// to that: each finalize inserts its own path on entry and removes only
    /// that path on exit, regardless of how many others are concurrently in
    /// flight. This must be checked PER-PATH, though, not by "is the set
    /// non-empty at all" — an earlier, still-wedged recording's entry
    /// staying in the set would otherwise make a caller's own, already-
    /// finished recording look permanently unfinished whenever the two
    /// overlap, which is exactly the scenario this whole mechanism exists to
    /// handle correctly.
    pub finalizing: Arc<Mutex<HashSet<PathBuf>>>,
}

#[derive(Serialize)]
pub struct StartResponse {
    pub temp_path: String,
}

#[derive(Serialize)]
pub struct StopResponse {
    pub temp_path: String,
    pub duration_ms: u64,
    pub byte_size: u64,
    /// True if ScreenCaptureKit's `stop_capture` did not return within
    /// `STOP_CAPTURE_TIMEOUT`. The WAV up to the last periodic flush
    /// checkpoint (see `audio.rs`) is still valid and playable even when
    /// this is true — the frontend should proceed to upload rather than
    /// treat this as a hard failure.
    pub stop_timed_out: bool,
}

#[derive(Serialize)]
pub struct StatusResponse {
    pub recording: bool,
    pub temp_path: Option<String>,
    pub elapsed_ms: u64,
    /// True while the path passed to `recording_status` specifically is
    /// still being finalized (see `RecorderState::finalizing`). After a
    /// `stop_timed_out` stop, the frontend polls until this goes false
    /// before uploading — `recording` is already false at that point and
    /// cannot express "still writing".
    pub finalizing: bool,
}

/// One startup-adopted leftover recording, as reported by
/// `list_leftover_recordings`. `path` is the canonical path — the exact
/// string `upload_recording`/`cleanup_recording` accept.
#[derive(Serialize)]
pub struct LeftoverRecording {
    pub path: String,
    pub file_name: String,
    pub byte_size: u64,
    /// File mtime as Unix epoch milliseconds (0 if unavailable). For a
    /// crash leftover this is roughly when the last flush checkpoint landed.
    pub modified_ms: u64,
}

fn allowed_dir() -> PathBuf {
    let base = std::fs::canonicalize(std::env::temp_dir()).unwrap_or_else(|_| std::env::temp_dir());
    base.join("ttobak-mac")
}

/// Resolve and whitelist-check a recording path. `pub(crate)` so
/// `upload.rs`'s `upload_recording` command can reuse the exact same guard
/// `read_recording_bytes`/`cleanup_recording` already relied on — no new
/// recording-path validation logic, just a new consumer of the existing one.
///
/// The frontend's `assertUploadRecordingAvailable` (`frontend/src/lib/tauri.ts`,
/// ADR-024) has an implicit dependency on this function running first and
/// failing on an empty path: it invokes `upload_recording('', '', '')` as a
/// cheap existence probe, relying on `canonicalize("")` erroring out here
/// before any FS/network work. If this validation is ever reordered to run
/// after something else in `upload_recording`, that probe silently stops
/// meaning what it currently means.
/// See `validate_recording_path`'s doc comment: the frontend's preflight probe
/// treats "any rejection other than command-not-found" as proof the command
/// exists, so an empty path must NOT produce a message matching its
/// `/not found|not allowed|unknown command/i` heuristic. Pinned here because
/// that contract otherwise lives only in comments on both sides.
#[cfg(test)]
mod preflight_contract_tests {
    use super::*;

    #[test]
    fn empty_path_rejection_is_not_mistaken_for_a_missing_command() {
        let err =
            validate_recording_path("", &HashSet::new()).expect_err("empty path must be rejected");
        let message = err.to_string().to_lowercase();
        for needle in ["not found", "not allowed", "unknown command"] {
            assert!(
                !message.contains(needle),
                "rejection message {message:?} contains {needle:?}, which the frontend's \
                 isCommandNotFound() would misread as a version skew",
            );
        }
    }
}

pub(crate) fn validate_recording_path(
    path: &str,
    recorded_paths: &HashSet<PathBuf>,
) -> Result<PathBuf, AppError> {
    let canonical = std::fs::canonicalize(path)
        .map_err(|e| AppError::Io(format!("canonicalize {path}: {e}")))?;
    let allowed = allowed_dir();
    if !canonical.starts_with(&allowed) {
        return Err(AppError::Permission(
            "path is outside the recording directory".into(),
        ));
    }
    if !recorded_paths.contains(&canonical) {
        return Err(AppError::Permission(
            "path was not created by this recording session".into(),
        ));
    }
    Ok(canonical)
}

#[tauri::command]
async fn start_recording(
    meeting_id: String,
    state: State<'_, RecorderState>,
    app: AppHandle,
) -> Result<StartResponse, AppError> {
    // Reserve the slot (cheap, non-blocking) and let `state.recorder`'s lock
    // go BEFORE the blocking ScreenCaptureKit FFI below — see
    // `AudioRecorder::begin_start`'s doc comment for the bug this closes
    // (holding the lock across a permission-dialog-length block used to
    // freeze `recording_status`, a sync main-thread command, for as long as
    // the dialog was up). Mirrors the shape `stop_recording` already uses
    // on the stop path.
    let reservation = {
        let mut rec = state.recorder.lock();
        rec.begin_start()?
    };
    // From here until `install()` succeeds, EVERY exit — an early `?`/`return
    // Err`, a panic, or this future being dropped at the `.await` below —
    // must release the reservation, or `starting` stays true forever and
    // every later start fails `AlreadyRunning`. The guard does that on drop;
    // the success path disarms it after `install()`. Never hold
    // `state.recorder`'s lock across a point where the guard could drop
    // (parking_lot is not reentrant) — every lock here is a temporary.
    let guard = StartGuard::new(&state.recorder);

    let path = audio::recording_path(&meeting_id)?;

    #[cfg(target_os = "macos")]
    {
        let path_for_task = path.clone();
        let build = tauri::async_runtime::spawn_blocking(move || {
            audio::macos::Backend::start(
                &path_for_task,
                app,
                reservation.generation,
                reservation.generation_counter,
            )
        })
        .await;

        let backend = match build {
            Ok(Ok(backend)) => backend,
            Ok(Err(e)) => return Err(e),
            Err(join_err) => {
                return Err(AppError::Backend(format!(
                    "start_capture task panicked: {join_err}"
                )));
            }
        };

        state.recorder.lock().install(path.clone(), backend);
        guard.disarm();

        let canonical = std::fs::canonicalize(&path).unwrap_or(path);
        state.recorded_paths.lock().insert(canonical.clone());
        return Ok(StartResponse {
            temp_path: canonical.to_string_lossy().into_owned(),
        });
    }

    // On non-macOS builds, the block above doesn't exist, so this is the
    // whole remaining function body — not dead code following an
    // unconditional early return, which the previous shape (a shared tail
    // after two cfg branches, one of which always returned) triggered an
    // `unreachable_code` lint for on this platform.
    #[cfg(not(target_os = "macos"))]
    {
        let _ = (&app, &reservation, &path, &guard);
        // `guard` drops here and cancels the reservation.
        Err(AppError::Unsupported)
    }
}

#[tauri::command]
async fn stop_recording(state: State<'_, RecorderState>) -> Result<StopResponse, AppError> {
    // Take the handle out AND mark its path `finalizing`, in the SAME
    // critical section — then let the `state.recorder` lock go BEFORE the
    // blocking, no-timeout-of-its-own `stop_capture()` FFI call. Holding
    // that lock across the FFI call used to mean a wedged ScreenCaptureKit
    // stop could block every other command needing `RecorderState.recorder`
    // (notably `recording_status`, which runs on the app's main thread).
    //
    // Doing the `finalizing` insert here — while `state.recorder`'s lock is
    // still held — closes a TOCTOU window a review caught: the previous
    // version released this lock right after `take_handle()` and only
    // inserted into `finalizing` afterward. In that gap, `upload_recording`
    // could acquire `state.recorder.lock()` (observing `recording: false`,
    // since the handle was already taken) AND find `finalizing` still
    // empty, passing both of its backstop checks against a WAV that hadn't
    // actually finished being written. Inserting under the SAME lock
    // guarantees — via Rust's Mutex acquire/release ordering — that any
    // thread which later observes `recording: false` here has also already
    // observed this insert, because it cannot acquire `state.recorder.lock()`
    // until this critical section releases it.
    let (handle, finalize_path) = {
        let mut rec = state.recorder.lock();
        let handle = rec.take_handle()?;
        // Canonicalize before inserting: `handle.path` is the raw temp path
        // (e.g. under `/tmp` on macOS, which symlinks to `/private/tmp` —
        // the same reason `allowed_dir()`/`validate_recording_path`
        // canonicalize elsewhere in this file). `upload_recording`'s
        // backstop compares against this set using the CANONICAL path it
        // already computed via `validate_recording_path` — without
        // canonicalizing here too, that comparison would never match on a
        // real Mac and the backstop would silently do nothing.
        let finalize_path =
            std::fs::canonicalize(&handle.path).unwrap_or_else(|_| handle.path.clone());
        state.finalizing.lock().insert(finalize_path.clone());
        (handle, finalize_path)
    };
    let duration_ms = handle.started_at.elapsed().as_millis() as u64;
    let path = handle.path;

    #[cfg(target_os = "macos")]
    let stop_timed_out = {
        let backend = handle.backend;
        // Let the blocking task itself clear `finalizing` on completion —
        // that way the set stays accurate on the timed-out path too, where
        // this command returns while the task keeps running in the
        // background. A per-path set, not a single shared bool: two
        // overlapping stops (this one, plus an earlier one that's still
        // wedged past its own timeout) must not let either's completion
        // make `recording_status`/`upload_recording` falsely treat the
        // OTHER one's path as done — see `RecorderState::finalizing`'s doc
        // comment.
        let finalizing = Arc::clone(&state.finalizing);
        let stop_task = tauri::async_runtime::spawn_blocking(move || {
            let result = backend.stop_and_finalize();
            finalizing.lock().remove(&finalize_path);
            result
        });

        match tokio::time::timeout(STOP_CAPTURE_TIMEOUT, stop_task).await {
            Ok(Ok(Ok(()))) => false,
            Ok(Ok(Err(e))) => return Err(e),
            Ok(Err(join_err)) => {
                return Err(AppError::Backend(format!(
                    "stop_capture task panicked: {join_err}"
                )));
            }
            Err(_elapsed) => {
                // The spawned blocking task is NOT cancelled by the timeout —
                // it keeps running (and will still call `finalize_writer`
                // whenever/if `stop_capture` eventually returns). We just
                // stop waiting for it so the command's promise always
                // settles.
                log::warn!(
                    "stop_capture did not return within {:?} — reporting \
                     stop_timed_out=true. The WAV up to the last periodic \
                     flush checkpoint is already valid on disk; finalize \
                     will still run in the background if/when \
                     ScreenCaptureKit's stop eventually completes.",
                    STOP_CAPTURE_TIMEOUT
                );
                true
            }
        }
    };

    #[cfg(not(target_os = "macos"))]
    let stop_timed_out = false;

    let byte_size = std::fs::metadata(&path).map(|m| m.len()).unwrap_or(0);

    Ok(StopResponse {
        temp_path: path.to_string_lossy().into_owned(),
        duration_ms,
        byte_size,
        stop_timed_out,
    })
}

#[tauri::command]
fn recording_status(path: String, state: State<'_, RecorderState>) -> StatusResponse {
    let snapshot = {
        let rec = state.recorder.lock();
        rec.snapshot()
    };
    // The recorder lock is already dropped by this point — this command
    // exists specifically so it never blocks (see `AudioRecorder::
    // begin_start`'s doc comment), and `std::fs::canonicalize` below is a
    // blocking syscall that has no reason to run while still holding it.
    //
    // Report `finalizing` for THIS specific path, not "is any recording
    // anywhere still finalizing" — the previous any-path aggregate let an
    // unrelated, still-wedged-past-timeout stop from an EARLIER recording
    // keep a LATER recording's own (already-finished) finalize looking
    // incomplete forever, so the frontend's post-timeout poll would exhaust
    // all its retries in exactly the overlapping-stop scenario this whole
    // mechanism exists to handle. `path` is whatever raw string the WebView
    // passed back, so containment is enforced BEFORE any filesystem access
    // (see `leftover::finalizing_for_path` — without that, this sync command
    // was a file-existence oracle for arbitrary paths).
    let finalizing = {
        let set = state.finalizing.lock();
        leftover::finalizing_for_path(&path, &allowed_dir(), &set)
    };
    StatusResponse {
        recording: snapshot.recording,
        temp_path: snapshot.path.map(|p| p.to_string_lossy().into_owned()),
        elapsed_ms: snapshot.elapsed_ms,
        finalizing,
    }
}

#[tauri::command]
async fn cleanup_recording(path: String, state: State<'_, RecorderState>) -> Result<(), AppError> {
    let canonical = validate_recording_path(&path, &state.recorded_paths.lock())?;
    tokio::fs::remove_file(&canonical)
        .await
        .map_err(|e| AppError::Io(format!("remove {}: {e}", canonical.display())))?;
    state.recorded_paths.lock().remove(&canonical);
    state.adopted_paths.lock().remove(&canonical);
    log::info!("cleaned up recording: {}", canonical.display());
    Ok(())
}

/// Leftover recordings adopted at startup from a previous run, newest first.
///
/// Reports ONLY `adopted_paths` (see its doc) — never a path this session's
/// own `start_recording` created. Defensive filters: a path still in
/// `finalizing` or currently being recorded is skipped (adopted files never
/// go through `stop_recording`, so this can't actually happen today — it
/// guards the invariant rather than a live case), and a path whose file has
/// since vanished (deleted by hand in Finder) is dropped from both sets so it
/// isn't offered again. Sync command: a handful of `stat` calls, no FFI, and
/// no lock is held across them.
#[tauri::command]
fn list_leftover_recordings(state: State<'_, RecorderState>) -> Vec<LeftoverRecording> {
    let adopted: Vec<PathBuf> = state.adopted_paths.lock().iter().cloned().collect();
    let active = {
        let rec = state.recorder.lock();
        rec.snapshot().path
    };
    let mut out = Vec::with_capacity(adopted.len());
    let mut vanished = Vec::new();
    for path in adopted {
        if active.as_ref() == Some(&path) || state.finalizing.lock().contains(&path) {
            continue;
        }
        let Ok(meta) = std::fs::metadata(&path) else {
            vanished.push(path);
            continue;
        };
        let modified_ms = meta
            .modified()
            .ok()
            .and_then(|m| m.duration_since(UNIX_EPOCH).ok())
            .map(|d| d.as_millis() as u64)
            .unwrap_or(0);
        out.push(LeftoverRecording {
            file_name: path
                .file_name()
                .map(|n| n.to_string_lossy().into_owned())
                .unwrap_or_default(),
            path: path.to_string_lossy().into_owned(),
            byte_size: meta.len(),
            modified_ms,
        });
    }
    if !vanished.is_empty() {
        let mut recorded = state.recorded_paths.lock();
        let mut adopted = state.adopted_paths.lock();
        for path in vanished {
            recorded.remove(&path);
            adopted.remove(&path);
        }
    }
    out.sort_by(|a, b| b.modified_ms.cmp(&a.modified_ms));
    out
}

#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    env_logger::init();

    tauri::Builder::default()
        .manage(RecorderState {
            recorder: Mutex::new(AudioRecorder::new()),
            recorded_paths: Mutex::new(HashSet::new()),
            adopted_paths: Mutex::new(HashSet::new()),
            finalizing: Arc::new(Mutex::new(HashSet::new())),
        })
        .invoke_handler(tauri::generate_handler![
            start_recording,
            stop_recording,
            recording_status,
            upload::upload_recording,
            cleanup_recording,
            list_leftover_recordings,
        ])
        .setup(|app| {
            // Adopt any leftover temp WAVs from a previous run (crash, force
            // quit, or a stop that never got a chance to finalize) into
            // `recorded_paths` AND `adopted_paths`, so `upload_recording`/
            // `cleanup_recording` can reach them and `list_leftover_recordings`
            // can offer them to the SPA — otherwise `validate_recording_path`
            // rejects them forever with "path was not created by this
            // recording session", even though they already live inside the
            // whitelisted directory. The scan itself (wav-only, regular files
            // only — no symlinks, stale files older than
            // `LEFTOVER_RECORDING_MAX_AGE` deleted instead of adopted) lives
            // in `leftover::scan_leftover_dir` so it can be unit-tested
            // without Tauri; see its doc for the rationale behind each rule.
            // Best-effort and launch-time only: this is not a continuous
            // sweep, so a file can outlive the 48h window while the app stays
            // open — it's re-evaluated on the next launch.
            let dir = allowed_dir();
            let scan =
                leftover::scan_leftover_dir(&dir, SystemTime::now(), LEFTOVER_RECORDING_MAX_AGE);
            let adopted = scan.adopt.len();
            if adopted > 0 {
                let state = app.state::<RecorderState>();
                let mut recorded = state.recorded_paths.lock();
                let mut adopted_set = state.adopted_paths.lock();
                for path in scan.adopt {
                    recorded.insert(path.clone());
                    adopted_set.insert(path);
                }
                log::info!(
                    "adopted {adopted} leftover recording(s) from a previous run in {}",
                    dir.display()
                );
            }
            if scan.deleted_stale > 0 {
                log::info!(
                    "deleted {} leftover recording(s) older than {:?} in {}",
                    scan.deleted_stale,
                    LEFTOVER_RECORDING_MAX_AGE,
                    dir.display()
                );
            }
            Ok(())
        })
        .build(tauri::generate_context!())
        .expect("error while building tauri application")
        .run(|app_handle, event| {
            // Best-effort finalize on a GRACEFUL quit (window closed, Cmd+Q,
            // app.exit()). This only helps that path — a SIGKILL (Activity
            // Monitor "Force Quit", `kill -9`) tears the process down
            // without running any callback here at all, same as it always
            // has; only the periodic 5s flush checkpoint
            // (`FLUSH_INTERVAL_CHANNEL_SAMPLES` in audio.rs) protects
            // against that case. Without this handler, a graceful quit
            // mid-recording never calls `stop_capture`/`finalize_writer`
            // either, so the WAV's RIFF/data header stays at hound's zero
            // placeholder beyond whatever that last checkpoint wrote.
            //
            // `RunEvent::Exit` fires as the app is already tearing down, so
            // this is deliberately synchronous and un-timed-out, unlike the
            // live `stop_recording` command's spawn_blocking+timeout dance —
            // there is no IPC promise to keep responsive here, and a slow
            // ScreenCaptureKit stop at this point is better to just wait out
            // (process exit is already in motion; nothing else needs this
            // thread) than to abandon and lose the tail of the recording.
            if let tauri::RunEvent::Exit = event {
                let state = app_handle.state::<RecorderState>();
                let handle = {
                    let mut rec = state.recorder.lock();
                    rec.take_handle().ok()
                };
                #[cfg(target_os = "macos")]
                if let Some(handle) = handle {
                    if let Err(e) = handle.backend.stop_and_finalize() {
                        log::error!("stop_and_finalize on exit failed: {e}");
                    }
                }
                #[cfg(not(target_os = "macos"))]
                let _ = handle;
            }
        });
}
