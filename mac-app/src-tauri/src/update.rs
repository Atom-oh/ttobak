//! In-app updates: periodic checks, the install guard and the app menu item.
//!
//! Uses only `tauri-plugin-updater`'s Rust API; no `updater:` permission is
//! granted to any capability, so neither the remote SPA nor the prompt window
//! can call the plugin's commands. Checks run on launch and every 6 hours. An
//! automatic check never shows anything on failure and never prompts while the
//! app is busy (`control::app_busy`); it re-evaluates every 30 seconds until
//! idle. Install refuses while busy, both before downloading and again after
//! the verified download, so a recording that started meanwhile keeps the
//! update available instead of restarting the app.
//!
//! Until the release key exists, `tauri.conf.json` carries the placeholder
//! pubkey: the plugin still initializes (it decodes the key only when
//! verifying a download), but `start` and `check_now` do nothing with it.

use std::path::PathBuf;
use std::sync::atomic::{AtomicBool, Ordering};
use std::time::Duration;

use parking_lot::Mutex;
use serde_json::{json, Value};
use tauri::menu::{Menu, MenuItem};
use tauri::{AppHandle, Manager, Wry};
use tauri_plugin_updater::{Update, UpdaterExt};

use crate::update_policy::{is_newer, should_prompt};

pub const PLACEHOLDER_PUBKEY: &str = "REPLACE_WITH_TTOBAK_UPDATER_PUBKEY";
pub const MENU_ID: &str = "update";
const SETTINGS_FILE: &str = "update.json";
const CURRENT_VERSION: &str = env!("CARGO_PKG_VERSION");
const CHECK_INTERVAL: Duration = Duration::from_secs(6 * 60 * 60);
const BUSY_RECHECK: Duration = Duration::from_secs(30);
const UP_TO_DATE_PREFIX: &str = "최신 버전입니다";
const CHECK_FAILED: &str = "업데이트를 확인하지 못했습니다. 잠시 후 다시 시도해 주세요.";

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum CheckOutcome {
    UpToDate,
    Available { version: String, notes: String },
    Failed,
}

/// What a check result asks the UI to do.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Reaction {
    Nothing,
    /// A native message (manual checks only).
    Notice(String),
    /// Open the update popup (automatic checks: only once idle).
    Offer,
}

#[allow(dead_code)] // called by the update popup (update_prompt.rs, next task)
#[derive(Debug, thiserror::Error)]
pub enum InstallError {
    #[error("the app is busy recording, finalizing or uploading")]
    Busy,
    #[error("no update is available")]
    NoUpdate,
    #[error("update download or install failed: {0}")]
    Download(String),
}

#[derive(Default)]
pub struct UpdateState {
    /// The last update a check found; cleared when a check finds none.
    available: Mutex<Option<Update>>,
    declined: Mutex<Option<String>>,
    settings_path: Mutex<Option<PathBuf>>,
    installing: AtomicBool,
    menu_item: Mutex<Option<MenuItem<Wry>>>,
}

impl UpdateState {
    fn pending(&self) -> Option<(String, String)> {
        self.available
            .lock()
            .as_ref()
            .map(|u| (u.version.clone(), u.body.clone().unwrap_or_default()))
    }
}

pub fn menu_label(available: Option<&str>) -> String {
    match available {
        Some(version) => format!("업데이트 가능 (v{version})…"),
        None => "업데이트 확인…".to_owned(),
    }
}

/// The declined version in `update.json`; missing or malformed → `None`.
pub fn parse_declined(bytes: Option<&[u8]>) -> Option<String> {
    let value: Value = serde_json::from_slice(bytes?).ok()?;
    let version = value.get("declinedVersion")?.as_str()?;
    (!version.is_empty()).then(|| version.to_owned())
}

pub fn declined_json(version: &str) -> Vec<u8> {
    json!({ "declinedVersion": version })
        .to_string()
        .into_bytes()
}

