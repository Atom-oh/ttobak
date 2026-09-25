//! Local control socket (ADR-046): lets a same-user process such as the TTOBAK
//! MCP server start/stop an app recording and read its status.
//!
//! `~/Library/Application Support/ttobak/control.sock` (directory 0700, socket
//! 0600) accepts one request line per connection from peers with this
//! process's uid only; browsers cannot reach Unix sockets. Rust never gains
//! meeting or auth knowledge: start/stop are forwarded to the SPA as a
//! `native-control-request` event, and the SPA — which owns sign-in, meeting
//! creation and upload — answers through `control_reply`. The SPA reports
//! readiness (`control_ready`) and its latest recording phase
//! (`control_report_state`), which `status` returns together with the native
//! recorder snapshot.

use std::collections::HashMap;
use std::os::unix::fs::{FileTypeExt, MetadataExt, PermissionsExt};
use std::path::PathBuf;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::Arc;
use std::time::Duration;

use parking_lot::Mutex;
use serde_json::{json, Value};
use tauri::{AppHandle, Emitter, Manager, State};
use tokio::io::{AsyncBufReadExt, AsyncReadExt, AsyncWriteExt, BufReader};
use tokio::net::{UnixListener, UnixStream};
use tokio::sync::{oneshot, Semaphore};

use crate::control_proto::{self as proto, Action, Code};
use crate::RecorderState;

const READ_TIMEOUT: Duration = Duration::from_secs(5);
/// Includes draft-meeting creation and the native capture start (which can
/// wait on a first-run permission prompt).
const START_TIMEOUT: Duration = Duration::from_secs(45);
/// Native stop plus WAV finalization; the upload itself is reported later
/// through `status`.
const STOP_TIMEOUT: Duration = Duration::from_secs(20);
const MAX_CONNECTIONS: usize = 8;

#[derive(Default)]
pub struct ControlState {
    /// `Some(logged_in)` once the SPA's bridge has mounted.
    ready: Mutex<Option<bool>>,
    pending: Mutex<HashMap<String, oneshot::Sender<Value>>>,
    last_state: Mutex<Option<Value>>,
    counter: AtomicU64,
}

pub fn socket_path() -> Option<PathBuf> {
    let home = std::env::var_os("HOME")?;
    Some(PathBuf::from(home).join("Library/Application Support/ttobak/control.sock"))
}

/// Starts the listener in the background. Failure only disables MCP control;
/// recording from the app window is unaffected.
pub fn start(app: AppHandle) {
    tauri::async_runtime::spawn(async move {
        if let Err(e) = serve(app).await {
            log::warn!("control socket unavailable: {e}");
        }
    });
}

async fn serve(app: AppHandle) -> std::io::Result<()> {
    let path = socket_path().ok_or_else(|| std::io::Error::other("HOME is not set"))?;
    let dir = path.parent().expect("socket path has a parent");
    std::fs::create_dir_all(dir)?;
    std::fs::set_permissions(dir, std::fs::Permissions::from_mode(0o700))?;

    match std::fs::symlink_metadata(&path) {
        Ok(meta) if meta.file_type().is_socket() => {
            // A live listener means another app instance owns control.
            if UnixStream::connect(&path).await.is_ok() {
                return Err(std::io::Error::new(
                    std::io::ErrorKind::AddrInUse,
                    "another TTOBAK instance is serving the control socket",
                ));
            }
            std::fs::remove_file(&path)?;
        }
        // Never delete something that is not our socket (file, symlink).
        Ok(_) => {
            return Err(std::io::Error::new(
                std::io::ErrorKind::AlreadyExists,
                format!("{} exists and is not a socket", path.display()),
            ))
        }
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => {}
        Err(e) => return Err(e),
    }

    let listener = UnixListener::bind(&path)?;
    std::fs::set_permissions(&path, std::fs::Permissions::from_mode(0o600))?;
    // The socket was created by this process, so its owner is our uid.
    let own_uid = std::fs::metadata(&path)?.uid();
    log::info!("control socket listening at {}", path.display());

    let slots = Arc::new(Semaphore::new(MAX_CONNECTIONS));
    loop {
        let (stream, _) = listener.accept().await?;
        let Ok(permit) = Arc::clone(&slots).try_acquire_owned() else {
            drop(stream); // over capacity: refuse rather than queue
            continue;
        };
        let app = app.clone();
        tauri::async_runtime::spawn(async move {
            handle(app, stream, own_uid).await;
            drop(permit);
        });
    }
}

