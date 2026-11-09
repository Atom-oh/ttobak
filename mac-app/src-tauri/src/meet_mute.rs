//! Follow Google Meet's microphone mute in a Chromium browser (ADR-061).
//!
//! Meet keeps the microphone open while muted, so Core Audio cannot tell.
//! The state comes from the page's own toggle through the Accessibility API:
//! Meet labels it "Turn on microphone" while the user is muted and "Turn off
//! microphone" while unmuted. Only the followed call's page showing Meet's
//! post-call screen ("Rejoin", "Return to home screen") counts as left; a page
//! without the toggle is otherwise unknown and keeps the last state. Pages of
//! other calls (the landing page or an earlier call) are not evidence. Once a
//! second call has shown a toggle (in the same read or later), no page is
//! evidence the user left. Only the
//! web area of a `meet.google.com` page in a browser instance using audio is
//! read. An unreadable page (e.g. another tab in front) keeps the last state.
//! Leaving the followed call, read for 3 s, reopens the microphone, as with
//! Zoom; a wrong verdict then records the user rather than dropping their
//! voice. A recording that never saw a toggle records the microphone. Rules
//! are pure and unit tested.

use crate::zoom_mute::{MicState, Silence};

/// Labels shown while muted (the button turns the microphone on).
const MUTED_LABELS: &[&str] = &["turn on microphone", "마이크 켜기"];
/// Labels shown while unmuted (the button turns the microphone off).
const UNMUTED_LABELS: &[&str] = &["turn off microphone", "마이크 끄기"];
/// Labels on Meet's post-call screen: a positive sign the user left. The Korean forms are
/// unverified; a missed label only falls back to the audio grace.
const LEFT_LABELS: &[&str] = &[
    "rejoin",
    "return to home screen",
    "다시 참여",
    "다시 참여하기",
    "홈 화면으로 돌아가기",
];

/// Lowercases, collapses spaces and drops a trailing shortcut hint such as
/// "(⌘ + d)".
pub fn normalize(label: &str) -> String {
    let mut s = label.trim();
    if s.ends_with(')') {
        if let Some(i) = s.rfind('(') {
            s = s[..i].trim_end();
        }
    }
    s.split_whitespace()
        .collect::<Vec<_>>()
        .join(" ")
        .to_lowercase()
}

/// Classifies the button labels of one readable Meet page. Without the toggle,
/// only the post-call screen's labels count as leaving; anything else is unknown.
pub fn classify<'a>(labels: impl IntoIterator<Item = &'a str>) -> MicState {
    let (mut muted, mut unmuted, mut left) = (false, false, false);
    for label in labels {
        let l = normalize(label);
        muted |= MUTED_LABELS.contains(&l.as_str());
        unmuted |= UNMUTED_LABELS.contains(&l.as_str());
        left |= LEFT_LABELS.contains(&l.as_str());
    }
    match (muted, unmuted) {
        (true, false) => MicState::Muted,
        (false, true) => MicState::Unmuted,
        // Both at once is not a single toggle: record the microphone.
        (true, true) => MicState::Unknown,
        (false, false) if left => MicState::NoMeeting,
        // A toolbar hidden in the call (e.g. behind a dialog) is not a left call.
        (false, false) => MicState::Unknown,
    }
}

/// Classifies one Meet page read. An incomplete read (a failed call, or the
/// node budget or depth bound reached) is unknown: a missing toggle is then
/// neither unmuted nor a left call, so the last state is kept.
pub fn classify_read<'a>(labels: impl IntoIterator<Item = &'a str>, complete: bool) -> MicState {
    if complete {
        classify(labels)
    } else {
        MicState::Unknown
    }
}

/// A Meet page's identity: lowercased, without query, fragment or trailing
/// slash. Kept in memory only; never logged.
pub fn call_key(url: &str) -> String {
    let end = url.find(['?', '#']).unwrap_or(url.len());
    let s = &url[..end];
    s.strip_suffix('/').unwrap_or(s).to_lowercase()
}

/// One Meet page read, or a browser or search that could not be read.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum PageRead {
    Page { key: String, state: MicState },
    Unread,
}

/// The Meet call this caller last saw with a toggle. Only that call's
/// page, read again without a toggle, is evidence the user left. Once a
/// second call has shown a toggle, no page is evidence the user left.
#[derive(Debug, Default)]
pub struct Call {
    last: Option<String>,
    /// Distinct calls seen with a toggle; past two the exact count is moot.
    calls: usize,
    seen: Vec<String>,
    /// Calls read on more than one page at once (the same room in two
    /// windows): leaving on one page does not show the call was left.
    shared: Vec<String>,
}

