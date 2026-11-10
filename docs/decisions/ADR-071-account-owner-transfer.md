# ADR-071: Transfer Account ownership to an existing member

- Status: Accepted for implementation; deployed acceptance is recorded separately.
- Decision date: 2026-11-10.
- Amends [ADR-034](ADR-034-account-member-permission-democratization.md) and
  [ADR-055](ADR-055-designated-account-owner.md): they forbade any post-creation
  Owner change; this ADR adds exactly one path.

## Decision

The current Owner may transfer ownership with
`POST /api/accounts/{accountId}/owner-transfer` and body `{"targetUserId"}`.
This is the only path that grants Owner after creation. Generic AddMember,
UpdateMemberRole and pending-share materialization stay owner-free.

- **Dual owner condition:** the requester must hold the Owner MEMBER role and be the
  META `ownerUserId`. The transaction conditions both, so a split state or a
  concurrent transfer fails closed.
- **Target identity:** the target must be an existing direct non-owner member
  (not self). Cognito `AdminGetUser` must report the same `sub`, Enabled and
  CONFIRMED; the repository cannot distinguish pending or disabled invitees.
  Lookup failure or an unwired client is 503, never a fallback.
- **Previous Owner:** becomes SA, retaining access without Owner-only powers.
- **Initial-owner designation:** the same transaction clears `initialOwnerEmail`
  and `initialOwnerInvitationPending`, so ADR-055 resend cannot re-target a
  transferred Account.
- **One transaction:** META, target MEMBER and previous-owner MEMBER change
  together; any condition failure writes nothing and returns 409.
- **Non-delegable:** the connector inventory disables delegation; the route is not
  exposed to MCP or other delegated callers.

## Consequences

Removal, revocation and reparenting remain Owner-only and now follow the new
Owner. Access tokens issued before transfer remain valid until expiry (ADR-032),
but authorization reads current membership, so the previous Owner loses Owner powers
immediately. There is no transfer to a non-member and no multi-owner state.

## Evidence

- [Service](../../backend/internal/service/account_owner_transfer.go) and
  [transaction](../../backend/internal/repository/account_owner_transfer.go), with
  their tests.
- Route contract: [API-SPEC](../API-SPEC.md) Accounts section.
