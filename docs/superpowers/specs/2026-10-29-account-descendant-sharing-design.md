# Descendant Account sharing

> Historical design record. Proposed behavior; this is not evidence of
> implementation or deployment.

- Status: Proposed; product code and production configuration are unchanged.
- Date: 2026-10-29.
- Source inspected: `6147a43ea2fdfe9471a5baf8c1f77331848124da`.
- User requirement: members of an upper Account group should receive access to
  content shared with its descendant Accounts.
- Scope assumption pending user confirmation: inherited access is read-only.

## Observed problem

An operator checked the canonical metadata of the reported meeting and its
Account chain with strongly consistent DynamoDB reads. The meeting is completed,
is published to the child Account, has a sharing-safe summary marker, and has a
child Account meeting reference. The child has one direct member; its immediate
parent has three direct members, including that child member. The additional two
parent members cannot use the current direct-membership authorization path.
No meeting text, credentials, or member email addresses are included here.

This follows the existing ADR-036 behavior: hierarchy organizes Accounts but
does not confer access. The new user requirement changes that behavior. Changing
one meeting's share rows would leave the same problem for future meetings, new
parent members, discovery lists, documents, and AI reads.

## Resulting behavior

A current member of an Account can read resources published to that Account and
any of its descendants. A grandparent membership also qualifies. Inherited
access applies to existing and future published resources and to members added
later; no re-sharing operation is required.

Examples:

| Membership | Resource Account | Result |
|---|---|---|
| Parent only | Child or grandchild | Read inherited shared content |
| Child only | Parent | No inherited access |
| Child only | Another child of the same parent | No inherited access |
| Ancestor and direct child member | Child | Preserve the direct child grant |
| Ancestor only, including ancestor Owner | Child | Read-only inherited access |
| Removed ancestor member | Child | Deny on subsequent reads unless another current grant qualifies |
| Owner or explicit individual recipient | Any | Preserve that independent resource grant |

Content must already satisfy its existing publication rules. A meeting merely
classified under an Account (`accountId` present, `sharedToAccount` false) remains
private. Existing summary privacy gates apply to the new readers. Inherited
access is a new grant and receives no legacy summary eligibility from the
ancestor membership's age or privacy version.

The requested surfaces are Account metadata and shared content discovery,
published meetings and attachments, Account documents, Account insights,
linked research, and read access to linked projects and their shared resources.
Web, authenticated MCP, and both QA transports must agree.

The builder's proposed permission choice is read-only inheritance. Direct
Account and project memberships retain their existing permissions. Inherited
read access does not authorize document writes, meeting edits, linking or
publishing resources, invitations, role updates, removals, ownership changes,
project mutations, or hierarchy changes. The separate billing service keeps its
existing direct-membership contract; changing financial access is outside this
content-sharing request.

## Authorization implementation

Keep `GetMember` as an exact direct-membership primitive. Add a separate read
grant resolver in the Go service layer with a narrow repository interface for
strongly consistent Account META and member reads. A corresponding Python helper
must implement the same policy for the separate QA artifact.

A resolved grant identifies the requested Account, the granting Account, the
current member, whether access is inherited, the observed parent chain, and a
stable grant fingerprint. Do not store fabricated child memberships or permanent
individual share rows for inherited readers.

For direct access, validate the canonical Account/member identities and preserve
the current direct permission. For inherited access, validate the complete
observed path from the requested Account to the root and choose the nearest
qualifying ancestor membership. Reuse ADR-036's maximum of 64 ancestors, valid
identifiers, and cycle rejection. Missing Accounts, malformed identities,
cycles, and excessive depth deny inherited access; storage failures propagate.
No ancestor names are exposed without a qualifying read grant.

Strong reads do not make several reads an atomic snapshot. Record the observed
parent links and grant member identity, then revalidate them before returning
source bytes. A changed or revoked grant causes a bounded retry or the endpoint's
existing stale/not-found error. Revalidate on every request, continuation, and
QA history replay. There is no cross-request positive authorization cache.

The grant fingerprint includes the target and granting Account identities,
ordered observed parent links, and membership identity/version fields already
used by the reading contracts. Continuations bind this fingerprint together with
the existing resource revision and selection. Reparenting or ancestor-member
removal invalidates a previous continuation.

An account-origin share row remains a discovery/cache row. If the reader has
only inherited access, it supplies read permission even when a stale account
share row contains edit permission. An independent individual share retains its
own existing permission.

## Discovery and relation persistence

The current Account META schema has no child discovery index. Add lightweight
child references in the parent partition:

- `PK = ACCOUNT#{parentId}`.
- `SK = CHILDREF#{childId}`.
- Fields identify the parent, child, and a dedicated entity type.

References discover candidates; they do not authorize access. Query pages must
advance and every candidate must be checked against its current canonical parent
link and live read grant. A stale reference must never expose a moved child.

Maintain the old-parent deletion and new-parent insertion in the same
transaction as canonical parent updates. Include creation with a parent, including
designated-Owner creation, and Account deletion if a delete path exists. Preserve
the existing owner, member, parent-chain, and race conditions on hierarchy writes.
Changing a parent now changes inherited readership and the UI should explain this
next to the existing parent control.

Provide a metadata-only, paginated, idempotent bootstrap for existing parent
links. Each reference write checks the strongly observed child's current parent
in its transaction. A concurrent move produces a retry/skip, never a false
authorization grant. The bootstrap records counts and incomplete work without
logging content. Reader activation requires a completed bootstrap and a
reconciliation check.

