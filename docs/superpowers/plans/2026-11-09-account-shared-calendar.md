# Account-Shared Calendar Implementation Plan

Status: historical implementation plan (2026-11-09), approved design, not yet
implemented. It is not a record of deployment.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Share Outlook events that match exactly one account with that
account's direct members, keep everything else private as candidates, and let
the `Private` Outlook category or any sharer's unshare hide a meeting.

**Architecture:** The Mac intake runner matches each event against a
server-provided directory of the user's direct-member accounts (title,
location, attendee domains) and sends only the result (account ID, evidence
kinds, candidates, private flag) with the existing private-calendar sync. The
Go service writes the private row as today plus, in the same transaction, a
per-sharer share row in the account partition keyed by a server-computed
`meetingKey`. Account members read a deduplicated view where any veto row for
a `meetingKey` hides the meeting.

**Tech Stack:** Go 1.27 (chi, DynamoDB expression builder, KMS), TypeScript
MCP adapter (`@modelcontextprotocol/sdk`), Next.js 16 / React 19 web,
`node:test`.

**Spec:** `docs/superpowers/specs/2026-11-09-account-shared-calendar-design.md`

## Global Constraints

- Prerequisite: PR #705 (Phase 1 runner) merged to `main`; rebase this branch on it.
- Visibility: direct account members only, every role. Ancestor readers and
  Billing never see shared events. Check with `GetMember(accountID, userID)`,
  never `GetAccount`.
- One veto hides the meeting: any sharer's `Private` category or unshare hides
  it from the account, including other sharers' copies.
- `Private` category match is case-insensitive, exact category name `private`.
- Never sent to TTOBAK: attendees, organizer, body, matched text.
- Locations are sanitized with the existing `sanitizeLocation` before sending.
- Sharing is a new field. Existing `accountId` keeps classifying only; old rows
  stay unshared.
- Manual sharing mode beats automatic results until cleared; it applies to the
  series (`seriesKey`) when present, else the occurrence.
- Shared events appear only on the web account page. QA, briefs, insights, KB,
  vault export and every MCP read tool exclude them.
- Unshare from TTOBAK is immediate for the clicked occurrence; other
  occurrences and category changes take effect at the next run (10 minutes).
- No new public route. All routes are authenticated with verified JWT user.
- Share rows: KMS-encrypted, TTL 90 days after event end (same as events).
- DynamoDB transactions stay at 100 items or fewer per call.
- Repository docs in English; no Korean in `.md` files.

## Review Focus

1. A sharer removed from the account: their share rows must stop appearing at
   read time, and new syncs with that account must get 403 for the share.
2. A page boundary between two sharers' rows of one `meetingKey`: the veto
   must still hide the meeting, never show it on one page.
3. An event whose title changes after being shared: the old `meetingKey` row
   must be deleted in the same transaction, never left behind.
4. A Hangul account name followed by a Hangul particle (for example the
   name plus the particle meaning "and") must match; a
   Latin alias inside a longer word (`HNB` in `HNBX`) must not.
5. `get_meeting` rejecting `calendar_view` IDs: the run must continue with
   title/location matching and report `attendeesUnavailable`, not fail.

Each line has a test in the owning task (Tasks 2, 2, 2, 5, 6).

---

## File map

