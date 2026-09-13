# Pending Grants for Invited Users Without a Profile

> Historical design record. Original date: 2026-08-04 (filename). The original
> addendum records partial implementation in PRs #157/#158. The five-flow proposal
> and old unchecked assumptions do not describe current coverage.

## Problem and original proposal

Email-based sharing failed when an invited Cognito user had not yet opened the app
and therefore lacked a DynamoDB profile. The proposal queued a grant and replayed
it during lazy profile provisioning, avoiding a new identity-provider trigger.
It targeted meeting/document/research sharing and account/project membership.

One pending row per email, grant type, and target would make repeated invitations
idempotent. The response would return `pending: true` without fabricating a user ID.
Pickers would accept a literal email when search found no profile, and show a one-time
confirmation rather than a persisted member row. Each failed replay would be logged
and skipped so it could not block unrelated grants or login.

## Current implementation and superseded assumptions

Only meeting shares and Account member additions queue
[PendingShare](../../../backend/internal/model/meeting.go). They use
`PENDING_SHARE#{email}` with `PENDING_ACCOUNT#{id}` or `PENDING_MEETING#{id}`.
Document/research shares and Project member additions still require a resolved user.

[MeetingService](../../../backend/internal/service/meeting.go) and
[AccountService](../../../backend/internal/service/account.go) verify an existing
invited Cognito account and bind its sub; the system does not queue an arbitrary
unknown address or create a Cognito user through member addition. Self signup
remains disabled, and only the admin invite flow creates users.

Materialization requires the bound sub, current verified email, and unexpired
30-day grant. The repository transaction rechecks grant authority/target state,
creates the grant, and removes the matching pending version atomically. Expired
cleanup is version-conditional so it cannot erase a newer re-invite.

The hook runs on qualifying ListMeetings/CreateMeeting requests, not only once when
a profile is first created. This permits retries after a partial failure. Pending
queries paginate. The old no-pagination and existing-user-no-retry test requirements
are obsolete. Individual replay failures remain nonfatal and retryable.

Two owner-gated revoke endpoints exist for a known email; no pending-list API is
established. Revocation returns conflict if materialization already produced live
access. The original no-revoke YAGNI decision did not ship. The table sweeps
`pendingShareExpiresAt`, not QA's unrelated uppercase `TTL` fields.

## Tradeoffs and validation intent

The design reduces invitation ordering friction without extra onboarding
infrastructure. It retains asynchronous first-use timing, no pending-list view,
identity checks, expiry, and retry/revoke races. A pending response means a stored
grant awaiting a matching login, not proof that an email was sent.

Regression coverage belongs in the meeting/account service tests and repository
pending-share tests: wrong identity, unverified email, expiry, stale inviter, duplicate
replay, concurrent re-invite, and revoke/materialize races. ADR-034's member-add rule
still excludes granting `owner`; destructive removal/revocation remains owner-only.

Current references: [documentation map](../../README.md), [API](../../API-SPEC.md),
[ADR-034](../../decisions/ADR-034-account-member-permission-democratization.md), and
[StorageStack](../../../infra/lib/storage-stack.ts).