The current user Account discovery starts with direct membership candidates,
rechecks each canonical member, then traverses child reference pages. Keep the
100-ID meeting filter contract and resource result limits. Descendant discovery
must be paginated and bounded per request rather than returning a silently
truncated subtree or scanning the entire application table for each request.
The builder chooses concrete page/work budgets and a compatible Account-list
continuation API; the frontend and MCP/QA callers must consume all needed pages.

Meeting team discovery uses these reachable Account candidates. Even cursor
candidates are reauthorized. Meeting references must still match a current
published meeting's exact Account. Deduplicate meetings with direct shares and
preserve the existing regular/team streams and filter binding.

## API, UI, MCP, and QA

Account responses add explicit access metadata, including read versus edit and
the granting ancestor ID for inherited access. Direct member DTOs remain actual
member records. An inherited reader is not reported as the child Owner or as a
direct child member.

The Account tree and meeting filters include authorized descendants. The Account
detail and document views show inherited read access and hide mutation controls
when the caller has no direct grant. Existing direct membership and independent
resource grants continue to determine their mutation controls. Avoid using an
ancestor's Owner role to determine child permissions.

Go read paths include shared meeting resolution, team meeting discovery, Account
detail/brief/alias resolution, insights, research, documents, bounded Account and
document reads, linked project/resource reads, attachment readers, and knowledge
services that reuse these grants. Review every use of the direct `GetMember`
primitive; only read authorization and discovery adopt the new resolver. Mutation
paths keep direct checks.

Project read and mutation authorization currently share helpers. Split those
capabilities before granting inherited project reads so reads cannot authorize
writes. Revalidate canonical project Account links as well as hierarchy grants.
For an inherited-only project reader, a project link alone does not authorize a
meeting's metadata, content, or derived insights. Each child resource must pass
its independent current read authorization: qualifying Account publication or an
existing owner/individual grant. Filter unauthorized meeting references before
rendering titles and filter aggregated insights by their authorized source.
Exclude insights whose source identity cannot be verified. Existing direct
project member behavior from ADR-025 remains unchanged. This is the builder's
scope choice to preserve the requested private-meeting boundary for new readers.

MCP delegates resource authorization to the authenticated Go APIs and preserves
opaque server continuation and byte limits. Update its Account discovery caller
if the Account list becomes paginated. No new public route or origin is required.

Python QA needs one shared read-grant/discovery helper, used by `handler.py`,
`account_reads.py`, `document_context.py`, and `indexed_retrieval.py`, with an audit
of other current-source consumers. Keep identity, revision, source freshness,
attachment, summary privacy, and history replay checks. Both HTTP and WebSocket
transport registrations must use the updated helpers. Membership or reparenting
changes invalidate any cached content/replayed history that depends on the grant.

Inherited summary views always enforce the current sharing-safe gate. Account
insights derived from summaries must use the same restriction. Existing owner and
independent direct-share eligibility retain their separate rules.

## Release and documentation

Introduce a nonsecret `ACCOUNT_DESCENDANT_READS_ENABLED` flag, default off, for
both Go and Python artifacts. Stage relation writers and bootstrap tooling first,
finish bootstrap/reconciliation, then release all read consumers and UI before
activation. A successful deployment alone does not establish consumer acceptance.

Activation requires authenticated acceptance using a parent-only test member:
the reported child meeting is discoverable and readable, a private linked-only
fixture is denied, an unrelated sibling fixture is denied, writes are denied,
and removing membership or detaching the child invalidates access and old
continuations/history. Do not modify the reported meeting or send new invitations
to manufacture the acceptance result.

Rolling the flag back to off restores direct-only hierarchy authorization. Child
references may remain as non-authoritative indexes. Do not remove existing
individual shares or direct memberships during rollback.

Record an ADR explicitly superseding only ADR-036's no-inheritance decision.
Update affected API, architecture, UI and QA contracts, the canonical guide, and
the generated review context. Preserve ADR-036's filtering and hierarchy-write
rules. Deployment uses only the affected functions/stacks under the existing
release procedures; implementation approval does not invent additional cloud
mutation or external-message authorization.

## Required verification

- Direct child grant wins; ancestor Owner gives only inherited read access.
- Parent, grandparent, future member, existing resource, and new resource reads.
- No upward, sibling, malformed, missing-node, cycle, or over-depth access.
- Link-only private meetings remain private across detail, list, filters and AI.
- New inherited readers cannot use a legacy unsafe summary or derived insights.
- Removing ancestor membership, unpublishing, moving/detaching an Account, and
  changing a project link revoke access despite stale references/share rows.
- Pagination includes empty advancing pages, descendants beyond a first page,
  forged cursor candidates, bounded work, deduplication, and stale continuations.
- Concurrent hierarchy writes and bootstrap use canonical/reverse transactions.
- Every inherited mutation attempt is rejected through the server, including
  project writes that currently share read authorization helpers.
- HTTP QA, WebSocket QA, MCP reads, and replayed histories share the same policy.
- Run full Go tests including `cmd/*`, Go vet and ARM64 API build; frontend lint,
  unit scripts and static build; affected QA/MCP tests and documentation checks.

## Approval boundary

The diagnosis is verified. This document proposes the read-only permission scope
and cross-component implementation. Product code, relation bootstrap, feature
activation and deployment have not been performed for this change.

A fresh builder review raised two material questions. Read-only inheritance,
including ancestor Owners, remains the single user scope confirmation. Project
resource exposure is settled above: new inherited-only readers must independently
qualify for each resource before its metadata or derived content is returned.
