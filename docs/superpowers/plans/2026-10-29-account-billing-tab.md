# Account Detail Tabs and Billing Tab Implementation Plan

> Historical plan record. Status: Draft rev 1, 2026-10-29 (after codex review a5);
> implementation intent, not proof of delivery or deployment.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn the Account detail page into tabs (Overview / Meetings / Projects / Docs / Billing) and add a per-Account Billing tab that summarizes that Account's billing data and links into the full `/billing` dashboard preselected on the Account.

**Architecture:** The Billing BFF gains account-scoped current-version reads (`/api/billing/data/<id>.json?acct=<id>`), so the tab never downloads other Accounts' rows. The Account page keeps its data fetching and handlers; only rendering moves into tab panels selected by an allowlisted `?tab=` query. `/billing?acct=` forwards a validated preselection into the dashboard iframe, and the shim uses it as the default entity.

**Tech Stack:** Next.js static export (React, TypeScript, Tailwind), frontend unit tests with `node --test 'scripts/*.test.mjs'`, Billing BFF (Express on Lambda Web Function, `node --test`), dashboard page (`services/billing/page`).

**Spec:** [Billing service design](../specs/2026-10-26-billing-service-design.md) §12 (per-Account Billing tab), plus the owner-approved menu design canvas (Account detail › Billing tab).

## Global Constraints

- Billing UI appears only when runtime config `billingService === true` **and** `/api/billing/accounts` lists this Account with `member: true`; upload controls only with `canUpload: true`.
- Every Billing request is same-origin `/api/billing/...` with `Authorization: Bearer <TTOBAK ID token>`, one refresh-and-retry on 401 (same behavior as `frontend/src/lib/api.ts` `apiFetch`).
- BFF: an inaccessible or malformed `acct` answers 404, never 403, never another Account's rows. Membership is re-read per request (`access.canAct(..., { fresh: true })`).
- No customer names or customer data in code, tests, fixtures or docs. Fixtures use synthetic IDs and numbers.
- Missing values render as `—`, never `0`; COT-only, CUR-only and no-data Accounts are distinct states.
- `.md` files English only; UI copy in Korean, matching the design canvas (strings supplied in task dispatches).
- One PR per task; each PR passes its package's tests, `npm run lint` and `npm run build` (frontend) before review.

## Review Focus

1. **Cross-Account leak through `?acct=`:** a member of A asking `?acct=B` gets 404 with no S3 read. Test `acct_only_nonmember_404_without_read` (Task 1).
2. **Pinned-version behavior regresses:** `?acct=&v=` keeps today's semantics exactly. Test `acct_and_version_unchanged` (Task 1).
3. **Dashboard preselect shows another Account:** `/billing?acct=X` where X has no data must not fall back to the first Account's numbers; it shows the upload prompt. Test `preselect_without_data_shows_upload_prompt` (Task 3).
4. **Stale tab content after navigation:** switching from Account A to B (client navigation) never shows A's Billing numbers under B's name; late responses are dropped. Test `late_response_for_previous_account_ignored` (Task 4).
5. **Tab refactor drops a control:** every control that exists today (member role select, remove, invite picker, resend owner invite, pending notice revoke/resend, slide upload, new note, parent editor) is still reachable in some tab. Test `every_existing_control_still_rendered` (Task 2).

---

### Task 1: BFF account-scoped current reads

**Files:**
- Modify: `services/billing/bff/app.js` (`/data/:id.json` handler)
- Test: `services/billing/bff/test/app.test.js`

**Interfaces:**
- Produces: `GET /api/billing/data/<id>.json?acct=<acctId>` → the rows `currentRows` returns for that one Account (same dataset allowlist, handwritten and CUR rules, `raw_*` and `raw_catalog` scoping).
- Query matrix: none → unchanged (all authorized Accounts); `acct` only → validate with `ACCT_ID`, require `access.canAct(req.sub, acct, { fresh: true })`, then pick the entry from `req.accounts`; `acct` + `v` → unchanged; `v` only, repeated params, non-string, invalid → 404.

- [ ] **Step 1: Failing tests:** `acct_only_returns_that_account_rows`, `acct_only_nonmember_404_without_read` (fake S3 records zero GetObject), `acct_only_invalid_id_404`, `v_only_404`, `acct_and_version_unchanged`, `acct_only_respects_cur_and_handwritten_sources`.
- [ ] **Step 2: Run** `cd services/billing/bff && npm test` → FAIL.
- [ ] **Step 3: Implement** in the handler; reuse `currentRows`; no new cache.
- [ ] **Step 4: Run** → PASS (all suites).
- [ ] **Step 5: Commit** `feat(billing): account-scoped current dataset reads`.

### Task 2: Account detail tabs

**Files:**
- Create: `frontend/src/lib/accountTabs.ts`, `frontend/scripts/account-tabs.test.mjs`
- Modify: `frontend/src/components/AccountDetailClient.tsx`

**Interfaces:**
- `ACCOUNT_TABS = ['overview', 'meetings', 'projects', 'docs', 'billing'] as const`; labels Overview, Meetings, Projects, Docs, Billing (Korean copy from the design canvas, given in the dispatch).
- `parseAccountTab(value: string | null, billingVisible: boolean): AccountTab` → allowlisted value, `billing` only when visible, else `overview`.
- `accountTabHref(search: string, tab: AccountTab): string` → keeps other query params, drops `tab` for `overview`.
- Panels: Overview = parent editor + Members (incl. invite, pending notice) + FieldInsights; Meetings = Shared Meetings; Projects = linked projects + research; Docs = Documents (slide upload, new note); Billing = placeholder slot filled by Task 4 (rendered only when visible; this task passes `billingVisible = false`).
- Tab bar: `role="tablist"`, buttons `role="tab"` with `aria-selected`, arrow-key navigation, horizontally scrollable on mobile, min height 44px. URL updates with `router.replace` (no history spam); back/forward re-reads `useSearchParams`. Follow the repo's `useSearchParams` + `Suspense` pattern for static export.
- Fetching and handlers stay as they are.

