# ADR-015: Accounts, roles, hierarchy and ownership

- Status: Accepted. Merges earlier records; each section below keeps one record's
  rules, and dates inside mark owner decisions.
- Implementation checked: 2026-11-11.

An account is a shared entity with members and roles. The sections below keep, in order, the base account record, member permissions, hierarchy and meeting filters, and self-service invitations. Where a later section changed an earlier rule, the earlier section has been corrected to say so. The designated initial Owner (ADR-055), descendant read sharing (ADR-058) and owner transfer (ADR-071) are still separate records.

## Account as a Shared First-Class Entity

### Status notes

- Status: Accepted; permissions extended by the member permissions section
  and hierarchy by the hierarchy section.
- Decision date: Not recorded in the original ADR.
- Implementation checked: 2026-09-13.

### Context and decision

Personal meeting partitions and per-meeting shares did not provide a durable team
home for customer knowledge. Add Account to the existing DynamoDB single table,
with memberships, roles, aliases, and reverse lookup. This avoids a new table or
GSI while supporting the sharing and knowledge workflows of ADR-016 through ADR-003.

### Current model and authorization

- Account metadata: `PK=ACCOUNT#{accountId}`, `SK=META`. Membership:
  `SK=MEMBER#{userId}`, with `GSI1PK=USER#{userId}` and
  `GSI1SK=ACCOUNT#{accountId}`. Account lists must retain the `ACCOUNT#` prefix
  condition so they do not mix with meeting date-index rows.
- **ADR-055 supersedes creator-only initial ownership:** creation atomically
  writes metadata, the selected initial Owner and optional creator AM membership.
  Detail reads check membership consistently, deliberately distinguishing missing
  accounts (`ErrNotFound`) from existing inaccessible accounts (`ErrForbidden`).
  Listing/alias lookup uses paginated membership queries and per-account reads;
  alias ambiguity is rejected rather than guessed.
- `model.AssignableRoles` contains `AM`, `TAM`, `SSA`, `SA`, `SA Manager`, and
  `AM Manager`. `owner` is assigned at creation and changes only through the
  owner-transfer endpoint (ADR-071); member APIs cannot grant or demote the
  Owner.
- **The member permissions section supersedes owner-only addition/role changes:** any current member may
  add members or change a non-owner's assignable role. Removing a member or
  revoking a pending invite remains owner-only. These labels do not introduce
  separate per-document authorization tiers.
- Pending grants for already invited users bind the Cognito sub and require verified
  email on materialization. They expire after 30 days. Any member can add
  an e-mail address; when no user exists, adding the member creates the
  Cognito identity and queues the invitation (see the invitations section).
- **The hierarchy section extends the flat model:** optional `parentAccountId` organizes accounts.
  Parent membership is required for creation under a parent; reparenting is
  owner-gated with conditional ancestor checks. Hierarchy never creates member rows. With
  `ACCOUNT_DESCENDANT_READS_ENABLED=1` (off by default) members of an ancestor
  may read a descendant's content as ADR-058 describes. Visible children can appear as roots when their
  parents are inaccessible. Account detail tabs remain account-scoped.

### Alternatives, consequences, and accepted risks

A separate table added infrastructure/access plumbing; per-meeting shares alone
could not accumulate account knowledge. Single-table membership enables durable
team access but retains N+1 account reads and coarse roles.

The original ADR accepted existing member email storage under the table's default
AWS-owned encryption, without a customer-managed key. Current IaC still does not
configure a customer-managed table key, and member rows do not carry the table's
`pendingShareExpiresAt` TTL field. Preserve that existing limitation explicitly:
it is not proof that all sensitive rows comply, nor a waiver of the current KMS
and retention/TTL policy for new or changed PII storage.

### Evidence

- [account.go](../../backend/internal/model/account.go): keys and role allowlist.
- [account.go](../../backend/internal/service/account.go),
  [account_test.go](../../backend/internal/service/account_test.go): gates and tests.
- [account.go](../../backend/internal/repository/account.go): transactions,
  consistent reads, and paginated membership queries.
- [account_hierarchy.go](../../backend/internal/service/account_hierarchy.go),
  [account_hierarchy_test.go](../../backend/internal/service/account_hierarchy_test.go).
- [storage-stack.ts](../../infra/lib/storage-stack.ts): encryption/TTL configuration.

## Account members may add members and update roles

### Status notes

- Status: Accepted; supersedes owner-only authorization for these two operations. ADR-016's removal safeguards remain in force.
- Decision date: 2026-08-25.
- Code checked: 2026-09-13. The hierarchy section adds separate owner-only reparenting, without inherited membership.
- The invitations section supersedes the earlier
  admin-only Cognito creation restriction for scoped Account invitations and
  aligns pending Account materialization with current-member addition authority.
