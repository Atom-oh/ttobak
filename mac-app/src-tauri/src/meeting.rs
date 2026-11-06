//! Meeting detection rules (ADR-061): which microphone users count as a
//! meeting, and when to announce a detected or ended meeting.
//!
//! Pure logic only, so it is unit tested off macOS. `meeting_detect.rs` feeds
//! it Core Audio observations. Detection only offers a recording: the user
//! confirms the start in the app, which then follows the normal control path.

use std::collections::{HashMap, HashSet};

/// A microphone user must stay active this long before it is announced, so
/// a short voice memo or permission probe does not raise a prompt.
pub const SUSTAIN_MS: u64 = 3_000;
/// An announced meeting ends after its microphone use stays absent this long;
/// muting in Zoom or Meet keeps the input running, so gaps are brief.
pub const END_GRACE_MS: u64 = 20_000;

#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub enum MeetingKind {
    Zoom,
    Teams,
    Webex,
    Slack,
    FaceTime,
    Chime,
    Discord,
    GoogleMeet,
}

impl MeetingKind {
    pub fn id(self) -> &'static str {
        match self {
            MeetingKind::Zoom => "zoom",
            MeetingKind::Teams => "teams",
            MeetingKind::Webex => "webex",
            MeetingKind::Slack => "slack",
            MeetingKind::FaceTime => "facetime",
            MeetingKind::Chime => "chime",
            MeetingKind::Discord => "discord",
            MeetingKind::GoogleMeet => "google-meet",
        }
    }

    pub fn label(self) -> &'static str {
        match self {
            MeetingKind::Zoom => "Zoom",
            MeetingKind::Teams => "Microsoft Teams",
            MeetingKind::Webex => "Webex",
            MeetingKind::Slack => "Slack 허들",
            MeetingKind::FaceTime => "FaceTime",
            MeetingKind::Chime => "Amazon Chime",
            MeetingKind::Discord => "Discord",
            MeetingKind::GoogleMeet => "Google Meet",
        }
    }
}

/// Native meeting apps, matched by bundle ID or one of its helper IDs
/// (`<id>.<suffix>`).
const NATIVE_APPS: &[(&str, MeetingKind)] = &[
    ("us.zoom.xos", MeetingKind::Zoom),
    // Zoom can capture audio in a separate host process.
    ("us.zoom.caphost", MeetingKind::Zoom),
    ("us.zoom.CptHost", MeetingKind::Zoom),
    ("com.microsoft.teams2", MeetingKind::Teams),
    ("com.microsoft.teams", MeetingKind::Teams),
    ("com.cisco.webexmeetingsapp", MeetingKind::Webex),
    ("Cisco-Systems.Spark", MeetingKind::Webex),
    ("com.tinyspeck.slackmacgap", MeetingKind::Slack),
    ("com.apple.FaceTime", MeetingKind::FaceTime),
    ("com.amazon.Amazon-Chime", MeetingKind::Chime),
    ("com.hnc.Discord", MeetingKind::Discord),
];

#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub enum Browser {
    Chrome,
    ChromeCanary,
    Arc,
    Edge,
    Brave,
    Vivaldi,
    Safari,
}

impl Browser {
    /// The browser application whose tabs are inspected.
    pub fn app_bundle_id(self) -> &'static str {
        match self {
            Browser::Chrome => "com.google.Chrome",
            Browser::ChromeCanary => "com.google.Chrome.canary",
            Browser::Arc => "company.thebrowser.Browser",
            Browser::Edge => "com.microsoft.edgemac",
            Browser::Brave => "com.brave.Browser",
            Browser::Vivaldi => "com.vivaldi.Vivaldi",
            Browser::Safari => "com.apple.Safari",
        }
    }
}

const BROWSERS: &[Browser] = &[
    // Canary first: its ID has Chrome's as a prefix.
    Browser::ChromeCanary,
    Browser::Chrome,
    Browser::Arc,
    Browser::Edge,
    Browser::Brave,
    Browser::Vivaldi,
    Browser::Safari,
];

/// Safari captures microphones in a shared WebKit process, not under its own
/// bundle ID. Other WebKit hosts (including this app's WebView) share it, so
/// it counts only when Safari itself has a meeting tab open.
const WEBKIT_GPU: &str = "com.apple.WebKit.GPU";

/// What a process using the microphone is.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum MicUser {
    Native(MeetingKind),
    Browser(Browser),
}

impl Browser {
    pub fn label(self) -> &'static str {
        match self {
            Browser::Chrome => "Chrome",
            Browser::ChromeCanary => "Chrome Canary",
            Browser::Arc => "Arc",
            Browser::Edge => "Edge",
            Browser::Brave => "Brave",
            Browser::Vivaldi => "Vivaldi",
            Browser::Safari => "Safari",
        }
    }
}

