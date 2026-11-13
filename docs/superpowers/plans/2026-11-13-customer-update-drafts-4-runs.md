# Customer Update Drafts, Plan 4: Run worker, context packs and drafts

Status: historical implementation plan for 2026-11-13; current behavior is in the code and ADR-026.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn each user's new inbox items into stored drafts: build authorized context packs, start an agent run, then validate and materialize its results.

**Architecture:** The `ttobak-customer-updates` Go Lambda's tick gains two phases after fan-out: finalize finished runs, then start new ones. Go is the trust boundary. It builds context packs with existing authorization, passes them in the invocation payload, and re-validates every result row (membership, budgets, deterministic draft checks) before writing `USER#` rows. AWS service names come from a names-only object the billing worker derives at publish time.

**Tech Stack:** Go 1.27, AWS SDK v2 (`bedrockagentcore`, S3, DynamoDB), Python (billing worker, eval scripts), CDK.

**Spec:** `docs/superpowers/specs/2026-11-13-customer-update-drafts-design.md`

**Depends on:** Plans 1, 2 and 3 merged; the Plan 3 runtime created by the owner (its ARN is needed in Task 6).

## Global Constraints

- Every earlier plan's constraints apply.
- Accounts in scope: the user is a direct member (owner included); the per-account setting is on (default on); at least one project has `SfdcOpptyID != ""` and `!model.IsTerminalStage(stage)` (an unset stage is open). Ancestor or Billing read alone never qualifies. At most 40 accounts per run.
- Context pack: at most 6,000 UTF-8 bytes per account when serialized. Shrink order: notes, then insights, then news from the tail.
- Pack contents: up to 15 service names; up to 5 news items (title, summary capped at 200 runes) from the last 30 days; open opportunities (`projectId`, `name`, `stage`); up to 5 insights of type need, opportunity, risk or tech from the last 90 days (text capped at 200 runes); notes from the 2 newest meetings the user may read (600 runes each), included only when `CUSTOMER_UPDATE_INCLUDE_NOTES=1` (default `1`; the owner has not confirmed notes, so this must be switchable without a code change).
- Budgets: 30 items per run; 20 drafts per user per KST day; 3 per account per KST day. Run lease 30 minutes. An item that fails 3 runs becomes `skipped`.
- Billing boundary (ADR-012): the worker reads exactly one object per account, `accounts/{accountId}/derived/services-in-use.json`, which contains service names only. No amounts, no other billing object, no call to the Billing BFF.
- Nothing from a run row is trusted: `urlHash` must belong to the run, `accountId` must have been in the run's packs and must still be in scope at finalize time.
- Logs carry `runId`, hashes and counts only.

## Review Focus

1. A user removed from an Account while a run is in flight must get no draft for that Account when the run finalizes.
2. Two overlapping ticks must not start two runs for one user, and must not finalize one run twice.
3. A run that never writes `DONE` must release its items after the lease, and an item must not loop forever.
4. A result row naming an account or URL that was not part of the run must be ignored and counted, not written.
5. An account without billing data must still be decided on news and opportunities, and a missing billing object must not fail the run.

---

## File Structure

| File | Responsibility |
|---|---|
| `services/billing/worker/worker/main.py` | Write the derived names-only object at publish |
| `backend/internal/service/customer_update_services.go` | `ServicesInUseReader` (S3) |
| `backend/internal/service/customer_update_context.go` | `CustomerUpdateContext`: scope and pack building |
| `backend/internal/service/customer_update_checks.go` | Go `CheckUpdateDraft` (same contract file as Python) |
| `backend/internal/repository/customer_update_run.go` | Run lease, day counters, pending list, run-row reads |
| `backend/internal/service/customer_update_runs.go` | `CustomerUpdateRuns`: start and finalize |
| `backend/cmd/customer-updates/main.go` | Tick: fan-out → finalize → start |
| `scripts/customer-updates/export_labels.py`, `backend/python/customer-update-agent/eval_gate.py` | Evaluation |
| `docs/decisions/ADR-026-customer-update-drafts.md` | Topic record |

