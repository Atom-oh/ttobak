//! Follow Zoom's own microphone mute while recording (ADR-061).
//!
//! The recording opens the microphone directly, so without this a user muted
//! in Zoom is still recorded. Like Notion's meeting notes, the state comes
//! from Zoom's UI through the Accessibility API: Zoom's menu bar offers
//! "Mute Audio" while the user is unmuted and "Unmute Audio" while muted.
//! Labels are read from Zoom's own localization files, so any Zoom language
//! works. Only an explicit state changes the gate; a brief unreadable poll
//! keeps the last state. Once a recording has followed Zoom, a long unknown
//! stretch (meeting left, Zoom quit) keeps the microphone silent: Zoom no
//! longer hears the user, so neither does the recording. A recording that
//! never saw a Zoom state records the microphone as before. Rules are pure
//! and unit tested.

use std::collections::HashSet;

/// The microphone state shown by Zoom.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum MicState {
    Muted,
    Unmuted,
    Unknown,
    /// Zoom confirms there is no meeting: it quit, or its meeting window and
    /// audio menu items are both gone.
    NoMeeting,
}

#[derive(Clone, Copy)]
pub struct ProcessSources {
    pub process_pid: fn(&str) -> Result<Option<i32>, ()>,
    pub browser_pids: fn() -> Vec<i32>,
}

pub fn classify_window_ids<'a>(identifiers: impl IntoIterator<Item = Option<&'a str>>) -> MicState {
    for identifier in identifiers {
        if identifier.is_none() || identifier == Some("zm.meeting.window.main") {
            return MicState::Unknown;
        }
    }
    MicState::NoMeeting
}

/// After this long without an explicit state the followed meeting is no longer
/// followed and the microphone reopens.
pub const UNKNOWN_RESET_MS: u64 = 3_000;

/// Normalized menu titles: "Mute Audio" (shown while unmuted) and
/// "Unmute Audio" (shown while muted), in every Zoom language.
#[derive(Clone, Debug, Default)]
pub struct Labels {
    mute: HashSet<String>,
    unmute: HashSet<String>,
}

pub fn normalize(s: &str) -> String {
    s.trim()
        .trim_end_matches(['…', '.'])
        .split_whitespace()
        .collect::<Vec<_>>()
        .join(" ")
        .to_lowercase()
}

impl Labels {
    /// Builds label sets; a title in both sets is dropped as ambiguous.
    pub fn new<'a>(
        mute: impl IntoIterator<Item = &'a str>,
        unmute: impl IntoIterator<Item = &'a str>,
    ) -> Self {
        let mut mute: HashSet<String> = mute.into_iter().map(normalize).collect();
        let mut unmute: HashSet<String> = unmute.into_iter().map(normalize).collect();
        mute.remove("");
        unmute.remove("");
        let both: Vec<String> = mute.intersection(&unmute).cloned().collect();
        for t in both {
            mute.remove(&t);
            unmute.remove(&t);
        }
        Self { mute, unmute }
    }

    pub fn is_empty(&self) -> bool {
        self.mute.is_empty() || self.unmute.is_empty()
    }
}

/// Classifies the titles of Zoom's menu items.
pub fn classify<'a>(titles: impl IntoIterator<Item = &'a str>, labels: &Labels) -> MicState {
    let (mut offers_mute, mut offers_unmute) = (false, false);
    for title in titles {
        let t = normalize(title);
        offers_mute |= labels.mute.contains(&t);
        offers_unmute |= labels.unmute.contains(&t);
    }
    match (offers_mute, offers_unmute) {
        (true, false) => MicState::Unmuted,
        (false, true) => MicState::Muted,
        _ => MicState::Unknown,
    }
}

/// Whether, and why, the microphone is silenced.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub enum Silence {
    #[default]
    Open,
    /// The followed meeting shows the user muted.
    Muted,
}

/// Turns observed states into the microphone gate.
#[derive(Debug, Default)]
pub struct Gate {
    silence: Silence,
    followed: bool,
    unknown_since: Option<u64>,
}

