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
  domain restriction; a storage error or missing e-mail fails the request.
- The exact address `demo@atomai.click` is exempt only on
  `PreSignUp_AdminCreateUser`, with no group grant and no domain-wide exception.
  The trigger's `index.mjs` and `policy.mjs` ship together.

### Identity attributes

- Every app client may write only `name` and `email`. `custom:*` attributes are
  written through admin APIs only.
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
- Cognito admin IAM permissions are scoped to the user pool.
- Login tracking in the post-authentication trigger must fail open: short abort
  timeout, errors swallowed, no reserved concurrency, and a `DISABLED=1` switch.
  It writes a `LOGIN` row and never the profile.

## Not implemented or off by default

- The profile's e-mail and its index key are written only when the profile is
  created. Nothing reconciles them after a verified e-mail change.
- Refresh-only use does not update the last-login time.

## Limits and accepted risks

- Sign-out, disable and delete revoke refresh tokens only. Access and ID tokens
  already issued stay valid until they expire, because they are verified
  locally.
- `PUT /api/settings/allowed-domains` requires sign-in but not the admin group.
  No frontend calls it. This gap is known and not accepted as policy.
- A self-service e-mail change does not pass the pre-sign-up domain check, and
  after a change the old address still resolves to the user for e-mail-keyed
  grants.
- Whether the pre-sign-up trigger runs when an invitation is resent is
  unverified; do not rely on it.
- The last-administrator check and the write are not atomic.

## Source

`infra/lib/auth-stack.ts`, `infra/lambda/pre-signup/`,
`infra/lambda/post-authentication/`, `backend/internal/middleware/auth.go`,
`backend/internal/service/user_admin.go`, `backend/internal/handler/settings.go`,
`backend/internal/repository/dynamodb.go` (`DetachDeletedUserProfile`,
`GetOrCreateUser`), `infra/lib/ai-stack.ts`.
