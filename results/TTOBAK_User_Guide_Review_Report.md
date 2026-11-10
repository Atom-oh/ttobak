# TTOBAK User Guide Content Review

## Review metadata

| Field | Value |
|---|---|
| Date | 2026-11-10 |
| Iteration | 2: meeting workflows and chat/model selection |
| Scope | 28 chapter navigation; complete start, Home, chat/model, recording/upload, meeting review, calendar, follow-up and simulation workflows |
| Reviewer | Codex inline, separate rubric pass; no independent content-review subagent was invoked |
| Rubric | Installed AWS Content Plugin `agents/content-review-agent.md` |
| Scale | 90 points: Playwright MCP unavailable, so the rubric's 10-point MCP visual category is exempt |
| Current score | **90 / 90** |
| Verdict | **PASS**; also meets the loaded plugin's mandatory >=85 deployment gate |

The user's lifecycle rule requires closing every spawned agent. This host has no
`close_agent` tool, so review ran inline. Independent latest-head CI review remains
required before merge. Local Python Playwright/Chromium provided additional real
browser evidence; that evidence is not represented as a MCP session or credited
to the exempt rubric category.

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

Scoring follows the fixed rubric: deductions require a located, quoted defect;
each defect costs one quarter of its category, summed then rounded once to the
nearest 0.5 with midpoints up. No unresolved defect was identified after revision,
so no deduction applies. Informational scope boundaries do not count as defects.

## Evidence and revisions

- Eight text-reviewable SVG menu illustrations were checked against local UI
  walkthroughs. Captions explicitly distinguish diagrams from illustrations. They
  contain no customer records, credentials, internal URLs or remote resources.
  Local raster walkthrough evidence remains private and outside the public artifact.
- The application walkthrough opened 26 local routes: 19 primary/detail routes and
  seven Account tab selections. API responses were fictional; non-loopback HTTP
  and external WebSocket construction were blocked. This was not live acceptance.
- Local guide checks covered 1920x1080, 3840x2160, 768x1024, 390x844 and 360x800;
  no page overflow, console/page errors or HTTP failures remained.
- Verified body search, inert search input, empty results, keyboard shortcut,
  mobile contents/focus/Escape, active chapter, theme persistence, all image
  loading, no-JavaScript reading and complete A4 PDF output.
- Fixed the doubled anchor offset and missing local favicon discovered during
  browser checks. Empty per-chapter contents stay hidden for legacy chapters.
- Nine documentation unit checks passed, including seven public-packager cases.
  Reviewed image references are copied; unreviewed images and symlinks are rejected.
  Local-only CSP, anchor integrity and existing-output preservation remain checked.
- `check_docs.py` and JavaScript syntax validation passed. No runtime application,
  AWS configuration or workflow/review-gate code changed.

## Critical and warning issues

None outstanding.

## Source-omission cross-check

| Source section | Status | Note |
|---|---|---|
| Existing 16 guide workflows | INCLUDED | Preserved while improving navigation and visuals |
| Source inventory / current menus | PARTIAL | Remaining customer/organization chapters are delivered by the next PR |
| Fictional UI orientation | INCLUDED | Eight reviewed SVG menu illustrations |
| Mobile, search, themes, print | INCLUDED | Browser-verified navigation foundation |
| Production and native acceptance | OMITTED deliberately | No production login, customer records or macOS capture was performed |

## Informational boundaries

1. Public illustrations contain generic menu labels. Public assets never contain
   the internal fictional walkthrough evidence archive.
2. This delivery completes meeting and chat workflows. It does not claim that the final
   enterprise content expansion is complete. The subsequent content PRs preserve
   the existing 95,000-byte AI review limit and are checked independently.

## Revision checklist and next steps

- [x] Resolve browser defects and publication-boundary failures.
- [x] Inspect all eight public illustration assets.
- [x] Record review scale, limitations and source omissions honestly.
- [ ] Obtain required latest-head CI review before merging these workflows.
- [ ] Complete and re-review the dependent 28-chapter content expansion.
