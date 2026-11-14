# Customer update agent runtime

One-time owner setup, smoke test, diagnosis and rollback for the customer update
agent: the AgentCore Runtime container in
[`backend/python/customer-update-agent`](../../backend/python/customer-update-agent/README.md).
[ADR-011](../decisions/ADR-011-interactive-deep-research.md) records its data,
egress and accepted limits. Nothing invokes the runtime until the Go worker that
calls it ships; completing these steps does not establish consumer acceptance.

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
   redeploy. Check the role and its inline policy in IAM.
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
   (`arn:aws:bedrock-agentcore:<region>:<account>:runtime/<id>`) for the Go
   worker plan; its invoke permission is granted there, not here.

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
