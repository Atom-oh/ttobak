# Meeting SpecReq Tasks Implementation Plan

Status: historical implementation plan (2026-11-10); not a current reference.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** When a customer meeting with another SA appears in the user's calendar
intake, TTOBAK creates one private task per occurrence, tells the user whether to
issue the SpecReq or ask the other SA, prepares an unsent Slack draft, and
acknowledges the SpecReq assigned to the user exactly once.

**Architecture:**
- Backend: a private task row per (user, calendar occurrence) in the user
  partition (`USER#{userId}` / `SRTASK#{eventId}`), changed only through a
  version-conditioned transition table. Four authenticated, non-delegable routes.
- Mac runner (`mcp-server/src/intake/`): after a successful calendar sync, a
  deterministic task step joins attendees from `amazon-meetings-mcp`, classifies
  against TTOBAK account members, polls `spec-req-mcp`, saves Slack drafts and
  appends to a local notification queue. Attendees, Slack data and SpecReq
  payloads stay on the Mac.
- Web: a task strip and per-event badge in `PrivateUpcomingMeetings`. Mac app:
  a one-minute reader of the notification queue.

**Tech Stack:** Go 1.27 (chi, aws-sdk-go-v2 DynamoDB expression builder, KMS),
TypeScript MCP client (`@modelcontextprotocol/sdk`, node:test), Next.js 16 /
React 19 / Tailwind 4 (node:test scripts), Tauri 2 / Rust
(`tauri-plugin-notification`).

**Spec:** `docs/superpowers/specs/2026-11-10-meeting-specreq-tasks-design.md`.
Its "Builder answers" and "Phase 0 findings / Consequences" sections override
earlier sections. Mail reading is dropped.

## Global Constraints

- Handler -> service -> repository; DynamoDB expression builder only; sentinel
  errors with `errors.Is` (`service.ErrForbidden`, `service.ErrNotFound`,
  `service.ErrInvalidInput`, `service.ErrConflict`,
  `repository.ErrConditionFailed`). Error shape `{ "error": { "code", "message" } }`.
- Every task update is a conditional `UpdateItem` on `version = :expected`,
  setting `version = :expected + 1`. No whole-item `PutItem` after create.
- Task rows hold only: occurrence identity (`eventId`, `sourceId`, occurrence
  `startsAt`/`endsAt`), `accountId`, `path`, `state`, `counterpartUserId`,
  linked SpecReq `id`/`title`/`status`, `failureCode`, timestamps, `version`,
  TTL. Never attendee addresses, mail, Slack text, Slack IDs or SpecReq bodies.
- The SpecReq title is sealed with the existing calendar KMS key
  (`KMS_KEY_ID`), purpose `ttobak-specreq-task-v1`, context
  `userId`/`sourceId`/`eventId`. TTL attribute `pendingShareExpiresAt` =
  occurrence end + 90 days (same retention as calendar rows).
- Routes are registered inside the `AuthWithConnectorGrants` group and listed in
  `contracts/connector/v1/operations.json` with `delegationAllowed: false`. No
  new unauthenticated route, no new MCP tool, no CDK change.
- Responses are at most 14,000 encoded bytes (`CalendarResponseMaxBytes`);
  request bodies at most 4 KiB; list pages at most 20 tasks.
- Runner provider tools (exact allowlist; any other name is refused before the
  call):
  - `amazon-meetings-mcp`: `list_meetings`, `get_meeting`.
  - `spec-req-mcp`: `check_user_is_tam`, `search_requests_advanced`,
    `get_request`, `acknowledge_request`.
  - `slack-mcp`: `lookup_user`, `open_dm_channel`, `list_drafts`, `post_draft`.
  - Never `post_message`, `schedule_message`, `complete_request` or any other
    write.
- `acknowledge_request` is called only when `get_request` shows
  `stateId == "NEXT_UP"` and `resolverLogin == me`, only for a request linked
  to exactly one "theirs" task, once per `requestId` ever, with the ledger
  entry written (fsync, atomic rename) before the call.
- `post_draft` is called only after `list_drafts` shows no draft in that DM
  channel, once per task.
- SpecReq states (tool schema): `PENDING|NEXT_UP|WORKING|WAITING|COMPLETED|CANCELLED`.
  `resolved` means `COMPLETED`.
- Local files: `~/.ttobak/intake/` (0700); every file 0600. Queue
  `~/.ttobak/intake/notify.jsonl`. Mac posts entries younger than 24 hours,
  polling at launch and every 60 seconds.
- UI, notification and Slack copy is Korean and lives in source constants
  (`SLACK_REQUEST_TEMPLATE`, `NOTICE_COPY`, `SPECREQ_TASK_COPY`), reviewed with
  the user. Docs stay English and refer to the symbols.
- Logs and CLI output carry IDs, counts and codes only.
- Checks: Go runs on the build host after syncing the branch (or CI):
  `(cd backend && go test ./... -count=1)`, `(cd backend && go vet ./...)`, the
  arm64 api build from `CLAUDE.md`. Locally: `(cd mcp-server && npm test)`,
  `(cd frontend && npm run lint && npm run test:unit && npm run build)`,
  `(cd mac-app/src-tauri && cargo fmt --check && cargo clippy --all-targets -- -D warnings && cargo test)`,
  `python3 scripts/docs/check_docs.py`.
- Do not edit `.github/workflows/**` or `scripts/pr-review/**`.

## Rulings on spec gaps (binding for this plan)

1. **Account picker writes the task row, not the event.** The intake upsert is a
   whole-row `Put` whose encrypted payload carries no `accountId`, and the
   decrypt path rejects a row whose plaintext `accountId` differs from the
   payload. An event-level setter would be reverted by the next changed
   occurrence. The picker sets `task.accountId` (membership checked like
   `authorizeCalendarAccounts`). The runner uses the event's own `accountId`
   when the cloud event has one, else the task's.
2. **`needs_account` needs evidence.** An occurrence without an account becomes
   `needs_account` only when another attendee is an `SA`/`SSA` member of at
   least one of my accounts. Otherwise every internal meeting would get a task.
3. **Owner candidates are `SA` or `SSA` members** (the step-2 set); "mine" means
   my own role on the account is `SA`. One candidate -> theirs; two or more ->
   `needs_owner`.
4. **Auto-ack scope.** Only a `NEXT_UP` request that links to exactly one
   theirs task is acknowledged. Unlinked assignments are untouched (no
   account-partition SpecReq rows in v1, builder answer 5).
