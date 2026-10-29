# Billing Phase 2 (Agent Stage on AgentCore Runtime, Worker in TTOBAK) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

> Historical plan record. Status: Draft rev 2, 2026-10-28 (revised after codex
> review a2); not started. This is implementation intent, not proof of delivery or
> deployment.
>
> **Prerequisite:** the Billing Phase 1 release is accepted. Phase 1 Task 10's
> acceptance is recorded in `docs/runbooks/billing-phase1-release.md` and
> `TtobakBillingStack` is deployed from the Phase 1 code (Web Function, dedicated
> code bucket, `deploy-billing.yml`). No task below starts before that. Phase 2
> delivers a validated **standby** replacement; production job delivery moves only
> in Phase 4.

**Goal:** Move the analysis worker into the TTOBAK repo as `services/billing/worker`
and run its agent stage (area agents, synthesis, verifier, FSI peer-case search,
report, publish) on Bedrock AgentCore Runtime. Ingest stays an ECS Fargate
RunTask. Everything is built in `TtobakBillingStack`; its production job rules are
created **disabled**. The companion stack keeps sole ownership of `jobs/*`
until Phase 4.

**Architecture:**
- `jobs/u*.json` (analyze) → `IngestJobRule` (disabled) → Fargate ingest in the
  fsi-demo-vpc `Isolated` subnet group. Ingest:
  - claims the upload, runs the COT/CUR pipeline and stages the version under
    `accounts/{acct}/versions/{v}/`;
  - sets `steps.agents = running` and calls `InvokeAgentRuntime` through the
    existing `bedrock-agentcore` interface endpoint (owned by `FsiDemoNetworkStack`);
  - on definitive acceptance it exits and keeps the lock;
  - an ambiguous result is resolved through the durable run item (below), never by
    failing and unlocking blindly.
- `jobs/r*.json` (rerun-agents) → `RerunJobRule` (disabled) → a non-VPC dispatcher
  Lambda → `InvokeAgentRuntime`. No ingest task starts.
- Runtime contract:
  - One runtime session is bound to exactly one job. The session ID is derived
    from `jobKey` and claim.
  - The payload is `{jobKey, claim}`. The runtime re-reads the job object and validates it.
  - For analyze it checks the claim on `UP#`, then takes **durable execution
    ownership**: a conditional `ACCT#{acct}` / `RUN#{jobId}` item with a random
    owner token. Only after that may it write to S3.
  - It runs `ai_step.py agents` and `report` as children in a per-job process
    scope, uploads outputs and, for analyze, runs the companion META + UP publish
    transaction with the **supplied** claim token, then releases `LOCK#`.
- FSI search goes straight from the runtime to the TTOBAK web-search gateway
  (us-east-1, SigV4); `agents.safe_query` stays.
- Phase 2 acceptance runs through a reserved **acceptance prefix**
  (`billing-acceptance/`) that the companion rule pattern (`jobs/`) cannot match.
  Two acceptance-only rules exist only while the acceptance context is set, and they
  accept only the reserved synthetic Account.
- Platform-table contract unchanged for the Phase 1 BFF and upload page:
  - `UP#` `status`, `steps.<name>`, `claim`, `claimedAt`, `ai`, `versionId`,
    `period`, `published`;
  - META `current_<src>`, `period_<src>`, `ai_<src>`.
  - The new `RUN#` rows are invisible to the BFF, which queries
    `begins_with(sk, 'UP#')` (`services/billing/bff/lib/store.js:146`) and reads
    `META`/`LOCK#` by key.

**Tech Stack:** Python 3.12 (stdlib `unittest`, boto3 1.42.97, duckdb 1.4.5,
FastAPI + uvicorn pinned), AWS CDK 2.273.0 (`aws-bedrockagentcore` **L1
`CfnRuntime`**, `aws-ecr-assets`, `aws-ecs`, `aws-events`, `aws-lambda`), Docker
linux/arm64 on the `ttobak-arm` runner.

**Spec:**
- [Billing service design](../specs/2026-10-26-billing-service-design.md) §4.3, §8, §9, §10
- [ADR-056](../../decisions/ADR-056-billing-service-origin-and-identity.md)
- [Billing Phase 1 plan](2026-10-27-billing-phase1-bff.md) (Task 8 stack, rulings K1–K4, A8b)

## Global Constraints

- No new VPC, subnet, route or VPC endpoint.
  - The ingest task uses `Vpc.fromLookup({ tags: { Name: 'fsi-demo-vpc' } })` and
    `subnetGroupName: 'Isolated'`. Never select by `SubnetType` alone: the VPC also
    holds `bank-*` isolated groups.
  - No IDs in code.
- Ingest SG: no ingress; egress 443 only to the VPC CIDR and the S3/DynamoDB
  gateway prefix lists; `assignPublicIp: false`.
- Agent runtime: public network mode (spec D2, no VPC), IAM inbound auth, HTTP
  protocol, ARM64.
- **Exactly one owner of every job key at all times:**
  - `IngestJobRule` (`jobs/u`) and `RerunJobRule` (`jobs/r`) are `enabled: false`
    unless `ttobak:billingJobsEnabled === 'true'`. Phase 2 never sets that key.
  - Acceptance rules match only `billing-acceptance/u` and `billing-acceptance/r`.
  - The companion rule's pattern and state are never touched by this plan.
- IAM:
  - The execution role is authored explicitly; the Runtime L2 is not used, because
    it adds X-Ray `Resource: "*"` grants.
  - No `Resource: "*"` without a condition, except the documented exceptions in
    Task 7 (`no_unconditioned_resource_star`).
- Data bucket and platform table are referenced by name only; never created,
  imported, modified or deleted. The bucket already emits EventBridge events.
- Account-scoped data access as today:
  - The ingest task role and the runtime role hold no `accounts/*` permission (only
    `jobs/*` and acceptance-prefix reads).
  - Each assumes its own data role with `session_policy(bucket, acct)`.
  - The platform-table `ACCT#*` LeadingKeys condition is a service-wide partition
    bound, not tenant authorization. Tenant isolation comes from the session policy
    and from every write naming the job's own `ACCT#{acct}`.
- Bedrock: `global.anthropic.claude-sonnet-5-5`, Converse, no sampling parameters,
  `toolChoice: auto`; companion model ARNs only.
- FSI search: only `safe_query`-approved queries are sent, they never name the
  customer, and they are never logged. Requests are `https://` only and redirects
  are refused.
- Code only moves: tracked files from `dd540f5` via `git archive`. No customer
  data, names, datasets, reports or notes. Customer-name regexes in companion tests
  are removed, not imported.
- English markdown; `docs/superpowers` files carry "historical" in the first 1000
  characters.
- Deploy only via `deploy-billing.yml` or
  `npx cdk deploy TtobakBillingStack --exclusively`; never `--all`. Context is
  checked-in `infra/cdk.json` strings; no CloudFormation exports.
- Every job on the self-hosted runner carries the fork guard
  `if: github.event_name != 'pull_request' || github.event.pull_request.head.repo.full_name == github.repository`.
- Logs carry step names, states, reason kinds, exception class names and counts
  only. No job keys, Account IDs, queries or reason text.

## Review Focus

1. **Two owners of one job.**
   - Default context gives `State: DISABLED` on both production rules.
   - Acceptance rules exist only with acceptance context, and their prefix can
     never start with `jobs/`.
   - Tests in Task 7: `job_rules_disabled_without_context`,
     `acceptance_prefix_cannot_overlap_companion`.
2. **The same job runs twice across sessions, retries or replays.** The `RUN#`
   conditional put admits one owner, and no S3 write precedes it. Tests in Task 4:
   `test_simultaneous_sessions_same_job_one_runs`, `test_delayed_retry_after_start_no_writes`,
   `test_replay_after_session_end_no_writes`.
