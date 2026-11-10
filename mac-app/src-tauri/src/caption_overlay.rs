//! Transparent, always-on-top caption overlay windows, one per language,
//! shown over a full-screen slideshow.
//!
//! The remote main window opens and closes them, sets their style and pushes
//! caption text; Rust relays text to the matching window as Tauri events.
//! Each window loads the bundled `src/caption-overlay.html` (a local origin),
//! holds no token, makes no network request and may call no app command
//! (`capabilities/caption-overlay.json`). Caption text is never logged.
//!
//! Each page listens with a target scoped to its own window, so an event sent
//! with `emit_to` reaches only that window; every payload also names its
//! language and the page drops any other language's payload.

use crate::caption_model::{self, CaptionSettings, CaptionStyle, WindowLayout};
use crate::caption_translate::{CaptionError, CaptionState};
use parking_lot::Mutex;
use serde::Serialize;
use std::time::{Duration, Instant};
use tauri::{AppHandle, Emitter, EventTarget, Manager, State, WebviewWindow, WindowEvent};

pub const MIN_WIDTH: f64 = 320.0;
pub const MIN_HEIGHT: f64 = 80.0;
/// Upper bound for a saved width or height, in logical points.
pub const MAX_EXTENT: f64 = 10_000.0;
const MAX_ID_BYTES: usize = 64;
const MAX_TEXT_BYTES: usize = 4000;
const PUSH_KINDS: [&str; 3] = ["entry", "clear", "preview"];
/// Minimum spacing between layout writes caused by moves and resizes.
const SAVE_INTERVAL: Duration = Duration::from_millis(500);
/// Logical rectangle `(x, y, width, height)`.
type Rect = (f64, f64, f64, f64);
const FALLBACK_SCREEN: Rect = (0.0, 0.0, 1440.0, 900.0);

/// `NSScreenSaverWindowLevel`: above full-screen presentation windows.
#[cfg(target_os = "macos")]
const SCREEN_SAVER_LEVEL: isize = 1000;
/// `canJoinAllSpaces | stationary | fullScreenAuxiliary`.
#[cfg(target_os = "macos")]
const COLLECTION_BEHAVIOR: usize = (1 << 0) | (1 << 4) | (1 << 8);

/// Page path for a language's window.
pub fn overlay_url(lang: &str) -> String {
    format!("caption-overlay.html?lang={lang}")
}

/// Window label for `lang`; `ja`/`zh` only when it is the selected extra
/// language.
pub fn extra_language_gate(lang: &str, extra: Option<&str>) -> Result<&'static str, CaptionError> {
    let label = caption_model::window_label(lang)
        .ok_or_else(|| CaptionError::invalid("Unsupported caption language."))?;
    if matches!(lang, "ja" | "zh") && extra != Some(lang) {
        return Err(CaptionError::invalid(
            "Select this language as the extra caption language first.",
        ));
    }
    Ok(label)
}

/// Window label for `lang` under the current settings. Open, set-style and
/// push all go through this gate.
pub fn gated_label(settings: &CaptionSettings, lang: &str) -> Result<&'static str, CaptionError> {
    extra_language_gate(lang, settings.extra_language.as_deref())
}

/// Validates a caption push.
pub fn check_push(kind: &str, id: &str, text: &str) -> Result<(), CaptionError> {
    if !PUSH_KINDS.contains(&kind) {
        return Err(CaptionError::invalid(
            "Caption kind must be entry, clear or preview.",
        ));
    }
    if id.len() > MAX_ID_BYTES {
        return Err(CaptionError::invalid("Caption id is too long."));
    }
    if text.len() > MAX_TEXT_BYTES {
        return Err(CaptionError::invalid("Caption text is too long."));
    }
    Ok(())
}

fn extent(v: f64, default: f64, min: f64) -> f64 {
    if v.is_finite() && v > 0.0 {
        v.clamp(min, MAX_EXTENT)
    } else {
        default
    }
}

