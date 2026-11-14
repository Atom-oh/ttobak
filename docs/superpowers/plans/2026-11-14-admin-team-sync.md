# Admin Team Sync Implementation Plan

- Status: historical implementation plan; awaiting owner review (2026-11-14).

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** An administrator's Mac app previews and writes each `@amazon.com` pool user's team (directory manager alias) into Cognito `custom:department`.

**Architecture:** A new admin-only Go endpoint validates and writes one attribute per user. The local adapter (`ttobak-mcp team-sync run`) lists pool users through the admin API, looks each alias up with `builder-mcp`, stores a preview, and on apply posts the changes. The Mac app spawns the adapter in its own run slot; the admin panel shows a Team column, the preview and the result.

**Tech Stack:** Go 1.27 (chi, aws-sdk-go-v2 cognitoidentityprovider), TypeScript adapter (`mcp-server`, node:test), Rust/Tauri 2 (`mac-app/src-tauri`), Next.js/React (`frontend`).

**Spec:** `docs/superpowers/specs/2026-11-14-admin-team-sync-design.md`

## Global Constraints

- The API writes exactly one attribute, `custom:department`, and only through this endpoint. No other `custom:*` write anywhere.
- Team value pattern: `^[a-z0-9][a-z0-9-]{0,49}$`.
- Target users: `email_verified` true and email ending `@amazon.com` (case-insensitive). All others are never looked up and never written.
- Request limits: at most 200 items, body at most 64 KiB, no duplicate `userId`.
- Adapter limits: at most 500 users per run; preview valid 15 minutes; only user IDs and team values are sent to the API; no directory field other than alias, manager flag and manager alias is read, logged, stored or sent.
- Value rule: manager → own alias; non-manager → manager alias; not found or no manager → no proposal, reported `notFound`, value untouched.
- Authorization is the API's `RequireAdmin`; Mac commands add no trust.
- No IAM, CDK resource or Cognito schema change. No email in logs.
- Sentinel errors with `errors.Is`; API error shape `{ "error": { "code", "message" } }`.
- Repository text in concise English; UI copy in Korean, literal UTF-8.
- Each task is one PR-sized commit on a feature branch; add a `CHANGELOG.md` `## [Unreleased]` entry in the task that makes the change user-visible (Task 5).

## Review Focus

1. **Value changed after preview** (cc admin edits a department between preview and apply): the user is reported as a conflict and not written. Pinned in Task 1 (`TestTeamSync_ConflictWritesNothing`) and Task 3 (`apply sends the previewed value as expected`).
2. **Directory answers for a different person or without a manager** (renamed alias, top of org): no proposal, `notFound`. Pinned in Task 3 (`lookup rejects a mismatched login`, `no manager yields notFound`).
3. **One Cognito failure in a batch**: that item is rejected with `COGNITO_ERROR`, the rest continue, and the response says which. Pinned in Task 1 (`TestTeamSync_PerItemCognitoErrorContinues`).
4. **Midway session expired on the admin's Mac**: the run ends with `AUTH_EXPIRED_MIDWAY`, nothing is stored or sent, and the panel tells the admin to run `mwinit`. Pinned in Task 3 (`auth failure stops before any API write`) and Task 5 (message mapping test).
5. **Older Mac app or adapter with a newer SPA**: the button is hidden when the command is missing and the run reports `ADAPTER_OUTDATED` when the bundle lacks the command. Pinned in Task 4 (`supports_team_sync`) and Task 5 (`team sync control is hidden without the command`).

---

### Task 1: API endpoint and list field (Go)

**Files:**
- Modify: `backend/internal/model/request.go` (admin user types, near `AdminUserSummary`)
- Modify: `backend/internal/service/user_admin.go`
- Modify: `backend/internal/handler/user_admin.go`
- Modify: `backend/cmd/api/main.go` (admin-only group, next to the other `/api/settings/users` routes)
- Test: `backend/internal/service/user_admin_test.go`, `backend/internal/handler/user_admin_test.go`
- Docs: `docs/API-SPEC.md`, `docs/decisions/ADR-007-sign-up-identity-and-admin.md`, and the comments in `infra/lib/auth-stack.ts` and `infra/lib/ai-stack.ts` stating the service never writes `custom:*`

