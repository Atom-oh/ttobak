# WhisperX Diarization Benchmark Runbook

> **Phase 2 landed (2026-09-03, ADR-035)**: the production engine now runs
> the pyannote 4.x community-1 diarization this runbook benchmarked
> (ASR-path packages unchanged and now exact-pinned; torch moved with the
> pyannote floor — see `backend/whisper/Dockerfile`'s comment). The
> procedures below remain valid for future engine comparisons; "legacy"
> descriptions of pyannote 3.1 refer to the pre-Phase-2 state. NOTE: a
> fresh §3 run of `ttobak-whisper` now produces community-1 output — its
> `SUFFIX=legacy` label no longer means "3.1 baseline". To reproduce a
> true 3.1 baseline, run a pre-Phase-2 task-definition revision (or the
> pre-Phase-2 image tag) and record the row as historical.

Phase 1 benchmark procedure for comparing the legacy Whisper+pyannote 3.1 pipeline
(task def `ttobak-whisper`, container `whisper`) against the WhisperX+pyannote 4.x
pipeline (task def `ttobak-whisperx`, container `whisperx`) on real, already-`done`
meetings. This is a manual, operator-driven benchmark: **when following this
runbook's procedure** (writing to `bench-transcripts/`, always passing an
explicit `OUTPUT_KEY` as §3 shows), a successful run produces no product
change and writes no output into real meeting's S3 data. That scoping matters
because of two things this runbook exists to warn about: the legacy
`ttobak-whisper` engine's fatal-error handler can still write `status=error`
to a real meeting's DynamoDB row even on a bench-scoped `OUTPUT_KEY` (see §3's
warning), and `transcribe_whisperx.py`'s own real-pipeline escape hatch
(`transcripts/*`, guarded by `validate_output_key`) exists for the Phase 2
cutover, not for this benchmark procedure — never point `OUTPUT_KEY` at a real
meeting's key while following this runbook. As of this PR,
`ttobak-whisperx-task-role` (`infra/lib/whisper-stack.ts`) no longer grants
S3 write on `transcripts/*` at all — only `bench-transcripts/*` — so
`validate_output_key`'s Phase-2 escape hatch is now also IAM-blocked in
Phase 1: pointing `OUTPUT_KEY` at a real meeting's key would fail with S3
`AccessDenied` even if it passed application-level validation. Account
180294183052, region ap-northeast-2 throughout.

Results feed the Phase 2 go/no-go decision and its ADR (see ADR-019 — the
speaker-diarization ADR this benchmark follows up on — for the format sibling
ADRs in this project use).

## 1. One-time setup

Deploy both stacks this benchmark needs, in this order:

1. **`TtobakStorageStack`** — adds the `bench-transcripts/` lifecycle rule
   (see `docs/INFRA-SPEC.md`). This is a separate stack from
   `TtobakWhisperStack`, so it needs its own deploy:

   ```bash
   cd infra && npx cdk deploy TtobakStorageStack --exclusively
   ```

2. **`TtobakWhisperStack`** — creates the `ttobak-whisperx` ECR repository
   and task definition. Deploy this second: a fresh setup that tries to
   `docker push` before this deploy fails with `RepositoryNotFoundException`.

   ```bash
   cd infra && npx cdk deploy TtobakWhisperStack --exclusively
   ```

`deploy-infra.yml` covers both of these automatically on merge, so this
two-step sequence is only needed for a manual/out-of-band setup.

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

# VERIFY the just-built LOCAL image BEFORE pushing (--pull=never makes sure
# this runs the local build, not a stale remote layer). A clean torchcodec
# import proves libpython3.12.so.1.0 loads — the exact failure that wasted
# the 2026-08-31 bench run.
docker run --rm --pull=never --entrypoint python3 \
  180294183052.dkr.ecr.ap-northeast-2.amazonaws.com/ttobak-whisperx:latest \
  -c "import torchcodec; print('torchcodec OK')"

