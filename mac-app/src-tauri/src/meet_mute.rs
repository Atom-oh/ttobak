//! Follow Google Meet's microphone mute in a Chromium browser (ADR-061).
//!
//! Meet keeps the microphone open while muted, so Core Audio cannot tell.
//! The state comes from the page's own toggle through the Accessibility API:
//! Meet labels it "Turn on microphone" while the user is muted and "Turn off
//! microphone" while unmuted. A Meet page without either toggle has left the
//! call (or is in a language not listed). Only the web area of a
//! `meet.google.com` page in a browser instance using audio is read. An
//! unreadable page (e.g. another tab in front) keeps the last state. Once a
//! recording has followed Meet, leaving the call keeps the microphone silent,
//! as with Zoom; a recording that never saw a toggle records it as before.
//! Rules are pure and unit tested.

use crate::zoom_mute::{MicState, Silence};

/// Labels shown while muted (the button turns the microphone on).
const MUTED_LABELS: &[&str] = &["turn on microphone", "마이크 켜기"];
/// Labels shown while unmuted (the button turns the microphone off).
const UNMUTED_LABELS: &[&str] = &["turn off microphone", "마이크 끄기"];

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

/// Classifies the button labels of one readable Meet page.
pub fn classify<'a>(labels: impl IntoIterator<Item = &'a str>) -> MicState {
    let (mut muted, mut unmuted) = (false, false);
    for label in labels {
        let l = normalize(label);
        muted |= MUTED_LABELS.contains(&l.as_str());
        unmuted |= UNMUTED_LABELS.contains(&l.as_str());
    }
    match (muted, unmuted) {
        (true, false) => MicState::Muted,
        (false, true) => MicState::Unmuted,
        // Both at once is not a single toggle: record the microphone.
        (true, true) => MicState::Unknown,
        (false, false) => MicState::NoMeeting,
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

/// Combines the states of every readable Meet page: an unmuted page wins,
/// then a muted one; no page at all is unknown.
pub fn combine(states: impl IntoIterator<Item = MicState>) -> MicState {
    let (mut muted, mut unmuted, mut left, mut unknown) = (false, false, false, false);
    for s in states {
        muted |= s == MicState::Muted;
        unmuted |= s == MicState::Unmuted;
        left |= s == MicState::NoMeeting;
        unknown |= s == MicState::Unknown;
    }
    if unmuted {
        MicState::Unmuted
    } else if muted {
        MicState::Muted
    } else if left && !unknown {
        // A page that could not be read may still hold the call.
        MicState::NoMeeting
    } else {
        MicState::Unknown
    }
}

/// Turns Meet states into the microphone gate.
#[derive(Debug, Default)]
pub struct Hold {
    silence: Silence,
    followed: bool,
}

impl Hold {
    pub fn observe(&mut self, state: MicState) -> Silence {
        match state {
            MicState::Muted => {
                self.silence = Silence::Muted;
                self.followed = true;
            }
            MicState::Unmuted => {
                self.silence = Silence::Open;
                self.followed = true;
            }
            MicState::NoMeeting if self.followed => self.silence = Silence::MeetingEnded,
            MicState::NoMeeting | MicState::Unknown => {}
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

    use super::{classify_read, combine};
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

    /// Whether `e` shows a Meet page, `Err` when its URL could not be read.
    fn is_meet_page(e: &CFType) -> Result<bool, ()> {
        Ok(copy_attr_checked(e, "AXURL")?
            .and_then(|v| v.downcast::<CFURL>())
            .is_some_and(|u| u.get_string().to_string().starts_with(MEET_PREFIX)))
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
    fn meet_pages(e: &CFType, depth: usize, budget: &mut usize, out: &mut Vec<MicState>) -> bool {
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
            if meet {
                let mut labels = Vec::new();
                let complete = button_labels(e, depth, budget, &mut labels);
                out.push(classify_read(labels.iter().map(String::as_str), complete));
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

    /// Meet's state across the given browser instances.
    pub fn current_state(pids: &[i32]) -> MicState {
        let mut states = Vec::new();
        for &pid in pids {
            let Some(app) = create_app(pid) else {
                continue;
            };
            let Ok(Some(windows)) = copy_attr_checked(&app, "AXWindows") else {
                // An unreadable browser may hold the call: neither unmuted nor left.
                states.push(MicState::Unknown);
                continue;
            };
            let mut budget = MAX_NODES;
            for w in crate::zoom_mute::ax::as_array(windows) {
                if !meet_pages(&w, 0, &mut budget, &mut states) {
                    // A page may have been missed: neither unmuted nor left.
                    states.push(MicState::Unknown);
                }
            }
        }
        combine(states)
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
        assert_eq!(classify(["Mute Alice's microphone"]), MicState::NoMeeting);
        assert_eq!(classify(["마이크 켜기", "마이크 끄기"]), MicState::Unknown);
    }

    #[test]
    fn an_unmuted_page_wins_over_a_muted_one() {
        assert_eq!(
            combine([MicState::Muted, MicState::Unmuted]),
            MicState::Unmuted
        );
        assert_eq!(
            combine([MicState::Muted, MicState::NoMeeting]),
            MicState::Muted
        );
        assert_eq!(combine([MicState::NoMeeting]), MicState::NoMeeting);
        assert_eq!(combine([]), MicState::Unknown);
    }

    #[test]
    fn leaving_a_followed_call_keeps_the_microphone_silent() {
        let mut h = Hold::default();
        assert_eq!(h.observe(MicState::Unmuted), Silence::Open);
        assert_eq!(h.observe(MicState::Unknown), Silence::Open);
        assert_eq!(h.observe(MicState::NoMeeting), Silence::MeetingEnded);
        // Rejoining restores following.
        assert_eq!(h.observe(MicState::Muted), Silence::Muted);
        assert_eq!(h.observe(MicState::Unmuted), Silence::Open);
    }

    #[test]
    fn a_recording_that_never_saw_the_toggle_keeps_the_microphone() {
        let mut h = Hold::default();
        assert_eq!(h.observe(MicState::NoMeeting), Silence::Open);
        assert_eq!(h.observe(MicState::Unknown), Silence::Open);
    }

    #[test]
    fn an_incomplete_read_is_neither_unmuted_nor_a_left_call() {
        assert_eq!(classify_read(Vec::<&str>::new(), false), MicState::Unknown);
        assert_eq!(classify_read(["Leave call"], false), MicState::Unknown);
        assert_eq!(classify_read(Vec::<&str>::new(), true), MicState::NoMeeting);
        assert_eq!(classify_read(["마이크 끄기"], true), MicState::Unmuted);
        assert_eq!(
            combine([MicState::NoMeeting, MicState::Unknown]),
            MicState::Unknown
        );
    }

    #[test]
    fn an_unreadable_page_keeps_a_retained_mute() {
        let mut h = Hold::default();
        assert_eq!(h.observe(MicState::Muted), Silence::Muted);
        assert_eq!(
            h.observe(combine([classify_read(Vec::<&str>::new(), false)])),
            Silence::Muted
        );
    }
}
