# PR #114 Review Fixes — Account Team Member Management

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Resolve the 3 MAJOR findings from the AI Code Review on PR #114 (`feat/account-team-members`) without expanding scope beyond what the review flagged.

**Context:** PR #114 adds owner-only member remove/change-role APIs and a search-based `MemberPicker`. The AI Code Review (chair-synthesized, 4-model panel, base-code-verified) blocked the PR on:
1. **L2 MAJOR** — `UpdateMemberRole` calls `PutMember` (unconditional `PutItem`) instead of a conditional update, so a role-change request racing a concurrent `RemoveMember` between the two calls' `GetMember`/`PutMember` window resurrects a deleted membership row.
2. **L2 MAJOR** — `MemberPicker`'s `<input>` has no Enter-key guard; on `AccountsClient`'s create form (where the whole Members section sits inside `<form onSubmit={handleCreate}>`), pressing Enter while searching for a teammate submits the form and creates the account prematurely.
3. **L3 MAJOR** — `RemoveMember` only deletes the `MEMBER#` row; it does not revoke the per-user `Share` records `ShareMeetingToAccount` already wrote for that user on every meeting shared to the account before removal, so a removed member keeps read access to previously-shared meeting transcripts/insights. The review also found `checkAccess`'s existing comment ("no stale per-user Share snapshot") is factually wrong given `ShareMeetingToAccount`'s per-member `CreateShare` loop — fix the comment alongside the fix.

Also apply 3 near-zero-risk MINORs the review flagged (fold into the same tasks, no separate task): the partial-add-failure error message getting lost by an immediate `router.push` in `AccountsClient`, the creator not being excluded from their own account's `MemberPicker` results, and the `api.ts` `updateMember` formatting nit.

**Tech Stack:** Go (chi router, DynamoDB expression builder, sentinel errors), Next.js/React/TypeScript.

---

## Task 1: Fix `UpdateMemberRole` race (conditional update, not unconditional PutMember)

**Files:**
- Modify: `backend/internal/repository/account.go`
- Modify: `backend/internal/service/account.go`
- Test: `backend/internal/service/account_test.go`

- [ ] **Step 1: Add a conditional `UpdateMemberRole` repository method**

In `backend/internal/repository/account.go`, add a new method next to the existing `DeleteMember` (which already uses the `attribute_exists(PK)` condition pattern — copy that pattern exactly):

```go
// UpdateMemberRole conditionally updates a member's role, requiring the item
// to already exist -- a concurrent RemoveMember between the service's
// GetMember check and this call surfaces ErrConditionFailed (mapped to
// ErrNotFound) instead of silently resurrecting a deleted membership row.
func (r *DynamoDBRepository) UpdateMemberRole(ctx context.Context, accountID, userID, role string) error {
	condition := expression.AttributeExists(expression.Name("PK"))
	update := expression.Set(expression.Name("role"), expression.Value(role))
	expr, err := expression.NewBuilder().WithCondition(condition).WithUpdate(update).Build()
	if err != nil {
		return fmt.Errorf("build update member role condition: %w", err)
	}
	_, err = r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: model.PrefixAccount + accountID},
			"SK": &types.AttributeValueMemberS{Value: model.PrefixMember + userID},
		},
		ConditionExpression:       expr.Condition(),
		UpdateExpression:          expr.Update(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
	})
	if err != nil {
		var ccfe *types.ConditionalCheckFailedException
		if errors.As(err, &ccfe) {
			return fmt.Errorf("%w: member %s not found", ErrConditionFailed, userID)
		}
		return fmt.Errorf("update member role: %w", err)
	}
	return nil
}
```

Note: `dynamodb.UpdateItemInput` needs no new import (`UpdateItem` is already a method on the same `r.client`); `expression.Set` is the same `expression` package already imported in this file.

- [ ] **Step 2: Wire the new repo method into the service**

