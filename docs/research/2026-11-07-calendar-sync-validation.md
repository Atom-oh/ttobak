# Calendar UI and persisted fixture validation — 2026-11-07

Historical validation record for Calendar Task 5 in
`/home/atomoh/.codex/worktrees/04be/ttobak`. The final acceptance follow-up below
supersedes the original pending-review status. This records synthetic local acceptance,
not production deployment, real Outlook access, or scheduler acceptance.

## Scope and behavior

Home and Meetings load the signed-in user's upcoming calendar independently of
recorded history and insights. The Account meetings tab loads only that user's
account-filtered calendar while visible. Account association does not share
events. Calendar reads use `apiFetch`, `expectedUserId`, abort signals and
`cache: no-store`; calendar data has no persistent browser cache.

Private data and captured pagination/retry callbacks are invalidated on user,
membership, account, window, or enabled-scope changes. Account read denials clear
account controls through UI3's existing access-loss callback. Invitation recovery
and parent mutation lifetime guards remain covered, including calendar denial
followed by successful metadata recovery.

The query window spans the current local day through seven local calendar days.
Minute ticks advance freshness and remove ended events without restarting reads;
local date rollover changes the query window. Source and event continuations have
separate explicit controls, 50-item requests, and no automatic draining. Failed
continuations preserve loaded rows and their cursor. A repeated cursor fails
explicitly. Source/event pagination, read errors, exact completed revision, window
coverage and completion age all participate in completed-empty status.

Never synced, unavailable routes, loading, failed installed reads, partial or
unknown completion, stale reports and fully completed empty windows remain
distinct. Completion is the local caller's report; timestamps never imply live
Outlook connectivity. Timed/ongoing events and all-day dates render separately,
with viewer-local time and the original timezone.

## Verified checks

| Check | Result |
|---|---|
| Frontend `npm run test:unit` | 402 passed; zero failures/skips |
| Frontend `npm run lint` | Exit 0; 13 pre-existing warnings, no errors |
| Frontend `npm run build` | Exit 0; production static export includes Home, Meetings and Accounts |
| Backend `go test ./... -count=1 -json` with explicit local endpoint | 24 passing test packages; 1,690 top-level tests and 2,857 subtests; persisted acceptance ran |
| Backend `go vet ./...` | Exit 0 |
| MCP `TMPDIR=/tmp npm test` | 253 tests plus 57 bundle checks passed; reproducibility/parity passed |
| Extended browser harness | 70/70 passed: preserved UI4 56, ten upcoming layouts, four targeted behavior cases |
| Documentation check and unit tests | 267 Markdown files/162 Go routes checked; six doc tests passed |
| Unchanged infra, reused controller evidence | 322 tests passed; offline synth contains 13 stacks |

Backend has four pre-existing opt-in test skips:
`TestProjectInvitationSubjectIntegration`, `TestNoteQualityHistoricalEvaluation`,
`TestNoteQualityEvaluation`, and `TestQualityLiveSummaryTransport`. Four command
packages report no test files. The calendar persisted test did not skip.
Use `/home/atomoh/go-sdk/go/bin/go`; `/usr/local/go/bin/go` is absent here.
The module resolves the installed Go 1.27.3 toolchain.

Task logs and browser artifacts are under
`.superpowers/sdd/2026-11-07-local-outlook-calendar-sync/`, including
`task-5-backend-full.jsonl`, `task-5-backend-vet.log`, `task-5-mcp-final.log`,
`task-5-units-final.log`, `task-5-lint-final.log`, `task-5-build-final.log` and
`task-5-browser-final/results.json`. Documentation results are in
`task-5-docs-check.log` and `task-5-docs-tests.log`. Controller infra evidence remains under
`.superpowers/sdd/2026-11-07-meeting-workspace-ui/`.

## Persisted transaction acceptance

The test uses the real stdio MCP adapter, Go calendar HTTP handler/service,
repository expression builder, and DynamoDB Local transactions. The controller's
memory-only container is `ttobak-calendar-04be-tests`, with the sole permitted
endpoint `http://127.0.0.1:32887`. SDK construction uses static dummy credentials
and an explicit endpoint, without loading AWS configuration or provider profiles.
The test creates and deletes one uniquely named local table.

Synthetic authenticated HTTP identities and account membership are test seams in
`calendar_persisted_test.go`, never production endpoints or JWT bypasses.
The existing local MCP authentication fixture supplies synthetic authentication.
A test KMS client uses AES-GCM and validates purpose/user/source/event context;
each persisted ciphertext is decrypted under its exact binding and rejected under
another owner's binding. This does not verify production KMS or Cognito.

Eleven persisted checkpoints pass: import, identical retry, another account
member's isolation, move, stale CAS rejection, reused-batch payload conflict,
lost response after commit, identical lost-response retry, explicit cancellation,
empty completion, and a second owner's independent source/events.

Every checkpoint scans the actual table with consistent reads, checks canonical
row/reference counts and identity/revision/TTL consistency, verifies authenticated
ciphertext and absence of plaintext sensitive fields, and inspects source
revision and saved receipts. Move removes the old reference; cancellation removes
canonical and reference rows. Conflicts and retries preserve the entire observed
stored state. Partial progress retains the prior completed revision. A lost
response is injected only after the real successful handler response; retrying
the identical batch returns the stored receipt without advancing again.

