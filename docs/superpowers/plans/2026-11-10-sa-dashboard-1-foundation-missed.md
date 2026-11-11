# SA Dashboard Plan 1: Foundation and "What I Missed" (Section B)

Status: historical implementation plan (2026-11-10), plan 1 of 3 for the SA
performance dashboard. Plans 2 (scorecard, section A) and 3 (customer map,
section C) follow separately.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship an owner-only `/performance` page. It lists SFDC logging gaps and
silent assigned customers, keeps the owner's yearly goals, and shows an
SFDC-to-Account mapping queue. The data comes from a new Mac intake connector
for SFDC.

**Architecture:**
- The Mac runner `ttobak-mcp intake sfdc` reads `aws-sentral-mcp` and posts
  revisioned, journaled batches to `POST /api/performance/sources/{source}/sync`.
  It copies the calendar connector's protocol.
- Go stores owner-private `USER#{id}` / `PERF*#` rows.
- Pure rule functions compute missed items from those rows, the owner's
  calendar events (extended with an organizer domain) and the owner's meetings.

**Tech Stack:**
- Go 1.27 chi API with the DynamoDB expression builder
- TypeScript intake runner (`node:test`)
- Next.js 16 / React 19 page (`node --test` scripts)
- CDK `knownPages`

**Spec:** `docs/superpowers/specs/2026-11-10-sa-performance-dashboard-design.md`

## Global Constraints

- **Stored fields:**
  - SFDC Tasks: ID, date, account, type/category, subject, status, linked
    SpecReq ID. Description and comments are never sent.
  - Calendar: title, start/end, organizer domain. Body, notes and join links
    are never sent.
