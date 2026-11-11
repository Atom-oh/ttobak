# ADR-044: Project invitations and authenticated session bootstrap

- Status: Accepted implementation; activation requires the rollout below.
- Date: 2026-09-18.
- Extends ADR-025 project membership and ADR-032 user administration.

## Decision

Resolve current Cognito identity before adding a project member. Identity creation
and mail remain explicit admin actions; project ownership grants neither. Queueing
requires a pending-capable client and `PROJECT_INVITATIONS_ENABLED=true`; absence
or other values disable it. Legacy direct adds require no conflicting pending grant.

Canonical grants use `PROJECT_INVITES#{email}` / `PENDING_PROJECT#{projectId}` with
`PROJECT#{projectId}` / `PENDING_MEMBER#{email}` reverse rows. Transactions recheck
owner, recipient sub, expiry/version and existing membership when queueing,
consuming or revoking. Refresh races must preserve the new grant and remain retryable. A project/sub
latest-intent marker prevents an old email invitation from restoring access after
member removal; removal and legacy direct add retire that marker atomically. Lists validate
reverse rows canonically. The separate user namespace protects project grants
from older account/meeting readers. Existing queues and their identity guards stay
unchanged. Grant expiry remains 30 days; temporary passwords remain seven days.
Deleted-project queue rows can persist until cleanup/TTL under ADR-025's residual
storage limit, but a missing/reassigned project cannot grant membership.

The companion authenticated bootstrap initializes the profile and processes project pages of at
most 25 rows under a five-second budget. Failed pages remain retryable. Identity
comes from verified JWTs, not the body. Account discovery hints require canonical
membership on reads. The companion client refreshes lists after later pages
without interrupting recordings, and adds verified-email/password-recovery UI.
Never bulk-mark legacy addresses verified or reset users silently.

## Rollout and rollback

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
