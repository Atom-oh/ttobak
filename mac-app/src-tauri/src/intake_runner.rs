//! Local intake runner (ADR-065): runs `node ~/.ttobak/server.mjs intake ...`
//! from the app, schedules runs, catches up missed slots at launch and wake,
//! and updates the adapter bundle from the app's own origin.
//!
//! The window loads the remote SPA, so every command here is callable from a
//! remote origin. Commands take typed options only; the one path accepted
//! (`intake_set_node_path`) must be an executable file named `node`. The
//! WebView receives only the CLI's final JSON line and its stderr progress
//! lines, never stdout noise, argv, env or selection paths.

use std::collections::{BTreeSet, VecDeque};
use std::io::{Read, Write};
use std::path::{Component, Path, PathBuf};
use std::process::Stdio;
use std::sync::atomic::{AtomicBool, AtomicI32, Ordering};
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};

use parking_lot::Mutex;
use serde::{Deserialize, Serialize};
use serde_json::{json, Value};
use tauri::{AppHandle, Emitter, Listener, Manager, State};
use tokio::io::AsyncReadExt;
use tokio::sync::{oneshot, Notify};

use crate::intake_plan::{self, Connector, IntakeAction, Schedule, SlotState};

/// The only adapter source: the app's own origin, exact path, no redirects.
pub const ADAPTER_URL: &str = "https://ttobak.atomai.click/mcp/ttobak-mcp.mjs";
const ADAPTER_HOST: &str = "ttobak.atomai.click";
const ADAPTER_PATH: &str = "/mcp/ttobak-mcp.mjs";
/// Largest adapter bundle accepted.
const MAX_ADAPTER_BYTES: usize = 8 * 1024 * 1024;
/// Bytes a served bundle must contain to be an intake-capable adapter.
const ADAPTER_MARKER: &[u8] = b"intake setup";
const ADAPTER_CONNECT_TIMEOUT: Duration = Duration::from_secs(15);
const ADAPTER_REQUEST_TIMEOUT: Duration = Duration::from_secs(120);

/// Progress line cap in characters and lines kept per run.
const MAX_PROGRESS_CHARS: usize = 300;
const MAX_PROGRESS_LINES: usize = 200;
/// Raw stderr bytes read per line before the rest of the line is dropped.
const MAX_STDERR_LINE_BYTES: usize = 4096;
/// Largest final stdout line kept.
const MAX_STDOUT_LINE_BYTES: usize = 256 * 1024;

const MAX_STATUS_BYTES: u64 = 256 * 1024;
const MAX_SELECTION_BYTES: u64 = 256 * 1024;
const MAX_SETTINGS_BYTES: u64 = 64 * 1024;
const MAX_LOGIN_PATH_BYTES: usize = 8192;
const MAX_NAME_BYTES: usize = 128;

/// A run, setup or login is killed with its process group after this.
const RUN_TIMEOUT: Duration = Duration::from_secs(20 * 60);
/// `intake schedule off` only removes a LaunchAgent.
const SCHEDULE_OFF_TIMEOUT: Duration = Duration::from_secs(60);
const LOGIN_PATH_TIMEOUT: Duration = Duration::from_secs(5);
/// SIGTERM to SIGKILL.
const KILL_GRACE: Duration = Duration::from_secs(3);
/// Wait for the pipes to close after the CLI exits (orphans may hold them).
const READER_GRACE: Duration = Duration::from_secs(2);

const FALLBACK_PATH: &str = "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin";
const FALLBACK_SHELL: &str = "/bin/zsh";
const LAUNCH_AGENT_PLIST: &str = "Library/LaunchAgents/click.atomai.ttobak.intake.plist";

/// Scheduler tick; a wall-clock jump beyond `WAKE_JUMP_SECS` counts as a wake.
const TICK: Duration = Duration::from_secs(60);
const WAKE_JUMP_SECS: i64 = 120;

const SETTINGS_FILE: &str = "intake.json";
const WAKE_EVENT: &str = "native-system-wake";
const PROGRESS_EVENT: &str = "intake://progress";
const FINISHED_EVENT: &str = "intake://finished";

// ---------------------------------------------------------------- pure parts

/// A progress line, trimmed and capped; `None` when blank.
fn cap_progress_line(line: &str) -> Option<String> {
    let t = line.trim();
    (!t.is_empty()).then(|| t.chars().take(MAX_PROGRESS_CHARS).collect())
}

/// The last `MAX_PROGRESS_LINES` progress lines of the current or last run.
#[derive(Debug, Default)]
struct ProgressLog {
    lines: VecDeque<String>,
}

impl ProgressLog {
    fn push(&mut self, line: String) {
        if self.lines.len() >= MAX_PROGRESS_LINES {
            self.lines.pop_front();
        }
        self.lines.push_back(line);
    }
}

/// Splits a byte stream into lines of at most `cap` bytes (the rest of a long
/// line is dropped), so a runaway child cannot grow memory.
struct LineSplitter {
    cap: usize,
    buf: Vec<u8>,
}

impl LineSplitter {
    fn new(cap: usize) -> Self {
        Self {
            cap,
            buf: Vec::new(),
        }
    }

    fn take(&mut self, seg: &[u8]) {
        let room = self.cap.saturating_sub(self.buf.len());
        self.buf.extend_from_slice(&seg[..room.min(seg.len())]);
    }

    fn feed(&mut self, bytes: &[u8], out: &mut dyn FnMut(&[u8])) {
        let mut rest = bytes;
        while let Some(i) = rest.iter().position(|&b| b == b'\n') {
            self.take(&rest[..i]);
            out(&self.buf);
            self.buf.clear();
            rest = &rest[i + 1..];
        }
        self.take(rest);
    }

    fn finish(&mut self, out: &mut dyn FnMut(&[u8])) {
        if !self.buf.is_empty() {
            out(&self.buf);
            self.buf.clear();
        }
    }
}

/// Keeps only the last non-blank stdout line.
#[derive(Default)]
struct LastLine {
    last: Vec<u8>,
    splitter: Option<LineSplitter>,
}

impl LastLine {
    fn feed(&mut self, bytes: &[u8]) {
        let Self { last, splitter } = self;
        splitter
            .get_or_insert_with(|| LineSplitter::new(MAX_STDOUT_LINE_BYTES))
            .feed(bytes, &mut |l| {
                if !l.trim_ascii().is_empty() {
                    *last = l.to_vec();
                }
            });
    }

    fn finish(&mut self) -> &[u8] {
        let Self { last, splitter } = self;
        if let Some(s) = splitter {
            s.finish(&mut |l| {
                if !l.trim_ascii().is_empty() {
                    *last = l.to_vec();
                }
            });
        }
        &self.last
    }
}

