//! Live caption translation with the Mac user's own AWS profile.
//!
//! The main window loads a remote web app, so every command here validates
//! its input. AWS credentials never cross IPC and are never logged; meeting
//! text is never logged (only codes, byte lengths and engine names).

use crate::caption_model::{self, CaptionSettings, FONTS, LANGS};
use crate::interpreter::{self, LLM_MAX_BYTES, MAX_CONTEXT, MAX_TEXT_BYTES, MODEL_ID};
use aws_config::{BehaviorVersion, Region};
use aws_sdk_bedrockruntime::error::{DisplayErrorContext, ProvideErrorMetadata};
use aws_sdk_bedrockruntime::primitives::Blob;
use parking_lot::Mutex;
use serde::Serialize;
use std::collections::{HashMap, VecDeque};
use std::path::PathBuf;
use std::time::{Duration, Instant};
use tauri::{AppHandle, Manager, State};

/// Requests allowed per sliding minute, across all callers.
const RATE_PER_MINUTE: u32 = 600;
/// Input bytes (text plus context) allowed per sliding minute, across all
/// callers. Bounds what a script in the remote page can send to the user's
/// AWS account.
const BYTES_PER_MINUTE: usize = 600_000;
const LLM_TIMEOUT: Duration = Duration::from_secs(6);
const TRANSLATE_TIMEOUT: Duration = Duration::from_secs(6);

#[cfg(test)]
const ERROR_CODES: [&str; 10] = [
    "profile-missing",
    "credentials-expired",
    "model-access",
    "rate-limited",
    "invalid-input",
    "invalid-output",
    "timeout",
    "aws-error",
    "save-failed",
    "window-error",
];

/// Error returned to the page. Messages are short, fixed English strings: no
/// meeting text and no credential material.
#[derive(Debug, Serialize)]
pub struct CaptionError {
    pub code: &'static str,
    pub message: String,
}

impl CaptionError {
    pub fn from_code(code: &'static str) -> Self {
        let message = match code {
            "profile-missing" => "Choose an AWS profile for caption translation.",
            "credentials-expired" => "AWS credentials are missing or expired. Sign in again.",
            "model-access" => "The AWS account cannot use the translation model.",
            "rate-limited" => "Too many translation requests. Try again shortly.",
            "invalid-input" => "The translation request is invalid.",
            "invalid-output" => "The translation result was rejected.",
            "timeout" => "The translation request timed out.",
            "save-failed" => "Caption settings could not be saved.",
            "window-error" => "The caption window could not be updated.",
            _ => "The AWS translation request failed.",
        };
        Self {
            code,
            message: message.to_string(),
        }
    }

    pub(crate) fn invalid(message: &str) -> Self {
        Self {
            code: "invalid-input",
            message: message.to_string(),
        }
    }
}

/// Sliding 60 s window request limiter.
pub struct RateCeiling {
    per_minute: u32,
    seen: VecDeque<Instant>,
}

impl RateCeiling {
    pub fn new(per_minute: u32) -> Self {
        Self {
            per_minute,
            seen: VecDeque::new(),
        }
    }

    pub fn allow(&mut self, now: Instant) -> bool {
        while let Some(&front) = self.seen.front() {
            if now.saturating_duration_since(front) >= Duration::from_secs(60) {
                self.seen.pop_front();
            } else {
                break;
            }
        }
        if self.seen.len() >= self.per_minute as usize {
            return false;
        }
        self.seen.push_back(now);
        true
    }
}

/// Sliding 60 s window input-byte budget. A rejected request is not counted.
pub struct ByteBudget {
    per_minute: usize,
    used: usize,
    seen: VecDeque<(Instant, usize)>,
}

impl ByteBudget {
    pub fn new(per_minute: usize) -> Self {
        Self {
            per_minute,
            used: 0,
            seen: VecDeque::new(),
        }
    }

    pub fn allow(&mut self, now: Instant, bytes: usize) -> bool {
        while let Some(&(at, n)) = self.seen.front() {
            if now.saturating_duration_since(at) >= Duration::from_secs(60) {
                self.seen.pop_front();
                self.used -= n;
            } else {
                break;
            }
        }
        if self.used.saturating_add(bytes) > self.per_minute {
            return false;
        }
        self.used += bytes;
        self.seen.push_back((now, bytes));
        true
    }
}

/// Input bytes a request sends: the text plus every context entry.
pub fn request_bytes(text: &str, context: &[String]) -> usize {
    text.len() + context.iter().map(String::len).sum::<usize>()
}