impl MicUser {
    /// Display name of the app using an input device.
    pub fn label(self) -> &'static str {
        match self {
            MicUser::Native(kind) => kind.label(),
            MicUser::Browser(browser) => browser.label(),
        }
    }
}

fn matches_id(bundle_id: &str, id: &str) -> bool {
    bundle_id == id
        || bundle_id
            .strip_prefix(id)
            .is_some_and(|rest| rest.starts_with('.'))
}

/// Classifies a Core Audio process bundle ID; `None` is not a meeting app.
pub fn classify_bundle(bundle_id: &str) -> Option<MicUser> {
    if bundle_id == WEBKIT_GPU {
        return Some(MicUser::Browser(Browser::Safari));
    }
    if let Some((_, kind)) = NATIVE_APPS.iter().find(|(id, _)| matches_id(bundle_id, id)) {
        return Some(MicUser::Native(*kind));
    }
    BROWSERS
        .iter()
        .find(|b| matches_id(bundle_id, b.app_bundle_id()))
        .map(|b| MicUser::Browser(*b))
}

/// URL prefixes the browser script may return; anything else stays inside
/// the browser. Kept in one place so the script filter and the Rust check
/// cannot drift.
pub const MEETING_URL_PREFIXES: &[&str] = &[
    "https://meet.google.com/",
    "https://teams.microsoft.com/",
    "https://teams.live.com/",
    "https://app.zoom.us/wc/",
    "https://zoom.us/wc/",
];

fn is_meet_code(path: &str) -> bool {
    // Google Meet room codes look like `abc-defg-hij`.
    let code = path.split(['/', '?', '#']).next().unwrap_or_default();
    let parts: Vec<&str> = code.split('-').collect();
    parts.len() == 3
        && [3, 4, 3]
            .iter()
            .zip(&parts)
            .all(|(len, p)| p.len() == *len && p.bytes().all(|b| b.is_ascii_lowercase()))
}

/// Classifies a browser tab URL as an in-progress web meeting.
pub fn classify_meeting_url(url: &str) -> Option<MeetingKind> {
    if let Some(path) = url.strip_prefix("https://meet.google.com/") {
        return is_meet_code(path).then_some(MeetingKind::GoogleMeet);
    }
    if url.starts_with("https://teams.microsoft.com/") || url.starts_with("https://teams.live.com/")
    {
        return Some(MeetingKind::Teams);
    }
    if url.starts_with("https://app.zoom.us/wc/") || url.starts_with("https://zoom.us/wc/") {
        return Some(MeetingKind::Zoom);
    }
    None
}

/// One meeting source currently using the microphone.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub struct Source {
    pub kind: MeetingKind,
    /// The browser hosting a web meeting, if any.
    pub browser: Option<Browser>,
}

