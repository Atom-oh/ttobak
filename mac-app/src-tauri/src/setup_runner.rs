//! One-click intake setup: PATH repair, adapter refresh, the adapter's
//! `setup apply` (skill packages and MCP servers) and `intake setup`, with a
//! per-step result for each. A failed adapter step stops the rest; a failed
//! server step does not stop `intake setup`; new servers wait for [Install].

use std::collections::BTreeSet;
use std::ffi::OsString;
use std::sync::atomic::{AtomicBool, Ordering};
use std::time::Duration;

use parking_lot::Mutex;
use serde::Serialize;
use serde_json::{json, Value};
use tauri::{AppHandle, Emitter, Manager, State};

use crate::intake_plan::IntakeAction;
use crate::intake_runner::{self as intake, IntakeState, Outcome, PathRefresh, ProcTrack, RunKind};

/// The adapter supports `setup apply` (mcp-server/scripts/check-bundle.mjs asserts it).
const SETUP_MARKER: &[u8] = b"ttobak-mcp setup apply";
const STEP_EVENT: &str = "setup://step";
const FINISHED_EVENT: &str = "setup://finished";
/// The run now holds the intake run slot (the UI shows cancel and progress).
const RUNNING_EVENT: &str = "setup://running";
const CLI_TIMEOUT: Duration = Duration::from_secs(20 * 60);
// The adapter's limit: four server lists of at most 100 entries each.
const MAX_APPROVALS: usize = 400;
const MAX_KEY_BYTES: usize = 600;
const MAX_ITEMS: usize = 400;
const MAX_TEXT_CHARS: usize = 200;
const CLI_STEPS: [&str; 3] = ["packages", "skills", "servers"];

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize)]
#[serde(rename_all = "kebab-case")]
pub enum StepStatus {
    Ok,
    Current,
    Skipped,
    Failed,
    NeedsApproval,
}

