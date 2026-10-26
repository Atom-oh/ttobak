# Designated Account Owner Implementation Plan

Status: Historical implementation record. Execution is in progress.

> **For agentic workers:** Use superpowers:executing-plans to implement this plan
> inline. Existing session authorization covers implementation, current-HEAD
> review, merge and deployment. The user approved the creator-membership
> checkbox; no additional approval is inferred from workflow templates.

**Goal:** Create an Account for another Owner, provision and invite a missing
login identity, and let the creator choose whether to remain an AM member.

**Architecture:** Keep owner designation exclusive to initial creation. Bind
the selected Owner to the current Cognito sub and transactionally create META
and initial members; a creation UUID and normalized-input hash make retries
safe. The frontend exposes the checkbox and retains a limited creation receipt
when the creator opts out.

**Tech Stack:** Go, DynamoDB expressions/transactions, Cognito, Next.js, React,
stdlib Go tests and node:test.

**Spec:** [Approved design](../specs/2026-10-26-designated-account-owner-design.md).

## Global constraints

- Verified requester email precedes designated-owner identity side effects.
- New/resend attempts share the existing 20-per-caller-per-UTC-hour counter.
- Initial owner and optional creator membership are atomic and privacy-stamped.
- Checkbox defaults unchecked only when another Owner is selected.
- No owner promotion through AddMember/UpdateMemberRole; no public signup.
- Parent authorization belongs to the creator, not the selected Owner.
- Opted-out creators gain no Account reads or implicit meeting/document shares.
- Creation JSON is at most 16 KiB and contains exactly one JSON value.
- Owner-invitation delivery recovery by an opted-out creator lasts 30 days.
- Keep existing origin/config/privacy flags and deployment workflows unchanged.
- Review current HEAD, content score at least 85, API before frontend delivery.

## Review focus

- A timed-out successful create must not produce another Account or resend mail.
- Replaying creation after creator removal must not restore membership.
- A recreated same-email identity must not take over an existing Account.
- Opting out after selecting additional members must not send hidden invites.
- Parent membership loss during mail preparation must prevent the final create.

## Task 1: Identity-bound atomic creation

**Files:** model/account.go; service/account_creation.go and tests;
repository/account_creation.go and tests; existing account service/test seams.

**Interfaces:**

- Add OwnerEmail, KeepCreatorAsMember, RequestID to CreateAccountRequest.
- Add CreatedByUserID and CreationRequestHash to Account metadata.
- AccountCreationResponse embeds AccountResponse and exposes creatorMember,
  ownerInvitationPending and ownerInvitationRequested.
- CreateAccountWithOwner(ctx, requesterID, requesterEmail, req) returns
  (*model.AccountCreationResponse, error); preserve legacy CreateAccount callers.
- Optional repository capability CreateAccountWithInitialMembers(ctx, account,
  owner, creator, requesterID, ancestors) atomically creates initial state.

- [ ] Add failing service tests for a designated Owner and both checkbox states;
  assert canonical owner, membership count and no creator grant when unchecked.
- [ ] Run focused Go tests and retain the expected missing-feature failure.
- [ ] Implement DTOs, validated identity resolution and atomic transaction.
- [ ] Add red/green tests for parent authorization before mail, transactional
  requester checks, identity mismatch, domain/quota failures and privacy stamps.
- [ ] Add request-ID/fingerprint replay tests including an ambiguous committed
  transaction and a later removed creator; implement bounded receipt replay.
- [ ] Run service/repository suites and commit the tested deliverable.

## Task 2: Authenticated HTTP and invitation recovery

**Files:** handler/account.go and creation tests; service/account_creation.go;
cmd/api/main.go; model response/error conventions.

**Interfaces:**

- POST /api/accounts uses CreateAccountWithOwner and truthful creation response.
- POST /api/accounts/{accountId}/owner-invitation sends only the canonical
  initial Owner's pending invitation; no caller-selected recipient.
- ResendOwnerInvitation(ctx, requesterID, accountID) returns
  (*model.AccountMemberDTO, error).

- [ ] Add failing handler tests for creator versus actual Owner and strict
  bounded single-JSON decoding; implement and verify.
- [ ] Add failing resend tests for a current member, opted-out initial creator
  within 30 days, expired creator authority, foreign caller, confirmed user and
  changed identity; implement using existing resend guards.
- [ ] Verify generic member endpoints still reject owner assignment.
- [ ] Run all Go packages including cmd, vet and Linux ARM64 API build.
- [ ] Commit the tested authenticated API behavior.

## Task 3: Owner picker and creator checkbox

**Files:** components/AccountsClient.tsx; components/AccountDetailClient.tsx;
lib/api.ts; types/meeting.ts; lib/accountCreation.ts; scripts/account-creation.test.mjs.

**Interfaces:** Frontend creation request/receipt matches Task 1. Expose the
canonical-owner resend API from Task 2. Pure helpers normalize submission,
create notices and determine whether to navigate/add members.

- [ ] Add failing executable component/helper tests for Me/another Owner,
  checked/unchecked creator membership and correct request fields.
- [ ] Implement Owner picker and checkbox, clear hidden additional selections,
  keep one creation UUID through retries, and consume the truthful receipt.
- [ ] Add red/green tests proving opt-out stays on the list and never requests
  Account detail or additional-member grants.
- [ ] Add red/green tests for failed/ambiguous create, partial additional-member
  results, invitation recovery and stale navigation.
- [ ] Run lint, all frontend unit tests, TypeScript and production build.
- [ ] Commit the tested UI behavior.

## Task 4: Review and production delivery

**Files:** ADR-055; ADR-015/034/054; API/DESIGN/README and canonical review context.
Operational evidence stays outside tracked source.

- [ ] Document the narrow initial-owner exception and checkbox behavior; sync
  generated review context and run documentation checks.
- [ ] Obtain independent whole-branch review; fix meaningful findings with
  regression tests. Preserve complete required Claude/Codex CI coverage.
- [ ] Open the feature PR, drive authorized review fixes up to 15 rounds, and
  merge only current reviewed HEAD with all required checks green.
- [ ] Build exact merged main artifacts and freeze Gateway-only assembly.
  Verify actual source provenance, converter image and content score at least 85.
- [ ] Publish the authenticated API before the static frontend; preserve
  runtime config and old chunks, invalidate CloudFront.
- [ ] Verify installed code, frontend bytes, origin boundaries and unchanged
  privacy/config flags; write a dated deployment receipt.
- [ ] Report the checkbox behavior and deployment outcome without claiming
  real-recipient mail or first-login acceptance that was not performed.
