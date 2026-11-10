# Enterprise User Guide Implementation Plan

Status: historical implementation record (2026-11-10); not a current reference.

> **For agentic workers:** Use superpowers:executing-plans to implement this
> plan task-by-task. Execution and a separate review pass stay inline because
> this host cannot close spawned agents as required by the user's lifecycle rule.

**Goal:** Publish a detailed, source-verified Korean product guide on the
existing TTOBAK GitHub Pages site.

**Architecture:** Extend the existing static guide and explicit asset packager.
The application is only inspected and previewed locally with fictional API
fixtures; no application behavior changes.

**Tech Stack:** Semantic HTML, CSS, browser JavaScript, Python stdlib, Playwright.

**Spec:** `docs/superpowers/specs/2026-11-10-enterprise-user-guide-design.md`

## Global constraints

- Preserve the original dirty checkout and existing Pages publication boundary.
- Publish no private source archive, production data or credentials.
- Label source configuration and synthetic UI independently of deployment.
- Keep the full guide readable/printable without JavaScript.
- Apply the installed content-review rubric before deployment.

## Review focus

- Newly added/conditional menus must not silently disappear from the guide.
- Sharing and hierarchy must not imply broader access than the current grants.
- Upload, preview, extraction and search readiness must remain distinct.
- Search must find body text and preserve the printable content.
- Mobile layout, keyboard focus and images must not hide necessary instructions.

## Tasks

### 1. Inventory and walkthrough

- [ ] Record current menu/detail/settings coverage and consequential source facts
  in `docs/user-guide/README.md`.
- [ ] Preview each menu with fictional fixtures and record limitations.

### 2. Product manual and navigation

- [ ] Expand `docs/user-guide/index.html` with all menu workflows, chat/model
  mapping, permissions, mechanisms, examples and recovery.
- [ ] Update `style.css` and `guide.js` for desktop/mobile contents, full-text
  search, themes, reading position and complete printing.
- [ ] Preserve/extend the explicit packager allowlist only for reviewed images;
  test unpublished paths and symlink rejection if the boundary changes.

### 3. Verification and content gate

- [ ] Run the guide packager, docs unit checks and JavaScript syntax check.
- [ ] Check real browser layouts, keyboard/search/theme behavior, anchors,
  print media, console and image readability.
- [ ] Perform a separate rubric review and record its findings and score.

### 4. Publish and confirm

- [ ] Commit/push only guide-owned paths and create a concrete PR.
- [ ] Check current-head required CI/review and resolve substantive findings.
- [ ] Publish through the existing Pages workflow once its delivery gates pass.
- [ ] Confirm the public revision and guide interactions.
