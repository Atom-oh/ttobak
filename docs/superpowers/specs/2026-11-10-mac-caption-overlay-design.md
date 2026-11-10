# Mac caption overlay windows: design

Status: Historical design record (2026-11-10), implemented with later changes;
[ADR-069](../../decisions/ADR-069-mac-caption-overlay.md) is the current reference.

## Intent

The user presents PowerPoint sessions to mixed Korean, Chinese and English
audiences, either on a projector in the room or over Zoom. During the
session they want TTOBAK's live interpretation to appear as movie-style
subtitles on top of the slides, one floating window per audience language,
so each group can read along. Q&A mixes Korean, Chinese and English
speakers.

Today live interpretation lives in a small card on the record page
(`frontend/src/components/TranslationView.tsx`, 48vh max height, small
text, one target language at a time). That works for the presenter, not
for an audience.

## Goals

- Mac app only: open one or more floating caption windows during a
  recording with live interpretation on. Each window shows one language
  (for example ko, en, zh at the same time).
- Each window can be moved, for example onto the projector display, and
  resized. It stays visible above a full-screen PowerPoint or Keynote
  slideshow.
- Movie-subtitle styling, set per window:
  - font family and font size
  - font color
  - outline (stroke) color and outline thickness
  - background color opacity, from fully transparent to opaque
- Each window remembers its language, style, position and size. The same
  layout reopens for the next session.
- Audience-facing content only: the text in the window's language, the
  latest two or three lines, large. No timestamps, status labels, engine
  badges or controls in the subtitle area.
- Interim text appears immediately, in normal style rather than dimmed, and
  is replaced in place when the final arrives. This was the user's decision.
- Each window shows the speaker's own words when the speaker uses its
  language, and a translation otherwise. A Korean speaker shows original
  Korean in the ko window and translations in the zh and en windows.
- The languages that matter are Korean, English, Chinese and Japanese.
  Korean and English are always on. The user can add at most one more,
  either Japanese or Chinese, so a session uses at most three languages.
  The same limit applies to live recognition and to caption windows.
- Overlay translations run on the Mac app user's own AWS account: the Mac
  app calls Bedrock directly with local AWS credentials. The TTOBAK server's
  translation limits and cost are not involved.

## Non-goals

- No web or mobile overlay. The web record page keeps its current
  translation tab.
- No change to the server's `/api/translate`, its limits or its model
  configuration. The in-page translation tab keeps using it unchanged.
- No change to the interpreter model choice. The server uses GPT-6 Luna for
  finals (`infra/lib/model-config.ts` `translation`) and Amazon Translate
  for interims. The user found Luna fast and accurate; the Mac-side path
  uses the same model in the user's account.
- No captions injected into Zoom itself, such as Zoom's closed-caption API
  or a virtual camera.
- No change to the saved meeting transcript or notes.

## Anti-goals (failure even if it "works")

- The subtitle window goes behind a full-screen slideshow on the projector.
- Captions lag far enough behind speech that the audience loses the thread.
  Interims must not wait on the slower final translation.
- Presenter-only information or UI chrome shows up on the projected
  subtitles.
- The overlay gets its own authenticated network access, or the remote
  production origin gains commands beyond the narrow, validated overlay set
  listed under "Calls I made".
- Meeting text gets logged, either in Rust logs or in the overlay.

## Constraints and known facts

- The Mac app's main window loads the remote SPA
  (`https://ttobak.atomai.click`, `mac-app/src-tauri/tauri.conf.json`).
  Every command it calls must be granted in the `remote` capability
  (`capabilities/default.json`).
- Prior art in the repo: `mac-app/src-tauri/src/meeting_prompt.rs` opens a
  floating, always-on-top, all-workspaces window. It loads a bundled local
  HTML page with its own narrow capability (`capabilities/meeting-prompt.json`).
- Window transparency on macOS with Tauri requires `macOSPrivateApi: true`
  (currently `false`), plus `transparent: true` on the overlay window. This
  matches the app's existing ad-hoc, non-App-Store distribution, but it is
  a config change and should be called out in review.
- Staying above a full-screen PowerPoint or Keynote slideshow likely needs a
  higher window level and full-screen-auxiliary collection behavior. A
  plain `always_on_top` is probably not enough. This must be verified on a
  real Mac with an external display. It is the main technical risk.
- Zoom: macOS screen sharing only includes the overlay when the presenter
  shares the entire screen or display. Sharing only the PowerPoint window
  excludes it, and code cannot work around that. The overlay settings UI
  must say so plainly.
- Live STT language identification is hard-coded to `'ko-KR,en-US'` in
  `frontend/src/lib/sttManager.ts` (two call sites). Allow one optional
  extra language, `zh-CN` or `ja-JP`. Check how
  Transcribe Streaming handles mid-stream language switches and what first
  sentence accuracy and latency look like. Also confirm the detected
  language maps to `zh` or `ja` in `translationSourceLanguage`.
- Translation today produces one target per entry (`liveTranslation.ts`,
  `targetLang` from the record page). Multiple windows need several targets
  per utterance. At most N-1 requests run per interim or final, where N is
  the number of distinct window languages.
- The server's per-user hourly limits (`DefaultTranslateHourlyLimit = 3000`,
  `DefaultTranslateHighHourlyLimit = 1500`) could not carry two or three
  targets with interims. That is why overlay translation moves to the
  user's own Bedrock (user decision) instead of changing the server.
