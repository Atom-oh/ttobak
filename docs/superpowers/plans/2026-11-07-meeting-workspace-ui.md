# Meeting Workspace UI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Status:** Historical plan recorded and authorized for subagent-driven
implementation on 2026-11-07. Checklists track execution, not deployment.

**Goal:** Implement the approved Home, Meetings, and Accounts design without losing existing features or authorization boundaries.

**Architecture:** Separate the home dashboard from meeting history and share small,
bounded display components. Reuse existing authenticated account/meeting APIs;
upcoming sections expose a truthful setup state until the companion calendar
plan supplies data.

**Tech Stack:** Existing Next.js, React, TypeScript, Tailwind, node:test, CDK frontend routing.

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

- A late response after account/user change must never repaint previous private data: Task 2.
- Local sorting/filtering must not imply all paginated history was searched: Task 1.
- Broken insights must not hide account meetings, documents, or navigation: Task 3.
- Legacy home search and account deep links must survive static routing: Task 2.
- Long malicious previews and narrow screens must remain safe and bounded: Tasks 1 and 4.

---

## Execution Preconditions

- Obtain plan approval and the execution-method choice before product edits.
- Ask consent for an isolated worktree; use detached current main unless a branch
  is explicitly authorized. Preserve all original dirty files and other worktrees.
- Read current root/scoped guides and compare main with reviewed revision
  `dd3f2f7479f366695eb70fb2c1beccf58a4847ca` before executing.
- Run baseline frontend lint/unit/build and infra tests in that worktree. Record
  unrelated failures without fixing them. Use local available Chromium for visual
  checks; do not install a browser over the shared environment.

### Task 1: Compact, Account-Grouped Meeting History

**Files:**
- Create: `frontend/src/lib/meetingWorkspace.ts`
- Create: `frontend/src/components/workspace/MeetingRow.tsx`
- Modify: `frontend/src/components/MeetingList.tsx`
- Test: `frontend/scripts/meeting-workspace.test.mjs`

**Interfaces:**
- Consumes: `Meeting` and the existing MeetingList props/callbacks.
- Produces: `meetingPreview(content: string): string`.
- Produces: `selectMeetingRows(meetings: Meeting[], options: MeetingViewOptions): Meeting[]`,
  where options contains `sort: 'newest' | 'oldest'`, `query: string`,
  `statuses: Meeting['status'][]`, optional `from`/`to` UTC boundaries.
- Produces: `groupMeetingRows(meetings: Meeting[]): { accountId: string | null; meetings: Meeting[] }[]`.
- Produces: `MeetingRow({ meeting, accountName, preview, onDelete })`, preserving
  existing detail navigation and delete permission checks.

- [x] Write failing tests using the existing TypeScript-transpile/node:test
  convention: out-of-order dates sort correctly; equal dates tie by meeting ID;
  invalid dates sort last; query/status/range filters intersect; unassigned account
  has its own group; input arrays are unchanged.
- [x] Add preview assertions: a 100,000-character input yields at most 180 code
  points, executable markup never becomes DOM/HTML, and supplementary Unicode
  characters are not split. Assert sort/filter UI states it covers loaded results
  when a cursor remains.
- [x] Run `cd frontend && node --test scripts/meeting-workspace.test.mjs`;
  confirm failure on the missing helper, not a broken test harness.
- [x] Implement bounded text helpers and compact semantic rows using existing
  tokens/icons. Replace list-card Markdown rendering only; leave detail previews
  elsewhere untouched. Add grouped/flat and newest/oldest controls.
- [x] Keep a 100-row display window, stable deduplication, explicit server-page
  loading, loaded-result count, and separate continuation/empty/error controls.
- [x] Run the focused test and existing `scripts/meeting-management.test.mjs`;
  expect pass. Inspect source diff for lost recording, sharing, and delete controls.

### Task 2: Home Dashboard and Meeting Route

**Files:**
- Create: `frontend/src/app/meetings/page.tsx`
- Create: `frontend/src/components/workspace/HomeDashboard.tsx`
- Create: `frontend/src/components/workspace/MeetingsWorkspace.tsx`
- Create: `frontend/src/components/workspace/UpcomingMeetings.tsx`
- Create: `frontend/src/types/calendar.ts`
- Modify: `frontend/src/app/page.tsx`
- Modify: `frontend/src/lib/navigation.ts`
- Modify: `frontend/src/components/layout/DesktopHeader.tsx`
- Modify: `infra/lib/frontend-stack.ts`
- Test: `frontend/scripts/workspace-navigation.test.mjs`
- Modify tests: `frontend/scripts/navigation.test.mjs`, `infra/test/frontend-stack.test.ts`

**Interfaces:**
- Consumes: Task 1 list presentation and existing auth/account/meeting APIs.
- Produces: `HomeDashboard()` and `MeetingsWorkspace()` as authenticated clients.
- Produces: `CalendarEvent`, `CalendarSourceStatus`, `CalendarEventPage`,
  `CalendarSourcePage` matching the companion plan; no calendar API calls yet.
- `CalendarEvent` has `eventId`, `sourceId`, `provider: 'outlook'`, `title`,
  `startsAt`, `endsAt`, `timeZone`, `isAllDay`, optional `startDate`, `endDate`,
  `location`, and `accountId`; page types contain the named array and nullable
  `nextCursor`. Source status matches the companion plan's Task 1.