3. **An ambiguous handoff aborts accepted work.** Only a definitive rejection fails
   and unlocks, and a rejection that follows a timed-out, disconnected or 5xx attempt
   is not definitive. An uncertain result is resolved by the `RUN#` item or by fencing it.
   Tests in Task 3: `test_ambiguous_invoke_with_run_item_hands_off`,
   `test_ambiguous_invoke_fenced_then_fails`,
   `test_ambiguous_then_rejected_resolves_through_run_item`; Task 6:
   `test_rejected_after_lost_acceptance_hands_off`.
4. **The runtime reads another Account's data.** Checks:
   - the session policy (Task 4, `test_runtime_data_session_is_account_scoped`);
   - a live denial canary on every job (Task 4, `test_cross_account_list_canary_fail_closed`);
   - the role scope (Task 7, `agent_data_role_versions_only`);
   - live Get/List/Put denials on the actual assumed session (Task 9, `selfcheck`).
5. **Write guards, stated accurately.**
   - Analyze UP/META writes are conditioned on `claim = :tok AND status = running`
     with the supplied token.
   - Rerun keeps companion semantics: UP `ai` needs only `attribute_exists(pk)`, and
     META `ai_<src>` needs `current_<src> = :v`. It is now once-only per job key via `RUN#`.
   - Tests in Task 4: `test_forged_claim_writes_nothing`,
     `test_rerun_without_up_row_still_updates_meta`.

## Pre-flight record (checked 2026-10-28)

AWS documentation:
- HTTP contract: `0.0.0.0:8080`, ARM64, `POST /invocations`, `GET /ping`. <https://docs.aws.amazon.com/bedrock-agentcore/latest/devguide/runtime-http-protocol-contract.html>
- `/ping` returns `Healthy` or `HealthyBusy`; the entrypoint must not block. <https://docs.aws.amazon.com/bedrock-agentcore/latest/devguide/runtime-long-run.html>
- Lifecycle: `idleRuntimeSessionTimeout` and `maxLifetime` 60–28800 s (microVMs),
  defaults 900 s / 28800 s, idle ≤ max. After the max lifetime the session itself
  can continue on a new instance. <https://docs.aws.amazon.com/bedrock-agentcore/latest/devguide/runtime-lifecycle-settings.html>
- Quotas: synchronous request 15 min, payload 100 MB, async job 8 h, persistent
  session storage 1 GB. The idle timeout terminates the environment, and a new
  one is created for the same session. <https://docs.aws.amazon.com/bedrock-agentcore/latest/devguide/bedrock-agentcore-limits.html>
- `InvokeAgentRuntime`: action `bedrock-agentcore:InvokeAgentRuntime`;
  `runtimeSessionId` 33–256 characters; `qualifier` names an endpoint (default
  `DEFAULT`). <https://docs.aws.amazon.com/bedrock-agentcore/latest/APIReference/API_InvokeAgentRuntime.html>
- PrivateLink data plane `com.amazonaws.<region>.bedrock-agentcore` supports Runtime. <https://docs.aws.amazon.com/bedrock-agentcore/latest/devguide/vpc-interface-endpoints.html>

Local code (aws-cdk-lib 2.273.0 in `infra/node_modules`):
- L1 `CfnRuntime{agentRuntimeName, agentRuntimeArtifact{containerConfiguration{containerUri}}, roleArn, networkConfiguration, protocolConfiguration, environmentVariables, lifecycleConfiguration{idleRuntimeSessionTimeout, maxLifetime}, authorizerConfiguration?, ...}`;
  attributes `attrAgentRuntimeArn` and `attrAgentRuntimeId`.
  - It has no `loggingConfigs`; application logs go to
    `/aws/bedrock-agentcore/runtimes/<id>-DEFAULT`, the path the L2 uses.
- The L2 `Runtime` constructor adds `XRayAccess` with `resources: ["*"]` and no
  condition (`runtime/runtime.js`), even with tracing off. This is why the plan uses L1.
- TTOBAK precedent: `backend/python/research-agent`:
  - FastAPI with a `/ping` HealthyBusy status;
  - a `botocore` SigV4 gateway call with redirects refused;
  - tool name `ttobak-web-search-tool___WebSearch`.
- Callers need only `bedrock-agentcore:InvokeGateway` on the gateway ARN (`infra/lib/ai-stack.ts`).
- Phase 1 `test-billing.yml:28` carries the fork guard on its single job only.

Companion source at `dd540f5` (`git show`, tracked files only):
- `scripts/aws_whats_new.json` is tracked:
  - eight entries of `{title, date, url, summary}`;
  - URLs only on `https://aws.amazon.com` (seven) and `https://repost.aws` (one);
  - public AWS announcements (Direct Connect, Bedrock, Lambda Web Functions,
    CloudFront, AgentCore payments, Bedrock cost-allocation tags), with short
    generic Korean summaries and no customer name or customer-specific text.
  - **Decision: import it unchanged.** `scripts/sessions.py:12` loads it
    unconditionally. A Task 1 test guards its shape.
- Tests that depend on files this plan does not import (Task 1 handles each):
  - `test_pipeline_wiring.py`: `setUp` reads `analysis/run_pipeline.sh` (l.17),
    l.48 runs it, l.73 reads `dashboard-app/app/server.js`, and l.142–147 run
    `git check-ignore` on companion paths.
  - `test_generic.py`: l.9 `dashboard-app/app/page/body.html`, l.10
    `analysis/tools/compare_datasets.py` (used from l.128), l.38–39 companion roots;
    l.14 and l.16 embed the customer-name regex.
  - `test_report.py:40` asserts the report mentions `run_pipeline.sh`, because
    `report.py:340` prints a rebuild hint with companion paths.
  - `test_fsisearch.py:6` loads `dashboard-app/lambda/fsisearch/index.py`.
- Rerun guards (`worker/main.py:947`, `:953`): UP `SET ai` with `attribute_exists(pk)`;
  META `SET ai_<src>` with `current_<src> = :v`; no claim.
  - Added with the import (after review): a per-version rerun lease
    `ACCT#{acct}` / `RERUN#{src}#{version}` (owner, `expiresAt`, `ttl`) so two reruns of one
    version never interleave their writes; the runtime keeps this lease in Task 4.
- `main.py:449` generates a fresh claim token per `Job`; `:626` sets `agents`
  running inside `ai_steps`; `:152` sweeps all direct children; `:963` installs
  SIGTERM only on the main thread.

Live account, read-only `describe-*` calls (180294183052, ap-northeast-2):
- Endpoint services include `bedrock-agentcore`, `bedrock-agentcore-control` and
  `bedrock-agentcore.gateway`.
- `fsi-demo-vpc` (tag `Name=fsi-demo-vpc`, stack `FsiDemoNetworkStack`) endpoints by owner:
  - `FsiDemoNetworkStack`: `bedrock-agentcore` (private DNS, full-access policy,
    ENIs in `PrivateSubnet1/2`, SG `fsi-demo-sg-vpc-endpoint` admits 443 from
    `10.0.0.0/16`), plus `sts`, `ecr.api`, `ecr.dkr`, `bedrock-runtime`,
    `bedrock-agentcore-control`, and the S3/DynamoDB gateways.
  - companion stack: `logs` (SGs `<companion>-EndpointSg…` and
    `BankPlatformNetwork-InfraEndpointAccess…`), `lambda` and `secretsmanager`.
- Subnet groups: `Public`, `Private`, `Isolated`, `bank-browser`, `bank-cloud`,
  `bank-onprem` (the `bank-*` groups are also Isolated type). The checked-in
  `infra/cdk.context.json` entry predates the `bank-*` groups.
- Companion rules `…JobCreated…` and `…Daily…` are `ENABLED`; the stack is
  `UPDATE_COMPLETE` (2026-10-26T12:29Z).
- The global lookup filter `Name=FsiDemoNetworkStack/FsiDemoVpc` matches no VPC;
  that name is on the subnets.

