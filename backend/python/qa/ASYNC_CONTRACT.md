# Asynchronous REST QA

FrontendStack remains opt-in: omitted/false `qaAsyncJobsEnabled` emits false.
The app explicitly selects true after the recorded
[backend acceptance](../../../docs/runbooks/qa-current-source-rollout.md#async-ui-opt-in--2026-09-13).
Runtime `qaAsyncJobs` must be exactly true to select jobs; missing/false config
uses sync before any job is submitted. Browser activation proof is still pending.
Initial installations must verify deployed routes, queue/IAM and current-source
job results before enabling the app opt-in.
GatewayStack explicitly deploys the QA consumer before the Go API's private-link
capability; stack ordering alone does not order sibling Lambda updates.
Reload clients after activation; no fallback occurs after submission.
Frontend wrappers still return `QAResponse`.

JWT-protected `POST /api/qa/jobs` accepts `requestId`, `mode` (`ask`/`meeting`),
`question`, optional `context`, `meetingId`, `sessionId`. Meeting mode uses saved
context. IDs are `{13-digit-epoch-ms}-{32-lowercase-hex}`, initially within five
minutes past/one minute future. User/ID binds normalized input: reuse returns
the same ticket, different input returns 409. `GET /api/qa/jobs/{jobId}` returns
status and successful `result` or failed `error`; foreign/missing=404, expired=410.
Frontend keeps one ID through at most two submissions and a 660-second polling
window. Polling normally pauses one second; server hints are clamped to 1–5
seconds. Each fetch has a 20-second abort limit. The user stays pinned through
refresh. `QAJobError` exposes the ID in both its field and displayed message;
reload does not resume polling.

Chat bounds WS answers at 330s for the shared 300s Lambda and REST at 690s for
the 660s API polling budget. Live QA retains its progress-rearmed idle watchdog.
HTTP proactive claims are reserved before dispatch, not after acknowledgement.
They stay claimed through network failure/unmount and later batches because a
failed request does not prove the backend received nothing. Assigned job IDs are
retained and shown on errors. Claims reset on a new recording/auth scope, not on
uncertain completion. Accepted WS sends are also retained. No automatic resend.
After a submitted HTTP failure, Live QA keeps the job ID/claim but gives the next
manual turn a fresh, random session ID. A refresh failure can end polling before
server work stops; isolating later history does not cancel or resubmit that job.
Meeting QAPanel also discards its session reference on every HTTP/empty-answer
error in sync and async modes. It keeps the visible conversation and draft; the
next explicit turn starts a random session. Successful turns retain continuity.
Meeting changes/unmounts discard the old panel and ignore its late responses.

Exact-queue SQS pointers contain only version/user/job IDs. Conditional claims
never take over RUNNING. Queued dispatch can recover through polling, with 10s
cooldown. Limits: work 240s, total 600s, Lambda 300s, HTTP 15s. Model SDK retries
are disabled. Confirmed research receipts are reused; ambiguous creation blocks
further creation. Never automatically resubmit uncertain work under a new ID.
Async model responses require a nonblank completed answer. Truncation or tool
budget exhaustion returns `QA_MODEL_INCOMPLETE`; completed, validated tool
receipts remain in history with an interruption notice instead of an empty success.

QA Converse and ConverseStream share the fixed
`completion_diagnostics.QA_OUTPUT_TOKENS=8192` ceiling per model call. Async
execution delegates to the same Converse path; the separate question detector
keeps its own 512-token budget. Existing WS/async completion rejection, source
checks, tool-round limits, deadlines and no-automatic-continuation behavior remain.
The larger ceiling can increase latency/cost and still hit completion or encoded
response-size limits. It does not fix the legacy synchronous HTTP timeout,
change legacy response behavior, or activate async UI.

Completion failures emit one `QA completion diagnostic` JSON record per failed
model round, bounded to 1024 bytes by its fixed schema. It contains a closed
failure/stop category, round and configured ceiling, message-stop/metadata
presence, bounded event/block/text-character/tool counts, open-block type and
allowlisted integer token usage. Unknown stop values become `other`; missing or
invalid usage is null. Queries, source/content text, raw events, identifiers,
arbitrary metadata and exception payloads never enter this record.

The 2026-09-13 WS incident logged `MODEL_STREAM_INCOMPLETE`. The host observed a
4096 maximum in a minute-level output-token metric spanning four samples, not
an individual stop reason. Budget exhaustion is therefore a strong inference,
not proven cause; open blocks, missing stop events and invalid tool completion
remain distinguishable alternatives. A host-run synthetic probe accepted 8192
and returned `end_turn` with four output tokens. This proves parameter acceptance
only, not real-meeting completion or semantic quality.

`JobDeadline` uses a separate two-second cleanup window. The loop checkpoints
only returned, tracked tool-use/result pairs with at most 128 KiB of history,
dependency/detail metadata and delivery proof combined. It omits pending calls
and the current tool round's unconfirmed assistant text. Invalid/oversized new
checkpoints leave the earlier valid checkpoint intact.

Deadline cleanup revalidates current sources before one history-save attempt;
only an acknowledged message write counts as saved. It never invokes a model,
replays a mutation, marks an uncertain tool successful, or publishes a successful
job. Source failure, cleanup timeout and unacknowledged writes cannot promise
continuity. The original deadline still records `FAILED/QA_JOB_INTERRUPTED`.
This is bounded best-effort recovery, not a transaction with tool execution:
a hard process termination or deadline before checkpoint capture can still lose
a receipt. Clients must not automatically resubmit uncertain work.

Separate `USER#{user}` items hold `QA_JOB#{id}` input (256 KiB), `QA_RESULT#{id}`
answer (320 KiB), and `QA_PROOF#{id}` proof (128 KiB). Complete items include all
keys/metadata in the 400-KiB check; control reserves 2048 bytes for all future
allowlisted fields. UTF-8 sizing follows [AWS guidance](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/CapacityUnitCalculations.html),
with 21 bytes reserved per integer. Each row enforces one-hour expiry through
active `pendingShareExpiresAt`. Bound artifacts precede conditional success;
lost acknowledgements never regenerate answers. Oversize fails without truncation.
The checked-in table's AWS-owned KMS default is unchanged; active TTL is added
only for these job rows, not claimed for legacy conversation rows.

Delivery captures reads before history limits: up to 512 dependencies/128 KiB,
and 4-MiB/65,536-node read-only fingerprints of effective public results. Existing
history overflow still retains current results with nonreplayable coverage.
Validate before later model rounds, persistence/publication, and both before and
after GET loads the answer. Only strict readonly callbacks/scoped empty searches
rerun; mutations never replay. Untracked/conflicting reads fail. Source changes
use `SOURCE_CHANGED`, unavailable proof uses `SOURCE_UNAVAILABLE`. Slow validation
may return 503: retry GET, not execution. Checks are point-in-time; serialize
same-session turns. Public acceptance remains separate.
