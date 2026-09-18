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
consuming or revoking. Refresh races must preserve the new grant. Lists validate
reverse rows canonically. The separate user namespace protects project grants
from older account/meeting readers. Existing queues and their identity guards stay
unchanged. Grant expiry remains 30 days; temporary passwords remain seven days.
Deleted-project queue rows can persist until cleanup/TTL under ADR-025's residual
storage limit, but a missing/reassigned project cannot grant membership.

Authenticated bootstrap initializes the profile and processes project pages of at
most 25 rows under a five-second budget. Failed pages remain retryable. Identity
comes from verified JWTs, not the body. Account discovery hints require canonical
membership on reads. The companion client refreshes lists after later pages
without interrupting recordings, and adds verified-email/password-recovery UI.
Never bulk-mark legacy addresses verified or reset users silently.

## Rollout and rollback

Verify the API and companion rollback guard before explicitly enabling queued
writers and publishing the frontend. Preserve runtime config, no-cache HTML and
CloudFront invalidation. For an incompatible API rollback, the guard must pin the
reviewed code/account, pause writers by revision, wait out old requests, conditionally
cancel queued project grants and verify an empty queue under the same fence.
Existing memberships remain unchanged. Never retain grants across an old API that
can add/remove members without retiring them; cancelled grants need fresh invites.

Run full Go tests including commands, vet/ARM64 build, frontend lint/build, policy
and browser tests, and documentation checks. Mail tests use fakes; they do not prove
inbox delivery. Actual recipient or identity operations need explicit operator scope.
