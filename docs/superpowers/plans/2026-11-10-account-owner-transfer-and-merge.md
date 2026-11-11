# Account Owner Transfer and Account Merge Implementation Plan

Status: historical implementation plan (2026-11-10); not a current reference.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let an Account Owner hand Ownership to another direct member, and let an
Owner merge a duplicate Account A into Account B, leaving A as a tombstone.

**Architecture:** Owner transfer is one conditional three-item TransactWrite
behind a new non-delegable route. Merge is a resumable job (`ACCOUNT#A / MERGE#job`)
started by a fenced transaction that marks A `mergingInto=B`; a summarize-function
worker (EventBridge `AccountMergeRequested`, the `PeopleResolveRequested` pattern)
runs per-category movers in bounded conditional transactions, and an operator CLI
runs the same movers plus user-partition scans.

**Tech Stack:** Go 1.27 (chi, aws-sdk-go-v2 DynamoDB expression builder, `internal/sealing`
KMS sealer), CDK TypeScript (`infra/lib/gateway-stack.ts`), Next.js 16 / React 19
(node:test scripts), Python contract generator.

**Spec:** `docs/superpowers/specs/2026-11-10-account-owner-transfer-and-merge-design.md`.
The "Rulings" section below overrides the spec where it says so.

## Global Constraints

- Deliver Part A (owner transfer) before Part B (merge). Each PR diff ≤ 95,000 bytes
  (`git diff origin/main... | wc -c`); documentation rides with the code it describes.
- Go builds and tests run only on the EC2 build host (`/home/ec2-user/ttobak`); sync the
  branch there. Run `(cd backend && /usr/local/go/bin/go test ./... -count=1)` and
  `go vet ./...` including `cmd/*`. Tests are stdlib, table-driven, with mock repos;
  repository tests use the `hierarchyWireRepo`-style captured-TransactWrite fake.
- Every TransactWrite ≤ 100 items. DynamoDB writes use the expression builder.
- Sentinel errors with `errors.Is`; API error shape `{ "error": { "code", "message" } }`.
- New routes are authenticated and `delegationAllowed: false` in
  `scripts/connector/generate_contract.py`; regenerate with
  `python3 scripts/connector/generate_contract.py` and pass
  `go test ./internal/connectorinventory`.
- Owner is granted only by the explicit transfer route (spec anti-goal). `AddMember`,
  `UpdateMemberRole` and `AssignableRoles` stay owner-free.
- Meeting owners never change; S3 objects never move; no undo.
- Previous owner's role after transfer: `model.RoleSA`. A's owner joining B without
  existing B membership: `model.RoleSA`.
- Repository text and docs are English (`check_docs.py` rejects Hangul). UI copy is Korean;
  each task exports its strings from one `const …_COPY` object in the component and the
  test imports that object, so the docs name strings by English meaning only.
- Never `cdk deploy --all`; GatewayStack changes deploy with `--exclusively`.
- After editing `CLAUDE.md`: `python3 scripts/docs/sync_review_context.py`, then
  `python3 scripts/docs/check_docs.py`.

## Rulings on spec gaps and corrections

1. **Owner checks are dual.** Code requires both MEMBER `role=owner` and META
   `ownerUserId` (`UpdateAccountParent`); `RemoveMember`/`RevokePendingMember` check
   only the MEMBER role. Transfer conditions on both; merge authorization uses both.
2. **Transfer target identity.** Target must have a MEMBER row (`userId` set) and, via the
   wired Cognito client, `AdminGetUser(Username=userId)` must be `Enabled` with status
   `CONFIRMED`. Unwired Cognito fails closed (`ErrOwnerTransferUnavailable`, 503).
3. **No existing "operator path" can move another user's meeting.**
   `actorAccountCondition` lets a non-creator act only on a meeting already published to
   the target account, and the generic writer rejects unsafe summaries
   (`ErrSummarySharingUnsafe`) on a new-account publish. Merge adds a dedicated writer
   `MoveMeetingForAccountMerge` (Task B5.1) governed by ADR-072.
4. **Account-origin Share rows are tagged with `accountId=A`.** `CreateShareIfMember(B)`
   will not overwrite them (condition `accountId = :B`), so the meeting mover retags
   them A→B explicitly, then grants B members.
5. **Pending A invites cannot be enumerated** (PK `PENDING_SHARE#{email}`, no account
   index). Instead of copying, `MaterializePendingAccountGrant` redirects a
   `PENDING_ACCOUNT#A` grant to `A.mergedInto` (one hop) at sign-in.
6. **Stale `MEETINGREF#`/`INSIGHT#` rows on A are left in place.** Readers already
   revalidate `meeting.accountId == A && sharedToAccount` (`meetingPublishedToAccount`),
   so moved meetings disappear from A. No `DeleteMeetingRef` exists today; the mover
   deletes A's ref in the same transaction anyway (cheap, keeps resume idempotent).
7. **Children reparenting needs its own writer.** `UpdateAccountParent` requires the
   child's Owner. Merge adds `ReparentAccountForMerge` fenced by the merge job and B's
   ancestor chain. Preview/start reject when `depth(B) + subtreeHeight(A) >
   AccountHierarchyMaxAncestors` (64).
8. **Account events reference contacts** (`EventAttendee.ContactID`). When a contact is
   deduplicated into an existing B contact, the event mover remaps the ID through
   persisted `MERGE#contact#{aContactId}` rows.
9. **No account-wide link refresh exists** (ADR-064 links are created per news/meeting by
   `LinkAuto`). v1 only emits `PeopleResolveRequested{AccountID: B, Reason: contacts}`;
   A's links stay on the tombstone.
10. **Audience is symmetric.** The spec's anti-goal names only A's members gaining B's
    content, but option ① also gives B's direct members (and ADR-058 ancestor readers of
    B) A's moved meetings. Preview returns `newAudience` and the UI shows it; no further
    flag. ADR-072 records this.