/// False for a missing, blank or placeholder pubkey: updates stay disabled.
pub fn pubkey_configured(pubkey: Option<&str>) -> bool {
    pubkey.is_some_and(|k| !k.trim().is_empty() && k != PLACEHOLDER_PUBKEY)
}

/// Maps a check result to its outcome and UI reaction. Any check error
/// (unreachable, HTML, malformed manifest, missing `darwin-aarch64`) is
/// `Failed`: silent on an automatic check, a message on a manual one. A manual
/// check offers even a declined version.
pub fn outcome_for<E: std::fmt::Display>(
    manual: bool,
    result: Result<Option<(String, String)>, E>,
    current: &str,
    declined: Option<&str>,
) -> (CheckOutcome, Reaction) {
    let up_to_date = || {
        let reaction = if manual {
            Reaction::Notice(format!("{UP_TO_DATE_PREFIX} (v{current})"))
        } else {
            Reaction::Nothing
        };
        (CheckOutcome::UpToDate, reaction)
    };
    match result {
        Err(_) => {
            let reaction = if manual {
                Reaction::Notice(CHECK_FAILED.to_owned())
            } else {
                Reaction::Nothing
            };
            (CheckOutcome::Failed, reaction)
        }
        Ok(None) => up_to_date(),
        Ok(Some((version, _))) if !is_newer(current, &version) => up_to_date(),
        Ok(Some((version, notes))) => {
            let reaction = if manual || should_prompt(current, &version, declined) {
                Reaction::Offer
            } else {
                Reaction::Nothing
            };
            (CheckOutcome::Available { version, notes }, reaction)
        }
    }
}

fn configured(app: &AppHandle) -> bool {
    pubkey_configured(
        app.config()
            .plugins
            .0
            .get("updater")
            .and_then(|u| u.get("pubkey"))
            .and_then(Value::as_str),
    )
}

/// Loads the declined version, then checks on launch and every 6 hours.
pub fn start(app: AppHandle) {
    let state = app.state::<UpdateState>();
    if let Ok(dir) = app.path().app_config_dir() {
        let path = dir.join(SETTINGS_FILE);
        *state.declined.lock() = parse_declined(std::fs::read(&path).ok().as_deref());
        *state.settings_path.lock() = Some(path);
    }
    if !configured(&app) {
        log::info!("updater public key not configured; update checks disabled");
        return;
    }
    tauri::async_runtime::spawn(async move {
        loop {
            let next = tokio::time::Instant::now() + CHECK_INTERVAL;
            if let CheckOutcome::Available { .. } = check_now(&app, false).await {
                offer_when_idle(&app, next).await;
            }
            tokio::time::sleep_until(next).await;
        }
    });
}

/// Waits (30 s steps, until `deadline`) for the app to be idle, then offers
/// the known update once if it should still be prompted.
async fn offer_when_idle(app: &AppHandle, deadline: tokio::time::Instant) {
    loop {
        if !crate::control::app_busy(app) {
            let state = app.state::<UpdateState>();
            let declined = state.declined.lock().clone();
            if let Some((version, notes)) = state.pending() {
                if should_prompt(CURRENT_VERSION, &version, declined.as_deref()) {
                    crate::update_prompt::offer(app, &version, &notes);
                }
            }
            return;
        }
        if tokio::time::Instant::now() + BUSY_RECHECK >= deadline {
            return;
        }
        tokio::time::sleep(BUSY_RECHECK).await;
    }
}