impl StepStatus {
    fn parse(s: &str) -> Option<Self> {
        Some(match s {
            "ok" => Self::Ok,
            "current" => Self::Current,
            "skipped" => Self::Skipped,
            "failed" => Self::Failed,
            "needs-approval" => Self::NeedsApproval,
            _ => return None,
        })
    }
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct StepView {
    pub id: String,
    pub status: StepStatus,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub code: Option<String>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub items: Vec<String>,
}

impl StepView {
    fn new(id: &str, status: StepStatus, code: Option<&str>) -> Self {
        Self {
            id: id.to_string(),
            status,
            code: code.map(str::to_string),
            items: Vec::new(),
        }
    }
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
pub struct Approval {
    pub key: String,
    pub label: String,
}

pub(crate) struct CliSteps {
    pub steps: Vec<StepView>,
    pub needs_approval: Vec<Approval>,
    pub warnings: Vec<String>,
}

impl CliSteps {
    /// `intake setup` waits while the user has not confirmed new servers.
    pub(crate) fn run_intake(&self) -> bool {
        self.needs_approval.is_empty()
    }
}

fn short(s: &str) -> String {
    s.chars()
        .filter(|c| !c.is_control())
        .take(MAX_TEXT_CHARS)
        .collect()
}

fn strings(v: Option<&Value>) -> Vec<String> {
    v.and_then(Value::as_array)
        .map(|a| {
            a.iter()
                .filter_map(Value::as_str)
                .filter(|s| s.len() <= MAX_KEY_BYTES)
                .take(MAX_ITEMS)
                .map(short)
                .collect()
        })
        .unwrap_or_default()
}

fn is_code(c: &str) -> bool {
    !c.is_empty()
        && c.len() <= 64
        && c.bytes()
            .all(|b| b.is_ascii_alphanumeric() || b == b'_' || b == b'-')
}

/// The CLI's steps in a fixed order; anything missing or malformed is a
/// failure, and no CLI result at all fails the package step.
pub(crate) fn cli_steps(result: Option<&Value>, code: Option<&str>) -> CliSteps {
    let listed = result
        .and_then(|r| r.get("steps"))
        .and_then(Value::as_array);
    let Some(listed) = listed else {
        let failure = result
            .and_then(|r| r.get("code"))
            .and_then(Value::as_str)
            .filter(|c| is_code(c))
            .or(code)
            .unwrap_or("INVALID_RESULT");
        let mut steps = vec![StepView::new("packages", StepStatus::Failed, Some(failure))];
        steps.extend(skipped_after(&CLI_STEPS[1..]));
        return CliSteps {
            steps,
            needs_approval: Vec::new(),
            warnings: Vec::new(),
        };
    };
    let steps = CLI_STEPS
        .iter()
        .map(|id| {
            let entry = listed
                .iter()
                .find(|e| e.get("id").and_then(Value::as_str) == Some(id));
            let status = entry
                .and_then(|e| e.get("status"))
                .and_then(Value::as_str)
                .and_then(StepStatus::parse);
            match (entry, status) {
                (Some(e), Some(status)) => StepView {
                    id: id.to_string(),
                    status,
                    code: e
                        .get("code")
                        .and_then(Value::as_str)
                        .filter(|c| is_code(c))
                        .map(str::to_string),
                    items: strings(e.get("items")),
                },
                _ => StepView::new(id, StepStatus::Failed, Some("INVALID_RESULT")),
            }
        })
        .collect();
    let needs_approval = result
        .and_then(|r| r.get("needsApproval"))
        .and_then(Value::as_array)
        .map(|a| {
            a.iter()
                .filter_map(|e| {
                    let key = e.get("key")?.as_str()?;
                    let label = e.get("label")?.as_str()?;
                    (key.len() <= MAX_KEY_BYTES).then(|| Approval {
                        key: key.to_string(),
                        label: short(label),
                    })
                })
                .take(MAX_ITEMS)
                .collect()
        })
        .unwrap_or_default();
    CliSteps {
        steps,
        needs_approval,
        warnings: strings(result.and_then(|r| r.get("warnings"))),
    }
}

pub(crate) fn path_step(r: Result<PathRefresh, &str>) -> StepView {
    match r {
        Ok(p) if !p.captured => {
            StepView::new("path", StepStatus::Failed, Some("LOGIN_SHELL_UNAVAILABLE"))
        }
        Ok(p) if p.added.is_empty() => StepView::new("path", StepStatus::Current, None),
        Ok(p) => StepView {
            items: p.added,
            ..StepView::new("path", StepStatus::Ok, None)
        },
        Err(code) => StepView::new("path", StepStatus::Failed, Some(code)),
    }
}

pub(crate) fn adapter_step(r: Result<bool, String>) -> StepView {
    match r {
        Ok(true) => StepView::new("adapter", StepStatus::Ok, None),
        Ok(false) => StepView::new("adapter", StepStatus::Current, None),
        Err(code) => StepView::new("adapter", StepStatus::Failed, Some(&code)),
    }
}

/// Without a current adapter no later step can run.
pub(crate) fn continue_after_adapter(step: &StepView) -> bool {
    step.status != StepStatus::Failed
}

pub(crate) fn skipped_after(ids: &[&str]) -> Vec<StepView> {
    ids.iter()
        .map(|id| StepView::new(id, StepStatus::Skipped, None))
        .collect()
}

/// `intake setup` runs unless new servers wait for approval or the user
/// cancelled (during the package step or between phases). A TTOBAK login
/// failure does not stop it: on a fresh Mac `intake setup` is what imports the
/// TTOBAK endpoints, and the panel then offers the login.
pub(crate) fn should_run_intake(cli: &CliSteps, cli_code: Option<&str>, cancelled: bool) -> bool {
    cli.run_intake() && cli_code != Some("CANCELLED") && !cancelled
}

/// Why `intake setup` did not run.
pub(crate) fn intake_skip_reason(
    _cli: &CliSteps,
    cli_code: Option<&str>,
    cancelled: bool,
) -> &'static str {
    if cancelled || cli_code == Some("CANCELLED") {
        "CANCELLED"
    } else {
        "AWAITING_APPROVAL"
    }
}

fn intake_step(outcome: &Outcome) -> StepView {
    if outcome.exit_code == Some(0) && outcome.code.is_none() {
        StepView::new("intake", StepStatus::Ok, None)
    } else {
        let code = outcome.code.clone().unwrap_or_else(|| "EXIT_FAILED".into());
        StepView::new("intake", StepStatus::Failed, Some(&code))
    }
}

/// `--approve` keys: at most 400 of at most 600 printable bytes, never a flag.
pub(crate) fn validate_approve(keys: &[String]) -> Result<(), &'static str> {
    let ok = keys.len() <= MAX_APPROVALS
        && keys.iter().all(|k| {
            !k.is_empty()
                && k.len() <= MAX_KEY_BYTES
                && !k.starts_with('-')
                && !k.chars().any(char::is_control)
        });
    if ok {
        Ok(())
    } else {
        Err("invalid-argument")
    }
}