11. **Write fence** is a META condition `attribute_not_exists(mergingInto) AND
    attribute_not_exists(mergedInto)` added to every transaction that already checks
    A's META, plus new META checks on member add and meeting classification/publication.
    `PutMember` becomes a two-item transaction. Fenced writes return
    `ErrAccountMerging` → 409 `ACCOUNT_MERGING`.
12. **Concurrent merges.** Start increments `incomingMerges` on B META; an account with
    `incomingMerges > 0` cannot start its own merge; finish decrements it.
13. **Status route added**: `GET /api/accounts/{A}/merge` for A's and B's direct members
    (the spec has no progress read; the UI needs one).
14. **Tombstone visibility.** `ListAccounts` hides `mergedInto` accounts; `GetAccount` for
    A returns `mergedInto`, and the web page redirects to B. A's MEMBER rows remain so
    unmoved categories stay readable through direct links.
15. **SRTASK#/CALEVENT# rows and link-only meetings** live in user partitions; only the
    CLI (`-scan-users`) rewrites them. The API worker reports them as `cliOnly` in preview.

## Review Focus

1. Merge requester loses Owner of A or B membership mid-job — every mover transaction
   must fail (`ErrAccountMerging`/`ErrForbidden`), the job becomes `failed` with
   `REQUESTER_REVOKED`, nothing partially published. Test in Task B6.1.
2. Worker crash between a mover transaction and its cursor update — re-run must be a
   no-op for already moved items (no duplicate contact, share, member). Test per mover
   (`…_Idempotent`).
3. Transfer races a concurrent role change or removal of the target — transaction fails
   with `ErrOwnerTransferConflict` (409), no half-applied role. Test in Task A1.
4. A meeting whose summary is not sharing-safe and has no approval bound to A — mover must
   keep it published only if it was published to A, never newly publish a link-only one.
   Test in Task B5.1.
5. Contact dedupe with an email that differs only by case/whitespace, or phone with
   hyphens — must merge, not duplicate. Test in Task B4.1.

---

## Part A — Owner transfer

### PR A1: backend, route, contract, ADR-071 (~45 KB)

### Task A1: Repository transfer transaction

**Files:**
- Create: `backend/internal/repository/account_owner_transfer.go`
- Test: `backend/internal/repository/account_owner_transfer_test.go`

**Interfaces:**
- Produces: `func (r *DynamoDBRepository) TransferAccountOwner(ctx context.Context, accountID, currentOwnerID, targetUserID string, at time.Time) error` — returns `ErrConditionFailed` (wrapped) on any cancelled condition.

- [ ] **Step 1: Write the failing test** `TestTransferAccountOwnerTransaction` using
  `hierarchyWireRepo`. Assert exactly 3 items:
  - Update `ACCOUNT#a/META`: condition contains `attribute_exists(PK)` and
    `ownerUserId = "u-old"`; update sets `ownerUserId = "u-new"`, `updatedAt`, and
    REMOVEs `initialOwnerEmail`, `initialOwnerInvitationPending`.
  - Update `ACCOUNT#a/MEMBER#u-new`: condition `attribute_exists(PK)`, `userId = "u-new"`,
    `role <> "owner"`; sets `role = "owner"`.
  - Update `ACCOUNT#a/MEMBER#u-old`: condition `role = "owner"`; sets `role = "SA"`.
  Add `TestTransferAccountOwnerConditionFailure`: a 400
  `TransactionCanceledException` body maps to `errors.Is(err, ErrConditionFailed)`.
- [ ] **Step 2: Run** `go test ./internal/repository -run TestTransferAccountOwner -count=1` — FAIL (undefined).
- [ ] **Step 3: Implement** reusing `accountHierarchyKey` and `writeAccountHierarchy`
  (already maps cancellation to `ErrConditionFailed`).
- [ ] **Step 4: Run** the same command — PASS.
- [ ] **Step 5: Commit** `feat(accounts): add conditional owner transfer transaction`.

### Task A2: Service, handler, route, contract

**Files:**
- Create: `backend/internal/service/account_owner_transfer.go`, `backend/internal/service/account_owner_transfer_test.go`
- Modify: `backend/internal/model/account.go` (DTO), `backend/internal/handler/account.go`, `backend/internal/handler/account_test.go`, `backend/cmd/api/main.go` (route after `/owner-invitation`), `scripts/connector/generate_contract.py` + regenerated `contracts/connector/v1/operations.json`, `backend/internal/connector/operations_generated.go`, `mcp-server/src/connector-operations.ts`, `backend/python/qa/connector_operations.py`

**Interfaces:**
- Consumes: `TransferAccountOwner` (Task A1).
- Produces:
  - `type TransferOwnerRequest struct { TargetUserID string \`json:"targetUserId"\` }`
  - `type TransferOwnerResponse struct { AccountID, OwnerUserID, PreviousOwnerUserID, PreviousOwnerRole string }` (json camelCase)
  - `var ErrOwnerTransferConflict, ErrOwnerTransferUnavailable error`
  - `type accountOwnerTransferRepo interface { TransferAccountOwner(...) error }` (type-asserted like `accountHierarchyRepo`)
  - `func (s *AccountService) TransferOwner(ctx context.Context, requesterID, accountID string, req *model.TransferOwnerRequest) (*model.TransferOwnerResponse, error)`
  - Handler `func (h *AccountHandler) TransferOwner(w, r)`; route `POST /api/accounts/{accountId}/owner-transfer`; contract row `["api.account.transferOwner", "ttobak-api-bridge", ["read", "share"], false, "POST", "/api/accounts/{accountId}/owner-transfer"]`.