| File | Responsibility |
|---|---|
| `backend/internal/model/calendar.go` | New input fields, share/sharing DTOs, key helpers |
| `backend/internal/repository/calendar_share.go` (new) | Share/veto/override rows, shared page query |
| `backend/internal/repository/calendar.go` | Batch transaction gains share operations |
| `backend/internal/service/calendar_share.go` (new) | `meetingKey`, share decision, shared read, sharing mode, directory |
| `backend/internal/service/calendar.go` | Sync validation and share wiring |
| `backend/internal/handler/calendar.go` | Three new routes |
| `backend/cmd/api/main.go` | Route registration, membership reader wiring |
| `mcp-server/src/calendar.ts`, `mcp-server/src/api.ts` | Sync schema fields, directory call |
| `mcp-server/src/intake/match.ts` (new) | Deterministic matcher |
| `mcp-server/src/intake/attendees.ts` (new) | `get_meeting` domain reader and cache |
| `mcp-server/src/intake/calendar.ts`, `run.ts` | Planner integration, selection flags |
| `frontend/src/types/calendar.ts`, `src/lib/api.ts`, `src/lib/calendarSharing.ts` (new) | Types, client, pure view helpers |
| `frontend/src/components/workspace/UpcomingMeetings.tsx` | Badges, assign/unshare actions |
| `frontend/src/components/account/AccountSchedule.tsx` (new), `src/app/accounts/[id]/page.tsx` | Account schedule section |
| Docs | ADR-066, API-SPEC, DESIGN-SPEC, architecture, runbook, ADR-065 status |

---

### Task 1: Sync contract fields and validation (Go model + service normalization)

**Files:**
- Modify: `backend/internal/model/calendar.go`
- Modify: `backend/internal/service/calendar.go` (`normalizeCalendarEvent`)
- Test: `backend/internal/service/calendar_share_test.go` (new)

**Interfaces:**
- Produces on `model.CalendarEventInput`:
  `ShareAccountID string \`json:"shareAccountId,omitempty"\``,
  `ShareEvidence []string \`json:"shareEvidence,omitempty"\`` (subset of `title`, `location`, `domain`, unique, sorted),
  `CandidateAccountIDs []string \`json:"candidateAccountIds,omitempty"\`` (≤5, unique, sorted, identifier regex),
  `Private bool \`json:"private,omitempty"\``,
  `SeriesKey string \`json:"seriesKey,omitempty"\`` (64 lowercase hex).
- Produces on `model.CalendarEvent` (owner read): `ShareAccountID`, `ShareEvidence`, `CandidateAccountIDs`, `Private`, `SharingMode string` (`auto`|`account`|`hidden`), `SeriesKey` omitted.

- [ ] **Step 1: Write failing table test `TestNormalizeCalendarEventSharing`** with cases:
  valid share (`shareAccountId:"acc_1"`, evidence `["title","domain"]`) → ok, evidence sorted;
  evidence without account → `ErrInvalidInput`; unknown evidence `"body"` → error;
  6 candidates → error; `private:true` with `shareAccountId` → error;
  `private:true` with candidates → error; `seriesKey:"ABC"` → error;
  share account equal to a candidate → error.
- [ ] **Step 2: Run** `cd backend && /usr/local/go/bin/go test ./internal/service -run TestNormalizeCalendarEventSharing -count=1` → FAIL (fields undefined).
- [ ] **Step 3: Add the fields and validate them in `normalizeCalendarEvent`.** The encoded payload limit stays 4096 bytes (`marshalCalendarPayload`); evidence, candidates, private and seriesKey go inside the encrypted payload; `ShareAccountID` is also copied to the row (Task 2).
- [ ] **Step 4: Run** the same command → PASS; then `/usr/local/go/bin/go test ./internal/... -count=1` → PASS (existing handler strict-JSON tests must still accept old payloads).
- [ ] **Step 5: Commit** `feat(calendar): sync fields for account sharing`.

### Task 2: Share storage (repository)

**Files:**
- Create: `backend/internal/repository/calendar_share.go`
- Modify: `backend/internal/repository/calendar.go` (`CalendarEventRow`, `CalendarWriteMutation`, `ApplyCalendarBatch`)
- Modify: `backend/internal/model/calendar.go` (prefixes, key helpers)
- Test: `backend/internal/repository/calendar_share_test.go`