In `backend/internal/service/account.go`:
- Add `UpdateMemberRole(ctx context.Context, accountID, userID, role string) error` to the `accountRepo` interface (alongside the existing `DeleteMember` line).
- In `AccountService.UpdateMemberRole`, replace the `target.Role = req.Role; s.repo.PutMember(ctx, target)` tail with a call to the new conditional repo method, mapping `repository.ErrConditionFailed` to `ErrNotFound` (mirror `RemoveMember`'s existing `errors.Is(err, repository.ErrConditionFailed)` handling immediately above it in the same file). Keep building the returned `*model.AccountMemberDTO` from `target` (its `Email`/`UserID` don't change) but set `Role: req.Role` directly rather than relying on the mutated `target.Role`.

- [ ] **Step 3: Update the mock repos in both test files**

`backend/internal/service/account_test.go`'s `mockAccountRepo` and `backend/internal/handler/account_test.go`'s `mockHandlerAccountRepo` both need a new `UpdateMemberRole` method (the `accountRepo`/`AccountRepo` interface grew). Mirror each mock's existing `DeleteMember` method shape (map lookup by `memberKey`/`acctMemberKey`, return the wrapped `repository.ErrConditionFailed` if absent, else mutate the stored `Role` field in place).

- [ ] **Step 4: Add a regression test that actually exercises the race window**

Calling `repo.DeleteMember` *before* invoking `svc.UpdateMemberRole` does NOT exercise the race: the service's own leading `GetMember` call would see the member already gone and return `ErrNotFound` at that check, never reaching the write — so this "test" would pass identically against the OLD buggy unconditional-`PutMember` code, proving nothing about the fix.

The real race window is *between* the service's `GetMember` call and its subsequent write. To simulate it, add a small test-only hook to `mockAccountRepo` (`backend/internal/service/account_test.go`): a field `deleteAfterGet string` (a `memberKey`-format string, empty = disabled) that `GetMember` consults *after* building its return copy — if the requested key matches `deleteAfterGet`, delete the member from the map before returning (simulating "another request's RemoveMember completed in the gap right after our GetMember read"). Add `TestUpdateMemberRole_ConcurrentlyRemovedMemberNotFound`: create an account, add a TAM member, set `repo.deleteAfterGet = memberKey(acc.AccountID, "tam-1")`, call `svc.UpdateMemberRole` for that member, and assert `errors.Is(err, ErrNotFound)`. Verify this test actually distinguishes old-vs-new behavior by mentally (or temporarily) checking it against the current `PutMember`-based code — it should fail there (member silently resurrected) and pass only with Task 1's conditional-update fix.

Keep the existing `TestUpdateMemberRole_OwnerChangesRole` test passing unchanged — it doesn't exercise the race path.

- [ ] **Step 5: Verify**

`cd backend && /home/atomoh/go-sdk/go/bin/go build ./... && /home/atomoh/go-sdk/go/bin/go test ./internal/service/... ./internal/handler/... -run Member -v` — all pass, including the new race test.

---

## Task 2: Fix MemberPicker Enter-key premature form submit

**Files:**
- Modify: `frontend/src/components/MemberPicker.tsx`
- Modify: `frontend/src/components/AccountsClient.tsx`

- [ ] **Step 1: Guard Enter in MemberPicker's input**

In `frontend/src/components/MemberPicker.tsx`, add an `onKeyDown` handler to the `<input>` that calls `e.preventDefault()` when `e.key === 'Enter'` — this is the fix that matters regardless of which parent renders the picker (defense at the source, not just at `AccountsClient`'s call site, since any future caller could also wrap it in a `<form>`).

- [ ] **Step 2 (MINOR, fold in): exclude the account creator from their own pending-members list**

In `frontend/src/components/AccountsClient.tsx`, the create form's `<MemberPicker excludeUserIds={pendingMembers.map(...)} .../>` doesn't exclude the current user (the creator, who will become `owner` automatically and doesn't need — and can't be — added as a regular member). Get the current user id from `useAuth()` (already the pattern used in `AccountDetailClient.tsx`) and include it in the `excludeUserIds` array passed to `MemberPicker`.

- [ ] **Step 3 (MINOR, fold in): surface partial add-member failures before navigating away**

In `AccountsClient.tsx`'s `handleCreate`, the `failed.length > 0` branch calls `setError(...)` immediately followed by `router.push(...)` in the same synchronous block, so the error never renders. Show a `window.confirm`-style pause is overkill; instead, only call `router.push` when `failed.length === 0`, and when there are failures, stay on the accounts list page with the error visible (the account still exists and is reachable from the list, so no data is lost — the owner can open it and retry from `AccountDetailClient`'s Members section).

