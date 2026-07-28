# PR #114 Round 8 Fix — Surface Ambiguous Untagged Shares in RemoveMember's Response

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Resolve the AI Code Review's persistent MAJOR on PR #114 (`feat/account-team-members`): a legacy account-origin `Share` (written by `ShareMeetingToAccount` before the `Origin` field existed, so `Origin == ""`) is indistinguishable from an owner's direct grant, so `RemoveMember`'s cleanup silently `continue`s past it and the removed member keeps transcript/summary/KB access forever with zero signal to the operator. The review's own suggestion #1 (round-7 review) is the fix: surface these skipped-because-ambiguous meetings in `RemoveMember`'s response instead of leaving them invisible, so an operator can react (run `backend/cmd/backfill-share-origin` for that account) instead of never finding out.

**Context (code-verified before writing this plan):**
- `backend/internal/service/account.go`'s `RemoveMember` cleanup loop currently has:
  ```go
  if share == nil || share.Origin != model.ShareOriginAccount {
      continue
  }
  ```
  This one `continue` covers two very different cases with no way to tell them apart in the response: (a) no share exists (nothing to do, fine), and (b) a share exists but has `Origin == ""` on a meeting still shared to this account — which is EITHER a legacy account-share written before the `Origin` field existed, OR a perfectly normal, correctly-preserved direct grant the owner made explicitly to the SAME removed member for a meeting that also happens to be linked to this account (both collapse to the identical `Origin == ""` shape — confirmed via `account_test.go`'s existing `TestRemoveMember_PreservesDirectShare`, which asserts a direct share with `Origin: ""` survives cleanup untouched, and `CreateShare`'s own clobber-guard, which deliberately leaves a collision at `Origin==""`). **This means a naive "flag every `Origin != account` share as legacy" surfaces a false positive whenever the removed member ALSO holds a direct share to a meeting linked to this account** — noise on that specific removal, not a useful signal for that meeting. This was caught in this plan's own plan-gate review (codex, MAJOR, confirmed against `account_test.go:524` and `CreateShare`'s clobber-guard) and is fixed below by renaming the field to `AmbiguousUntaggedMeetingIDs` and documenting the false-positive possibility explicitly, rather than pretending the signal is precise.
- `RemoveMember` currently returns `(failedMeetingIDs []string, err error)`. This plan changes the return type to a new `*RemoveMemberResult{FailedMeetingIDs, AmbiguousUntaggedMeetingIDs []string}` struct so the two lists are distinguishable to callers.
- The HTTP handler (`backend/internal/handler/account.go`'s `RemoveMember`) currently returns 204 when `len(failedMeetingIDs) == 0`, or 200 + `{"removed": true, "cleanupFailedForMeetings": [...]}` otherwise. This plan adds an `ambiguousUntaggedMeetingIDs` field to that same 200 body, triggering the 200 (non-204) path whenever either list is non-empty. Both slices are explicitly initialized to `[]string{}` (not left as a nil zero value) so the JSON response always has `[]`, never `null`, for these two fields — a nil Go slice marshals to JSON `null`, which is an unnecessary special case for every consumer of this response to handle.
- `docs/API-SPEC.md`'s "Remove Member" section documents the current 204/200 contract and already has a "Known limitation & remediation" paragraph about legacy shares — this plan updates both to mention the new response field, INCLUDING the false-positive caveat (the removed member's own direct share to a meeting linked to this account will be listed here too — this is a coarse signal, not a precise "these are definitely legacy" list).

**Explicitly out of scope for this round (tracked as follow-ups, not silently dropped):**
1. **Frontend wiring** — `frontend/src/lib/api.ts`'s `removeMember` return type and `AccountDetailClient.tsx`'s `handleRemoveMember` only reference `cleanupFailedForMeetings` today; this plan does not add `ambiguousUntaggedMeetingIDs` to either. The new field is API-visible (inspectable via network tab / curl) but not yet shown in the product UI. Flagged here so it isn't mistaken for an oversight.
2. **Backfill CLI remediation for already-removed members** — `backend/cmd/backfill-share-origin/main.go` only iterates `ListAccountMembers` (current members), so a member who is already removed by the time this new signal is seen has no CLI path back to remediation for their own share. This plan's field makes the problem *visible* sooner (at removal time, not after-the-fact discovery), which narrows but does not close this gap.

**Tech Stack:** Go (chi router, sentinel errors, typed DTOs).

---

## Task 1: Add `RemoveMemberResult` and surface ambiguous untagged shares

**Files:**
- Modify: `backend/internal/service/account.go`
- Modify: `backend/internal/service/account_test.go`
- Modify: `backend/internal/handler/account.go`
- Modify: `backend/internal/handler/account_test.go`
- Modify: `docs/API-SPEC.md`