**Interfaces:**
- Prefixes: `PrefixCalendarShare = "CALSHARE#"`, `PrefixCalendarOverride = "CALOVERRIDE#"`.
- `model.CalendarShareSortKey(startsAt time.Time, meetingKey, userID string) string` → `CALSHARE#{startsAt UTC, same format as CalendarTimeSortKey}#{meetingKey}#{userID}`; partition `ACCOUNT#{accountId}`.
- `model.CalendarOverrideSortKey(sourceID, scopeKey string) string` → `CALOVERRIDE#{sourceID}#{scopeKey}`; partition `USER#{userId}`.
- `CalendarEventRow` gains `ShareAccountID`, `MeetingKey`, `ShareState` (`""`|`shared`|`veto`), `SeriesKey`.
- `CalendarShareRow{PK, SK, AccountID, MeetingKey, UserID, SourceID, EventID, StartsAt, EndsAt, State ("shared"|"veto"), Ciphertext (empty for veto), SharerEmail, ExpiresAt}`.
- `CalendarWriteMutation` gains `Share *CalendarShareRow` (new row, nil when none) and `PreviousShareKey *ShareKey{AccountID, SK}` (delete when it differs from the new one, and always on `remove`).
- `(repo) ListCalendarShareRows(ctx, accountID string, query model.CalendarQuery) (*CalendarSharePage, error)`; `CalendarSharePage{Groups [][]CalendarShareRow, NextCursor *string}`.
- `(repo) GetCalendarOverride / PutCalendarOverride / DeleteCalendarOverride(ctx, userID, sourceID, scopeKey)`; `CalendarOverrideRow{Mode ("account"|"hidden"), AccountID}`.
- `(repo) SetCalendarShareState(ctx, userID, sourceID, eventID string, share *CalendarShareRow, previous *ShareKey) error`: one transaction updating the event row's share fields (condition: row exists with same revision) plus share put/delete.

- [ ] **Step 1: Failing tests** (mock `CalendarDynamoDBClient` as in `calendar_test.go`):
  `TestApplyBatchWritesShareRow` — upsert with `Share` puts `ACCOUNT#acc_1` / `CALSHARE#...#mk#u1` in the same `TransactWriteItems` call;
  `TestApplyBatchDeletesPreviousShareKey` (Review Focus 3) — previous key `mk_old` deleted, new `mk_new` put, one call;
  `TestApplyBatchRemoveDeletesShareRow` — a `remove` mutation for a shared event deletes its share or veto row in the same call;
  `TestApplyBatchRejectsOver100Items` — 17 share-bearing upserts → `ErrInvalidCalendarBatch` before any call;
  `TestListShareRowsKeepsGroupsWhole` (Review Focus 2) — limit 2 with rows `[mk1/u1, mk1/u2(veto), mk2/u1]` returns group `mk1` with both rows and a cursor positioned after `mk1`, never splitting it;
  `TestListShareRowsOverlap` — row starting 3 days before `from` and ending inside window is returned (lower bound `from - 93 days`, as `ListCalendarEventRows`).
- [ ] **Step 2: Run** `go test ./internal/repository -run 'Share|Over100' -count=1` → FAIL.
- [ ] **Step 3: Implement.** Count operations before calling DynamoDB: `1 (source) + 1 (receipt) + per mutation (existing ops + share put + previous delete)`; reject when > 100. Group pagination: keep querying until the last group's `meetingKey` changes or the partition range ends; the cursor is the last row of the last complete group (reuse `encodeCalendarCursor` with `Kind:"shares"`, `AccountID`). Filter expired rows (`ExpiresAt`) like references.
- [ ] **Step 4: Run** → PASS; full `go test ./internal/repository -count=1` → PASS.
- [ ] **Step 5: Commit** `feat(calendar): account share rows and grouped reads`.

### Task 3: Share decision, shared read, sharing mode, directory (service)

**Files:**
- Create: `backend/internal/service/calendar_share.go`
- Modify: `backend/internal/service/calendar.go` (`Sync`, `NewCalendarService`, `ListEvents` decrypt of new payload fields)
- Test: `backend/internal/service/calendar_share_test.go`

