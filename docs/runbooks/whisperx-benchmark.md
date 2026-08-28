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

The `bench-transcripts/` lifecycle rule this PR adds lives in
`TtobakStorageStack` (see `docs/INFRA-SPEC.md`), not `TtobakWhisperStack` — a
manual deploy also needs `cd infra && npx cdk deploy TtobakStorageStack --exclusively`.
`deploy-infra.yml` covers this automatically on merge, so this step is only
needed for a manual/out-of-band setup.

### Deploy the stack

Deploy this first: the `ttobak-whisperx` ECR repository is *created* by this
CDK deploy, so a fresh setup that tries to `docker push` before deploying
fails with `RepositoryNotFoundException`.

```bash
cd infra && npx cdk deploy TtobakWhisperStack --exclusively
```

Never `cdk deploy --all` (see root `CLAUDE.md` Known Issues) — always a single
changed stack with `--exclusively`.

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
# Recommended (shell-history-safe -- avoids the token landing in
# ~/.bash_history or .zsh_history via the inline VAR=... assignment form):
read -rs HF_TOKEN && export HF_TOKEN
./upload-whisperx-diarization-model.sh

# Equivalent but leaves the token in shell history:
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

### Verify the host NVIDIA driver supports CUDA 12.8 wheels (first run only)

On the very first bench run, confirm the ECS GPU AL2 AMI's host NVIDIA
driver is new enough for the CUDA 12.8 torch wheels this image bundles:
check `nvidia-smi`'s output in the task log (or via an SSM session per §4)
and read the reported driver version -- torch cu128 requires driver
>= 525. If it's older, the container will fail at model-load time rather
than at task launch.

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

**⚠️ WARNING: A failed `ttobak-whisper` (legacy engine) bench run may still corrupt
real meeting data.**

The `whisperx` engine's fatal-error handler (`transcribe_whisperx.py`) checks
`should_mark_meeting_error(OUTPUT_KEY)` before writing meeting status: a
bench-scoped `OUTPUT_KEY` (anything under `bench-transcripts/`, as used by
this runbook) is recognized and the `status=error` write is skipped entirely
— a failed WhisperX bench run only ever surfaces in the ECS task log, never
on the real meeting row.

**`OUTPUT_KEY` is MANDATORY for the `ttobak-whisperx` task -- there is no
default.** Unlike the legacy engine, `transcribe_whisperx.py` raises before
any download/GPU work if `OUTPUT_KEY` is unset or blank; it never falls back
to the real pipeline's `transcripts/{meetingId}.json` key. The run-task
command above already always passes `OUTPUT_KEY` explicitly -- just don't
omit it if you adapt this command by hand.

The **legacy `ttobak-whisper` engine** (`transcribe.py`) has no such guard.
It does not import `whisper_common` at all -- Phase 1 gave it its own inline,
private copy of the error-marking logic (a plain `dynamodb.Table(...).
update_item(...)` in its `__main__` handler, not a call to
`whisper_common.mark_meeting_error`) -- but that inline copy still runs
unconditionally on any fatal failure (unstaged model bundle, VRAM OOM,
dependency crash, etc.), writing `status=error` to the **real meeting's
DynamoDB row** even though the `OUTPUT_KEY` was bench-scoped. This is visible
to users in the UI as a corrupted done meeting. So the risk below applies
specifically to the `ttobak-whisper` half of each benchmark pair.

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

### RECOMMENDED: CloudWatch agent `nvidia_smi` metrics (zero IAM change)

Before reaching for the SSM approach below, check whether CloudWatch agent GPU
metrics (the `nvidia_smi` plugin) are already configured on the ASG, or
whether the engine ever logs VRAM figures to its own CloudWatch log group —
either would answer the VRAM question with **zero IAM change and zero risk to
the running ASG**. This is the primary path: prefer it whenever it's
available, since it requires no elevation of the instance role and no ASG
cycling at all.

Neither is set up on `ttobak-whisper-asg` today. If configuring the
CloudWatch agent for this benchmark is out of scope for the time available,
the SSM attach below is the **fallback** — a deliberate, temporary elevation
for this one benchmark, not the default path, and one that carries real risk
to production STT (see the mandatory precondition below).

### FALLBACK: SSM attach (attach-before-bench, never cycle the ASG)

**Never scale/cycle `ttobak-whisper-asg` to force a fresh instance.** This
ASG (`infra/lib/whisper-stack.ts`) has `enableManagedTerminationProtection:
false`, so scaling it to 0 and back terminates *every* instance immediately,
including one mid-task on a real (non-benchmark) transcription — that
meeting gets stuck in `transcribing` until the 60-minute auto-expiry marks it
`error`. Do not suggest or perform an ASG scale-to-0/back-up cycle for this
runbook, ever, under any circumstance.

**The safe procedure relies on the ASG's own zero-scale-by-default
behavior, not on cycling it.** `ttobak-whisper-asg` normally runs at desired
capacity 0 (real transcriptions launch it on demand), and instances it does
launch are short-lived — roughly 16–27 minutes observed — so a benchmark run
started with `aws ecs run-task` (step 3 above) already launches a fresh
instance almost every time; you don't need to manufacture one. The
procedure is simply: **attach the SSM policy first, then start the
benchmark run.**

