# ADR-009: Batch speech-to-text and speaker diarization

- Status: Accepted. Consolidates the earlier records on Whisper on GPU ECS
  with zero idle capacity, acoustic diarization, and the pyannote 4
  community-1 and ASR pins.
- Implementation checked: 2026-11-11.

## Decisions

### Worker and capacity

- Batch transcription runs faster-whisper on ECS EC2 GPU capacity. `cmd/transcribe`
  uses Whisper by default and uses Amazon Transcribe only when the cluster or
  task-definition configuration is missing; a failed task does not fail over.
- Production ASR forces Korean (`language="ko"`).
- The Auto Scaling group idles at zero. It mixes `g4dn.xlarge` and
  `g4dn.2xlarge`, with one On-Demand base instance and price-capacity-optimized
  Spot above it. Root volumes are 200 GiB encrypted gp3 and stopped-task cleanup
  is short, a deliberate response to disk exhaustion on reused hosts.
- The capacity provider uses ECS managed draining, so CDK's own drain hook is
  omitted. AZRebalance is suspended through a custom resource with a constant
  physical ID; deleting the resource resumes it.
  `ECS_ENABLE_SPOT_INSTANCE_DRAINING=true` stops new placement on an interrupted
  host. Tasks already running there still die; scheduled recovery (ADR-031)
  re-drives eligible single-file meetings.
- The production image entry point is `python3 transcribe.py` with
  `PYTHONUNBUFFERED=1`, so a killed worker's progress lines reach the log.
- The benchmark image dispatches through `ENTRYPOINT ["python3", "run_engine.py"]`
  and selects an engine from the `ENGINE` allowlist. Its task definition sets no
  `entryPoint` or `command`; adding either, or bypassing the dispatcher, is a
  critical defect. CDK and image tests enforce this.

### Diarization

- Speakers are separated acoustically with pyannote 4.x community-1. The pins in
  the Dockerfile must agree with `verify_pins.py`, which gates the image.
- The image, not CDK, owns the diarization bundle key, so an image and a stack
  deployment cannot race on it. The deploy workflow strips a stray override.
- Diarization is best effort: a missing bundle or a failure yields unlabeled
  segments, never a failed transcript. Logs carry exception types only.
- The participant count is an upper bound (`max_speakers`), not an exact count.
  `DiarizationSpeakerHint`, set by rediarization, overrides it when positive.
- Segments take the speaker with the greatest time overlap; labels are
  normalized to `spk_N` in order of first appearance and namespaced by audio
  part index when parts are merged.
- Acoustic labels are authoritative. The refinement model cleans text only:
  `remapPreservedSpeakers` recomputes labels by time overlap and
  `hasCrossSpeakerMerge` falls back when a merge crosses speakers.
- Rediarization is owner-only and limited to idle, single-part Whisper meetings.

## Not implemented or off by default

- WhisperX and `fw_p4` exist only as benchmark engines, not a production path.
- Language auto-detection for Korean and English code-switching is an open
  benchmark question; production stays Korean.

## Limits and accepted risks

- On-Demand base capacity costs the normal rate while active and does not
  guarantee capacity. Both instance sizes have one 16 GB T4.
- Reverting only the pins fails the image gate; reverting only the bundle key
  skips diarization loudly. Roll both back together.
- Template and image tests do not prove runtime compatibility, the deployed
  image, or that the bundle exists in S3.

## Source

`infra/lib/whisper-stack.ts`, `infra/test/whisper-stack.test.ts`,
`backend/whisper/` (`transcribe.py`, `verify_pins.py`, `run_engine.py`, both
Dockerfiles), `backend/cmd/transcribe/main.go`, `backend/cmd/summarize/merge.go`,
`backend/internal/service/bedrock.go`, `upload.go`,
`.github/workflows/deploy-whisper.yml`.
