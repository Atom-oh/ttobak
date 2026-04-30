//! Ttobak Mac App library entry point.
//!
//! Tauri commands exposed to the WebView:
//! - `start_recording(meeting_id)` — begin system-audio capture into a temp WAV
//! - `stop_recording()` — finalize WAV and return the absolute file path
//! - `get_recording_asset_url(path)` — return an asset: URL for the WAV (path-validated)
//! - `cleanup_recording(path)` — delete a temp WAV file after successful upload
//! - `recording_status()` — current capture state for the UI
//!
//! Auth is intentionally NOT handled here: the Tauri WebView loads the existing
//! Ttobak SPA (`ttobak.atomai.click`), which already runs the Cognito flow via
//! `amazon-cognito-identity-js`. The frontend detects `window.__TAURI_INTERNALS__`
//! and switches the recording path to the native commands below.

mod audio;
mod error;

use std::collections::HashSet;
use std::path::PathBuf;
use std::sync::Arc;

use parking_lot::Mutex;
use serde::Serialize;
use tauri::{Manager, State};

use crate::audio::AudioRecorder;
use crate::error::AppError;

/// Shared, thread-safe handle to the audio recorder + recorded path whitelist.
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
    let mut dir = std::env::temp_dir();
    dir.push("ttobak-mac");
    dir
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
    state.recorded_paths.lock().insert(path.clone());
    Ok(StartResponse {
        temp_path: path.to_string_lossy().into_owned(),
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
fn get_recording_asset_url(
    path: String,
    app_handle: tauri::AppHandle,
    state: State<'_, RecorderState>,
) -> Result<String, AppError> {
    let canonical = validate_recording_path(&path, &state.recorded_paths.lock())?;
    let url = app_handle.convert_file_src(canonical)
        .map_err(|e| AppError::Io(format!("convert_file_src: {e}")))?;
    Ok(url)
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
            get_recording_asset_url,
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
