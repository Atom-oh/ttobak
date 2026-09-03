//! Startup adoption of leftover temp WAVs, plus the path-containment helper
//! `recording_status` uses.
//!
//! Deliberately free of any Tauri dependency: `lib.rs`'s `.setup()` and
//! `recording_status` are thin wrappers over these functions so the actual
//! logic can be unit-tested on a Linux dev box (this module has no CI, and
//! the full crate only builds where Tauri's platform libraries exist).

use std::collections::HashSet;
use std::path::{Path, PathBuf};
use std::time::{Duration, SystemTime};

/// Result of one `scan_leftover_dir` pass.
#[derive(Debug, Default)]
pub(crate) struct LeftoverScan {
    /// Canonical paths of regular `*.wav` files younger than `max_age`,
    /// ready to be whitelisted.
    pub adopt: Vec<PathBuf>,
    /// How many stale files were deleted outright.
    pub deleted_stale: usize,
}

/// Scan `dir` for leftover recordings from a previous run (crash, force
/// quit, or a stop that never got a chance to finalize).
///
/// Best-effort: a missing/unreadable directory or entry yields an empty
/// result, never an error — this runs at app startup and must not turn a
/// stray file into a launch failure.
///
/// Two hardenings beyond the bare "any *.wav in this dir" check:
/// - `entry.file_type()` (which, unlike `Path::metadata()`, does NOT follow
///   symlinks) must report a regular file before a path is adopted. This is
///   defense-in-depth on top of `validate_recording_path`'s
///   `starts_with(allowed_dir)` containment check (which already blocks a
///   symlink pointing outside the directory at upload/delete time) — it
///   keeps the whitelist itself from ever holding anything but an actual
///   recording file.
/// - a file whose mtime is at least `max_age` before `now` is deleted
///   instead of adopted: raw meeting audio (PII) sitting unbounded in a temp
///   directory across every future launch is worse than losing a
///   long-abandoned crash's tail. `now` is a parameter (not read inside) so
///   tests can exercise the stale branch without touching mtimes.
///   An unreadable or future mtime counts as "not stale" (adopted).
pub(crate) fn scan_leftover_dir(dir: &Path, now: SystemTime, max_age: Duration) -> LeftoverScan {
    let mut scan = LeftoverScan::default();
    let Ok(entries) = std::fs::read_dir(dir) else {
        return scan;
    };
    for entry in entries.flatten() {
        let path = entry.path();
        if path.extension().and_then(|e| e.to_str()) != Some("wav") {
            continue;
        }
        let is_regular_file = entry.file_type().map(|t| t.is_file()).unwrap_or(false);
        if !is_regular_file {
            continue;
        }
        let age = entry
            .metadata()
            .ok()
            .and_then(|m| m.modified().ok())
            .and_then(|modified| now.duration_since(modified).ok());
        if age.map(|a| a >= max_age).unwrap_or(false) {
            if std::fs::remove_file(&path).is_ok() {
                scan.deleted_stale += 1;
            }
            continue;
        }
        if let Ok(canonical) = std::fs::canonicalize(&path) {
            scan.adopt.push(canonical);
        }
    }
    scan
}

