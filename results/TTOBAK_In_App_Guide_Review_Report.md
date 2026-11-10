# In-app User Guide Content Review

## Review metadata

| Field | Value |
|---|---|
| Date | 2026-11-10 |
| Scope | In-app entry points, embedded reading, screenshot enlargement and CloudFront framing |
| Reviewer | Codex inline review using the installed content-review-agent rubric |
| Score | **88.5/90** |
| Verdict | **PASS**, exceeding the mandatory pre-deployment score of 85 |
| Visual evidence | Local Python Playwright/Chromium, production export with proposed CloudFront headers |

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
duplication remains one accepted warning. There are no unresolved new Critical
or Warning findings in the corrected change.

The fixed rubric deducts one quarter of the applicable category per located
defect, sums before rounding to 0.5 with midpoint deductions rounded up, and
keeps Critical findings separate. No new deduction is justified by the corrected
implementation and evidence. The initial static-only review missed CloudFront's
framing denial. Required independent CI review identified that deployment
blocker; this revision replaces the earlier acceptance claim with header-aware
checks. It also fixes the responsive-ancestor issue reported by that review.

## Evidence

- Desktop sidebar, header help, mobile menu and profile open the same manual
  without changing the route or unmounting the current workspace.
- At 1440x1000, 768x1024 and 390x844, 64 browser assertions pass against the
  production static export with proposed CloudFront response headers. The
  public-origin fixture avoids loopback address-space restrictions introduced
  by intercepted HTML responses. Checks cover search, theme,
  screenshot loading, viewport fit, close/focus return, Escape and preservation
  of the mobile menu beneath the reader. The reader stays visible when either
  desktop opener's ancestor becomes hidden after a mobile breakpoint crossing.
  Page-error lists are empty.
- Escape inside the isolated guide closes the app reader through a single
  close-only message. The parent accepts only the active iframe's WindowProxy,
  its opaque origin and the exact close string. Unrelated close messages are
  ignored. Search reset, mobile contents dismissal and screenshot dismissal
  retain precedence over closing the reader; standalone reading is unchanged.
- Embedded screenshots open inside the reader, retain their original pixel
  width, allow scrolling and return focus on close. Escape dismisses the image
  viewer while preserving the manual. Standalone screenshot links are unchanged.
- The opaque-origin iframe cannot read app DOM or storage. It grants scripts
  and printing, without same-origin access, popups or top navigation. The change
  adds no native capability.
- The existing distribution serves `/user-guide/*` from its private S3 origin,
  with `SAMEORIGIN` and `frame-ancestors 'self'`. Its dedicated CSP permits only
  local scripts/styles/images and no connections or form submissions. CDK
  assertions retain default application `DENY`/`frame-ancestors 'none'`, constrain
  the manual policy to exactly one path, and preserve the existing Billing
  exception. No origin, IAM grant, security group or secret value is added.
  A negative browser check reproduces the old headers' empty reader.
- The production export contains exactly the 40 public allowlisted assets.
  The four source assets are byte-identical to their repository sources; all
  image bytes retain the reviewed archive identities. Internal documentation,
  manifests and review reports are excluded.
- Online packaging passes. An invalid offline archive fails, preserves all
  previous generated asset bytes and leaves no temporary staging directory.
- Frontend: all 709 unit tests pass after the latest base integration.
  Lint has zero errors, and production build
  passes without development authentication. Fifteen lint warnings are in
  existing files. Documentation packaging/media tests (18), publication tests
  (11), documentation validation and JavaScript syntax checks pass.
- Infrastructure synthesis, TypeScript compilation and all 335 tests pass.

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

An infrastructure-owned `userGuideRevision: same-origin-framing-v1` marker is
published only after the distribution update. Exports containing the manual
require that exact deployed marker before any upload or invalidation. Negative
publication tests demonstrate that missing, boolean and obsolete markers leave
the site untouched; the ready path publishes the manual and preserves config.
This corrects the independent review's frontend/infrastructure ordering blocker.

Deploy the reviewed FrontendStack framing behavior before the frontend assets.
Wait for CloudFront propagation, verify the live manual headers and retained
application framing denial, and then publish through the existing frontend
workflow. Latest-head independent review and infrastructure synth/tests remain
required release gates. A local static-server pass alone is not acceptance.