- [ ] **Step 1: Write failing service tests** `TestTransferOwner` table:
  | case | expect |
  |---|---|
  | requester MEMBER role owner but META owner differs | `ErrForbidden` |
  | requester is AM | `ErrForbidden` |
  | target == requester | `ErrInvalidInput` |
  | target not a member | `ErrNotFound` |
  | empty/invalid IDs (`validateAccountHierarchyIDs`) | `ErrInvalidInput` |
  | Cognito user disabled / not CONFIRMED | `ErrInvalidInput` |
  | Cognito unwired | `ErrOwnerTransferUnavailable` |
  | repo returns `ErrConditionFailed` | `ErrOwnerTransferConflict` |
  | success | response `{OwnerUserID: target, PreviousOwnerRole: "SA"}`, repo called once |
  Add `TestTransferOwnerThenGenericRoutesStillOwnerFree`: after success, `UpdateMemberRole(req role "owner")` → `ErrInvalidInput`; `UpdateMemberRole` on the new owner → `ErrInvalidInput`; `ResendOwnerInvitation` → `ErrInvitationUnavailable` (cleared `InitialOwnerEmail`).
- [ ] **Step 2: Run** `go test ./internal/service -run 'TestTransferOwner' -count=1` — FAIL.
- [ ] **Step 3: Implement** `TransferOwner`: read META + both MEMBER rows (strong),
  check rule 1, Cognito rule 2 with a 10 s budget, call repo, map errors.
- [ ] **Step 4: Handler test** `TestTransferOwnerHandler`: body limited to 1 KiB with
  `DisallowUnknownFields`; unverified email → 403 (same gate as `ResendOwnerInvitation`);
  `ErrOwnerTransferConflict` → 409 `CONFLICT`; `ErrOwnerTransferUnavailable` → 503;
  others via `writeAccountHierarchyError`. Implement and register the route.
- [ ] **Step 5: Contract** — add the row, regenerate, run
  `python3 scripts/connector/generate_contract.py --check` and
  `go test ./internal/connectorinventory ./internal/handler ./internal/service -count=1` — PASS.
- [ ] **Step 6: Commit** `feat(accounts): add owner transfer route`.

### Task A3: ADR-071 and current references

**Files:**
- Create: `docs/decisions/ADR-071-account-owner-transfer.md`
- Modify: `docs/decisions/ADR-015-accounts-roles-and-hierarchy.md`, `docs/decisions/ADR-055-designated-account-owner.md` (one "Amended by ADR-071" bullet each), `docs/README.md` (ADR table row), `docs/API-SPEC.md` (Accounts section + Go route inventory), `CLAUDE.md` Accounts line, regenerated `AGENTS.md`

- [ ] **Step 1:** Write ADR-071 (Status: Accepted for implementation; Decision date
  2026-11-10; Amends ADR-015 and ADR-055): dual owner condition, target identity rule,
  previous owner → SA, cleared initial-owner designation, generic routes stay owner-free,
  non-delegable route.
- [ ] **Step 2:** Update `CLAUDE.md` Accounts bullet to: "Removal/revocation/reparenting
  are owner-only; Owner changes only through owner transfer (ADR-071)." Run
  `python3 scripts/docs/sync_review_context.py && python3 scripts/docs/check_docs.py` — PASS.
- [ ] **Step 3: Commit** `docs: record account owner transfer (ADR-071)`. Open PR A1.

### PR A2: web UI (~15 KB)

### Task A4: "Transfer owner" action

**Files:**
- Modify: `frontend/src/lib/api.ts` (`accountApi.transferOwner`), `frontend/src/components/AccountDetailClient.tsx` (members table), `docs/DESIGN-SPEC.md`
- Create: `frontend/scripts/account-owner-transfer.test.mjs`

**Interfaces:**
- Produces: `transferOwner: (id: string, targetUserId: string, expectedUserId: string) => invalidatingAccountList(api.post<{ accountId: string; ownerUserId: string; previousOwnerUserId: string; previousOwnerRole: string }>(\`/api/accounts/${encodeURIComponent(id)}/owner-transfer\`, { targetUserId }, { expectedUserId }))`

- [ ] **Step 1: Failing test** (render pattern of `account-inherited-controls.test.mjs`):
  owner viewer sees `OWNER_TRANSFER_COPY.action` (Korean for "Transfer owner") on each non-owner member row with a `userId`; an AM
  viewer and an inherited reader see none; pending rows (no `userId`) have none; clicking
  confirms with `window.confirm` text containing the target email and `OWNER_TRANSFER_COPY.previousOwnerNote` ("the previous owner becomes SA")
  and calls `transferOwner(accountId, userId, viewerId)` once, then reloads the account.
- [ ] **Step 2: Run** `(cd frontend && node --test scripts/account-owner-transfer.test.mjs)` — FAIL.
- [ ] **Step 3: Implement** a `handleTransferOwner(userId)` next to `handleRemoveMember`,
  gated by `isOwner && canAct()`; 409 shows `OWNER_TRANSFER_COPY.conflict` ("Members changed; refresh and retry").
- [ ] **Step 4: Run** `(cd frontend && npm run lint && npm run test:unit && npm run build)` — PASS.
- [ ] **Step 5:** DESIGN-SPEC members-table note; commit `feat(web): add account owner transfer action`; open PR A2.

---

## Part B — Account merge

Dependency order: B1 → B2 → B3 → B4 → B5 → B6 → B7 → B8. B1–B5 ship inert
(no route starts a job until B6).

### PR B1: model, tombstone readers, preview, ADR-072 (~70 KB)

### Task B1.1: Merge model and tombstone readers

**Files:**
- Create: `backend/internal/model/account_merge.go`
- Modify: `backend/internal/model/account.go` (META fields, `AccountResponse`, `AccountSummary`), `backend/internal/service/account.go` (`toAccountResponse`, `ListAccounts`, `ResolveAccountByAlias`), `backend/internal/repository/dynamodb.go` (`MaterializePendingAccountGrant`), tests in `backend/internal/service/account_merge_readers_test.go`, `backend/internal/repository/account_merge_pending_test.go`

