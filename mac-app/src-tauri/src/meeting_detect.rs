//! Meeting detection runtime (ADR-061): watches which processes use the
//! microphone and offers to record when a meeting app or a web-meeting tab
//! is active.
//!
//! A background thread polls Core Audio process objects' `IsRunningInput`,
//! `IsRunningOutput`, `PID` and `BundleID`. A browser using
//! the microphone counts only if one of its tabs is a meeting URL; an
//! AppleScript filters tab URLs inside the browser and returns only meeting
//! URLs, which are classified and never logged. Rules live in `meeting.rs`.
//!
//! Detection never records by itself: the SPA shows the prompt and a confirmed
//! start goes through the same `native-control-request` path as MCP control.

use std::path::PathBuf;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;
use std::time::{Duration, Instant};

use parking_lot::Mutex;
use serde_json::{json, Value};
use tauri::{AppHandle, Emitter, Manager, State};

use crate::control;
use crate::control_proto::{self as proto, Action};
use crate::meeting::{Detection, Detector, Event};
use crate::RecorderState;

/// Poll interval while some process is capturing or playing audio.
const ACTIVE_POLL: Duration = Duration::from_secs(1);
/// Poll interval while no process is capturing or playing audio.
const IDLE_POLL: Duration = Duration::from_secs(2);
const SETTINGS_FILE: &str = "meeting-detection.json";

pub struct MeetingState {
    enabled: AtomicBool,
    detector: Mutex<Detector>,
    /// The last announced detection, kept after it ends so its stop prompt
    /// can still be answered.
    last: Mutex<Option<Detection>>,
    /// The recording a detection may stop (detection ID and WAV path): the one
    /// its confirmed start produced, or the one running when it ended. A stop
    /// prompt may stop only that recording.
    started: Mutex<Option<(String, PathBuf)>>,
    settings_path: Mutex<Option<PathBuf>>,
}

impl MeetingState {
    /// The event payload of `id` while it is the latest detection.
    pub fn last_json(&self, id: &str) -> Option<Value> {
        self.last
            .lock()
            .as_ref()
            .filter(|d| d.id == id)
            .map(detection_json)
    }

    /// The label of `id` while it is the latest detection.
    pub fn last_label(&self, id: &str) -> Option<&'static str> {
        self.last
            .lock()
            .as_ref()
            .filter(|d| d.id == id)
            .map(|d| d.source.kind.label())
    }
}

impl Default for MeetingState {
    fn default() -> Self {
        MeetingState {
            enabled: AtomicBool::new(true),
            detector: Mutex::new(Detector::default()),
            last: Mutex::new(None),
            started: Mutex::new(None),
            settings_path: Mutex::new(None),
        }
    }
}

/// Whether `id`'s confirmed start produced the recording running now.
fn owns_recording(app: &AppHandle, state: &MeetingState, id: &str) -> bool {
    let current = app.state::<RecorderState>().recorder.lock().snapshot().path;
    state
        .started
        .lock()
        .as_ref()
        .is_some_and(|(started_id, path)| started_id == id && current.as_ref() == Some(path))
}

fn detection_json(d: &Detection) -> Value {
    json!({
        "detectionId": d.id,
        "kind": d.source.kind.id(),
        "label": d.source.kind.label(),
        "browser": d.source.browser.map(|b| b.app_bundle_id()),
    })
}

/// Loads the saved preference and starts the watcher thread.
pub fn start(app: AppHandle) {
    let state = app.state::<Arc<MeetingState>>().inner().clone();
    if let Ok(dir) = app.path().app_config_dir() {
        let path = dir.join(SETTINGS_FILE);
        if let Some(enabled) = std::fs::read(&path)
            .ok()
            .and_then(|b| serde_json::from_slice::<Value>(&b).ok())
            .and_then(|v| v.get("enabled").and_then(Value::as_bool))
        {
            state.enabled.store(enabled, Ordering::Relaxed);
        }
        *state.settings_path.lock() = Some(path);
    }
    #[cfg(target_os = "macos")]
    {
        let spawned = std::thread::Builder::new()
            .name("meeting-detect".into())
            .spawn(move || watch(app, state));
        if let Err(e) = spawned {
            log::warn!("meeting detection unavailable: {e}");
        }
    }
    #[cfg(not(target_os = "macos"))]
    let _ = (app, state);
}

