# Account Hierarchy and Meeting Filters Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Persist account parent relationships and support hierarchical, multi-account checkbox filtering of meetings.

**Architecture:** Optional parent IDs extend the existing account META records. A
dedicated hierarchy service/repository path validates and conditionally updates
ancestry. Meeting filters accept normalized explicit account-ID sets across all
existing listing streams. The frontend builds one reusable visible-account tree
for account navigation, parent pickers, and checkbox selection.

**Tech Stack:** Go Lambda, DynamoDB AWS SDK v2 expression builder, Next.js 16,
TypeScript, Tailwind v4.

**Spec:** `docs/superpowers/specs/2026-09-11-account-hierarchy-and-meeting-filters.md`

## Global Constraints

- No inferred access inheritance and no production seed/migration writes.
- Preserve legacy root accounts and legacy `accountId` consumers.
- Maximum 100 distinct filter IDs; bound ancestry validation to 64 nodes.
- Atomic ancestor checks and partial parent updates; no whole-item replacement.
- Use `/usr/local/go/bin/go`; stdlib tests; frontend lint/build only.
- Worktree: `/home/atomoh/ttobak/.worktrees/account-hierarchy`.
- Base: `origin/main` at `97b2cf1`. PR #183 and root `AGENTS.md` changes are separate.
- Parent checkboxes select accessible descendants; selection is expanded by the
  frontend before sending explicit IDs to the API.

### Task 1: Account parent persistence and management

**Files:** `backend/internal/model/account.go`,
`backend/internal/service/account.go`, new
`backend/internal/service/account_hierarchy.go` and tests, new
`backend/internal/repository/account_hierarchy.go` and tests,
`backend/internal/handler/account.go` and hierarchy tests,
`backend/cmd/api/main.go`.

**Interfaces:**

```go
// Add to Account, CreateAccountRequest, AccountResponse, AccountSummary.
ParentAccountID string // JSON/DynamoDB: parentAccountId,omitempty

type UpdateAccountParentRequest struct {
    ParentAccountID *string `json:"parentAccountId"` // required; "" explicitly detaches
}

// HTTP success payload:
// {"accountId":"child","parentAccountId":"parent"}
// PUT /api/accounts/{accountId}/parent is authenticated like existing routes.
```

- [ ] Add failing tests for owner-only moves, parent membership, missing parent,
  self/cycle rejection, detaching, legacy fields, and opposing concurrent moves.
- [ ] Add optional parent fields and a focused hierarchy persistence interface
  so unrelated account/document mocks need not implement new capabilities.
- [ ] Implement fresh ancestry reads and a transaction with child creation or
  partial update, parent membership check, and conditions on all ancestor links.
  Keep puts conditional; map conditional conflicts to a sentinel and retry
  from fresh reads at most three times.
- [ ] Expose create-with-parent and parent-update behavior in the service,
  responses, handler, and existing authenticated account routes.
- [ ] Verify focused service/repository/handler tests and format touched Go.

### Task 2: Multi-account meeting queries and cursor integrity

**Files:** `backend/internal/handler/meeting.go`,
`backend/internal/service/meeting.go`,
`backend/internal/service/meeting_team_list.go`,
`backend/internal/repository/dynamodb.go`, focused matching tests and new
filter helper files within these packages.

**Interfaces:**

```text
GET /api/meetings?accountIds=account-a,account-b&tab=all
GET /api/meetings?accountId=account-a                 # remains supported
GET /api/meetings?accountId=a&accountIds=b            # 400
```

```go
// Extend repository.ListMeetingsParams:
AccountIDs []string
// Keep AccountID for existing callers/tests.
```

- [ ] Add failing tests for two selected accounts and one excluded account in
  owned, direct-share, and inherited-team streams.
- [ ] Add parser/normalizer tests: empty selection, duplicate/reordered IDs,
  malformed IDs, limit, ambiguous legacy/new parameters, cursor context mismatch.
- [ ] Implement the OR expression for owned queries and canonical membership
  checks for direct/team shared results. Preserve date ordering, bounded
  pagination work, deduplication, and first-login team discovery hints.
- [ ] Bind every new multi-account cursor to user/tab/normalized IDs. Keep
  legacy API behavior and tests compatible without trusting opaque cursor data.
- [ ] Verify focused tests and format touched Go.

### Task 3: Shared tree UI, parent editor, and checkbox filter

**Files:** `frontend/src/types/meeting.ts`, `frontend/src/lib/api.ts`,
`frontend/src/lib/accountTree.ts`, `frontend/src/components/AccountTreePicker.tsx`,
`frontend/src/components/AccountsClient.tsx`,
`frontend/src/components/AccountDetailClient.tsx`,
`frontend/src/components/MeetingList.tsx`, `frontend/src/app/page.tsx`.

**Interfaces:**

```ts
// Add to Account and AccountSummary:
parentAccountId?: string;

// Add parentAccountId to accountApi.create's input.
accountApi.updateParent(id, { parentAccountId: string });

// Add to meetingsApi.list's input:
accountIds?: string[];
// Serialize as accountIds.join(','); preserve accountId for legacy consumers.
```

- [ ] Build a cycle-safe visible forest sorted by Korean name, with missing
  parents promoted to roots. Reuse it for navigation and selectors.
- [ ] Render expandable account rows. Add parent selection on create and an
  owner-only parent editor on detail with errors and refresh after save.
- [ ] Replace the meeting single-select with searchable hierarchical checkboxes,
  partial group state, removable chips, and clear all. Group selection expands
  into explicit IDs; cap the resulting selection at 100 with a clear message.
- [ ] Wire selected IDs through HomePage and API, reset/cancel pagination when
  filters change, and preserve URL text search, tabs, and tags.
- [ ] Run ESLint on changed files. Coordinator runs one integrated static build.

### Task 4: Integration, documentation, review and PR

**Files:** `docs/API-SPEC.md`, `docs/DESIGN-SPEC.md`,
`docs/decisions/ADR-036-account-hierarchy-and-meeting-filters.md`.

- [ ] Review each task's diff against the spec; resolve cross-package contracts
  and run its relevant checks before accepting it.
- [ ] Document parent management, unchanged permissions, tree UI, and filter
  cursor behavior in the existing specs and ADR.
- [ ] Run:

```bash
cd backend
/usr/local/go/bin/go test ./...
/usr/local/go/bin/go vet ./...
GOOS=linux GOARCH=arm64 /usr/local/go/bin/go build -tags lambda.norpc -o /tmp/ttobak-account-hierarchy-api ./cmd/api
cd ../frontend
npm run build
npm run lint
```

- [ ] Complete an independent whole-branch review, commit/push the feature,
  create its PR against main, and follow current-head AI review/CI. Never
  treat missing Kiro responses as successful review coverage; report the
  known quota block if it persists.