/// Whether `raw` (a path string handed back by the WebView) is currently in
/// the `finalizing` set — with containment enforced BEFORE any filesystem
/// access.
///
/// `raw` is fully attacker-controlled input from the WebView. Calling
/// `canonicalize` on it unconditionally would turn `recording_status` into
/// a file-existence oracle for arbitrary paths (canonicalize succeeds only
/// for paths that exist), so anything not lexically under `allowed` is
/// answered `false` without a single syscall. The frontend only ever passes
/// back the canonical `temp_path` the Rust side returned from
/// `start_recording`, so a legitimate caller always passes this check; a
/// relative or `..`-bearing path is rejected by design. The post-canonicalize
/// `starts_with` re-check covers a symlink inside the directory that resolves
/// outside it.
///
/// Deliberately NOT `validate_recording_path`: that guard also requires
/// whitelist membership and returns an error, but this function must stay
/// infallible and must answer for a path that is mid-finalize (which is in
/// `recorded_paths`, but the caller shouldn't need to prove that just to
/// poll status).
pub(crate) fn finalizing_for_path(
    raw: &str,
    allowed: &Path,
    finalizing: &HashSet<PathBuf>,
) -> bool {
    if !Path::new(raw).starts_with(allowed) {
        return false;
    }
    std::fs::canonicalize(raw)
        .ok()
        .filter(|canonical| canonical.starts_with(allowed))
        .map(|canonical| finalizing.contains(&canonical))
        .unwrap_or(false)
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs;

    /// Fresh, unique scratch dir per test (no `tempfile` dev-dependency —
    /// same convention as `upload.rs`'s tests).
    fn scratch_dir(tag: &str) -> PathBuf {
        let dir = std::env::temp_dir().join(format!(
            "ttobak-leftover-{tag}-{}-{}",
            std::process::id(),
            SystemTime::now()
                .duration_since(SystemTime::UNIX_EPOCH)
                .map(|d| d.as_nanos())
                .unwrap_or(0)
        ));
        fs::create_dir_all(&dir).unwrap();
        dir
    }

    const MAX_AGE: Duration = Duration::from_secs(48 * 3600);

    #[test]
    fn fresh_wav_is_adopted_as_canonical_path() {
        let dir = scratch_dir("fresh");
        let wav = dir.join("meeting-1.wav");
        fs::write(&wav, b"RIFF").unwrap();

        let scan = scan_leftover_dir(&dir, SystemTime::now(), MAX_AGE);

        assert_eq!(scan.adopt, vec![fs::canonicalize(&wav).unwrap()]);
        assert_eq!(scan.deleted_stale, 0);
        assert!(wav.exists(), "a fresh file must not be deleted");
        fs::remove_dir_all(&dir).ok();
    }

    #[test]
    fn stale_wav_is_deleted_not_adopted() {
        let dir = scratch_dir("stale");
        let wav = dir.join("old.wav");
        fs::write(&wav, b"RIFF").unwrap();
        // Inject a `now` far past the file's real mtime instead of touching
        // the mtime itself.
        let far_future = SystemTime::now() + MAX_AGE + Duration::from_secs(3600);

        let scan = scan_leftover_dir(&dir, far_future, MAX_AGE);

        assert!(scan.adopt.is_empty());
        assert_eq!(scan.deleted_stale, 1);
        assert!(!wav.exists(), "stale file must be removed from disk");
        fs::remove_dir_all(&dir).ok();
    }

    #[test]
    fn non_wav_files_are_ignored() {
        let dir = scratch_dir("ext");
        fs::write(dir.join("notes.txt"), b"x").unwrap();
        fs::write(dir.join("clip.wav.bak"), b"x").unwrap();

        let scan = scan_leftover_dir(&dir, SystemTime::now(), MAX_AGE);

        assert!(scan.adopt.is_empty());
        assert_eq!(scan.deleted_stale, 0);
        fs::remove_dir_all(&dir).ok();
    }

    #[cfg(unix)]
    #[test]
    fn symlink_to_wav_is_not_adopted() {
        let dir = scratch_dir("symlink");
        let outside = scratch_dir("symlink-target");
        let target = outside.join("real.wav");
        fs::write(&target, b"RIFF").unwrap();
        std::os::unix::fs::symlink(&target, dir.join("link.wav")).unwrap();

        let scan = scan_leftover_dir(&dir, SystemTime::now(), MAX_AGE);

        assert!(
            scan.adopt.is_empty(),
            "symlinks must never enter the whitelist"
        );
        assert!(target.exists(), "the symlink target must be left alone");
        fs::remove_dir_all(&dir).ok();
        fs::remove_dir_all(&outside).ok();
    }

    #[test]
    fn missing_directory_yields_empty_scan() {
        let dir =
            std::env::temp_dir().join(format!("ttobak-leftover-missing-{}", std::process::id()));
        fs::remove_dir_all(&dir).ok();

        let scan = scan_leftover_dir(&dir, SystemTime::now(), MAX_AGE);

        assert!(scan.adopt.is_empty());
        assert_eq!(scan.deleted_stale, 0);
    }

    #[test]
    fn finalizing_rejects_paths_outside_allowed_dir_without_touching_fs() {
        let allowed = scratch_dir("allowed");
        // An existing file outside the allowed dir: with no containment
        // check, canonicalize would succeed and this would be an oracle.
        let outside = scratch_dir("outside");
        let foreign = outside.join("exists.wav");
        fs::write(&foreign, b"RIFF").unwrap();
        let mut set = HashSet::new();
        set.insert(fs::canonicalize(&foreign).unwrap());

        assert!(!finalizing_for_path(
            foreign.to_str().unwrap(),
            &allowed,
            &set
        ));
        assert!(!finalizing_for_path("/etc/passwd", &allowed, &set));
        assert!(!finalizing_for_path("../escape.wav", &allowed, &set));
        fs::remove_dir_all(&allowed).ok();
        fs::remove_dir_all(&outside).ok();
    }

    #[test]
    fn finalizing_reports_membership_for_contained_path() {
        let allowed = fs::canonicalize(scratch_dir("contained")).unwrap();
        let wav = allowed.join("mid-finalize.wav");
        fs::write(&wav, b"RIFF").unwrap();
        let mut set = HashSet::new();
        set.insert(wav.clone());

        assert!(finalizing_for_path(wav.to_str().unwrap(), &allowed, &set));
        set.clear();
        assert!(!finalizing_for_path(wav.to_str().unwrap(), &allowed, &set));
        // Contained but nonexistent: canonicalize fails -> nothing to report.
        assert!(!finalizing_for_path(
            allowed.join("gone.wav").to_str().unwrap(),
            &allowed,
            &set
        ));
        fs::remove_dir_all(&allowed).ok();
    }

    #[cfg(unix)]
    #[test]
    fn finalizing_rejects_symlink_escaping_allowed_dir() {
        let allowed = fs::canonicalize(scratch_dir("escape")).unwrap();
        let outside = scratch_dir("escape-target");
        let target = outside.join("real.wav");
        fs::write(&target, b"RIFF").unwrap();
        let link = allowed.join("link.wav");
        std::os::unix::fs::symlink(&target, &link).unwrap();
        let mut set = HashSet::new();
        set.insert(fs::canonicalize(&target).unwrap());

        assert!(!finalizing_for_path(link.to_str().unwrap(), &allowed, &set));
        fs::remove_dir_all(&allowed).ok();
        fs::remove_dir_all(&outside).ok();
    }
}