5. **`post_draft` is a second, named provider write** in the ADR amendment
   (unsent draft, never overwriting). Phase 0 replaced the earlier "never
   `post_draft`" line.
6. **Auto-ack off** posts one `ack_needed` notice; a later `WORKING` read moves
   the task to `acked`.
7. **No new ADR.** ADR-065 (local MCP intake) is amended. ADR-068 stays free.

## Delivery parts (stacked PRs, each diff <= 95,000 bytes)

Docs-only PRs cannot merge, so the spec and this plan ship with part 1. Each part
branches from the previous one. If a part exceeds the cap, split it at a task
boundary, never inside a task.

| Part | Tasks | Content | Rough size |
|---|---|---|---|
| 1 | spec + plan, Task 1 | Task model, keys, transition rules | ~72 KB |
| 2 | Tasks 2-3 | Repository; title sealing and calendar row getter | ~40 KB |
| 3 | Tasks 4-5 | Service; handlers, routes, contract, API-SPEC | ~80 KB |
| 4 | Tasks 6-8 | Runner selection/allowlist; API client + event ID; attendee join + classifier | ~55 KB |
| 5 | Tasks 9-10 | SpecReq linking + ack ledger; Slack draft + notify queue | ~60 KB |
| 6 | Task 11 | Task step orchestration; ADR-065 amendment; runbook | ~45 KB |
| 7 | Tasks 12-13 | Frontend task strip, actions, DESIGN-SPEC | ~60 KB |
| 8 | Task 14 | Mac notification reader; architecture and acceptance docs | ~35 KB |

Deploy order (when the user authorizes): API (parts 2-3) before any runner
release, frontend after API, Mac last. No stack other than the API Lambda
changes.

## Review Focus

1. **Outlook IDs with `<`, `>` or `&`.** Go's `json.Marshal` escapes them, so a
   naive `JSON.stringify` in the runner computes a different `eventId` and every
   task call 404s. Task 7: `calendarEventId matches Go vectors`.
2. **An ack call times out or the process dies after the ledger write.** It is
   never called again; the next `get_request` decides `acked` or `ack_failed`.
   Task 9: `ack after pending ledger entry is never retried` and
   `unknown outcome resolves from later read`.
3. **The user picks an owner while the runner classifies the same task.** The
   runner's `classify` gets 409, re-reads, sees `request_needed`, and stops; the
   pick is never overwritten. Tasks 4 and 11: `TestTransition_ClassifyRejectedAfterPick`,
   `runner re-reads once on 409 and skips a web-picked task`.
4. **Attendee address variants.** `Alias@Amazon.COM`, me listed as organizer or
   attendee, and a member without `@amazon.com`. Task 8:
   `classify ignores me in organizer and attendees` and
   `classify matches case-insensitively and rejects non-amazon counterpart`.
5. **A hand-typed Slack draft already sits in the DM.** It is kept, and
   `list_drafts` failing means no `post_draft`. Task 10:
   `existing draft in channel is never overwritten` and
   `list_drafts failure skips post_draft`.

---

### Task 1: Task model, keys and transition rules (part 1)

**Files:**
- Create: `backend/internal/model/specreq_task.go`
- Create: `backend/internal/service/specreq_task_rules.go`
- Test: `backend/internal/service/specreq_task_rules_test.go`

**Interfaces:**
- Produces (model):
  - `PrefixSpecReqTask = "SRTASK#"`; `func SpecReqTaskSortKey(eventID string) string`.
  - Paths `SpecReqPathMine = "mine"`, `SpecReqPathTheirs = "theirs"`.
  - States: `issue_needed`, `issued`, `request_needed`, `requested`, `acked`,
    `ack_failed`, `resolved`, `needs_account`, `needs_owner`, `closed` as
    `SpecReqState*` constants. (`ack_needed` is local only and never stored.)
  - Failure codes: `ACK_REJECTED`, `ACK_UNCONFIRMED`, `ACK_NOT_ELIGIBLE`.
  - SpecReq statuses: `SpecReqStatuses = []string{"PENDING","NEXT_UP","WORKING","WAITING","COMPLETED","CANCELLED"}`.
  - `type SpecReqTask struct { EventID, SourceID, AccountID, Path, State, CounterpartUserID, SpecReqID, SpecReqTitle, SpecReqStatus, FailureCode string; OccurrenceStartsAt, OccurrenceEndsAt, CreatedAt, UpdatedAt, StateChangedAt time.Time; Version int64 }` (JSON camelCase, empty strings omitted).
  - `type SpecReqTaskEvent struct { Title string; StartsAt, EndsAt time.Time; TimeZone string; IsAllDay bool; StartDate, EndDate string }`.
  - `type SpecReqTaskView struct { SpecReqTask; Event *SpecReqTaskEvent \`json:"event,omitempty"\` }`; `type SpecReqTaskPage struct { Tasks []SpecReqTaskView; NextCursor *string }`.
  - `type CreateSpecReqTaskRequest struct { SourceID, ExternalID, AccountID, Path, State, CounterpartUserID string }`.
  - `type SpecReqTaskTransition struct { Op string; Version int64; AccountID, CounterpartUserID, Path, State, SpecReqID, SpecReqTitle, SpecReqStatus, FailureCode string }`.
  - Ops: `classify`, `pickAccount`, `pickOwner`, `markRequested`, `linkSpecReq`,
    `recordAck`, `refreshSpecReq`, `close`.
- Produces (service):
  - `func validateSpecReqCreate(req model.CreateSpecReqTaskRequest) error`
  - `func applySpecReqTransition(current model.SpecReqTask, change model.SpecReqTaskTransition) (model.SpecReqTask, error)`:
    pure; returns the next row (version not yet incremented) or `ErrInvalidInput`
    / `ErrConflict` (wrong source state).

Transition table (anything else is `ErrConflict`):

| Op | From | To / effect |
|---|---|---|
| `classify` | `needs_account`, `needs_owner` | `needs_account`, `needs_owner` (account required), `issue_needed` (mine, account), `request_needed` (theirs, account, counterpart) |
| `pickAccount` | `needs_account`, `needs_owner` | state `needs_account`, sets `accountId`, clears path/counterpart |
| `pickOwner` | `needs_owner` | `request_needed`, path theirs, sets counterpart |
| `markRequested` | `request_needed` | `requested` |
| `linkSpecReq` | mine `issue_needed`; theirs `request_needed`/`requested` | mine -> `issued`; theirs keeps state; sets SpecReq id (+ title/status when given) |
| `recordAck` | theirs `request_needed`, `requested`, `ack_failed` | `acked`, or `ack_failed` with a failure code; sets SpecReq fields |
| `refreshSpecReq` | any with a SpecReq id, not `closed` | updates title/status; theirs `acked` + `COMPLETED` -> `resolved`; theirs `ack_failed` + `WORKING`/`WAITING`/`COMPLETED` -> `acked` |
| `close` | any but `closed` | `closed` |

