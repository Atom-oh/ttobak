# Mac interpret-only mode: design

Date: 2026-11-13. Status: historical design record; approved by the user on
2026-11-13. Current behavior is in the code and ADR-024. Repository docs are
English and UI copy is Korean: backticked camelCase names such as `navLabel`
are keys of `docs/superpowers/plans/2026-11-13-mac-interpret-only-ui-copy.json`.

## Intent

The user wants live interpretation in the Mac app without holding a meeting:
no meeting record, no saved audio, no transcript, no notes. They start it,
the app listens to both the microphone and the Mac's own sound, and shows the
translation. When they stop, nothing remains.

Today interpretation exists only as an option inside a recording on
`/record`. The Mac app loads that same web page, and native capture is only
reachable through `start_recording(meeting_id, ...)`, which always writes a
WAV bound to a meeting ID.

## Goals

- Mac app only: a dedicated screen, reached from a sidebar entry labelled
  `navLabel`, that runs live interpretation with no meeting.
- Hears both sources at once: microphone and system audio, mixed the same
  way a native recording mixes them.
- Start and stop from that screen. While running it shows the original text
  and its translation, large enough to read comfortably.
- Same interpretation controls as the record page: target language
  (`TRANSLATION_TARGETS`, default `bilingualTarget`) and the opt-in
  `interimToggle` interim toggle.
- Nothing is persisted: no audio file, no meeting, no transcript, no upload.

## Non-goals

- No web-browser or mobile interpret-only mode. Outside the Mac app the
  screen says it needs the Mac app and links to `/record`.
- No floating caption windows from this screen. They exist for recordings
  (ADR-018, `caption_overlay.rs`); wiring them to an interpretation session
  is a possible follow-up, and nothing here depends on it.
- No change to `/api/translate`, its limits, models, or prompts.
- No export, copy-all, or history of past interpretation sessions.
- No change to the record page's behavior.

## Anti-goals (failure even if it "works")

- Any audio reaches disk during an interpretation session, even temporarily.
- A meeting, transcript, checkpoint, or leftover-recording entry is created.
- Interpretation text is logged in Rust or the web console, or stored in
  `localStorage`/IndexedDB.
- A recording and an interpretation session capture at the same time.
- The record page or native recording regresses.

## Approaches considered

1. **Dedicated screen plus a capture mode that writes no file (chosen by the
   user).** New Rust commands start and stop a listen-only capture; a new
   web route reuses the existing live STT and translation code. Touches the
   most places (Rust capture, Tauri ACL, a web route, CloudFront routing)
   but is the only option where audio never touches disk.
2. **Run a normal recording and delete the file afterwards.** Almost no Rust
   change. Rejected: audio is on disk for the whole session and a crash
   leaves it in the leftover-recording recovery list, contradicting "no
   recording".
3. **An "interpret only" switch inside `/record`.** Same Rust work as 1, no
   new route. Rejected: the record page is about 1,900 lines with meeting
   creation and upload woven through; a wrong branch creates a meeting, and
   the feature is hard to find.

## Design

### Native capture (Rust, `mac-app/src-tauri/`)

- Add two commands, `start_listening(input_device_uid?)` and
  `stop_listening()`. Register both in `build.rs` `COMMANDS` and grant
  `allow-start-listening` / `allow-stop-listening` in
  `capabilities/default.json` (the existing
  `registered_commands_are_declared_and_granted` test must cover them).
- Listening reuses the existing capture backend (`audio::macos::Backend`):
  same process tap plus microphone, same mixer, same `native-pcm-chunk` and
  `native-audio-level` events. The only difference is that no WAV writer
  exists. The worker already holds the writer as an `Option`; listening
  starts it empty, and `recording_path` is never called.
