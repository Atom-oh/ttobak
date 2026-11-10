//! Pure caption overlay model: styles, window layouts, settings validation and
//! persistence, and window placement. No Tauri imports.
#![allow(dead_code)] // the overlay windows (caption_overlay.rs) consume the rest

use serde::{Deserialize, Serialize};
use std::collections::BTreeMap;
use std::path::Path;

pub const LANGS: [&str; 4] = ["ko", "en", "ja", "zh"];

pub const FONTS: [&str; 6] = [
    "Apple SD Gothic Neo",
    "PingFang SC",
    "Hiragino Sans",
    "Helvetica Neue",
    "Noto Sans CJK KR",
    "system-ui",
];

/// Window label for a caption language; `None` for unknown languages.
pub fn window_label(lang: &str) -> Option<&'static str> {
    match lang {
        "ko" => Some("caption-ko"),
        "en" => Some("caption-en"),
        "ja" => Some("caption-ja"),
        "zh" => Some("caption-zh"),
        _ => None,
    }
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase", default)]
pub struct CaptionStyle {
    pub font_family: String,
    pub font_size: f64,
    pub font_color: String,
    pub outline_color: String,
    pub outline_width: f64,
    pub background_opacity: f64,
}

impl Default for CaptionStyle {
    fn default() -> Self {
        Self {
            font_family: "Apple SD Gothic Neo".into(),
            font_size: 48.0,
            font_color: "#FFFFFF".into(),
            outline_color: "#000000".into(),
            outline_width: 3.0,
            background_opacity: 0.4,
        }
    }
}

/// Matches `^#[0-9A-Fa-f]{6}$`.
fn is_hex_color(s: &str) -> bool {
    s.len() == 7 && s.starts_with('#') && s[1..].bytes().all(|b| b.is_ascii_hexdigit())
}

fn in_range(v: f64, lo: f64, hi: f64) -> bool {
    v.is_finite() && (lo..=hi).contains(&v)
}

