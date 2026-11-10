# Enterprise user guide

Status: historical implementation record (2026-11-10); not a current reference.

## Requested result

Expand the existing Korean public GitHub Pages guide into a complete product
manual. Walk every current menu and explain practical procedures, expected
results, mechanisms, rationale, the right chat surface, AI model configuration,
permissions, recovery, and enterprise adoption.

## Scope and decisions

- Use the existing dependency-free HTML/CSS/JavaScript site and Pages workflow.
  Preserve anchor navigation and full-guide printing without JavaScript.
- Cover every item in `frontend/src/lib/navigation.ts`, conditional Billing,
  recording/upload, meeting detail, Account tabs and people/import, Project
  detail, document detail, Settings sections, and mobile/Mac entry points.
- Search actual section text, with accessible results, keyboard focus and a
  mobile contents drawer. Add an active chapter indicator and light/dark themes.
- Explain Assistant, meeting Q&A, Research Assistant, and external MCP clients
  separately. Model names are source configuration, not live deployment claims.
- Use fictional fixtures for browser walkthroughs. No production login,
  customer content, credentials, account identifiers or internal origins enter
  the public artifact. Screenshots must be visibly captioned as local examples.
- Publish only explicitly allowlisted public assets. Internal evidence and
  documentation remain outside the Pages artifact.
- Korean is scoped to the user-requested public guide. Engineering evidence
  and maintenance documentation remain English.

## Acceptance

Each menu has a purpose, path, prerequisites, numbered procedure, expected
outcome, mechanism/rationale, and relevant recovery guidance or cross-reference.
Source evidence maps consequential claims to current implementation. Browser
checks cover desktop/mobile navigation, search, focus, themes, diagrams and
printing. Packaging validates anchors, assets and the local-only CSP. A separate
content-review pass must satisfy the installed content rubric before publication.

The chosen implementation and publication work are within the user's explicit
request. Existing dirty application changes stay in the original checkout;
the guide uses a separate worktree based on main `fec64b30`.