pub(crate) fn supports_setup(adapter: &[u8]) -> bool {
    adapter
        .windows(SETUP_MARKER.len())
        .any(|w| w == SETUP_MARKER)
}

// ---------------------------------------------------------------- state

#[derive(Default)]
pub struct SetupState {
    running: AtomicBool,
    last: Mutex<Option<Value>>,
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
pub struct SetupStateView {
    running: bool,
    last: Option<Value>,
}

pub fn manage(app: &tauri::App) {
    app.manage(SetupState::default());
}

struct Run<'a> {
    app: &'a AppHandle,
    steps: Vec<StepView>,
}

impl Run<'_> {
    fn push(&mut self, step: StepView) {
        let _ = self.app.emit(STEP_EVENT, &step);
        self.steps.push(step);
    }
}

async fn run_setup(app: &AppHandle, approve: Vec<String>, accept_private_calendar: bool) -> Value {
    let state = app.state::<IntakeState>();
    let mut run = Run {
        app,
        steps: Vec::new(),
    };
    let mut cli = CliSteps {
        steps: Vec::new(),
        needs_approval: Vec::new(),
        warnings: Vec::new(),
    };
    let Some(home) = intake::home_dir() else {
        run.push(StepView::new(
            "path",
            StepStatus::Failed,
            Some("HOME_MISSING"),
        ));
        return json!({ "steps": run.steps, "needsApproval": [], "warnings": [] });
    };
    run.push(path_step(intake::refresh_login_path(&state, &home).await));
    let adapter = adapter_step(intake::update_adapter(&state, &home).await);
    let go_on = continue_after_adapter(&adapter);
    run.push(adapter);
    if !go_on {
        for step in skipped_after(&["packages", "skills", "servers", "intake"]) {
            run.push(step);
        }
        return json!({ "steps": run.steps, "needsApproval": [], "warnings": [] });
    }
    let cancel = match state.try_begin(RunKind::OneClickSetup) {
        Ok(c) => c,
        Err(code) => {
            for id in ["packages", "skills", "servers", "intake"] {
                run.push(StepView::new(id, StepStatus::Failed, Some(&code)));
            }
            return json!({ "steps": run.steps, "needsApproval": [], "warnings": [] });
        }
    };
    let _ = app.emit(RUNNING_EVENT, json!({}));
    let mut cli_code: Option<String> = None;
    if intake::local_adapter(&home).is_some_and(|b| supports_setup(&b)) {
        (cli, cli_code) = run_cli(app, &state, approve, cancel).await;
    } else {
        cli.steps = vec![StepView::new(
            "packages",
            StepStatus::Failed,
            Some("ADAPTER_OUTDATED"),
        )];
        cli.steps.extend(skipped_after(&CLI_STEPS[1..]));
    }
    for step in cli.steps.clone() {
        run.push(step);
    }
    let cancelled = state.cancel_requested();
    if should_run_intake(&cli, cli_code.as_deref(), cancelled) {
        let action = IntakeAction::Setup {
            accept_private_calendar,
            skip: BTreeSet::new(),
        };
        // `intake_cancel` now reaches this phase.
        let cancel = state.rearm_cancel();
        let outcome = intake::execute(app, &action, intake::RUN_TIMEOUT, Some(cancel), true).await;
        let payload = json!({
            "action": RunKind::Setup,
            "exitCode": outcome.exit_code,
            "result": outcome.result,
            "code": outcome.code,
        });
        *state.last_result.lock() = Some(payload.clone());
        run.push(intake_step(&outcome));
        let _ = app.emit(intake::FINISHED_EVENT, payload);
    } else {
        let reason = intake_skip_reason(&cli, cli_code.as_deref(), cancelled);
        run.push(StepView::new("intake", StepStatus::Skipped, Some(reason)));
    }
    state.finish_run();
    json!({ "steps": run.steps, "needsApproval": cli.needs_approval, "warnings": cli.warnings })
}

