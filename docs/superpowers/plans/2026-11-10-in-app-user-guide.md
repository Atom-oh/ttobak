# In-app User Guide Implementation Plan

> **For agentic workers:** Use superpowers:executing-plans for inline implementation.

Status: historical implementation plan for 2026-11-10.

**Goal:** Make the existing manual readable from the app and web UI.

**Architecture:** A shared guide button opens an in-page HTML dialog. Its
isolated iframe reads the same reviewed guide assets bundled with the frontend.

**Tech Stack:** React, Next.js static export, HTML dialog, existing Python packager.

**Spec:** [Design](../specs/2026-11-10-in-app-user-guide-design.md).

## Constraints and review focus

Keep routes mounted, restore focus, and keep mobile menu Escape independent.
Preserve public asset allowlisting, screenshot identities and existing CSP.
Do not grant guide content native permissions or publish internal reference files.
Rebuilds must validate the new package before replacing previous generated output.

## Task 1: Bundle and expose the manual

- [x] Run frontend baseline tests.
- [x] Observe a failing browser assertion for the missing profile guide entry.
- [x] Add `frontend/scripts/build-user-guide.mjs`, ignored generated output, and build hooks.
- [x] Add `UserGuideButton.tsx` and wire sidebar, header, mobile menu and profile.
- [x] Verify all entry points, responsive dialog, dismissal, focus and guide behavior.
- [x] Run frontend unit tests, lint, build and existing guide packaging tests.
- [x] Record content review and browser evidence.
- [ ] Push a scoped PR, satisfy latest-head independent review, merge and deploy.
- [ ] Verify published manual assets and production guide access.