/// Rejects a translation into `target` unless its overlay window is open
/// (`is_open` is asked about the window label).
pub fn require_open_window(
    target: &str,
    is_open: impl Fn(&str) -> bool,
) -> Result<(), CaptionError> {
    let label = caption_model::window_label(target)
        .ok_or_else(|| CaptionError::invalid("Unsupported language."))?;
    if is_open(label) {
        Ok(())
    } else {
        Err(CaptionError::invalid(
            "Open the caption window for this language first.",
        ))
    }
}

/// Maps an AWS error code and message to one of the page-facing codes.
pub fn classify(error_code: Option<&str>, message: &str) -> &'static str {
    let code = error_code.unwrap_or_default();
    let lower = message.to_lowercase();
    if code.starts_with("ExpiredToken")
        || code.starts_with("UnrecognizedClient")
        || code.starts_with("InvalidSignature")
        || lower.contains("credential")
    {
        "credentials-expired"
    } else if code.starts_with("AccessDenied")
        || code.starts_with("ResourceNotFound")
        || (code == "ValidationException" && lower.contains("model"))
    {
        "model-access"
    } else {
        "aws-error"
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Engine {
    Llm,
    Translate,
}

impl Engine {
    fn name(self) -> &'static str {
        match self {
            Engine::Llm => "llm",
            Engine::Translate => "translate",
        }
    }
}

/// Final sentences that fit the model budget use the LLM; everything else
/// uses Amazon Translate.
pub fn choose_engine(is_final: bool, text_bytes: usize) -> Engine {
    if is_final && text_bytes <= LLM_MAX_BYTES {
        Engine::Llm
    } else {
        Engine::Translate
    }
}

/// Longest prefix of `s` that is at most `max` bytes and ends on a char boundary.
fn truncate_bytes(s: &str, max: usize) -> &str {
    if s.len() <= max {
        return s;
    }
    let mut end = max;
    while !s.is_char_boundary(end) {
        end -= 1;
    }
    &s[..end]
}

/// Validates a request and returns the context to send (last `MAX_CONTEXT`
/// entries, each cut to `LLM_MAX_BYTES`).
pub fn check_input(
    text: &str,
    source: &str,
    target: &str,
    context: &[String],
) -> Result<Vec<String>, CaptionError> {
    if !LANGS.contains(&source) || !LANGS.contains(&target) {
        return Err(CaptionError::invalid("Unsupported language."));
    }
    if source == target {
        return Err(CaptionError::invalid("Source and target languages match."));
    }
    if text.trim().is_empty() {
        return Err(CaptionError::invalid("Text is empty."));
    }
    if text.len() > MAX_TEXT_BYTES {
        return Err(CaptionError::invalid("Text is too long."));
    }
    let skip = context.len().saturating_sub(MAX_CONTEXT);
    Ok(context[skip..]
        .iter()
        .map(|c| truncate_bytes(c, LLM_MAX_BYTES).to_string())
        .collect())
}

#[derive(Clone)]
struct Clients {
    bedrock: aws_sdk_bedrockruntime::Client,
    translate: aws_sdk_translate::Client,
}

/// Managed state: current settings, the settings file, cached AWS clients
/// keyed by `(profile, region)` and the request and byte limiters.
pub struct CaptionState {
    settings: Mutex<CaptionSettings>,
    path: PathBuf,
    clients: Mutex<HashMap<(String, String), Clients>>,
    rate: Mutex<RateCeiling>,
    budget: Mutex<ByteBudget>,
}

impl CaptionState {
    /// Loads the saved settings, falling back to defaults when the file is
    /// missing, corrupt or invalid.
    pub fn new(path: PathBuf) -> Self {
        let loaded = caption_model::load(&path);
        let settings = if caption_model::validate_settings(&loaded).is_ok() {
            loaded
        } else {
            CaptionSettings::default()
        };
        Self {
            settings: Mutex::new(settings),
            path,
            clients: Mutex::new(HashMap::new()),
            rate: Mutex::new(RateCeiling::new(RATE_PER_MINUTE)),
            budget: Mutex::new(ByteBudget::new(BYTES_PER_MINUTE)),
        }
    }

    async fn clients(&self, profile: &str, region: &str) -> Clients {
        let key = (profile.to_string(), region.to_string());
        if let Some(c) = self.clients.lock().get(&key) {
            return c.clone();
        }
        let config = aws_config::defaults(BehaviorVersion::latest())
            .profile_name(profile)
            .region(Region::new(region.to_string()))
            .load()
            .await;
        let built = Clients {
            bedrock: aws_sdk_bedrockruntime::Client::new(&config),
            translate: aws_sdk_translate::Client::new(&config),
        };
        self.clients.lock().insert(key, built.clone());
        built
    }

