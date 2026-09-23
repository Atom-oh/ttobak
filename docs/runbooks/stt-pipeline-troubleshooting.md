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

## Summarizing

Check the expected transcripts/ output and matching EventBridge rule, then
summarize logs. Multipart meetings have a separate all-parts completion path.
ImageUploadCompleted is unrelated; DynamoDB stream enablement is not the current
summarize trigger.

```bash
aws events describe-rule --name ttobak-transcript-upload --region ap-northeast-2
aws logs tail /aws/lambda/ttobak-summarize --since 30m --region ap-northeast-2
```

The Lambda budget is 15 minutes. Retry eligibility after 20 minutes uses an atomic
claim; it does not schedule another delivery. GetMeeting reconciles stuck status
after 60 minutes. These thresholds solve different problems; do not shorten one
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
For an operator-approved full rebatch, inspect the maintenance script and preview
one target first:

```bash
python3 scripts/whisper-rebatch.py <MEETING_ID>
```

The script defaults to dry run. `--run` actually starts ECS work; `--num-speakers`
requires a single meeting ID. Check its present selection/write behavior before
executing. Do not fabricate an event with reserved `aws.s3` source via PutEvents or
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