**Interfaces:**
- `type CalendarMembership interface { GetMember(ctx context.Context, accountID, userID string) (*model.AccountMember, error); ListAccountMembers(ctx context.Context, accountID string) ([]model.AccountMember, error); ListAccountsForUser(ctx context.Context, userID string) ([]model.AccountMember, error); GetAccountMeta(ctx context.Context, accountID string) (*model.Account, error) }` — `*repository.DynamoDBRepository` satisfies it (add `GetAccountMeta` only if no equivalent getter exists; reuse the existing one otherwise).
- `NewCalendarService(store, cipher, accounts, membership CalendarMembership)`.
- `CalendarStore` gains the Task 2 methods.
- `func calendarMeetingKey(accountID string, startsAt, endsAt time.Time, title string) string` — hex SHA-256 of `accountID|startsAt UTC RFC3339Nano|endsAt UTC RFC3339Nano|norm(title)`, `norm` = NFKC (`golang.org/x/text/unicode/norm`, already in go.mod), lowercase, whitespace runs collapsed to one space, trimmed.
- Share decision per upsert, in order: override `hidden` → veto row for the auto account (or none if no account); override `account` → shared with override account; `Private` → veto row for the previously shared account if any, else none; `ShareAccountID` → shared; else none. Every account written requires `GetMember` non-nil for the caller, else `ErrForbidden` for the batch.
- Share ciphertext: `CalendarCipher.EncryptShare(ctx, accountID, meetingKey, userID string, plaintext []byte)` / `DecryptShare(...)` with context `{"purpose":"ttobak-calendar-share-v1","accountId","meetingKey","userId"}`; plaintext = `{title,startsAt,endsAt,timeZone,isAllDay,startDate,endDate,location}` only.
- `(svc) ListAccountCalendar(ctx, userID, accountID string, query model.CalendarQuery) (*model.AccountCalendarPage, error)`: caller must be a direct member; drop rows whose `UserID` is not in the current member list (Review Focus 1); drop any group containing a `veto` row; emit one `model.AccountCalendarEvent{MeetingKey, Title, StartsAt, EndsAt, TimeZone, IsAllDay, StartDate, EndDate, Location, SharedBy []string (emails, ≤5), SharedByMe bool}` per group from the first decryptable `shared` row.
- `(svc) SetEventSharing(ctx, userID, sourceID, eventID string, request model.CalendarSharingRequest) (*model.CalendarEvent, error)`; `CalendarSharingRequest{Mode string; AccountID string}` with mode `auto`|`account`|`hidden`; scope key = event's `SeriesKey` or `eventID`; writes/deletes the override, then `SetCalendarShareState` for this occurrence (`auto` restores the last automatic result stored in the payload).
- `(svc) ShareDirectory(ctx, userID string) (*model.CalendarShareDirectory, error)`: `{accounts:[{accountId,name,aliases,domains}]}` for direct memberships only, ≤200 accounts, sorted by `accountId`.

- [ ] **Step 1: Failing tests:**
  `TestCalendarMeetingKeyNormalizes` (`"  Hana  EKS Review "` and `"hana eks review"` equal; different `endsAt` differs);
  `TestSyncShareRequiresDirectMember` (inherited-only access → `ErrForbidden`, nothing written);
  `TestSyncOverrideHiddenBeatsMatch`; `TestSyncPrivateWritesVetoForPreviouslySharedAccount`;
  `TestListAccountCalendarVetoHidesGroup` (two sharers, one veto → empty);
  `TestListAccountCalendarDropsRemovedSharer` (Review Focus 1);
  `TestListAccountCalendarRequiresDirectMember` (Billing/ancestor caller → `ErrForbidden`);
  `TestSetEventSharingSeriesScope` (override stored under `seriesKey`);
  `TestShareDirectoryDirectOnly`.