    /// A copy of the current settings.
    pub fn snapshot(&self) -> CaptionSettings {
        self.settings.lock().clone()
    }

    /// Applies `change` to a copy of the settings, validates it, writes it
    /// when `write` is set, then commits it. The lock is held across the
    /// write (there is no await inside) so saves never share the fixed
    /// temp-file name. Callers must not hold other locks the main thread
    /// takes, and must not call this while waiting on the main thread.
    pub fn update(
        &self,
        write: bool,
        change: impl FnOnce(&mut CaptionSettings),
    ) -> Result<CaptionSettings, CaptionError> {
        let mut guard = self.settings.lock();
        let mut next = guard.clone();
        change(&mut next);
        caption_model::validate_settings(&next).map_err(|e| CaptionError::invalid(&e))?;
        if write {
            self.write(&next)?;
        }
        *guard = next.clone();
        Ok(next)
    }

    /// Writes the current settings (used for throttled layout saves).
    pub fn persist(&self) -> Result<(), CaptionError> {
        let guard = self.settings.lock();
        self.write(&guard)
    }

    fn write(&self, settings: &CaptionSettings) -> Result<(), CaptionError> {
        if let Some(dir) = self.path.parent() {
            let _ = std::fs::create_dir_all(dir);
        }
        caption_model::save(&self.path, settings).map_err(|e| {
            log::warn!("caption settings save failed: {:?}", e.kind());
            CaptionError::from_code("save-failed")
        })
    }

    fn drop_client(&self, profile: &str, region: &str) {
        self.clients
            .lock()
            .remove(&(profile.to_string(), region.to_string()));
    }
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
pub struct SettingsView {
    settings: CaptionSettings,
    profiles: Vec<String>,
    fonts: Vec<String>,
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
pub struct TranslateResult {
    translated_text: String,
    engine: &'static str,
}

#[tauri::command]
pub async fn caption_settings_get(
    state: State<'_, CaptionState>,
) -> Result<SettingsView, CaptionError> {
    let settings = state.settings.lock().clone();
    Ok(SettingsView {
        settings,
        profiles: crate::aws_profiles::list(),
        fonts: FONTS.iter().map(|f| f.to_string()).collect(),
    })
}

/// Validates, saves and commits the profile, region and extra language, then
/// drops the cached AWS clients.
fn apply_settings(
    state: &CaptionState,
    profile: Option<String>,
    region: String,
    extra_language: Option<String>,
) -> Result<CaptionSettings, CaptionError> {
    // `update` holds the lock across validation and the file write so
    // concurrent saves never share the fixed temp-file name.
    let next = state.update(true, |s| {
        s.profile = profile;
        s.region = region;
        s.extra_language = extra_language;
    })?;
    state.clients.lock().clear();
    Ok(next)
}

#[tauri::command]
pub async fn caption_settings_set(
    state: State<'_, CaptionState>,
    profile: Option<String>,
    region: String,
    extra_language: Option<String>,
) -> Result<CaptionSettings, CaptionError> {
    if let Some(p) = &profile {
        if !crate::aws_profiles::list().contains(p) {
            return Err(CaptionError::from_code("profile-missing"));
        }
    }
    apply_settings(&state, profile, region, extra_language)
}

fn sdk_code<E, R>(err: &aws_sdk_bedrockruntime::error::SdkError<E, R>) -> &'static str
where
    E: ProvideErrorMetadata + std::error::Error + 'static,
    R: std::fmt::Debug,
{
    classify(err.code(), &DisplayErrorContext(err).to_string())
}

async fn run_llm(
    clients: &Clients,
    text: &str,
    source: &str,
    target: &str,
    context: &[String],
) -> Result<String, &'static str> {
    let body = interpreter::build_request(text, source, target, context);
    let call = clients
        .bedrock
        .invoke_model()
        .model_id(MODEL_ID)
        .content_type("application/json")
        .accept("application/json")
        .body(Blob::new(body))
        .send();
    let out = match tokio::time::timeout(LLM_TIMEOUT, call).await {
        Err(_) => return Err("timeout"),
        Ok(Err(e)) => return Err(sdk_code(&e)),
        Ok(Ok(o)) => o,
    };
    let translated =
        interpreter::parse_response(out.body().as_ref()).map_err(|_| "invalid-output")?;
    interpreter::validate(text, &translated, target).map_err(|_| "invalid-output")?;
    Ok(translated)
}

async fn run_translate(
    clients: &Clients,
    text: &str,
    source: &str,
    target: &str,
) -> Result<String, &'static str> {
    let call = clients
        .translate
        .translate_text()
        .text(text)
        .source_language_code(source)
        .target_language_code(target)
        .send();
    let out = match tokio::time::timeout(TRANSLATE_TIMEOUT, call).await {
        Err(_) => return Err("timeout"),
        Ok(Err(e)) => return Err(sdk_code(&e)),
        Ok(Ok(o)) => o,
    };
    let translated = out.translated_text().trim().to_string();
    if translated.is_empty() {
        return Err("invalid-output");
    }
    Ok(translated)
}