UNVERIFIED (each is checked at the start of the task that needs it):
- U1: microVM CPU, memory and ephemeral disk per session. Task 4 adds a size
  guard; Task 9 measures the largest version data prefix.
- U2: `PR_SET_CHILD_SUBREAPER` inside the microVM. Best effort: logged, not fatal (Task 4).
- U3: the CloudFormation handler for `AWS::BedrockAgentCore::Runtime` accepts
  `lifecycleConfiguration` in ap-northeast-2. Covered by the change-set preview in Task 9.
- U4: the service tolerates a pre-created `/aws/bedrock-agentcore/runtimes/<id>-DEFAULT`
  log group with retention. Fallback: no pre-creation, retention set post-deploy by
  runbook (Task 7).
- U5: the bedrock-agentcore client in the Lambda Python 3.12 runtime's bundled boto3.
  The plan bundles pinned boto3 1.42.97 instead (Task 6).

## Trigger options considered

- **(a) Chosen for analyze.** The Fargate ingest calls `InvokeAgentRuntime` through
  the existing `bedrock-agentcore` endpoint.
  - No extra hop and no marker objects.
  - Ingest sees the result directly and resolves uncertainty through `RUN#`.
- **(b) Rejected for analyze.** A marker object plus EventBridge plus a Lambda adds
  a second event path whose lost event leaves `running` rows behind.
- **(b′) Chosen for rerun.** A non-VPC dispatcher Lambda on `jobs/r*` means no
  ingest task for a rerun (spec §4.3).
- **Single-owner alternatives rejected:**
  - Running both stacks and trusting the claim fails because reruns have no claim.
  - A controlled pause or drain of the companion for acceptance would stop
    production uploads.
  - The reserved acceptance prefix gives provable separation instead. EventBridge
    prefix matching is literal, `billing-acceptance/` never starts with `jobs/`,
    and the BFF cannot write there (its role allows `s3:PutObject` only on `jobs/*`
    and uploads).

## Durable ownership and handoff protocol

- Run item:
  - key `pk = ACCT#{acct}`, `sk = RUN#{jobId}`, where `jobId` is the job key's
    basename without `.json`;
  - attributes `owner` (random 32-hex per acquisition), `state`
    (`running` | `done` | `failed` | `fenced`), `kind`, `startedAt`, `ttl` (30 days).
- Acquire, in the runtime: `PutItem` with `attribute_not_exists(pk)`.
  - The analyze job verifies the UP claim and `running` status first.
  - If acquisition fails, the invocation returns `duplicate` or `fenced` and writes nothing.
  - There is no takeover. A crashed owner leaves `running`, and the BFF stale rule
    (`claimedAt + 10800 + 1800` s) frees the upload for discard and re-upload.
- Ingest invoke outcomes:
  - **Accepted:** HTTP 200 `{"status": "accepted" | "duplicate"}`. Ingest exits 0
    and keeps the lock.
  - **Definitive rejection:** `AccessDeniedException`, `ValidationException`,
    `ResourceNotFoundException`, or HTTP 400 `{"status": "rejected"}`, but only
    when it is the outcome of the **first** attempt of this handoff. The app
    rejects before acquiring. Ingest marks `agents` failed, finishes failed and
    releases the lock.
  - **Rejection after a non-definitive attempt is ambiguous.** If an earlier attempt
    with the same session ID timed out, lost its connection or returned 5xx, that
    attempt may have been accepted: the runtime may already own `RUN#{jobId}` and
    will publish or `fail_running` later. Such a rejection never takes the failure
    path directly. It is resolved exactly like an ambiguous result (below): the
    `RUN#` re-read, then the fence. Test: `test_ambiguous_then_rejected_resolves_through_run_item`.
  - **Ambiguous:** timeout, connection error or 5xx after bounded retries with the
    same session ID. Ingest polls `RUN#{jobId}` (consistent read, 5 s interval, 60 s).
    - If the item is present, the job was handed off: exit 0.
    - If it is absent, ingest **fences** with a conditional put
      `state = fenced, owner = ingest token` (`attribute_not_exists`).
      - On success no runtime can ever acquire, so the failure path is safe.
      - On condition failure, or when the fence write's own result is lost, ingest
        re-reads `RUN#{jobId}` (consistent). `state = fenced` with `owner` = its own
        token means its fence committed: take the failure path. Any other owner means
        the runtime won the race: handed off. The runtime's `acquire` reconciles a
        lost acquisition response the same way (re-read, compare `owner`).
      - Tests: `test_fence_committed_response_lost_fails_job`,
        `test_acquire_committed_response_lost_runs_once`.
- Dispatcher (rerun): one retry budget inside a 30 s function: botocore
  `connect_timeout=3`, `read_timeout=10`, `retries={"total_max_attempts": 2, "mode": "standard"}`
  (two requests in total, at most 2 × (3 + 10) s plus backoff ≤ 28 s), and no outer
  retry loop in the dispatcher (`invoke_agents(..., attempts=1)`). An ambiguous result raises, Lambda's async retry
  re-invokes with the same session ID, and `RUN#` deduplicates. The final failure
  goes to the on-failure destination (`JobDlq`).

## File Structure

```
services/billing/worker/
  README.md                       provenance (dd540f5), layout, local tests, retired companion tests
  .dockerignore                   allowlist
  requirements-ingest.txt         duckdb==1.4.5, boto3==1.42.97
  requirements-agents.txt         boto3==1.42.97, fastapi==<pin>, uvicorn==<pin>
  requirements-dev.txt            httpx==<pin>
  Dockerfile.ingest, Dockerfile.agents
  run_account.sh, scripts/*.py (incl. aws_whats_new.json), cur/*.py
  scripts/fsi_gateway.py          new
  worker/common.py                shared helpers + JobState + ProcessScope + RunItem
  worker/main.py                  ingest (analyze only) + handoff
  worker/handoff.py               session_id, invoke_agents, InvokeOutcome
  worker/selfcheck.py             live cross-Account denial probe (acceptance only)
  worker/ai_step.py
  runtime/app.py, runtime/job.py
  dispatch/index.py, dispatch/package.sh   (dist/ is git-ignored: index.py + handoff.py + pinned boto3)
  tests/
infra/lib/billing-analysis.ts, infra/test/billing-analysis.test.ts
docs/runbooks/billing-phase2-release.md   acceptance evidence + Phase 4 cut-over design
```

---

### Task 1: Import the worker under `services/billing/worker`

**Files:**
- Create: the tree above except `common.py`, `handoff.py`, `selfcheck.py`,
  `runtime/`, `dispatch/` and the new tests.
- Modify:
  - `.gitignore`: add `services/billing/worker/{datasets,out,.agent_cache}/` and `services/billing/worker/dispatch/dist/`.
  - `.github/workflows/test-billing.yml`: add a `worker` job **with the fork guard**.
- Source: `git -C <companion repo> archive dd540f5 analysis/scripts analysis/cur analysis/worker analysis/tests analysis/run_account.sh analysis/requirements-worker.txt analysis/.dockerignore | tar -x -C /home/atomoh/.tmp/billing-worker-import`.
  - Not imported: `analysis/tools/`, `run_pipeline.sh`, anything git-ignored.

**Interfaces:**
- Produces the companion modules at the same relative paths;
  `requirements-worker.txt` becomes `requirements-ingest.txt`.
