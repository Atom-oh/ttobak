# Multi-File Audio and Linked Meetings: Design

> Historical design record. Original date: not recorded. This preserves the
> original coordination design and its later corrections; copied code/signatures
> and historical guard descriptions are not current implementation mandates.

## Model and ordering

The design extended Meeting with optional `AudioKeys`, `AudioPartCount`,
`AudioPartsReady`, and `LinkedMeetingIDs`, while retaining `AudioKey`.
`GetEffectiveAudioKeys` would prefer the list and fall back to the legacy key.
Part order was an explicit index, not the order uploads or transcriptions finished.
No bulk migration of existing records was intended.

Audio keys used `audio/{userId}/{meetingId}/part_{NNN}_...`; transcript parts used
`transcripts/{meetingId}_part_{NNN}.json`. Single-file and archived merged output
used `transcripts/{meetingId}.json`. These are application audio parts, distinct
from S3's multipart-upload protocol.

## Upload and completion design

The refined proposal lazily preallocated indexed slots on the first multi-part
upload-complete call, then wrote the requested slot conditionally. It replaced an
earlier list-append sketch so retries and out-of-order completion would not reorder
parts. Inputs needed bounds, existing-meeting checks, and server-side key ownership.
The original maximum was ten parts; strict rejection of overwriting a populated slot
was optional, not a documented current rule.

Each uploaded audio triggered independent transcription with an explicit `OUTPUT_KEY`.
The summarize Lambda, not the transcribe Lambda, consumed part-transcript events
and determined readiness. A custom `AllPartsTranscribed` event initiated merge and
summary directly. Archiving the merged object could trigger another invocation,
which required state guards rather than relying on event delivery being unique.

## Merge and linked context

Merge would require ordered complete parts, preserve their content, and add each
part's duration to later timestamps. True decoded duration matters because the last
spoken word can precede the file end. For 20-, 15-, and 10-minute parts, offsets
would be zero, 20, and 35 minutes. Independent speaker labels need part namespaces;
that does not establish identity matching across recordings.

Follow-up links kept separate meetings and supplied at most three same-owner
predecessor summaries, capped at 2,000 characters plus 500 action-item characters
each. The API would validate target/predecessor ownership, self-links, and reverse
links. The UI would offer predecessor selection and navigation. Character limits
bound prompt size but are not a language-independent token guarantee.

## Intended UI, failures, and exclusions

The proposal included multi-file selection/reordering, per-file progress/retry,
playlist playback and absolute seeking, ready/total display, partial transcript
visibility, and carried-over action presentation. It proposed safe later append,
a cumulative 5 GB check, and part-scaled expiry up to 120 minutes. Those were
unverified extensions, not consequences of adding the core fields.

No retroactive meeting merge, audio editing, cross-user predecessor grants, or
new frontend library was required. Partial uploads, duplicate events, missing
transcripts, emission failures, inaccurate legacy duration, and share changes
needed focused tests and explicit recovery.

## Current corrections and evidence

- [Repository operations](../../../backend/internal/repository/dynamodb.go) track
  ready indices in a Number Set, not an unguarded increment. The
  [summarize handler](../../../backend/cmd/summarize/main.go) claims event emission
  and attempts compensation on send failure; this is not exactly-once delivery.
- [merge.go](../../../backend/cmd/summarize/merge.go) checks complete contiguous parts.
  The duration field is `whisper_metadata.duration_seconds`, not the draft's
  `duration`; logged fallback durations can drift.
- [AudioUploader](../../../frontend/src/components/AudioUploader.tsx) uploads
  sequentially. A new selection is not a safe append transaction for existing parts.
- The current predecessor-context gate re-reads sharing state and omits private
  linked context for collaborative targets. The link handler rejects direct
  reverse links, not arbitrary graph-wide cycles.
- [ADR-031](../../../docs/decisions/ADR-031-summarize-timeout-resilience.md) supersedes
  the old unconditional skip of stale `summarizing` jobs: retry eligibility after
  20 minutes differs from the 60-minute stuck threshold.

[ADR-014](../../../docs/decisions/ADR-014-multi-file-audio-and-linked-meetings.md),
[current API](../../../docs/API-SPEC.md), [infrastructure](../../../docs/INFRA-SPEC.md),
and [documentation map](../../../docs/README.md) are the current references.
