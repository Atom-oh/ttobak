# Historical implementation record: WhisperX benchmark isolation

- Original plan date: 2026-08-28; Phase 1.
- Historical benchmark plan, not an execution checklist or current review mandate. Source pointers checked 2026-09-13.

## Scope and rationale

Compare WhisperX/pyannote 4.x against the then-default faster-whisper/pyannote 3.1 on real meetings without selecting a new production engine in Phase 1. Add a separate image, ECR repository, ECS task, staging script, helpers, tests, and manual runbook, reusing GPU capacity but avoiding automatic invocation.

The planned output retained the summarize JSON contract and distinguished audio duration from transcription wall time. Shared helpers supplied overlap/midpoint and word-majority speaker assignment, normalized labels, audio discovery, safe streaming tar extraction, and injected AWS operations. Alignment and diarization failures were best-effort fallbacks, not reasons to discard valid ASR output.

## Recorded review changes and current boundary

Inline amendments record three departures from the first template: a dedicated scoped benchmark task role replaced the reused broad role; benchmark/empty output keys could not mark a real meeting failed; and the deployment path filter stopped rebuilding the default image for unrelated benchmark files. These are recorded implementation amendments, not benchmark-quality measurements.

Current benchmark writes are scoped to `bench-transcripts/*`, avoiding summarize events. The old example under `transcripts/*` is superseded. `OUTPUT_KEY` validation rejects empty values, and production-status mutation is separately guarded. The image dispatcher accepts only `ENGINE=whisperx|fw_p4`; positional commands and ECS `entryPoint`/`command` overrides must not bypass it. The role still reads cross-user audio and the task uses host networking, so benchmark isolation is not a tenant sandbox.

## Validation, tradeoffs, and later decision

Intended tests covered output shape, speaker normalization/fallbacks, helper errors, CDK resource/IAM separation, and default-path regressions. Planned operator work included model-gating approval, an x86 image build, representative completed-meeting pairs, coverage/speaker comparison, peak GPU/memory/runtime sampling, and removal of benchmark artifacts. Reusing capacity could affect available GPU throughput; bundle schema/path compatibility required an actual load check.

The original checklist was unchecked and contained no completed benchmark table. [ADR-035](../../decisions/ADR-035-diarization-pyannote4-community1-asr-pins.md) records the later benchmark-based decision: keep faster-whisper ASR, adopt community-1 diarization, and pin default ASR dependencies. Its later default-image changes supersede this Phase 1 byte-identical constraint.

## Evidence pointers

[Benchmark runbook](../../runbooks/whisperx-benchmark.md), [WhisperX worker](../../../backend/whisper/transcribe_whisperx.py), [shared helpers](../../../backend/whisper/whisper_common.py), [dispatcher](../../../backend/whisper/run_engine.py), [image guard tests](../../../backend/whisper/test_dockerfile_entrypoint.py), [ECS definitions](../../../infra/lib/whisper-stack.ts), [CDK tests](../../../infra/test/whisper-stack.test.ts), [deployment filter](../../../.github/workflows/deploy-whisper.yml).