- [ ] **Step 4: Verify**

`cd frontend && npm run lint && npx tsc --noEmit && npm run build` — all clean. Manually confirm (or reason through) that typing in the create-form's member search box and pressing Enter no longer submits the form.

---

## Task 3: Revoke ACCOUNT-ORIGIN per-user Share records on member removal + fix stale comment

**Design note (revised after TWO rounds of cross-AI plan review):** the first draft
planned an unconditional `DeleteShare` for every account meeting. Round 1 review (Codex)
found the missing provenance distinction (a direct share and an account share use the same
key and are indistinguishable) and the `ShareMeetingToAccount`/`RemoveMember` race; both
were real and are fixed below (Steps 1-4). Round 2 review, re-examining the fix, raised
further concerns — some real and folded in (mock coverage gap in
`mockHandlerMeetingRepo`; the race-test design flaw), and some **explicitly out of scope
for this task**, documented here rather than silently ignored:

- **Full transactional atomicity between the membership check and the Share write/delete**
  (round 2's suggestion: put the `GetMember` condition inside the same DynamoDB transaction
  as `CreateShare`/`DeleteShare`). This codebase's own `ShareMeetingToAccount` already
  rejects full `TransactWriteItems` atomicity for its own multi-item write sequence, with an
  explicit comment explaining why (100-item transaction cap would bound account team size —
  `meeting.go:671-675`). Demanding stricter atomicity for this narrower fix than the
  function it lives in already provides would be inconsistent, and closing the read-then-write
  gap with a single extra existence check (Step 2) — while not a hard guarantee — matches
  the granularity of consistency this codebase accepts everywhere else in this exact
  function. Residual risk after Step 2: an extremely narrow window between the recheck and
  the write itself. Accepted, not fixed, same as every other multi-step write in
  `ShareMeetingToAccount`.
- **A single `Share` row can't represent two independent origins for the same (user,
  meeting) pair simultaneously** (round 2: an account-share write could overwrite an
  existing direct share's `Origin` tag, so a later cleanup would incorrectly delete a share
  that is *also* a direct grant). This is real, but the fix (multiple grant records per
  user+meeting, or a set-typed origin/reference-count field) is a `Share`-model redesign
  well beyond 3 CI-blocking findings on an unrelated PR. **Mitigation actually included below
  (Step 1a)**: `CreateShare` must NOT downgrade/overwrite an existing direct share when
  called for account-sharing — check-before-write, refuse (skip, log) rather than clobber.
  This closes the "account write destroys a direct grant" direction of the bug without a
  model redesign; the reverse direction (a later direct share overwriting an account tag)
  is pre-existing behavior this task doesn't change and doesn't make worse.
- **Historical Shares created by `ShareMeetingToAccount` before this fix ships have no
  `Origin` tag**, so they'll be treated as direct (never cleaned up by future removals).
  This is an expected, inherent consequence of adding a new field to existing data — no
  contradiction with the fix's own correctness (it doesn't claim to retroactively fix
  already-existing rows), and a backfill script is out of scope for a PR-review-fix task.
  Documented in Step 6's API-SPEC note.
- **`KnowledgeService.Ask`'s independent `GetShare` check** is a pre-existing, unrelated
  read path (Q&A over a meeting) that this task does not touch — it already grants access
  identically for direct and account shares today, before and after this fix, so this task
  neither improves nor worsens it. Out of scope.

The bounded fix: give `Share` a provenance tag, refuse to clobber a direct share when
writing an account share, and make `ShareMeetingToAccount` skip (not blindly write for) a
member who's no longer present at write time.

