# Recording Account picker implementation plan

Status: Historical implementation plan; production acceptance remains separate.

> **For agentic workers:** Use superpowers:executing-plans for the scoped implementation. Steps use checkbox syntax for tracking.

**Goal:** Show available directly assignable Accounts before recording, and refresh readable reference Accounts when memberships change.

**Architecture:** Keep authenticated discovery in `accountApi.list()` and preserve existing direct-membership filters. Give SA Account and personal-document requests independent completion paths, refresh both recording pickers on authenticated membership revisions, and reject stale request results.

**Tech Stack:** Next.js/React TypeScript; existing `node:test` component lifecycle harnesses.

**Spec:** Approved independent bug scope: available directly assignable Accounts must appear when starting recording; inherited read-only access must not become assignment permission.

## Constraints and evidence

- Reviewed base: `4c6de607`; use the existing isolated worktree.
- No backend, `api.ts`, assignment-permission, commit, push, or deployment changes.
- `RecordingConfig.tsx` has summary/translation settings and no Account picker.
- The recording page uses `SAPreparation` and `ReferencePanel` on web and in the Mac WebView.
- `accountApi.list()` already consumes paginated and legacy responses, advancing empty pages, and declared overlaps. No pagination/shape defect was established.
- `SAPreparation.loadCatalog()` awaits both `accountApi.list()` and `docApi.list('prep')` before publishing either result. A pending document request therefore hides a successful Account response.
- `AuthProvider` increments `membershipRevision` after invitation materialization without unmounting active forms. Neither recording picker currently observes that revision.
- These are reproducible code paths, not confirmed causes of the user's reported production symptom. No authenticated browser or private meeting inspection is claimed.

## Review focus

- Delayed or rejected personal-document discovery must not hide healthy Account options.
- Later membership materialization must refresh an initially empty recording picker.
- Older requests must not replace newer identity/revision/retry results.
- Inherited Accounts remain excluded from assignment but visible in reference browsing.
- Load failures remain distinguishable from a successfully empty Account list and remain retryable.

## Task 1: SA preparation catalog lifecycle

**Files:** `frontend/src/components/record/SAPreparation.tsx`; new `frontend/scripts/recording-account-picker.test.mjs`.

**Interfaces:** Consume unchanged `accountApi.list()`, `docApi.list('prep')`, `useAuth().membershipRevision`, and `isEditableAccount()`. Preserve the component props and recording creation's separate direct-membership validation.

- [x] Write lifecycle tests for independently completing Accounts, membership refresh, retry, and stale results.
- [x] Run the tests against the original component; record behavioral failures.
- [x] Publish Account and document results independently with separate errors/loading and request cleanup.
- [x] Refresh on authenticated user/membership revision; retain the direct assignment filter.
- [x] Run focused picker and existing inherited-control/binding/discovery tests.

## Task 2: Reference picker membership lifecycle

**Files:** `frontend/src/components/ReferencePanel.tsx`; picker lifecycle tests above.

**Interfaces:** Preserve readable `accountApi.list()` results, including inherited Accounts, and existing `value`/`onChange` props.

- [x] Write a membership-revision refresh regression and a stale-response regression.
- [x] Run them against the original picker and record the failures.
- [x] Refresh on authenticated user/membership revision and reset request loading/errors.
- [x] Run focused tests and lint for changed files.

## Verification ledger

- RED: `node --test scripts/recording-account-picker.test.mjs` against the original components reported 7 tests, 2 passing and 5 behavioral failures. Healthy Accounts were hidden behind pending documents; SA/reference membership refresh did not issue a second request; stale-result scenarios could not observe the newer list.
- RED: the separately added loading/empty-state regression failed against the original component because its Account select had no loading state.
- GREEN: the final picker suite has 9 passing tests. It also verifies an Account failure can be retried while an older personal-document request remains pending.
- GREEN: `node --test scripts/recording-account-picker.test.mjs scripts/account-inherited-controls.test.mjs scripts/recording-account-binding.test.mjs scripts/account-discovery-pages.test.mjs` reports 48 tests, 48 passing, 0 failures.
- Changed-file ESLint passes without suppressions. Request state is derived from the identity/revision/retry key, and completed callbacks publish only while their effect remains active.
- The existing static inherited-controls fixture was adjusted for the additional ReferencePanel request-key state slot; its access assertions are unchanged.
- No authenticated browser acceptance, production cause verification, deployment, commit, or push was performed. Broader integration checks and independent review belong to the parent task.
