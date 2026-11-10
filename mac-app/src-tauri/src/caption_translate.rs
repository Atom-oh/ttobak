//! Live caption translation with the Mac user's own AWS profile.
//!
//! The main window loads a remote web app, so every command here validates
//! its input. AWS credentials never cross IPC and are never logged; meeting
//! text is never logged (only codes, byte lengths and engine names).

use crate::caption_model::{self, CaptionSettings, FONTS, LANGS};
use crate::interpreter::{self, LLM_MAX_BYTES, MAX_CONTEXT, MAX_TEXT_BYTES, MODEL_ID};
use aws_config::profile::ProfileFileCredentialsProvider;
use aws_config::provider_config::ProviderConfig;
use aws_config::{BehaviorVersion, Region};
use aws_sdk_bedrockruntime::error::{DisplayErrorContext, ProvideErrorMetadata};
use aws_sdk_bedrockruntime::primitives::Blob;
use parking_lot::Mutex;
use serde::Serialize;
use std::collections::{HashMap, VecDeque};
use std::path::PathBuf;
use std::sync::atomic::{AtomicBool, Ordering};
use std::time::{Duration, Instant};
use tauri::{AppHandle, Manager, State};

/// Requests allowed per sliding minute, across all callers.
const RATE_PER_MINUTE: u32 = 600;
const LLM_TIMEOUT: Duration = Duration::from_secs(6);
const TRANSLATE_TIMEOUT: Duration = Duration::from_secs(6);

#[cfg(test)]
const ERROR_CODES: [&str; 11] = [
    "profile-missing",
    "credentials-expired",
    "model-access",
    "rate-limited",
    "invalid-input",
    "invalid-output",
    "timeout",
    "aws-error",
    "save-failed",
    "confirmation-declined",
    "unavailable",
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
            "confirmation-declined" => "The AWS profile change was cancelled.",
            "unavailable" => "Captions are unavailable on this Mac.",
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

/// Credentials for exactly one named profile from the shared config and
/// credentials files (static keys, SSO, `credential_process`, assume-role).
///
/// The SDK default chain is deliberately not used: it reads environment
/// variables (`AWS_ACCESS_KEY_ID`, web identity, container and instance
/// metadata) before the profile, so an app launched from a terminal with
/// exported keys would bill and send meeting text to that identity while the
/// settings name another profile. A profile that itself sets
/// `credential_source` still follows that explicit choice.
// `ProfileFiles` is a deprecated alias of `aws_runtime`'s `EnvConfigFiles`,
// which aws-config does not otherwise re-export; `files` is only set by tests.
#[allow(deprecated)]
pub fn profile_credentials(
    profile: &str,
    region: &str,
    files: Option<aws_config::profile::profile_file::ProfileFiles>,
) -> ProfileFileCredentialsProvider {
    let config =
        ProviderConfig::without_region().with_region(Some(Region::new(region.to_string())));
    let mut builder = ProfileFileCredentialsProvider::builder()
        .configure(&config)
        .profile_name(profile);
    if let Some(files) = files {
        builder = builder.profile_files(files);
    }
    builder.build()
}

/// Whether changing the AWS profile or region needs the user's native
/// confirmation. Clearing the profile never does; choosing a profile, or a
/// different profile or region while one is chosen, always does.
pub fn needs_confirmation(
    old_profile: Option<&str>,
    old_region: &str,
    new_profile: Option<&str>,
    new_region: &str,
) -> bool {
    match new_profile {
        None => false,
        Some(next) => old_profile != Some(next) || old_region != new_region,
    }
}

const CONFIRM_ALLOW: &str = "허용";
const CONFIRM_CANCEL: &str = "취소";

/// Body of the native confirmation. Profile names and regions are validated
/// to plain ASCII before this is shown.
pub fn confirmation_message(profile: &str, region: &str) -> String {
    format!(
        "TTOBAK이 AWS 프로필 '{profile}' (리전 {region})으로 자막을 번역하려고 합니다. \
         이 계정에 요금이 청구될 수 있어요. 허용할까요?"
    )
}

/// Shows the confirmation as a sheet on the main window, raised from Rust
/// so the page cannot draw or answer it. Never blocks a thread: the sheet is
/// started on the main thread and its future is awaited on the async
/// runtime. Resolves `false` for 취소, a closed window or any failure.
#[cfg(target_os = "macos")]
async fn confirm_aws_change(app: &AppHandle, profile: &str, region: &str) -> bool {
    let (tx, rx) = tokio::sync::oneshot::channel();
    let message = confirmation_message(profile, region);
    let handle = app.clone();
    let posted = app.run_on_main_thread(move || {
        let mut dialog = rfd::AsyncMessageDialog::new()
            .set_level(rfd::MessageLevel::Warning)
            .set_title("TTOBAK")
            .set_description(message)
            .set_buttons(rfd::MessageButtons::OkCancelCustom(
                CONFIRM_ALLOW.to_string(),
                CONFIRM_CANCEL.to_string(),
            ));
        if let Some(window) = handle.get_webview_window("main") {
            dialog = dialog.set_parent(&window);
        }
        let shown = dialog.show();
        tauri::async_runtime::spawn(async move {
            let _ = tx.send(shown.await);
        });
    });
    if posted.is_err() {
        return false;
    }
    matches!(rx.await, Ok(rfd::MessageDialogResult::Custom(label)) if label == CONFIRM_ALLOW)
}

/// No native dialog off macOS, so a change that needs one is declined.
#[cfg(not(target_os = "macos"))]
async fn confirm_aws_change(_app: &AppHandle, _profile: &str, _region: &str) -> bool {
    false
}

/// Managed state: current settings, the settings file, cached AWS clients
/// keyed by `(profile, region)` and the request limiter. Without a settings
/// file (the config directory could not be resolved) every caption command
/// returns `unavailable`; recording and upload are unaffected.
pub struct CaptionState {
    settings: Mutex<CaptionSettings>,
    path: Option<PathBuf>,
    clients: Mutex<HashMap<(String, String), Clients>>,
    rate: Mutex<RateCeiling>,
    /// Set while a `caption_settings_set` call (and its confirmation) runs.
    changing: AtomicBool,
}

/// Clears `CaptionState::changing` when a settings change ends.
struct ChangeGuard<'a>(&'a AtomicBool);

