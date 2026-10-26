# Self-service Account invitations implementation plan

Status: Historical implementation record. Execution is in progress; current
behavior is specified by ADR-054 and deployment requires a separate receipt.

> **For agentic workers:** Use superpowers:executing-plans to implement this plan
> inline. Existing user authorization covers implementation, current-HEAD review,
> merge and deployment; no additional approval is inferred from skill templates.

**Goal:** Let Account members invite unregistered emails without administrator help.

**Architecture:** The authenticated add-member API creates a missing Cognito
identity and queues the existing PendingShare. Claiming revalidates membership.
The client uses the API's invitation receipt rather than an admin-only fallback.

**Tech Stack:** Go, Cognito, DynamoDB transactions, Next.js and node:test.

**Spec:** [ADR-054](../../decisions/ADR-054-self-service-account-invitations.md).

## Global constraints

Keep self-sign-up disabled, domain controls, non-owner roles, identity/expiry
binding, owner-only removal, CloudFront ingress and current rollout flags.
Use 20 invitation or explicit resend attempts per caller per UTC hour; counter
failures stop email creation. Ordinary existing-identity retries send no mail.

## Review focus

Non-member requests, owner-role attempts, concurrent identity creation, failed
grant queues after email delivery, and an inviter removed before claim all need
explicit tests. Do not send mail to real recipients during automated validation.

## Task 1: API and identity-bound grants

Files: Account service/handler/model, API wiring, pending Account transactions
and their service, handler and repository tests.

Interfaces: `AccountService.AddMember` keeps its signature; `AccountMemberDTO`
adds an optional `invited` receipt. A queue failure after invitation is a sentinel
error surfaced as `INVITATION_GRANT_FAILED`. Cognito and window-counter seams
remain injectable.

- [x] Add failing tests for ordinary-member new-email invitation, normalization,
  existing-identity reuse, concurrent creation, denied authority, invalid roles,
  rate limits and partial queue failure.
- [x] Run tests and record the failure before changing behavior.
- [x] Implement bounded invitation creation and membership-checked queue/claim.
- [x] Run Go tests including `cmd/*`, vet and ARM64 builds.

## Task 2: Account creation and detail feedback

Files: `accountInvite.ts`, Account components, API member type and unit tests.

Interfaces: `addMemberWithInvite` consumes only add-member API responses carrying
`pending` and `invited`; it no longer calls the administrator invitation API.

- [x] Add failing tests for server invitation receipts and partial failure.
- [x] Implement both callers and remove administrator-request guidance.
- [x] Run frontend unit tests, lint and production build.
- [x] Update current API/UI references and ADR-034 supersession notes.

## Task 3: Reviewed release

- [ ] Obtain an independent branch review and latest-HEAD required PR coverage;
  fix concrete Critical/Major findings within the user's authorized review bound.
- [ ] Merge only the reviewed SHA after required checks pass.
- [ ] Build source-bound artifacts, retain current configuration and deploy the
  affected artifacts through the guarded release procedure.
- [ ] Pass the mandatory content-review quality gate (at least 85).
- [ ] Verify served frontend assets, API alias and configuration; record the
  deployed revision and the remaining mailbox-delivery acceptance limit.
