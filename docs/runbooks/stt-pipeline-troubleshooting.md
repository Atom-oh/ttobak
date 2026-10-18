# STT troubleshooting

Trace the actual meeting's audio keys, provider, status and last update before
retrying. Use authenticated application reads; avoid placing raw transcript/audio
in public diagnostics. The source of truth is transcribe/summarize commands,
service/upload.go and WhisperStack, not old live-ECS plans.

## Transcribing

```bash
aws logs tail /aws/lambda/ttobak-transcribe --since 30m --region ap-northeast-2
aws ecs list-tasks --cluster ttobak-whisper --region ap-northeast-2
aws ecs describe-tasks --cluster ttobak-whisper --tasks <TASK_ARN> --region ap-northeast-2
```

For a stopped task, inspect stoppedReason, container exit reason and the log
configuration in its task definition. Resolve the log group/stream from that
configuration rather than assuming `/ecs/whisper`. Determine whether failure was
Spot capacity, image/bundle loading, disk, model compatibility or output writing.

For zero-scale startup, inspect the current ASG and capacity provider:

```bash
aws autoscaling describe-auto-scaling-groups --auto-scaling-group-names ttobak-whisper-asg --region ap-northeast-2
aws ecs describe-capacity-providers --capacity-providers ttobak-whisper-spot --region ap-northeast-2
```

Use ASG scaling activities and actual subnet availability for capacity failures.
Do not force a historic AZ list. Reused hosts require disk headroom and shortened
stopped-task cleanup; do not remove those settings as arbitrary overprovisioning.

Check that AZRebalance is still suspended and that no CDK drain hook exists
(managed draining replaces it):

```bash
aws autoscaling describe-auto-scaling-groups --auto-scaling-group-names ttobak-whisper-asg \
  --query 'AutoScalingGroups[0].SuspendedProcesses[].ProcessName' --region ap-northeast-2
aws autoscaling describe-lifecycle-hooks --auto-scaling-group-name ttobak-whisper-asg --region ap-northeast-2
```

`AZRebalance` must be listed. The only termination hook should be ECS's own
`ecs-managed-draining-termination-hook`; a CDK `...DrainHook...` hook means the
old InstanceDrainHook is back. A manual resume is
drift; redeploy WhisperStack (idle cluster only) rather than editing it by hand.

## Scheduled STT recovery

`ttobak-stt-recovery` invokes `ttobak-transcribe` every 15 minutes (ADR-031
amendment). Its log lines start with `stt-recovery` and carry only meeting IDs,
statuses, sizes, attempts and fixed reasons; each tick ends with one
`Ttobak/SttRecovery` embedded-metric line of counts.

```bash
aws logs tail /aws/lambda/ttobak-transcribe --since 1h --filter-pattern '"stt-recovery"' --region ap-northeast-2
aws ecs list-tasks --cluster ttobak-whisper --started-by sttrec-<MEETING_ID> --desired-status STOPPED --region ap-northeast-2
```

| Action | Meaning |
|---|---|
| `redrive_stt` | Stuck past its size-based deadline; claims attempt n (max 2) and runs Whisper with `ClientToken sttrec-{id}-{n}` |
| `redrive_summary` | `transcripts/{id}.json` exists, is not older than the latest restart (newest audio-prefix object such as a `rediarize_` copy, last claim, or `updatedAt` while transcribing) and was never published; self-copy emits a real S3 event (max 2) |
| `mark_exhausted` | Both attempts used for this audio key; marks `RECOVERY_EXHAUSTED` |
| `report_checkpoint_only` | Only `recording_progress.*` exists; the owner recovers it (ADR-048) |
| `report_multipart`, `report_unsupported`, `report` | Not processed automatically; investigate |
| `terminal`, `would_delete`, `delete` | `AUDIO_CORRUPT`/`NO_SPEECH` mark; deletion after seven untouched days |
| `cluster_busy`, `stt_deferred` | Another task holds the GPU pool or the one-run budget is used |

`STT_RECOVERY_MODE` and `STT_RECOVERY_DELETE_MODE` take `off`, `report` or
`active` (unknown values mean `off`). `report` writes nothing. To stop the tick in
an incident, set either to `off` through a GatewayStack change, or disable the
rule. Any user write after a mark (notes, re-upload, rediarize) bumps `updatedAt`
and permanently cancels that mark's deletion.

