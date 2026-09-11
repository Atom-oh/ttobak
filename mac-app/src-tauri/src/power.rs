//! Idle-sleep protection shared by recordings awaiting upload or discard.
//!
//! This does not prevent lid-close, Apple-menu, or low-battery sleep. Keep
//! the recording checkpoints and startup recovery paths for those cases.

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
pub(crate) use macos::PowerAssertion;

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

    pub(crate) struct PowerAssertion {
        id: u32,
    }

    impl PowerAssertion {
        /// Best effort: a power-management failure must not reject a recording.
        /// Apple documents that this assertion only prevents idle sleep:
        /// https://developer.apple.com/library/archive/qa/qa1340/_index.html
        pub(crate) fn acquire(reason: &str) -> Option<Self> {
            let assertion_type = CFString::new("PreventUserIdleSystemSleep");
            let assertion_name = CFString::new(reason);
            let mut id = 0;
            // SAFETY: both CFStrings remain alive for the synchronous call,
            // and `id` is a valid output pointer. Level 255 is kIOPMAssertionLevelOn.
            let result = unsafe {
                IOPMAssertionCreateWithName(
                    assertion_type.as_concrete_TypeRef(),
                    255,
                    assertion_name.as_concrete_TypeRef(),
                    &mut id,
                )
            };
            if result == 0 {
                log::info!("idle-sleep assertion acquired: {reason}");
                Some(Self { id })
            } else {
                log::warn!("idle-sleep assertion unavailable (IOReturn {result})");
                None
            }
        }
    }

    impl Drop for PowerAssertion {
        fn drop(&mut self) {
            // SAFETY: this instance uniquely owns a successfully acquired ID.
            let result = unsafe { IOPMAssertionRelease(self.id) };
            if result != 0 {
                log::warn!("failed to release idle-sleep assertion (IOReturn {result})");
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
