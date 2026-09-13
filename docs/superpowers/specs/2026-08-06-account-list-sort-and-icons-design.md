# Account Name Sorting and Category Icons

> Historical design record. Original date: 2026-08-06 (filename); no original status
> was recorded. Sorting was described as already implemented; category icons remained
> a proposal and are not a requirement inferred from this archive.

## Goals and proposed design

Make account lists and pickers predictable through Korean-aware name collation,
and make financial-sector customers easier to scan with a user-selected icon.
Existing accounts would require no migration and retain the default building icon.

The icon was deliberately separate from free-text `industry`, not inferred from it.
Creation would accept one fixed key: `card`, `bank`, `insurance`, `securities`,
`coin`, or `default`. A server-side allowlist would normalize unknown/empty input
to the default; frontend mapping would choose the corresponding Material Symbol.
A six-choice picker would feed the create request, and list/detail DTOs would carry
the optional key. Editing the icon after creation and changing industry semantics
were excluded.

## Rationale, risks, and validation intent

Client-side collation avoided a new index for a small loaded account list. A fixed
icon set avoided arbitrary rendered identifiers and inconsistent presentation;
shared lookup/fallback behavior was needed for legacy or unknown values. The draft's
unconditional map lookup was not itself a complete unknown-key guard.

Planned checks covered every icon, empty/invalid input, existing records, list/picker
consistency, and stable name sorting. The historical statement that no memoization
was necessary was a scale assumption, not a prohibition on future optimization.

## Current evidence and supersession

[accountTree.ts](../../../frontend/src/lib/accountTree.ts) sorts visible tree nodes
by Korean-aware name and ID. [ADR-036](../../decisions/ADR-036-account-hierarchy-and-meeting-filters.md)
replaces the flat-list model with a visible hierarchy.
The current [Account model](../../../backend/internal/model/account.go) and
[create API](../../../frontend/src/lib/api.ts) do not define the proposed icon
field/allowlist. Existing `corporate_fare` rendering is not proof that category
selection shipped or a new defect in an unrelated PR.

Current references: [documentation map](../../README.md), [API](../../API-SPEC.md),
and [UI reference](../../DESIGN-SPEC.md).