- [ ] **Step 2: Run** `go test ./internal/service -run 'Share|MeetingKey|EventSharing' -count=1` → FAIL.
- [ ] **Step 3: Implement** in `calendar_share.go`; `Sync` reads the existing event row (`getCalendarEventRow` via a new store method `GetCalendarEventRow`) to compute `PreviousShareKey`.
- [ ] **Step 4: Run** → PASS; `go test ./... -count=1` and `go vet ./...` from `backend/` → PASS.
- [ ] **Step 5: Commit** `feat(calendar): share decisions, account calendar read and sharing mode`.

### Task 4: HTTP routes and API docs

**Files:**
- Modify: `backend/internal/handler/calendar.go`, `backend/cmd/api/main.go`
- Test: `backend/internal/handler/calendar_test.go`
- Modify: `docs/API-SPEC.md` ("Private calendar" section and route table)

**Interfaces:**
- `GET /api/accounts/{accountId}/calendar?from&to&limit&cursor` → `{events:[AccountCalendarEvent],nextCursor}`, same strict query, limit and 14,000-byte rules as `/api/calendar/events`.
- `PUT /api/calendar/sources/{sourceId}/events/{eventId}/sharing` body `{"mode":"auto"|"account"|"hidden","accountId"?}` → owner event view. Strict JSON (`validCalendarJSON`).
- `GET /api/calendar/share-directory` → `CalendarShareDirectory`; 64 KiB cap.
- `CalendarAPI` gains `ListAccountCalendar`, `SetEventSharing`, `ShareDirectory`.

- [ ] **Step 1: Failing handler tests** for each route: success shape; unknown query/body field → 400; `ErrForbidden` → 403; `Cache-Control: private, no-store`.
- [ ] **Step 2: Run** `go test ./internal/handler -run Calendar -count=1` → FAIL.
- [ ] **Step 3: Implement** handlers and register the three routes next to the existing calendar routes; pass `repo` as `CalendarMembership`.
- [ ] **Step 4: Run** → PASS; `GOOS=linux GOARCH=arm64 /usr/local/go/bin/go build -tags lambda.norpc -o cmd/api/bootstrap ./cmd/api` → success. Update API-SPEC: new fields, routes, "shared to direct members; one veto hides", replacing "grants no sharing"; `python3 scripts/docs/check_docs.py` → passed (route count +3).
- [ ] **Step 5: Commit** `feat(api): account calendar, event sharing and share directory routes`.

### Task 5: Matcher (MCP intake)

**Files:**
- Create: `mcp-server/src/intake/match.ts`
- Test: `mcp-server/test/intake-match.test.mjs`

**Interfaces:**
- `interface DirectoryAccount { accountId: string; name: string; aliases: string[]; domains: string[] }`
- `buildMatcher(accounts: DirectoryAccount[], ownDomains: string[]): Matcher`
- `Matcher.match(input: { title: string; location?: string; domains: string[] }): MatchResult`
- `type MatchResult = { kind: 'matched'; accountId: string; evidence: ('title'|'location'|'domain')[] } | { kind: 'candidate'; accountIds: string[] } | { kind: 'none' }`
- Rules: NFKC + lowercase + collapse whitespace; terms shorter than 2 code points ignored; a term containing Hangul matches as a substring; any other term needs non-letter/digit boundaries on both sides; domain match is exact or subdomain (`a.b.com` matches `b.com`); `ownDomains` (default `["amazon.com"]`) are removed from both sides first. Exactly one account → `matched`; 2..n → `candidate` with up to 5 IDs sorted; 0 → `none`.

- [ ] **Step 1: Failing tests:** a Hangul alias followed directly by a Hangul particle and Latin text matches (Review Focus 4; fixture strings live in the test file); `HNBX sync` does not match alias `HNB`; `HNB/AWS` matches; attendee `ops.hanabank.com` matches domain `hanabank.com` with evidence `["domain"]`; title and domain of the same account → evidence `["domain","title"]`; two accounts → candidate; `amazon.com` attendee never matches an account listing `amazon.com`; one-letter alias ignored.
- [ ] **Step 2: Run** `cd mcp-server && npm run build && node --test test/intake-match.test.mjs` → FAIL.
- [ ] **Step 3: Implement** `match.ts`.
- [ ] **Step 4: Run** → PASS.
- [ ] **Step 5: Commit** `feat(mcp): deterministic account matcher for intake`.

