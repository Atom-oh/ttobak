# Customer update agent runtime

One-time owner setup, smoke test, diagnosis and rollback for the customer update
agent: the AgentCore Runtime container in
[`backend/python/customer-update-agent`](../../backend/python/customer-update-agent/README.md).
[ADR-011](../decisions/ADR-011-interactive-deep-research.md) records its data,
egress and accepted limits; [ADR-030](../decisions/ADR-030-customer-update-drafts.md)
records the Go worker's run phases. The worker invokes the runtime only after its
ARN is configured ([switching the run phases on](#switching-the-run-phases-on));
completing these steps does not establish consumer acceptance.

The runtime and its ECR repository are created by hand. CDK creates only the
execution role `ttobak-agentcore-customer-update-role` (`CustomerUpdates` in
TtobakCrawlerStack). The deploy workflow updates the image and environment of an
existing runtime. While any of its repository variables is unset, a run emits a
"setup is incomplete" notice and skips every later step, so pushes to `main`
before this setup show a green, skipped run that builds, pushes and deploys
nothing. A `CUSTOMER_UPDATE_ECR_REPO` other than `ttobak-customer-update-agent`
fails the run.

## One-time setup

1. **Role.** Deploy TtobakCrawlerStack alone:
   `npx cdk deploy TtobakCrawlerStack --exclusively`. Never `--all`. The role's
   KMS statement reuses AiStack's existing key-ID export, so AiStack needs no
   redeploy. Check the role and its inline policy in IAM. The same deploy gives
   the Go worker its run grants with the phases still off.
2. **Repository.** Create the private ECR repository
   `ttobak-customer-update-agent` in the application Region (the role's pull
   grant names exactly this repository), with tag immutability off (the
   workflow pushes `latest` and an 8-character commit tag).
3. **First image.** The runtime needs an image before it exists, and the
   workflow needs the runtime ID. On an ARM64 host with Docker, from the
   repository root:
   `docker build -f backend/python/customer-update-agent/Dockerfile -t <repo-uri>:initial backend/python`,
   log in to ECR and push `<repo-uri>:initial`.
4. **Runtime.** Create the runtime the way the research runtime is configured:
   `aws bedrock-agentcore-control create-agent-runtime` with
   `--agent-runtime-name ttobakCustomerUpdate`, the `initial` image as
   `containerConfiguration.containerUri`, `--role-arn` of
   `ttobak-agentcore-customer-update-role`, `--network-configuration
   '{"networkMode":"PUBLIC"}'`, the default HTTP protocol and IAM (SigV4)
   inbound authorization, and environment variables `TABLE_NAME=ttobak-main`,
   `KMS_KEY_ID` (AiStack output `KmsKeyId`), `WEB_SEARCH_GATEWAY_URL` (same value
   as the research runtime) and `WEB_SEARCH_GATEWAY_REGION=us-east-1`. Do not add
   a public endpoint, function URL or resource policy with `Principal: "*"`.
   Wait for `READY`.
5. **Variables.** Set GitHub repository variables `CUSTOMER_UPDATE_ECR_REPO`
   (`ttobak-customer-update-agent`) and `CUSTOMER_UPDATE_RUNTIME_ID` (the runtime
   ID, not the ARN). Optional `CUSTOMER_UPDATE_ORCHESTRATOR_MODEL_ID` overrides
   the orchestrator model; leave it unset for luna. Optional runtime variable
   `RUN_DEADLINE_SECONDS` (default 1500) bounds one run; the workflow does not
   set it.
6. **Deploy.** Run `deploy-customer-update-agent.yml` with `workflow_dispatch`.
   It runs the unit tests, resolves `KMS_KEY_ID` from TtobakAiStack, builds,
   pushes and calls `update-agent-runtime`. The workflow replaces the whole
   environment on every run, so set values through it, not in the console.
7. **Record.** Record the runtime ARN
   (`arn:aws:bedrock-agentcore:<region>:<account>:runtime/<id>`); the next
   section gives it to the Go worker.

## Switching the run phases on

The worker's run phases are off while CDK context `customerUpdateRuntimeArn`
is unset: the worker gets no `CUSTOMER_UPDATE_RUNTIME_ARN` and no
`InvokeAgentRuntime` grant, and fan-out continues. All other run grants and
`KMS_KEY_ID` are already deployed. The deploy workflow passes no `-c` values;
it synthesizes with `infra/cdk.json`.

1. **Billing first.** Deploy the Billing stack that owns the worker writing the
   names-only object (`TtobakBillingStack`, through its release runbook) with
   `--exclusively`, and confirm after one publish that
   `accounts/{accountId}/derived/services-in-use.json` exists and holds names
   only. `BILLING_DATA_BUCKET` is resolved from
   `/ttobak/billing/data-bucket-name` only when `ttobak:billingEnabled` is
   `"true"`; before the Billing cutover that parameter names the companion
   bucket, which has no services object, so drafts simply get no service
   signal. After repointing it, redeploy TtobakCrawlerStack with `--force`.
2. **Set the ARN.** Add `"customerUpdateRuntimeArn": "<runtime ARN>"` to the
   `context` of `infra/cdk.json` in a reviewed PR (the CI deploy uses it), or
   pass `-c customerUpdateRuntimeArn=<runtime ARN>` for a manual deploy. Synth
   fails on anything but an exact runtime ARN.
3. **Deploy** `npx cdk deploy TtobakCrawlerStack --exclusively`. Never `--all`.
   Check the worker's environment (`CUSTOMER_UPDATE_RUNTIME_ARN`,
   `CUSTOMER_UPDATE_ENDPOINT_NAME=DEFAULT`, `KMS_KEY_ID`) and that its role's
   invoke statement names exactly the ARN and `ARN/*`.
4. **Post-deploy check.** Let one tick run for a user who has a team-shared
   meeting on an in-scope account (this exercises the GSI3 owner lookup and the
   GSI1 account query), then search the worker log group
   `/aws/lambda/ttobak-customer-updates` for `AccessDenied`. Any hit means a
   worker role statement does not match a query; see the checklist below.
5. **Invoke answers.** It is unverified whether AgentCore returns the
   container's 400/409/503 as the response status or as a runtime client
   error. On the first runs, check the start log lines: if `BUSY` or a
   contract code appears as an AWS error code (`startFailed`) rather than as
   `not_accepted:<code>`, the contract-rejection attempt never fires.

**First day after switch-on.** Only items first seen within the last 3 days are
processed; older inbox items are skipped. Each run sends at most 30 items and
10 researched pairs; a user pauses for the KST day at 20 drafts or at the
research cap (30 researched pairs by default).

To switch off, remove the context value and redeploy the same way. With the
ARN removed nothing processes leases: a lease row stays until its TTL (7 days
after lease end), in-flight run rows expire after 2 days and their results are
lost. Nothing blocks fan-out.

**Meeting notes.** Packs include notes of the two newest readable account
meetings while `CUSTOMER_UPDATE_INCLUDE_NOTES=1` (the default). Set context
`customerUpdateIncludeNotes` to `false` and redeploy TtobakCrawlerStack to send
`0` and leave notes out; no code change is needed.

**Budgets and holds.** Per user and KST day: at most 20 drafts, 3 per account,
and `CUSTOMER_UPDATE_RESEARCH_PAIRS_PER_DAY` researched pairs (decisions with
outcome drafted or discarded; CDK sets 30; owner-tunable; 0 starts no runs).

- Not started, not held: the user already has 20 drafts today at start; the
  items are rescanned next tick.
- Held until 00:00 KST, attempts unchanged: a Go `day_budget` or
  `account_day_budget` deferral, a runtime `account_budget` or `draft_budget`
  deferral, all in-scope accounts capped, or the research cap reached.
- Held one hour, attempts unchanged: any in-scope account's scope or pack
  read failed (`buildFailedHeld`); nothing starts for that user this tick. A
  count that repeats every hour points to one broken account.
- Left new, attempts unchanged: a runtime `research_budget` deferral. The
  next run sends the item with `decidedAccounts`, so pairs already decided
  are not researched again.
- One more attempt: `gate_error`, a missing `ITEM` row, lease expiry, or a
  contract rejection of the payload (HTTP 400/413). The third attempt skips
  the item.

A hold wins over "left new", which wins over an attempt.

**No drafts appear.** Check in order, from the worker log line
`customer update runs: ...` (counts only):

- Every count 0: the runtime ARN is unset (the phases do nothing), or no user
  has a pending mark (no new inbox item).
- `started=0` with users: a run is still leased, every item is held (`held`),
  or no account is in scope (direct membership, the account setting, and a
  project with an SFDC opportunity id and an open stage).
- `errors`: a user's run failed this tick (logged with hashed ids and an error
  class); it is retried next tick.
