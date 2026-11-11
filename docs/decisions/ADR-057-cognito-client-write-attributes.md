# ADR-057: Cognito app client write attributes and self-service email change

- Status: Accepted (owner decision 2026-10-29); implemented in `infra/lib/auth-stack.ts`,
  deployed once this ADR's PR is deployed with `TtobakAuthStack --exclusively`.
- Decision date: 2026-10-29.
- Related: cc-on-bedrock shares this user pool for sign-in (cc-on-bedrock's own
  ADR-016, a separate repository; unrelated to this repository's ADR-016).

## Context

Every app client of `TtobakUserPool` was created without `WriteAttributes`. For an
unset list, Cognito lets a signed-in user write **every** mutable attribute with
`UpdateUserAttributes`, custom attributes included. The API reference says only
standard attributes are writable; a throwaway pool on 2026-10-29 showed a user
writing `custom:department` through such a client. cc-on-bedrock now stores
authorization data in this pool's custom attributes (`custom:department` drives
budgets, `custom:subdomain` names an IAM role), so they must not be user-writable.

`email` is a required attribute of this pool, and Cognito rejects a client whose
`WriteAttributes` omit a required attribute ("Invalid write attributes specified").
That is why the `name`-only list in #523 failed to deploy. A required flag cannot
change on an existing pool, and the pool is retained and deletion-protected, so
`email` has to stay writable.

## Decision and rationale

- Every CDK app client sets `WriteAttributes = [name, email]`; no `custom:*`
  attribute is user-writable. Custom attributes are written only with Admin* APIs.
- `keepOriginal: { email: true }` (`AttributesRequireVerificationBeforeUpdate`): a
  user's email change takes effect only after the new address is verified, so an
  unverified address never becomes a sign-in identity.
- **Accepted risk: self-service email change.** A signed-in user can still change
  their own email to a new address they verify. This is narrower than what was
  live before this decision (every attribute writable, and an email change applied
  without verification), not a new capability.

## Consequences

The TTOBAK backend writes a profile's email and its email GSI key only when the
profile is first created (`GetOrCreateUser`, `backend/internal/repository/dynamodb.go`),
and nothing reconciles them afterwards. After a verified email change:

- `GetUserByEmail` still resolves the **old** address to this user, so email-keyed
  grants (Account members, meeting and research sharing, project onboarding,
  account creation) addressed to the old address reach the former holder.
- Grants addressed to the **new** address fail with user-not-found until the
  profile is updated.
- Cognito releases the old address. If an admin later invites someone else with
  it, two profiles carry the same GSI key once the new invitee signs in, and
  resolution is nondeterministic. This is the same duplicate-key hazard that
  `DetachDeletedUserProfile` handles on the deletion path.
- The PreSignUp email-domain allowlist applies to invitations
  (`PreSignUp_AdminCreateUser`), not to a later self-service change, so a user can
  move their sign-in address to a domain outside the allowlist after verifying it.

No TTOBAK UI offers an email change, and CloudTrail shows no `UpdateUserAttributes`
call in this account in the 90 days before 2026-10-29. A user would need to call the
Cognito API directly with their own access token.

## Alternatives

- **`name`-only write attributes.** Rejected by Cognito because `email` is required.
- **Backend reconciliation.** `GetOrCreateUser` would update the profile email and
  GSI key when the verified `email` claim differs from the stored profile, with the
  same duplicate handling as the deletion path. This removes the stale index and is
  the intended follow-up. It changes backend identity behavior, so it is a separate
  change.
- **Leave clients unset.** Rejected: custom authorization attributes stay
  user-writable.

## Evidence and open questions

- Throwaway pools (2026-10-29, deleted after the test):
  - A client with `WriteAttributes` unset allowed a user write to `custom:department`.
  - A `name`-only client denied it, while still allowing `GetUserAttributeVerificationCode`
    and `VerifyUserAttribute` for email, which the frontend `EmailVerificationPanel` uses.
  - With email required, a `[name]` client was rejected and a `[name, email]` client was accepted.
  - A CloudFormation update that adds custom attributes already present on the pool
    completed, so the redeploy after the failed #523 deploy is safe.
- Open: backend email reconciliation (above). Until it lands, treat a reported email
  change as an admin task: update the profile, or delete and reinvite the user.