### Task 6: Attendee domains, planner integration, sync fields (MCP)

**Files:**
- Create: `mcp-server/src/intake/attendees.ts`
- Modify: `mcp-server/src/intake/calendar.ts`, `mcp-server/src/intake/run.ts`, `mcp-server/src/calendar.ts` (`syncInput` schema), `mcp-server/src/api.ts`
- Modify: `mcp-server/test/fixtures/intake-outlook-server.mjs` (add `get_meeting` fixture server mode or a second fixture `intake-meetings-server.mjs`)
- Test: `mcp-server/test/intake.test.mjs`
- Regenerate: `frontend/public/mcp/ttobak-mcp.mjs` (`cmp` equal to `mcp-server/dist/ttobak-mcp.mjs`)

**Interfaces:**
- `TtobakApi.calendarShareDirectory(): Promise<{ accounts: DirectoryAccount[] }>`.
- `MEETINGS_TOOL = { name: 'get_meeting', properties: ['meetingId', 'userEmail'] }`.
- `class AttendeeDomains { constructor(store: IntakeStore, sourceId: string, call: (meetingId: string) => Promise<unknown>); domainsFor(item: OutlookItem): Promise<string[] | null> }` — cache in `calendar/<sourceId>/attendees.json` keyed by `meetingId` + `meetingChangeKey`, storing domains only (lowercased, unique, ≤50), pruned with `pruneLedger`; `null` when the call fails; after 3 consecutive not-found/argument errors the run stops calling and counts `attendeesUnavailable` (Review Focus 5).
- `Selection.calendar` gains `meetings?: { host: HostId; server: string; userEmail: string }` and `ownDomains: string[]`; `select calendar` gains `--meetings-server <name>`, `--user-email <alias@amazon.com>`, repeatable `--own-domain` (default `amazon.com`). `--accept-unknown-sensitivity` and the `SENSITIVITY_UNKNOWN` guard are removed.
- `planCalendar(items, ledger, window, zone, sharing?: { matcher: Matcher; domains: Map<string, string[] | null> })` adds to each upsert: `private: true` when categories include `private` (case-insensitive); otherwise `shareAccountId`+`shareEvidence` or `candidateAccountIds`; `seriesKey` = hex SHA-256 of `sourceId|seriesId` when `seriesId` exists. The fingerprint covers these fields so a category change re-sends the event.
- `MAX_BATCH` becomes 16 (server limit of 100 transaction items).
- `CalendarCounts` gains `shared`, `candidates`, `privateCategory`, `attendeesUnavailable`.

- [ ] **Step 1: Failing tests:** `Private` category event → upsert with `private:true` and no share fields; category added after a shared run → re-sent with `private:true`; attendee domain match → `shareEvidence:["domain"]`; meetings server returning errors 3 times → run succeeds, `attendeesUnavailable>0`, title matches still shared (Review Focus 5); attendee names/emails never appear in stdout, `~/.ttobak` files or the sync payload (canary); batches ≤16; `syncInput` accepts the new fields and rejects `shareEvidence:["body"]`.
- [ ] **Step 2: Run** `cd mcp-server && npm run build && node --test test/intake.test.mjs` → FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `npm test` → intake and calendar tests PASS (note known macOS `guardUploadPath` failure, also on `main`); `npm run bundle && cp dist/ttobak-mcp.mjs ../frontend/public/mcp/ttobak-mcp.mjs && cmp dist/ttobak-mcp.mjs ../frontend/public/mcp/ttobak-mcp.mjs`.
- [ ] **Step 5: Commit** `feat(mcp): intake account matching, attendee domains and Private category`.