- [ ] **Step 1: Define `RemoveMemberResult` and change `RemoveMember`'s signature**

In `backend/internal/service/account.go`, above `RemoveMember`, add:
```go
// RemoveMemberResult reports the outcome of RemoveMember's best-effort Share
// cleanup. FailedMeetingIDs is a genuine cleanup error (retryable, worth
// alerting on). AmbiguousUntaggedMeetingIDs is NOT a failure -- it flags
// meetings where a Share exists with Origin != "account" on a meeting still
// shared to this account, so cleanup could not safely touch it. This is a
// COARSE signal, not a precise "these are legacy" list: Origin=="" is the
// shape of BOTH a legacy account-share (written before the Origin field
// existed) AND a perfectly normal direct grant the owner made explicitly --
// they are indistinguishable at this layer (see model.Share.Origin's doc
// and CreateShare's clobber-guard). Any meeting where the removed member
// ALSO holds a direct share to a meeting still linked to this account will
// appear here too, regardless of whether a legacy account-share also
// exists; this is intentional noise traded for not silently hiding the
// legacy case, not a bug. Surfacing this list gives an operator a signal to
// check whether backfill-share-origin is needed for this account, without
// RemoveMember itself guessing wrong and revoking an owner's explicit
// direct share.
type RemoveMemberResult struct {
	FailedMeetingIDs            []string
	AmbiguousUntaggedMeetingIDs []string
}
```
Change `RemoveMember`'s signature from `(failedMeetingIDs []string, err error)` to `(*RemoveMemberResult, error)`. Every early `return nil, err`/`return nil, ErrX` in the function already returns `nil` for the first value, so those lines need no change — only the success-path plumbing does.

- [ ] **Step 2: Split the single `continue` into two branches, build the result with pre-initialized (non-nil) slices**

Replace the named-return `failedMeetingIDs = append(...)` pattern with a local result built up through the loop, returned at the end:
```go
result := &RemoveMemberResult{
	FailedMeetingIDs:            []string{},
	AmbiguousUntaggedMeetingIDs: []string{},
}
```
(Pre-initializing to empty slices, not `nil`, means the JSON response always encodes `[]` for these fields rather than `null` when nothing was appended.)

Where the loop currently does:
```go
if share == nil || share.Origin != model.ShareOriginAccount {
    continue
}
```
split it into:
```go
if share == nil {
    continue
}
if share.Origin != model.ShareOriginAccount {
    // Origin != "account" here (in practice always Origin=="") means cleanup
    // must NOT touch this share -- it might be a legitimate direct grant.
    // But it's ALSO the exact shape a legacy pre-Origin-field account-share
    // has, and there is no way to tell the two apart at this layer (see the
    // RemoveMemberResult doc comment above). Report it as ambiguous rather
    // than silently doing nothing, even knowing this will include false
    // positives for accounts that have real direct shares.
    result.AmbiguousUntaggedMeetingIDs = append(result.AmbiguousUntaggedMeetingIDs, ref.MeetingID)
    continue
}
```
Every other `failedMeetingIDs = append(failedMeetingIDs, ref.MeetingID)` in the function (the `GetMeetingByID` error path, the `GetShare` error path, the `DeleteShareIfAccountOrigin` non-condition-failure error path) becomes `result.FailedMeetingIDs = append(result.FailedMeetingIDs, ref.MeetingID)`.

- [ ] **Step 3: Update the handler to read the new type and add the response field**

In `backend/internal/handler/account.go`'s `RemoveMember`, change `failedMeetingIDs, err := h.accountService.RemoveMember(...)` to `result, err := h.accountService.RemoveMember(...)`. Change the 200-vs-204 branch condition from `len(failedMeetingIDs) > 0` to `len(result.FailedMeetingIDs) > 0 || len(result.AmbiguousUntaggedMeetingIDs) > 0`, and add `"ambiguousUntaggedMeetingIDs": result.AmbiguousUntaggedMeetingIDs` alongside the existing `"cleanupFailedForMeetings": result.FailedMeetingIDs` in the response map. Run `gofmt` on both edited files to fix struct-literal field alignment.

- [ ] **Step 4: Update tests**

`backend/internal/service/account_test.go`'s `TestRemoveMember_CleanupFailureDoesNotFailRemoval` currently does `failed, err := svc.RemoveMember(...)` then checks `len(failed) != 1 || failed[0] != "m-1"` — change to check `result.FailedMeetingIDs` instead of `failed` directly (var name `result`, type is now `*RemoveMemberResult`). All other `RemoveMember` call sites in that file use `_, err := svc.RemoveMember(...)` or `if _, err := svc.RemoveMember(...); err != nil` — those compile unchanged since only the discarded value's type changed.

