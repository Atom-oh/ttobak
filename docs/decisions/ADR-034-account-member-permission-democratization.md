# ADR-034: Account members may add members and update roles

- Status: Accepted; supersedes owner-only authorization for these two operations. ADR-023's removal safeguards remain in force.
- Decision date: 2026-08-25.
- Code checked: 2026-09-13. [ADR-036](ADR-036-account-hierarchy-and-meeting-filters.md) adds separate owner-only reparenting, without inherited membership.

## Original decision and rationale

Owner-only additions and role changes blocked normal teamwork when the owner was absent. Open these reversible operations to existing account members. Keep removal and cancellation of pending invitations owner-only because they revoke access and may trigger meeting-share cleanup. A manager-role tier was rejected as unnecessary permission-model expansion.

## Current behavior and invariants

- `AddMember` requires an existing member of the target account, regardless of role. `UpdateMemberRole` uses `requireMember` for the same rule. Non-members remain forbidden; these endpoints are not open registration.
- Both operations require `isAssignableRole` / `model.AssignableRoles`, which excludes `owner`. Role changes also reject an existing owner as the target. Members cannot promote themselves/others to owner or demote the owner through these APIs.
- `RemoveMember` and `RevokePendingMember` remain owner-only. The member-management UI exposes role controls to members and destructive controls to the owner; backend checks remain authoritative.
- Account/meeting PendingShare handling remains a separate identity-bound workflow: an existing Cognito invite without a profile may be queued. Materialization requires the invited Cognito sub, verified email, and an unexpired grant; a different user cannot claim it merely by adopting the email. TTL and synchronous expiry use the 30-day policy.

## Tradeoffs and limits

Any existing member can extend the account team and change non-owner role labels. This is the intended authorization decision, including the resulting access to account-published content and Projects linked under ADR-025. It does not grant membership in a parent/child account, permission to reparent, or permission to remove members.

This ADR does not authorize Cognito self-sign-up, automatic account creation for arbitrary emails, owner promotion, or relaxing PendingShare identity checks. The old non-owner-denial tests were replaced by member-success and non-member-denial cases.

## Evidence

- [Member authorization](../../backend/internal/service/account.go): `AddMember`, `UpdateMemberRole`, `RemoveMember`, `RevokePendingMember`; [tests](../../backend/internal/service/account_test.go).
- [Role allowlist](../../backend/internal/model/account.go), [handler tests](../../backend/internal/handler/account_test.go), [member UI](../../frontend/src/components/AccountDetailClient.tsx).
- [Pending identity/expiry checks](../../backend/internal/service/meeting.go): `MaterializePendingShares`, `materializeOne`; [materialization transactions](../../backend/internal/repository/dynamodb.go).
