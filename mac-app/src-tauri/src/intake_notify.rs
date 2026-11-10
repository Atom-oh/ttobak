//! Reads the SpecReq task notification queue written by the local MCP intake
//! runner (`mcp-server/src/intake/notify.ts`, ADR-065) and posts each unseen
//! notice as a desktop notification.
//!
//! The queue is `~/.ttobak/intake/notify.jsonl`: append-only JSON lines, a
//! crash can duplicate a line (same `key`), and the last line may be partial.
//! Everything read from it is untrusted text: the in-app path is re-validated
//! against a strict shape and joined onto the origin of the configured main
//! window, never onto anything taken from the file.

use std::collections::HashMap;
use std::io::Read;
use std::path::{Path, PathBuf};
use std::sync::Arc;
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use parking_lot::Mutex;
use serde_json::Value;
use tauri::{AppHandle, Manager, Url, WebviewUrl};

/// Queue contract version this reader understands.
const QUEUE_VERSION: u64 = 1;
/// Largest queue file the poller will read.
const MAX_QUEUE_BYTES: u64 = 1024 * 1024;
/// The runner caps each line at 4 KiB.
const MAX_LINE_BYTES: usize = 4096;
const MAX_TITLE_CHARS: usize = 120;
const MAX_BODY_CHARS: usize = 400;
/// Notices older than this are ignored; so are ones this far in the future.
const MAX_AGE: Duration = Duration::from_secs(24 * 60 * 60);
const MAX_FUTURE: Duration = Duration::from_secs(5 * 60);
/// Seen-store entries are dropped after this long.
const SEEN_RETENTION: Duration = Duration::from_secs(48 * 60 * 60);
/// How long a posted notice may be opened by the next reopen or focus.
const OPEN_WINDOW: Duration = Duration::from_secs(10 * 60);
const POLL_INTERVAL: Duration = Duration::from_secs(60);
/// A burst (first launch with a day of backlog) posts a few per cycle; the
/// rest stay unseen and follow on later cycles.
const MAX_POSTS_PER_CYCLE: usize = 5;
const SEEN_FILE: &str = "intake-notified.json";

/// A validated queue entry.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Notice {
    pub key: String,
    pub title: String,
    pub body: String,
    pub path: String,
}

fn is_lower_hex64(s: &str) -> bool {
    s.len() == 64 && s.bytes().all(|b| matches!(b, b'0'..=b'9' | b'a'..=b'f'))
}

/// `<64 lowercase hex>:<state>` with a `[a-z_]{3,20}` state; returns the event id.
fn event_id_of_key(key: &str) -> Option<&str> {
    let (id, state) = key.split_once(':')?;
    let state_ok = (3..=20).contains(&state.len())
        && state.bytes().all(|b| b.is_ascii_lowercase() || b == b'_');
    (is_lower_hex64(id) && state_ok).then_some(id)
}

/// Days since 1970-01-01 for a proleptic Gregorian date.
fn days_from_civil(y: i64, m: i64, d: i64) -> i64 {
    let y = if m <= 2 { y - 1 } else { y };
    let era = y.div_euclid(400);
    let yoe = y - era * 400;
    let mp = (m + 9) % 12;
    let doy = (153 * mp + 2) / 5 + d - 1;
    let doe = yoe * 365 + yoe / 4 - yoe / 100 + doy;
    era * 146_097 + doe - 719_468
}

/// Parses `YYYY-MM-DDTHH:MM:SS[.fff]Z` into Unix seconds.
fn parse_iso_utc(s: &str) -> Option<i64> {
    let b = s.as_bytes();
    if b.len() < 20 || !s.is_ascii() || *b.last()? != b'Z' {
        return None;
    }
    let num = |r: std::ops::Range<usize>| -> Option<i64> {
        let part = &s[r];
        part.bytes()
            .all(|c| c.is_ascii_digit())
            .then(|| part.parse().ok())?
    };
    if b[4] != b'-' || b[7] != b'-' || b[10] != b'T' || b[13] != b':' || b[16] != b':' {
        return None;
    }
    let (y, mo, d) = (num(0..4)?, num(5..7)?, num(8..10)?);
    let (h, mi, sec) = (num(11..13)?, num(14..16)?, num(17..19)?);
    let frac = &s[19..s.len() - 1];
    let frac_ok = frac.is_empty()
        || (frac.starts_with('.')
            && frac.len() > 1
            && frac[1..].bytes().all(|c| c.is_ascii_digit()));
    if !frac_ok
        || !(1..=12).contains(&mo)
        || !(1..=31).contains(&d)
        || h > 23
        || mi > 59
        || sec > 60
    {
        return None;
    }
    Some(days_from_civil(y, mo, d) * 86_400 + h * 3600 + mi * 60 + sec)
}