Add two new tests in the same file:
1. `TestRemoveMember_SurfacesAmbiguousLegacyShare` — create an account, add a TAM member, set `repo.meetingRefs[acc.AccountID]` to one ref, set `repo.meetings["m-1"]` to `&model.Meeting{MeetingID: "m-1", AccountID: acc.AccountID}` (needed since round 7 added a `GetMeetingByID` cross-account guard before the share check), set `repo.shares[acctShareKey("tam-1", "m-1")]` to a `Share` with `Origin: ""` (legacy shape). Call `RemoveMember`, assert `err == nil`, assert `result.AmbiguousUntaggedMeetingIDs` equals `["m-1"]`, assert `len(result.FailedMeetingIDs) == 0` (this is NOT a failure), and assert the share itself was left untouched (`repo.shares[acctShareKey("tam-1", "m-1")] != nil`).
2. `TestRemoveMember_EmptyResultListsAreNotNil` — create an account, add and then remove a member with NO meeting refs at all (empty account). Call `RemoveMember`, assert `err == nil`, then assert `result.FailedMeetingIDs != nil && len(result.FailedMeetingIDs) == 0` and the same for `result.AmbiguousUntaggedMeetingIDs` — proves Step 2's pre-initialization actually took effect (a test using `reflect.DeepEqual(result.FailedMeetingIDs, []string{})` or simply checking `!= nil` both work; prefer the explicit nil-check since that's the exact JSON-encoding distinction that matters).

Add one new test in `backend/internal/handler/account_test.go` (mirroring the existing `TestHandlerRemoveMember_PartialCleanupFailureReturns200WithBody`'s setup style): `TestHandlerRemoveMember_AmbiguousShareReturns200WithBody` — set up an account/member/meeting/ref the same way, but give the share `Origin: ""` (ambiguous shape) instead of forcing a `shareOpErr`. Call the handler, assert the HTTP status is 200 (not 204), decode the JSON body and assert `cleanupFailedForMeetings` is present and equals `[]` (an empty array, not absent/null — confirms the pre-initialized-slice fix from Step 2 survives JSON encoding through the real handler path, not just the service-layer struct), and assert `ambiguousUntaggedMeetingIDs` equals `["m-1"]`.

- [ ] **Step 5: Update API-SPEC.md**

In `docs/API-SPEC.md`'s "Remove Member (owner 전용)" section: add `ambiguousUntaggedMeetingIDs` to the documented 200 response body example (a sibling field to `cleanupFailedForMeetings`); update the existing "Response: 200 OK (멤버십 삭제는 성공했으나 일부 미팅의 Share cleanup이 실패한 경우)" line's condition text to also cover the ambiguous-share case (200 now triggers on cleanup failure OR an ambiguous untagged share, not failure alone) so the status-code contract description matches Step 3's actual `||` condition; and add to the existing "Known limitation & remediation" paragraph: (a) `RemoveMember` now surfaces which meetings hit this exact ambiguity in its own response, and (b) this is explicitly a COARSE/noisy signal — **specifically**, any meeting where the removed member ALSO holds a direct share (independent of whether a legacy account-share exists) will appear here too, so the presence of an entry does not by itself mean "this is definitely a legacy share needing backfill." Also add one sentence noting the frontend does not yet surface this new field (tracked as a follow-up, not silently dropped) and that already-removed members still have no backfill CLI path back to remediation (also a follow-up).

- [ ] **Step 6: Verify**

`cd backend && /home/atomoh/go-sdk/go/bin/go build ./... && /home/atomoh/go-sdk/go/bin/go test ./...` — full suite must stay green, including both new tests and all existing `RemoveMember`/`TestHandlerRemoveMember_*` tests (handler tests use `map[string]interface{}` response parsing already, so they're unaffected by the Go-side type change as long as the JSON field names match).

---

## Verification Summary

- Go build + full test suite green (`internal/service`, `internal/handler`, and everything else — no other package touches `RemoveMember`'s return type).
- New test 1 proves an ambiguous (potentially-legacy) share is (a) left untouched, (b) reported in `AmbiguousUntaggedMeetingIDs`, (c) NOT counted as a failure.
- New test 2 proves both result slices are non-nil (encode as `[]`, not `null`) even when nothing was appended.
- `docs/API-SPEC.md` reflects the new response field AND its false-positive caveat, plus the two explicitly-scoped-out follow-ups (frontend wiring, already-removed-member backfill path).
