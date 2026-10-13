# Pipeline failure retention and controlled replay

These are checked-in controls, not deployed acceptance. Record deployment,
notification delivery and synthetic failure/recovery separately.

## What is retained

GatewayStack owns two seven-day queues with Amazon SQS-managed server-side
encryption (SSE-SQS), Transport Layer Security (TLS), RETAIN on stack removal
and no consumer or automatic redrive.

Fixed names can collide after stack deletion or rollback. Inventory ownership
before recreation; preserve messages and review import or a new-name migration,
monitoring both queues until recovery finishes. Never purge/delete a retained
queue or change RETAIN to unblock deployment.

| Queue | Record | Coverage |
|---|---|---|
| `ttobak-pipeline-delivery-failures` | EventBridge event and `RULE_ARN`/`TARGET_ARN` attributes | Audio/crop, image, transcript/all-parts, optional slide conversion |
| `ttobak-pipeline-execution-failures` | Lambda `requestPayload`, execution context and error output | Transcribe, summarize, process-image, optional convert-doc |

EventBridge retry defaults remain unchanged. Lambda keeps two async retries and
six-hour maximum event age; exhausted failures reach the execution queue.
Other delivery/retry controls remain separate. Action-item/saved-summary executions
also use the summarize destination. QA, canonical-index, action-item and
saved-summary dead-letter queues (DLQs) gain backlog/age alarms.

Image model/metadata/publication failures use closed error categories without
classification or output. Failure preserves prior content; publication requires
the same unfinished attachment. Redelivery ignores completed/deleted/changed rows.

Lambda success creates no destination record even after a recorded application
error. ECS submission does not prove transcription: later GPU/Spot failures need
meeting/device recovery and investigation. Check canonical state as well as metrics/queues.

## Alarm routing and release acceptance

Alarms cover backlog/age, Lambda errors/throttles/async drops/destination loss and
core EventBridge delivery failures. They use one breaching sample in two one-minute
periods and `notBreaching` missing data; backlog threshold is one message and age
is fifteen minutes. Tune against measured traffic. Delivery loss uses the exact
nonzero-only `AWS/Events` metric `InvocationsFailedToBeSentToDlq`, scoped by `RuleName`.

GatewayStack's optional `PipelineAlarmTopicArn` parameter defaults empty: alarms
have no actions and `PipelineAlarmRouting` reports `UNCONFIGURED`. Configure an
existing standard Amazon Simple Notification Service (SNS) topic with reviewed
regional CloudWatch publish permission and a confirmed operator subscription.
This change creates no topic/subscription and sends no notifications.

After reviewing the exact synth/diff, deploy changed stacks individually with
`--exclusively`; never deploy KnowledgeStack incidentally. Example parameter:

```bash
npx cdk deploy TtobakGatewayStack --exclusively \
  --parameters TtobakGatewayStack:PipelineAlarmTopicArn=arn:aws:sns:ap-northeast-2:ACCOUNT:APPROVED_TOPIC
```

Complete the acceptance record below in a synthetic pipeline. Never break a
production invocation policy or publish private meeting content for testing.

## Inspect one retained event

Install `boto3<2`; use the intended AWS profile/region. AWS Security Token Service
(STS) determines the account: the tool uses its `ttobak-main`,
`ttobak-assets-ACCOUNT` and allowlisted functions.
Grant STS identity, exact-table `GetItem`, scoped S3 HEAD and, for execution,
`lambda:InvokeFunction` on the intended worker. Queue inspection/deletion is separate.

Save one `ReceiveMessage` message with all attributes to a private file. Its
sensitive filenames/provider errors must not enter tickets, logs or review comments.

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

Use `--kind execution` for destinations, `--attachment-id` for images and
`--document-id` for personal slides. Shared copies/unrelated queues are unsupported.
Output is decision/target/confirmation hash, never the body.

Default operation is read-only: validate account/region, exact rule/target or
function, event age, canonical owner/resource, work state and available ETag/version.
Strongly consistent metadata reads and S3 HEAD never read transcript/note bodies.
Custom events reject sources modified after the event; inspected versions enter
the confirmation hash.

RFC3339Nano parsing retains nanoseconds on Python 3.9/3.12. Whole-second events
represent a one-second creation/emission interval; next-second creation rejects
and fractional times retain exact ordering. All-parts replay requires the last
meeting update to precede its canonical emission marker.

S3 audio events precede completion binding. Later meeting updates require the exact
bound key, unchanged ETag and matching original non-null S3 version. Missing versions,
completed parts and rebindings reject. Multipart transcript replay checks its own
readiness bit and current part audio's modification time, rejecting newer/unknown
generations while permitting sibling progress. Other transcript/crop freshness
checks remain strict. Personal documents can be created after S3 delivery; current
document/file-key and object identity bind their replay.

Image/all-parts events lack complete original revisions: source checks and worker
publication guards do not establish missing subsecond provenance.

Use product recovery for missing identities, rebound/changed sources, stale runs,
expired leases, completed parts or active summary claims. Never edit retained
events to invent run IDs or clear claims.

## Submit only a verified, quiescent replay

Resolve the error and verify no Lambda, ECS worker, edit or retry can race replay.
Metadata checks are not atomic claims; ordinary audio submission has no replay
lease. Pause competing recovery and verify workers before acknowledging
`--workers-quiesced`.

Re-run with the dry-run hash and the same identity arguments:

```bash
python3 scripts/operations/replay_pipeline_failure.py \
  --message-file /tmp/ttobak-one-failure.json --kind delivery \
  --owner-id OWNER --meeting-id MEETING \
  --execute --workers-quiesced --confirm-plan PLAN_HASH
```

The tool re-reads state and asynchronously submits the unchanged event once.
It never mutates DynamoDB, resets budgets, republishes through EventBridge or
deletes messages. `accepted_not_completed` means accepted delivery only.
Verify canonical completion/source before separate deletion; investigate lost
responses before retrying. Remove the private file after resolution.

Terminal summaries use owner/editor POST `/api/meetings/{id}/resummary`; old transcript
replay alone is a no-op. Expired/failed saved-summary and action-item runs need their
authenticated retry APIs to create a run; the tool never creates one. See
[meeting summary recovery](meeting-document-release.md).

## Acceptance record

No live acceptance is supplied. Record the deployed revision and:

- Queue/destination settings, scoped send policies, alarm actions and confirmed
  subscription; the operator receives a synthetic alarm with diagnostic-only access.
- Both failure shapes arrive after original retries; removing a destination send
  grant in the synthetic pipeline triggers its own alarm.
- Dry run performs no writes; restored delivery and approved replay complete once.
- A completed resource, changed source and stale run are rejected on replay.
- An ambiguous invocation is investigated without automatic duplicate submission.
- Queue messages and private diagnostic files follow the seven-day retention and
  incident cleanup policy; source content does not enter notification bodies.
