# Historical review record: PR #115 second-round speaker fixes

- Original plan date: 2026-07-16.
- Historical review plan, not an execution checklist or current review mandate. Source pointers checked 2026-09-13.

## Findings and rationale

The review reported the prior required fixes applied, then identified three speaker defects and two documentation gaps. Registered participant count was being used as an exact cluster count; separately transcribed parts reused `spk_0`; and failed refinement collapsed a chunk's acoustic labels to one fallback speaker. Generated reviewer context and the architecture ADR index were also stale.

The plan changed the pyannote argument to `max_speakers`, retained per-segment acoustic labels in raw fallback, and namespaced numeric labels by part index. Numeric offsets preserved the frontend's `spk_`-plus-digits matching and sorting, unlike a new `spk_part_label` format. The million-label interval bounded each part's namespace; real speakers appearing in several parts still needed manual identity mapping.

## Validation and result record

Intended checks covered cross-part collisions, out-of-range labels, non-acoustic fallback, frontend label parsing, Python/Go suites, reviewer-context freshness, and ADR indexing. The original claim that the maximum-speaker argument could not be unit-tested is obsolete: current Python tests mock the pipeline. The introductory review confirmation concerned prior fixes, not an attached successful run of this whole plan; its task boxes were unchecked.

## Current references and supersession

- [Speaker helper](../../../backend/internal/speaker/label.go), [label tests](../../../backend/internal/speaker/label_test.go), [multipart merge](../../../backend/cmd/summarize/merge.go), [refinement](../../../backend/internal/service/bedrock.go), [Python tests](../../../backend/whisper/test_transcribe.py).
- [ADR-019](../../decisions/ADR-019-acoustic-speaker-diarization-pyannote.md) also requires structural label remapping and cross-speaker-merge fallback; [ADR-035](../../decisions/ADR-035-diarization-pyannote4-community1-asr-pins.md) supersedes model/dependency choices.
- [Current review-context generator](../../../scripts/docs/sync_review_context.py) replaces the archived plugin-path instructions. Historical dependency/checksum deferrals are not exemptions from current security review.