#[cfg(target_os = "macos")]
fn watch(app: AppHandle, state: Arc<MeetingState>) {
    let epoch = Instant::now();
    let mut probe = mac::Probe::default();
    // Browser instances last seen using audio: a Meet page that has left the
    // call is read there after the browser releases its audio.
    let mut meet_pids: Vec<i32> = Vec::new();
    let mut meet_left = crate::meeting::LeftCall::default();
    let mut last_meet_state: Option<crate::zoom_mute::MicState> = None;
    let mut ended_logged = false;
    loop {
        if !state.enabled.load(Ordering::Relaxed) {
            std::thread::sleep(IDLE_POLL);
            continue;
        }
        // Core Audio reads happen without any app lock held.
        let sources = probe.sources();
        let recording = app
            .state::<RecorderState>()
            .recorder
            .lock()
            .snapshot()
            .recording;
        let now_ms = epoch.elapsed().as_millis() as u64;
        // Zoom's own UI ends its meeting at once (window closed or Zoom quit)
        // instead of waiting for its audio to stay silent for the grace period.
        let zoom = crate::meeting::Source {
            kind: crate::meeting::MeetingKind::Zoom,
            browser: None,
        };
        let zoom_involved = sources.contains(&zoom) || state.detector.lock().tracks(&zoom);
        let mut ended: Vec<String> = if zoom_involved
            && crate::zoom_mute::meeting_over(crate::zoom_mute::ProcessSources {
                process_pid: crate::audio::macos::process_pid,
                browser_pids: capturing_browser_pids,
            }) {
            vec![zoom.key()]
        } else {
            Vec::new()
        };
        // Likewise a Meet page that shows it left the call (Accessibility),
        // instead of waiting for the browser's audio grace period. Like the
        // Zoom end, it keeps filtering the source while the page stays left,
        // so audio the browser still holds does not resume the meeting.
        let is_meet = |s: &crate::meeting::Source| {
            s.kind == crate::meeting::MeetingKind::GoogleMeet && s.browser.is_some()
        };
        let mut meet_sources: Vec<crate::meeting::Source> =
            sources.iter().copied().filter(|s| is_meet(s)).collect();
        if let Some(t) = state
            .detector
            .lock()
            .tracked_source()
            .filter(|s| is_meet(s))
        {
            if !meet_sources.contains(&t) {
                meet_sources.push(t);
            }
        }
        if meet_sources.is_empty() {
            meet_left = crate::meeting::LeftCall::default();
            last_meet_state = None;
        } else {
            let capturing = capturing_browser_pids();
            if !capturing.is_empty() {
                meet_pids = capturing;
            }
            let meet_state = crate::meet_mute::current_state(&meet_pids);
            if last_meet_state != Some(meet_state) {
                log::info!(
                    "Meet page state: {meet_state:?} ({} browser instance(s))",
                    meet_pids.len()
                );
                last_meet_state = Some(meet_state);
            }
            let left = meet_state == crate::zoom_mute::MicState::NoMeeting;
            if meet_left.observe(now_ms, left) {
                if !ended_logged {
                    log::info!("Meet page left the call; ending without the audio grace");
                    ended_logged = true;
                }
                ended.extend(meet_sources.iter().map(|s| s.key()));
            } else {
                ended_logged = false;
            }
        }
        let events = state
            .detector
            .lock()
            .observe_with_ended(now_ms, &sources, recording, &ended);
        for event in events {
            announce(&app, &state, &event);
        }
        std::thread::sleep(if sources.is_empty() && !probe.audio_active() {
            IDLE_POLL
        } else {
            ACTIVE_POLL
        });
    }
}

