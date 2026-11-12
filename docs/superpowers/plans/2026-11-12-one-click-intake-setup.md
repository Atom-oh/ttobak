# One-click Mac intake setup implementation plan

Status: historical implementation plan for 2026-11-12; current behavior is in
the code, `docs/runbooks/local-mcp-intake.md` and ADR-003.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** One [Setup] button in the Mac app that repairs PATH, refreshes the adapter, installs the effective org/personal skill package and missing MCP servers, then runs `intake setup`; plus Slack channel exclusions and a Bedrock guard for the report.

**Architecture:** TTOBAK stores org and personal package zips in S3 with metadata rows and computes the effective merge (Go). The adapter CLI (`ttobak-mcp setup plan|apply`, Node) downloads, re-validates and installs skills and servers with fixed argv. The Rust app owns PATH repair, adapter refresh, the approval prompt and per-step progress, and calls the CLI.

**Tech Stack:** Go 1.27 (chi, aws-sdk-go-v2 S3/DynamoDB, `archive/zip`), TypeScript Node 22 adapter (no new dependencies; `node:zlib`), Tauri 2/Rust, Next.js 16/React 19.

**Spec:** `docs/superpowers/specs/2026-11-12-one-click-intake-setup-design.md`

## Delivery

Seven PRs, each through the AI review/merge loop in `docs/runbooks/pr-review.md`.
Parts 1 and 2 are independent and ship first. Part 4 needs Part 3. Part 5 needs
Part 3 deployed. Part 6 needs Part 5. Part 7 needs Part 1.

| Part | PR | Deploy |
|---|---|---|
| 1 | Mac PATH repair, visible [Update], Bedrock guard | adapter bundle (frontend) + Mac rebuild |
| 2 | Report allowlist for v1.5.0 | adapter bundle |
| 3 | Skill package API | API Lambda, then StorageStack `--exclusively` |
| 4 | Web "Skill packages" settings | frontend; then the owner uploads v1.5.0 as org |
| 5 | Adapter `setup plan/apply` | adapter bundle |
| 6 | Mac [Setup] button | Mac rebuild |
| 7 | Slack channel exclusions | adapter bundle + Mac rebuild |

## Global Constraints

- Package limits: zip at most 20 MiB, at most 500 entries, total uncompressed at most 50 MiB; one top-level folder; no absolute paths, no `..`, no symlinks or special files; at least one skill; each `skills/<name>` has `SKILL.md`.
- Skill and server names match `^[a-z0-9][a-z0-9-]{0,63}$`; registries are `s3://` URLs; HTTP server URLs are `https://`.
- If `sa-performance-report` is present it must contain every `SKILL_LAYOUT` file (`mcp-server/src/report/claude.ts`).
- Never execute anything from a package. `*_EXTRA` arrays are not installed.
- Merge: personal skill replaces the same-named org skill; server lists are unioned; nothing is removed.
- Skills are written only under `~/.claude/skills/<name>/` and backups only under `~/.claude/skills-backup/<name>-<timestamp>/`; containment is checked before canonicalization.
- MCP installs use fixed argv, never a shell: `toolbox registry add <s3url>`, `toolbox install <name>`, `claude mcp add -s user <name> -- <name>`, `aim mcp install --clients claude-code <name>`, `claude mcp add -s user -t http <name> <url>`.
- New registries, servers and HTTP URLs need the user's [Install] once per Mac; approvals are stored locally per item.
- PATH repair appends, when they exist and are missing: `~/.toolbox/bin`, `~/.aim/mcp-servers`, `~/.local/bin`. Shell startup files are never edited.
- No new unauthenticated route. Org upload and rollback are behind `middleware.RequireAdmin`. Request bodies are bounded.
- `~/.ttobak/server.mjs` stays the only adapter path, fetched only from `ADAPTER_URL`.
- Repository docs are English; UI copy is Korean. Exact copy lives in `docs/superpowers/plans/2026-11-12-one-click-intake-setup-ui-copy.json`; backticked camelCase names in UI steps are its keys. Tool parameters keep literal UTF-8.
- Do not commit the internal-mcp-setup package or its skill text. Tests use synthetic packages that mirror its layout and script grammar.
- Mac changes have no CI: run `cargo fmt --check && cargo clippy --all-targets -- -D warnings && cargo test` locally and validate on the owner's Mac.

## Review Focus

1. A zip entry that escapes the target (`../`, absolute path, symlink, a file named like a directory) must be rejected on the server and again on the Mac, and nothing is written. Tests: Task 3.1 `TestValidateSkillPackageRejectsUnsafeEntries`, Task 5.1 `zip reader rejects unsafe entries`.
2. A second [Setup] press with nothing new reports every step as skipped/current and creates no backup folder. Test: Task 5.3 `identical skill is current and makes no backup`; Task 6.2 `setup_is_idempotent_step_view`.
3. No `toolbox`/`aim`, or Midway expiring mid-install: the server step is skipped or fails with `AUTH_EXPIRED_MIDWAY`, skills are still installed and `intake setup` still runs. Test: Task 5.4 `missing toolbox skips the server step`, `auth failure stops remaining installs`.
4. Deleting the personal package returns the effective set to the org package; a personal skill never affects another user. Test: Task 3.2 `TestEffectivePackage*`, Task 3.3 `TestPersonalPackageIsOwnerScoped`.
5. A download whose sha256 differs from the metadata, or an expired download URL between plan and apply, fails the package step before any write. Test: Task 5.2 `sha mismatch fails before install`.