# Push only after the verification above prints "torchcodec OK".
docker push 180294183052.dkr.ecr.ap-northeast-2.amazonaws.com/ttobak-whisperx:latest
```

> **Rebuild required (2026-09-02)**: the first bench run's diarization
> failure (pyannote 4.x decodes via torchcodec, whose FFmpeg-6 variant
> failed to load because `libpython3.12.so.1.0` was missing → RuntimeError)
> is fixed from this commit on — `Dockerfile.whisperx` now installs
> the shared library (`libpython3.12t64`, `libpython3.12` fallback), and `transcribe_whisperx.py` preloads the WAV in-memory
> so the torchcodec path is bypassed entirely. Rebuild, VERIFY (the
> torchcodec import smoke check in the build block above — run against the
> local image before pushing), and only then push; results produced by the
> image pushed on 2026-08-31 carry no diarization data and must not be used
> for comparison. The smoke check exists because `dpkg -s libpython3.12`
> is unreliable across the noble t64 transition (the Dockerfile installs
> `libpython3.12t64` with a `libpython3.12` fallback) — checking the actual
> import is what proves libpython3.12.so.1.0 loads.

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
check the driver version torch/CUDA reports at model-load time (the
`GPU[model-loaded]` line from §4 only carries memory/utilization, not driver
version -- if you need the driver version specifically, add a one-off
`nvidia-smi` call, or check the container's own startup/error log for a
CUDA/driver mismatch) -- torch cu128 requires driver >= 525. If it's older,
the container will fail at model-load time rather than at task launch.

### Verify whisperx 3.8.6 actually honors `initial_prompt` (first run only)

whisperx's batched transcription pipeline may ignore `asr_options.initial_prompt`
depending on the installed version -- the legacy engine (`transcribe.py`,
faster-whisper directly) always applies the custom-vocabulary prompt from
`VOCAB_KEY`/`config/custom-vocabulary.txt`, but whisperx's own ASR wrapper
does not guarantee the same behavior. Before trusting any §5/§6 text-quality
comparison that involves custom vocabulary, check whether the pinned
`whisperx==3.8.6`'s ASR module actually reads `initial_prompt`:

```bash
# Inside the built image, or a matching venv:
python3 -c "import whisperx, os; print(os.path.dirname(whisperx.__file__))"
grep -rn "initial_prompt\|hotwords" "$(python3 -c 'import whisperx, os; print(os.path.dirname(whisperx.__file__))')"
```

If `initial_prompt` doesn't appear (or is present but unused/overridden) in
the ASR call path, the comparison is unfair to whichever engine doesn't
apply it: the legacy engine's transcript benefits from vocabulary hints the
whisperx transcript never got. In that case, either switch to whatever
whisperx does support for vocabulary hinting (`hotwords`, if grep finds it
wired into the ASR options) or annotate every §6 result row noting that
custom-vocabulary terms were not comparably applied.

## 2. Selecting meetings

Pick 3–5 meetings already in `done` status, spanning a range of speaker counts
and durations (e.g. a short 2-speaker call, a longer 3-4 speaker meeting, one
with cross-talk if you have one). For each, note:

- `userId`
- `meetingId`
- audio S3 key(s), from the meeting's `audioKey` or `audioKeys` (DynamoDB `ttobak-main`,
  `PK=USER#<userId>`, `SK=MEETING#<meetingId>`)

Single-file meetings populate `audioKey` only; multi-part meetings populate `audioKeys` (a list) — for the Phase 1 benchmark prefer single-part meetings (the WhisperX task processes one `AUDIO_KEY` per run; multi-part meetings would need one run per part and are better excluded from selection).

