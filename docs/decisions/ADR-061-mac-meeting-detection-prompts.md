# ADR-061: Mac meeting detection prompts

- Status: Accepted for implementation; detection needs macOS verification with
  real Zoom, Teams and browser meetings.
- Decision date: 2026-11-06.
- Builds on: ADR-046 (Core Audio capture and the local control path).

## Context

Users forget to start a TTOBAK recording when a Zoom or Google Meet call
begins. A static review of Notion's desktop app (7.41.1) showed the common
desktop approach: a native module watches the default input device's
`IsRunningSomewhere` state, polls the Core Audio processes that are using
input, and the application decides whether to show a "start meeting notes"
prompt. Notion's prompt is a suggestion; it does not record by itself.

Mic activity alone cannot tell a Google Meet tab from any other browser
microphone use, because the browser process is the one using the input.

## Decision

The Mac app detects meetings and **offers** a recording; it never starts one
without a user confirmation.

- A background thread polls `kAudioHardwarePropertyProcessObjectList` (every
  2 s, every second while any process has audio running) and reads each
  process's `IsRunningInput`, `IsRunningOutput`, `PID` and `BundleID`. The app's
  own process is excluded and recorder locks are not held. The default input
  device's `IsRunningSomewhere` is not used: Zoom was observed capturing from a
  non-default USB microphone while that flag stayed false.
- Input or output counts, so a listen-only participant is detected even after
  the app or browser releases the microphone. Output-only browser audio checks
  tabs only for a browser whose tab script already succeeded in this run, so a
  video never raises the first Automation prompt.
- Native meeting apps (Zoom including its capture host, Teams, Webex, Slack,
  FaceTime, Amazon Chime, Discord) are matched by bundle ID or helper ID.
- A browser using the microphone counts only when one of its tabs is a meeting
  URL (`meet.google.com/<code>`, Teams web, Zoom web client). A fixed
  script run through `osascript` receives the browser's tab URLs, discards
  non-meeting ones inside that child process, and returns only meeting URLs to
  the app, which classifies and never logs them. The script addresses the
  browser instance whose helper runs the audio: the app walks parent
  processes to the nearest app main executable and reads that PID's tabs
  through Scripting Bridge (PID passed as an argument). `tell application id`
  reaches only one instance, and an automation-launched Chrome beside the
  user's hid the Meet tab. If the instance cannot be addressed, or for Safari's
  shared WebKit process, the bundle-ID AppleScript is used. This needs the macOS
  Automation permission per browser; a denial backs off for five minutes, and
  the script may wait up to 30 s for the first consent dialog. Safari's shared
  `com.apple.WebKit.GPU` process counts only with a Safari meeting tab, because
  other WebKit hosts share it. Firefox has no tab
  scripting and is not detected.
- `meeting.rs` holds the pure rules: a 3 s sustain before announcing, one
  tracked meeting at a time, and an end after 20 s of absent audio. A meeting
  that starts during a recording is tracked silently; it is announced if the
  recording stops while the meeting continues. A dismissal silences that source
  until it ends.
- When a tracked meeting ends during a native recording (started from the
  prompt or by hand), the SPA shows a 30 s countdown, then stops and uploads that
  recording unless the user keeps recording. The stop is bound to the WAV path
  running when the meeting ended, so a later recording is never stopped by it.
  The automatic path only ever sends that one stop. If the meeting's audio
  returns during the countdown, the app resumes it at the first sighting (no
  sustain), reports it as resumed and clears the binding, and the stop command
  also refuses while that audio is present. The SPA auto-stops only when
  `meeting_detection_get` reports `autoStop`; older app builds keep the manual
  stop prompt.
- Automatic microphone choice prefers native meeting apps over browsers and
  ignores Safari's shared WebKit process.
- Rust emits `native-meeting-detected`/`native-meeting-ended` to the main window.
  A detection also opens a small always-on-top prompt window at the top center
  of the screen (`meeting_prompt.rs`, bundled `meeting-prompt.html`) with Record
  and Ignore, as Notion does; the app's macOS notifications were not shown
  reliably. Its only command, `meeting_prompt_choose`, goes through
  `meeting_detection_respond`, and `native-meeting-answered` closes the banner.
  It closes after 30 s unanswered, on an answer in the banner, or at the end.
  An end still posts a local notification. The SPA banner's confirmation calls
  `meeting_detection_respond`, which sends the start/stop through the same
  `native-control-request` path as ADR-046, so busy and sign-in checks are
  unchanged. Detection starts do not post the MCP "started" notification.
- Detection is on by default and toggled in Settings; the preference is stored
  in the app config directory.
- A recording uses the microphone the user picks in System Audio mode, or by
  default the input device the meeting app is capturing from (process
  `Devices`, input scope), else the system default. This keeps multi-microphone
  and clamshell setups aligned with what Zoom hears. `switch_recording_input`
  changes the microphone during a recording: the tap, WAV and worker continue
  while only the aggregate is rebuilt; the capture gap needs native measurement.
  A device that cannot run at the recording's sample rate is refused. A failed
  start attempts to restore the previous device. If restoration also fails,
  capture stops: the switch returns an error saying so, later switches are
  refused, and stop finalizes what was recorded with a warning.
