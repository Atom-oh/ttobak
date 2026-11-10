//! The "new version available" popup (ADR-061 window pattern, like
//! `meeting_prompt.rs`). It loads the bundled `update-prompt.html`, a local
//! origin, and may call only `update_prompt_choose`. Choosing 업데이트 is the
//! user's confirmation; nothing downloads or installs without it.

use tauri::{AppHandle, Manager};

use crate::update::InstallError;

pub const WINDOW_LABEL: &str = "update-prompt";
const WIDTH: f64 = 420.0;
const HEIGHT: f64 = 220.0;
/// Below the menu bar.
const TOP_MARGIN: f64 = 36.0;
/// Release notes shown in the popup are cut to this many characters.
const MAX_NOTES_CHARS: usize = 500;

/// Percent-encodes a query value (UTF-8 bytes).
fn encode(value: &str) -> String {
    value
        .bytes()
        .map(|b| match b {
            b'A'..=b'Z' | b'a'..=b'z' | b'0'..=b'9' | b'-' | b'_' | b'.' => (b as char).to_string(),
            _ => format!("%{b:02X}"),
        })
        .collect()
}

pub fn page_url(version: &str, notes: &str) -> String {
    let notes: String = notes.chars().take(MAX_NOTES_CHARS).collect();
    format!(
        "update-prompt.html?version={}&notes={}",
        encode(version),
        encode(&notes)
    )
}

/// Left edge that centers the popup on a screen of `screen_width`.
pub fn centered_x(screen_x: f64, screen_width: f64) -> f64 {
    screen_x + ((screen_width - WIDTH) / 2.0).max(0.0)
}

/// The version an open popup shows (its `version` query value).
pub fn shown_version(url: &tauri::Url) -> Option<String> {
    url.query_pairs()
        .find(|(k, _)| k == "version")
        .map(|(_, v)| v.into_owned())
}

/// Whether an open popup should be reloaded to show `version`: only when it
/// shows another version and no install is running (a reload mid-install
/// would lose the install reply).
pub fn needs_refresh(shown: Option<&str>, version: &str, installing: bool) -> bool {
    !installing && shown != Some(version)
}

/// Opens the popup for `version`. An already-open popup is focused, and
/// reloaded in place if it shows another version; it is never closed and
/// rebuilt, since that races the async close under the same label and could
/// drop the window mid-download.
pub fn offer(app: &AppHandle, version: &str, notes: &str) {
    if let Some(w) = app.get_webview_window(WINDOW_LABEL) {
        if let Ok(mut url) = w.url() {
            let shown = shown_version(&url);
            let installing = crate::update::installing(app);
            if needs_refresh(shown.as_deref(), version, installing) {
                let page = page_url(version, notes);
                let query = page.split_once('?').map(|(_, q)| q.to_owned());
                url.set_query(query.as_deref());
                if let Err(e) = w.navigate(url) {
                    log::warn!("could not refresh the update prompt: {e}");
                }
            }
        }
        let _ = w.show();
        let _ = w.set_focus();
        return;
    }
    let (x, y) = match app.primary_monitor() {
        Ok(Some(m)) => {
            let scale = m.scale_factor();
            let pos = m.position().to_logical::<f64>(scale);
            let size = m.size().to_logical::<f64>(scale);
            (centered_x(pos.x, size.width), pos.y + TOP_MARGIN)
        }
        _ => (400.0, TOP_MARGIN),
    };
    let url = tauri::WebviewUrl::App(page_url(version, notes).into());
    let built = tauri::WebviewWindowBuilder::new(app, WINDOW_LABEL, url)
        .title("TTOBAK")
        .inner_size(WIDTH, HEIGHT)
        .position(x, y)
        .decorations(false)
        .resizable(false)
        .always_on_top(true)
        .visible_on_all_workspaces(true)
        .skip_taskbar(true)
        .focused(true)
        .shadow(true)
        .build();
    if let Err(e) = built {
        log::warn!("could not open the update prompt: {e}");
    }
}

/// Closes the popup if it is open.
pub fn close(app: &AppHandle) {
    if let Some(w) = app.get_webview_window(WINDOW_LABEL) {
        let _ = w.close();
    }
}

/// What the page gets back from an install attempt, and whether the popup
/// closes. The popup stays open on every failure so the user always sees why.
#[derive(Debug, PartialEq, Eq)]
pub struct InstallOutcome {
    pub reply: Result<String, String>,
    pub close: bool,
}

pub fn install_outcome(result: Result<(), InstallError>) -> InstallOutcome {
    match result {
        Ok(()) => InstallOutcome {
            reply: Ok("installed".into()),
            close: true,
        },
        Err(InstallError::Busy) => InstallOutcome {
            reply: Ok("busy".into()),
            close: false,
        },
        Err(InstallError::NoUpdate) => InstallOutcome {
            reply: Err("설치할 업데이트가 없습니다.".into()),
            close: false,
        },
        Err(InstallError::Download(_)) => InstallOutcome {
            reply: Err("업데이트를 내려받지 못했습니다. 잠시 후 다시 시도해 주세요.".into()),
            close: false,
        },
    }
}