---

### Task 1: Names-only services object from the billing worker

**Files:**
- Modify: `services/billing/worker/worker/main.py` (`publish`), a test under `services/billing/worker/tests/`
- Modify: `docs/decisions/ADR-012-billing-origin-identity-and-team-access.md`

**Interfaces:**
- Produces S3 object `self.s3_key("derived/services-in-use.json")`, content type `application/json`:

```json
{"version":1,"source":"cot","versionId":"…","generatedAt":"2026-11-13T00:00:00Z","services":["Amazon EC2","Amazon Bedrock"]}
```

- `services_in_use(services_rows: list[dict], limit: int = 30) -> list[str]`: sums `m6` per `service` across entities, sorts descending, returns names only, drops empty names and services whose sum is not positive.

- [ ] **Step 1: Write failing tests:** rows for two entities sharing a service are summed; order is by total; the output has no numeric field; a zero-total service is dropped; more than 30 services are truncated.
- [ ] **Step 2: Run** the billing worker tests as its README describes. Expected: FAIL.
- [ ] **Step 3: Implement** and call it from `publish` after the `services` dataset is uploaded. A failure to write the derived object is logged and does not fail the publish.
- [ ] **Step 4: Run again.** Expected: PASS.
- [ ] **Step 5: Update ADR-012** with the derived object, its content rule and its single extra reader. Commit `feat(billing): derive names-only services-in-use object`.

### Task 2: Draft checks in Go

**Files:**
- Create: `backend/internal/service/customer_update_checks.go`, `customer_update_checks_test.go`

**Interfaces:**
- Term lists are mirrored from `contracts/customer-updates/draft-terms-ko-v1.json`, with a test asserting the mirror.
- Produces: `CheckUpdateDraft(in UpdateDraftCheckInput) []string` with `UpdateDraftCheckInput{Body string; Links, AllowedURLs, BlockList, Stages []string}`, returning the same failure codes as Plan 3 Task 2.

- [ ] **Step 1: Write the failing test** `TestCheckUpdateDraftContract` that loads `contracts/customer-updates/draft-checks-v1.json` and asserts every case's codes as a set.
- [ ] **Step 2: Run** `(cd backend && /usr/local/go/bin/go test ./internal/service -run CheckUpdateDraft -count=1)`. Expected: FAIL.
- [ ] **Step 3: Implement.** Reuse the internal-host suffixes from `model`.
- [ ] **Step 4: Run again.** Expected: PASS.
- [ ] **Step 5: Commit** `feat(customer-updates): add Go draft checks`.

### Task 3: Scope and context packs

**Files:**
- Create: `backend/internal/service/customer_update_services.go`, `customer_update_context.go`, tests beside each

**Interfaces:**
- Consumes: `(*AccountService).ListAccounts`, `ListAccountInsights`, `ListAccountMeetings`; `(*ProjectService).ListAccountProjects`; `(*AccountNewsService).CurrentNews` (authorize first: it does no authorization itself); the direct-member check from Plan 1; `GetAccountOpt`; a meeting read that enforces the caller's access and returns `Meeting.Notes`; `capRunes`.
- Produces:
  - `ServicesInUseReader` interface: `ServicesInUse(ctx, accountID string) ([]string, error)`; the S3 implementation `NewS3ServicesInUseReader(client, bucket string)` returns `nil, nil` when the object is missing or the bucket is unset, validates `version == 1`, and caps the list at 15.
  - `AccountPack` with JSON fields exactly as `contracts/customer-updates/run-v1.json` (`accountId`, `name`, `services`, `opportunities`, `news`, `insights`, `notes`, `blockList`, `draftsRemaining`).
  - `NewCustomerUpdateContext(deps CustomerUpdateContextDeps, now func() time.Time) *CustomerUpdateContext`
  - `(*CustomerUpdateContext).Build(ctx, userID string, dayCounts map[string]int) ([]AccountPack, error)`
  - `(*CustomerUpdateContext).InScope(ctx, userID, accountID string) (bool, error)` (used again at finalize)