fn unix_secs(t: SystemTime) -> i64 {
    match t.duration_since(UNIX_EPOCH) {
        Ok(d) => d.as_secs() as i64,
        Err(e) => -(e.duration().as_secs() as i64),
    }
}

fn parse_line(line: &[u8], now: i64) -> Option<Notice> {
    if line.len() > MAX_LINE_BYTES {
        return None;
    }
    let v: Value = serde_json::from_slice(line).ok()?;
    if v.get("v")?.as_u64()? != QUEUE_VERSION {
        return None;
    }
    let key = v.get("key")?.as_str()?;
    let event_id = event_id_of_key(key)?;
    let path = v.get("path")?.as_str()?;
    if path != format!("/meetings?task={event_id}") {
        return None;
    }
    let created = parse_iso_utc(v.get("createdAt")?.as_str()?)?;
    if created < now - MAX_AGE.as_secs() as i64 || created > now + MAX_FUTURE.as_secs() as i64 {
        return None;
    }
    let title = v.get("title")?.as_str()?;
    let body = v.get("body")?.as_str()?;
    if title.chars().count() > MAX_TITLE_CHARS || body.chars().count() > MAX_BODY_CHARS {
        return None;
    }
    Some(Notice {
        key: key.to_string(),
        title: title.to_string(),
        body: body.to_string(),
        path: path.to_string(),
    })
}

/// Parses the queue bytes. Malformed lines (including a partial trailing
/// line) and entries failing any contract check are skipped. Duplicate keys
/// are kept here; `SeenStore::unseen` collapses them.
pub fn parse_notices(bytes: &[u8], now: SystemTime) -> Vec<Notice> {
    let now = unix_secs(now);
    bytes
        .split(|&b| b == b'\n')
        .filter(|l| !l.is_empty())
        .filter_map(|l| parse_line(l, now))
        .collect()
}

/// Keys already posted, with the post time (Unix seconds), persisted at
/// `intake-notified.json` in the app data directory.
#[derive(Debug, Default)]
pub struct SeenStore {
    posted: HashMap<String, i64>,
    path: Option<PathBuf>,
    /// Unsaved changes; a failed save stays dirty and is retried.
    dirty: bool,
}

impl SeenStore {
    /// Loads the store; a missing or corrupt file starts empty.
    pub fn load(path: PathBuf) -> Self {
        let posted = std::fs::read(&path)
            .ok()
            .and_then(|b| serde_json::from_slice::<HashMap<String, i64>>(&b).ok())
            .unwrap_or_default();
        Self {
            posted,
            path: Some(path),
            dirty: false,
        }
    }

    /// Unseen notices, one per key (first wins), in queue order.
    pub fn unseen(&self, notices: Vec<Notice>) -> Vec<Notice> {
        let mut taken = std::collections::HashSet::new();
        notices
            .into_iter()
            .filter(|n| !self.posted.contains_key(&n.key) && taken.insert(n.key.clone()))
            .collect()
    }

    pub fn record(&mut self, key: &str) {
        self.posted
            .insert(key.to_string(), unix_secs(SystemTime::now()));
        self.dirty = true;
    }

    /// Drops entries posted more than 48 hours before `now`.
    pub fn prune(&mut self, now: SystemTime) {
        let cutoff = unix_secs(now) - SEEN_RETENTION.as_secs() as i64;
        let before = self.posted.len();
        self.posted.retain(|_, at| *at >= cutoff);
        self.dirty |= self.posted.len() != before;
    }

    /// Saves only when something changed; a failure stays dirty for the next call.
    pub fn save_if_dirty(&mut self) -> std::io::Result<()> {
        if !self.dirty {
            return Ok(());
        }
        self.save()?;
        self.dirty = false;
        Ok(())
    }