#[tauri::command]
pub async fn caption_translate(
    app: AppHandle,
    state: State<'_, CaptionState>,
    text: String,
    source_lang: String,
    target_lang: String,
    is_final: bool,
    context: Vec<String>,
) -> Result<TranslateResult, CaptionError> {
    if !state.rate.lock().allow(Instant::now()) {
        return Err(CaptionError::from_code("rate-limited"));
    }
    require_open_window(&target_lang, |label| {
        app.get_webview_window(label).is_some()
    })?;
    let context = check_input(&text, &source_lang, &target_lang, &context)?;
    if !state
        .budget
        .lock()
        .allow(Instant::now(), request_bytes(&text, &context))
    {
        return Err(CaptionError::from_code("rate-limited"));
    }
    let (profile, region) = {
        let s = state.settings.lock();
        (s.profile.clone(), s.region.clone())
    };
    let profile = profile.ok_or_else(|| CaptionError::from_code("profile-missing"))?;
    if !crate::aws_profiles::list().contains(&profile) {
        return Err(CaptionError::from_code("profile-missing"));
    }
    let clients = state.clients(&profile, &region).await;

    let engine = choose_engine(is_final, text.len());
    let fail = |code: &'static str| {
        if code == "credentials-expired" {
            state.drop_client(&profile, &region);
        }
        log::warn!("caption translate failed: code={code} bytes={}", text.len());
        CaptionError::from_code(code)
    };

    if engine == Engine::Llm {
        match run_llm(&clients, &text, &source_lang, &target_lang, &context).await {
            Ok(translated_text) => {
                log::debug!("caption translate ok: engine=llm bytes={}", text.len());
                return Ok(TranslateResult {
                    translated_text,
                    engine: "llm",
                });
            }
            Err(code @ ("credentials-expired" | "model-access")) => return Err(fail(code)),
            Err(code) => {
                log::warn!("caption llm fallback: code={code} bytes={}", text.len());
            }
        }
    }
    match run_translate(&clients, &text, &source_lang, &target_lang).await {
        Ok(translated_text) => {
            log::debug!(
                "caption translate ok: engine=translate bytes={}",
                text.len()
            );
            Ok(TranslateResult {
                translated_text,
                engine: Engine::Translate.name(),
            })
        }
        Err(code) => Err(fail(code)),
    }
}

