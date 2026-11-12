# ADR-015: Accounts, roles, hierarchy and ownership

- Status: Accepted. Merges earlier records; each section below keeps one record's
  rules, and dates inside mark owner decisions.
- Implementation checked: 2026-11-11.

An account is a shared entity with members and roles. The sections below keep, in order, the base account record, member permissions, hierarchy and meeting filters, self-service invitations, the designated initial Owner, descendant read sharing and owner transfer. Where a later section changed an earlier rule, the earlier section has been corrected to say so.

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
- **The initial Owner section supersedes creator-only initial ownership:** creation atomically
  writes metadata, the selected initial Owner and optional creator AM membership.
  Detail reads check membership consistently, deliberately distinguishing missing
  accounts (`ErrNotFound`) from existing inaccessible accounts (`ErrForbidden`).
  Listing/alias lookup uses paginated membership queries and per-account reads;
  alias ambiguity is rejected rather than guessed.
- `model.AssignableRoles` contains `AM`, `TAM`, `SSA`, `SA`, `SA Manager`, and
  `AM Manager`. `owner` is assigned at creation and changes only through the
  owner-transfer endpoint (the owner transfer section); member APIs cannot grant or demote the
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
  may read a descendant's content as the descendant reads section describes. Visible children can appear as roots when their
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
- The initial Owner section permits another initial Owner and
  optional creator AM membership only during creation; member role rules remain.
- The owner transfer section amends the Owner rule: Owner changes
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
- Superseded in part by the descendant reads section: the no-inheritance policy is replaced by descendant read grants. Filtering and hierarchy mutation rules remain applicable.
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
- The initial Owner section extends initial Account creation
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

## Prepare an Account for another initial Owner

### Status notes

- Status: Accepted for implementation; deployed acceptance is recorded separately.
- Decision date: 2026-10-26.
- Supersedes the base account section's creator-only initial ownership. Extends the base account section only
  for initial creation and canonical Owner invitation recovery.
- Amended by the owner transfer section: ownership may later move
  through owner transfer, which clears the initial-owner invitation designation.

### Decision

An authenticated user can prepare a customer Account for a colleague by selecting
that colleague's email as the initial Owner. If no login identity exists, request
the domain-limited Cognito invitation described by the base account section. Bind ownership to the
current Cognito sub; an existing PROFILE email index must agree with that sub.
Pre-existing identities must be enabled, have a verified current email, and be
CONFIRMED or FORCE_CHANGE_PASSWORD before initial Owner membership is published.
The same gate applies when another request concurrently creates the identity.
Public self-signup and administrator-group grants remain disabled.

The creation UI offers an unchecked "Keep me as a regular member (AM)" checkbox
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

### Retry and delivery boundaries

- Creation accepts a single JSON object capped at 16 KiB. Owner email and input
  collections have additional service limits; arbitrary Owner IDs are rejected.
- Resolve/authorize the creator's selected parent before Cognito side effects.
  The creation transaction rechecks that creator membership and observed ancestry.
  The designated Owner neither needs nor inherits parent access.
- The frontend retains a creation UUID for the same normalized input across
  failed requests. Changed input gets a new UUID. The server records the exact
  creator and normalized request hash, and rereads after ambiguous transactions.
- Receipt replay returns original request fields and current creator membership.
  It never restores a removed creator, reveals later Account data, creates a
  second Account or resends mail. Legacy callers may omit the creation UUID.
- Frontend creation and canonical Owner resend bind requests to the initiating
  user. A session change during token refresh rejects the operation before an
  HTTP retry can execute it as a different user.
- Members, or the exact recorded creator for thirty days, may explicitly resend
  the canonical initial Owner invitation. The authenticated endpoint accepts no
  caller-selected identity, email or role; creator recovery is a delivery
  capability rather than a read grant.
- Resend rechecks current Cognito email/sub and FORCE_CHANGE_PASSWORD status.
  Confirmed users are never reset and recreated same-email identities cannot
  acquire ownership. The initial invitation hint in detail/receipts is not proof
  of current status; actual resend performs the authoritative check.