impl Drop for ChangeGuard<'_> {
    fn drop(&mut self) {
        self.0.store(false, Ordering::Release);
    }
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
        Self::with_path(settings, Some(path))
    }

    /// State whose commands all return `unavailable`.
    pub fn unavailable() -> Self {
        Self::with_path(CaptionSettings::default(), None)
    }

    fn with_path(settings: CaptionSettings, path: Option<PathBuf>) -> Self {
        Self {
            settings: Mutex::new(settings),
            path,
            clients: Mutex::new(HashMap::new()),
            rate: Mutex::new(RateCeiling::new(RATE_PER_MINUTE)),
            changing: AtomicBool::new(false),
        }
    }

    /// Fails with `unavailable` when there is no settings file.
    pub fn require_available(&self) -> Result<(), CaptionError> {
        if self.path.is_some() {
            Ok(())
        } else {
            Err(CaptionError::from_code("unavailable"))
        }
    }

    /// Starts a settings change, or `None` while another one (possibly
    /// waiting on its confirmation) is running.
    fn begin_change(&self) -> Option<ChangeGuard<'_>> {
        self.changing
            .compare_exchange(false, true, Ordering::AcqRel, Ordering::Acquire)
            .ok()
            .map(|_| ChangeGuard(&self.changing))
    }

    async fn clients(&self, profile: &str, region: &str) -> Clients {
        let key = (profile.to_string(), region.to_string());
        if let Some(c) = self.clients.lock().get(&key) {
            return c.clone();
        }
        let config = aws_config::defaults(BehaviorVersion::latest())
            .profile_name(profile)
            .region(Region::new(region.to_string()))
            .credentials_provider(profile_credentials(profile, region, None))
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

    fn write(&self, settings: &CaptionSettings) -> Result<(), CaptionError> {
        let path = self
            .path
            .as_ref()
            .ok_or_else(|| CaptionError::from_code("unavailable"))?;
        if let Some(dir) = path.parent() {
            let _ = std::fs::create_dir_all(dir);
        }
        caption_model::save(path, settings).map_err(|e| {
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
    state.require_available()?;
    let settings = state.snapshot();
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

/// Saves the profile, region and extra language. Choosing or changing the
/// AWS profile or region first asks the user in a native dialog (the remote
/// page must not silently switch which account is billed); declining leaves
/// the settings unchanged and returns `confirmation-declined`. A call made
/// while another change is still running (its dialog open) is rejected.
#[tauri::command]
pub async fn caption_settings_set(
    app: AppHandle,
    state: State<'_, CaptionState>,
    profile: Option<String>,
    region: String,
    extra_language: Option<String>,
) -> Result<CaptionSettings, CaptionError> {
    state.require_available()?;
    if let Some(p) = &profile {
        if !crate::aws_profiles::list().contains(p) {
            return Err(CaptionError::from_code("profile-missing"));
        }
    }
    let _change = state
        .begin_change()
        .ok_or_else(|| CaptionError::invalid("Another caption settings change is in progress."))?;
    let current = state.snapshot();
    let mut candidate = current.clone();
    candidate.profile = profile.clone();
    candidate.region = region.clone();
    candidate.extra_language = extra_language.clone();
    // Never ask about values that would be rejected anyway.
    caption_model::validate_settings(&candidate).map_err(|e| CaptionError::invalid(&e))?;
    if let Some(next) = profile.as_deref() {
        if needs_confirmation(
            current.profile.as_deref(),
            &current.region,
            Some(next),
            &region,
        ) && !confirm_aws_change(&app, next, &region).await
        {
            log::info!("caption AWS profile change declined");
            return Err(CaptionError::from_code("confirmation-declined"));
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
    state: State<'_, CaptionState>,
    text: String,
    source_lang: String,
    target_lang: String,
    is_final: bool,
    context: Vec<String>,
) -> Result<TranslateResult, CaptionError> {
    state.require_available()?;
    if !state.rate.lock().allow(Instant::now()) {
        return Err(CaptionError::from_code("rate-limited"));
    }
    let context = check_input(&text, &source_lang, &target_lang, &context)?;
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

/// Registers the managed state. Without an app config directory captions
/// disable themselves (every command returns `unavailable`) and the rest of
/// the app keeps working.
pub fn manage(app: &tauri::App) {
    let state = match app.path().app_config_dir() {
        Ok(dir) => CaptionState::new(dir.join("caption-overlay.json")),
        Err(e) => {
            log::warn!("captions unavailable: {e}");
            CaptionState::unavailable()
        }
    };
    app.manage(state);
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
    fn unavailable_state_rejects_every_change() {
        let state = CaptionState::unavailable();
        assert_eq!(state.require_available().unwrap_err().code, "unavailable");
        let err = apply_settings(&state, None, "us-east-1".into(), None).unwrap_err();
        assert_eq!(err.code, "unavailable");
        assert_eq!(state.snapshot(), CaptionSettings::default());
    }

    #[test]
    fn confirmation_rules() {
        type Case<'a> = (Option<&'a str>, &'a str, Option<&'a str>, &'a str, bool);
        let cases: [Case; 8] = [
            (None, "us-east-1", Some("work"), "us-east-1", true),
            (Some("work"), "us-east-1", Some("work"), "us-east-1", false),
            (Some("work"), "us-east-1", Some("home"), "us-east-1", true),
            (Some("work"), "us-east-1", Some("work"), "us-west-2", true),
            (Some("work"), "us-east-1", None, "us-east-1", false),
            (Some("work"), "us-east-1", None, "us-west-2", false),
            (None, "us-east-1", None, "us-west-2", false),
            (None, "us-east-1", None, "us-east-1", false),
        ];
        for (old_p, old_r, new_p, new_r, want) in cases {
            assert_eq!(
                needs_confirmation(old_p, old_r, new_p, new_r),
                want,
                "{old_p:?} {old_r} -> {new_p:?} {new_r}"
            );
        }
    }

    #[test]
    fn confirmation_names_profile_and_region() {
        let m = confirmation_message("work", "ap-northeast-2");
        assert_eq!(
            m,
            "TTOBAK이 AWS 프로필 'work' (리전 ap-northeast-2)으로 자막을 번역하려고 합니다. \
             이 계정에 요금이 청구될 수 있어요. 허용할까요?"
        );
        assert!(m.contains("요금이 청구될 수 있어요"));
    }

    #[test]
    fn only_one_settings_change_runs_at_a_time() {
        let state = CaptionState::unavailable();
        let first = state.begin_change().expect("first");
        assert!(state.begin_change().is_none(), "second while first runs");
        drop(first);
        assert!(state.begin_change().is_some(), "free again");
    }

    /// The provider resolves the named profile from the given files. That it
    /// never consults environment credentials is a property of
    /// `ProfileFileCredentialsProvider` (the default chain is not used), not
    /// something this test can show without mutating the process environment.
    #[tokio::test]
    #[allow(deprecated)]
    async fn profile_credentials_use_the_named_profile() {
        use aws_config::profile::profile_file::{ProfileFileKind, ProfileFiles};
        use aws_sdk_translate::config::ProvideCredentials;
        let dir = std::env::temp_dir().join(format!("caption_creds_{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        let file = dir.join("credentials");
        std::fs::write(
            &file,
            "[default]\naws_access_key_id = AKIDDEFAULT\naws_secret_access_key = d\n\n\
             [work]\naws_access_key_id = AKIDWORK\naws_secret_access_key = w\n",
        )
        .unwrap();
        let files = ProfileFiles::builder()
            .with_file(ProfileFileKind::Credentials, &file)
            .build();
        let creds = profile_credentials("work", "us-east-1", Some(files))
            .provide_credentials()
            .await
            .expect("credentials");
        assert_eq!(creds.access_key_id(), "AKIDWORK");
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
