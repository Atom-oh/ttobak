# WhisperX benchmark operations

Compare engines using operator-authorized recordings and isolated benchmark output.
ADR-035 changed production to pyannote 4.x/community-1 on 2026-09-03 while preserving
production ASR pins. A new ttobak-whisper run is therefore not a pyannote 3.1 baseline,
even if an old script labels its suffix legacy. Record image digest and actual
engine; a task family or mutable latest tag is insufficient provenance.

## Boundaries

The dedicated ttobak-whisperx task role reads audio/models/config and writes only
bench-transcripts/. It has no DynamoDB grant. The same image dispatches ENGINE=whisperx
or ENGINE=fw_p4 through run_engine.py. Never override entryPoint/command in CDK or
pass command overrides to RunTask; the dispatcher allowlist is a security boundary.

Explicit OUTPUT_KEY is mandatory. Use
`bench-transcripts/{meetingId}_bench_{engine}_{attempt}.json`, with a distinct key
for each comparison. Never put benchmark output under transcripts/: that prefix
triggers production summarization. No OAC change is needed; benchmark objects are
operator-only data, not application media.

The production transcribe.py fatal handler can still set a real meeting to error
when used for a benchmark, despite a bench-only OUTPUT_KEY. This risk does not apply
to the dedicated benchmark role/engines. Prefer isolated benchmark engines. If a
production baseline is necessary, use an explicitly authorized disposable meeting
and record its initial state. Do not blindly restore done with an unconditional
write after failure; first rule out concurrent real work and use a reviewed recovery.

## Prepare and validate

Inspect StorageStack lifecycle and WhisperStack role/task definitions. For an
authorized fresh environment, deploy Storage then Whisper with --exclusively,
never --all or implicit dependencies. Do not redeploy unrelated stacks.

Build the benchmark image on x86_64 with its Dockerfile and validate before pushing:

```bash
docker build --platform linux/amd64 -f backend/whisper/Dockerfile.whisperx \
  -t ttobak-whisperx-check backend/whisper
docker run --rm --pull=never --entrypoint python3 ttobak-whisperx-check \
  -c "import torchcodec; print('torchcodec OK')"
```

The entrypoint override above is a local dependency smoke check with no AWS task
role, not an ECS/CDK configuration. It verifies the shared Python/FFmpeg dependency
failure seen in the first historical run. Push only the validated image through
the authorized ECR/workflow path; record its digest. Check host driver compatibility
and the installed whisperx/faster-whisper versions before drawing conclusions.

Stage the community-1 bundle with
`backend/whisper/upload-whisperx-diarization-model.sh` after accepting the model's
access conditions. Supply credentials without writing tokens into commands/history
or docs. The current bundle and ASR keys belong to image/task configuration; do not
change production DIARIZATION_S3_KEY in CDK to run a benchmark.

Run existing engine tests before executing cloud work:

```bash
(cd backend/whisper && python3 -m unittest test_transcribe test_whisper_common test_transcribe_whisperx test_transcribe_fw_p4 test_run_engine test_dockerfile_entrypoint -v)
(cd infra && npm test)
```

## Select and run an attempt

Use a known single-part meeting and its authorized audio key. Multipart audio needs
separate runs/part bookkeeping. Participant count is a max_speakers hint, not
reference truth about how many people actually spoke.

Create an overrides JSON file for one attempt, replacing the placeholders with
validated values. Keep configuration as JSON data rather than shell interpolation:

```json
{
  "containerOverrides": [{
    "name": "whisperx",
    "environment": [
      {"name": "ENGINE", "value": "whisperx"},
      {"name": "MEETING_ID", "value": "MEETING_ID"},
      {"name": "USER_ID", "value": "USER_ID"},
      {"name": "AUDIO_KEY", "value": "audio/USER_ID/MEETING_ID/recording.webm"},
      {"name": "OUTPUT_KEY", "value": "bench-transcripts/MEETING_ID_bench_whisperx_attempt1.json"}
    ]
  }]
}
```

With the authorized file saved as overrides.json:

```bash
aws ecs run-task --cluster ttobak-whisper --task-definition ttobak-whisperx \
  --count 1 --capacity-provider-strategy capacityProvider=ttobak-whisper-spot,weight=1 \
  --overrides file://overrides.json --region ap-northeast-2
```

Check failures in the RunTask response as well as returned task ARNs. Track each
attempt's task status, log stream, imageDigest, engine label and output key. Do not
count missing output or disabled diarization as a successful comparison.

For fw_p4, change ENGINE and give it a different output suffix. This hybrid uses
sequential faster-whisper ASR plus community-1 without WhisperX alignment; VAD
knobs specific to WhisperX are ignored. Verify the dispatcher log and
whisper_metadata.engine rather than assuming a new variable took effect in an old
image. It is a benchmark implementation, not proof of exact parity with production.

For WhisperX tuning, inspect the validated WHISPERX_VAD_METHOD/ONSET/OFFSET/CHUNK_SIZE
options in transcribe_whisperx.py. Record actual effective settings. Lower thresholds
may recover speech but also include noise/hallucination; inspect real missed content,
not just segment count or transcript length. The historical 2026-09-02 comparisons
motivated keeping production ASR while upgrading diarization (ADR-035).

## Measure and retain evidence

Use GPU[stage] lines in the task's own logs for VRAM measurements; no SSM or broader
production instance-role permission is needed. Compare identical audio by time
ranges, not segment indexes: alignment may legitimately re-split segments.

WhisperX preserves normalized rendered text across alignment; lossy/reordered output
falls back. alignment_repaired counts partially repaired segments, not fully aligned
success. Use n/a for engines without alignment. Speaker quality needs known turns,
over/under-splitting and cross-talk checks, not simply participant-count equality.

Retain one aggregate row per attempt:

| Meeting ID | Duration | Engine | Image digest | Effective VAD | Real speech gap seconds/count | Detected speakers | Peak VRAM | Wall time | Alignment repairs | Verdict |
|---|---|---|---|---|---|---|---|---|---|---|

The verdict must distinguish execution failure, missing diarization, ASR recall,
speaker quality and partial alignment. Preserve enough config to reproduce the
comparison without keeping raw meeting text in the repository.

## Cleanup

Download transcript material only into a unique temporary directory and remove
local copies after the session. Delete only this attempt's benchmark keys, not the
entire shared prefix. On a versioned bucket, deletion creates a marker; previous
versions survive until lifecycle expiry. Current and noncurrent 30-day expiry can
mean roughly 60 days before physical removal. Earlier erasure needs targeted
version deletion under explicit authorization.

Do not kill unrelated tasks or force the shared ASG to zero; its capacity provider
scales idle capacity down. Record cleanup and any failed attempt's product-state
impact. Current constraints are in [infrastructure](../INFRA-SPEC.md),
[ADR-035](../decisions/ADR-035-diarization-pyannote4-community1-asr-pins.md), and
[STT troubleshooting](stt-pipeline-troubleshooting.md).
