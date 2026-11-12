//! SA report runner: runs `node ~/.ttobak/server.mjs report run ...` from the
//! app's [Make report] button (ADR-023), reusing the intake runner's spawn,
//! kill and progress machinery (`intake_runner::run_child`).
//!
//! The window loads the remote SPA, so every command here is callable from a
//! remote origin. Inputs are typed and validated before anything spawns:
//! the period is an enum, custom dates are strict calendar dates of at most
//! 400 days (the server rule), highlights are bounded plain text written to
//! a 0600 file in a fresh 0700 directory that is removed after the run, and
//! the AWS profile must be one of the local profile names. The WebView gets
//! only the CLI's final JSON line and its stderr progress lines.
//!
//! Report runs use their own run slot: they may overlap an intake run (the
//! CLI holds separate locks). Settings live under `report` in the intake
//! settings file so one mutex owns that file.

use std::ffi::OsString;
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicBool, AtomicI32, AtomicU64, Ordering};
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use parking_lot::Mutex;
use serde::{Deserialize, Deserializer, Serialize};
use serde_json::{json, Value};
use tauri::{AppHandle, Emitter, Manager, State};
use tokio::sync::oneshot;

use crate::intake_runner::{self, IntakeState, Outcome, ProcTrack, ProgressLog};

/// Rust's outer bound; the CLI's own timeout is 45 minutes.
const REPORT_TIMEOUT: Duration = Duration::from_secs(50 * 60);
/// SIGTERM to SIGKILL: the CLI removes its run and session directories on
/// SIGTERM, so it gets longer than an intake run.
const REPORT_KILL_GRACE: Duration = Duration::from_secs(10);

const MAX_HIGHLIGHT_ITEMS: usize = 20;
const MAX_HIGHLIGHT_CHARS: usize = 200;
/// The CLI's `--highlights-file` size cap.
const MAX_HIGHLIGHTS_BYTES: usize = 64 * 1024;
/// Inclusive custom-period length (server and CLI rule).
const MAX_CUSTOM_DAYS: i64 = 400;
const MIN_YEAR: i64 = 2000;
const MAX_YEAR: i64 = 2100;
/// The CLI's accepted environment value shape (`ENV_VALUE`).
const MAX_ENV_VALUE_BYTES: usize = 512;

const PROGRESS_EVENT: &str = "report://progress";
const FINISHED_EVENT: &str = "report://finished";

// ---------------------------------------------------------------- pure parts

#[derive(Debug, Clone, Copy, PartialEq, Eq, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum ReportPeriod {
    Week,
    Quarter,
    Year,
    Custom,
}

impl ReportPeriod {
    fn as_str(self) -> &'static str {
        match self {
            Self::Week => "week",
            Self::Quarter => "quarter",
            Self::Year => "year",
            Self::Custom => "custom",
        }
    }
}

/// A validated period argument set.
#[derive(Debug, Clone, PartialEq, Eq)]
struct PeriodArgs {
    period: ReportPeriod,
    /// Both set exactly for `custom`.
    range: Option<(String, String)>,
}

/// Days since 1970-01-01 of a strict `YYYY-MM-DD` calendar date in
/// 2000..=2100; `None` otherwise.
fn parse_date(s: &str) -> Option<i64> {
    let b = s.as_bytes();
    if b.len() != 10 || b[4] != b'-' || b[7] != b'-' {
        return None;
    }
    let num = |r: std::ops::Range<usize>| -> Option<i64> {
        let part = &b[r];
        if !part.iter().all(u8::is_ascii_digit) {
            return None;
        }
        part.iter()
            .try_fold(0i64, |acc, d| Some(acc * 10 + i64::from(d - b'0')))
    };
    let (y, m, d) = (num(0..4)?, num(5..7)?, num(8..10)?);
    if !(MIN_YEAR..=MAX_YEAR).contains(&y) || !(1..=12).contains(&m) {
        return None;
    }
    let leap = (y % 4 == 0 && y % 100 != 0) || y % 400 == 0;
    let days_in_month = match m {
        2 if leap => 29,
        2 => 28,
        4 | 6 | 9 | 11 => 30,
        _ => 31,
    };
    if !(1..=days_in_month).contains(&d) {
        return None;
    }
    // Howard Hinnant's days_from_civil.
    let y = if m <= 2 { y - 1 } else { y };
    let era = y.div_euclid(400);
    let yoe = y - era * 400;
    let mp = (m + 9) % 12;
    let doy = (153 * mp + 2) / 5 + d - 1;
    let doe = yoe * 365 + yoe / 4 - yoe / 100 + doy;
    Some(era * 146_097 + doe - 719_468)
}

/// Dates only for `custom`, where both are required, real, ordered and span
/// at most `MAX_CUSTOM_DAYS` days inclusive.
fn validate_period(
    period: ReportPeriod,
    start: Option<String>,
    end: Option<String>,
) -> Result<PeriodArgs, &'static str> {
    const INVALID: &str = "INVALID_ARGUMENT";
    if period != ReportPeriod::Custom {
        if start.is_some() || end.is_some() {
            return Err(INVALID);
        }
        return Ok(PeriodArgs {
            period,
            range: None,
        });
    }
    let (Some(start), Some(end)) = (start, end) else {
        return Err(INVALID);
    };
    let (Some(s), Some(e)) = (parse_date(&start), parse_date(&end)) else {
        return Err(INVALID);
    };
    if e < s || e - s >= MAX_CUSTOM_DAYS {
        return Err(INVALID);
    }
    Ok(PeriodArgs {
        period,
        range: Some((start, end)),
    })
}