- [ADR-055](ADR-055-designated-account-owner.md) permits another initial Owner and
  optional creator AM membership only during creation; member role rules remain.
- [ADR-071](ADR-071-account-owner-transfer.md) amends the Owner rule: Owner changes
  only through the dedicated owner transfer; member role updates stay owner-free.

### Original decision and rationale

Owner-only additions and role changes blocked normal teamwork when the owner was absent. Open these reversible operations to existing account members. Keep removal and cancellation of pending invitations owner-only because they revoke access and may trigger meeting-share cleanup. A manager-role tier was rejected as unnecessary permission-model expansion.

### Current behavior and invariants

- `AddMember` requires an existing member of the target account, regardless of role. `UpdateMemberRole` uses `requireMember` for the same rule. Non-members remain forbidden; these endpoints are not open registration.
- Both operations require `isAssignableRole` / `model.AssignableRoles`, which excludes `owner`. Role changes also reject an existing owner as the target. Members cannot promote themselves/others to owner or demote the owner through these APIs.
- `RemoveMember` and `RevokePendingMember` remain owner-only. The member-management UI exposes role controls to members and destructive controls to the owner; backend checks remain authoritative.
- Account/meeting PendingShare handling remains a separate identity-bound workflow: an existing Cognito invite without a profile may be queued. Materialization requires the invited Cognito sub, verified email, and an unexpired grant; a different user cannot claim it merely by adopting the email. TTL and synchronous expiry use the 30-day policy.

### Tradeoffs and limits

Any existing member can extend the account team and change non-owner role labels. This is the intended authorization decision, including the resulting access to account-published content and Projects linked under ADR-025. It does not grant membership in a parent/child account, permission to reparent, or permission to remove members.

This ADR does not authorize Cognito self-sign-up, owner promotion, or relaxing
PendingShare identity checks. Its original restriction against automatic Cognito
creation is superseded narrowly by the invitations section's domain-limited, rate-limited Account
invitations. The old non-owner-denial tests were replaced by member-success and
non-member-denial cases.

### Evidence

- [Member authorization](../../backend/internal/service/account.go): `AddMember`, `UpdateMemberRole`, `RemoveMember`, `RevokePendingMember`; [tests](../../backend/internal/service/account_test.go).
- [Role allowlist](../../backend/internal/model/account.go), [handler tests](../../backend/internal/handler/account_test.go), [member UI](../../frontend/src/components/AccountDetailClient.tsx).
- [Pending identity/expiry checks](../../backend/internal/service/meeting.go): `MaterializePendingShares`, `materializeOne`; [materialization transactions](../../backend/internal/repository/dynamodb.go).

## Account hierarchy and multi-account meeting filters

### Status notes

- Status: Accepted; extends flat account organization and single-account filtering. The member permissions section's member permissions and ADR-025's explicit Project-account links remain unchanged.
- Superseded in part by [ADR-058](ADR-058-descendant-account-read-sharing.md): the no-inheritance policy is replaced by descendant read grants. Filtering and hierarchy mutation rules remain applicable.
- Decision date: 2026-09-11.
- Code checked: 2026-09-13. Backend and UI exist in this checkout; deployment order/status is not established by source.

### Original decision and rationale

Represent affiliates under user-managed account groups and view several accounts' meetings together without migrating records or creating inherited authorization. Retain the single-account API for existing clients. The original delivery plan put compatible backend APIs before the tree/checkbox UI; that remains rollout guidance, not an outstanding implementation stage.

### Current behavior and invariants

- Optional `parentAccountId` gives an account one parent; omitted/empty values represent roots. Hierarchy is organization metadata, not membership or content authorization. Group detail tabs remain scoped to that account.
- Creating under a parent requires membership in the immediate parent. Reparenting requires both canonical child ownership and its owner-role membership, plus membership in the new immediate parent. Detaching needs no access to the old parent. Missing/null update fields are invalid; an explicitly empty string detaches.
- Ancestor reads are strongly consistent and bounded to 64 nodes. A partial-write transaction checks the child's observed parent/owner, owner membership, proposed-parent membership, and each observed ancestor parent link, including the root. Opposing moves cannot commit against the same stale ancestry. Conflicts reread/retry up to three attempts, then return conflict; unrelated storage failures remain errors.
- The tree is built only from caller-visible accounts. A visible child whose parent is absent/inaccessible is displayed as a root without showing the parent's name. Responses still carry `parentAccountId`; this is not a guarantee that the parent's identifier is hidden.
- Parent selectors and meeting checkboxes share the tree model. A group checkbox expands to its visible subtree, with mixed states and removable chips. The client sends explicit IDs; the server does not recursively expand hierarchy or grant descendant access.
- `accountIds` is a comma-separated OR selection, trimmed, deduplicated, sorted, and limited to 100 distinct IDs. Empty selection means all otherwise-authorized meetings. Supplying both `accountId` and `accountIds`, repeating either parameter, or using malformed IDs is rejected. Legacy `accountId` remains supported.
- Multi-filter pagination covers owned, direct-shared, and account-team discovery streams. Continuations bind user, tab, and normalized selection; empty pages can still carry a continuation. Cursors are pagination state, **not signed grants**: canonical meeting links and current membership remain checked independently, including cursor-supplied discovery candidates.