Grace deletion removes only current S3 versions. To restore an object, list its
versions and delete the delete marker:

```bash
aws s3api list-object-versions --bucket <BUCKET> --prefix audio/<USER_ID>/<MEETING_ID>/ --region ap-northeast-2
aws s3api delete-object --bucket <BUCKET> --key <KEY> --version-id <DELETE_MARKER_VERSION_ID> --region ap-northeast-2
```

The DynamoDB rows are not restorable this way; restoring a meeting requires a
table point-in-time recovery review.

## Summarizing

Check the expected transcripts/ output and matching EventBridge rule, then
summarize logs. Multipart meetings have a separate all-parts completion path.
ImageUploadCompleted is unrelated; DynamoDB stream enablement is not the current
summarize trigger.

```bash
aws events describe-rule --name ttobak-transcript-upload --region ap-northeast-2
aws logs tail /aws/lambda/ttobak-summarize --since 30m --region ap-northeast-2
```

The Lambda budget is 15 minutes. Summarize-retry eligibility after 20 minutes uses
an atomic claim; it does not schedule another delivery. GetMeeting reconciles stuck
status after 60 minutes. The separate STT recovery tick re-drives an unpublished
single-file transcript after 20 minutes. A blank transcript is marked `NO_SPEECH`
instead of being summarized. These thresholds solve different problems; do not shorten one
based on a stale runbook or rewrite rows unconditionally.

Final notes that stop at `max_tokens` continue within the same source snapshot,
using the same model, system prompt, original messages and per-call token budget.
At most two continuation calls are allowed, with a 256 KiB combined UTF-8 output
cap and the caller's existing deadline. No partial result is published: the final
response must end normally, and all source/permission/publication checks still
run. Auxiliary refinement/image/action calls retain their own completion rules.
Continuation is not a reset of the durable source-conflict retry budget.
Thinking consumes the same output limit and may return no visible text. Preserve
the response's signed thinking/redacted blocks unchanged when continuing; never
log or publish them. The opaque continuation context has a separate 1 MiB cap.

A saved-summary `SOURCE_CHANGED` failure means the snapshot no longer matched;
preserve the newer human edits and request a fresh summary. Do not remove its
conditions or publish output produced from the old snapshot.

## Authorized recovery

Use the application Recover action for a saved recording_progress object, or
Rediarize for supported single-part Whisper meetings and a corrected speaker bound.
The scheduled tick supersedes bulk rebatching. For an operator-approved one-off,
the maintenance script requires an explicit meeting ID and previews by default:

```bash
python3 scripts/whisper-rebatch.py <MEETING_ID>
```

`--run` starts ECS work only for an `error`/`transcribing` meeting, after a
conditional update on its observed status and `updatedAt`; it prints no titles. Do not fabricate an event with reserved `aws.s3` source via PutEvents or
blindly reset DynamoDB state. Preserve coherent ASR/pyannote image pins (ADR-035).

For an operator-authorized saved-source recovery, `backend/cmd/resummary` defaults
to read-only diagnostics (state, byte counts, hashes and source-object validation;
never note/transcript text). It verifies the explicitly supplied AWS account and
resolves the canonical owner before using the same service as the authenticated
API. The report includes the operator ARN, and failed selected-source validation
returns a nonzero exit status. Unselected transcripts do not block recovery.
Run from `backend`, with the approved temporary-credential profile:

```bash
go run ./cmd/resummary --expected-account "$EXPECTED_ACCOUNT" --bucket "$ASSET_BUCKET" --meeting-id "$MEETING_ID"
go run ./cmd/resummary --expected-account "$EXPECTED_ACCOUNT" --bucket "$ASSET_BUCKET" --meeting-id "$MEETING_ID" --request
```

Wait for `ANALYSIS#summary` success and verify its result hash matches the saved
content. `--request-action-items` separately queues analysis of that saved summary,
without transcript/attachment preconditions, preserving existing task IDs/completion
through the normal service. The tool never
resets meeting status or retry counters, overwrites source text, or reruns STT.