/// Seen call keys kept per `Call`.
const MAX_SEEN_CALLS: usize = 8;

impl Call {
    fn see(&mut self, key: &str) {
        if self.seen.iter().any(|k| k == key) {
            return;
        }
        if self.seen.len() < MAX_SEEN_CALLS {
            self.seen.push(key.to_owned());
        }
        // Counted even past the cap: only "more than one" matters.
        self.calls = self.calls.saturating_add(1);
    }

    /// Combines every Meet page read: an unmuted page wins, then a muted one.
    /// A page that could not be read may still hold the call. No page at all
    /// is unknown.
    pub fn judge(&mut self, pages: impl IntoIterator<Item = PageRead>) -> MicState {
        let pages: Vec<PageRead> = pages.into_iter().collect();
        for (i, p) in pages.iter().enumerate() {
            let PageRead::Page { key, .. } = p else {
                continue;
            };
            let again = pages[i + 1..]
                .iter()
                .any(|q| matches!(q, PageRead::Page { key: k, .. } if k == key));
            if again && !self.shared.contains(key) && self.shared.len() < MAX_SEEN_CALLS {
                self.shared.push(key.clone());
            }
        }
        // Every page showing a toggle is a call, even one that does not win:
        // two calls seen in one read disable left evidence like two in turn.
        for p in &pages {
            if let PageRead::Page { key, state } = p {
                if matches!(state, MicState::Muted | MicState::Unmuted) {
                    self.see(key);
                }
            }
        }
        for want in [MicState::Unmuted, MicState::Muted] {
            let key = pages.iter().find_map(|p| match p {
                PageRead::Page { key, state } if *state == want => Some(key.clone()),
                _ => None,
            });
            if let Some(key) = key {
                self.last = Some(key);
                return want;
            }
        }
        let unknown = pages.iter().any(|p| match p {
            PageRead::Page { state, .. } => *state == MicState::Unknown,
            PageRead::Unread => true,
        });
        if unknown {
            return MicState::Unknown;
        }
        if self.calls > 1 {
            // With more than one call seen, a left page may not be the tracked call: fall back
            // to the audio grace.
            return MicState::Unknown;
        }
        let left = pages.iter().any(|p| match p {
            PageRead::Page { key, state } => {
                *state == MicState::NoMeeting
                    && self.last.as_ref() == Some(key)
                    && !self.shared.contains(key)
            }
            PageRead::Unread => false,
        });
        if left {
            MicState::NoMeeting
        } else {
            // Toggle-less pages of other calls are not evidence.
            MicState::Unknown
        }
    }
}

/// Turns Meet states into the microphone gate. The followed call's left page
/// must hold for `MEET_LEFT_CONFIRM_MS` before the microphone reopens.
#[derive(Debug, Default)]
pub struct Hold {
    silence: Silence,
    followed: bool,
    left: crate::meeting::LeftCall,
}

impl Hold {
    pub fn observe(&mut self, now_ms: u64, state: MicState) -> Silence {
        match state {
            MicState::Muted => {
                self.silence = Silence::Muted;
                self.followed = true;
                self.left.observe(now_ms, false);
            }
            MicState::Unmuted => {
                self.silence = Silence::Open;
                self.followed = true;
                self.left.observe(now_ms, false);
            }
            MicState::NoMeeting if self.followed => {
                if self.left.observe(now_ms, true) {
                    self.silence = Silence::Open;
                    self.followed = false;
                }
            }
            // An unreadable read between two left reads restarts the window.
            MicState::NoMeeting | MicState::Unknown => {
                self.left.observe(now_ms, false);
            }
        }
        self.silence
    }
}

#[cfg(target_os = "macos")]
pub use mac::current_state;

#[cfg(target_os = "macos")]
mod mac {
    use core_foundation::base::CFType;
    use core_foundation::string::CFString;
    use core_foundation::url::CFURL;

    use super::{call_key, classify_read, Call, PageRead};
    use crate::zoom_mute::ax::{children_checked, copy_attr_checked, create_app};
    use crate::zoom_mute::MicState;

    const MEET_PREFIX: &str = "https://meet.google.com/";
    /// Elements visited per browser; Meet's page tree is far smaller.
    const MAX_NODES: usize = 6_000;
    const MAX_DEPTH: usize = 80;

    /// A string attribute, `Ok(None)` when absent, `Err` when the read failed.
    fn string_attr(e: &CFType, name: &str) -> Result<Option<String>, ()> {
        Ok(copy_attr_checked(e, name)?
            .and_then(|v| v.downcast::<CFString>())
            .map(|s| s.to_string()))
    }