Reproduce from the worktree:

```bash
(cd mcp-server && TMPDIR=/tmp npm run build)
(cd backend && \
  TTOBAK_CALENDAR_DYNAMODB_ENDPOINT=http://127.0.0.1:32887 \
  /home/atomoh/go-sdk/go/bin/go test ./internal/handler \
  -run '^TestCalendarPersistedStdioAcceptance$' -count=1 -v)
```

Without the endpoint variable, ordinary backend tests explicitly skip this
opt-in acceptance. Any other endpoint is rejected. MCP child clients, the fixture
HTTP server and the synthetic table are cleaned up; the controller owns container
shutdown. No scoped backend producer defect was uncovered.

## Browser and performance evidence

The existing harness retains strict loopback routing, static rewrite rules,
blocked external requests/WebSockets, installed Playwright/Chromium, and cleanup
in `finally`. Chromium `151.0.7922.34`, `Asia/Seoul`, `ko-KR`, 1,000px height,
light/dark at 360, 390, 768, 1024 and 1440px were used. All 70 checks passed,
including keyboard/focus, overflow, independent sections, private account races,
empty continuation/retry and advancing freshness without extra requests.
The run produced 191 synthetic screenshots; this is not a claim that every
image received visual inspection.

Selected images inspected: `calendar-layout-390-light.png`,
`calendar-layout-1440-dark.png`, and `calendar-account-denied.png`.
These show readable calendar cards, local time, privacy copy and account denial
without the former account's controls. No real customer content was captured.

The matched 200-record fixture and three paired runs remain unchanged:
SHA-256 `57554226dc910f52ed8275980263309fede036e4f03fe7e5d616009c712e574a`.
At 1440×1000, baseline mounted 200 rows/14,014 DOM nodes; the final implementation
mounted 100 rows/2,142–2,152 nodes. Median response-to-first-row time was 367ms
versus 49ms; median navigation-to-loaded time was 1,006.87ms versus 379.95ms.
These are local fixture measurements, not production latency claims.

```bash
(cd frontend && \
  PLAYWRIGHT_PACKAGE_PATH=/home/atomoh/.local/lib/python3.9/site-packages/playwright/driver/package \
  PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH=/home/atomoh/.cache/ms-playwright/chromium-1234/chrome-linux/chrome \
  WORKSPACE_PREVIEW_URL=http://127.0.0.1:8771 \
  WORKSPACE_PROTOTYPE_PATH=/home/atomoh/ttobak/.superpowers/brainstorm/3192558-1794008007/content/meeting-workspace-v1.html \
  WORKSPACE_CHECK_OUTPUT=/home/atomoh/.codex/worktrees/04be/ttobak/.superpowers/sdd/2026-11-07-local-outlook-calendar-sync/task-5-browser-final \
  node scripts/workspace-browser-check.mjs)
```

During verification, the controller's preview server served `frontend/out`.
The harness starts and stops its own baseline server/browser. The controller
owns shutdown of its 8771 server and DynamoDB container after final checks.

## Remaining gates

Actual local Outlook reader consent, recurrence/deletion delivery, checkpoint behavior,
manual provider sync and scheduler execution remain unperformed. Production
KMS/IAM/JWT readiness and deployment remain unverified. No Outlook reader or
scheduler was implemented/configured; no infra, branch, commit or deployment was
created by this task.

## Final acceptance follow-up

Independent Task 5 review approved the denial-lifetime fixes: a definitive
denial retires both private read channels and promptly invalidates account
controls even while a sibling request stalls. Ordinary continuation failures
still retain loaded rows; explicit retry starts a fresh lifetime.

The final whole-feature review covered all 83 changed paths, including producer,
storage, privacy, MCP, UI, CloudFront routing and the local operation guide.
Its two remaining UI findings were fixed together and independently approved:
all loaded timed/all-day events remain reachable with bounded local pages, and
Home deletion updates rows/counts/backfill. No actionable review finding remains.

After that fix, the controller reran frontend lint, **431 unit tests**, production
build and **76 browser checks** successfully. Lint has 13 existing warnings and
no errors. Backend/MCP/infra code was unchanged by the final five-file fix, so
the reviewed full backend/vet/ARM64, 253 MCP plus 57 bundle checks and 322 infra
test evidence remain applicable. The controller's independent persisted stdio
test rerun passed all **11 DynamoDB Local checkpoints**, recorded in
`controller-persisted-final.log` under this plan's SDD workspace.

Final frontend/review artifacts are in the sibling
`.superpowers/sdd/2026-11-07-meeting-workspace-ui/` directory; see the final
follow-up in `2026-11-07-meeting-workspace-ui-validation.md`. Both worktree and
local evidence remain uncommitted pending the user's integration decision.
The public MCP bundle matches its source build byte-for-byte; that is not
publication. Real Outlook consent/filtering/checkpoints/scheduling and deployed
Cognito/KMS acceptance are still required, not simulated away by these tests.
