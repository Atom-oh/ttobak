//! Pure intake planning: schedule slots, catch-up decisions, CLI argv and node
//! resolution. No Tauri imports so it stays unit-testable.

use std::collections::BTreeSet;
use std::ffi::OsString;
use std::path::{Path, PathBuf};

use serde::{Deserialize, Serialize};

const DAY_MIN: i64 = 1440;

/// A local wall-clock time of day.
#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord)]
pub struct HhMm {
    pub hour: u8,
    pub minute: u8,
}

impl HhMm {
    /// Strict two-digit `HH:MM`, 00:00 through 23:59.
    pub fn parse(s: &str) -> Result<Self, String> {
        let b = s.as_bytes();
        if b.len() != 5 || b[2] != b':' {
            return Err(format!("invalid time: {s}"));
        }
        let digit = |i: usize| -> Result<u8, String> {
            if b[i].is_ascii_digit() {
                Ok(b[i] - b'0')
            } else {
                Err(format!("invalid time: {s}"))
            }
        };
        let hour = digit(0)? * 10 + digit(1)?;
        let minute = digit(3)? * 10 + digit(4)?;
        if hour > 23 || minute > 59 {
            return Err(format!("invalid time: {s}"));
        }
        Ok(HhMm { hour, minute })
    }

    fn format(self) -> String {
        format!("{:02}:{:02}", self.hour, self.minute)
    }

    fn minute_of_day(self) -> i64 {
        i64::from(self.hour) * 60 + i64::from(self.minute)
    }
}

/// How often the app runs intake.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Schedule {
    Off,
    Hourly,
    Daily(HhMm),
    TwiceDaily(HhMm, HhMm),
}

#[derive(Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
struct ScheduleWire {
    kind: String,
    times: Vec<String>,
}

impl Schedule {
    /// Build a schedule from its wire kind and `HH:MM` strings. Rejects bad
    /// shapes, invalid times and duplicate times.
    pub fn parse_times(kind: &str, times: &[String]) -> Result<Self, String> {
        let parsed = times
            .iter()
            .map(|t| HhMm::parse(t))
            .collect::<Result<Vec<_>, _>>()?;
        match (kind, parsed.as_slice()) {
            ("off", []) => Ok(Schedule::Off),
            ("hourly", []) => Ok(Schedule::Hourly),
            ("daily", [a]) => Ok(Schedule::Daily(*a)),
            ("twiceDaily", [a, b]) if a != b => Ok(Schedule::TwiceDaily(*a, *b)),
            ("twiceDaily", [_, _]) => Err("duplicate times".to_string()),
            ("off" | "hourly" | "daily" | "twiceDaily", _) => {
                Err(format!("wrong number of times for {kind}"))
            }
            _ => Err(format!("unknown schedule kind: {kind}")),
        }
    }

    /// Slot times of day (minutes since local midnight), ascending.
    fn day_minutes(&self) -> Vec<i64> {
        match self {
            Schedule::Off | Schedule::Hourly => Vec::new(),
            Schedule::Daily(a) => vec![a.minute_of_day()],
            Schedule::TwiceDaily(a, b) => {
                let mut v = vec![a.minute_of_day(), b.minute_of_day()];
                v.sort_unstable();
                v
            }
        }
    }
}

impl Serialize for Schedule {
    fn serialize<S: serde::Serializer>(&self, s: S) -> Result<S::Ok, S::Error> {
        let (kind, times) = match self {
            Schedule::Off => ("off", vec![]),
            Schedule::Hourly => ("hourly", vec![]),
            Schedule::Daily(a) => ("daily", vec![a.format()]),
            Schedule::TwiceDaily(a, b) => ("twiceDaily", vec![a.format(), b.format()]),
        };
        ScheduleWire {
            kind: kind.to_string(),
            times,
        }
        .serialize(s)
    }
}