- `startFailed`: a definite failure, logged with its error code: a runtime
  refusal, or an AWS API error such as a missing invoke grant, a wrong
  qualifier, a validation error or throttling. The lease is deleted and items
  keep their attempts (only a contract rejection adds one). A run id is never
  retried; the next tick uses a new one.
- `startAmbiguous`: no service response (timeout, transport error) or an
  unreadable answer; the lease is kept, so a started run is finalized and one
  that never started expires after 30 minutes with one more attempt.
- `errors` together with a run that has a `DONE` row: finalize keeps failing
  for that run; its rows expire after 2 days.
- `servicesUnavailable`: the services object read was denied (normal while it
  is missing and the role has no `ListBucket`); drafts get no service signal.
- `finalized` with `discarded`: Go rejected drafts; reasons are codes only.
- `deferred`/`held`: day budgets; wait for the next KST day.
- `unreadable`/`malformed`: KMS or row format; check the record key and the
  worker's purpose conditions.
- AccessDenied on the table or an index in the worker log: compare the failing
  call with the worker role's statements (INFRA-SPEC "Customer update worker
  run phases"): its leading-key prefix, the index ARN, and for GSI1/GSI3 the
  allowed attribute list, which must name the table keys, the index keys and
  every projected attribute.
- A runtime-side failure leaves no `DONE` row; see the next sections.

