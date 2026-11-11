# Mac Caption Overlay Implementation Plan

Status: Historical implementation plan (2026-11-10), implemented; [ADR-069](../../decisions/ADR-069-mac-caption-overlay.md) is the current reference.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Floating, movie-subtitle caption windows in the Mac app, one per
language (ko, en, plus ja or zh), translated through the user's own AWS
account, for projector and Zoom audiences.

**Architecture:** The remote SPA keeps doing live STT. For each open caption
window it runs one extra `LiveTranslation` instance whose `translate`
function invokes a Rust command instead of `/api/translate`; Rust calls
Bedrock (Luna) or Amazon Translate with a locally chosen AWS profile. Each
translation update is pushed through a second Rust command, which relays it
as a Tauri event to a transparent, always-on-top window that loads a bundled
local page. The overlay page holds no token and makes no network calls.

**Tech Stack:** Tauri 2 / Rust (`aws-config`, `aws-sdk-bedrockruntime`,
`aws-sdk-translate`, `objc2`), bundled static HTML, Next.js 16 / React 19 /
TypeScript, `node:test`.

**Spec:** `docs/superpowers/specs/2026-11-10-mac-caption-overlay-design.md`

## Global Constraints

- Work in a fresh worktree branched from `main` (`feat/mac-caption-overlay`).
  Copy the spec and this plan into it; the current checkout has unrelated
  uncommitted work.
- Repository text (code comments, docs, commit messages) is English. User
  interface copy is Korean.
- Languages: `ko` and `en` always; at most one of `ja` or `zh`. At most
  three caption windows. Window labels are exactly `caption-ko`,
  `caption-en`, `caption-ja`, `caption-zh`.
- Recognition default stays `ko-KR,en-US`. The optional third language adds
  `zh-CN` or `ja-JP` only for the session that chose it.
- No change to `/api/translate`, its limits, or backend code.
- Interpreter model ID: `global.openai.gpt-6-luna`. Request format, prompt
  and validation must match `backend/internal/service/translate.go`; both
  sides get a comment naming the other file.
- AWS keys and tokens never cross IPC and are never logged. Meeting text is
  never logged (Rust or overlay page).
- The remote origin gains only the commands listed in Task 4 and Task 5.
  Every new command is added to `build.rs` `COMMANDS`, `generate_handler!`
  and `capabilities/default.json` (the `command_acl_tests` test enforces
  this).
- Error strings returned to the SPA must not contain "not found", "not
  allowed" or "unknown command" (`isCommandNotFound` misreads them).
- All new UI is gated on `isTauri()`; web and mobile behavior is unchanged.
- Expired-credential copy, exactly: Korean copy saying the credentials expired and to run `mwinit` in a terminal, then retry (the shipped string is `captionErrorMessage` in `frontend/src/lib/captionOverlay.ts`)
- Mac checks: `(cd mac-app/src-tauri && cargo fmt --check && cargo clippy --all-targets -- -D warnings && cargo test)`.
  Frontend checks: `(cd frontend && npm run lint && npm run test:unit && npm run build)`.

## Review Focus

1. Credentials expire mid-session: the overlay must go idle and the main
   window must show the `mwinit` message; after re-authentication the next
   request succeeds without an app restart (Task 3 builds a fresh client on
   a credential error; Task 6 test `expired credentials surface once and
   recover`).
2. An interim translation arrives after its final: the final must not be
   overwritten on the overlay (Task 6 test `late interim does not replace a
   final`; `LiveTranslation` already drops it, the controller must not
   reorder pushes).
3. Chinese or Japanese speech when the third language is off: text must
   still be labelled sensibly and never crash the fan-out (Task 6 test
   `unselected source language still translates into open windows`).
4. A saved window position on a display that is no longer connected: the
   window must open on the primary display instead of off-screen (Task 4
   test `off_screen_position_falls_back`).
5. Malformed or hostile input from the remote page: oversized text, unknown
   language, out-of-range style values, a flood of requests (Task 1 tests
   for `validate_style`, Task 3 tests for `RateCeiling` and size caps).

---

### Task 1: Pure caption model and layout persistence (Rust)

