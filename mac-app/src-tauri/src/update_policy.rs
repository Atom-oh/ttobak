//! Pure update policy: when the app is busy and which versions to prompt for.
//! No Tauri imports so it stays unit-testable.

use semver::Version;

/// SPA phases during which an update must not prompt or install.
const BUSY_PHASES: [&str; 7] = [
    "starting",
    "recording",
    "notes",
    "creating",
    "saving",
    "uploading",
    "redirecting",
];

/// Busy if native recording, any finalizing path, any upload in flight, or the
/// SPA reports a busy phase. `None`, `idle`, `error` and unknown phases are
/// not busy by themselves.
pub fn is_busy(
    native_recording: bool,
    finalizing: usize,
    uploads_in_flight: usize,
    spa_phase: Option<&str>,
) -> bool {
    native_recording
        || finalizing > 0
        || uploads_in_flight > 0
        || spa_phase.is_some_and(|p| BUSY_PHASES.contains(&p))
}

/// True when `candidate` is a valid semver strictly newer than `current`.
/// Any parse failure yields false.
pub fn is_newer(current: &str, candidate: &str) -> bool {
    match (Version::parse(current), Version::parse(candidate)) {
        (Ok(c), Ok(n)) => n > c,
        _ => false,
    }
}

/// Prompt only for a version newer than the running one that the user has not
/// declined; a version newer than the declined one prompts again.
pub fn should_prompt(current: &str, available: &str, declined: Option<&str>) -> bool {
    is_newer(current, available) && declined != Some(available)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn idle_everywhere_is_not_busy() {
        assert!(!is_busy(false, 0, 0, None));
        assert!(!is_busy(false, 0, 0, Some("idle")));
        assert!(!is_busy(false, 0, 0, Some("error")));
    }
    #[test]
    fn native_work_is_busy_even_if_spa_says_idle() {
        assert!(is_busy(true, 0, 0, Some("idle")));
        assert!(is_busy(false, 1, 0, None));
        assert!(is_busy(false, 0, 1, Some("idle")));
    }
    #[test]
    fn spa_phases_are_busy() {
        for p in [
            "starting",
            "recording",
            "notes",
            "creating",
            "saving",
            "uploading",
            "redirecting",
        ] {
            assert!(is_busy(false, 0, 0, Some(p)), "{p}");
        }
    }
    #[test]
    fn unknown_phase_is_not_busy() {
        assert!(!is_busy(false, 0, 0, Some("whatever")));
    }
    #[test]
    fn version_rules() {
        assert!(is_newer("0.1.0", "0.2.0"));
        assert!(!is_newer("0.2.0", "0.2.0"));
        assert!(!is_newer("0.2.0", "0.1.9"));
        assert!(!is_newer("0.1.0", "garbage"));
    }
    #[test]
    fn declined_version_is_not_prompted_but_newer_is() {
        assert!(should_prompt("0.1.0", "0.2.0", None));
        assert!(!should_prompt("0.1.0", "0.2.0", Some("0.2.0")));
        assert!(should_prompt("0.1.0", "0.3.0", Some("0.2.0")));
        assert!(!should_prompt("0.2.0", "0.2.0", None));
    }
}
