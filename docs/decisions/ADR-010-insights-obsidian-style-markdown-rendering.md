# ADR-010: Rich Markdown Reading Without Content Migration

- Status: Accepted; later document features extend the original reading surface.
- Decision date: Not recorded; design specification dated 2026-04-25.
- Implementation checked: 2026-09-13.

## Context and decision

Long research reports needed clearer headings, navigation, readable code/tables,
and portable Markdown. Use shared React Markdown components while retaining the
existing S3 Markdown format. MDX required a different content/execution pipeline;
a block editor added unnecessary editing complexity to a reading surface.

## Current implementation

`MarkdownRenderer` composes headings, callouts, tables, blockquotes, code blocks,
and Mermaid diagrams. It uses GFM and an Obsidian-style callout plugin, followed
by raw-HTML parsing **and sanitization**. Do not remove sanitization or treat
external Markdown as trusted HTML.

Shiki highlighting is dynamically imported in `CodeBlock` and falls back to plain
code. Mermaid is dynamically imported with explicit `securityLevel: 'strict'`;
diagrams also support expanded viewing. TOC navigation uses heading IDs and
scroll observation. List/detail pages provide richer browsing and Markdown export.
There is no fixed component-count, bundle-size, or sidebar-width contract.

The research agent's `save_report` splits output at h2 headings into S3 sections
and stores section metadata. These report sections are distinct from the child
research jobs introduced by ADR-011. Plain Markdown remains the stored source;
this is not an MDX migration.

The renderer explicitly permits `transcript://` links and resolves them to
`#ts-{segmentId}` for summary navigation (ADR-013). Preserve both the sanitizer
protocol allowlist and the URL-transform handling; ordinary unsafe schemes
remain rejected.

## Consequences and risks

Shared rendering improves readability without migrating stored documents and
preserves Markdown portability. It adds component/plugin maintenance and deferred
highlighting/diagram loading. Invalid diagrams can still fail to render, and
long reports still require navigation. Sanitization and Mermaid's strict mode are
current protections, not reasons to assume all future renderer changes are safe.

## Evidence

- [markdown](../../frontend/src/components/markdown): `MarkdownRenderer`,
  `remarkCallout`, `CodeBlock`, `MermaidBlock`, and `TOCSidebar`.
- [ResearchDetailClient.tsx](../../frontend/src/app/insights/research/[researchId]/ResearchDetailClient.tsx):
  report/section navigation.
- [tools.py](../../backend/python/research-agent/tools.py): `_split_sections`,
  `save_report`.