#[cfg_attr(not(target_os = "macos"), allow(dead_code))]
fn announce(app: &AppHandle, state: &MeetingState, event: &Event) {
    use tauri_plugin_notification::NotificationExt;
    // A meeting's end offers to stop whatever native recording is running
    // now (started from the prompt or by hand), bound to that exact WAV so a
    // later recording is never stopped by this prompt.
    if let Event::Tracking(d) = event {
        // A meeting resumed during the recording: no stop is pending any more.
        *state.started.lock() = None;
        log::info!("native-meeting-resumed: {}", d.source.kind.id());
        let _ = app.emit_to("main", "native-meeting-resumed", detection_json(d));
        return;
    }
    let recording = match event {
        Event::Detected(_) | Event::Tracking(_) => false,
        Event::Ended(d) => {
            let current = app.state::<RecorderState>().recorder.lock().snapshot().path;
            let running = current.is_some();
            *state.started.lock() = current.map(|path| (d.id.clone(), path));
            running
        }
    };
    let (name, detection, notice) = match event {
        Event::Tracking(_) => return,
        Event::Detected(d) => (
            "native-meeting-detected",
            d,
            Some((
                "회의가 감지됐어요".to_owned(),
                format!(
                    "{} — TTOBAK에서 녹음을 시작할 수 있어요.",
                    d.source.kind.label()
                ),
            )),
        ),
        Event::Ended(d) => (
            "native-meeting-ended",
            d,
            recording.then(|| {
                (
                    "회의가 끝난 것 같아요".to_owned(),
                    format!(
                        "{} — 잠시 후 TTOBAK 녹음을 자동으로 종료하고 업로드합니다.",
                        d.source.kind.label()
                    ),
                )
            }),
        ),
    };
    *state.last.lock() = Some(detection.clone());
    log::info!("{name}: {}", detection.source.kind.id());
    let mut payload = detection_json(detection);
    payload["recording"] = json!(recording);
    if let Err(e) = app.emit_to("main", name, payload) {
        log::warn!("could not deliver {name}: {e}");
    }
    match event {
        // The detected prompt carries a Record button (meeting_prompt.rs).
        // The detected prompt is a floating window with a Record button
        // (meeting_prompt.rs); macOS notifications were not shown reliably.
        Event::Detected(d) => {
            let _ = notice;
            crate::meeting_prompt::offer(app, &d.id, d.source.kind.label());
        }
        _ => {
            crate::meeting_prompt::close(app);
            if let Some((title, body)) = notice {
                let _ = app.notification().builder().title(title).body(body).show();
            }
        }
    }
}

// --- Commands called by the SPA --------------------------------------------

#[tauri::command]
pub fn meeting_detection_get(state: State<'_, Arc<MeetingState>>) -> Value {
    json!({
        "supported": cfg!(target_os = "macos"),
        // This build reports meeting ends for any running recording and
        // resumes (`native-meeting-resumed`), so the SPA may auto-stop.
        "autoStop": cfg!(target_os = "macos"),
        "enabled": state.enabled.load(Ordering::Relaxed),
        "active": state.detector.lock().announced().map(detection_json),
    })
}

#[tauri::command]
pub fn meeting_detection_set_enabled(
    app: AppHandle,
    enabled: bool,
    state: State<'_, Arc<MeetingState>>,
) -> Result<(), String> {
    // Persist first, so a failed write leaves the live setting unchanged.
    let path = state.settings_path.lock().clone();
    if let Some(path) = path {
        if let Some(dir) = path.parent() {
            std::fs::create_dir_all(dir).map_err(|e| e.to_string())?;
        }
        std::fs::write(&path, json!({ "enabled": enabled }).to_string())
            .map_err(|e| e.to_string())?;
    }
    state.enabled.store(enabled, Ordering::Relaxed);
    if !enabled {
        state.detector.lock().reset();
        *state.started.lock() = None;
        // Retire any visible prompt.
        if let Some(last) = state.last.lock().take() {
            let mut payload = detection_json(&last);
            payload["recording"] = json!(false);
            let _ = app.emit_to("main", "native-meeting-ended", payload);
        }
    }
    Ok(())
}