**Interfaces:**
- Produces:
  - `AdminUserSummary.Department string` with JSON `department` (always present, empty when unset).
  - `model.TeamSyncItem{UserID, Team, Expected string}` (JSON `userId`, `team`, `expected`), `model.TeamSyncRequest{Items []TeamSyncItem}`, `model.TeamSyncRejection{UserID, Code string}`, `model.TeamSyncResponse{Updated []string; Unchanged int; Conflicts []string; Rejected []TeamSyncRejection}` (JSON `updated`, `unchanged`, `conflicts`, `rejected`; slices never null).
  - `func (s *UserAdminService) SyncTeams(ctx context.Context, actorUserID string, items []model.TeamSyncItem) (*model.TeamSyncResponse, error)`; returns the existing `service.ErrInvalidInput` (wrapped) for request-level faults (empty, over 200, duplicate `userId`); the handler maps it to 400 `BAD_REQUEST`.
  - `func ValidTeamValue(v string) bool` in `service` (pure).
  - `POST /api/settings/users/team-sync` → 200 with `TeamSyncResponse`.
  - Rejection codes: `NOT_FOUND`, `NOT_DIRECTORY_USER` (unverified or non-`@amazon.com`), `INVALID_TEAM`, `COGNITO_ERROR`.

- [ ] **Step 1: Write failing service tests** in `user_admin_test.go`, using `fakeCognitoAdminAPI` (extend it to record `AdminUpdateUserAttributes` inputs and to fail for a chosen username):

```go
func TestValidTeamValue(t *testing.T) // ok: "mihong", "a", "team-1", 50 chars; bad: "", "Mihong", "-x", "a b", "a=b", "a,b", 51 chars
func TestListUsers_IncludesDepartment(t *testing.T) // custom:department "mihong" → Department "mihong"; absent → ""
func TestTeamSync_WritesOnlyDepartment(t *testing.T) // one item; recorded input has exactly one attribute, Name "custom:department", Value "mihong"; Updated == [userId]
func TestTeamSync_ConflictWritesNothing(t *testing.T) // current "hana", expected "", team "mihong" → Conflicts == [userId], zero update calls
func TestTeamSync_UnchangedIsCounted(t *testing.T) // current == team → Unchanged 1, zero update calls
func TestTeamSync_RejectsNonDirectoryUsers(t *testing.T) // table: gmail address; amazon.com with email_verified false; "X@AMAZON.COM" verified is accepted
func TestTeamSync_RejectsInvalidTeamAndMissingUser(t *testing.T) // INVALID_TEAM; UserNotFoundException → NOT_FOUND
func TestTeamSync_PerItemCognitoErrorContinues(t *testing.T) // 3 items, 2nd update fails → Rejected[0].Code "COGNITO_ERROR", Updated has 1st and 3rd
func TestTeamSync_RequestLimits(t *testing.T) // 0 items, 201 items, duplicate userId → request-level error, zero Cognito calls
```

- [ ] **Step 2: Run** `(cd backend && /usr/local/go/bin/go test ./internal/service -run 'TeamSync|ValidTeamValue|ListUsers_IncludesDepartment' -count=1)`. Expected: compile failure on the missing symbols.

- [ ] **Step 3: Implement** the model types, `ValidTeamValue`, the `custom:department` case in `ListUsers`' attribute switch, and `SyncTeams`. Per item: `AdminGetUser` → checks in the order user exists, directory user, team pattern, conflict (`current != Expected`), unchanged (`current == Team`), then `AdminUpdateUserAttributes`. Log one line per write through the existing audit style (`auditAdminAction` neighbour): actor sub, target sub, old value, new value.

- [ ] **Step 4: Run** the Step 2 command. Expected: PASS.

- [ ] **Step 5: Write failing handler tests** in `handler/user_admin_test.go`:

```go
func TestTeamSyncHandler_ReturnsResult(t *testing.T) // 200, JSON has updated/unchanged/conflicts/rejected arrays (not null)
func TestTeamSyncHandler_RejectsOversizedBody(t *testing.T) // 64 KiB + 1 → 400 BAD_REQUEST
func TestTeamSyncHandler_RejectsMalformedAndLimitErrors(t *testing.T) // invalid JSON, unknown field, 201 items → 400 with the standard error shape
```