---

## Part 1: Mac PATH repair, visible [Update], Bedrock guard

### Task 1.1: PATH repair on every launch

**Files:**
- Modify: `mac-app/src-tauri/src/intake_runner.rs` (`login_path`, new `repair_path`, new command `intake_refresh_path`)
- Modify: `mac-app/src-tauri/src/lib.rs` (register command)

**Interfaces:**
- Produces: `pub(crate) fn repair_path(path: &str, home: &Path, is_dir: impl Fn(&Path) -> bool) -> String`; `pub(crate) async fn refresh_login_path(state: &IntakeState, home: &Path) -> Result<PathRefresh, &'static str>` with `pub struct PathRefresh { pub added: Vec<String>, pub captured: bool }` (serde camelCase); Tauri command `intake_refresh_path() -> PathRefresh`.

- [ ] **Step 1: Write the failing tests** in `intake_runner.rs` tests:
  - `repair_path_appends_missing_existing_dirs`: `repair_path("/usr/bin:/bin", home, |p| p.ends_with(".toolbox/bin") || p.ends_with(".local/bin"))` equals `"/usr/bin:/bin:<home>/.toolbox/bin:<home>/.local/bin"`.
  - `repair_path_keeps_present_dirs_once`: input already containing `<home>/.toolbox/bin` is unchanged.
  - `repair_path_result_still_parses`: output passes `parse_login_path`.
- [ ] **Step 2: Run** `cd mac-app/src-tauri && cargo test repair_path` — expect FAIL (not defined).
- [ ] **Step 3: Implement** `repair_path`; make `login_path` return `repair_path(cached_or_captured, home, Path::is_dir)` so the cached value from 2026-11-11 is repaired without any user action; `refresh_login_path` always recaptures, stores the repaired value as `loginPath`, and reports the appended dirs (`captured: false` when the fallback PATH was used).
- [ ] **Step 4: Run** `cargo fmt --check && cargo clippy --all-targets -- -D warnings && cargo test` — PASS.
- [ ] **Step 5: Commit** `fix(mac): repair the intake PATH with toolbox and aim dirs`.

### Task 1.2: [Update] always reports its result

**Files:**
- Modify: `mac-app/src-tauri/src/intake_runner.rs` (`intake_update_adapter`)
- Modify: `frontend/src/lib/tauri.ts`, `frontend/src/lib/intake.ts`, `frontend/src/components/IntakeSettings.tsx`
- Test: `frontend/scripts/intake.test.mjs` (existing file for `lib/intake.ts`; create if absent and add to `test:unit`)

**Interfaces:**
- Produces: `intake_update_adapter() -> AdapterUpdate` where `pub struct AdapterUpdate { adapter: AdapterView, changed: bool }`; TS `updateIntakeAdapter(): Promise<{ adapter: IntakeStateView['adapter']; changed: boolean }>`; `adapterUpdateMessage(changed: boolean): string`.

- [ ] **Step 1: Failing tests:** Rust `adapter_update_reports_unchanged_bytes` (install identical bytes into a temp dir via a new pure helper `fn adapter_changed(current: Option<&[u8]>, served: &[u8]) -> bool`); TS `adapterUpdateMessage(true)` is `adapterUpdated` and `adapterUpdateMessage(false)` is `adapterCurrent`; every `intake_update_adapter` error code (`network`, `adapter-invalid`, `save-failed`, `unavailable`, `RUN_IN_PROGRESS`) has a non-empty `intakeCodeLabel`.
- [ ] **Step 2: Run** `cargo test adapter_update` and `cd frontend && npm run test:unit` — FAIL.
- [ ] **Step 3: Implement.** Skip `install_adapter` when unchanged; the button sets the message on success too (today it clears the message and shows nothing, which matches the 2026-11-11 report). Log the error kind at `info`.
- [ ] **Step 4: Run** Rust checks and `cd frontend && npm run lint && npm run test:unit` — PASS.
- [ ] **Step 5: Commit** `fix(mac): show the adapter update result`.

### Task 1.3: Bedrock guard

**Files:**
- Modify: `mac-app/src-tauri/src/report_runner.rs` (`report_settings_set`, `ReportStateView`)
- Modify: `mcp-server/src/report/errors.ts`, `mcp-server/src/report/run.ts`
- Modify: `frontend/src/lib/performanceReport.ts` (+ its test), report dialog component that calls `setReportSettings`
- Test: `mcp-server/test/report-run.test.mjs`, `frontend/scripts/performanceReport.test.mjs`

**Interfaces:**
- Produces: report code `CLAUDE_CLI_BEDROCK`; Rust error string `profile-required`; `ReportStateView.settings_attention: bool` (true when `use_bedrock && aws_profile.is_none()`).

