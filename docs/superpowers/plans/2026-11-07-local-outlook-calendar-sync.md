# Local Outlook Calendar Sync Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Status:** Historical plan recorded and authorized for subagent-driven
implementation on 2026-11-07. Checklists track execution, not deployment.

**Goal:** Add private, repeatable Outlook event ingestion through local TTOBAK MCP, upcoming UI integration, and a truthful recurring-sync guide.

**Architecture:** A user-approved local Outlook reader supplies normalized batches
to stdio-only MCP tools. Existing authenticated Go APIs store separate owner-scoped
calendar rows with source-revision CAS, atomic time references, encryption, and
idempotent receipts; the web reads only the current viewer's calendar.

**Tech Stack:** Existing Go/chi/AWS SDK, DynamoDB, KMS, TypeScript MCP, Next.js, node:test.

**Spec:** `docs/superpowers/specs/2026-11-07-meeting-workspace-outlook-design.md`

## Global Constraints

- Preserve existing authorization, explicit sharing, recording, uploads, and navigation.
- Account association classifies an item; it never grants another user access.
- Keep Outlook credentials and provider checkpoints local; never send them to TTOBAK.
- Use existing authenticated APIs and CloudFront; add no public application origin.
- Keep new calendar tools stdio-only; do not expand the remote private MCP bridge.
- Keep calendar events separate from Meeting status, transcripts, notes, and STT.
- Use existing dependencies and visual tokens; add no UI framework or external font.
- Write new documentation in concise English; retain the application's Korean UI copy.
- No commit, PR, merge, or deployment without a separate user request.

## Review Focus

- Same batch retried after an unknown response must not duplicate or overwrite newer data: Tasks 1–3.
- Recurrence, rescheduling, all-day/DST boundaries, and changed import windows must preserve identity: Tasks 1 and 4.
- Account association, guessed IDs, and stale references must never expose another user's calendar: Task 2.
- A failed partial provider read must not delete events or claim successful freshness: Tasks 1 and 4.
- No Outlook credentials, private-event content, or new write capability may leak into remote MCP: Tasks 2–4.

---

## Execution Preconditions

Use the UI plan's approval/isolation rules and source revision. This plan's API,
MCP, and guide are independently testable; Task 5 integrates with the UI plan.
Read backend/MCP/infra scoped guides before edits. Resolve the available Go
executable rather than assuming the stale documented `/usr/local/go/bin/go` path.

Verify the configured KMS key and API role support context-bound encryption.
Missing configuration is a fail-closed feature prerequisite, not permission to
store plaintext or silently broaden IAM. Any required IAM change needs its own
reviewed exact-key policy and AWS security pre-check before implementation.

### Task 1: Calendar Transactions and Retention

**Files:**
- Create: `backend/internal/model/calendar.go`
- Create: `backend/internal/repository/calendar.go`
- Test: `backend/internal/repository/calendar_test.go`

**Interfaces:**
- Models: `CalendarEvent`, `CalendarMutation`, `CalendarSyncRequest`,
  `CalendarSyncReceipt`, `CalendarSourceStatus`, `CalendarEventPage`,
  `CalendarSourcePage`, `CalendarQuery`.
- JSON event fields match the UI plan's Task 2. Upsert input additionally requires
  `externalId`; `eventId` is server-derived. Mutation shape is
  `{ op: 'upsert', event: CalendarEventInput }` or `{ op: 'remove', externalId }`.
- `CalendarSyncReceipt`: `batchId`, `revision`, `upserted`, `removed`, `completed`.
- `CalendarSourceStatus`: `sourceId`, `provider`, `revision`, `lastAttemptAt`,
  optional `lastCompletedAt`, optional completed window boundaries.