pub fn validate_style(style: &CaptionStyle) -> Result<(), String> {
    if !FONTS.contains(&style.font_family.as_str()) {
        return Err("unsupported font family".into());
    }
    if !in_range(style.font_size, 16.0, 160.0) {
        return Err("font size must be between 16 and 160".into());
    }
    if !is_hex_color(&style.font_color) || !is_hex_color(&style.outline_color) {
        return Err("colors must be #RRGGBB".into());
    }
    if !in_range(style.outline_width, 0.0, 12.0) {
        return Err("outline width must be between 0 and 12".into());
    }
    if !in_range(style.background_opacity, 0.0, 1.0) {
        return Err("background opacity must be between 0 and 1".into());
    }
    Ok(())
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase", default)]
pub struct WindowLayout {
    pub open: bool,
    pub style: CaptionStyle,
    pub x: Option<f64>,
    pub y: Option<f64>,
    pub width: f64,
    pub height: f64,
}

impl Default for WindowLayout {
    fn default() -> Self {
        Self {
            open: false,
            style: CaptionStyle::default(),
            x: None,
            y: None,
            width: 900.0,
            height: 180.0,
        }
    }
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase", default)]
pub struct CaptionSettings {
    pub profile: Option<String>,
    pub region: String,
    pub extra_language: Option<String>,
    pub windows: BTreeMap<String, WindowLayout>,
}

impl Default for CaptionSettings {
    fn default() -> Self {
        Self {
            profile: None,
            region: "ap-northeast-2".into(),
            extra_language: None,
            windows: BTreeMap::new(),
        }
    }
}

/// Matches `^[A-Za-z0-9_.@-]{1,64}$`.
fn is_valid_profile(s: &str) -> bool {
    (1..=64).contains(&s.len())
        && s.bytes()
            .all(|b| b.is_ascii_alphanumeric() || matches!(b, b'_' | b'.' | b'@' | b'-'))
}

/// Matches `^[a-z]{2}(-[a-z]+)+-\d$`.
fn is_valid_region(s: &str) -> bool {
    let parts: Vec<&str> = s.split('-').collect();
    if parts.len() < 3 {
        return false;
    }
    let lower = |p: &str| !p.is_empty() && p.bytes().all(|b| b.is_ascii_lowercase());
    let (first, last) = (parts[0], parts[parts.len() - 1]);
    first.len() == 2
        && lower(first)
        && parts[1..parts.len() - 1].iter().all(|p| lower(p))
        && last.len() == 1
        && last.bytes().all(|b| b.is_ascii_digit())
}

pub fn validate_settings(s: &CaptionSettings) -> Result<(), String> {
    if !matches!(s.extra_language.as_deref(), None | Some("ja") | Some("zh")) {
        return Err("extra language must be ja or zh".into());
    }
    for (lang, w) in &s.windows {
        if !LANGS.contains(&lang.as_str()) {
            return Err(format!("unknown caption language: {lang}"));
        }
        if (lang == "ja" || lang == "zh") && w.open && s.extra_language.as_deref() != Some(lang) {
            return Err(format!("{lang} window requires extra language {lang}"));
        }
        validate_style(&w.style)?;
    }
    if let Some(p) = &s.profile {
        if !is_valid_profile(p) {
            return Err("invalid AWS profile name".into());
        }
    }
    if !is_valid_region(&s.region) {
        return Err("invalid AWS region".into());
    }
    Ok(())
}

/// Loads settings; a missing or corrupt file yields defaults.
pub fn load(path: &Path) -> CaptionSettings {
    std::fs::read(path)
        .ok()
        .and_then(|b| serde_json::from_slice(&b).ok())
        .unwrap_or_default()
}

/// Writes a temp file next to `path`, then renames it into place.
pub fn save(path: &Path, s: &CaptionSettings) -> std::io::Result<()> {
    let json = serde_json::to_vec_pretty(s).map_err(std::io::Error::other)?;
    let mut tmp = path.as_os_str().to_owned();
    tmp.push(".tmp");
    let tmp = std::path::PathBuf::from(tmp);
    std::fs::write(&tmp, json)?;
    std::fs::rename(&tmp, path).inspect_err(|_| {
        let _ = std::fs::remove_file(&tmp);
    })
}

/// Saved position if the window rect intersects any monitor `(x, y, w, h)`,
/// else bottom-centered on `primary` with an 80pt bottom margin.
pub fn placement(
    saved: Option<(f64, f64)>,
    size: (f64, f64),
    monitors: &[(f64, f64, f64, f64)],
    primary: (f64, f64, f64, f64),
) -> (f64, f64) {
    if let Some((x, y)) = saved {
        let visible = monitors.iter().any(|&(mx, my, mw, mh)| {
            x < mx + mw && x + size.0 > mx && y < my + mh && y + size.1 > my
        });
        if visible {
            return (x, y);
        }
    }
    let (px, py, pw, ph) = primary;
    (px + (pw - size.0) / 2.0, py + ph - size.1 - 80.0)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn tmp(name: &str) -> std::path::PathBuf {
        let d = std::env::temp_dir().join(format!("caption_model_{}_{}", name, std::process::id()));
        std::fs::create_dir_all(&d).unwrap();
        d.join("captions.json")
    }

    fn open_win() -> WindowLayout {
        WindowLayout {
            open: true,
            ..WindowLayout::default()
        }
    }

    #[test]
    fn labels_cover_only_known_languages() {
        for l in LANGS {
            assert_eq!(window_label(l).unwrap(), format!("caption-{l}"));
        }
        assert_eq!(window_label("ko"), Some("caption-ko"));
        assert_eq!(window_label("fr"), None);
        assert_eq!(window_label(""), None);
    }

    #[test]
    fn default_style_is_valid() {
        assert_eq!(validate_style(&CaptionStyle::default()), Ok(()));
    }

    #[test]
    fn style_rejects_out_of_range() {
        let base = CaptionStyle::default();
        let bad = [
            CaptionStyle {
                font_size: 15.9,
                ..base.clone()
            },
            CaptionStyle {
                font_size: 161.0,
                ..base.clone()
            },
            CaptionStyle {
                background_opacity: 1.01,
                ..base.clone()
            },
            CaptionStyle {
                outline_width: -1.0,
                ..base.clone()
            },
            CaptionStyle {
                font_color: "red".into(),
                ..base.clone()
            },
            CaptionStyle {
                outline_color: "#12345".into(),
                ..base.clone()
            },
            CaptionStyle {
                font_family: "Comic Sans".into(),
                ..base.clone()
            },
            CaptionStyle {
                font_size: f64::NAN,
                ..base.clone()
            },
            CaptionStyle {
                outline_width: f64::INFINITY,
                ..base.clone()
            },
        ];
        for s in bad {
            assert!(validate_style(&s).is_err(), "{s:?}");
        }
    }

    #[test]
    fn settings_reject_two_extra_languages() {
        let mut s = CaptionSettings {
            extra_language: Some("ja".into()),
            ..CaptionSettings::default()
        };
        s.windows.insert("ja".into(), open_win());
        s.windows.insert("zh".into(), open_win());
        assert!(validate_settings(&s).is_err());
        s.windows.remove("ja");
        s.extra_language = Some("zh".into());
        assert_eq!(validate_settings(&s), Ok(()));
    }

    #[test]
    fn settings_reject_bad_profile() {
        for p in ["a b", "../x", "", &"a".repeat(65)] {
            let s = CaptionSettings {
                profile: Some(p.into()),
                ..CaptionSettings::default()
            };
            assert!(validate_settings(&s).is_err(), "{p}");
        }
        let ok = CaptionSettings {
            profile: Some("my.prof_1@x-y".into()),
            ..CaptionSettings::default()
        };
        assert_eq!(validate_settings(&ok), Ok(()));
        let bad_region = CaptionSettings {
            region: "US-east-1".into(),
            ..CaptionSettings::default()
        };
        assert!(validate_settings(&bad_region).is_err());
        assert_eq!(CaptionSettings::default().region, "ap-northeast-2");
    }

    #[test]
    fn corrupt_file_loads_default() {
        let p = tmp("corrupt");
        std::fs::write(&p, b"{not json").unwrap();
        assert_eq!(load(&p), CaptionSettings::default());
        assert_eq!(
            load(&p.with_file_name("missing.json")),
            CaptionSettings::default()
        );
    }

    #[test]
    fn save_then_load_round_trips() {
        let p = tmp("roundtrip");
        let mut s = CaptionSettings {
            profile: Some("work".into()),
            extra_language: Some("ja".into()),
            ..CaptionSettings::default()
        };
        s.windows.insert(
            "ja".into(),
            WindowLayout {
                x: Some(10.0),
                y: Some(20.0),
                ..open_win()
            },
        );
        save(&p, &s).unwrap();
        assert_eq!(load(&p), s);
    }

    #[test]
    fn off_screen_position_falls_back() {
        let mon = [(0.0, 0.0, 1440.0, 900.0)];
        let prim = mon[0];
        assert_eq!(
            placement(Some((5000.0, 5000.0)), (900.0, 180.0), &mon, prim),
            (270.0, 640.0)
        );
        assert_eq!(
            placement(Some((100.0, 100.0)), (900.0, 180.0), &mon, prim),
            (100.0, 100.0)
        );
        assert_eq!(placement(None, (900.0, 180.0), &mon, prim), (270.0, 640.0));
    }
}