/// The CLI's final JSON object: the last non-blank stdout line, if it parses
/// as a JSON object.
fn final_json(stdout_last_line: &[u8]) -> Option<Value> {
    let s = std::str::from_utf8(stdout_last_line).ok()?.trim();
    serde_json::from_str::<Value>(s)
        .ok()
        .filter(Value::is_object)
}

/// An error code as the CLI writes them: `A-Z`, digits and `_`.
fn is_code(c: &str) -> bool {
    !c.is_empty()
        && c.len() <= 64
        && c.bytes()
            .all(|b| b.is_ascii_uppercase() || b.is_ascii_digit() || b == b'_')
}

/// `None` on success, else the CLI's `code` (or `EXIT_FAILED`).
fn outcome_code(exit_ok: bool, json: Option<&Value>) -> Option<String> {
    let reported_failure = json.and_then(|v| v.get("ok")).and_then(Value::as_bool) == Some(false);
    if exit_ok && !reported_failure {
        return None;
    }
    let code = json
        .and_then(|v| v.get("code"))
        .and_then(Value::as_str)
        .filter(|c| is_code(c));
    Some(code.unwrap_or("EXIT_FAILED").to_string())
}

/// One connector as the web may see it: names only.
#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct SelectionEntry {
    connector: Connector,
    server: String,
    host: String,
    disabled: bool,
}

/// Selection entries from `selection.json`, dropping everything but the
/// connector, server, host and disabled flag (no pathDirs, env, commands or
/// the `ttobak` block).
fn selection_view(selection: &Value) -> Vec<SelectionEntry> {
    let name = |e: &Value, k: &str| {
        e.get(k)
            .and_then(Value::as_str)
            .filter(|s| !s.is_empty() && s.len() <= MAX_NAME_BYTES)
            .map(str::to_string)
    };
    Connector::ALL
        .iter()
        .filter_map(|&connector| {
            let e = selection.get(connector.as_str())?;
            Some(SelectionEntry {
                connector,
                server: name(e, "server")?,
                host: name(e, "host")?,
                disabled: e.get("disabled").and_then(Value::as_bool).unwrap_or(false),
            })
        })
        .collect()
}

fn is_executable_file(p: &Path) -> bool {
    use std::os::unix::fs::PermissionsExt;
    std::fs::metadata(p).is_ok_and(|m| m.is_file() && m.permissions().mode() & 0o111 != 0)
}

/// Accepts an absolute path to an executable regular file named `node`
/// (symlinks resolved). Every rejection has the same code.
fn validate_node_path(path: &str) -> Result<PathBuf, &'static str> {
    const INVALID: &str = "invalid-node-path";
    if path.is_empty() || path.len() > 4096 || path.contains('\0') {
        return Err(INVALID);
    }
    let p = Path::new(path);
    if !p.is_absolute()
        || p.components()
            .any(|c| matches!(c, Component::ParentDir | Component::CurDir))
        || p.file_name().and_then(|n| n.to_str()) != Some("node")
        || !is_executable_file(p)
    {
        return Err(INVALID);
    }
    Ok(p.to_path_buf())
}

/// True when `url` is exactly the adapter origin and path over HTTPS.
fn is_adapter_url(url: &str) -> bool {
    let Ok(u) = reqwest::Url::parse(url) else {
        return false;
    };
    u.scheme() == "https"
        && u.username().is_empty()
        && u.password().is_none()
        && u.host_str() == Some(ADAPTER_HOST)
        && u.port().is_none()
        && u.path() == ADAPTER_PATH
        && u.query().is_none()
        && u.fragment().is_none()
}

/// A served bundle is usable when within the size cap and intake-capable.
fn validate_adapter(bytes: &[u8]) -> Result<(), &'static str> {
    if bytes.len() <= MAX_ADAPTER_BYTES
        && bytes
            .windows(ADAPTER_MARKER.len())
            .any(|w| w == ADAPTER_MARKER)
    {
        Ok(())
    } else {
        Err("adapter-invalid")
    }
}

/// The login shell's `PATH` output: one line of absolute entries.
fn parse_login_path(out: &[u8]) -> Option<String> {
    if out.is_empty() || out.len() > MAX_LOGIN_PATH_BYTES {
        return None;
    }
    let s = std::str::from_utf8(out).ok()?;
    if s.chars().any(char::is_control) || !s.split(':').all(|e| e.starts_with('/')) {
        return None;
    }
    Some(s.to_string())
}

/// `$SHELL` when it is an absolute path to an existing file, else zsh.
fn login_shell(shell: Option<&std::ffi::OsStr>) -> PathBuf {
    shell
        .map(Path::new)
        .filter(|p| p.is_absolute() && p.is_file())
        .map_or_else(|| PathBuf::from(FALLBACK_SHELL), Path::to_path_buf)
}

/// The child's PATH: the node binary's directory first (servers started by
/// the CLI often need `node`/`npx`), then the login PATH.
fn child_path(node: &Path, path: &str) -> String {
    match node.parent().and_then(Path::to_str) {
        Some(dir) if !dir.is_empty() && !path.split(':').any(|e| e == dir) => {
            format!("{dir}:{path}")
        }
        _ => path.to_string(),
    }
}

/// The wall clock moved differently from the monotonic clock (which stops
/// during sleep on macOS) by more than `WAKE_JUMP_SECS`.
fn wall_jumped(wall_delta_secs: i64, mono_delta_secs: i64) -> bool {
    (wall_delta_secs - mono_delta_secs).abs() > WAKE_JUMP_SECS
}

/// The local UTC offset in minutes at `epoch_secs` (0 if unknown).
fn local_utc_offset_min(epoch_secs: i64) -> i32 {
    let t = epoch_secs as libc::time_t;
    // SAFETY: `tm` is plain data; localtime_r writes it and is thread-safe.
    let mut tm: libc::tm = unsafe { std::mem::zeroed() };
    let r = unsafe { libc::localtime_r(&t, &mut tm) };
    if r.is_null() {
        0
    } else {
        (tm.tm_gmtoff / 60) as i32
    }
}

fn default_schedule() -> Schedule {
    Schedule::Off
}

/// `app_config_dir()/intake.json`.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
struct IntakeSettings {
    #[serde(default = "default_schedule")]
    schedule: Schedule,
    #[serde(default)]
    node_path: Option<String>,
    #[serde(default)]
    login_path: Option<String>,
    #[serde(default)]
    slot_state: SlotState,
}

impl Default for IntakeSettings {
    fn default() -> Self {
        Self {
            schedule: Schedule::Off,
            node_path: None,
            login_path: None,
            slot_state: SlotState::default(),
        }
    }
}

