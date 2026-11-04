# Recording Customer Loading Implementation Plan

Historical implementation record. Status: Local verification passed
(2026-11-04); production acceptance pending.

> **For agentic workers:** Use `superpowers:executing-plans` to implement this
> scoped fix inline. Steps use checkboxes for tracking.

**Goal:** Make directly editable recording customers available without waiting
for descendant discovery or requests from collapsed reference preparation.

**Architecture:** Add a bounded direct-membership scope to the existing account
listing API, use it from recording preparation, and mount reference preparation
on first expansion, retaining opened panel state after collapse. Preserve
canonical authorization and default descendant listing.

**Tech Stack:** Go, DynamoDB repository primitives, React, Next.js, node:test.

**Spec:** `docs/superpowers/specs/2026-11-04-recording-account-loading-design.md`

## Global Constraints

- Scan at most 25 direct candidates per API page.
- Recheck canonical membership and metadata for every returned row.
- Bind continuations to the user, scope, requested size and anchor revision.
- Prefer deploying the API producer before the frontend consumer; the first-request
  compatibility guard also supports independent automatic deployments.
- Preserve all inherited browsing and recording request-generation guards.

## Review Focus

- Stale index membership must never become inherited edit access.
- Revocation or role/metadata changes before publication must invalidate output.
- Empty advancing pages and continuation across scope/user changes must be safe.
- Initially collapsed references must make no catalog requests; after first
  expansion, collapsing must retain drafts and previously loaded panel state.
- Invitation refresh and user changes must still discard older completions.

### Task 1: Direct customer listing

**Files:** `backend/internal/service/account_read_discovery.go`,
`account_direct_list.go`, `account_direct_list_test.go`,
`docs/API-SPEC.md`. The existing handler already routes scoped queries correctly.

**Interface:** `GET /api/accounts?pageSize=100&scope=direct` returns the existing
`AccountListPage`; default scope remains unchanged.

- [x] Write failing service tests for bounded direct listing, stale membership,
  revocation, continuation, scope binding and descendant flag compatibility.
- [x] Run the new tests and confirm the expected failures.
- [x] Implement direct scope with one navigation query and canonical rechecks.
- [x] Run Go tests, vet and the API ARM64 build; update the API reference.

### Task 2: Recording consumers

**Files:** `frontend/src/lib/api.ts`,
`frontend/src/components/record/SAPreparation.tsx`,
`frontend/src/app/record/page.tsx`, relevant frontend tests,
`docs/DESIGN-SPEC.md`.

**Interfaces:** `accountApi.list({ scope: 'direct' })`; reference preparation
mounts on details expansion and continues using the default scope.

- [x] Write failing transport/component tests for direct scope and lazy catalogs.
- [x] Run the targeted tests and confirm the expected failures.
- [x] Implement the direct request and conditional reference mount.
- [x] Run frontend lint, unit tests and production build; update the UI reference.
- [ ] Obtain independent code review and resolve material findings.
- [ ] Commit and submit the reviewed fix; deploy API before frontend when gates pass.