impl Gate {
    pub fn observe(&mut self, now_ms: u64, state: MicState) -> Silence {
        match state {
            MicState::Muted | MicState::Unmuted => {
                self.silence = if state == MicState::Muted {
                    Silence::Muted
                } else {
                    Silence::Open
                };
                self.followed = true;
                self.unknown_since = None;
            }
            // An ended meeting reopens the microphone: a wrong end then
            // records the user rather than dropping their voice.
            MicState::NoMeeting => self.reopen(),
            MicState::Unknown => {
                let since = *self.unknown_since.get_or_insert(now_ms);
                if self.followed && now_ms.saturating_sub(since) >= UNKNOWN_RESET_MS {
                    self.reopen();
                }
            }
        }
        self.silence
    }

    fn reopen(&mut self) {
        self.silence = Silence::Open;
        self.followed = false;
        self.unknown_since = None;
    }
}

pub fn observe_meet(
    zoom: Silence,
    now_ms: u64,
    trusted: bool,
    muted: &std::sync::atomic::AtomicBool,
    hold: &mut crate::meet_mute::Hold,
    inspect: impl FnOnce() -> MicState,
) -> Silence {
    if zoom != Silence::Open {
        muted.store(true, std::sync::atomic::Ordering::Relaxed);
    }
    hold.observe(
        now_ms,
        if trusted {
            inspect()
        } else {
            MicState::Unknown
        },
    )
}

/// Outcome of one Accessibility attribute read.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum AxRead {
    Value,
    /// The element has no such attribute or value (e.g. a leaf's children).
    Missing,
    /// The call failed (timed out, element gone, ...): the read is incomplete.
    Failed,
}

/// `kAXErrorAttributeUnsupported` and `kAXErrorNoValue`.
pub const AX_ATTRIBUTE_UNSUPPORTED: i32 = -25205;
pub const AX_NO_VALUE: i32 = -25212;

/// Maps an `AXUIElementCopyAttributeValue` status to a read outcome.
pub fn ax_read(status: i32, has_value: bool) -> AxRead {
    match status {
        0 if has_value => AxRead::Value,
        0 | AX_ATTRIBUTE_UNSUPPORTED | AX_NO_VALUE => AxRead::Missing,
        _ => AxRead::Failed,
    }
}

#[cfg(target_os = "macos")]
pub use mac::{watch, Watch};

/// Accessibility helpers shared with `meet_mute`.
#[cfg(target_os = "macos")]
pub(crate) mod ax {
    pub(crate) use super::mac::{as_array, children_checked, copy_attr_checked, create_app};
}

#[cfg(target_os = "macos")]
mod mac {
    use std::ffi::c_void;
    use std::path::{Path, PathBuf};
    use std::sync::atomic::{AtomicBool, Ordering};
    use std::sync::{Arc, Once, OnceLock};
    use std::time::{Duration, Instant};

    use core_foundation::array::{CFArray, CFArrayRef};
    use core_foundation::base::{CFType, CFTypeRef, TCFType};
    use core_foundation::boolean::CFBoolean;
    use core_foundation::data::CFData;
    use core_foundation::dictionary::{CFDictionary, CFDictionaryRef};
    use core_foundation::propertylist::{create_with_data, CFPropertyList};
    use core_foundation::string::{CFString, CFStringRef};
    use tauri::{AppHandle, Emitter};

    use super::{
        classify, classify_window_ids, observe_meet, Gate, Labels, MicState, ProcessSources,
        Silence,
    };

    const ZOOM_BUNDLE: &str = "us.zoom.xos";
    const POLL: Duration = Duration::from_millis(300);
    /// Zoom's PID is re-resolved this often (it may start or quit mid-recording).
    const PID_REFRESH: Duration = Duration::from_secs(3);
    /// Bound on each Accessibility call so a busy Zoom never stalls the poll.
    const AX_TIMEOUT_SECONDS: f32 = 0.25;
    /// Menu items scanned per poll; Zoom's whole menu bar is far smaller.
    const MAX_ITEMS: usize = 600;