```bash
aws dynamodb get-item --table-name ttobak-main \
  --key '{"PK":{"S":"USER#<USER_ID>"},"SK":{"S":"MEETING#<MEETING_ID>"}}' \
  --region ap-northeast-2 \
  --query '{multi: Item.audioKeys, single: Item.audioKey}'
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
real meeting data. This warning applies ONLY to the legacy engine, not to `ttobak-whisperx`.**

The `whisperx` engine's fatal-error handler (`transcribe_whisperx.py`) checks
`should_mark_meeting_error(OUTPUT_KEY)` before writing meeting status: a
bench-scoped `OUTPUT_KEY` (anything under `bench-transcripts/`, as used by
this runbook) is recognized and the `status=error` write is skipped entirely
— a failed WhisperX bench run only ever surfaces in the ECS task log, never
on the real meeting row. As of the dedicated `ttobak-whisperx-task-role`
(`infra/lib/whisper-stack.ts`), this is now doubly true even in the
edge case where an operator mistakenly points `OUTPUT_KEY` at a real
meeting's `transcripts/{meetingId}.json` key: that role carries no DynamoDB
grant at all, so any attempted `status=error` write is denied at the IAM
layer (logged as an `AccessDenied`, not applied) regardless of what
`should_mark_meeting_error` decides. The `ttobak-whisperx` bench task can no
longer flip a real meeting's status under any circumstance.

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

### 3b. VAD recall tuning (per-run env overrides)

The first full bench pair (2026-09-02, a435a3dc) showed whisperx's default
VAD dropping ~25% of real speech: 252s of legacy-covered speech time had NO
whisperx segments, and sampled gap text was clearly real meeting content.
Speaker separation won; ASR recall lost. Tune recall by adding VAD env
overrides to the whisperx `run-task` container overrides.

**Precondition — one image rebuild.** The knobs are read by engine code, so
the running image must already contain them (any image built after the PR
adding `_vad_config_from_env` merged). An older image ignores the env vars
silently. Rebuild once via the temporary `build-whisperx-image.yml`
workflow. If that workflow has been retired, rebuild by hand from an
x86_64 host (the image is amd64 CUDA — an ARM dev host cannot build it
locally, which is why the workflow pins the `ttobak-x86` runner):

```bash
aws ecr get-login-password --region ap-northeast-2 | docker login --username AWS \
  --password-stdin 180294183052.dkr.ecr.ap-northeast-2.amazonaws.com
docker build --platform linux/amd64 -f backend/whisper/Dockerfile.whisperx \
  -t 180294183052.dkr.ecr.ap-northeast-2.amazonaws.com/ttobak-whisperx:latest backend/whisper
# torchcodec smoke check BEFORE push (same gate as the workflow):
docker run --rm --pull=never --entrypoint python3 \
  180294183052.dkr.ecr.ap-northeast-2.amazonaws.com/ttobak-whisperx:latest \
  -c "import torchcodec; print('torchcodec OK')"