async fn run_cli(
    app: &AppHandle,
    state: &IntakeState,
    approve: Vec<String>,
    cancel: tokio::sync::oneshot::Receiver<()>,
) -> (CliSteps, Option<String>) {
    let launch = match intake::node_launch(state).await {
        Ok(l) => l,
        Err(code) => return (cli_steps(None, Some(code)), Some(code.to_string())),
    };
    let mut args: Vec<OsString> = vec![
        launch.server.clone().into(),
        "setup".into(),
        "apply".into(),
        "--noninteractive".into(),
    ];
    for key in approve {
        args.extend(["--approve".into(), key.into()]);
    }
    let cmd = intake::node_command(&launch, args);
    let line_app = app.clone();
    let on_line = move |line: String| {
        let state = line_app.state::<IntakeState>();
        let _ = line_app.emit(intake::PROGRESS_EVENT, json!({ "line": line }));
        state.progress.lock().push(line);
    };
    let track = ProcTrack {
        pgid: &state.pgid,
        shutting_down: &state.shutting_down,
    };
    let outcome = intake::run_child(
        cmd,
        track,
        CLI_TIMEOUT,
        intake::KILL_GRACE,
        Some(cancel),
        on_line,
    )
    .await;
    (
        cli_steps(outcome.result.as_ref(), outcome.code.as_deref()),
        outcome.code,
    )
}

/// Starts one setup run; each step emits `setup://step`, the end `setup://finished`.
#[tauri::command]
pub async fn setup_start(
    app: AppHandle,
    approve: Vec<String>,
    accept_private_calendar: bool,
) -> Result<(), String> {
    validate_approve(&approve).map_err(str::to_string)?;
    app.state::<IntakeState>().available()?;
    let setup = app.state::<SetupState>();
    if setup.running.swap(true, Ordering::SeqCst) {
        return Err("RUN_IN_PROGRESS".into());
    }
    let app = app.clone();
    tauri::async_runtime::spawn(async move {
        let payload = run_setup(&app, approve, accept_private_calendar).await;
        let setup = app.state::<SetupState>();
        *setup.last.lock() = Some(payload.clone());
        setup.running.store(false, Ordering::SeqCst);
        let _ = app.emit(FINISHED_EVENT, payload);
    });
    Ok(())
}

