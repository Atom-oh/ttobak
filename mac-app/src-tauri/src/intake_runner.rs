//! Local intake runner (ADR-003): runs `node ~/.ttobak/server.mjs intake ...`
//! from the app, schedules runs, catches up missed slots at launch and wake,
//! and updates the adapter bundle from the app's own origin.
//!
//! The window loads the remote SPA, so every command here is callable from a
//! remote origin. Commands take typed options only; the one path accepted
//! (`intake_set_node_path`) must be an executable file named `node`. The
//! WebView receives only the CLI's final JSON line and its stderr progress
//! lines, never stdout noise, argv, env or selection paths.

use std::collections::{BTreeSet, VecDeque};
use std::ffi::OsString;
use std::io::{Read, Write};
use std::path::{Component, Path, PathBuf};
use std::process::Stdio;
use std::sync::atomic::{AtomicBool, AtomicI32, AtomicU64, Ordering};
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};

use parking_lot::Mutex;
use serde::{Deserialize, Serialize};
use serde_json::{json, Value};
use tauri::{AppHandle, Emitter, Listener, Manager, State};
use tokio::io::AsyncReadExt;
use tokio::sync::{oneshot, Notify};

use crate::intake_plan::{self, CalendarResponse, Connector, IntakeAction, Schedule, SlotState};
use crate::report_runner::ReportSettings;

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
/// A wake re-evaluates slots only after this, so the network can come back.
const WAKE_CATCH_UP_DELAY: Duration = Duration::from_secs(45);

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
pub(crate) struct ProgressLog {
    lines: VecDeque<String>,
}

impl ProgressLog {
    pub(crate) fn clear(&mut self) {
        self.lines.clear();
    }

    pub(crate) fn to_vec(&self) -> Vec<String> {
        self.lines.iter().cloned().collect()
    }