- `CalendarQuery`: `From`, `To`, optional `AccountID`, `Limit`, `Cursor`.
- Repository: `ApplyCalendarBatch(ctx context.Context, userID, sourceID string,
  batch CalendarWriteBatch) (*model.CalendarSyncReceipt, error)`;
  `GetCalendarBatchReceipt(ctx context.Context, userID, sourceID, batchID string)
  (*CalendarBatchReceipt, error)`;
  `ListCalendarEventRows(ctx context.Context, userID string, query model.CalendarQuery)
  (*CalendarRowPage, error)`;
  `ListCalendarSources(ctx context.Context, userID, cursor string, limit int32)
  (*model.CalendarSourcePage, error)`.
- Repository-private `CalendarWriteBatch` contains validated source CAS values,
  normalized payload digest, encrypted canonical rows, event identities, TTLs,
  and the receipt. The repository derives old/new reference keys through
  consistent canonical reads within ApplyCalendarBatch before its transaction.
  `CalendarRowPage` contains canonical encrypted rows and an opaque continuation,
  never decrypted text.

- [x] Write failing repository tests: identical batch retries return one receipt;
  same ID/different digest conflicts; stale source revision conflicts atomically;
  reschedule replaces the old chronological reference; duplicate external
  identities are rejected; no missing-item inference occurs on incomplete input.
- [x] Add assertions for caller/query-bound cursors, retained empty-page
  continuation, expired rows hidden before TTL cleanup, canonical reference
  revalidation, the 93-day overlap lookback, and event/reference 90-day versus
  receipt seven-day expiration.
- [x] Run the repository calendar tests with the available Go executable;
  confirm failures reflect missing behavior.
- [x] Implement owner-scoped canonical/source/reference/receipt keys using the
  Go expression builder and conditional transactions. Keep every batch below
  transaction item limits; bound read work to five query pages and retain cursor.
  Source revision serializes all mutations for that source.
- [x] Fetch canonical rows consistently before deriving reference replacements;
  source CAS protects against concurrent moves. Keep removed identities guarded
  by the source fence, and receipts free of event text.
- [x] Run focused tests, then repository tests; expect pass.

### Task 2: Encrypted Private Calendar APIs

**Files:**
- Create: `backend/internal/service/calendar.go`
- Create: `backend/internal/service/calendar_crypto.go`
- Create: `backend/internal/handler/calendar.go`
- Modify: `backend/cmd/api/main.go`
- Test: `backend/internal/service/calendar_test.go`
- Test: `backend/internal/service/calendar_crypto_test.go`
- Test: `backend/internal/handler/calendar_test.go`

**Interfaces:**
- `CalendarService.Sync(ctx context.Context, userID, sourceID string,
  request model.CalendarSyncRequest) (*model.CalendarSyncReceipt, error)`.
- `CalendarService.ListEvents(ctx context.Context, userID string,
  query model.CalendarQuery) (*model.CalendarEventPage, error)`.
- `CalendarService.ListSources(ctx context.Context, userID, cursor string,
  limit int32) (*model.CalendarSourcePage, error)`.
- `CalendarCipher.Encrypt(ctx context.Context, userID, sourceID, eventID string,
  plaintext []byte) (string, error)` and matching `Decrypt(..., ciphertext string)
  ([]byte, error)` bind purpose/user/source/event as KMS encryption context.
- Handler methods `Sync`, `ListEvents`, `ListSources` use existing verified auth,
  sentinel errors, shaped errors, and the three routes in the spec.

- [x] Write failing tests for unauthorized requests, another member's calendar,
  forged caller IDs, guessed source IDs, inaccessible account assignment/filter,
  stale references, and removal of account membership. Do not reveal private titles.
- [x] Add validation tests at exact boundaries: 20/21 events, 64 KiB/one byte over,
  512-byte title/external IDs, 256-byte location, 93-day query window, malformed
  JSON/unknown fields, repeated IDs, reversed times, timezone failures, recurring
  occurrence identity, DST and exclusive all-day end. Reject entire invalid batch.
- [x] Add crypto tests: absent key and KMS error fail closed, ciphertext only in
  storage, wrong context cannot decrypt, receipt retry compares the normalized
  logical digest rather than randomized ciphertext. Assert payload remains under
  the KMS plaintext limit after JSON encoding.
