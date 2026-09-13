# ADR-036: Account hierarchy and multi-account meeting filters

- Status: Accepted; extends flat account organization and single-account filtering. ADR-034's member permissions and ADR-025's explicit Project-account links remain unchanged.
- Decision date: 2026-09-11.
- Code checked: 2026-09-13. Backend and UI exist in this checkout; deployment order/status is not established by source.

## Original decision and rationale

Represent affiliates under user-managed account groups and view several accounts' meetings together without migrating records or creating inherited authorization. Retain the single-account API for existing clients. The original delivery plan put compatible backend APIs before the tree/checkbox UI; that remains rollout guidance, not an outstanding implementation stage.

## Current behavior and invariants

- Optional `parentAccountId` gives an account one parent; omitted/empty values represent roots. Hierarchy is organization metadata, not membership or content authorization. Group detail tabs remain scoped to that account.
- Creating under a parent requires membership in the immediate parent. Reparenting requires both canonical child ownership and its owner-role membership, plus membership in the new immediate parent. Detaching needs no access to the old parent. Missing/null update fields are invalid; an explicitly empty string detaches.
- Ancestor reads are strongly consistent and bounded to 64 nodes. A partial-write transaction checks the child's observed parent/owner, owner membership, proposed-parent membership, and each observed ancestor parent link, including the root. Opposing moves cannot commit against the same stale ancestry. Conflicts reread/retry up to three attempts, then return conflict; unrelated storage failures remain errors.
- The tree is built only from caller-visible accounts. A visible child whose parent is absent/inaccessible is displayed as a root without showing the parent's name. Responses still carry `parentAccountId`; this is not a guarantee that the parent's identifier is hidden.
- Parent selectors and meeting checkboxes share the tree model. A group checkbox expands to its visible subtree, with mixed states and removable chips. The client sends explicit IDs; the server does not recursively expand hierarchy or grant descendant access.
- `accountIds` is a comma-separated OR selection, trimmed, deduplicated, sorted, and limited to 100 distinct IDs. Empty selection means all otherwise-authorized meetings. Supplying both `accountId` and `accountIds`, repeating either parameter, or using malformed IDs is rejected. Legacy `accountId` remains supported.
- Multi-filter pagination covers owned, direct-shared, and account-team discovery streams. Continuations bind user, tab, and normalized selection; empty pages can still carry a continuation. Cursors are pagination state, **not signed grants**: canonical meeting links and current membership remain checked independently, including cursor-supplied discovery candidates.

## Tradeoffs and limits

No descendant or meeting rewrite is needed, but ancestry checks add bounded transactional work and conflicts. The UI can only select visible descendants, not hidden affiliates. The ancestry/filter limits are explicit validation limits, not arbitrary truncation. Backend-first rollout avoids older servers rejecting the new client request shape; verify actual deployment separately.

## Evidence

- [Hierarchy service](../../backend/internal/service/account_hierarchy.go), [transactions](../../backend/internal/repository/account_hierarchy.go), [cycle/access tests](../../backend/internal/service/account_hierarchy_test.go), [transaction tests](../../backend/internal/repository/account_hierarchy_test.go).
- [Filter service/cursors](../../backend/internal/service/meeting_filter.go), [normalization](../../backend/internal/repository/meeting_filter.go), [access/pagination tests](../../backend/internal/service/meeting_filter_test.go), [HTTP tests](../../backend/internal/handler/meeting_filter_test.go).
- [Tree helpers](../../frontend/src/lib/accountTree.ts), [hierarchy UI](../../frontend/src/components/AccountHierarchyList.tsx), [parent editor](../../frontend/src/components/AccountParentEditor.tsx), [meeting picker](../../frontend/src/components/AccountTreePicker.tsx).