**Interfaces:**
- Produces (model):
  - `Account` fields: `MergingInto string \`dynamodbav:"mergingInto,omitempty"\``, `MergedInto string \`dynamodbav:"mergedInto,omitempty"\``, `MergedAt *time.Time \`dynamodbav:"mergedAt,omitempty"\``, `IncomingMerges int \`dynamodbav:"incomingMerges,omitempty"\``.
  - `AccountResponse`: `MergingInto, MergedInto string` (`json:",omitempty"` camelCase).
  - `const SKAccountMergeJob = "MERGE#job"; PrefixMergeContactMap = "MERGE#contact#"`
  - `type AccountMergeJob struct { PK, SK, SourceAccountID, TargetAccountID, RequesterUserID, State, Step, Cursor, ErrorCode, LeaseOwner string; LeaseUntil int64; AcceptUnmoved bool; Counts map[string]int; StartedAt, UpdatedAt time.Time; FinishedAt *time.Time; EntityType string }` (`EntityTypeAccountMergeJob = "ACCOUNT_MERGE_JOB"`)
  - States `MergeStateRunning = "running"`, `MergeStateDone = "done"`, `MergeStateFailed = "failed"`.
  - `var MergeSteps = []string{"members", "children", "projects", "research", "contacts", "events", "meetings", "metadata", "finish"}`
  - `type AccountMergeContactMap struct { PK, SK, SourceContactID, TargetContactID, EntityType string }`

- [ ] **Step 1: Failing tests**:
  `TestListAccountsHidesMergedTombstone` (member of A and B → only B listed);
  `TestGetAccountReportsMergedInto` (`resp.MergedInto == "b"`);
  `TestResolveAccountByAliasSkipsTombstone` (A name == B alias → returns B, not ambiguous);
  `TestMaterializePendingAccountGrantRedirectsToMergeTarget` (pending row
  `PENDING_ACCOUNT#a`, A META `mergedInto=b` → transaction puts `ACCOUNT#b/MEMBER#u` with
  pending role, deletes the pending row; B itself tombstoned → no grant, row deleted).
- [ ] **Step 2: Run** `go test ./internal/service ./internal/repository -run 'Tombstone|MergedInto|RedirectsToMergeTarget' -count=1` — FAIL.
- [ ] **Step 3: Implement**; redirect follows exactly one hop and conditions B META on
  `attribute_not_exists(mergedInto)`.
- [ ] **Step 4: Run** — PASS. **Step 5: Commit** `feat(accounts): add merge tombstone model and readers`.

### Task B1.2: Eligibility and preview

**Files:**
- Create: `backend/internal/service/account_merge_preview.go`, `backend/internal/service/account_merge_preview_test.go`, `backend/internal/handler/account_merge.go`, `backend/internal/handler/account_merge_test.go`, `docs/decisions/ADR-072-account-merge.md`
- Modify: `backend/cmd/api/main.go`, contract generator + generated files, `docs/API-SPEC.md`, `docs/README.md`

**Interfaces:**
- Produces:
  - `type AccountMergeService struct` with `func NewAccountMergeService(repo accountMergeRepo, accounts *AccountService) *AccountMergeService`; `accountMergeRepo` grows per task (each task lists the methods it adds).
  - `func (s *AccountMergeService) authorize(ctx, requesterID, fromID, intoID string) (*mergeSubjects, error)` — `mergeSubjects{From, Into *model.Account; IntoAncestors []repository.AccountParentLink; SubtreeHeight int}`.
  - `func (s *AccountMergeService) Preview(ctx context.Context, requesterID, fromID, intoID string) (*model.AccountMergePreview, error)`
  - `type AccountMergePreview struct { FromAccountID, IntoAccountID string; Counts map[string]int; Unmoved []MergeUnmovedCategory; CLIOnly []string; NewAudience MergeAudience; RequiresAcceptUnmoved bool }`, `MergeUnmovedCategory{Category string; Present bool}`, `MergeAudience{IntoMembersNotInFrom int; IntoHasAncestors bool}`.
  - Errors: `ErrAccountMergeIneligible` (409 `MERGE_INELIGIBLE`, message names the reason), reuse `ErrForbidden`/`ErrNotFound`.
  - Route `GET /api/accounts/{accountId}/merge-preview?into={id}`; contract `["api.accountMerge.preview", "ttobak-api-bridge", ["read"], false, "GET", "/api/accounts/{accountId}/merge-preview"]`.

- [ ] **Step 1: Failing test** `TestAccountMergeAuthorize` table: A == B → `ErrInvalidInput`;
  requester not owner of A (either MEMBER role or META owner) → `ErrForbidden`; requester
  not direct member of B (inherited read only) → `ErrForbidden`; B is descendant of A →
  `ErrAccountMergeIneligible`; A or B `mergingInto`/`mergedInto` set →
  `ErrAccountMergeIneligible`; A `IncomingMerges > 0` → `ErrAccountMergeIneligible`;
  `depth(B)+subtreeHeight(A) = 65` → `ErrAccountMergeIneligible`; 64 → ok.
- [ ] **Step 2: Failing test** `TestAccountMergePreviewCounts`: counts for `members`,
  `meetings` (refs whose meeting is still A-bound), `projects`, `research`, `contacts`,
  `events`, `children`; `Unmoved` lists `newsSources`, `kb`, `documents`, `calendarShares`,
  `dictionary`, `billing` with `Present`; `CLIOnly == ["specreqTasks","calendarEvents","linkOnlyMeetings"]`;
  `RequiresAcceptUnmoved` true iff any `Present`. Preview performs no writes (mock
  write methods `t.Fatal`).
- [ ] **Step 3: Run** `go test ./internal/service -run TestAccountMerge -count=1` — FAIL;
  implement using existing list functions with page caps (contacts/events count capped at
  10,000, reported as `-1` when exceeded); subtree height walks `ListChildAccountIDsPage`
  breadth-first, at most 2,000 accounts, else ineligible.
- [ ] **Step 4: Handler test** `TestMergePreviewHandler` (missing `into` → 400, mapping of
  each error); register route; contract regen; run
  `go test ./internal/... -run 'Merge|Inventory' -count=1` — PASS.