- [ ] **Step 6: Implement** `func (h *UserAdminHandler) SyncTeams(w http.ResponseWriter, r *http.Request)`: `http.MaxBytesReader` at `64 << 10`, `DisallowUnknownFields`, actor from `middleware.GetUserID`. Register `r.Post("/api/settings/users/team-sync", userAdminHandler.SyncTeams)` inside the `RequireAdmin` group.

- [ ] **Step 7: Run** `(cd backend && /usr/local/go/bin/go test ./... -count=1 && /usr/local/go/bin/go vet ./...)`. Expected: PASS. The route count in `scripts/docs/check_docs.py` output rises by one after the API-SPEC update.

- [ ] **Step 8: Update docs**: API-SPEC (endpoint, limits, codes, `department` field); ADR-007 rule rewritten in place to "the service writes `name`, and `custom:department` only through the admin team sync"; the two infra comments. Run `python3 scripts/docs/check_docs.py`. Expected: passes.

- [ ] **Step 9: Commit** `feat(api): admin team sync endpoint and department in user list`.

### Task 2: Adapter API client and value rule (TypeScript, pure)

**Files:**
- Modify: `mcp-server/src/api.ts`
- Create: `mcp-server/src/team-sync/rule.ts`
- Test: `mcp-server/test/team-sync-rule.test.mjs`, additions to `mcp-server/test/api.test.mjs`

**Interfaces:**
- Produces:
  - `TtobakApi.adminListUsers(): Promise<{ users: AdminUser[]; truncated?: boolean }>` (`GET /api/settings/users`), `AdminUser = { userId: string; email: string; emailVerified: boolean; department: string }` (other fields ignored).
  - `TtobakApi.syncTeams(body: TeamSyncBody): Promise<TeamSyncResult>` (`POST /api/settings/users/team-sync`), `TeamSyncBody = { items: { userId: string; team: string; expected: string }[] }`, `TeamSyncResult = { updated: string[]; unchanged: number; conflicts: string[]; rejected: { userId: string; code: string }[] }`. Refuse client-side above 200 items or 64 KiB.
  - `TEAM_PATTERN: RegExp` = `/^[a-z0-9][a-z0-9-]{0,49}$/`.
  - `directoryAlias(user: AdminUser): string | null`: lowercased local part when verified, domain is `amazon.com` and the local part matches `TEAM_PATTERN`; else `null`.
  - `interface DirectoryPerson { login: string; isManager: boolean; managerLogin: string | null }`.
  - `teamFor(alias: string, person: DirectoryPerson | null): string | null`.
  - `interface TeamPlan { changes: { userId: string; alias: string; from: string; to: string }[]; unchanged: number; notFound: string[]; skippedNonDirectory: number }`.
  - `planTeams(users: AdminUser[], people: Map<string, DirectoryPerson | null>): TeamPlan` (changes sorted by alias).

- [ ] **Step 1: Write failing tests**:

```js
test('directoryAlias accepts only verified amazon.com addresses', ...) // 'A@Amazon.com' verified → 'a'; unverified → null; 'a@gmail.com' → null; 'a.b+c@amazon.com' → null
test('manager is grouped with their own alias', ...)   // teamFor('mihong', {login:'mihong', isManager:true, managerLogin:'moonjong'}) === 'mihong'
test('non-manager gets the manager alias', ...)        // teamFor('atomoh', {login:'atomoh', isManager:false, managerLogin:'mihong'}) === 'mihong'
test('no manager yields notFound', ...)                // managerLogin null and not a manager → null; person null → null
test('lookup rejects a mismatched login', ...)         // teamFor('atomoh', {login:'other', ...}) === null
test('manager alias failing the pattern yields null', ...) // managerLogin 'Bad Alias' → null
test('planTeams separates changes, unchanged, notFound and skipped', ...) // 4 users: change, same value, missing person, gmail → counts 1/1/['x']/1
test('syncTeams refuses more than 200 items before sending', ...)
```