**Prerequisite: the ASG's EC2 instance role has no SSM permissions today.**
`infra/lib/whisper-stack.ts` scopes the ASG's EC2 instance role to
ECS-agent/ECR-pull access only (S3/DynamoDB access belongs to the task role
used by the container, not this instance role) — no `ssm:*`/
`AmazonSSMManagedInstanceCore` policy is attached, so a `ttobak-whisper-asg`
instance never registers with SSM, and
`aws ssm send-command` below fails with `InvalidInstanceId`. This is a
one-time, reversible, out-of-band operator step (not a CDK change — do not
add this permission to `whisper-stack.ts` for a one-off benchmark):

```bash
# 1. Find the ASG's instance role via its launch template (not the task/
#    execution roles used above -- this is the EC2 host, not the container).
aws autoscaling describe-auto-scaling-groups \
  --auto-scaling-group-names ttobak-whisper-asg --region ap-northeast-2 \
  --query 'AutoScalingGroups[0].LaunchTemplate'

aws ec2 describe-launch-template-versions \
  --launch-template-id <LAUNCH_TEMPLATE_ID> --region ap-northeast-2 \
  --query 'LaunchTemplateVersions[0].LaunchTemplateData.IamInstanceProfile'

# 2. Resolve the instance profile to its role, then attach the managed policy
#    BEFORE starting the benchmark run in step 3 above. The SSM agent is
#    preinstalled on the ECS-optimized AL2 GPU AMI already in use, so it
#    registers on its own at boot as soon as the role allows it -- no AMI
#    change needed.
aws iam attach-role-policy \
  --role-name <ROLE_NAME_FROM_INSTANCE_PROFILE> \
  --policy-arn arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore
```

Only instances launched *after* the policy is attached register with SSM.
If an already-running instance (one that predates the attach) doesn't show
up in SSM, **just wait for natural turnover** — instances in this ASG are
short-lived (~16–27 min) and get replaced on their own; never terminate or
cycle one manually to speed this up.

Revert once benchmarking is done — see the SSM detach checklist item in §7
Cleanup. Keep the whole SSM elevation time-boxed to this benchmark session:
attach, measure, detach the same day — don't leave the policy attached
across sessions or overnight.

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

Transcript contents are meeting PII -- download into a throwaway directory,
not a fixed `/tmp/*.json` path, so a leftover copy doesn't sit around with a
guessable name:

```bash
WORKDIR=$(mktemp -d)
for S in legacy whisperx; do
  aws s3 cp "s3://ttobak-assets-180294183052/bench-transcripts/${MEETING_ID}_bench_${S}.json" "$WORKDIR/${S}.json"
  echo "== $S: speakers =="
  jq '[.whisper_metadata.segments[].speaker] | unique' "$WORKDIR/${S}.json"
  echo "== $S: turn timeline =="
  jq -r '.whisper_metadata.segments[] | "\(.start)\t\(.end)\t\(.speaker // "-")\t\(.text)"' "$WORKDIR/${S}.json" | head -80
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

Keep only this table -- the aggregate metrics -- beyond the current session;
it is the primary input to the Phase 2 go/no-go decision and the ADR that
records it. The raw per-meeting transcripts (`$WORKDIR/*.json` from §5) still
contain PII and must not be retained past the session: if a reviewer needs to
re-check a specific turn boundary before the ADR is written, re-run the
comparison in §5 against the bench S3 objects (not yet deleted at that
point, see §7) rather than keeping a local copy around.

## 7. Cleanup

Checklist:

- [ ] Delete the bench S3 objects once the comparison table is recorded:

  ```bash
  aws s3 rm "s3://ttobak-assets-180294183052/bench-transcripts/" --recursive
  ```

  This bucket is versioned: `aws s3 rm --recursive` only writes delete
  markers, it does not purge the prior object versions. The underlying
  versions (still containing the PII transcript content) persist until the
  bucket's lifecycle rule expires them -- current-version expiration at 30
  days plus noncurrent-version expiration 30 days after that, so worst-case
  effective retention before the data is actually gone is ~60 days (see
  `docs/INFRA-SPEC.md`'s `bench-transcripts/` lifecycle entry). If a bench
  object must be purged sooner, delete its specific versions with
  `aws s3api delete-object --version-id`, not a bare `s3 rm`.

- [ ] Delete local copies of downloaded bench transcripts on the operator
  machine -- the `$WORKDIR/legacy.json` / `$WORKDIR/whisperx.json` files
  fetched in §5:

  ```bash
  rm -rf "$WORKDIR"
  ```

- [ ] Detach the one-time SSM permission granted in §4 (if it was added for
  this benchmark run):

  ```bash
  aws iam detach-role-policy \
    --role-name <ROLE_NAME_FROM_INSTANCE_PROFILE> \
    --policy-arn arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore
  ```

  Verify the detach actually took -- while this policy stays attached,
  anyone with `ssm:SendCommand` against this account can run arbitrary
  (including root) commands on a host that processes real meeting
  audio/credentials, so don't leave it attached on the strength of having
  run the command above:

  ```bash
  aws iam list-attached-role-policies --role-name <ROLE_NAME_FROM_INSTANCE_PROFILE>
  # expect: AmazonSSMManagedInstanceCore absent from the output
  ```

No ASG scale-down step is needed — `ttobak-whisper-spot` scales the cluster
back to 0 on its own once no tasks are running (same behavior as the
production Whisper pipeline's zero-scale cold start, see
`docs/runbooks/stt-pipeline-troubleshooting.md`).
