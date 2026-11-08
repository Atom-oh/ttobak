//! Follow Google Meet's microphone mute in a Chromium browser (ADR-061).
//!
//! Meet keeps the microphone open while muted, so Core Audio cannot tell.
//! The state comes from the page's own toggle through the Accessibility API:
//! Meet labels it "Turn on microphone" ("마이크 켜기") while the user is muted;
//! a readable page without that label counts as unmuted. Only the web area of a `meet.google.com` page in
//! the browser instances the caller supplies (the runtime passes those
//! using audio) is read. Unlike Zoom, an
//! unreadable page (e.g. another tab in front) keeps the last state: the
//! user is still in the meeting. Rules are pure and unit tested.

use crate::zoom_mute::MicState;

/// Labels shown while muted (the button turns the microphone on).
const MUTED_LABELS: &[&str] = &["turn on microphone", "마이크 켜기"];

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

/// Classifies the button labels of one readable Meet page. Only the
/// muted label silences: any other page (unmuted, or a language not listed)
/// records the microphone as before, so an unrecognized label never drops
/// the user's voice.
pub fn classify<'a>(labels: impl IntoIterator<Item = &'a str>) -> MicState {
    let muted = labels
        .into_iter()
        .any(|l| MUTED_LABELS.contains(&normalize(l).as_str()));
    if muted {
        MicState::Muted
    } else {
        MicState::Unmuted
    }
}

/// Classifies one Meet page read. An incomplete read (a failed call, or the
/// node budget or depth bound reached) is unknown: it keeps the last state
/// instead of reading a missing muted label as unmuted.
pub fn classify_read<'a>(labels: impl IntoIterator<Item = &'a str>, complete: bool) -> MicState {
    if complete {
        classify(labels)
    } else {
        MicState::Unknown
    }
}

/// Combines the states of every readable Meet page: any muted page with no
/// unmuted one means muted.
pub fn combine(states: impl IntoIterator<Item = MicState>) -> MicState {
    let (mut muted, mut unmuted) = (false, false);
    for s in states {
        muted |= s == MicState::Muted;
        unmuted |= s == MicState::Unmuted;
    }
    match (muted, unmuted) {
        (_, true) => MicState::Unmuted,
        (true, false) => MicState::Muted,
        _ => MicState::Unknown,
    }
}

/// Holds Meet's last explicit state: muted until an unmuted page is read.
#[derive(Debug, Default)]
pub struct Hold {
    muted: bool,
}

impl Hold {
    pub fn observe(&mut self, state: MicState) -> bool {
        match state {
            MicState::Muted => self.muted = true,
            MicState::Unmuted => self.muted = false,
            MicState::Unknown | MicState::NoMeeting => {}
        }
        self.muted
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
                // An unreadable browser may hold the call: never read as unmuted.
                states.push(MicState::Unknown);
                continue;
            };
            let mut budget = MAX_NODES;
            for w in crate::zoom_mute::ax::as_array(windows) {
                if !meet_pages(&w, 0, &mut budget, &mut states) {
                    // A page may have been missed: never read that as unmuted.
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
    fn the_muted_label_silences() {
        assert_eq!(
            classify(["마이크 켜기", "손들기(ctrl + ⌘ + h)"]),
            MicState::Muted
        );
        assert_eq!(
            classify(["  Turn ON   microphone (⌘ + d)"]),
            MicState::Muted
        );
    }

    #[test]
    fn any_other_readable_page_records_the_microphone() {
        assert_eq!(classify(["Turn off microphone (⌘ + d)"]), MicState::Unmuted);
        assert_eq!(classify(["Leave call"]), MicState::Unmuted);
        assert_eq!(classify(Vec::<&str>::new()), MicState::Unmuted);
        // A remote participant action is not the local toggle.
        assert_eq!(classify(["Mute Alice's microphone"]), MicState::Unmuted);
    }

    #[test]
    fn an_unmuted_page_wins_over_a_muted_one() {
        assert_eq!(
            combine([MicState::Muted, MicState::Unmuted]),
            MicState::Unmuted
        );
        assert_eq!(
            combine([MicState::Muted, MicState::Unknown]),
            MicState::Muted
        );
        assert_eq!(combine([]), MicState::Unknown);
    }

    #[test]
    fn hold_keeps_the_last_state_through_unreadable_polls() {
        let mut h = Hold::default();
        assert!(!h.observe(MicState::Unknown));
        assert!(h.observe(MicState::Muted));
        assert!(h.observe(MicState::Unknown));
        assert!(!h.observe(MicState::Unmuted));
    }

    #[test]
    fn an_incomplete_read_is_unknown_not_unmuted() {
        assert_eq!(classify_read(Vec::<&str>::new(), false), MicState::Unknown);
        assert_eq!(classify_read(["Leave call"], false), MicState::Unknown);
        assert_eq!(classify_read(["마이크 켜기"], false), MicState::Unknown);
        assert_eq!(classify_read(["Leave call"], true), MicState::Unmuted);
        assert_eq!(classify_read(["마이크 켜기"], true), MicState::Muted);
    }

    #[test]
    fn an_unreadable_page_keeps_a_retained_mute() {
        let mut h = Hold::default();
        assert!(h.observe(MicState::Muted));
        assert!(h.observe(combine([classify_read(Vec::<&str>::new(), false)])));
        assert!(h.observe(combine([MicState::Muted, MicState::Unknown])));
    }
}
