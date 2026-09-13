# WhisperX Diarization Benchmark Design

> Historical design record. Original date: 2026-08-28 (filename). A post-design note
> dated 2026-09-02 added ASR recall to the evaluation. This Phase 1 benchmark did not
> authorize a production engine cutover or establish measured hardware headroom.

## Goal and isolation decision

Evaluate speaker diarization quality on real meeting audio while retaining ECS GPU
Spot. Speed was secondary. Use a separate WhisperX image/task rather than a
SageMaker-oriented serving image or a production dependency update. The first
benchmark's reported speech omissions made ASR coverage a second explicit criterion;
qualitative diarization alone was insufficient.

The downstream contract remained segment-level start/end/text/speaker plus engine,
language, confidence, true audio duration, and a plain transcript. Speaker labels
use first-appearance `spk_N` names; existing overlap-based preservation and
cross-speaker-merge safeguards remain authoritative. Word-level consumption and
Go/API engine selection were excluded from Phase 1.

## Proposed and implemented benchmark components

The separate image used WhisperX VAD/batched ASR, attempted optional alignment, and
pyannote diarization. Alignment failure could fall back to segment timing. The
implemented benchmark calls pyannote directly and uses shared overlap assignment;
the earlier wrapper/word-vote sketch was not the final mapping algorithm.

`whisper_common.py` serves benchmark engines while production retains independent
helpers. A staging script builds an offline community-1 bundle. The second task
shares the existing GPU cluster/ASG/capacity provider, so isolation of code does
not isolate capacity contention. A later storage change added 30-day current and
noncurrent expiration for `bench-transcripts/`; the original purely-additive-resource
claim was therefore too broad.

## Evaluation and risks

Compare the same recordings under distinct `bench-transcripts/` output keys, never
production `transcripts/`. Review speaker count, turn boundaries, known speaker
stretches, and ASR coverage. No WER claim is justified without reference transcripts.
Measure peak GPU VRAM and container CPU/RAM; tune batch size before assuming the
existing instance fits. Driver/library compatibility, gated model access, missing
alignment support, model failures, and shared-capacity cost remain relevant.

## Current successor and evidence

[ADR-035](../../decisions/ADR-035-diarization-pyannote4-community1-asr-pins.md) chose
community-1 diarization with the production faster-whisper ASR, not a WhisperX
cutover. Production pins remain fixed with the documented benchmarked-security-update
process. Model identity is now specified by the staging script and image defaults;
it is no longer an unresolved repository-name guess.

[Dockerfile.whisperx](../../../backend/whisper/Dockerfile.whisperx),
[run_engine.py](../../../backend/whisper/run_engine.py),
[transcribe_whisperx.py](../../../backend/whisper/transcribe_whisperx.py), and
[whisper_common.py](../../../backend/whisper/whisper_common.py) define the benchmark.
Keep the dispatcher entrypoint: task-definition `entryPoint`/`command` overrides
are forbidden, with engine selection through `ENGINE` only. The
[WhisperStack tests](../../../infra/test/whisper-stack.test.ts) enforce that boundary.

Use the [benchmark runbook](../../runbooks/whisperx-benchmark.md) for current procedures
and the [documentation map](../../README.md) for current references. Historical
benchmark notes are not fresh quality or deployment verification.