- Test command: `cd services/billing/worker && python3 -m unittest discover -s tests -v`.
- Companion test disposition (listed in README):
  - `test_fsisearch.py`: retired; replaced by Task 2.
  - `test_pipeline_wiring.py`:
    - `setUp` no longer reads `run_pipeline.sh`.
    - Retire `test_pipeline_wiring`, `test_wrapper_runs_accounts_then_merges`,
      `test_agents_guarded_and_report_after_briefing` and every other test that
      reads `self.sh` or runs the wrapper. Each one tests the local wrapper only.
    - Retarget `test_server_allows_agent_notes` to `services/billing/bff/app.js`'s `DATASETS`.
    - Retarget `test_generated_outputs_ignored` to `services/billing/worker/{.agent_cache,out}` in the TTOBAK `.gitignore`.
    - Keep the git-init cache tests (`test_*_cache_*`); they use temp repos.
  - `test_generic.py`:
    - Remove the customer alternates from `FIXED`.
    - Delete `CUSTOMER` and `test_no_customer_name_outside_docs`; Task 1 Step 5 replaces them.
    - `test_no_fixed_period_or_customer_literals` scans `scripts/*.py` and `run_account.sh` only.
    - Retire the `compare_datasets` tests (`test_renamed_fields_match`,
      `test_value_change_reported_without_values`, `test_dataset_missing_on_one_side_is_a_difference`
      and their helper).
  - `report.py:340` rebuild hint: changed to "rerun from the Billing upload page
    (AI rerun)". `test_report.py:40` asserts the new hint and `run_pipeline.sh` is absent.

- [ ] **Step 1: Import and apply the dispositions above.**
- [ ] **Step 2: Write the failing `tests/test_contract.py`:**
  - `test_no_companion_paths`: no file references `dashboard-app/`, `analysis/` or `run_pipeline.sh`.
  - `test_dockerignore_allowlist`: starts with `*`; excludes `tests/`, `datasets/`, `out/`, `.agent_cache/`.
  - `test_whats_new_seed_is_public_aws`: every `url` starts with
    `https://aws.amazon.com/` or `https://repost.aws/`; keys are exactly
    `{title, date, url, summary}`.
  - `test_sessions_loads_seed`: `import sessions` succeeds from `scripts/`.
- [ ] **Step 3: Run** the unittest command. Expected: FAIL first, then PASS. Record
  the `Ran N tests` line and the retired-test list in the commit body.
- [ ] **Step 4: CI.** Add the `worker` job:
  - fork guard, runner `ttobak-arm`, `setup-python` 3.12;
  - `pip install -r requirements-ingest.txt -r requirements-agents.txt -r requirements-dev.txt`;
  - then the unittest command.
- [ ] **Step 5: Customer-name check.**
  `git grep -niE "<owner-supplied list>" services/billing/worker` returns nothing.
  The list comes in the dispatch only (Phase 1 Ruling P1).
- [ ] **Step 6: Commit** `feat(billing): import the analysis worker`.

### Task 2: FSI search through the web-search gateway

**Files:**
- Create: `scripts/fsi_gateway.py`, `tests/test_fsi_gateway.py`.
- Modify: `scripts/agents.py`, `worker/ai_step.py`, `tests/test_agents.py`.
- Modify: `CLAUDE.md` (QA-search transport list gains
  `services/billing/worker/scripts/fsi_gateway.py`), then run
  `python3 scripts/docs/sync_review_context.py` (never hand-edit `AGENTS.md`).

**Interfaces:**
- `fsi_gateway.search(queries: list[str], post: Callable[[str], dict]) -> {"results": [...], "errors": [int]}`
  ports FsiSearch `search`, `_items` and `_sse_json`.
- `fsi_gateway.make_post(url: str, region: str, credentials) -> Callable[[str], dict]`:
  SigV4 `bedrock-agentcore` in `region`, redirects refused, 15 s timeout, 2,000,000-byte cap.
- `fsi_gateway.check_config(arn: str, url: str, region: str) -> None` raises `ValueError` unless:
  - the ARN region equals `region`;
  - the ARN gateway ID appears as the URL host's first label;
  - the host ends with `.gateway.bedrock-agentcore.{region}.amazonaws.com`;
  - the URL is `https://`.
- `agents.gateway_search(url: str, region: str, tool: str, session=None) -> Callable[[list[str]], list]`
  keeps the old `lambda_search` contract.
- `ai_step.py agents` uses `WEB_SEARCH_GATEWAY_ARN`, `WEB_SEARCH_GATEWAY_URL`,
  `WEB_SEARCH_GATEWAY_REGION` and `WEB_SEARCH_TOOL`. All four or none; none means
  the "not connected" note. `FSI_SEARCH_FUNCTION` is removed.

- [ ] **Step 1: Failing tests** (`post` stubbed):
  - `test_sse_frame_unwrapped`
  - `test_https_only_results`
  - `test_iso_dates_only`
  - `test_one_failed_query_does_not_sink_others`
  - `test_signs_for_bedrock_agentcore_in_gateway_region` (scope `/us-east-1/bedrock-agentcore/`)
  - `test_redirect_refused`
  - `test_http_url_rejected`
  - `test_queries_not_logged`
  - `test_config_region_mismatch_rejected`
  - `test_config_gateway_id_mismatch_rejected`
  - In `test_agents.py`:
    - `test_unsafe_query_never_posted`
    - `test_no_lambda_search_left`
    - `test_gateway_env_incomplete_means_not_connected`
- [ ] **Step 2: Run** `python3 -m unittest tests.test_fsi_gateway tests.test_agents -v`. Expected: FAIL.
- [ ] **Step 3: Implement**, then run the full suite. Expected: PASS.
- [ ] **Step 4: Commit** `feat(billing): FSI search through the web-search gateway`.

### Task 3: Shared state, ingest staging and handoff

**Files:**
- Create: `worker/common.py`, `worker/handoff.py`, `tests/test_handoff.py`, `tests/test_common.py`.
- Modify: `worker/main.py`, `tests/test_worker.py`.

**Interfaces:**
- `common.py`:
  - Takes from `main.py`: the regexes, `STEPS`, `SOURCES`, `RERUN_FILES`, `to_av`,
    `from_av`, `session_policy`, `DataSession`, the S3 helpers, `each_regular` and
    `validate_job(job, prefixes=("jobs/",))`.
  - `class JobState(env, job, ddb, data, *, token: str, steps: dict[str, str])`
    holds the UP/META/LOCK helpers. The token and steps are always passed in, never
    generated inside.
  - `JobState.hydrate(ddb, table, acct, upload_id) -> Hydrated` returns the persisted
    `claim`, `status`, `claimedAt` and `steps` in one consistent read
    (`Hydrated` is a small dataclass with those four fields).
  - `class ProcessScope(cancel: threading.Event, killpg=os.killpg)` provides
    `spawn(cmd, env, cwd, deadline, on_line=None) -> (rc, stdout)` and `close()`.
    - It tracks only the process groups it created.
    - The orphan sweep kills only re-parented processes whose session ID belongs
      to one of its groups, plus re-parented orphans while it is the process's
      only scope (always true: one job per runtime session, one job per ingest task).
    - Cancellation comes from `cancel` (host-owned). In ingest, the SIGTERM handler
      sets it from the main thread; in the runtime, the app's shutdown hook sets it.
  - `class RunItem(ddb, table, acct, job_id)`:
    - `acquire(kind, owner) -> bool`
    - `get() -> dict | None` (consistent)
    - `fence(owner) -> bool`
    - `finish(owner, state)` (conditioned on `owner`)
- `handoff.py`:
  - `session_id(job_key, claim) -> str`: `"billing-" + sha256(f"{job_key}|{claim or ''}")`, 72 characters.
  - `class InvokeOutcome(Enum)`: `ACCEPTED`, `REJECTED`, `AMBIGUOUS`.
  - `invoke_agents(client, runtime_arn, job_key, claim, qualifier="DEFAULT", attempts=3, sleep=time.sleep) -> InvokeOutcome`
    classifies outcomes as in the protocol section.
  - `resolve_ambiguous(run_item: RunItem, owner: str, poll_s=5, window_s=60, sleep=time.sleep, now=time.monotonic) -> bool`
    returns True when the job was handed off and False when fenced.
- `main.py`:
  - analyze only; env adds `AGENT_RUNTIME_ARN` and `JOB_PREFIXES` (default `jobs/`).
  - `_analyze()`: validate → pipeline → `stage_version()` (data JSON, `checks.json`,
    CUR parquet; no `report.md`, no transaction) → `set_step("agents", "running")` → `hand_off()`.
  - An acceptance-prefix job must name `ACCEPTANCE_ACCOUNT_ID` (env), or it exits 2 with no writes.