**Files:**
- Modify: `backend/internal/model/meeting.go`
- Modify: `backend/internal/repository/dynamodb.go`
- Modify: `backend/internal/service/account.go`
- Modify: `backend/internal/service/meeting.go`
- Modify: `docs/API-SPEC.md`
- Test: `backend/internal/service/account_test.go`
- Test: `backend/internal/service/meeting_test.go`
- Test: `backend/internal/handler/account_test.go`
- Test: `backend/internal/handler/meeting_test.go`

- [ ] **Step 1: Tag account-origin shares with provenance**

In `backend/internal/model/meeting.go`, add an optional field to `Share`:
```go
// Origin distinguishes a share created by ShareMeetingToAccount ("account")
// from one created by the owner directly via ShareMeetingByEmail (empty/"direct").
// RemoveMember's cleanup must only ever delete "account"-origin shares -- a
// direct share is a separate grant the owner made explicitly and removing a
// team member must never silently revoke it.
Origin string `dynamodbav:"origin,omitempty"`
```
Extend `CreateShare`'s repository signature (`dynamodb.go:1042`) to accept an `origin string`
parameter, set on both written `Share` items (`shareForRecipient`/`shareForMeeting`).
Update both call sites in `meeting.go`: `ShareMeetingByEmail` (~line 444) passes `""`
(direct), `ShareMeetingToAccount`'s member loop (~line 715) passes `"account"`. Update the
`meetingRepo`/`accountRepo` interface signatures and **every** mock implementing
`CreateShare` to match the new parameter — this is at least
`backend/internal/service/meeting_test.go`'s `mockMeetingRepo` AND
`backend/internal/handler/meeting_test.go`'s `mockHandlerMeetingRepo` (confirmed present at
`meeting_test.go:150` in the handler package — round 2 review caught that the first draft
named only the service-package mock; grep for `CreateShare` across all `*_test.go` under
`backend/internal/` before finishing this step to make sure no mock is missed, since a
missed one is a compile error, not a subtle bug).

- [ ] **Step 1a: Never let an account-share write clobber an existing direct share**

