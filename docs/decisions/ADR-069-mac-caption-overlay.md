# ADR-069: Mac caption overlay windows

- Status: Accepted for implementation; partly observed in a local test build (see the verification record); not verified on a projector or over a full-screen slideshow
  or against a real AWS account (see the verification record).
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
  exist before they land.

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

**Idle and failure display.** A failed translation pushes that entry's id with empty
text, which removes its line, so expired credentials mid-sentence never leave a stale
draft on the projector. Text longer than the 4,000-byte push cap is cut to its tail
on a UTF-8 character boundary before pushing (`MAX_PUSH_TEXT_BYTES`). Skipped filler
finals push nothing. Once an id is shown as final, later drafts for it are dropped
because commands can emit out of order. A transparent overlay still takes clicks meant for
the windows under it, so a `clear` push orders the window out, and a non-empty entry
or a preview orders it back in without making it key, so focus stays with the
slideshow. Ordering out alone left cases where an empty window kept swallowing
clicks (for example when failed translations removed every line), so the page also
reports each idle/active change through `caption_overlay_idle`, which sets only the
calling overlay window to ignore the pointer while it shows no caption; `clear` sets
it in Rust before emitting. An idle window cannot be dragged or hovered, which is
acceptable because it is invisible; windows are never closed automatically, so the
user's layout choice survives.

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
held in Rust only; only profile names cross IPC. They come only from the selected
profile (`profile_credentials`: static keys, SSO, `credential_process`, roles),
never from the SDK default chain, which reads environment variables first and
would bill an exported terminal identity instead of the named profile. Expired credentials, missing
model access, a missing profile, rate limiting, timeout and rejected output
return distinct fixed error codes; an expired-credential failure drops the cached
client so the next call after `mwinit` uses fresh credentials without a restart.
Caption text is never logged, only codes and byte lengths.

**Region.** New settings default to `ap-northeast-2` (saved settings keep their
region). The model is a global inference profile, so the region only selects the
endpoint requests go to and the Amazon Translate region; the record page offers
`ap-northeast-2` and `us-east-1` plus any other saved region.

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
both): `caption_settings_get`, `caption_settings_set`, `caption_translate`,
`caption_overlay_open`, `caption_overlay_close`, `caption_overlay_set_style`,
`caption_overlay_push`. Rust validates every input: language allowlist (and the
extra-language gate), AWS profile name pattern and membership in the local
profile list, region pattern, style ranges (font allowlist, size 16-160, outline
0-12, opacity 0-1, `#RRGGBB` colors), push kind (`entry`, `clear`, `preview`), id
at most 64 bytes, pushed text at most 4,000 bytes, translation text at most 9,000
bytes with at most five context items of at most 3,000 bytes, and a sliding
ceiling of 600 translation requests and 600,000 input bytes (text plus context) per
minute across all callers so a misbehaving page cannot run up the user's bill.
`caption_translate` also rejects a target language whose overlay window is not open;
the record page starts an engine only after its window opened and stops it before
closing the window. None of the commands reads
arbitrary files, reaches an arbitrary network address or exposes tokens. Error
codes are listed in `mac-app/CLAUDE.md`.

**Overlay capability.** The overlay windows get `capabilities/caption-overlay.json`:
`core:event:allow-listen`, `allow-unlisten`, `core:window:allow-start-dragging`
and `core:window:allow-set-size`, plus the app command `allow-caption-overlay-idle`
(own-window pass-through only; not granted to the remote SPA). No event emit and no
other app command.

**Mouse move and resize.** Hovering an overlay shows a dashed outline, a top-centre
move handle and a bottom-right resize grip, all hidden again when the pointer leaves,
so a projected view shows only subtitles; the chrome sits in reserved padding and
never covers text. tao 0.37 returns `NotSupported` for `drag_resize_window` on macOS,
so the grip resizes the window from the page with `setSize(LogicalSize)` once per
animation frame, clamped to 320x80; the resulting `Resized` event saves the layout. The page is a local origin, holds no
token and makes no network call.

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

## Manual verification record

Recorded 2026-11-10. The owner ran a local test build (the app pointed at a
local dev server) on a laptop with an external display; rows marked "observed"
come from that single session. Nothing was run on a projector, over a full-screen
slideshow or in a Zoom share. Go is not installed locally, so `go vet` was
not run for the comment-only change in `translate.go`. Unit tests in the Rust
modules and the frontend scripts are separate from this checklist.

| Check | Result |
|---|---|
| Overlay stays above a full-screen PowerPoint slideshow on an external display | not run |
| Overlay stays above a full-screen Keynote slideshow on an external display | not run |
| Transparent background and text outline render correctly | not run |
| Zoom full-screen share shows the overlay | not run |
| Zoom single-window share does not show the overlay | not run |
| Expired credentials show the error, then `mwinit`, then retry succeeds without restart | not run |
| Missing model access and missing profile show their own messages | not run |
| Chinese and Japanese speakers with the third language on (recognition, source labels, original-vs-translation windows) | not run |
| Saved layout and style restored after app restart | not run |
| Two windows open: each shows only its own language | observed by the owner, local test build, 2026-11-10 |
| Two windows open: a style change applies only to its own window | not run |
| Three windows (ko, en, zh) open with the third language on: each shows its own language | observed by the owner, local test build, 2026-11-10 |
| Hover chrome appears, the move handle drags and the resize grip resizes | observed by the owner, local test build, 2026-11-10 |
| Overlay becomes invisible after a recording stops | observed by the owner, local test build, 2026-11-10 |
| Display unplugged: window reopens visible on the primary display | not run |
| Changing the profile or region shows the native confirmation above the main window; Cancel keeps the saved values and Allow applies them | not run |
| Overlay translation on a real account | `caption_translate` returned an Amazon Translate interim and a Luna final for ko to zh once, 2026-11-10; latency not measured |
