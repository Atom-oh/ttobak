# ADR-014: Meeting audio files and lookup

- Status: Accepted. Consolidates the earlier records on the GSI3 sort key for
  meeting lookup, multi-file audio and linked meetings, private audio crops
  and seekable playback copies.
- Implementation checked: 2026-11-11.

## Decisions

### Lookup by meeting ID

- GSI3 is keyed by `meetingId` and `entityType`. `GetMeetingByID` queries
  `entityType = MEETING` with a limit of one to discover the owner, then reads
  the base table.
- Discovery is not authorization: the index is eventually consistent, and access
  is decided by the base-table read and the caller's identity. Callers that
  already know the owner read the base table directly.

### Multi-file audio and linked meetings

- A meeting has at most 10 audio parts (`MaxAudioParts`), stored as
  `audio/{userId}/{meetingId}/part_{NNN}_{name}` and uploaded sequentially.
- Each part transcript is `transcripts/{meetingId}_part_{NNN}.json`. Summarize
  records ready parts in an idempotent number set and merges only when the
  count matches and the indexes are contiguous. Speaker labels are namespaced by
  part index (ADR-009).
- A meeting may link up to three predecessors. Empty input, self-links and a
  one-hop reverse link are rejected. A non-creator member may link only
  predecessors published to the same account, through a fenced write.
- Linked predecessors are navigation only. Their summaries are never generation
  evidence for the successor's notes (ADR-070).

### Private audio crops

- The owner may crop one single-part meeting in status `done` or `error`. The
  source key is derived from the owner's canonical meeting under
  `audio/{userId}/{meetingId}/`; clients cannot supply a key.
- Limits: 6 hours selected, a 24-hour end offset and a 2 GiB source.
- The worker heartbeats while FFmpeg runs under a timeout. Crop and copy writes
  are conditional and tied to a run; an ambiguous publication keeps the audio
  object instead of deleting it.

### Seekable playback copies

- WebM and Ogg recordings are remuxed into `audio-playback/{userId}/{meetingId}/`
  so browsers can seek. Checkpoint files are excluded.
- A copy is served only when its recorded source ETag and version ID match the
  current source; otherwise, or on a lookup error or timeout, the original is
  served. A copy never replaces the original, and STT reads only `audio/`.
- The remux role reads `audio/*` and `audio-playback/*` and writes only
  `audio-playback/*`. The prefix is in the OAC allowlist, moves to infrequent
  access after 90 days, and is covered by failed-recording cleanup.

## Not implemented or off by default

- Audio crop is off unless `ttobak:audioCropEnabled` is set; production
  acceptance is recorded separately from the code.
- The player plays only the first part of a multi-part meeting.
- Parts are merged in the transcript only; there is no record-level merge.
- Playback copies for older recordings exist only if the backfill script was
  run with `--apply`.

## Limits and accepted risks

- The 500 MB upload limit is enforced in the browser, not on the server.
- Deleting a meeting does not delete its S3 audio or playback copies.

## Source

`infra/lib/storage-stack.ts`, `ai-stack.ts`, `gateway-stack.ts`,
`backend/internal/repository/dynamodb.go`, `backend/internal/service/upload.go`,
`meeting_manager.go`, `audio_crop.go`, `audio_playback.go`,
`backend/internal/audioplayback/`, `backend/cmd/summarize/merge.go`,
`backend/cmd/audio-remux/`, `backend/whisper/audio_crop.py`,
`frontend/src/components/AudioPlayer.tsx`, `AudioUploader.tsx`.
