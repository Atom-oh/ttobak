# ADR-007: Supplemental Email Domain Allowlist

- Status: Original self-signup design superseded by the admin-created-only
  security policy; domain validation remains implemented.
- Decision date: Not recorded in the original ADR.
- Implementation checked: 2026-09-13.

## Original context and decision

The original proposal restricted otherwise open registration using a dynamic
email-domain list: Cognito Pre Sign-Up enforced it, and a frontend pre-check
provided clearer feedback. An empty list meant no domain restriction.

Identity-provider enforcement was preferred to frontend/backend-only validation,
which direct Cognito calls could bypass. This historical design does not authorize
reopening registration or adding a public onboarding endpoint.

## Current policy and implementation

**Self signup is forbidden.** `auth-stack.ts` explicitly sets
`selfSignUpEnabled: false`. New users are created through the admin-gated
`POST /api/settings/invite-user` flow calling `AdminCreateUser`. Invited users
complete the temporary-password challenge; there is no signup form.

The Pre Sign-Up Lambda remains attached. Except for the exact exception below,
it reads `PK=CONFIG`, `SK=ALLOWED_DOMAINS`, lowercases the email domain, and
rejects a nonmatching domain when the list is nonempty. Missing/empty
configuration removes only the domain restriction; it never enables self
signup. Missing email/domain and DynamoDB read errors fail the request.

The **2026-09-13 amendment** permits only `demo@atomai.click` through
`PreSignUp_AdminCreateUser` to bypass the domain lookup. The address comparison
is case-insensitive, without whitespace trimming. Other addresses, aliases,
self signup and federation receive no exception. It grants no group membership
and stores no password. Removing it requires a reviewed code change and does
not disable an existing user.

A stale allowlist can still block other admin invites. Domain validation is
supplemental; the admin gate decides who may create users. Whether Cognito
invokes the trigger for `AdminCreateUser(MessageAction=RESEND)` remains
unverified; do not rely on it for resend enforcement.

## Configuration boundaries and unresolved gap

`GET /api/auth/allowed-domains` sits outside Go's auth group, but the gateway's
JWT-protected `/api/{proxy+}` route still covers it. A handler comment calling it
"public" does not create a publicly accessible API route. The only approved
unauthenticated application route remains `GET /api/public/docs/{token}`
(ADR-022), with its own token validation.

`PUT /api/settings/allowed-domains` normalizes and stores the list. Current code
places this write in the authenticated group, **outside** `RequireAdmin`.
The old ADR's claim of admin-only configuration is therefore not implemented.
This is a documented authorization gap, not a newly accepted exception or a
change to the security policy.

## Consequences and risks

Runtime configuration avoids redeploying for domain changes. It adds an
identity-provider dependency on DynamoDB and requires careful list management.
Allowlist changes do not revoke existing users. Domain membership alone is not
an authorization grant, and the frontend is never the enforcement boundary.

## Evidence

- [auth-stack.ts](../../infra/lib/auth-stack.ts),
  [pre-signup/index.mjs](../../infra/lambda/pre-signup/index.mjs),
  [policy.mjs](../../infra/lambda/pre-signup/policy.mjs),
  [policy tests](../../infra/test/pre-signup.test.ts).
- [main.go](../../backend/cmd/api/main.go): route groups and admin gate.
- [settings.go](../../backend/internal/handler/settings.go),
  [meeting.go](../../backend/internal/service/meeting.go): configuration and invite.
- [gateway-stack.ts](../../infra/lib/gateway-stack.ts): JWT catch-all and the
  single public-document exception.
