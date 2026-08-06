# Account list: sort by name, per-category icons

## Problem

1. The Accounts list (`AccountsClient.tsx`) and the account picker dropdown
   (added to `ProjectDetailClient.tsx` in a prior fix) render accounts in
   whatever order `GET /api/accounts` returns them — no sort — which reads as
   random/chaotic to a user with more than a few accounts.
2. Every account renders the same hardcoded `corporate_fare` icon regardless
   of what kind of company it is, even though many of a user's accounts are
   financial-sector customers (card companies, banks, insurers, securities
   firms, crypto exchanges) where a category-specific icon would make the
   list scannable at a glance.

## Goals

- Accounts list and account picker sorted by name (Korean-aware collation).
- A user can pick an icon for an account at creation time, from a fixed set:
  card / bank / insurance / securities / coin / default (unknown).
- Accounts without an icon (existing data, pre-this-change) render the
  default icon — no backfill/migration needed.

## Non-goals

- Editing an account's icon after creation (not requested — flag as a
  follow-up if wanted).
- Deriving the icon automatically from the existing free-text `Industry`
  field — this is a new, separate `icon` field, independent of `Industry`.
- Any change to `Industry`'s own semantics or the create-form's industry
  input.

## Design

### Sorting (already implemented)

`AccountsClient.tsx` and `ProjectDetailClient.tsx`'s account picker both sort
their `AccountSummary[]` at render time with
`[...accounts].sort((a, b) => a.name.localeCompare(b.name, 'ko'))` — no
backend change, since the full list is already fetched in one call
(`accountApi.list()`) and re-sorting a small in-memory array on every render
is cheap enough not to warrant `useMemo`.

### Icon field

- `Account` (`backend/internal/model/account.go`): add
  `Icon string \`dynamodbav:"icon,omitempty"\``.
- `CreateAccountRequest`: add `Icon string \`json:"icon,omitempty"\``.
- `AccountResponse` / `AccountSummary`: add `Icon string \`json:"icon,omitempty"\``.
- Fixed allowlist (`model.AccountIcons = []string{"card", "bank",
  "insurance", "securities", "coin", "default"}`), enforced server-side in
  `AccountService.CreateAccount`: any value not in the allowlist (including
  empty string) is stored as `"default"`. This is a trust-boundary
  validation, not just a nicety — an unvalidated client-supplied string here
  would otherwise flow straight into a rendered icon key.
- Material Symbols mapping (frontend-only, a plain lookup object — no need
  for a backend-side name-to-icon table):
  - `card` → `credit_card`
  - `bank` → `account_balance`
  - `insurance` → `health_and_safety`
  - `securities` → `candlestick_chart`
  - `coin` → `currency_bitcoin`
  - `default` → `corporate_fare` (today's hardcoded icon — unchanged for
    accounts with no icon set)

### Frontend

- `frontend/src/types/meeting.ts`: add `icon?: string` to `AccountSummary`
  (and `Account`/`AccountResponse`-equivalent type if a separate detail type
  exists), plus an exported `ACCOUNT_ICONS` map (icon key →
  `{ symbol: string; label: string }`) for the picker and list render to
  share.
- `AccountsClient.tsx`'s create-account form: a row of 6 icon buttons (same
  visual pattern as tag-filter chips — `rounded-full`, selected state
  `bg-primary text-white ring-2 ring-primary/30`), backed by a new
  `icon` state (defaulting to `'default'`), included in the
  `accountApi.create()` call.
- Both the Accounts list and the `ProjectDetailClient` account picker replace
  the hardcoded `<span className="material-symbols-outlined">corporate_fare</span>`
  with a lookup: `ACCOUNT_ICONS[a.icon || 'default'].symbol`.

## Testing

- Backend: table-driven test for the icon-allowlist validation (valid value
  passes through, invalid/empty value normalizes to `"default"`).
- Frontend: manual check — create an account with each of the 6 icon
  choices, confirm the list and project account-picker show the right icon;
  confirm an account created before this change (no `icon` field) still
  renders the default icon.