Field rules: `sourceId` canonical UUID; `externalId` nonblank, <= 512 UTF-8
bytes; account and user IDs match `calendarIdentifier`; SpecReq ID canonical
UUID; title valid UTF-8, <= 512 bytes; status in `SpecReqStatuses`; unknown
`op`, `path`, `state` or `failureCode` -> `ErrInvalidInput`. `StateChangedAt`
changes only when `state` changes.

- [ ] **Step 1: Write failing tests** (table-driven)
  - `TestApplySpecReqTransition_Table`: one row per allowed table entry, asserting
    the resulting `State`, `Path`, `CounterpartUserID`, `AccountID`.
  - `TestApplySpecReqTransition_RejectsOtherSources`: e.g. `markRequested` from
    `requested`, `classify` from `request_needed` -> `ErrConflict`.
  - `TestTransition_ClassifyRejectedAfterPick`: current `request_needed` (picked)
    + `classify` -> `ErrConflict`.
  - `TestApplySpecReqTransition_ResolvedOnlyOnCompleted`: `refreshSpecReq` with
    `CANCELLED` on `acked` keeps `acked`; `COMPLETED` gives `resolved`; mine
    `issued` + `COMPLETED` stays `issued`.
  - `TestValidateSpecReqCreate`: allowed initial states only `needs_account`
    (no account), `needs_owner` (account), `issue_needed` (mine + account),
    `request_needed` (theirs + account + counterpart); 513-byte external ID and
    a non-UUID source fail.
- [ ] **Step 2: Run** `(cd backend && go test ./internal/service/ -run 'SpecReq' -count=1)` on the build host. Expected: FAIL (undefined).
- [ ] **Step 3: Implement** the model file and the two functions.
- [ ] **Step 4: Run** the same command plus `go vet ./...`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(tasks): SpecReq task model and transition rules` (with the spec and this plan).

### Task 2: Task repository (part 2)

**Files:**
- Create: `backend/internal/repository/specreq_task.go`
- Test: `backend/internal/repository/specreq_task_test.go`

**Interfaces:**
- Consumes: Task 1 model; `CalendarDynamoDBClient`-style client (add `UpdateItem`).
- Produces:
  - `type SpecReqTaskRow struct` = storage form: `PK`, `SK`, `userId`, task
    fields with `specReqTitleSealed` instead of the title, `version`,
    `pendingShareExpiresAt`, `entityType = "SPECREQ_TASK"`.
  - `func NewSpecReqTaskRepository(client SpecReqTaskDynamoDBClient, tableName string) *SpecReqTaskRepository`
  - `CreateSpecReqTask(ctx, row SpecReqTaskRow, eventRevision int64) error`:
    `TransactWriteItems` = `ConditionCheck` on `USER#{userId}` /
    `CALEVENT#{sourceId}#{eventId}` (`attribute_exists(PK)`, `userId = :u`,
    `revision = :rev`) + `Put` with `attribute_not_exists(PK)`. Cancellation on
    the Put -> `ErrConditionFailed`; on the check -> `ErrSpecReqEventGone`.
  - `GetSpecReqTask(ctx, userID, eventID string) (*SpecReqTaskRow, error)` (strong read; nil when absent).
  - `ListSpecReqTasks(ctx, userID, cursor string, limit int32) ([]SpecReqTaskRow, *string, error)`:
    `begins_with(SK, "SRTASK#")`, consistent read, limit 1..20, opaque base64
    cursor bound to `userId` (reject a cursor for another user or prefix).
  - `UpdateSpecReqTask(ctx, row SpecReqTaskRow, expectedVersion int64) error`:
    `UpdateItem` of the mutable fields only, condition
    `attribute_exists(PK) AND version = :expected`, sets `version = :expected+1`,
    removes empty optional attributes. Condition failure -> `ErrConditionFailed`.
  - Sentinel: `ErrSpecReqEventGone = errors.New("calendar occurrence is gone")`.

- [ ] **Step 1: Write failing tests** with a mock client capturing inputs:
  - `TestCreateSpecReqTask_ChecksEventRevisionAndAbsence` (asserts both condition expressions and key values).
  - `TestCreateSpecReqTask_MapsCancellationReasons` (index 0 -> `ErrSpecReqEventGone`, index 1 -> `ErrConditionFailed`).
  - `TestUpdateSpecReqTask_VersionCondition` (expression contains `version = :expected`, `SET version = :next`; never a `PutItem`).
  - `TestListSpecReqTasks_PaginatesAndBindsCursor` (two pages; foreign-user cursor -> `ErrInvalidCalendarCursor`-style sentinel `ErrInvalidSpecReqCursor`).
  - `TestListSpecReqTasks_RejectsForeignRows` (a row with another `userId` -> `ErrCalendarCorruptData`).
- [ ] **Step 2: Run** `(cd backend && go test ./internal/repository/ -run SpecReq -count=1)`. Expected: FAIL.
- [ ] **Step 3: Implement** with `expression.NewBuilder()`.
- [ ] **Step 4: Run** the same command. Expected: PASS.
- [ ] **Step 5: Commit** `feat(tasks): SpecReq task repository`

### Task 3: Title sealing and calendar row getter (part 2)

**Files:**
- Modify: `backend/internal/service/calendar_crypto.go`
- Modify: `backend/internal/repository/calendar.go`
- Test: `backend/internal/service/calendar_crypto_test.go`, `backend/internal/repository/calendar_test.go`

**Interfaces:**
- Produces:
  - `func (crypto *CalendarCipher) SealTaskText(ctx, userID, sourceID, eventID string, plaintext []byte) (string, error)`
    and `OpenTaskText(...) ([]byte, error)`: same bounds as `Encrypt`/`Decrypt`,
    context `purpose = "ttobak-specreq-task-v1"`. Refactor the existing pair onto
    a private `encryptWithContext`/`decryptWithContext`; `Encrypt`/`Decrypt`
    behaviour is unchanged.
  - `func (repo *CalendarRepository) GetCalendarEventRow(ctx, userID, sourceID, eventID string) (*CalendarEventRow, error)`
    (exported wrapper of `getCalendarEventRow`, identity-checked).