- New delivery and resend share the base account section's current domain allowlist and fail-closed
  twenty-attempts-per-caller-per-UTC-hour counter. A mail request followed by
  unconfirmed persistence is reported as partial success; mailbox delivery is
  never inferred.
- Account creation requires `read`, `share` and `write` capabilities in the
  connector inventory, matching its invitation and third-party ownership effects.
  A delegated principal approved only for `read` and `write` cannot create an
  Account. The new resend operation is inventoried with delegation disabled. No new
  unauthenticated route, origin, IAM permission or project rollout is introduced.

### Evidence and consequences

Service tests cover creator checkbox states, stale identities, parent loss,
quota/dependency rejection, ambiguous committed transactions, safe replay and
time-limited creator recovery. Wire tests inspect the atomic DynamoDB transaction
and both privacy stamps. Executable component tests cover hidden selections,
retry UUIDs, navigation and stale creation/resend controls.

The creator can intentionally create an Account they cannot later administer.
The colleague's first login must use the provisioned identity. These tests and
code review do not prove real-recipient mail delivery or first-login acceptance;
production artifact and deployment receipts are separate evidence.

## Descendant Account read sharing

[ADR-012](ADR-012-billing-origin-identity-and-team-access.md) supersedes only the Billing exclusion: shared ancestor teams may act on descendant Billing. Other inherited mutation restrictions remain.

- Status: Accepted on 2026-10-29 following explicit user authorization.
- Amended 2026-11-06 by the ADR-016 amendments (explicit user authorization):
  inherited members of an ancestor Account get `edit` on meetings published to a
  descendant, may correct speakers, and also manage those meetings like the
  creator except delete: email shares and invites, their revocation,
  previous-meeting links, uploads, recovery, rediarize, crop and the simulator.
  Project links are not included: they still need project access, which
  inheritance never grants (owner, direct project member or direct member of a
  linked account). This supersedes the invitation/linking
  restriction below **for meetings only**. Classification and publication still
  require direct membership of the target account; roles, hierarchy, documents
  and account invitations keep the inherited restrictions.
- Supersedes the base account section's no-inheritance policy only. Its meeting classification,
  explicit multi-filter semantics and owner-only hierarchy writes remain.
- (Superseded 2026-11-06 for meetings by the amendment above.) Narrowed ADR-016's
  reader speaker-correction permission: inherited-only readers could not correct
  speakers. Its direct-member and independent-share grants remain.
- Deployment is a separate gate in `docs/runbooks/account-descendant-sharing.md`,
  delivered with the final QA and rollout companion.

Members of an Account may read its descendants' published content, including
grandchildren. An ancestor Owner receives read-only access. Direct memberships
and independent resource shares retain their permissions; inheritance never
creates child member rows, Account roles, publication, linking or hierarchy
changes. For a meeting published to a descendant, inherited members get `edit`
under the 2026-11-06 amendment; Account-level grants stay read-only.

This is a staged, disabled policy at the foundation release. The Go environment
gate `ACCOUNT_DESCENDANT_READS_ENABLED` accepts exactly `1` to activate; an absent
or different value keeps inheritance disabled. The companion CDK parameter and
all consumers must land before activation. No deployed acceptance is claimed here.

Resolve authorization from current canonical META parent links and the nearest
ancestor membership, validating the complete chain to root. Missing, malformed,
cyclic or more-than-64-ancestor paths deny access. Storage failures surface.
Unpublishing, removing membership and reparenting revoke access on subsequent
reads. Bind source continuations and AI history to the complete grant identity,
including summary privacy eligibility, and revalidate before returning bytes.

Publication remains mandatory: a meeting merely classified under a child stays
private. Inherited readers always receive the current summary privacy guard
without ancestor legacy eligibility. Projects readable only through inheritance
do not expose private linked sources' titles or insights; each source needs
independent current read access.

