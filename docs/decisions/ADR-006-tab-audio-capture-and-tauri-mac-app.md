# ADR-006: Browser Tab Capture and Native macOS System Audio

- Status: Accepted; supersedes [ADR-001](ADR-001-system-audio-capture-for-remote-meetings.md).
  Native transport and recovery are extended by
  [ADR-024](ADR-024-mac-app-native-streaming-upload-and-system-audio-captions.md);
  the ScreenCaptureKit capture engine is superseded by
  [ADR-046](ADR-046-mac-core-audio-tap-and-microphone.md).
- Decision date: Not recorded; browser design specification dated 2026-04-20.
- Implementation checked: 2026-09-13.

## Context and decision

Browser meetings and desktop conferencing apps need different capture paths.
Choose browser tab recording where available and a Tauri macOS wrapper using
ScreenCaptureKit for desktop system audio. Reuse the SPA, authenticated upload
flow, and backend transcription pipeline.

Tauri was preferred to Electron to reuse the system WebView and avoid bundling
Chromium. Browser-only capture could not meet the macOS desktop-app requirement;
an extension or virtual loopback driver added installation and routing work
without the same integrated native path.

## Current implementation and superseded plans

- `RecordButton` selects microphone, tab, or Tauri system audio. Tab capture uses
  `getDisplayMedia`, stops video tracks, and requires an audio track. It records
  the selected tab stream without a separately mixed microphone.
- The Mac app loads the deployed SPA in its WebView. SPA authentication and
  presign/upload-complete requests remain in the frontend. The original plan for
  a separate native OAuth PKCE login was not implemented.
- Native capture writes WAV to disk. Rust streams the finished file to S3;
  completed WAV bytes must not cross WebView IPC. Small PCM chunks cross IPC
  deliberately for live captions, alongside level and upload-progress events.
- Native screen capture supplies meeting image attachments. The native logger
  writes `<tmp>/ttobak-mac/app.log`.
- Local WAV recovery exists, but it is not a general offline-first application
  or automatic connectivity-based synchronization queue. Phone/external-recorder
  uploads remain a complementary path.
- Distribution uses local builds and ad-hoc signing with explicit entitlements.
  There is no Mac CI or completed Developer ID notarization flow.

## Consequences and accepted risks

The hybrid provides native capture while retaining one backend. It adds Rust/macOS
maintenance, permission prompts, source-selection limitations, and local file
lifecycle work. System audio may include unrelated sounds.

Preserve ADR-024's exact S3-host pin, path containment, recorder-lock/FFI separation,
RAII start reservation, and bounded upload waits. These are current constraints,
not optional elements of the original sketch.

Startup best-effort deletes leftovers whose known age is at least 48 hours;
unreadable/future modification times may still be adopted. This is not a hard
retention cutoff or continuous sweep. Upload/delete requires per-file confirmation. They are scoped to the macOS user's directory,
**not** the Cognito account: a subsequent SPA login can access another login's
leftover. That is an accepted residual risk, not fixed by the confirmation dialog.
Ad-hoc `codesign --deep` is existing distribution debt; the Tauri CSP setting must
not be presented as the effective policy for the remote-origin WebView.

## Evidence

- [RecordButton.tsx](../../frontend/src/components/RecordButton.tsx),
  [tauri.ts](../../frontend/src/lib/tauri.ts),
  [usePostRecording.ts](../../frontend/src/hooks/usePostRecording.ts).
- [tauri.conf.json](../../mac-app/src-tauri/tauri.conf.json),
  [lib.rs](../../mac-app/src-tauri/src/lib.rs),
  [audio.rs](../../mac-app/src-tauri/src/audio.rs),
  [upload.rs](../../mac-app/src-tauri/src/upload.rs),
  [leftover.rs](../../mac-app/src-tauri/src/leftover.rs).
- [sign.sh](../../mac-app/scripts/sign.sh): local signing.