/// Saved position (only when both coordinates are finite) and a size clamped
/// to `[MIN, MAX_EXTENT]`; non-finite or non-positive sizes use the default.
pub fn sanitize_geometry(layout: &WindowLayout) -> (Option<(f64, f64)>, (f64, f64)) {
    let d = WindowLayout::default();
    let size = (
        extent(layout.width, d.width, MIN_WIDTH),
        extent(layout.height, d.height, MIN_HEIGHT),
    );
    let pos = match (layout.x, layout.y) {
        (Some(x), Some(y)) if x.is_finite() && y.is_finite() => Some((x, y)),
        _ => None,
    };
    (pos, size)
}

/// Spaces layout writes at least `SAVE_INTERVAL` apart, with at most one
/// write scheduled at a time.
#[derive(Default)]
pub struct SaveThrottle {
    last: Option<Instant>,
    pending: bool,
}

impl SaveThrottle {
    /// Delay before the write the caller must schedule, or `None` when a
    /// write is already scheduled.
    pub fn request(&mut self, now: Instant) -> Option<Duration> {
        if self.pending {
            return None;
        }
        self.pending = true;
        Some(match self.last {
            Some(last) => SAVE_INTERVAL.saturating_sub(now.saturating_duration_since(last)),
            None => Duration::ZERO,
        })
    }

    /// Marks the scheduled write as starting now. Call it before reading the
    /// settings to write, so a change made during the write schedules another.
    pub fn wrote(&mut self, now: Instant) {
        self.pending = false;
        self.last = Some(now);
    }
}

/// Managed state for layout write throttling.
#[derive(Default)]
pub struct OverlayState {
    throttle: Mutex<SaveThrottle>,
}

/// `caption-update` payload.
#[derive(Clone, Serialize)]
#[serde(rename_all = "camelCase")]
struct CaptionUpdate {
    lang: String,
    kind: String,
    id: String,
    order: u32,
    text: String,
    is_final: bool,
}

impl CaptionUpdate {
    fn new(lang: &str, kind: &str, id: &str, order: u32, text: &str, is_final: bool) -> Self {
        Self {
            lang: lang.to_string(),
            kind: kind.to_string(),
            id: id.to_string(),
            order,
            text: text.to_string(),
            is_final,
        }
    }
}

/// `caption-style` payload: the style fields plus the window's language.
#[derive(Clone, Serialize)]
struct StyleUpdate<'a> {
    lang: &'a str,
    #[serde(flatten)]
    style: &'a CaptionStyle,
}

/// Whether this window event must close every overlay: the overlays have no
/// close control and float above everything, so they must not outlive the
/// main window.
pub fn closes_overlays(label: &str, event: &WindowEvent) -> bool {
    label == "main" && matches!(event, WindowEvent::Destroyed)
}

/// Destroys every open overlay without emitting `CloseRequested`, so their
/// saved `open` flags survive and they reopen next session. Runs on the main
/// thread from the run-loop callback.
pub fn destroy_all(app: &AppHandle) {
    for lang in caption_model::LANGS {
        let Some(label) = caption_model::window_label(lang) else {
            continue;
        };
        if let Some(window) = app.get_webview_window(label) {
            if let Err(e) = window.destroy() {
                log::warn!("caption overlay destroy failed: {e}");
            }
        }
    }
}

fn window_error(what: &str, e: tauri::Error) -> CaptionError {
    log::warn!("caption overlay {what} failed: {e}");
    CaptionError::from_code("window-error")
}

fn logical(m: &tauri::Monitor) -> Rect {
    let scale = m.scale_factor();
    let p = m.position().to_logical::<f64>(scale);
    let s = m.size().to_logical::<f64>(scale);
    (p.x, p.y, s.width, s.height)
}

