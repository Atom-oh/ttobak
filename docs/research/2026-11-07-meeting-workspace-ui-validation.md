# Meeting workspace browser validation — 2026-11-07

Historical, dated implementation evidence for UI Task 4, not a deployment,
production-performance, or Outlook-connectivity claim. The final acceptance
follow-up below supersedes this task's original pending-review status.
The binding scope is `docs/superpowers/specs/2026-11-07-meeting-workspace-outlook-design.md`.

## Environment and reproduction

Worktree: `/home/atomoh/.codex/worktrees/04be/ttobak`. The original checkout was
read only for the approved prototype. No dependencies, browsers, branches,
commits, deployments, or nested agents were created.

The harness is `frontend/scripts/workspace-browser-check.mjs`. It uses the
installed Playwright package and Chromium **151.0.7922.34**, a new isolated context
per case, five widths (**360, 390, 768, 1024, 1440 CSS pixels**), height 1000,
device scale 1, both light/dark themes, `ko-KR`, `Asia/Seoul`, reduced motion, and
fixed Date `2026-11-07T09:00:00.000Z`.

Run from the worktree root after the production export is built:

```bash
export PLAYWRIGHT_PACKAGE_PATH=/home/atomoh/.local/lib/python3.9/site-packages/playwright/driver/package
export PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH=/home/atomoh/.cache/ms-playwright/chromium-1234/chrome-linux/chrome
export WORKSPACE_PREVIEW_URL=http://127.0.0.1:8771
export WORKSPACE_PROTOTYPE_PATH=/home/atomoh/ttobak/.superpowers/brainstorm/3192558-1794008007/content/meeting-workspace-v1.html
WORKSPACE_CHECK_OUTPUT=.superpowers/sdd/2026-11-07-meeting-workspace-ui/task-4-browser-final \
  node frontend/scripts/workspace-browser-check.mjs \
  > .superpowers/sdd/2026-11-07-meeting-workspace-ui/task-4-browser-final.log 2>&1
```

The controller already owns the loopback 8771 server serving `frontend/out`.
The harness never starts or stops that server. It starts an ephemeral loopback
server for the preserved baseline export and closes it, all contexts, and its
browser in `finally`. `WORKSPACE_BASELINE_EXPORT` optionally overrides the default
`.superpowers/sdd/2026-11-07-meeting-workspace-ui/baseline-export`.
`WORKSPACE_CHECK_FILTER` optionally selects case names by regular expression;
an empty selection fails. Missing prototype input fails its cases rather than
silently omitting visual-reference coverage.

Fixtures use synthetic Cognito SDK local-session entries through the existing
auth provider, not a product auth bypass. Explicit synthetic meeting, account,
section, mutation, failure, and deferred-response routes replace API traffic.
Unexpected API calls, browser exceptions, and attempted external requests fail
the case. Non-loopback HTTP is aborted; WebSockets are closed, service workers
blocked, and browser background networking disabled. No real sessions, meeting
text, credentials, provider calls, or external network were used.

Test-only HTML and segmented RSC rewrites preserve browser route paths while
adapting the simple preview server to the export's `meetings.html` and dynamic
placeholder files. An ordinary Python server's missing rewrite is not treated
as a product routing defect.

## Results

All logs below are under
`.superpowers/sdd/2026-11-07-meeting-workspace-ui/`.

| Command | Exact outcome | Log |
|---|---|---|
| `(cd frontend && npm run lint)` | Exit 0; 0 errors, 13 existing warnings | `task-4-lint.log` |
| `(cd frontend && npm run test:unit)` | Exit 0; 384 passed, 0 failed/skipped/cancelled/todo | `task-4-unit.log` |
| `(cd frontend && npm run build)` | Exit 0; icon/billing checks and static export succeed, including `/meetings` | `task-4-build.log` |
| Browser command above | Exit 0; **56/56 cases**, 172 viewport screenshots | `task-4-browser-final.log` |
| `python3 scripts/docs/check_docs.py` | Exit 0; 266 Markdown files (4 immutable evidence files), 162 Go routes, 23,999-byte review context | `task-4-docs-check.log` |

The 56 cases comprise six matched performance runs, ten prototype cases
(three views each), thirty implementation width/theme cases (Home, Meetings,
Accounts), and ten additional state/action/race cases. A passing case requires
all its assertions, not merely successful navigation or screenshot creation.

### Exercised behavior

- Meetings: grouped/flat layout, newest/oldest sorting, search and URL state,
  date/status/recent/shared filters, account checkbox selection/clear/Escape,
  loaded-set continuation when filtering finds no match, and genuine empty state.
  Previous/next 100-row display windows cause no server fetch. Explicit
  Load More failure retains rows; retry deduplicates overlapping IDs and respects
  cursor exhaustion. A mutation observer records the maximum mounted row count.
- Home: truthful local-sync setup state, six recent records, at most five
  selected-account insights, no all-account insights fan-out, independent
  recent/insights errors and retries, empty accounts/records, and obsolete
  account-response rejection.
