# ADR-014: Multi-Part Audio and Linked Follow-Up Meetings

- Status: Accepted; upload, transcription, merge, playback, and linking implemented.
- Decision date: Not recorded; upload/playback completion recorded as 2026-05-26.
- Implementation checked: 2026-09-13.

## Context and decision

Recorder interruptions and external devices can split one conversation across
files. Process each audio part independently and merge transcripts, rather than
concatenating mixed codecs on the server or materializing long audio in browser
memory. Preserve single-file compatibility through `GetEffectiveAudioKeys`.

Separately, link follow-up meetings to predecessors for bounded contextual
summaries. Linking retains independent meetings; it does not merge their records
or authorize sharing a predecessor's content.

## Current multi-part implementation

- `AudioKeys`, `AudioPartCount`, and `AudioPartsReady` extend the meeting model.
  The API supports at most ten parts; the uploader checks 500 MiB per file.
- Presigned audio keys include `part_{NNN}_`. Upload completion preallocates an
  indexed list and writes a fixed slot; it does not append in completion order
  or count uploaded files as completed transcripts.
- Transcribe writes `transcripts/{meetingId}_part_{NNN}.json`. The summarize
  Lambda records completed indices in an idempotent Number Set, claims the
  all-parts event, and emits `AllPartsTranscribed`. Emit failure attempts to
  release the claim for retry. This coordination belongs to summarize, not a
  missing transcribe-phase increment.
- Merge requires the expected part count and contiguous indices. It sorts parts,
  refines text, namespaces speakers by part, offsets segment times, and summarizes
  the combined transcript. Prefer true audio duration; last refined/raw segment
  end is a logged fallback that can underestimate trailing silence.
- `AudioPlayer` advances through separate files with part controls. The uploader
  sends files sequentially and assigns part indices only to audio, not image or
  document attachments. ECS jobs can overlap as capacity permits; parallel
  transcription is possible, not a guaranteed three-task speedup.

## Follow-up linking and sharing boundary

The link endpoint accepts one to three owned predecessor IDs, verifies the target
is owned, rejects self-links and direct reverse links, and partially updates
`linkedMeetingIds`. The picker excludes later-dated meetings and orders selections
chronologically. The API does not independently enforce that date ordering or a
complete graph-wide cycle check; context traversal is limited to one level.

Summarization includes at most three same-owner predecessor summaries, each capped
at 2,000 characters plus 500 characters of action items. **It omits all linked
context when the target has any non-owner share or is shared to an account.**
The gate re-reads base-table meeting state and shares consistently and fails
closed on lookup errors. This prevents a collaborator from using the target to
extract private predecessor content; it deliberately also skips account shares
with no currently materialized per-member Share row.

## Consequences and residual risks

Users retain split recordings without external merging, and private follow-ups
can reuse prior decisions. Coordination adds retry/claim windows, duration fallback
can shift later timestamps, and audio playback remains multi-track. Claiming an
event and sending it are separate writes, not an exactly-once guarantee. Namespace
separation avoids speaker collisions but does not identify the same person across
parts. Record-level meeting merging and automatic action-item reconciliation are
not established by this design. A generated summary copied or shared later is
not automatically scrubbed of context already included.

## Evidence

- [upload.go](../../backend/internal/service/upload.go),
  [dynamodb.go](../../backend/internal/repository/dynamodb.go): part validation,
  indexed writes, ready-set and event claims.
- [main.go](../../backend/cmd/summarize/main.go),
  [merge.go](../../backend/cmd/summarize/merge.go),
  [bedrock.go](../../backend/internal/service/bedrock.go): merge and collaborator gate.
- [meeting.go](../../backend/internal/handler/meeting.go): `LinkMeetings`.
- [AudioUploader.tsx](../../frontend/src/components/AudioUploader.tsx),
  [AudioPlayer.tsx](../../frontend/src/components/AudioPlayer.tsx),
  [LinkMeetingsModal.tsx](../../frontend/src/components/meeting/LinkMeetingsModal.tsx).