/// Checks the endpoint once. A manual check reports up-to-date/failure with a
/// native message and opens the popup for any available update.
pub async fn check_now(app: &AppHandle, manual: bool) -> CheckOutcome {
    let result = if configured(app) {
        match app.updater() {
            Ok(updater) => updater.check().await.map_err(|e| e.to_string()),
            Err(e) => Err(e.to_string()),
        }
    } else {
        Err("updater public key not configured".to_owned())
    };
    if let Err(e) = &result {
        log::warn!("update check failed: {e}");
    }
    let found = result.as_ref().ok().cloned().flatten();
    let summary = result.map(|u| {
        u.map(|u| {
            let notes = u.body.unwrap_or_default();
            (u.version, notes)
        })
    });
    let state = app.state::<UpdateState>();
    let declined = state.declined.lock().clone();
    let (outcome, reaction) = outcome_for(manual, summary, CURRENT_VERSION, declined.as_deref());
    match outcome {
        CheckOutcome::Available { .. } => *state.available.lock() = found,
        CheckOutcome::UpToDate => *state.available.lock() = None,
        // Keep a previously found update across a transient failure.
        CheckOutcome::Failed => {}
    }
    refresh_menu(app);
    match (&reaction, &outcome) {
        (Reaction::Notice(message), _) => show_notice(app, message),
        (Reaction::Offer, CheckOutcome::Available { version, notes }) if manual => {
            crate::update_prompt::offer(app, version, notes)
        }
        _ => {}
    }
    outcome
}

/// Clears the `installing` flag however `install` returns.
struct InstallingGuard<'a>(&'a AtomicBool);

impl Drop for InstallingGuard<'_> {
    fn drop(&mut self) {
        self.0.store(false, Ordering::SeqCst);
    }
}

/// Downloads, verifies and installs the known update, then restarts. Refuses
/// while busy before the download and again before installing; a refusal
/// keeps the update available.
#[allow(dead_code)] // called by the update popup (update_prompt.rs, next task)
pub async fn install(app: &AppHandle) -> Result<(), InstallError> {
    let state = app.state::<UpdateState>();
    let update = state
        .available
        .lock()
        .clone()
        .ok_or(InstallError::NoUpdate)?;
    if crate::control::app_busy(app) || state.installing.swap(true, Ordering::SeqCst) {
        return Err(InstallError::Busy);
    }
    let _installing = InstallingGuard(&state.installing);
    // `download` verifies the signature against the configured pubkey.
    let bytes = update
        .download(|_, _| {}, || {})
        .await
        .map_err(|e| InstallError::Download(e.to_string()))?;
    if crate::control::app_busy(app) {
        log::info!(
            "update v{} downloaded but the app became busy; not installing",
            update.version
        );
        return Err(InstallError::Busy);
    }
    update
        .install(&bytes)
        .map_err(|e| InstallError::Download(e.to_string()))?;
    if crate::control::app_busy(app) {
        // The new bundle is on disk; it runs from the next launch.
        log::info!(
            "update v{} installed; restart deferred while busy",
            update.version
        );
        *state.available.lock() = None;
        refresh_menu(app);
        return Ok(());
    }
    log::info!("update v{} installed; restarting", update.version);
    app.restart();
}

/// Stores the available version as declined (persisted in `update.json`).
#[allow(dead_code)] // called by the update popup (update_prompt.rs, next task)
pub fn decline(app: &AppHandle) {
    let state = app.state::<UpdateState>();
    let Some((version, _)) = state.pending() else {
        return;
    };
    *state.declined.lock() = Some(version.clone());
    let path = state.settings_path.lock().clone();
    if let Some(path) = path {
        let written = path
            .parent()
            .map_or(Ok(()), std::fs::create_dir_all)
            .and_then(|()| std::fs::write(&path, declined_json(&version)));
        if let Err(e) = written {
            log::warn!("could not persist declined update version: {e}");
        }
    }
}

/// Builds the default menu with the update item after "About" in the app
/// submenu.
pub fn install_menu(app: &AppHandle) -> tauri::Result<()> {
    let menu = Menu::default(app)?;
    let item = MenuItem::with_id(app, MENU_ID, menu_label(None), true, None::<&str>)?;
    let items = menu.items()?;
    match items.first().and_then(|k| k.as_submenu()) {
        Some(app_menu) => app_menu.insert(&item, 1)?,
        None => log::warn!("app submenu missing; update menu item not added"),
    }
    app.set_menu(menu)?;
    *app.state::<UpdateState>().menu_item.lock() = Some(item);
    Ok(())
}

