# WhisperX Diarization Benchmark Runbook

Phase 1 benchmark procedure for comparing the legacy Whisper+pyannote 3.1 pipeline
(task def `ttobak-whisper`, container `whisper`) against the WhisperX+pyannote 4.x
pipeline (task def `ttobak-whisperx`, container `whisperx`) on real, already-`done`
meetings. This is a manual, operator-driven benchmark — it produces no product
change and writes no output into real meeting's S3 data (all bench transcripts go to
`bench-transcripts/` prefix). Account 180294183052, region ap-northeast-2 throughout.

Results feed the Phase 2 go/no-go decision and its ADR (see
`docs/decisions/ADR-006` sibling ADRs for the format).

## 1. One-time setup

### Build & push the WhisperX image

The image bundles CUDA/PyTorch native deps built for x86_64. Build it on an
x86_64 host or CI runner (`ttobak-x86`) — this repo's dev host is ARM and
`--platform linux/amd64` alone will not get you a runnable image locally, only
a cross-built one to push.

```bash
aws ecr get-login-password --region ap-northeast-2 \
  | docker login --username AWS --password-stdin 180294183052.dkr.ecr.ap-northeast-2.amazonaws.com

cd backend/whisper
docker build --platform linux/amd64 -f Dockerfile.whisperx \
  -t 180294183052.dkr.ecr.ap-northeast-2.amazonaws.com/ttobak-whisperx:latest .
docker push 180294183052.dkr.ecr.ap-northeast-2.amazonaws.com/ttobak-whisperx:latest
```

### Stage the diarization model bundle

The pyannote 4.x pipeline repo is `pyannote/speaker-diarization-community-1`
(gated, CC-BY-4.0, self-contained `config.yaml` — no separate sub-model repos
to accept, unlike the 3.1-era bundle). Before running the staging script,
accept the gated-model terms for that repo on huggingface.co with the account
whose token you're about to export, then:

```bash
HF_TOKEN=... ./upload-whisperx-diarization-model.sh
```

This uploads `models/whisperx-diarization-4.x.tar.gz` to
`s3://ttobak-assets-180294183052/`. The script rewrites `config.yaml`'s
model-path references to local paths so the container needs no runtime
HuggingFace access — but that rewrite is only exercised at model *load* time,
not at staging time. Treat the first benchmark run below as validation of
that rewrite: if the WhisperX container logs "Diarization model
unavailable/failed" (or an equivalent pyannote load error), pull the staged
`config.yaml` back down and inspect its rewritten paths first, before
suspecting the WhisperX code itself.

### Deploy the stack

```bash
cd infra && npx cdk deploy TtobakWhisperStack --exclusively
```

Never `cdk deploy --all` (see root `CLAUDE.md` Known Issues) — always a single
changed stack with `--exclusively`.

## 2. Selecting meetings

Pick 3–5 meetings already in `done` status, spanning a range of speaker counts
and durations (e.g. a short 2-speaker call, a longer 3-4 speaker meeting, one
with cross-talk if you have one). For each, note:

- `userId`
- `meetingId`
- audio S3 key, from the meeting's `AudioKeys` (DynamoDB `ttobak-main`,
  `PK=USER#<userId>`, `SK=MEETING#<meetingId>`)

```bash
aws dynamodb get-item --table-name ttobak-main \
  --key '{"PK":{"S":"USER#<USER_ID>"},"SK":{"S":"MEETING#<MEETING_ID>"}}' \
  --region ap-northeast-2 \
  --query 'Item.AudioKeys'
```

## 3. Running a benchmark pair

For each selected meeting, run **both** task defs against the same audio,
writing output to bench-only keys — never to the real
`transcripts/{meetingId}.json` that the live pipeline reads.

**Output prefix: use `bench-transcripts/`, not `transcripts/`.** Verified by
grepping the transcript-upload EventBridge rule:

```
infra/lib/gateway-stack.ts:492:            key: [{ prefix: 'transcripts/' }],
```