- [ ] **Step 2: Run** `(cd mcp-server && npm run build && node --test test/team-sync-rule.test.mjs)`. Expected: build fails on missing module.
- [ ] **Step 3: Implement** `rule.ts` and the two API methods (follow `syncPerformance` for the request call).
- [ ] **Step 4: Run** the Step 2 command plus `node --test test/api.test.mjs`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(mcp): team sync rule and admin API client`.

### Task 3: Adapter command `team-sync run` (TypeScript)

**Files:**
- Create: `mcp-server/src/team-sync/directory.ts`, `mcp-server/src/team-sync/run.ts`
- Modify: `mcp-server/src/index.ts` (CLI dispatch beside `intake`/`report`/`setup`), `mcp-server/src/intake/errors.ts` (codes), `mcp-server/scripts/check-bundle.mjs`
- Test: `mcp-server/test/team-sync-run.test.mjs`
- Docs: `docs/decisions/ADR-003-mcp-access.md`

**Interfaces:**
- Consumes: Task 2 exports; `signIn`, `Selection`, `IntakeEnvironment` (`intake/run.ts`); `IntakeStore`; `McpSession`, `resolveCommand`.
- Produces:
  - `DIRECTORY_TOOL = 'ReadInternalWebsites'`, `DIRECTORY_URL_PREFIX = 'https://phonetool.amazon.com/users/'`.
  - `interface Directory { lookup(alias: string): Promise<DirectoryPerson | null>; close(): Promise<void> }`.
  - `openDirectory(environment): Promise<Directory>`: resolves `builder-mcp` on the environment PATH (missing → `IntakeError('DIRECTORY_UNAVAILABLE')`), opens an `McpSession` with `allowedTools: [DIRECTORY_TOOL]`, 30 s call timeout.
  - `parsePerson(alias: string, toolText: string): DirectoryPerson | null`: reads only `content.status`, `content.content.login`, `is_manager`, `manager.login`; a non-success status that is not an auth failure → `null`.
  - `runTeamSync(args: string[], environment: TeamSyncEnvironment): Promise<number>`, where `TeamSyncEnvironment extends IntakeEnvironment { openDirectory?: () => Promise<Directory> }` for tests.
  - CLI: `ttobak-mcp team-sync run` (preview) and `ttobak-mcp team-sync run --apply --preview-id <32 hex>`. Help text contains the literal `ttobak-mcp team-sync run`.
  - Final stdout JSON line, preview: `{ ok: true, previewId, changes, unchanged, notFound, skippedNonDirectory, truncated }`; apply: `{ ok: true, updated, unchanged, conflicts, rejected }`; failure: `{ ok: false, code }`. Progress on stderr as `team-sync: <n>/<total>` only (no alias).
  - Stored preview: `team-sync-preview.json` in the intake store: `{ previewId, createdAt, changes }`.
  - New codes: `DIRECTORY_UNAVAILABLE`, `NOT_ADMIN` (API 403), `PREVIEW_EXPIRED` (missing, ID mismatch, or older than 15 minutes), `TOO_MANY_USERS` (over 500 directory users).

- [ ] **Step 1: Write failing tests** with a fake API and fake directory injected through the environment:

```js
test('parsePerson reads only the three fields', ...) // fixture with phone and title; result deep-equals {login, isManager, managerLogin}
test('preview stores changes and prints no directory extras', ...) // stdout line has no 'job_title'/'mobile'; store file mode 0600
test('lookup URL is the fixed prefix plus the alias', ...) // recorded tool input deep-equals { inputs: ['https://phonetool.amazon.com/users/atomoh'] }
test('apply sends the previewed value as expected', ...) // body item { userId, team: to, expected: from }; preview file removed afterwards
test('apply refuses an old or unknown preview', ...) // createdAt 16 minutes ago → code PREVIEW_EXPIRED, zero API writes; wrong id likewise
test('auth failure stops before any API write', ...) // directory throws AUTH_EXPIRED_MIDWAY → exit non-zero, code AUTH_EXPIRED_MIDWAY, no preview stored
test('non-admin login ends with NOT_ADMIN', ...) // adminListUsers rejects with 403
test('more than 500 directory users ends with TOO_MANY_USERS', ...)
test('apply with no changes sends nothing', ...) // preview with empty changes → ok, updated []
```

- [ ] **Step 2: Run** `(cd mcp-server && npm run build && node --test test/team-sync-run.test.mjs)`. Expected: FAIL (missing module).
- [ ] **Step 3: Implement** `directory.ts` and `run.ts`. Sign in exactly as `report/run.ts` does (`store.read<Selection>('selection.json')`, `signIn(selection, environment, false, true)`). Look up sequentially; batch apply bodies in chunks of 200 and merge the results. Add the `team-sync` branch in `index.ts` and the bundle assertion `/ttobak-mcp team-sync run/` in `check-bundle.mjs`.
- [ ] **Step 4: Run** `(cd mcp-server && npm test)`. Expected: PASS, including the bundle check.
- [ ] **Step 5: Update** ADR-003 (new local command, the single allowlisted directory tool, what leaves the Mac). Run `python3 scripts/docs/check_docs.py`.
- [ ] **Step 6: Commit** `feat(mcp): team-sync run command`.

### Task 4: Mac commands (Rust)

**Files:**
- Create: `mac-app/src-tauri/src/team_sync_runner.rs`
- Modify: `mac-app/src-tauri/src/lib.rs` (module, `invoke_handler`, `manage`), `mac-app/src-tauri/build.rs` (COMMANDS), `mac-app/src-tauri/capabilities/default.json`
- Docs: `mac-app/CLAUDE.md` wire table

**Interfaces:**
- Consumes: `intake_runner::{node_launch, local_adapter, node_command, run_child}` and `IntakeState`, used as `report_runner::run_report` uses them.
- Produces:
  - `#[tauri::command] team_sync_run(mode: TeamSyncMode, preview_id: Option<String>) -> Result<(), String>`; `TeamSyncMode` is a serde enum `preview | apply`. Errors: `INVALID_ARGUMENT` (apply without an ID, preview with one, or an ID not matching `^[a-f0-9]{32}$`), `RUN_IN_PROGRESS`.
  - `#[tauri::command] team_sync_cancel() -> Result<bool, String>`.
  - `#[tauri::command] team_sync_get_state() -> TeamSyncStateView { running: bool, last_result: Option<Value>, progress: Vec<String> }` (camelCase on the wire) so the page recovers after a reload.
  - Events to the main window: `team-sync://progress` `{line}`, `team-sync://finished` `{mode, exitCode, result, code}`.
  - `fn team_sync_argv(mode: TeamSyncMode, preview_id: Option<&str>) -> Vec<String>` → `["team-sync","run"]` or `["team-sync","run","--apply","--preview-id",id]`.
  - `fn supports_team_sync(adapter: &[u8]) -> bool` (contains `ttobak-mcp team-sync run`); otherwise the run ends with `ADAPTER_OUTDATED`.
  - Timeout 10 minutes → `TEAM_SYNC_TIMEOUT`; cancel → `TEAM_SYNC_CANCELLED`; SIGTERM then SIGKILL after 3 s; killed at app quit like other runs.

