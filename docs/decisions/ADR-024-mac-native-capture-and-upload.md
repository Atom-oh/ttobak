# ADR-024: Mac native capture and upload

- Status: Accepted. Consolidates the earlier records on the Tauri Mac app,
  native streaming upload with system-audio captions, and the Core Audio
  process tap with microphone. Browser recording is ADR-006.
- Implementation checked: 2026-11-11. The Mac app has no CI; capture behavior
  needs verification on macOS.

## Decisions

### Shell

- The Mac app is a Tauri 2 window over the production web app. The remote
  capability is granted only to `https://ttobak.atomai.click`.
- Minimum macOS is 14.2, where the process tap API exists. The screen is never
  captured.
- The main window sets `dragDropEnabled: false`, so file drops reach the page's
  HTML5 drop zones instead of Tauri's native drop events. `FileDropGuard` blocks
  a file dropped outside any zone, which would otherwise navigate the WebView to
  the local file.

### Capture

- System audio comes from a private Core Audio process tap that excludes the
  app's own process. The tap and one input device run in one aggregate device
  clocked by the input, and the microphone is mixed into the recording. Without
  a chosen device the app uses the input a meeting app is using, else the
  system default. The aggregate is created without tap auto-start
  (`kAudioAggregateDeviceTapAutoStartKey` false, 2026-11-13): with auto-start,
  Core Audio held the whole aggregate, microphone included, until some tapped
  process played sound, so a capture started on a quiet Mac delivered nothing
  (no IOProc callback) until then. Observed on macOS 26 in `coreaudiod`
  ("Starting tap after waiting for writers"); with the setting off the
  microphone IO began 34 ms after start on a quiet Mac and a later session
  recorded a non-zero system-audio peak. Meetings hid the defect because the
  other participants' audio starts the tap.
- With no usable input device, capture is system audio only and start returns
  a warning.
- The real-time IOProc only mixes and enqueues to a bounded queue; a worker
  writes the WAV and flushes every five seconds. A full queue drops buffers and
  the count is reported as a stop warning. The worker emits events only while
  its recording generation is current.
- Teardown stops the device first (`AudioDeviceStop`), then destroys the IOProc,
  the aggregate and the tap. If a stop or detach fails, the callback context is
  leaked on purpose instead of freed under a live callback.
- The recorder lock is released around blocking Core Audio calls. Transition
  state changes in the same critical section, and `StartGuard` clears an
  abandoned start. Stop has a 10-second timeout and tracks finalization per
  file path, never through a global flag; a stop that times out does not
  certify a finished WAV.
- Zero callbacks or zero samples are hard errors; other problems are returned
  as warnings.
- Live interpretation without a meeting (2026-11-13) is a listening session:
  `start_listening` runs the same capture with no WAV writer and no path. It
  shares the one recorder slot, so it never captures alongside a recording.
  Nothing is written, and path-based code (status, upload, cleanup, leftover
  adoption, meeting detection) does not see it as a recording. A recording
  start stops it first; `sleep.rs` ends it at will-sleep whenever no recording
  runs (so a start still in flight is stopped once installed), as a task the
  sleep deadline does not cancel; the updater treats
  it as busy; it holds its own idle-sleep assertion. Listening starts and
  stops run one at a time (`listening_lifecycle`), so a stop waits for a
  start still in flight and then stops the session it installed. `stop_listening` only
  stops capture: the zero-sample check above is a recording check and does
  not apply. The `/interpret` screen stops a session after 10 minutes with no
  recognized speech, because nothing else ends it while it keeps the Mac
  awake and streams to Transcribe, which the translation limits do not cover.

### Upload

- Presigning, authentication and the completion call stay in the web app; only
  the byte transport is native.
- A finished WAV streams from disk to S3 in Rust and never crosses WebView IPC.
  Live PCM for captions does cross IPC, by design, as 16 kHz mono chunks of
  1,024 samples.
- The upload URL must be HTTPS and match the exact pinned bucket host
  (`EXPECTED_BUCKET_HOST`), not an AWS domain suffix.