impl<'de> Deserialize<'de> for Schedule {
    fn deserialize<D: serde::Deserializer<'de>>(d: D) -> Result<Self, D::Error> {
        let w = ScheduleWire::deserialize(d)?;
        Schedule::parse_times(&w.kind, &w.times).map_err(serde::de::Error::custom)
    }
}

/// Latest slot at or before now, as UTC epoch minutes. Local wall clock is
/// `now_epoch_min + utc_offset_min`.
pub fn latest_slot_at_or_before(
    schedule: &Schedule,
    now_epoch_min: i64,
    utc_offset_min: i32,
) -> Option<i64> {
    let off = i64::from(utc_offset_min);
    let local = now_epoch_min + off;
    if matches!(schedule, Schedule::Hourly) {
        return Some(local.div_euclid(60) * 60 - off);
    }
    let times = schedule.day_minutes();
    let &last = times.last()?;
    let day = local.div_euclid(DAY_MIN);
    let mod_ = local.rem_euclid(DAY_MIN);
    let slot_local = match times.iter().rev().find(|&&t| t <= mod_) {
        Some(&t) => day * DAY_MIN + t,
        None => (day - 1) * DAY_MIN + last,
    };
    Some(slot_local - off)
}

/// First slot strictly after now, as UTC epoch minutes.
pub fn next_slot_after(
    schedule: &Schedule,
    now_epoch_min: i64,
    utc_offset_min: i32,
) -> Option<i64> {
    let off = i64::from(utc_offset_min);
    let local = now_epoch_min + off;
    if matches!(schedule, Schedule::Hourly) {
        return Some((local.div_euclid(60) + 1) * 60 - off);
    }
    let times = schedule.day_minutes();
    let first = *times.first()?;
    let day = local.div_euclid(DAY_MIN);
    let mod_ = local.rem_euclid(DAY_MIN);
    let slot_local = match times.iter().find(|&&t| t > mod_) {
        Some(&t) => day * DAY_MIN + t,
        None => (day + 1) * DAY_MIN + first,
    };
    Some(slot_local - off)
}

/// Persisted catch-up bookkeeping, as UTC epoch-minute slots.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct SlotState {
    #[serde(default)]
    pub last_satisfied_slot: Option<i64>,
    #[serde(default)]
    pub last_attempted_slot: Option<i64>,
    /// Attempts recorded for `last_attempted_slot`.
    #[serde(default)]
    pub attempts_on_last_slot: u8,
}

/// Attempts allowed per slot: the scheduled (or first catch-up) run plus one
/// launch/wake retry.
pub const MAX_SLOT_ATTEMPTS: u8 = 2;

/// The slot to run now, if the latest slot is unsatisfied and has fewer than
/// [`MAX_SLOT_ATTEMPTS`] recorded attempts. A slot whose first attempt failed
/// without satisfying it (e.g. `AUTH_EXPIRED_MIDWAY`) is retried by a launch or
/// wake at most once; a slot missed while the app was closed gets its catch-up
/// run plus at most one more.
pub fn should_catch_up(
    schedule: &Schedule,
    state: &SlotState,
    now_epoch_min: i64,
    utc_offset_min: i32,
) -> Option<i64> {
    let slot = latest_slot_at_or_before(schedule, now_epoch_min, utc_offset_min)?;
    let unsatisfied = state.last_satisfied_slot.is_none_or(|s| slot > s);
    let attempts = if state.last_attempted_slot == Some(slot) {
        state.attempts_on_last_slot
    } else {
        0
    };
    (unsatisfied && attempts < MAX_SLOT_ATTEMPTS).then_some(slot)
}

/// Record a run for `slot`. No error code or `PARTIAL_FAILURE` (the runner
/// passes it only when a connector succeeded) satisfies the
/// slot; every other code (e.g. `AUTH_EXPIRED_MIDWAY`) only counts an attempt.
pub fn record_result(state: &mut SlotState, slot: i64, code: Option<&str>) {
    if state.last_attempted_slot == Some(slot) {
        state.attempts_on_last_slot = state.attempts_on_last_slot.saturating_add(1);
    } else {
        state.last_attempted_slot = Some(slot);
        state.attempts_on_last_slot = 1;
    }
    if matches!(code, None | Some("PARTIAL_FAILURE")) {
        state.last_satisfied_slot = Some(slot);
    }
}