    pub(crate) fn push(&mut self, line: String) {
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

/// What a finished run records against its slot.
#[derive(Debug, Clone, PartialEq, Eq)]
enum RecordDecision {
    /// The run never started (another run or an adapter update held the slot).
    Skip,
    /// Passed to `record_result`; `None` and `PARTIAL_FAILURE` satisfy the slot.
    Record(Option<String>),
}

/// `PARTIAL_FAILURE` satisfies the slot only when a connector in
/// `result.results` succeeded; with none, the run is recorded as
/// `ALL_FAILED` (an attempt only), e.g. a wake run before the network is up.
fn slot_outcome(exit_ok: bool, code: Option<&str>, result: Option<&Value>) -> RecordDecision {
    match code {
        Some("RUN_IN_PROGRESS" | "ADAPTER_UPDATING" | "SHUTTING_DOWN") => RecordDecision::Skip,
        None if exit_ok => RecordDecision::Record(None),
        None => RecordDecision::Record(Some("EXIT_FAILED".into())),
        Some("PARTIAL_FAILURE") => {
            let any_ok = result
                .and_then(|v| v.get("results"))
                .and_then(Value::as_object)
                .is_some_and(|m| {
                    m.values()
                        .any(|r| r.get("result").and_then(Value::as_str) == Some("ok"))
                });
            RecordDecision::Record(Some(
                if any_ok {
                    "PARTIAL_FAILURE"
                } else {
                    "ALL_FAILED"
                }
                .into(),
            ))
        }
        Some(c) => RecordDecision::Record(Some(c.to_string())),
    }
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

/// The sync options stored in `selection.json`, as names only.
#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct OptionsView {
    calendar_responses: Vec<CalendarResponse>,
    sfdc_contacts: bool,
    /// No calendar responses are stored: the defaults apply, so previously
    /// synced unanswered events are removed on the next run.
    defaults: bool,
}

/// The top-level `options` object: `calendarResponses` (known names, canonical
/// order; absent or not a list: the CLI default accept/organizer/tentative)
/// and `sfdcContacts` (only `false` turns it off), mirroring the CLI's
/// `syncOptions`.
fn options_view(selection: &Value) -> OptionsView {
    let options = selection.get("options");
    let stored = options
        .and_then(|o| o.get("calendarResponses"))
        .and_then(Value::as_array);
    let calendar_responses = match stored {
        Some(stored) => CalendarResponse::ALL
            .into_iter()
            .filter(|r| stored.iter().any(|v| v.as_str() == Some(r.as_str())))
            .collect(),
        None => vec![
            CalendarResponse::Accept,
            CalendarResponse::Organizer,
            CalendarResponse::Tentative,
        ],
    };
    let sfdc_contacts = options
        .and_then(|o| o.get("sfdcContacts"))
        .and_then(Value::as_bool)
        != Some(false);
    OptionsView {
        calendar_responses,
        sfdc_contacts,
        defaults: stored.is_none(),
    }
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

/// Directories the app appends to the login PATH when they exist and are
/// missing: `aim` lives in `~/.toolbox/bin` (often added only by `~/.zshrc`,
/// which a login shell does not read) and the AIM server wrappers in
/// `~/.aim/mcp-servers` exec it.
const PATH_REPAIR_DIRS: [&str; 3] = [".toolbox/bin", ".aim/mcp-servers", ".local/bin"];

/// `path` with each existing, absent `PATH_REPAIR_DIRS` entry appended in
/// order. Entries that would not be absolute are never added.
pub(crate) fn repair_path(path: &str, home: &Path, is_dir: impl Fn(&Path) -> bool) -> String {
    let mut out = path.to_string();
    if !home.is_absolute() {
        return out;
    }
    for rel in PATH_REPAIR_DIRS {
        let dir = home.join(rel);
        let Some(entry) = dir.to_str() else { continue };
        if entry.contains(':') || out.split(':').any(|e| e == entry) || !is_dir(&dir) {
            continue;
        }
        out.push(':');
        out.push_str(entry);
    }
    out
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
pub(crate) struct IntakeSettings {
    #[serde(default = "default_schedule")]
    schedule: Schedule,
    #[serde(default)]
    node_path: Option<String>,
    #[serde(default)]
    login_path: Option<String>,
    #[serde(default)]
    slot_state: SlotState,
    /// Report-run settings; an invalid value falls back to the default
    /// without discarding the rest of the file.
    #[serde(default, deserialize_with = "crate::report_runner::lenient_settings")]
    pub(crate) report: ReportSettings,
}

impl Default for IntakeSettings {
    fn default() -> Self {
        Self {
            schedule: Schedule::Off,
            node_path: None,
            login_path: None,
            slot_state: SlotState::default(),
            report: ReportSettings::default(),
        }
    }
}

impl IntakeSettings {
    pub(crate) fn from_bytes(b: &[u8]) -> Self {
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
pub(crate) fn write_new_private(path: &Path, bytes: &[u8]) -> std::io::Result<()> {
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

/// A cancelled or timed-out run always ends with a group SIGKILL after the
/// grace period: provider grandchildren own their pipes, so the leader exiting
/// proves nothing about them. Normal completion leaves the group alone.
fn should_group_kill(forced: Option<&str>) -> bool {
    matches!(forced, Some("TIMEOUT" | "CANCELLED"))
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
    /// `intake_launch_agent_off`: tracked and exclusive, but not a run.
    ScheduleOff,
    /// `intake_set_options`: tracked and exclusive, but not a run.
    Options,
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
    /// Set by the app-exit path before `kill_running`; no new child spawns.
    shutting_down: AtomicBool,
    progress: Mutex<ProgressLog>,
    /// Bumped per begun run; a reader of an older run never writes progress.
    run_gen: AtomicU64,
    last_result: Mutex<Option<Value>>,
    update_available: Mutex<Option<bool>>,
    adapter_busy: AtomicBool,
    /// Scheduler wake-up; `reevaluate` makes it ask `should_catch_up`.
    wake: Notify,
    reevaluate: AtomicBool,
    schedule_changed: AtomicBool,
    /// A due slot found the runner busy; re-evaluate when that run ends.
    retry_after_run: AtomicBool,
    /// A delayed wake re-evaluation is pending.
    wake_pending: AtomicBool,
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
            shutting_down: AtomicBool::new(false),
            progress: Mutex::new(ProgressLog::default()),
            run_gen: AtomicU64::new(0),
            last_result: Mutex::new(None),
            update_available: Mutex::new(None),
            adapter_busy: AtomicBool::new(false),
            wake: Notify::new(),
            reevaluate: AtomicBool::new(false),
            schedule_changed: AtomicBool::new(false),
            retry_after_run: AtomicBool::new(false),
            wake_pending: AtomicBool::new(false),
        }
    }

    pub(crate) fn available(&self) -> Result<&Path, String> {
        self.settings_path
            .as_deref()
            .ok_or_else(|| "unavailable".to_string())
    }

    pub(crate) fn snapshot(&self) -> IntakeSettings {
        self.settings.lock().clone()
    }

    pub(crate) fn update_settings(
        &self,
        change: impl FnOnce(&mut IntakeSettings),
    ) -> Result<(), String> {
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

    /// Claims the run slot. Refused while another run or an adapter update
    /// is going; both checks happen under the `running` lock, which
    /// `begin_adapter_update` also takes, so the two never overlap.
    fn try_begin(&self, kind: RunKind) -> Result<oneshot::Receiver<()>, String> {
        let mut running = self.running.lock();
        if running.is_some() {
            return Err("RUN_IN_PROGRESS".into());
        }
        if self.adapter_busy.load(Ordering::SeqCst) {
            return Err("ADAPTER_UPDATING".into());
        }
        *running = Some(kind);
        let (tx, rx) = oneshot::channel();
        *self.cancel.lock() = Some(tx);
        self.run_gen.fetch_add(1, Ordering::SeqCst);
        if !matches!(kind, RunKind::ScheduleOff | RunKind::Options) {
            self.progress.lock().lines.clear();
        }
        Ok(rx)
    }

    /// Claims the adapter update; refused while a run holds the slot.
    fn begin_adapter_update(&self) -> Result<AdapterGuard<'_>, String> {
        let running = self.running.lock();
        if running.is_some() {
            return Err("RUN_IN_PROGRESS".into());
        }
        if self.adapter_busy.swap(true, Ordering::SeqCst) {
            return Err("busy".into());
        }
        Ok(AdapterGuard(self))
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

/// Grace before SIGKILL at app exit.
const QUIT_GRACE: Duration = Duration::from_millis(300);
/// At app exit a report CLI gets longer: on SIGTERM it stops claude's own
/// process group (not reached by a kill of node's group) and removes its run
/// and session directories. Waited for only while a report runs, and only
/// until its group is gone.
const REPORT_QUIT_GRACE: Duration = Duration::from_secs(2);

/// Whether any process of group `pgid` still exists (EPERM counts).
fn group_exists(pgid: i32) -> bool {
    // SAFETY: signal 0 only checks that the group exists.
    if unsafe { libc::killpg(pgid, 0) } == 0 {
        return true;
    }
    std::io::Error::last_os_error().raw_os_error() == Some(libc::EPERM)
}

/// Waits until `pgid` is gone, at most `max`, polling every 50 ms.
fn wait_group_gone(pgid: i32, max: Duration) {
    let deadline = std::time::Instant::now() + max;
    while group_exists(pgid) && std::time::Instant::now() < deadline {
        std::thread::sleep(Duration::from_millis(50));
    }
}

/// Kills the current intake and report runs' process groups (app exit) and
/// refuses any later spawn of either.
pub fn kill_running(app: &AppHandle) {
    let mut intake = 0;
    let mut report = 0;
    if let Some(state) = app.try_state::<IntakeState>() {
        state.shutting_down.store(true, Ordering::SeqCst);
        intake = state.pgid.swap(0, Ordering::SeqCst);
    }
    if let Some(state) = app.try_state::<crate::report_runner::ReportState>() {
        let track = state.track();
        track.shutting_down.store(true, Ordering::SeqCst);
        report = track.pgid.swap(0, Ordering::SeqCst);
    }
    let groups: Vec<i32> = [intake, report].into_iter().filter(|&g| g > 1).collect();
    if groups.is_empty() {
        return;
    }
    for &g in &groups {
        kill_group(g, libc::SIGTERM);
    }
    let started = std::time::Instant::now();
    if report > 1 {
        wait_group_gone(report, REPORT_QUIT_GRACE);
    }
    if let Some(rest) = QUIT_GRACE.checked_sub(started.elapsed()) {
        if intake > 1 {
            std::thread::sleep(rest);
        }
    }
    for &g in &groups {
        kill_group(g, libc::SIGKILL);
    }
}

// ---------------------------------------------------------------- spawning

pub(crate) struct Outcome {
    pub exit_code: Option<i32>,
    pub result: Option<Value>,
    pub code: Option<String>,
}

impl Outcome {
    pub(crate) fn failed(code: &str) -> Self {
        Self {
            exit_code: None,
            result: None,
            code: Some(code.to_string()),
        }
    }
}

/// The stored login PATH, captured once on first need.
/// The stored login PATH (captured once when absent), repaired on every use
/// so a value cached before the toolbox or AIM install still finds them.
async fn login_path(state: &IntakeState, home: &Path) -> String {
    if let Some(p) = state.snapshot().login_path {
        return repair_path(&p, home, Path::is_dir);
    }
    match capture_login_path(home).await {
        Some(p) => {
            let p = repair_path(&p, home, Path::is_dir);
            if let Err(e) = state.update_settings(|s| s.login_path = Some(p.clone())) {
                log::warn!("login PATH not stored: {e}");
            }
            p
        }
        None => {
            log::warn!("login shell PATH unavailable; using the fallback PATH");
            repair_path(FALLBACK_PATH, home, Path::is_dir)
        }
    }
}

/// Result of a forced PATH recapture: the directories the repair appended and
/// whether the login shell answered (otherwise the fallback PATH was used).
#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct PathRefresh {
    pub added: Vec<String>,
    pub captured: bool,
}

/// Recaptures the login PATH (ignoring the stored value), repairs and stores it.
pub(crate) async fn refresh_login_path(
    state: &IntakeState,
    home: &Path,
) -> Result<PathRefresh, &'static str> {
    let captured = capture_login_path(home).await;
    let base = captured
        .clone()
        .unwrap_or_else(|| FALLBACK_PATH.to_string());
    let repaired = repair_path(&base, home, Path::is_dir);
    let added = repaired
        .split(':')
        .skip(base.split(':').count())
        .map(str::to_string)
        .collect();
    if captured.is_some() {
        state
            .update_settings(|s| s.login_path = Some(repaired.clone()))
            .map_err(|_| "save-failed")?;
    }
    Ok(PathRefresh {
        added,
        captured: captured.is_some(),
    })
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

/// Where a runner (intake or report) records its current child's process
/// group, and the app-exit flag that refuses any later spawn.
pub(crate) struct ProcTrack<'a> {
    pub pgid: &'a AtomicI32,
    pub shutting_down: &'a AtomicBool,
}

/// Resolved inputs of a `node ~/.ttobak/server.mjs ...` spawn.
pub(crate) struct NodeLaunch {
    pub home: PathBuf,
    pub node: PathBuf,
    pub server: PathBuf,
    pub path: String,
}

/// Resolves HOME, the adapter, the node binary (stored when newly found) and
/// the child PATH; a fixed failure code otherwise.
pub(crate) async fn node_launch(state: &IntakeState) -> Result<NodeLaunch, &'static str> {
    let home = home_dir().ok_or("HOME_MISSING")?;
    let server = home.join(".ttobak").join("server.mjs");
    if !server.is_file() {
        return Err("ADAPTER_MISSING");
    }
    let stored = state.snapshot().node_path;
    let node = resolve_node(stored.as_deref(), &home).ok_or("NODE_MISSING")?;
    if let Some(found) = node.to_str().filter(|n| stored.as_deref() != Some(*n)) {
        let found = found.to_string();
        let _ = state.update_settings(|s| s.node_path = Some(found));
    }
    let path = child_path(&node, &login_path(state, &home).await);
    Ok(NodeLaunch {
        home,
        node,
        server,
        path,
    })
}

/// `node <args>` with a cleared environment (HOME, LANG, PATH) in HOME.
/// Callers may add further fixed variables; `run_child` sets the stdio and
/// the process group.
pub(crate) fn node_command(launch: &NodeLaunch, args: Vec<OsString>) -> tokio::process::Command {
    let mut cmd = tokio::process::Command::new(&launch.node);
    cmd.args(args)
        .env_clear()
        .env("HOME", &launch.home)
        .env("LANG", "ko_KR.UTF-8")
        .env("PATH", &launch.path)
        .current_dir(&launch.home);
    cmd
}

/// Spawns `cmd` in its own process group (recorded in `track.pgid` for
/// app-exit cleanup) and waits for it, the timeout or a cancel. A forced end
/// SIGTERMs the group, SIGKILLs it after `kill_grace`, and always SIGKILLs
/// the group once more. Each capped stderr line goes to `on_line`; the last
/// stdout line is parsed as the result.
pub(crate) async fn run_child(
    mut cmd: tokio::process::Command,
    track: ProcTrack<'_>,
    timeout: Duration,
    kill_grace: Duration,
    cancel: Option<oneshot::Receiver<()>>,
    mut on_line: impl FnMut(String) + Send + 'static,
) -> Outcome {
    cmd.stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .process_group(0)
        .kill_on_drop(true);
    // Checked right before spawn: the PATH capture can outlast the app-exit
    // kill.
    if track.shutting_down.load(Ordering::SeqCst) {
        return Outcome::failed("SHUTTING_DOWN");
    }
    let mut child = match cmd.spawn() {
        Ok(c) => c,
        Err(e) => {
            log::warn!("CLI spawn failed: {:?}", e.kind());
            return Outcome::failed("SPAWN_FAILED");
        }
    };
    let pgid = child.id().map_or(0, |p| p as i32);
    track.pgid.store(pgid, Ordering::SeqCst);

    let stdout = child.stdout.take();
    let mut out_task = tokio::spawn(async move {
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
    let mut err_task = tokio::spawn(async move {
        let Some(mut s) = stderr else {
            return;
        };
        let mut emit = |raw: &[u8]| {
            if let Some(line) = cap_progress_line(&String::from_utf8_lossy(raw)) {
                on_line(line);
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
            if tokio::time::timeout(kill_grace, child.wait())
                .await
                .is_err()
            {
                kill_group(pgid, libc::SIGKILL);
                let _ = child.wait().await;
            }
            // Even if the leader exited, a provider that ignored SIGTERM may
            // live on in the group (ESRCH is ignored).
            if should_group_kill(Some(code)) {
                kill_group(pgid, libc::SIGKILL);
            }
            (None, Some(code))
        }
    };
    // Orphans left in the group can hold the pipes open; their readers are
    // aborted so nothing reaches a later run.
    let readers = async {
        let out = (&mut out_task).await.unwrap_or_default();
        let _ = (&mut err_task).await;
        out
    };
    let stdout_last = match tokio::time::timeout(READER_GRACE, readers).await {
        Ok(out) => out,
        Err(_) => {
            kill_group(pgid, libc::SIGKILL);
            out_task.abort();
            err_task.abort();
            Vec::new()
        }
    };
    track.pgid.store(0, Ordering::SeqCst);
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

/// Runs one CLI action to completion in the claimed run slot (its pgid is
/// recorded for app-exit cleanup). Progress lines are kept and emitted when
/// `progress` is set.
async fn execute(
    app: &AppHandle,
    action: &IntakeAction,
    timeout: Duration,
    cancel: Option<oneshot::Receiver<()>>,
    progress: bool,
) -> Outcome {
    let state = app.state::<IntakeState>();
    let launch = match node_launch(&state).await {
        Ok(l) => l,
        Err(code) => return Outcome::failed(code),
    };
    let cmd = node_command(&launch, intake_plan::argv(action, &launch.server));
    // `try_begin` bumped the generation before this run started.
    let gen = state.run_gen.load(Ordering::SeqCst);
    let line_app = app.clone();
    let on_line = move |line: String| {
        let state = line_app.state::<IntakeState>();
        if progress && state.run_gen.load(Ordering::SeqCst) == gen {
            let _ = line_app.emit(PROGRESS_EVENT, json!({ "line": line }));
            state.progress.lock().push(line);
        }
    };
    let track = ProcTrack {
        pgid: &state.pgid,
        shutting_down: &state.shutting_down,
    };
    run_child(cmd, track, timeout, KILL_GRACE, cancel, on_line).await
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
            let decision = slot_outcome(
                outcome.exit_code == Some(0),
                outcome.code.as_deref(),
                outcome.result.as_ref(),
            );
            if let RecordDecision::Record(code) = decision {
                if let Err(e) = state.update_settings(|s| {
                    intake_plan::record_result(&mut s.slot_state, slot, code.as_deref())
                }) {
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
        poke_after_wake(&wake_app);
    });
    tauri::async_runtime::spawn(scheduler(app));
}

/// Re-evaluates slots `WAKE_CATCH_UP_DELAY` after a wake; one pending at a time.
fn poke_after_wake(app: &AppHandle) {
    if app
        .state::<IntakeState>()
        .wake_pending
        .swap(true, Ordering::SeqCst)
    {
        return;
    }
    let app = app.clone();
    tauri::async_runtime::spawn(async move {
        tokio::time::sleep(WAKE_CATCH_UP_DELAY).await;
        let state = app.state::<IntakeState>();
        state.wake_pending.store(false, Ordering::SeqCst);
        state.poke(true);
    });
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
        let woke = wall_jumped(now - last_wall, mono);
        if woke {
            // A slot that passed during sleep waits for the delayed catch-up
            // instead of firing before the network is back.
            deadline = None;
            poke_after_wake(&app);
        }
        if state.reevaluate.swap(false, Ordering::SeqCst) {
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
                    Err(e) if e == "RUN_IN_PROGRESS" || e == "ADAPTER_UPDATING" => {
                        state.retry_after_run.store(true, Ordering::SeqCst);
                        // The busy run may have ended before the flag was set;
                        // its finisher then missed it, so re-poke here.
                        let idle = state.running.lock().is_none()
                            && !state.adapter_busy.load(Ordering::SeqCst);
                        if idle && state.retry_after_run.swap(false, Ordering::SeqCst) {
                            state.poke(true);
                        }
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

/// Writes `server.mjs.tmp`, makes the single `server.mjs.bak` a link (or
/// copy) of the current file, then renames the new file over it. The
/// current file is never absent: the only change to `server.mjs` is the
/// final atomic rename.
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
    let backup = || -> std::io::Result<()> {
        if std::fs::symlink_metadata(&target).is_err() {
            return Ok(());
        }
        match std::fs::remove_file(&bak) {
            Ok(()) => {}
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => {}
            Err(e) => return Err(e),
        }
        std::fs::hard_link(&target, &bak).or_else(|_| std::fs::copy(&target, &bak).map(|_| ()))
    };
    backup()
        .and_then(|()| std::fs::rename(&tmp, &target))
        .inspect_err(|_| {
            let _ = std::fs::remove_file(&tmp);
        })
}

pub(crate) fn local_adapter(home: &Path) -> Option<Vec<u8>> {
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
    /// Sync options from `selection.json` (names only).
    options: OptionsView,
    launch_agent_installed: bool,
    /// Progress lines of the current or last run (not persisted).
    progress: Vec<String>,
}

/// The CLI's LaunchAgent plist exists (the UI warns when the app schedule is
/// also on).
fn launch_agent_present(home: &Path) -> bool {
    std::fs::symlink_metadata(home.join(LAUNCH_AGENT_PLIST)).is_ok()
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
    let launch_agent_installed = home.as_deref().is_some_and(launch_agent_present);
    let selection = read_json("selection.json", MAX_SELECTION_BYTES);
    Ok(IntakeStateView {
        adapter: adapter_view(&state, home.as_deref()),
        node_missing: node.is_none(),
        node_path: node.and_then(|n| n.to_str().map(str::to_string)),
        schedule: settings.schedule,
        running: *state.running.lock(),
        last_result: state.last_result.lock().clone(),
        status: read_json("status.json", MAX_STATUS_BYTES),
        selection: selection_view(&selection),
        options: options_view(&selection),
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
    let state = app.state::<IntakeState>();
    state.available()?;
    let cancel = state.try_begin(RunKind::ScheduleOff)?;
    let outcome = execute(
        &app,
        &IntakeAction::ScheduleOff,
        SCHEDULE_OFF_TIMEOUT,
        Some(cancel),
        false,
    )
    .await;
    state.end();
    if state.retry_after_run.swap(false, Ordering::SeqCst) {
        state.poke(true);
    }
    match outcome.code {
        None => Ok(outcome.result.unwrap_or(Value::Null)),
        Some(code) => Err(code),
    }
}

/// Runs `intake options` with typed values in the exclusive run slot; returns
/// the CLI's JSON result (`{ok, options}`) or its code.
#[tauri::command]
pub async fn intake_set_options(
    app: AppHandle,
    calendar_responses: Vec<CalendarResponse>,
    sfdc_contacts: bool,
) -> Result<Value, String> {
    let state = app.state::<IntakeState>();
    state.available()?;
    let action = IntakeAction::Options {
        calendar_responses: calendar_responses.into_iter().collect(),
        sfdc_contacts,
    };
    let cancel = state.try_begin(RunKind::Options)?;
    let outcome = execute(&app, &action, SCHEDULE_OFF_TIMEOUT, Some(cancel), false).await;
    state.end();
    if state.retry_after_run.swap(false, Ordering::SeqCst) {
        state.poke(true);
    }
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

/// Clears `adapter_busy`; a slot that came due meanwhile is re-evaluated.
struct AdapterGuard<'a>(&'a IntakeState);

impl Drop for AdapterGuard<'_> {
    fn drop(&mut self) {
        self.0.adapter_busy.store(false, Ordering::SeqCst);
        if self.0.retry_after_run.swap(false, Ordering::SeqCst) {
            self.0.poke(true);
        }
    }
}

/// Recaptures and repairs the login PATH that intake and report runs use.
#[tauri::command]
pub async fn intake_refresh_path(state: State<'_, IntakeState>) -> Result<PathRefresh, String> {
    state.available()?;
    let home = home_dir().ok_or("unavailable")?;
    refresh_login_path(&state, &home)
        .await
        .map_err(str::to_string)
}

/// The served bundle differs from the installed one (or none is installed).
pub(crate) fn adapter_changed(current: Option<&[u8]>, served: &[u8]) -> bool {
    current != Some(served)
}

/// Result of [Update]: the new adapter view and whether the file changed.
#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
pub struct AdapterUpdate {
    adapter: AdapterView,
    changed: bool,
}

/// Fetches the served adapter and installs it when it differs, keeping one
/// `server.mjs.bak`. Returns whether anything changed.
pub(crate) async fn update_adapter(state: &IntakeState, home: &Path) -> Result<bool, String> {
    let _busy = state.begin_adapter_update()?;
    let served = fetch_adapter().await.map_err(|e| {
        log::info!("adapter fetch failed: {e}");
        e.to_string()
    })?;
    let changed = adapter_changed(local_adapter(home).as_deref(), &served);
    if changed {
        install_adapter(&home.join(".ttobak"), &served).map_err(|e| {
            log::warn!("adapter install failed: {:?}", e.kind());
            "save-failed".to_string()
        })?;
    }
    *state.update_available.lock() = Some(false);
    Ok(changed)
}

/// Downloads and installs the served adapter; always reports the outcome.
#[tauri::command]
pub async fn intake_update_adapter(state: State<'_, IntakeState>) -> Result<AdapterUpdate, String> {
    state.available()?;
    let home = home_dir().ok_or("unavailable")?;
    let changed = update_adapter(&state, &home).await?;
    Ok(AdapterUpdate {
        adapter: adapter_view(&state, Some(&home)),
        changed,
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn quit_wait_ends_when_the_group_is_gone() {
        use std::os::unix::process::CommandExt;
        // Above any real pid_max: no such group, no wait.
        let started = std::time::Instant::now();
        wait_group_gone(2_147_483_646, Duration::from_secs(2));
        assert!(started.elapsed() < Duration::from_millis(200));
        // A group that exits on SIGTERM (and is reaped) ends the wait early.
        let mut child = std::process::Command::new("/bin/sleep")
            .arg("30")
            .process_group(0)
            .spawn()
            .expect("sleep");
        let pgid = child.id() as i32;
        let reaper = std::thread::spawn(move || child.wait());
        assert!(group_exists(pgid));
        kill_group(pgid, libc::SIGTERM);
        let started = std::time::Instant::now();
        wait_group_gone(pgid, Duration::from_secs(5));
        assert!(started.elapsed() < Duration::from_secs(2));
        assert!(!group_exists(pgid));
        let _ = reaper.join();
    }
    use serde_json::json;

    #[test]
    fn group_kill_follows_cancel_and_timeout_only() {
        assert!(should_group_kill(Some("TIMEOUT")));
        assert!(should_group_kill(Some("CANCELLED")));
        assert!(!should_group_kill(None));
        assert!(!should_group_kill(Some("WAIT_FAILED")));
    }

    #[test]
    fn shutdown_refusal_does_not_record_a_slot_attempt() {
        assert!(matches!(
            slot_outcome(false, Some("SHUTTING_DOWN"), None),
            RecordDecision::Skip
        ));
    }

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

    #[test]
    fn options_view_reads_names_with_defaults() {
        use CalendarResponse::*;
        let legacy = options_view(&json!({"calendar": {"server": "x"}, "sfdc": {"server": "y"}}));
        assert_eq!(
            legacy.calendar_responses,
            vec![Accept, Organizer, Tentative]
        );
        assert!(legacy.sfdc_contacts);
        assert!(legacy.defaults);
        assert_eq!(options_view(&json!(null)), legacy);
        let set = options_view(&json!({
            "calendar": {"pathDirs": ["/secret"]},
            "options": {"calendarResponses": ["none", "bogus", 7, "accept"], "sfdcContacts": false}
        }));
        assert_eq!(set.calendar_responses, vec![Accept, None]);
        assert!(!set.sfdc_contacts);
        assert!(!set.defaults);
        assert!(options_view(&json!({"options": {"calendarResponses": []}}))
            .calendar_responses
            .is_empty());
        assert!(options_view(&json!({"options": {"sfdcContacts": "no"}})).sfdc_contacts);
        // The per-connector fields are not read.
        let old =
            options_view(&json!({"calendar": {"responses": []}, "sfdc": {"contacts": false}}));
        assert_eq!(old, legacy);
        let text = serde_json::to_string(&set).expect("json");
        assert_eq!(
            text,
            r#"{"calendarResponses":["accept","none"],"sfdcContacts":false,"defaults":false}"#
        );
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
    fn slot_outcome_needs_one_ok_connector_for_partial_failure() {
        use RecordDecision::{Record, Skip};
        let rec = |c: &str| Record(Some(c.to_string()));
        assert_eq!(slot_outcome(true, None, None), Record(None));
        assert_eq!(slot_outcome(false, None, None), rec("EXIT_FAILED"));
        assert_eq!(slot_outcome(false, Some("RUN_IN_PROGRESS"), None), Skip);
        assert_eq!(slot_outcome(false, Some("ADAPTER_UPDATING"), None), Skip);
        let some_ok = json!({"ok": false, "code": "PARTIAL_FAILURE", "results": {
            "calendar": {"result": "ok", "created": 1},
            "sfdc": {"result": "TTOBAK_UNAVAILABLE"}}});
        assert_eq!(
            slot_outcome(true, Some("PARTIAL_FAILURE"), Some(&some_ok)),
            rec("PARTIAL_FAILURE")
        );
        let none_ok = json!({"ok": false, "code": "PARTIAL_FAILURE", "results": {
            "calendar": {"result": "TTOBAK_UNAVAILABLE"},
            "sfdc": {"result": "TTOBAK_UNAVAILABLE"}}});
        assert_eq!(
            slot_outcome(true, Some("PARTIAL_FAILURE"), Some(&none_ok)),
            rec("ALL_FAILED")
        );
        assert_eq!(
            slot_outcome(true, Some("PARTIAL_FAILURE"), None),
            rec("ALL_FAILED")
        );
        assert_eq!(
            slot_outcome(false, Some("AUTH_EXPIRED_MIDWAY"), None),
            rec("AUTH_EXPIRED_MIDWAY")
        );
    }

    #[test]
    fn all_failed_run_does_not_satisfy_the_slot() {
        let mut state = intake_plan::SlotState::default();
        intake_plan::record_result(&mut state, 10, Some("ALL_FAILED"));
        assert_eq!(state.last_satisfied_slot, None);
        assert_eq!(state.attempts_on_last_slot, 1);
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
    fn adapter_update_reports_unchanged_bytes() {
        assert!(!adapter_changed(Some(b"same"), b"same"));
        assert!(adapter_changed(Some(b"old"), b"new"));
        assert!(adapter_changed(None, b"new"));
    }

    #[test]
    fn repair_path_appends_missing_existing_dirs() {
        let home = Path::new("/Users/u");
        let exists = |p: &Path| p.ends_with(".toolbox/bin") || p.ends_with(".local/bin");
        assert_eq!(
            repair_path("/usr/bin:/bin", home, exists),
            "/usr/bin:/bin:/Users/u/.toolbox/bin:/Users/u/.local/bin"
        );
    }

    #[test]
    fn repair_path_keeps_present_dirs_once() {
        let home = Path::new("/Users/u");
        let path = "/Users/u/.toolbox/bin:/usr/bin:/Users/u/.aim/mcp-servers";
        assert_eq!(
            repair_path(path, home, |_| true),
            format!("{path}:/Users/u/.local/bin")
        );
        assert_eq!(repair_path(path, home, |_| false), path);
    }

    #[test]
    fn repair_path_result_still_parses() {
        let repaired = repair_path("/usr/bin", Path::new("/Users/u"), |_| true);
        assert_eq!(
            parse_login_path(repaired.as_bytes()).as_deref(),
            Some(repaired.as_str())
        );
        // A HOME that would make an invalid entry adds nothing.
        assert_eq!(
            repair_path("/usr/bin", Path::new("rel"), |_| true),
            "/usr/bin"
        );
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
        assert_eq!(
            std::fs::read(dir.join("server.mjs.bak")).expect("bak"),
            b"v1"
        );
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

    #[test]
    fn adapter_install_never_removes_the_current_file() {
        let dir = std::env::temp_dir().join(format!(
            "ttobak-adapter-swap-{}-{}",
            std::process::id(),
            std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .expect("time")
                .as_nanos()
        ));
        install_adapter(&dir, b"old").expect("first");
        let target = dir.join("server.mjs");
        // The old file stays at its path while the backup is made: the bak is
        // a second link to the same inode, not a moved file.
        let before = std::fs::metadata(&target).expect("meta");
        install_adapter(&dir, b"new").expect("second");
        let bak = std::fs::metadata(dir.join("server.mjs.bak")).expect("bak");
        {
            use std::os::unix::fs::MetadataExt;
            assert_eq!(bak.ino(), before.ino(), "bak links the replaced file");
        }
        assert_eq!(std::fs::read(&target).expect("read"), b"new");
        // A stale tmp or bak is replaced, never followed.
        std::fs::write(dir.join("server.mjs.tmp"), b"stale").expect("tmp");
        install_adapter(&dir, b"newer").expect("third");
        assert_eq!(std::fs::read(&target).expect("read"), b"newer");
        assert_eq!(
            std::fs::read(dir.join("server.mjs.bak")).expect("bak"),
            b"new"
        );
        assert!(!dir.join("server.mjs.tmp").exists());
        let _ = std::fs::remove_dir_all(&dir);
    }

    #[test]
    fn run_and_adapter_update_exclude_each_other() {
        let state = IntakeState::new(None);
        {
            let _guard = state.begin_adapter_update().expect("update");
            assert_eq!(
                state.try_begin(RunKind::Run).err().as_deref(),
                Some("ADAPTER_UPDATING")
            );
            assert_eq!(state.running.lock().clone(), None, "nothing claimed");
            assert_eq!(state.begin_adapter_update().err().as_deref(), Some("busy"));
        }
        let _rx = state.try_begin(RunKind::ScheduleOff).expect("free again");
        assert_eq!(
            state.begin_adapter_update().err().as_deref(),
            Some("RUN_IN_PROGRESS")
        );
        assert_eq!(
            state.try_begin(RunKind::Run).err().as_deref(),
            Some("RUN_IN_PROGRESS")
        );
        state.end();
        assert!(state.begin_adapter_update().is_ok());
    }

    #[test]
    fn schedule_off_keeps_the_last_runs_progress() {
        let state = IntakeState::new(None);
        let _rx = state.try_begin(RunKind::Run).expect("run");
        state.progress.lock().push("진행".into());
        let gen = state.run_gen.load(Ordering::SeqCst);
        state.end();
        let _rx = state.try_begin(RunKind::ScheduleOff).expect("off");
        assert_eq!(state.progress.lock().lines.len(), 1);
        assert_ne!(
            state.run_gen.load(Ordering::SeqCst),
            gen,
            "older readers are fenced"
        );
        state.end();
        let _rx = state.try_begin(RunKind::Setup).expect("setup");
        assert!(state.progress.lock().lines.is_empty());
    }

    #[test]
    fn launch_agent_presence_ignores_the_app_schedule() {
        let home = std::env::temp_dir().join(format!(
            "ttobak-la-{}-{}",
            std::process::id(),
            std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .expect("time")
                .as_nanos()
        ));
        assert!(!launch_agent_present(&home));
        let plist = home.join(LAUNCH_AGENT_PLIST);
        std::fs::create_dir_all(plist.parent().expect("parent")).expect("dir");
        std::fs::write(&plist, b"<plist/>").expect("write");
        assert!(launch_agent_present(&home));
        let _ = std::fs::remove_dir_all(&home);
    }
}
