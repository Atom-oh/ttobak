# Incident response

Use current resource IDs/configuration and a specific time window. This runbook is
an investigation sequence, not authorization to deploy, reset state or alter IAM.
Do not paste credentials or meeting content into public issues/review logs.

## Triage

1. Record failing operation, user-visible error, timestamp, request/meeting ID,
   affected revision and scope. Separate capture, upload, transcription, note
   generation, authentication and retrieval failures.
2. Inspect relevant Lambda logs and metrics. Missing datapoints are not zero errors;
   sum the intended interval instead of selecting an arbitrary first datapoint.
3. For frontend failures, fetch runtime config.json and inspect HTML/chunk responses
   through CloudFront. For auth, distinguish expiry/refresh/configuration errors
   from missing server validation.
4. For asynchronous work, trace the input object/event, orchestrator task, output
   object and final conditional write. Use actual task/stack descriptions rather
   than old resource names from historical reports.

Read-only examples for the configured application region:

```bash
aws logs tail /aws/lambda/ttobak-api --since 30m --region ap-northeast-2
aws logs tail /aws/lambda/ttobak-transcribe --since 30m --region ap-northeast-2
aws logs tail /aws/lambda/ttobak-summarize --since 30m --region ap-northeast-2
aws cloudformation describe-stack-events --stack-name TtobakGatewayStack --region ap-northeast-2
```

Logs can contain application content; inspect them privately and redact excerpts.
The historical priority targets were 15 minutes for outages, one hour for broken
features and four hours for degradation; they are triage goals, not an on-call SLA.

## Recovery selection

- Missing config.json: restore through the authorized FrontendStack/deployment
  path. Do not run a full infra deployment by reflex.
- Stale HTML/chunks: preserve runtime config, force HTML refresh and invalidate
  CloudFront as the deployment workflow does.
- STT/Spot failure: follow [STT troubleshooting](stt-pipeline-troubleshooting.md).
- Failed CloudFormation operation: inspect events/change sets and state first.
  Do not blindly delete change sets or invoke rollback on an unrelated stack.
- Bad release: redeploy the last working artifact with its safety prerequisites;
  do not assume a Lambda live alias exists for every function.

Never reset meeting status with an unconditional DynamoDB write; it can violate
current ownership/retry/multipart invariants. Never use generic all-stack deployment
or implicit dependencies. See [deployment](deployment.md), and preserve the QA
storage guard during rollback as described in [its rollout record](qa-transcript-read-rollout.md).

Record root cause, actual change/revision, verification and unresolved limits.
Historical documentation or a successful merge is not proof of recovery.
