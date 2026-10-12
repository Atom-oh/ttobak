# Pipeline failure retention and controlled replay

This runbook describes checked-in recovery controls. Deployment, notification
delivery and synthetic failure/recovery acceptance must be recorded separately.

## What is retained

GatewayStack owns two seven-day, SSE-SQS encrypted, TLS-only queues, retained on
stack removal. Neither queue has a consumer or an automatic redrive destination.

| Queue | Record | Coverage |
|---|---|---|
| `ttobak-pipeline-delivery-failures` | Original EventBridge event plus `RULE_ARN`/`TARGET_ARN` message attributes | Audio upload/crop, image completion, transcript upload, all-parts completion and optional slide conversion |
| `ttobak-pipeline-execution-failures` | Lambda destination record with `requestPayload`, execution context and possible error output | Transcribe, summarize, process-image and optional convert-doc |

Existing EventBridge retry defaults are unchanged. These Lambda functions keep
two asynchronous retries and a six-hour maximum event age; only exhausted
failures are sent to the execution queue. The action-item, saved-summary, QA,
extraction and canonical-index delivery/retry controls remain separate.
Action-item and saved-summary executions also use the summarize destination.
QA, canonical-index, action-item and saved-summary DLQs gain backlog/age alarms.

Image analysis now returns closed failure categories for model, metadata and
publication failures. It preserves prior content on failure and conditionally
publishes only to the same unfinished attachment. Completed/deleted/changed
attachments are ignored on redelivery. Classification and output are never put
in these new error messages.

Destinations capture Lambda failures, not every product failure. A handler that
deliberately records a terminal application error and returns success does not
produce a destination record. Successful ECS task submission is not successful
transcription; subsequent GPU/Spot failures still need the existing meeting/device
recovery and worker investigation. Check canonical meeting/analysis state as well
as queues and Lambda metrics.

## Alarm routing and release acceptance

Alarms cover visible backlog, oldest-message age, Lambda errors/throttles/async
drops/destination delivery failures, and core EventBridge delivery failures.
Sparse failure metrics use one breaching sample in two one-minute periods and
`notBreaching` missing-data treatment. Backlog alarms trigger at one message;
queue-age alarms trigger at fifteen minutes. Tune against measured traffic.

`PipelineAlarmTopicArn` is an optional CloudFormation parameter on GatewayStack.
An empty value creates alarms without notification actions, and stack output
`PipelineAlarmRouting` explicitly reports `UNCONFIGURED`. Configure an existing
standard SNS topic with a reviewed policy permitting the regional CloudWatch
service to publish and a confirmed operator subscription. This change does not
create a topic, subscribe an address or send notifications.

After reviewing the exact synth/diff, deploy changed stacks individually with
`--exclusively`; never deploy KnowledgeStack incidentally. Example parameter:

```bash
npx cdk deploy TtobakGatewayStack --exclusively \
  --parameters TtobakGatewayStack:PipelineAlarmTopicArn=arn:aws:sns:ap-northeast-2:ACCOUNT:APPROVED_TOPIC
```

A configured ARN is not proof that anyone receives alarms. Before release,
record the deployed revision, queue/destination settings, scoped send policies,
alarm actions, confirmed subscription status and a received synthetic alarm.
Use a synthetic test pipeline to exercise both delivery rejection and a worker
that exhausts its configured retries. Do not break a production invocation policy
or publish private meeting content as a test. Confirm the expected queue receives
each shape, a missing destination send grant raises its own alarm, and restored
delivery completes a synthetic resource exactly once.

## Inspect one retained event

Install `boto3<2` in the operator environment. Use the intended AWS profile and
region. The replay tool derives the account from STS and uses only that account's
`ttobak-main` table, `ttobak-assets-ACCOUNT` bucket and allowlisted functions.
Its credentials require STS identity, exact-table `GetItem`, scoped S3 metadata
reads and, only for execution, `lambda:InvokeFunction` on the intended worker.
Queue inspection/deletion permissions are separate operator responsibilities.

Save exactly one `ReceiveMessage` message to a private file, including all
message attributes. It can contain sensitive filenames or provider error output:
do not paste its body in tickets, terminal logs or review comments.

```bash
umask 077
aws sqs receive-message --queue-url "$FAILURE_QUEUE_URL" \
  --max-number-of-messages 1 --visibility-timeout 300 \
  --message-attribute-names All --query 'Messages[0]' \
  > /tmp/ttobak-one-failure.json
python3 scripts/operations/replay_pipeline_failure.py \
  --message-file /tmp/ttobak-one-failure.json --kind delivery \
  --owner-id OWNER --meeting-id MEETING
```

Use `--kind execution` for the Lambda destination queue. Add `--attachment-id`
for image events or `--document-id` for personal slide conversion. The tool does
not support account-shared slide copies or unrelated queues. Output contains only
the decision, target function and a confirmation hash, never the event body.

The default is read-only. The tool validates account, region, exact delivery
rule/target or execution function, event age, canonical owner/resource identity,
current work state and source ETag/version where available. It uses strongly
consistent metadata reads and S3 HEAD, without reading transcript/note bodies.
Custom events without source versions additionally reject objects modified after
the event; the inspected current version is included in the confirmation hash.

Rejects are deliberate. Missing identities, rebound audio/files, changed source
objects, stale analysis run IDs, expired leases, completed parts and active
summary claims must be handled through the supported product recovery path.
Never edit a retained event to invent a newer run ID or clear a claim.

## Submit only a verified, quiescent replay

Inspect the retained error, resolve its cause and verify that no active Lambda,
ECS worker, user edit or retry can race the replay. The source metadata check is
not an atomic worker claim. In particular, ordinary audio task submission has
no operator replay lease. Pause competing recovery work and verify worker status
before using the required `--workers-quiesced` acknowledgement.

Re-run with the dry-run hash and the same identity arguments:

```bash
python3 scripts/operations/replay_pipeline_failure.py \
  --message-file /tmp/ttobak-one-failure.json --kind delivery \
  --owner-id OWNER --meeting-id MEETING \
  --execute --workers-quiesced --confirm-plan PLAN_HASH
```

The tool re-reads state before submitting the unchanged event. It does not mutate
DynamoDB, reset retry budgets, republish an `aws.s3` event through EventBridge,
delete the queue message or automatically retry an ambiguous invocation. It
invokes the allowlisted Lambda asynchronously once. `accepted_not_completed`
means only that Lambda accepted delivery. Verify canonical completion and
source identity before separately deleting the original queue message. If the
invoke response is lost, inspect current work instead of immediately repeating.
Remove the private local message file after the incident is resolved.

Terminal batch summaries use owner/editor POST `/api/meetings/{id}/resummary`;
replaying their old transcript event alone is a no-op. Expired/failed saved-summary
or action-item runs likewise need their authenticated retry APIs to create a new
run. The operator tool never manufactures that transition. See
[meeting summary recovery](meeting-document-release.md).

## Acceptance record

No live record is supplied by this source change. Complete these checks against
the deployed revision before claiming monitored failure recovery:

- Both failure shapes arrive in their intended queue after the original retries.
- The configured operator receives a synthetic alarm and can access only the
  required diagnostic resources.
- A synthetic dry run performs no writes; an approved replay completes once.
- A completed resource, changed source and stale run are rejected on replay.
- An ambiguous invocation is investigated without automatic duplicate submission.
- Queue messages and private diagnostic files follow the seven-day retention and
  incident cleanup policy; source content does not enter notification bodies.