    /// Atomically writes the store with mode 0600.
    pub fn save(&self) -> std::io::Result<()> {
        let Some(path) = &self.path else {
            return Ok(());
        };
        if let Some(dir) = path.parent() {
            std::fs::create_dir_all(dir)?;
        }
        let tmp = path.with_extension("json.tmp");
        write_private(&tmp, &serde_json::to_vec(&self.posted)?)?;
        std::fs::rename(&tmp, path)
    }
}

fn write_private(path: &Path, bytes: &[u8]) -> std::io::Result<()> {
    use std::io::Write;
    let mut opts = std::fs::OpenOptions::new();
    opts.write(true).create(true).truncate(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        opts.mode(0o600);
    }
    let mut f = opts.open(path)?;
    f.write_all(bytes)
}

/// Latest posted notice path, openable once within ten minutes.
#[derive(Debug, Default)]
pub struct OpenTarget {
    slot: Mutex<Option<(String, SystemTime)>>,
}

impl OpenTarget {
    pub fn set(&self, path: &str, now: SystemTime) {
        *self.slot.lock() = Some((path.to_string(), now));
    }

    /// Returns the path once if it is still inside the window, and clears it.
    /// While `blocked` (a recording is running) the target is kept, still
    /// bounded by its expiry, so a later focus can open it.
    pub fn take(&self, now: SystemTime, blocked: bool) -> Option<String> {
        let mut slot = self.slot.lock();
        let (_, at) = slot.as_ref()?;
        if now.duration_since(*at).unwrap_or_default() > OPEN_WINDOW {
            *slot = None;
            return None;
        }
        if blocked {
            return None;
        }
        slot.take().map(|(path, _)| path)
    }
}

/// Joins a validated in-app path onto the configured origin. The path is
/// re-checked here so the open step does not depend on the parser alone.
fn target_url(base: &Url, path: &str) -> Option<Url> {
    let id = path.strip_prefix("/meetings?task=")?;
    if !is_lower_hex64(id) || !matches!(base.scheme(), "https" | "http") {
        return None;
    }
    let mut url = base.clone();
    url.set_path("/meetings");
    url.set_query(Some(&format!("task={id}")));
    url.set_fragment(None);
    (url.origin() == base.origin()).then_some(url)
}

/// Origin source: the main window URL in the app config, never the queue.
fn main_window_base(app: &AppHandle) -> Option<Url> {
    app.config()
        .app
        .windows
        .iter()
        .find(|w| w.label == "main")
        .and_then(|w| match &w.url {
            WebviewUrl::External(u) => Some(u.clone()),
            _ => None,
        })
}

/// Pure navigation guard: the native recorder must be idle and the SPA must
/// have reported phase `idle` (not recording, starting or in a post-recording
/// step). A missing or unrecognised state is treated as busy.
fn may_navigate(native_recording: bool, spa: Option<&Value>) -> bool {
    !native_recording
        && spa
            .and_then(|v| v.get("phase"))
            .and_then(Value::as_str)
            .is_some_and(|p| p == "idle")
}

/// Called on `RunEvent::Reopen` and main-window `Focused(true)`: navigates the
/// main window to the pending notice once, if it has not expired.
pub fn open_pending(app: &AppHandle) {
    let Some(target) = app.try_state::<Arc<OpenTarget>>() else {
        return;
    };
    // Reloading the SPA mid-recording, or during the stop/upload tail, would
    // lose live-caption state and unsaved text. Navigate only when the native
    // recorder is idle AND the SPA reports `idle`; unknown state fails closed.
    let native_recording = app
        .try_state::<crate::RecorderState>()
        .map(|s| s.recorder.lock().snapshot().recording)
        .unwrap_or(false);
    let spa = app
        .try_state::<crate::control::ControlState>()
        .and_then(|c| c.spa_state());
    let blocked = !may_navigate(native_recording, spa.as_ref());
    let Some(path) = target.take(SystemTime::now(), blocked) else {
        return;
    };
    let Some(url) = main_window_base(app).and_then(|b| target_url(&b, &path)) else {
        return;
    };
    if let Some(window) = app.get_webview_window("main") {
        let _ = window.unminimize();
        let _ = window.show();
        if let Err(e) = window.navigate(url) {
            log::warn!("intake notice navigation failed: {e}");
        }
        let _ = window.set_focus();
    }
}