/// Answers a detection prompt: `dismiss`, `start` (with the SPA-formatted
/// `title`) or `stop` (after the meeting ended). Start/stop return the record
/// page's reply in control-protocol shape.
#[tauri::command]
pub async fn meeting_detection_respond(
    app: AppHandle,
    detection_id: String,
    action: String,
    title: Option<String>,
) -> Result<Value, String> {
    log::info!("meeting_detection_respond: {action}");
    let state = app.state::<Arc<MeetingState>>().inner().clone();
    let known = state
        .last
        .lock()
        .as_ref()
        .is_some_and(|d| d.id == detection_id);
    if !known {
        return Err("unknown or expired detection".into());
    }
    let action = match action.as_str() {
        "dismiss" => {
            state.detector.lock().dismiss(&detection_id);
            crate::meeting_prompt::close(&app);
            return Ok(json!({ "ok": true }));
        }
        "start" => {
            if !state.detector.lock().is_active(&detection_id) {
                return Err("the meeting is no longer active".into());
            }
            let title = title.unwrap_or_default();
            if !proto::bounded_text(&title, proto::MAX_TITLE_CHARS) {
                return Err("title must be 1-200 characters on one line".into());
            }
            Action::Start {
                title: title.trim().to_owned(),
                account_id: None,
            }
        }
        "stop" => {
            // The meeting's audio is back (resumed): never stop it.
            let source = state.last.lock().as_ref().map(|d| d.source);
            if source.is_some_and(|s| state.detector.lock().is_present(&s)) {
                return Err("the meeting resumed; the recording continues".into());
            }
            if !owns_recording(&app, &state, &detection_id) {
                return Err("the recording this meeting started is no longer running".into());
            }
            Action::Stop { upload: true }
        }
        _ => return Err("action must be dismiss, start or stop".into()),
    };
    let is_start = matches!(action, Action::Start { .. });
    let reply = control::dispatch_local(&app, &detection_id, action).await;
    if reply.get("ok").and_then(Value::as_bool) == Some(true) {
        // Answered here or in the banner: the floating prompt is done.
        crate::meeting_prompt::close(&app);
    }
    if is_start && reply.get("ok").and_then(Value::as_bool) == Some(true) {
        let path = app.state::<RecorderState>().recorder.lock().snapshot().path;
        *state.started.lock() = path.map(|p| (detection_id.clone(), p));
    }
    Ok(reply)
}

/// Main PIDs of Chromium browser instances capturing or playing audio; the
/// Google Meet mute follower reads only these (Safari's shared WebKit process
/// has no resolvable instance).
#[cfg(target_os = "macos")]
pub use mac::{capturing_browser_pids, other_output_running};

#[cfg(target_os = "macos")]
mod mac {
    use std::collections::{HashMap, HashSet};
    use std::ffi::c_void;
    use std::io::Read;
    use std::process::{Command, Stdio};
    use std::ptr::NonNull;
    use std::time::{Duration, Instant};

    use core_foundation::base::TCFType;
    use core_foundation::string::CFString;
    use objc2_core_audio::{
        kAudioHardwarePropertyProcessObjectList, kAudioObjectPropertyElementMain,
        kAudioObjectPropertyScopeGlobal, kAudioObjectSystemObject, kAudioProcessPropertyBundleID,
        kAudioProcessPropertyIsRunningInput, kAudioProcessPropertyIsRunningOutput,
        kAudioProcessPropertyPID, AudioObjectGetPropertyData, AudioObjectGetPropertyDataSize,
        AudioObjectID, AudioObjectPropertyAddress, AudioObjectPropertySelector,
    };