- Listening and recording share the single `RecorderState.recorder` slot, so
  they are mutually exclusive by construction and keep the existing
  generation guard that stops a superseded capture from emitting events.
  Starting either while the other runs fails with the existing
  `AlreadyRunning` error. The start reservation, `StartGuard`, lock
  discipline around blocking Core Audio FFI, and teardown order (ADR-024)
  apply unchanged.
- A listening session has no path. Everything that reads
  `snapshot().path` (`recording_status`, `cleanup_recording`,
  `list_leftover_recordings`, `upload.rs`, `control.rs`, `sleep.rs`,
  `meeting_detect.rs`) must treat it as "not recording": it is never
  inserted into `recorded_paths`, never protected by the
  "awaiting upload" power assertion, never offered for upload or leftover
  adoption. `stop_recording` must not stop a listening session and
  `stop_listening` must not stop a recording.
- While listening, hold a power assertion so idle sleep does not cut the
  session; release it on stop. (My call.)
- Microphone choice follows the recording default: the explicit device UID
  if given, else the meeting app's input, else the system default. The
  existing microphone picker component is reused on the screen; switching
  the microphone mid-session is not required. (My call.)
- No interpretation text or audio content in logs.

### Interaction with other native features

- **Starting a recording while listening:** the listening session is stopped
  first, then the recording starts. This covers the record page, the
  meeting-detected prompt, and the control channel
  (`ttobak_app_start_recording`). Because the interpret screen stops its
  session on unmount, navigating to `/record` already ends it; the builder
  must make sure the stop has completed before the recording start is
  issued rather than relying on timing.
- **Meeting detection:** prompts keep appearing as today. Listening does not
  count as "already recording" for detection.
- **Control channel state:** reports not recording while listening.
- **System sleep:** `sleep.rs` stops a listening session at will-sleep, as
  it stops a recording. After wake the screen is idle with a line saying
  interpretation stopped because the Mac slept; the user presses start
  again. There is no automatic resume after sleep. (My call.)

### Web screen (`frontend/`)

- New static route `/interpret`. Add it to FrontendStack `knownPages`
  (`infra/lib/frontend-stack.ts`) and to the header title map.
- Sidebar gets a `navLabel` item (icon `translate`) rendered only when
  `isTauri()` is true, so browsers and mobile never see it.