/// Connectors that setup may skip.
#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum Connector {
    Calendar,
    Sfdc,
    Meetings,
    Specreq,
    Slack,
}

impl Connector {
    pub const ALL: [Connector; 5] = [
        Connector::Calendar,
        Connector::Sfdc,
        Connector::Meetings,
        Connector::Specreq,
        Connector::Slack,
    ];

    pub fn as_str(self) -> &'static str {
        match self {
            Connector::Calendar => "calendar",
            Connector::Sfdc => "sfdc",
            Connector::Meetings => "meetings",
            Connector::Specreq => "specreq",
            Connector::Slack => "slack",
        }
    }
}

/// One `intake` CLI invocation.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum IntakeAction {
    Run,
    Setup {
        accept_private_calendar: bool,
        skip: BTreeSet<Connector>,
    },
    Login,
    // `intake status`; the app reads status.json directly instead.
    #[allow(dead_code)]
    Status,
    /// `intake schedule off`: removes the LaunchAgent.
    ScheduleOff,
}

/// Arguments for the node binary: `[server_mjs, "intake", ...]`. Never adds
/// `--schedule`; the app owns scheduling.
pub fn argv(action: &IntakeAction, server_mjs: &Path) -> Vec<OsString> {
    let mut v: Vec<OsString> = vec![server_mjs.into(), "intake".into()];
    match action {
        IntakeAction::Run => v.extend(["run".into(), "--noninteractive".into()]),
        IntakeAction::Setup {
            accept_private_calendar,
            skip,
        } => {
            v.extend(["setup".into(), "--noninteractive".into()]);
            if *accept_private_calendar {
                v.push("--accept-unknown-sensitivity".into());
            }
            for c in skip {
                v.extend(["--skip".into(), c.as_str().into()]);
            }
        }
        IntakeAction::Login => v.push("login".into()),
        IntakeAction::Status => v.push("status".into()),
        IntakeAction::ScheduleOff => v.extend(["schedule".into(), "off".into()]),
    }
    v
}

/// Candidate node binaries: Homebrew paths, then nvm versions newest first.
/// Entries of `nvm_versions` that are not semver (optional leading `v`) are
/// ignored.
pub fn node_candidates(home: &Path, nvm_versions: &[String]) -> Vec<PathBuf> {
    let mut nvm: Vec<(semver::Version, &String)> = nvm_versions
        .iter()
        .filter_map(|n| {
            semver::Version::parse(n.strip_prefix('v').unwrap_or(n))
                .ok()
                .map(|v| (v, n))
        })
        .collect();
    nvm.sort_by(|a, b| b.0.cmp(&a.0));
    let mut out = vec![
        PathBuf::from("/opt/homebrew/bin/node"),
        PathBuf::from("/usr/local/bin/node"),
    ];
    out.extend(
        nvm.into_iter()
            .map(|(_, n)| home.join(".nvm/versions/node").join(n).join("bin/node")),
    );
    out
}

