# ADR-032: Admin user management and login tracking

- Status: Accepted. The original immediate-session-expiry claim is corrected below.
- Decision date: 2026-08-18.
- Code checked: 2026-09-13; Cognito runtime behavior and deployed trigger configuration were not exercised.

## Original decision and rationale

Extend an invite-only service with user listing, enable/disable/delete, invite resend, password reset, and login history. Use a separate login item to avoid a PostAuthentication write creating an incomplete `PROFILE` before `GetOrCreateUser` can populate email/search indexes. Preserve meeting/document data when deleting a Cognito account.

## Current behavior and security invariants

- Six user-management routes use backend `RequireAdmin` with verified `cognito:groups`; frontend admin display is cosmetic. Cognito self-sign-up remains disabled. Admin-created invitations are the permitted account-creation path.
- PostAuthentication writes `USER#{sub}/LOGIN`, never `PROFILE`. It catches write errors and returns the event, with a 1.5-second abort, short SDK timeouts, two attempts, and a `DISABLED=1` bypass. CDK sets a five-second Lambda timeout and no reserved concurrency. This is **best-effort fail-open code**, not protection against invocation throttling, initialization failure, or process timeout before it returns.
- `lastLoginAt` measures authentication, not ongoing activity; refresh-token use does not update it. Dormant means an existing timestamp older than 90 days. Missing timestamps distinguish invitation pending from no record, not inactivity. A failed timestamp join is surfaced as unavailable.
- Resend rereads the target, requires `FORCE_CHANGE_PASSWORD`, and uses the email returned by Cognito for `AdminCreateUser(RESEND)`. Forced reset requires `CONFIRMED`; the login UI includes password-code recovery. Whether the pre-sign-up trigger runs for RESEND remains unverified here.
- Delete/disable reject self-targeting and removal of the last **enabled** admin, then recheck enabled-admin availability after the mutation. The check/write gap remains; the second check reports a warning rather than providing a transaction lock.
- Global sign-out runs before deletion and accompanies disabling. It revokes refresh capability; **already-issued JWTs accepted by local signature/expiry checks remain usable until expiry**. The original claim of immediate API-session termination was incorrect. Side-effect failures are logged and returned as warnings after a successful primary operation.
- Deletion retains application data and the old profile but detaches `GSI2PK`/`GSI2SK` so a later invitation with the same email does not resolve to the dead user. Detachment failure is a warning requiring remediation.

## Tradeoffs and accepted residual risks

Login tracking can miss active refresh-only users. Local JWT validation leaves a token-expiry tail after disable/delete. Concurrent admin changes can leave no enabled admin; out-of-band Cognito recovery is still required in that case. Retained profiles/data need a separate retention/cleanup decision.

Admin-management IAM actions are scoped to the pool. The older `CognitoListUsers` policy still has an unconditional wildcard in current source; this is a specific unresolved legacy policy conflict, not an exemption for new wildcards. API activity tracking and destructive data cascade were rejected as different semantics and greater deletion risk.

## Evidence

- [PostAuthentication](../../infra/lambda/post-authentication/index.mjs), [auth configuration](../../infra/lib/auth-stack.ts), [self-sign-up tests](../../infra/test/auth-stack.test.ts).
- [Admin service](../../backend/internal/service/user_admin.go), [service tests](../../backend/internal/service/user_admin_test.go), [handlers](../../backend/internal/handler/user_admin.go), [handler tests](../../backend/internal/handler/user_admin_test.go).
- [Route registration](../../backend/cmd/api/main.go), [IAM](../../infra/lib/ai-stack.ts), [password recovery UI](../../frontend/src/components/auth/ForgotPasswordForm.tsx).