    use crate::meeting::{
        classify_bundle, classify_meeting_url, is_app_main_executable, Browser, MicUser, Source,
        MEETING_URL_PREFIXES,
    };

    /// Tab lists are refreshed at most this often per browser.
    const TAB_CACHE: Duration = Duration::from_secs(5);
    /// After Automation access is denied, wait this long before asking again.
    const DENIED_BACKOFF: Duration = Duration::from_secs(300);
    /// Long enough to cover the first Automation consent dialog, which blocks
    /// the script; killing it early would dismiss the dialog and re-prompt.
    /// Script result when the browser is not running (no URL starts with it).
    const NOT_RUNNING: &str = "ttobak:not-running";
    const SCRIPT_TIMEOUT: Duration = Duration::from_secs(30);

    #[derive(Default)]
    pub struct Probe {
        /// Keyed by browser and, when known, the PID of the browser instance
        /// whose audio is running (several instances may run at once).
        tabs: HashMap<(Browser, Option<i32>), (Instant, Vec<Source>)>,
        /// Browsers whose tab script has succeeded in this run. Output-only
        /// audio (e.g. a video) checks tabs only for these, so it never
        /// raises the first Automation prompt; microphone use does.
        granted: HashSet<Browser>,
        audio_active: bool,
    }

    impl Probe {
        pub fn audio_active(&self) -> bool {
            self.audio_active
        }

        /// Meeting sources currently capturing or playing audio.
        ///
        /// Each process's `IsRunningInput` is read directly: a meeting app can
        /// capture from a non-default or virtual input device, which leaves
        /// the default device's `IsRunningSomewhere` false (observed with Zoom).
        /// Output counts too, so a listen-only participant whose app or
        /// browser has released the microphone is still detected.
        pub fn sources(&mut self) -> Vec<Source> {
            let own_pid = std::process::id() as i32;
            let mut sources = Vec::new();
            self.audio_active = false;
            for process in process_objects() {
                let input = get_u32(process, kAudioProcessPropertyIsRunningInput) == Some(1);
                let output = get_u32(process, kAudioProcessPropertyIsRunningOutput) == Some(1);
                if !input && !output {
                    continue;
                }
                self.audio_active = true;
                if get_i32(process, kAudioProcessPropertyPID) == Some(own_pid) {
                    continue;
                }
                let pid = get_i32(process, kAudioProcessPropertyPID);
                let Some(bundle) = bundle_id(process) else {
                    continue;
                };
                match classify_bundle(&bundle) {
                    Some(MicUser::Native(kind)) => sources.push(Source {
                        kind,
                        browser: None,
                    }),
                    Some(MicUser::Browser(browser)) if input || self.granted.contains(&browser) => {
                        // Safari's shared WebKit process has no Safari ancestor.
                        let root = match browser {
                            Browser::Safari => None,
                            _ => pid.and_then(browser_root_pid),
                        };
                        sources.extend(self.browser_meetings(browser, root))
                    }
                    Some(MicUser::Browser(_)) => {}
                    None => {}
                }
            }
            sources.sort_by_key(|s| s.key());
            sources.dedup();
            sources
        }

        fn browser_meetings(&mut self, browser: Browser, pid: Option<i32>) -> Vec<Source> {
            if let Some((at, cached)) = self.tabs.get(&(browser, pid)) {
                if at.elapsed() < TAB_CACHE {
                    return cached.clone();
                }
            }
            let (found, backoff) = match meeting_tab_urls(browser, pid) {
                Ok(urls) => {
                    self.granted.insert(browser);
                    (
                        urls.iter()
                            .filter_map(|u| classify_meeting_url(u))
                            .map(|kind| Source {
                                kind,
                                browser: Some(browser),
                            })
                            .collect(),
                        false,
                    )
                }
                Err(denied) => (Vec::new(), denied),
            };
            // A denial is cached long enough not to re-prompt every second.
            let at = if backoff {
                Instant::now() + DENIED_BACKOFF - TAB_CACHE
            } else {
                Instant::now()
            };
            self.tabs.insert((browser, pid), (at, found.clone()));
            found
        }
    }

