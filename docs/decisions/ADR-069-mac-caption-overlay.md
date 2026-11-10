# ADR-069: Mac caption overlay windows

- Status: Accepted for implementation; not verified on a projector or over a
  full-screen slideshow or against a real AWS account. The manual verification
  record arrives with the overlay PR (2/4).
- Decision date: 2026-11-10.
- Numbering: ADR-069 because ADR-066 is reserved by the account-shared
  calendar work and ADR-068 is SA performance data residency.
- Builds on: ADR-024 (Mac app and system-audio captions), ADR-046 (Core Audio
  capture), ADR-061 (floating prompt window pattern).
- Design: the working design and plan documents were not merged; this ADR is
  the durable decision.
- Delivery: four stacked PRs. 1/4: caption model, profile discovery, interpreter,
  the three translation commands and the profile confirmation; 2/4: overlay
  windows, page and capability, byte budget, open-window check; 3/4: record-page
  bridge and panel; 4/4: layout menu and remaining docs. Parts of later PRs do not
  exist before they land: the windows, event routing, transparency, window level
  and overlay capability sections describe 2/4, and window reopen on the record
  page describes 3/4.

## Context

A presenter shows slides to mixed Korean, Chinese, Japanese and English audiences,
on a projector or over Zoom, and wants live interpretation as movie-style
subtitles over a full-screen slideshow, one window per audience language. The
record page's interpretation card is for the presenter only. The server's
`/api/translate` has per-user hourly limits that two or three simultaneous
targets with interims would exhaust, and the user wants overlay cost on their own
AWS account.

## Decision

**Windows.** The Mac app opens up to three floating windows (`caption_overlay.rs`):
`caption-ko`, `caption-en`, and one of `caption-ja` / `caption-zh`. Languages are
ko and en plus at most one of ja or zh (`CaptionSettings.extra_language`); a ja or
zh window is rejected unless it is the selected extra language, in open,
set-style and push alike (`gated_label`). Each window loads the bundled
`mac-app/src/caption-overlay.html`, not the remote SPA. Style (font from an
allowlist, size, colors, outline, background opacity), position and size persist
per language in a JSON file in the app config directory (`caption_model.rs`);
saved positions off every monitor fall back to bottom-center of the primary
display only when at least 100 pt of the window's width and 40 pt of its height lie
on some display. The caption area shows only text in the window's language.

**Event routing.** Rust sends `caption-update` and `caption-style` with `emit_to` to
one window, but in Tauri 2.12 a JavaScript listener registered with the default
target (`Any`) receives every emit. Each page therefore listens with a target scoped
to its own window (`{kind: 'WebviewWindow', label: 'caption-<lang>'}`), and both
payloads carry `lang`; the page drops any payload for another language.

**Transparency.** `tauri.conf.json` sets `macOSPrivateApi: true` and Cargo enables
the `macos-private-api` feature of `tauri`; windows are built `transparent`. This
fits the app's existing non-App-Store distribution. It is an app-wide config
change, so review it as such.

**Window level.** Each overlay is lifted with `setLevel: 1000`
(`NSScreenSaverWindowLevel`) and collection behavior `canJoinAllSpaces |
stationary | fullScreenAuxiliary` on the main thread, in addition to
`always_on_top`. Whether this stays above a full-screen PowerPoint or Keynote
slideshow is not verified.

**Zoom limit.** macOS screen sharing includes the overlay only when the whole
screen is shared. Sharing a single window (for example only PowerPoint) excludes
it, and the app cannot change that. The settings UI must say so.

**Translation runs on the user's AWS account.** `caption_translate.rs` translates
with the profile and region the user picks (`caption_settings_set`; profile names
come from `~/.aws/credentials` and `~/.aws/config` via `aws_profiles.rs`, including
config-only SSO and role profiles). Finals up to 3,000 bytes use Bedrock
`global.openai.gpt-6-luna` (`interpreter.rs`); interims, longer text and any
LLM failure other than expired credentials or missing model access use Amazon Translate. The overlay never calls the
server's `/api/translate`, so server limits, cost and configuration are not
involved and that path and the in-page translation tab are unchanged. There is no
fallback to the server when the profile is missing. Credentials are resolved and
held in Rust only; only profile names cross IPC. SigV4 credentials come only from
the selected profile (`profile_credentials`: static keys, SSO, `credential_process`,
roles), never from the SDK default credentials chain, which reads environment
variables first and would bill an exported terminal identity instead of the named
profile. Other SDK settings are still loaded by `aws_config::defaults` and can come
from the environment or the shared config files. Expired credentials, missing
model access, a missing profile, rate limiting, timeout and rejected output
return distinct fixed error codes; an expired-credential failure drops the cached
client so the next call after `mwinit` uses fresh credentials without a restart.
Caption text is never logged, only codes and byte lengths.

