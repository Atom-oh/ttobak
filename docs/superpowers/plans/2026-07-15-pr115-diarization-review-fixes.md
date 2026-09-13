# Historical review record: PR #115 initial diarization fixes

- Original plan date: 2026-07-15; subject: `feat/pyannote-speaker-diarization`.
- Historical review plan, not an execution checklist or current review mandate. Current model configuration is governed by ADR-035.

## Findings and proposed response

The review identified four required changes: ffmpeg conversion outside the best-effort block could discard successful Whisper text; `duration_seconds` incorrectly held transcription wall time; unfiltered tar extraction permitted path escape; and the infrastructure reference omitted the new bundle setting.

The proposed `_safe_diarize` helper enclosed conversion and diarization so failure returned no labels instead of aborting transcript upload. `duration_seconds` would use decoded audio length, including trailing silence, while `transcription_duration_seconds` preserved the pre-diarization timing metric. Correct duration mattered once per-part `OUTPUT_KEY` support activated multipart offset calculation.

Safe tar extraction used the data filter. The legacy model-staging script would write absolute container paths, avoiding uncertain config-relative resolution at the cost of coupling the script to the extraction directory.

## Risks and intended validation

Old generated transcripts could retain the former duration meaning; JSON alone did not reliably identify them. Model bundle/path compatibility and disk sizing needed runtime evidence. This round deferred exact-versus-maximum speaker semantics and capacity questions; later work addressed speaker semantics.

Planned checks included mocked ffmpeg failure, Python speaker tests, Go builds/tests, manual path inspection, and infrastructure-document consistency. No model upload or GPU smoke-test result was recorded, and all task boxes were unchecked. The quoted historical review verdict was not authorization to bypass future HEAD review.

## Current references and supersession

- [Transcription](../../../backend/whisper/transcribe.py), [Python tests](../../../backend/whisper/test_transcribe.py), [multipart offsets](../../../backend/cmd/summarize/merge.go), [legacy staging](../../../backend/whisper/upload-diarization-model.sh).
- [Round 2](2026-07-16-pr115-review-round2-fixes.md) adds maximum-speaker semantics and label preservation. [ADR-019](../../decisions/ADR-019-acoustic-speaker-diarization-pyannote.md) defines the behavior; [ADR-035](../../decisions/ADR-035-diarization-pyannote4-community1-asr-pins.md) supersedes the pyannote 3.1/CDK-owned bundle default with community-1 and image-owned configuration.
