//! Follow Google Meet's microphone mute in a Chromium browser (ADR-061).
//!
//! Meet keeps the microphone open while muted, so Core Audio cannot tell.
//! The state comes from the page's own toggle through the Accessibility API:
//! Meet labels it "Turn on microphone" ("마이크 켜기") while the user is muted;
//! a readable page without that label counts as unmuted. Only the web area of a `meet.google.com` page in
//! a browser instance that is capturing audio is read. Unlike Zoom, an
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

    use super::{classify, combine};
    use crate::zoom_mute::ax::{children, copy_attr, create_app};
    use crate::zoom_mute::MicState;

    const MEET_PREFIX: &str = "https://meet.google.com/";
    /// Elements visited per browser; Meet's page tree is far smaller.
    const MAX_NODES: usize = 6_000;
    const MAX_DEPTH: usize = 80;

    fn string_attr(e: &CFType, name: &str) -> Option<String> {
        copy_attr(e, name)
            .and_then(|v| v.downcast::<CFString>())
            .map(|s| s.to_string())
    }

    fn is_meet_page(e: &CFType) -> bool {
        copy_attr(e, "AXURL")
            .and_then(|v| v.downcast::<CFURL>())
            .is_some_and(|u| u.get_string().to_string().starts_with(MEET_PREFIX))
    }

    /// Collects button labels under `e`; never logged.
    fn button_labels(e: &CFType, depth: usize, budget: &mut usize, out: &mut Vec<String>) {
        if *budget == 0 || depth > MAX_DEPTH {
            return;
        }
        *budget -= 1;
        if string_attr(e, "AXRole").as_deref() == Some("AXButton") {
            if let Some(d) = string_attr(e, "AXDescription") {
                out.push(d);
            }
            return;
        }
        for c in children(e) {
            button_labels(&c, depth + 1, budget, out);
        }
    }

    /// Finds Meet web areas under `e` and classifies each.
    fn meet_pages(e: &CFType, depth: usize, budget: &mut usize, out: &mut Vec<MicState>) {
        if *budget == 0 || depth > MAX_DEPTH {
            return;
        }
        *budget -= 1;
        if string_attr(e, "AXRole").as_deref() == Some("AXWebArea") {
            if is_meet_page(e) {
                let mut labels = Vec::new();
                button_labels(e, depth, budget, &mut labels);
                out.push(classify(labels.iter().map(String::as_str)));
            }
            // Nested frames are not Meet's toolbar.
            return;
        }
        for c in children(e) {
            meet_pages(&c, depth + 1, budget, out);
        }
    }

    /// Meet's state across the given browser instances.
    pub fn current_state(pids: &[i32]) -> MicState {
        let mut states = Vec::new();
        for &pid in pids {
            let Some(app) = create_app(pid) else {
                continue;
            };
            let Some(windows) = copy_attr(&app, "AXWindows") else {
                continue;
            };
            let mut budget = MAX_NODES;
            for w in crate::zoom_mute::ax::as_array(windows) {
                meet_pages(&w, 0, &mut budget, &mut states);
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
}
