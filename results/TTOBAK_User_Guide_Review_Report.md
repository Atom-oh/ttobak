# TTOBAK User Guide Content Review

## Review metadata

| Field | Value |
|---|---|
| Date | 2026-11-10 |
| Iteration | 5: actual app UI replaces simplified menu illustrations |
| Scope | All 28 chapters and 36 actual UI screenshots |
| Reviewer | Codex inline rubric review; independent latest-head CI review remains required |
| Rubric | Installed AWS Content Plugin `agents/content-review-agent.md` |
| Scale | 90 points: Playwright MCP cannot launch its missing Chrome distribution; its 10-point category is exempt |
| Current score | **90 / 90** |
| Verdict | **PASS**, including the loaded plugin's mandatory score >=85 before publication |

Local Python Playwright/Chromium provided real browser evidence separately;
it is not represented as a Playwright MCP session. No subagent was started.
Scoring starts each category at full marks: each located defect deducts one
quarter of its category, summed and rounded once to the nearest 0.5, midpoints
up. No unresolved defect was found after revision; informational limitations
do not deduct points.

## Quality gate result

| Category | Score | Critical | Warning | Info |
|---|---:|---:|---:|---:|
| Layout | 8/8 | 0 | 0 | 0 |
| Terminology | 8/8 | 0 | 0 | 0 |
| No hallucination | 12/12 | 0 | 0 | 0 |
| Language consistency | 8/8 | 0 | 0 | 0 |
| No sensitive data | 12/12 | 0 | 0 | 1 |
| Content-type quality | 2/2 | 0 | 0 | 0 |
| Icon appropriateness | 5/5 | 0 | 0 | 0 |
| Readability | 5/5 | 0 | 0 | 0 |
| Accessibility | 5/5 | 0 | 0 | 0 |
| Structural completeness | 5/5 | 0 | 0 | 1 |
| Data accuracy / references | 5/5 | 0 | 0 | 0 |
| Legal compliance | 5/5 | 0 | 0 | 0 |
| Message clarity | 5/5 | 0 | 0 | 0 |
| Duplication / gaps | 5/5 | 0 | 0 | 0 |
| **Total** | **90/90** | **0** | **0** | **2** |

## Evidence and corrected findings

- The user's requested actual UI is now shown directly. All 36 captures were
  visually inspected at original resolution or in full-resolution review sheets.
  Sidebar labels, menu order, icons, controls and layouts are the real frontend.
  No app DOM/CSS rewrite, redrawing, cropping or image resizing was used.
- Captured revision: `0d9f1075169ba8d4692036dc98360b6514ef3b44`.
  Later main changes before review affect the MCP bundle/intake, not these UI
  components. The manifest records the captured revision rather than implying
  that guide tooling changes existed at capture time.
- A Linux system-font issue made some Korean body text display as separated
  jamo. Configuring the installed Korean system font fixed the browser environment;
  the final complete capture pass has legible Hangul and unchanged app styles.
- The local app used fictional read-only fixtures. External HTTP/WebSocket
  access was blocked, no API mutations were attempted, and all 36 page-error
  lists were empty. No provider request, production login or microphone was used.
- Every figure has specific alt text, an original-size local link, and a caption
  disclosing actual app UI, example data and capture date. The introduction
  explains permission/configuration differences. Mobile captures stay at or
  below their original 390-pixel width. All images load eagerly and stay visible.
- The tested public artifact has 141 valid anchors and 40 explicit files.
  All 45 reader/image checks passed at desktop FHD/4K, tablet and small mobile
  sizes. Search, themes, keyboard navigation, no-JavaScript reading and printing
  passed with no console/page/HTTP errors. The 83-page A4 PDF contains exactly
  36 screenshot placements; a rendered screenshot page was visually inspected.
- All 18 documentation unit tests, documentation validation and JavaScript
  syntax validation passed. Archive/image identity, dimensions, inventory,
  bounded fetches and rejection-before-output are covered. The media gate was
  implemented only after its new tests failed against the original packager.

## Publication boundary

The reviewed lossless images total a 1,465,184-byte stored ZIP. It contains only
36 explicitly named WebP images. The documentation-only Release asset is
separate from application version releases and is not marked latest. Its exact
URL, size and SHA-256, plus every image identity/dimension/route, are reviewed in
`docs/user-guide/screenshots.json`. The packager validates all media before
creating output and emits only local files. The self-only CSP remains unchanged.
The Pages reader makes no Release or API requests. Internal manifests, fixtures,
plans and reports are not copied into the site. No workflow, source-review limit,
application runtime or AWS deployment code changed.

## Source-omission cross-check

| Requested/source section | Status | Note |
|---|---|---|
| Actual application menu UI | INCLUDED | Real sidebar and all primary menus, replacing eight schematic figures |
| Detail controls and Account tabs | INCLUDED | Accounts, contacts/people, projects, documents/library, news/Tech/research |
| Settings and permissions | INCLUDED | Notion form, import, admin and AI connection permissions; meeting sharing |
| Recording, upload, meeting and follow-up | INCLUDED | Real entry screens and example meeting controls |
| Mobile UI | INCLUDED | Actual responsive Home and recording layouts |
| Enterprise explanations and workflows | INCLUDED | Existing verified 28-chapter manual retained |
| Production/provider/native acceptance | OMITTED deliberately | Clearly disclosed; not required to show actual web UI |

## Informational boundaries

1. Fictional customer/contact/meeting records use explicit example names and
   reserved `example.invalid` addresses. The development user's source-defined
   `Dev User` / `dev@ttobak.io` label remains unchanged; it is not a retrieved
   production account. Notion inputs contain only the real form's placeholders.
2. Empty lists show real product empty states. Enabled Billing and admin fixtures
   illustrate conditional controls; they do not prove live permissions, costs,
   completed AI work, native capture or provider authorization.

## Revision checklist and next steps

- [x] Replace schematic menus with actual UI and inspect every capture.
- [x] Preserve UTF-8 readability, source identity and publication bounds.
- [x] Verify all screenshots, responsive reading and complete PDF output.
- [x] Pass the mandatory content quality gate before publishing the media.
- [ ] Obtain required latest-head CI and independent review before merging.
- [ ] Verify final Pages publication and all public asset hashes.