- Accounts: overview without secondary-section fetches, URL-preserving tabs,
  arrow-key activation, 100-row meeting-history windows, nine insight kinds
  including unknown fallback, risk filtering, evidence/action/source display,
  projects/research/documents, runtime-disabled billing, section error/retry/empty
  states, initial loading/error/retry, access-loss hiding, and stale-account
  response rejection. Synthetic inherited-read accounts have no write controls.
- The previously approved pending parent-save/tab-switch behavior remains tested:
  the pending request survives the tab change and its saved hierarchy reappears
  when returning. No parent-editor behavior was rewritten.
- Legacy Home search redirects to Meetings while preserving other parameters.
  Record/upload destinations and keyboard focus are checked without recording
  real audio. Delete failure/retry uses actual confirmation and UI controls.
  HTML-like summary input remains text, without an image/event-handler node.
- Keyboard focus is asserted and captured on meeting tabs, rows, account tabs,
  and the record action. Page/main horizontal bounds and at most 100 mounted
  meeting links are asserted; lower Home records and the last mounted Meetings
  row are reached through the real internal scroll container.

## Matched performance experiment

Compare baseline **`/`** from the preserved export at
`dd3f2f7479f366695eb70fb2c1beccf58a4847ca` with implementation **`/meetings`**,
not Home's intentionally short recent list. Both receive the identical ordered
200-record payload and two accounts, verified against the controller fixture.
Fixture SHA-256:
`57554226dc910f52ed8275980263309fede036e4f03fe7e5d616009c712e574a`.

Three baseline/implementation pairs run interleaved in the same browser process,
each in a fresh context, at 1440×1000/light with the same locale/timezone/date.
The response metric starts when the intercepted meeting `fetch` promise resolves
and ends at the first DOM mutation containing meeting links. It includes client
scheduling, response decoding, and rendering; it is **not CPU-only render time,
LCP, full-list completion time, or live-server latency**.

After the expected row count, fonts readiness, and two animation frames, the
harness measures whole-document DOM nodes. Its separately recorded
`navigationToLoadedMs` starts before context setup/navigation and ends at that
same readiness point; it is not compared with older network-idle measurements.

| Variant | Response-to-first-row ms, runs 1/2/3 | Median ms | DOM nodes, each run | Mounted rows |
|---|---|---|---|---|
| Baseline `/` | 369 / 368 / 371 | 369 | 14,014 | 200 |
| Implementation `/meetings` | 47 / 47 / 47 | 47 | 2,128 | 100 |

Observed in this local synthetic experiment: **84.8% fewer DOM nodes** and
**87.3% lower median response-to-first-row elapsed time**. These are descriptive
measurements, not production SLAs or universal performance guarantees.
`navigationToLoadedMs` values are 1062.097036 / 992.135074 / 957.668678 baseline
and 379.706660 / 369.116734 / 364.107246 implementation.

The controller's older 14,028-node, approximately 365–366ms artifacts and UI1's
component benchmark use different measurement contexts. Neither is mixed into
this comparison. Raw full-precision values are in `task-4-browser-final/results.json`.

## Actual visual inspection and regression fixes

Images were opened and inspected, including contact sheets built from the actual
viewport captures, not inferred from numerical overflow checks. Home, Meetings,
Accounts overview/insights, picker, and account focus were reviewed at all ten
width/theme combinations. All three prototype views were inspected across the
same ten combinations. The prototype provides visual direction, not fictitious
calendar data or a requirement to replace the existing application navigation.

Mobile controls wrap without losing actions; account tab/filter rails remain
scrollable. Desktop keeps the existing sidebar, compact rows, and account
selector/detail split. Internal-scroll captures show the sixth Home record,
last displayed Meetings row, display-page controls, and Load More reachable
above the mobile navigation. The implementation deliberately replaces prototype
calendar cards with setup instructions and uses real existing account sections.

Three concrete regressions were corrected, with failing browser evidence first:

| Regression | Red evidence under the log root | Minimal source correction |
|---|---|---|
| Account overview tab at 1440/light had `:focus-visible=true` but `outline:none` and no shadow | `task-4-focus-red.json`, `task-4-focus-red.png`; initial smoke 9/11 | `frontend/src/app/globals.css`: suppress outlines only for non-focus-visible focus |
| At 1440/light the account picker opened underneath the left sidebar; its checkbox click was intercepted by the sidebar `/kb` link | `task-4-picker-red.json`, `task-4-picker-red.png`; initial full run 42/53 | `frontend/src/components/AccountTreePicker.tsx`: remove breakpoint right alignment from the leading-toolbar popup |
| Actual dark screenshots revealed pale labels on a white picker, even after an initial all-green interaction run; measured contrast was 1.990154:1 | `task-4-contrast-red/results.json`, `task-4-contrast-red/account-filter-360-dark.png`, `task-4-contrast-red.log`; targeted 0/1, exit 1 | Same picker: replace nonexistent `dark:bg-surface-low` with registered `dark:bg-surface-lowest` |