Canonical parent changes and `ACCOUNT#{parent}/CHILDREF#{child}` discovery rows
share a transaction. References are indexes, never authorization. Bootstrap
repairs use an observed-parent condition. Discovery consumes empty advancing
pages and fails explicitly at compatibility limits rather than truncating.
Bounded discovery retains independently indexed direct roots even when an
ancestor traversal cannot reach them. Account pages declare overlapping results
with `overlapPossible` and `unionKey: "accountId"`; clients union by ID and use
the latest returned permissions. Account/project/meeting/vault catalog data pages
likewise declare their stable union key. Canonical grants authorize every result;
candidate-index presence and cursor state never establish access.

Go/API, bounded MCP readers, Account/documents, meeting discovery, projects,
research and both QA transports use the policy. Frontend responses distinguish
inherited reads and hide mutation/billing controls. The API and QA feature flag
defaults off. Bootstrap and deployed consumers precede activation; disabling
the flag restores exact direct-membership semantics without deleting references.

Evidence: [design](../superpowers/specs/2026-10-29-account-descendant-sharing-design.md),
[plan](../superpowers/plans/2026-10-29-account-descendant-sharing.md),
[Go resolver](../../backend/internal/service/account_read_access.go),
Python resolver `backend/python/qa/account_read_access.py` (final QA companion),
[relation writes](../../backend/internal/repository/account_hierarchy.go).

## Transfer Account ownership to an existing member

### Status notes

- Status: Accepted for implementation; deployed acceptance is recorded separately.
- Decision date: 2026-11-10.
- Amends the base account section and
  the initial Owner section: they forbade any post-creation
  Owner change; this ADR adds exactly one path.

### Decision

The current Owner may transfer ownership with
`POST /api/accounts/{accountId}/owner-transfer` and body `{"targetUserId"}`.
This is the only path that grants Owner after creation. Generic AddMember,
UpdateMemberRole and pending-share materialization stay owner-free.

- **Dual owner condition:** the requester must hold the Owner MEMBER role and be the
  META `ownerUserId`. The transaction conditions both, so a split state or a
  concurrent transfer fails closed.
- **Target identity:** the target must be an existing direct non-owner member
  (not self). Cognito `AdminGetUser` must report the same `sub`, Enabled and
  CONFIRMED with `email_verified` `"true"`; the repository cannot distinguish
  pending or disabled invitees. UserNotFound, disabled, unconfirmed, unverified
  (false or absent) or sub-mismatch targets are 400. 503 is returned only for an unwired
  Cognito client or a lookup error other than not-found, never a fallback.
- **Previous Owner:** becomes SA, retaining access without Owner-only powers.
- **Initial-owner designation:** the same transaction clears `initialOwnerEmail`
  and `initialOwnerInvitationPending`, so the initial Owner section resend cannot re-target a
  transferred Account. It also sets `originalOwnerUserId` once
  (`if_not_exists`), so the initial Owner section creation receipts keep replaying the original Owner.
- **Owner-fenced member writers:** generic role-update and removal writes condition
  on `role <> owner`, removal also on the requester still being Owner (META and
  MEMBER), and AddMember's put is always create-only. A writer that interleaves
  with a transfer returns 409 (or member-exists) instead of overwriting or
  deleting the new Owner row.
- **One transaction:** META, target MEMBER and previous-owner MEMBER change
  together; any condition failure writes nothing and returns 409.
- **Non-delegable:** delegation is
  disabled for this operation in the connector inventory.

### Consequences

Removal, revocation and reparenting remain Owner-only and now follow the new
Owner. Access tokens issued before transfer remain valid until expiry (ADR-007),
but authorization reads current membership, so the previous Owner loses Owner powers
immediately. There is no transfer to a non-member and no multi-owner state.

### Evidence

- [Service](../../backend/internal/service/account_owner_transfer.go) and
  [transaction](../../backend/internal/repository/account_owner_transfer.go), with
  their tests.