**Files:**
- Create: `mac-app/src-tauri/src/caption_model.rs`
- Modify: `mac-app/src-tauri/src/lib.rs` (add `mod caption_model;`)

**Interfaces:**
- Produces:
  - `pub const LANGS: [&str; 4] = ["ko", "en", "ja", "zh"];`
  - `pub fn window_label(lang: &str) -> Option<&'static str>` (`"ko"` → `"caption-ko"`; unknown → `None`)
  - `pub struct CaptionStyle { font_family: String, font_size: f64, font_color: String, outline_color: String, outline_width: f64, background_opacity: f64 }` with serde `rename_all = "camelCase"`, `Default` = `"Apple SD Gothic Neo"`, `48.0`, `"#FFFFFF"`, `"#000000"`, `3.0`, `0.4`
  - `pub const FONTS: [&str; 6] = ["Apple SD Gothic Neo", "PingFang SC", "Hiragino Sans", "Helvetica Neue", "Noto Sans CJK KR", "system-ui"];`
  - `pub fn validate_style(style: &CaptionStyle) -> Result<(), String>`: font in `FONTS`; size 16..=160; colors match `#RRGGBB`; outline 0..=12; opacity 0..=1; all numbers finite
  - `pub struct WindowLayout { open: bool, style: CaptionStyle, x: Option<f64>, y: Option<f64>, width: f64, height: f64 }` (default 900×180, closed)
  - `pub struct CaptionSettings { profile: Option<String>, region: String, extra_language: Option<String>, windows: BTreeMap<String, WindowLayout> }` camelCase, default region `"us-east-1"`
  - `pub fn validate_settings(s: &CaptionSettings) -> Result<(), String>`: `extra_language` is `None`, `"ja"` or `"zh"`; window keys in `LANGS`; an open `ja`/`zh` window requires the matching `extra_language`; profile matches `^[A-Za-z0-9_.@-]{1,64}$`; region matches `^[a-z]{2}(-[a-z]+)+-\d$`
  - `pub fn load(path: &Path) -> CaptionSettings` (missing or corrupt file → default, never panics)
  - `pub fn save(path: &Path, s: &CaptionSettings) -> std::io::Result<()>` (write temp file, then rename)
  - `pub fn placement(saved: Option<(f64, f64)>, size: (f64, f64), monitors: &[(f64, f64, f64, f64)], primary: (f64, f64, f64, f64)) -> (f64, f64)`: the saved point if the window rectangle intersects any monitor rectangle `(x, y, w, h)`, else bottom-centered on `primary` with a 80pt bottom margin

- [ ] **Step 1: Write failing tests** in `caption_model.rs` `#[cfg(test)]`:
  `labels_cover_only_known_languages`, `default_style_is_valid`,
  `style_rejects_out_of_range` (size 15.9 and 161, opacity 1.01, outline -1,
  color `"red"`, font `"Comic Sans"`, `f64::NAN`),
  `settings_reject_two_extra_languages` (open `ja` and `zh`),
  `settings_reject_bad_profile` (`"a b"`, `"../x"`),
  `corrupt_file_loads_default`, `save_then_load_round_trips`,
  `off_screen_position_falls_back` (saved `(5000, 5000)`, one monitor
  `(0,0,1440,900)`, size `(900,180)` → `(270, 640)`).
- [ ] **Step 2:** Run `cargo test caption_model` in `mac-app/src-tauri`; expect compile failure.
- [ ] **Step 3:** Implement the items above. No Tauri imports in this file.
- [ ] **Step 4:** Run `cargo test caption_model`; expect PASS.
- [ ] **Step 5:** Commit `feat(mac): caption overlay model and layout persistence`.

### Task 2: AWS profile discovery and interpreter contract (Rust, pure)

**Files:**
- Create: `mac-app/src-tauri/src/aws_profiles.rs`
- Create: `mac-app/src-tauri/src/interpreter.rs`
- Modify: `mac-app/src-tauri/src/lib.rs` (two `mod` lines)
- Modify: `backend/internal/service/translate.go` (one comment above `interpreterSystemPrompt`: "Mirrored in mac-app/src-tauri/src/interpreter.rs; keep aligned.")