/// All monitors and the primary one, in logical points.
fn screens(app: &AppHandle) -> (Vec<Rect>, Rect) {
    let monitors: Vec<Rect> = app
        .available_monitors()
        .unwrap_or_default()
        .iter()
        .map(logical)
        .collect();
    let primary = app
        .primary_monitor()
        .ok()
        .flatten()
        .map(|m| logical(&m))
        .or_else(|| monitors.first().copied())
        .unwrap_or(FALLBACK_SCREEN);
    (monitors, primary)
}

/// Lifts the window above full-screen apps on every Space.
#[cfg(target_os = "macos")]
fn float_above_full_screen(window: &WebviewWindow) {
    let target = window.clone();
    let result = window.run_on_main_thread(move || {
        let Ok(ns_window) = target.ns_window() else {
            log::warn!("caption overlay: no native window");
            return;
        };
        let ns_window = ns_window.cast::<objc2::runtime::AnyObject>();
        if ns_window.is_null() {
            return;
        }
        // SAFETY: `ns_window` is the live NSWindow of `target`, used on the
        // main thread; both selectors take a plain integer and return void.
        unsafe {
            let _: () = objc2::msg_send![ns_window, setLevel: SCREEN_SAVER_LEVEL];
            let _: () = objc2::msg_send![ns_window, setCollectionBehavior: COLLECTION_BEHAVIOR];
        }
    });
    if let Err(e) = result {
        log::warn!("caption overlay level change failed: {e}");
    }
}

#[cfg(not(target_os = "macos"))]
fn float_above_full_screen(_window: &WebviewWindow) {}

/// Records a geometry change in memory and schedules a throttled write.
fn remember(app: &AppHandle, lang: &str, change: impl FnOnce(&mut WindowLayout)) {
    let state = app.state::<CaptionState>();
    let changed = state.update(false, |s| {
        if let Some(w) = s.windows.get_mut(lang) {
            change(w);
        }
    });
    if changed.is_err() {
        return;
    }
    let Some(delay) = app
        .state::<OverlayState>()
        .throttle
        .lock()
        .request(Instant::now())
    else {
        return;
    };
    let app = app.clone();
    tauri::async_runtime::spawn(async move {
        tokio::time::sleep(delay).await;
        app.state::<OverlayState>()
            .throttle
            .lock()
            .wrote(Instant::now());
        let _ = app.state::<CaptionState>().persist();
    });
}

fn mark_closed(state: &CaptionState, lang: &str) -> Result<CaptionSettings, CaptionError> {
    state.update(true, |s| {
        if let Some(w) = s.windows.get_mut(lang) {
            w.open = false;
        }
    })
}

/// Keeps the saved layout in step with the window. Runs on the main thread.
fn watch(app: &AppHandle, window: &WebviewWindow, lang: String) {
    let app = app.clone();
    let label = window.label().to_string();
    window.on_window_event(move |event| {
        let scale = || {
            app.get_webview_window(&label)
                .and_then(|w| w.scale_factor().ok())
                .unwrap_or(1.0)
        };
        match event {
            WindowEvent::Moved(p) => {
                let p = p.to_logical::<f64>(scale());
                remember(&app, &lang, |w| {
                    w.x = Some(p.x);
                    w.y = Some(p.y);
                });
            }
            WindowEvent::Resized(size) if size.width > 0 && size.height > 0 => {
                let size = size.to_logical::<f64>(scale());
                remember(&app, &lang, |w| {
                    w.width = size.width;
                    w.height = size.height;
                });
            }
            WindowEvent::CloseRequested { .. } => {
                let _ = mark_closed(&app.state::<CaptionState>(), &lang);
            }
            _ => {}
        }
    });
}

/// Initial style for the page, before any `caption-style` event. The style
/// was validated, so the JSON holds only allowlisted fonts, `#RRGGBB` colors
/// and numbers.
fn style_script(style: &CaptionStyle) -> String {
    let json = serde_json::to_string(style).unwrap_or_else(|_| "null".into());
    format!("window.__TTOBAK_CAPTION_STYLE__ = {json};")
}

