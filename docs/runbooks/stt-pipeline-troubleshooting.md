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