**Interfaces:**
- Produces (`aws_profiles.rs`):
  - `pub fn profile_names(credentials: &str, config: &str) -> Vec<String>`: section names from the credentials file (`[name]`) and config file (`[default]`, `[profile name]`; ignore `[sso-session …]` and `[services …]`), deduplicated, `default` first, then sorted; names failing `^[A-Za-z0-9_.@-]{1,64}$` are dropped
  - `pub fn list() -> Vec<String>`: reads `AWS_SHARED_CREDENTIALS_FILE` / `AWS_CONFIG_FILE` or `~/.aws/credentials` / `~/.aws/config`; unreadable files count as empty
- Produces (`interpreter.rs`), a port of the Go file named above:
  - `pub const MODEL_ID: &str = "global.openai.gpt-6-luna";`
  - `pub const LLM_MAX_BYTES: usize = 3000;` `pub const MAX_TEXT_BYTES: usize = 9000;` `pub const MAX_CONTEXT: usize = 5;`
  - `pub fn language_name(code: &str) -> Option<&'static str>` (ko/en/ja/zh only)
  - `pub fn system_prompt(source: &str, target: &str) -> String` (byte-identical to Go `interpreterSystemPrompt` for the same inputs)
  - `pub fn build_request(text: &str, source: &str, target: &str, context: &[String]) -> Vec<u8>`: `{"messages":[{"role":"developer",…},{"role":"user","content":"<json of {previous_segments?, current_segment}>"}],"max_completion_tokens":2048,"reasoning_effort":"low"}`
  - `pub fn parse_response(body: &[u8]) -> Result<String, &'static str>`: `choices[0].message.content`, requires `finish_reason == "stop"`, trimmed, non-empty (check `parseOpenAISummaryResponse` in the Go service package for the exact shape)
  - `pub fn validate(source: &str, output: &str, target: &str) -> Result<(), &'static str>`: the Go `validateInterpretation` rules (meta prefixes, echoed structure, `len > 4*len(source)+200`, Hangul share for ko and en), plus: target `ja` or `zh` rejects a Hangul share above 0.1