- [ ] **Step 1: Failing tests:**
  - Rust `bedrock_without_profile_is_refused`: `validate_bedrock(true, None)` is `Err("profile-required")`, `(false, None)` and `(true, Some("cc-bedrock"))` are Ok.
  - Node `bedrock timeout maps to CLAUDE_CLI_BEDROCK`: fake claude emits `{"type":"result","subtype":"error_during_execution","is_error":true,"result":"Request timed out"}` with `TTOBAK_REPORT_USE_BEDROCK=1`; stdout JSON code is `CLAUDE_CLI_BEDROCK`. Without Bedrock the same output stays `REPORT_INVALID`.
  - Frontend: `CLAUDE_CLI_BEDROCK` label is `claudeCliBedrock`; `profile-required` label is `profileRequired`.
- [ ] **Step 2: Run** the three suites — FAIL.
- [ ] **Step 3: Implement.** In `run.ts`, before the generic `REPORT_INVALID`, map `result.is_error` + `/timed out/i` on `result.result` + `childEnv.CLAUDE_CODE_USE_BEDROCK === '1'`. The dialog disables Save while Bedrock is on without a profile and shows the attention line when `settingsAttention`.
- [ ] **Step 4: Run** `(cd mcp-server && npm test)`, Rust checks, frontend checks — PASS.
- [ ] **Step 5: Commit** `fix(report): refuse Bedrock without a profile and name its timeout`; rebuild the bundle (`cd mcp-server && npm run bundle`, copy to `frontend/public/mcp/ttobak-mcp.mjs` as the existing bundle script does) and update `docs/runbooks/local-mcp-intake.md` (codes table: `CLAUDE_CLI_BEDROCK`, PATH repair note) in the same PR.

---

## Part 2: Report allowlist for v1.5.0 (separate PR, separate review)

### Task 2.1: Read tools and scripts that v1.5.0 calls

**Files:**
- Modify: `mcp-server/src/report/tools.ts`, `mcp-server/src/report/prompt.ts`
- Test: `mcp-server/test/report-run.test.mjs`

Evidence from the v1.5.0 `references/data-sources.md`: new calls are `search_pfrs`, `search_customer_influences`, `fetch_pfr_details`, `fetch_customer_influence_details`, `get_opportunity_details`, `fetch_account_details` (aws-sentral-mcp), `email_search` (aws-outlook-mcp, interview supplement) and the scripts `interview_count.py`, `pfr_ci_summary.py`. No `ccmt-mcp` tool is called (it appears only in a `mcp_stdio.py` usage comment), so no ccmt entry is added. `search_campaigns` belongs to activity logging, not the report, and stays out. `mcp_stdio.py` stays denied.

- [ ] **Step 1: Failing tests:** `allowedTools` output contains `mcp__aws-sentral-mcp__search_pfrs`, `mcp__aws-sentral-mcp__fetch_account_details`, `mcp__aws-outlook-mcp__email_search`, `Bash(python3 -I <skill>/scripts/interview_count.py:*)`, `Bash(python3 -I <skill>/scripts/pfr_ci_summary.py:*)`; it still contains no `ccmt-mcp`, no `search_campaigns`, no `mcp_stdio.py` allow rule; every listed tool passes `WRITE_TOOL_PATTERN` check.
- [ ] **Step 2: Run** `cd mcp-server && npm test` — FAIL.
- [ ] **Step 3: Implement.** Update the header comment to v1.5.0 (2026-11-10). Add one prompt rule: when a tool result is too small to leave a tool-results file, write the returned JSON to `outDir` with Write instead of `mcp_stdio.py`.
- [ ] **Step 4: Run** `npm test` — PASS.
- [ ] **Step 5: Commit** `feat(report): allow the v1.5.0 read tools`; rebuild the bundle; ADR-023 gets one sentence that the allowlist follows the reviewed skill version.

---

## Part 3: Skill package API (Go)

### Task 3.1: Package validator

**Files:**
- Create: `backend/internal/service/skill_package_zip.go`, `backend/internal/service/skill_package_zip_test.go`
- Create: `backend/internal/model/skill_package.go`

**Interfaces:**
- Produces (model):
  ```go
  type SkillPackageServers struct {
      ToolboxRegistries []string          `json:"toolboxRegistries" dynamodbav:"toolboxRegistries"`
      ToolboxServers    []string          `json:"toolboxServers" dynamodbav:"toolboxServers"`
      AimServers        []string          `json:"aimServers" dynamodbav:"aimServers"`
      HTTPServers       map[string]string `json:"httpServers" dynamodbav:"httpServers"`
  }
  type SkillPackageManifest struct {
      Version string `json:"version"`; Root string `json:"root"`
      Skills []string `json:"skills"`; Servers SkillPackageServers `json:"servers"`
  }
  ```
- Produces (service): `func ValidateSkillPackage(data []byte) (*model.SkillPackageManifest, error)`; `type SkillPackageError struct{ Rule, Detail string }` implementing `error`; rule constants `zip-too-large`, `too-many-entries`, `uncompressed-too-large`, `unsafe-path`, `not-regular-file`, `top-level`, `no-skills`, `skill-missing-md`, `bad-skill-name`, `bad-server-name`, `bad-registry`, `bad-http-url`, `report-skill-layout`, `bad-manifest`, `bad-server-list`.

