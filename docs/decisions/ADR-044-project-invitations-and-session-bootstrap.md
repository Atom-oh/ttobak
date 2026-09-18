# ADR-044: Project invitations and authenticated session bootstrap

- Status: Accepted implementation; deployment requires the rollout checks below.
- Date: 2026-09-18.
- Extends ADR-025 project membership and ADR-032 user administration. Existing
  account/meeting pending-grant identity and revocation constraints are retained.

## Decision

Project AddMember resolves the current Cognito identity instead of treating a
missing app profile as an unknown user. New account creation and email sending
remain separate, explicit administrator actions. A project owner cannot create
users or assign global/account roles merely by adding a project member.

Use a canonical `PROJECT_INVITES#{email}` / `PENDING_PROJECT#{projectId}` row and a
`PROJECT#{projectId}` / `PENDING_MEMBER#{email}` reverse row. Queue writes atomically
check current project ownership and absence of an existing member. Consumption
checks current project ownership, exact recipient sub, invitation version and
expiry; it writes membership and deletes both invitation rows in one transaction.
A concurrent refresh/revoke must never be overwritten using an older snapshot.
Existing membership or lost inviter authority retires only the observed version.
Owner-visible listing is paginated and validates reverse rows against canonical
rows. Cancellation detects materialization/version races and returns conflict.

The separate user namespace is intentional: in-flight older APIs must not delete
an unknown project invitation kind during rollout. Existing account/meeting queues
retain their names. All invitation rows use the existing configured TTL field;
30-day grant expiry is independent of the seven-day temporary-password validity.
Project deletion may leave inaccessible queue rows until cleanup/TTL, consistent
with ADR-025's accepted deleted-project residual storage race; consumption always
requires the project to exist with the same authorized inviter.

An authenticated bootstrap creates the caller's profile and retries pending
memberships before frontend page requests. Identity comes from verified JWT claims,
never the request body. Bootstrap returns bounded fresh account discovery hints;
meeting listing treats them as hints only and revalidates canonical membership,
preserving immediate team-meeting discovery through reverse-index propagation.

Unverified users can explicitly request and confirm an email verification code
through authenticated Cognito SDK calls. Refresh claims before consuming pending
grants. Never automatically mark legacy addresses verified or bulk-reset them.
Admin UI exposes verification, disabled and reset-required states, and the API
rejects mail/reset actions whose prerequisites are absent. Password guidance and
validation match the configured policy, and reset-code entry is available without
requesting an unnecessary second code.

## Rollout and verification

Run all Go tests including command packages, vet, ARM64 API build, frontend lint and
production build, password-policy tests, and documentation checks. Deploy API first
and verify its code revision/new authenticated routes before frontend publication.
Frontend deployment must preserve runtime config, set HTML no-cache and invalidate
CloudFront as in the deployment runbook. Roll back frontend before API.

Tests cover unknown/invited/recreated identities, current owner checks, revoked or
refreshed grants, deleted projects, existing members, canonical pagination, missing
email verification, disabled accounts, trusted bootstrap identity and forged
account discovery hints. Mail tests use fakes; a passing unit test does not prove
inbox delivery. Live recipient mail or identity changes require explicit operator
scope and are not a side effect of deploying this implementation.