/// The UTC calendar year of a day count since 1970-01-01 (Howard Hinnant's
/// civil_from_days, year only).
fn year_of_days(days: i64) -> i64 {
    let z = days + 719_468;
    let era = z.div_euclid(146_097);
    let doe = z - era * 146_097;
    let yoe = (doe - doe / 1460 + doe / 36_524 - doe / 146_096) / 365;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let month = if mp < 10 { mp + 3 } else { mp - 9 };
    yoe + era * 400 + i64::from(month <= 2)
}

/// The current UTC year.
fn current_year() -> i64 {
    let days = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map_or(0, |d| (d.as_secs() / 86_400) as i64);
    year_of_days(days)
}

/// The server keeps a report until January 1 of (period end year + 3) and
/// refuses one already past that, so such a custom period is refused before
/// a 45-minute run. Presets are always current.
fn check_retention(args: &PeriodArgs, year: i64) -> Result<(), &'static str> {
    let Some((_, end)) = &args.range else {
        return Ok(());
    };
    let end_year = parse_date(end).map_or(0, year_of_days);
    if end_year + 3 <= year {
        Err("REPORT_PERIOD_EXPIRED")
    } else {
        Ok(())
    }
}

/// The four highlight lists of the [Make report] form.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Highlights {
    #[serde(default)]
    projects: Vec<String>,
    #[serde(default)]
    initiatives: Vec<String>,
    #[serde(default)]
    channels: Vec<String>,
    #[serde(default)]
    programs: Vec<String>,
}

impl Highlights {
    fn lists(&self) -> [&Vec<String>; 4] {
        [
            &self.projects,
            &self.initiatives,
            &self.channels,
            &self.programs,
        ]
    }

    fn is_empty(&self) -> bool {
        self.lists().iter().all(|l| l.is_empty())
    }
}

/// Plain text as the CLI accepts it, but stricter: no control character at
/// all, no tag, Markdown link or Slack link, at most `MAX_HIGHLIGHT_CHARS`
/// characters.
fn highlight_ok(item: &str) -> bool {
    let lower = item.to_lowercase();
    item.chars().count() <= MAX_HIGHLIGHT_CHARS
        && !item.chars().any(char::is_control)
        && !item.contains("](")
        && !lower.contains("slack.com")
        && !item
            .as_bytes()
            .windows(2)
            .any(|w| w[0] == b'<' && (w[1].is_ascii_alphabetic() || w[1] == b'/'))
}

/// Each list at most `MAX_HIGHLIGHT_ITEMS` valid items.
fn validate_highlights(h: &Highlights) -> Result<(), &'static str> {
    let ok = h.lists().iter().all(|list| {
        list.len() <= MAX_HIGHLIGHT_ITEMS && list.iter().all(|item| highlight_ok(item))
    });
    if ok {
        Ok(())
    } else {
        Err("INVALID_ARGUMENT")
    }
}

/// `report run` argv after the node binary.
fn report_argv(args: &PeriodArgs, server: &Path, highlights: Option<&Path>) -> Vec<OsString> {
    let mut v: Vec<OsString> = vec![
        server.into(),
        "report".into(),
        "run".into(),
        "--period".into(),
        args.period.as_str().into(),
    ];
    if let Some((start, end)) = &args.range {
        v.extend(["--start".into(), start.into(), "--end".into(), end.into()]);
    }
    if let Some(path) = highlights {
        v.extend(["--highlights-file".into(), path.into()]);
    }
    v.push("--noninteractive".into());
    v
}

/// `app_config_dir()/intake.json` → `report`.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ReportSettings {
    #[serde(default)]
    pub use_bedrock: bool,
    #[serde(default)]
    pub aws_profile: Option<String>,
}

/// A malformed `report` value becomes the default instead of failing the
/// whole settings file (which would drop the intake schedule).
pub(crate) fn lenient_settings<'de, D: Deserializer<'de>>(
    d: D,
) -> Result<ReportSettings, D::Error> {
    let v = Value::deserialize(d)?;
    Ok(serde_json::from_value(v).unwrap_or_default())
}

/// A chosen profile must be one of the local profile names.
fn validate_profile(profile: Option<&str>, profiles: &[String]) -> Result<(), &'static str> {
    match profile {
        None => Ok(()),
        Some(p) if profiles.iter().any(|n| n == p) => Ok(()),
        Some(_) => Err("profile-missing"),
    }
}

/// Bedrock needs a chosen AWS profile: without one the CLI's Bedrock request
/// can hang until it times out (2026-11-11).
fn validate_bedrock(use_bedrock: bool, profile: Option<&str>) -> Result<(), &'static str> {
    if use_bedrock && profile.is_none() {
        return Err("profile-required");
    }
    Ok(())
}