    /// The URL when `e` shows a Meet page, `Err` when its URL could not be
    /// read. Never logged.
    fn is_meet_page(e: &CFType) -> Result<Option<String>, ()> {
        Ok(copy_attr_checked(e, "AXURL")?
            .and_then(|v| v.downcast::<CFURL>())
            .map(|u| u.get_string().to_string())
            .filter(|u| u.starts_with(MEET_PREFIX)))
    }

    /// Collects button labels under `e`; never logged. Returns false when the
    /// read was incomplete.
    fn button_labels(e: &CFType, depth: usize, budget: &mut usize, out: &mut Vec<String>) -> bool {
        if *budget == 0 || depth > MAX_DEPTH {
            return false;
        }
        *budget -= 1;
        let Ok(role) = string_attr(e, "AXRole") else {
            return false;
        };
        if role.as_deref() == Some("AXButton") {
            let Ok(description) = string_attr(e, "AXDescription") else {
                return false;
            };
            out.extend(description);
            return true;
        }
        let Some(kids) = children_checked(e) else {
            return false;
        };
        let mut complete = true;
        for c in kids {
            complete &= button_labels(&c, depth + 1, budget, out);
        }
        complete
    }

    /// Finds Meet web areas under `e` and classifies each. Returns false when
    /// the search itself was cut short (a Meet page may have been missed).
    fn meet_pages(e: &CFType, depth: usize, budget: &mut usize, out: &mut Vec<PageRead>) -> bool {
        if *budget == 0 || depth > MAX_DEPTH {
            return false;
        }
        *budget -= 1;
        let Ok(role) = string_attr(e, "AXRole") else {
            return false;
        };
        if role.as_deref() == Some("AXWebArea") {
            let Ok(meet) = is_meet_page(e) else {
                return false;
            };
            if let Some(url) = meet {
                let mut labels = Vec::new();
                let complete = button_labels(e, depth, budget, &mut labels);
                out.push(PageRead::Page {
                    key: call_key(&url),
                    state: classify_read(labels.iter().map(String::as_str), complete),
                });
            }
            // Nested frames are not Meet's toolbar.
            return true;
        }
        let Some(kids) = children_checked(e) else {
            return false;
        };
        let mut complete = true;
        for c in kids {
            complete &= meet_pages(&c, depth + 1, budget, out);
        }
        complete
    }

