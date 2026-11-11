# Workspace density implementation

Status: historical implementation plan for 2026-11-11; current behavior is in
`docs/DESIGN-SPEC.md` and the frontend source.

**Goal:** Fill the available workspace while keeping reading and navigation usable.

**Architecture:** Shared shell sizing and CSS layout classes; explicit changes to
existing page wrappers. No API, authorization, recording, or model changes.

**Tech stack:** Next.js, React, Tailwind 4, local Chromium/Playwright.

**Spec:** [Responsive workspace density](../specs/2026-11-11-workspace-density-design.md)

## Tasks

- [x] Create an isolated worktree; preserve the original checkout.
- [x] Inspect the reference and current navigation/page wrappers; run unit baseline.
- [x] Capture the actual baseline and record measurements and defects.
- [x] Implement common workspace gutters, full-width pages, adaptive card/section
  grids, and shell/navigation sizing. Keep inner reading-width constraints.
- [x] Verify real geometry and interactions across the viewport matrix and themes;
  fix regressions. Run lint, unit tests, production build, and docs checks.
- [ ] Record the design review, evidence, limits, and content quality gate.
- [ ] Deliver through latest-HEAD PR review, required CI, merge and frontend release.

## Execution record

The user explicitly requested design improvements and confirmed the reference.
Routine layout decisions follow that direction without another approval cycle.
The implementation runs inline; no Codex subagents are needed.

Baseline: 771 frontend unit tests passed. Browser fixtures are confined to
loopback, reject API writes, and block external requests. Early fixture runs were
incomplete because network-idle waits conflicted with settings polling; the
capture waits for page hydration instead. A missing SA Activity fixture property
was corrected in the external probe, without changing the application.