In the repository `CreateShare` (`dynamodb.go:1042`), when `origin == "account"`, first
`GetItem` the existing `shareForRecipient` key; if a share already exists there with
`Origin != "account"` (i.e. it's a pre-existing direct grant), skip the write entirely and
return the existing share unchanged (no error — this is an expected, silent no-op: the
recipient already has access, direct grants are a superset of what account-sharing would
give them read-only, and account-team removal must never revoke a grant the owner made
directly). When `origin == ""` (a direct share write), no such check is needed — an owner
explicitly re-sharing directly is always allowed to (over)write, matching existing
behavior. This single-item `GetItem`-then-maybe-`Put` isn't atomic against a concurrent
write either, but it only needs to prevent the specific case Step 1 exists to prevent
(account-sharing silently destroying a direct grant), not provide general-purpose
mutual exclusion — see the Task 3 design note above for why full atomicity is out of scope.

- [ ] **Step 2: Close the `ShareMeetingToAccount` / `RemoveMember` race at the write, not with a lock**

In `ShareMeetingToAccount`'s member loop (`meeting.go` ~line 706-719), immediately before
calling `CreateShare` for each member, re-check `s.repo.GetMember(ctx, accountID, m.UserID)`
and skip creating the share if that returns `nil` (the member was removed after the earlier
`ListAccountMembers` snapshot but before this write). This one extra read per member closes
the race without a distributed lock: a membership check right before the corresponding
write and a corresponding cleanup pass on removal (Step 3) can no longer both "miss" the
same in-flight member, because whichever operation's relevant read/write runs last sees the
other's completed effect.

- [ ] **Step 3: Give `AccountService` access to `GetShare`/`DeleteShare` and check provenance in the service layer**

`GetShare(ctx, sharedToID, meetingID string) (*model.Share, error)` and
`DeleteShare(ctx, sharedToID, meetingID string) error` already exist on
`*repository.DynamoDBRepository` (`dynamodb.go:1098`/`:1123`) and are already in
`meetingRepo` (`meeting.go:60`), but NOT in `accountRepo` (`account.go`'s interface) —
`RemoveMember` lives in `AccountService`, so add both method signatures to the `accountRepo`
interface in `backend/internal/service/account.go`. No repo-layer change needed for
`DeleteShare` itself (it's already an unconditional delete-by-key, correct for its existing
`RevokeShare` caller); the provenance check happens in the service layer, where the fetched
`Share`'s `Origin` field is in scope: `AccountService.RemoveMember` calls
`s.repo.GetShare(ctx, targetUserID, meetingID)` for each meeting, and only calls
`s.repo.DeleteShare(ctx, targetUserID, meetingID)` when the returned share's
`Origin == "account"` (skip silently — no delete, no error — when it's `""`/direct or when
there's no share at all for that meeting). This leaves the existing `DeleteShare` repo
method and its `RevokeShare` caller completely unchanged.

- [ ] **Step 4: Revoke shares in `RemoveMember`, with a cleanup-failure contract that never fails the removal and never re-adds the member**

In `AccountService.RemoveMember`, after `s.repo.DeleteMember` succeeds (the membership row
is now gone — this must not be undone by anything below), call
`s.repo.ListMeetingRefsForAccount(ctx, accountID)` and for each ref run Step 3's
check-then-delete. Collect (don't stop on) any `DeleteShare`/`GetShare` errors from
individual meetings; `log.Printf` each one (matches the existing best-effort logging
pattern in `deleteDoc`/`updateDoc` in this same file for S3 cleanup) but always return `nil`
from `RemoveMember` itself once the membership delete has succeeded — a share-cleanup
failure on meeting N of M must never make the API return an error for an operation whose
core effect (membership removed) already succeeded, and must never trigger any kind of
"undo" that would resurrect the member. This directly resolves the review's point 3 above:
no 500 despite success, and no silent total failure either (it's logged).

- [ ] **Step 5: Fix the now-confirmed-stale comment in `checkAccess`**

In `backend/internal/service/meeting.go` around line 133-137, the comment says
account-membership access is live "(no stale per-user Share snapshot)" — inaccurate, since
`ShareMeetingToAccount` does write a per-user Share snapshot at share time. Rewrite to
describe the actual post-fix behavior precisely — **do not overclaim "immediate" access
revocation**, because `checkAccess`'s existing branch order (round-3 review caught this
precisely: `GetShare` is checked BEFORE the account-membership branch, `meeting.go:118`
runs ahead of the `SharedToAccount`+`GetMember` branch at `:142`) means a meeting whose
Share row hasn't been cleaned up yet is granted access via that Share row directly,
**without the membership check ever running for that meeting**. The accurate statement is:
membership grants live read access for meetings that have NO per-user Share row for this
user (a later-added member sees a meeting immediately, with no new Share write needed);
for a meeting that already has one (via prior `ShareMeetingToAccount`), access continues
until `RemoveMember`'s best-effort cleanup (Step 4) actually deletes that specific Share
row — which happens synchronously within the same `RemoveMember` call for every meeting in
`ListMeetingRefsForAccount` at call time, but is not instantaneous across N meetings and
is not transactional with the membership delete (a crash or error mid-loop leaves later
meetings' Shares un-cleaned until a retry). Do not describe this as "immediate" anywhere in
the comment or the API-SPEC note (Step 6) — describe it as: membership delete blocks new
account-derived access instantly; Share cleanup (same call, same request, but a separate
per-meeting operation) revokes existing derived access, is not atomic with the membership
delete, and its result should be read from the operation's logs if it matters when it
completes for a given meeting.

- [ ] **Step 6: Update `docs/API-SPEC.md`'s "Known limitation" note**

Update the `Remove Member` section to describe the new behavior accurately, matching Step
5's corrected wording — do not say "immediate" for the Share-cleanup path: removal blocks
NEW access to any meeting that has no existing per-user Share row for that user (this part
is instant, via the live membership check). For a meeting that DOES already have a
per-user Share row (because it was shared to the account before this removal), that access
is revoked by this same `RemoveMember` call's best-effort cleanup pass over every meeting
in the account's MeetingRef list — not instantaneous across N meetings, not transactional
with the membership delete, and a cleanup failure on one meeting is logged (not surfaced as
an error to the caller) without affecting any *direct* share the owner separately granted
that user. Note the one remaining known limitation explicitly: Share records written by
`share-account` **before** this fix shipped have no origin tag and are therefore treated as
direct grants, not cleaned up by a removal that happens after the fix ships — pre-existing
shares are unaffected by this change, only Shares created going forward are tagged and
thus cleanable.

- [ ] **Step 7: Add regression tests**

In `backend/internal/service/account_test.go`, add:
- `TestRemoveMember_RevokesAccountOriginShare`: seed an account with owner + TAM, a
  `MeetingRef`, and a `Share` for the TAM member with `Origin: "account"` (mock needs a
  `shares` map keyed by `sharedToID+"|"+meetingID`, plus `GetShare`/`DeleteShare` methods —
  check if `mockAccountRepo` needs these added or if a shared mock already exists between
  packages). Call `svc.RemoveMember`, assert the share is gone.
- `TestRemoveMember_PreservesDirectShare`: same setup but the seeded share has
  `Origin: ""` (direct). Call `svc.RemoveMember`, assert the share is STILL present
  afterward — this is the regression test for the provenance bug the plan review caught.
- `TestRemoveMember_CleanupFailureDoesNotFailRemoval`: seed a mock that returns an error
  from `DeleteShare`/`GetShare` for one meeting; assert `svc.RemoveMember` still returns
  `nil` and the membership row is still gone (not resurrected).

In `backend/internal/service/meeting_test.go`, add
`TestShareMeetingToAccount_SkipsRemovedMember`. Round 2 review correctly flagged that
simply removing a member from the mock's membership map *before* calling
`svc.ShareMeetingToAccount` doesn't exercise Step 2's fix: `ListAccountMembers` (called
first, inside `ShareMeetingToAccount`) would just never return that member at all, so the
per-member `CreateShare` loop naturally skips them regardless of whether Step 2's extra
`GetMember` recheck exists — the OLD code would pass this test too. To actually simulate
"member removed in the gap between the members list snapshot and this particular member's
share write", add the same kind of test-only hook used in Task 1 Step 4
(`mockMeetingRepo`'s `GetMember`, or reuse a similarly-named hook if `mockAccountRepo`'s
`deleteAfterGet` pattern is more naturally shared — check whether `mockMeetingRepo` and
`mockAccountRepo` are the same struct or genuinely separate, and place the hook wherever
`ShareMeetingToAccount`'s `GetMember` recheck actually reads from): seed 2 members, set the
hook so `GetMember` returns `nil` for one specific member on its NEXT call (simulating
"deleted after the ListAccountMembers snapshot, before this member's write"), call
`svc.ShareMeetingToAccount`, and assert `CreateShare` was invoked for only the remaining
member.

Update every mock whose interface gained methods this task added
(`GetShare`/`DeleteShare` on `accountRepo`; the `origin` parameter on every `CreateShare`
mock per Step 1's note) — this includes, at minimum,
`backend/internal/handler/account_test.go`'s `mockHandlerAccountRepo` and
`backend/internal/handler/meeting_test.go`'s `mockHandlerMeetingRepo`. Run
`grep -rn "CreateShare\|GetShare\|DeleteShare" backend/internal/**/*_test.go` before
finishing this step and confirm every match is either already updated or doesn't need to be
(e.g. a mock for an interface that never had these methods).

- [ ] **Step 8: Verify**

`cd backend && /home/atomoh/go-sdk/go/bin/go build ./... && /home/atomoh/go-sdk/go/bin/go test ./...` — full suite green, including all new tests from Step 7.

---

## Final verification (all tasks)

- [ ] `cd backend && /home/atomoh/go-sdk/go/bin/go build ./... && /home/atomoh/go-sdk/go/bin/go test ./...`
- [ ] `cd frontend && npm run lint && npx tsc --noEmit && npm run build`
- [ ] Re-read the diff against the original 3 MAJOR findings and confirm each is concretely addressed (conditional update, Enter-key guard, Share revocation + corrected comment).