### Tradeoffs and limits

No descendant or meeting rewrite is needed, but ancestry checks add bounded transactional work and conflicts. The UI can only select visible descendants, not hidden affiliates. The ancestry/filter limits are explicit validation limits, not arbitrary truncation. Backend-first rollout avoids older servers rejecting the new client request shape; verify actual deployment separately.

### Evidence

- [Hierarchy service](../../backend/internal/service/account_hierarchy.go), [transactions](../../backend/internal/repository/account_hierarchy.go), [cycle/access tests](../../backend/internal/service/account_hierarchy_test.go), [transaction tests](../../backend/internal/repository/account_hierarchy_test.go).
- [Filter service/cursors](../../backend/internal/service/meeting_filter.go), [normalization](../../backend/internal/repository/meeting_filter.go), [access/pagination tests](../../backend/internal/service/meeting_filter_test.go), [HTTP tests](../../backend/internal/handler/meeting_filter_test.go).
- [Tree helpers](../../frontend/src/lib/accountTree.ts), [hierarchy UI](../../frontend/src/components/AccountHierarchyList.tsx), [parent editor](../../frontend/src/components/AccountParentEditor.tsx), [meeting picker](../../frontend/src/components/AccountTreePicker.tsx).

## Account members can invite new users directly

### Status notes

- Status: Accepted for implementation; deployed acceptance is recorded separately.
- Decision date: 2026-10-26.
- Supersedes the member permissions section's restriction against automatic Cognito creation for account
  invitations and the owner-only account materialization condition.
- [ADR-055](ADR-055-designated-account-owner.md) extends initial Account creation
  with a designated canonical Owner and optional creator membership; existing
  non-owner pending grants retain this ADR's rules.

### Decision

An authenticated member of an Account can add an email without asking an
administrator to create the user first. The existing authenticated add-member
operation creates a missing Cognito identity through `AdminCreateUser`, requests
email delivery, and queues an identity-bound Account grant. Existing registered
users continue to be added directly. Existing Cognito identities are reused;
ordinary retries do not resend invitations or reset passwords. Account members
may explicitly request a resend for an identity still awaiting its first
password change; confirmed users are never reset.

This decision implements the user's explicit request to let ordinary users invite
other people from Account creation and Account member management. It does not
enable Cognito self-sign-up or the separately gated project-invitation rollout.

### Constraints

- Authorize current membership and validate a bounded, normalized email and an
  assignable non-owner role before Cognito side effects.
- Keep the existing pre-sign-up domain allowlist and administrator endpoints.
  Member invitations never call group-management APIs or grant administrator status.
- Limit new invitation and explicit resend attempts to 20 per caller per UTC hour using the existing
  DynamoDB window counter. Fail closed if the counter is unavailable.
- Require the requester's verified email. Enforce the current domain allowlist
  before creation or resend; do not assume RESEND invokes the pre-sign-up trigger.
- Reuse Cognito's email-delivered temporary-password workflow. Preserve the
  current verified-email claim handling; materialization still requires a
  verified email and the exact invited Cognito sub.
- Revalidate the inviter's Account membership transactionally when queuing and
  claiming the grant. Any current member has addition authority under the member permissions section;
  removal and pending cancellation remain owner-only.
- Preserve the 30-day expiry, version-conditioned deletion, and atomic membership
  creation plus pending-row deletion. A removed inviter cannot confer access.
- Report an invitation sent but grant queue failure as partial success so the
  caller can retry without sending another email.
- Publish compatible API behavior before publishing clients that use it. Keep
  runtime configuration, origin proof, privacy rollout and unrelated flags intact.

### User-visible result

Account creation and member addition display whether the member was added,
whether an invitation email was requested and a grant queued, or whether a
specific operation failed. Ordinary users are no longer directed to ask an
administrator to invite a missing user.

Code, tests, current-HEAD review and a deployment receipt are required evidence.
They do not establish actual delivery to a recipient's mailbox.