    type AXUIElementRef = CFTypeRef;

    #[link(name = "ApplicationServices", kind = "framework")]
    extern "C" {
        fn AXUIElementCreateApplication(pid: i32) -> AXUIElementRef;
        fn AXUIElementCopyAttributeValue(
            element: AXUIElementRef,
            attribute: CFStringRef,
            value: *mut CFTypeRef,
        ) -> i32;
        fn AXUIElementSetMessagingTimeout(element: AXUIElementRef, seconds: f32) -> i32;
        fn AXIsProcessTrusted() -> bool;
        fn AXIsProcessTrustedWithOptions(options: CFDictionaryRef) -> bool;
        static kAXTrustedCheckOptionPrompt: CFStringRef;
    }

    /// Asks macOS for Accessibility access once per app launch.
    fn request_trust() -> bool {
        static PROMPT: Once = Once::new();
        let mut trusted = unsafe { AXIsProcessTrusted() };
        PROMPT.call_once(|| {
            if !trusted {
                let key = unsafe { CFString::wrap_under_get_rule(kAXTrustedCheckOptionPrompt) };
                let options = CFDictionary::from_CFType_pairs(&[(key, CFBoolean::true_value())]);
                trusted = unsafe { AXIsProcessTrustedWithOptions(options.as_concrete_TypeRef()) };
            }
        });
        trusted
    }

    pub(crate) fn copy_attr(element: &CFType, name: &str) -> Option<CFType> {
        let name = CFString::new(name);
        let mut value: CFTypeRef = std::ptr::null();
        let status = unsafe {
            AXUIElementCopyAttributeValue(
                element.as_CFTypeRef(),
                name.as_concrete_TypeRef(),
                &mut value,
            )
        };
        (status == 0 && !value.is_null()).then(|| unsafe { CFType::wrap_under_create_rule(value) })
    }

    /// An attribute's value, `Ok(None)` when the element has none, or `Err`
    /// when the read failed, so callers can tell an incomplete read apart.
    pub(crate) fn copy_attr_checked(element: &CFType, name: &str) -> Result<Option<CFType>, ()> {
        let name = CFString::new(name);
        let mut value: CFTypeRef = std::ptr::null();
        let status = unsafe {
            AXUIElementCopyAttributeValue(
                element.as_CFTypeRef(),
                name.as_concrete_TypeRef(),
                &mut value,
            )
        };
        match super::ax_read(status, !value.is_null()) {
            super::AxRead::Value => Ok(Some(unsafe { CFType::wrap_under_create_rule(value) })),
            super::AxRead::Missing => Ok(None),
            super::AxRead::Failed => Err(()),
        }
    }

    /// An element's children, or `None` when reading them failed.
    pub(crate) fn children_checked(element: &CFType) -> Option<Vec<CFType>> {
        match copy_attr_checked(element, "AXChildren") {
            Ok(Some(value)) => Some(as_array(value)),
            Ok(None) => Some(Vec::new()),
            Err(()) => None,
        }
    }

    pub(crate) fn as_array(value: CFType) -> Vec<CFType> {
        if value.type_of() != CFArray::<CFType>::type_id() {
            return Vec::new();
        }
        let array: CFArray<CFType> =
            unsafe { CFArray::wrap_under_get_rule(value.as_CFTypeRef() as CFArrayRef) };
        array.iter().map(|c| c.clone()).collect()
    }

    /// The application's Accessibility element, with a bounded call timeout.
    pub(crate) fn create_app(pid: i32) -> Option<CFType> {
        let app = unsafe { AXUIElementCreateApplication(pid) };
        if app.is_null() {
            return None;
        }
        let app = unsafe { CFType::wrap_under_create_rule(app) };
        unsafe { AXUIElementSetMessagingTimeout(app.as_CFTypeRef(), AX_TIMEOUT_SECONDS) };
        Some(app)
    }