/// Registers the managed state once the app config directory is known.
pub fn manage(app: &tauri::App) -> Result<(), Box<dyn std::error::Error>> {
    let path = app.path().app_config_dir()?.join("caption-overlay.json");
    app.manage(CaptionState::new(path));
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::time::{Duration, Instant};

    #[test]
    fn rate_ceiling_blocks_601st() {
        let mut r = RateCeiling::new(600);
        let now = Instant::now();
        assert!((0..600).all(|_| r.allow(now)));
        assert!(!r.allow(now));
    }

    #[test]
    fn rate_ceiling_recovers_after_window() {
        let mut r = RateCeiling::new(2);
        let t0 = Instant::now();
        assert!(r.allow(t0));
        assert!(r.allow(t0));
        assert!(!r.allow(t0 + Duration::from_secs(59)));
        assert!(r.allow(t0 + Duration::from_secs(61)));
    }

    #[test]
    fn byte_budget_limits_input_per_minute() {
        let mut b = ByteBudget::new(600_000);
        let t0 = Instant::now();
        assert!(b.allow(t0, 400_000));
        assert!(b.allow(t0, 200_000));
        assert!(!b.allow(t0, 1), "budget spent");
        assert!(!b.allow(t0 + Duration::from_secs(59), 1));
        // A rejected request does not count against the budget.
        assert!(b.allow(t0 + Duration::from_secs(60), 600_000));
        let mut fresh = ByteBudget::new(600_000);
        assert!(!fresh.allow(t0, 600_001));
        assert!(fresh.allow(t0, 600_000));
    }

    #[test]
    fn request_bytes_count_text_and_context() {
        assert_eq!(
            request_bytes("가나", &["ab".to_string(), "c".to_string()]),
            9
        );
        assert_eq!(request_bytes("x", &[]), 1);
    }

    #[test]
    fn translation_needs_the_target_window_open() {
        let open = |label: &str| label == "caption-en";
        assert!(require_open_window("en", open).is_ok());
        let err = require_open_window("ko", open).unwrap_err();
        assert_eq!(err.code, "invalid-input");
        let m = err.message.to_lowercase();
        for bad in ["not found", "not allowed", "unknown command"] {
            assert!(!m.contains(bad), "{m}");
        }
        assert_eq!(
            require_open_window("fr", |_| true).unwrap_err().code,
            "invalid-input"
        );
    }

    #[test]
    fn failed_settings_save_reports_save_failed() {
        let dir = std::env::temp_dir().join(format!("caption_save_{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        let blocker = dir.join("blocker");
        std::fs::write(&blocker, b"x").unwrap();
        // The parent is a regular file, so the save cannot succeed.
        let state = CaptionState::new(blocker.join("caption-overlay.json"));
        let err = apply_settings(&state, None, "us-east-1".into(), None).unwrap_err();
        assert_eq!(err.code, "save-failed");
        let _ = std::fs::remove_dir_all(&dir);
    }

    #[test]
    fn classify_maps_expired() {
        assert_eq!(
            classify(Some("ExpiredTokenException"), ""),
            "credentials-expired"
        );
        assert_eq!(
            classify(Some("UnrecognizedClientException"), ""),
            "credentials-expired"
        );
        assert_eq!(
            classify(Some("InvalidSignatureException"), ""),
            "credentials-expired"
        );
        assert_eq!(
            classify(None, "failed to load credentials: provider error"),
            "credentials-expired"
        );
    }

    #[test]
    fn classify_maps_access() {
        assert_eq!(classify(Some("AccessDeniedException"), ""), "model-access");
        assert_eq!(
            classify(Some("ResourceNotFoundException"), ""),
            "model-access"
        );
        assert_eq!(
            classify(
                Some("ValidationException"),
                "The provided model identifier is invalid"
            ),
            "model-access"
        );
        assert_eq!(
            classify(Some("ValidationException"), "bad body"),
            "aws-error"
        );
    }

    #[test]
    fn classify_defaults_to_aws_error() {
        assert_eq!(
            classify(Some("ThrottlingException"), "slow down"),
            "aws-error"
        );
        assert_eq!(classify(None, "dispatch failure"), "aws-error");
    }

    #[test]
    fn input_rules() {
        let ctx: Vec<String> = Vec::new();
        assert!(check_input("hi", "ko", "ko", &ctx).is_err());
        assert!(check_input("hi", "fr", "en", &ctx).is_err());
        assert!(check_input("hi", "ko", "fr", &ctx).is_err());
        assert!(check_input("   ", "ko", "en", &ctx).is_err());
        assert!(check_input(&"a".repeat(9001), "ko", "en", &ctx).is_err());
        assert!(check_input(&"a".repeat(9000), "ko", "en", &ctx).is_ok());
        let err = check_input("hi", "fr", "en", &ctx).unwrap_err();
        assert_eq!(err.code, "invalid-input");

        let many: Vec<String> = (0..8).map(|i| format!("c{i}")).collect();
        let kept = check_input("hi", "ko", "en", &many).unwrap();
        assert_eq!(kept, vec!["c3", "c4", "c5", "c6", "c7"]);

        let long = vec!["가".repeat(2000)];
        let kept = check_input("hi", "ko", "en", &long).unwrap();
        assert!(kept[0].len() <= crate::interpreter::LLM_MAX_BYTES);
        assert!(!kept[0].is_empty());
    }

    #[test]
    fn engine_choice() {
        assert_eq!(choose_engine(true, 10), Engine::Llm);
        assert_eq!(
            choose_engine(true, crate::interpreter::LLM_MAX_BYTES),
            Engine::Llm
        );
        assert_eq!(
            choose_engine(true, crate::interpreter::LLM_MAX_BYTES + 1),
            Engine::Translate
        );
        assert_eq!(choose_engine(false, 10), Engine::Translate);
    }

    #[test]
    fn error_messages_avoid_skew_words() {
        for code in ERROR_CODES {
            let m = CaptionError::from_code(code).message.to_lowercase();
            assert!(!m.is_empty(), "{code}");
            for bad in ["not found", "not allowed", "unknown command"] {
                assert!(!m.contains(bad), "{code}: {m}");
            }
        }
    }
}