impl Source {
    pub fn key(self) -> String {
        match self.browser {
            Some(b) => format!("{}@{}", self.kind.id(), b.app_bundle_id()),
            None => self.kind.id().to_owned(),
        }
    }
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Detection {
    pub id: String,
    pub source: Source,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Event {
    Detected(Detection),
    Ended(Detection),
}

/// Debounces observations into detected/ended announcements.
///
/// At most one meeting is tracked at a time. A meeting that starts while the
/// app is already recording is tracked silently: it is announced only if the
/// recording stops while the meeting continues, and its end is still reported
/// so the running recording can be stopped. A dismissed meeting stays quiet
/// until its audio has been absent for `END_GRACE_MS`.
#[derive(Default)]
pub struct Detector {
    counter: u64,
    first_seen: HashMap<String, u64>,
    last_seen: HashMap<String, u64>,
    suppressed: HashSet<String>,
    active: Option<Detection>,
    /// The active meeting has not been announced (found while recording).
    silent: bool,
}

impl Detector {
    #[cfg(test)]
    pub fn active(&self) -> Option<&Detection> {
        self.active.as_ref()
    }

    pub fn observe(&mut self, now_ms: u64, sources: &[Source], recording: bool) -> Vec<Event> {
        let mut events = Vec::new();
        let present: HashSet<String> = sources.iter().map(|s| s.key()).collect();
        for key in &present {
            self.first_seen.entry(key.clone()).or_insert(now_ms);
            self.last_seen.insert(key.clone(), now_ms);
        }
        // Forget sources gone past the grace period.
        let expired: Vec<String> = self
            .last_seen
            .iter()
            .filter(|(k, last)| {
                !present.contains(*k) && now_ms.saturating_sub(**last) >= END_GRACE_MS
            })
            .map(|(k, _)| k.clone())
            .collect();
        for key in &expired {
            self.first_seen.remove(key);
            self.last_seen.remove(key);
            self.suppressed.remove(key);
        }
        // A brief absence restarts the sustain window, not the grace period:
        // the window starts again at the next observation that sees it.
        self.first_seen.retain(|key, _| present.contains(key));

        if let Some(active) = &self.active {
            if expired.contains(&active.source.key()) {
                events.push(Event::Ended(active.clone()));
                self.active = None;
                self.silent = false;
            } else if self.silent && !recording {
                // The recording stopped while the meeting goes on: offer it.
                self.silent = false;
                events.push(Event::Detected(active.clone()));
            }
            return events;
        }
        let ready = sources.iter().find(|s| {
            let key = s.key();
            !self.suppressed.contains(&key)
                && self
                    .first_seen
                    .get(&key)
                    .is_some_and(|first| now_ms.saturating_sub(*first) >= SUSTAIN_MS)
        });
        if let Some(source) = ready {
            self.counter += 1;
            let detection = Detection {
                id: format!("meeting-{}", self.counter),
                source: *source,
            };
            self.active = Some(detection.clone());
            self.silent = recording;
            if !recording {
                events.push(Event::Detected(detection));
            }
        }
        events
    }

    /// The user declined to record: forget the prompt and keep that meeting
    /// quiet until it ends. Returns false for a stale ID.
    pub fn dismiss(&mut self, id: &str) -> bool {
        match &self.active {
            Some(d) if d.id == id => {
                self.suppressed.insert(d.source.key());
                self.active = None;
                self.silent = false;
                true
            }
            _ => false,
        }
    }

    /// Whether `id` names the announced meeting.
    pub fn is_active(&self, id: &str) -> bool {
        !self.silent && self.active.as_ref().is_some_and(|d| d.id == id)
    }

    /// The announced meeting, if any (a silently tracked one is not offered).
    pub fn announced(&self) -> Option<&Detection> {
        self.active.as_ref().filter(|_| !self.silent)
    }

    /// Clears any announcement, e.g. when detection is turned off.
    pub fn reset(&mut self) {
        *self = Detector {
            counter: self.counter,
            ..Detector::default()
        };
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    const ZOOM: Source = Source {
        kind: MeetingKind::Zoom,
        browser: None,
    };
    const MEET: Source = Source {
        kind: MeetingKind::GoogleMeet,
        browser: Some(Browser::Chrome),
    };

    #[test]
    fn classifies_native_apps_and_their_helpers() {
        assert_eq!(
            classify_bundle("us.zoom.xos"),
            Some(MicUser::Native(MeetingKind::Zoom))
        );
        assert_eq!(
            classify_bundle("us.zoom.xos.helper"),
            Some(MicUser::Native(MeetingKind::Zoom))
        );
        assert_eq!(
            classify_bundle("com.microsoft.teams2"),
            Some(MicUser::Native(MeetingKind::Teams))
        );
        // A shared prefix that is not a helper does not match.
        assert_eq!(classify_bundle("us.zoom.xosother"), None);
        assert_eq!(classify_bundle("com.apple.VoiceMemos"), None);
    }

    #[test]
    fn classifies_browsers_including_helpers_and_webkit() {
        assert_eq!(
            classify_bundle("com.google.Chrome.helper"),
            Some(MicUser::Browser(Browser::Chrome))
        );
        assert_eq!(
            classify_bundle("com.google.Chrome.canary.helper"),
            Some(MicUser::Browser(Browser::ChromeCanary))
        );
        assert_eq!(
            classify_bundle("com.apple.WebKit.GPU"),
            Some(MicUser::Browser(Browser::Safari))
        );
        assert_eq!(classify_bundle("org.mozilla.firefox"), None);
    }

    #[test]
    fn recognizes_meeting_urls_only() {
        assert_eq!(
            classify_meeting_url("https://meet.google.com/abc-defg-hij"),
            Some(MeetingKind::GoogleMeet)
        );
        assert_eq!(
            classify_meeting_url("https://meet.google.com/abc-defg-hij?authuser=1"),
            Some(MeetingKind::GoogleMeet)
        );
        // The Meet landing page and other paths are not meetings.
        assert_eq!(classify_meeting_url("https://meet.google.com/"), None);
        assert_eq!(
            classify_meeting_url("https://meet.google.com/landing"),
            None
        );
        assert_eq!(
            classify_meeting_url("https://meet.google.com/ABC-defg-hij"),
            None
        );
        assert_eq!(
            classify_meeting_url("https://teams.microsoft.com/v2/"),
            Some(MeetingKind::Teams)
        );
        assert_eq!(
            classify_meeting_url("https://app.zoom.us/wc/123/join"),
            Some(MeetingKind::Zoom)
        );
        assert_eq!(
            classify_meeting_url("https://www.youtube.com/watch?v=x"),
            None
        );
        assert_eq!(
            classify_meeting_url("http://meet.google.com/abc-defg-hij"),
            None
        );
    }

    #[test]
    fn every_recognized_url_passes_the_script_filter() {
        for url in [
            "https://meet.google.com/abc-defg-hij",
            "https://teams.microsoft.com/v2/",
            "https://teams.live.com/meet/1",
            "https://app.zoom.us/wc/1/join",
            "https://zoom.us/wc/1/join",
        ] {
            assert!(classify_meeting_url(url).is_some(), "{url}");
            assert!(
                MEETING_URL_PREFIXES.iter().any(|p| url.starts_with(p)),
                "{url}"
            );
        }
    }

    #[test]
    fn announces_only_after_sustained_use() {
        let mut d = Detector::default();
        assert!(d.observe(0, &[ZOOM], false).is_empty());
        assert!(d.observe(2_000, &[ZOOM], false).is_empty());
        let events = d.observe(3_000, &[ZOOM], false);
        assert!(matches!(&events[..], [Event::Detected(det)] if det.source == ZOOM));
        // Announced once.
        assert!(d.observe(5_000, &[ZOOM], false).is_empty());
    }

    #[test]
    fn a_gap_restarts_the_sustain_window() {
        let mut d = Detector::default();
        d.observe(0, &[ZOOM], false);
        d.observe(2_000, &[], false);
        assert!(d.observe(4_000, &[ZOOM], false).is_empty());
        // Sustained since 4s, not since the absent observation at 2s.
        assert!(d.observe(6_000, &[ZOOM], false).is_empty());
        assert!(matches!(
            &d.observe(7_000, &[ZOOM], false)[..],
            [Event::Detected(_)]
        ));
    }

    #[test]
    fn stays_quiet_while_recording() {
        let mut d = Detector::default();
        d.observe(0, &[ZOOM], true);
        assert!(d.observe(10_000, &[ZOOM], true).is_empty());
        // Once the recording stops, the still-running meeting is offered.
        assert!(matches!(
            &d.observe(11_000, &[ZOOM], false)[..],
            [Event::Detected(_)]
        ));
    }

    #[test]
    fn a_meeting_during_a_manual_recording_reports_its_end() {
        let mut d = Detector::default();
        d.observe(0, &[ZOOM], true);
        // Tracked silently: no prompt while recording.
        assert!(d.observe(5_000, &[ZOOM], true).is_empty());
        assert!(d.announced().is_none());
        d.observe(10_000, &[], true);
        let events = d.observe(30_000, &[], true);
        assert!(matches!(&events[..], [Event::Ended(det)] if det.source == ZOOM));
    }

    #[test]
    fn ends_after_the_grace_period_even_while_recording() {
        let mut d = Detector::default();
        d.observe(0, &[MEET], false);
        let Event::Detected(det) = d.observe(3_000, &[MEET], false).remove(0) else {
            panic!("expected detection");
        };
        // Brief mute-like gaps do not end it.
        assert!(d.observe(10_000, &[], true).is_empty());
        assert!(d.observe(15_000, &[MEET], true).is_empty());
        assert!(d.observe(30_000, &[], true).is_empty());
        let events = d.observe(35_000, &[], true);
        assert_eq!(events, vec![Event::Ended(det)]);
        assert!(d.active().is_none());
    }

    #[test]
    fn dismissal_suppresses_until_the_meeting_ends() {
        let mut d = Detector::default();
        d.observe(0, &[ZOOM], false);
        let Event::Detected(det) = d.observe(3_000, &[ZOOM], false).remove(0) else {
            panic!("expected detection");
        };
        assert!(!d.dismiss("meeting-0"));
        assert!(d.dismiss(&det.id));
        assert!(d.observe(60_000, &[ZOOM], false).is_empty());
        // Gone past the grace period, then a new meeting starts.
        d.observe(70_000, &[], false);
        d.observe(90_000, &[], false);
        d.observe(91_000, &[ZOOM], false);
        let events = d.observe(94_000, &[ZOOM], false);
        assert!(matches!(&events[..], [Event::Detected(next)] if next.id != det.id));
    }

    #[test]
    fn reset_clears_the_announcement_but_keeps_ids_unique() {
        let mut d = Detector::default();
        d.observe(0, &[ZOOM], false);
        d.observe(3_000, &[ZOOM], false);
        d.reset();
        assert!(d.active().is_none());
        d.observe(4_000, &[ZOOM], false);
        let events = d.observe(7_000, &[ZOOM], false);
        assert!(matches!(&events[..], [Event::Detected(det)] if det.id == "meeting-2"));
    }
}
