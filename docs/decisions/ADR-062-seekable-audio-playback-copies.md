# ADR-062: Seekable audio playback copies

- Status: Accepted; staged across several PRs. This PR adds the storage
  prefix, key mapping and cleanup coverage; the remux worker, API selection
  and backfill follow.
- Decision date: 2026-11-06.
- Related: ADR-014 (multi-part audio), ADR-031 (STT recovery), ADR-022
  (source-bound previews).

## Context

Browser recordings are MediaRecorder WebM/Opus. Headers of the six latest
`audio/` objects showed a Segment of unknown size with no Duration element and
no Cues, so `<audio>.duration` is `Infinity`/`NaN` and seeking does not work.
An interrupted recording can also end mid-cluster (`ffmpeg` reports "File ended
prematurely"). `ffmpeg -c copy` rewrites such a file with Duration and Cues and
drops the incomplete tail without re-encoding.

The original object is the input to STT, crop, checkpoint recovery and the
ADR-031 recovery tick, so it must not be rewritten in place.

## Decision

- A playback copy lives at `audio-playback/{userId}/{meetingId}/{name}` for a
  source `audio/{userId}/{meetingId}/{name}` (`backend/internal/audioplayback`).
  The separate top-level prefix keeps the copy's S3 event out of the `audio/`
  transcription rule.
- Only WebM/Ogg sources get a copy. `recording_progress.*` checkpoints are
  excluded because they are overwritten during recording; recovered copies are
  included. M4A, WAV and other containers already carry duration data.
- A copy records its source's `source-etag` and `source-version-id` metadata.
  Readers use a copy only when both equal the source's current HEAD; otherwise
  they serve the original. The mapping is deterministic, so a worker replaces a
  copy conditionally instead of accumulating objects.
- The OAC read allowlist, the 90-day IA transition and failed-recording grace
  deletion include `audio-playback/`.
- The worker reads `audio/*` and writes `audio-playback/*` only. Existing
  recordings are converted by a one-off backfill after deployment.

## Consequences

- Storage roughly doubles for remuxed recordings until both age to IA.
- Copies are optional: a missing, stale or failed copy falls back to the
  original, so deployment order is not a correctness dependency.
- User meeting deletion does not remove S3 audio today; copies share that
  existing gap. Multi-part meetings get one copy per part, but the player
  still plays only the first part.