Script grammar (when no `ttobak-manifest.json`): read `<root>/setup-internal-mcp.sh` (at most 1 MiB); arrays `TOOLBOX_REGISTRIES`, `TOOLBOX_MCPS_BASE`, `AIM_MCPS_BASE`, `HTTP_MCPS` in `NAME=(...)` form on one or several lines; tokens are `"..."` without `$`, backtick or backslash, or bare `[A-Za-z0-9._:/=@+-]+`; `#` starts a comment; `HTTP_MCPS` tokens are `name=url`. A token with expansion fails `bad-server-list`. A missing array is empty. Version: manifest `version`, else `VERSION="x.y.z"`, else the root folder name. `aim` is implied in toolbox servers only by the installer and is not added here (the CLI adds it, Task 5.4).

- [ ] **Step 1: Failing tests** (table-driven, zips built in-test with `archive/zip`):
  - `TestValidateSkillPackageAcceptsInstallerLayout`: synthetic root `pkg-v9.9.9/` with `skills/demo-skill/SKILL.md`, a script with the four arrays split over lines and a comment → `Skills == ["demo-skill"]`, registries/servers/http parsed, `Version == "9.9.9"`.
  - `TestValidateSkillPackagePrefersManifest`: manifest present overrides the script.
  - `TestValidateSkillPackageIgnoresExtraArrays`: `AIM_MCPS_EXTRA` names are absent.
  - `TestValidateSkillPackageRejectsUnsafeEntries`: `../x`, `/abs`, symlink mode entry, two top-level folders → rules `unsafe-path`, `unsafe-path`, `not-regular-file`, `top-level`.
  - `TestValidateSkillPackageLimits`: 501 entries → `too-many-entries`; declared uncompressed sum > 50 MiB → `uncompressed-too-large`; `len(data) > 20 MiB` → `zip-too-large`.
  - `TestValidateSkillPackageNames`: `Bad_Name` skill, `http://` URL, `https://` registry, `$HOME` token → matching rules.
  - `TestValidateSkillPackageReportLayout`: `sa-performance-report` without `references/report-structure.md` → `report-skill-layout` with the file in `Detail`.
- [ ] **Step 2: Run** `cd backend && /usr/local/go/bin/go test ./internal/service -run SkillPackage -count=1` — FAIL.
- [ ] **Step 3: Implement** with `archive/zip.NewReader`; check names before use; never extract. Keep the `SKILL_LAYOUT` list as a Go constant with a comment naming `mcp-server/src/report/claude.ts`.
- [ ] **Step 4: Run** the same command — PASS.
- [ ] **Step 5: Commit** `feat(api): validate skill packages`.

### Task 3.2: Effective merge

**Files:**
- Create: `backend/internal/service/skill_package_merge.go`, `..._merge_test.go`

**Interfaces:**
- Produces: `type EffectiveSkill struct{ Name, Source string }` (`Source` is `org` or `personal`); `type EffectivePackage struct{ Skills []EffectiveSkill; Servers model.SkillPackageServers }`; `func MergeSkillPackages(org, personal *model.SkillPackageManifest) EffectivePackage` (either may be nil; output sorted by name; HTTP map conflict keeps the org URL and is reported in a `Conflicts []string` field).

- [ ] **Step 1: Failing tests:** `TestEffectivePackagePersonalSkillWins`, `TestEffectivePackageUnionsServers` (no duplicates, nothing removed), `TestEffectivePackageOrgOnly`, `TestEffectivePackagePersonalOnly`, `TestEffectivePackageHTTPConflictKeepsOrg`.
- [ ] **Step 2–4:** run `go test ./internal/service -run EffectivePackage`, implement, rerun — PASS.
- [ ] **Step 5: Commit** `feat(api): merge org and personal skill packages`.

### Task 3.3: Storage, repository, service and routes

**Files:**
- Create: `backend/internal/repository/skill_package.go` (+ test with the existing mock pattern), `backend/internal/service/skill_package.go` (+ test), `backend/internal/handler/skill_package.go` (+ test)
- Modify: `backend/internal/model/skill_package.go` (rows, keys), `backend/cmd/api/main.go` (wiring, routes), `infra/lib/storage-stack.ts` (+ `infra/test`), `docs/API-SPEC.md`, `docs/INFRA-SPEC.md`, `docs/decisions/ADR-003-mcp-access.md`

**Keys and objects:**
- Org current: `PK=SKILLPKG#org`, `SK=CURRENT`; org history: `PK=SKILLPKG#org`, `SK=VERSION#{uploadedAt RFC3339}#{sha256}`; keep the newest 5 versions and delete pruned objects.
- Personal: `PK=USER#{userId}`, `SK=SKILLPKG#personal`.
- Row: `model.SkillPackage{Scope, Version, SHA256, Size, UploadedBy, UploadedAt, Skills, Servers, ObjectKey}`.
- Objects: staging `skillpkg/staging/{userId}/{uploadId}.zip` (`uploadId` 32 lowercase hex); final `skillpkg/org/{sha256}.zip`, `skillpkg/user/{userId}/{sha256}.zip`.
- StorageStack lifecycle rule `ExpireSkillPackageStaging`: prefix `skillpkg/staging/`, expiration and noncurrent expiration 1 day. No OAC change: downloads are S3 presigned GETs (the API role already has bucket read/write).