docker push 180294183052.dkr.ecr.ap-northeast-2.amazonaws.com/ttobak-whisperx:latest
```

The task definition references the mutable `latest` tag, so a pushed
rebuild is picked up by the next `run-task` automatically — no
task-definition change needed. Then verify the override actually applied
by checking the task log for the `VAD config: method=... options=...`
line, which the engine prints on every run (it shows `defaults` when no
override is in effect — which is also how a typo'd env NAME, silently
ignored, gets caught) — treat a run whose line doesn't echo your intended
config as misconfigured, not as a data point. After that one rebuild,
sweeping configs needs no further rebuilds. A rejected config
(`BenchConfigError`) exits before any download or DynamoDB access — bench
tasks never touch meeting rows regardless (`should_mark_meeting_error`
returns false for bench keys), so a failed sweep attempt needs no cleanup.

| env | meaning | valid range | default | try |
|---|---|---|---|---|
| `WHISPERX_VAD_ONSET` | speech-start sensitivity (lower = more eager, higher recall) | (0, 1) exclusive | 0.500 | 0.35 (with OFFSET 0.25), then 0.25 (with OFFSET 0.15) |
| `WHISPERX_VAD_OFFSET` | speech-end sensitivity; must stay BELOW the effective onset on the pyannote path | (0, 1) exclusive | 0.363 | paired with ONSET above |
| `WHISPERX_VAD_METHOD` | `pyannote` or `silero` (silero uses ONSET + CHUNK_SIZE only; OFFSET is ignored there — the engine warns and skips the hysteresis check) | those two values | pyannote | silero + ONSET 0.25 |
| `WHISPERX_VAD_CHUNK_SIZE` | merge window (s); the engine forwards it to both `vad_options` and `model.transcribe()` — the latter is what governs the merge on the pyannote path | 1–30 (Whisper's encoder window is 30s; larger would truncate chunk tails) | 30 (`transcribe()`'s default; unset silero uses whisperx's own `vad_options` default) | — |

Onset/offset are a hysteresis pair on the pyannote path: lowering
`WHISPERX_VAD_ONSET` below the 0.363 default offset requires setting
`WHISPERX_VAD_OFFSET` lower still (the engine fail-fasts with
`BenchConfigError` on an inverted pair rather than producing oscillating
micro-segments that would skew the comparison). Don't move one knob alone
across the other's default — lowering ONSET below 0.363 or raising OFFSET
above 0.500 both invert the pair; use the pairs in the table. Example
sweep-attempt overrides (add to the §3 `run-task` `--overrides` JSON's
`environment` array, alongside a distinct `OUTPUT_KEY`):

```json
{"name": "WHISPERX_VAD_ONSET", "value": "0.35"},
{"name": "WHISPERX_VAD_OFFSET", "value": "0.25"}
```

Use a distinct `OUTPUT_KEY` per configuration
(`..._bench_whisperx_onset035.json` etc.) so attempts are comparable.
Measure recall per attempt by time-coverage gap against the legacy output:
merge each engine's segment intervals, subtract whisperx's coverage from
legacy's, and inspect the >5s gaps' legacy text (real content vs
hallucination) under the §5 handling rules (mktemp throwaway dir; it is
meeting PII — keep only aggregate numbers per §6, never the text itself).
Record each attempt as its own §6 row (VAD config + real-content gap
columns). Sweep outputs are additional PII copies under
`bench-transcripts/` — §7's recursive cleanup + the 30-day lifecycle
already cover them, no per-sweep cleanup needed. Converge on the config
with near-zero real-content gaps, then re-check speaker distribution
didn't regress (§5).

### 3c. Hybrid engine: legacy faster-whisper ASR + community-1 diarization

The 2026-09-02 sweep left the two engines winning on different axes:
legacy faster-whisper had the best ASR recall (whisperx's best config,
silero onset 0.25, still left ~143s of real content uncovered vs legacy's
~49s), while whisperx produced the clean speaker count. If the diarization
win comes from the pyannote community-1 (4.x) model rather than from
whisperx itself, "keep the existing whisper ASR, upgrade only diarization"
beats a whisperx cutover under an accuracy-and-diarization-first decision
rule. `transcribe_fw_p4.py` tests exactly that: legacy `transcribe.py`
ASR parameters + the whisperx image's community-1 diarization.

It ships inside the whisperx image (faster-whisper is already a whisperx
dependency) — run it from the SAME task definition by adding one entry to
the §3 `--overrides` JSON's `environment` array:

```json
{"name": "ENGINE", "value": "fw_p4"}
```

The image's ENTRYPOINT is pinned to `run_engine.py`, a dispatcher that
allowlists the two engine scripts (`whisperx` default, `fw_p4`) and
rejects ANY `containerOverrides` command arguments — a command override
is deliberately a hard no-op channel, because with a plain
`ENTRYPOINT ["python3"]` it would double as arbitrary-code execution on a
host-networkMode task with all-users audio read access. For the same
reason, never set `entryPoint`/`command` on the CDK task definition: a
CDK `entryPoint` SILENTLY bypasses the pin — and CDK is the only bypass
channel, since ECS RunTask `containerOverrides` has no `entryPoint` field
at all — while a `command` is loudly rejected by the dispatcher, banned
in CDK all the same so the surface stays declarative. `whisper-stack.ts`
sets neither today; a `whisper-stack.test.ts` assertion (test-infra.yml
at PR time, deploy-infra.yml pre-deploy) and a CLAUDE.md gotcha guard
this.

A bad `ENGINE` value fails fast and loud: `FATAL: ENGINE must be one
of ['fw_p4', 'whisperx'] ...` on stderr, exit 1, before any model or S3
work.

**Precondition — one image rebuild** (same as §3b): the image must have
been built after `run_engine.py`/`transcribe_fw_p4.py` landed; rebuild
once via the §1 build procedure. An image built BEFORE the dispatcher
ignores the ENGINE env silently and runs whisperx — so before recording a
§6 row, verify in the task log that the run printed
`run_engine: dispatching to transcribe_fw_p4.py` and
`Transcribing (legacy sequential)...`, and check the output JSON's
`whisper_metadata.engine` field is `fw-legacy-pyannote4-bench`, not
`whisperx-large-v3-gpu`.

Of the per-run override env vars, this engine reads only
`MEETING_ID`/`USER_ID`/`OUTPUT_KEY`/`AUDIO_KEY`/`INITIAL_PROMPT`/
`NUM_SPEAKERS`. Task-definition-level vars apply unchanged:
`BUCKET_NAME`/`TABLE_NAME` are required at import time; `VOCAB_KEY`,
`MODEL_S3_KEY` and `WHISPERX_DIARIZATION_S3_KEY` have code defaults but
the task definition sets them, and the hybrid uses them too (same staged
CT2 model dir, same community-1 bundle). Only the §3b `WHISPERX_VAD_*`
knobs and alignment settings are whisperx-only and silently unused here —
drop those from the overrides JSON for hybrid runs. §4's per-stage VRAM
measurement applies to hybrid runs unchanged (same `GPU[stage]` log
lines).

**OUTPUT_KEY convention**: use the `_bench_fw_p4` suffix —
`bench-transcripts/{meetingId}_bench_fw_p4.json`. The §3 snippet's
`SUFFIX` derivation only knows `legacy`/`whisperx`; reusing it verbatim
for a hybrid run would overwrite the meeting's `_bench_whisperx.json` and
§5 would misread the hybrid output as whisperx. Set the suffix explicitly.

Its `OUTPUT_KEY` must be under `bench-transcripts/` (the engine refuses
anything else, including the real-pipeline key `validate_output_key`
would allow — it is bench-only by construction and never marks meetings
errored). Output rows carry `engine: fw-legacy-pyannote4-bench`. Compare
against `bench_legacy` (same ASR, pyannote 3.x) for the diarization delta
and against the best whisperx config for the recall delta, and record it
as a §6 row like any other attempt. Known non-parity to keep in mind:
this image's faster-whisper comes via whisperx 3.8.6 (1.2.1), while the
legacy `Dockerfile` installs faster-whisper UNPINNED — the deployed legacy
image has whatever was current at its last build. Check the running legacy
image's actual version before attributing an ASR delta to anything else.

Hybrid outputs are more PII transcript copies under `bench-transcripts/`
— when the comparison series concludes, run the §7 cleanup rather than
waiting out the 30-day lifecycle.

## 4. Resource measurement per WhisperX run

This resolves the design doc's open VRAM/instance-sizing question.

### PRIMARY (and default): read the `GPU[...]` lines from the task's CloudWatch logs

`transcribe_whisperx.py` self-reports one `nvidia-smi` sample to stdout at
up to five points in a run (`model-loaded`, `transcribed`, `aligning`,
`align-freed`, `diarized` — diarization is best-effort and is skipped
entirely when the model bundle isn't staged, in which case only the first
four lines appear), printed as
`GPU[<stage>]: <memory.used>, <memory.total>, <utilization.gpu>`. This
answers the peak-VRAM question directly from the task's own CloudWatch log
group — **no IAM change, no elevation of the shared production instance
role, and no ASG involvement at all.** This is the primary path and requires
no setup; just read the log after (or during) a run.

Also note: after transcription, the engine frees the ASR model
(`del model` + `torch.cuda.empty_cache()`) before alignment loads its own
model. `aligning` is sampled immediately after `whisperx.align(...)`
returns, while the wav2vec2 align model is still resident on the GPU — this
is the sample that actually captures alignment's own VRAM use, which is
often the run's peak. Only after that sample does the engine free the align
model (`align_model = None` + `torch.cuda.empty_cache()`); `align-freed` is
sampled next and reflects residency with that model already released, not
alignment's own peak (an earlier revision sampled only this point, under the
stage label `aligned`, which meant alignment's own VRAM use was never
captured at all). So each `GPU[...]` sample reflects only the model(s)
actually resident at that point, not a cumulative all-models-resident
figure — take the **max across all (up to five) samples** as the run's
peak VRAM, not just the last one. One honest caveat: these are still
point-in-time samples taken right after each stage's own work completes
(or, for `aligning`, right as alignment finishes but before its model is
freed), not a continuous trace — a stage could still transiently peak
higher mid-computation than what its sample captures (see the CloudWatch
agent aside below if that gap matters for a specific run).

Find the task's own log stream and filter for the GPU lines — **scope to
this run's stream, not the whole log group**: the log group's 30-day
retention means a plain `filter-log-events` over the whole group mixes GPU
lines from other runs, which silently corrupts the peak-VRAM reading for the
run you actually care about. The awslogs driver composes the stream name as
`<streamPrefix>/<containerName>/<task-id>`; for this task definition that's
always `whisperx/whisperx/<task-id>` (`streamPrefix: 'whisperx'` and
container name `whisperx`, both from `infra/lib/whisper-stack.ts`):

```bash
aws ecs list-tasks --cluster ttobak-whisper --region ap-northeast-2
TASK_ID=$(aws ecs describe-tasks --cluster ttobak-whisper --tasks <TASK_ARN> \
  --region ap-northeast-2 --query 'tasks[0].taskArn' --output text | awk -F/ '{print $NF}')