The controller explicitly authorized the expanded minimal layout/class scope
after the pointer-interception evidence. AccountTreePicker is the affected
MeetingList filter component; no business/auth logic changed. Final normal
checkbox clicks and a label/background contrast assertion of at least 4.5:1 pass
at all widths/themes; the targeted dark rerun passed 1/1 before the final 56/56.
No force clicks, hidden controls, overflow-clipping workaround, or disabled
assertions were used. Other early failures were harness selector ambiguities
(header versus list search, and nested action text), not eleven product defects.

### Screenshot index

The ignored local artifact root is
`.superpowers/sdd/2026-11-07-meeting-workspace-ui/task-4-browser-final/`.
`results.json` lists every one of the 172 original screenshots. For each
`{width}` in `360,390,768,1024,1440` and `{theme}` in `light,dark`:

- `prototype-{home,meetings,accounts}-{width}-{theme}.png`
- `{home,meetings,accounts-overview,accounts-insights,accounts-meetings,accounts-inherited}-{width}-{theme}.png`
- `{account-filter,account-focus,meeting-focus,meeting-row-focus}-{width}-{theme}.png`
- `{home-recent-scroll,meetings-last-row}-{width}-{theme}.png`
- Derived inspection sheets: `inspection-{width}-{theme}.png` and
  `inspection-prototype-{width}-{theme}.png`.

Additional evidence includes `performance-{baseline,implementation}-{1,2,3}.png`,
`meeting-initial-loading.png`, `meeting-initial-error.png`,
`meeting-pagination-error-retained.png`, `meeting-genuine-empty.png`,
`home-independent-insights-error.png`, `home-genuine-empty.png`,
`account-insights-error.png`, `account-section-empty-after-retry.png`,
`account-initial-loading.png`, `account-initial-error.png`,
`account-access-lost.png`, `parent-save-after-tab-switch.png`,
`meeting-account-race.png`, and `meeting-delete-error.png`.

## Reused infrastructure evidence and limits

Per controller authorization, unchanged route/IaC evidence was reused:
`(cd infra && npm test -- --runInBand)` passed **28 suites / 322 tests**,
logged in `controller-infra-final.log`. The offline synthesis logged in
`offline-cdk-synth.log` exited 0 and its manifest contains **13 stack artifacts**.
The controller ran `npx --no-install ts-node --prefer-ts-exts bin/infra.ts` from
`infra`, with `CDK_DEFAULT_ACCOUNT=180294183052`,
`CDK_DEFAULT_REGION=ap-northeast-2`, the SDD `offline-cdk.out` output directory,
and merged context including `"aws:cdk:bundling-stacks":[]`.
Existing deprecation/validation warnings remain; assets were deliberately not
bundled. This is not credential-backed provider or deployment verification.

Unchanged hashes at Task 4 verification:

- `infra/lib/frontend-stack.ts`:
  `5190fd941c3ab8b6329c38d672f71fb81e1b75db20b99081325b23666ed3dad4`
- `infra/test/frontend-stack.test.ts`:
  `e4b60c5ae230a2b1339c4dcf5482de2dcf778f797cfc00c36b939062a7af1451`

Only Chromium with synthetic APIs was exercised. Browser inherited-read and
access-loss fixtures prove UI behavior, not backend authorization. No real user,
AWS provider, Outlook, microphone, scheduler, or deployed application acceptance
was performed. At this Task 4 checkpoint, Upcoming was setup-only; calendar wiring
and synthetic calendar states were subsequently implemented in Calendar Task 5.

## Final acceptance follow-up

On 2026-11-07, independent task reviews and the complete 83-path feature review
finished. The final review found two integration defects: loaded agenda events
beyond 50 were unreachable, and successful Home deletion left stale rows/counts.
One consolidated fix adds independent bounded timed/all-day display navigation
and the Home success callback. Its scoped independent rereview approved both
fixes, with no new actionable finding. No review finding remains parked.

The controller independently reran frontend lint, all **431 unit tests**, the
production static build and all **76 browser checks** after the final fix.
Everything passed; lint retains 13 existing warnings and no errors. Browser
checks preserve the original width/theme matrix and add bounded 20+20+11 event
pages, keyboard navigation, continuation failures, clock clamping and Home
deletion failure/retry/backfill/empty behavior. Earlier performance numbers
remain measurements of their dated runs, not new production claims.

Final local evidence remains under
`.superpowers/sdd/2026-11-07-meeting-workspace-ui/`:
`final-review.md`, `final-fix-report.md`, `final-fix-review.md`,
`controller-final-fix-frontend.log`, `controller-final-fix-browser.log` and
`controller-final-fix-browser/results.json`. The controller also inspected the
final timed-event-51 and Home-backfill captures. Calendar/backend/MCP acceptance
and its operational limits are recorded in
`2026-11-07-calendar-sync-validation.md`.

Implementation remains uncommitted in the isolated managed worktree; the
original dirty checkout is preserved. No branch, PR, deployment, real Outlook
reader or scheduler was created. Local review evidence is retained until
integration; it is not a deployed artifact.