    /// Tab URLs of the browser instance `pid`, filtered to meeting URLs, or
    /// of the browser by bundle ID when the instance is unknown or cannot be
    /// addressed. `Err(true)` means Automation access was denied.
    fn meeting_tab_urls(browser: Browser, pid: Option<i32>) -> Result<Vec<String>, bool> {
        if let Some(pid) = pid {
            match instance_tab_urls(pid) {
                Err(false) => {}
                result => return result,
            }
        }
        bundle_tab_urls(browser)
    }

    /// JavaScript string literal list of the meeting URL prefixes.
    fn prefix_list() -> String {
        MEETING_URL_PREFIXES
            .iter()
            .map(|p| format!("\"{p}\""))
            .collect::<Vec<_>>()
            .join(", ")
    }

    /// Reads the tabs of exactly the instance `pid` through Scripting
    /// Bridge. `tell application id` reaches only one instance when several
    /// run (e.g. an automation-launched Chrome beside the user's), which can
    /// hide the meeting tab. The PID is passed as an argument, never
    /// interpolated, and only meeting URLs leave the child process.
    fn instance_tab_urls(pid: i32) -> Result<Vec<String>, bool> {
        let script = format!(
            "ObjC.import('ScriptingBridge');\n\
             var failure = 0;\n\
             ObjC.registerSubclass({{ name: 'TtobakTabProbe', methods: {{\n\
             'eventDidFail:withError:': {{ types: ['id', ['id', 'id']],\n\
             implementation: function (e, error) {{ failure = error.code; return $(); }} }} }} }});\n\
             function run(argv) {{\n\
             var prefixes = [{prefixes}];\n\
             var app = $.SBApplication.applicationWithProcessIdentifier(Number(argv[0]));\n\
             if (!app || app.isNil()) return '{NOT_RUNNING}';\n\
             app.delegate = $.TtobakTabProbe.alloc.init;\n\
             var nested;\n\
             try {{ nested = ObjC.deepUnwrap(app.valueForKey('windows').valueForKeyPath('tabs.URL')) || []; }}\n\
             catch (x) {{ if (failure) throw new Error('automation ' + failure); throw x; }}\n\
             if (failure) throw new Error('automation ' + failure);\n\
             var out = '';\n\
             nested.forEach(function (w) {{ [].concat(w).forEach(function (u) {{\n\
             u = String(u);\n\
             if (prefixes.some(function (p) {{ return u.indexOf(p) === 0; }})) out += u + '\\n';\n\
             }}); }});\n\
             return out;\n\
             }}",
            prefixes = prefix_list(),
        );
        let pid = pid.to_string();
        run_tab_script(
            &["-l", "JavaScript", "-e", &script, &pid],
            &format!("pid {pid}"),
        )
    }

    /// Reads the tabs of the browser by bundle ID.
    fn bundle_tab_urls(browser: Browser) -> Result<Vec<String>, bool> {
        let id = browser.app_bundle_id();
        let filter = MEETING_URL_PREFIXES
            .iter()
            .map(|p| format!("(u starts with \"{p}\")"))
            .collect::<Vec<_>>()
            .join(" or ");
        let script = format!(
            "if application id \"{id}\" is not running then return \"{NOT_RUNNING}\"\n\
             tell application id \"{id}\" to set urlLists to URL of every tab of every window\n\
             set out to \"\"\n\
             repeat with l in urlLists\n\
             repeat with v in l\n\
             set u to v as text\n\
             if {filter} then set out to out & u & linefeed\n\
             end repeat\n\
             end repeat\n\
             return out"
        );
        run_tab_script(&["-e", &script], id)
    }

