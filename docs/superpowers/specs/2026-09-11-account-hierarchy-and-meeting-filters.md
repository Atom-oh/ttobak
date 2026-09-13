# Account Hierarchy and Meeting Checkbox Filters

> Historical design record. Original date: 2026-09-11 (filename); no original status
> was recorded. [ADR-036](../../decisions/ADR-036-account-hierarchy-and-meeting-filters.md)
> records the decision. This spec preserves design intent, not proof of deployment.

## Requested behavior and rationale

Organize related customer accounts into a user-managed tree and filter meetings
through a searchable checkbox tree. Example company groups were illustrative,
not production seed data. Selecting a parent includes its visible descendants;
OR selection, partial group state, removable chips, selected count, and clear-all
make the effective scope understandable.

## Persistence and authorization design

An optional `parentAccountId` gives each account at most one parent; existing
records remain roots. Creating under a parent requires membership there.
Reparenting requires ownership of the child and membership of the new parent;
clearing the parent returns a root. Hierarchy grants no account membership or
meeting/document/research/project access. A visible child with an inaccessible
parent appears as a root without fetching or disclosing that parent's name.

Reject missing parents, self-parenting, and cycles. Read ancestry consistently and
condition one partial transaction on the observed parent links and membership.
Bound ancestry to 64 nodes and retry conflicts with fresh reads a bounded number
of times. A stale whole-account replacement is not an acceptable implementation.
This prevents concurrent opposing moves without introducing a global tree lock.

## Filter and UI contract

The design uses an explicit, normalized OR set of at most 100 account IDs. Keep the
legacy single `accountId` alternative, reject ambiguous/malformed inputs, and treat
empty selection as no filter. The UI expands visible subtrees; the API does not
infer a grant or fetch inaccessible descendants. Owned, direct-share, and inherited
meeting streams apply the same selection. New cursors bind user, tab, and selected
IDs; selection changes reset paging and cancel obsolete requests.

Accounts and parent pickers use the same sorted tree. Parent editing is owner-only;
self/known descendants are excluded in the UI, with server validation authoritative.
The meeting control needs keyboard access, mobile wrapping, and loading/error/retry
states. Group chips may summarize fully selected subtrees; partial selections remain
explicit. Existing tokens/icons are reused without a new frontend test framework.

## Tradeoffs and verification intent

The hierarchy adds transaction complexity and bounded conflict retries while avoiding
record migrations or access inheritance. Account detail tabs remain scoped to one
account; cross-account viewing belongs to the meeting filter. Planned tests cover
cycles/races, membership, roots, all access streams, cursor mismatch, normalization,
and legacy requests. Frontend validation remains lint/build plus interaction checks.

## Current evidence

[account_hierarchy.go](../../../backend/internal/service/account_hierarchy.go),
[repository hierarchy writes](../../../backend/internal/repository/account_hierarchy.go),
[meeting_filter.go](../../../backend/internal/service/meeting_filter.go), and their
tests implement the core contract. [accountTree.ts](../../../frontend/src/lib/accountTree.ts)
and [MeetingList](../../../frontend/src/components/MeetingList.tsx) implement tree
selection. Source inspection supports these statements, not runtime rollout status.

Current references: [documentation map](../../README.md), [API](../../API-SPEC.md),
and [project guide](../../../CLAUDE.md).
