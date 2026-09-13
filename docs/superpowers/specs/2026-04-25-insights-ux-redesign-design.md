# Insights Reading and Navigation Redesign

> Historical design record. Original date: 2026-04-25. Original status: Approved.
> Visual specifications and component sketches below are design intent, not a frozen UI.

## Goal and design choices

Make long research reports and technical articles readable, primarily on desktop,
without migrating stored Markdown. The design chose reusable React Markdown
components over MDX, Shiki over Prism for highlighting/theme support, standard
Obsidian callouts, and IntersectionObserver-based TOC tracking rather than continuous
scroll handlers. Mobile would use a simpler full-width reading layout.

## Proposed surfaces

- Card and compact sortable-table views with tags, source/date metadata, pagination,
  and a persisted view preference.
- Headings with anchors and visual hierarchy; responsive tables and blockquotes;
  callouts for summaries, warnings, tips, danger, and information.
- Lazy code highlighting with language labels/copy, and lazy Mermaid diagrams with
  a readable failure fallback. Architecture, network, flow, and state diagrams
  were intended outputs, not trusted executable content.
- Article headers with type/mode, sources, reading metadata, original-source links,
  and copy/download Markdown with frontmatter. Notion publishing was deferred.
- A sticky desktop TOC; mobile would omit it initially. Original cyan colors, exact
  dimensions, component counts, and bundle estimates were mockup choices rather
  than current design-token requirements.

## Research section navigation

The proposed `save_report` path split h2 sections into separate S3 objects while
retaining a full report. Section metadata identified title, slug, order, key, and
word count. A section list, section detail, previous/next controls, and full-report
view would let readers explore long reports without losing navigation. Section
links are distinct from separately executed child research jobs.

## Constraints and validation intent

Retain Markdown sanitization, safe links, responsive overflow, and usable plain
fallbacks. Validate long tables/code, headings, callouts, copy/export, section links,
and light/dark reading. Deferred loading reduces initial work but does not remove
loading failures or ongoing plugin maintenance. No exact library size or export
compatibility was proven by the mockups.

## Current evidence

[ADR-010](../../decisions/ADR-010-insights-obsidian-style-markdown-rendering.md) records
the decision. The [markdown components](../../../frontend/src/components/markdown)
use dynamic imports, sanitized Markdown, and explicit Mermaid strict mode.
[save_report](../../../backend/python/research-agent/tools.py) writes h2 sections;
[ResearchDetailClient](../../../frontend/src/app/insights/research/[researchId]/ResearchDetailClient.tsx)
renders them. Current colors and spacing come from
[globals.css](../../../frontend/src/app/globals.css), not this historical mockup.

The original Figma reference was design `f3DqT7x6kK6994MeUjMwuX`; it is not evidence
of current source behavior. Current references: [documentation map](../../README.md)
and [UI reference](../../DESIGN-SPEC.md).