- Layout:

  ```
  +----------------------------------------------------------+
  | title                                  [ ] interimToggle |
  | [microphone v]             level  00:12:41  [stopButton] |
  +----------------------------------------------------------+
  | language select (TranslationView header)                 |
  |  original caption          |  translated caption         |
  |  original caption          |  translated caption         |
  |  (pending caption)                                       |
  +----------------------------------------------------------+
  ```

  Idle state shows a single `startButton` button and a line stating that
  nothing is recorded or saved. The translation list fills the remaining
  window height (not the record page's 48vh card) and uses larger text.
  Reuse `TranslationView` where it fits; extend it with a size variant
  rather than forking it.
- Pipeline: `start_listening` → `onNativePcmChunk` → `SttManager.startNative`
  (Transcribe Streaming with the existing runtime config) →
  `LiveTranslation` → `/api/translate`. Translation is always on here.
  Whether to reuse `useRecordingSession` or a slimmer hook is the builder's
  choice, as long as no meeting API, checkpoint, upload, or transcript-save
  path can run from this screen.
- Stopping (button, navigating away, window close, sign-out) calls
  `stop_listening` and stops the STT session. The text already on screen
  stays until the user starts again or leaves the screen, then is gone.
- Errors, in Korean, reusing existing wording where one exists:
  - Live transcription drops mid-session (`transcribe-native-unavailable`
    after a successful start, for example a network blip): captions are the
    whole feature here, so the screen reconnects by itself. It keeps the
    native capture running, discards the failed STT session, and starts a
    fresh one, up to three attempts with a short backoff, showing
    `reconnecting`. Text already on screen is kept. If all attempts fail
    it stops the capture and shows the error. This retry lives in the
    interpret screen's own code; `sttManager.ts` and
    `transcribeStreamingClient.ts` behavior on the record page must not
    change. (My call.)
  - Transcribe not configured or unreachable at start: interpretation
    cannot run; say so and do not leave the capture running.
  - Microphone or system-audio permission denied: reuse the record page's
    start warnings.
  - There is no "capture lost" state here: today that flag is only set by a
    failed live microphone switch, which this screen does not offer (the
    microphone is chosen before start). No status command is added.
    `stop_listening` is idempotent, and the screen calls it once on mount
    so a session orphaned by a page reload cannot keep capturing.
  - Translation rate limit (`limited: true`): show the existing notice.
  - Installed Mac app too old to know `start_listening`
    (`isCommandNotFound`): show `VERSION_SKEW_MESSAGE`.
- Server load: uses the same per-user hourly translation limits as the
  record page (3000 fast, 1500 high). No new limit.
- Forgotten sessions: a session has no meeting to end it, holds a power
  assertion, and streams to Transcribe (which the translation limits do not
  cover). The screen therefore stops by itself after 10 minutes with no
  recognized speech and says why. Speech resets the timer. (User decision,
  2026-11-13.)

### Rollout

- Build on a fresh branch from `main`. The uncommitted
  `fix/mac-detect-native-output-only` work (`meeting_detect.rs`,
  `meeting.rs`, the meeting-detection ADR) is separate and is not carried along. A path-less
  listening session already reads as "not recording" to detection, so this
  design needs no detection-logic change; if the fix merges first, rebase.
- The web page is safe to deploy before users update the Mac app: an old
  app shows the sidebar entry and then the version-skew message.
- FrontendStack must be deployed (for `knownPages`) along with the frontend
  build, or a hard reload of `/interpret` falls back to `index.html`.
- The Mac app must be rebuilt and reinstalled for the new commands.

## Verification

- Rust: unit tests for the command registry/ACL test, for the
  "listening is not recording" behavior of every path-based consumer that
  can be tested without Core Audio, and for mutual exclusion in
  `AudioRecorder`. `cargo fmt --check`, `cargo clippy --all-targets -- -D
  warnings`, `cargo test`.
- Frontend: `npm run lint`, `npm run test:unit` (add `node:test` cases for
  any new pure logic), `npm run build`.
- Infra: `npx cdk synth && npm test`, including the `knownPages` change.
- Manual on a real Mac (no CI coverage for Mac changes; report this limit
  honestly): start/stop, both sources heard, no file appears under the
  recordings directory during or after a session, leftover list stays
  empty after a forced quit mid-session, starting a recording from the
  record page and from a meeting prompt while listening, old-app version
  skew message.

## Documentation

Update `docs/DESIGN-SPEC.md` (new screen), `docs/INFRA-SPEC.md`
(`knownPages`), `docs/architecture.md` (listen-only capture path), and
`mac-app/README.md`. Record the decision that listen-only capture shares the
recorder slot and never writes audio in the existing topic record
`ADR-024-mac-native-capture-and-upload.md` (decisions are capped at 30
topic records; see `docs/decisions/README.md`), and add the new commands
and event to `mac-app/CLAUDE.md`'s wire contract.

## Left to the builder

- Internal Rust shape for a path-less session (an enum on the handle, an
  optional path, or similar), provided the invariants above hold.
- Whether `stop_listening` returns diagnostics such as dropped-buffer
  counts.
- Hook structure on the web side and the exact text sizes.
- The precise mechanism that sequences "stop listening, then start
  recording".

## Revision note

Written against a stale checkout; re-checked on 2026-11-13 against `main`
at `887d2a3b`. Differences folded in above: caption overlays now exist
(ADR-018), ADRs were renumbered (native capture is ADR-024), and
`TranslationView` already has a `fill` variant. The in-app updater treats
a running capture as busy (`control::app_busy`); a listening session must
count as busy too.