    /// What Zoom's UI shows, or `None` when it cannot be read.
    fn zoom_ui(pid: i32, labels: &Labels) -> Option<MicState> {
        let app = create_app(pid)?;
        let titles = menu_titles(&app)?;
        let mic = classify(titles.iter().map(String::as_str), labels);
        if mic != MicState::Unknown {
            return Some(mic);
        }
        let windows = copy_attr(&app, "AXWindows")?;
        if windows.type_of() != CFArray::<CFType>::type_id() {
            return None;
        }
        let identifiers: Vec<Option<String>> = as_array(windows)
            .iter()
            .map(|window| {
                copy_attr(window, "AXIdentifier")
                    .and_then(|value| value.downcast::<CFString>())
                    .map(|identifier| identifier.to_string())
            })
            .collect();
        Some(classify_window_ids(
            identifiers.iter().map(Option::as_deref),
        ))
    }

    /// Titles of every item in Zoom's menus, or `None` when unreadable.
    fn menu_titles(app: &CFType) -> Option<Vec<String>> {
        let bar = copy_attr(app, "AXMenuBar")?;
        let mut titles = Vec::new();
        // A failed read is unreadable, not an empty menu bar (which would
        // confirm the meeting's end).
        for top in children_checked(&bar)? {
            for menu in children_checked(&top)? {
                for item in children_checked(&menu)? {
                    if titles.len() >= MAX_ITEMS {
                        return Some(titles);
                    }
                    if let Some(t) =
                        copy_attr(&item, "AXTitle").and_then(|v| v.downcast::<CFString>())
                    {
                        titles.push(t.to_string());
                    }
                }
            }
        }
        Some(titles)
    }

    fn zoom_app_paths() -> Vec<PathBuf> {
        let mut paths = vec![PathBuf::from("/Applications/zoom.us.app")];
        if let Some(home) = std::env::var_os("HOME") {
            paths.push(Path::new(&home).join("Applications/zoom.us.app"));
        }
        paths
    }

    /// "Mute Audio"/"Unmute Audio" in every language Zoom ships.
    pub(super) fn load_labels() -> Labels {
        let (mut mute, mut unmute) = (
            vec!["Mute Audio".to_owned()],
            vec!["Unmute Audio".to_owned()],
        );
        for app in zoom_app_paths() {
            let Ok(dirs) = std::fs::read_dir(app.join("Contents/Resources")) else {
                continue;
            };
            for dir in dirs.flatten() {
                let path = dir.path().join("Localizable.strings");
                let Ok(bytes) = std::fs::read(&path) else {
                    continue;
                };
                let data = CFData::from_buffer(&bytes);
                let Ok((plist, _)) = create_with_data(data, 0) else {
                    continue;
                };
                let plist = unsafe { CFPropertyList::wrap_under_create_rule(plist) };
                let Some(dict) = plist.downcast_into::<CFDictionary>() else {
                    continue;
                };
                for (key, out) in [("Mute Audio", &mut mute), ("Unmute Audio", &mut unmute)] {
                    let k = CFString::new(key);
                    if let Some(v) = dict.find(k.as_concrete_TypeRef() as *const c_void) {
                        let v = unsafe { CFType::wrap_under_get_rule(*v as CFTypeRef) };
                        if let Some(s) = v.downcast::<CFString>() {
                            out.push(s.to_string());
                        }
                    }
                }
            }
        }
        Labels::new(
            mute.iter().map(String::as_str),
            unmute.iter().map(String::as_str),
        )
    }