- [ ] **Step 1: Write failing tests**
  - `TestSealTaskText_UsesTaskPurpose`: the mock KMS sees purpose `ttobak-specreq-task-v1`; a ciphertext sealed by `Encrypt` does not open with `OpenTaskText` (mock enforces context equality).
  - `TestCalendarCipher_EventPurposeUnchanged`: existing context stays `ttobak-calendar-event-v1`.
  - `TestGetCalendarEventRow_IdentityChecked`: a row whose `userId` differs -> `ErrCalendarCorruptData`; absent -> `nil, nil`.
- [ ] **Step 2: Run** `(cd backend && go test ./internal/service/ ./internal/repository/ -run 'TaskText|EventPurpose|GetCalendarEventRow' -count=1)`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** the full backend suite. Expected: PASS.
- [ ] **Step 5: Commit** `feat(tasks): seal task titles and read calendar rows`

### Task 4: Task service (part 3)

**Files:**
- Create: `backend/internal/service/specreq_task.go`
- Test: `backend/internal/service/specreq_task_test.go`

**Interfaces:**
- Consumes: Tasks 1-3; `calendarEventID` and `decryptCalendarEvent` from
  `service/calendar.go`; `CalendarAccountReader.GetAccount`.
- Produces:
  - `type SpecReqTaskStore interface { CreateSpecReqTask; GetSpecReqTask; ListSpecReqTasks; UpdateSpecReqTask }` (Task 2 signatures).
  - `type SpecReqEventReader interface { GetCalendarEventRow(ctx, userID, sourceID, eventID string) (*repository.CalendarEventRow, error) }`.
  - `func NewSpecReqTaskService(store SpecReqTaskStore, events SpecReqEventReader, cipher *CalendarCipher, accounts CalendarAccountReader) *SpecReqTaskService`
  - `Create(ctx, userID string, req model.CreateSpecReqTaskRequest) (*model.SpecReqTaskView, error)`
  - `Get(ctx, userID, eventID string) (*model.SpecReqTaskView, error)`
  - `List(ctx, userID, cursor string, limit int32) (*model.SpecReqTaskPage, error)`
  - `Transition(ctx, userID, eventID string, change model.SpecReqTaskTransition) (*model.SpecReqTaskView, error)`

Decisions:
- `Create`: `eventId = calendarEventID(sourceId, externalId)`; read the event row
  (absent -> `ErrNotFound`); copy occurrence times; TTL = `EndsAt + 90d`;
  `accountId` (when set) must pass `GetAccount`; a counterpart must be a member
  of that account with role `SA` or `SSA`, a non-empty `userId`, an
  `@amazon.com` email, and not the caller. Existing row -> `ErrConflict`.
- `Transition`: load, `applySpecReqTransition`, re-check account and
  counterpart for `classify`/`pickAccount`/`pickOwner`, seal a non-empty title,
  `UpdateSpecReqTask(row, change.Version)`. Stale version or condition failure
  -> `ErrConflict`. A `change.Version` different from the stored one is
  `ErrConflict` without a write.
- Views: decrypt the event payload for `Event` (gone or corrupt event -> `Event`
  nil, task still returned); an inaccessible `accountId` is blanked in the view
  (as `ListEvents` does); the page is trimmed to fit 14,000 bytes with the
  cursor moved back, like `ListEvents`.

- [ ] **Step 1: Write failing tests** (mock store, events, KMS, accounts):
  - `TestSpecReqCreate_DerivesEventIDAndTTL` (asserts `eventId` equals `calendarEventID` and TTL = end + 90 days).
  - `TestSpecReqCreate_MissingEventNotFound`; `TestSpecReqCreate_DuplicateConflict`.
  - `TestSpecReqCreate_CounterpartRules`: table over role `AM` (rejected), pending member without userId, non-amazon email, self, valid `SSA`.
  - `TestSpecReqCreate_ForeignAccountForbidden`.
  - `TestSpecReqTransition_StaleVersionConflict` (no store write when versions differ; condition failure maps to `ErrConflict`).
  - `TestSpecReqTransition_SealsTitle` (stored row has no plaintext title; view returns it).
  - `TestSpecReqList_BlanksRevokedAccountAndFits` (a revoked account blanked; 20 large titles trimmed under 14,000 bytes with a cursor).
- [ ] **Step 2: Run** `(cd backend && go test ./internal/service/ -run SpecReq -count=1)`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** the same command. Expected: PASS.
- [ ] **Step 5: Commit** `feat(tasks): SpecReq task service`

### Task 5: Handlers, routes and connector contract (part 3)

**Files:**
- Create: `backend/internal/handler/specreq_task.go`
- Modify: `backend/cmd/api/main.go` (service wiring next to `calendarService`; routes after `/api/calendar/events`)
- Modify: `contracts/connector/v1/operations.json`, then regenerate `backend/internal/connector/operations_generated.go`, `mcp-server/src/connector-operations.ts`, `backend/python/qa/connector_operations.py` with `python3 scripts/connector/generate_contract.py`
- Modify: `backend/internal/connector/contract_test.go`
- Modify: `docs/API-SPEC.md` (route inventory block and a short "SpecReq tasks" section)
- Test: `backend/internal/handler/specreq_task_test.go`

**Interfaces:**

| Method | Path | Handler | Operation | Caps |
|---|---|---|---|---|
| GET | `/api/specreq-tasks` | `specReqTaskHandler.List` | `api.specReqTask.list` | read |
| GET | `/api/specreq-tasks/{eventId}` | `specReqTaskHandler.Get` | `api.specReqTask.get` | read |
| POST | `/api/specreq-tasks` | `specReqTaskHandler.Create` | `api.specReqTask.create` | read, write |
| POST | `/api/specreq-tasks/{eventId}/transitions` | `specReqTaskHandler.Transition` | `api.specReqTask.transition` | read, write |

- All `delegationAllowed: false`; `Cache-Control: no-store`; bodies <= 4 KiB
  with unknown fields rejected; `{eventId}` must match `^[0-9a-f]{64}$`.
- `List` query: `cursor` (<= 4,096 bytes), `limit` 1..20.
- Errors: `ErrInvalidInput` 400 `BAD_REQUEST`; `ErrForbidden` 403; `ErrNotFound`
  404; `ErrConflict` 409 `CONFLICT`; encryption unavailable 503.