/// Opens (or reuses) the overlay for `lang` and returns its saved layout.
#[tauri::command]
pub async fn caption_overlay_open(
    app: AppHandle,
    state: State<'_, CaptionState>,
    lang: String,
) -> Result<WindowLayout, CaptionError> {
    let settings = state.snapshot();
    let label = gated_label(&settings, &lang)?;
    let saved = settings.windows.get(&lang).cloned().unwrap_or_default();
    let (saved_pos, size) = sanitize_geometry(&saved);

    let pos = if app.get_webview_window(label).is_some() {
        saved_pos
    } else {
        let (monitors, primary) = screens(&app);
        let (x, y) = caption_model::placement(saved_pos, size, &monitors, primary);
        let url = tauri::WebviewUrl::App(overlay_url(&lang).into());
        let built = tauri::WebviewWindowBuilder::new(&app, label, url)
            .title("TTOBAK captions")
            .initialization_script(style_script(&saved.style))
            .inner_size(size.0, size.1)
            .min_inner_size(MIN_WIDTH, MIN_HEIGHT)
            .position(x, y)
            .transparent(true)
            .decorations(false)
            .shadow(false)
            .resizable(true)
            .always_on_top(true)
            .visible_on_all_workspaces(true)
            .skip_taskbar(true)
            .focused(false)
            .build();
        match built {
            Ok(window) => {
                float_above_full_screen(&window);
                watch(&app, &window, lang.clone());
            }
            // A concurrent open created it first.
            Err(_) if app.get_webview_window(label).is_some() => {}
            Err(e) => return Err(window_error("open", e)),
        }
        Some((x, y))
    };

    let next = state.update(true, |s| {
        let w = s.windows.entry(lang.clone()).or_default();
        w.open = true;
        if let Some((x, y)) = pos {
            w.x = Some(x);
            w.y = Some(y);
        }
        w.width = size.0;
        w.height = size.1;
    })?;
    Ok(next.windows.get(&lang).cloned().unwrap_or_default())
}

/// Closes the overlay for `lang` (if open) and saves it as closed.
#[tauri::command]
pub async fn caption_overlay_close(
    app: AppHandle,
    state: State<'_, CaptionState>,
    lang: String,
) -> Result<(), CaptionError> {
    let label = caption_model::window_label(&lang)
        .ok_or_else(|| CaptionError::invalid("Unsupported caption language."))?;
    if let Some(window) = app.get_webview_window(label) {
        window.close().map_err(|e| window_error("close", e))?;
    }
    mark_closed(&state, &lang)?;
    Ok(())
}

/// Validates and saves a window's style, then sends it to that window.
#[tauri::command]
pub async fn caption_overlay_set_style(
    app: AppHandle,
    state: State<'_, CaptionState>,
    lang: String,
    style: CaptionStyle,
) -> Result<(), CaptionError> {
    let label = gated_label(&state.snapshot(), &lang)?;
    caption_model::validate_style(&style).map_err(|e| CaptionError::invalid(&e))?;
    let mut gated = Ok(label);
    state.update(true, |s| {
        // Re-checked under the lock: the extra language may have changed.
        gated = gated_label(s, &lang);
        if gated.is_ok() {
            s.windows.entry(lang.clone()).or_default().style = style.clone();
        }
    })?;
    gated?;
    if app.get_webview_window(label).is_some() {
        let payload = StyleUpdate {
            lang: &lang,
            style: &style,
        };
        app.emit_to(EventTarget::webview_window(label), "caption-style", payload)
            .map_err(|e| window_error("style", e))?;
    }
    Ok(())
}