impl IntakeSettings {
    fn from_bytes(b: &[u8]) -> Self {
        let mut s: Self = serde_json::from_slice(b).unwrap_or_default();
        if s.login_path
            .as_deref()
            .is_some_and(|p| parse_login_path(p.as_bytes()).is_none())
        {
            s.login_path = None;
        }
        s
    }
}

// ---------------------------------------------------------------- file helpers

fn now_secs() -> i64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map_or(0, |d| d.as_secs() as i64)
}

fn home_dir() -> Option<PathBuf> {
    let home = PathBuf::from(std::env::var_os("HOME")?);
    home.is_absolute().then_some(home)
}

/// A regular, non-symlink file of at most `cap` bytes; `None` otherwise.
fn read_capped(path: &Path, cap: u64) -> Option<Vec<u8>> {
    use std::os::unix::fs::OpenOptionsExt;
    let file = std::fs::OpenOptions::new()
        .read(true)
        .custom_flags(libc::O_NOFOLLOW | libc::O_NONBLOCK)
        .open(path)
        .ok()?;
    let meta = file.metadata().ok()?;
    if !meta.is_file() || meta.len() > cap {
        return None;
    }
    let mut bytes = Vec::new();
    file.take(cap + 1).read_to_end(&mut bytes).ok()?;
    (bytes.len() as u64 <= cap).then_some(bytes)
}

/// Writes `bytes` to a fresh 0600 file (never through an existing link).
fn write_new_private(path: &Path, bytes: &[u8]) -> std::io::Result<()> {
    use std::os::unix::fs::OpenOptionsExt;
    let _ = std::fs::remove_file(path);
    let mut f = std::fs::OpenOptions::new()
        .write(true)
        .create_new(true)
        .mode(0o600)
        .open(path)?;
    f.write_all(bytes)?;
    f.sync_all()
}

/// Temp file plus rename, like the caption settings.
fn save_settings(path: &Path, s: &IntakeSettings) -> std::io::Result<()> {
    if let Some(dir) = path.parent() {
        std::fs::create_dir_all(dir)?;
    }
    let json = serde_json::to_vec_pretty(s).map_err(std::io::Error::other)?;
    let tmp = path.with_extension("json.tmp");
    write_new_private(&tmp, &json)?;
    std::fs::rename(&tmp, path).inspect_err(|_| {
        let _ = std::fs::remove_file(&tmp);
    })
}

fn nvm_versions(home: &Path) -> Vec<String> {
    std::fs::read_dir(home.join(".nvm/versions/node"))
        .map(|rd| {
            rd.filter_map(|e| e.ok()?.file_name().into_string().ok())
                .take(200)
                .collect()
        })
        .unwrap_or_default()
}

fn resolve_node(stored: Option<&str>, home: &Path) -> Option<PathBuf> {
    intake_plan::resolve_node(
        stored.map(Path::new),
        &intake_plan::node_candidates(home, &nvm_versions(home)),
        is_executable_file,
    )
}

fn kill_group(pgid: i32, signal: libc::c_int) {
    if pgid > 1 {
        // SAFETY: killpg has no memory preconditions; pgid is our child's group.
        unsafe {
            libc::killpg(pgid, signal);
        }
    }
}

// ---------------------------------------------------------------- state

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub enum RunKind {
    Run,
    Setup,
    Login,
}

/// What a finished run is recorded against.
#[derive(Debug, Clone, Copy)]
enum Origin {
    /// `intake_run_now`: the latest slot at the click, if any.
    Button(Option<i64>),
    /// A scheduled or catch-up run of this slot.
    Scheduled(i64),
    /// Setup and login are not recorded.
    Unrecorded,
}

pub struct IntakeState {
    settings_path: Option<PathBuf>,
    /// Held across the (synchronous) settings write, never across an await.
    settings: Mutex<IntakeSettings>,
    running: Mutex<Option<RunKind>>,
    cancel: Mutex<Option<oneshot::Sender<()>>>,
    /// Process group of the current run (0 = none); killed on app exit.
    pgid: AtomicI32,
    progress: Mutex<ProgressLog>,
    last_result: Mutex<Option<Value>>,
    update_available: Mutex<Option<bool>>,
    adapter_busy: AtomicBool,
    /// Scheduler wake-up; `reevaluate` makes it ask `should_catch_up`.
    wake: Notify,
    reevaluate: AtomicBool,
    schedule_changed: AtomicBool,
    /// A due slot found the runner busy; re-evaluate when that run ends.
    retry_after_run: AtomicBool,
}

impl IntakeState {
    fn new(settings_path: Option<PathBuf>) -> Self {
        let settings = settings_path
            .as_deref()
            .and_then(|p| read_capped(p, MAX_SETTINGS_BYTES))
            .map(|b| IntakeSettings::from_bytes(&b))
            .unwrap_or_default();
        Self {
            settings_path,
            settings: Mutex::new(settings),
            running: Mutex::new(None),
            cancel: Mutex::new(None),
            pgid: AtomicI32::new(0),
            progress: Mutex::new(ProgressLog::default()),
            last_result: Mutex::new(None),
            update_available: Mutex::new(None),
            adapter_busy: AtomicBool::new(false),
            wake: Notify::new(),
            reevaluate: AtomicBool::new(false),
            schedule_changed: AtomicBool::new(false),
            retry_after_run: AtomicBool::new(false),
        }
    }

    fn available(&self) -> Result<&Path, String> {
        self.settings_path
            .as_deref()
            .ok_or_else(|| "unavailable".to_string())
    }

    fn snapshot(&self) -> IntakeSettings {
        self.settings.lock().clone()
    }

    fn update_settings(&self, change: impl FnOnce(&mut IntakeSettings)) -> Result<(), String> {
        let path = self.available()?;
        let mut guard = self.settings.lock();
        let mut next = guard.clone();
        change(&mut next);
        if next == *guard {
            return Ok(());
        }
        save_settings(path, &next).map_err(|e| {
            log::warn!("intake settings save failed: {:?}", e.kind());
            "save-failed".to_string()
        })?;
        *guard = next;
        Ok(())
    }

    fn try_begin(&self, kind: RunKind) -> Result<oneshot::Receiver<()>, String> {
        let mut running = self.running.lock();
        if running.is_some() {
            return Err("RUN_IN_PROGRESS".into());
        }
        *running = Some(kind);
        let (tx, rx) = oneshot::channel();
        *self.cancel.lock() = Some(tx);
        self.progress.lock().lines.clear();
        Ok(rx)
    }

    fn end(&self) {
        *self.cancel.lock() = None;
        *self.running.lock() = None;
    }

    fn poke(&self, reevaluate: bool) {
        if reevaluate {
            self.reevaluate.store(true, Ordering::SeqCst);
        }
        self.wake.notify_one();
    }
}