- **Visibility:**
  - The page and every `/api/performance/*` route are owner-only (the JWT
    user's own `USER#` partition).
  - No account-membership, admin-UI or Billing read path reaches these rows.
  - There is no share action.
- **Calls and routes:**
  - Internal MCP servers are called only by the Mac runner. The server never
    calls them.
  - No new unauthenticated route. No new HTTP MCP write tool.
- **Thresholds:** silent customer 60 days, logging-gap window ±3 days. Plan 1
  uses them as fixed defaults; plan 2 makes them user-tunable.
- **Retention:** keep the current year plus the previous one. Rows carry
  `pendingShareExpiresAt` set to 00:00 UTC on January 1 of the year two years
  after the row's own year (`year+2`).
- **Encryption:** default table encryption plus owner-only authorization (the
  owner's decision). There is no envelope sealing for `PERF*` rows. ADR-068
  records this as an accepted exception.
- **Unmapped data:** unmapped items stay unmapped and are counted, never
  guessed. A manual mapping always wins over an automatic one.
- **Language:** repository text is English. UI copy is Korean, like the rest of
  the UI.
- **Verification commands:** as in `CLAUDE.md`. Go tests include `cmd/*`.

## Review Focus

1. **Another user reads the data.** An account co-member, or any second user,
   calls `GET /api/performance/missed` or the goals and mappings routes and gets
   empty or own-only data, never the owner's rows. Tests: Task 3
   `TestPerformanceRoutesAreOwnerScoped`.
2. **A payload carries a body or link.** A connector batch containing
   `description`, `body`, `comments` or a join URL is rejected with 400 by
   `DisallowUnknownFields`, and the runner never builds such fields. Tests:
   Task 3 `TestSyncRejectsUnknownFields`, Task 5
   `sfdc tasks never carry description`.
3. **A batch is replayed or arrives late.** Replaying an identical batch is
   idempotent. A different payload under the same `batchId`, or a stale
   `expectedRevision`, returns 409. Tests: Task 2
   `TestApplyPerformanceBatchRevisionCAS`, Task 3 `TestSyncReplay`.
4. **Account names are ambiguous.** Two Accounts match one SFDC account name, so
   the SFDC account stays unmapped and appears in the queue. A manual mapping
   survives later automatic runs. Tests: Task 6
   `TestMatchSFDCAccountsAmbiguousStaysUnmapped`, `TestManualMappingWins`.
5. **Midway has expired.** The runner exits with `AUTH_EXPIRED_MIDWAY`. It
   does not post a partial snapshot, and the pending batch is kept for an
   identical retry. Tests: Task 5 `sfdc auth expiry keeps no partial snapshot`.

---

### Task 1: ADR-068 and spec correction

**Files:**
- Create: `docs/decisions/ADR-068-sa-performance-data-residency.md`
- Modify: `docs/README.md` (ADR index, near the other ADR-06x rows)
- Modify: `docs/superpowers/specs/2026-11-10-sa-performance-dashboard-design.md`

**Interfaces:** Produces the residency rules that Tasks 2–8 cite.

- [ ] **Step 1: Write ADR-068.**
  - Use the existing ADR format: `# ADR-068: SA performance data residency`, a
    bullet header (Status Accepted, Decision date 2026-11-10, Design = the spec,
    Related = ADR-003 local MCP intake and ADR-021 account people edges by
    title), then Context, Decision and Consequences.
  - The Decision section records:
    - the spec's residency table, as owner-private and separate from the
      account-shared limit (which stays opportunity ID, name and stage);
    - default table encryption as an accepted exception to the
      sensitive-data KMS rule, scoped to `PERF*` rows;
    - acceptance that private calendar items are included;
    - the retention rule;
    - that `calendar_view` exposes no attendee list, so a calendar event's
      counterpart is its organizer's domain only.
- [ ] **Step 2: Amend the spec.**
  - In the Touchpoint bullet, replace "counterpart domains, an organizer/self
    flag and the response state" with "the organizer's email domain (the source
    has no attendee list)".
  - State that a calendar event maps to an account by organizer domain, or by a
    unique title match on the account name or aliases.
- [ ] **Step 3: Check the docs and commit.** Run
  `python3 scripts/docs/check_docs.py`. Expected: no new errors. The ADR-003
  broken link that already exists on main is the only error left.
  `git commit -m "docs: ADR-068 SA performance data residency"`

### Task 2: Performance row model and repository

**Files:**
- Create: `backend/internal/model/performance.go`
- Create: `backend/internal/repository/performance.go`
- Test: `backend/internal/repository/performance_test.go`

**Interfaces:**
- Produces model types and key builders:
  - PK: `USER#{userID}`.
  - SK builders: `PerformanceSourceSortKey(source) = "PERFSRC#"+source`,
    `PerformanceBatchSortKey(source, batchID) = "PERFBATCH#"+source+"#"+batchID`,
    `PerformanceAssignmentSortKey(sfdcAccountID) = "PERFASSIGN#"+id`,
    `PerformanceTaskSortKey(taskID) = "PERFTASK#"+id`,
    `PerformanceMappingSortKey(sfdcAccountID) = "PERFMAP#"+id`,
    `PerformanceGoalsSortKey(year int) = fmt.Sprintf("PERFGOAL#%04d", year)`.
  - Sources: `PerformanceSourceSFDCAssignments = "sfdc-assignments"` and
    `PerformanceSourceSFDCTasks = "sfdc-tasks"`. Only these two are valid.
  - `SFDCAssignment{SFDCAccountID, Name, Role string}`.
  - `SFDCTask{TaskID, SFDCAccountID, ActivityDate (YYYY-MM-DD), Type, Subject, Status, SpecReqID string}`.
  - `PerformanceMutation{Op "upsert"|"remove"; Assignment *SFDCAssignment; Task *SFDCTask; ExternalID string}`.
  - `PerformanceSyncRequest{BatchID string; ExpectedRevision int64; Complete bool; Mutations []PerformanceMutation}`.
  - `PerformanceSyncReceipt{Revision int64; Completed bool; Upserted, Removed int}`.
  - `AccountMapping{SFDCAccountID, AccountID string; Manual bool; UpdatedAt string}`.
  - `PerformanceGoals{Year int; Text string (≤ 8,000 UTF-8 bytes); UpdatedAt string}`.
- Produces repository methods on `*DynamoDBRepository`:
  - `ApplyPerformanceBatch(ctx, userID, source string, req model.PerformanceSyncRequest, digest string) (*model.PerformanceSyncReceipt, error)`
  - `ListPerformanceTasks(ctx, userID string, from, to string) ([]model.SFDCTask, error)`
  - `ListPerformanceAssignments(ctx, userID) ([]model.SFDCAssignment, error)`
  - `ListAccountMappings(ctx, userID) ([]model.AccountMapping, error)`
  - `PutAccountMapping(ctx, userID string, m model.AccountMapping, onlyIfNotManual bool) error`
  - `GetPerformanceGoals(ctx, userID string, year int) (*model.PerformanceGoals, error)`
  - `PutPerformanceGoals(ctx, userID string, g model.PerformanceGoals) error`

- [ ] **Step 1: Write failing tests.** Follow the calendar repository's mock
  client pattern in `repository/calendar_test.go`.
  - `TestApplyPerformanceBatchRevisionCAS`: the transaction contains a source
    update conditioned on `revision = :expected`, or `attribute_not_exists(PK)`
    when the expected revision is 0. A condition failure maps to
    `ErrConditionFailed`.
  - `TestApplyPerformanceBatchReceiptReplay`: the batch receipt put uses
    `attribute_not_exists(PK)`. A stored receipt with the same digest returns
    the saved receipt.
  - `TestPerformanceTaskTTL`: a task dated `2026-03-04` gets
    `pendingShareExpiresAt` = Unix time of `2028-01-01T00:00:00Z`.
  - `TestPutAccountMappingKeepsManual`: with `onlyIfNotManual=true` the put is
    conditioned on `attribute_not_exists(PK) OR manual = :false`.
- [ ] **Step 2: Run the tests and see them fail.**
  `(cd backend && /usr/local/go/bin/go test ./internal/repository -run Performance -count=1)`.
  Expected: FAIL (undefined).
- [ ] **Step 3: Implement.**
  - Model the batch after `ApplyCalendarBatch`: one `TransactWriteItems` call
    with `NopRetryer`, at most 20 mutations plus the source and receipt items.
  - Remove `remove` mutations by sort key.
  - Receipts expire after 7 days, as in calendar.
  - List queries use `begins_with(SK, prefix)` and paginate fully. Task range
    filtering happens on `activityDate` after the query.
- [ ] **Step 4: Run the tests and see them pass.** Same command. Expected: PASS.
- [ ] **Step 5: Commit.** `feat(performance): owner-private SFDC rows and revisioned batches`

### Task 3: Performance service, handlers and routes

**Files:**
- Create: `backend/internal/service/performance.go`
- Create: `backend/internal/handler/performance.go`
- Modify: `backend/cmd/api/main.go` (wiring and routes next to the calendar routes)
- Modify: `contracts/connector/v1/operations.json`, then regenerate
- Modify: `docs/API-SPEC.md`
- Test: `backend/internal/service/performance_test.go`, `backend/internal/handler/performance_test.go`

**Interfaces:**
- Consumes the Task 2 repository methods through
  `type PerformanceStore interface{...}`, which lists them.
- Produces these routes. All take `userID` only from the verified JWT.

  | Route | Behavior |
  |---|---|
  | `POST /api/performance/sources/{source}/sync` | 64 KiB body, `DisallowUnknownFields`. Returns `PerformanceSyncReceipt`. |
  | `GET /api/performance/sources` | Per-source revision and last completed time. |
  | `GET /api/performance/goals?year=YYYY` / `PUT` same path | Body `{text}`. |
  | `GET /api/performance/mappings` | Assignments, each with its mapping and `mapped:false` when unmapped. |
  | `PUT /api/performance/mappings/{sfdcAccountId}` | Body `{accountId}`. The caller must be a current member of that account (`AccountService` read check); otherwise 403. Writes `manual:true`. |
  | `GET /api/performance/missed?period=week|quarter|year` | Filled in by Task 7. Until then it returns `{"items":[]}`. |

- Connector rows (six columns):
  - `["api.performance.sync","ttobak-api-bridge",["delete","read","write"],false,"POST","/api/performance/sources/{sourceId}/sync"]`
  - read-only rows for the GET routes;
  - a write row for the PUTs.
  - The route parameter name must match `main.go`.

- [ ] **Step 1: Write failing tests.**
  - `TestSyncRejectsUnknownFields`: a task carrying `"description"` → 400.
  - `TestSyncRejectsUnknownSource`: `source=mail` → 400.
  - `TestSyncReplay`: the same batch twice returns the same receipt. The same
    `batchId` with a different digest → 409 `CONFLICT`.
  - `TestPerformanceRoutesAreOwnerScoped`: user B gets empty data and never
    user A's rows. Mapping to an account B is not a member of → 403.
  - `TestGoalsTooLarge`: 8,001 bytes → 400.
- [ ] **Step 2: Run the tests and see them fail.**
  `(cd backend && /usr/local/go/bin/go test ./internal/service ./internal/handler -run Performance -count=1)`.
- [ ] **Step 3: Implement.**
  - The digest is sha256 over the canonical JSON of the request, as in calendar.
  - Validate the fields:
    - `ActivityDate` is a real date;
    - IDs are 1–64 characters of `[A-Za-z0-9_-]`;
    - `Subject` is at most 300 bytes and `Name` at most 200;
    - an assignment mutation is valid only for `sfdc-assignments`, and a task
      mutation only for `sfdc-tasks`.
  - Errors use the `{error:{code,message}}` shape, with sentinel errors and
    `errors.Is`.
- [ ] **Step 4: Regenerate the connector contract and check it.**
  - Run `python3 scripts/connector/generate_contract.py`, then the same with
    `--check`.
  - Run `(cd backend && /usr/local/go/bin/go test ./... -count=1)`. Expected:
    PASS, including `TestInventoryCoversCurrentHTTPRoutes`.
- [ ] **Step 5: Document and commit.** Add a Performance section to
  `docs/API-SPEC.md` that states owner-only access and the residency rules.
  `feat(performance): owner-only sync, goals and mapping API`

### Task 4: Calendar organizer domain

**Files:**
- Modify: `mcp-server/src/intake/calendar.ts` (`OutlookItem.organizer?: {email?: string}`, `toEvent`)
- Modify: `mcp-server/src/calendar.ts` (`CalendarEvent.organizerDomain?`, `validateCalendarSync`)
- Modify: `backend/internal/model/calendar.go`, `backend/internal/service/calendar.go` (validation; sealed payload carries the field)
- Modify: `mcp-server/test/fixtures/intake-outlook-server.mjs`, `docs/API-SPEC.md`
- Test: `mcp-server/test/intake.test.mjs`, `backend/internal/service/calendar_test.go`

**Interfaces:** Produces `CalendarEvent.organizerDomain?: string`. It is the
lowercase domain after the last `@`, matching `^[a-z0-9.-]{1,253}$`; anything
else is omitted. Task 7 reads it from `GET /api/calendar/events`.

- [ ] **Step 1: Write failing tests.**
  - intake test `calendar sends organizer domain only`: an organizer
    `Alice@Example.COM` gives `organizerDomain:"example.com"`. The serialized
    batch contains neither `alice` nor `@`.
  - Go test `TestCalendarSyncRejectsBadOrganizerDomain`: `"a b"` → 400.
- [ ] **Step 2: Run the tests and see them fail.**
  `(cd mcp-server && npm test)` and
  `(cd backend && /usr/local/go/bin/go test ./internal/service -run Calendar -count=1)`.
- [ ] **Step 3: Implement.**
  - The domain joins the event fingerprint, so a changed organizer re-upserts
    the event.
  - Correction (final review I1): older readers do not ignore the new field.
    The sealed plaintext was decoded strictly, so an older API fails to read
    newly sealed events. The sealed decoder now tolerates unknown fields; the
    API must be deployed before the runner sends `organizerDomain`, and an API
    rollback below this release breaks reading newly sealed events until a
    re-sync.
- [ ] **Step 4: Run the tests and see them pass.** Same commands. Expected: PASS.
- [ ] **Step 5: Commit.** `feat(calendar): send the organizer's email domain only`

### Task 5: Mac intake connector `intake sfdc`

**Files:**
- Create: `mcp-server/src/intake/sfdc.ts`
- Modify: `mcp-server/src/intake/run.ts` (USAGE, dispatch, `select --connector sfdc`, `Selection.sfdc`, `RunStatus.sfdc`)
- Modify: `mcp-server/src/intake/errors.ts` (no new codes expected; add only if a test needs one)
- Modify: `mcp-server/src/api.ts` (`syncPerformance(source, body)` → `POST /api/performance/sources/{source}/sync`)
- Create: `mcp-server/test/fixtures/intake-sentral-server.mjs` (`FAKE_SENTRAL_MODE=ok|auth|schema`)
- Test: `mcp-server/test/intake.test.mjs`
- Modify: `docs/runbooks/local-outlook-calendar-sync.md` (add an "SFDC connector" section) or create `docs/runbooks/local-sfdc-intake.md`

**Interfaces:**
- Consumes `McpSession`, `resultJson`, `IntakeStore`, `IntakeError`,
  `resolveServer` and `accountScope` (see `mcp-server/src/intake/`).
- Produces:
  - `SFDC_TOOLS`, with `ToolExpectation`s for `get_my_personal_details`,
    `get_registry_assignments` and `search_tasks`;
  - `parseAssignments(payload: unknown): SFDCAssignment[]`;
  - `parseTasks(payload: unknown): SFDCTask[]`, which copies only the Global
    Constraints fields;
  - `runSfdc(options: SfdcRunOptions): Promise<SfdcRunResult>`.
- `SfdcRunOptions` mirrors `CalendarRunOptions`: `{store, api?, source, sourceId, scope?, now, dryRun, reconcile?}`.
- The tasks query covers January 1 of the previous year through `now`, with
  `ownerId` = the ID from `get_my_personal_details`.

- [ ] **Step 1: Record the real result shapes.**
  - On the owner's Mac, call each of the three tools once with the session's
    MCP access.
  - Write a minimal fixture with field names copied from the real output and
    synthetic values only. Commit no real IDs or names.
- [ ] **Step 2: Write failing tests.** Use the canary and fixture pattern in
  `intake.test.mjs`.
  - `sfdc dry run reports counts`
  - `sfdc tasks never carry description`: the fixture puts
    `SECRET-CANARY-7f3a9c` in description and comments; neither the batch nor
    the state file contains it.
  - `sfdc snapshot removes vanished assignments`
  - `sfdc auth expiry keeps no partial snapshot`: no batch is posted, and the
    exit carries `AUTH_EXPIRED_MIDWAY`.
  - `sfdc schema change fails closed`
- [ ] **Step 3: Run the tests and see them fail.**
  `(cd mcp-server && npm test)`. Expected: FAIL.
- [ ] **Step 4: Implement.**
  - Reuse the calendar journaling, batching, receipt validation and
    409/reconcile handling.
  - Extract shared helpers from `calendar.ts` into `mcp-server/src/intake/batch.ts`
    only where both connectors call them.
  - Each source keeps its own ledger and revision state at
    `sfdc/<sourceId>/<scope>/<source>.json`.
- [ ] **Step 5: Run the tests and see them pass.** Same command. Expected: PASS.
- [ ] **Step 6: Commit.** `feat(intake): SFDC assignments and SA Activity connector`

### Task 6: SFDC-to-Account auto mapping

**Files:**
- Create: `backend/internal/service/performance_mapping.go`
- Test: `backend/internal/service/performance_mapping_test.go`
- Modify: `backend/internal/service/performance.go` (after a `sfdc-assignments` batch with `complete:true`, run auto mapping)

**Interfaces:**
- Consumes `NormalizePersonName` (`service/account_people_resolve.go`) for
  name folding, and the owner's readable accounts with names and aliases from
  `listReadableAccountIDs` plus account reads (`service/account_read_discovery.go`).
- Produces
  `MatchSFDCAccounts(assignments []model.SFDCAssignment, accounts []NamedCandidate) map[string]string`.
  It returns SFDC account ID → account ID for unique matches only.

- [ ] **Step 1: Write failing table tests.**
  - `TestMatchSFDCAccountsUniqueNameOrAlias`: "Acme Corp" matches an account
    whose alias is "acme corp".
  - `TestMatchSFDCAccountsAmbiguousStaysUnmapped`: two accounts match → no entry.
  - `TestManualMappingWins`: an existing manual mapping is not overwritten,
    because `onlyIfNotManual=true`.
- [ ] **Step 2: Run the tests and see them fail.**
  `(cd backend && /usr/local/go/bin/go test ./internal/service -run SFDCAccounts -count=1)`.
- [ ] **Step 3: Implement.**
  - Fold names with `NormalizePersonName` and compare them for equality. No
    fuzzy matching.
  - Write each match with `PutAccountMapping(..., onlyIfNotManual=true)`.
- [ ] **Step 4: Run the tests and see them pass.** Same command. Expected: PASS.
- [ ] **Step 5: Commit.** `feat(performance): unique-match SFDC account mapping`

### Task 7: Missed-item rules and endpoint

**Files:**
- Create: `backend/internal/service/performance_missed.go`
- Test: `backend/internal/service/performance_missed_test.go`
- Modify: `backend/internal/service/performance.go`, `backend/internal/handler/performance.go`

**Interfaces:**
- Consumes:
  - Task 2 lists and mappings;
  - calendar events from `CalendarService` (decrypted, owner only);
  - the owner's meetings that are linked to an account and that they own or
    that are shared to them, through the existing meeting list service.
- Produces:
  - `Period{From, To time.Time}`, built by `PeriodFor(kind string, now time.Time, loc *time.Location)`.
    `week` is ISO Monday–Sunday, `quarter` is the calendar quarter, `year` is
    January 1 through now. `loc` is `Asia/Seoul`.
  - `Touchpoint{AccountID, Kind ("calendar"|"meeting"|"sfdc"), RefID string; At time.Time}`.
  - `ComputeMissed(p Period, in MissedInputs, cfg MissedConfig) []MissedItem`,
    where `MissedConfig{SilentDays: 60, GapDays: 3}`.
  - `MissedItem{Kind ("logging_gap"|"silent_customer"), AccountID, SFDCAccountID, RefKind, RefID, At string; DaysSince int}`.
- The endpoint returns `{items, unmappedCount, sourceFreshness}`, with at most
  500 items, ordered newest first.

- [ ] **Step 1: Write failing table tests.**
  - `TestLoggingGapWhenNoTaskWithinWindow`: a calendar touchpoint on 03-10 with
    a task on 03-14 is a gap, because 4 days is more than 3.
  - `TestNoGapWhenTaskWithinWindow`: a task on 03-12 → no gap.
  - `TestSilentCustomerAfter60Days`: the last touchpoint was 61 days ago → silent;
    60 days ago → not silent.
  - `TestUnmappedNeverGuessed`: an event from an unmapped domain produces no
    item and increments `unmappedCount`.
  - `TestCalendarTouchpointByOrganizerDomainOrUniqueTitle`: the domain matches
    `Account.Domains`. A title containing exactly one account's name or alias
    matches. Two such accounts → no match.
  - `TestInternalOrganizerNeedsTitleMatch`: the organizer domain is
    `amazon.com`, so only a title match counts.
- [ ] **Step 2: Run the tests and see them fail.**
  `(cd backend && /usr/local/go/bin/go test ./internal/service -run 'Missed|Gap|Silent|Touchpoint' -count=1)`.
- [ ] **Step 3: Implement.**
  - Write pure functions first, then wire the service.
  - A logging gap is a calendar or meeting touchpoint on a mapped account with
    no SFDC task on the same SFDC account within ±`GapDays`.
  - A silent customer is an assigned, mapped account whose last touchpoint of
    any kind is older than `SilentDays` at `p.To`.
- [ ] **Step 4: Run all backend tests and checks.**
  - `(cd backend && /usr/local/go/bin/go test ./... -count=1 && /usr/local/go/bin/go vet ./...)`
  - The arm64 api build command from `CLAUDE.md`.
  - Expected: PASS.
- [ ] **Step 5: Commit.** `feat(performance): logging-gap and silent-customer rules`

### Task 8: `/performance` page (goals, missed list, mapping queue)

**Files:**
- Create: `frontend/src/app/performance/page.tsx`
- Create: `frontend/src/components/performance/MissedList.tsx`, `GoalsEditor.tsx`, `MappingQueue.tsx`
- Create: `frontend/src/lib/performance.ts` (pure helpers: period labels, item grouping)
- Modify: `frontend/src/lib/api.ts` (`performanceApi` with `missed`, `goals`, `putGoals`, `mappings`, `putMapping`, `sources`, following the `calendarApi` pattern)
- Modify: `frontend/src/lib/navigation.ts` (an "SA activity" entry, Korean label, in the work group), `frontend/src/components/layout/DesktopHeader.tsx` (`pathLabels`)
- Modify: `infra/lib/frontend-stack.ts` (`knownPages`: add `/performance`), `infra/test/frontend-stack.test.ts` (`['/performance','/performance.html']`)
- Modify: `docs/DESIGN-SPEC.md`
- Test: `frontend/scripts/performance.test.mjs`, `frontend/scripts/navigation.test.mjs`

**Interfaces:**
- Consumes the Task 3 and Task 7 routes.
- Produces the page layout. A top period selector (this week / quarter / this year)
  drives `missed`. Then come:
  1. a placeholder card for section A, holding the goals editor for the current
     year (Plan 2 fills in the scorecard);
  2. section B with two groups: SFDC logging gaps, and silent customers. Each item
     links to `/meeting/{id}` or `/accounts/{id}`;
  3. a mapping queue listing unmapped SFDC accounts, each with an account
     picker;
  4. source freshness, for example "SFDC last collected: …", with the intake
     command when a source has never run.

- [ ] **Step 1: Write failing tests.**
  - `performance.test.mjs`:
    - `groups missed items by kind newest first`
    - `period labels`
    - `freshness shows never-run hint`
  - Update `navigation.test.mjs` with the exact new group order.
- [ ] **Step 2: Run the tests and see them fail.**
  `(cd frontend && npm run test:unit)`. Expected: FAIL.
- [ ] **Step 3: Implement.**
  - Use the `insights/page.tsx` auth/spinner/`AppLayout` pattern and tokens
    from `globals.css`.
  - The page has no share control.
- [ ] **Step 4: Run the checks.**
  - `(cd frontend && npm run lint && npm run test:unit && npm run build)`
  - `(cd infra && npx cdk synth && npm test)`
  - `python3 scripts/docs/check_docs.py`
  - Expected: PASS.
- [ ] **Step 5: Commit.** `feat(frontend): owner-only SA activity page with missed items`

### Delivery

Open one PR per task group in stacked order, following
`docs/runbooks/pr-review.md`: Tasks 1–3, then 4–5, then 6–8.

Deploy order:
1. Backend (routes and readers).
2. Frontend.
3. Run the Mac connector:
   - `ttobak-mcp intake sfdc --dry-run`, then a real run;
   - re-run `intake calendar` to backfill organizer domains. This covers only
     the intake window (the past 7 days through the next 30), not the full
     year; older retained events keep no domain until they age out.
4. Manual check:
   - spot-check 5 logging gaps against SFDC;
   - confirm that a second test user sees nothing.
