//! Idle-sleep protection shared by recordings awaiting upload or discard,
//! plus a separate, deliberately narrow lid-close guard.
//!
//! `PowerAssertion` (`PreventUserIdleSystemSleep`) does not prevent lid-close,
//! Apple-menu, or low-battery sleep — keep the recording checkpoints and
//! startup recovery paths for those. `LidCloseGuard` (`PreventSystemSleep`) is
//! IOKit's own AC-power-only assertion for blocking lid-close sleep during a
//! short, bounded operation (Apple's own example: burning a disc) — never for
//! the length of an open-ended recording, which can run for hours; its actual
//! effect on lid-close has NOT been validated against real macOS
//! (`pmset -g assertions` + a physical lid-close test on both AC and battery)
//! from this change alone. It is acquired around two windows that are each
//! bounded by a DIFFERENT thing, not both by wall-clock time:
//! `stop_recording`'s `stop_and_finalize` is bounded by this command's own
//! lifetime (`STOP_CAPTURE_TIMEOUT`) — deliberately NOT by whatever the
//! spawned background finalize task ends up taking if that timeout is hit,
//! since that background task's completion time is unbounded and holding a
//! lid-close-blocking assertion until then would defeat the whole point of
//! this guard type (see `lib.rs`'s `stop_recording` for exactly how that's
//! kept bounded); `upload_recording`'s transfer is bounded by progress, not
//! total duration (`STALL_TIMEOUT` + `RESPONSE_DEADLINE_AFTER_FULL_SEND`
//! only abort a STALLED transfer — a slow-but-still-progressing one holds
//! this for as long as it takes, matching the project's existing
//! stall-not-duration upload-timeout invariant, not a fixed bound). The live
//! recording itself stays idle-sleep-only protected, same as before —
//! closing the lid mid-meeting is still expected to suspend capture; only the
//! finish-and-upload tail right after "end meeting" is additionally guarded
//! against a lid close now, and only on AC power, best-effort.

use std::collections::HashSet;
use std::path::{Path, PathBuf};

/// One assertion shared across pending paths, so finishing an older upload
/// cannot release protection for a newer recording.
pub(crate) struct RecordingPower<A> {
    pending: HashSet<PathBuf>,
    assertion: Option<A>,
}

impl<A> RecordingPower<A> {
    pub(crate) fn new() -> Self {
        Self {
            pending: HashSet::new(),
            assertion: None,
        }
    }

    pub(crate) fn protect(&mut self, path: PathBuf, acquire: impl FnOnce() -> Option<A>) {
        self.pending.insert(path);
        if self.assertion.is_none() {
            self.assertion = acquire();
        }
    }

    pub(crate) fn finish(&mut self, path: &Path) {
        self.pending.remove(path);
        if self.pending.is_empty() {
            self.assertion.take();
        }
    }
}

#[cfg(target_os = "macos")]
pub(crate) use macos::{LidCloseGuard, PowerAssertion};

#[cfg(target_os = "macos")]
mod macos {
    use core_foundation::base::TCFType;
    use core_foundation::string::{CFString, CFStringRef};

    #[link(name = "IOKit", kind = "framework")]
    extern "C" {
        fn IOPMAssertionCreateWithName(
            assertion_type: CFStringRef,
            assertion_level: u32,
            assertion_name: CFStringRef,
            assertion_id: *mut u32,
        ) -> i32;
        fn IOPMAssertionRelease(assertion_id: u32) -> i32;
    }

    /// Shared by both assertion types below — only the IOKit assertion-type
    /// string differs between them. Best effort: a power-management failure
    /// must not reject a recording, so callers turn `Err` into `None` — but
    /// the `IOReturn` code itself is still returned (not collapsed away)
    /// so callers can log it: on this macOS-only, no-CI module, that code
    /// is the only diagnostic available when acquisition fails.
    fn acquire(assertion_type: &str, reason: &str) -> Result<u32, i32> {
        let assertion_type = CFString::new(assertion_type);
        let assertion_name = CFString::new(reason);
        let mut id = 0;
        // SAFETY: both CFStrings remain alive for the synchronous call, and
        // `id` is a valid output pointer. Level 255 is kIOPMAssertionLevelOn.
        let result = unsafe {
            IOPMAssertionCreateWithName(
                assertion_type.as_concrete_TypeRef(),
                255,
                assertion_name.as_concrete_TypeRef(),
                &mut id,
            )
        };
        if result == 0 {
            Ok(id)
        } else {
            Err(result)
        }
    }

    fn release(id: u32) -> i32 {
        // SAFETY: the caller uniquely owns a successfully acquired ID.
        unsafe { IOPMAssertionRelease(id) }
    }