/// Registers the managed state. Without an app config directory every
/// command returns `unavailable` and nothing is scheduled.
pub fn manage(app: &tauri::App) {
    let path = match app.path().app_config_dir() {
        Ok(dir) => Some(dir.join(SETTINGS_FILE)),
        Err(e) => {
            log::warn!("intake runner unavailable: {e}");
            None
        }
    };
    app.manage(IntakeState::new(path));
}

/// Kills the current run's process group (app exit).
pub fn kill_running(app: &AppHandle) {
    let Some(state) = app.try_state::<IntakeState>() else {
        return;
    };
    let pgid = state.pgid.swap(0, Ordering::SeqCst);
    if pgid > 1 {
        kill_group(pgid, libc::SIGTERM);
        std::thread::sleep(Duration::from_millis(300));
        kill_group(pgid, libc::SIGKILL);
    }
}

// ---------------------------------------------------------------- spawning

struct Outcome {
    exit_code: Option<i32>,
    result: Option<Value>,
    code: Option<String>,
}

impl Outcome {
    fn failed(code: &str) -> Self {
        Self {
            exit_code: None,
            result: None,
            code: Some(code.to_string()),
        }
    }
}

/// The stored login PATH, captured once on first need.
async fn login_path(state: &IntakeState, home: &Path) -> String {
    if let Some(p) = state.snapshot().login_path {
        return p;
    }
    match capture_login_path(home).await {
        Some(p) => {
            if let Err(e) = state.update_settings(|s| s.login_path = Some(p.clone())) {
                log::warn!("login PATH not stored: {e}");
            }
            p
        }
        None => {
            log::warn!("login shell PATH unavailable; using the fallback PATH");
            FALLBACK_PATH.to_string()
        }
    }
}

/// `$SHELL -l -c 'printf %s "$PATH"'`: the only shell use. Fixed script, no
/// user input, 5 s timeout, killed with its group on timeout.
async fn capture_login_path(home: &Path) -> Option<String> {
    let shell = login_shell(std::env::var_os("SHELL").as_deref());
    let mut cmd = tokio::process::Command::new(&shell);
    cmd.args(["-l", "-c", "printf %s \"$PATH\""])
        .env_clear()
        .env("HOME", home)
        .env("SHELL", &shell)
        .env("TERM", "dumb")
        .current_dir(home)
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::null())
        .process_group(0)
        .kill_on_drop(true);
    for key in ["USER", "LOGNAME"] {
        if let Some(v) = std::env::var_os(key) {
            cmd.env(key, v);
        }
    }
    let mut child = cmd.spawn().ok()?;
    let pgid = child.id().map_or(0, |p| p as i32);
    let mut stdout = child.stdout.take()?;
    let read = async {
        let mut out = Vec::new();
        (&mut stdout)
            .take(MAX_LOGIN_PATH_BYTES as u64 + 1)
            .read_to_end(&mut out)
            .await
            .ok()?;
        child.wait().await.ok()?.success().then_some(out)
    };
    match tokio::time::timeout(LOGIN_PATH_TIMEOUT, read).await {
        Ok(Some(out)) => parse_login_path(&out),
        _ => {
            kill_group(pgid, libc::SIGKILL);
            None
        }
    }
}

/// Runs one CLI action to completion. Progress lines are emitted when
/// `track` is set (and its pgid recorded for app-exit cleanup).
async fn execute(
    app: &AppHandle,
    action: &IntakeAction,
    timeout: Duration,
    cancel: Option<oneshot::Receiver<()>>,
    track: bool,
) -> Outcome {
    let state = app.state::<IntakeState>();
    let Some(home) = home_dir() else {
        return Outcome::failed("HOME_MISSING");
    };
    let server = home.join(".ttobak").join("server.mjs");
    if !server.is_file() {
        return Outcome::failed("ADAPTER_MISSING");
    }
    let stored = state.snapshot().node_path;
    let Some(node) = resolve_node(stored.as_deref(), &home) else {
        return Outcome::failed("NODE_MISSING");
    };
    if let Some(found) = node.to_str().filter(|n| stored.as_deref() != Some(*n)) {
        let found = found.to_string();
        let _ = state.update_settings(|s| s.node_path = Some(found));
    }
    let path = child_path(&node, &login_path(&state, &home).await);

    let mut cmd = tokio::process::Command::new(&node);
    cmd.args(intake_plan::argv(action, &server))
        .env_clear()
        .env("HOME", &home)
        .env("LANG", "ko_KR.UTF-8")
        .env("PATH", path)
        .current_dir(&home)
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .process_group(0)
        .kill_on_drop(true);
    let mut child = match cmd.spawn() {
        Ok(c) => c,
        Err(e) => {
            log::warn!("intake spawn failed: {:?}", e.kind());
            return Outcome::failed("SPAWN_FAILED");
        }
    };
    let pgid = child.id().map_or(0, |p| p as i32);
    if track {
        state.pgid.store(pgid, Ordering::SeqCst);
    }

    let stdout = child.stdout.take();
    let out_task = tokio::spawn(async move {
        let mut last = LastLine::default();
        if let Some(mut s) = stdout {
            let mut buf = vec![0u8; 8192];
            while let Ok(n) = s.read(&mut buf).await {
                if n == 0 {
                    break;
                }
                last.feed(&buf[..n]);
            }
        }
        last.finish().to_vec()
    });
    let stderr = child.stderr.take();
    let err_app = app.clone();
    let err_task = tokio::spawn(async move {
        let Some(mut s) = stderr else {
            return;
        };
        let state = err_app.state::<IntakeState>();
        let mut emit = |raw: &[u8]| {
            let Some(line) = cap_progress_line(&String::from_utf8_lossy(raw)) else {
                return;
            };
            if track {
                let _ = err_app.emit(PROGRESS_EVENT, json!({ "line": line }));
                state.progress.lock().push(line);
            }
        };
        let mut splitter = LineSplitter::new(MAX_STDERR_LINE_BYTES);
        let mut buf = vec![0u8; 8192];
        while let Ok(n) = s.read(&mut buf).await {
            if n == 0 {
                break;
            }
            splitter.feed(&buf[..n], &mut emit);
        }
        splitter.finish(&mut emit);
    });

    let cancelled = async {
        let sent = match cancel {
            Some(rx) => rx.await.is_ok(),
            None => false,
        };
        if !sent {
            std::future::pending::<()>().await;
        }
    };
    let waited = tokio::select! {
        s = child.wait() => Ok(s),
        _ = tokio::time::sleep(timeout) => Err("TIMEOUT"),
        _ = cancelled => Err("CANCELLED"),
    };
    let (exit_code, forced) = match waited {
        Ok(Ok(status)) => (status.code(), None),
        Ok(Err(_)) => (None, Some("WAIT_FAILED")),
        Err(code) => {
            kill_group(pgid, libc::SIGTERM);
            if tokio::time::timeout(KILL_GRACE, child.wait())
                .await
                .is_err()
            {
                kill_group(pgid, libc::SIGKILL);
                let _ = child.wait().await;
            }
            (None, Some(code))
        }
    };
    // Orphans left in the group can hold the pipes open.
    let readers = async {
        let out = out_task.await.unwrap_or_default();
        let _ = err_task.await;
        out
    };
    let stdout_last = match tokio::time::timeout(READER_GRACE, readers).await {
        Ok(out) => out,
        Err(_) => {
            kill_group(pgid, libc::SIGKILL);
            Vec::new()
        }
    };
    if track {
        state.pgid.store(0, Ordering::SeqCst);
    }
    let result = final_json(&stdout_last);
    let code = forced
        .map(str::to_string)
        .or_else(|| outcome_code(exit_code == Some(0), result.as_ref()));
    Outcome {
        exit_code,
        result,
        code,
    }
}

