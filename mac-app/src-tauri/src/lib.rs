//! TTOBAK Mac App library entry point.
//!
//! Tauri commands exposed to the WebView:
//! - `start_recording(meeting_id)` — begin system-audio capture into a temp WAV
//! - `stop_recording()` — stop capture, finalize the WAV, return its path
//! - `upload_recording(path, upload_url, content_type)` — stream the WAV
//!   straight from disk to a presigned S3 URL (bulk audio bytes never cross
//!   the IPC bridge to the WebView — see `upload.rs` module docs)
//! - `cleanup_recording(path)` — delete a temp WAV file and revoke whitelist entry
//! - `recording_status()` — current capture state for the UI
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
mod upload;

use std::collections::HashSet;
use std::path::PathBuf;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;
use std::time::Duration;

use parking_lot::Mutex;
use serde::Serialize;
use tauri::{AppHandle, State};

use crate::audio::AudioRecorder;
use crate::error::AppError;

/// How long `stop_recording` waits for ScreenCaptureKit's `stop_capture` to
/// return before giving up and reporting `stop_timed_out: true`. The
/// underlying FFI call has no timeout of its own (it blocks on a plain
/// `Condvar`), so without this a wedged stream previously could have hung
/// the command's promise forever.
const STOP_CAPTURE_TIMEOUT: Duration = Duration::from_secs(10);

pub struct RecorderState {
    pub recorder: Mutex<AudioRecorder>,
    pub recorded_paths: Mutex<HashSet<PathBuf>>,
    /// True while a `stop_and_finalize` is still running — including one
    /// that outlived `STOP_CAPTURE_TIMEOUT` and kept going in the
    /// background. `recording_status` exposes this so the frontend can wait
    /// for the WAV to actually be finalized after a `stop_timed_out: true`
    /// response: `recording` alone flips false the moment `take_handle()`
    /// empties the recorder, long before the background finalize is done.
    pub finalizing: Arc<AtomicBool>,
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
    /// True while a stop's `stop_and_finalize` is still running (see
    /// `RecorderState::finalizing`). After a `stop_timed_out` stop, the
    /// frontend polls until this goes false before uploading — `recording`
    /// is already false at that point and cannot express "still writing".
    pub finalizing: bool,
}

fn allowed_dir() -> PathBuf {
    let base = std::fs::canonicalize(std::env::temp_dir())
        .unwrap_or_else(|_| std::env::temp_dir());
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
    let mut rec = state.recorder.lock();
    let path = rec.start(&meeting_id, app)?;
    let canonical = std::fs::canonicalize(&path).unwrap_or(path);
    state.recorded_paths.lock().insert(canonical.clone());
    Ok(StartResponse {
        temp_path: canonical.to_string_lossy().into_owned(),
    })
}

#[tauri::command]
async fn stop_recording(state: State<'_, RecorderState>) -> Result<StopResponse, AppError> {
    // Take the handle out and let the `state.recorder` lock go BEFORE the
    // blocking, no-timeout-of-its-own `stop_capture()` FFI call — holding
    // that lock across it used to mean a wedged ScreenCaptureKit stop could
    // block every other command needing `RecorderState.recorder` (notably
    // `recording_status`, which runs on the app's main thread).
    let handle = {
        let mut rec = state.recorder.lock();
        rec.take_handle()?
    };
    let duration_ms = handle.started_at.elapsed().as_millis() as u64;
    let path = handle.path;

    #[cfg(target_os = "macos")]
    let stop_timed_out = {
        let backend = handle.backend;
        // Raise `finalizing` for the whole stop_and_finalize window and let
        // the blocking task itself clear it — that way the flag stays
        // accurate on the timed-out path too, where this command returns
        // while the task keeps running in the background.
        state.finalizing.store(true, Ordering::SeqCst);
        let finalize_done = Arc::clone(&state.finalizing);
        let stop_task = tauri::async_runtime::spawn_blocking(move || {
            let result = backend.stop_and_finalize();
            finalize_done.store(false, Ordering::SeqCst);
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
fn recording_status(state: State<'_, RecorderState>) -> StatusResponse {
    let rec = state.recorder.lock();
    let snapshot = rec.snapshot();
    StatusResponse {
        recording: snapshot.recording,
        temp_path: snapshot.path.map(|p| p.to_string_lossy().into_owned()),
        elapsed_ms: snapshot.elapsed_ms,
        finalizing: state.finalizing.load(Ordering::SeqCst),
    }
}

#[tauri::command]
async fn cleanup_recording(
    path: String,
    state: State<'_, RecorderState>,
) -> Result<(), AppError> {
    let canonical = validate_recording_path(&path, &state.recorded_paths.lock())?;
    tokio::fs::remove_file(&canonical)
        .await
        .map_err(|e| AppError::Io(format!("remove {}: {e}", canonical.display())))?;
    state.recorded_paths.lock().remove(&canonical);
    log::info!("cleaned up recording: {}", canonical.display());
    Ok(())
}

#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    env_logger::init();

    tauri::Builder::default()
        .manage(RecorderState {
            recorder: Mutex::new(AudioRecorder::new()),
            recorded_paths: Mutex::new(HashSet::new()),
            finalizing: Arc::new(AtomicBool::new(false)),
        })
        .invoke_handler(tauri::generate_handler![
            start_recording,
            stop_recording,
            recording_status,
            upload::upload_recording,
            cleanup_recording,
        ])
        .setup(|app| {
            #[cfg(target_os = "macos")]
            {
                let _ = app;
            }
            Ok(())
        })
        .run(tauri::generate_context!())
        .expect("error while running tauri application");
}