    /// Meet's state across the given browser instances, judged against the
    /// call `call` last saw with a toggle.
    pub fn current_state(pids: &[i32], call: &mut Call) -> MicState {
        let mut pages = Vec::new();
        for &pid in pids {
            let Some(app) = create_app(pid) else {
                continue;
            };
            let Ok(Some(windows)) = copy_attr_checked(&app, "AXWindows") else {
                // An unreadable browser may hold the call: neither unmuted nor left.
                pages.push(PageRead::Unread);
                continue;
            };
            let mut budget = MAX_NODES;
            for w in crate::zoom_mute::ax::as_array(windows) {
                if !meet_pages(&w, 0, &mut budget, &mut pages) {
                    // A page may have been missed: neither unmuted nor left.
                    pages.push(PageRead::Unread);
                }
            }
        }
        call.judge(pages)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_toggle_label_gives_the_state() {
        assert_eq!(
            classify(["마이크 켜기", "손들기(ctrl + ⌘ + h)"]),
            MicState::Muted
        );
        assert_eq!(classify(["Turn off microphone (⌘ + d)"]), MicState::Unmuted);
        assert_eq!(classify(["  Turn ON   microphone "]), MicState::Muted);
    }

    #[test]
    fn a_page_without_the_toggle_has_left_the_call() {
        assert_eq!(classify(["", "Rejoin"]), MicState::NoMeeting);
        // A remote participant action is not the local toggle.
        assert_eq!(classify(["Mute Alice's microphone"]), MicState::Unknown);
        assert_eq!(classify(["마이크 켜기", "마이크 끄기"]), MicState::Unknown);
    }

    #[test]
    fn a_hidden_toolbar_is_not_a_left_call() {
        assert_eq!(classify(["Chat with everyone", "Close"]), MicState::Unknown);
        assert_eq!(classify(["Return to home screen"]), MicState::NoMeeting);
        assert_eq!(
            classify(["돌아가기", "홈 화면으로 돌아가기"]),
            MicState::NoMeeting
        );
    }

    fn page(key: &str, state: MicState) -> PageRead {
        PageRead::Page {
            key: key.to_owned(),
            state,
        }
    }

    #[test]
    fn an_unmuted_page_wins_over_a_muted_one() {
        let mut c = Call::default();
        assert_eq!(
            c.judge([page("a", MicState::Muted), page("b", MicState::Unmuted)]),
            MicState::Unmuted
        );
        assert_eq!(
            c.judge([page("a", MicState::Muted), page("b", MicState::NoMeeting)]),
            MicState::Muted
        );
        // Two calls have shown a toggle, so no page is evidence of leaving.
        assert_eq!(c.judge([page("a", MicState::NoMeeting)]), MicState::Unknown);
        assert_eq!(c.judge([]), MicState::Unknown);
    }

    #[test]
    fn a_page_that_never_showed_a_toggle_is_not_evidence() {
        // An unlisted language or a companion page.
        let mut c = Call::default();
        assert_eq!(c.judge([page("a", MicState::NoMeeting)]), MicState::Unknown);
    }

    #[test]
    fn the_followed_call_without_a_toggle_has_left() {
        let mut c = Call::default();
        assert_eq!(c.judge([page("a", MicState::Unmuted)]), MicState::Unmuted);
        assert_eq!(
            c.judge([page("a", MicState::NoMeeting)]),
            MicState::NoMeeting
        );
    }

    #[test]
    fn a_stale_page_of_an_earlier_call_does_not_end_the_current_one() {
        let mut c = Call::default();
        assert_eq!(c.judge([page("a", MicState::Unmuted)]), MicState::Unmuted);
        assert_eq!(c.judge([page("b", MicState::Muted)]), MicState::Muted);
        // B's tab is unreadable (pushes nothing); A's old page has no toggle.
        assert_eq!(c.judge([page("a", MicState::NoMeeting)]), MicState::Unknown);
    }

    #[test]
    fn two_calls_in_one_read_both_count() {
        // A and B both unmuted in one read; B's tab then goes to the background
        // (no read) while A shows its post-call screen. B may still be live.
        let mut c = Call::default();
        assert_eq!(
            c.judge([page("a", MicState::Unmuted), page("b", MicState::Unmuted)]),
            MicState::Unmuted
        );
        assert_eq!(c.judge([page("a", MicState::NoMeeting)]), MicState::Unknown);

        // Likewise when the second call is muted and does not win.
        let mut c = Call::default();
        assert_eq!(
            c.judge([page("a", MicState::Unmuted), page("b", MicState::Muted)]),
            MicState::Unmuted
        );
        assert_eq!(c.judge([page("a", MicState::NoMeeting)]), MicState::Unknown);
        assert_eq!(c.judge([page("b", MicState::NoMeeting)]), MicState::Unknown);
    }

    #[test]
    fn two_calls_in_one_read_keep_a_retained_mute() {
        use crate::meeting::MEET_LEFT_CONFIRM_MS;

        // Muted in both calls; A's departure page must not stop following B.
        let mut h = Hold::default();
        let mut c = Call::default();
        let both = [page("a", MicState::Muted), page("b", MicState::Muted)];
        assert_eq!(h.observe(0, c.judge(both)), Silence::Muted);
        for t in [0, MEET_LEFT_CONFIRM_MS, 2 * MEET_LEFT_CONFIRM_MS] {
            assert_eq!(
                h.observe(t + 300, c.judge([page("a", MicState::NoMeeting)])),
                Silence::Muted
            );
        }
    }

    #[test]
    fn leaving_a_call_open_in_two_windows_is_ambiguous() {
        // The same room in two windows; the user leaves in one while the other
        // is a background tab that yields no read.
        let mut c = Call::default();
        assert_eq!(
            c.judge([page("a", MicState::Muted), page("a", MicState::Muted)]),
            MicState::Muted
        );
        assert_eq!(c.judge([page("a", MicState::NoMeeting)]), MicState::Unknown);

        // Seen twice in one read even when only one page showed the toggle.
        let mut c = Call::default();
        assert_eq!(
            c.judge([page("a", MicState::Unmuted), page("a", MicState::Unknown)]),
            MicState::Unmuted
        );
        assert_eq!(c.judge([page("a", MicState::NoMeeting)]), MicState::Unknown);

        // A call only ever read on one page still ends.
        let mut c = Call::default();
        assert_eq!(c.judge([page("a", MicState::Muted)]), MicState::Muted);
        assert_eq!(
            c.judge([page("a", MicState::NoMeeting)]),
            MicState::NoMeeting
        );
    }

    #[test]
    fn a_repeated_call_is_counted_once() {
        let mut c = Call::default();
        assert_eq!(c.judge([page("a", MicState::Unmuted)]), MicState::Unmuted);
        assert_eq!(c.judge([page("a", MicState::Muted)]), MicState::Muted);
        assert_eq!(
            c.judge([page("a", MicState::NoMeeting)]),
            MicState::NoMeeting
        );
    }

    #[test]
    fn a_second_call_disables_left_evidence() {
        let mut c = Call::default();
        assert_eq!(c.judge([page("a", MicState::Unmuted)]), MicState::Unmuted);
        assert_eq!(c.judge([page("b", MicState::Muted)]), MicState::Muted);
        assert_eq!(c.judge([page("b", MicState::NoMeeting)]), MicState::Unknown);
        assert_eq!(c.judge([page("a", MicState::NoMeeting)]), MicState::Unknown);
    }

    #[test]
    fn an_unread_entry_keeps_the_followed_call_open() {
        let mut c = Call::default();
        assert_eq!(c.judge([page("a", MicState::Muted)]), MicState::Muted);
        assert_eq!(
            c.judge([page("a", MicState::NoMeeting), PageRead::Unread]),
            MicState::Unknown
        );
    }

    #[test]
    fn the_call_key_drops_query_fragment_and_trailing_slash() {
        assert_eq!(
            call_key("https://meet.google.com/ABC-defg-HIJ/?authuser=0#x"),
            "https://meet.google.com/abc-defg-hij"
        );
        assert_eq!(
            call_key("https://meet.google.com/abc-defg-hij#frag"),
            "https://meet.google.com/abc-defg-hij"
        );
        assert_eq!(
            call_key("https://meet.google.com/abc-defg-hij"),
            "https://meet.google.com/abc-defg-hij"
        );
    }

    #[test]
    fn leaving_a_followed_call_reopens_the_microphone() {
        use crate::meeting::MEET_LEFT_CONFIRM_MS;

        let mut h = Hold::default();
        assert_eq!(h.observe(0, MicState::Muted), Silence::Muted);
        assert_eq!(h.observe(0, MicState::NoMeeting), Silence::Muted);
        assert_eq!(
            h.observe(MEET_LEFT_CONFIRM_MS - 1, MicState::NoMeeting),
            Silence::Muted
        );
        assert_eq!(
            h.observe(MEET_LEFT_CONFIRM_MS, MicState::NoMeeting),
            Silence::Open
        );
        // Rejoining restores following.
        assert_eq!(
            h.observe(MEET_LEFT_CONFIRM_MS + 300, MicState::Muted),
            Silence::Muted
        );
        assert_eq!(
            h.observe(MEET_LEFT_CONFIRM_MS + 600, MicState::Unmuted),
            Silence::Open
        );
    }

    #[test]
    fn a_reloading_page_does_not_end_the_call() {
        let mut h = Hold::default();
        assert_eq!(h.observe(0, MicState::Muted), Silence::Muted);
        assert_eq!(h.observe(100, MicState::NoMeeting), Silence::Muted);
        assert_eq!(h.observe(1_000, MicState::Unknown), Silence::Muted);
        assert_eq!(h.observe(3_200, MicState::NoMeeting), Silence::Muted);
        assert_eq!(h.observe(3_500, MicState::Unmuted), Silence::Open);
    }

    #[test]
    fn a_recording_that_never_saw_the_toggle_keeps_the_microphone() {
        let mut h = Hold::default();
        assert_eq!(h.observe(0, MicState::NoMeeting), Silence::Open);
        assert_eq!(h.observe(300, MicState::Unknown), Silence::Open);
    }

    #[test]
    fn an_incomplete_read_is_neither_unmuted_nor_a_left_call() {
        assert_eq!(classify_read(Vec::<&str>::new(), false), MicState::Unknown);
        assert_eq!(classify_read(["Leave call"], false), MicState::Unknown);
        assert_eq!(classify_read(Vec::<&str>::new(), true), MicState::Unknown);
        assert_eq!(classify_read(["마이크 끄기"], true), MicState::Unmuted);
        let mut c = Call::default();
        assert_eq!(c.judge([page("a", MicState::Unmuted)]), MicState::Unmuted);
        assert_eq!(
            c.judge([page("a", MicState::NoMeeting), page("b", MicState::Unknown)]),
            MicState::Unknown
        );
    }

    #[test]
    fn an_unreadable_page_keeps_a_retained_mute() {
        let mut h = Hold::default();
        let mut c = Call::default();
        assert_eq!(
            h.observe(0, c.judge([page("a", MicState::Muted)])),
            Silence::Muted
        );
        assert_eq!(
            h.observe(
                300,
                c.judge([page("a", classify_read(Vec::<&str>::new(), false))])
            ),
            Silence::Muted
        );
    }
}
