# ADR-014: Meeting audio files and lookup

- Status: Accepted. Consolidates the earlier records on the GSI3 sort key for
  meeting lookup, multi-file audio and linked meetings, private audio crops
  and seekable playback copies.
- Implementation checked: 2026-11-11.

## Decisions

### Lookup by meeting ID

- GSI3 is keyed by `meetingId` and `entityType`. `GetMeetingByID` queries
  `entityType = MEETING` with a limit of one to discover the owner, then reads
  the base table. It assumes one canonical meeting entity per ID, indexed items
  must carry `entityType`, and any change to GSI3 needs its own reviewed
  rollout.
- Discovery is not authorization: the index is eventually consistent, and access
  is decided by the base-table read and the caller's identity. Callers that
  already know the owner read the base table directly.

### Multi-file audio and linked meetings

- A meeting has at most 10 audio parts (`MaxAudioParts`), stored as
  `audio/{userId}/{meetingId}/part_{NNN}_{name}` and uploaded sequentially.
- Each part transcript is `transcripts/{meetingId}_part_{NNN}.json`. Summarize
  records ready parts in an idempotent number set and merges only when the
  count matches and the indexes are contiguous. Speaker labels are namespaced by
  part index (ADR-009). Single-file meetings use the same path.
- A meeting may link up to three predecessors. The link endpoint rejects empty
  input, self-links and a one-hop reverse link. A non-creator member may link only
  predecessors published to the same account, through a fenced write.
- Linked predecessors are navigation only. Their summaries are never generation
  evidence for the successor's notes (ADR-070).

### Private audio crops

- A crop is cut from one single-part meeting in status `done` or `error`. The
  source key is derived from the creator's canonical meeting under
  `audio/{userId}/{meetingId}/`; clients cannot supply a key.
- The creator's own crop stays private. A managing account member may crop a
  published meeting: the copy still belongs to the creator, is published to the
  same account, and is created in one transaction that rechecks the source
  publication and the member's live grant.
- A crop copy keeps only saved notes and participants. Attachments, relations,
  shares, earlier transcripts, human summary edits and derived analyses are not
  carried over.
- Cropped audio is immutable: it cannot be replaced by an upload, and the
  worker's result key is excluded from ordinary STT.
- Limits: 6 hours selected, a 24-hour end offset and a 2 GiB source.
- The worker heartbeats every 30 seconds while FFmpeg runs with a file and pipe
  protocol whitelist and a 900-second timeout; an overrun or short output fails
  instead of shortening the range. Crop and copy writes
  are conditional and tied to a run; an ambiguous publication keeps the audio
  object instead of deleting it.

### Seekable playback copies

- WebM and Ogg recordings are remuxed into `audio-playback/{userId}/{meetingId}/`
  so browsers can seek. Checkpoint files are excluded.
- A copy is served only when its recorded source ETag and version ID match the
  current source; otherwise, or on a lookup error or a two-second timeout, the
  original is served. Copy keys are deterministic and replaced conditionally;
  recovered recordings are included. A copy never replaces the original, and STT reads only `audio/`.
- The remux role reads `audio/*` and `audio-playback/*` and writes only
  `audio-playback/*`. The prefix is in the OAC allowlist, moves to infrequent
  access after 90 days, and is covered by failed-recording cleanup.

## Not implemented or off by default

- Audio crop is off unless `ttobak:audioCropEnabled` is set, after the worker
  rollout and crop acceptance. Deploy the transcribe consumer, the crop rule
  and its invocation permission before the API producer.
- A failed crop is not restarted automatically; the user requests a new crop
  from the original meeting.
- The player plays only the first part of a multi-part meeting.
- Parts are merged in the transcript only; there is no record-level merge.
- Playback copies for older recordings exist only if the backfill script was
  run with `--apply`; it is a dry run otherwise and touches only keys that
  meeting rows reference.

## Limits and accepted risks

- The 500 MiB upload limit is enforced in the browser, not on the server.
- The link API does not enforce date order (only the picker does) and does not
  check the whole graph for cycles.
- Claiming the all-parts event and emitting it are separate writes, so delivery
  is at least once; a failed emit releases the claim.
- When duration metadata is missing, the last segment end stands in for the
  part duration and can miss trailing silence.
- Summaries generated while linked predecessors still fed generation were not
  rewritten.
- Crop tests with synthetic media and a stub recognizer do not establish
  production GPU or model acceptance.
- Deleting a meeting does not delete its S3 audio or playback copies.

## Source

`infra/lib/storage-stack.ts`, `ai-stack.ts`, `gateway-stack.ts`,
`backend/internal/repository/dynamodb.go`, `backend/internal/service/upload.go`,
`meeting_manager.go`, `audio_crop.go`, `audio_playback.go`,
`backend/internal/audioplayback/`, `backend/cmd/summarize/merge.go`,
`backend/cmd/audio-remux/`, `backend/whisper/audio_crop.py`,
`frontend/src/components/AudioPlayer.tsx`, `AudioUploader.tsx`.