**Routes** (all authenticated; bodies at most 4 KiB):

| Method and path | Who | Result |
|---|---|---|
| `GET /api/skill-packages` | any user | `{org, personal, effective, downloads: {org?, personal?}}`; downloads are 5-minute presigned GETs |
| `POST /api/skill-packages/personal/upload` | any user | presigned POST `{url, fields, uploadId, expiresIn}` with `content-length-range` 1..20 MiB |
| `POST /api/skill-packages/personal/complete` | any user | body `{uploadId}`; validates, copies to the final key, writes the row; 400 `{error:{code:"INVALID_PACKAGE", message:"<rule>: <detail>"}}` |
| `DELETE /api/skill-packages/personal` | any user | deletes row then object |
| `POST /api/skill-packages/org/upload`, `/org/complete` | admin | same as personal, org scope |
| `GET /api/skill-packages/org/versions` | admin | newest first |
| `POST /api/skill-packages/org/rollback` | admin | body `{sha256}` of a kept version |

**Interfaces:**
- Produces (service): `SkillPackageService` with `Get(ctx, userID) (*model.SkillPackageView, error)`, `StartUpload(ctx, userID, scope string) (*model.SkillPackageUpload, error)`, `CompleteUpload(ctx, userID, scope, uploadID string) (*model.SkillPackage, error)`, `DeletePersonal(ctx, userID) error`, `ListOrgVersions(ctx) ([]model.SkillPackage, error)`, `RollbackOrg(ctx, userID, sha string) (*model.SkillPackage, error)`. Sentinel errors: `service.ErrNotFound`, `service.ErrForbidden`, `ErrInvalidPackage` (wraps `SkillPackageError`).
- Produces (JSON for Part 4/5): `SkillPackageView{Org, Personal *SkillPackage; Effective EffectivePackage; Downloads map[string]string}`.

- [ ] **Step 1: Failing tests:**
  - Handler: `TestSkillPackageOrgRoutesRequireAdmin` (non-admin JWT gets 403 on all four org routes), `TestSkillPackageCompleteRejectsForeignUploadID` (staging key of another user → 404), `TestSkillPackageCompleteRejectsBadUploadID` (`../x`, uppercase, 31 chars → 400), `TestSkillPackageBodyLimit`.
  - Service: `TestCompleteUploadRejectsOversizeObject` (HeadObject 20 MiB + 1 → `zip-too-large`, staging deleted), `TestCompleteUploadStoresByHash`, `TestPersonalPackageIsOwnerScoped` (user B's `Get` never returns A's personal row), `TestOrgHistoryKeepsFive`, `TestDeletePersonalFallsBackToOrg`, `TestRollbackUnknownSHA` → `ErrNotFound`.
  - Infra: storage-stack test asserts the `skillpkg/staging/` lifecycle rule.
- [ ] **Step 2: Run** `cd backend && /usr/local/go/bin/go test ./... -count=1` and `cd infra && npm test` — FAIL.
- [ ] **Step 3: Implement.** Use the expression builder; org `CURRENT` writes are conditional on the previous `sha256` (or absent). Surface a failed best-effort delete of an old object in the response (`warnings`). Use `PresignPostObject` with a `content-length-range` condition and the exact key; complete reads at most 20 MiB + 1 bytes.
- [ ] **Step 4: Run** the full Go suite, `go vet ./...`, the ARM64 build command, `cd infra && npx cdk synth && npm test` — PASS.
- [ ] **Step 5: Docs and commit.** API-SPEC: new "Skill packages" section. INFRA-SPEC: the `skillpkg/` prefix and lifecycle rule. ADR-003 (owns local intake): package trust (server and Mac both validate, never execute, explicit [Setup], approvals for new servers) and merge rules. Commit `feat(api): org and personal skill packages`. Deploy API, then `cdk deploy TtobakStorageStack --exclusively` (check the stack name in `infra/bin/infra.ts`).

---

## Part 4: Web "Skill packages" settings

### Task 4.1: API client and copy

**Files:**
- Create: `frontend/src/lib/skillPackages.ts`, `frontend/scripts/skillPackages.test.mjs` (add to `test:unit`)

**Interfaces:**
- Consumes: Part 3 routes and JSON.
- Produces: `getSkillPackages()`, `uploadSkillPackage(scope: 'personal'|'org', file: File)` (upload → form POST with returned fields → complete), `deletePersonalSkillPackage()`, `listOrgSkillPackageVersions()`, `rollbackOrgSkillPackage(sha256: string)`, `packageErrorText(code: string, message: string): string`.

- [ ] **Step 1: Failing tests:** `packageErrorText('INVALID_PACKAGE', 'unsafe-path: ../x')` is `pkgUnsafePath` with `../x` as `{detail}`; one Korean label per rule constant of Task 3.1; a file over 20 MiB is refused client-side with `pkgTooLargeClient` before any request.
- [ ] **Step 2–4:** `cd frontend && npm run test:unit` FAIL → implement with `apiFetch` → PASS.
- [ ] **Step 5: Commit** `feat(web): skill package client`.