## Smoke test

1. Take `example` from
   [`contracts/customer-updates/run-v1.json`](../../contracts/customer-updates/run-v1.json).
   Give it a fresh 32-hex `runId`. For a real read, replace the item `url` with
   a public AWS announcement and `urlHash` with the hash
   `model.NormalizeUpdateURL` computes for it, so the `CUURL#` cache key is right.
2. Invoke: `aws bedrock-agentcore invoke-agent-runtime --agent-runtime-arn <arn>
   --qualifier DEFAULT --runtime-session-id <33+ characters> --payload
   fileb://payload.json out.json`. Expect `{"accepted": true, "runId": ...}`.
   A 400 names the contract field code; 503 `UNAVAILABLE` means `TABLE_NAME`
   or `KMS_KEY_ID` is missing or the claim write failed; 409 `BUSY` means that
   session already runs a run; 409 `RUN_EXISTS` means the `runId` was already
   used (a `runId` is single-use: take a fresh one).
3. Within the run (minutes), expect rows under `PK = CURUN#{runId}`: `CLAIM`
   first, one `ITEM#{urlHash}` per item, `DEC#...`/`DRAFT#...` rows when the
   gate chose a customer, and a final `DONE` row with `finishedAt` and `stats`.
   Sealed attributes are opaque; the runtime role cannot decrypt them. An item
   `deferred` with reason `gate_error` means a gate model call failed; it is
   retried in a later run.
4. This is where luna tool calling through Strands is confirmed. In the
   runtime log (`/aws/bedrock-agentcore/runtimes/<id>-DEFAULT`), an
   `orchestrator failed run=... type=...` warning means the orchestrator agent
   could not call its tools. Results are still complete because the
   deterministic sweep finishes every item. To change the orchestrator model,
   set `CUSTOMER_UPDATE_ORCHESTRATOR_MODEL_ID` and rerun the workflow:
   `global.anthropic.claude-sonnet-5-5` works with the current role;
   `global.anthropic.claude-haiku-5-5` first needs that profile added to the
   role's Bedrock statement in `infra/lib/customer-updates.ts` and a
   TtobakCrawlerStack deploy.

## A run left no DONE row

`DONE` is written last and only after every other row succeeded, so a missing
`DONE` means the run failed or is still running. Logs carry `runId`, codes,
counts and exception class names only.

- `run failed run=<id> type=<class>` in the runtime log: `KeyError` points to
  missing environment; `ClientError` to DynamoDB or KMS permissions (check the
  key ID and that the role's `kms:GenerateDataKey` purpose condition matches
  `ttobak-customer-update-run`); `SealingError` to the KMS response;
  `RunDeadline` to a run past `RUN_DEADLINE_SECONDS` (no results are written).
- `orchestrator stopped run=<id> code=tool_limit|deadline` is a normal end of
  the agent phase; the sweep still finishes the run.
- No log line for the `runId`: the invocation never started a run (check the
  invoke response code) or the session was recycled before the run ended.
- `/ping` reports `HealthyBusy` while a run is alive; a session idle past its
  timeout is stopped by AgentCore, which ends any run without `DONE`.
- Partial rows carry `pendingShareExpiresAt` (two days) and expire through the
  table TTL; the consumer ignores a run without `DONE`. Rerun with a new
  `runId`; the old one is refused.

## Rollback

Each workflow run pushes an 8-character commit tag. Roll back by calling
`aws bedrock-agentcore-control update-agent-runtime` with the previous tag's
`containerUri`, the same role ARN, `PUBLIC` network mode and the same
environment variables (the call replaces all of them), or rerun the workflow
from the previous commit. Removing the role means removing the agent runtime
props from TtobakCrawlerStack and deploying it `--exclusively` after the
runtime is deleted.