- Timeouts measure stalled progress (60 seconds) and the wait for a response
  after the full send (180 seconds), not total duration. The web app retries
  with a fresh presigned URL per attempt inside a 45-minute budget.
- The WAV is deleted only after upload completion succeeds. The web app keeps
  pending audio until then, and a retry of the completion call does not upload
  again.
- Upload, cleanup and status accept only a path in the session's recorded or
  adopted set, not any file in the directory.

### Files and leftovers

- Recordings live in the app's directory under the system temp directory. The
  status helper checks containment before canonicalization, so it cannot be
  used to probe whether other files exist.
- On start, the app offers leftover WAVs for adoption, and the record page lists
  only those adopted at startup: regular files only
  (symlinks are never adopted), with a confirmation per file. Known-age files
  are cleaned up after at least 48 hours, best effort.

### Local control socket

- The MCP control socket sits in a 0700 directory with 0600 permissions,
  accepts only peers with the app's own uid, and bounds read time, request
  size (16 KiB) and concurrent connections. A stale socket is removed only
  after a failed connection probe, and a path that is not a socket is never
  deleted. If the socket fails to start, only that channel is disabled.
- Rust holds no meeting or sign-in state. Start and stop go to the signed-in
  web app as an event, and its replies and state reports are validated and
  size-bounded before they are forwarded. The app-control client is wired only
  to the stdio MCP server.
- Busy, signed-out and not-ready states are explicit errors, and a capture
  whose record page is closed reports `recording_unowned`. Start and stop time
  out and report an unknown outcome. A remote start needs the record screen and
  is refused on the file-upload screen or while a recording or upload is in
  progress. A stop uploads through the notes-skip path, and over-long notes are
  refused before upload.

## Not implemented or off by default

- A change of the default input device during a recording is not followed.
- Upload retry resends the whole file; there is no byte-range resume.
- Native screen capture for image attachments and a file logger are not built.
- Native sign-in is not implemented; authentication stays in the web app.
- Leftover recovery is not an offline sync queue: uploads start only from the
  app, with the user's confirmation. Storage outside the temp directory and
  binding leftovers to an account are not built.
- Acceptance still to record on macOS: a headphone call records both voices, a
  denied permission produces the silence warning, removing the input falls back
  to system audio, and the System Audio Recording prompt appears on 14.2 and
  14.3. If it does not appear there, raise the minimum version to the first
  release that shows it.
- Detection of zero callbacks at startup is not built.

## Limits and accepted risks

- Leftover WAV adoption is scoped to the macOS user, not the Cognito account.
  The per-file confirmation names this; files with unknown or future
  modification times may survive cleanup. There is no single-instance guard, so
  a second instance may adopt an active WAV.
- Force Quit cannot finalize a recording; the five-second flush limits the
  loss.
- The system tap can capture unrelated sounds; it excludes only the app itself.
- A listening session has no unit coverage beyond the recorder slot rules:
  preemption by a recording, the sleep stop and the absence of any file need a
  real Mac to verify.
- A Bluetooth headset used as input may switch to a lower-quality profile.
- Before recording, the web app probes the upload command with an empty path
  to catch a version mismatch with the app. The probe relies on path
  validation failing before any file or network work, so reordering that
  validation breaks it silently; a command-not-found error means the app needs
  an update, not a retry. This is a heuristic, not a compatibility check.
- If the app's own audio process cannot be identified, its sounds may be
  recorded; start returns a warning.
- With no CI for the Mac app, `cargo fmt`, `clippy`, `cargo test` and a signed
  build are run locally.
- Signing uses `codesign --deep`; notarization with a real Developer ID needs
  explicit nested signing (see ADR-019).
- The CSP in the Tauri configuration is not the policy in force for the remote
  window; the remote site's policy is.

## Source

`mac-app/src-tauri/src/` (`audio.rs`, `mix.rs`, `upload.rs`, `leftover.rs`,
`control.rs`, `control_proto.rs`, `input_select.rs`, `lib.rs`),
`mac-app/src-tauri/capabilities/default.json`, `mac-app/src-tauri/Info.plist`, `mac-app/scripts/sign.sh`, `frontend/src/lib/tauri.ts`,
`frontend/src/app/record/page.tsx`.
