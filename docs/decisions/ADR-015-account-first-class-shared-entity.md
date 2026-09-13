# ADR-015: Account as a Shared First-Class Entity

- Status: Accepted; permissions extended by [ADR-034](ADR-034-account-member-permission-democratization.md)
  and hierarchy by [ADR-036](ADR-036-account-hierarchy-and-meeting-filters.md).
- Decision date: Not recorded in the original ADR.
- Implementation checked: 2026-09-13.

## Context and decision

Personal meeting partitions and per-meeting shares did not provide a durable team
home for customer knowledge. Add Account to the existing DynamoDB single table,
with memberships, roles, aliases, and reverse lookup. This avoids a new table or
GSI while supporting the sharing and knowledge workflows of ADR-016 through ADR-018.

## Current model and authorization

- Account metadata: `PK=ACCOUNT#{accountId}`, `SK=META`. Membership:
  `SK=MEMBER#{userId}`, with `GSI1PK=USER#{userId}` and
  `GSI1SK=ACCOUNT#{accountId}`. Account lists must retain the `ACCOUNT#` prefix
  condition so they do not mix with meeting date-index rows.
- Creation writes metadata and the creator's owner membership transactionally.
  Detail reads check membership consistently, deliberately distinguishing missing
  accounts (`ErrNotFound`) from existing inaccessible accounts (`ErrForbidden`).
  Listing/alias lookup uses paginated membership queries and per-account reads;
  alias ambiguity is rejected rather than guessed.
- `model.AssignableRoles` contains `AM`, `TAM`, `SSA`, `SA`, `SA Manager`, and
  `AM Manager`. `owner` is creator-assigned and cannot be granted by member APIs.
- **ADR-034 supersedes owner-only addition/role changes:** any current member may
  add members or change a non-owner's assignable role. Removing a member or
  revoking a pending invite remains owner-only. These labels do not introduce
  separate per-document authorization tiers.
- Pending grants for already invited users bind the Cognito sub and require verified
  email on materialization. They expire after 30 days; account membership is not
  equivalent to permission to create a Cognito user.
- **ADR-036 extends the flat model:** optional `parentAccountId` organizes accounts.
  Parent membership is required for creation under a parent; reparenting is
  owner-gated with conditional ancestor checks. Hierarchy does not inherit
  membership or content access. Visible children can appear as roots when their
  parents are inaccessible. Account detail tabs remain account-scoped.

## Alternatives, consequences, and accepted risks

A separate table added infrastructure/access plumbing; per-meeting shares alone
could not accumulate account knowledge. Single-table membership enables durable
team access but retains N+1 account reads and coarse roles.

The original ADR accepted existing member email storage under the table's default
AWS-owned encryption, without a customer-managed key. Current IaC still does not
configure a customer-managed table key, and member rows do not carry the table's
`pendingShareExpiresAt` TTL field. Preserve that existing limitation explicitly:
it is not proof that all sensitive rows comply, nor a waiver of the current KMS
and retention/TTL policy for new or changed PII storage.

## Evidence

- [account.go](../../backend/internal/model/account.go): keys and role allowlist.
- [account.go](../../backend/internal/service/account.go),
  [account_test.go](../../backend/internal/service/account_test.go): gates and tests.
- [account.go](../../backend/internal/repository/account.go): transactions,
  consistent reads, and paginated membership queries.
- [account_hierarchy.go](../../backend/internal/service/account_hierarchy.go),
  [account_hierarchy_test.go](../../backend/internal/service/account_hierarchy_test.go).
- [storage-stack.ts](../../infra/lib/storage-stack.ts): encryption/TTL configuration.
