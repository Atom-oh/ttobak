# ADR-013: Summary Links to Source Transcript Segments

- Status: Accepted, including the source-fidelity addendum dated 2026-09-11.
- Decision date: Original decision date not recorded.
- Implementation checked: 2026-09-13.

## Context and decision

Summary readers need to inspect the conversation supporting a point without
searching the entire transcript. Choose a hybrid: the model emits approximate
seconds as `[TS:NNN]`, and a deterministic postprocessor maps them to real segment
IDs in `transcript://{segmentId}` links.

Pure model-generated anchors could invent targets. A separate similarity-matching
pass added latency and ambiguous matching. Time hints reuse timestamped input and
the application's established custom-link pattern without a new entity schema.

## Current implementation

`SummarizeTranscript` selects the effective transcript, A or B, and verifies
candidate segments against that source. The same verified segments feed the prompt
and `resolveTranscriptAnchors`. The resolver chooses the closest segment start;
it strips markers without segments and uses plain timestamps when IDs are absent.
The original function names and arbitrary time-range sketch were illustrative.

The frontend resolves links to `#ts-{segmentId}`, scrolls the transcript, and
briefly highlights the target. Its sanitizer and URL transform explicitly support
this scheme. A resolvable anchor proves that a segment exists, not that the
model's summary is semantically entailed by that segment.

## Source-fidelity requirements

- Editing A must not discard shared candidates that may describe B. Each consumer
  rejects candidates that do not match its own effective source.
- B-only legacy meetings retain matching segments. Legacy Transcribe punctuation
  is reconstructed from the selected source; grouped comparison tolerates
  repeated same-speaker headers without dropping body text, labels, IDs, or times.
- Saved notes are separately encoded untrusted context. The prompt must distinguish
  note-only corrections/uncertainty from recorded speech and must not assign
  transcript anchors to note-only claims. This is not a claim of a separate
  semantic validator for every generated assertion.
- Final summary responses must be complete and nonblank before completion is saved.
  This strict parser contract does not silently extend to auxiliary image analysis
  or refinement paths.

## Consequences and risks

Readers can verify summary context without extra matching-model calls. Prompt and
postprocessing maintenance increase; nearest-time matching remains approximate,
and poor transcript segmentation limits precision. User verification remains
necessary. Linked predecessor context has separate confidentiality rules in ADR-014.

## Evidence

- [bedrock.go](../../backend/internal/service/bedrock.go): effective-source checks,
  `SummarizeTranscript`, `resolveTranscriptAnchors`, final-response parsing.
- [ai_note_source_test.go](../../backend/internal/service/ai_note_source_test.go),
  [transcript_source_policy_test.go](../../backend/internal/service/transcript_source_policy_test.go):
  source selection, notes, and response regression coverage.
- [MarkdownRenderer.tsx](../../frontend/src/components/markdown/MarkdownRenderer.tsx),
  [MeetingDetailClient.tsx](../../frontend/src/app/meeting/[id]/MeetingDetailClient.tsx): navigation.
