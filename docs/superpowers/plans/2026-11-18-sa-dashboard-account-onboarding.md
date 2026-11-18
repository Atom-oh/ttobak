# SA Dashboard Account Onboarding Implementation Plan

- Status: historical implementation plan (2026-11-18).

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** From `/performance`, the owner creates a missing TTOBAK Account for an assigned SFDC account and invites that account's SFDC team (AM, TAM, SA) with mapped roles.

**Architecture:** A third owner-private performance source, `sfdc-account-team`, reuses the revisioned batch protocol and stores `PERFTEAM#` rows. A new read endpoint joins the stored team with the mapped Account's current and pending members. The Mac adapter collects `get_team_members` in a step after the SFDC sync; the web page reuses the existing create and add-member APIs.

**Tech Stack:** Go 1.27 (chi, DynamoDB expression builder), TypeScript adapter (`mcp-server`, node:test), Next.js/React (`frontend`, node:test on `scripts/*.test.mjs`).

**Spec:** `docs/superpowers/specs/2026-11-18-sa-dashboard-account-onboarding-design.md`

## Global Constraints

- Stored team fields: alias, name, title, `teamMemberRole`, `allTeamRoles`. Never access levels, user IDs, created dates, email or phone. Email is derived as `alias@amazon.com`, never stored.
- At most 100 team members per SFDC account are stored; the rest are counted.
- At most 20 invitations per click.
- Owner-private: every read and write scopes to the caller's own `USER#` partition. Nothing here is visible to Account members.
- No automatic invitations; no Owner grant through this flow; no SFDC writes.
- Role mapping: `teamMemberRole` "Account Manager" AND `allTeamRoles` contains "Account Manager" → AM; "Technical Account Manager" → TAM; "Solutions Architect" → SA; "Enterprise Support Manager" → TAM; anything else → no role ("other").
- The owner's own row (alias equal to the owner's SFDC alias) is skipped by the adapter.
- Reuse `POST /api/accounts` (with `requestId`), `PUT /api/performance/mappings/{sfdcAccountId}` and `AccountService.AddMember`; do not duplicate their validation.
- Repository text in concise English; UI copy in Korean, literal UTF-8.
- Each task is one PR on its own branch, merged in order T1 → T2 → T3.

## Review Focus

1. **Alias with a dot or mixed case** (`kim.lee`, `DHLee`): stored lower-case, its remove ID round-trips, and the member match against `dhlee@amazon.com` is case-insensitive. Pinned in Task 1 (`TestTeamExternalIDRoundTrip`, `TestOnboardingStatusCaseInsensitive`) and Task 2 (`team rows lower-case aliases`).
2. **SFDC account dropped from assignments**: its team rows are removed in the same run and the server never reports a team for an unassigned SFDC account. Pinned in Task 1 (`TestOnboardingUnassignedNotFound`) and Task 2 (`account leaving the snapshot removes its members`).
3. **`get_team_members` missing or failing for one account**: the SFDC sync still succeeds; the team step reports a failure and sends nothing partial for that account. Pinned in Task 2 (`team step failure keeps sync ok`, `incomplete team read skips the account`).
4. **Mapped Account the owner cannot read or is not a direct member of**: the endpoint reports `accountReadable:false` with every status `unknown`, never another Account's members. Pinned in Task 1 (`TestOnboardingUnreadableAccount`).
5. **More than 20 selected or a partial invite failure**: the client sends only the first 20 and shows per-person results, continuing after one failure. Pinned in Task 3 (`invite plan caps at 20`, `inviteResultText maps 409 and errors`).

---

### Task 1: Backend source, storage and onboarding read

**Files:**
- Modify: `backend/internal/model/performance.go`, `backend/internal/model/account.go`
- Modify: `backend/internal/repository/performance.go`
- Modify: `backend/internal/service/performance.go`; Create: `backend/internal/service/performance_onboarding.go`
- Modify: `backend/internal/handler/performance.go`, `backend/cmd/api/main.go`
- Test: `backend/internal/model/account_test.go`, `backend/internal/repository/performance_test.go`, `backend/internal/service/performance_onboarding_test.go`, `backend/internal/handler/performance_test.go`
- Docs: `docs/API-SPEC.md`, `docs/decisions/ADR-023-sa-performance-data-residency.md` (new residency row), `CHANGELOG.md` (Unreleased)

