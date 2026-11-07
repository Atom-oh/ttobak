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
  AppleScript run through `osascript` receives the browser's tab URLs, discards
  non-meeting ones inside that child process, and returns only meeting URLs to
  the app, which classifies and never logs them. This needs the macOS
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
- Rust emits `native-meeting-detected`/`native-meeting-ended` to the main window
  and posts a local notification. The SPA banner's confirmation calls
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
  while only the aggregate is rebuilt (a sub-second gap). A device that cannot
  run at the recording's sample rate is refused, and a failed start restores
  the previous device.
- The recording follows Zoom's own mute, as Notion's meeting notes do
  (observed in Notion's shipped `mac_utils.node`: an Accessibility observer of
  Zoom's mute menu item, toolbar and tiles). `zoom_mute.rs` polls Zoom's menu
  bar every 300 ms for the "Mute Audio"/"Unmute Audio" item, using labels from
  Zoom's own `Localizable.strings` in every language, and mixes the microphone
  at zero gain while Zoom shows the user muted. Once a recording has followed
  Zoom, a confirmed end (Zoom quit, or neither the `zm.meeting.window.main`
  window nor an audio item remains) silences the microphone at once, and an
  unreadable stretch over 3 s does too: Zoom no longer hears the user. A
  recording that never saw a Zoom state records the microphone as before.
  The same confirmed end lets detection end the Zoom meeting without the 20 s
  audio grace. This needs Accessibility permission; without it the start
  response warns and the microphone is always recorded. Ad-hoc re-signing
  invalidates the grant, as it does other TCC grants.
- When the Mac is about to sleep during a recording (e.g. the lid closes
  without a stop), `sleep.rs` stops it through the control path with the
  upload requested and delays sleep until the stop is answered or 20 s pass.
  On wake the app emits `native-system-wake` and the record page retries an
  upload left in the error state. Clamshell use on an external display does
  not sleep and keeps recording.

## Consequences

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
  that speech is not recorded. Zoom's web client and other meeting apps keep
  recording the microphone regardless of their mute.
- An upload interrupted by sleep completes only after wake.