/// The stored node path if still executable, else the first executable
/// candidate.
pub fn resolve_node(
    stored: Option<&Path>,
    candidates: &[PathBuf],
    is_exec: impl Fn(&Path) -> bool,
) -> Option<PathBuf> {
    stored
        .filter(|p| is_exec(p))
        .map(Path::to_path_buf)
        .or_else(|| candidates.iter().find(|c| is_exec(c)).cloned())
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::ffi::OsString;
    use std::path::{Path, PathBuf};

    fn hm(h: u8, m: u8) -> HhMm {
        HhMm { hour: h, minute: m }
    }
    const DAY: i64 = 1440;
    // Local day 100 midnight, with UTC offset +540 (KST): utc = local - 540.
    const OFF: i32 = 540;
    fn local(day: i64, h: i64, m: i64) -> i64 {
        day * DAY + h * 60 + m
    }
    fn now_utc(day: i64, h: i64, m: i64) -> i64 {
        local(day, h, m) - OFF as i64
    }

    #[test]
    fn daily_slot_math() {
        let s = Schedule::Daily(hm(8, 30));
        assert_eq!(
            latest_slot_at_or_before(&s, now_utc(100, 9, 0), OFF),
            Some(now_utc(100, 8, 30))
        );
        assert_eq!(
            latest_slot_at_or_before(&s, now_utc(100, 8, 0), OFF),
            Some(now_utc(99, 8, 30))
        );
        assert_eq!(
            latest_slot_at_or_before(&s, now_utc(100, 8, 30), OFF),
            Some(now_utc(100, 8, 30))
        );
        assert_eq!(
            next_slot_after(&s, now_utc(100, 8, 30), OFF),
            Some(now_utc(101, 8, 30))
        );
        assert_eq!(
            next_slot_after(&s, now_utc(100, 8, 0), OFF),
            Some(now_utc(100, 8, 30))
        );
    }

    #[test]
    fn twice_daily_slot_math() {
        let s = Schedule::TwiceDaily(hm(18, 0), hm(9, 0));
        assert_eq!(
            latest_slot_at_or_before(&s, now_utc(100, 12, 0), OFF),
            Some(now_utc(100, 9, 0))
        );
        assert_eq!(
            latest_slot_at_or_before(&s, now_utc(100, 8, 0), OFF),
            Some(now_utc(99, 18, 0))
        );
        assert_eq!(
            next_slot_after(&s, now_utc(100, 12, 0), OFF),
            Some(now_utc(100, 18, 0))
        );
        assert_eq!(
            next_slot_after(&s, now_utc(100, 19, 0), OFF),
            Some(now_utc(101, 9, 0))
        );
    }

    #[test]
    fn hourly_slot_math() {
        let s = Schedule::Hourly;
        assert_eq!(
            latest_slot_at_or_before(&s, now_utc(100, 9, 25), OFF),
            Some(now_utc(100, 9, 0))
        );
        assert_eq!(
            next_slot_after(&s, now_utc(100, 9, 25), OFF),
            Some(now_utc(100, 10, 0))
        );
        assert_eq!(
            next_slot_after(&s, now_utc(100, 9, 0), OFF),
            Some(now_utc(100, 10, 0))
        );
        // Negative offset and pre-epoch values use floor math.
        assert_eq!(latest_slot_at_or_before(&s, -1, -300), Some(-60));
    }

    #[test]
    fn off_has_no_slots() {
        let s = Schedule::Off;
        assert_eq!(latest_slot_at_or_before(&s, 1000, 0), None);
        assert_eq!(next_slot_after(&s, 1000, 0), None);
        assert_eq!(should_catch_up(&s, &SlotState::default(), 1000, 0), None);
    }

    #[test]
    fn catch_up_runs_once_per_slot() {
        let s = Schedule::Daily(hm(8, 30));
        let mut st = SlotState::default();
        let now = now_utc(100, 9, 0);
        let slot = should_catch_up(&s, &st, now, OFF).expect("due");
        assert_eq!(slot, now_utc(100, 8, 30));
        record_result(&mut st, slot, None);
        assert_eq!(should_catch_up(&s, &st, now, OFF), None);
        assert_eq!(
            should_catch_up(&s, &st, now_utc(101, 9, 0), OFF),
            Some(now_utc(101, 8, 30))
        );
    }

    #[test]
    fn auth_expired_does_not_satisfy_slot() {
        let s = Schedule::Daily(hm(8, 30));
        let mut st = SlotState::default();
        let now = now_utc(100, 9, 0);
        let slot = now_utc(100, 8, 30);
        // The scheduled attempt fails midway.
        record_result(&mut st, slot, Some("AUTH_EXPIRED_MIDWAY"));
        assert_eq!(st.last_satisfied_slot, None);
        assert_eq!(st.last_attempted_slot, Some(slot));
        assert_eq!(st.attempts_on_last_slot, 1);
        // A launch or wake retries it once.
        assert_eq!(should_catch_up(&s, &st, now, OFF), Some(slot));
        record_result(&mut st, slot, Some("AUTH_EXPIRED_MIDWAY"));
        assert_eq!(st.attempts_on_last_slot, 2);
        assert_eq!(should_catch_up(&s, &st, now, OFF), None);
        // A later slot is due again.
        assert_eq!(
            should_catch_up(&s, &st, now_utc(101, 9, 0), OFF),
            Some(now_utc(101, 8, 30))
        );
        record_result(&mut st, now_utc(101, 8, 30), None);
        assert_eq!(st.attempts_on_last_slot, 1);
        assert_eq!(st.last_satisfied_slot, Some(now_utc(101, 8, 30)));
    }

    #[test]
    fn partial_failure_satisfies_slot() {
        let s = Schedule::Daily(hm(8, 30));
        let mut st = SlotState::default();
        let slot = now_utc(100, 8, 30);
        record_result(&mut st, slot, Some("PARTIAL_FAILURE"));
        assert_eq!(st.last_satisfied_slot, Some(slot));
        assert_eq!(st.last_attempted_slot, Some(slot));
        assert_eq!(should_catch_up(&s, &st, now_utc(100, 9, 0), OFF), None);
    }

    #[test]
    fn slot_state_serde_roundtrip() {
        let st = SlotState {
            last_satisfied_slot: Some(10),
            last_attempted_slot: Some(20),
            attempts_on_last_slot: 1,
        };
        let json = serde_json::to_string(&st).unwrap();
        assert_eq!(
            json,
            r#"{"lastSatisfiedSlot":10,"lastAttemptedSlot":20,"attemptsOnLastSlot":1}"#
        );
        assert_eq!(serde_json::from_str::<SlotState>(&json).unwrap(), st);
        let legacy: SlotState =
            serde_json::from_str(r#"{"lastSatisfiedSlot":10,"lastAttemptedSlot":20}"#).unwrap();
        assert_eq!(legacy.attempts_on_last_slot, 0);
        assert_eq!(
            serde_json::from_str::<SlotState>("{}").unwrap(),
            SlotState::default()
        );
    }

    fn argv_strs(a: &IntakeAction) -> Vec<String> {
        argv(a, Path::new("/x/server.mjs"))
            .into_iter()
            .map(|s: OsString| s.to_string_lossy().into_owned())
            .collect()
    }

    #[test]
    fn argv_run_is_noninteractive() {
        assert_eq!(
            argv_strs(&IntakeAction::Run),
            ["/x/server.mjs", "intake", "run", "--noninteractive"]
        );
        assert_eq!(
            argv_strs(&IntakeAction::Login),
            ["/x/server.mjs", "intake", "login"]
        );
        assert_eq!(
            argv_strs(&IntakeAction::Status),
            ["/x/server.mjs", "intake", "status"]
        );
        assert_eq!(
            argv_strs(&IntakeAction::ScheduleOff),
            ["/x/server.mjs", "intake", "schedule", "off"]
        );
    }

    #[test]
    fn argv_setup_maps_consent_and_skip() {
        let plain = IntakeAction::Setup {
            accept_private_calendar: false,
            skip: BTreeSet::new(),
        };
        assert_eq!(
            argv_strs(&plain),
            ["/x/server.mjs", "intake", "setup", "--noninteractive"]
        );
        let full = IntakeAction::Setup {
            accept_private_calendar: true,
            skip: [Connector::Slack, Connector::Calendar]
                .into_iter()
                .collect(),
        };
        assert_eq!(
            argv_strs(&full),
            [
                "/x/server.mjs",
                "intake",
                "setup",
                "--noninteractive",
                "--accept-unknown-sensitivity",
                "--skip",
                "calendar",
                "--skip",
                "slack"
            ]
        );
    }

    #[test]
    fn argv_never_contains_schedule() {
        let all = [
            IntakeAction::Run,
            IntakeAction::Login,
            IntakeAction::Status,
            IntakeAction::Setup {
                accept_private_calendar: true,
                skip: Connector::ALL.into_iter().collect(),
            },
        ];
        for a in &all {
            assert!(!argv_strs(a).iter().any(|s| s == "--schedule"));
        }
    }

    #[test]
    fn resolve_node_prefers_stored_then_candidates() {
        let cands = vec![PathBuf::from("/a/node"), PathBuf::from("/b/node")];
        let stored = PathBuf::from("/s/node");
        let all = |_: &Path| true;
        assert_eq!(
            resolve_node(Some(&stored), &cands, all),
            Some(stored.clone())
        );
        let only_b = |p: &Path| p == Path::new("/b/node");
        assert_eq!(
            resolve_node(Some(&stored), &cands, only_b),
            Some(PathBuf::from("/b/node"))
        );
        assert_eq!(resolve_node(None, &cands, |_| false), None);
        assert_eq!(
            resolve_node(None, &cands, |p| p == Path::new("/a/node")),
            Some(PathBuf::from("/a/node"))
        );
    }

    #[test]
    fn node_candidates_orders_nvm_newest_first() {
        let v: Vec<String> = ["v18.2.0", "v20.11.1", "v20.9.0", "junk"]
            .iter()
            .map(|s| s.to_string())
            .collect();
        let c = node_candidates(Path::new("/h"), &v);
        assert_eq!(c[0], PathBuf::from("/opt/homebrew/bin/node"));
        assert_eq!(c[1], PathBuf::from("/usr/local/bin/node"));
        assert_eq!(
            c[2],
            PathBuf::from("/h/.nvm/versions/node/v20.11.1/bin/node")
        );
        assert_eq!(
            c[3],
            PathBuf::from("/h/.nvm/versions/node/v20.9.0/bin/node")
        );
        assert_eq!(
            c[4],
            PathBuf::from("/h/.nvm/versions/node/v18.2.0/bin/node")
        );
        assert_eq!(c.len(), 5);
    }

    #[test]
    fn schedule_rejects_bad_times() {
        for bad in [
            r#"{"kind":"daily","times":["24:00"]}"#,
            r#"{"kind":"daily","times":["8:3"]}"#,
            r#"{"kind":"daily","times":["08:60"]}"#,
            r#"{"kind":"twiceDaily","times":["08:00","08:00"]}"#,
            r#"{"kind":"twiceDaily","times":["08:00"]}"#,
            r#"{"kind":"daily","times":[]}"#,
            r#"{"kind":"hourly","times":["08:00"]}"#,
            r#"{"kind":"weekly","times":[]}"#,
        ] {
            assert!(serde_json::from_str::<Schedule>(bad).is_err(), "{bad}");
        }
    }

    #[test]
    fn schedule_serde_roundtrip() {
        for (s, j) in [
            (Schedule::Off, r#"{"kind":"off","times":[]}"#),
            (Schedule::Hourly, r#"{"kind":"hourly","times":[]}"#),
            (
                Schedule::Daily(hm(8, 30)),
                r#"{"kind":"daily","times":["08:30"]}"#,
            ),
            (
                Schedule::TwiceDaily(hm(9, 0), hm(18, 5)),
                r#"{"kind":"twiceDaily","times":["09:00","18:05"]}"#,
            ),
        ] {
            assert_eq!(serde_json::to_string(&s).unwrap(), j);
            assert_eq!(serde_json::from_str::<Schedule>(j).unwrap(), s);
        }
    }
}
