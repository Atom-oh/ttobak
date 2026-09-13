# ADR-019: Acoustic speaker diarization with pyannote

- Status: Accepted; model selection superseded by [ADR-035](ADR-035-diarization-pyannote4-community1-asr-pins.md).
- Original decision date: Not recorded. Model revision: 2026-09-03.
- Code checked: 2026-09-13. Repository configuration does not establish deployment state.

## Original decision and rationale

Add acoustic diarization after Whisper transcription on the same ECS GPU. Text-only speaker inference had merged distinct speakers with similar roles and topics. The original choice was the pyannote 3.1 bundle: simpler than NeMo and compatible with S3 model staging. Switching back to AWS Transcribe would sacrifice the mixed Korean/English transcription quality that motivated ADR-009; a headcount prompt alone could not distinguish voices.

## Current behavior

- The default worker remains `transcribe.py` with faster-whisper ASR. ADR-035 replaces only the diarization model/runtime selection with pyannote 4.x community-1 and pins the ASR dependencies. The separate WhisperX task is a benchmark path.
- Diarization follows transcription, using a mono 16 kHz waveform and an S3-staged model bundle. `len(meeting.Participants)` reaches `NUM_SPEAKERS` and pyannote's **`max_speakers` upper bound**, never an exact speaker count.
- `_assign_speakers` chooses the turn with maximum segment overlap, falling back to nearest midpoint. Labels become `spk_N` in first-appearance order. Multipart merging namespaces labels by part index; it does not identify the same person across files.
- With acoustic labels, `RefineTranscript` cleans text in preserve mode. It recomputes output labels structurally with `remapPreservedSpeakers`; model-returned speaker labels are not trusted. `hasCrossSpeakerMerge` rejects ambiguous cross-speaker output and uses the raw chunk fallback.
- Missing bundles, conversion errors, or diarization failures leave unlabeled segments and log the failure; text-based speaker inference remains the fallback. The metadata's `enabled` flag indicates bundle availability, not proof of successful diarization.
- The owner-only rediarization operation supports eligible single-part Whisper meetings, clears obsolete speaker mappings, and retriggers processing. It is not a cross-file identity repair.

## Tradeoffs and invariants

The additional model increases image dependencies, GPU time, and operator staging work. Overlapping speech and text-only fallback remain accuracy risks; acoustic labels do not guarantee correct real-world identities. Speaker maps remain user-editable.

Keep the WhisperX image dispatcher pinned to `run_engine.py`; do not add ECS `entryPoint` or `command` overrides. That security invariant and its image/CDK tests are detailed in ADR-035.

## Evidence

- [Transcription and diarization](../../backend/whisper/transcribe.py): `_safe_diarize`, `_diarize`, `_assign_speakers`; [tests](../../backend/whisper/test_transcribe.py).
- [Refinement](../../backend/internal/service/bedrock.go): `refineChunk`, `hasCrossSpeakerMerge`, `remapPreservedSpeakers`; [tests](../../backend/internal/service/bedrock_test.go).
- [Participant hint](../../backend/cmd/transcribe/main.go), [multipart merge](../../backend/cmd/summarize/merge.go), [rediarization eligibility](../../backend/internal/service/upload.go).