/// A stored setting saved before the guard: Bedrock on without a profile.
fn settings_attention(s: &ReportSettings) -> bool {
    validate_bedrock(s.use_bedrock, s.aws_profile.as_deref()).is_err()
}

/// The report variables of the child environment: none unless Bedrock is
/// enabled; then `TTOBAK_REPORT_USE_BEDROCK=1` and the chosen, still present
/// `TTOBAK_REPORT_AWS_PROFILE` (Bedrock without a profile is refused).
fn report_env(
    s: &ReportSettings,
    profiles: &[String],
) -> Result<Vec<(&'static str, String)>, &'static str> {
    if !s.use_bedrock {
        return Ok(Vec::new());
    }
    validate_bedrock(s.use_bedrock, s.aws_profile.as_deref())?;
    validate_profile(s.aws_profile.as_deref(), profiles)?;
    let mut env = vec![("TTOBAK_REPORT_USE_BEDROCK", "1".to_string())];
    if let Some(p) = &s.aws_profile {
        env.push(("TTOBAK_REPORT_AWS_PROFILE", p.clone()));
    }
    Ok(env)
}

/// A USER/LOGNAME value the CLI accepts: 1-512 printable ASCII bytes.
fn env_value_ok(v: &str) -> bool {
    !v.is_empty() && v.len() <= MAX_ENV_VALUE_BYTES && v.bytes().all(|b| (0x20..=0x7e).contains(&b))
}

/// `USER` and `LOGNAME` (a Keychain login of the claude CLI needs them) from
/// the app's environment, when well-formed.
fn user_env(user: Option<&str>, logname: Option<&str>) -> Vec<(&'static str, String)> {
    [("USER", user), ("LOGNAME", logname)]
        .into_iter()
        .filter_map(|(k, v)| v.filter(|v| env_value_ok(v)).map(|v| (k, v.to_string())))
        .collect()
}

/// The app's `TMPDIR` for the report child, when it is an absolute path.
fn tmpdir_env(value: Option<std::ffi::OsString>) -> Option<std::ffi::OsString> {
    value.filter(|v| Path::new(v).is_absolute())
}

/// Usage text only an adapter with the `report run` command contains
/// (`mcp-server/src/report/run.ts`). An intake-only adapter exits with an
/// unrelated startup error, so the app checks this before spawning.
const REPORT_MARKER: &[u8] = b"report run --period";

fn supports_report(bytes: &[u8]) -> bool {
    bytes
        .windows(REPORT_MARKER.len())
        .any(|w| w == REPORT_MARKER)
}

/// Rust's forced ends in the CLI's report vocabulary.
fn report_code(code: Option<String>) -> Option<String> {
    code.map(|c| match c.as_str() {
        "TIMEOUT" => "REPORT_TIMEOUT".to_string(),
        "CANCELLED" => "REPORT_CANCELLED".to_string(),
        _ => c,
    })
}

// ---------------------------------------------------------------- highlights file

static TEMP_SEQ: AtomicU64 = AtomicU64::new(0);

/// A 0600 highlights file in a fresh 0700 directory; dropping it removes the
/// directory.
struct HighlightsFile {
    dir: PathBuf,
    file: PathBuf,
}

impl Drop for HighlightsFile {
    fn drop(&mut self) {
        if let Err(e) = std::fs::remove_dir_all(&self.dir) {
            log::warn!("report highlights dir not removed: {:?}", e.kind());
        }
    }
}

fn write_highlights(base: &Path, h: &Highlights) -> std::io::Result<HighlightsFile> {
    use std::os::unix::fs::DirBuilderExt;
    let json = serde_json::to_vec(h).map_err(std::io::Error::other)?;
    if json.len() > MAX_HIGHLIGHTS_BYTES {
        return Err(std::io::Error::other("highlights too large"));
    }
    let nanos = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map_or(0, |d| d.as_nanos());
    let mut last = None;
    for _ in 0..8 {
        let seq = TEMP_SEQ.fetch_add(1, Ordering::SeqCst);
        let dir = base.join(format!("{TEMP_PREFIX}{}-{nanos}-{seq}", std::process::id()));
        // Not recursive: an existing directory (or link) is never reused.
        match std::fs::DirBuilder::new().mode(0o700).create(&dir) {
            Ok(()) => {
                let guard = HighlightsFile {
                    file: dir.join("highlights.json"),
                    dir,
                };
                intake_runner::write_new_private(&guard.file, &json)?;
                return Ok(guard);
            }
            Err(e) if e.kind() == std::io::ErrorKind::AlreadyExists => last = Some(e),
            Err(e) => return Err(e),
        }
    }
    Err(last.unwrap_or_else(|| std::io::Error::other("no temp dir")))
}

// ---------------------------------------------------------------- state

pub struct ReportState {
    running: Mutex<bool>,
    cancel: Mutex<Option<oneshot::Sender<()>>>,
    pgid: AtomicI32,
    shutting_down: AtomicBool,
    progress: Mutex<ProgressLog>,
    /// Bumped per begun run; a reader of an older run never writes progress.
    run_gen: AtomicU64,
    last_result: Mutex<Option<Value>>,
}

impl ReportState {
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

    /// Claims the report run slot (independent of the intake slot).
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
    app.manage(ReportState::new());
    sweep_stale_highlights(&std::env::temp_dir());
}

/// The app's highlights directories: `ttobak-report-hl-<pid>-<nanos>-<seq>`.
/// Distinct from the CLI's own `ttobak-report-XXXXXX` run directories, which
/// the CLI sweeps itself under its run lock.
const TEMP_PREFIX: &str = "ttobak-report-hl-";
/// The earlier app format `ttobak-report-<pid>-<nanos>-<seq>`.
const LEGACY_TEMP_PREFIX: &str = "ttobak-report-";

/// The pid of a highlights directory name: exactly three all-digit parts
/// after the app's prefix (current or legacy); `None` for any other name.
fn highlights_dir_pid(name: &str) -> Option<i32> {
    let rest = name
        .strip_prefix(TEMP_PREFIX)
        .or_else(|| name.strip_prefix(LEGACY_TEMP_PREFIX))?;
    let parts: Vec<&str> = rest.split('-').collect();
    if parts.len() != 3
        || parts
            .iter()
            .any(|p| p.is_empty() || p.len() > 39 || !p.bytes().all(|b| b.is_ascii_digit()))
    {
        return None;
    }
    parts[0].parse::<i32>().ok().filter(|&p| p > 0)
}

/// Whether `pid` names a live process other than this one (EPERM is alive).
fn pid_alive(pid: i32) -> bool {
    if pid == std::process::id() as i32 {
        return false;
    }
    // SAFETY: signal 0 only checks that the process exists.
    if unsafe { libc::kill(pid, 0) } == 0 {
        return true;
    }
    std::io::Error::last_os_error().raw_os_error() == Some(libc::EPERM)
}

/// Removes highlight directories an earlier app process left behind (a quit
/// or crash mid-run). Only real directories owned by this user, named
/// in the app's exact format for a pid that is no longer alive, are removed;
/// links and everything else are skipped. Errors are ignored.
fn sweep_stale_highlights(base: &Path) {
    use std::os::unix::fs::MetadataExt;
    let Ok(entries) = std::fs::read_dir(base) else {
        return;
    };
    // SAFETY: geteuid has no preconditions.
    let me = unsafe { libc::geteuid() };
    for entry in entries.flatten() {
        let name = entry.file_name();
        let Some(pid) = name.to_str().and_then(highlights_dir_pid) else {
            continue;
        };
        let Ok(meta) = std::fs::symlink_metadata(entry.path()) else {
            continue;
        };
        if !meta.is_dir() || meta.uid() != me {
            continue;
        }
        if pid_alive(pid) {
            continue;
        }
        let _ = std::fs::remove_dir_all(entry.path());
    }
}

// ---------------------------------------------------------------- run

async fn run_report(
    app: &AppHandle,
    args: &PeriodArgs,
    highlights: Option<&Path>,
    env: &[(&'static str, String)],
    cancel: oneshot::Receiver<()>,
) -> Outcome {
    let intake = app.state::<IntakeState>();
    let report = app.state::<ReportState>();
    let launch = match intake_runner::node_launch(&intake).await {
        Ok(l) => l,
        Err(code) => return Outcome::failed(code),
    };
    if !intake_runner::local_adapter(&launch.home).is_some_and(|b| supports_report(&b)) {
        return Outcome::failed("ADAPTER_OUTDATED");
    }
    let mut cmd =
        intake_runner::node_command(&launch, report_argv(args, &launch.server, highlights));
    let user = std::env::var("USER").ok();
    let logname = std::env::var("LOGNAME").ok();
    for (k, v) in user_env(user.as_deref(), logname.as_deref())
        .iter()
        .chain(env)
    {
        cmd.env(k, v);
    }
    // The CLI's run directory and the highlights file share the app's temp
    // root, so both sides' stale-directory sweeps look in the same place.
    if let Some(tmp) = tmpdir_env(std::env::var_os("TMPDIR")) {
        cmd.env("TMPDIR", tmp);
    }
    let gen = report.run_gen.load(Ordering::SeqCst);
    let line_app = app.clone();
    let on_line = move |line: String| {
        let state = line_app.state::<ReportState>();
        if state.run_gen.load(Ordering::SeqCst) == gen {
            let _ = line_app.emit(PROGRESS_EVENT, json!({ "line": line }));
            state.progress.lock().push(line);
        }
    };
    intake_runner::run_child(
        cmd,
        report.track(),
        REPORT_TIMEOUT,
        REPORT_KILL_GRACE,
        Some(cancel),
        on_line,
    )
    .await
}

// ---------------------------------------------------------------- commands

#[derive(Debug, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ReportSettingsView {
    use_bedrock: bool,
    aws_profile: Option<String>,
    /// Local AWS profile names only.
    profiles: Vec<String>,
}

#[derive(Debug, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ReportStateView {
    running: bool,
    last_result: Option<Value>,
    progress: Vec<String>,
    settings: ReportSettings,
    /// Bedrock is on without a profile; the dialog asks for a fix.
    settings_attention: bool,
}

/// Starts one background report run; finishes with `report://finished`.
#[tauri::command]
pub async fn report_generate(
    app: AppHandle,
    period: ReportPeriod,
    start: Option<String>,
    end: Option<String>,
    highlights: Option<Highlights>,
) -> Result<(), String> {
    let intake = app.state::<IntakeState>();
    intake.available()?;
    let args = validate_period(period, start, end).map_err(str::to_string)?;
    check_retention(&args, current_year()).map_err(str::to_string)?;
    let highlights = highlights.unwrap_or_default();
    validate_highlights(&highlights).map_err(str::to_string)?;
    let env = report_env(&intake.snapshot().report, &crate::aws_profiles::list())
        .map_err(str::to_string)?;
    let file = if highlights.is_empty() {
        None
    } else {
        Some(
            write_highlights(&std::env::temp_dir(), &highlights).map_err(|e| {
                log::warn!("report highlights not written: {:?}", e.kind());
                "save-failed".to_string()
            })?,
        )
    };
    let report = app.state::<ReportState>();
    let cancel = report.try_begin()?;
    let app = app.clone();
    tauri::async_runtime::spawn(async move {
        let path = file.as_ref().map(|f| f.file.clone());
        let outcome = run_report(&app, &args, path.as_deref(), &env, cancel).await;
        drop(file);
        let payload = json!({
            "exitCode": outcome.exit_code,
            "result": outcome.result,
            "code": report_code(outcome.code),
        });
        let state = app.state::<ReportState>();
        *state.last_result.lock() = Some(payload.clone());
        state.end();
        let _ = app.emit(FINISHED_EVENT, payload);
    });
    Ok(())
}

/// Cancels the current report run; `false` when nothing was running.
#[tauri::command]
pub async fn report_cancel(state: State<'_, ReportState>) -> Result<bool, String> {
    Ok(state
        .cancel
        .lock()
        .take()
        .is_some_and(|tx| tx.send(()).is_ok()))
}

#[tauri::command]
pub async fn report_get_state(
    intake: State<'_, IntakeState>,
    report: State<'_, ReportState>,
) -> Result<ReportStateView, String> {
    intake.available()?;
    let settings = intake.snapshot().report;
    Ok(ReportStateView {
        running: *report.running.lock(),
        last_result: report.last_result.lock().clone(),
        progress: report.progress.lock().to_vec(),
        settings_attention: settings_attention(&settings),
        settings,
    })
}

#[tauri::command]
pub async fn report_settings_get(
    intake: State<'_, IntakeState>,
) -> Result<ReportSettingsView, String> {
    intake.available()?;
    let s = intake.snapshot().report;
    Ok(ReportSettingsView {
        use_bedrock: s.use_bedrock,
        aws_profile: s.aws_profile,
        profiles: crate::aws_profiles::list(),
    })
}

#[tauri::command]
pub async fn report_settings_set(
    intake: State<'_, IntakeState>,
    use_bedrock: bool,
    aws_profile: Option<String>,
) -> Result<ReportSettingsView, String> {
    intake.available()?;
    validate_bedrock(use_bedrock, aws_profile.as_deref()).map_err(str::to_string)?;
    let profiles = crate::aws_profiles::list();
    validate_profile(aws_profile.as_deref(), &profiles).map_err(str::to_string)?;
    intake.update_settings(|s| {
        s.report = ReportSettings {
            use_bedrock,
            aws_profile: aws_profile.clone(),
        }
    })?;
    Ok(ReportSettingsView {
        use_bedrock,
        aws_profile,
        profiles,
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    fn s(v: &str) -> Option<String> {
        Some(v.to_string())
    }

    #[test]
    fn bedrock_without_profile_is_refused() {
        assert_eq!(validate_bedrock(true, None), Err("profile-required"));
        assert_eq!(validate_bedrock(false, None), Ok(()));
        assert_eq!(validate_bedrock(true, Some("cc-bedrock")), Ok(()));
        let stale = ReportSettings {
            use_bedrock: true,
            aws_profile: None,
        };
        assert!(settings_attention(&stale));
        assert!(!settings_attention(&ReportSettings::default()));
    }

    fn argv_strings(v: Vec<OsString>) -> Vec<String> {
        v.into_iter()
            .map(|a| a.into_string().expect("utf8"))
            .collect()
    }

    #[test]
    fn sweep_removes_only_stale_owned_report_dirs() {
        let base = std::env::temp_dir().join(format!("ttobak-sweep-test-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&base);
        std::fs::create_dir_all(&base).expect("base");
        // A dead pid (above any real pid_max), in the current and legacy formats.
        let stale = base.join("ttobak-report-hl-2147483646-1-0");
        let legacy = base.join("ttobak-report-2147483646-1-0");
        // A live pid (the test's parent) is kept.
        // SAFETY: getppid has no preconditions.
        let live = base.join(format!("ttobak-report-hl-{}-1-0", unsafe {
            libc::getppid()
        }));
        // The CLI's own run directory (mkdtemp, six characters; even all digits) and other names.
        let cli_run = base.join("ttobak-report-123456");
        let cli_run_alpha = base.join("ttobak-report-Ab12Cd");
        let other = base.join("other-dir");
        let bad = base.join("ttobak-report-notapid-1-0");
        let extra = base.join("ttobak-report-hl-2147483646-1-0-9");
        let suffix = base.join("ttobak-report-hl-2147483646-1-x");
        for d in [
            &stale,
            &legacy,
            &live,
            &cli_run,
            &cli_run_alpha,
            &other,
            &bad,
            &extra,
            &suffix,
        ] {
            std::fs::create_dir_all(d.join("sub")).expect("dir");
        }
        let file = base.join("ttobak-report-hl-2147483646-2-0");
        std::fs::write(&file, b"x").expect("file");
        // A link with a matching name is skipped, and its target is untouched.
        let target = base.join("link-target");
        std::fs::create_dir_all(target.join("sub")).expect("target");
        let link = base.join("ttobak-report-hl-2147483646-3-0");
        std::os::unix::fs::symlink(&target, &link).expect("link");
        sweep_stale_highlights(&base);
        assert!(!stale.exists());
        assert!(!legacy.exists());
        for kept in [
            &live,
            &cli_run,
            &cli_run_alpha,
            &other,
            &bad,
            &extra,
            &suffix,
            &file,
        ] {
            assert!(kept.exists(), "{kept:?}");
        }
        assert!(std::fs::symlink_metadata(&link).is_ok(), "the link stays");
        assert!(target.join("sub").exists());
        let _ = std::fs::remove_dir_all(&base);
    }

    #[test]
    fn highlights_dir_names_are_exact() {
        assert_eq!(highlights_dir_pid("ttobak-report-hl-42-1-0"), Some(42));
        assert_eq!(highlights_dir_pid("ttobak-report-42-1-0"), Some(42));
        for bad in [
            "ttobak-report-123456",
            "ttobak-report-hl-0-1-0",
            "ttobak-report-hl--1-0",
            "ttobak-report-hl-42-1",
            "ttobak-report-hl-42-1-0-0",
            "ttobak-report-hl-+42-1-0",
            "ttobak-report-hl-42-1-0a",
            "other",
        ] {
            assert_eq!(highlights_dir_pid(bad), None, "{bad}");
        }
        let h = Highlights {
            projects: vec!["p".into()],
            ..Default::default()
        };
        let f = write_highlights(&std::env::temp_dir(), &h).expect("write");
        let name = f.dir.file_name().and_then(|n| n.to_str()).expect("name");
        assert_eq!(highlights_dir_pid(name), Some(std::process::id() as i32));
    }

    #[test]
    fn expired_custom_periods_are_refused() {
        assert_eq!(year_of_days(0), 1970);
        assert_eq!(parse_date("2026-11-11").map(year_of_days), Some(2026));
        assert_eq!(parse_date("2024-12-31").map(year_of_days), Some(2024));
        assert_eq!(parse_date("2025-01-01").map(year_of_days), Some(2025));
        assert_eq!(parse_date("2000-02-29").map(year_of_days), Some(2000));
        let custom =
            |a: &str, b: &str| validate_period(ReportPeriod::Custom, s(a), s(b)).expect("custom");
        assert_eq!(
            check_retention(&custom("2023-01-01", "2023-12-31"), 2026),
            Err("REPORT_PERIOD_EXPIRED")
        );
        assert_eq!(
            check_retention(&custom("2024-01-01", "2024-03-31"), 2026),
            Ok(())
        );
        assert_eq!(
            check_retention(&custom("2023-12-01", "2024-01-31"), 2026),
            Ok(())
        );
        let week = validate_period(ReportPeriod::Week, None, None).expect("week");
        assert_eq!(check_retention(&week, 2026), Ok(()));
    }

    #[test]
    fn tmpdir_is_passed_only_when_absolute() {
        assert_eq!(
            tmpdir_env(Some("/var/folders/x/T/".into())),
            Some("/var/folders/x/T/".into())
        );
        assert_eq!(tmpdir_env(Some("relative".into())), None);
        assert_eq!(tmpdir_env(None), None);
    }

    #[test]
    fn report_argv_maps_period_and_dates() {
        let server = Path::new("/h/.ttobak/server.mjs");
        let week = validate_period(ReportPeriod::Week, None, None).expect("week");
        assert_eq!(
            argv_strings(report_argv(&week, server, None)),
            [
                "/h/.ttobak/server.mjs",
                "report",
                "run",
                "--period",
                "week",
                "--noninteractive"
            ]
        );
        for (p, name) in [
            (ReportPeriod::Quarter, "quarter"),
            (ReportPeriod::Year, "year"),
        ] {
            let a = validate_period(p, None, None).expect("preset");
            assert_eq!(argv_strings(report_argv(&a, server, None))[4], name);
        }
        let custom = validate_period(ReportPeriod::Custom, s("2026-01-01"), s("2026-03-31"))
            .expect("custom");
        assert_eq!(
            argv_strings(report_argv(
                &custom,
                server,
                Some(Path::new("/t/d/highlights.json"))
            )),
            [
                "/h/.ttobak/server.mjs",
                "report",
                "run",
                "--period",
                "custom",
                "--start",
                "2026-01-01",
                "--end",
                "2026-03-31",
                "--highlights-file",
                "/t/d/highlights.json",
                "--noninteractive"
            ]
        );
        // One day and exactly 400 days inclusive are accepted; 401 is not.
        assert!(validate_period(ReportPeriod::Custom, s("2024-02-29"), s("2024-02-29")).is_ok());
        assert!(validate_period(ReportPeriod::Custom, s("2025-01-01"), s("2026-02-04")).is_ok());
        assert!(validate_period(ReportPeriod::Custom, s("2025-01-01"), s("2026-02-05")).is_err());

        for (start, end) in [
            (s("2026-03-31"), s("2026-01-01")), // end before start
            (s("2026-02-30"), s("2026-03-01")), // not a calendar date
            (s("2025-02-29"), s("2025-03-01")), // not a leap year
            (s("2026-1-01"), s("2026-01-02")),
            (s("2026-01-01 "), s("2026-01-02")),
            (s("２０２６-01-01"), s("2026-01-02")),
            (s("+026-01-01"), s("2026-01-02")),
            (s("2026-13-01"), s("2026-12-02")),
            (s("1999-12-31"), s("2000-01-01")),
            (s("2026-01-01"), s("2101-01-01")),
            (s("2026-01-01"), s("--period")),
            (s("2026-01-01"), None),
            (None, s("2026-01-01")),
            (None, None),
        ] {
            assert_eq!(
                validate_period(ReportPeriod::Custom, start.clone(), end.clone()),
                Err("INVALID_ARGUMENT"),
                "{start:?} {end:?}"
            );
        }
        // Dates only with custom.
        assert!(validate_period(ReportPeriod::Week, s("2026-01-01"), None).is_err());
        assert!(validate_period(ReportPeriod::Year, None, s("2026-01-01")).is_err());
        // Unknown periods fail to deserialize.
        assert!(serde_json::from_str::<ReportPeriod>("\"month\"").is_err());
        assert_eq!(
            serde_json::from_str::<ReportPeriod>("\"custom\"").ok(),
            Some(ReportPeriod::Custom)
        );
    }

    #[test]
    fn dates_count_days_correctly() {
        assert_eq!(parse_date("1970-01-01"), None, "before 2000");
        assert_eq!(parse_date("2000-01-01"), Some(10_957));
        assert_eq!(parse_date("2026-11-11"), Some(20_768));
        assert_eq!(
            parse_date("2024-03-01")
                .zip(parse_date("2024-02-28"))
                .map(|(a, b)| a - b),
            Some(2)
        );
    }

    #[test]
    fn highlights_are_capped() {
        let ok = Highlights {
            projects: vec!["가".repeat(MAX_HIGHLIGHT_CHARS); MAX_HIGHLIGHT_ITEMS],
            initiatives: vec!["EKS 마이그레이션".into()],
            channels: vec![],
            programs: vec!["a < b".into()],
        };
        assert_eq!(validate_highlights(&ok), Ok(()));

        let mut too_many = ok.clone();
        too_many.channels = vec!["x".into(); MAX_HIGHLIGHT_ITEMS + 1];
        let mut too_long = ok.clone();
        too_long.programs = vec!["가".repeat(MAX_HIGHLIGHT_CHARS + 1)];
        for bad in [
            too_many,
            too_long,
            Highlights {
                projects: vec!["a\nb".into()],
                ..Default::default()
            },
            Highlights {
                projects: vec!["tab\there".into()],
                ..Default::default()
            },
            Highlights {
                channels: vec!["<script>".into()],
                ..Default::default()
            },
            Highlights {
                channels: vec!["[x](https://e)".into()],
                ..Default::default()
            },
            Highlights {
                channels: vec!["https://acme.SLACK.com/archives/C1".into()],
                ..Default::default()
            },
        ] {
            assert_eq!(
                validate_highlights(&bad),
                Err("INVALID_ARGUMENT"),
                "{bad:?}"
            );
        }
        assert!(
            serde_json::from_str::<Highlights>(r#"{"projects":[],"extra":[]}"#).is_err(),
            "unknown lists are refused"
        );
        assert_eq!(
            serde_json::from_str::<Highlights>(r#"{"channels":["c"]}"#).expect("partial"),
            Highlights {
                channels: vec!["c".into()],
                ..Default::default()
            }
        );

        // The largest valid input fits the CLI's file cap.
        let quote = "\"".repeat(MAX_HIGHLIGHT_CHARS);
        let wide = "😀".repeat(MAX_HIGHLIGHT_CHARS);
        for item in [quote, wide] {
            let max = Highlights {
                projects: vec![item.clone(); MAX_HIGHLIGHT_ITEMS],
                initiatives: vec![item.clone(); MAX_HIGHLIGHT_ITEMS],
                channels: vec![item.clone(); MAX_HIGHLIGHT_ITEMS],
                programs: vec![item; MAX_HIGHLIGHT_ITEMS],
            };
            assert_eq!(validate_highlights(&max), Ok(()));
            assert!(serde_json::to_vec(&max).expect("json").len() <= MAX_HIGHLIGHTS_BYTES);
        }
    }

    #[cfg(unix)]
    #[test]
    fn highlights_file_is_private_and_removed() {
        use std::os::unix::fs::PermissionsExt;
        let base = std::env::temp_dir();
        let h = Highlights {
            projects: vec!["프로젝트".into()],
            ..Default::default()
        };
        let f = write_highlights(&base, &h).expect("write");
        let dir = f.dir.clone();
        let mode = |p: &Path| std::fs::metadata(p).expect("meta").permissions().mode() & 0o777;
        assert_eq!(mode(&dir), 0o700);
        assert_eq!(mode(&f.file), 0o600);
        assert!(f.file.starts_with(&dir));
        let back: Highlights =
            serde_json::from_slice(&std::fs::read(&f.file).expect("read")).expect("json");
        assert_eq!(back, h);
        let other = write_highlights(&base, &h).expect("second");
        assert_ne!(other.dir, dir, "a fresh directory per run");
        drop(f);
        assert!(!dir.exists());
        drop(other);
    }

    #[test]
    fn bedrock_env_only_when_enabled() {
        let profiles = vec!["default".to_string(), "cc-bedrock".to_string()];
        let off = ReportSettings {
            use_bedrock: false,
            aws_profile: s("cc-bedrock"),
        };
        assert_eq!(report_env(&off, &profiles), Ok(vec![]));
        let on = ReportSettings {
            use_bedrock: true,
            aws_profile: s("cc-bedrock"),
        };
        assert_eq!(
            report_env(&on, &profiles),
            Ok(vec![
                ("TTOBAK_REPORT_USE_BEDROCK", "1".to_string()),
                ("TTOBAK_REPORT_AWS_PROFILE", "cc-bedrock".to_string()),
            ])
        );
        let on_default_chain = ReportSettings {
            use_bedrock: true,
            aws_profile: None,
        };
        // Bedrock without a chosen profile timed out on 2026-11-11: refused.
        assert_eq!(
            report_env(&on_default_chain, &profiles),
            Err("profile-required")
        );
        // A stored profile that has since disappeared refuses the run.
        let gone = ReportSettings {
            use_bedrock: true,
            aws_profile: s("removed"),
        };
        assert_eq!(report_env(&gone, &profiles), Err("profile-missing"));

        assert_eq!(
            user_env(Some("atom"), Some("atom")),
            vec![
                ("USER", "atom".to_string()),
                ("LOGNAME", "atom".to_string())
            ]
        );
        assert!(user_env(Some("a\nb"), Some("")).is_empty());
        assert!(user_env(None, None).is_empty());
    }

    #[test]
    fn aws_profile_must_exist() {
        let profiles = crate::aws_profiles::profile_names("[default]\n[cc-bedrock]\n", "");
        assert_eq!(validate_profile(None, &profiles), Ok(()));
        assert_eq!(validate_profile(Some("cc-bedrock"), &profiles), Ok(()));
        for bad in ["", "other", "cc-bedrock ", "default\n", "../x"] {
            assert_eq!(
                validate_profile(Some(bad), &profiles),
                Err("profile-missing"),
                "{bad:?}"
            );
        }
    }

    #[test]
    fn report_settings_survive_in_the_intake_file() {
        use crate::intake_runner::IntakeSettings;
        let good = IntakeSettings::from_bytes(
            br#"{"schedule":{"kind":"daily","times":["09:30"]},"report":{"useBedrock":true,"awsProfile":"p"}}"#,
        );
        assert_eq!(
            good.report,
            ReportSettings {
                use_bedrock: true,
                aws_profile: s("p"),
            }
        );
        let bad = IntakeSettings::from_bytes(
            br#"{"schedule":{"kind":"daily","times":["09:30"]},"report":{"useBedrock":"yes"}}"#,
        );
        assert_eq!(bad.report, ReportSettings::default());
        assert_ne!(bad, IntakeSettings::default(), "the schedule is kept");
    }

    #[test]
    fn adapter_without_report_command_is_outdated() {
        // An intake-only adapter (before the report command) has no usage line.
        assert!(!supports_report(b"intake setup ... --transport stdio|http"));
        assert!(!supports_report(b""));
        assert!(supports_report(
            b"usage: ttobak-mcp report run --period week|quarter|year|custom"
        ));
        // The adapter bundle this revision serves keeps the marker.
        let bundle = std::fs::read(
            Path::new(env!("CARGO_MANIFEST_DIR")).join("../../frontend/public/mcp/ttobak-mcp.mjs"),
        )
        .expect("served adapter bundle");
        assert!(supports_report(&bundle));
    }

    #[test]
    fn forced_ends_use_report_codes() {
        assert_eq!(report_code(s("TIMEOUT")), s("REPORT_TIMEOUT"));
        assert_eq!(report_code(s("CANCELLED")), s("REPORT_CANCELLED"));
        assert_eq!(report_code(s("REPORT_NO_SFDC")), s("REPORT_NO_SFDC"));
        assert_eq!(report_code(None), None);
    }

    #[test]
    fn report_slot_is_independent_and_exclusive() {
        let state = ReportState::new();
        state.progress.lock().push("old".into());
        let _rx = state.try_begin().expect("first");
        assert!(state.progress.lock().to_vec().is_empty());
        assert_eq!(state.try_begin().err().as_deref(), Some("RUN_IN_PROGRESS"));
        state.end();
        assert!(state.try_begin().is_ok());
        state.end();
        state.shutting_down.store(true, Ordering::SeqCst);
        assert_eq!(state.try_begin().err().as_deref(), Some("SHUTTING_DOWN"));
    }
}