### Task 4.2: Settings section

**Files:**
- Create: `frontend/src/components/SkillPackageSettings.tsx`
- Modify: `frontend/src/app/settings/page.tsx` (render next to `IntakeSettings`, desktop and in-app), `docs/DESIGN-SPEC.md`

- [ ] **Step 1:** Render: org version/date, my personal version or `pkgNone`, the effective skill list with `pkgSourceOrg`/`pkgSourcePersonal` badges, `pkgUploadPersonal`, `pkgDeletePersonal`; when `isAdmin`, `pkgUploadOrg` and the version list with `pkgRollback`. Errors use `packageErrorText`.
- [ ] **Step 2: Run** `cd frontend && npm run lint && npm run test:unit && env -u NODE_ENV npm run build` — PASS.
- [ ] **Step 3: Commit** `feat(web): skill packages settings`; deploy frontend per the runbook. The owner then uploads `internal-mcp-setup-v1.5.0.zip` as the org package (seed) and confirms the effective list shows `sa-performance-report`.

---

## Part 5: Adapter `setup plan` / `setup apply`

All files under `mcp-server/src/setup/`; tests in `mcp-server/test/setup-*.test.mjs` with an in-test zip builder `test/helpers/zip.mjs` (`makeZip(entries: {name, data?, mode?, method?: 'store'|'deflate'}[]) → Buffer`).

### Task 5.1: Zip reader and package validation

**Files:** Create `src/setup/zip.ts`, `src/setup/package.ts`, `test/helpers/zip.mjs`, `test/setup-package.test.mjs`

**Interfaces:**
- Produces: `readZip(data: Buffer): ZipEntry[]` (`{ name: string; size: number; isFile: boolean; read(): Buffer }`, stored and deflate only, each `read` capped at the declared size and the 50 MiB running total); `validatePackage(data: Buffer): PackageManifest` with the same rules and `SetupError('PACKAGE_INVALID', '<rule>: <detail>')` as Task 3.1; `skillFiles(entries, root, skill): { path: string; data: Buffer }[]`.

- [ ] **Step 1: Failing tests:** the Task 3.1 table re-expressed (same rule names), plus `zip reader rejects unsafe entries` and `zip reader stops at the uncompressed cap` (a deflate entry whose real output exceeds its declared size).
- [ ] **Step 2–4:** `cd mcp-server && npm test` FAIL → implement (central directory parse, `zlib.inflateRawSync` with `maxOutputLength`) → PASS.
- [ ] **Step 5: Commit** `feat(mcp): read and validate skill packages locally`.

### Task 5.2: Fetch the effective packages

**Files:** Modify `src/api.ts` (`getSkillPackages()`); create `src/setup/fetch.ts`, `test/setup-fetch.test.mjs`

**Interfaces:**
- Produces: `fetchEffective(api: TtobakApi, fetchImpl?): Promise<{ view: SkillPackageView; zips: Partial<Record<'org'|'personal', Buffer>> }>`; downloads require `https:`, at most 20 MiB, and the metadata `sha256`.

- [ ] **Step 1: Failing tests:** `sha mismatch fails before install` (`PACKAGE_INTEGRITY`), `expired download URL fails` (403 → `PACKAGE_DOWNLOAD`), `no packages yields an empty effective set` (step status `skipped`).
- [ ] **Step 2–5:** run, implement, run, commit `feat(mcp): fetch skill packages from TTOBAK`.

### Task 5.3: Skill install with backup

**Files:** Create `src/setup/skills.ts`, `test/setup-skills.test.mjs`

**Interfaces:**
- Produces: `planSkills(home: string, effective: EffectiveSkill[], zips): SkillPlan[]` (`{ name, source, state: 'current'|'install'|'update' }`); `installSkills(home, plans, zips, now: Date): StepResult` where `StepResult = { id: string; status: 'ok'|'current'|'skipped'|'failed'|'needs-approval'; code?: string; items?: string[] }`.

- [ ] **Step 1: Failing tests:** `identical skill is current and makes no backup`; `changed skill is backed up then replaced` (backup at `~/.claude/skills-backup/<name>-<YYYYMMDD-HHMMSS>/` equals the old tree); `local-only files are kept`; `symlinked skills dir is refused` (`~/.claude/skills/<name>` is a symlink → `failed`, `SKILL_PATH_UNSAFE`, nothing written); `personal source is used when both exist`.
- [ ] **Step 2–5:** run, implement (write to `<name>.tmp-<pid>` then rename per file; containment check on the joined path before `realpath`), run, commit `feat(mcp): install skills from packages with backups`.

### Task 5.4: Server detection and install

**Files:** Create `src/setup/servers.ts`, `src/setup/approvals.ts`, `test/setup-servers.test.mjs`, `test/fixtures/setup/` (captured outputs)