/// Starts a tracked run in the background; finishes with `intake://finished`.
fn start_job(
    app: &AppHandle,
    kind: RunKind,
    action: IntakeAction,
    origin: Origin,
) -> Result<(), String> {
    let state = app.state::<IntakeState>();
    state.available()?;
    let cancel = state.try_begin(kind)?;
    let app = app.clone();
    tauri::async_runtime::spawn(async move {
        let outcome = execute(&app, &action, RUN_TIMEOUT, Some(cancel), true).await;
        let state = app.state::<IntakeState>();
        let slot = match origin {
            Origin::Button(slot) => slot,
            Origin::Scheduled(slot) => Some(slot),
            Origin::Unrecorded => None,
        };
        if let Some(slot) = slot {
            if outcome.code.as_deref() != Some("RUN_IN_PROGRESS") {
                let code = outcome.code.as_deref();
                if let Err(e) = state
                    .update_settings(|s| intake_plan::record_result(&mut s.slot_state, slot, code))
                {
                    log::warn!("intake slot state not saved: {e}");
                }
            }
        }
        let payload = json!({
            "action": kind,
            "exitCode": outcome.exit_code,
            "result": outcome.result,
            "code": outcome.code,
        });
        *state.last_result.lock() = Some(payload.clone());
        state.end();
        let _ = app.emit(FINISHED_EVENT, payload);
        if state.retry_after_run.swap(false, Ordering::SeqCst) {
            state.poke(true);
        }
    });
    Ok(())
}

// ---------------------------------------------------------------- scheduler

/// Starts the scheduler: launch catch-up, slot runs, wake catch-up.
pub fn start(app: AppHandle) {
    if app
        .try_state::<IntakeState>()
        .is_none_or(|s| s.settings_path.is_none())
    {
        return;
    }
    let wake_app = app.clone();
    app.listen(WAKE_EVENT, move |_| {
        wake_app.state::<IntakeState>().poke(true);
    });
    tauri::async_runtime::spawn(scheduler(app));
}

async fn scheduler(app: AppHandle) {
    let state = app.state::<IntakeState>();
    let mut catch_up = true; // launch
    let mut deadline: Option<i64> = None;
    let mut last_wall = now_secs();
    let mut last_mono = Instant::now();
    loop {
        let now = now_secs();
        let mono = last_mono.elapsed().as_secs() as i64;
        if wall_jumped(now - last_wall, mono) || state.reevaluate.swap(false, Ordering::SeqCst) {
            catch_up = true;
        }
        last_wall = now;
        last_mono = Instant::now();
        if state.schedule_changed.swap(false, Ordering::SeqCst) {
            deadline = None;
        }
        let settings = state.snapshot();
        let now_min = now.div_euclid(60);
        let offset = local_utc_offset_min(now);
        if catch_up || deadline.is_some_and(|d| now_min >= d) {
            catch_up = false;
            if let Some(slot) = intake_plan::should_catch_up(
                &settings.schedule,
                &settings.slot_state,
                now_min,
                offset,
            ) {
                match start_job(
                    &app,
                    RunKind::Run,
                    IntakeAction::Run,
                    Origin::Scheduled(slot),
                ) {
                    Ok(()) => log::info!("intake scheduled run started"),
                    Err(e) if e == "RUN_IN_PROGRESS" => {
                        state.retry_after_run.store(true, Ordering::SeqCst);
                    }
                    Err(e) => log::warn!("intake scheduled run not started: {e}"),
                }
            }
        }
        deadline = intake_plan::next_slot_after(&settings.schedule, now_min, offset);
        tokio::select! {
            _ = tokio::time::sleep(TICK) => {}
            _ = state.wake.notified() => {}
        }
    }
}

// ---------------------------------------------------------------- adapter

async fn fetch_adapter() -> Result<Vec<u8>, &'static str> {
    if !is_adapter_url(ADAPTER_URL) {
        return Err("adapter-invalid");
    }
    let client = reqwest::Client::builder()
        .redirect(reqwest::redirect::Policy::none())
        .https_only(true)
        .connect_timeout(ADAPTER_CONNECT_TIMEOUT)
        .timeout(ADAPTER_REQUEST_TIMEOUT)
        .build()
        .map_err(|_| "network")?;
    let mut resp = client
        .get(ADAPTER_URL)
        .send()
        .await
        .map_err(|_| "network")?;
    if !resp.status().is_success() || !is_adapter_url(resp.url().as_str()) {
        return Err("network");
    }
    if resp
        .content_length()
        .is_some_and(|n| n > MAX_ADAPTER_BYTES as u64)
    {
        return Err("adapter-invalid");
    }
    let mut body = Vec::new();
    while let Some(chunk) = resp.chunk().await.map_err(|_| "network")? {
        if body.len() + chunk.len() > MAX_ADAPTER_BYTES {
            return Err("adapter-invalid");
        }
        body.extend_from_slice(&chunk);
    }
    validate_adapter(&body)?;
    Ok(body)
}

/// Writes `server.mjs.tmp`, moves the old file to the single `server.mjs.bak`,
/// then renames the new file into place.
fn install_adapter(dir: &Path, bytes: &[u8]) -> std::io::Result<()> {
    use std::os::unix::fs::DirBuilderExt;
    std::fs::DirBuilder::new()
        .recursive(true)
        .mode(0o700)
        .create(dir)?;
    let tmp = dir.join("server.mjs.tmp");
    let target = dir.join("server.mjs");
    let bak = dir.join("server.mjs.bak");
    write_new_private(&tmp, bytes)?;
    let had_old = std::fs::symlink_metadata(&target).is_ok();
    if had_old {
        if let Err(e) = std::fs::rename(&target, &bak) {
            let _ = std::fs::remove_file(&tmp);
            return Err(e);
        }
    }
    std::fs::rename(&tmp, &target).inspect_err(|_| {
        let _ = std::fs::remove_file(&tmp);
        if had_old {
            let _ = std::fs::rename(&bak, &target);
        }
    })
}

