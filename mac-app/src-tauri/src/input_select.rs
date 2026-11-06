//! Microphone choice for a recording (ADR-061): a device the user picked, or
//! by default the input device a meeting app is capturing from, so the
//! recording hears the same microphone as Zoom/Teams/Meet even with several
//! microphones or in clamshell mode. Pure, so the rules are unit tested.

/// Why a device was chosen.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Reason {
    Requested,
    /// Followed the named meeting app's input.
    Meeting(String),
    SystemDefault,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Choice {
    /// Device UID; `None` when no input device is usable.
    pub uid: Option<String>,
    pub reason: Option<Reason>,
    pub warning: Option<String>,
}

/// `available`: UIDs of devices that have input channels.
/// `meeting_inputs`: (app label, device UID) for meeting apps capturing now,
/// in priority order. `default_uid`: the system default input.
pub fn choose(
    requested: Option<&str>,
    available: &[String],
    meeting_inputs: &[(String, String)],
    default_uid: Option<&str>,
) -> Choice {
    let usable = |uid: &str| available.iter().any(|a| a == uid);
    let mut warning = None;
    if let Some(uid) = requested.filter(|u| !u.is_empty()) {
        if usable(uid) {
            return Choice {
                uid: Some(uid.to_owned()),
                reason: Some(Reason::Requested),
                warning: None,
            };
        }
        warning = Some(
            "The selected microphone is not available; choosing one automatically.".to_owned(),
        );
    }
    if let Some((label, uid)) = meeting_inputs.iter().find(|(_, uid)| usable(uid)) {
        return Choice {
            uid: Some(uid.clone()),
            reason: Some(Reason::Meeting(label.clone())),
            warning,
        };
    }
    match default_uid.filter(|u| usable(u)) {
        Some(uid) => Choice {
            uid: Some(uid.to_owned()),
            reason: Some(Reason::SystemDefault),
            warning,
        },
        None => Choice {
            uid: None,
            reason: None,
            warning,
        },
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn ids(v: &[&str]) -> Vec<String> {
        v.iter().map(|s| (*s).to_owned()).collect()
    }

    #[test]
    fn requested_device_wins_when_available() {
        let c = choose(
            Some("usb"),
            &ids(&["usb", "builtin"]),
            &[("Zoom".into(), "builtin".into())],
            Some("builtin"),
        );
        assert_eq!(c.uid.as_deref(), Some("usb"));
        assert_eq!(c.reason, Some(Reason::Requested));
        assert!(c.warning.is_none());
    }

    #[test]
    fn follows_the_meeting_app_input_over_the_default() {
        let c = choose(
            None,
            &ids(&["usb", "builtin"]),
            &[("Zoom".into(), "usb".into())],
            Some("builtin"),
        );
        assert_eq!(c.uid.as_deref(), Some("usb"));
        assert_eq!(c.reason, Some(Reason::Meeting("Zoom".into())));
    }

    #[test]
    fn missing_request_falls_back_with_a_warning() {
        let c = choose(Some("gone"), &ids(&["builtin"]), &[], Some("builtin"));
        assert_eq!(c.uid.as_deref(), Some("builtin"));
        assert_eq!(c.reason, Some(Reason::SystemDefault));
        assert!(c.warning.is_some());
    }

    #[test]
    fn skips_meeting_inputs_that_are_not_usable() {
        // E.g. a closed-lid built-in microphone that vanished.
        let c = choose(
            None,
            &ids(&["usb"]),
            &[
                ("Zoom".into(), "builtin".into()),
                ("Teams".into(), "usb".into()),
            ],
            Some("builtin"),
        );
        assert_eq!(c.uid.as_deref(), Some("usb"));
        assert_eq!(c.reason, Some(Reason::Meeting("Teams".into())));
    }

    #[test]
    fn no_usable_input_yields_none() {
        let c = choose(None, &[], &[], Some("builtin"));
        assert_eq!(c.uid, None);
        assert_eq!(c.reason, None);
    }
}