- [x] Run focused service/handler tests and confirm intended failures.
- [x] Implement service validation, current-account authorization, stable event
  identity, encryption, source-revision/receipt handling, and chronological reads.
  Expiration, cancellations, ongoing/all-day events follow the spec. No event body,
  secret, private text, or full request goes into logs.
- [x] Register only authenticated routes in the existing chi auth group; cap
  original request bytes and encoded result bytes before publication. Shorten a
  read page only at event boundaries while retaining real continuation.
- [x] Run backend `go test ./... -count=1` and `go vet ./...` using the verified
  executable, including cmd packages. No infrastructure activation claim.

### Task 3: Local-Only MCP Calendar Tools

**Files:**
- Create: `mcp-server/src/calendar.ts`
- Modify: `mcp-server/src/index.ts`
- Modify: `mcp-server/src/remote-tools.ts`
- Modify: `mcp-server/src/api.ts`
- Create: `mcp-server/test/calendar.test.mjs`
- Modify: `mcp-server/test/http-transport.test.mjs`
- Modify: `mcp-server/scripts/check-bundle.mjs`

**Interfaces:**
- `CALENDAR_TOOLS: Tool[]` and `CALENDAR_TOOL_NAMES: string[]`.
- `callCalendarTool(name: string, args: Record<string, unknown>, api: TtobakApi):
  Promise<CallToolResult>` uses the existing exported API client type.
- Exact names: `ttobak_sync_calendar_events`, `ttobak_list_calendar_events`,
  `ttobak_calendar_sync_status`.
- Sync arguments contain `sourceId` plus CalendarSyncRequest fields; list/status
  arguments mirror the bounded server query. Results forward actual pages or
  receipts; never synthesize success or continuation.

- [x] Write failing tests for tool schemas, unknown-field rejection, UTF-8 byte
  bounds, exact API route/body, receipt forwarding, repeated ambiguous requests
  retaining batch identity/revision, and non-success API errors staying errors.
- [x] Add HTTP tests for both discovery exclusion and explicit tools/call
  rejection. Assert no connector-private-api allowlist or approval binding changes.
- [x] Run `cd mcp-server && npm run build && node --test test/calendar.test.mjs`;
  confirm intended failures.
- [x] Implement the tool module and stdio dispatch using existing auth and
  CloudFront API configuration. Enforce 32,000-byte serialized results and bounded
  original HTTP bytes; never truncate a success receipt into an ambiguous result.
- [x] Add names to LOCAL_ONLY_TOOLS and verify the server factory's transport
  dispatch guard actually enforces it, not just discovery.
- [x] Run `cd mcp-server && npm test`. Extend existing bundle checks to run the
  same calendar fixtures against the standalone stdio bundle; expect parity.
  Follow the existing artifact distribution workflow, without deployment.

### Task 4: Local Outlook Continuous-Sync Guide

**Files:**
- Create: `docs/runbooks/local-outlook-calendar-sync.md`
- Modify: `frontend/src/lib/mcpGuide.ts`
- Modify: `frontend/src/components/McpGuide.tsx`
- Modify: `frontend/scripts/mcp-guide.test.mjs`
- Modify: `mcp-server/README.md`
- Modify: `docs/README.md`
- Modify: `docs/API-SPEC.md`

**Interfaces:**
- `outlookSyncGuide(): ClientGuide` uses the existing guide data shape and exact
  Task 3 tool names. Keep local-only guidance distinct from HTTP setup.
- The runbook defines the local reader's normalized event/explicit-removal
  contract, stable source mapping, provider checkpoint ownership, and recurring
  task setup; it does not implement or auto-configure an Outlook connector.

- [x] Verify official documentation for the chosen local reader/host and provider
  semantics before documenting commands. Cover immutable occurrence IDs, calendar
  pagination, cancellations, all-day/timezones, token expiry, and read-only consent.
  If no host/OS is selected, retain clearly labeled executable/path placeholders
  and avoid claiming a working unattended installation.
- [x] Write guide tests: local stdio prerequisite and exact tool names appear;
  recurring execution is attributed to the local host/OS, not MCP itself; no
  Outlook password/browser-token copying instructions; HTTP clients cannot enable
  calendar writes.
