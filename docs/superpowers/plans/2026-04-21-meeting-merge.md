# Historical implementation record: Proposed destructive meeting merge

- Original plan date: 2026-04-21.
- Historical proposal, not an execution checklist or current review mandate. Source comparison: 2026-09-13.

## Proposed scope and rationale

Combine split recordings or multi-device captures into one meeting with independent audio-source tabs and a unified summary. The proposal introduced nested `AudioSource` records, `MergedFrom`, per-source A/B transcript selection and speaker maps, and lazy conversion of legacy flat fields on reads.

A proposed owner-only merge endpoint would require completed source/target meetings, collect audio sources, move attachment records, delete source meetings, set the target to summarizing, and regenerate notes. Frontend work included a selection modal and source-specific audio/transcript controls. Multi-source prompting was meant to preserve separate source context while synthesizing one account of the discussion.

## Risks and intended validation

Source deletion and attachment rekeying introduced partial-failure and recovery concerns. The draft's post-response Lambda goroutine was not a durable asynchronous job mechanism. Its copied whole-item updates and error-string matching are not current implementation patterns to restore.

Intended validation covered ownership/status/self-merge rejection, transcript hydration, source-specific speaker/audio selection, Go tests/builds, frontend lint/build, and end-to-end re-summarization. All task boxes were unchecked; expected results did not establish implementation.

## Current disposition

The proposed `MergeService`, merge handler/route, and `AudioSources` model are absent from the reviewed source. Current `AudioKeys` multipart processing and `LinkedMeetingIDs` predecessor context are different features, not evidence this destructive merge shipped.

- [ADR-014](../../decisions/ADR-014-multi-file-audio-and-linked-meetings.md), [meeting model](../../../backend/internal/model/meeting.go), [API routes](../../../backend/cmd/api/main.go), [multipart transcript merge](../../../backend/cmd/summarize/merge.go).
- [ADR-019](../../decisions/ADR-019-acoustic-speaker-diarization-pyannote.md) governs speaker preservation; [ADR-031](../../decisions/ADR-031-summarize-timeout-resilience.md) governs summary retries. Neither authorizes deleting source meetings under this old proposal.
