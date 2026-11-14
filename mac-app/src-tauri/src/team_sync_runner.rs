//! Admin team sync runner: runs `node ~/.ttobak/server.mjs team-sync run ...`
//! from the admin page (preview, then apply), reusing the intake runner's
//! spawn, kill and progress machinery (`intake_runner::run_child`).
//!
//! The window loads the remote SPA, so every command here is callable from a
//! remote origin. Authorization is the API's `RequireAdmin`; these commands
//! add no trust. The mode is an enum and the preview ID must match
//! `^[a-f0-9]{32}$`, both checked before anything spawns, so no free-form
//! string reaches argv. The WebView gets only the CLI's final JSON line and
//! its stderr progress lines. The child gets only the intake's cleared
//! environment (HOME, LANG, PATH).
//!
//! Team sync runs use their own run slot; the CLI holds its own lock and
//! reports `RUN_IN_PROGRESS` if another team-sync CLI is running.

use std::ffi::OsString;
use std::sync::atomic::{AtomicBool, AtomicI32, AtomicU64, Ordering};
use std::time::Duration;

use parking_lot::Mutex;
use serde::{Deserialize, Serialize};
use serde_json::{json, Value};
use tauri::{AppHandle, Emitter, Manager, State};
use tokio::sync::oneshot;

use crate::intake_runner::{self, IntakeState, Outcome, ProcTrack, ProgressLog};

/// Rust's outer bound for one preview or apply run.
const TEAM_SYNC_TIMEOUT: Duration = Duration::from_secs(10 * 60);

const PROGRESS_EVENT: &str = "team-sync://progress";
const FINISHED_EVENT: &str = "team-sync://finished";

/// Present in an adapter bundle that supports the command.
const TEAM_SYNC_MARKER: &[u8] = b"ttobak-mcp team-sync run";

// ---------------------------------------------------------------- pure parts

#[derive(Debug, Clone, Copy, PartialEq, Eq, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum TeamSyncMode {
    Preview,
    Apply,
}

impl TeamSyncMode {
    fn as_str(self) -> &'static str {
        match self {
            Self::Preview => "preview",
            Self::Apply => "apply",
        }
    }
}

/// `^[a-f0-9]{32}$`.
fn preview_id_ok(id: &str) -> bool {
    id.len() == 32 && id.bytes().all(|b| matches!(b, b'0'..=b'9' | b'a'..=b'f'))
}

/// Apply needs a valid preview ID; preview takes none.
fn validate_args(mode: TeamSyncMode, preview_id: Option<&str>) -> Result<(), &'static str> {
    match (mode, preview_id) {
        (TeamSyncMode::Preview, None) => Ok(()),
        (TeamSyncMode::Apply, Some(id)) if preview_id_ok(id) => Ok(()),
        _ => Err("INVALID_ARGUMENT"),
    }
}

fn team_sync_argv(mode: TeamSyncMode, preview_id: Option<&str>) -> Vec<String> {
    let mut argv = vec!["team-sync".to_string(), "run".to_string()];
    if let (TeamSyncMode::Apply, Some(id)) = (mode, preview_id) {
        argv.extend(["--apply".into(), "--preview-id".into(), id.to_string()]);
    }
    argv
}

fn supports_team_sync(adapter: &[u8]) -> bool {
    adapter
        .windows(TEAM_SYNC_MARKER.len())
        .any(|w| w == TEAM_SYNC_MARKER)
}

/// Rust's forced ends in the team-sync vocabulary.
fn team_sync_code(code: Option<String>) -> Option<String> {
    code.map(|c| match c.as_str() {
        "TIMEOUT" => "TEAM_SYNC_TIMEOUT".to_string(),
        "CANCELLED" => "TEAM_SYNC_CANCELLED".to_string(),
        _ => c,
    })
}

// ---------------------------------------------------------------- state

pub struct TeamSyncState {
    running: Mutex<bool>,
    cancel: Mutex<Option<oneshot::Sender<()>>>,
    pgid: AtomicI32,
    shutting_down: AtomicBool,
    progress: Mutex<ProgressLog>,
    /// Bumped per begun run; a reader of an older run never writes progress.
    run_gen: AtomicU64,
    last_result: Mutex<Option<Value>>,
}

impl TeamSyncState {
    fn new() -> Self {
        Self {
            running: Mutex::new(false),
            cancel: Mutex::new(None),
            pgid: AtomicI32::new(0),
            shutting_down: AtomicBool::new(false),
            progress: Mutex::new(ProgressLog::default()),
            run_gen: AtomicU64::new(0),
            last_result: Mutex::new(None),
        }
    }

    pub(crate) fn track(&self) -> ProcTrack<'_> {
        ProcTrack {
            pgid: &self.pgid,
            shutting_down: &self.shutting_down,
        }
    }

    /// Claims the team sync run slot (independent of intake and report).
    fn try_begin(&self) -> Result<oneshot::Receiver<()>, String> {
        let mut running = self.running.lock();
        if *running {
            return Err("RUN_IN_PROGRESS".into());
        }
        if self.shutting_down.load(Ordering::SeqCst) {
            return Err("SHUTTING_DOWN".into());
        }
        *running = true;
        let (tx, rx) = oneshot::channel();
        *self.cancel.lock() = Some(tx);
        self.run_gen.fetch_add(1, Ordering::SeqCst);
        self.progress.lock().clear();
        Ok(rx)
    }

    fn end(&self) {
        *self.cancel.lock() = None;
        *self.running.lock() = false;
    }
}

