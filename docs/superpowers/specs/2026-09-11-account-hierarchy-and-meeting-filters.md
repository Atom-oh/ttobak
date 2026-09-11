# Account hierarchy and meeting checkbox filters

## Requested behavior

Accounts form a tree rather than a flat list. For example, a user can create
`토스` with `토스증권`, `코어`, and `비바리퍼블리카` beneath it, or
`하나금융그룹` with `하나은행` beneath it. These are examples, not production
seed data.

The meeting list provides a searchable account checkbox tree. Multiple checked
accounts are combined with OR; existing tab, tag, and text-search behavior stays
available. Checking a group checks its accessible descendants as well. Selected
accounts are displayed as removable chips such as `토스증권 ×`, with a clear-all
action and partial selection state for groups.

## Persistence and access

- `Account.parentAccountId` is optional. Existing records remain roots without
  a migration. Each account has at most one parent.
- Account create accepts an optional parent; account detail lets the account
  owner change or clear the parent. Lists and detail responses include the ID.
- Reparenting requires ownership of the child and membership of the new parent.
  Creation under a parent requires membership of that parent.
- Hierarchy organizes accounts and filters; it grants no membership, meeting,
  document, research, or project access. Existing account permissions apply.
- An account whose parent is not visible is displayed as a root in that user's
  tree; the UI must not fetch or disclose an inaccessible parent's name.
- Reject missing parents, self-parenting, and cycles. Parent changes use partial,
  conditional transactional writes. Check every observed ancestor's parent link
  in the same transaction, preventing concurrent A→B and B→A updates from creating
  a cycle. Limit one ancestry validation to 64 nodes to bound work and keep the
  transaction under DynamoDB's 100-item limit.
- Retry conditional hierarchy conflicts a bounded number of times with fresh
  reads, then return a conflict response. Never overwrite an entire stale account.

## API contract

```ts
interface AccountSummary {
  accountId: string;
  name: string;
  role: string;
  parentAccountId?: string;
}

// Additional field on POST /api/accounts and account detail responses:
parentAccountId?: string;

// PUT /api/accounts/{accountId}/parent
// Empty string detaches the account into the root level.
{ parentAccountId: string }
// Success:
{ accountId: string; parentAccountId?: string }

// GET /api/meetings?accountIds=id1,id2&tab=all&cursor=...
```

Meeting filtering accepts up to 100 distinct account IDs, normalizes order and
duplicates, and rejects malformed IDs. Existing `accountId` requests remain
compatible; a request supplying both `accountId` and `accountIds` is rejected as
ambiguous. An empty selection means no account filter.

The UI expands group selections into explicit account IDs from the visible
account tree. The server treats these as an OR filter and still validates every
meeting's existing access. It does not trust hierarchy membership as a grant.
Owned, individually shared, and account-inherited meetings all honor the same
selection. New multi-account continuation cursors are bound to user, tab, and
the normalized selected IDs, including regular and inherited-team streams.
Changing filters resets pagination and cancels outdated requests.

## UI details

- Accounts page: sorted, expandable hierarchy, existing role badges and links;
  create form includes an optional parent picker.
- Account detail: show hierarchy position and an owner-only parent editor.
  Exclude self and known descendants from the picker; server validation remains
  authoritative.
- Meeting filter: collapsible/searchable checkbox tree, tri-state group
  checkboxes, removable chips, selected count, clear all, loading/error/retry
  states, keyboard-accessible controls, mobile wrapping.
- A fully selected subtree may collapse into one group chip. Partial selections
  remain explicit; a selected group's own meetings can be labeled `(직접)` if
  its descendants are only partially selected.
- Keep the established Tailwind tokens and Material Symbols. Do not add a UI
  library or frontend test framework.

## Verification

Go stdlib tests cover parent authorization, missing/self/cyclic parents,
conditional-write conflicts, transaction ancestry checks, legacy flat records,
multi-account union, selection-order invariance, all meeting access paths,
cursor/filter mismatch, pagination, and legacy single-account compatibility.
Run all Go packages, vet, and an ARM64 API build. Frontend verification uses
ESLint for changed files plus the static build; report existing full-lint
failures separately. Update API/design documentation and record the hierarchy
decision. PR review must cover the latest commit and preserve the existing
mandatory AI review/CI gates.