#[tauri::command]
pub async fn setup_get_state(setup: State<'_, SetupState>) -> Result<SetupStateView, String> {
    Ok(SetupStateView {
        running: setup.running.load(Ordering::SeqCst),
        last: setup.last.lock().clone(),
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn cli(steps: serde_json::Value, needs: serde_json::Value) -> serde_json::Value {
        json!({ "ok": true, "dryRun": false, "steps": steps, "needsApproval": needs, "warnings": ["w"] })
    }

    #[test]
    fn setup_order_stops_after_adapter_failure() {
        let failed = adapter_step(Err("network".into()));
        assert_eq!(failed.status, StepStatus::Failed);
        assert_eq!(failed.code.as_deref(), Some("network"));
        assert!(!continue_after_adapter(&failed));
        assert!(continue_after_adapter(&adapter_step(Ok(false))));
        assert_eq!(adapter_step(Ok(false)).status, StepStatus::Current);
        assert_eq!(adapter_step(Ok(true)).status, StepStatus::Ok);
        let view = skipped_after(&["packages", "skills", "servers", "intake"]);
        assert!(view.iter().all(|s| s.status == StepStatus::Skipped));
    }

    #[test]
    fn servers_failure_still_runs_intake() {
        let out = cli(
            json!([{ "id": "packages", "status": "ok" }, { "id": "skills", "status": "current" },
                   { "id": "servers", "status": "failed", "code": "INSTALL_FAILED", "items": ["aim:x"] }]),
            json!([]),
        );
        let parsed = cli_steps(Some(&out), None);
        assert_eq!(parsed.steps.len(), 3);
        assert_eq!(parsed.steps[2].code.as_deref(), Some("INSTALL_FAILED"));
        assert!(parsed.run_intake());
        assert_eq!(parsed.warnings, vec!["w".to_string()]);
    }

    #[test]
    fn needs_approval_defers_intake() {
        let out = cli(
            json!([{ "id": "packages", "status": "ok" }, { "id": "skills", "status": "ok", "items": ["demo"] },
                   { "id": "servers", "status": "needs-approval", "items": ["aim:new-mcp"] }]),
            json!([{ "key": "aim:new-mcp", "label": "AIM 서버 new-mcp" }]),
        );
        let parsed = cli_steps(Some(&out), None);
        assert!(!parsed.run_intake());
        assert_eq!(parsed.needs_approval[0].key, "aim:new-mcp");
    }

    #[test]
    fn missing_cli_result_fails_the_package_step() {
        let parsed = cli_steps(None, Some("TIMEOUT"));
        assert_eq!(parsed.steps[0].id, "packages");
        assert_eq!(parsed.steps[0].status, StepStatus::Failed);
        assert_eq!(parsed.steps[0].code.as_deref(), Some("TIMEOUT"));
        assert!(parsed.steps[1..]
            .iter()
            .all(|s| s.status == StepStatus::Skipped));
        assert!(parsed.run_intake());
        // An unknown step id or status from the CLI is dropped, not trusted.
        let odd = cli(
            json!([{ "id": "../x", "status": "ok" }, { "id": "skills", "status": "weird" }]),
            json!([]),
        );
        assert!(cli_steps(Some(&odd), None)
            .steps
            .iter()
            .all(|s| s.status == StepStatus::Failed));
    }

    #[test]
    fn setup_is_idempotent_step_view() {
        let out = cli(
            json!([{ "id": "packages", "status": "ok" }, { "id": "skills", "status": "current" },
                   { "id": "servers", "status": "current" }]),
            json!([]),
        );
        let parsed = cli_steps(Some(&out), None);
        assert!(parsed.steps[1..]
            .iter()
            .all(|s| s.status == StepStatus::Current));
        assert_eq!(
            path_step(Ok(PathRefresh {
                added: vec![],
                captured: true
            }))
            .status,
            StepStatus::Current
        );
        assert_eq!(
            path_step(Ok(PathRefresh {
                added: vec!["/Users/u/.toolbox/bin".into()],
                captured: true
            }))
            .status,
            StepStatus::Ok
        );
        assert_eq!(
            path_step(Ok(PathRefresh {
                added: vec![],
                captured: false
            }))
            .code
            .as_deref(),
            Some("LOGIN_SHELL_UNAVAILABLE")
        );
    }

    #[test]
    fn approve_keys_are_bounded() {
        assert_eq!(validate_approve(&["aim:x".to_string()]), Ok(()));
        let many: Vec<String> = (0..401).map(|i| format!("aim:s{i}")).collect();
        assert_eq!(validate_approve(&many), Err("invalid-argument"));
        assert_eq!(validate_approve(&many[..400]), Ok(()));
        assert_eq!(
            validate_approve(&["x".repeat(601)]),
            Err("invalid-argument")
        );
        assert_eq!(validate_approve(&["".to_string()]), Err("invalid-argument"));
        assert_eq!(
            validate_approve(&["--approve".to_string()]),
            Err("invalid-argument")
        );
        assert_eq!(
            validate_approve(&["a\nb".to_string()]),
            Err("invalid-argument")
        );
    }

    #[test]
    fn setup_refuses_while_intake_runs() {
        let state = crate::intake_runner::IntakeState::new(None);
        let _run = state
            .try_begin(crate::intake_runner::RunKind::Run)
            .expect("first");
        assert_eq!(
            state
                .try_begin(crate::intake_runner::RunKind::OneClickSetup)
                .err()
                .as_deref(),
            Some("RUN_IN_PROGRESS")
        );
    }

    #[test]
    fn cancelled_cli_skips_intake() {
        let parsed = cli_steps(None, Some("CANCELLED"));
        assert!(!should_run_intake(&parsed, Some("CANCELLED"), false));
        assert!(should_run_intake(&parsed, Some("TIMEOUT"), false));
    }

    #[test]
    fn login_required_skips_intake() {
        let out = json!({ "ok": false, "steps": [
            { "id": "packages", "status": "failed", "code": "TTOBAK_LOGIN_REQUIRED" },
            { "id": "skills", "status": "skipped" }, { "id": "servers", "status": "skipped" }],
            "needsApproval": [], "warnings": [] });
        let parsed = cli_steps(Some(&out), None);
        // intake setup imports the TTOBAK endpoints on a fresh Mac: it still runs.
        assert!(should_run_intake(&parsed, None, false));
        assert!(!should_run_intake(&parsed, None, true));
        assert_eq!(intake_skip_reason(&parsed, None, true), "CANCELLED");
    }

    #[test]
    fn a_cancel_between_phases_is_remembered() {
        let state = crate::intake_runner::IntakeState::new(None);
        let rx = state
            .try_begin(crate::intake_runner::RunKind::OneClickSetup)
            .expect("begin");
        drop(rx); // the first phase ended and dropped its receiver
        assert!(!state.send_cancel());
        assert!(state.cancel_requested());
        state.finish_run();
        let _rx = state
            .try_begin(crate::intake_runner::RunKind::Run)
            .expect("again");
        assert!(!state.cancel_requested());
    }

    #[test]
    fn cancel_reaches_every_phase() {
        let state = crate::intake_runner::IntakeState::new(None);
        let mut first = state
            .try_begin(crate::intake_runner::RunKind::OneClickSetup)
            .expect("begin");
        let mut second = state.rearm_cancel();
        assert!(state.send_cancel());
        assert!(second.try_recv().is_ok());
        // The first phase's receiver no longer belongs to the run.
        assert!(first.try_recv().is_err());
    }

    #[test]
    fn finishing_a_setup_run_reevaluates_a_due_slot() {
        let state = crate::intake_runner::IntakeState::new(None);
        let _rx = state
            .try_begin(crate::intake_runner::RunKind::OneClickSetup)
            .expect("begin");
        state.mark_retry_after_run();
        state.finish_run();
        assert!(!state.retry_pending());
        assert!(state.reevaluate_pending());
        assert!(state.try_begin(crate::intake_runner::RunKind::Run).is_ok());
    }

    #[test]
    fn setup_support_is_detected_by_the_adapter_marker() {
        assert!(supports_setup(
            b"... ttobak-mcp setup apply [--approve <key>] ..."
        ));
        assert!(!supports_setup(b"ttobak-mcp intake setup"));
    }
}
