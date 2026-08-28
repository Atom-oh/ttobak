# WhisperX diarization benchmark — design spec

## Context

AWS announced Deep Learning Containers for WhisperX (3.8.6: faster-whisper 1.2.1, CTranslate2 4.8.0, **pyannote.audio 4.0.7**, PyTorch 2.8/CUDA 12.8, EC2+SageMaker images, AL2023/Python 3.12). The current Whisper batch pipeline (`backend/whisper/transcribe.py`, ECS GPU Spot g5.xlarge) uses `faster-whisper` directly plus `pyannote.audio>=3.1,<4` for speaker diarization (ADR-019).

The primary motivation for evaluating WhisperX is **speaker diarization quality**, not transcription speed. WhisperX's VAD-batched inference and word-level forced alignment are not currently consumed anywhere downstream (the Go pipeline only reads segment-level `{start, end, text, speaker}`), so this spec scopes strictly to diarization.

Key constraint discovered during exploration: `internal/service/bedrock.go` (the only consumer of Whisper output) is fully decoupled from the diarization engine's internals. It only requires:
- `whisper_metadata.segments[]` — each `{start, end, text, speaker}`, `speaker` normalized to `spk_N` in first-appearance order (or omitted if diarization unavailable)
- `whisper_metadata.{engine, language, language_probability, duration_seconds}`
- `results.transcripts[0].transcript`

As long as a new engine emits this same shape, **no Go/API changes are required** to evaluate it. `remapPreservedSpeakers`/`hasCrossSpeakerMerge` (ADR-019's structural-not-prompt-trusted safeguards) already treat `speaker` as an opaque string and re-derive segment-level identity by time overlap — they don't care which model produced the label.

## Decisions from stakeholder discussion

1. **Goal**: diarization quality improvement only, not speed.
2. **Deployment path**: stay on ECS GPU Spot (no SageMaker migration). Adopt `whisperx` as a pip package in a **new**, separate container image — not the SageMaker-oriented DLC image itself, which assumes an invocation model (SageMaker endpoint) this project doesn't use.
3. **Rollout strategy**: build an isolated second engine + benchmark it against real meeting audio *before* touching any production code path. Only after the comparison looks favorable does a follow-up (separate spec/PR) wire engine selection into `startWhisperTask`.
4. **This spec's scope is Phase 1 only**: the new image, task definition, model staging, and a manual benchmark procedure. **Zero Go/API changes.** Cutover (Phase 2) is explicitly out of scope and will get its own design.

## Architecture

### New files (all under `backend/whisper/`, existing files untouched)

- **`whisper_common.py`** — extracted from `transcribe.py`: `_audio_key_exists`, `_find_audio_key`, `_load_custom_vocab_prompt`, S3 output upload, the top-level error→DynamoDB-status-update wrapper, and a shared `normalize_speakers(segments, raw_label_per_segment_fn)` helper implementing the same first-appearance `spk_N` normalization `_assign_speakers` already uses. In Phase 1 only `transcribe_whisperx.py` imports this (the production `transcribe.py` keeps its private copies so the legacy image stays byte-identical); migrating `transcribe.py` onto the shared module is part of the Phase 2 cutover. The helper implementations are copied verbatim from `transcribe.py` so the two stay behaviorally identical until then.
- **`transcribe_whisperx.py`** — new engine entry point. Same env var contract as `transcribe.py` (`MEETING_ID`, `USER_ID`, `AUDIO_KEY`, `OUTPUT_KEY`, `NUM_SPEAKERS`, `INITIAL_PROMPT`, `BUCKET_NAME`, `TABLE_NAME`) so it's a drop-in target for `ecs RunTask` with only the task-definition ARN changed.
- **`Dockerfile.whisperx`** — new image, independent of the existing `Dockerfile`. Free to pick whatever CUDA/torch version `whisperx` needs (no shared venv/layer with the existing image — this is what avoids the pyannote 3.x/4.x version conflict entirely).
- **`upload-whisperx-diarization-model.sh`** — one-time operator script, same pattern as `upload-diarization-model.sh` (HF gated-repo download → rewrite `config.yaml` to local paths → tar → S3 upload). **The exact HuggingFace repo ID(s) for the pyannote 4.x diarization pipeline are unconfirmed and must be looked up at implementation time** — do not guess this in the script.

### Pipeline inside `transcribe_whisperx.py`

1. `whisperx.load_model(...)` + `transcribe()` — VAD-batched inference, replaces `faster_whisper.WhisperModel.transcribe()`.
2. Forced alignment (`whisperx.load_align_model` + `align()`) is **attempted but not required**: Korean wav2vec2 alignment model availability in whisperx's registry is unconfirmed. Wrap in try/except — on failure, log and continue with segment-level timestamps only (matches the existing script's segment granularity; nothing downstream reads word-level timestamps today, so this is a safe degradation, not a partial failure).
3. As shipped: `pyannote.audio.Pipeline.from_pretrained()` used directly (not `whisperx.diarize.DiarizationPipeline`) + `whisper_common.assign_speakers` for the word/segment-to-speaker mapping (not `assign_word_speakers()`). The original intent above was to route diarization through whisperx's own wrapper; the shipped code calls pyannote 4.x directly and reuses the shared assignment helper `whisper_common` already provides, so both engines assign speakers the same way.
4. Segment-level speaker: majority vote over the segment's assigned word speakers (or, if alignment was skipped, treat the whole segment as one unit assigned by the diarization pipeline's own turn/segment overlap — same time-overlap logic `_assign_speakers` uses today). Normalize via `whisper_common.normalize_speakers` — **same `spk_N` first-appearance convention**, so output is structurally identical to the existing engine's.
5. `whisper_metadata.engine = "whisperx-large-v3-gpu"` — distinguishes benchmark output from the legacy engine's `"whisper-large-v3-gpu"` when comparing JSON side-by-side.