aws logs get-log-events \
  --log-group-name /ttobak/whisperx \
  --log-stream-name "whisperx/whisperx/${TASK_ID}" \
  --region ap-northeast-2 \
  --query 'events[*].message' --output text | grep 'GPU\['
```

If you must use `filter-log-events` instead (e.g. across a short list of
known task ids), pass `--log-stream-names` to keep the scoping and quote the
filter pattern's literal `[` (CloudWatch filter-pattern syntax treats a bare
`[` as the start of a structured/space-delimited pattern, so an unquoted
`--filter-pattern "GPU["` does not match literally):

```bash
aws logs filter-log-events \
  --log-group-name /ttobak/whisperx \
  --log-stream-names "whisperx/whisperx/${TASK_ID}" \
  --filter-pattern '"GPU["' \
  --region ap-northeast-2 \
  --query 'events[*].message'
```

`/ttobak/whisperx` is the WhisperX task's dedicated log group
(`WhisperXLogGroup` in `infra/lib/whisper-stack.ts`, 30-day retention) — the
name above is exact, not a placeholder to adjust.

Record the highest `memory.used` value across the (up to five) `GPU[...]`
lines — that's the number that matters for Phase 2's instance-sizing
decision.

### ASIDE: CloudWatch agent `nvidia_smi` metrics

If a continuous GPU utilization/memory *timeseries* is needed (not just five
point samples), configuring the CloudWatch agent's `nvidia_smi` plugin on the
ASG is an option — also zero additional IAM change beyond what the agent
itself needs, and zero risk to the running ASG. Not set up on
`ttobak-whisper-asg` today; out of scope unless the five log-line samples
above turn out to be insufficient for the benchmark.

**Never scale/cycle `ttobak-whisper-asg` to force a fresh instance**,
regardless of which measurement approach is used. This ASG
(`infra/lib/whisper-stack.ts`) has `enableManagedTerminationProtection:
false`, so scaling it to 0 and back terminates *every* instance immediately,
including one mid-task on a real (non-benchmark) transcription — that
meeting gets stuck in `transcribing` until the 60-minute auto-expiry marks it
`error`. Do not suggest or perform an ASG scale-to-0/back-up cycle for this
runbook, ever, under any circumstance.

## 5. Comparing outputs

Transcript contents are meeting PII -- download into a throwaway directory,
not a fixed `/tmp/*.json` path, so a leftover copy doesn't sit around with a
guessable name:

```bash
WORKDIR=$(mktemp -d)
# Include every suffix present for the meeting: drop fw_p4 if no §3c
# hybrid run exists, add `whisperx_silero025`-style §3b sweep keys as used.
for S in legacy whisperx fw_p4; do
  aws s3 cp "s3://ttobak-assets-180294183052/bench-transcripts/${MEETING_ID}_bench_${S}.json" "$WORKDIR/${S}.json" || { echo "== $S: no bench output, skipping =="; continue; }
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

Build one table row per (meeting, engine, config) attempt — the legacy
baseline, each §3b VAD config, and each §3c hybrid run get their own row —
since this table is the ONLY thing retained past the session: without the
engine/config/recall columns below, §7 cleanup would erase the evidence
for which attempt won, leaving the Phase-2 go/no-go with no recorded
basis.

| Meeting ID | Duration | Participants | Engine (`whisper_metadata.engine`) | Image digest | VAD config (method/onset/offset/chunk, "defaults" if none; n/a for legacy and fw_p4) | Real-content coverage gap vs legacy (s) | Speakers detected | Peak VRAM | Wall-clock | Alignment repaired segments | Qualitative verdict |
|---|---|---|---|---|---|---|---|---|---|---|---|

"Engine" is the output JSON's `whisper_metadata.engine`
(`whisper-large-v3-gpu` / `whisperx-large-v3-gpu` /
`fw-legacy-pyannote4-bench`) — with the §3c dispatcher, whisperx and
fw_p4 rows come from the SAME image, so the digest alone cannot tell them
apart. "Image digest" pins which build produced the attempt — the task
definition references the mutable `latest` tag and an older image ignores
the ENGINE/VAD env knobs silently, so without digest + engine a row is
not reproducible evidence. Capture the digest per attempt from the task
itself (the runtime log line disappears with the session):
`aws ecs describe-tasks --cluster ttobak-whisper --tasks <task-id>
--query 'tasks[0].containers[0].imageDigest' --output text`.

"Alignment repaired segments" applies to whisperx rows only — record n/a for legacy and fw_p4 rows (both run with `alignment_enabled: false` by construction).

"Real-content coverage gap" is §3b's recall metric: total seconds of
legacy-covered speech with no whisperx segment, counting only gaps whose
legacy text was judged real content (not hallucination) — record the
aggregate seconds and gap count, never the text itself.

"Alignment repaired segments" is `whisper_metadata.alignment_repaired` from
the WhisperX transcript JSON (§5) — the count of segments the aligner could
not fully align, repaired to segment-level timestamps (copied from the
same-index input when the aligner kept the input segmentation, or
interpolated from neighbor boundaries when it re-split) with their words
dropped. No segment text is ever discarded — the re-split path requires the
whitespace-normalized RENDERED transcript (segment texts joined by single
spaces, as build_result produces) to be identical to the ASR input's; any
aligner-side loss, duplication, reorder, or mid-word boundary insertion
discards the aligned result entirely (`alignment_enabled: false`).
Note (2026-09-02): whisperx.align() re-splitting segments (e.g. 43→117) is
its normal behavior and the re-split output is now ACCEPTED — so the
whisperx side of a bench pair typically has a DIFFERENT segment count from
the legacy side; compare speaker turns by time ranges, not by index.
A nonzero value flags a partial-repair run so it isn't silently read as
equivalent to a clean, fully-aligned one.

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

No SSM/IAM cleanup step is needed — §4's measurement path reads GPU figures
straight from the task's own CloudWatch log, with no elevation of the
production instance role at any point in this runbook.

No ASG scale-down step is needed — `ttobak-whisper-spot` scales the cluster
back to 0 on its own once no tasks are running (same behavior as the
production Whisper pipeline's zero-scale cold start, see
`docs/runbooks/stt-pipeline-troubleshooting.md`).
