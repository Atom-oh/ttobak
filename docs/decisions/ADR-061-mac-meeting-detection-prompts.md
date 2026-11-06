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

- A background thread polls Core Audio: the default input device's
  `kAudioDevicePropertyDeviceIsRunningSomewhere` every 2 s, then once per second
  while the microphone is in use, `kAudioHardwarePropertyProcessObjectList`
  with each process's `IsRunningInput`, `PID` and `BundleID`. The app's own
  process is excluded. Recorder locks are not held during these calls.
- Native meeting apps (Zoom including its capture host, Teams, Webex, Slack,
  FaceTime, Amazon Chime, Discord) are matched by bundle ID or helper ID.
- A browser using the microphone counts only when one of its tabs is a meeting
  URL (`meet.google.com/<code>`, Teams web, Zoom web client). A fixed
  AppleScript run through `osascript` filters tab URLs inside the browser and
  returns only meeting URLs. URLs are classified and never logged. This needs
  the macOS Automation permission per browser; a denial backs off for five
  minutes. Safari's shared `com.apple.WebKit.GPU` process counts only with a
  Safari meeting tab, because other WebKit hosts share it. Firefox has no tab
  scripting and is not detected.
- `meeting.rs` holds the pure rules: a 3 s sustain before announcing, one
  announcement at a time, no announcement while the app records, and an end
  after 20 s of absent microphone use. A dismissal silences that source until it
  ends.
- Rust emits `native-meeting-detected`/`native-meeting-ended` to the main window
  and posts a local notification. The SPA banner's confirmation calls
  `meeting_detection_respond`, which sends the start/stop through the same
  `native-control-request` path as ADR-046, so busy and sign-in checks are
  unchanged. Detection starts do not post the MCP "started" notification.
- Detection is on by default and toggled in Settings; the preference is stored
  in the app config directory.

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