- [ ] **Step 1: Failing tests:**
  - `test_analyze_stages_datasets_before_handoff`: no `transact_write_items` in ingest.
  - `test_agents_running_written_once_by_ingest`
  - `test_accepted_keeps_lock_and_running_status`
  - `test_definitive_rejection_marks_agents_failed_and_releases_lock`
  - `test_ambiguous_invoke_with_run_item_hands_off`: no failure write; the lock is kept.
  - `test_ambiguous_invoke_fenced_then_fails`: the fence is written, then the failure path runs.
  - `test_ambiguous_then_rejected_resolves_through_run_item`: attempt 1 times out after the
    fake runtime acquired `RUN#`, attempt 2 raises `AccessDeniedException`; no failure write,
    the lock is kept and the job is handed off.
  - `test_fence_lost_to_runtime_hands_off`
  - `test_invoke_retries_keep_session_id`
  - `test_session_id_length_and_determinism`
  - `test_rerun_job_rejected_by_ingest`
  - `test_acceptance_prefix_requires_acceptance_account`
  - `test_check_failure_needs_review_never_invokes`
  - `test_payload_has_only_job_key_and_claim`
  - `test_process_scope_kills_only_its_groups`: a foreign child of the test process survives.
  - `test_process_scope_cancel_aborts_child`
  - The ported claim, discard, timeout, SIGTERM, symlink and orphan tests stay green
    against `ProcessScope`.
- [ ] **Step 2: Run** `python3 -m unittest tests.test_worker tests.test_handoff tests.test_common -v`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** the full suite. Expected: PASS.
- [ ] **Step 5: Commit** `feat(billing): ingest hands the agent stage to AgentCore Runtime`.

### Task 4: Runtime job runner

**Files:** Create `runtime/__init__.py`, `runtime/job.py`, `tests/test_runtime_job.py`.

**Interfaces:**
- `class AgentJob(env, s3, ddb, sts, s3_factory, cancel: threading.Event, now=time.time)`.
  - env: `BUCKET, TABLE, DATA_ROLE_ARN, PUBLIC_URL, JOB_TIMEOUT_S, RERUN_TIMEOUT_S (3600), WORK_ROOT (/tmp/billing), JOB_PREFIXES, ACCEPTANCE_ACCOUNT_ID?, MAX_DATA_BYTES (536870912), WEB_SEARCH_*`.
- `AgentJob.run(job_key: str, claim: str | None) -> Literal["done", "failed", "duplicate", "fenced", "rejected"]`.
  Order of operations:
  1. Read the job object (≤ 64 KiB), then run `validate_job` and the acceptance-Account rule.
  2. Analyze only: `hydrate()`. Continue only if the persisted claim equals the
     supplied claim and `status = running`; otherwise return `rejected` with no write.
     Build `JobState` with the **supplied** token and the **hydrated** steps.
  3. `RunItem.acquire()`; on failure return `duplicate` or `fenced`, with no write.
  4. Assume the data session, then run the **cross-Account list canary**:
     `ListObjectsV2` on `accounts/<random 16-hex>/` must raise `AccessDenied`;
     otherwise fail closed with `PipelineError("scope")` before any read.
  5. Size guard (`MAX_DATA_BYTES`), then download `versions/{v}/data/*.json`.
  6. Run `ai_steps` under a `ProcessScope` (agents child with role env, report child
     with no AWS env). `agents=running` is already persisted, so no write repeats it.
     Then `agents done`, `report running/done`.
  7. Upload `RERUN_FILES` and `report.md`.
  8. Analyze: the companion publish transaction (same expressions), then
     `release_lock`. Rerun: the companion `_rerun` updates.
  9. `RunItem.finish(owner, done | failed)`. The failure path for analyze is
     `fail_running`, `finish` and `release_lock`, as in the companion.
  - Deadlines: analyze `claimedAt + JOB_TIMEOUT_S`; rerun `startedAt + RERUN_TIMEOUT_S`.

- [ ] **Step 1: Failing tests:**
  - `test_forged_claim_writes_nothing`
  - `test_not_running_upload_writes_nothing`
  - `test_simultaneous_sessions_same_job_one_runs`: two `AgentJob`s on one FakeDDB
    race; exactly one acquires, and the other makes zero S3 writes.
  - `test_delayed_retry_after_start_no_writes`
  - `test_replay_after_session_end_no_writes`: `RUN#` is `done`, so nothing is written.
  - `test_fenced_job_never_runs`
  - `test_supplied_claim_token_used_for_every_up_write`
  - `test_hydrated_steps_timeout_marks_agents_not_validate`
  - `test_step_sequence_matches_phase1_contract`: ingest plus runtime `steps.*`
    writes equal the companion sequence, with `agents=running` exactly once.
  - `test_publish_transaction_matches_companion`
  - `test_lock_released_only_by_runtime_finish`
  - `test_runtime_data_session_is_account_scoped`
  - `test_cross_account_list_canary_fail_closed`: an allowed list yields failed
    with no download.
  - `test_agents_child_gets_role_env_report_child_none`
  - `test_deadline_from_claimed_at`
  - `test_cancel_records_aborted`
  - `test_data_too_large_fails_before_download`
  - `test_rerun_updates_ai_only_when_current` (ported)
  - `test_rerun_without_up_row_still_updates_meta` (ported legacy behavior: no UP
    row is created, META `ai_<src>` is updated)
  - `test_harden_failure_is_logged_not_fatal` (U2)
- [ ] **Step 2: Run** `python3 -m unittest tests.test_runtime_job -v`. Expected: FAIL.
- [ ] **Step 3: Implement** (reuse `common.py`; no copy of `main.py` logic).
- [ ] **Step 4: Run** the full suite. Expected: PASS.
- [ ] **Step 5: Commit** `feat(billing): agent runtime job runner with durable ownership`.

### Task 5: Runtime HTTP contract and images

**Files:** Create `runtime/app.py`, `Dockerfile.agents`, `tests/test_runtime_app.py`;
modify `Dockerfile.ingest` and `.dockerignore`.

**Interfaces:**
- `class SessionJob(prepare: Callable[[str, str | None], Prepared], run: Callable[[Prepared, threading.Event], str], clock=time.time)`:
  - `bind(job_key, claim) -> Literal["accepted", "duplicate", "busy", "rejected", "retry"]`.
    - The first call runs `prepare` **synchronously** before answering: read and
      validate the job object, check the UP claim (analyze) and acquire `RUN#`
      (Task 4 steps 1–3; bounded, seconds). Only a durable `RUN#` owned by this
      session yields `accepted`, and only then does one daemon thread start `run`.
    - A definitive rejection from `prepare` gives `rejected`; a transient failure
      (S3/DynamoDB retries exhausted, throttling) gives `retry` and binds nothing,
      so the caller's retry with the same session ID can try again. `RUN#` already
      owned by this session (lost response) counts as `accepted`/`duplicate`.
    - The same `(job_key, claim)` gives `duplicate`; any other pair gives `busy`.
  - `status() -> Literal["Healthy", "HealthyBusy"]`.
  - `last_status_change() -> int`.
  - `cancel(grace_s: float = 60) -> None`.