pub fn manage(app: &tauri::App) {
    app.manage(TeamSyncState::new());
}

// ---------------------------------------------------------------- run

async fn run_team_sync(
    app: &AppHandle,
    mode: TeamSyncMode,
    preview_id: Option<&str>,
    cancel: oneshot::Receiver<()>,
) -> Outcome {
    let intake = app.state::<IntakeState>();
    let state = app.state::<TeamSyncState>();
    let launch = match intake_runner::node_launch(&intake).await {
        Ok(l) => l,
        Err(code) => return Outcome::failed(code),
    };
    if !intake_runner::local_adapter(&launch.home).is_some_and(|b| supports_team_sync(&b)) {
        return Outcome::failed("ADAPTER_OUTDATED");
    }
    let mut args: Vec<OsString> = vec![launch.server.clone().into_os_string()];
    args.extend(
        team_sync_argv(mode, preview_id)
            .into_iter()
            .map(OsString::from),
    );
    let cmd = intake_runner::node_command(&launch, args);
    let gen = state.run_gen.load(Ordering::SeqCst);
    let line_app = app.clone();
    let on_line = move |line: String| {
        let state = line_app.state::<TeamSyncState>();
        if state.run_gen.load(Ordering::SeqCst) == gen {
            let _ = line_app.emit(PROGRESS_EVENT, json!({ "line": line }));
            state.progress.lock().push(line);
        }
    };
    intake_runner::run_child(
        cmd,
        state.track(),
        TEAM_SYNC_TIMEOUT,
        intake_runner::KILL_GRACE,
        Some(cancel),
        on_line,
    )
    .await
}

// ---------------------------------------------------------------- commands

#[derive(Debug, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct TeamSyncStateView {
    running: bool,
    last_result: Option<Value>,
    progress: Vec<String>,
}

/// Starts one background run; finishes with `team-sync://finished`.
#[tauri::command]
pub async fn team_sync_run(
    app: AppHandle,
    mode: TeamSyncMode,
    preview_id: Option<String>,
) -> Result<(), String> {
    let intake = app.state::<IntakeState>();
    intake.available()?;
    validate_args(mode, preview_id.as_deref()).map_err(str::to_string)?;
    let state = app.state::<TeamSyncState>();
    let cancel = state.try_begin()?;
    let app = app.clone();
    tauri::async_runtime::spawn(async move {
        let outcome = run_team_sync(&app, mode, preview_id.as_deref(), cancel).await;
        let payload = json!({
            "mode": mode.as_str(),
            "exitCode": outcome.exit_code,
            "result": outcome.result,
            "code": team_sync_code(outcome.code),
        });
        let state = app.state::<TeamSyncState>();
        *state.last_result.lock() = Some(payload.clone());
        state.end();
        let _ = app.emit(FINISHED_EVENT, payload);
    });
    Ok(())
}

/// Cancels the current run; `false` when nothing was running.
#[tauri::command]
pub async fn team_sync_cancel(state: State<'_, TeamSyncState>) -> Result<bool, String> {
    Ok(state
        .cancel
        .lock()
        .take()
        .is_some_and(|tx| tx.send(()).is_ok()))
}

#[tauri::command]
pub async fn team_sync_get_state(
    state: State<'_, TeamSyncState>,
) -> Result<TeamSyncStateView, String> {
    Ok(TeamSyncStateView {
        running: *state.running.lock(),
        last_result: state.last_result.lock().clone(),
        progress: state.progress.lock().to_vec(),
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn argv_for_preview_and_apply() {
        assert_eq!(
            team_sync_argv(TeamSyncMode::Preview, None),
            vec!["team-sync", "run"]
        );
        let id = "0123456789abcdef0123456789abcdef";
        assert_eq!(
            team_sync_argv(TeamSyncMode::Apply, Some(id)),
            vec!["team-sync", "run", "--apply", "--preview-id", id]
        );
    }

    #[test]
    fn preview_id_must_be_32_lower_hex() {
        for bad in [
            "",
            "0123456789abcdef0123456789abcde",
            "0123456789ABCDEF0123456789ABCDEF",
            "../x",
            "0123456789abcdef0123456789abcdeg",
            "0123456789abcdef0123456789abcdef0",
        ] {
            assert!(!preview_id_ok(bad), "{bad}");
        }
        assert!(preview_id_ok("0123456789abcdef0123456789abcdef"));
    }

    #[test]
    fn apply_requires_id_and_preview_refuses_one() {
        let id = "0123456789abcdef0123456789abcdef";
        assert_eq!(
            validate_args(TeamSyncMode::Apply, None),
            Err("INVALID_ARGUMENT")
        );
        assert_eq!(
            validate_args(TeamSyncMode::Preview, Some(id)),
            Err("INVALID_ARGUMENT")
        );
        assert_eq!(
            validate_args(TeamSyncMode::Apply, Some("bad")),
            Err("INVALID_ARGUMENT")
        );
        assert_eq!(validate_args(TeamSyncMode::Preview, None), Ok(()));
        assert_eq!(validate_args(TeamSyncMode::Apply, Some(id)), Ok(()));
    }

    #[test]
    fn supports_team_sync_needs_the_marker() {
        assert!(supports_team_sync(b"... ttobak-mcp team-sync run ..."));
        assert!(!supports_team_sync(b"ttobak-mcp intake setup"));
    }
}