**Interfaces:**
- Produces:
  - `model.PerformanceSourceSFDCAccountTeam = "sfdc-account-team"`, `model.PrefixPerformanceTeam = "PERFTEAM#"`, `model.PerformanceTeamSortKey(sfdcAccountID, alias string) string` → `PERFTEAM#{sfdcAccountId}#{alias}`.
  - `model.SFDCTeamMember{SFDCAccountID, Alias, Name, Title, TeamMemberRole, AllTeamRoles string}` (JSON `sfdcAccountId`, `alias`, `name`, `title`, `teamMemberRole`, `allTeamRoles`); `PerformanceMutation.TeamMember *SFDCTeamMember` (JSON `teamMember`).
  - Remove external ID for this source: `{sfdcAccountId}:{alias}`; pattern `^[A-Za-z0-9_-]{1,64}:[a-z0-9._-]{1,64}$`.
  - `model.MapSFDCTeamRole(teamMemberRole, allTeamRoles string) string` → `RoleAM`, `RoleTAM`, `RoleSA` or `""`. `allTeamRoles` is split on `;` and `,`, trimmed, exact-match compared.
  - `PerformanceStore.ListPerformanceTeam(ctx, userID, sfdcAccountID string) ([]model.SFDCTeamMember, error)` (at most 101 rows, consistent read).
  - `model.OnboardingMember{SFDCTeamMember; Role string; Status string}` where Status is `member`, `pending`, `none` or `unknown`.
  - `model.OnboardingDetail{SFDCAccountID, Name, AccountID string; Mapped, AccountReadable, Truncated bool; Members []OnboardingMember}`.
  - `model.OnboardingSummaryItem{SFDCAccountID, AccountID string; Roles map[string]string}` (role → `member`/`pending`/`missing`; a role absent from the SFDC team is omitted), `model.OnboardingSummary{Items []OnboardingSummaryItem; Truncated bool}`.
  - `func (svc *PerformanceService) GetOnboarding(ctx, userID, sfdcAccountID string) (*model.OnboardingDetail, error)` and `ListOnboarding(ctx, userID string) (*model.OnboardingSummary, error)`.
  - Routes: `GET /api/performance/onboarding`, `GET /api/performance/onboarding/{sfdcAccountId}` and `POST /api/performance/onboarding/{sfdcAccountId}/invites`.
  - `model.OnboardingInviteRequest{Items []OnboardingInviteItem{Alias, Role}}` (1 to 20 items, alias in the stored team, role in `AssignableRoles`); `model.OnboardingInviteResponse{Results []OnboardingInviteResult{Alias, Outcome, Code}}` with Outcome `added`, `invited`, `already_member` or `failed`.
  - Pending status comes from owner-private `PERFINVITE#{sfdcAccountId}#{alias}` rows written on an `invited` outcome (30-day TTL, matching pending-invite expiry), because the platform has no pending-invite list.

