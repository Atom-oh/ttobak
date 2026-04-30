//! Ttobak Mac App library entry point.
//!
//! Tauri commands exposed to the WebView:
//! - `start_recording(meeting_id)` — begin system-audio capture into a temp WAV
//! - `stop_recording()` — finalize WAV and return the absolute file path
//! - `read_recording_bytes(path)` — read the WAV file bytes for upload
//! - `recording_status()` — current capture state for the UI
//!
//! Auth is intentionally NOT handled here: the Tauri WebView loads the existing
//! Ttobak SPA (`ttobak.atomai.click`), which already runs the Cognito flow via
//! `amazon-cognito-identity-js`. The frontend detects `window.__TAURI_INTERNALS__`
//! and switches the recording path to the native commands below.

mod audio;
mod error;

use std::sync::Arc;

use parking_lot::Mutex;
use serde::Serialize;
use tauri::{Manager, State};

use crate::audio::AudioRecorder;
use crate::error::AppError;

/// Shared, thread-safe handle to the audio recorder.
pub struct RecorderState(pub Arc<Mutex<AudioRecorder>>);

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

#[tauri::command]
async fn start_recording(
    meeting_id: String,
    state: State<'_, RecorderState>,
) -> Result<StartResponse, AppError> {
    let mut rec = state.0.lock();
    let path = rec.start(&meeting_id)?;
    Ok(StartResponse {
        temp_path: path.to_string_lossy().into_owned(),
    })
}

#[tauri::command]
async fn stop_recording(state: State<'_, RecorderState>) -> Result<StopResponse, AppError> {
    let mut rec = state.0.lock();
    let summary = rec.stop()?;
    Ok(StopResponse {
        temp_path: summary.path.to_string_lossy().into_owned(),
        duration_ms: summary.duration_ms,
        byte_size: summary.byte_size,
    })
}

#[tauri::command]
fn recording_status(state: State<'_, RecorderState>) -> StatusResponse {
    let rec = state.0.lock();
    let snapshot = rec.snapshot();
    StatusResponse {
        recording: snapshot.recording,
        temp_path: snapshot.path.map(|p| p.to_string_lossy().into_owned()),
        elapsed_ms: snapshot.elapsed_ms,
    }
}

#[tauri::command]
async fn read_recording_bytes(path: String) -> Result<Vec<u8>, AppError> {
    let bytes = tokio::fs::read(&path)
        .await
        .map_err(|e| AppError::Io(format!("read {path}: {e}")))?;
    Ok(bytes)
}

#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    env_logger::init();

    tauri::Builder::default()
        .plugin(tauri_plugin_shell::init())
        .plugin(tauri_plugin_dialog::init())
        .plugin(tauri_plugin_fs::init())
        .plugin(tauri_plugin_http::init())
        .manage(RecorderState(Arc::new(Mutex::new(AudioRecorder::new()))))
        .invoke_handler(tauri::generate_handler![
            start_recording,
            stop_recording,
            recording_status,
            read_recording_bytes,
        ])
        .setup(|app| {
            #[cfg(target_os = "macos")]
            {
                let _ = app; // reserved for menu/tray hookups
            }
            Ok(())
        })
        .run(tauri::generate_context!())
        .expect("error while running tauri application");
}