    fn labels() -> &'static Labels {
        static LABELS: OnceLock<Labels> = OnceLock::new();
        LABELS.get_or_init(load_labels)
    }

    /// Zoom's state now, with `pid` refreshed when it is missing or stale.
    fn current_state(
        pid: &mut Option<i32>,
        refreshed: &mut Option<Instant>,
        sources: ProcessSources,
    ) -> MicState {
        if !unsafe { AXIsProcessTrusted() } || labels().is_empty() {
            return MicState::Unknown;
        }
        if !refreshed.is_some_and(|checked| checked.elapsed() < PID_REFRESH) {
            match (sources.process_pid)(ZOOM_BUNDLE) {
                Ok(p) => {
                    *pid = p;
                    *refreshed = Some(Instant::now());
                }
                Err(()) => {
                    // A failed list read is not evidence Zoom quit.
                    *refreshed = None;
                    return MicState::Unknown;
                }
            }
        }
        let Some(p) = *pid else {
            return MicState::NoMeeting;
        };
        match zoom_ui(p, labels()) {
            Some(state) => state,
            None => {
                // Zoom may have quit: re-resolve its PID on the next poll.
                *refreshed = None;
                MicState::Unknown
            }
        }
    }

    /// The running watcher; dropping it stops the thread.
    pub struct Watch {
        running: Arc<AtomicBool>,
    }

    impl Drop for Watch {
        fn drop(&mut self) {
            self.running.store(false, Ordering::Relaxed);
        }
    }

    /// Payload of the `native-mic-muted` event.
    #[derive(Clone, serde::Serialize)]
    struct MuteEvent {
        muted: bool,
        app: &'static str,
        /// `muted` while muted.
        #[serde(skip_serializing_if = "Option::is_none")]
        reason: Option<&'static str>,
    }

    /// Starts following Zoom's mute into `muted` for one recording. Returns a
    /// warning when Zoom is running but Accessibility access is missing.
    pub fn watch(
        app: AppHandle,
        muted: Arc<AtomicBool>,
        sources: ProcessSources,
    ) -> (Watch, Option<String>) {
        let running = Arc::new(AtomicBool::new(true));
        let zoom_running = matches!((sources.process_pid)(ZOOM_BUNDLE), Ok(Some(_)));
        let browser_running = !(sources.browser_pids)().is_empty();
        let warning = ((zoom_running || browser_running) && !request_trust()).then(|| {
            "Allow TTOBAK in System Settings > Privacy & Security > Accessibility so the \
             recording follows Zoom's and Google Meet's mute; until then the microphone is \
             always recorded."
                .to_owned()
        });
        let flag = running.clone();
        let spawned = std::thread::Builder::new()
            .name("zoom-mute".into())
            .spawn(move || run(app, muted, flag, sources));
        if let Err(e) = spawned {
            log::warn!("could not start the Zoom mute watcher: {e}");
        }
        (Watch { running }, warning)
    }

    /// Browser instances are re-resolved this often.
    const BROWSER_REFRESH: Duration = Duration::from_secs(2);

    fn run(
        app: AppHandle,
        muted: Arc<AtomicBool>,
        running: Arc<AtomicBool>,
        sources: ProcessSources,
    ) {
        let started = Instant::now();
        let zoom_labels = !labels().is_empty();
        if !zoom_labels {
            log::warn!("Zoom mute labels unavailable; not following Zoom's mute");
        }
        let mut gate = Gate::default();
        let mut meet = crate::meet_mute::Hold::default();
        let mut meet_call = crate::meet_mute::Call::default();
        let mut pid: Option<i32> = None;
        let mut pid_checked: Option<Instant> = None;
        let mut browsers: Vec<i32> = Vec::new();
        let mut browsers_checked: Option<Instant> = None;
        let mut announced: Option<(Silence, &'static str)> = None;
        while running.load(Ordering::Relaxed) {
            let trusted = unsafe { AXIsProcessTrusted() };
            let now_ms = started.elapsed().as_millis() as u64;
            let zoom = if zoom_labels {
                let state = current_state(&mut pid, &mut pid_checked, sources);
                gate.observe(now_ms, state)
            } else {
                Silence::Open
            };
            let meet_silence = observe_meet(zoom, now_ms, trusted, &muted, &mut meet, || {
                if !browsers_checked.is_some_and(|checked| checked.elapsed() < BROWSER_REFRESH) {
                    // Keep the last browsers using audio: a left Meet page is
                    // still read after the browser releases its audio.
                    let fresh = (sources.browser_pids)();
                    if !fresh.is_empty() {
                        browsers = fresh;
                    }
                    browsers_checked = Some(Instant::now());
                }
                crate::meet_mute::current_state(&browsers, &mut meet_call)
            });
            let current = match (zoom, meet_silence) {
                (Silence::Open, Silence::Open) => {
                    // Reopening reports the app that had silenced the microphone.
                    (Silence::Open, announced.map_or("Zoom", |a| a.1))
                }
                (Silence::Open, s) => (s, "Google Meet"),
                (s, _) => (s, "Zoom"),
            };
            muted.store(current.0 != Silence::Open, Ordering::Relaxed);
            if announced.map(|a| a.0) != Some(current.0) {
                if announced.is_some() || current.0 != Silence::Open {
                    log::info!("{} microphone gate: {:?}", current.1, current.0);
                    let reason = match current.0 {
                        Silence::Open => None,
                        Silence::Muted => Some("muted"),
                    };
                    let _ = app.emit(
                        "native-mic-muted",
                        MuteEvent {
                            muted: current.0 != Silence::Open,
                            app: current.1,
                            reason,
                        },
                    );
                }
                announced = Some(current);
            }
            std::thread::sleep(POLL);
        }
        muted.store(false, Ordering::Relaxed);
    }
}

