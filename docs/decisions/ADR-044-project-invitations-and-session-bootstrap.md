# ADR-044: Project invitations and authenticated session bootstrap

- Status: Accepted implementation; deployment requires the rollout checks below.
- Date: 2026-09-18.
- Extends ADR-025 project membership and ADR-032 user administration. Existing
  account/meeting pending-grant identity and revocation constraints are retained.

## Decision

Project AddMember resolves the current Cognito identity instead of treating a
missing app profile as an unknown user. Pending production requires an explicit pending-capable client request, and
`PROJECT_INVITATIONS_ENABLED=false` fences new queued writes. Legacy clients may
add a registered user only with an atomic pending-absence check. New account creation and email sending
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

Each project bootstrap page is limited to 25 canonical rows and five seconds,
with a caller-scoped continuation cursor and retry state. Bootstrap initializes
the caller profile; companion clients refresh lists after subsequent grant pages. Identity comes from verified JWT claims,
never the request body. Bootstrap returns bounded fresh account discovery hints;
meeting listing treats them as hints only and revalidates canonical membership,
preserving immediate team-meeting discovery through reverse-index propagation.

The companion recovery/client release adds authenticated email verification,
reset-code entry and state-specific admin controls. It must refresh verified JWT
claims before retrying grants; it must never bulk-mark legacy emails verified.

## Rollout and verification

Run all Go tests including command packages, vet, ARM64 API build, frontend lint and
production build, password-policy tests, and documentation checks. Deploy API first
and verify its code revision/new authenticated routes before frontend publication.
Frontend deployment must preserve runtime config, set HTML no-cache and invalidate
CloudFront. Incompatible API rollback requires the companion operator guard:
pin the reviewed serving alias/code/account, pause the persistent database
fence by revision, conditionally cancel canonical/reverse project invitations
and verify an empty queue while that transactional fence still holds. Existing
memberships remain unchanged. Never retain pending grants across an older API that
can add/remove members without retiring them; cancelled grants need fresh invites.

Tests cover unknown/invited/recreated identities, current owner checks, revoked or
refreshed grants, deleted projects, existing members, canonical pagination, missing
email verification, disabled accounts, trusted bootstrap identity and forged
account discovery hints. Mail tests use fakes; a passing unit test does not prove
inbox delivery. Live recipient mail or identity changes require explicit operator
scope and are not a side effect of deploying this implementation.

The rollback fence is `CONTROL#PROJECT_INVITATIONS` / `STATE`, storing `enabled`
and a revision. Queue transactions pin their observed revision; claims check the
same control row. Pausing therefore also rejects delayed pre-pause transactions,
without estimating old Lambda execution budgets. Neither code/alias deployments
nor environment replacement resets the row. Resume is an explicit revision-guarded
operator action requiring an empty queue and a reviewed compatible serving version.
The infrastructure activation parameter defaults closed and salts the version
description so `live` receives the intended environment on parameter changes.
