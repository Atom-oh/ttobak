# Account Contacts Implementation Plan

Status: historical implementation plan; current behavior is documented in the API and design specifications.

> **For agentic workers:** Apply the project security requirements and test behavior at each boundary.

**Goal:** Let customer account teams manage Remember contacts and use them as speaker suggestions.

**Architecture:** Direct-account membership gates paginated contacts and team suggestions. The browser imports mapped CSV/XLSX rows into one account with duplicate checks. MCP forwards bounded structured operations through the same authorized API.

**Tech Stack:** Go, DynamoDB, Next.js, TypeScript, MCP.

**Spec:** `docs/superpowers/specs/2026-11-06-account-contacts-design.md`

## Global constraints

- Direct member only; account relation and meeting share never grant contacts.
- Each mutation atomically checks the current account and membership.
- Every imported Remember field is preserved within explicit bounds.
- No server-local file paths, public endpoints, contact logging or automatic speaker assignment.
- Preserve the user's original worktree and avoid deployment.

## Review focus

- Revocation between authorization and write: conditional transaction test.
- Cursor or contact from a different account: reject or isolate by account.
- A meeting shared outside the account: no suggestions; manual names still work.
- Late responses after switching account or user: no stale details displayed.
- CSV quoting, Korean Remember headers, duplicates and partial failures: preview and report accurately.

### Task 1: Authorized contacts API and team suggestions

**Files:** `backend/internal/model/`, `backend/internal/repository/`, `backend/internal/service/`, `backend/internal/handler/`, `backend/cmd/api/main.go`, `docs/API-SPEC.md`.

- [ ] Write failing service/repository/handler tests for CRUD, invalid inputs, membership, revocation and pagination.
- [ ] Implement the routes and types from the spec, with account-bound cursor and atomic membership checks.
- [ ] Verify the tests and Go suite; document the API.

### Task 2: Account contact UI, import and meeting suggestions

**Files:** `frontend/src/types/meeting.ts`, `frontend/src/lib/api.ts`, new contact/import UI components and helpers, `frontend/src/components/AccountDetailClient.tsx`, `frontend/src/components/meeting/SpeakerMapEditor.tsx`, `frontend/src/app/meeting/[id]/MeetingDetailClient.tsx`, frontend unit tests, `docs/DESIGN-SPEC.md`.

- [ ] Test header mapping, quoted CSV, duplicate normalization and stale-account protection.
- [ ] Implement direct-member address-book CRUD, mapped CSV/XLSX preview and explicit import with per-row results.
- [ ] Offer team and contact names in the speaker editor, with pagination and preserved free text.
- [ ] Run frontend lint, unit tests and production build.

### Task 3: MCP contacts

**Files:** `mcp-server/src/`, `mcp-server/test/`, MCP/API documentation.

- [ ] Test bounded authenticated list and structured creation/import, denied access, partial failures and both transports.
- [ ] Register tools using existing request-scoped API clients; no server file paths or PII in logs.
- [ ] Run `npm test` in `mcp-server` and inspect final security boundaries.
