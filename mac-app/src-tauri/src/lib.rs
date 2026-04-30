//! Ttobak Mac App library entry point.
//!
//! Tauri commands exposed to the WebView:
//! - `start_recording(meeting_id)` — begin system-audio capture into a temp WAV
//! - `stop_recording()` — finalize WAV and return the absolute file path
//! - `read_recording_bytes(path)` — return WAV bytes via IPC binary response (path-validated)
//! - `cleanup_recording(path)` — delete a temp WAV file after successful upload
//! - `recording_status()` — current capture state for the UI

mod audio;
mod error;

use std::collections::HashSet;
use std::path::PathBuf;
use parking_lot::Mutex;
use serde::Serialize;
use tauri::State;
use tauri::ipc::Response;

use crate::audio::AudioRecorder;
use crate::error::AppError;

pub struct RecorderState {
    pub recorder: Mutex<AudioRecorder>,
    pub recorded_paths: Mutex<HashSet<PathBuf>>,
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
}

#[derive(Serialize)]
pub struct StatusResponse {
    pub recording: bool,
    pub temp_path: Option<String>,
    pub elapsed_ms: u64,
}

fn allowed_dir() -> PathBuf {
    let base = std::fs::canonicalize(std::env::temp_dir())
        .unwrap_or_else(|_| std::env::temp_dir());
    base.join("ttobak-mac")
}

fn validate_recording_path(path: &str, recorded_paths: &HashSet<PathBuf>) -> Result<PathBuf, AppError> {
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
) -> Result<StartResponse, AppError> {
    let mut rec = state.recorder.lock();
    let path = rec.start(&meeting_id)?;
    let canonical = std::fs::canonicalize(&path).unwrap_or(path);
    state.recorded_paths.lock().insert(canonical.clone());
    Ok(StartResponse {
        temp_path: canonical.to_string_lossy().into_owned(),
    })
}

#[tauri::command]
async fn stop_recording(state: State<'_, RecorderState>) -> Result<StopResponse, AppError> {
    let mut rec = state.recorder.lock();
    let summary = rec.stop()?;
    Ok(StopResponse {
        temp_path: summary.path.to_string_lossy().into_owned(),
        duration_ms: summary.duration_ms,
        byte_size: summary.byte_size,
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
    }
}

#[tauri::command]
fn read_recording_bytes(
    path: String,
    state: State<'_, RecorderState>,
) -> Result<Response, AppError> {
    let canonical = validate_recording_path(&path, &state.recorded_paths.lock())?;
    let bytes = std::fs::read(&canonical)
        .map_err(|e| AppError::Io(format!("read {}: {e}", canonical.display())))?;
    Ok(Response::new(bytes))
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
        })
        .invoke_handler(tauri::generate_handler![
            start_recording,
            stop_recording,
            recording_status,
            read_recording_bytes,
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