- `blockList`: names of the user's other accounts and display names of this account's members. Names shorter than 2 runes are left out.

- [ ] **Step 1: Write failing table-driven tests** with mock dependencies: a non-member account is excluded; a disabled setting excludes; only projects with `SfdcOpptyID` and a non-terminal stage count, and an unset stage counts; an account with no open opportunity is excluded; missing billing data yields an empty `services` and no error; a pack over 6,000 bytes loses notes first, then insights, then trailing news; `CUSTOMER_UPDATE_INCLUDE_NOTES=0` produces no notes and makes no meeting read; `draftsRemaining` is `3 - dayCounts[accountID]` and an account at 0 is excluded; a meeting the user cannot read contributes nothing; the 41st account is dropped.
- [ ] **Step 2: Run** `(cd backend && /usr/local/go/bin/go test ./internal/service -run 'UpdateContext|ServicesInUse' -count=1)`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run again.** Expected: PASS.
- [ ] **Step 5: Commit** `feat(customer-updates): build authorized context packs`.

### Task 4: Run repository

**Files:**
- Create: `backend/internal/repository/customer_update_run.go` and test
- Modify: `backend/internal/model/customer_update.go` (add `UpdateRun`, `UpdateDay`, the run-row types, and two `UpdateDecision` fields: `GateInput *UpdateGateInput{Question string; Options []string; Context string}` and `DiscardReason string`)

**Interfaces:**
- Rows: `USER#{u}` / `CURUN` → `UpdateRun{RunID, StartedAt, LeaseUntil, ItemHashes []string, AccountIDs []string}`; `USER#{u}` / `CUDAY#{yyyymmdd}` → `UpdateDay{Total int, PerAccount map[string]int}` with a 3-day TTL (the day is KST).
- Produces:
  - `ListPendingUsers(ctx, limit int) ([]string, error)`, `ClearPending(ctx, userID string) error`
  - `ListNewItems(ctx, userID string, limit int) ([]model.UpdateItem, error)` (oldest first)
  - `SetItemState(ctx, userID, urlHash, state string, attemptsDelta int) error`
  - `CreateRunIfAbsent(ctx, userID string, r *model.UpdateRun) error` (`attribute_not_exists`; conflict → `ErrConditionFailed`)
  - `GetRun(ctx, userID string) (*model.UpdateRun, error)`, `DeleteRunIfMatch(ctx, userID, runID string) error`
  - `ReadRunRows(ctx, runID string) (*model.RunRows, error)` with `RunRows{Done bool; Items []RunItemRow; Decisions []RunDecisionRow; Drafts []RunDraftRow}`, paginated, at most 2,000 rows
  - `DeleteRunRows(ctx, runID string) error`
  - `GetDay(ctx, userID, day string) (*model.UpdateDay, error)`, `AddDayDraft(ctx, userID, day, accountID string, maxTotal, maxPerAccount int) error` (conditional increment; over a limit → `ErrConditionFailed`)
  - `PutDecisionIfAbsent(ctx, userID string, d *model.UpdateDecision) error`

- [ ] **Step 1: Write failing tests** on the extracted builders: `CreateRunIfAbsent` carries `attribute_not_exists(PK)`; `DeleteRunIfMatch` conditions on `runId`; `AddDayDraft` conditions on both limits; `ReadRunRows` classifies `ITEM#`, `DEC#`, `DRAFT#` and `DONE` sort keys and rejects malformed ones.
- [ ] **Step 2: Run** `(cd backend && /usr/local/go/bin/go test ./internal/repository -run CustomerUpdateRun -count=1)`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run again.** Expected: PASS.
- [ ] **Step 5: Commit** `feat(customer-updates): add run repository`.

### Task 5: Start and finalize runs