- `create_app(job: SessionJob) -> FastAPI`:
  - `POST /invocations`:
    - 400 `{"status": "rejected"}` for a bad key or claim format;
    - 409 `{"status": "busy"}` for a second job in the session;
    - 503 `{"status": "retry"}` for a transient `prepare` failure (ingest and the
      dispatcher treat it as ambiguous and retry with the same session ID; the
      dispatcher's final failure goes to `JobDlq`);
    - 200 `{"status": "accepted" | "duplicate"}` otherwise. Only `prepare` runs
      before the answer; the agent stages never block it.
  - Tests add `test_prepare_failure_before_acquire_returns_retry` and
    `test_accepted_only_after_run_item_acquired`.
  - `GET /ping` returns `{"status", "time_of_last_update"}`. `time_of_last_update`
    changes only when the status changes.
  - A lifespan shutdown calls `job.cancel()`.
- Images: `--platform=linux/arm64 public.ecr.aws/docker/library/python:3.12-slim`, uid 10001.
  - The agents image copies `scripts/`, `worker/` and `runtime/`, `EXPOSE 8080`, and
    runs `uvicorn runtime.app:app --host 0.0.0.0 --port 8080`.
  - The ingest image is the companion image plus `worker/common.py` and `handoff.py`.

- [ ] **Step 1: Failing tests** (TestClient; `run` blocks on an Event):
  - `test_invocation_returns_before_job_ends`
  - `test_ping_busy_while_job_runs_then_healthy`
  - `test_ping_time_changes_only_on_status_change`
  - `test_duplicate_invocation_starts_once`
  - `test_second_job_in_session_rejected_busy`
  - `test_bad_job_key_rejected_without_start`
  - `test_payload_extra_fields_ignored`
  - `test_job_exception_returns_to_healthy`
  - `test_shutdown_cancels_job_and_records_aborted`
- [ ] **Step 2: Run, implement, run.** Expected: FAIL, then PASS.
- [ ] **Step 3: Build** both images with `docker build --platform linux/arm64 -f Dockerfile.{agents,ingest} .`
  from `services/billing/worker`. Run `docker run --rm <agents> python3 -c "import runtime.app"`.
  Expected: OK; neither image has `tests/`.
- [ ] **Step 4: Commit** `feat(billing): AgentCore Runtime HTTP contract`.

### Task 6: Rerun dispatcher Lambda

**Files:** Create `dispatch/index.py`, `dispatch/package.sh`, `tests/test_dispatch.py`.

**Interfaces:**
- `rerun_key_from_event(event: dict, bucket: str, prefixes: tuple[str, ...]) -> str`:
  the bucket must match, and the key must match `^(jobs/|billing-acceptance/)r[A-Za-z0-9_-]{1,79}\.json$`
  with a prefix in `prefixes`. Otherwise it raises `ValueError`.
- `handler(event, context) -> {"status": str}`:
  - `ACCEPTED` returns.
  - `REJECTED` first re-reads `RUN#{jobId}` (consistent read). Present means an earlier
    async attempt (a retried invocation, or the client's second request) was accepted:
    return handed off. Absent raises `ValueError`, which goes to the DLQ after retries.
  - The account for that key is not in the event or the job key (the BFF names rerun
    jobs by version and time, `services/billing/bff/lib/uploads.js`). Before invoking,
    the handler reads the job object once: `GetObject` on the exact key, at most 16 KiB
    (`Range: bytes=0-16383`, rejected if `ContentLength` is larger), parsed as JSON and
    validated (`acct` matches `ACCT_ID`, `kind == "rerun"`, `version` matches `VERSION_ID`).
    Invalid objects raise `ValueError` without invoking. The validated `acct` builds both
    the session payload and the `ACCT#{acct}` / `RUN#{jobId}` key.
  - Dispatcher role: `s3:GetObject` on `jobs/r*.json` (plus `billing-acceptance/r*.json`
    only with acceptance context) and `dynamodb:GetItem` with `LeadingKeys` `ACCT#*`.
  - `AMBIGUOUS` raises `RuntimeError` so the async retry re-invokes with the same session.
- Client: `botocore.config.Config(connect_timeout=3, read_timeout=10, retries={"total_max_attempts": 2, "mode": "standard"})`; `invoke_agents(..., attempts=1)`; function timeout 30 s.
- `package.sh` builds `dispatch/dist/` from `index.py`, `../worker/handoff.py` and
  `pip install --target dist boto3==1.42.97` (U5). Workflows run it before synth.

- [ ] **Step 1: Failing tests:**
  - `test_job_object_read_is_bounded_and_validated`: over 16 KiB, bad `acct`, wrong `kind`
    each raise `ValueError` with no invoke.
  - `test_rejected_after_lost_acceptance_hands_off`: a prior attempt was accepted (fake
    `RUN#` present), this attempt is `REJECTED`; the handler returns without raising.
  - `test_rerun_key_accepted`
  - `test_analyze_key_rejected`
  - `test_other_bucket_rejected`
  - `test_traversal_key_rejected`
  - `test_acceptance_prefix_only_when_enabled`
  - `test_ambiguous_raises_for_retry`
  - `test_rejected_raises_value_error`
  - `test_client_timeouts_bounded`: total worst case is 26 s or less.
  - `test_logs_have_no_key`
- [ ] **Step 2: Run, implement, run.** Expected: FAIL, then PASS.
- [ ] **Step 3: Commit** `feat(billing): rerun dispatcher for the agent runtime`.

### Task 7: `BillingAnalysis` in `TtobakBillingStack`

**Files:**
- Create `infra/lib/billing-analysis.ts`, `infra/test/billing-analysis.test.ts`.
- Modify `infra/lib/billing-stack.ts`, `infra/bin/infra.ts`, `infra/cdk.json`,
  `infra/cdk.context.json` (refreshed fsi-demo-vpc lookup) and `docs/INFRA-SPEC.md`.

**Interfaces:**
- `new BillingAnalysis(scope, 'Analysis', { dataBucketName, platformTableName, publicUrl, vpcName, webSearch?: {gatewayArn, gatewayUrl, toolName}, jobsEnabled: boolean, acceptance?: {prefix: string, accountId: string} })`.
  It exists only when `ttobak:billingAnalysisEnabled === 'true'`.
- Context keys:
  - `ttobak:billingVpcName` (default `fsi-demo-vpc`);
  - `ttobak:billingWebSearchGatewayArn`, `ttobak:billingWebSearchGatewayUrl` and
    `ttobak:billingWebSearchTool`: all or none, checked by the same rules as
    `fsi_gateway.check_config` (ported to TS);
  - `ttobak:billingJobsEnabled`;
  - `ttobak:billingAcceptancePrefix` and `ttobak:billingAcceptanceAccountId`: both
    or none. The prefix must match `^billing-acceptance/$`, so it can never start with `jobs/`.
- Asset paths: `path.join(__dirname, '..', '..', 'services', 'billing', 'worker')`
  and `.../dispatch/dist`.
- Resources:
  - `DockerImageAsset` `AgentsImage` (`Dockerfile.agents`, `LINUX_ARM64`, `exclude` as `.dockerignore`).
  - `iam.Role` `AgentRuntimeRole`:
    - Trust: `bedrock-agentcore.amazonaws.com` with `aws:SourceAccount` and
      `ArnLike aws:SourceArn arn:aws:bedrock-agentcore:<region>:<acct>:runtime/*`.
    - ECR: `BatchGetImage` and `GetDownloadUrlForLayer` on the asset repository ARN;
      `GetAuthorizationToken` on `*` with condition `aws:RequestedRegion = <region>`.
    - Logs: `CreateLogGroup` and `DescribeLogStreams` on `log-group:/aws/bedrock-agentcore/runtimes/*`;
      `CreateLogStream` and `PutLogEvents` on `...:log-stream:*`; `DescribeLogGroups`
      on `log-group:*` in this account and Region.
    - `cloudwatch:PutMetricData` on `*` with `cloudwatch:namespace = bedrock-agentcore`.
    - No X-Ray and no workload-identity grants (tracing off, no OAuth).
    - `s3:GetObject` on `jobs/*` (plus `billing-acceptance/*` only with acceptance context).
    - `sts:AssumeRole` on `AgentDataRole`.
    - Platform table `GetItem`, `PutItem` (`RUN#`), `UpdateItem`, `DeleteItem` and
      `ConditionCheckItem` with `LeadingKeys ACCT#*`.
    - The three Bedrock model ARNs.
    - `bedrock-agentcore:InvokeGateway` on the gateway ARN, only when `webSearch` is set.
  - `AgentDataRole` (trust: runtime role, 1 h): Get and Put on `accounts/*/versions/*`;
    ListBucket with prefix `accounts/*`; Get on `data/whats_new_feed.json`.
  - `bedrockagentcore.CfnRuntime` `ttobak_billing_agents`:
    - `containerUri: AgentsImage.imageUri`, `networkMode: PUBLIC`, `protocolConfiguration: HTTP`;
    - `lifecycleConfiguration { idleRuntimeSessionTimeout: 300, maxLifetime: 14400 }`;
    - env `BUCKET, TABLE, DATA_ROLE_ARN, PUBLIC_URL, JOB_TIMEOUT_S=10800, RERUN_TIMEOUT_S=3600, JOB_PREFIXES, AGENT_MODEL, AWS_REGION`,
      plus `ACCEPTANCE_ACCOUNT_ID` and `WEB_SEARCH_*` when set;
    - depends on the role's default policy.
  - Log group `/aws/bedrock-agentcore/runtimes/<attrAgentRuntimeId>-DEFAULT`, one-month retention (U4).
  - Ingest:
    - cluster `ttobak-billing`;
    - task definition `ttobak-billing-ingest`: ARM64, 4096 / 16384 MiB / 100 GiB,
      `Dockerfile.ingest`, stop timeout 120 s, awslogs with one-month retention;
    - env `BUCKET, TABLE, DATA_ROLE_ARN, PUBLIC_URL, JOB_TIMEOUT_S, AGENT_RUNTIME_ARN, JOB_PREFIXES, AWS_REGION, ACCEPTANCE_ACCOUNT_ID?`.
  - Task role:
    - `jobs/*` Get (plus the acceptance prefix);
    - AssumeRole on `IngestDataRole`;
    - the platform-table actions above;
    - `bedrock-agentcore:InvokeAgentRuntime` on the runtime ARN and `ARN/*`.
  - `IngestDataRole`: the companion `WorkerDataRole` statements.
  - `IngestSg`: no ingress; egress 443 to the VPC CIDR and the S3/DDB prefix lists.
  - Dispatcher:
    - Python 3.12 ARM64, no VPC, 30 s, asset `dispatch/dist`;
    - an explicit role with logs only on its own log group, `InvokeAgentRuntime`
      on the runtime, and the two Task 6 reads (`s3:GetObject` on rerun job keys,
      `dynamodb:GetItem` on `ACCT#*`); no `AWSLambdaBasicExecutionRole`;
    - async retries 2 and `maxEventAge` 15 min, on-failure `SqsDestination(JobDlq)`;
      the EventBridge target also sets `maxEventAge` 15 min, so every delivery of a job
      ends within a known window (used by the Task 8 rollback).
  - `JobDlq` plus `JobDlqAlarm`.
  - `IngestJobRule` (`jobs/u`, EcsTask on the `Isolated` group, `JOB_KEY` from
    `$.detail.object.key`, retries 2, DLQ) and `RerunJobRule` (`jobs/r`,
    dispatcher, retries 2, DLQ); both `enabled: jobsEnabled`.
  - With acceptance context only: `AcceptanceIngestRule` (`billing-acceptance/u`)
    and `AcceptanceRerunRule` (`billing-acceptance/r`), enabled, same targets.
  - Outputs (no exports): `AgentRuntimeArn`, `IngestClusterName`,
    `IngestTaskDefinitionArn`, `IngestSecurityGroupId`, `JobDlqUrl`.

- [ ] **Step 1: Refresh the VPC lookup** with a read-only identity. Check that the
  `Isolated` group lists only the two `FsiDemoNetworkStack/FsiDemoVpc/IsolatedSubnet*` subnets.
- [ ] **Step 2: Failing `billing-analysis.test.ts`:**
  - `absent_without_analysis_context`
  - `job_rules_disabled_without_context`
  - `job_rules_enabled_only_with_true_string`
  - `rule_prefixes_split_analyze_and_rerun`
  - `acceptance_rules_absent_without_context`
  - `acceptance_prefix_cannot_overlap_companion`: `jobs/`, `jobs/x/` and `billing-acceptance` (no slash) throw.
  - `acceptance_requires_account_id`
  - `runtime_is_l1_public_http_iam`: no `authorizerConfiguration` is rendered, so IAM is the default.
  - `runtime_lifecycle_within_limits`
  - `no_unconditioned_resource_star`: walk every `AWS::IAM::Policy` and `AWS::IAM::Role`
    inline policy in the stack template. `Resource: "*"` is allowed only for:
    - `ecr:GetAuthorizationToken` with `aws:RequestedRegion` (our role);
    - `cloudwatch:PutMetricData` with `cloudwatch:namespace`;
    - nothing else: the ECS task execution role is authored explicitly (`IngestExecutionRole`)
      with `ecr:GetAuthorizationToken` on `*` conditioned on `aws:RequestedRegion`, ECR pulls
      on the asset repository ARN and logs on its own log group, and is passed as
      `executionRole` so CDK generates no unconditioned grant.
  - `no_xray_permissions`
  - `ingest_task_uses_isolated_group_only`
  - `ingest_sg_has_no_ingress_and_no_public_egress`
  - `no_public_ip`
  - `no_vpc_or_endpoint_created`
  - `agent_data_role_versions_only`
  - `task_role_has_no_bedrock_and_no_data_objects`
  - `runtime_role_bedrock_model_scope`
  - `gateway_grant_only_with_web_search`
  - `web_search_context_consistency`: region and gateway-ID mismatch throw.
  - `dispatcher_not_in_vpc_and_scoped`: invoke on the runtime, `GetObject` only on rerun
    job keys, `GetItem` only on `ACCT#*`; `maxEventAge` 15 min on the function and target.
  - `bucket_and_table_not_created`
  - `outputs_are_not_exported`
  - `asset_paths_resolve_from_dirname`: synth from a different `cwd` succeeds.
- [ ] **Step 3: Run** `cd infra && npm test -- billing-analysis`. Expected: FAIL.
- [ ] **Step 4: Implement**, then run `npx cdk synth` (with and without analysis or
  acceptance context) and `npm test`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(infra): Billing agent runtime and ingest (triggers disabled)`.

### Task 8: CI, documentation and the Phase 4 cut-over design

**Files:**
- Modify `.github/workflows/deploy-billing.yml` (written by Phase 1 with paths
  `services/billing/**`, `infra/lib/billing-stack.ts` and the workflow file):
  - add `infra/lib/billing-analysis.ts`, `infra/bin/infra.ts` and `infra/cdk.json`
    to the paths;
  - add a `worker-test` job with the fork guard, needed by deploy;
  - run `dispatch/package.sh` before synth;
  - keep the fork guard on every job.
- Modify `.github/workflows/test-infra.yml` (and any other workflow that
  synthesizes the app): run `dispatch/package.sh` before synth.
- Modify `docs/INFRA-SPEC.md`, `docs/README.md`, `services/billing/README.md`.
- Create `docs/runbooks/billing-phase2-release.md`, with a section
  "Phase 4 cut-over procedure (design; executed in Phase 4)" covering:
  - **Gates before Phase 4 starts:**
    - (G1) the `logs` interface endpoint **and its companion endpoint SG**
      are re-homed to `FsiDemoNetworkStack`, or retained with equivalent
      connectivity, before any companion deletion; the same for `lambda` and
      `secretsmanager` if still used;
    - (G2) a runtime endpoint `live` pinned to a named runtime version exists, and
      ingest and dispatcher use `qualifier=live`. Promotion means repointing `live`
      after acceptance on `DEFAULT`; rollback means repointing to the previous version;
    - (G3) the What's New collector plan: disable the companion `Daily` schedule
      before enabling the new one.
  - **Drain:**
    1. Record T0 and disable the companion `JobCreated` rule through a companion deploy.
    2. Wait until the companion has zero `RUNNING`/`PENDING` worker tasks and its
       rule's `FailedInvocations` and retry window have stayed quiet for 30 min
       (EventBridge retries up to the rule's retry policy).
  - **Manifest** (read-only tool, written in Phase 4), listing each item with its
    disposition:
    - every `jobs/` object with `LastModified ≥ T0 − 4 h` whose UP row is not
      terminal (`done`, `needs_review`, `failed`, `discarded`);
    - every `jobs/r*` object after T0 whose own `RUN#{jobId}` is absent or `running`
      (job-specific evidence; META `ai_<src>` is shared state that other versions and
      rollbacks rewrite, so it is never used as completion evidence);
    - every UP row `queued`/`running`.
  - **Activate:** merge `ttobak:billingJobsEnabled: "true"`, deploy, confirm the
    rules are `ENABLED` and record T1 (confirmed activation).
  - **Final manifest:** collect the manifest again **after** T1 with the window
    `LastModified ≥ T0 − 4 h` through `T1 + 5 min`, so jobs written while neither
    rule was active (between the drain and T1) are included. Replay works from this
    final manifest, not the first one.
  - **Replay:** for each final-manifest item still unresolved, copy the job object onto
    itself, which emits an Object Created event. `RUN#` and the claim make a replay
    of anything already owned a no-op.
  - **Rollback:** disable the new rules, then drain the new side:
    1. **Fence every outstanding job first.** For each item of the new side's manifest
       whose `RUN#{jobId}` is absent, write the fence (`state = fenced`,
       `attribute_not_exists`). A queued Lambda async retry or a late EventBridge
       delivery that arrives later finds `fenced` and returns without running, so
       nothing can acquire after this point.
    2. Wait until there are no running ingest tasks, no `RUN#` in `running` younger
       than the job timeout, and at least the 15 min `maxEventAge` has passed since the
       rules were disabled.
    3. Re-enable the companion rule and replay the fenced items through the companion
       the same way. A fenced job is never replayed to the new side.
    - Test (Phase 4 tool): `test_rollback_fences_before_reenable`: a dispatcher retry
      delivered after the fence does not acquire `RUN#`.

- [ ] **Step 1: Failing `scripts/ci/test_billing_workflows.py`** (unittest, parses YAML):
  - `test_every_self_hosted_job_has_fork_guard`: every job in `test-billing.yml`
    and `deploy-billing.yml` with `runs-on: ttobak-arm` has the exact guard.
  - `test_deploy_billing_exclusive_single_stack`
  - `test_deploy_billing_paths_include_worker_and_context`
  - `test_deploy_needs_worker_tests`
  - `test_dispatch_packaged_before_synth`
- [ ] **Step 2: Run, implement, run**; then run `python3 scripts/docs/check_docs.py`. Expected: PASS.
- [ ] **Step 3: Commit** `ci(billing): build and test the worker before deploy`.

### Task 9: Deploy and accept (standby only; owner approval per step)

**Files:** Modify `docs/runbooks/billing-phase2-release.md` (dated evidence; no
tokens, real Account names or customer data) and `docs/README.md`.

- [ ] **Step 1: Preflight (read-only).**
  - Identity is account 180294183052.
  - The required endpoints exist (Pre-flight list), and the `logs` interface
    endpoint's security groups admit 443 from the Isolated subnets or from
    `IngestSg` (`describe-security-groups` on the endpoint's SGs). If not, stop:
    the owner decides between a rule on the endpoint SG (outside this plan) and
    pulling gate G1 forward.
  - The platform table's TTL is enabled on attribute `ttl`
    (`aws dynamodb describe-time-to-live`). If not, stop and ask the owner; `RUN#`
    and `RERUN#` rows rely on it.
  - The companion `JobCreated` rule is `ENABLED` and its pattern is `jobs/` (unchanged).
  - U1: sizes of the largest `accounts/*/versions/*/data/` prefixes (`aws s3 ls --summarize`; sizes only).
  - `npx cdk diff TtobakBillingStack` (U3).
- [ ] **Step 2: Reserve two synthetic Accounts** (`SYN-A` and `SYN-B`).
  - They are created through the normal TTOBAK Account API by the owner, with
    synthetic names and only test users as members.
  - They have no real data and no companion job history.
- [ ] **Step 3: Deploy standby with acceptance.**
  - Merge a PR adding to `infra/cdk.json`:
    - `ttobak:billingAnalysisEnabled: "true"`;
    - the web-search keys (from `TtobakWebSearchGatewayStack` outputs);
    - `ttobak:billingAcceptancePrefix: "billing-acceptance/"`;
    - `ttobak:billingAcceptanceAccountId: "<SYN-A id>"`.
  - `ttobak:billingJobsEnabled` stays absent.
  - `deploy-billing.yml` deploys `TtobakBillingStack --exclusively`.
- [ ] **Step 4: Acceptance.** Every job key is under `billing-acceptance/`, which
  the companion rule cannot match. Never write to `jobs/`.
  - Rules: the production rules are `DISABLED`, the acceptance rules are `ENABLED`,
    and the companion rule is `ENABLED` with its pattern unchanged.
  - `GetAgentRuntime`: `READY`, `PUBLIC`, IAM. The role policy has no X-Ray grant.
  - Analyze (SYN-A):
    1. Upload a synthetic COT zip to `accounts/SYN-A/uploads/<id>/source.zip`.
    2. Create the `UP#` row (`queued`) and the `LOCK#cot` row exactly as the BFF's
       `complete` would, using the runbook's JSON.
    3. Write `billing-acceptance/u<id>.json` (the `u` analyze discriminator).
    4. Check that the `/billing?view=upload` page shows the same step progression,
       the version becomes current with `ai` set, the lock is gone, and there is one
       `RUN#` row, `done`.
    5. The companion worker log shows no task for this key.
  - Rerun (SYN-A): write `billing-acceptance/r<ver>-<ts>.json`.
    - The dispatcher invokes the runtime and the agent files are rewritten.
    - META `ai_cot` is set and `RUN#` is `done`.
    - Writing the same key again (a replay) changes nothing.
  - Rejections:
    - an acceptance key naming SYN-B or any other Account exits 2 or is rejected,
      with no writes;
    - a direct `InvokeAgentRuntime` with a forged claim changes nothing;
    - a second invoke with another job in the same session gives 409.
  - Live scope check:
    - `aws ecs run-task` on `ttobak-billing-ingest` with a command override
      `python3 -I worker/selfcheck.py SYN-A SYN-B`, using the Isolated subnets,
      `IngestSg` and no public IP.
    - On the assumed session it expects `AccessDenied` for Get (on an object that
      exists under SYN-B), List and Put under `accounts/SYN-B/`, and success for
      List under `accounts/SYN-A/`. It exits 0 only if all hold.
    - The runtime's per-job list canary result is visible in the analyze run's logs
      (`scope_canary=denied`).
  - Idle termination: after the analyze job, `/ping` turns `Healthy`; within about
    300 s plus 15 s the session's environment is terminated (session metric /
    `ListSessions` or log stream end).
- [ ] **Step 5: Close acceptance.**
  - Merge a PR removing both acceptance keys and deploy; the acceptance rules are deleted.
  - Delete the `billing-acceptance/` objects. SYN-A and SYN-B data stays, or is
    deleted on the owner's decision.
- [ ] **Step 6: Report** to the owner. Production activation is Phase 4, following
  the Task 8 design.

---

## Follow-on plans (not in this plan)

- **Phase 4:** gates G1–G3, then drain, manifest, activation, replay and the rollback rehearsal.
  - Collector transfer (disable the old schedule first).
  - FsiSearch Lambda retirement.
  - Redirects and companion stack retirement only after G1.
- The companion worker selects `SubnetType.PRIVATE_ISOLATED`. A fresh lookup could
  include the `bank-*` groups. It stays untouched until Phase 4 retires it.