    /// Runs `osascript` with `args`; `target` names the browser in logs.
    fn run_tab_script(args: &[&str], target: &str) -> Result<Vec<String>, bool> {
        let mut child = Command::new("/usr/bin/osascript")
            .args(args)
            .stdin(Stdio::null())
            .stdout(Stdio::piped())
            .stderr(Stdio::piped())
            .spawn()
            .map_err(|_| false)?;
        let deadline = Instant::now() + SCRIPT_TIMEOUT;
        let status = loop {
            match child.try_wait() {
                Ok(Some(status)) => break status,
                Ok(None) if Instant::now() < deadline => {
                    std::thread::sleep(Duration::from_millis(50))
                }
                _ => {
                    let _ = child.kill();
                    let _ = child.wait();
                    return Err(false);
                }
            }
        };
        let mut out = String::new();
        let mut err = String::new();
        if let Some(mut s) = child.stdout.take() {
            let _ = s.read_to_string(&mut out);
        }
        if let Some(mut s) = child.stderr.take() {
            let _ = s.read_to_string(&mut err);
        }
        // Not running proves nothing about Automation access.
        if out.trim() == NOT_RUNNING {
            return Err(false);
        }
        if !status.success() {
            // -1743: the user has not allowed TTOBAK to control this browser.
            let denied = err.contains("-1743");
            if denied {
                log::info!("automation access to {target} is not granted");
            }
            return Err(denied);
        }
        Ok(out
            .lines()
            .map(str::trim)
            .filter(|l| !l.is_empty())
            .map(str::to_owned)
            .collect())
    }

    /// Whether a process other than this app is playing audio.
    pub fn other_output_running() -> bool {
        let own_pid = std::process::id() as i32;
        process_objects().into_iter().any(|p| {
            get_u32(p, kAudioProcessPropertyIsRunningOutput) == Some(1)
                && get_i32(p, kAudioProcessPropertyPID) != Some(own_pid)
        })
    }

    pub fn capturing_browser_pids() -> Vec<i32> {
        let mut pids = Vec::new();
        for process in process_objects() {
            let input = get_u32(process, kAudioProcessPropertyIsRunningInput) == Some(1);
            let output = get_u32(process, kAudioProcessPropertyIsRunningOutput) == Some(1);
            if !input && !output {
                continue;
            }
            let browser = bundle_id(process).and_then(|b| match classify_bundle(&b) {
                Some(MicUser::Browser(b)) if b != Browser::Safari => Some(b),
                _ => None,
            });
            if browser.is_none() {
                continue;
            }
            if let Some(root) =
                get_i32(process, kAudioProcessPropertyPID).and_then(browser_root_pid)
            {
                if !pids.contains(&root) {
                    pids.push(root);
                }
            }
        }
        pids
    }

    extern "C" {
        fn proc_pidinfo(pid: i32, flavor: i32, arg: u64, buffer: *mut c_void, size: i32) -> i32;
        fn proc_pidpath(pid: i32, buffer: *mut c_void, size: u32) -> i32;
    }

    /// `proc_bsdinfo`'s `pbi_ppid` follows four `u32` fields.
    const PROC_PIDTBSDINFO: i32 = 3;
    const PBI_PPID_OFFSET: usize = 16;

    fn parent_pid(pid: i32) -> Option<i32> {
        // Larger than `proc_bsdinfo` (136 bytes); 8-byte aligned.
        let mut buf = [0u64; 32];
        let size = std::mem::size_of_val(&buf) as i32;
        let n = unsafe { proc_pidinfo(pid, PROC_PIDTBSDINFO, 0, buf.as_mut_ptr().cast(), size) };
        if (n as usize) < PBI_PPID_OFFSET + 4 {
            return None;
        }
        let bytes = unsafe {
            std::slice::from_raw_parts(buf.as_ptr().cast::<u8>(), std::mem::size_of_val(&buf))
        };
        let ppid = u32::from_ne_bytes(
            bytes[PBI_PPID_OFFSET..PBI_PPID_OFFSET + 4]
                .try_into()
                .ok()?,
        );
        Some(ppid as i32)
    }

