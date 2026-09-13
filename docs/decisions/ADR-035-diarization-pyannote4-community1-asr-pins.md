# ADR-035: Community-1 diarization with pinned ASR dependencies

- Status: Accepted; supersedes only [ADR-019](ADR-019-acoustic-speaker-diarization-pyannote.md)'s pyannote 3.1 model/runtime choice.
- Decision date: 2026-09-03; ASR reference image dated 2026-08-28.
- Code checked: 2026-09-13. Current deployed images and S3 bundles were not inspected.

## Original decision and rationale

The original benchmark compared four meetings, including two with operator-confirmed speaker counts. Community-1 improved phantom-speaker behavior with the existing faster-whisper ASR path; the WhisperX benchmark stack also omitted a roughly five-minute speech interval in another meeting. Retain the ASR engine and change diarization rather than cut over to WhisperX or add Qwen3-ASR.

The record identified ctranslate2 and torch/CUDA differences as candidates, not proof that one package alone caused the omission. Accuracy took priority over speed; those historical observations are not a universal accuracy guarantee.

## Current package and model contract

| Component | Exact default-image pin |
|---|---|
| faster-whisper | 1.2.1 |
| ctranslate2 | 4.8.1 |
| onnxruntime | 1.29.0 |
| av | 18.1.0 |
| tokenizers | 0.23.1 |
| huggingface-hub | 1.29.0 |
| pyannote.audio | 4.0.7 |
| torch / torchaudio | 2.8.0+cu128 |
| torchcodec | 0.7.0 |

The default `transcribe.py` image owns `DIARIZATION_S3_KEY`, defaulting to `models/whisperx-diarization-4.x.tar.gz`. The filename reflects benchmark staging; the bundle is a self-contained community-1 pipeline. CDK does not set this variable for the default task, and `deploy-whisper.yml` strips inherited copies when cloning task definitions.

`verify_pins.py` and `pip check` run during image build. Tests compare Dockerfile pins with the verifier. These freeze named packages, not every dependency or base-image digest. Torch was raised for pyannote compatibility; faster-whisper's inference/VAD/decode package pins remain unchanged. Waveform preloading bypasses pyannote's torchcodec decoding path, but full audio regression testing is still required for dependency changes.

## Preserved behavior and security invariants

- ADR-019's post-transcription GPU diarization, `max_speakers` semantics, structural speaker preservation, multipart namespacing, and logged unlabeled fallback remain authoritative.
- `_bundle_pyannote_mismatch` checks recognized bundle-generation names against installed major versions and logs/skips mismatches. It is not content verification of an arbitrary bundle key. Missing bundles can still disable diarization.
- The separate WhisperX benchmark image has `ENTRYPOINT ["python3", "run_engine.py"]`; `ENGINE` selects only `whisperx` or `fw_p4`, and positional command arguments are rejected. **Neither ECS task definition sets `entryPoint` or `command`; introducing either on WhisperX violates the dispatcher security boundary.** Image and CDK tests cover both sides. Its host networking and cross-user audio-read capability make bypass consequential despite its separate benchmark role.

## Operations and tradeoffs

The original rollback decision required reverting the complete model/package/verification change and coordinating image and stack delivery; changing only a pin or bundle key can fail build or disable diarization. Use a coherent reviewed rollback against today's tree rather than assuming a historical revert remains conflict-free. Legacy staging scripts remain in the repository; actual bundle availability must be checked before rollout.

Security fixes may change frozen pins after representative coverage/speaker benchmarks. Update Dockerfile and verifier together. Neither frozen dependencies nor a small historical benchmark justifies ignoring new vulnerabilities or claiming all audio regressions are excluded.

## Evidence

- [Default Dockerfile](../../backend/whisper/Dockerfile), [pin verifier](../../backend/whisper/verify_pins.py), [runtime model handling](../../backend/whisper/transcribe.py).
- [Dispatcher](../../backend/whisper/run_engine.py), [dispatcher tests](../../backend/whisper/test_run_engine.py), [image/pin tests](../../backend/whisper/test_dockerfile_entrypoint.py).
- [ECS definitions](../../infra/lib/whisper-stack.ts), [CDK guard tests](../../infra/test/whisper-stack.test.ts), [image deployment](../../.github/workflows/deploy-whisper.yml), [benchmark record](../runbooks/whisperx-benchmark.md).