- Route contract: [API-SPEC](../API-SPEC.md) Accounts section.

## Customer brief (2026-11-12)

### Status notes

The worker (model, repository, service, summarize dispatch and the
`ttobak-customer-brief-requested` rule) and the API producer (GET/POST
`/api/accounts/{accountId}/customer-brief` and the EventBridge publisher in the API
Lambda; see [API-SPEC](../API-SPEC.md) customer brief) are in code. The UI is not
shipped.

### Decision

- A current **direct** member (a MEMBER row on an existing account) requests a
  one-page pre-meeting brief. Inherited ancestor or billing read access does not
  qualify. The worker rechecks membership as the requester before reading and
  again before publishing; the completion transaction also requires the MEMBER
  and META rows.
- One row per (account, member): `ACCOUNT#{accountId}` / `CUSTBRIEF#{userId}`
  holds plaintext run state (run ID, status, lease, input hash, error code) and
  the last good brief sealed with the record sealer, bound to account and user.
  A failed run writes only the error state.
- `CustomerBriefRequested` carries `{accountId,userId,runId}` only. Leases follow
  the saved re-summary run (queued 5 min, running 10 min, 8-minute work budget);
  a live lease returns the existing run and an expired one may be replaced.
- Inputs are bounded: the account record, readable insights (90 days), the 20
  most recent readable meetings, current customer news (30 days, 30 items),
  the earliest five upcoming account or synced-calendar events, and the account
  and personal dictionary term keys. Changed inputs at publication fail the run
  with `SOURCE_CHANGED`. The synced-calendar read follows its continuation for
  at most ten more pages; reaching that bound before five matching events fails
  the run (`SOURCE_UNAVAILABLE`) rather than omitting the next meeting. A
  completion refused for revoked membership records `ACCESS_REVOKED`.
- `BEDROCK_MODEL_ID` writes Korean JSON through the strict complete-response
  helper. Every statement must cite opaque input references; a statement whose
  citations do not all resolve is dropped. Source kind, ID and label come from
  the worker's inputs, the next meeting is computed, and terms must occur
  literally in their cited inputs. Output is plain text, at most 24,000 bytes.
- A successful publish sets `briefExpiresAt` and `pendingShareExpiresAt` 30 days
  ahead. Reads treat an expired row as absent and return the brief only while
  `now < briefExpiresAt`. Queuing over a readable brief pins that expiry and
  floors only the row TTL so a working run is not swept; it never extends the
  brief. Queuing over a brief past its expiry clears the sealed content in the
  same write. Rows written before `briefExpiresAt` existed use the row TTL as the
  brief expiry (the next queue pins it).
- The POST publishes `CustomerBriefRequested` after claiming the run. A delivery
  failure records `failed/PUBLISH_FAILED` and returns 503, as re-summary does, so
  the run never stays queued silently and can be retried.
- Non-members and missing accounts get the same 404. Only allowlisted worker error
  codes reach clients; anything else reads as `FAILED`.
- `inDictionary` uses the effective personal term keys: rows plus, before
  migration, legacy blob terms no row hides. The worker reads the blob with
  GetItem only and never runs the migration write.
- Removing a member deletes their `CUSTBRIEF#` row in the member-removal
  transaction.

### Known limits

- Other ways of losing membership (account merge, account deletion) leave the
  row until expiry; the read-time membership check makes it unreadable.
- Account merge does not move brief rows.
- Inherited readers cannot generate a brief.
- A published brief is a snapshot. It is not revalidated when a meeting or
  insight it cites is later unpublished or re-restricted: the reader had access
  at generation time, and the brief can show that text until it expires or is
  regenerated.

### Evidence

- [Service](../../backend/internal/service/customer_brief.go),
  [inputs and validator](../../backend/internal/service/customer_brief_content.go)
  and [repository](../../backend/internal/repository/customer_brief.go), with
  their tests.
- Delivery: [INFRA-SPEC](../INFRA-SPEC.md) customer brief worker delivery.