- Create returns 201 with the view; transition returns 200 with the view.

- [ ] **Step 1: Write failing tests**
  - `TestSpecReqTaskHandler_RoutesAndErrors`: table over the four routes x {success, 400, 403, 404, 409}; asserts status, error shape and `Cache-Control`.
  - `TestSpecReqTaskHandler_RejectsBadEventID` and `_RejectsUnknownFields` and `_RejectsLargeBody`.
  - `TestSpecReqTaskOperationsRemainNondelegable` in `contract_test.go`, mirroring `TestCalendarOperationsRemainNondelegable` for the four operations.
- [ ] **Step 2: Run** `(cd backend && go test ./internal/handler/ ./internal/connector/ ./internal/connectorinventory/ -count=1)`. Expected: FAIL.
- [ ] **Step 3: Implement**, register routes, add contract rows, run
  `python3 scripts/connector/generate_contract.py` then `--check`, update the
  API-SPEC inventory and section (fields, transition table by reference to the
  op names, size limits, privacy).
- [ ] **Step 4: Run** backend tests and vet, the arm64 api build,
  `(cd mcp-server && npm test)`, `python3 scripts/docs/check_docs.py` (route
  count rises by 4 from the base). Expected: PASS.
- [ ] **Step 5: Commit** `feat(tasks): private SpecReq task routes`

### Task 6: Connector selection and tool allowlist (part 4)

**Files:**
- Create: `mcp-server/src/intake/connectors.ts`
- Modify: `mcp-server/src/intake/run.ts` (`Selection`, `select`, `status`, `USAGE`)
- Modify: `mcp-server/src/intake/mcp-client.ts`
- Modify: `mcp-server/src/intake/errors.ts`
- Create: `mcp-server/test/fixtures/intake-work-server.mjs` (synthetic meetings + SpecReq + Slack tools, mode via env)
- Test: `mcp-server/test/intake-tasks.test.mjs`

**Interfaces:**
- Produces:
  - `type ConnectorId = 'calendar' | 'meetings' | 'specreq' | 'slack'`.
  - `CONNECTOR_TOOLS: Record<Exclude<ConnectorId,'calendar'>, ToolExpectation[]>`:
    - meetings: `list_meetings` [`startTime`,`endTime`,`userEmail`], `get_meeting` [`meetingId`,`getMaster`,`userEmail`];
    - specreq: `check_user_is_tam` [], `search_requests_advanced` [`resolverId`,`requesterId`,`stateId`,`au_ca_gte`,`pageSize`,`fields_json`,`lastEvaluatedIndex_json`], `get_request` [`request_id`], `acknowledge_request` [`request_id`];
    - slack: `lookup_user` [`query`], `open_dm_channel` [`userIds`], `list_drafts` [], `post_draft` [`channel`,`text`].
  - `Selection` gains `meetings?`, `specreq?` (with `autoAck: boolean`), `slack?`, each `{ host, server, pathDirs, selectedAt }`.
  - CLI: `intake select meetings|specreq|slack --host <h> --server <name> [--auto-ack on|off]` (`--auto-ack` only for specreq, default `on`); probes every tool of that connector with `requireTool`.
  - `McpSession.open(spec, name, { pathDirs, allowedTools: string[] })`; `call` throws `IntakeError('INVALID_ARGUMENT')` for a name outside `allowedTools` before sending.
  - New codes: `IDENTITY_MISMATCH`, `TASKS_PARTIAL`.
  - `intake status` lists each selected connector's host and server, and `status.tasks`.

- [ ] **Step 1: Write failing tests**
  - `select specreq stores autoAck default on and off`.
  - `select slack fails with SCHEMA_CHANGED when post_draft lacks text`.
  - `session refuses a tool outside the allowlist` (calling `post_message` throws and the fixture records no call).
  - `selection never stores host command, args or env` (canary absent from every file).