/// The popup's buttons: `install` or `later`. Replies `"installed"` (the app
/// restarts or defers the restart) or `"busy"` (the page shows the retry-after-recording message).
#[tauri::command]
pub async fn update_prompt_choose(action: String, app: AppHandle) -> Result<String, String> {
    log::info!("update prompt: {action}");
    match action.as_str() {
        "later" => {
            crate::update::decline(&app);
            close(&app);
            Ok("later".into())
        }
        "install" => {
            let result = crate::update::install(&app).await;
            if let Err(e) = &result {
                log::warn!("update install did not complete: {e}");
            }
            let outcome = install_outcome(result);
            if outcome.close {
                close(&app);
            }
            outcome.reply
        }
        _ => Err("action must be install or later".into()),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_page_url_encodes_version_and_notes() {
        assert_eq!(
            page_url("0.3.0", "메모"),
            "update-prompt.html?version=0.3.0&notes=%EB%A9%94%EB%AA%A8"
        );
        assert_eq!(
            page_url("1.0.0", "a&b=c"),
            "update-prompt.html?version=1.0.0&notes=a%26b%3Dc"
        );
    }

    #[test]
    fn notes_are_truncated_to_500_characters() {
        let notes = "가".repeat(600);
        let url = page_url("0.3.0", &notes);
        let encoded = url.split("notes=").nth(1).expect("notes");
        assert_eq!(encoded, "%EA%B0%80".repeat(500));
    }

    #[test]
    fn an_open_popup_refreshes_only_for_another_version_when_idle() {
        let url = tauri::Url::parse(&format!("tauri://localhost/{}", page_url("0.2.0", "a&b")))
            .expect("url");
        assert_eq!(shown_version(&url).as_deref(), Some("0.2.0"));
        assert!(!needs_refresh(Some("0.2.0"), "0.2.0", false));
        assert!(needs_refresh(Some("0.2.0"), "0.3.0", false));
        assert!(!needs_refresh(Some("0.2.0"), "0.3.0", true));
        assert!(needs_refresh(None, "0.3.0", false));
    }

    #[test]
    fn the_popup_is_centered_and_stays_on_screen() {
        assert_eq!(centered_x(0.0, 1440.0), (1440.0 - WIDTH) / 2.0);
        assert_eq!(centered_x(0.0, 300.0), 0.0);
    }

    #[test]
    fn busy_keeps_the_popup_open_and_replies_busy() {
        assert_eq!(
            install_outcome(Err(InstallError::Busy)),
            InstallOutcome {
                reply: Ok("busy".into()),
                close: false
            }
        );
    }

    #[test]
    fn failures_keep_the_popup_open_with_a_message() {
        for err in [
            InstallError::NoUpdate,
            InstallError::Download("boom".into()),
        ] {
            let outcome = install_outcome(Err(err));
            assert!(!outcome.close);
            let message = outcome.reply.expect_err("an error message");
            assert!(!message.contains("boom"), "internal detail leaked");
        }
    }

    #[test]
    fn success_closes_the_popup() {
        assert!(install_outcome(Ok(())).close);
    }

    #[test]
    fn the_page_never_uses_inner_html_and_shows_the_busy_message() {
        let page = include_str!("../../src/update-prompt.html");
        assert!(!page.contains("innerHTML"));
        assert!(page.contains("녹음이 끝난 뒤 다시 시도해 주세요."));
        assert!(page.contains("update_prompt_choose"));
    }

    #[test]
    fn only_the_update_prompt_capability_grants_the_command() {
        let dir = std::path::Path::new(env!("CARGO_MANIFEST_DIR")).join("capabilities");
        let mut granting = Vec::new();
        for entry in std::fs::read_dir(&dir).expect("capabilities dir") {
            let path = entry.expect("entry").path();
            if path.extension().and_then(|e| e.to_str()) != Some("json") {
                continue;
            }
            let cap: serde_json::Value =
                serde_json::from_slice(&std::fs::read(&path).expect("read")).expect("json");
            let grants = cap["permissions"]
                .as_array()
                .expect("permissions")
                .iter()
                .any(|p| p.as_str() == Some("allow-update-prompt-choose"));
            if grants {
                assert!(cap.get("remote").is_none(), "{} is remote", path.display());
                assert_eq!(cap["windows"], serde_json::json!(["update-prompt"]));
                granting.push(cap["identifier"].as_str().unwrap_or_default().to_owned());
            }
        }
        assert_eq!(granting, vec!["update-prompt".to_owned()]);
        let default = include_str!("../capabilities/default.json");
        assert!(!default.contains("update-prompt-choose"));
        let build = include_str!("../build.rs");
        assert!(build.contains("\"update_prompt_choose\""));
    }
}