**Profile confirmation.** The remote origin can call all three translation
commands, and a compromised page must not silently switch which AWS account is
billed and receives meeting text. Before `caption_settings_set` chooses or changes
the profile or region (`needs_confirmation`), Rust shows a native sheet on the main
window titled "TTOBAK" whose Korean text names the profile and region and warns of
charges, with Allow and Cancel buttons. Clearing the profile or changing only the
extra language asks nothing. Cancel, or a sheet that cannot be shown, keeps the
settings and returns `confirmation-declined`; a call while another runs returns
`invalid-input`. The sheet
uses `rfd` directly: `tauri-plugin-dialog` would inject `window.alert`/`confirm`
overrides and page-callable commands into every page. No capability grants a
`dialog:` permission (`no_capability_grants_updater_permissions`).

**Startup without a config directory.** Captions then disable themselves (every
caption command returns `unavailable`); recording and upload keep working, as the
control channel and meeting detection already fail open.

**Remote origin command set.** The remote `default` capability gains exactly
seven commands (also listed in `build.rs` COMMANDS; `command_acl_tests` enforces
both): `caption_settings_get`, `caption_settings_set` and `caption_translate` in
1/4, and `caption_overlay_open`, `caption_overlay_close`, `caption_overlay_set_style`
and `caption_overlay_push` in 2/4. Rust validates every input: language allowlist,
AWS profile name pattern and membership in the local profile list, region pattern,
translation text at most 9,000 bytes with at most five context items of at most
3,000 bytes, and a sliding ceiling of 600 translation requests and 600,000 input
bytes (text plus context) per minute across all callers so a misbehaving page
cannot run up the user's bill (1/4). From 2/4 it also enforces the extra-language
gate, style ranges (font allowlist, size 16-160, outline 0-12, opacity 0-1,
`#RRGGBB` colors), push kind (`entry`, `clear`, `preview`), id at most 64 bytes and
pushed text at most 4,000 bytes, and `caption_translate` rejects a target language
whose overlay window is not open; the record page (3/4) starts an engine only after
its window opened and stops it before closing the window. None of the commands reads
arbitrary files, reaches an arbitrary network address or exposes tokens. Error
codes are listed in `mac-app/CLAUDE.md`.

**Overlay capability.** The overlay windows get `capabilities/caption-overlay.json`:
`core:event:allow-listen`, `allow-unlisten`, `core:window:allow-start-dragging`
and `core:window:allow-set-size` only. No event emit and no app command.

**Recognition language.** The third recognition language is opt-in per session;
the default stays `ko-KR,en-US` on web, mobile and Mac. The record page option
adds `zh-CN` or `ja-JP` (`liveLanguageOptions`, threaded through `sttManager`),
and the matching caption window uses the same language.
`translationSourceLanguage` now labels Chinese `zh` and Japanese `ja`.

**Interpreter contract mirrored.** `interpreter.rs` ports the prompt, request
body, response parsing and output validation of
`backend/internal/service/translate.go`. Each file carries a comment saying the
other must stay aligned; change both together.

## Consequences

- Overlay translation depends on the user's local AWS configuration and Bedrock
  access to the Luna model; there is no server fallback.
- Accepted exposure: after confirmation the remote page can spend on that profile
  within the fixed model, size limits and per-minute request and byte ceilings
  without asking again, and can read local profile names. The confirmation guards
  which account is used, not how much within those limits.
- The expired-credential message names `mwinit` for internal users.
- Accepted limit: the Tauri event ACL has no per-event-name scope, so the overlay
  page, which may listen, could listen to other broadcast events such as
  `native-pcm-chunk`. Targeted events (`native-control-request`, meeting events)
  go to the main window only. The page is bundled, loads no remote content and
  has no emit or command access; do not add remote content to it.
- Overlay windows are destroyed when the main window is destroyed (`closes_overlays`
  and `destroy_all` in `caption_overlay.rs`, from the app run loop), without a close
  request, so their saved `open` flags survive. Clicking an overlay activates the app;
  this is known behavior and not addressed.
- Windows reopen when the record page mounts: `useCaptionOverlay` opens each allowed
  window whose saved `open` flag is true (`savedOpenWindows`) and starts its engine
  only after the open succeeds. A failed reopen leaves that window closed, shows the
  error and never affects recording. Rust does not open windows at app setup.
- `mac-app/src-tauri/Cargo.lock` stays git-ignored (existing policy), so the AWS
  SDK crates resolve at build time.
- Mac changes have no CI coverage.
- Known gap: an echoed English final passes validation for ja and zh targets. The
  server validator has the same gap, so change `interpreter.rs` and `translate.go`
  together.

## Manual verification record

The verification record arrives with the overlay PR (2/4), whose windows it checks.
