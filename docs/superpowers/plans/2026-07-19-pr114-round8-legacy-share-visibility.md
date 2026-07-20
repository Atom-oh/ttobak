# PR #114 Round 8 Fix — Surface Legacy Untagged Shares in RemoveMember's Response

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Resolve the AI Code Review's persistent MAJOR on PR #114 (`feat/account-team-members`): a legacy account-origin `Share` (written by `ShareMeetingToAccount` before the `Origin` field existed, so `Origin == ""`) is indistinguishable from an owner's direct grant, so `RemoveMember`'s cleanup silently `continue`s past it and the removed member keeps transcript/summary/KB access forever with zero signal to the operator. The review's own suggestion #1 (round-7 review) is the fix: surface these skipped-because-ambiguous meetings in `RemoveMember`'s response instead of leaving them invisible, so an operator can react (run `backend/cmd/backfill-share-origin` for that account) instead of never finding out.

**Context (code-verified before writing this plan):**
- `backend/internal/service/account.go`'s `RemoveMember` cleanup loop currently has:
  ```go
  if share == nil || share.Origin != model.ShareOriginAccount {
      continue
  }
  ```
  This one `continue` covers two very different cases with no way to tell them apart in the response: (a) no share exists (nothing to do, fine), and (b) a share exists but predates the `Origin` field (`Origin == ""`) on a meeting still shared to this account — a legacy account-share cleanup cannot safely touch (touching it risks revoking an owner's actual direct grant, since the two collapse into the same DB shape). Case (b) is the one the review wants surfaced.
- `RemoveMember` currently returns `(failedMeetingIDs []string, err error)`. This plan changes the return type to a new `*RemoveMemberResult{FailedMeetingIDs, LegacyUntaggedMeetingIDs []string}` struct so the two lists are distinguishable to callers.
- The HTTP handler (`backend/internal/handler/account.go`'s `RemoveMember`) currently returns 204 when `len(failedMeetingIDs) == 0`, or 200 + `{"removed": true, "cleanupFailedForMeetings": [...]}` otherwise. This plan adds a `legacyUntaggedMeetingIDs` field to that same 200 body, triggering the 200 (non-204) path whenever either list is non-empty.
- `docs/API-SPEC.md`'s "Remove Member" section documents the current 204/200 contract and already has a "Known limitation & remediation" paragraph about legacy shares — this plan updates both to mention the new response field.

**Tech Stack:** Go (chi router, sentinel errors, typed DTOs).

---

## Task 1: Add `RemoveMemberResult` and surface legacy shares

**Files:**
- Modify: `backend/internal/service/account.go`
- Modify: `backend/internal/service/account_test.go`
- Modify: `backend/internal/handler/account.go`
- Modify: `docs/API-SPEC.md`

- [ ] **Step 1: Define `RemoveMemberResult` and change `RemoveMember`'s signature**

In `backend/internal/service/account.go`, above `RemoveMember`, add:
```go
// RemoveMemberResult reports the outcome of RemoveMember's best-effort Share
// cleanup: FailedMeetingIDs is a genuine cleanup error (retryable, worth
// alerting on); LegacyUntaggedMeetingIDs is not a failure at all -- it flags
// meetings where a Share exists but predates the Origin field (origin=="")
// on a meeting still shared to this account, so cleanup could not tell it
// apart from a direct grant and left it untouched. Surfacing this list gives
// an operator the same visibility the backfill CLI's dry-run output does,
// without RemoveMember itself guessing wrong and revoking an owner's
// explicit direct share.
type RemoveMemberResult struct {
	FailedMeetingIDs         []string
	LegacyUntaggedMeetingIDs []string
}
```
Change `RemoveMember`'s signature from `(failedMeetingIDs []string, err error)` to `(*RemoveMemberResult, error)`. Every early `return nil, err`/`return nil, ErrX` in the function already returns `nil` for the first value, so those lines need no change — only the success-path plumbing does.

- [ ] **Step 2: Split the single `continue` into two branches, build the result**

Replace the named-return `failedMeetingIDs = append(...)` pattern with a local `result := &RemoveMemberResult{}` built up through the loop, returned at the end (`return result, nil`). Where the loop currently does:
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
    // origin=="" here can't be distinguished from an owner's explicit
    // direct grant (see model.Share.Origin's doc), so cleanup must NOT
    // touch it -- but a legacy account-share written before the Origin
    // field existed collapses into this exact same shape. Surface it
    // instead of silently doing nothing, so an operator has a signal to
    // run backfill-share-origin for this account before it's too late
    // (this member is already removed, so ListAccountMembers will never
    // surface them as a backfill candidate again).
    result.LegacyUntaggedMeetingIDs = append(result.LegacyUntaggedMeetingIDs, ref.MeetingID)
    continue
}
```
Every other `failedMeetingIDs = append(failedMeetingIDs, ref.MeetingID)` in the function (the `GetMeetingByID` error path, the `GetShare` error path, the `DeleteShareIfAccountOrigin` non-condition-failure error path) becomes `result.FailedMeetingIDs = append(result.FailedMeetingIDs, ref.MeetingID)`.

- [ ] **Step 3: Update the handler to read the new type and add the response field**

In `backend/internal/handler/account.go`'s `RemoveMember`, change `failedMeetingIDs, err := h.accountService.RemoveMember(...)` to `result, err := h.accountService.RemoveMember(...)`. Change the 200-vs-204 branch condition from `len(failedMeetingIDs) > 0` to `len(result.FailedMeetingIDs) > 0 || len(result.LegacyUntaggedMeetingIDs) > 0`, and add `"legacyUntaggedMeetingIDs": result.LegacyUntaggedMeetingIDs` alongside the existing `"cleanupFailedForMeetings": result.FailedMeetingIDs` in the response map. Run `gofmt` on both edited files to fix struct-literal field alignment.

- [ ] **Step 4: Update tests**

`backend/internal/service/account_test.go`'s `TestRemoveMember_CleanupFailureDoesNotFailRemoval` currently does `failed, err := svc.RemoveMember(...)` then checks `len(failed) != 1 || failed[0] != "m-1"` — change to check `result.FailedMeetingIDs` instead of `failed` directly (var name `result`, type is now `*RemoveMemberResult`). All other `RemoveMember` call sites in that file use `_, err := svc.RemoveMember(...)` or `if _, err := svc.RemoveMember(...); err != nil` — those compile unchanged since only the discarded value's type changed.

Add a new test `TestRemoveMember_SurfacesLegacyUntaggedShare` in the same file: create an account, add a TAM member, set `repo.meetingRefs[acc.AccountID]` to one ref, set `repo.meetings["m-1"]` to `&model.Meeting{MeetingID: "m-1", AccountID: acc.AccountID}` (needed since round 7 added a `GetMeetingByID` cross-account guard before the share check), set `repo.shares[acctShareKey("tam-1", "m-1")]` to a `Share` with `Origin: ""` (legacy shape). Call `RemoveMember`, assert `err == nil`, assert `result.LegacyUntaggedMeetingIDs == ["m-1"]`, assert `len(result.FailedMeetingIDs) == 0` (this is NOT a failure), and assert the share itself was left untouched (`repo.shares[acctShareKey("tam-1", "m-1")] != nil`, matching the existing `TestRemoveMember_PreservesDirectShare`'s pattern — a legacy share and a direct share are handled identically by cleanup, which is exactly the point).

- [ ] **Step 5: Update API-SPEC.md**

In `docs/API-SPEC.md`'s "Remove Member (owner 전용)" section, add `legacyUntaggedMeetingIDs` to the documented 200 response body example (a sibling field to `cleanupFailedForMeetings`), and add one sentence to the existing "Known limitation & remediation" paragraph noting that `RemoveMember` now surfaces which meetings hit this exact ambiguity in its own response (not just the backfill CLI's separate dry-run output), so an operator doesn't need to run the CLI speculatively to discover them.

- [ ] **Step 6: Verify**

`cd backend && /home/atomoh/go-sdk/go/bin/go build ./... && /home/atomoh/go-sdk/go/bin/go test ./...` — full suite must stay green, including the new test and all existing `RemoveMember`/`TestHandlerRemoveMember_*` tests (handler tests use `map[string]interface{}` response parsing already, so they're unaffected by the Go-side type change as long as the JSON field names match).

---

## Verification Summary

- Go build + full test suite green (`internal/service`, `internal/handler`, and everything else — no other package touches `RemoveMember`'s return type).
- New test proves a legacy share is (a) left untouched, (b) reported in `LegacyUntaggedMeetingIDs`, (c) NOT counted as a failure.
- `docs/API-SPEC.md` reflects the new response field.