- [ ] **Step 1: Write failing tests.**
  - `model`: `TestMapSFDCTeamRole` table with real strings: ("Account Manager","Account Manager")→AM; ("Account Manager","Migration BDM")→""; ("Technical Account Manager","Technical Account Manager")→TAM; ("Solutions Architect","Solutions Architect;Other")→SA; ("Enterprise Support Manager","")→TAM; ("Inside Sales Rep","Inside Sales Rep")→""; ("Business Development Manager","Other")→"".
  - `repository`: `TestTeamExternalIDRoundTrip` (upsert `kim.lee` then remove `001A:kim.lee` deletes `PERFTEAM#001A#kim.lee`); `TestTeamUpsertRejectsExtraFields` (alias with upper case or `@` rejected); team batch shares the revision/receipt path (`TestTeamBatchReplay`).
  - `service`: `TestTeamSyncCapPerAccount` (a batch raising one SFDC account to 101 rows → `ErrInvalidInput`; updates pass); `TestOnboardingStatusCaseInsensitive` (member email `DHLee@Amazon.com` → `member`; pending DTO → `pending`; absent → `none`); `TestOnboardingUnassignedNotFound` (sfdc ID not in assignments → `ErrNotFound`); `TestOnboardingUnreadableAccount` (GetAccount returns `ErrForbidden` → `AccountReadable:false`, all `unknown`); `TestOnboardingUnmapped` (no mapping → `Mapped:false`, statuses `none`); `TestListOnboardingSummary` (AM member, TAM pending, SA missing; roles without SFDC people omitted; at most 100 mapped accounts read, `Truncated` set beyond).
  - `handler`: 200 shapes for both routes; 404 for unknown SFDC ID; `sfdc-account-team` accepted by `POST /api/performance/sources/{source}/sync`; `ListSources` includes the new source.