Detection rules (from the installer): registry present when its URL appears in `toolbox registry list`; toolbox server present when it appears as a word in `toolbox list --installed`; Claude registration present when `claude mcp list` has a line starting `<name>:`; AIM server present when `~/.aim/config.json` has key `toolBundles.<name>`; `aim` itself is added to the toolbox install list when the effective package has AIM servers. Approvals: `~/.ttobak/setup/approvals.json` (0600) `{ "approved": ["registry:<url>", "toolbox:<name>", "aim:<name>", "http:<name>=<url>"] }`.

**Interfaces:**
- Produces: `planServers(env: SetupEnv, servers: SkillPackageServers): ServerPlan` (`{ available: boolean; actions: { key: string; argv: string[]; approved: boolean }[] }`); `applyServers(env, plan, approve: string[]): Promise<StepResult>`; `SetupEnv = { home: string; path: string; run(argv: string[], timeoutMs: number): Promise<{ code: number|null; stdout: string; stderr: string }> }`.

- [ ] **Step 1: Capture fixtures on the owner's Mac** (read-only commands): `toolbox registry list`, `toolbox list --installed`, `claude mcp list`, and the key shape of `~/.aim/config.json` with values replaced by `{}`; save under `test/fixtures/setup/` with internal URLs replaced by synthetic ones.
- [ ] **Step 2: Failing tests:** `present items produce no action`; `new items need approval` (status `needs-approval`, `items` are the keys); `approved items install with fixed argv` (fake `run` records argv equal to the Global Constraints list); `missing toolbox skips the server step` (`skipped`, code `TOOLBOX_MISSING`); `auth failure stops remaining installs` (stderr matching the intake `AUTH_PATTERN` → `failed`, `AUTH_EXPIRED_MIDWAY`, later actions not run); `missing Midway cookie fails before installs` (`~/.midway/cookie` absent → `AUTH_EXPIRED_MIDWAY`); `approvals file is private` (mode 0600).
- [ ] **Step 3–5:** run, implement (`execFile` with `env: { HOME, PATH, LANG }`, 5-minute timeout per command), run, commit `feat(mcp): install missing MCP servers from packages`.

### Task 5.5: `setup` command

**Files:** Create `src/setup/run.ts`; modify `src/index.ts` (route `setup` like `report`); `test/setup-run.test.mjs`; `scripts/check-bundle.mjs` (assert `ttobak-mcp setup plan` in help)

**Interfaces:**
- Produces: `runSetup(args: string[], environment: IntakeEnvironment): Promise<number>`; `ttobak-mcp setup plan` and `ttobak-mcp setup apply [--approve <key>]... [--noninteractive]`; stdout one JSON line `{ ok: boolean; steps: StepResult[]; needsApproval: { key: string; label: string }[]; warnings: string[] }` with step ids `packages`, `skills`, `servers`; Korean progress on stderr; takes the intake lock; `--approve` at most 50 keys of at most 600 bytes.
- Rust detects the capability by the marker `setup apply` in the adapter bytes (like `REPORT_MARKER`).

- [ ] **Step 1: Failing tests:** `plan writes nothing`; `apply without approvals installs skills and present-approved servers and reports needs-approval for the rest`; `apply with approve keys persists approvals`; `unknown approve key is ignored with a warning`; `TTOBAK login required maps to TTOBAK_LOGIN_REQUIRED`; `skill references a tool the report run denies` → warning naming the tool (compare `data-sources.md` backtick tool names with `READ_TOOLS`).
- [ ] **Step 2–4:** run, implement, `npm test` — PASS.
- [ ] **Step 5: Commit** `feat(mcp): setup plan and apply commands`; rebuild the bundle; runbook section "One-click setup".

---

## Part 6: Mac [Setup] button

### Task 6.1: Setup runner

**Files:**
- Create: `mac-app/src-tauri/src/setup_runner.rs`
- Modify: `mac-app/src-tauri/src/intake_runner.rs` (`RunKind::OneClickSetup`, reuse `node_launch`, `run_child`, `fetch_adapter`, `install_adapter`, `refresh_login_path`), `mac-app/src-tauri/src/lib.rs`

**Interfaces:**
- Consumes: Task 1.1 `refresh_login_path`, Task 1.2 `adapter_changed`, Task 5.5 CLI JSON.
- Produces: Tauri commands `setup_start(approve: Vec<String>) -> ()` and `setup_get_state() -> SetupStateView`; events `setup://step` `{ id, status, code?, items? }` and `setup://finished` `{ steps, needsApproval }`. Step ids: `path`, `adapter`, `packages`, `skills`, `servers`, `intake`.

Order: `path` → `adapter` (failure stops all) → CLI `setup apply` (`packages`/`skills`/`servers` from its JSON; an outdated adapter without the `setup apply` marker fails `packages` with `ADAPTER_OUTDATED`) → if `needsApproval` is non-empty, finish and wait for the user; otherwise `intake` = existing `intake setup --noninteractive` job. `servers` failure does not stop `intake`; `packages` failure skips `skills` and `servers`.

- [ ] **Step 1: Failing tests** (pure functions): `setup_order_stops_after_adapter_failure`, `servers_failure_still_runs_intake`, `needs_approval_defers_intake`, `setup_is_idempotent_step_view` (CLI JSON with all `current` → view all current/skipped), `approve_keys_are_bounded` (51 keys or a 601-byte key → `invalid-argument`), `setup_refuses_while_intake_runs` (`RUN_IN_PROGRESS`).
- [ ] **Step 2–4:** `cargo test setup_` FAIL → implement → Rust checks PASS.
- [ ] **Step 5: Commit** `feat(mac): one-click intake setup runner`.