**Files:**
- Create: `backend/internal/service/customer_update_runs.go` and test
- Modify: `backend/cmd/customer-updates/main.go` and test

**Interfaces:**
- Consumes: Tasks 2-4; Plan 1 `PutDraftIfAbsent`; an `AgentInvoker` interface `Invoke(ctx, runID string, payload []byte) error` whose implementation calls `bedrockagentcore.InvokeAgentRuntime` as `cmd/research-worker/main.go` does (session id derived from `runID`, qualifier from `CUSTOMER_UPDATE_ENDPOINT_NAME`, default `DEFAULT`).
- Produces: `NewCustomerUpdateRuns(deps, now) *CustomerUpdateRuns`; `(*CustomerUpdateRuns).Tick(ctx) (RunStats, error)` with `RunStats{Users, Started, Finalized, Expired, Drafts, Discarded, Ignored int}`. `Tick` handles at most 25 pending users and stops starting work when less than 60 seconds remain before the context deadline.

Per pending user:

1. **A run exists.** Read its rows.
   - `Done` → finalize (below), then delete run rows and the run with `DeleteRunIfMatch`. `ErrConditionFailed` there means another tick finalized it: stop for this user.
   - Not done and `LeaseUntil` passed → every run item gets `attempts+1` (state `skipped` at 3), delete run rows and the run; count `Expired`.
   - Otherwise skip the user.
2. **No run.** Load up to 30 `new` items. None → `ClearPending`. Day total at 20 → skip. Build packs; no account in scope → mark the items `skipped` and continue. Create the run with `CreateRunIfAbsent` (conflict → skip), then invoke. An invoke error deletes the run and leaves the items `new`.

Finalize:

- A decision row is accepted only if its `urlHash` is in `ItemHashes` and its `accountId` is in `AccountIDs`; otherwise count `Ignored`. Accepted decisions are written with `PutDecisionIfAbsent` (TTL 180 days), keeping `gateInput` only for `needed`.
- A draft row is accepted only if: its pair has an accepted `needed` decision; `InScope` is still true; `CheckUpdateDraft` returns no codes (allowed URLs are the row's `fetchedUrls` plus the item URL; block list and stages are rebuilt from a fresh pack); and `AddDayDraft` succeeds. Then write `PutDraftIfAbsent` with `Opportunities` resolved from the current pack by `opportunityIds`, `Status new`, `Revision 0`, TTL 90 days. A rejected draft sets the decision outcome to `discarded` with the reason; a budget rejection sets it to `deferred`.
- Item rows set the inbox item state. `deferred` leaves the item `new` with `attempts+1`.

The Lambda `tick` runs fan-out, then `Tick`; a failure in one phase is returned after the other phases ran.

- [ ] **Step 1: Write failing tests** with mocks: no run and 2 items → run created and invoked with a payload that validates against the contract example's shape; invoke error → run deleted, items untouched; `CreateRunIfAbsent` conflict → no invoke; `Done` run with one valid draft → one draft written and counters incremented; a draft for an account now out of scope → not written, decision `discarded`; a draft failing `figure` → `discarded`; the 21st draft of the day → `deferred`; a decision naming an unknown account → `Ignored 1`; lease expired without `DONE` → attempts incremented, third time `skipped`; `DeleteRunIfMatch` conflict → no second finalize.
- [ ] **Step 2: Run** `(cd backend && /usr/local/go/bin/go test ./internal/service ./cmd/customer-updates -run 'UpdateRuns|Handle' -count=1)`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `(cd backend && /usr/local/go/bin/go test ./... -count=1 && /usr/local/go/bin/go vet ./...)`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(customer-updates): start and finalize agent runs`.

### Task 6: Infrastructure, ADR and docs

**Files:**
- Modify: `infra/lib/customer-updates.ts` and test, `infra/bin/infra.ts` (the runtime ARN constant next to the research one)
- Create: `docs/decisions/ADR-026-customer-update-drafts.md`
- Modify: `docs/decisions/README.md` (index), `docs/INFRA-SPEC.md`, `docs/architecture.md`, `CLAUDE.md` (one feature-invariant bullet), `docs/runbooks/customer-update-agent.md`

Worker additions:

| Addition | Value |
|---|---|
| Env | `CUSTOMER_UPDATE_RUNTIME_ARN`, `CUSTOMER_UPDATE_ENDPOINT_NAME=DEFAULT`, `BILLING_DATA_BUCKET` (from SSM `/ttobak/billing/data-bucket-name`), `CUSTOMER_UPDATE_INCLUDE_NOTES=1` |
| IAM | `bedrock-agentcore:InvokeAgentRuntime` on the runtime ARN and `ARN/*`; `s3:GetObject` on `arn:aws:s3:::{bucket}/accounts/*/derived/services-in-use.json` only; DynamoDB leading keys gain `CURUN#*`, plus read on `ACCOUNT#*` and `PROJECT#*`-family rows the context services use (list the exact prefixes found while wiring, with the `Null` guard) |
| Timeout | 5 minutes, reserved concurrency 1 (unchanged) |

ADR-026 records: Slack bodies stay on the Mac and only filtered public URLs are uploaded; per-URL sharing with no submitter; the runtime's `CURUN#`/`CUURL#`-only role and Go as the trust boundary; the researcher's limited input and the accepted egress of opportunity and service names in search queries; the names-only billing object; drafts are owner-private, unsent and expire after 90 days; the swappable gate and the criteria for replacing luna.

- [ ] **Step 1: Write failing CDK assertions:** the S3 statement's resource ends with `/accounts/*/derived/services-in-use.json` and nothing broader; the invoke statement targets only the runtime ARN; no `Resource: "*"` without a condition.
- [ ] **Step 2: Run** `(cd infra && npm test)`. Expected: FAIL.
- [ ] **Step 3: Implement.** If the owner has not supplied the runtime ARN yet, stop and report: do not invent one.
- [ ] **Step 4: Run** `(cd infra && npx cdk synth && npm test)`, `python3 scripts/docs/sync_review_context.py && python3 scripts/docs/check_docs.py`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(customer-updates): wire run worker and record ADR-026`.

### Task 7: Evaluation tools

**Files:**
- Create: `scripts/customer-updates/export_labels.py`, `scripts/customer-updates/test_export_labels.py`, `backend/python/customer-update-agent/eval_gate.py`, `test_eval_gate.py`

**Interfaces:**
- `export_labels.py --user-id U --out labels.jsonl` (operator tool, boto3): joins `CUDEC#` rows that have `gateInput` with their draft status. Label rule: `adopted` → `needed`; `dismissed` with `not_relevant` → `not_needed`; every other status or reason is excluded. Output line: `{"question","options","context","label","lunaScore"}`.
- `eval_gate.py --labels labels.jsonl --backend luna|module:Class [--threshold 0.7]` prints `n`, precision, recall and the Korean-text subset size, and exits 1 when `n < 300` with the message `need at least 300 labelled pairs`.

- [ ] **Step 1: Write failing tests:** label mapping for the five status and reason combinations; a fake backend scoring fixed values gives the expected precision and recall; fewer than 300 pairs exits 1.
- [ ] **Step 2: Run** `python3 -m unittest discover -s scripts/customer-updates -p 'test_*.py' -v` and the agent directory's tests. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run again.** Expected: PASS.
- [ ] **Step 5: Commit** `feat(customer-updates): add gate evaluation tools`.

## Deployment and acceptance

Deploy order, each `--exclusively`: the billing stack that owns the worker (Task 1), then `TtobakCrawlerStack`. Acceptance on the owner's account: after one billing publish, the derived object exists and holds names only; register an RSS source whose recent post concerns a service one customer uses; within two ticks after the runtime finishes, a draft appears on that Account's drafts tab and none on an unrelated Account; removing yourself from a test Account hides its drafts.