- [ ] **Step 2: Run** `(cd mcp-server && npm test)`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `(cd mcp-server && npm test)`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(intake): select meetings, SpecReq and Slack servers`

### Task 7: TTOBAK client methods and event ID (part 4)

**Files:**
- Modify: `mcp-server/src/api.ts`
- Create: `mcp-server/src/intake/event-id.ts`
- Test: `mcp-server/test/intake-tasks.test.mjs`

**Interfaces:**
- Produces:
  - `TtobakApi.listSpecReqTasks(cursor?: string)`, `getSpecReqTask(eventId)`,
    `createSpecReqTask(body: CreateSpecReqTaskBody)`,
    `transitionSpecReqTask(eventId, body: SpecReqTransitionBody)`,
    `listAccountProjects(accountId)` (`GET /api/accounts/{id}/projects`, returns
    `{ projectId, sfdcOpptyId? }[]`). Existing `getAccount` supplies members and
    `listAccounts` my accounts. TS types mirror Task 1 field names.
  - `calendarEventId(sourceId: string, externalId: string): string`: SHA-256 hex
    of Go `json.Marshal([]string{sourceId, externalId})`, i.e. `JSON.stringify`
    followed by replacing `<`, `>`, `&`, U+2028 and U+2029 with Go's `<`,
    `>`, `&`, ` `, ` ` escapes.
  - `tokenEmail(idToken)` next to `tokenSubject` in `run.ts` (verified ID token
    `email`, lowercased; missing -> `TTOBAK_LOGIN_REQUIRED`).

- [ ] **Step 1: Write failing tests**
  - `calendarEventId matches Go vectors`: three vectors (plain, `a<b>&c`, U+2028) whose expected hex is produced by a Go test added in this task (`TestCalendarEventIDVectors` in `backend/internal/service/calendar_test.go`) and pasted into both tests.
  - `task client methods send exact paths and bodies` (fixture preload as in `calendar.test.mjs`).
- [ ] **Step 2: Run** `(cd mcp-server && npm test)` and the Go vector test. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** both. Expected: PASS.
- [ ] **Step 5: Commit** `feat(intake): task API client and occurrence IDs`

### Task 8: Attendee join and classifier (part 4)

**Files:**
- Create: `mcp-server/src/intake/attendees.ts`, `mcp-server/src/intake/classify.ts`
- Test: `mcp-server/test/intake-tasks.test.mjs`

**Interfaces:**
- Consumes: `OutlookItem` (`calendar.ts`).
- Produces:
  - `parseMeetingList(payload: unknown): MeetingListItem[]` with `{ meetingId, subject, start, end, isRecurring }`; strict types, unknown fields ignored, <= 5,000 items.
  - `joinMeeting(item: OutlookItem, list: MeetingListItem[]): MeetingListItem | null`: exactly one candidate with the same start instant, end instant (compared as epoch ms) and identical subject string; else null.
  - `parseAttendees(payload: unknown): { addresses: string[]; isPrivate: boolean } | null` from `requiredAttendees`, `optionalAttendees`, `organizer`; lowercased, trimmed, deduplicated, <= 500.
  - `type MemberView = { userId: string; email: string; role: string }`.
  - `classifyOccurrence(input: { me: string; accountId: string | null; attendees: string[]; members: MemberView[]; anyAccountSaEmails: Set<string> }): Classification`
    where `Classification = { kind: 'skip' } | { kind: 'task'; state; path?; counterpartUserId? }`.
    Rules: remove `me`; account set -> counterpart candidates = attendees that equal a member email (exact, lowercase) with role `SA`/`SSA`, non-empty userId, `@amazon.com`; none -> skip; my role `SA` -> mine `issue_needed`; one -> theirs `request_needed`; more -> `needs_owner`. Account null -> `needs_account` if any attendee is in `anyAccountSaEmails`, else skip.
  - Private meetings (`isPrivate`) are skipped.

- [ ] **Step 1: Write failing tests**
  - `join requires exactly one exact match` (two same-time candidates -> null; subject differs -> null; offset-different ISO strings with equal instants -> match).
  - `classify ignores me in organizer and attendees`.
  - `classify matches case-insensitively and rejects non-amazon counterpart`.
  - `classify table`: mine, theirs, needs_owner (two SAs), skip (only AM attendee), needs_account (no account, SA of another account), skip (no account, no SA).
  - `private meeting is skipped`.
- [ ] **Step 2: Run** `(cd mcp-server && npm test)`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run.** Expected: PASS.
- [ ] **Step 5: Commit** `feat(intake): attendee join and task classification`

### Task 9: SpecReq linking and acknowledge ledger (part 5)

**Files:**
- Create: `mcp-server/src/intake/specreq.ts`, `mcp-server/src/intake/ack-ledger.ts`
- Test: `mcp-server/test/intake-tasks.test.mjs`

**Interfaces:**
- Produces:
  - `parseSearch(payload): { requests: SpecReqSummary[]; next: string | null }` with `SpecReqSummary = { requestId; title; stateId; requesterLogin; resolverLogin; salesforceId; meetingDateRanges: string[]; createdAt: number }`; fixed `fields_json` of exactly these fields, `pageSize` 50, at most 4 pages per query.
  - `parseRequest(payload)`: reads `data` envelope (`{ success, data }`) into `SpecReqSummary`.
  - `meetingInRanges(startsAt: string, zone: string, ranges: string[]): boolean`: local date inside any inclusive `YYYY-MM-DD - YYYY-MM-DD`; malformed ranges never match.
  - `linkRequests(requests, tasks, opptyAccounts: Map<string, Set<string>>, rule: { path: 'mine'|'theirs'; me: string; counterpartLogin: (task) => string | null }): Map<requestId, eventId>`:
    a pair matches when `opptyAccounts.get(salesforceId)` holds the task account,
    the meeting date is in range, and the requester is me (mine) or the
    counterpart alias (theirs). Keep only pairs unique in both directions; skip
    requests already linked to another task. Mine also requires `createdAt` >=
    task `createdAt` and < occurrence end + 7 days.
  - Ledger at `['specreq', scope, 'ack-ledger.json']`:
    `{ version: 1, entries: Record<requestId, { eventId; attemptedAt; outcome: 'pending'|'ok'|'rejected'|'unknown'; cloudSynced: boolean }> }`.
    `beginAck(store, scope, requestId, eventId)` (throws if an entry exists),
    `finishAck(...)`, `markSynced(...)`; entries pruned after 120 days.
  - `acknowledgeOnce(session, ledger, request, me): Promise<'ok'|'rejected'|'unknown'|'not_eligible'>`:
    `get_request` -> eligible only if `NEXT_UP` and `resolverLogin === me` ->
    `beginAck` -> `acknowledge_request` -> `finishAck`. Tool error -> `rejected`;
    timeout or crash -> `unknown`.
  - Later runs: entries with `unknown`/`rejected` are re-read with `get_request`;
    `WORKING`/`WAITING`/`COMPLETED` with me as resolver -> `acked`; otherwise
    `ack_failed` (`ACK_UNCONFIRMED` or `ACK_REJECTED`). Never a second
    `acknowledge_request`.
  - Me: `check_user_is_tam().employee_login` must equal the local part of the
    signed-in `@amazon.com` email, else `IDENTITY_MISMATCH` and the SpecReq step stops.

- [ ] **Step 1: Write failing tests** (fixture records every call)
  - `ack after pending ledger entry is never retried` (pre-seeded pending entry; fixture `acknowledge_request` count stays 0).
  - `unknown outcome resolves from later read` (timeout mode, then `WORKING` -> acked; `NEXT_UP` -> ack_failed `ACK_UNCONFIRMED`).
  - `ack refused unless NEXT_UP and resolver is me`.
  - `ledger is written before the ack call` (fixture reads the ledger file inside its handler and sees `pending`).
  - `link requires oppty account, date range and requester` table, including `two tasks match one request -> no link` and `malformed range -> no link`.
  - `identity mismatch stops SpecReq step`.
- [ ] **Step 2: Run** `(cd mcp-server && npm test)`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run.** Expected: PASS.
- [ ] **Step 5: Commit** `feat(intake): SpecReq linking and single acknowledge`

### Task 10: Slack draft and notification queue (part 5)

**Files:**
- Create: `mcp-server/src/intake/slack-draft.ts`, `mcp-server/src/intake/notify.ts`
- Create: `contracts/specreq/slack-request-v1.json` (template, a synthetic input, the expected Korean output)
- Modify: `mcp-server/src/intake/store.ts` (append)
- Test: `mcp-server/test/intake-tasks.test.mjs`

**Interfaces:**
- Produces:
  - `SLACK_REQUEST_TEMPLATE` and `renderSlackRequest({ accountName, meetingTitle, localDateTime, myName }): string` (no model; placeholders `{account}`, `{title}`, `{when}`, `{me}`; output <= 2,000 chars).
  - `prepareDraft(session, counterpartEmail, text): Promise<'drafted'|'kept_existing'|'no_user'|'failed'>`:
    `lookup_user({ query: email })` -> exactly one user ID matching `^[UW][A-Z0-9]{2,20}$`;
    `open_dm_channel({ userIds })` -> channel ID; `list_drafts({})` -> any draft
    for that channel -> `kept_existing`; else `post_draft({ channel, text })`.
    A `replacedExisting: true` response is logged as a count only and its
    `previousText` is dropped. Slack IDs live in memory only.
  - Draft ledger `['slack', scope, 'drafts.json']`: `Record<eventId, { at; outcome }>`; one attempt per task.
  - `IntakeStore.append(line: string, ...relative)`: `O_APPEND|O_CREAT` 0600,
    same owner/type checks as `read`, rejects lines over 4 KiB or with newlines.
  - `NOTICE_COPY` and `enqueueNotice(store, scope, notice: { eventId; state; title; body })`:
    key `${eventId}:${state}`; a sent-key set at `['notify', scope, 'sent.json']`
    makes it one notice per (task, state); line
    `{"v":1,"key","state","title","body","path":"/meetings?task=<eventId>","createdAt"}`;
    title <= 120 chars, body <= 400 chars. When the file passes 256 KiB it is
    rewritten atomically with entries younger than 7 days.
  - Notified states: `issue_needed`, `request_needed` (body says drafted, kept
    existing, or copy from TTOBAK), `acked`, `ack_failed`, and `ack_needed` when
    auto-ack is off.

- [ ] **Step 1: Write failing tests**
  - `existing draft in channel is never overwritten`.
  - `list_drafts failure skips post_draft`.
  - `slack template renders the contract fixture` (reads `contracts/specreq/slack-request-v1.json`).
  - `notice is enqueued once per task and state` and `queue file is 0600 and rejects a symlink`.
  - `no Slack ID or previous draft text reaches any file` (canary).
- [ ] **Step 2: Run** `(cd mcp-server && npm test)`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run.** Expected: PASS.
- [ ] **Step 5: Commit** `feat(intake): Slack request drafts and notification queue`

### Task 11: Task step orchestration, ADR amendment, runbook (part 6)

**Files:**
- Create: `mcp-server/src/intake/tasks.ts`
- Modify: `mcp-server/src/intake/calendar.ts` (return the plan's upserted and removed external IDs in `CalendarRunResult`)
- Modify: `mcp-server/src/intake/run.ts` (call `runTasks` after a successful non-dry-run calendar sync; record `status.tasks`)
- Modify: `docs/decisions/ADR-065-local-mcp-intake.md`, `docs/runbooks/local-mcp-intake.md`
- Test: `mcp-server/test/intake-tasks.test.mjs`

**Interfaces:**
- Consumes: Tasks 6-10.
- Produces: `runTasks(options: { store; api; scope; zone; now; sourceId; selection; sessions: { meetings?; specreq?; slack? }; calendar: { upserted: OutlookItem[]; removedExternalIds: string[] }; me: { email; userId } }): Promise<TaskCounts>`.

Order per run (each step bounded; a step failure records its code and the run
continues with independent steps, ending as `TASKS_PARTIAL`):
1. Load cloud tasks (<= 10 pages) and the window's cloud events (for `accountId`).
2. Close tasks whose occurrence was removed (`close`).
3. Evaluate new occurrences (not in `['tasks', scope, 'seen.json']`) and tasks in
   `needs_*` with an account: one `list_meetings` over the sync window,
   `get_meeting` for at most 25 joined items, classify, `createSpecReqTask` or
   `classify`. Mark seen only after the cloud write or a `skip`.
4. Mine linking (`requesterId = [me]`, `au_ca_gte` = oldest open mine task
   `createdAt`), `linkSpecReq`.
5. Theirs: `stateId = ["NEXT_UP"]`, `resolverId = [me]`; link; auto-ack on ->
   `acknowledgeOnce` + `recordAck`; off -> `ack_needed` notice. Ledger entries
   with `cloudSynced: false` are retried here.
6. Refresh linked open tasks with `get_request` (<= 25), `refreshSpecReq`.
7. Drafts for `request_needed` tasks without a draft ledger entry.
8. Notices for every state first entered in this run.
- Every transition sends the last seen `version`; on 409 it re-reads that task
  once and re-plans only if the transition is still valid.
- Opportunity map: `listAccountProjects` for each distinct account of open tasks
  (<= 20 accounts), `sfdcOpptyId -> accountIds`.
- CLI output adds `tasks: { result, counts }`; the exit code follows the calendar
  result. Without `meetings` and `specreq` selections the step is skipped.

- [ ] **Step 1: Write failing tests** (end to end through `runIntake` with the work fixture and an API preload)
  - `theirs path: task, draft, ack, notices in order` (asserts fixture call sequence and queue lines `request_needed`, `acked`).
  - `mine path links the created SpecReq and marks issued`.
  - `runner re-reads once on 409 and skips a web-picked task`.
  - `removed occurrence closes its task`.
  - `cloud write failure after ack is retried next run without a second ack`.
  - `no attendee address or Slack ID reaches the API or any file` (canary over request bodies and the state tree).
- [ ] **Step 2: Run** `(cd mcp-server && npm test)`. Expected: FAIL.
- [ ] **Step 3: Implement**; amend ADR-065: Decision lists the three connectors
  and tool allowlist; Consequences adds the two named provider writes
  (`acknowledge_request` under the eligibility rule, `post_draft` never
  overwriting), the private task rows, and the `get_meeting` `isPrivate`
  sensitivity source. Runbook: select commands, `--auto-ack`, task acceptance
  steps on the Mac (synthetic meeting, then one real assignment with the user
  present), recovery for `IDENTITY_MISMATCH` and `ack_failed`.
- [ ] **Step 4: Run** `(cd mcp-server && npm test)` and `python3 scripts/docs/check_docs.py`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(intake): meeting SpecReq task step`