fn local_adapter(home: &Path) -> Option<Vec<u8>> {
    read_capped(
        &home.join(".ttobak").join("server.mjs"),
        MAX_ADAPTER_BYTES as u64 + 1,
    )
}

// ---------------------------------------------------------------- commands

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
pub struct AdapterView {
    present: bool,
    update_available: Option<bool>,
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
pub struct IntakeStateView {
    adapter: AdapterView,
    node_path: Option<String>,
    node_missing: bool,
    schedule: Schedule,
    running: Option<RunKind>,
    last_result: Option<Value>,
    status: Value,
    selection: Vec<SelectionEntry>,
    launch_agent_installed: bool,
    /// Progress lines of the current or last run (not persisted).
    progress: Vec<String>,
}

fn adapter_view(state: &IntakeState, home: Option<&Path>) -> AdapterView {
    AdapterView {
        present: home.is_some_and(|h| h.join(".ttobak").join("server.mjs").is_file()),
        update_available: *state.update_available.lock(),
    }
}

#[tauri::command]
pub async fn intake_get_state(state: State<'_, IntakeState>) -> Result<IntakeStateView, String> {
    state.available()?;
    let settings = state.snapshot();
    let home = home_dir();
    let intake_dir = home.as_ref().map(|h| h.join(".ttobak").join("intake"));
    let read_json = |name: &str, cap: u64| {
        intake_dir
            .as_ref()
            .and_then(|d| read_capped(&d.join(name), cap))
            .and_then(|b| serde_json::from_slice::<Value>(&b).ok())
            .unwrap_or(Value::Null)
    };
    let node = home
        .as_deref()
        .and_then(|h| resolve_node(settings.node_path.as_deref(), h));
    let launch_agent_installed = settings.schedule != Schedule::Off
        && home
            .as_ref()
            .is_some_and(|h| std::fs::symlink_metadata(h.join(LAUNCH_AGENT_PLIST)).is_ok());
    Ok(IntakeStateView {
        adapter: adapter_view(&state, home.as_deref()),
        node_missing: node.is_none(),
        node_path: node.and_then(|n| n.to_str().map(str::to_string)),
        schedule: settings.schedule,
        running: *state.running.lock(),
        last_result: state.last_result.lock().clone(),
        status: read_json("status.json", MAX_STATUS_BYTES),
        selection: selection_view(&read_json("selection.json", MAX_SELECTION_BYTES)),
        launch_agent_installed,
        progress: state.progress.lock().lines.iter().cloned().collect(),
    })
}

#[tauri::command]
pub async fn intake_set_schedule(
    state: State<'_, IntakeState>,
    schedule: Schedule,
) -> Result<(), String> {
    state.update_settings(|s| s.schedule = schedule)?;
    state.schedule_changed.store(true, Ordering::SeqCst);
    state.poke(false);
    Ok(())
}

#[tauri::command]
pub async fn intake_set_node_path(
    state: State<'_, IntakeState>,
    path: String,
) -> Result<String, String> {
    state.available()?;
    let node = validate_node_path(&path).map_err(str::to_string)?;
    let node = node.to_str().ok_or("invalid-node-path")?.to_string();
    state.update_settings(|s| s.node_path = Some(node.clone()))?;
    Ok(node)
}

/// Runs `intake schedule off` (removes the CLI's LaunchAgent); returns the
/// CLI's JSON result or its code.
#[tauri::command]
pub async fn intake_launch_agent_off(app: AppHandle) -> Result<Value, String> {
    app.state::<IntakeState>().available()?;
    let outcome = execute(
        &app,
        &IntakeAction::ScheduleOff,
        SCHEDULE_OFF_TIMEOUT,
        None,
        false,
    )
    .await;
    match outcome.code {
        None => Ok(outcome.result.unwrap_or(Value::Null)),
        Some(code) => Err(code),
    }
}

#[tauri::command]
pub async fn intake_run_now(app: AppHandle) -> Result<(), String> {
    let settings = app.state::<IntakeState>().snapshot();
    let now = now_secs();
    let slot = intake_plan::latest_slot_at_or_before(
        &settings.schedule,
        now.div_euclid(60),
        local_utc_offset_min(now),
    );
    start_job(&app, RunKind::Run, IntakeAction::Run, Origin::Button(slot))
}

#[tauri::command]
pub async fn intake_setup(
    app: AppHandle,
    accept_private_calendar: bool,
    skip: Vec<Connector>,
) -> Result<(), String> {
    let skip: BTreeSet<Connector> = skip.into_iter().collect();
    start_job(
        &app,
        RunKind::Setup,
        IntakeAction::Setup {
            accept_private_calendar,
            skip,
        },
        Origin::Unrecorded,
    )
}

#[tauri::command]
pub async fn intake_login(app: AppHandle) -> Result<(), String> {
    start_job(
        &app,
        RunKind::Login,
        IntakeAction::Login,
        Origin::Unrecorded,
    )
}

/// Cancels the current run; `false` when nothing was running.
#[tauri::command]
pub async fn intake_cancel(state: State<'_, IntakeState>) -> Result<bool, String> {
    state.available()?;
    Ok(state
        .cancel
        .lock()
        .take()
        .is_some_and(|tx| tx.send(()).is_ok()))
}

/// Compares the served adapter with `~/.ttobak/server.mjs`. Offline or an
/// invalid bundle leaves `updateAvailable` null.
#[tauri::command]
pub async fn intake_check_adapter(state: State<'_, IntakeState>) -> Result<AdapterView, String> {
    state.available()?;
    let home = home_dir();
    let available = match (fetch_adapter().await, home.as_deref()) {
        (Ok(served), Some(h)) => Some(local_adapter(h).as_deref() != Some(served.as_slice())),
        (Err(e), _) => {
            log::info!("adapter check failed: {e}");
            None
        }
        (_, None) => None,
    };
    *state.update_available.lock() = available;
    Ok(adapter_view(&state, home.as_deref()))
}

struct BusyGuard<'a>(&'a AtomicBool);

impl Drop for BusyGuard<'_> {
    fn drop(&mut self) {
        self.0.store(false, Ordering::SeqCst);
    }
}