    pub(crate) struct PowerAssertion {
        id: u32,
    }

    impl PowerAssertion {
        /// Apple documents that this assertion only prevents IDLE sleep, not
        /// lid-close/Apple-menu/low-battery sleep:
        /// https://developer.apple.com/library/archive/qa/qa1340/_index.html
        /// Safe to hold for the length of an open-ended recording.
        pub(crate) fn acquire(reason: &str) -> Option<Self> {
            match acquire("PreventUserIdleSystemSleep", reason) {
                Ok(id) => {
                    log::info!("idle-sleep assertion acquired: {reason}");
                    Some(Self { id })
                }
                Err(result) => {
                    log::warn!("idle-sleep assertion unavailable (IOReturn {result}): {reason}");
                    None
                }
            }
        }
    }

    impl Drop for PowerAssertion {
        fn drop(&mut self) {
            let result = release(self.id);
            if result != 0 {
                log::warn!("failed to release idle-sleep assertion (IOReturn {result})");
            }
        }
    }

    /// Blocks lid-close sleep too, unlike `PowerAssertion` — see this
    /// module's doc comment for why it's only ever held for a short, bounded
    /// operation (Apple's own guidance), never for a whole recording.
    pub(crate) struct LidCloseGuard {
        id: u32,
    }

    impl LidCloseGuard {
        pub(crate) fn acquire(reason: &str) -> Option<Self> {
            match acquire("PreventSystemSleep", reason) {
                Ok(id) => {
                    log::info!("lid-close guard acquired: {reason}");
                    Some(Self { id })
                }
                Err(result) => {
                    log::warn!("lid-close guard unavailable (IOReturn {result}): {reason}");
                    None
                }
            }
        }
    }

    impl Drop for LidCloseGuard {
        fn drop(&mut self) {
            let result = release(self.id);
            if result != 0 {
                log::warn!("failed to release lid-close guard (IOReturn {result})");
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::cell::Cell;
    use std::rc::Rc;

    struct Assertion(Rc<Cell<usize>>);

    impl Drop for Assertion {
        fn drop(&mut self) {
            self.0.set(self.0.get() + 1);
        }
    }

    #[test]
    fn overlapping_recordings_keep_protection_in_either_cleanup_order() {
        for order in [["a.wav", "b.wav"], ["b.wav", "a.wav"]] {
            let releases = Rc::new(Cell::new(0));
            let mut power = RecordingPower::new();
            power.protect("a.wav".into(), || Some(Assertion(releases.clone())));
            power.protect("b.wav".into(), || {
                panic!("an existing assertion must be retained")
            });
            power.finish(Path::new(order[0]));
            assert_eq!(releases.get(), 0, "another recording is still pending");
            power.finish(Path::new(order[1]));
            assert_eq!(releases.get(), 1);
        }
    }

    #[test]
    fn retrying_the_same_upload_does_not_require_extra_cleanup() {
        let releases = Rc::new(Cell::new(0));
        let mut power = RecordingPower::new();
        power.protect("retry.wav".into(), || Some(Assertion(releases.clone())));
        power.protect("retry.wav".into(), || panic!("retry must reuse protection"));
        power.finish(Path::new("retry.wav"));
        assert_eq!(releases.get(), 1);
    }

    #[test]
    fn failed_acquisition_can_recover_without_forgetting_pending_recordings() {
        let releases = Rc::new(Cell::new(0));
        let mut power = RecordingPower::new();
        power.protect("a.wav".into(), || None);
        power.protect("b.wav".into(), || Some(Assertion(releases.clone())));
        power.finish(Path::new("b.wav"));
        assert_eq!(
            releases.get(),
            0,
            "a.wav still needs the recovered assertion"
        );
        power.finish(Path::new("a.wav"));
        assert_eq!(releases.get(), 1);
    }

    #[test]
    fn recovered_upload_can_acquire_without_a_start_recording_call() {
        let releases = Rc::new(Cell::new(0));
        let mut power = RecordingPower::new();
        power.protect("recovered.wav".into(), || Some(Assertion(releases.clone())));
        power.finish(Path::new("unrelated.wav"));
        assert_eq!(releases.get(), 0);
        power.finish(Path::new("recovered.wav"));
        assert_eq!(releases.get(), 1);
    }

    #[test]
    fn dropping_state_releases_protection_for_failed_uploads() {
        let releases = Rc::new(Cell::new(0));
        let mut power = RecordingPower::new();
        power.protect("failed-upload.wav".into(), || {
            Some(Assertion(releases.clone()))
        });
        drop(power);
        assert_eq!(releases.get(), 1);
    }
}
