# Owner-approved Account sharing Implementation Plan

Status: Historical implementation plan; deployment acceptance is recorded separately.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Share current completed notes and Insights with an Account without regeneration.

**Architecture:** Add a separate owner disclosure stamp without changing summary provenance or generation. Mint it atomically with canonical publication and validate it in all Account readers; retain source-only regeneration as an option.

**Tech Stack:** Go, DynamoDB expression builder, Python QA, Next.js/React, CDK TypeScript.

**Spec:** `docs/superpowers/specs/2026-11-04-owner-approved-account-sharing.md`

## Global Constraints

- Preserve current notes, derivatives and analysis state during sharing.
- Approval is owner-only and Account-scoped; inherited access remains read-only.
- `SUMMARY_OWNER_ACCOUNT_SHARE_ENABLED` defaults to `0`; install readers first.
- Ordinary content/live writes invalidate approval even during writer rollback.
- Documentation is English; product copy follows existing Korean UI.
- Preserve unrelated dirty source checkout and production parameters.

## Review Focus

- Legacy notes without a generation keep their existing action analysis identity.
- Same-content regeneration changes generation and invalidates old approval.
- Moving/unpublishing an Account cannot reuse a previous audience approval.
- Forged approval fields cannot bypass trusted owner publication.
- Metadata and full Go/Python reads must make the same scope decision.

### Task 1: Owner disclosure and canonical publication

**Files:** model/meeting.go, model/summary_privacy.go, repository/summary_privacy.go,
repository/dynamodb.go, service/meeting.go and focused model/repository/service tests.

**Interfaces:**
- Produces `SummaryAccountApproval`, `Meeting.AccountSummaryShareable() bool`,
  `Meeting.AccountSummaryMetadataShareable() bool`.
- Produces `PublishMeetingToAccount(ctx, ownerID, meetingID, accountID) error`.
- Consumes canonical snapshot and direct member CAS from the existing writer.

- [x] Add failing model and SDK transport tests for preserved private/unknown output, wrong scope, changed bytes/generation, legacy empty generation, races and forged server fields.
- [x] Run focused Go tests and observe failure.
- [x] Implement server-only Account approval, preserving generation and analysis fields; invalidate it on every content/live publication path.
- [x] Run focused tests and confirm success.
- [ ] Commit independently testable publication/read contract.

### Task 2: Reader compatibility and user-visible behavior

**Files:** service/summary_privacy.go, service/account.go, repository/account.go,
Python qa/summary_privacy.py and reader projection/fingerprint callers,
frontend meeting AccountSection/ResummaryControls/types and relevant tests.

**Interfaces:**
- Consumes Task 1 metadata/full-read methods and approved stamp.
- Produces Python `account_summary_shareable(record)` and API
  `summarySharing.canShareToAccount`.

- [x] Add failing protected Account/Insight/QA reader tests, including stale approval and inherited reads.
- [x] Run affected Go/Python tests and observe failure.
- [x] Apply the same audience/owner/generation/hash decision to projections, source fingerprints and full reads.
- [x] Update status and concise copy so Account sharing preserves current output and regeneration stays optional.
- [x] Run affected tests and frontend lint/unit/build.
- [ ] Commit the compatible readers and UX.

### Task 3: Controlled rollout and delivery

**Files:** infra/lib/gateway-stack.ts, infra privacy tests, ADR-029 and current contract/runbook references.

**Interfaces:**
- Consumes reader compatibility and API-only writer gate.
- Produces default-off CfnParameter `SummaryOwnerAccountShareEnabled`.

- [x] Test default-off API-only wiring and privacy-writer prerequisite.
- [x] Add rollout parameter and update ADR-029's narrowly superseded disclosure requirement.
- [x] Run Go full checks, Python QA, frontend checks, infra synth/tests and docs checks.
- [ ] Request fresh code/security/content review, fix supported findings, create PR and pass exact-head required review/CI.
- [ ] Merge after required checks; deploy readers with approval disabled, verify packages, then enable the API writer and publish frontend.
- [ ] Record deployed code/config evidence and remaining authenticated browser acceptance limits.