/// Downloads and installs the served adapter, keeping one `server.mjs.bak`.
#[tauri::command]
pub async fn intake_update_adapter(state: State<'_, IntakeState>) -> Result<AdapterView, String> {
    state.available()?;
    if state.running.lock().is_some() {
        return Err("RUN_IN_PROGRESS".into());
    }
    if state.adapter_busy.swap(true, Ordering::SeqCst) {
        return Err("busy".into());
    }
    let _busy = BusyGuard(&state.adapter_busy);
    let home = home_dir().ok_or("unavailable")?;
    let served = fetch_adapter().await.map_err(str::to_string)?;
    if state.running.lock().is_some() {
        return Err("RUN_IN_PROGRESS".into());
    }
    install_adapter(&home.join(".ttobak"), &served).map_err(|e| {
        log::warn!("adapter install failed: {:?}", e.kind());
        "save-failed".to_string()
    })?;
    *state.update_available.lock() = Some(false);
    Ok(adapter_view(&state, Some(&home)))
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn progress_lines_are_capped() {
        assert_eq!(cap_progress_line("   "), None);
        assert_eq!(cap_progress_line("  안녕 \r").as_deref(), Some("안녕"));
        let long = "가".repeat(400);
        let capped = cap_progress_line(&long).expect("line");
        assert_eq!(capped.chars().count(), MAX_PROGRESS_CHARS);

        let mut log = ProgressLog::default();
        for i in 0..250 {
            log.push(format!("line {i}"));
        }
        assert_eq!(log.lines.len(), MAX_PROGRESS_LINES);
        assert_eq!(log.lines.front().map(String::as_str), Some("line 50"));
        assert_eq!(log.lines.back().map(String::as_str), Some("line 249"));
    }

    #[test]
    fn stdout_json_is_last_line() {
        let mut lines = LastLine::default();
        lines.feed(b"noise\n{\"ok\":false,\"code\":\"X\"}\n");
        lines.feed(b"{\"ok\":true,");
        lines.feed(b"\"email\":\"a@b\"}\n\n  \n");
        let v = final_json(lines.finish()).expect("json");
        assert_eq!(v, json!({"ok": true, "email": "a@b"}));

        let mut no_newline = LastLine::default();
        no_newline.feed(b"{\"ok\":true}");
        assert_eq!(final_json(no_newline.finish()), Some(json!({"ok": true})));

        let mut trailing_noise = LastLine::default();
        trailing_noise.feed(b"{\"ok\":true}\nnot json\n");
        assert_eq!(final_json(trailing_noise.finish()), None);
        assert_eq!(final_json(b"[1,2]"), None, "only an object counts");
        assert_eq!(final_json(b""), None);
    }

    #[test]
    fn line_splitter_caps_long_lines() {
        let mut out = Vec::new();
        let mut s = LineSplitter::new(8);
        s.feed(b"abcdefghijkl\nxy", &mut |l| out.push(l.to_vec()));
        s.feed(b"z\n", &mut |l| out.push(l.to_vec()));
        s.finish(&mut |l| out.push(l.to_vec()));
        assert_eq!(out, vec![b"abcdefgh".to_vec(), b"xyz".to_vec()]);
    }

    #[test]
    fn selection_view_drops_paths() {
        let selection = json!({
            "version": 1,
            "calendar": {"host": "claude", "server": "aws-outlook-mcp", "sourceId": "s",
                "pathDirs": ["/secret/bin"], "selectedAt": "t", "disabled": true},
            "sfdc": {"host": "kiro", "server": "aws-sentral-mcp", "env": {"TOKEN": "x"},
                "command": "/bin/x"},
            "slack": {"host": "claude"},
            "meetings": "bad",
            "ttobak": {"apiUrl": "https://x", "clientId": "c"}
        });
        let view = selection_view(&selection);
        assert_eq!(
            view,
            vec![
                SelectionEntry {
                    connector: Connector::Calendar,
                    server: "aws-outlook-mcp".into(),
                    host: "claude".into(),
                    disabled: true,
                },
                SelectionEntry {
                    connector: Connector::Sfdc,
                    server: "aws-sentral-mcp".into(),
                    host: "kiro".into(),
                    disabled: false,
                },
            ]
        );
        let text = serde_json::to_string(&view).expect("json");
        for leaked in [
            "pathDirs", "secret", "TOKEN", "command", "apiUrl", "sourceId",
        ] {
            assert!(!text.contains(leaked), "{leaked} leaked");
        }
        assert!(selection_view(&json!(null)).is_empty());
    }

    #[cfg(unix)]
    #[test]
    fn node_path_validation_rejects_non_node() {
        use std::os::unix::fs::PermissionsExt;
        let dir = std::env::temp_dir().join(format!(
            "ttobak-node-{}-{}",
            std::process::id(),
            std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .expect("time")
                .as_nanos()
        ));
        std::fs::create_dir_all(dir.join("bin")).expect("dir");
        let node = dir.join("bin/node");
        std::fs::write(&node, b"#!/bin/sh\n").expect("write");
        std::fs::set_permissions(&node, std::fs::Permissions::from_mode(0o755)).expect("chmod");
        let other = dir.join("bin/python3");
        std::fs::write(&other, b"#!/bin/sh\n").expect("write");
        std::fs::set_permissions(&other, std::fs::Permissions::from_mode(0o755)).expect("chmod");
        let plain = dir.join("plain/node");
        std::fs::create_dir_all(plain.parent().expect("parent")).expect("dir");
        std::fs::write(&plain, b"x").expect("write");
        std::fs::set_permissions(&plain, std::fs::Permissions::from_mode(0o644)).expect("chmod");
        let link = dir.join("link/node");
        std::fs::create_dir_all(link.parent().expect("parent")).expect("dir");
        std::os::unix::fs::symlink(&node, &link).expect("symlink");

        assert_eq!(
            validate_node_path(node.to_str().expect("utf8")),
            Ok(node.clone())
        );
        assert!(validate_node_path(link.to_str().expect("utf8")).is_ok());
        for bad in [
            other.to_str().expect("utf8").to_string(),
            plain.to_str().expect("utf8").to_string(),
            dir.join("bin").to_str().expect("utf8").to_string(),
            dir.join("missing/node").to_str().expect("utf8").to_string(),
            "bin/node".to_string(),
            "node".to_string(),
            String::new(),
            format!("{}/bin/../bin/node", dir.display()),
            format!("{}\0", node.display()),
        ] {
            assert_eq!(
                validate_node_path(&bad),
                Err("invalid-node-path"),
                "{bad:?} accepted"
            );
        }
        let _ = std::fs::remove_dir_all(&dir);
    }

    #[test]
    fn adapter_update_requires_exact_origin() {
        assert!(is_adapter_url(ADAPTER_URL));
        for bad in [
            "http://ttobak.atomai.click/mcp/ttobak-mcp.mjs",
            "https://ttobak.atomai.click.evil.example/mcp/ttobak-mcp.mjs",
            "https://evil.example/mcp/ttobak-mcp.mjs",
            "https://ttobak.atomai.click:8443/mcp/ttobak-mcp.mjs",
            "https://ttobak.atomai.click/mcp/other.mjs",
            "https://ttobak.atomai.click/mcp/ttobak-mcp.mjs?x=1",
            "https://user@ttobak.atomai.click/mcp/ttobak-mcp.mjs",
        ] {
            assert!(!is_adapter_url(bad), "{bad} accepted");
        }
        assert_eq!(validate_adapter(b"// x\nintake setup\n"), Ok(()));
        assert_eq!(validate_adapter(b"// no marker"), Err("adapter-invalid"));
        let mut big = vec![b' '; MAX_ADAPTER_BYTES + 1];
        big[..12].copy_from_slice(ADAPTER_MARKER);
        assert_eq!(validate_adapter(&big), Err("adapter-invalid"));
    }

    #[test]
    fn outcome_code_uses_cli_code_only_on_failure() {
        assert_eq!(outcome_code(true, Some(&json!({"ok": true}))), None);
        assert_eq!(outcome_code(true, None), None);
        assert_eq!(
            outcome_code(
                false,
                Some(&json!({"ok": false, "code": "AUTH_EXPIRED_MIDWAY"}))
            )
            .as_deref(),
            Some("AUTH_EXPIRED_MIDWAY")
        );
        assert_eq!(
            outcome_code(true, Some(&json!({"ok": false, "code": "PARTIAL_FAILURE"}))).as_deref(),
            Some("PARTIAL_FAILURE")
        );
        assert_eq!(outcome_code(false, None).as_deref(), Some("EXIT_FAILED"));
        assert_eq!(
            outcome_code(false, Some(&json!({"ok": false, "code": "bad code; rm"}))).as_deref(),
            Some("EXIT_FAILED"),
            "an unexpected code shape is not passed through"
        );
    }

    #[test]
    fn login_path_output_is_validated() {
        assert_eq!(
            parse_login_path(b"/opt/homebrew/bin:/usr/bin:/bin").as_deref(),
            Some("/opt/homebrew/bin:/usr/bin:/bin")
        );
        assert_eq!(parse_login_path(b""), None);
        assert_eq!(parse_login_path(b"relative:/usr/bin"), None);
        assert_eq!(parse_login_path(b"/usr/bin\n/bin"), None);
        assert_eq!(parse_login_path(&[b'/'; 9000]), None);
        assert_eq!(parse_login_path(b"/usr/\xffbin"), None);
    }

    #[test]
    fn child_path_prepends_node_dir_once() {
        let node = Path::new("/Users/u/.nvm/versions/node/v22.1.0/bin/node");
        assert_eq!(
            child_path(node, "/usr/bin:/bin"),
            "/Users/u/.nvm/versions/node/v22.1.0/bin:/usr/bin:/bin"
        );
        assert_eq!(
            child_path(Path::new("/usr/bin/node"), "/usr/bin:/bin"),
            "/usr/bin:/bin"
        );
    }

    #[test]
    fn wall_clock_jump_counts_as_wake() {
        assert!(!wall_jumped(60, 60));
        assert!(!wall_jumped(170, 60));
        assert!(wall_jumped(181, 60));
        assert!(wall_jumped(-200, 60), "a clock set back also re-evaluates");
    }

    #[test]
    fn local_offset_is_plausible() {
        let now = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .expect("time")
            .as_secs() as i64;
        let off = local_utc_offset_min(now);
        assert!((-14 * 60..=14 * 60).contains(&off), "{off}");
    }

    #[test]
    fn settings_round_trip_and_tolerate_garbage() {
        let s = IntakeSettings::from_bytes(
            br#"{"schedule":{"kind":"daily","times":["09:30"]},"nodePath":"/opt/homebrew/bin/node",
                "loginPath":"/usr/bin","slotState":{"lastSatisfiedSlot":5}}"#,
        );
        assert_eq!(
            s.schedule,
            crate::intake_plan::Schedule::Daily(crate::intake_plan::HhMm {
                hour: 9,
                minute: 30
            })
        );
        assert_eq!(s.slot_state.last_satisfied_slot, Some(5));
        let back = IntakeSettings::from_bytes(&serde_json::to_vec(&s).expect("json"));
        assert_eq!(back, s);
        assert_eq!(
            IntakeSettings::from_bytes(b"{nope"),
            IntakeSettings::default()
        );
        assert_eq!(
            IntakeSettings::from_bytes(br#"{"schedule":{"kind":"weekly","times":[]}}"#),
            IntakeSettings::default()
        );
    }

    #[test]
    fn login_shell_requires_an_absolute_existing_file() {
        use std::ffi::OsStr;
        assert_eq!(
            login_shell(Some(OsStr::new("/bin/sh"))),
            PathBuf::from("/bin/sh")
        );
        assert_eq!(
            login_shell(Some(OsStr::new("sh"))),
            PathBuf::from(FALLBACK_SHELL)
        );
        assert_eq!(
            login_shell(Some(OsStr::new("/bin"))),
            PathBuf::from(FALLBACK_SHELL)
        );
        assert_eq!(
            login_shell(Some(OsStr::new("/no/such/shell"))),
            PathBuf::from(FALLBACK_SHELL)
        );
        assert_eq!(login_shell(None), PathBuf::from(FALLBACK_SHELL));
    }

    #[test]
    fn adapter_install_keeps_exactly_one_backup() {
        let dir = std::env::temp_dir().join(format!(
            "ttobak-adapter-{}-{}",
            std::process::id(),
            std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .expect("time")
                .as_nanos()
        ));
        install_adapter(&dir, b"v1").expect("first install");
        assert_eq!(std::fs::read(dir.join("server.mjs")).expect("read"), b"v1");
        assert!(!dir.join("server.mjs.bak").exists());
        install_adapter(&dir, b"v2").expect("second");
        install_adapter(&dir, b"v3").expect("third");
        assert_eq!(std::fs::read(dir.join("server.mjs")).expect("read"), b"v3");
        assert_eq!(
            std::fs::read(dir.join("server.mjs.bak")).expect("bak"),
            b"v2"
        );
        let names: Vec<String> = std::fs::read_dir(&dir)
            .expect("dir")
            .map(|e| e.expect("entry").file_name().into_string().expect("utf8"))
            .collect();
        assert_eq!(names.len(), 2, "{names:?}");
        let _ = std::fs::remove_dir_all(&dir);
    }
}