pub fn refresh_menu(app: &AppHandle) {
    let state = app.state::<UpdateState>();
    let label = menu_label(state.pending().as_ref().map(|(v, _)| v.as_str()));
    let item = state.menu_item.lock().clone();
    if let Some(item) = item {
        if let Err(e) = item.set_text(label) {
            log::warn!("could not update the update menu item: {e}");
        }
    }
}

/// The update menu item: opens the popup for a known update, else checks.
pub fn on_menu_event(app: &AppHandle, id: &str) {
    if id != MENU_ID {
        return;
    }
    match app.state::<UpdateState>().pending() {
        Some((version, notes)) => crate::update_prompt::offer(app, &version, &notes),
        None => {
            let app = app.clone();
            tauri::async_runtime::spawn(async move {
                check_now(&app, true).await;
            });
        }
    }
}

/// A native alert with `message` (manual check results).
fn show_notice(app: &AppHandle, message: &str) {
    #[cfg(target_os = "macos")]
    {
        let message = message.to_owned();
        let shown = app.run_on_main_thread(move || {
            use objc2::MainThreadMarker;
            use objc2_app_kit::NSAlert;
            use objc2_foundation::NSString;
            let Some(mtm) = MainThreadMarker::new() else {
                return;
            };
            let alert = NSAlert::new(mtm);
            alert.setMessageText(&NSString::from_str(&message));
            alert.runModal();
        });
        if let Err(e) = shown {
            log::warn!("could not show update notice: {e}");
        }
    }
    #[cfg(not(target_os = "macos"))]
    {
        let _ = app;
        log::info!("{message}");
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn menu_labels() {
        assert_eq!(menu_label(None), "업데이트 확인…");
        assert_eq!(menu_label(Some("0.3.0")), "업데이트 가능 (v0.3.0)…");
    }

    #[test]
    fn declined_round_trip_and_bad_files() {
        let bytes = declined_json("0.2.0");
        let value: serde_json::Value = serde_json::from_slice(&bytes).unwrap();
        assert_eq!(value, serde_json::json!({ "declinedVersion": "0.2.0" }));
        assert_eq!(parse_declined(Some(&bytes)).as_deref(), Some("0.2.0"));
        assert_eq!(parse_declined(None), None);
        assert_eq!(parse_declined(Some(b"not json")), None);
        assert_eq!(parse_declined(Some(b"{}")), None);
        assert_eq!(parse_declined(Some(br#"{"declinedVersion":3}"#)), None);
        assert_eq!(parse_declined(Some(br#"{"declinedVersion":""}"#)), None);
    }

    #[test]
    fn placeholder_or_missing_pubkey_is_not_configured() {
        assert!(!pubkey_configured(None));
        assert!(!pubkey_configured(Some("")));
        assert!(!pubkey_configured(Some("  ")));
        assert!(!pubkey_configured(Some(PLACEHOLDER_PUBKEY)));
        assert!(pubkey_configured(Some(
            "dW50cnVzdGVkIGNvbW1lbnQ6IG1pbmlzaWdu"
        )));
    }

    /// The checked-in placeholder must still parse as the plugin's config, or
    /// plugin init would fail and the app would not launch (ruling 2).
    #[test]
    fn checked_in_updater_config_parses_and_is_unconfigured() {
        let conf: serde_json::Value =
            serde_json::from_str(include_str!("../tauri.conf.json")).expect("json");
        let updater = conf["plugins"]["updater"].clone();
        assert_eq!(
            updater["endpoints"],
            serde_json::json!(["https://ttobak.atomai.click/mac/latest.json"])
        );
        let parsed: tauri_plugin_updater::Config =
            serde_json::from_value(updater.clone()).expect("plugin config parses");
        assert_eq!(parsed.endpoints.len(), 1);
        assert!(!pubkey_configured(updater["pubkey"].as_str()));
    }

    /// The release manifest carries an extra `sha256` per platform; the
    /// plugin's own response type must ignore it.
    #[test]
    fn manifest_with_sha256_parses_via_plugin_type() {
        let manifest = r#"{
            "version": "0.2.0",
            "notes": "fixes",
            "pub_date": "2026-11-10T00:00:00Z",
            "platforms": {
                "darwin-aarch64": {
                    "url": "https://ttobak.atomai.click/mac/TTOBAK_0.2.0_aarch64.app.tar.gz",
                    "signature": "c2ln",
                    "sha256": "00ff"
                }
            }
        }"#;
        let release: tauri_plugin_updater::RemoteRelease =
            serde_json::from_str(manifest).expect("manifest parses");
        assert_eq!(release.version.to_string(), "0.2.0");
        assert!(release.download_url("darwin-aarch64").is_ok());
        assert!(release.download_url("darwin-x86_64").is_err());
    }

    fn available(v: &str) -> Result<Option<(String, String)>, String> {
        Ok(Some((v.to_owned(), "notes".to_owned())))
    }

    #[test]
    fn check_errors_fail_silently_when_automatic() {
        for err in [
            "unreachable",
            "HTML instead of JSON",
            "malformed manifest",
            "darwin-aarch64 missing",
        ] {
            let r: Result<Option<(String, String)>, String> = Err(err.to_owned());
            assert_eq!(
                outcome_for(false, r, "0.1.0", None),
                (CheckOutcome::Failed, Reaction::Nothing),
                "{err}"
            );
            let r: Result<Option<(String, String)>, String> = Err(err.to_owned());
            assert_eq!(
                outcome_for(true, r, "0.1.0", None),
                (
                    CheckOutcome::Failed,
                    Reaction::Notice(
                        "업데이트를 확인하지 못했습니다. 잠시 후 다시 시도해 주세요.".to_owned()
                    )
                ),
                "{err}"
            );
        }
    }

    #[test]
    fn up_to_date_notifies_only_when_manual() {
        assert_eq!(
            outcome_for::<String>(false, Ok(None), "0.1.0", None),
            (CheckOutcome::UpToDate, Reaction::Nothing)
        );
        assert_eq!(
            outcome_for::<String>(true, Ok(None), "0.1.0", None),
            (
                CheckOutcome::UpToDate,
                Reaction::Notice("최신 버전입니다 (v0.1.0)".to_owned())
            )
        );
    }

    #[test]
    fn available_offers_unless_auto_and_declined() {
        let want = CheckOutcome::Available {
            version: "0.2.0".to_owned(),
            notes: "notes".to_owned(),
        };
        assert_eq!(
            outcome_for(false, available("0.2.0"), "0.1.0", None),
            (want.clone(), Reaction::Offer)
        );
        assert_eq!(
            outcome_for(false, available("0.2.0"), "0.1.0", Some("0.2.0")),
            (want.clone(), Reaction::Nothing)
        );
        assert_eq!(
            outcome_for(true, available("0.2.0"), "0.1.0", Some("0.2.0")),
            (want, Reaction::Offer)
        );
        assert_eq!(
            outcome_for(false, available("0.3.0"), "0.1.0", Some("0.2.0")).1,
            Reaction::Offer
        );
    }

    /// A manifest version that is not newer is never offered, even manually.
    #[test]
    fn not_newer_is_up_to_date() {
        assert_eq!(
            outcome_for(true, available("0.1.0"), "0.1.0", None),
            (
                CheckOutcome::UpToDate,
                Reaction::Notice("최신 버전입니다 (v0.1.0)".to_owned())
            )
        );
    }
}