- The recording follows Zoom's own mute, as Notion's meeting notes do
  (observed in Notion's shipped `mac_utils.node`: an Accessibility observer of
  Zoom's mute menu item, toolbar and tiles). `zoom_mute.rs` polls Zoom's menu
  bar every 300 ms for the "Mute Audio"/"Unmute Audio" item, using labels from
  Zoom's own `Localizable.strings` in every language, and mixes the microphone
  at zero gain while Zoom shows the user muted. When the followed meeting
  ends (Zoom quit, or neither the `zm.meeting.window.main` window nor an audio
  item remains) or stays unreadable for over 3 s, the microphone reopens: a
  wrong end then records the user rather than dropping their voice. A failed
  Core Audio process-list read is not evidence Zoom quit. A recording that
  never saw a Zoom state records the microphone as before. Detection still
  ends a Zoom meeting only after the 20 s audio grace; ending it earlier on
  Zoom's own UI is deferred until it is verified on a Mac.
- Google Meet keeps the microphone open while muted, so `meet_mute.rs` reads
  the Meet page of each Chromium browser instance playing or capturing audio
  through Accessibility (the `AXWebArea` whose URL is `meet.google.com`). The
  toggle's label gives the state ("Turn on microphone" while muted, "Turn off
  microphone" while unmuted, and their Korean forms; both verified on a live
  call). The call page that last showed the toggle has left the call only
  when it shows Meet's post-call screen ("Rejoin", "Return to home screen";
  the Korean forms are unverified). A page without the toggle is otherwise
  unknown and keeps the last state, so a toolbar hidden in the call (for
  example behind a dialog) is never read as leaving. Pages of other calls (the
  landing page or an earlier call) are not evidence, and once a second call
  has shown a toggle during the recording no page is. Leaving the followed
  call, read for 3 s, reopens the microphone, as with Zoom.
  A recording that never saw the toggle (e.g. an unlisted language) records the
  microphone. An unreadable page (another tab in front) keeps the last state,
  including when Accessibility trust is lost after an observed state. Safari's
  shared WebKit process is not followed. Without Accessibility permission at
  startup, the response warns and the microphone records normally until a
  meeting's mute has been observed. Ad-hoc re-signing invalidates the grant,
  as it does other TCC grants. Detection ends a Meet meeting only after the
  20 s audio grace, as before.
- During a native recording, if other apps play audio for 30 s while the
  system tap has delivered only exact zeros, the app posts a notification that
  System Audio Recording is likely denied, instead of reporting it only at
  stop. A process launched from a terminal is attributed to that terminal by
  TCC; launch the app through Finder or `open` when testing permissions.
- When the Mac is about to sleep during a recording (e.g. the lid closes
  without a stop), `sleep.rs` stops it through the control path with the
  upload requested and delays sleep until the stop is answered or 20 s pass.
  On wake the app emits `native-system-wake` and the record page retries an
  upload left in the error state. Clamshell use on an external display does
  not sleep and keeps recording.

## Consequences

### Staged mute-follow library

`zoom_mute` and `meet_mute` were introduced as compiled public library modules
without activating capture. This runtime supplies `ProcessSources` using the
existing Core Audio process/browser discovery, calls `watch`, retains its handle
and consumes the microphone flag. `meeting_over` uses the same injected discovery.
Native acceptance remains required before claiming that recording follows a
meeting application's mute.

When invoked, the watcher may request Accessibility access once per app launch.
It emits `native-mic-muted` with `muted`, `app` and an optional `reason`
(`muted`; `meeting_ended` is reserved and not emitted). Zoom labels come from its localization resources.
Unreadable window identifiers are unknown, never a confirmed meeting end;
after three seconds of unknown state the gate stops following and reopens the
microphone.
An observed Zoom silence reaches the microphone gate before synchronous browser
inspection, so a slow Meet accessibility tree cannot delay that known decision.

Meet's English/Korean muted toggle labels and readable-page fallback prioritize
not dropping voice in unsupported languages. Unknown pages retain the previous
state; another readable unmuted page wins. A page read whose role, label, URL, window or
children attribute fails (any Accessibility status other than success, no
value or unsupported) or that reaches the node or depth bound is unknown, as is a search cut short before every
window was read, and a lost Accessibility grant feeds unknown to the hold, so
none of them clears a retained mute. A Zoom menu read that fails is unreadable,
not an empty menu bar. Hidden-tab or departed-call holds are not proof of the
user's current intent. These limitations need native verification
and remain separate from the compiled library's availability.

- No new network origin, IAM change or server route. The SPA owns titles,
  meeting creation and uploads as before.
- Users see an Automation permission prompt the first time a browser meeting
  uses the microphone.
- Unlisted apps and browsers are not detected. Teams/Zoom web tabs count even
  when the call is in another tab of the same browser that uses the
  microphone; the prompt still requires confirmation.
- No CI covers this module; the Core Audio probe and AppleScript path need
  manual verification on macOS.
- Push-to-talk (holding Space while muted) is not reflected in Zoom's menu, so
  that speech is not recorded. Zoom's web client, Teams and other meeting apps keep recording the
  microphone regardless of their mute; Meet's mute is followed in Chromium
  browsers only, in English and Korean.
- An upload interrupted by sleep completes only after wake.
