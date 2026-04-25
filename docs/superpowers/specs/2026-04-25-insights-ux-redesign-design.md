# Ttobak Insights UX Redesign — Design Spec

**Date:** 2026-04-25
**Status:** Approved
**Figma:** https://www.figma.com/design/f3DqT7x6kK6994MeUjMwuX

## Overview

Upgrade the Insights page from "메모장 느낌" to Obsidian-grade reading experience. Desktop-first. Primary consumers: Deep Research reports (5000-20000 words) and Tech articles.

## 1. View Modes (Card + Table Toggle)

### Card View (default)
- 2-column grid layout for desktop (1-column mobile)
- Each card: title, source, date, tags, 3-line summary, KB badge, Read button
- Tag chips filterable, sort dropdown (newest/oldest/title)

### Table View
- Obsidian Dataview style: sortable columns (Title, Source, Date, Tags, KB)
- Row click → article detail
- Compact rows with tag pills
- Bottom: "Showing 1-10 of N documents" + pagination

### Toggle
- Right-aligned icon buttons: `Card | Table`
- Persisted in localStorage

## 2. Rich Markdown Rendering (Core Feature)

Replace inline Tailwind `<ReactMarkdown>` with dedicated component system.

### Component Structure

```
frontend/src/components/markdown/
  MarkdownRenderer.tsx   — ReactMarkdown wrapper + custom component mapping
  Heading.tsx            — h1-h6: left cyan accent bar + anchor link
  Callout.tsx            — Obsidian-style admonition boxes
  CodeBlock.tsx          — Shiki syntax highlighting + copy + lang label
  DataTable.tsx          — Responsive: horizontal scroll + striped + sticky header
  BlockQuote.tsx         — Left bar + translucent bg + italic
  TOCSidebar.tsx         — Right sticky: auto-extract headings + scroll highlight
```

### Heading Component
- h2: 4px left cyan bar + bold 22px + hover anchor icon (🔗)
- h3: semi-bold 18px, no bar
- h4-h6: medium weight, decreasing size
- All headings: auto-generated `id` for anchor linking

### Callout Component
Parses `> [!type] title` syntax (Obsidian-compatible).

