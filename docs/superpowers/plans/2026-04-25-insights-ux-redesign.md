# Historical implementation record: Rich Insights reading UI

- Original plan date: 2026-04-25.
- Historical proposal, not an execution checklist or current review mandate. Source pointers checked 2026-09-13.

## Scope and rationale

Replace plain-looking Insight/Research Markdown with an Obsidian-compatible reading surface. A shared `MarkdownRenderer` would map headings, blockquotes, tables, inline/block code, links, and callouts to focused components instead of duplicating article markup.

Distinctive choices included Korean-capable heading anchors, summary/warning/tip/danger/info callouts parsed from `[!type]`, lazy Shiki highlighting with a plain-code fallback, clipboard feedback, horizontally scrollable tables, and a desktop h2/h3 table of contents tracked by IntersectionObserver. Insight lists gained a persisted card/table preference; exports included Markdown and YAML frontmatter.

## Risks and intended validation

Raw Markdown HTML required sanitation after parsing, with only the callout attributes explicitly allowed. Loading a highlighter must not block readable text. Narrow screens needed overflow handling and a hidden TOC; export/copy behavior needed to preserve source text.

Intended checks covered TypeScript/build, both detail pages, TOC scroll tracking, callouts/tables/code, persistent view choice, export, and mobile layout. No completed validation results were recorded; the checklist was entirely unchecked.

## Current references

- [ADR-010](../../decisions/ADR-010-insights-obsidian-style-markdown-rendering.md), [renderer](../../../frontend/src/components/markdown/MarkdownRenderer.tsx), [code highlighting](../../../frontend/src/components/markdown/CodeBlock.tsx), [TOC](../../../frontend/src/components/markdown/TOCSidebar.tsx), [table view](../../../frontend/src/components/InsightsTableView.tsx).
- [2026-08-04 readability record](2026-08-04-insights-readability-redesign.md) extends structured content and layout. Original exact colors, widths, dependency versions, and component line numbers are historical choices, not current review requirements.