### Task 12: Frontend task data and strip (part 7)

**Files:**
- Create: `frontend/src/types/specreqTask.ts`, `frontend/src/hooks/useSpecReqTasks.ts`, `frontend/src/components/workspace/SpecReqTasks.tsx`
- Modify: `frontend/src/lib/api.ts` (`specReqTaskApi.list/get/transition`, `cache: 'no-store'`), `frontend/src/components/workspace/UpcomingMeetings.tsx`
- Test: `frontend/scripts/specreq-tasks.test.mjs`

**Interfaces:**
- Produces:
  - `useSpecReqTasks({ accountId?, highlight? })`: loads all pages (<= 10), keyed by `userId` and `membershipRevision`, aborts on scope change; exposes `tasks`, `byEventId`, `state`, `transition(eventId, change)` that replaces the task on success and reloads it on 409.
  - `SpecReqTasks`: open tasks (not `closed`/`resolved`/`issued`), filtered by `accountId` when given, each row showing event title and local time (or "time unknown" copy when `event` is absent), state label, SpecReq title/status; `highlight` row scrolled into view.
  - `PrivateUpcomingMeetings` renders `SpecReqTasks` above the events and a state badge on `EventCard` for events with a task. `MeetingsWorkspace` passes `?task=` (validated `^[0-9a-f]{64}$`) as `highlight`.
  - `SPECREQ_TASK_COPY` holds every Korean label.