### Task 6.2: [Setup] UI

**Files:** Modify `frontend/src/lib/tauri.ts` (`startSetup`, `getSetupState`, `onSetupStep`, `onSetupFinished`), `frontend/src/lib/intake.ts` (`setupStepLabel(id)`, `setupStatusLabel(status, code)`), `frontend/src/components/IntakeSettings.tsx`; tests in `frontend/scripts/intake.test.mjs`

- [ ] **Step 1: Failing tests:** labels for every step id (`stepPath`, `stepAdapter`, `stepPackages`, `stepSkills`, `stepServers`, `stepIntake`) and status (`statusOk`, `statusCurrent`, `statusSkipped`, `statusFailed`, `statusNeedsApproval`); `AUTH_EXPIRED_MIDWAY` text includes `mwinit -o`; `TOOLBOX_MISSING` text is `toolboxMissing`.
- [ ] **Step 2:** UI: a primary `setupButton` button at the top of the intake section, the step list with live status, and when `needsApproval` is present a list of new items with `setupInstall` (calls `startSetup(keys)`) and `setupCancel`. A browser or an app without `setup_start` hides the button (`isCommandNotFound`).
- [ ] **Step 3: Run** frontend checks and Rust checks — PASS.
- [ ] **Step 4: Owner Mac validation:** rebuild the app; with the 2026-11-11 `intake.json` intact, press `setupButton`: PATH step adds `~/.toolbox/bin` if the cached value lacks it, adapter step reports current or updated, skills step installs v1.5.0 scripts with a backup, servers step reports current, intake step re-enables meetings and Slack. Press again: all current/skipped. Record the result in the PR.
- [ ] **Step 5: Commit** `feat(mac): one-click setup button`; DESIGN-SPEC and runbook updates.

---

## Part 7: Report Slack channel exclusions

### Task 7.1: CLI candidates and the exclusion flag

**Files:** Create `mcp-server/src/report/slack-channels.ts`; modify `mcp-server/src/report/run.ts` (flag), `prompt.ts`, `schema.ts` (check); tests in `mcp-server/test/report-run.test.mjs`, `test/report-slack-channels.test.mjs`

**Interfaces:**
- Produces: `ttobak-mcp report slack-channels [--days 90]` → `{ ok, channels: { name: string; count: number }[], partial: boolean }`; `report run ... --exclude-slack-channel <name>` repeatable, at most 100, name matches `/^[\p{Ll}\p{Lo}\p{Nd}][\p{Ll}\p{Lo}\p{Nd}._-]{0,79}$/u`; code `REPORT_EXCLUDED_CHANNEL`.
- Uses the intake's selected Slack connector (`selection.slack`, `openSession`) and its `search` tool with `from:me after:<YYYY-MM-DD>`, paging with at least 4 s between calls (15 per minute) and at most 40 pages; `partial: true` when the cap is reached.

- [ ] **Step 1: Capture** one real `search` result shape on the owner's Mac and save a synthetic fixture with the same fields.
- [ ] **Step 2: Failing tests:** `channels are counted per channel and sorted by count`; `DMs and group DMs are left out`; `page cap sets partial`; `exclusions enter the prompt as data`; `a report mentioning an excluded channel is rejected` (any string equal to the name or containing `#<name>` → `REPORT_EXCLUDED_CHANNEL`); `invalid channel name is INVALID_ARGUMENT`.
- [ ] **Step 3–5:** run, implement, run, commit `feat(report): exclude chosen Slack channels`; rebuild the bundle.

### Task 7.2: App settings and picker

**Files:** Modify `mac-app/src-tauri/src/report_runner.rs` (`ReportSettings.excluded_slack_channels: Vec<String>`, `report_settings_set` gains `excluded_slack_channels`, args builder adds the flags, command `report_slack_channels_refresh()` storing `slack-channels.json` in the app config dir), `frontend/src/lib/tauri.ts`, `frontend/src/lib/performanceReport.ts`, the report dialog; docs ADR-023 (exclusions stay on the Mac)

- [ ] **Step 1: Failing tests:** Rust `report_args_include_exclusions` (order kept, duplicates removed), `exclusions_are_bounded` (101 names → `invalid-argument`), `old_settings_without_exclusions_load` (lenient default); frontend `channelRows(candidates, excluded)` returns every candidate checked unless excluded, plus excluded names no longer in the candidates (still listed, unchecked).
- [ ] **Step 2:** UI: report options `slackChannels` opens a checklist of the cached candidates with counts, `slackRefresh`, and the note `slackExclusionNote`.
- [ ] **Step 3: Run** Rust and frontend checks — PASS.
- [ ] **Step 4: Owner Mac validation:** refresh candidates, uncheck one hobby channel, run a week report, confirm the channel is absent.
- [ ] **Step 5: Commit** `feat(mac): pick Slack channels to leave out of the report`.
