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
- Model weights are streamed from S3 at run time, not baked into the image.
- The Auto Scaling group idles at zero (minimum 0, maximum 10). It mixes `g4dn.xlarge` and
  `g4dn.2xlarge`, with one On-Demand base instance and price-capacity-optimized
  Spot above it, capped at $1.10. Root volumes are 200 GiB encrypted gp3 and stopped-task cleanup
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

- Speakers are separated acoustically with pyannote 4.x community-1.
- Production ASR and diarization dependencies are pinned to exact versions in
  the Dockerfile, and `verify_pins.py` gates the image on the same list. A
  security update may move a pin only after representative coverage and speaker
  benchmarks, changing both files together; a general dependency refresh does
  not move them.
- The image, not CDK, owns the diarization bundle key, so an image and a stack
  deployment cannot race on it. The deploy workflow strips a stray override.
- Diarization is best effort: a missing bundle or a failure yields unlabeled
  segments, never a failed transcript. Diarization logs carry exception types
  only, except the WAV format error, whose message is safe by construction.
- `whisper_metadata.diarization.enabled` reports that a bundle was configured,
  not that diarization succeeded; check the detected speaker count and
  unlabeled segments.
- Without acoustic labels, refinement infers speaker turns from text.
- The participant count is an upper bound (`max_speakers`), not an exact count.
  `DiarizationSpeakerHint`, set by rediarization, overrides it when positive.
- Segments take the speaker with the greatest time overlap; labels are
  normalized to `spk_N` in order of first appearance and namespaced by audio
  part index when parts are merged.
- Acoustic labels are authoritative. The refinement model cleans text only:
  `remapPreservedSpeakers` recomputes labels by time overlap and
  `hasCrossSpeakerMerge` falls back when a merge crosses speakers.
- Rediarization is available to the creator or a managing account member, for
  idle, single-part Whisper meetings, with 2 to 20 speakers. It clears the
  speaker map, sets the hint and moves the meeting to `transcribing` in one
  conditional write before the audio copy, so the new run cannot read stale
  state.

## Not implemented or off by default

- WhisperX and `fw_p4` exist only as benchmark engines, not a production path.
- Language auto-detection for Korean and English code-switching is an open
  benchmark question; production stays Korean.

## Limits and accepted risks

- On-Demand base capacity costs the normal rate while active and does not
  guarantee capacity. Both instance sizes have a single GPU; the larger adds
  host memory and CPU only.
- Before a rollout, confirm that the diarization bundle exists in S3 and that
  the On-Demand G and VT instance quota is sufficient.
- The bundle pre-check compares the bundle's generation name with the installed
  pyannote major version. It does not verify content.
- Waveform preloading bypasses pyannote's own decoding path, and the pins
  freeze named packages, not transitive dependencies or the base image digest.
  Any dependency change needs a full audio regression.
- The benchmark task uses host networking and its role can read `audio/*`
  across users, which is why bypassing its dispatcher is consequential.
- Labels are not identities. Part merging does not recognize the same person
  across files, overlapping speech and the text-only fallback are accuracy
  risks, and speaker maps stay user-editable.
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