#[cfg(all(test, target_os = "macos"))]
mod mac_tests {
    /// Reads the installed Zoom's localization files when Zoom is present.
    #[test]
    fn loads_labels_from_an_installed_zoom() {
        if !std::path::Path::new("/Applications/zoom.us.app").exists() {
            return;
        }
        let labels = super::mac::load_labels();
        assert!(!labels.is_empty());
        assert_eq!(
            super::classify(["오디오 음소거 해제"], &labels),
            super::MicState::Muted
        );
        assert_eq!(
            super::classify(["Mute audio"], &labels),
            super::MicState::Unmuted
        );
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn labels() -> Labels {
        Labels::new(
            ["Mute Audio", "Mute audio", "오디오 음소거"],
            ["Unmute Audio", "오디오 음소거 해제"],
        )
    }

    #[test]
    fn menu_offering_mute_means_unmuted() {
        let l = labels();
        assert_eq!(
            classify(["Join Audio", "Mute Audio", "Start Video"], &l),
            MicState::Unmuted
        );
        assert_eq!(classify(["오디오 음소거 해제"], &l), MicState::Muted);
    }

    #[test]
    fn missing_or_conflicting_items_are_unknown() {
        let l = labels();
        assert_eq!(classify(["Join Audio"], &l), MicState::Unknown);
        assert_eq!(
            classify(["Mute Audio", "Unmute Audio"], &l),
            MicState::Unknown
        );
        assert_eq!(classify(Vec::<&str>::new(), &l), MicState::Unknown);
    }

    #[test]
    fn titles_are_normalized() {
        let l = labels();
        assert_eq!(classify(["  unmute   AUDIO… "], &l), MicState::Muted);
        // A participant action is not the local menu item.
        assert_eq!(classify(["Ask to unmute"], &l), MicState::Unknown);
    }

    #[test]
    fn a_title_in_both_sets_is_ignored() {
        let l = Labels::new(["Mute"], ["Mute", "Unmute Audio"]);
        assert_eq!(classify(["Mute"], &l), MicState::Unknown);
        assert!(l.is_empty());
    }

    #[test]
    fn gate_follows_explicit_states_and_holds_through_short_gaps() {
        let mut g = Gate::default();
        assert_eq!(g.observe(0, MicState::Muted), Silence::Muted);
        // A brief unreadable poll keeps the state.
        assert_eq!(g.observe(300, MicState::Unknown), Silence::Muted);
        assert_eq!(g.observe(600, MicState::Unmuted), Silence::Open);
        assert_eq!(g.observe(900, MicState::Unknown), Silence::Open);
        assert_eq!(
            g.observe(900 + UNKNOWN_RESET_MS - 1, MicState::Unknown),
            Silence::Open
        );
    }

    #[test]
    fn a_followed_meeting_that_goes_unreadable_reopens_the_microphone() {
        let mut g = Gate::default();
        assert_eq!(g.observe(0, MicState::Muted), Silence::Muted);
        assert_eq!(g.observe(300, MicState::Unknown), Silence::Muted);
        assert_eq!(
            g.observe(300 + UNKNOWN_RESET_MS, MicState::Unknown),
            Silence::Open
        );
        // Rejoining restores following.
        assert_eq!(g.observe(10_000, MicState::Muted), Silence::Muted);
    }

    #[test]
    fn a_confirmed_meeting_end_reopens_the_microphone() {
        let mut g = Gate::default();
        assert_eq!(g.observe(0, MicState::Muted), Silence::Muted);
        assert_eq!(g.observe(300, MicState::NoMeeting), Silence::Open);
        assert_eq!(g.observe(600, MicState::Unknown), Silence::Open);
        let mut fresh = Gate::default();
        assert_eq!(fresh.observe(0, MicState::NoMeeting), Silence::Open);
    }

    #[test]
    fn a_recording_without_zoom_keeps_the_microphone() {
        let mut g = Gate::default();
        assert_eq!(g.observe(0, MicState::Unknown), Silence::Open);
        assert_eq!(g.observe(60_000, MicState::Unknown), Silence::Open);
    }

    #[test]
    fn incomplete_window_identifiers_do_not_confirm_meeting_end() {
        for identifiers in [
            vec![None],
            vec![Some("settings"), None],
            vec![Some("zm.meeting.window.main")],
        ] {
            let observed = classify_window_ids(identifiers);
            assert_eq!(observed, MicState::Unknown);
            let mut gate = Gate::default();
            assert_eq!(gate.observe(0, MicState::Unmuted), Silence::Open);
            assert_eq!(gate.observe(300, observed), Silence::Open);
        }
        assert_eq!(classify_window_ids([]), MicState::NoMeeting);
        assert_eq!(
            classify_window_ids([Some("settings"), Some("chat")]),
            MicState::NoMeeting
        );
    }

    #[test]
    fn zoom_silence_is_applied_before_browser_inspection() {
        use std::sync::atomic::{AtomicBool, Ordering};

        let muted = AtomicBool::new(false);
        let mut hold = crate::meet_mute::Hold::default();
        let meet = observe_meet(Silence::Muted, 0, true, &muted, &mut hold, || {
            assert!(muted.load(Ordering::Relaxed));
            MicState::Unmuted
        });
        assert_eq!(meet, Silence::Open);
        assert!(muted.load(Ordering::Relaxed));
    }

    #[test]
    fn permission_loss_retains_a_previously_observed_meet_mute() {
        use std::sync::atomic::AtomicBool;

        let muted = AtomicBool::new(true);
        let mut hold = crate::meet_mute::Hold::default();
        hold.observe(0, MicState::Muted);
        assert_eq!(
            observe_meet(Silence::Open, 300, false, &muted, &mut hold, || {
                panic!("Untrusted Accessibility inspection must not run")
            }),
            Silence::Muted
        );
        assert_eq!(
            observe_meet(Silence::Open, 600, true, &muted, &mut hold, || {
                MicState::Unmuted
            }),
            Silence::Open
        );
        let mut fresh = crate::meet_mute::Hold::default();
        assert_eq!(
            observe_meet(Silence::Open, 0, false, &muted, &mut fresh, || {
                panic!("Untrusted Accessibility inspection must not run")
            }),
            Silence::Open
        );
    }

    #[test]
    fn accessibility_statuses_separate_failures_from_missing_values() {
        assert_eq!(ax_read(0, true), AxRead::Value);
        assert_eq!(ax_read(0, false), AxRead::Missing);
        assert_eq!(ax_read(AX_ATTRIBUTE_UNSUPPORTED, false), AxRead::Missing);
        assert_eq!(ax_read(AX_NO_VALUE, false), AxRead::Missing);
        // kAXErrorCannotComplete (timeout) and kAXErrorInvalidUIElement.
        assert_eq!(ax_read(-25204, false), AxRead::Failed);
        assert_eq!(ax_read(-25202, false), AxRead::Failed);
    }
}