- [ ] **Step 1: Failing tests** in `account-tabs.test.mjs` (follow `scripts/account-invite-navigation.test.mjs` style): `unknown_tab_falls_back_to_overview`, `billing_hidden_falls_back_to_overview`, `href_keeps_other_params`, `overview_drops_tab_param`, `every_existing_control_still_rendered` (source-level check that each listed control's handler is still referenced inside a panel).
- [ ] **Step 2: Run** `cd frontend && npm run test:unit` → FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `npm run test:unit && npm run lint && npm run build` → PASS.
- [ ] **Step 5: Commit** `feat(web): tabs on the Account detail page`.

### Task 3: Dashboard preselect by Account

**Files:**
- Modify: `frontend/src/app/billing/page.tsx`, `services/billing/page/dash-shim.js`, `services/billing/page/body.html` (empty-Account state only)
- Test: `frontend/scripts/billing-insight.test.mjs` or a new `frontend/scripts/billing-preselect.test.mjs`; `services/billing/bff/test/shim.test.js`, `services/billing/bff/test/page-body.test.js`

**Interfaces:**
- `/billing?acct=<id>` → iframe `src` `/billing-app/index.html?acct=<id>`; `/billing?view=upload&acct=<id>` → `/billing-app/upload.html?acct=<id>`. The id is passed only when it matches `^[A-Za-z0-9_-]{1,64}$` (use the BFF `ACCT_ID` pattern if stricter), encoded with `URLSearchParams`; the iframe path stays fixed.
- Shim: after `/api/billing/accounts`, if `location.search` `acct` names a listed Account, `DEFAULTS.entity = acct` even when it has no data; otherwise today's default.
- Body: when the selected entity has neither COT nor CUR data, show the existing upload prompt (link `upload.html?acct=`) instead of `ent()` falling back to another Account.

- [ ] **Step 1: Failing tests:** `iframe_src_carries_valid_acct`, `iframe_src_drops_invalid_acct`, `upload_view_carries_acct`, `shim_preselects_listed_account`, `shim_ignores_unlisted_account`, `preselect_without_data_shows_upload_prompt`.
- [ ] **Step 2: Run** the frontend unit tests and `cd services/billing/bff && npm test` → FAIL.
- [ ] **Step 3: Implement**; rebuild the dashboard bundle the way `frontend/scripts/build-billing-app.mjs` expects.
- [ ] **Step 4: Run** → PASS, plus frontend lint and build.
- [ ] **Step 5: Commit** `feat(billing): open the dashboard on a chosen Account`.

### Task 4: Billing tab content

**Files:**
- Create: `frontend/src/lib/billingApi.ts`, `frontend/src/components/AccountBillingTab.tsx`, `frontend/scripts/account-billing-tab.test.mjs`
- Modify: `frontend/src/components/AccountDetailClient.tsx` (visibility + panel)

**Interfaces:**
- `billingApi.ts`: `billingFetch<T>(path: string, signal?: AbortSignal): Promise<T>` (same-origin, bearer + one refresh retry, BFF flat `{error}` bodies → `ApiError`); `fetchBillingAccounts()`; `fetchAccountDataset<T>(id: BillingDatasetId, acct: string, signal?)` using Task 1's `?acct=`.
- Visibility: `billingService` runtime flag true and the Account listed with `member: true`. Has data = `sources.cot || sources.cur`.
- Content (lazy, only when the Billing tab is open; abort and ignore responses for a previous Account):
  - KPI tiles: latest month spend = `kpis.last_spend`; month over month = `kpis.mom_pct`; COH recommendations, deduplicated per month = `kpis.coh_dedup`; data period = `period` rows (`source`, `first`, `last`, falling back to `months`), COT and CUR shown separately.
  - Cards: business signals = `tag_signals` + `cur_tag_signals` (`tag_key`, `tag_value`, `kinds`), top 3; session proposals = `sessions` with `origin === 'generated'` sorted by `rank` (`type`, `title`), top 3; top actions = `briefing` sorted by `rank` (`title`, `impact`, `owner`), top 3.
  - Links: card links open `/billing?acct=<id>` (dashboard tabs via hash only if the dashboard supports it); the COT/CUR upload button → `/billing?view=upload&acct=<id>` shown only with `canUpload`; the open-full-dashboard button → `/billing?acct=<id>`.
  - States: loading, error (retry button), no data (upload prompt when `canUpload`, plain note otherwise), CUR-only (KPI tiles that need COT show `—`).
  - Footnote: visible only to members of this Account (Korean copy in the dispatch).

- [ ] **Step 1: Failing tests:** `hidden_without_flag`, `hidden_for_non_member`, `fetches_with_acct_param_only_when_open`, `late_response_for_previous_account_ignored`, `missing_kpi_renders_dash`, `cur_only_account_state`, `no_data_shows_upload_prompt_only_with_can_upload`, `top_three_sorted_by_rank`, `links_encode_account_id`.
- [ ] **Step 2: Run** `cd frontend && npm run test:unit` → FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `npm run test:unit && npm run lint && npm run build` → PASS.
- [ ] **Step 5: Commit** `feat(web): Billing tab on the Account detail page`.

### Release order

Task 1 merges and deploys (`deploy-billing.yml`) before Task 4 merges. Tasks 2 and 3 can merge independently. Frontend changes publish with `scripts/publish-frontend.sh` after the owner's OK.
