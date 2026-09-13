# Historical implementation record: Account hierarchy and meeting filters

- Original plan date: 2026-09-11; recorded planning base `97b2cf1`.
- Historical proposal, not an execution checklist or current review mandate. Source pointers checked 2026-09-13; PR #183 and root reviewer-context work were separately scoped.

## Scope and rationale

Organize existing accounts as a visible tree and support checkbox selection across related accounts without inheriting authorization or seeding production company data. The backend and UI were separated into account-parent persistence, multi-account pagination, and reusable tree controls.

Parent creation/moves required appropriate ownership and immediate-parent membership. Optional parent metadata preserved old root records; explicit empty parent detached. Strongly consistent ancestor observations, partial transactional updates, and conditions on all observed links prevented opposing moves from committing a cycle. Validation was bounded to 64 ancestors and three conflict attempts.

Meeting `accountIds` represented a normalized OR selection of at most 100 explicit IDs across owned, direct-shared, and team discovery streams. Legacy `accountId` remained valid; mixed/repeated parameter forms were invalid. New cursors bound user, tab, and selection without making cursor content an authorization grant. Sparse-page continuation and first-login discovery hints were part of the intended contract.

The frontend expanded selected groups into visible descendant IDs, with searchable checkboxes, mixed states, removable chips, and cancellation/reset on filter changes. Missing parents displayed children as roots; group detail tabs remained account-scoped.

## Risks, validation, and result record

Intended tests covered cycle/access/revocation races, legacy roots, malformed and bounded filters, cursor context, deduplication, sparse pages, and shared-access freshness. Go tests/vet/ARM64 build and integrated frontend lint/static build were planned. All task boxes were unchecked; no executed results or merge were recorded. The old worktree/base information is provenance, not a current branch instruction.

## Current references

[ADR-036](../../decisions/ADR-036-account-hierarchy-and-meeting-filters.md), [hierarchy service](../../../backend/internal/service/account_hierarchy.go), [transaction tests](../../../backend/internal/repository/account_hierarchy_test.go), [filter service](../../../backend/internal/service/meeting_filter.go), [filter tests](../../../backend/internal/service/meeting_filter_test.go), [tree helpers](../../../frontend/src/lib/accountTree.ts), [picker](../../../frontend/src/components/AccountTreePicker.tsx).

Backend and UI code exist, but that does not prove deployment. Current responses retain parent identifiers even when the tree cannot display a parent's name. [ADR-034](../../decisions/ADR-034-account-member-permission-democratization.md) governs member changes independently; hierarchy does not broaden those permissions.
