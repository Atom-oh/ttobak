# ADR-009: Batch Whisper on ECS GPU Spot Capacity

- Status: Accepted; diarization and dependency selection superseded in part by
  [ADR-019](ADR-019-acoustic-speaker-diarization-pyannote.md) and
  [ADR-035](ADR-035-diarization-pyannote4-community1-asr-pins.md).
- Decision date: Not recorded; motivating benchmark dated 2026-04-23.
- Implementation checked: 2026-09-13.

## Context and decision

The original Korean/English technical-meeting benchmark rated Transcribe 3.5/10
and Whisper large-v3 7.5/10. CPU Whisper was too slow; GPU inference made
asynchronous final transcription practical. Those measurements and the original
per-meeting prices were historical observations, not accuracy or cost guarantees.

Choose ECS on EC2 GPU Spot capacity for batch Whisper, with browser Transcribe
Streaming retained for live captions. This avoids a request-serving load balancer
and lets GPU capacity scale down between jobs. Managed Transcribe remains a
fallback; the legacy `nova-sonic` path still invokes Transcribe in current code.

## Current implementation

- Upload events launch `ttobak-transcribe`, which defaults to `whisper` and calls
  ECS. Missing cluster/task configuration falls back to Transcribe; an arbitrary
  running-task failure is not an automatic provider failover.
- Production runs `transcribe.py`: faster-whisper large-v3, CUDA float16,
  `language="ko"`, vocabulary prompting, then pyannote community-1 diarization.
  Participants supply a `max_speakers` upper bound, not an exact count.
- Model weights are streamed/extracted from S3 at runtime. The original
  "weights baked into ECR" description is obsolete.
- The imported VPC's private-egress subnets supply available AZs. The g5.xlarge
  Spot ASG has minimum/desired capacity zero and maximum ten, encrypted 200 GiB
  gp3 root volumes, and three-minute stopped-task cleanup. Zero minimum capacity
  does not eliminate scale-in delay, storage, registry, or shared networking cost.
- Transcript objects trigger summarization; multiple parts follow ADR-014.
  Diarization failure can retain unlabeled ASR with diagnostics, followed by
  inference-mode refinement. Acoustic preserve mode and part speaker namespaces
  remain required when labels exist.

## Engine, pin, and deployment constraints

Production ASR pins are `faster-whisper==1.2.1`, `ctranslate2==4.8.1`,
`onnxruntime==1.29.0`, `av==18.1.0`, `tokenizers==0.23.1`, and
`huggingface-hub==1.29.0`. Diarization uses `pyannote.audio==4.0.7`,
torch/torchaudio 2.8.0 cu128, and `torchcodec==0.7.0`.
`verify_pins.py` and image tests enforce the pairing. Preserve ADR-035's
benchmarked security-update exception; a generic dependency refresh is not
permission to move these pins.

The production image owns the default `DIARIZATION_S3_KEY`; CDK must not set it.
`deploy-whisper.yml` rebuilds production separately and strips a stale key when
cloning the task definition. Rollback must restore the coordinated image,
verifier, and infrastructure state described by ADR-035.

The separate WhisperX benchmark task/image is not the production cutover.
Its `run_engine.py` dispatcher selects `whisperx` or `fw_p4` via `ENGINE`.
**Never set `entryPoint` or `command` on that task definition**: bypassing its
allowlisting entrypoint is a critical security violation. It shares GPU capacity,
so benchmark jobs can also contend with production.

## Consequences and accepted risks

GPU batch inference improves the evaluated workload while retaining live captions.
Costs include cold starts, large runtime dependencies, model staging, GPU AMI
maintenance, and Spot capacity/interruption failures. User retry and existing
pipeline recovery are not a guarantee of automatic task recovery. Deploy only
named changed stacks with `--exclusively`; never deploy the staged Knowledge
stack teardown through dependencies.

## Evidence

- [whisper-stack.ts](../../infra/lib/whisper-stack.ts),
  [whisper-stack.test.ts](../../infra/test/whisper-stack.test.ts).
- [Dockerfile](../../backend/whisper/Dockerfile),
  [transcribe.py](../../backend/whisper/transcribe.py),
  [verify_pins.py](../../backend/whisper/verify_pins.py).
- [run_engine.py](../../backend/whisper/run_engine.py),
  [test_dockerfile_entrypoint.py](../../backend/whisper/test_dockerfile_entrypoint.py).
- [deploy-whisper.yml](../../.github/workflows/deploy-whisper.yml),
  [main.go](../../backend/cmd/transcribe/main.go).
