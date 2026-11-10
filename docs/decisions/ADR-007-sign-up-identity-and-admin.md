# ADR-007: Sign-up, identity attributes and user administration

- Status: Accepted. Consolidates the earlier records on the e-mail domain
  allowlist, the admin user management panel and Cognito app-client write
  attributes.
- Implementation checked: 2026-11-11.

## Decisions

### Sign-up

- Cognito self sign-up stays disabled. Accounts are created only by an
  administrator's invitation (`AdminCreateUser`); that is the entry gate.
- The pre-sign-up domain allowlist is supplemental. An empty list means no
  domain restriction; a storage error or missing e-mail fails the request. It
  is stored as the `CONFIG` / `ALLOWED_DOMAINS` row, and domain membership is
  never an authorization grant.
- The address `demo@atomai.click` (case-insensitive exact match) is exempt only on
  `PreSignUp_AdminCreateUser`, with no group grant and no domain-wide exception.
  The trigger's `index.mjs` and `policy.mjs` ship together.

### Identity attributes

- Every app client may write only `name` and `email`. `custom:*` attributes are
  authorization data shared with cc-on-bedrock: the list is append-only and
  only admin APIs write them.
- Users may change their own e-mail; Cognito keeps the original until the new
  one is verified.

### Administration

- Admin routes sit behind `RequireAdmin`, which reads `cognito:groups` from the
  verified JWT. The frontend's `isAdmin` is cosmetic.
- Delete and disable reject the caller's own account and the last enabled
  administrator, and warn after the write if a race left none.
- Resending an invitation uses the e-mail stored in Cognito and requires
  `FORCE_CHANGE_PASSWORD`; a forced password reset requires a confirmed user
  with a verified e-mail.
- Deleting a user keeps the profile and data, sets `deletedAt`, and removes the
  e-mail index keys so the address no longer resolves to that user.
- The Cognito user-management permissions are scoped to the user pool.
- Login tracking in the post-authentication trigger must fail open: short abort
  timeout, errors swallowed, no reserved concurrency, and a `DISABLED=1` switch.
  It writes a `LOGIN` row and never the profile.

## Not implemented or off by default

- The profile's e-mail and its index key are written only when the profile is
  created. Nothing reconciles them after a verified e-mail change.
- Refresh-only use does not update the last-login time.
- Profiles and data kept after deletion have no retention or cleanup rule yet;
  that decision is open.
- Deploy the auth stack with `--exclusively`. The user pool is retained and
  deletion-protected, and a required attribute cannot change on an existing
  pool.

## Limits and accepted risks

- Sign-out, disable and delete revoke refresh tokens only. Access and ID tokens
  already issued stay valid until they expire, because they are verified
  locally. Access and ID tokens last one hour and refresh tokens 30 days.
- `PUT /api/settings/allowed-domains` requires sign-in but not the admin group.
  No frontend calls it. This gap is known and not accepted as policy.
- A self-service e-mail change does not pass the pre-sign-up domain check.
  After a change the old address still resolves to the user and the new one
  fails e-mail-keyed grants until the profile is updated. If an administrator
  later invites someone with the old address, two profiles can share one e-mail
  index key and resolution becomes nondeterministic.
- Changing the allowlist does not revoke existing users, and a stale allowlist
  can block other admin invitations because the check also runs for them.
  Removing the demo exemption needs a reviewed code change and does not disable
  the existing user.
- If removing a deleted profile's e-mail index keys fails, the delete still
  succeeds with a warning; re-inviting that address may resolve to the wrong
  user until it is fixed.
- The API role's `cognito-idp:ListUsers` permission still uses `Resource: "*"`.
  It is legacy, not an exemption for new wildcards.
- Fail-open login tracking does not cover throttling, an initialization
  failure or a timeout before the handler returns.
- Whether the pre-sign-up trigger runs when an invitation is resent is
  unverified; do not rely on it.
- The last-administrator check and the write are not atomic. Concurrent
  removals can leave no enabled administrator; recovery is an operator adding a
  user to the admins group out of band.

## Source

`infra/lib/auth-stack.ts`, `infra/lambda/pre-signup/`,
`infra/lambda/post-authentication/`, `backend/internal/middleware/auth.go`,
`backend/internal/service/user_admin.go`, `backend/internal/handler/settings.go`,
`backend/internal/repository/dynamodb.go` (`DetachDeletedUserProfile`,
`GetOrCreateUser`), `infra/lib/ai-stack.ts`.