/// Sends caption text to the window for `lang`; a closed window ignores it.
/// An `entry` with empty text removes that line.
// The arguments are the command's wire contract (one per payload field).
#[allow(clippy::too_many_arguments)]
#[tauri::command]
pub async fn caption_overlay_push(
    app: AppHandle,
    state: State<'_, CaptionState>,
    lang: String,
    kind: String,
    id: String,
    order: u32,
    text: String,
    is_final: bool,
) -> Result<(), CaptionError> {
    let label = gated_label(&state.snapshot(), &lang)?;
    check_push(&kind, &id, &text)?;
    if app.get_webview_window(label).is_none() {
        return Ok(());
    }
    let update = CaptionUpdate::new(&lang, &kind, &id, order, &text, is_final);
    app.emit_to(EventTarget::webview_window(label), "caption-update", update)
        .map_err(|e| window_error("push", e))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn push_rejects_bad_kind_and_oversized_fields() {
        assert!(check_push("entry", "a1", "hello").is_ok());
        assert!(check_push("clear", "", "").is_ok());
        // Empty entry text removes that line from the window.
        assert!(check_push("entry", "a1", "").is_ok());
        assert!(check_push("preview", "", "").is_ok());
        assert_eq!(
            check_push("bogus", "a1", "x").unwrap_err().code,
            "invalid-input"
        );
        assert!(check_push("entry", "a1", &"a".repeat(4000)).is_ok());
        assert_eq!(
            check_push("entry", "a1", &"a".repeat(4001))
                .unwrap_err()
                .code,
            "invalid-input"
        );
        assert!(check_push("entry", &"i".repeat(64), "x").is_ok());
        assert_eq!(
            check_push("entry", &"i".repeat(65), "x").unwrap_err().code,
            "invalid-input"
        );
    }

    #[test]
    fn payloads_carry_the_window_language() {
        let update = serde_json::to_value(CaptionUpdate::new("en", "entry", "a1", 3, "hi", true))
            .expect("json");
        assert_eq!(update["lang"], "en");
        assert_eq!(update["kind"], "entry");
        assert_eq!(update["id"], "a1");
        assert_eq!(update["order"], 3);
        assert_eq!(update["text"], "hi");
        assert_eq!(update["isFinal"], true);
        let style = CaptionStyle::default();
        let value = serde_json::to_value(StyleUpdate {
            lang: "zh",
            style: &style,
        })
        .expect("json");
        assert_eq!(value["lang"], "zh");
        assert_eq!(value["fontSize"], style.font_size);
        assert_eq!(value["fontColor"], style.font_color.as_str());
    }

    #[test]
    fn overlays_close_only_when_the_main_window_is_destroyed() {
        assert!(closes_overlays("main", &WindowEvent::Destroyed));
        assert!(!closes_overlays("caption-ko", &WindowEvent::Destroyed));
        assert!(!closes_overlays("meeting-prompt", &WindowEvent::Destroyed));
        assert!(!closes_overlays("main", &WindowEvent::Focused(false)));
    }

    #[test]
    fn overlay_url_names_the_language() {
        assert_eq!(overlay_url("zh"), "caption-overlay.html?lang=zh");
        assert_eq!(overlay_url("ko"), "caption-overlay.html?lang=ko");
    }

    #[test]
    fn extra_languages_open_only_when_selected() {
        assert_eq!(
            extra_language_gate("ja", Some("zh")).unwrap_err().code,
            "invalid-input"
        );
        assert!(extra_language_gate("ja", None).is_err());
        assert!(extra_language_gate("fr", None).is_err());
        assert_eq!(extra_language_gate("ja", Some("ja")).unwrap(), "caption-ja");
        assert_eq!(extra_language_gate("ko", Some("zh")).unwrap(), "caption-ko");
        assert_eq!(extra_language_gate("en", None).unwrap(), "caption-en");
    }

    #[test]
    fn open_style_and_push_share_the_extra_language_gate() {
        let zh = CaptionSettings {
            extra_language: Some("zh".into()),
            ..CaptionSettings::default()
        };
        assert_eq!(gated_label(&zh, "ja").unwrap_err().code, "invalid-input");
        assert_eq!(gated_label(&zh, "zh").unwrap(), "caption-zh");
        assert_eq!(gated_label(&zh, "ko").unwrap(), "caption-ko");
        let none = CaptionSettings::default();
        assert!(gated_label(&none, "ja").is_err());
        assert!(gated_label(&none, "zh").is_err());
        assert!(gated_label(&none, "fr").is_err());
        assert_eq!(gated_label(&none, "en").unwrap(), "caption-en");
    }

    #[test]
    fn overlay_capability_grants_only_listening_dragging_and_sizing() {
        let cap: serde_json::Value =
            serde_json::from_str(include_str!("../capabilities/caption-overlay.json"))
                .expect("json");
        assert!(cap.get("remote").is_none(), "no remote origin");
        let windows: Vec<&str> = cap["windows"]
            .as_array()
            .expect("windows")
            .iter()
            .filter_map(|w| w.as_str())
            .collect();
        assert_eq!(
            windows,
            ["caption-ko", "caption-en", "caption-ja", "caption-zh"]
        );
        let mut granted: Vec<&str> = cap["permissions"]
            .as_array()
            .expect("permissions")
            .iter()
            .map(|p| p.as_str().expect("plain permission id"))
            .collect();
        granted.sort();
        assert_eq!(
            granted,
            [
                "core:event:allow-listen",
                "core:event:allow-unlisten",
                "core:window:allow-set-size",
                "core:window:allow-start-dragging",
            ]
        );
        for p in &granted {
            assert_ne!(*p, "core:event:default");
            assert!(!p.contains("allow-emit"), "{p}");
            assert!(p.starts_with("core:"), "app command permission {p}");
        }
    }

    #[test]
    fn geometry_rejects_non_finite_and_non_positive_values() {
        let d = WindowLayout::default();
        let bad = WindowLayout {
            x: Some(f64::NAN),
            y: Some(10.0),
            width: f64::INFINITY,
            height: -5.0,
            ..d.clone()
        };
        assert_eq!(sanitize_geometry(&bad), (None, (d.width, d.height)));
        let zero = WindowLayout {
            width: 0.0,
            height: 0.0,
            ..d.clone()
        };
        assert_eq!(sanitize_geometry(&zero), (None, (d.width, d.height)));
        let small = WindowLayout {
            x: Some(-200.0),
            y: Some(40.0),
            width: 10.0,
            height: 1.0,
            ..d.clone()
        };
        assert_eq!(
            sanitize_geometry(&small),
            (Some((-200.0, 40.0)), (MIN_WIDTH, MIN_HEIGHT))
        );
        let huge = WindowLayout {
            width: 1e9,
            height: 1e9,
            ..d.clone()
        };
        assert_eq!(sanitize_geometry(&huge).1, (MAX_EXTENT, MAX_EXTENT));
    }

    #[test]
    fn layout_writes_are_throttled_to_one_per_interval() {
        let mut t = SaveThrottle::default();
        let t0 = Instant::now();
        assert_eq!(t.request(t0), Some(Duration::ZERO));
        assert_eq!(t.request(t0), None, "a write is already scheduled");
        t.wrote(t0);
        assert_eq!(
            t.request(t0 + Duration::from_millis(100)),
            Some(Duration::from_millis(400))
        );
        t.wrote(t0 + Duration::from_millis(500));
        assert_eq!(
            t.request(t0 + Duration::from_millis(1500)),
            Some(Duration::ZERO)
        );
    }

    #[test]
    fn overlay_errors_avoid_skew_words() {
        for e in [
            check_push("bogus", "", "").unwrap_err(),
            extra_language_gate("ja", Some("zh")).unwrap_err(),
            extra_language_gate("fr", None).unwrap_err(),
        ] {
            let m = e.message.to_lowercase();
            for bad in ["not found", "not allowed", "unknown command"] {
                assert!(!m.contains(bad), "{m}");
            }
        }
    }
}