    fn executable_path(pid: i32) -> Option<String> {
        let mut buf = vec![0u8; 4096];
        let n = unsafe { proc_pidpath(pid, buf.as_mut_ptr().cast(), buf.len() as u32) };
        if n <= 0 {
            return None;
        }
        buf.truncate(n as usize);
        String::from_utf8(buf).ok()
    }

    /// The browser instance owning `pid`: the nearest process, starting at
    /// `pid`, that is an application's main executable.
    fn browser_root_pid(pid: i32) -> Option<i32> {
        let mut current = pid;
        for _ in 0..8 {
            if is_app_main_executable(&executable_path(current)?) {
                return Some(current);
            }
            current = parent_pid(current)?;
            if current <= 1 {
                return None;
            }
        }
        None
    }

    fn address(selector: AudioObjectPropertySelector) -> AudioObjectPropertyAddress {
        AudioObjectPropertyAddress {
            mSelector: selector,
            mScope: kAudioObjectPropertyScopeGlobal,
            mElement: kAudioObjectPropertyElementMain,
        }
    }

    fn get<T: Copy>(object: AudioObjectID, selector: AudioObjectPropertySelector) -> Option<T> {
        let addr = address(selector);
        let mut value = std::mem::MaybeUninit::<T>::uninit();
        let mut size = std::mem::size_of::<T>() as u32;
        let status = unsafe {
            AudioObjectGetPropertyData(
                object,
                NonNull::from(&addr),
                0,
                std::ptr::null(),
                NonNull::from(&mut size),
                NonNull::new(value.as_mut_ptr().cast::<c_void>())?,
            )
        };
        (status == 0 && size as usize == std::mem::size_of::<T>())
            .then(|| unsafe { value.assume_init() })
    }

    fn get_u32(object: AudioObjectID, selector: AudioObjectPropertySelector) -> Option<u32> {
        get::<u32>(object, selector)
    }

    fn get_i32(object: AudioObjectID, selector: AudioObjectPropertySelector) -> Option<i32> {
        get::<i32>(object, selector)
    }

    fn process_objects() -> Vec<AudioObjectID> {
        let system = kAudioObjectSystemObject as AudioObjectID;
        let addr = address(kAudioHardwarePropertyProcessObjectList);
        let mut size = 0u32;
        let status = unsafe {
            AudioObjectGetPropertyDataSize(
                system,
                NonNull::from(&addr),
                0,
                std::ptr::null(),
                NonNull::from(&mut size),
            )
        };
        let count = size as usize / std::mem::size_of::<AudioObjectID>();
        if status != 0 || count == 0 {
            return Vec::new();
        }
        let mut ids = vec![0 as AudioObjectID; count];
        let mut size = (count * std::mem::size_of::<AudioObjectID>()) as u32;
        let status = unsafe {
            AudioObjectGetPropertyData(
                system,
                NonNull::from(&addr),
                0,
                std::ptr::null(),
                NonNull::from(&mut size),
                NonNull::new(ids.as_mut_ptr().cast::<c_void>()).expect("vec pointer"),
            )
        };
        if status != 0 {
            return Vec::new();
        }
        // The list can shrink between the two calls.
        ids.truncate(size as usize / std::mem::size_of::<AudioObjectID>());
        ids
    }

    fn bundle_id(process: AudioObjectID) -> Option<String> {
        let raw = get::<*const c_void>(process, kAudioProcessPropertyBundleID)?;
        if raw.is_null() {
            return None;
        }
        // The HAL returns a +1 retained CFString.
        let id = unsafe {
            CFString::wrap_under_create_rule(raw as core_foundation::string::CFStringRef)
        };
        Some(id.to_string()).filter(|s| !s.is_empty())
    }
}