### Infrastructure (`infra/lib/whisper-stack.ts`)

Purely additive — no existing resource is modified. [As shipped: review added one modification to an existing resource — a scoped lifecycle rule (`bench-transcripts/` prefix, 30-day current-version expiration + 30-day noncurrent-version expiration) on the existing assets bucket in `TtobakStorageStack`, added for PII-TTL reasons. This is the one exception to "purely additive"; everything else below remains net-new.]
- New ECR repository (`WhisperXRepo`), separate from the existing `ecrRepository`.
- New ECS Task Definition (`WhisperXTaskDefinition`) reusing the **same** cluster, ASG, capacity provider (`ttobak-whisper-spot`), and IAM roles as the existing task definition — deliberately not a separate GPU instance pool, to avoid doubling idle-capacity cost during evaluation.
- New env vars on the new task def: `MODEL_S3_KEY` is reused as-is from the existing task def (faster-whisper CT2 weights are engine-compatible, confirmed at implementation time — no separate `WHISPERX_MODEL_S3_KEY`), plus a new S3 prefix for the pyannote 4.x diarization bundle, e.g. `models/whisperx-diarization-4.x.tar.gz`.

### Benchmark procedure (manual, no product code touched)

1. Pick N real meetings spanning different speaker counts/durations.
2. For each, `aws ecs run-task` against **both** task definitions with the same source audio, routing output to distinct S3 keys via `OUTPUT_KEY` override (e.g. `bench-transcripts/{meetingId}_legacy.json` / `bench-transcripts/{meetingId}_whisperx.json`) — never overwrites the meeting's real transcript. (As shipped, the runbook's exact naming is `bench-transcripts/{meetingId}_bench_{legacy|whisperx}.json`; either shape lives under the dedicated `bench-transcripts/` prefix, never `transcripts/`.)
3. Compare `whisper_metadata.segments` between the two outputs: speaker count, turn-boundary placement, qualitative correctness on known multi-speaker stretches.
4. **Scope is qualitative diarization comparison, not WER.** No reference transcripts exist to compute WER against, and building that harness is out of scope for this spec — if a numeric quality bar becomes necessary later, that's a separate follow-up.
5. Capture peak GPU VRAM (`nvidia-smi --query-gpu=memory.used --format=csv -l 5` or similar, sampled during the WhisperX run) and container CPU/memory (ECS/CloudWatch container insights) for each WhisperX benchmark run — resolves the VRAM/instance-sizing unknown above with real numbers instead of estimates before any Phase 2 cutover decision.

## Explicitly out of scope (Phase 2, future spec)

- Wiring an engine selector into `cmd/transcribe`'s `startWhisperTask` (env-var default + optional per-meeting override).
- `RediarizeMeeting` API changes to let a user/admin pick engine.
- Removing/retiring the legacy `pyannote<4` engine.
- SageMaker migration.
- Word-level timestamp consumption anywhere downstream.

## Risks / open unknowns (carried forward, not resolved here)

- Exact HF repo ID(s) for pyannote 4.x diarization — confirm before writing `upload-whisperx-diarization-model.sh`.
- Korean wav2vec2 alignment model availability — degrades gracefully (see above) but unconfirmed either way.
- Whether `whisperx`'s pip dependency set (torch 2.8/CUDA 12.8 per the DLC's known-good combo) works cleanly on the existing ECS GPU AMI (`ecs.EcsOptimizedImage.amazonLinux2(GPU)`) driver version — check at Dockerfile build/deploy time, not assumed.
- **VRAM headroom on g5.xlarge (single A10G, 24GB)**: model footprint itself is roughly unchanged (large-v3 CT2 weights reused, pyannote 4.x diarization models comparable in size to the current 3.x bundle), but WhisperX's batched transcription (default `batch_size=16`, multiple audio chunks processed on-GPU concurrently instead of faster-whisper's sequential one-chunk-at-a-time) can push peak VRAM meaningfully higher than today's usage. Should still fit in 24GB with `batch_size` tuned down if needed — but this needs measuring (peak `nvidia-smi` VRAM during a real transcription run), not assuming, before deciding whether the instance type itself needs to change. CPU/RAM (4 vCPU/16GB) headroom for the added VAD/alignment work is a smaller, same-category unknown. Disk is already covered by the unrelated 200GB root-volume bump (`9615ad1`). Add VRAM/CPU/RAM peak measurement to the benchmark procedure's per-run checklist.

## Testing / verification

No unit-testable logic changes on the Go side (out of scope). On the Python side: `whisper_common.normalize_speakers` should get the same kind of pure-function unit test `_assign_speakers` already has in `test_transcribe.py` (importable without pyannote/whisperx heavy deps, per that file's existing convention). Container-level verification is the benchmark procedure itself — there's no automated pass/fail gate for diarization quality in this phase.