| Type | Color | Icon | Use Case |
|------|-------|------|----------|
| `[!summary]` | Cyan (#00E5FF) | 💡 | Executive Summary |
| `[!warning]` | Amber (#EA9619) | ⚠️ | Cautions, limitations |
| `[!tip]` | Green (#4DC290) | ✅ | AWS recommendations |
| `[!danger]` | Red (#EF4444) | 🚫 | Security risks |
| `[!info]` | Blue (#3B82F6) | ℹ️ | Background info |

Structure: 4px left accent bar + tinted background (4% opacity) + icon + title + body.

### CodeBlock Component
- **Shiki** for syntax highlighting (lazy loaded via `next/dynamic`)
- Theme: `github-dark-default`
- Languages: bash, python, json, yaml, terraform, typescript, go
- Top bar: language label (left) + Copy button (right)
- Dark background (#0a0a0f) with border

### DataTable Component
- Wrapper with horizontal scroll on overflow
- Header: sticky, 4% white bg
- Rows: alternating subtle stripe, 1px separator
- Cells: proper padding, left-aligned
- First column: medium weight (row identifier)

### MermaidBlock Component
Renders ` ```mermaid ` fenced code blocks as SVG diagrams.

- **Library**: `mermaid` (lazy loaded via `next/dynamic`)
- **Theme**: dark mode compatible (`theme: 'dark'` when `.dark` class present)
- **Supported types**: graph, flowchart, sequenceDiagram, classDiagram, stateDiagram
- **Fallback**: if rendering fails, show raw code block with Shiki
- **Copy**: "Copy Mermaid" button for pasting into other tools
- **Size**: auto-width, max-height 600px with scroll
- **Integration**: `CodeBlock` checks `language === 'mermaid'` and delegates to `MermaidBlock`

The Research Agent generates Mermaid diagrams for:
- Network topology (VPC, Direct Connect, VPN)
- Architecture diagrams (microservices, event-driven)
- Data flow (pipeline stages)
- Decision trees (process flows)

### BlockQuote Component
- 3px left bar (cyan 40% opacity)
- Background: cyan 3% opacity
- Text: italic, muted color
- Attribution line after `—` in smaller text

### TOCSidebar Component
- **Desktop only**: 264px right sticky panel
- Title: "ON THIS PAGE" (uppercase, tracked)
- Extracts h2 (level 0) and h3 (level 1)
- Active section: 2px cyan left indicator + cyan text + semi-bold
- Inactive: muted gray, regular weight
- Click: smooth scroll to section
- IntersectionObserver for auto-tracking
- **Mobile**: hidden (optional floating TOC button later)

## 3. Article Detail Page Layout

```
Desktop (1440px):
├── Sidebar (256px) — existing nav
├── Content (920px, max-width 800px centered)
│   ├── Back button
│   ├── Header: badges, title, meta, tags, original link
│   └── Markdown content (MarkdownRenderer)
└── TOC (264px) — sticky right panel

Mobile (<768px):
├── Mobile header with back button
└── Full-width content (no TOC)
```

### Header Card
- Type badge (News amber / Tech blue / Research purple)
- Mode badge (Quick green / Standard blue / Deep purple) — research only
- Title: bold 26-28px
- Meta: source count, word count, date, reading time
- Tags: clickable pills
- Original link: "🔗 View Original Article"

## 4. Export Feature

### Export Button (article header area)
- **Copy as Markdown** — clipboard with Obsidian-compatible frontmatter
- **Download .md** — file download with frontmatter
- Future: **Send to Notion** (Notion API integration)

### Obsidian-Compatible Frontmatter
```yaml
---
title: "Article Title"
date: 2026-04-25
tags: [금융, 보안, 클라우드, AWS]
source: ttobak-research
type: research | news | tech
url: https://original-source.com
---
```

Ensures drag-and-drop into Obsidian vault works immediately.

## 5. Dependencies

| Package | Purpose | Size | Loading |
|---------|---------|------|---------|
| `shiki` | Syntax highlighting | ~2MB | Lazy (next/dynamic) |
| `mermaid` | Diagram rendering | ~1.5MB | Lazy (next/dynamic) |
| `react-markdown` | Markdown parsing | Existing | Eager |
| `remark-gfm` | GFM tables/checkboxes | Existing | Eager |
| `rehype-raw` | HTML in markdown | Existing | Eager |
| `rehype-sanitize` | XSS prevention | Existing | Eager |

## 6. Files to Create/Modify

### New Files
- `frontend/src/components/markdown/MarkdownRenderer.tsx`
- `frontend/src/components/markdown/Heading.tsx`
- `frontend/src/components/markdown/Callout.tsx`
- `frontend/src/components/markdown/CodeBlock.tsx`
- `frontend/src/components/markdown/DataTable.tsx`
- `frontend/src/components/markdown/BlockQuote.tsx`
- `frontend/src/components/markdown/MermaidBlock.tsx`
- `frontend/src/components/markdown/TOCSidebar.tsx`
- `frontend/src/components/InsightsTableView.tsx`

### Modified Files
- `frontend/src/components/InsightsList.tsx` — add view toggle + table view
- `frontend/src/app/insights/[sourceId]/[docHash]/InsightDetailClient.tsx` — replace markdown rendering + add TOC + export
- `frontend/src/app/insights/research/[researchId]/ResearchDetailClient.tsx` — same markdown upgrade

## 7. Design Decisions

- **Obsidian callout syntax** (`> [!type]`) chosen over custom markers for future Obsidian export compatibility
- **Shiki over Prism.js** — better theme support, WASM-based, no CSS theme files needed
- **TOC via IntersectionObserver** — no scroll event listener, better performance
- **No MDX** — existing S3 markdown content stays as-is, zero migration cost
- **Desktop-first** — long research reports are primarily read on desktop; mobile gets simplified layout
- **Export frontmatter** — YAML format compatible with both Obsidian and common static site generators