### Task 7: Web — owner badges and actions, account schedule

**Files:**
- Modify: `frontend/src/types/calendar.ts`, `frontend/src/lib/api.ts` (`calendarApi.accountCalendar`, `calendarApi.setSharing`)
- Create: `frontend/src/lib/calendarSharing.ts`, `frontend/scripts/calendar-sharing.test.mjs`
- Modify: `frontend/src/components/workspace/UpcomingMeetings.tsx`
- Create: `frontend/src/components/account/AccountSchedule.tsx`; Modify: `frontend/src/app/accounts/[id]/page.tsx`
- Modify: `docs/DESIGN-SPEC.md`

**Interfaces:**
- `sharingBadge(event: CalendarEvent): { kind: 'shared'|'candidate'|'private'|'none'; accountId?: string; evidence?: string[] }`.
- `sharingActions(event: CalendarEvent, directAccountIds: string[]): ('assign'|'unshare'|'reset')[]` — `assign` for candidates/none (not private), `unshare` for shared, `reset` when `sharingMode !== 'auto'`.
- UI copy is Korean, defined once in `calendarSharing.ts` (`SHARING_COPY`), with these meanings: badge "Shared · {account}" with evidence tooltip "title" / "location" / "attendee domain"; "Candidate"; "Private"; actions "Assign account", "Unshare", "Back to automatic"; account section title "Schedule", empty state "No shared events", sharer line "Shared by {n}".
- `AccountSchedule({ accountId })` uses `useCalendarWindow()` and paginates `accountCalendar` on demand with a "Load more" button until `nextCursor` is null.

- [ ] **Step 1: Failing tests** in `calendar-sharing.test.mjs` for `sharingBadge` and `sharingActions` across shared/candidate/private/manual-mode events.
- [ ] **Step 2: Run** `cd frontend && node --test scripts/calendar-sharing.test.mjs` → FAIL.
- [ ] **Step 3: Implement** helpers, then components using existing tokens in `globals.css` and existing button/dialog components (`account-component-harness.mjs` pattern for any component test).
- [ ] **Step 4: Run** `npm run lint && npm run test:unit && npm run build` → PASS. Update DESIGN-SPEC.
- [ ] **Step 5: Commit** `feat(web): calendar sharing badges and account schedule`.

### Task 8: Decision records and runbooks

**Files:**
- Create: `docs/decisions/ADR-066-account-shared-calendar.md` (supersedes ADR-065's sensitivity consequence and the API-SPEC "grants no sharing" rule; records direct-only visibility, one-veto rule, 10-minute category latency, excluded consumers)
- Modify: `docs/decisions/ADR-065-local-mcp-intake.md` (status: superseded in part by ADR-066), `docs/runbooks/local-mcp-intake.md` (new select flags, counts, acceptance steps below), `docs/architecture.md` (calendar data flow), `docs/README.md` (index row)

- [ ] **Step 1:** Write the docs; `python3 scripts/docs/check_docs.py` → passed; `python3 -m unittest discover -s scripts/docs -p 'test_*.py'` → OK.
- [ ] **Step 2: Commit** `docs: ADR-066 account-shared calendar`.

### Task 9: Mac acceptance (manual, after deploy of API and web)

Deploy only the changed stacks per the deploy runbook (no `cdk deploy --all`); backend before web.

- [ ] `intake select calendar --host claude --server aws-outlook-mcp --meetings-server amazon-meetings-mcp --user-email <alias>@amazon.com`, then `intake calendar --dry-run` → counts include `shared`, `candidates`, `privateCategory`, `attendeesUnavailable`.
- [ ] Synthetic Outlook events: title with an account alias; attendee from an account domain only; two accounts; `Private` category; category added after sharing; edited title after sharing.
- [ ] Real run; verify account page shows matched events once, `Private` ones nowhere, candidates on the owner workspace; unshare is immediate; second run has no mutations.
- [ ] Record date and results in ADR-066 status; delete the synthetic events.