- [ ] **Step 1: Write failing unit tests** in `team_sync_runner.rs`:

```rust
#[test] fn argv_for_preview_and_apply()        // exact vectors above
#[test] fn preview_id_must_be_32_lower_hex()   // "", 31 chars, uppercase, "../x" rejected; 32 hex accepted
#[test] fn apply_requires_id_and_preview_refuses_one()
#[test] fn supports_team_sync_needs_the_marker() // b"... ttobak-mcp team-sync run ..." true; b"ttobak-mcp intake setup" false
```

Also extend `command_acl_tests` expectations by adding the three commands to `build.rs` COMMANDS and `capabilities/default.json` (`allow-team-sync-run`, `allow-team-sync-cancel`, `allow-team-sync-get-state`).

- [ ] **Step 2: Run** `(cd mac-app/src-tauri && cargo test team_sync)`. Expected: FAIL (missing module).
- [ ] **Step 3: Implement** the module on the pattern of `report_runner.rs` (own `TeamSyncState` slot, no extra environment variables beyond the intake child's cleared environment).
- [ ] **Step 4: Run** `(cd mac-app/src-tauri && cargo fmt --check && cargo clippy --all-targets -- -D warnings && cargo test)`. Expected: PASS. No CI covers this module; state that in the PR.
- [ ] **Step 5: Add** the three commands to the `mac-app/CLAUDE.md` wire table.
- [ ] **Step 6: Commit** `feat(mac): team sync commands`.

### Task 5: Admin panel (frontend)

**Files:**
- Create: `frontend/src/lib/teamSync.ts` (pure helpers), `frontend/src/hooks/useTeamSync.ts`
- Modify: `frontend/src/lib/tauri.ts`, `frontend/src/lib/api.ts` (admin user type gains `department: string`), `frontend/src/components/UserManagement.tsx`
- Test: `frontend/scripts/team-sync.test.mjs`
- Docs: `docs/DESIGN-SPEC.md`, `CHANGELOG.md`

**Interfaces:**
- Consumes: Task 1 `department` field; Task 4 commands and events; Task 3 final JSON shapes.
- Produces:
  - `tauri.ts`: `teamSyncRun(mode: 'preview' | 'apply', previewId?: string)`, `teamSyncCancel()`, `teamSyncGetState()`, `hasTeamSyncCommand(): Promise<boolean>` (false in a browser and when the command is not registered).
  - `teamSync.ts`: `teamSyncVisible(isAdmin: boolean, hasCommand: boolean): boolean`; `teamSyncMessage(code: string | null): string`; `canApply(preview: { changes: unknown[] } | null): boolean`.
  - Korean UI copy lives only in `teamSync.ts` as exported constants (this
    document stays English); tests compare against the constants, as
    `scripts/account-overview.test.mjs` does. Constant and meaning:
    - `TEAM_SYNC_BUTTON_LABEL` "Sync teams"; `teamSyncApplyLabel(n)` "Apply n changes"; `TEAM_COLUMN_LABEL` "Team"; `TEAM_EMPTY` an em dash;
    - `TEAM_SYNC_BUDGET_NOTE`: cc-on-bedrock department budgets follow this value;
    - `TEAM_SYNC_MESSAGES`, keyed by code:
      - `AUTH_EXPIRED_MIDWAY`: corporate sign-in expired; run `mwinit` in a terminal and retry;
      - `NOT_ADMIN`: runs only in a Mac app signed in as an administrator;
      - `PREVIEW_EXPIRED`: the preview expired; press sync again;
      - `ADAPTER_OUTDATED`: update the local adapter and retry;
      - `DIRECTORY_UNAVAILABLE`: `builder-mcp` was not found;
      - `RUN_IN_PROGRESS`: already running;
    - `teamSyncFallbackMessage(code)`: team sync failed, with the code in parentheses.

- [ ] **Step 1: Write failing tests** in `scripts/team-sync.test.mjs` (transpile `src/lib/teamSync.ts` with `typescript` and `vm`, as `scripts/account-overview.test.mjs` does):

```js
test('team sync control is hidden without the command', ...) // teamSyncVisible(true,false)===false; (false,true)===false; (true,true)===true
test('apply is offered only when there are changes', ...)    // canApply(null)===false; {changes:[]}===false; {changes:[{}]}===true
test('each known code maps to its message constant', ...)    // teamSyncMessage(code) === TEAM_SYNC_MESSAGES[code] for all six; unknown 'X' → teamSyncFallbackMessage('X'), which contains '(X)'; every constant is non-empty
```

- [ ] **Step 2: Run** `(cd frontend && node --test scripts/team-sync.test.mjs)`. Expected: FAIL (missing file).
- [ ] **Step 3: Implement** `teamSync.ts`, the `tauri.ts` wrappers and `useTeamSync` (state: idle / previewing / preview / applying / done / error; subscribes to the two events; restores from `teamSyncGetState` on mount), then the panel: Team column from `department`; the button (shown by `teamSyncVisible`); a preview list of `alias: from → to` with `—` for empty, the not-found aliases, the unchanged count and the note; the result list of updated users, conflicts and rejections; refetch the user list after apply. Use existing tokens from `globals.css` and the panel's existing table and button styles.
- [ ] **Step 4: Run** `(cd frontend && npm run lint && npm run test:unit && npm run build)`. Expected: PASS.
- [ ] **Step 5: Update** `docs/DESIGN-SPEC.md` (panel column and sync flow) and add the `CHANGELOG.md` `## [Unreleased]` entry. Run `python3 scripts/docs/check_docs.py`.
- [ ] **Step 6: Commit** `feat(web): team column and admin team sync`.

### Task 6: End-to-end check on the owner's Mac

Not automated; needs the corporate network and an admin login.

- [ ] Deploy in the spec's rollout order: API, adapter bundle, SPA, then a signed Mac build (`npm run build:signed`).
- [ ] Preview on the live pool. Expected after the 2026-11-14 backfill: zero changes, 35 unchanged, the two non-`@amazon.com` users and the release-test account counted as skipped or not found.
- [ ] Change one test user's `custom:department` by hand, preview again, apply. Expected: one change shown, one updated, value restored; the user list shows it.
- [ ] As a non-admin login, confirm the button is absent and a direct `POST /api/settings/users/team-sync` returns 403.
- [ ] Record the results in the PR description, including that Mac capture paths were not touched.