- [x] Add synthetic protocol fixtures: retry creates one event, reschedule keeps
  identity, cancellation removes it, private reclassification removes previously
  imported data, recurrence is occurrence-level, and a failed page neither emits
  inferred deletions nor completes the sync.
- [x] Run focused guide/calendar tests; confirm intended failures.
- [x] Write the guide: dry run, minimum fields, explicit account mapping,
  past-seven/next-30-day window, manual acceptance, ten-minute nonoverlapping task,
  launchd/Task Scheduler templates, local checkpoint-after-ack rules, conflict
  reconciliation, sleep/offline/stale behavior, pause and uninstall.
- [x] Add a copyable host instruction and synthetic API/MCP example. Clearly
  state that the local reader, credentials, and scheduler require user setup;
  provider tokens/checkpoints stay local and never appear in TTOBAK logs.
- [x] Run frontend guide tests, MCP tests, and `python3 scripts/docs/check_docs.py`;
  update current API/docs references only after their source implementation exists.

### Task 5: Upcoming UI and End-to-End Verification

**Files:**
- Modify: `frontend/src/types/calendar.ts`
- Modify: `frontend/src/lib/api.ts`
- Create: `frontend/src/hooks/useCalendarEvents.ts`
- Modify: `frontend/src/components/workspace/UpcomingMeetings.tsx`
- Modify: `frontend/src/components/workspace/HomeDashboard.tsx`
- Modify: `frontend/src/components/workspace/MeetingsWorkspace.tsx`
- Modify: `frontend/src/components/AccountDetailClient.tsx`
- Create: `frontend/scripts/calendar-workspace.test.mjs`
- Modify: `frontend/scripts/workspace-browser-check.mjs`
- Create: `docs/research/2026-11-07-calendar-sync-validation.md`

**Interfaces:**
- `calendarApi.events(query, { signal? }): Promise<CalendarEventPage>` and
  `calendarApi.sources({ cursor?, limit? }, { signal? }): Promise<CalendarSourcePage>`.
- `useCalendarEvents({ from, to, accountId? })` returns `events`, `sources`,
  `state`, `nextCursor`, `loadMore`, `retry`; use existing auth identity and
  membership invalidation, no persistent plaintext browser calendar cache.
- Reuse the UI plan's Calendar types and UpcomingMeetings props unchanged.

- [x] Write failing tests for next/ongoing event selection, seven-day agenda,
  all-day separation, local timezone/date formatting, cancellation, account scope,
  no account-team exposure, and stable ascending pagination.
- [x] Assert never-synced, unavailable API, empty completed window, partial sync,
  failed read, and more-than-30-minute stale status are distinct. Clock/date
  fixtures must be injected; production must not use the prototype's fixed date.
- [x] Run focused tests and confirm intended failures.
- [x] Implement authenticated calendar fetching with abort and generation guards;
  wire private upcoming sections into Home, Meetings, and Accounts. Keep sync
  errors independent from recorded meetings and insights. Account linkage never
  invokes share or recording APIs.
- [x] Run a synthetic stdio-to-authenticated-API integration cycle covering import,
  repeat, move, cancellation, lost response, stale revision, and another user.
  Verify persisted encrypted rows/reference consistency, not just mock tool calls.
- [x] Run backend all-package tests/vet, MCP tests/bundle parity, frontend
  lint/unit/build, changed infra checks, browser matrix, and docs checks. Record
  what passed and any unrelated baseline failures.
- [x] Obtain a fresh security/correctness review. Report fixture acceptance
  separately from real Outlook/manual scheduler acceptance, which requires the
  user's local reader. Do not claim production synchronization or deployment.

## Self-Review

All spec limits, owner boundaries, source CAS, encryption, retention, stdio-only
tools, guide requirements, and UI states have owning tasks. Review Focus cases
are assigned above. KMS prerequisite and actual local Outlook acceptance remain
explicit operational gates, not assumptions that tests or documentation satisfy.
