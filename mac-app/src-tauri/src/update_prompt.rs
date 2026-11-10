//! Update prompt window (Task 3 replaces this stub). For now it only logs.

use tauri::AppHandle;

/// Offers `version` to the user. Stub: logs only, no window yet.
pub fn offer(app: &AppHandle, version: &str, notes: &str) {
    let _ = app;
    log::info!(
        "update v{version} available ({} bytes of notes); prompt window not built yet",
        notes.len()
    );
}
