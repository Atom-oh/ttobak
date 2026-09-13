# Multi-File Audio and Linked Meetings: Requirements

> Historical requirements record. Original date: not recorded. These user stories
> accompanied ADR-014; unchecked proposals are not current requirements, and neither
> the stories nor earlier status marks prove implementation or deployment.

## Intended user outcomes

1. **Split-recording upload:** select several audio files for one meeting, arrange
   their order, see per-file progress, and receive one transcript/summary/action
   list without external audio concatenation. Independent transcription could
   overlap when capacity permitted.
2. **Later audio additions:** append supplementary files to a completed/error
   meeting without replacing prior audio, then re-merge old and new transcripts.
   This was a distinct requested workflow, not implied by initial multipart upload.
3. **Single-file compatibility:** preserve one-file upload and legacy `audioKey`
   playback; audio without a part prefix retained the single-transcript path.
4. **Playback:** advance through parts, show part/total progress, and eventually
   map an absolute transcript timestamp to the correct file and local offset.
5. **Follow-up context:** link owned predecessor meetings at creation/detail time,
   retain separate records, navigate their chain, and use bounded prior summaries
   and actions to improve continuity. Only direct predecessors supplied context.
6. **Processing visibility:** show ready/total progress, identify failed parts, and
   make completed partial transcripts available while remaining work continued.

The original stories also proposed reorder controls, overall progress, failed-part
retry, automatic append/reprocessing, cross-track seek, partial transcript display,
and structured carried-over action badges. Each needs separate implementation
verification; none follows automatically from the existence of `audioKeys`.

## Scope and constraints

Excluded: retroactive merging/deleting completed meeting records, in-browser audio
editing, live recording automatically producing multipart files, and cross-user
predecessor linking. The intended benefit was continuity without destructive source
merge or codec conversion.

File identity/order, duplicate completion events, missing parts, long processing,
legacy duration metadata, and ownership checks were material risks. Linking a
predecessor did not grant a target meeting's collaborators access to that private
predecessor. Historical action-item status examples were desired model output, not
an established structured reconciliation system.

## Current mapping and limits

[ADR-014](../../../docs/decisions/ADR-014-multi-file-audio-and-linked-meetings.md)
records implemented multi-part upload, merge, playback, and linking. The current
[uploader](../../../frontend/src/components/AudioUploader.tsx) sends selected files
sequentially, assigns indices only to audio, and does not calculate a safe append
range from existing meeting parts. The indexed
[upload service](../../../backend/internal/service/upload.go) is not a general
append-after-completion API. [AudioPlayer](../../../frontend/src/components/AudioPlayer.tsx)
provides part playback; it does not establish every proposed absolute-seek behavior.

[Summarization](../../../backend/cmd/summarize/main.go) tracks completed part identities
and skips predecessor context when the target has non-owner collaborators or account
publication. [ADR-031](../../../docs/decisions/ADR-031-summarize-timeout-resilience.md)
records later retry/timeout behavior; old fixed or part-multiplied deadlines are
not current acceptance criteria.

Use the [current documentation map](../../../docs/README.md),
[API](../../../docs/API-SPEC.md), and [architecture](../../../docs/architecture.md)
for live contracts. Validation must use source/tests, not these historical stories.