#[cfg(unix)]
fn queue_path() -> Option<PathBuf> {
    let home = std::env::var_os("HOME")?;
    let home = PathBuf::from(home);
    home.is_absolute()
        .then(|| home.join(".ttobak").join("intake").join("notify.jsonl"))
}

/// Reads the queue only when it is a regular, non-symlink file owned by this
/// user, not writable by others, and at most 1 MiB. The checks run on the
/// opened handle (O_NOFOLLOW) so the file cannot be swapped between them.
#[cfg(unix)]
fn read_queue(path: &Path) -> std::io::Result<Option<Vec<u8>>> {
    use std::os::unix::fs::{MetadataExt, OpenOptionsExt};
    let file = match std::fs::OpenOptions::new()
        .read(true)
        .custom_flags(libc::O_NOFOLLOW | libc::O_NONBLOCK)
        .open(path)
    {
        Ok(f) => f,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Ok(None),
        Err(e) => return Err(e),
    };
    let meta = file.metadata()?;
    // SAFETY: geteuid has no preconditions.
    let me = unsafe { libc::geteuid() };
    if !meta.is_file()
        || meta.uid() != me
        || meta.mode() & 0o022 != 0
        || meta.len() > MAX_QUEUE_BYTES
    {
        log::warn!("intake notice queue ignored: not a private regular file within the size cap");
        return Ok(None);
    }
    let mut bytes = Vec::new();
    file.take(MAX_QUEUE_BYTES + 1).read_to_end(&mut bytes)?;
    Ok((bytes.len() as u64 <= MAX_QUEUE_BYTES).then_some(bytes))
}

/// One poll cycle: read, validate, post unseen notices, record them.
#[cfg(unix)]
fn poll_once(app: &AppHandle, store: &mut SeenStore, target: &OpenTarget, queue: &Path) {
    use tauri_plugin_notification::NotificationExt;
    let now = SystemTime::now();
    store.prune(now);
    persist(store);
    let bytes = match read_queue(queue) {
        Ok(Some(b)) => b,
        Ok(None) => return,
        Err(e) => {
            log::warn!("intake notice queue unreadable: {e}");
            return;
        }
    };
    for notice in store
        .unseen(parse_notices(&bytes, now))
        .into_iter()
        .take(MAX_POSTS_PER_CYCLE)
    {
        match app
            .notification()
            .builder()
            .title(&notice.title)
            .body(&notice.body)
            .show()
        {
            Ok(()) => {
                target.set(&notice.path, SystemTime::now());
                store.record(&notice.key);
            }
            // Not recorded, so the next cycle retries it.
            Err(e) => log::warn!("intake notice not posted: {e}"),
        }
    }
    persist(store);
}

#[cfg(unix)]
fn persist(store: &mut SeenStore) {
    if let Err(e) = store.save_if_dirty() {
        log::warn!("intake seen store not saved (will retry): {e}");
    }
}

/// Starts the background poller (launch, then every 60 seconds). Failure only
/// disables notices; the main app is unaffected.
pub fn start(app: AppHandle) {
    #[cfg(unix)]
    {
        let (Some(queue), Ok(dir)) = (queue_path(), app.path().app_data_dir()) else {
            log::warn!("intake notices unavailable: no home or data directory");
            return;
        };
        let target = Arc::new(OpenTarget::default());
        app.manage(target.clone());
        let spawned = std::thread::Builder::new()
            .name("intake-notify".into())
            .spawn(move || {
                let mut store = SeenStore::load(dir.join(SEEN_FILE));
                loop {
                    poll_once(&app, &mut store, &target, &queue);
                    std::thread::sleep(POLL_INTERVAL);
                }
            });
        if let Err(e) = spawned {
            log::warn!("intake notices unavailable: {e}");
        }
    }
    #[cfg(not(unix))]
    let _ = app;
}

#[cfg(test)]
mod tests {
    use super::*;

    const ID: &str = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef";
    const OTHER: &str = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210";

    fn at(secs: i64) -> SystemTime {
        UNIX_EPOCH + Duration::from_secs(secs as u64)
    }

    // 2026-11-10T12:00:00Z
    const NOW: i64 = 1_794_312_000;

    fn line(key: &str, path: &str, created: &str) -> String {
        format!(
            r#"{{"v":1,"key":"{key}","state":"x","title":"t","body":"b","path":"{path}","createdAt":"{created}"}}"#
        )
    }

