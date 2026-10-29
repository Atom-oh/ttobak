# ADR-058: Descendant Account read sharing

- Status: Accepted on 2026-10-29 following explicit user authorization.
- Supersedes ADR-036's no-inheritance policy only. Its meeting classification,
  explicit multi-filter semantics and owner-only hierarchy writes remain.
- Narrows ADR-051's reader speaker-correction permission: inherited-only readers
  cannot correct speakers. Its direct-member and independent-share grants remain.
- Deployment requires companion consumers, conditional reference bootstrap and
  an explicit flag activation. This foundation PR remains disabled.

Members of an Account may read its descendants' published content, including
grandchildren. An ancestor Owner receives read-only access. Direct memberships
and independent resource shares retain their permissions; inheritance never
creates child member rows or authorizes edits, invitations, roles, billing,
publication, linking or hierarchy changes.

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

Go/API, bounded MCP readers, Account/documents, meeting discovery, projects,
research and both QA transports use the policy. Frontend responses distinguish
inherited reads and hide mutation/billing controls. The API and QA feature flag
defaults off. Bootstrap and deployed consumers precede activation; disabling
the flag restores exact direct-membership semantics without deleting references.

Evidence: [design](../superpowers/specs/2026-10-29-account-descendant-sharing-design.md),
[plan](../superpowers/plans/2026-10-29-account-descendant-sharing.md),
[Go resolver](../../backend/internal/service/account_read_access.go),
Python resolver (companion PR),
[relation writes](../../backend/internal/repository/account_hierarchy.go).
