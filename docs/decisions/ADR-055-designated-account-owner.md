# ADR-055: Prepare an Account for another initial Owner

- Status: Accepted for implementation; deployed acceptance is recorded separately.
- Decision date: 2026-10-26.
- Supersedes ADR-015's creator-only initial ownership. Extends ADR-034/054 only
  for initial creation and canonical Owner invitation recovery.

## Decision

An authenticated user can prepare a customer Account for a colleague by selecting
that colleague's email as the initial Owner. If no login identity exists, request
the domain-limited Cognito invitation described by ADR-054. Bind ownership to the
current Cognito sub; an existing PROFILE email index must agree with that sub.
Public self-signup and administrator-group grants remain disabled.

The backend supports this decision. The corresponding creation and resend UI is
pending in companion PR #479. Its approved requirements are:

The creation UI must offer an unchecked "Keep me as a regular member (AM)" checkbox
when another Owner is selected. Checked creates a creator AM membership;
unchecked creates none. An opted-out creator may receive separate explicit
meeting/document shares, but creation grants no content reads or Account access.
Hide and clear optional additional members when the creator opts out. Stay on
the creation receipt instead of navigating to an inaccessible Account.

Create META, the canonical Owner member and optional creator AM member in one
conditional transaction, with existing membership privacy stamps. Initial
ownership is not an expiring owner-valued PendingShare. Existing pending grants
keep their non-owner allowlist, identity binding, verified-email and expiry guards.
Generic AddMember and UpdateMemberRole still cannot assign or demote an Owner.
Removal, revocation and reparenting stay Owner-only.

## Retry and delivery boundaries

- Creation accepts a single JSON object capped at 16 KiB. Owner email and input
  collections have additional service limits; arbitrary Owner IDs are rejected.
- Resolve/authorize the creator's selected parent before Cognito side effects.
  The creation transaction rechecks that creator membership and observed ancestry.
  The designated Owner neither needs nor inherits parent access.
- The pending frontend must retain a creation UUID for the same normalized input across
  failed requests. Changed input gets a new UUID. The server records the exact
  creator and normalized request hash, and rereads after ambiguous transactions.
- Receipt replay returns original request fields and current creator membership.
  It never restores a removed creator, reveals later Account data, creates a
  second Account or resends mail. Legacy callers may omit the creation UUID.
- Members, or the exact recorded creator for thirty days, may explicitly resend
  the canonical initial Owner invitation. The authenticated endpoint accepts no
  caller-selected identity, email or role; creator recovery is a delivery
  capability rather than a read grant.
- Resend rechecks current Cognito email/sub and FORCE_CHANGE_PASSWORD status.
  Confirmed users are never reset and recreated same-email identities cannot
  acquire ownership. The initial invitation hint in detail/receipts is not proof
  of current status; actual resend performs the authoritative check.
- New delivery and resend share ADR-054's current domain allowlist and fail-closed
  twenty-attempts-per-caller-per-UTC-hour counter. A mail request followed by
  unconfirmed persistence is reported as partial success; mailbox delivery is
  never inferred.
- Account creation requires `read`, `share` and `write` capabilities in the
  connector inventory, matching its invitation and third-party ownership effects.
  A delegated principal approved only for `read` and `write` cannot create an
  Account. The new resend operation is inventoried with delegation disabled. No new
  unauthenticated route, origin, IAM permission or project rollout is introduced.

## Evidence and consequences

Service tests cover creator checkbox states, stale identities, parent loss,
quota/dependency rejection, ambiguous committed transactions, safe replay and
time-limited creator recovery. Wire tests inspect the atomic DynamoDB transaction
and both privacy stamps. Executable component tests for hidden selections,
retry UUIDs, navigation and stale creation/resend controls are pending with
the frontend in companion PR #479 and are not evidence at this revision.

The creator can intentionally create an Account they cannot later administer.
The colleague's first login must use the provisioned identity. These tests and
code review do not prove real-recipient mail delivery or first-login acceptance;
production artifact and deployment receipts are separate evidence.
