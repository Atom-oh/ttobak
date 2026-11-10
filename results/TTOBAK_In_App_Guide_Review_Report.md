# In-app User Guide Content Review

## Review metadata

| Field | Value |
|---|---|
| Date | 2026-11-10 |
| Scope | In-app manual entry points, embedded reading and screenshot enlargement |
| Reviewer | Codex inline review using the installed content-review-agent rubric |
| Score | **88.5/90** |
| Verdict | **PASS**, exceeding the mandatory pre-deployment score of 85 |
| Visual evidence | Local Python Playwright/Chromium, including the production export |

The rubric's Playwright MCP category remains exempt because that server's Chrome
distribution was unavailable in this environment. Local browser checks are
additional evidence, not a claim of an MCP session. Independent current-head CI
review remains required before merge.

The unchanged Korean HTML, captions, screenshot receipt and 36 verified image
identities retain the content assessment in
[the full guide review](TTOBAK_User_Guide_Review_Report.md). This review covers
the added access controls and embedded image-viewing behavior. It does not
recertify every guide statement against later application changes.

## Quality gate

The inherited category scores remain unchanged: all categories have full marks
except Duplication/Gaps, 3.5/5. The existing Account overview/People image
duplication remains one accepted warning. There are no new Critical or Warning
findings in the reviewed change.

The fixed rubric deducts one quarter of the applicable category per located
defect, sums before rounding to 0.5 with midpoint deductions rounded up, and
keeps Critical findings separate. No new deduction is justified by the evidence.

## Evidence

- Desktop sidebar, header help, mobile menu and profile open the same manual
  without changing the route or unmounting the current workspace.
- At 1440x1000, 768x1024 and 390x844, 55 browser assertions pass against both
  development and the production static export. Checks cover search, theme,
  screenshot loading, viewport fit, close/focus return, Escape and preservation
  of the mobile menu beneath the reader. Page-error lists are empty.
- Embedded screenshots open inside the reader, retain their original pixel
  width, allow scrolling and return focus on close. Escape dismisses the image
  viewer while preserving the manual. Standalone screenshot links are unchanged.
- The opaque-origin iframe cannot read app DOM or storage. It grants scripts
  and printing, without same-origin access, popups, top navigation or native IPC.
- The production export contains exactly the 40 public allowlisted assets.
  The four source assets are byte-identical to their repository sources; all
  image bytes retain the reviewed archive identities. Internal documentation,
  manifests and review reports are excluded.
- Online packaging passes. An invalid offline archive fails, preserves all
  previous generated asset bytes and leaves no temporary staging directory.
- Frontend: 665 unit tests pass, lint has zero errors, and production build
  passes without development authentication. Fifteen lint warnings are in
  existing files. Documentation packaging/media tests (18), publication tests
  (8), documentation validation and JavaScript syntax checks pass.

Browser evidence and export identities are recorded in
`TTOBAK_In_App_Guide_Browser_Checks.json`. Tests use synthetic Cognito/API
fixtures; no production credentials, customer data or provider calls are used.

## Limits and publication

The native Mac app loads the same production frontend, so no Rust permission
or application update is introduced. Native macOS/WebKit and system printing
were not executed on this Linux host. Clipboard access remains subject to the
guide's existing visible manual-copy fallback in isolated readers.

Frontend workflow source triggers now include the guide and its packager,
ensuring manual updates rebuild the bundled copy. Existing review, test,
publication ordering, runtime configuration preservation and deployment gates
remain intact.
