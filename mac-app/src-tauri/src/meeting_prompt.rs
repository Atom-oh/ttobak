//! The floating "meeting detected" prompt with a Record button (ADR-061).
//!
//! macOS notifications from the app were not shown reliably, so, like
//! Notion's meeting notes, the app opens its own small always-on-top window
//! at the top center of the screen above the meeting app. Choosing Record is
//! the user's confirmation and starts the recording through
//! `meeting_detection_respond`, the same path as the in-app banner; Ignore
//! dismisses the detection. Nothing records without that choice. The window
//! loads a bundled page (`src/meeting-prompt.html`), never a remote origin,
//! and may call only `meeting_prompt_choose`.

use tauri::{AppHandle, Manager};

pub const WINDOW_LABEL: &str = "meeting-prompt";
const WIDTH: f64 = 420.0;
const HEIGHT: f64 = 72.0;
/// Below the menu bar.
const TOP_MARGIN: f64 = 36.0;

/// Default meeting title, matching the SPA banner: "Zoom 회의 · 11월 8일 21:05".
pub fn meeting_title(label: &str, month: u32, day: u32, hour: u32, minute: u32) -> String {
    format!("{label} 회의 · {month}월 {day}일 {hour:02}:{minute:02}")
}

/// Percent-encodes a query value (labels are short ASCII, IDs alphanumeric).
fn encode(value: &str) -> String {
    value
        .bytes()
        .map(|b| match b {
            b'A'..=b'Z' | b'a'..=b'z' | b'0'..=b'9' | b'-' | b'_' | b'.' => (b as char).to_string(),
            _ => format!("%{b:02X}"),
        })
        .collect()
}

pub fn page_url(detection_id: &str, label: &str) -> String {
    format!(
        "meeting-prompt.html?id={}&label={}",
        encode(detection_id),
        encode(label)
    )
}

/// Left edge that centers the prompt on a screen of `screen_width`.
pub fn centered_x(screen_x: f64, screen_width: f64) -> f64 {
    screen_x + ((screen_width - WIDTH) / 2.0).max(0.0)
}

#[cfg(target_os = "macos")]
fn local_title(label: &str) -> String {
    #[repr(C)]
    struct Tm {
        tm_sec: i32,
        tm_min: i32,
        tm_hour: i32,
        tm_mday: i32,
        tm_mon: i32,
        tm_year: i32,
        tm_wday: i32,
        tm_yday: i32,
        tm_isdst: i32,
        tm_gmtoff: std::ffi::c_long,
        tm_zone: *const std::ffi::c_char,
    }
    extern "C" {
        fn time(t: *mut i64) -> i64;
        fn localtime_r(t: *const i64, out: *mut Tm) -> *mut Tm;
    }
    let mut tm: Tm = unsafe { std::mem::zeroed() };
    let now = unsafe { time(std::ptr::null_mut()) };
    if unsafe { localtime_r(&now, &mut tm) }.is_null() {
        return format!("{label} 회의");
    }
    meeting_title(
        label,
        (tm.tm_mon + 1) as u32,
        tm.tm_mday as u32,
        tm.tm_hour as u32,
        tm.tm_min as u32,
    )
}

#[cfg(not(target_os = "macos"))]
fn local_title(label: &str) -> String {
    format!("{label} 회의")
}

/// Opens the prompt for a detected meeting, replacing any earlier one.
pub fn offer(app: &AppHandle, detection_id: &str, label: &str) {
    close(app);
    let (x, y) = match app.primary_monitor() {
        Ok(Some(m)) => {
            let scale = m.scale_factor();
            let pos = m.position().to_logical::<f64>(scale);
            let size = m.size().to_logical::<f64>(scale);
            (centered_x(pos.x, size.width), pos.y + TOP_MARGIN)
        }
        _ => (400.0, TOP_MARGIN),
    };
    let url = tauri::WebviewUrl::App(page_url(detection_id, label).into());
    let built = tauri::WebviewWindowBuilder::new(app, WINDOW_LABEL, url)
        .title("TTOBAK")
        .inner_size(WIDTH, HEIGHT)
        .position(x, y)
        .decorations(false)
        .resizable(false)
        .always_on_top(true)
        .visible_on_all_workspaces(true)
        .skip_taskbar(true)
        .focused(false)
        .shadow(true)
        .build();
    if let Err(e) = built {
        log::warn!("could not open the meeting prompt: {e}");
    }
}

/// Closes the prompt if it is open (answered, ended, or started elsewhere).
pub fn close(app: &AppHandle) {
    if let Some(w) = app.get_webview_window(WINDOW_LABEL) {
        let _ = w.close();
    }
}

/// Tells the main window the detection was answered here, so its banner
/// prompt closes (`native-meeting-answered`, same payload as detections).
fn answered(app: &AppHandle, detection_id: &str) {
    use tauri::Emitter;
    let payload = app
        .state::<std::sync::Arc<crate::meeting_detect::MeetingState>>()
        .last_json(detection_id);
    if let Some(payload) = payload {
        let _ = app.emit_to("main", "native-meeting-answered", payload);
    }
}

/// The prompt's buttons: `start`, `dismiss`, or `close` (timed out).
#[tauri::command]
pub async fn meeting_prompt_choose(
    app: AppHandle,
    detection_id: String,
    action: String,
) -> Result<(), String> {
    log::info!("meeting prompt: {action}");
    let id_for_event = detection_id.clone();
    let result = match action.as_str() {
        "start" => {
            let label = app
                .state::<std::sync::Arc<crate::meeting_detect::MeetingState>>()
                .last_label(&detection_id)
                .ok_or("unknown or expired detection")?;
            let reply = crate::meeting_detect::meeting_detection_respond(
                app.clone(),
                detection_id,
                "start".into(),
                Some(local_title(label)),
            )
            .await?;
            if reply.get("ok").and_then(serde_json::Value::as_bool) == Some(true) {
                Ok(())
            } else {
                Err(reply
                    .pointer("/error/message")
                    .and_then(serde_json::Value::as_str)
                    .unwrap_or("녹음을 시작하지 못했습니다.")
                    .to_owned())
            }
        }
        "dismiss" => crate::meeting_detect::meeting_detection_respond(
            app.clone(),
            detection_id,
            "dismiss".into(),
            None,
        )
        .await
        .map(|_| ()),
        "close" => Ok(()),
        _ => Err("action must be start, dismiss or close".into()),
    };
    if result.is_ok() {
        close(&app);
        if action != "close" {
            answered(&app, &id_for_event);
        }
    }
    result
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_title_matches_the_banner_format() {
        assert_eq!(
            meeting_title("Zoom", 11, 8, 9, 5),
            "Zoom 회의 · 11월 8일 09:05"
        );
        assert_eq!(
            meeting_title("Google Meet", 1, 31, 21, 30),
            "Google Meet 회의 · 1월 31일 21:30"
        );
    }

    #[test]
    fn the_page_url_encodes_its_parameters() {
        assert_eq!(
            page_url("meeting-3", "Google Meet"),
            "meeting-prompt.html?id=meeting-3&label=Google%20Meet"
        );
        assert_eq!(
            page_url("a&b", "x=y"),
            "meeting-prompt.html?id=a%26b&label=x%3Dy"
        );
    }

    #[test]
    fn the_prompt_is_centered_and_stays_on_screen() {
        assert_eq!(centered_x(0.0, 1440.0), (1440.0 - WIDTH) / 2.0);
        assert_eq!(centered_x(1440.0, 1920.0), 1440.0 + (1920.0 - WIDTH) / 2.0);
        assert_eq!(centered_x(0.0, 300.0), 0.0);
    }
}