That rule (`ttobak-transcript-upload`, gateway-stack.ts:481-497) matches any
object key with prefix `transcripts/` and fires the summarize Lambda. A key
like `transcripts/{meetingId}_bench_whisperx.json` still matches that prefix,
so it WOULD trigger summarize — which would then fail its own meeting lookup
(the `_bench_*` suffix doesn't parse as a real meeting id) and log a noisy but
harmless error, plus burn an unnecessary Bedrock/Lambda invocation. Writing to
`bench-transcripts/` instead avoids this rule entirely — no EventBridge rule
in `gateway-stack.ts` matches that prefix, so a benchmark write triggers
nothing downstream.

No CloudFront/OAC change is needed for this prefix. Benchmark artifacts are
only ever read by the operator via `aws s3 cp` in step 5 below, never served
through the app — and `storage-stack.ts`'s OAC allowlist is already scoped to
`audio/images/files/docs/docs-pdf` only (storage-stack.ts:179-183, which
explicitly calls out that `transcripts/*` is "internal STT-pipeline data...
never handed out as a download URL"). `bench-transcripts/` doesn't need adding
to that allowlist for the same reason `transcripts/` isn't in it.

```bash
MEETING_ID=<meetingId>
USER_ID=<userId>
AUDIO_KEY=<audio S3 key from step 2>

for TD in ttobak-whisper ttobak-whisperx; do
  SUFFIX=$([ "$TD" = ttobak-whisper ] && echo legacy || echo whisperx)
  CONTAINER=$([ "$TD" = ttobak-whisper ] && echo whisper || echo whisperx)
  aws ecs run-task --cluster ttobak-whisper --task-definition "$TD" --count 1 \
    --capacity-provider-strategy capacityProvider=ttobak-whisper-spot,weight=1 \
    --overrides "{\"containerOverrides\":[{\"name\":\"$CONTAINER\",\"environment\":[
      {\"name\":\"MEETING_ID\",\"value\":\"$MEETING_ID\"},
      {\"name\":\"USER_ID\",\"value\":\"$USER_ID\"},
      {\"name\":\"AUDIO_KEY\",\"value\":\"$AUDIO_KEY\"},
      {\"name\":\"OUTPUT_KEY\",\"value\":\"bench-transcripts/${MEETING_ID}_bench_${SUFFIX}.json\"}]}]}" \
    --region ap-northeast-2
done
```

**⚠️ WARNING: Failed benchmark runs may corrupt real meeting data.**

If a benchmark task fails (unstaged model bundle, VRAM OOM, dependency crash, etc.),
the task's fatal-error handler calls `whisper_common.mark_meeting_error`, which
writes `status=error` to the **real meeting's DynamoDB row** — even though the
`OUTPUT_KEY` was bench-scoped. This is visible to users in the UI as a corrupted
done meeting.

**Recovery**: After any failed bench run, verify the meeting's status:

```bash
aws dynamodb get-item --table-name ttobak-main \
  --key '{"PK":{"S":"USER#<userId>"},"SK":{"S":"MEETING#<meetingId>"}}' \
  --region ap-northeast-2 \
  --query 'Item.status'
```

If it shows `error`, restore it to `done`:

```bash
aws dynamodb update-item --table-name ttobak-main \
  --key '{"PK":{"S":"USER#<userId>"},"SK":{"S":"MEETING#<meetingId>"}}' \
  --update-expression 'SET #s = :s' \
  --expression-attribute-names '{"#s":"status"}' \
  --expression-attribute-values '{":s":{"S":"done"}}' \
  --region ap-northeast-2
```

Successful benchmark runs never touch DynamoDB — only the fatal-error path does.

Repeat for each selected meeting.

## 4. Resource measurement per WhisperX run

This resolves the design doc's open VRAM/instance-sizing question.

Find the container instance running the task:

```bash
aws ecs list-tasks --cluster ttobak-whisper --region ap-northeast-2
aws ecs describe-tasks --cluster ttobak-whisper --tasks <TASK_ARN> --region ap-northeast-2 \
  --query 'tasks[0].containerInstanceArn'
aws ecs describe-container-instances --cluster ttobak-whisper \
  --container-instances <CONTAINER_INSTANCE_ARN> --region ap-northeast-2 \
  --query 'containerInstances[0].ec2InstanceId'
```

Sample GPU memory/utilization for the duration of the run (adjust the loop
count to the run's expected wall-clock):

```bash
aws ssm send-command --instance-ids <IID> --document-name AWS-RunShellScript \
  --parameters 'commands=["for i in $(seq 60); do nvidia-smi --query-gpu=memory.used,utilization.gpu --format=csv,noheader; sleep 5; done"]' \
  --region ap-northeast-2
```

Fetch the command output (via `aws ssm get-command-invocation`) and record the
peak `memory.used` value — this is the number that matters for Phase 2's
instance-sizing decision.

Container-level CPU/memory over the same window, via the same SSM channel:

```bash
aws ssm send-command --instance-ids <IID> --document-name AWS-RunShellScript \
  --parameters 'commands=["docker stats --no-stream"]' \
  --region ap-northeast-2
```

## 5. Comparing outputs

```bash
for S in legacy whisperx; do
  aws s3 cp "s3://ttobak-assets-180294183052/bench-transcripts/${MEETING_ID}_bench_${S}.json" "/tmp/${S}.json"
  echo "== $S: speakers =="
  jq '[.whisper_metadata.segments[].speaker] | unique' "/tmp/${S}.json"
  echo "== $S: turn timeline =="
  jq -r '.whisper_metadata.segments[] | "\(.start)\t\(.end)\t\(.speaker // "-")\t\(.text)"' "/tmp/${S}.json" | head -80
done
```

Judge qualitatively, per meeting:

- Detected speaker count (`legacy` vs `whisperx`) vs the meeting's known
  participant count.
- Turn-boundary placement on stretches you know involve rapid back-and-forth
  or cross-talk.
- Over-splitting (one real speaker fragmented into multiple labels) or
  under-splitting (two real speakers merged into one label).

## 6. Recording results

Build one table row per meeting:

| Meeting ID | Duration | Participants | Legacy speakers detected | WhisperX speakers detected | Peak VRAM (WhisperX) | Wall-clock (legacy / whisperx) | Qualitative verdict |
|---|---|---|---|---|---|---|---|

This table is the primary input to the Phase 2 go/no-go decision and the ADR
that records it — don't discard the raw per-meeting jq output until that ADR
is written, in case a reviewer wants to re-check a specific turn boundary.

## 7. Cleanup

Delete the bench S3 objects once the comparison table is recorded:

```bash
aws s3 rm "s3://ttobak-assets-180294183052/bench-transcripts/" --recursive
```

No ASG scale-down step is needed — `ttobak-whisper-spot` scales the cluster
back to 0 on its own once no tasks are running (same behavior as the
production Whisper pipeline's zero-scale cold start, see
`docs/runbooks/stt-pipeline-troubleshooting.md`).