- [ ] **Step 2: Run** `(cd backend && /usr/local/go/bin/go test ./internal/... -count=1)` — expect the new tests to FAIL.
- [ ] **Step 3: Implement.** Add the source everywhere `validPerformanceSourceName`, `validPerformanceSource` and `performanceSources` list sources. Normalize team upserts in the service (alias lower-cased; name/title ≤ 200 bytes; roles ≤ 64 bytes via `validPerformanceText`). Cap check counts rows per affected SFDC account with `ListPerformanceTeam`, mirroring `checkAssignmentCap`. `GetOnboarding` requires the SFDC ID in `ListPerformanceAssignments`, reads the mapping via `GetAccountMappings`, and calls `svc.accounts.GetAccount` only for a mapped account; it matches members by `strings.EqualFold(email, alias+"@amazon.com")`, never returns emails, and maps `ErrForbidden`/`ErrNotFound` to `AccountReadable:false`. Members are sorted core roles first (AM, TAM, SA), then alias.
- [ ] **Step 4: Run** the Step 2 command plus `go vet ./...` and the arm64 API build from `backend/CLAUDE.md` — expect PASS.
- [ ] **Step 5: Docs.** API-SPEC entries for both routes and the new sync source; ADR-023 residency row (SFDC account team: alias, name, title, `teamMemberRole`, `allTeamRoles`, owner-private, refreshed by each complete sync, removed with the assignment by the adapter's snapshot); CHANGELOG Unreleased. Run `python3 scripts/docs/check_docs.py`.
- [ ] **Step 6: Commit** `feat(performance): SFDC account team source and onboarding read`.

### Task 2: Adapter team step

**Files:**
- Create: `mcp-server/src/intake/sfdc-team.ts`
- Modify: `mcp-server/src/intake/sfdc.ts` (export state helpers and `deliver`/`send` for reuse; `PerformanceSource` union), `mcp-server/src/api.ts`, `mcp-server/src/intake/run.ts`
- Test: `mcp-server/test/intake-sfdc-team.test.mjs`
- Rebuild the bundled adapter the way the repo builds `frontend/public/mcp/ttobak-mcp.mjs` (follow `mcp-server/package.json` scripts).

**Interfaces:**
- Consumes: Task 1 source name, mutation shape, remove ID format and 100-member cap.
- Produces: `SFDC_TEAM_TOOL: ToolExpectation = { name: 'get_team_members', properties: ['memberType', 'id', 'limit', 'after'] }`; `parseTeamMembers(payload, sfdcAccountId, ownerAlias): { members: SFDCTeamMember[]; skippedCap: number }`; `runSfdcTeam(options): Promise<{ result: IntakeCode | 'ok' | 'disabled'; counts: SfdcTeamCounts }>`; run output key `sfdcAccountTeam` (status entry with `lastRunAt`, `lastSuccessAt`, `result`, `counts` only); CLI flag `--sfdc-account-team on|off` on `intake options` (default on).

- [ ] **Step 1: Write failing tests** with a fake session: `parse keeps only stored fields` (input has `email`, `userId`, `accountAccessLevel`, `createdDate`; output keys exactly the five plus `sfdcAccountId`); `team rows lower-case aliases`; `owner row skipped`; `cap keeps core roles first and counts the rest` (120 members → 100 stored, `skippedCap` 20, all AM/TAM/SA kept); `pages follow cursor`; `incomplete team read skips the account` (short page → no mutation for that account, others sent, result `PARTIAL_FAILURE`); `account leaving the snapshot removes its members`; `team step failure keeps sync ok` (tool missing → SFDC run `ok`, `sfdcAccountTeam.result` `SCHEMA_CHANGED`); `disabled option skips`; `dry run sends nothing`.
- [ ] **Step 2: Run** `(cd mcp-server && npm test)` — expect FAIL.
- [ ] **Step 3: Implement.** The step runs after `runSfdc` succeeds, on the same session, over the assignment IDs it just planned, with the same revisioned ledger/pending machinery as `sfdc.ts` (state file `sfdc-account-team.json`). An account whose read failed keeps its previous ledger entries (no removals for it). Budget: no new account starts after 8 minutes, like `SFDC_CONTACTS_BUDGET_MS`.
- [ ] **Step 4: Run** `(cd mcp-server && npm test)` — expect PASS.
- [ ] **Step 5: Commit** `feat(mcp): collect the SFDC account team after the SFDC sync`.

### Task 3: Web onboarding UI

**Files:**
- Create: `frontend/src/lib/onboarding.ts`, `frontend/src/components/performance/OnboardingPanel.tsx`
- Modify: `frontend/src/lib/api.ts` (`performanceApi.getOnboarding`, `listOnboarding`), `frontend/src/components/performance/MappingQueue.tsx` (a "create customer" button on unmapped rows), `frontend/src/app/performance/page.tsx` (onboarding section)
- Test: `frontend/scripts/onboarding.test.mjs`
- Docs: `docs/DESIGN-SPEC.md` (performance page section), `docs/user-guide/index.html` if it documents `/performance`

**Interfaces:**
- Consumes: Task 1 response shapes; existing `accountApi.create`, `accountCreationAttempt`, `accountApi.addMember`, `performanceApi.putMapping`.
- Produces (pure, in `lib/onboarding.ts`): `invitePlan(members: OnboardingMember[], selected: Set<string>): { alias: string; email: string; role: string }[]` (selected, status `none`, mapped role non-empty or chosen, first 20); `defaultSelection(members): Set<string>` (core roles with status `none`); `summaryText(roles: Record<string,string>): string` (e.g. AM done, TAM pending, SA not invited, as Korean UI copy); `inviteResultText(outcome): string`.

- [ ] **Step 1: Write failing tests:** `invite plan caps at 20`; `invite plan skips members and pending`; `default selection is core roles only`; `summaryText orders AM, TAM, SA and omits absent roles`; `inviteResultText maps 409 and errors` (invited, already a member, failed with a reason; Korean UI copy).
- [ ] **Step 2: Run** `(cd frontend && npm run test:unit)` — expect FAIL.
- [ ] **Step 3: Implement.** a "create customer" button creates with the SFDC name through `accountApi.create` and `accountCreationAttempt` (retry replays the same `requestId`), then `putMapping`, then opens `OnboardingPanel` for that SFDC account. The panel lists members with role and status, a "show all" toggle reveals `other` members unchecked, the invite button sends `invitePlan` to `performanceApi.inviteOnboarding` and shows per-person results, then refetches. The onboarding section lists `listOnboarding` items with `summaryText` and an "invite team" button. Invites go through `performanceApi.inviteOnboarding`. Statuses `unknown` show a notice that the Account members cannot be read and disable invites.
- [ ] **Step 4: Run** `(cd frontend && npm run lint && npm run test:unit && npm run build)` — expect PASS.
- [ ] **Step 5: Commit** `feat(web): create and onboard SFDC accounts from the performance page`.