- [ ] **Step 1: Write failing tests** (`renderToStaticMarkup` harness as in `calendar-workspace.test.mjs`)
  - `strip lists open tasks and hides closed, resolved and issued`.
  - `account page filters by accountId`.
  - `badge appears on the event with a task`.
  - `invalid task query param is ignored`.
- [ ] **Step 2: Run** `(cd frontend && npm run test:unit)`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** lint, unit tests and build. Expected: PASS.
- [ ] **Step 5: Commit** `feat(web): SpecReq task strip`

### Task 13: Frontend task actions (part 7)

**Files:**
- Create: `frontend/src/components/workspace/SpecReqTaskActions.tsx`, `frontend/src/lib/slackRequest.ts`
- Modify: `frontend/src/components/workspace/SpecReqTasks.tsx`, `docs/DESIGN-SPEC.md`
- Test: `frontend/scripts/specreq-tasks.test.mjs`

**Interfaces:**
- Produces:
  - Actions by state: `request_needed` -> "copy Slack request" (clipboard of
    `renderSlackRequest` with account name from the accounts list, event title
    and time, my display name) and "I sent it" (`markRequested`);
    `needs_account` -> account picker over my accounts (`pickAccount`), with
    waiting copy once an account is set; `needs_owner` -> owner picker over the
    account's `SA`/`SSA` members with a userId, excluding me (`pickOwner`);
    mine `issue_needed` and theirs `requested` -> "link SpecReq" input
    accepting a UUID (`linkSpecReq`); SpecReq link opens the SpecReq page for
    that ID in a new tab with `rel="noopener noreferrer"`.
  - `frontend/src/lib/slackRequest.ts` exports the same `SLACK_REQUEST_TEMPLATE`.
  - Buttons disable while a transition is pending; a 409 shows reload copy.

- [ ] **Step 1: Write failing tests**
  - `slack copy renders the contract fixture` (reads `../contracts/specreq/slack-request-v1.json`).
  - `actions per state` table (which buttons render for each state/path).
  - `owner picker excludes me, AM roles and pending members`.
  - `transition sends the current version`.
- [ ] **Step 2: Run** `(cd frontend && npm run test:unit)`. Expected: FAIL.
- [ ] **Step 3: Implement**; add the strip and actions to DESIGN-SPEC (tokens from `globals.css`).
- [ ] **Step 4: Run** lint, unit tests, build, `python3 scripts/docs/check_docs.py`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(web): SpecReq task actions`

### Task 14: Mac notification reader (part 8)

**Files:**
- Create: `mac-app/src-tauri/src/intake_notify.rs`
- Modify: `mac-app/src-tauri/src/lib.rs` (module, start the poller in `setup`, handle `RunEvent::Reopen` and main-window focus)
- Modify: `docs/architecture.md`, `docs/runbooks/local-mcp-intake.md` (Mac acceptance)
- Test: unit tests inside `intake_notify.rs`

**Interfaces:**
- Produces:
  - `pub fn parse_notices(bytes: &[u8], now: SystemTime) -> Vec<Notice>`: one JSON
    object per line; skips malformed lines, `v != 1`, key not
    `^[0-9a-f]{64}:[a-z_]{3,20}$`, path not exactly `/meetings?task=<same 64 hex>`,
    `createdAt` older than 24 hours or more than 5 minutes ahead, title over
    120 or body over 400 chars.
  - `pub struct SeenStore` at the app data dir `intake-notified.json`
    (0600), keys with post time, pruned after 48 hours; `fn unseen(&self, notices) -> Vec<Notice>`, `fn record(&mut self, key)`.
  - Poller: at launch and every 60 seconds on a background thread, reads
    `~/.ttobak/intake/notify.jsonl` only when it is a regular, non-symlink file
    owned by the user and <= 1 MiB; posts each unseen notice with
    `tauri-plugin-notification`, then records it.
  - Click: the plugin has no desktop click callback, so posting stores the
    latest notice path with a 10-minute expiry; the next `RunEvent::Reopen` or
    main-window `Focused(true)` inside that window navigates the main window to
    the configured main-window origin + path once, then clears it. The origin
    comes from the app config, never from the file.
- [ ] **Step 1: Write failing tests**
  - `parse_skips_malformed_old_and_future_lines`.
  - `parse_rejects_foreign_paths_and_mismatched_keys` (absolute URL, `//host`, other route, key/path event mismatch).
  - `seen_store_posts_once_and_prunes`.
  - `open_target_expires_and_is_consumed_once`.
- [ ] **Step 2: Run** `(cd mac-app/src-tauri && cargo test intake_notify)`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `cargo fmt --check && cargo clippy --all-targets -- -D warnings && cargo test` on macOS, then the runbook's manual check (one synthetic queue line posts once; clicking brings TTOBAK to `/meetings?task=...`). ADR-061 found app notifications unreliable for the meeting prompt; record the observed behaviour in the runbook instead of claiming delivery.
- [ ] **Step 5: Commit** `feat(mac): post SpecReq task notifications`

## Self-review notes

- Spec coverage: states (Task 1), classification (Tasks 8, 11), attendee join
  (Task 8), Slack draft (Tasks 10, 13), ack (Task 9), residency (Global
  Constraints, Tasks 2-4 and canary tests), surfaces (Tasks 12-14), ADR change
  (Task 11), builder answers 1-8 and Phase 0 consequences (Tasks 9-11).
- Not covered by design: issuing/resolving SpecReqs, sending Slack, SFDC
  account teams, model calls, and an event-level account setter (Ruling 1).