async fn handle(app: AppHandle, mut stream: UnixStream, own_uid: u32) {
    match stream.peer_cred() {
        Ok(cred) if cred.uid() == own_uid => {}
        _ => return, // another user (or unknown): close silently
    }
    let mut line = Vec::new();
    let read = {
        let mut reader = BufReader::new((&mut stream).take(proto::MAX_REQUEST_BYTES as u64 + 1));
        tokio::time::timeout(READ_TIMEOUT, reader.read_until(b'\n', &mut line)).await
    };
    let response = match read {
        Err(_) => proto::error_line(None, Code::Timeout.as_str(), "request not received in time"),
        Ok(Err(_)) => return,
        Ok(Ok(_)) if line.len() > proto::MAX_REQUEST_BYTES => {
            proto::error_line(None, Code::BadRequest.as_str(), "request too large")
        }
        Ok(Ok(_)) => {
            // Strip the trailing newline / whitespace (MSRV 1.77: no trim_ascii_end).
            let end = line
                .iter()
                .rposition(|b| !b.is_ascii_whitespace())
                .map_or(0, |i| i + 1);
            match proto::parse_request(&line[..end]) {
                Err(e) => proto::error_line(e.id.as_deref(), e.code.as_str(), &e.message),
                Ok(request) => dispatch(&app, request).await,
            }
        }
    };
    let _ = stream.write_all(response.as_bytes()).await;
    let _ = stream.shutdown().await;
}

async fn dispatch(app: &AppHandle, request: proto::Request) -> String {
    let control = app.state::<ControlState>();
    if request.action == Action::Status {
        return proto::ok_line(&request.id, status(app, &control));
    }
    match *control.ready.lock() {
        None => {
            return proto::error_line(
                Some(&request.id),
                Code::AppNotReady.as_str(),
                "The TTOBAK app window has not loaded yet; open the app and sign in.",
            )
        }
        Some(false) => {
            return proto::error_line(
                Some(&request.id),
                "login_required",
                "Sign in to TTOBAK in the Mac app first.",
            )
        }
        Some(true) => {}
    }

    let request_id = format!(
        "{}-{}",
        request.id,
        control.counter.fetch_add(1, Ordering::Relaxed)
    );
    let (tx, rx) = oneshot::channel();
    control.pending.lock().insert(request_id.clone(), tx);

    if matches!(request.action, Action::Start { .. }) {
        focus_window(app);
    }
    let payload = json!({
        "requestId": request_id,
        "action": request.action.name(),
        "params": request.action.params(),
    });
    if let Err(e) = app.emit_to("main", "native-control-request", payload) {
        control.pending.lock().remove(&request_id);
        return proto::error_line(
            Some(&request.id),
            Code::Internal.as_str(),
            &format!("could not reach the app window: {e}"),
        );
    }

    let limit = if matches!(request.action, Action::Start { .. }) {
        START_TIMEOUT
    } else {
        STOP_TIMEOUT
    };
    let reply = tokio::time::timeout(limit, rx).await;
    control.pending.lock().remove(&request_id);
    match reply {
        Ok(Ok(reply)) => {
            if let (Action::Start { title, .. }, Some(true)) =
                (&request.action, reply.get("ok").and_then(Value::as_bool))
            {
                notify_started(app, title);
            }
            proto::spa_reply_line(&request.id, &reply)
        }
        _ => proto::error_line(
            Some(&request.id),
            Code::Timeout.as_str(),
            "The TTOBAK app did not answer in time; check the app window.",
        ),
    }
}

fn status(app: &AppHandle, control: &ControlState) -> Value {
    let snapshot = app.state::<RecorderState>().recorder.lock().snapshot();
    json!({
        "app": { "version": env!("CARGO_PKG_VERSION") },
        "ready": *control.ready.lock() == Some(true),
        "signedIn": *control.ready.lock(),
        "native": { "recording": snapshot.recording, "elapsedMs": snapshot.elapsed_ms },
        "spa": control.last_state.lock().clone(),
    })
}

/// Brings the app window forward so an MCP-initiated start is visible.
fn focus_window(app: &AppHandle) {
    if let Some(window) = app.get_webview_window("main") {
        let _ = window.unminimize();
        let _ = window.show();
        let _ = window.set_focus();
    }
}

/// Posts a notification once an MCP-initiated recording is running
/// (best-effort).
fn notify_started(app: &AppHandle, title: &str) {
    use tauri_plugin_notification::NotificationExt;
    let _ = app
        .notification()
        .builder()
        .title("TTOBAK 녹음 시작")
        .body(format!("MCP 요청으로 녹음을 시작합니다: {title}"))
        .show();
}

// --- Commands called by the SPA bridge ------------------------------------

#[tauri::command]
pub fn control_ready(info: Value, control: State<'_, ControlState>) {
    let logged_in = info
        .get("loggedIn")
        .and_then(Value::as_bool)
        .unwrap_or(false);
    *control.ready.lock() = Some(logged_in);
}

#[tauri::command]
pub fn control_reply(
    request_id: String,
    result: Value,
    control: State<'_, ControlState>,
) -> Result<(), String> {
    let sender = control
        .pending
        .lock()
        .remove(&request_id)
        .ok_or("unknown or expired control request")?;
    let _ = sender.send(result);
    Ok(())
}

#[tauri::command]
pub fn control_report_state(state: Value, control: State<'_, ControlState>) -> Result<(), String> {
    let size = serde_json::to_vec(&state)
        .map(|b| b.len())
        .unwrap_or(usize::MAX);
    if !state.is_object() || size > proto::MAX_SPA_PAYLOAD_BYTES {
        return Err("state must be a small object".into());
    }
    *control.last_state.lock() = Some(state);
    Ok(())
}
