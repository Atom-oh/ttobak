# PR #114 Review Round 2 Fixes — Account Team Member Management

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Resolve the second AI Code Review round's findings on PR #114 (`feat/account-team-members`): 1 confirmed frontend MAJOR (partial-failure error message never renders) and 3 confirmed backend MAJORs (legacy Share records permanently un-cleanable, a TOCTOU race that creates permanently-orphaned Shares, and fail-open cleanup with no reconciliation path) — all three backend MAJORs are variations on the same root symptom the PR's own stated goal is to fix: "removed member keeps transcript access."

**Context:** All 4 findings were code-verified before writing this plan (not taken on the reviewer's word alone):
1. **Frontend MAJOR** — `AccountsClient.tsx`'s `handleCreate` sets an error message on partial add-member failure, then immediately calls `fetchAccounts()`, whose first line is `setError(null)` — the error is cleared in the same tick it was set and never renders.
2. **Backend MAJOR (legacy data)** — `Share` records written by `ShareMeetingToAccount` before this PR's `Origin` field existed have `Origin == ""` (zero value). `RemoveMember`'s cleanup check (`share.Origin != model.ShareOriginAccount`) treats them as direct grants and skips them — permanently, since nothing else ever revisits them. Combined with `checkAccess` checking the per-user `Share` row before the account-membership branch (confirmed: `meeting.go:118` runs before `:144`), every pre-existing account-share stays un-revocable by `RemoveMember` forever.
3. **Backend MAJOR (TOCTOU)** — `ShareMeetingToAccount`'s per-member loop does `GetMember` then `CreateShare` as two separate, non-atomic calls (`meeting.go:717-724`). If `RemoveMember` completes its own membership-delete + cleanup pass for that exact member in the gap between those two calls, `CreateShare` still runs and writes a fresh account-origin Share that no future `RemoveMember` call will ever revisit (the member is already gone, so nobody calls `RemoveMember` for them again) — a permanently orphaned Share.
4. **Backend MAJOR (fail-open, no reconciliation)** — `RemoveMember`'s cleanup loop only `log.Printf`s a `GetShare`/`DeleteShare` failure and moves on (`account.go:301-311`). A transient DynamoDB error on one meeting leaves that specific Share permanently un-revoked, with no retry queue, no metric, no reconciliation sweep — silent and permanent, unlike this codebase's other best-effort patterns (e.g. S3 object cleanup) which are cosmetic/cost issues, not access-control issues.

**Tech Stack:** Go (chi router, DynamoDB expression builder + TransactWriteItems, sentinel errors), Next.js/React/TypeScript.

---

## Task 1: Fix the frontend partial-failure error message

**Files:**
- Modify: `frontend/src/components/AccountsClient.tsx`

- [ ] **Step 1: Reorder so the error is set AFTER the refetch, not before**

In `handleCreate`'s `failed.length > 0` branch, swap the order: call `await fetchAccounts()` FIRST, then call `setError(...)` after it resolves. `fetchAccounts` sets `error` to `null` synchronously on entry and only sets it again on its own failure path — calling it before `setError` means the success-path `setError` call is the last one to run and actually sticks:

```tsx
if (failed.length > 0) {
  await fetchAccounts();
  setError(`Account created, but failed to add: ${failed.join(', ')}. Open the account below and retry.`);
  return;
}
```

- [ ] **Step 2: Verify**

`cd frontend && npx tsc --noEmit && npm run lint`. Manually trace: create an account, have one `addMember` call fail (e.g. temporarily point it at a bad email in a local test, or reason through the code path) — confirm the error banner text is what's visible after the list refreshes, not blanked.

---

## Task 2: Backfill `Origin` on legacy account-origin Share records

**Files:**
- Create: `backend/cmd/backfill-share-origin/main.go`

**Design note (revised after plan-gate review — kiro-cli code-verified, CRITICAL+MAJOR):**
1. **Location, CRITICAL**: the first draft placed this at repo-root `scripts/backfill-share-origin.go`. Go's build root is `backend/` (`backend/go.mod`); `scripts/` sits OUTSIDE that module entirely, and even if it had its own module, Go's internal-package rule forbids any package outside `.../backend/...` from importing `backend/internal/...` — which this task must do (`repository`, `model`). This is a hard compile error, not a style nit. Fixed by moving it under `backend/cmd/backfill-share-origin/`, the same convention every other Go entry point in this repo already follows (`cmd/api`, `cmd/kb`, etc.) — this one just isn't a Lambda handler, it's a one-shot CLI main().
2. **Discriminator, MAJOR**: the first draft claimed a coincidental direct share is "never touched" because of a 4-part match (Share exists + `Origin==""` + current member + meeting's `SharedToAccount`+`AccountID` match). This is false: `Share` has exactly ONE row per `(sharedToID, meetingID)` key pair (`PK=USER#{id}/SK=SHARED#{meetingId}` — confirmed in `dynamodb.go`'s `CreateShare`). If a meeting was BOTH directly shared to a user AND shared to that user's account, the existing `CreateShare` guard (`dynamodb.go:1043-1050`, added in the prior review round) deliberately leaves that single collapsed row at `Origin==""` — it satisfies all 4 of the backfill's criteria, so the backfill would incorrectly retag it `"account"`, after which a future `RemoveMember` would revoke a grant the owner made directly. There is no field that distinguishes "legacy account share" from "direct share on an account-shared meeting" — this ambiguity is real and must be documented as an accepted risk, not claimed away.

- [ ] **Step 1: Identify candidate Shares, with the ambiguity documented up front**

For each account (accept a `--account-id` flag to target one account at a time for a safer phased rollout — do not default to scanning the whole table), for each of that account's `MeetingRef`s (`ListMeetingRefsForAccount`), for each of that account's current members (`ListAccountMembers`, excluding the owner — mirrors `ShareMeetingToAccount`'s own exclusion), check whether a `Share` exists for that (member, meeting) pair with `Origin == ""`. Print, for every candidate found, a warning that this heuristic cannot distinguish a true legacy account-share from a direct share on a meeting that also happens to be account-shared — the operator reviews the printed list (which includes account name, meeting title, member email) before ever passing `--apply` (Step 3), since this is exactly the kind of judgment call a human should make per-account for a first, careful rollout rather than a script fully automating away.

- [ ] **Step 2: Conditionally tag matching Shares as account-origin**

For each identified `(member, meeting)` pair meeting ALL of: (a) a `Share` row exists, (b) `Origin == ""`, (c) the member is a CURRENT member of the account, (d) the meeting's `SharedToAccount == true` and `AccountID` matches — update just that Share's `Origin` field to `model.ShareOriginAccount`. Deliberately conservative in one more way: only backfills shares for members who are STILL in the account today; a share for someone already removed is left alone (nobody knows whether that removal predated or postdated this backfill running).

Use a `ConditionExpression` requiring the item still has `origin` absent/empty at write time (`attribute_not_exists(origin) OR origin = :empty`), so a concurrent legitimate `RemoveMember` cleanup running during the backfill can't race it — if `RemoveMember` already deleted the Share, or a live `ShareMeetingToAccount`/direct-share call already set some other value, the conditional update simply no-ops for that one item instead of erroring the whole backfill run.

- [ ] **Step 3: Dry-run mode + logging**

Default to `--dry-run` (prints what WOULD be tagged, touches nothing); require an explicit `--apply` flag to actually write. Log every account/meeting/member touched (or skipped and why) so the operator has an audit trail before/after running against production.

- [ ] **Step 4: Verify**

`cd backend && /home/atomoh/go-sdk/go/bin/go build ./...` (now correctly inside the module — this alone catches the import error the prior draft would have hit). No automated test beyond that (this is an operator script touching production data shape, following the existing `scripts/whisper-rebatch.py` precedent of being manually operated, not CI-tested) — a manual `--dry-run` against a test/staging AWS profile if one is available is the remaining verification step; otherwise this task's correctness rests on careful code review of the read-then-conditional-write logic in Steps 1-2 plus the operator manually reviewing the dry-run output per Step 1's documented ambiguity.

---

## Task 3: Close the `ShareMeetingToAccount` / `RemoveMember` TOCTOU race with a transactional write

**Files:**
- Modify: `backend/internal/repository/dynamodb.go`
- Modify: `backend/internal/service/meeting.go`
- Test: `backend/internal/service/meeting_test.go`
- Test: `backend/internal/handler/meeting_test.go`

**Design note (revised after plan-gate review — kiro-cli code-verified, MAJOR):** the reviewer's
suggested fix — wrap the membership `ConditionCheck` and the Share `Put` in one
`TransactWriteItems` call — is adopted, replacing my prior "recheck-then-write, accepted
narrow window" design (that residual window is what this round's review caught as
unacceptable, given the permanence of the resulting orphan). BUT the first draft of this
transactional rewrite told the implementer to do a plain `Put` for the Share item, which
**silently drops the existing `CreateShare` clobber-guard** (`dynamodb.go:1043-1050`, added in
the PRIOR review round specifically to stop an account-share write from overwriting a
pre-existing direct share). A plain `Put` in the transaction would unconditionally overwrite
ANY existing Share row at that key — including a direct one — reintroducing the exact bug
`Origin` was added to prevent, just via a different code path than the original one that got
fixed. Fixed below by putting a `ConditionExpression` on the Share `Put` itself, inside the
same transaction, so the guard survives the move to `TransactWriteItems` instead of being lost.

- [ ] **Step 1: Add a transactional CreateShare variant for account-origin writes, preserving the clobber-guard**

In `backend/internal/repository/dynamodb.go`, add `CreateShareIfMember(ctx context.Context, meetingID, ownerID, ownerEmail, accountID, sharedToID, email, permission string) (*model.Share, error)` (or extend the existing `CreateShare` with an optional membership-conditioned path — pick whichever produces the smaller diff against the current 8-arg `CreateShare` signature from the prior round's fix). Build a `TransactWriteItems` call with:
  - A `ConditionCheck` on the `AccountMember` item (`PK=ACCOUNT#{accountID}`, `SK=MEMBER#{sharedToID}`) with `ConditionExpression: attribute_exists(PK)` — fails the whole transaction if the member was removed.
  - A `Put` for EACH of the two existing Share items (recipient-lookup + meeting-lookup — mirror `CreateShare`'s existing two-item write, so this is a 3-item transaction total: 1 ConditionCheck + 2 Puts, nowhere near the 100-item cap), each with `Origin: model.ShareOriginAccount` AND a `ConditionExpression: "attribute_not_exists(PK) OR #origin = :accountOrigin"` (`#origin` mapped to the `origin` attribute name, `:accountOrigin` to `model.ShareOriginAccount`). **Condition on `PK`, not on `origin`**: `Origin` has `dynamodbav:"origin,omitempty"` (confirmed in `model/meeting.go`), so a direct share (`Origin == ""`) never writes the `origin` attribute at all — `attribute_not_exists(origin)` would be TRUE for a direct share too (the attribute is genuinely absent from that item, not just holding an empty string), which would incorrectly permit clobbering it. `attribute_not_exists(PK)` instead checks whether the ITEM exists at all (PK is always present on any real item), which correctly distinguishes "no item yet" (write allowed) from "item exists with some origin" (only allowed when that origin is already `"account"`) — this is the ported clobber-guard: the Put succeeds when the item doesn't exist yet, OR when it already exists with `Origin == "account"` (idempotent re-share), and FAILS when it exists with any other origin including the omitted/empty case (a direct share), exactly matching today's `CreateShare` behavior of refusing to overwrite a direct grant.
  - The AWS SDK v2 `TransactWriteItems` error on any single item's condition failure returns a `TransactionCanceledException` with per-item `CancellationReasons` — inspect that to distinguish "membership ConditionCheck failed" (member removed → treat as skip, matches today's `member == nil` behavior) from "Share Put condition failed" (pre-existing direct share → treat as today's `CreateShare` behavior of returning the existing share unchanged, NOT an error). Both are legitimate non-error outcomes, not failures of the transaction API itself — map them to distinct sentinels (or a single sentinel plus a way to tell them apart) so the service layer doesn't conflate "member gone" with "direct share preserved."

- [ ] **Step 2: Use the transactional path in `ShareMeetingToAccount`**

In `backend/internal/service/meeting.go`'s per-member loop (~line 708-728), replace the separate `GetMember` + `CreateShare` calls with the single new transactional method. On the "member removed" outcome, treat it exactly like the current `member == nil` skip (continue to the next member, don't increment `shared`, don't error out the whole `ShareMeetingToAccount` call). On the "direct share preserved" outcome, this is success from `ShareMeetingToAccount`'s perspective too (the member already effectively has read access via their direct share) — do NOT treat it as an error; decide whether to still count it toward `shared` (arguably yes, since the member does have access) and document the choice in a comment. Remove the now-redundant standalone `GetMember` call — the transaction's `ConditionCheck` IS the membership check, done atomically with the write instead of before it.

- [ ] **Step 3: Add regression tests for BOTH races this transaction now protects against**

In `backend/internal/service/meeting_test.go`:
- Update `TestShareMeetingToAccount_SkipsRemovedMember` (prior round): the old `forceGetMemberNil` hook on the plain `GetMember` call is no longer exercised once Step 2 removes that standalone call. Update the mock's transactional method to consult the SAME underlying `members` map at write time (not a separately-forced value) so the test genuinely proves atomicity: seed 2 members, and inside the mock's transactional-write method (not before calling `ShareMeetingToAccount`), delete one member from the map to simulate "removed by a concurrent RemoveMember exactly at the transaction boundary" — assert no Share was created for that member.
- Add a NEW test (the one the plan-gate review specifically flagged as missing): pre-seed a direct (`Origin: ""`) share for a member, then call `ShareMeetingToAccount` for a meeting shared to that member's account, and assert the pre-existing share's `Origin` is STILL `""` afterward (not overwritten to `"account"`) — this is the regression test proving the clobber-guard survived the move to a transaction.

- [ ] **Step 4: Verify**

`cd backend && /home/atomoh/go-sdk/go/bin/go build ./... && /home/atomoh/go-sdk/go/bin/go test ./...`.

---

## Task 4: Surface Share-cleanup failures instead of only logging them

**Files:**
- Modify: `backend/internal/service/account.go`
- Modify: `backend/internal/handler/account.go`
- Modify: `docs/API-SPEC.md`
- Modify: `frontend/src/lib/api.ts`
- Modify: `frontend/src/components/AccountDetailClient.tsx`
- Test: `backend/internal/service/account_test.go`
- Test: `backend/internal/handler/account_test.go`

**Addendum (caught by P4 final gate, code-verified):** the backend now sometimes returns
a 200-with-body instead of 204 for `DELETE .../members/{userId}`, but the frontend client
(`accountApi.removeMember`) still typed the response as `void` and the caller
(`AccountDetailClient.tsx`'s `handleRemoveMember`) discarded it — so the new signal never
reached a user even after this task shipped. Fixed: `removeMember` now types the response
as `{ removed: boolean; cleanupFailedForMeetings: string[] } | undefined` (`undefined` on
the unchanged 204 fully-clean path — confirmed via `apiFetch`'s actual 204 handling, which
returns `undefined`, not `null`), and the caller surfaces `cleanupFailedForMeetings` in the
existing error banner after refetching.

**Design note:** a full async retry/DLQ mechanism (SQS, a scheduled reconciliation Lambda,
a dedicated retry endpoint) is a genuinely bigger infrastructure change than a PR-review-fix
round should introduce without its own separate design discussion — new CDK resources, IAM
policy, monitoring wiring, an extra API surface to document and secure. That's follow-up
work, not this task. The bounded fix here: stop the failure from being invisible. Today an
owner who removes a member and hits a transient `DeleteShare` error gets a `204` with zero
indication anything is wrong — this task makes that failure visible in the response so the
owner at least knows to investigate (via logs/support) instead of believing revocation fully
succeeded. Making it *retryable* via a dedicated endpoint is explicitly deferred.

- [ ] **Step 1: `RemoveMember` returns which meetings failed cleanup, without changing its no-fail contract**

Change `AccountService.RemoveMember`'s signature to return `(failedMeetingIDs []string, err error)`. `err` remains nil whenever the membership delete itself succeeded — this task does NOT change the existing "removal request itself never fails due to cleanup issues" contract, it only adds visibility into partial cleanup failures. Keep every existing `log.Printf` call (unchanged) and additionally append the meeting ID to the returned slice at each of the two failure points (`GetShare` error, `DeleteShare` error).

- [ ] **Step 2: Handler surfaces partial-cleanup info without breaking the existing 204 contract for the common case**

In `backend/internal/handler/account.go`'s `RemoveMember` handler, when `failedMeetingIDs` is empty (the common, fully-successful case), keep returning `204 No Content` exactly as today — no behavior change for existing callers. When non-empty, return `200 OK` with `{"removed": true, "cleanupFailedForMeetings": [...]}` (204 cannot carry a body per HTTP semantics, so a non-empty result needs a different status code). This is additive: any caller only checking for 2xx success sees no change; a caller that inspects the body can now detect incomplete cleanup.

- [ ] **Step 3: Update API-SPEC.md**

Document the 200-with-body-vs-204 distinction for `DELETE /api/accounts/{accountId}/members/{userId}`. Update the "Known limitation" note from the prior round: cleanup failures are now surfaced in the response instead of only logged; note that automated retry/reconciliation is tracked as follow-up work, not yet implemented.

- [ ] **Step 4: Add regression tests**

Extend `TestRemoveMember_CleanupFailureDoesNotFailRemoval` (prior round, in `account_test.go`) to also assert the returned `failedMeetingIDs` contains the meeting whose cleanup failed. Add a handler-level test asserting `200` (with the expected body) when cleanup partially fails, and that the existing `204` case (all-succeeds) is unchanged.

- [ ] **Step 5: Verify**

`cd backend && /home/atomoh/go-sdk/go/bin/go build ./... && /home/atomoh/go-sdk/go/bin/go test ./...`.

---

## Final verification (all tasks)

- [ ] `cd backend && /home/atomoh/go-sdk/go/bin/go build ./... && /home/atomoh/go-sdk/go/bin/go test ./...`
- [ ] `cd frontend && npm run lint && npx tsc --noEmit && npm run build`
- [ ] Re-read the diff against all 4 confirmed findings and confirm each is concretely addressed.
