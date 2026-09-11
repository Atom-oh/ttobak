# ADR-036: Account hierarchy and multi-account meeting filters

- Date: 2026-09-11
- Status: Accepted

## Context

A flat account list cannot represent groups with multiple affiliates. The
meeting list's single-account selector also makes it difficult to view meetings
for several related customer accounts together.

## Delivery stages

The hierarchy and multi-account filter APIs are integrated before the tree and
checkbox UI. Complete the backend deployment before rolling out the frontend
so existing clients remain compatible during the transition.

## Decision

Add optional `parentAccountId` metadata to existing account records. An account
has one parent or is a root; records without the field remain valid roots.
Owners can reparent their accounts into an account they belong to. Creation under
a parent requires the same parent membership. Hierarchy does not alter existing
membership or content authorization.

Validate proposed parent chains with strongly consistent reads. Write the child
parent field using a conditional partial transaction, including conditions on
the observed ancestor parent links and the requester's parent membership.
Concurrent opposing moves must not create a cycle. Bound ancestry reads to 64
nodes, below the transaction item limit, and retry conflicts at most three times
before returning a conflict.

Render the caller-visible accounts as an expandable tree. A missing or
inaccessible parent is not disclosed; its visible child appears as a root.
Use this same tree for parent pickers and meeting checkbox filters. Parent
selection expands to accessible descendants, while partial group selections and
removable chips make the effective selection visible.

The meeting API accepts `accountIds`, a normalized OR selection of up to 100
distinct explicit IDs. It preserves the existing single `accountId` contract,
rejects ambiguous requests, filters all existing meeting access streams, and
binds new continuation cursors to the user, tab, and selected-ID set.

## Consequences

- Existing accounts and integrations need no migration or permission changes.
- Groups are user-managed organization metadata, not legal-company seed data.
- The UI resolves visible subtrees; the API still authorizes meetings using
  their existing owner/share/account rules, independently of hierarchy.
- Reparenting does not rewrite descendants or meeting records. Ancestor
  transaction conditions protect against cycles without a global hierarchy lock.
- The ancestry-work and filter-size limits are explicit validation bounds.
- Group detail's existing meeting/research/document tabs remain account-scoped;
  cross-account meeting viewing is provided by the checkbox filter.