- [ ] **Step 1: Write failing tests.** `aws_profiles`: `lists_default_first`
  (credentials `[cc-bedrock]\n[default]`, config `[profile dev]\n[sso-session x]`
  → `["default","cc-bedrock","dev"]`), `drops_unsafe_names`, `empty_inputs`.
  `interpreter`: `prompt_matches_server` (assert the exact string for
  `("ko","zh")` copied from the Go function's output), `request_shape`
  (parse the bytes; assert role order, `max_completion_tokens == 2048`,
  `reasoning_effort == "low"`, user content parses to JSON with
  `current_segment`), `context_is_omitted_when_empty`,
  `parse_requires_stop` (`finish_reason: "length"` → Err),
  `validate_rejects_meta_prefix` (a Korean reply carrying a "translation:" label prefix), `validate_rejects_echo`
  (`{"current_segment":…}`), `validate_rejects_korean_for_zh`,
  `validate_accepts_plain_chinese`.
- [ ] **Step 2:** Run `cargo test aws_profiles interpreter`; expect compile failure.
- [ ] **Step 3:** Implement both files. No Tauri or AWS SDK imports.
- [ ] **Step 4:** Run the tests; expect PASS.
- [ ] **Step 5:** Commit `feat(mac): AWS profile discovery and interpreter contract`.

### Task 3: Local translation commands (Rust, AWS SDK)

**Files:**
- Create: `mac-app/src-tauri/src/caption_translate.rs`
- Modify: `mac-app/src-tauri/Cargo.toml` (add `aws-config`, `aws-sdk-bedrockruntime`, `aws-sdk-translate`, `aws-credential-types`; pick current 1.x versions, `behavior-version-latest` on `aws-config`)
- Modify: `mac-app/src-tauri/build.rs`, `src/lib.rs`, `capabilities/default.json` (commands below)

**Interfaces:**
- Consumes: Task 1 `CaptionSettings`, `load`, `save`, `validate_settings`; Task 2 everything.
- Produces:
  - `pub struct CaptionState` (managed): settings behind a mutex, the settings file path (`app.path().app_config_dir()/caption-overlay.json`), cached AWS clients keyed by `(profile, region)`, and a `RateCeiling`
  - `pub struct RateCeiling::new(per_minute: u32)` with `fn allow(&mut self, now: Instant) -> bool` (sliding 60 s window). Ceiling: `600`.
  - `#[derive(Serialize)] pub struct CaptionError { code: &'static str, message: String }`. Codes: `profile-missing`, `credentials-expired`, `model-access`, `rate-limited`, `invalid-input`, `invalid-output`, `timeout`, `aws-error`. Messages are short English, carry no meeting text and no credential material.
  - `pub fn classify(error_code: Option<&str>, message: &str) -> &'static str`: `ExpiredToken*`, `UnrecognizedClient*`, `InvalidSignature*`, or a credentials-provider failure → `credentials-expired`; `AccessDenied*`, `ResourceNotFound*`, `ValidationException` naming the model → `model-access`; else `aws-error`
  - Commands (all `async`, all return `Result<_, CaptionError>`):
    - `caption_settings_get() -> { settings: CaptionSettings, profiles: Vec<String>, fonts: Vec<String> }`
    - `caption_settings_set(profile: Option<String>, region: String, extra_language: Option<String>) -> CaptionSettings` (validates, persists, drops cached clients)
    - `caption_translate(text: String, source_lang: String, target_lang: String, is_final: bool, context: Vec<String>) -> { translatedText: String, engine: "llm" | "translate" }`
- `caption_translate` rules: both languages in `LANGS` and different; text non-empty and ≤ `MAX_TEXT_BYTES`; context truncated to the last `MAX_CONTEXT`, each ≤ `LLM_MAX_BYTES`; rate ceiling checked first. Final and ≤ `LLM_MAX_BYTES` → Luna via `InvokeModel` with a 6 s timeout, `parse_response`, `validate`; on any Luna failure other than a credential or model-access error, fall back to Amazon Translate. Interim, or longer text → Amazon Translate (`zh` is sent as `zh`). On a `credentials-expired` result, drop the cached client so the next call rebuilds the credential chain.
- The profile is loaded with `aws_config::defaults(BehaviorVersion::latest()).profile_name(..).region(..)`, so static keys, SSO and `credential_process` profiles all work.

- [ ] **Step 1: Write failing tests** (pure parts only):
  `rate_ceiling_blocks_601st` and `rate_ceiling_recovers_after_window`;
  `classify_maps_expired` (`"ExpiredTokenException"`), `classify_maps_access`
  (`"AccessDeniedException"`), `classify_defaults_to_aws_error`;
  `input_rules` for an extracted `fn check_input(text, source, target, context) -> Result<Vec<String>, CaptionError>`
  (same language, unknown language `"fr"`, 9001-byte text, 8-entry context →
  last 5); `error_messages_avoid_skew_words` (no message for any code
  contains "not found", "not allowed", "unknown command").
- [ ] **Step 2:** Run `cargo test caption_translate`; expect compile failure.
- [ ] **Step 3:** Implement. Register the three commands in `build.rs`
  `COMMANDS`, `generate_handler!`, and `capabilities/default.json`
  (`allow-caption-settings-get`, `allow-caption-settings-set`,
  `allow-caption-translate`); `.manage(CaptionState::…)` in `setup` once the
  config dir is known.
- [ ] **Step 4:** Run `cargo test`; expect PASS, including `command_acl_tests`.
- [ ] **Step 5:** Run `cargo clippy --all-targets -- -D warnings`; expect clean.
- [ ] **Step 6:** Commit `feat(mac): translate captions with the user's own AWS profile`.

### Task 4: Overlay windows and bundled page (Rust + HTML)

**Files:**
- Create: `mac-app/src-tauri/src/caption_overlay.rs`
- Create: `mac-app/src/caption-overlay.html`
- Create: `mac-app/src-tauri/capabilities/caption-overlay.json`
- Modify: `mac-app/src-tauri/tauri.conf.json` (`"macOSPrivateApi": true`), `Cargo.toml` (`tauri` feature `macos-private-api`), `build.rs`, `src/lib.rs`, `capabilities/default.json`

**Interfaces:**
- Consumes: Task 1 (`window_label`, `CaptionStyle`, `validate_style`, `placement`, `WindowLayout`), Task 3 `CaptionState`.
- Produces commands (remote-callable, `Result<_, CaptionError>`):
  - `caption_overlay_open(lang: String) -> WindowLayout`: rejects languages outside `LANGS` and `ja`/`zh` that differ from `extra_language`; reuses an existing window; persists `open: true`
  - `caption_overlay_close(lang: String)`; persists `open: false`
  - `caption_overlay_set_style(lang: String, style: CaptionStyle)`; validates, persists, emits `caption-style` to that window
  - `caption_overlay_push(lang: String, kind: String, id: String, order: u32, text: String, is_final: bool)`; `kind` is `entry`, `clear` or `preview`; `id` ≤ 64 bytes; `text` ≤ 4000 bytes; emits `caption-update` `{kind,id,order,text,isFinal}` to window `caption-<lang>` only; a closed window is a silent no-op
- Window: `WebviewUrl::App("caption-overlay.html?lang=<lang>")`, `transparent(true)`, `decorations(false)`, `shadow(false)`, `resizable(true)`, `always_on_top(true)`, `visible_on_all_workspaces(true)`, `skip_taskbar(true)`, `focused(false)`, min size 320×80. On macOS, after build, set the NSWindow level to `1000` (screen-saver level) and collection behavior to `canJoinAllSpaces | fullScreenAuxiliary | stationary` via `objc2::msg_send!` on `ns_window()`, on the main thread. Position from `placement` using `available_monitors()`.
- Moves and resizes update the saved layout, written at most once per 500 ms. `CloseRequested` persists `open: false`.
- `caption-overlay.json` capability: windows `["caption-ko","caption-en","caption-ja","caption-zh"]`, permissions `core:event:default` and `core:window:allow-start-dragging`. No app commands.
- `caption-overlay.html`: one self-contained file, no external requests. Reads `lang` from the query string, sets `<html lang>`. Whole body is `data-tauri-drag-region`. Keeps the latest 3 entries by `order` (an entry with a known `id` replaces in place; `clear` empties; `preview` shows a built-in sample sentence for its language). Renders text only with `textContent`. Applies style with CSS variables: `font-family`, `font-size`, `color`, `-webkit-text-stroke: <width>px <color>` with `paint-order: stroke fill`, background `rgba(0,0,0,<opacity>)`. Lines are bottom-aligned and centered; new lines push old ones up with a 150 ms transform transition; replaced text does not animate. Interims render in the same style as finals. Starts in `preview`. No console logging of caption text.

- [ ] **Step 1: Write failing tests** in `caption_overlay.rs` for pure helpers:
  `check_push` (`kind` `"bogus"` rejected; 4001-byte text rejected; 65-byte
  id rejected), `overlay_url` (`"zh"` → `"caption-overlay.html?lang=zh"`),
  `extra_language_gate` (`ja` open with `extra_language = Some("zh")` →
  `invalid-input`).
- [ ] **Step 2:** Run `cargo test caption_overlay`; expect compile failure.
- [ ] **Step 3:** Implement the module, page, capability and config changes; register the four commands in all three places.
- [ ] **Step 4:** Run `cargo fmt --check && cargo clippy --all-targets -- -D warnings && cargo test`; expect PASS.
- [ ] **Step 5:** Run `npm run build:signed:debug` in `mac-app/`; expect a built app. Report plainly if this cannot run in the environment.
- [ ] **Step 6:** Commit `feat(mac): transparent caption overlay windows`.

### Task 5: Language detection and recognition options (frontend lib)

**Files:**
- Modify: `frontend/src/lib/liveTranslation.ts` (`translationSourceLanguage`)
- Modify: `frontend/src/lib/sttManager.ts` (config, both `languageOptions` sites, `transcriptHandler`)
- Modify: `frontend/scripts/live-translation.test.mjs`
- Create: `frontend/scripts/stt-language-options.test.mjs`

**Interfaces:**
- Produces:
  - `translationSourceLanguage(text, detectedLanguage?)`: detected `ko`/`en`/`ja`/`zh` wins; otherwise Hangul → `ko`, kana → `ja`, Han ideographs → `zh`, else `en`
  - `export type ExtraLiveLanguage = 'zh-CN' | 'ja-JP';`
  - `export function liveLanguageOptions(extra?: ExtraLiveLanguage): string` → `'ko-KR,en-US'` or `'ko-KR,en-US,zh-CN'` / `'ko-KR,en-US,ja-JP'`
  - `SttManagerConfig.extraLanguage?: ExtraLiveLanguage` used at both `languageOptions` sites
  - `SttManagerConfig.onSegment?: (id: string, text: string, isFinal: boolean, languageCode?: string) => void`, called in `transcriptHandler` right after `translation.update`

- [ ] **Step 1: Write failing tests.** In `live-translation.test.mjs`:
  `detects zh and ja` (`('你好', 'zh-CN')` → `zh`; `('こんにちは')` → `ja`;
  `('你好')` → `zh`; (Hangul mixed with Han) → `ko`; `('hello')` → `en`). New file:
  `default options are unchanged` (`liveLanguageOptions()` ===
  `'ko-KR,en-US'`), `adds exactly one language`. Load modules with the
  `loadModule` helper pattern already in `live-translation.test.mjs`.
- [ ] **Step 2:** Run `npm run test:unit` in `frontend/`; expect the new tests to fail.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** Run `npm run test:unit`; expect PASS with no existing test changed in meaning.
- [ ] **Step 5:** Commit `feat(captions): optional third recognition language`.

### Task 6: Overlay bridge and fan-out controller (frontend lib)

**Files:**
- Create: `frontend/src/lib/captionOverlay.ts`
- Create: `frontend/scripts/caption-overlay.test.mjs`

**Interfaces:**
- Consumes: Task 3 and Task 4 commands; `LiveTranslation` and its `TranslationOptions`.
- Produces:
  - Types `CaptionLang = 'ko' | 'en' | 'ja' | 'zh'`, `CaptionStyle`, `WindowLayout`, `CaptionSettings`, `CaptionSettingsView` (camelCase mirrors of the Rust structs), `CaptionErrorCode`
  - Thin wrappers over `window.__TAURI__.core.invoke`: `getCaptionSettings()`, `setCaptionSettings(profile, region, extraLanguage)`, `openCaptionWindow(lang)`, `closeCaptionWindow(lang)`, `setCaptionStyle(lang, style)`, `pushCaption(lang, kind, id, order, text, isFinal)`, `translateCaption(text, sourceLang, targetLang, isFinal, context)`
  - `export function captionErrorMessage(code: string): string` Korean copy: `credentials-expired` → the exact string in Global Constraints; `profile-missing` → Korean copy asking to choose an AWS profile first; `model-access` → Korean copy saying GPT-6 Luna is unavailable in the chosen account and to check Bedrock model access; `rate-limited` → Korean copy saying translation paused because of too many requests; other → Korean copy saying caption translation failed and to retry shortly
  - `export class CaptionOverlayController` constructed with `{ translate, push, onError }` (injected for tests; defaults are the wrappers):
    - `setLanguages(langs: CaptionLang[])`: creates or stops one `LiveTranslation` per language (`enabled: true`, `interim: true`, `targetLang: lang`), `onUpdate` → `push(lang, 'entry', entry.id, entry.order, entry.translated || entry.original, entry.isFinal)`. An entry with status `error` pushes nothing.
    - `update(id, text, isFinal, detectedLanguage?)`: forwards to every instance
    - `clear()`: pushes `clear` to every language; `preview()`: pushes `preview`
    - `stop()`: stops all instances
    - A rejected translate with a `code` calls `onError(code)` at most once per code until `resetErrors()`.

- [ ] **Step 1: Write failing tests** with fake `translate`/`push`:
  `fans out to every open language` (Korean final, languages ko/en/zh → ko
  push carries the original, en and zh carry fake translations),
  `late interim does not replace a final`,
  `unselected source language still translates into open windows` (Chinese
  text with no detected language, windows ko/en → two translate calls with
  `sourceLang: 'zh'`),
  `expired credentials surface once and recover` (two rejections with
  `{code:'credentials-expired'}` → one `onError`; after `resetErrors()` and a
  successful translate, pushes resume),
  `closing a language stops its requests`,
  `error messages` (exact `mwinit` string).
- [ ] **Step 2:** Run `npm run test:unit`; expect failures.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** Run `npm run test:unit && npm run lint`; expect PASS.
- [ ] **Step 5:** Commit `feat(captions): overlay bridge and per-language fan-out`.

### Task 7: Record page panel and wiring (frontend UI)

**Files:**
- Create: `frontend/src/hooks/useCaptionOverlay.ts`
- Create: `frontend/src/components/record/CaptionOverlayPanel.tsx`
- Modify: `frontend/src/hooks/useRecordingSession.ts` (accept `extraLanguage` and `onSegment`, pass both to `SttManager`)
- Modify: `frontend/src/app/record/page.tsx` (mount the hook and panel next to `RecordingConfig`)
- Create: `frontend/scripts/caption-overlay-panel.test.mjs`

**Interfaces:**
- Consumes: Task 5 `ExtraLiveLanguage`, `onSegment`; Task 6 everything.
- Produces:
  - `useCaptionOverlay({ isRecording }) -> { supported, view, error, openLangs, extraLanguage: ExtraLiveLanguage | undefined, onSegment, setProfile, setRegion, setExtraLanguage, toggleWindow, updateStyle, retry }`. `supported` is `isTauri()` and the settings command exists (a command-missing error from an older app build hides the panel). Recording start pushes nothing extra; recording stop calls `clear()`; opening a window while idle calls `preview()`.
  - `CaptionOverlayPanel` (props = the hook result plus `isRecording`): renders nothing when `!supported`. Sections: AWS profile select and region input; third language select (none, `日本語`, `中文`), disabled while recording, with the note Korean copy saying recognition languages change only before recording starts; one row per allowed language with an open/close toggle; for each open window a style editor (font select from `view.fonts`, size 16–160, font color, outline color, outline width 0–12, background opacity 0–100%); the notice Korean copy saying Zoom must share the whole screen, not a window, for captions to show; an error line with a retry button when `error` is set.
  - Changing the third language closes the window of the other one.
  - Window toggles are disabled, with the `profile-missing` message, until a profile is chosen.
- Use existing tokens and patterns from `RecordingConfig.tsx`; no new colors.

- [ ] **Step 1: Write failing tests** (render with `renderToStaticMarkup`, following `live-translation.test.mjs`): `renders nothing outside the Mac app`, `disables window toggles without a profile`, `offers only one third language` (with `extraLanguage: 'zh'` there is no `caption-ja` row), `shows the Zoom notice`, `shows the mwinit message`.
- [ ] **Step 2:** Run `npm run test:unit`; expect failures.
- [ ] **Step 3:** Implement the hook, panel and wiring.
- [ ] **Step 4:** Run `npm run lint && npm run test:unit && npm run build`; expect PASS.
- [ ] **Step 5:** Commit `feat(record): caption window controls for the Mac app`.

### Task 8: Documentation and manual verification record

**Files:**
- Create: `docs/decisions/ADR-066-mac-caption-overlay.md` (confirm 066 is the next free number)
- Modify: `mac-app/CLAUDE.md` (wire contract rows for the seven commands and two events), `docs/DESIGN-SPEC.md` (record page panel), `docs/architecture.md` (one paragraph: local Bedrock path), `docs/README.md` index if it lists ADRs
- Modify: `CLAUDE.md` accepted-limits table only if review asks; otherwise leave it

**ADR content (decisions to record):** `macOSPrivateApi` enabled for
transparency; overlay translation uses the user's AWS profile rather than
the server; remote origin command set and its validation; window level
choice; the Zoom window-share limit; interpreter prompt mirrored from the
server.

- [ ] **Step 1:** Write the ADR and reference updates in concise English.
- [ ] **Step 2:** Run `python3 scripts/docs/check_docs.py`; expect PASS.
- [ ] **Step 3:** Add a manual checklist to the ADR with a result column, and
  run what the environment allows: overlay above full-screen PowerPoint and
  Keynote on an external display; transparency and outline rendering; Zoom
  full-screen share shows the overlay and window share does not; expired
  credentials then `mwinit` then retry; Chinese and Japanese speakers with
  the third language on; saved layout after restart; display unplugged.
  Mark every item not actually run as "not run".
- [ ] **Step 4:** Commit `docs: ADR-066 Mac caption overlay`.