- Local Bedrock path: AWS credentials are resolved in Rust from the user's
  local AWS configuration. The Mac app's settings list the profile names
  found in `~/.aws/credentials` and `~/.aws/config` (for example `default`
  or `cc-bedrock`) and the user picks one, plus a region (user decision).
  Only profile names cross IPC to the settings UI, never keys or tokens.
  The picker must handle both static-key profiles and config-only profiles
  such as SSO or assumed roles. Credentials never cross IPC to the webview and are never
  logged. The remote main window can only ask Rust to translate bounded
  text into allowlisted languages; Rust enforces size caps and a request
  rate ceiling so a misbehaving page cannot run up the user's bill.
- The user's account needs Bedrock access to the Luna model
  (`global.openai.gpt-6-luna` on the server today; verify the ID and
  request format against `backend/internal/service/translate.go`). Missing
  credentials, expired sessions or missing model access must show a clear
  message in the main window and an idle overlay, never a silent blank.
  The users are internal developers, so the expired-credential message
  names the fix in Korean: credentials expired, run `mwinit` in a
  terminal, then retry (user decision). Distinguish
  expired credentials from missing model access and from a missing profile,
  because each has a different fix. After the user re-authenticates, a
  retry must pick up the fresh credentials without restarting the app.
- The server validates interpreter output before showing it
  (`validateInterpretation`) and uses a specific prompt and context of
  earlier finals. The Mac-side path needs equivalent prompt and validation;
  keep the two aligned and say so in both places.
- Mac changes have no CI coverage. Report native validation limits
  honestly (see CLAUDE.md).
- Mobile and web behavior must not regress. Gate all new UI on the Mac app
  runtime.

## Decisions made in conversation

- Users: the audience reads the overlay on a projector or via Zoom screen
  share (user).
- Languages: ko and en always, plus at most one of ja or zh, with
  mixed-language speakers in Q&A (user).
- Overlay translation uses the Mac app user's own AWS account Bedrock,
  called directly from the Mac app (user).
- Interims show at full style and are replaced by finals in place (user).
- Styling controls: font, size, font color, outline color, outline
  thickness and background opacity (user).
- Keep GPT-6 Luna for final translations (user; already the configured
  model).

## Calls I made (the user can override)

- Overlay windows load a bundled local page (like `meeting-prompt.html`),
  not the remote SPA. The main window pushes caption updates through a new
  narrow Tauri command, and Rust relays them to the overlay windows as
  events. The overlay holds no token and makes no network calls.
- The record page shows an caption-window control, only in the Mac app, to add a
  window, choose its language and open its style settings. Style settings
  live in a small panel in the main window, so that no controls render on
  the projected window. The builder may add a hover-revealed close or drag
  affordance on the overlay if it stays out of the subtitle area.
- Layout and style persist on the Mac side, for example as a JSON file in
  app data, keyed by window. The builder may choose the mechanism.
- Overlay windows can open before recording starts, in a preview mode with
  sample text, so the presenter can place them on the projector, confirm
  they stay above the full-screen slideshow, and tune the style. Live text
  flows only while recording with interpretation on. When recording stops,
  they show nothing, or a quiet idle state, until closed.
- The third recognition language is opt-in per session, not a global
  change. The default stays `ko-KR,en-US` on web, mobile and Mac, so
  ordinary Korean meetings keep today's accuracy and latency. The record
  page offers a recognition-language option that adds either `zh-CN` or
  `ja-JP`, and the third caption window uses that same language. Thread the option through `sttManager` (both call
  sites and the `transcribeStreamingClient.ts` fallback).
- Caption windows are limited to ko, en and the one optional language (ja
  or zh), so at most three windows. This is my reading of the user's
  three-language limit. The "speaker's own words" rule applies whenever the
  detected source language equals the window language.
  `translationSourceLanguage` must label Chinese as `zh` and Japanese as
  `ja`; today it maps everything that is not Korean to `en`.
- The remote main window may call a small set of overlay commands: open or
  close a window, set its language and style, read the saved layout, and
  push caption text. These are the only new `remote` grants. Rust validates
  every input: a fixed window-label pattern and a window-count cap, a
  language allowlist, numeric ranges for style values, a color format, and
  a size cap on pushed text. None of them reads files, reaches the network
  or exposes tokens.
- The existing in-page translation tab keeps working through the server
  with its single selected target. Overlay targets are additional and go
  only through the local Bedrock path.
- Interims on the local path keep the server's split if it is fast enough:
  Amazon Translate in the user's account for interims, Luna for finals. The
  builder may use Luna for both if measurement shows it keeps up, since
  that needs one fewer permission.
- Opening caption windows requires the local AWS profile to be configured.
  There is no fallback to the server for overlay translations.
- Defaults: large sans-serif font, white text, black outline of moderate
  thickness, black background at about 40% opacity, bottom-of-screen
  placement on the display the user drags the window to.
- Transitions: new lines push older ones up smoothly. A replaced interim
  must not cause a jump.

## Left to the builder

- Exact IPC payload shape, throttling and coalescing of caption pushes.
- How multi-target translation is structured on the local path: parallel
  calls per target, or one request that returns several languages.
- The Rust AWS client choice (official SDK crates or a small SigV4 client)
  and where the chosen profile name and region are stored.
- Window level and collection-behavior implementation, and whether it needs
  native AppKit calls.
- Font list. Offer system fonts that cover CJK well, such as Apple SD Gothic
  Neo and PingFang SC.
- Test strategy: unit tests for multi-target fan-out, language mapping and
  settings persistence, plus a manual macOS checklist for projector,
  full-screen PowerPoint, Keynote, and Zoom full-screen share.
- Documentation updates (`docs/DESIGN-SPEC.md`, the Mac app docs, and an
  ADR if the private-API or window-level choice is durable).