- [ ] **Step 5: ADR-072** (Accepted for implementation): ruling 3, 4, 5, 6, 7, 10, 11, 12, 14;
  meetings move with owner unchanged; approval bound to A rebinds to B; unmoved categories
  require `acceptUnmoved`; tombstone stays readable; no undo. API-SPEC + README row.
  Run `python3 scripts/docs/check_docs.py` — PASS.
- [ ] **Step 6: Commit** `feat(accounts): add merge eligibility and preview`; open PR B1.

### PR B2: write fence (~40 KB)

### Task B2.1: `mergingInto` fence on account writes

**Files:**
- Create: `backend/internal/repository/account_merge_fence.go`, `backend/internal/repository/account_merge_fence_test.go`
- Modify: `backend/internal/repository/account_contacts.go` (`WriteAccountContact`), `account_events.go` (`WriteAccountEvent`), `account_links.go` (`accountLinkAccessChecks`), `account.go` (`PutMember` → transaction), `project.go` (`ProjectAccountLinkTransactional`), `research.go` (`LinkAccountTransactional`), `account_hierarchy.go` (`UpdateAccountParent` child META and new-parent META), `account_owner_transfer.go`, `dynamodb.go` (`updateMeetingFieldsChecked` adds a target-account META check when `accountId` is assigned or `sharedToAccount` set true; `MaterializePendingAccountGrant` adds an open check on the grant's account so a `mergingInto` A rejects and leaves the pending row for redirect after finish), `backend/internal/service/*` error mapping, `backend/internal/handler/*` 409 mapping, `backend/internal/model/request.go`

**Interfaces:**
- Produces:
  - `func accountOpenCondition() expression.ConditionBuilder` — `attribute_not_exists(mergingInto) AND attribute_not_exists(mergedInto)`.
  - `func (r *DynamoDBRepository) accountOpenCheck(accountID string) (types.TransactWriteItem, error)`
  - `var ErrAccountMerging = errors.New("account is merging or merged")` in `repository`; re-exported as `service.ErrAccountMerging`; `model.ErrCodeAccountMerging = "ACCOUNT_MERGING"`.
  - Classification: when a cancelled transaction's reason index is the open-check item, return `ErrAccountMerging` instead of `ErrConditionFailed` (read `CancellationReasons`, as `classifyContactTransaction` does).

- [ ] **Step 1: Failing test** `TestAccountWritesCarryMergeFence`: for each listed writer,
  the captured transaction contains a META item for the account whose condition includes
  `attribute_not_exists(mergingInto)` and `attribute_not_exists(mergedInto)`; a cancellation
  at that index yields `errors.Is(err, ErrAccountMerging)`. `PutMember` sends a 2-item
  transaction and still honours the `SummaryPrivacyWritesEnabled` absent-PK condition.
- [ ] **Step 2: Failing test** `TestMeetingPublicationFencedByTargetAccount`: publish/link to
  a merging account → `ErrAccountMerging`; unrelated field writes (notes) add no META check.
- [ ] **Step 3: Run** `go test ./internal/repository -run 'MergeFence|FencedByTargetAccount' -count=1` — FAIL; implement; PASS.
- [ ] **Step 4: Handler test** that contacts/events/members/publication return 409
  `ACCOUNT_MERGING`; run full `go test ./... -count=1` — PASS.
- [ ] **Step 5:** API-SPEC "Contracts that require care" note; commit `feat(accounts): fence account writes during merge`; open PR B2.

### PR B3: plain-row movers (~60 KB)

All movers share one signature and one transaction fence.

**Shared interfaces (Task B3.1 produces):**
- `func (r *DynamoDBRepository) mergeFenceChecks(job *model.AccountMergeJob, now time.Time) ([]types.TransactWriteItem, error)` — 4 items: A META `ownerUserId = requester AND mergingInto = B`; A `MEMBER#requester` `role = owner`; B `MEMBER#requester` exists; A `MERGE#job` `state = running AND leaseOwner = :me AND leaseUntil > :now`. Every mover transaction includes them; a failure at any of them maps to `ErrMergeFenceLost`.
- `type MergeStepResult struct { Cursor string; Done bool; Moved, Skipped int }`
- `type mergeMover func(ctx context.Context, job *model.AccountMergeJob, cursor string, budget int) (MergeStepResult, error)` registered in `func (s *AccountMergeService) movers() map[string]mergeMover`.
- `budget` = items per call (worker passes 25).

### Task B3.1: Fence helper and members mover

**Files:**
- Create: `backend/internal/repository/account_merge_rows.go`, `backend/internal/repository/account_merge_rows_test.go`, `backend/internal/service/account_merge_movers.go`, `backend/internal/service/account_merge_movers_test.go`

**Interfaces:**
- Produces: `func (r *DynamoDBRepository) PutMergedMember(ctx context.Context, job *model.AccountMergeJob, member *model.AccountMember) error` — Put `ACCOUNT#B/MEMBER#u` (GSI1 set, `stampSummaryMembership`) conditioned `attribute_not_exists(PK)`; condition failure on that item returns `nil` (existing B role kept); `func (s *AccountMergeService) moveMembers(...)`.

- [ ] **Step 1: Failing tests** `TestMoveMembersRoleMapping` (A owner absent in B → SA; A AM
  → AM; existing B member keeps role; pending invites untouched),
  `TestMoveMembers_Idempotent` (second run: Moved 0, no error),
  `TestMergeFenceLostStopsMover` (fence cancellation → `ErrMergeFenceLost`, cursor unchanged).
- [ ] **Step 2–4:** run `go test ./internal/... -run 'MoveMembers|MergeFence' -count=1` FAIL → implement → PASS.
- [ ] **Step 5: Commit** `feat(accounts): add merge fence and member mover`.

### Task B3.2: Children, projects, research, metadata movers

**Interfaces:**
- Produces:
  - `func (r *DynamoDBRepository) ReparentAccountForMerge(ctx context.Context, job *model.AccountMergeJob, childID string, intoAncestors []AccountParentLink) error` — child META `parentAccountId = A` → `B`; delete `ACCOUNT#A/CHILDREF#child`; put `ACCOUNT#B/CHILDREF#child`; B ancestor META parent checks (reuse `hierarchyCheck`/`accountParentCondition`); plus fence. ≤ 64+3+4 items.
  - `func (r *DynamoDBRepository) MoveProjectAccountLinkForMerge(ctx, job, projectID string) error` — project CONFIG: condition `contains(accountIds, A)`, `ADD accountIds {B}` then `DELETE accountIds {A}`; put `ACCOUNT#B/PROJECTREF#p` (copy of A's ref with B ids), delete A's ref; plus fence.
  - `func (r *DynamoDBRepository) MoveResearchAccountLinkForMerge(ctx, job, researchID string) error` — same shape for `RESEARCHREF#`.
  - `func (r *DynamoDBRepository) MergeAccountMetadata(ctx, job *model.AccountMergeJob, into *model.Account, aliases, domains []string) error` — B META `updatedAt = observed` CAS sets unioned `aliases` (case-insensitive dedupe, A's `Name` + A aliases appended) and `domains`.
- [ ] **Step 1: Failing tests**: `TestReparentAccountForMergeTransaction` (items and
  conditions; 64 ancestors → 71 items OK), `TestMoveProjectLinkForMerge_AlreadyLinkedToB`
  (ADD no-op, A removed, refs consistent), `TestMoveResearchLinkForMerge_Idempotent`
  (A no longer in set → mover skips via pre-read, no write), `TestMergeAccountMetadataUnion`
  (B `["LG Electronics"]` + A `{Name:"LGE", Aliases:["lg"]}` → `["LG Electronics","LGE","lg"]`; dedupe of `"LGE "`; add one Hangul alias case in the Go test itself).
- [ ] **Step 2–4:** run `go test ./internal/... -run 'ForMerge|MergeAccountMetadata' -count=1` FAIL → implement → PASS.
- [ ] **Step 5: Commit** `feat(accounts): add hierarchy, project, research and metadata merge movers`; open PR B3.

### PR B4: sealed movers (~50 KB)

### Task B4.1: Contacts mover with dedupe

**Files:**
- Create: `backend/internal/repository/account_merge_sealed.go`, `..._test.go`; `backend/internal/service/account_merge_contacts.go`, `..._test.go`

**Interfaces:**
- Produces:
  - `func (r *DynamoDBRepository) MoveContactForMerge(ctx, job *model.AccountMergeJob, from model.AccountContact) error` — put B contact sealed with `contactBinding(B, id)` (same `ContactID`, new revision) `attribute_not_exists(PK)`; delete A contact `revision = from.Revision`; put `MERGE#contact#{id}` map row (`TargetContactID = id`); plus fence.
  - `func (r *DynamoDBRepository) MergeContactIntoExisting(ctx, job, from model.AccountContact, into model.AccountContact) error` — update B contact `revision = into.Revision` with merged seal; delete A contact; put map row (`TargetContactID = into.ContactID`); plus fence.
  - `func contactMatchKeys(c model.AccountContact) []string` — `"e:"+lower(trim(email))`, `"p:"+digits(phone)`, `"p:"+digits(workPhone)`; empty values skipped; digits keys shorter than 7 skipped.
  - `func mergeContactFields(into, from model.AccountContact) model.AccountContact` — every string field: keep `into` when non-empty after trim, else `from`.
- [ ] **Step 1: Failing tests** `TestContactMatchKeys` (`" A@X.com "` and `"a@x.com"` equal;
  `"010-1234-5678"` and `"01012345678"` equal), `TestMergeContactFieldsBWins`,
  `TestMoveContacts_Idempotent` (map row present → skip), `TestMoveContactsIndexCap`
  (B > 10,000 contacts → job fails `CONTACT_INDEX_TOO_LARGE` before any write),
  `TestMoveContactsBindingIsB` (sealer fake records binding `accountId = B`).
- [ ] **Step 2–4:** run `go test ./internal/... -run 'Contact.*Merge|MoveContacts|ContactMatchKeys' -count=1` FAIL → implement (B index built once per call from `ListAccountContacts` pages) → PASS.
- [ ] **Step 5: Commit** `feat(accounts): move and deduplicate contacts on merge`.

### Task B4.2: Events mover

**Interfaces:**
- Produces: `func (r *DynamoDBRepository) MoveEventForMerge(ctx, job, sk string, from model.AccountEvent, contactMap map[string]string) error` — put B `EVENT#…` (same SK) sealed with `eventBinding(B, id)` and attendee `ContactID`s remapped; put B `EVENTREF#id`; delete A event and A ref; plus fence (8 items). `func (r *DynamoDBRepository) GetMergeContactMap(ctx, sourceAccountID string, ids []string) (map[string]string, error)`.
- [ ] **Step 1: Failing tests** `TestMoveEventRemapsAttendees` (attendee `c1` merged into `b9`
  → `b9`; an ID with no map row and no B contact is cleared to `""`, keeping
  `Name`/`Email`), `TestMoveEvents_Idempotent`. Run events only after contacts is `Done`.
- [ ] **Step 2–4:** run FAIL → implement → PASS.
- [ ] **Step 5: Commit** `feat(accounts): move account events on merge`; open PR B4.

### PR B5: meetings mover (~60 KB)

### Task B5.1: Meeting re-point, approval rebind, shares, insights

**Files:**
- Create: `backend/internal/repository/account_merge_meetings.go`, `..._test.go`; `backend/internal/service/account_merge_meetings.go`, `..._test.go`

**Interfaces:**
- Produces:
  - `func (r *DynamoDBRepository) MoveMeetingForAccountMerge(ctx context.Context, job *model.AccountMergeJob, ownerID, meetingID string, ref *model.MeetingRef) error` — captures `CaptureSummaryPrivacy`; meeting Update condition: `accountId = A`, `sharedToAccount` equals observed, `summaryCondition(snapshot.Checks[0])`; sets `accountId = B`, `ADD authorizationRevision 1`, and when `summaryAccountApproval.accountId = A` sets `summaryAccountApproval.accountId = B` (other approval fields unchanged); never sets `sharedToAccount` true if it was false. Extra items: put `ACCOUNT#B/MEETINGREF#…` (same SK), delete A ref, plus fence. B stays open by ruling 12, so no B META check is needed. Does not call the generic sharing-safe gate (ADR-072).
  - `func (r *DynamoDBRepository) RetagAccountShareForMerge(ctx, job *model.AccountMergeJob, meetingID, sharedToID string) error` — both rows (`USER#u/SHARE#m`, `MEETING#m/SHARETO#u`) conditioned `origin = account AND accountId = A`, set `accountId = B`; condition failure → `nil`.
  - `func (r *DynamoDBRepository) ListMeetingSharesTo(ctx, meetingID string) ([]model.Share, error)` (if no equivalent exists; check `dynamodb.go` first and reuse).
  - Service `moveMeetings`: per ref page → `GetMeetingByID`; skip when `AccountID != A`; move; retag A-origin shares; `CreateShareIfMember(B)` for B members lacking a share (published meetings only); `BuildAccountInsights(B, meeting)` → `PutAccountInsights`.
- [ ] **Step 1: Failing repo test** `TestMoveMeetingForAccountMergeTransaction` (conditions,
  `ADD authorizationRevision`, approval rebind only when bound to A, ≤ 9 items);
  `TestMoveMeetingKeepsLinkOnlyUnpublished` (Review Focus 4).
- [ ] **Step 2: Failing service tests** `TestMoveMeetingsRetagsAccountShares` (A-origin
  share retagged; direct share untouched; B-only member gains share),
  `TestMoveMeetingsSkipsStaleRef` (meeting now on C → Skipped++ no write),
  `TestMoveMeetings_Idempotent`, `TestMoveMeetingsRebuildsInsightsForB`.
- [ ] **Step 3–4:** run `go test ./internal/... -run 'MoveMeeting|RetagAccountShare' -count=1` FAIL → implement → PASS.
- [ ] **Step 5: Commit** `feat(accounts): move meetings on account merge`; open PR B5.

### PR B6: job, start/status routes, worker, infra (~70 KB)

### Task B6.1: Job store and start/status

**Files:**
- Create: `backend/internal/repository/account_merge_job.go`, `..._test.go`; `backend/internal/service/account_merge_job.go`, `..._test.go`
- Modify: `backend/internal/handler/account_merge.go`, `backend/cmd/api/main.go`, contract generator + generated files, `docs/API-SPEC.md`

**Interfaces:**
- Produces:
  - `func (r *DynamoDBRepository) StartAccountMerge(ctx, job *model.AccountMergeJob, intoAncestors []AccountParentLink) error` — A META: `accountOpenCondition() AND ownerUserId = requester AND (attribute_not_exists(incomingMerges) OR incomingMerges = 0)`, set `mergingInto = B`; B META: open condition, `ADD incomingMerges 1`; A `MEMBER#req` role owner; B `MEMBER#req` exists; B ancestor checks; put job `attribute_not_exists(PK) OR state = failed`.
  - `func (r *DynamoDBRepository) ClaimMergeLease(ctx, sourceID, owner string, now time.Time, ttl time.Duration) (*model.AccountMergeJob, error)`; `SaveMergeProgress(ctx, job, step, cursor string, counts map[string]int) error` (lease-conditioned); `FailMergeJob(ctx, job, code string) error`; `FinishAccountMerge(ctx, job) error` — A META remove `mergingInto`, set `mergedInto`, `mergedAt`; B `ADD incomingMerges -1`; job `state = done`.
  - Failed job: A keeps `mergingInto` (fenced) until the owner restarts via start (resumes from saved step) — no auto-rollback.
  - `func (s *AccountMergeService) Start(ctx, requesterID, fromID string, req *model.StartAccountMergeRequest) (*model.AccountMergeJobDTO, error)`; `StartAccountMergeRequest{IntoAccountID string; AcceptUnmoved bool}`; `func (s *AccountMergeService) Status(ctx, userID, fromID string) (*model.AccountMergeJobDTO, error)` (A or B direct member).
  - Routes `POST /api/accounts/{accountId}/merge`, `GET /api/accounts/{accountId}/merge`; contract rows `api.accountMerge.start` `["read","share","write"]` false, `api.accountMerge.status` `["read"]` false.
- [ ] **Step 1: Failing tests** `TestStartAccountMergeTransaction`,
  `TestStartRequiresAcceptUnmoved` (unmoved present, flag false → `ErrInvalidInput`),
  `TestStartResumesFailedJob`, `TestStartRejectsRunningJob` (409),
  `TestStatusVisibility` (A member yes, B member yes, stranger `ErrForbidden`),
  `TestMergeRequesterRevokedFailsJob` (Review Focus 1: fence lost → `FailMergeJob("REQUESTER_REVOKED")`).
- [ ] **Step 2–4:** FAIL → implement → PASS.
- [ ] **Step 5: Commit** `feat(accounts): add merge job start and status`.

### Task B6.2: Worker in summarize

**Files:**
- Create: `backend/internal/service/account_merge_worker.go`, `..._test.go`
- Modify: `backend/internal/model/people_events.go` (add `AccountMergeRequested{SourceAccountID string}`), `backend/cmd/summarize/main.go` (dispatch `ttobak.analysis` / `AccountMergeRequested`), `backend/cmd/summarize/*_test.go`, `infra/lib/gateway-stack.ts` (rule `ttobak-account-merge-requested` → summarize, `maxEventAge` 1 h, `retryAttempts` 3, DLQ `ttobak-account-merge-dlq`, `pipelineFailures.watchQueue('AccountMerge', …)`; API function `events:PutEvents` already granted? verify and extend the existing statement only), `infra/test/gateway-stack.test.ts`, `docs/INFRA-SPEC.md`

**Interfaces:**
- Produces: `func AccountMergePublisher(client actionEventClient) func(context.Context, model.AccountMergeRequested) error`; `func (s *AccountMergeService) ProcessAccountMergeRequested(ctx context.Context, e model.AccountMergeRequested, deadline time.Time) error` — claim 5-minute lease (owner = Lambda request ID), run `MergeSteps` from saved step with budget 25, save progress after each call, stop 3 minutes before `deadline` and re-publish; on finish publish `PeopleResolveRequested{AccountID: B, Reason: model.PeopleResolveReasonContacts}`.
- [ ] **Step 1: Failing tests** `TestWorkerRunsStepsInOrder`, `TestWorkerRepublishesBeforeDeadline`,
  `TestWorkerLeaseHeldElsewhereNoops`, `TestWorkerFinishEmitsPeopleResolve`; CDK test asserting
  the rule pattern and DLQ.
- [ ] **Step 2–4:** run `go test ./internal/service ./cmd/summarize -run 'Worker|AccountMerge' -count=1` and `(cd infra && npm test)` FAIL → implement → PASS. Run `(cd infra && npx cdk synth)`.
- [ ] **Step 5: Commit** `feat(accounts): run account merge worker in summarize`; open PR B6.
  Deploy order (separate authorization): GatewayStack (rule) before enabling the start route.

### PR B7: operator CLI (~40 KB)

### Task B7.1: `cmd/merge-accounts`

**Files:**
- Create: `backend/cmd/merge-accounts/main.go`, `backend/cmd/merge-accounts/main_test.go`, `docs/runbooks/account-merge.md`
- Modify: `backend/internal/repository/account_merge_scan.go` (+ test)

**Interfaces:**
- Flags: `-from`, `-into`, `-requester` (required; must pass the same authorization), `-apply` (default false = counts only), `-accept-unmoved`, `-scan-users` (bounded parallel scan, `-max-pages` default 100000 like `backfill-account-people`).
- Produces: `func (r *DynamoDBRepository) ScanMergeUserRowsPage(ctx, accountID, cursor string) (*MergeUserRowsPage, error)` — filter `accountId = A` on `SK begins_with SRTASK#|CALEVENT#|MEETING#`; rewrites: SRTASK/CALEVENT `accountId` A→B conditioned `accountId = A` (also `shareAccountId`, `candidateAccountIds` element replacement for CALEVENT); link-only meetings via `MoveMeetingForAccountMerge`.
- CLI runs `Start` then the worker loop in-process (lease owner `cli-<host>-<pid>`).
- [ ] **Step 1: Failing tests** `TestMergeCLIDryRunWritesNothing`, `TestMergeCLIRequiresRequester`,
  `TestScanMergeUserRowsRewritesOnlyA`.
- [ ] **Step 2–4:** FAIL → implement → PASS; `go vet ./cmd/merge-accounts`.
- [ ] **Step 5:** Runbook (dry-run, apply, resume, `failed` recovery, verification queries); commit `feat(accounts): add merge-accounts operator CLI`; open PR B7.

### PR B8: web merge flow (~45 KB)

### Task B8.1: Merge dialog, progress, tombstone redirect

**Files:**
- Create: `frontend/src/components/AccountMergeDialog.tsx`, `frontend/scripts/account-merge.test.mjs`
- Modify: `frontend/src/lib/api.ts` (`accountApi.mergePreview`, `startMerge`, `mergeStatus`), `frontend/src/types/meeting.ts` (`mergingInto?`, `mergedInto?`), `frontend/src/components/AccountDetailClient.tsx`, `docs/DESIGN-SPEC.md`, `docs/architecture.md`

**Interfaces:**
- Produces: `mergePreview(id, intoId, expectedUserId)`, `startMerge(id, { intoAccountId, acceptUnmoved }, expectedUserId)` (via `invalidatingAccountList`), `mergeStatus(id, expectedUserId)`.
- [ ] **Step 1: Failing test**: owner sees `ACCOUNT_MERGE_COPY.action` ("Merge into another Account"); dialog picks B from
  editable accounts (`isEditableAccount`, excluding A); preview renders counts,
  `newAudience`, unmoved list with a required `ACCOUNT_MERGE_COPY.acceptUnmoved` ("I reviewed the items that will not move") checkbox
  when `requiresAcceptUnmoved`; start disabled until checked; status polls every 5 s until
  `done`/`failed`; account with `mergedInto` redirects to `/accounts/{B}`; `mergingInto`
  shows a read-only banner and hides write controls.
- [ ] **Step 2: Run** `(cd frontend && node --test scripts/account-merge.test.mjs)` — FAIL.
- [ ] **Step 3: Implement** (no new static route; `/accounts/[id]` already exists).
- [ ] **Step 4: Run** `(cd frontend && npm run lint && npm run test:unit && npm run build)` — PASS.
- [ ] **Step 5:** DESIGN-SPEC + architecture cross-component note; `python3 scripts/docs/check_docs.py`; commit `feat(web): add account merge flow`; open PR B8.

## Self-review notes

- Spec coverage: transfer (A1–A4); preview (B1.2); start/job/lease (B6.1); worker
  (B6.2); members, meetings, contacts/events, projects/research, aliases/domains, children
  (B3–B5); people resolver (B6.2); SRTASK/CALEVENT and link-only meetings (B7); unmoved
  categories + `acceptUnmoved` (B1.2, B6.1); tombstone + hidden list + redirect (B1.1,
  B8.1); CLI (B7); ADR amending 034/055 (A3); merge ADR (B1.2). Pending invites covered
  by ruling 5 (redirect) instead of copy; link refresh dropped by ruling 9.
- Names used across tasks: `AccountMergeJob`, `mergeFenceChecks`, `ErrMergeFenceLost`,
  `ErrAccountMerging`, `MoveMeetingForAccountMerge`, `MergeSteps` — defined once each.