- Produces: `UpcomingMeetings({ events, sources, state, onRetry })`,
  with state `'setup' | 'loading' | 'ready' | 'error'`, displaying only provided
  real data. Setup links to the MCP guide; there is no manual/demo create action.

- [x] Write failing route/state tests: Home `/` and Meetings `/meetings` are
  distinct active items; `/?q=hello` preserves query while routing to meetings;
  existing nav entries survive; `/meetings` maps to its exported HTML at the edge;
  unauthenticated users cannot render workspace data.
- [x] Add request-race assertions for the extracted clients: a delayed response
  from account A is ignored after selecting B; a user/membership change clears
  obsolete rows; failing a next page retains prior rows and continuation.
- [x] Run `cd frontend && node --test scripts/workspace-navigation.test.mjs scripts/navigation.test.mjs`;
  confirm intended failures.
- [x] Move existing authenticated list behavior into MeetingsWorkspace, retaining
  bootstrap-account hints, abort/generation guards, recording/upload actions, and
  invite handling. Keep the original login path in the root page.
- [x] Implement independent Home sections: recent meetings, selected-account
  insights, and upcoming setup. Reuse account cache; load insights only for the
  selected account. Preserve supported source links and explicit empty states.
- [x] Update desktop search, navigation, and edge knownPages together; preserve
  existing account/detail static routing and special insights nav handling.
- [x] Run focused tests, `cd frontend && npm run lint && npm run test:unit && npm run build`,
  and `cd infra && npm test`; expect pass or document unchanged baseline failures.

### Task 3: Account Master-Detail and Insights

**Files:**
- Modify: `frontend/src/components/AccountsClient.tsx`
- Modify: `frontend/src/components/AccountDetailClient.tsx`
- Create: `frontend/src/components/workspace/AccountWorkspaceSelector.tsx`
- Modify: `frontend/src/lib/accountTabs.ts`
- Modify: `frontend/src/lib/api.ts`
- Modify: `frontend/src/components/FieldInsightsSection.tsx`
- Test: `frontend/scripts/account-workspace.test.mjs`
- Modify tests: `frontend/scripts/account-tabs.test.mjs`

**Interfaces:**
- Consumes: `accountApi.list`, `accountApi.peekList`, `accountApi.meetings`,
  `accountApi.insights`, membershipRevision, and existing account detail routing.
- Produces: `AccountWorkspaceSelector({ accounts, selectedId, onSelect })`.
- Adds `'insights'` to `AccountTab`; existing tab href/parsing remains compatible.
- Extends account detail read methods with optional `{ signal?: AbortSignal }`
  without changing response types or callers that omit options.

- [x] Write failing tests: insights tab round-trips query parameters; billing
  remains runtime-gated; customer switching preserves the chosen compatible tab;
  one failing section does not suppress others.
- [x] Add assertions for permission and race cases: old customer responses are
  ignored, revocation clears cached details, account-linked private meetings are
  never substituted for account-shared history, existing insight types and
  unknown fallback render without missing source evidence being invented.
- [x] Run `cd frontend && node --test scripts/account-workspace.test.mjs scripts/account-tabs.test.mjs`;
  confirm intended failures.
- [x] Implement responsive account selection and the insights tab; split the
  current all-or-nothing secondary load into tab-scoped requests with independent
  retry/error states and generation checks. Keep member, creation, hierarchy,
  project, document, and billing controls.
- [x] Render compact history and evidence-linked insights with occurrence dates
  and next actions. Use existing shared-history and source authorization paths.
- [x] Run focused tests plus `npm run test:unit` from frontend; expect pass.

### Task 4: Visual and Performance Acceptance

**Files:**
- Modify only as needed: `frontend/src/app/globals.css`
- Create: `frontend/scripts/workspace-browser-check.mjs`
- Create: `docs/research/2026-11-07-meeting-workspace-ui-validation.md`

**Interfaces:**
- Consumes: built routes and deterministic synthetic fixture responses.
- Produces: a browser check accepting `WORKSPACE_PREVIEW_URL` and
  `PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH`, screenshots, and a dated evidence report.

- [x] Add browser assertions for the five spec widths, both themes, tab/group/
  sort/search actions, keyboard focus, initial/empty/error/retry states, account
  switch races, no page overflow, and at most 100 mounted meeting rows.
- [x] Run against the approved prototype as visual reference and the baseline
  application with the same 200-record fixture. Record list-render duration and
  DOM counts; save no real user data.
- [x] Run against the implementation, fix only UI regressions in this scope,
  and record measured differences without claiming live-server improvement.
- [x] Run final frontend lint/unit/build, infra tests/synth for route changes,
  and `python3 scripts/docs/check_docs.py`. Record exact outcomes.
- [x] Obtain a fresh implementation review; report which UI states are real and
  that upcoming data remains setup-only until the calendar companion is complete.
  Do not deploy or commit.

## Self-Review

Spec coverage includes navigation, account insights, compact bounded rendering,
honest loaded-set sorting, and independent states. All five Review Focus cases
have owning tests. Calendar types/props are the integration seam; ingestion and
local scheduler configuration are deliberately absent from this UI-only plan.