    fn good(state: &str) -> String {
        line(
            &format!("{ID}:{state}"),
            &format!("/meetings?task={ID}"),
            "2026-11-10T11:59:00.000Z",
        )
    }

    #[test]
    fn iso_parser_matches_known_instant() {
        assert_eq!(parse_iso_utc("2026-11-10T12:00:00Z"), Some(NOW));
        assert_eq!(parse_iso_utc("2026-11-10T12:00:00.123Z"), Some(NOW));
        assert_eq!(parse_iso_utc("2026-11-10T12:00:00+00:00"), None);
        assert_eq!(parse_iso_utc("2026-13-10T12:00:00Z"), None);
    }

    #[test]
    fn parse_skips_malformed_old_and_future_lines() {
        let mut q = String::new();
        q.push_str(&good("issue_needed"));
        q.push('\n');
        q.push_str("not json\n");
        q.push_str(&good("issue_needed").replace(r#""v":1"#, r#""v":2"#));
        q.push('\n');
        q.push_str(&line(
            &format!("{ID}:acked"),
            &format!("/meetings?task={ID}"),
            "2026-11-09T11:00:00Z",
        ));
        q.push('\n');
        q.push_str(&line(
            &format!("{ID}:ack_failed"),
            &format!("/meetings?task={ID}"),
            "2026-11-10T12:06:00Z",
        ));
        q.push('\n');
        let long = good("ack_needed").replace(
            r#""title":"t""#,
            &format!(r#""title":"{}""#, "가".repeat(121)),
        );
        q.push_str(&long);
        q.push('\n');
        let ok_long = good("request_needed").replace(
            r#""title":"t""#,
            &format!(r#""title":"{}""#, "가".repeat(120)),
        );
        q.push_str(&ok_long);
        q.push('\n');
        // Partial trailing line (crash mid-append).
        q.push_str(&good("acked")[..40]);
        let notices = parse_notices(q.as_bytes(), at(NOW));
        let keys: Vec<_> = notices.iter().map(|n| n.key.as_str()).collect();
        assert_eq!(
            keys,
            vec![format!("{ID}:issue_needed"), format!("{ID}:request_needed")]
        );
    }

    #[test]
    fn parse_rejects_foreign_paths_and_mismatched_keys() {
        let ts = "2026-11-10T11:59:00Z";
        let k = format!("{ID}:acked");
        let paths = [
            "https://evil.example/meetings?task=".to_string() + ID,
            format!("//evil.example/meetings?task={ID}"),
            format!("/other?task={ID}"),
            format!("/meetings?task={OTHER}"),
            format!("/meetings?task={ID}&x=1"),
            "/meetings".to_string(),
        ];
        for p in paths {
            let l = line(&k, &p, ts);
            assert!(parse_notices(l.as_bytes(), at(NOW)).is_empty(), "{p}");
        }
        for bad_key in [
            format!("{}:acked", ID.to_uppercase()),
            format!("{ID}:Acked"),
            format!("{ID}:ab"),
            format!("{ID}:{}", "a".repeat(21)),
            ID.to_string(),
        ] {
            let l = line(&bad_key, &format!("/meetings?task={ID}"), ts);
            assert!(parse_notices(l.as_bytes(), at(NOW)).is_empty(), "{bad_key}");
        }
        assert_eq!(parse_notices(good("acked").as_bytes(), at(NOW)).len(), 1);
    }

    #[test]
    fn seen_store_posts_once_and_prunes() {
        let dir = std::env::temp_dir().join(format!(
            "ttobak-seen-{}-{}",
            std::process::id(),
            SystemTime::now()
                .duration_since(UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        ));
        let file = dir.join("intake-notified.json");
        let mut store = SeenStore::load(file.clone());
        let q = format!("{}\n{}\n", good("acked"), good("acked"));
        let notices = parse_notices(q.as_bytes(), at(NOW));
        assert_eq!(notices.len(), 2);
        let first = store.unseen(notices.clone());
        assert_eq!(first.len(), 1, "duplicate key collapses");
        store.record(&first[0].key);
        store.save().unwrap();
        assert!(store.unseen(notices.clone()).is_empty());
        let reloaded = SeenStore::load(file.clone());
        assert!(reloaded.unseen(notices.clone()).is_empty());
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let mode = std::fs::metadata(&file).unwrap().permissions().mode();
            assert_eq!(mode & 0o777, 0o600);
        }
        let mut store = reloaded;
        store.prune(SystemTime::now() + Duration::from_secs(47 * 3600));
        assert!(store.unseen(notices.clone()).is_empty());
        store.prune(SystemTime::now() + Duration::from_secs(49 * 3600));
        assert_eq!(store.unseen(notices).len(), 1);
        let _ = std::fs::remove_file(&file);
        let _ = std::fs::remove_dir(&dir);
    }

    #[test]
    fn open_target_expires_and_is_consumed_once() {
        let t = OpenTarget::default();
        let p = format!("/meetings?task={ID}");
        t.set(&p, at(NOW));
        assert_eq!(t.take(at(NOW + 599), false), Some(p.clone()));
        assert_eq!(t.take(at(NOW + 600), false), None, "consumed");
        t.set(&p, at(NOW));
        assert_eq!(t.take(at(NOW + 601), false), None, "expired");
        assert_eq!(t.take(at(NOW), false), None, "expiry also clears");
    }

    #[test]
    fn open_target_is_kept_while_recording() {
        let t = OpenTarget::default();
        let p = format!("/meetings?task={ID}");
        t.set(&p, at(NOW));
        assert_eq!(t.take(at(NOW + 60), true), None, "blocked");
        assert_eq!(t.take(at(NOW + 120), false), Some(p.clone()), "kept");
        t.set(&p, at(NOW));
        assert_eq!(t.take(at(NOW + 601), true), None);
        assert_eq!(t.take(at(NOW + 602), false), None, "expired while blocked");
    }

    #[test]
    fn navigation_guard_fails_closed() {
        let idle = serde_json::json!({"phase": "idle"});
        assert!(may_navigate(false, Some(&idle)));
        let browser = serde_json::json!({"phase": "recording", "source": "browser"});
        assert!(!may_navigate(false, Some(&browser)), "browser recording");
        // Native stop tail: recorder handle gone, SPA still finalizing/uploading.
        let tail = serde_json::json!({"phase": "uploading"});
        assert!(!may_navigate(false, Some(&tail)), "stop/upload tail");
        let starting = serde_json::json!({"phase": "starting"});
        assert!(!may_navigate(false, Some(&starting)));
        assert!(!may_navigate(true, Some(&idle)), "native recording");
        assert!(!may_navigate(false, None), "unknown state");
        assert!(!may_navigate(false, Some(&serde_json::json!({}))));
        assert!(!may_navigate(false, Some(&serde_json::json!({"phase": 1}))));
    }

    #[test]
    fn seen_store_retries_failed_save_and_saves_after_prune() {
        let dir = std::env::temp_dir().join(format!(
            "ttobak-seen-dirty-{}-{}",
            std::process::id(),
            SystemTime::now()
                .duration_since(UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        ));
        let file = dir.join("intake-notified.json");
        // A directory squatting on the tmp path makes the first save fail.
        std::fs::create_dir_all(file.with_extension("json.tmp")).unwrap();
        let mut store = SeenStore::load(file.clone());
        store.record("k");
        assert!(store.save_if_dirty().is_err());
        std::fs::remove_dir(file.with_extension("json.tmp")).unwrap();
        assert!(store.save_if_dirty().is_ok(), "retried while dirty");
        assert!(SeenStore::load(file.clone()).posted.contains_key("k"));
        store.prune(SystemTime::now() + Duration::from_secs(49 * 3600));
        store.save_if_dirty().unwrap();
        assert!(
            SeenStore::load(file.clone()).posted.is_empty(),
            "prune persisted"
        );
        let _ = std::fs::remove_file(&file);
        let _ = std::fs::remove_dir(&dir);
    }

    #[test]
    fn target_url_uses_configured_origin_only() {
        let base = Url::parse("https://ttobak.atomai.click/some/page?x=1#f").unwrap();
        let p = format!("/meetings?task={ID}");
        assert_eq!(
            target_url(&base, &p).unwrap().as_str(),
            format!("https://ttobak.atomai.click/meetings?task={ID}")
        );
        assert!(target_url(&base, "//evil.example/meetings?task=x").is_none());
        assert!(target_url(&base, &format!("/meetings?task={OTHER}&a=b")).is_none());
        assert!(target_url(&Url::parse("file:///x").unwrap(), &p).is_none());
    }
}
