# ADR-054: Account members can invite new users directly

- Status: Accepted for implementation; deployed acceptance is recorded separately.
- Decision date: 2026-10-26.
- Supersedes ADR-034's restriction against automatic Cognito creation for account
  invitations and the owner-only account materialization condition.
- [ADR-055](ADR-055-designated-account-owner.md) extends initial Account creation
  with a designated canonical Owner and optional creator membership; existing
  non-owner pending grants retain this ADR's rules.

## Decision

An authenticated member of an Account can add an email without asking an
administrator to create the user first. The existing authenticated add-member
operation creates a missing Cognito identity through `AdminCreateUser`, requests
email delivery, and queues an identity-bound Account grant. Existing registered
users continue to be added directly. Existing Cognito identities are reused;
ordinary retries do not resend invitations or reset passwords. Account members
may explicitly request a resend for an identity still awaiting its first
password change; confirmed users are never reset.

This decision implements the user's explicit request to let ordinary users invite
other people from Account creation and Account member management. It does not
enable Cognito self-sign-up or the separately gated project-invitation rollout.

## Constraints

- Authorize current membership and validate a bounded, normalized email and an
  assignable non-owner role before Cognito side effects.
- Keep the existing pre-sign-up domain allowlist and administrator endpoints.
  Member invitations never call group-management APIs or grant administrator status.
- Limit new invitation and explicit resend attempts to 20 per caller per UTC hour using the existing
  DynamoDB window counter. Fail closed if the counter is unavailable.
- Require the requester's verified email. Enforce the current domain allowlist
  before creation or resend; do not assume RESEND invokes the pre-sign-up trigger.
- Reuse Cognito's email-delivered temporary-password workflow. Preserve the
  current verified-email claim handling; materialization still requires a
  verified email and the exact invited Cognito sub.
- Revalidate the inviter's Account membership transactionally when queuing and
  claiming the grant. Any current member has addition authority under ADR-034;
  removal and pending cancellation remain owner-only.
- Preserve the 30-day expiry, version-conditioned deletion, and atomic membership
  creation plus pending-row deletion. A removed inviter cannot confer access.
- Report an invitation sent but grant queue failure as partial success so the
  caller can retry without sending another email.
- Publish compatible API behavior before publishing clients that use it. Keep
  runtime configuration, origin proof, privacy rollout and unrelated flags intact.

## User-visible result

Account creation and member addition display whether the member was added,
whether an invitation email was requested and a grant queued, or whether a
specific operation failed. Ordinary users are no longer directed to ask an
administrator to invite a missing user.

Code, tests, current-HEAD review and a deployment receipt are required evidence.
They do not establish actual delivery to a recipient's mailbox.
