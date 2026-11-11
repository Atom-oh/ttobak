# ADR-006: Browser and mobile recording

- Status: Accepted. Consolidates the earlier records on system audio capture
  for remote meetings, browser tab capture, mobile live captions that never
  sacrifice the recording, and browser recording backups. The Mac app's native
  capture is ADR-024.
- Implementation checked: 2026-11-11.

## Decisions

### Tab audio

- Remote-meeting audio in a browser is captured from a shared tab with
  `getDisplayMedia`. The tab branch records only that stream; it does not open
  or mix the microphone. Shared video tracks are stopped, and a stream without
  an audio track is an error. The option appears for Chromium-family user
  agents (not Android) that support display capture.
- When an audio track ends, microphone or tab, recording stops through the
  normal path so the captured audio is finalized.

### Captions never cost the recording

- Recording continues when live captions fail; a caption error only raises a
  banner. A recorder or track failure stops the recording through the normal
  finalize path and is reported separately from a caption failure. The default caption provider is Transcribe Streaming on every
  platform.
- On iOS, iPadOS and Android, Web Speech is not offered and the fallback to it reports
  unavailability without opening the microphone, because a second microphone
  consumer can silence the recorder. The idle microphone preview is skipped for
  the same reason.
- An explicit Web Speech choice is never promoted to another provider, and a
  paused or stopped session is not restarted by late configuration.
- A caption watchdog detects a stalled stream and reconnects once per
  recording, which can wait until the page is visible. The wake lock and the
  visibility and focus handlers re-acquire audio. A waveform watchdog only
  detects a stall and offers its own manual recovery control, which is not
  redundant with automatic recovery.
- Recovery messages clear only on evidence: the first transcript after a
  reconnect, or waveform confirmation, not the start of an audio context.
- Manual recovery runs synchronously from the click: `resumeAudio()` and
  `manualStallRecovery()` are called before any `await`, because browsers allow
  audio to resume only inside a user gesture.
- The capture worklet downsamples from the context's actual rate to 16 kHz; no
  sample rate is forced.

### Browser recording backups

- Recorded chunks are saved in IndexedDB under the Cognito `sub` and a random
  recording ID. A chunk and
  its metadata commit together, and the saved time advances only after commit.
- After the first failed append the queue stops, keeping the contiguous
  committed prefix. A storage or lock failure never stops capture or blocks
  uploading the complete in-memory audio.
- A stop can keep the recorded audio and final notes for a later upload. The
  recovery card offers resume, download and a confirmed delete.
- On the server, a meeting that has only checkpoint files is user data: the
  scheduled recovery tick reports it and the owner recovers it; it is never
  deleted automatically. Checkpoint names must match the exact allowlisted
  `recording_progress.{webm|m4a|ogg}` names, not a suffix (ADR-013).
- One tab owns a recording through a Web Lock; without the lock there is no
  recovery. A generation counter rejects blobs from an earlier flow.
- Restore re-checks the signed-in user and binds to the server meeting: a
  missing meeting clears the stored identity, other errors keep it, and a
  different audio key is refused. The same upload key is reused across
  acknowledgement retries. When an acknowledged upload is restored and the
  saved notes or title differ from the server, the copy is kept and the notes
  step reopens without re-reading audio.
- The local copy is deleted only after upload completion is acknowledged or
  after an explicit, confirmed delete. There is no retention clock.

## Not implemented or off by default

- No automatic sync queue: upload resumes in the current flow or from the
  recovery card.
- Metadata-only rows are hidden from the list but never cleaned up.
- No TTL and no purge on sign-out.
- Tab audio is unavailable in Firefox, Safari and on Android.
- Reload, account switch, lock contention, quota failure and receipt replay
  are checked by hand in a real browser; the scripted tests cover the backup
  and recovery logic only.

## Limits and accepted risks

- Backups are plaintext in the browser profile, keyed by user ID. Isolation is
  at the application level only, not encryption. Same-origin script or the profile owner can read them; the
  card warns about shared devices.
- Tab capture leaves out the local speaker when that voice is not in the
  tab's output. It records one combined track with no per-participant
  channels, and depends on the user's permission and the chosen source.
- Mobile streaming uses the existing authenticated identity-pool permission
  and adds no IAM capability.
- The server's seven-day deletion of failed recordings (ADR-013) is separate
  from browser backups and never touches owner-recoverable checkpoints.
- The recovery API depends on the transcribe function in GatewayStack, so the
  consumer deploys first; local tests do not prove a rollout.
- Desktop Web Speech can send audio to the browser vendor. The default
  streaming path sends audio to AWS and adds streaming usage and cost.
- A phone without working Transcribe Streaming has no caption fallback, and
  audio contexts can still suspend, so captions are not guaranteed.
- Recovery covers committed chunks only. Storage eviction, quota, unavailable
  Web Locks, a suspended recorder or a missed final callback can limit it, and
  closing a tab or lid gives no guarantee of a final upload.
- Suspension behavior on physical iOS and Android devices cannot be shown from
  source.

## Source

`frontend/src/components/RecordButton.tsx`, `frontend/src/app/record/page.tsx`,
`frontend/src/lib/` (`device.ts`, `sttManager.ts`, `transcribeStreamingClient.ts`,
`browserRecordingBackup.ts`), `frontend/src/hooks/useRecordingSession.ts`,
`usePostRecording.ts`, `frontend/src/components/record/BrowserRecordingsCard.tsx`,
`frontend/public/pcm-processor.js`,
`frontend/scripts/recording-backup.test.mjs`, `recording-recovery.test.mjs`,
`backend/internal/service/stt_recovery.go`.
