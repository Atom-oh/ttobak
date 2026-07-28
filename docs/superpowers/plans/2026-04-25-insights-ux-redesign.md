# Insights UX Redesign Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the "notepad" markdown rendering in Insights with an Obsidian-grade reading experience -- rich headings, callout boxes, syntax-highlighted code blocks, responsive tables, TOC sidebar, Card/Table view toggle, and Obsidian-compatible export.

**Architecture:** Custom ReactMarkdown component system (`components/markdown/`) maps standard markdown elements to styled React components. A remark plugin parses Obsidian `> [!type]` callout syntax. Shiki provides syntax highlighting via lazy-loaded WASM. TOC extracts headings via IntersectionObserver. The list page gains a Card/Table view toggle persisted in localStorage.

**Tech Stack:** React 19, Next.js 16, ReactMarkdown 10, remark-gfm, rehype-raw, rehype-sanitize, Shiki (lazy), Tailwind v4, TypeScript

---

## File Structure

### New Files
| File | Responsibility |
|------|---------------|
| `frontend/src/components/markdown/MarkdownRenderer.tsx` | ReactMarkdown wrapper with custom component mapping |
| `frontend/src/components/markdown/Heading.tsx` | h1-h6 with accent bar, anchor ID, hover link |
| `frontend/src/components/markdown/Callout.tsx` | Obsidian `> [!type]` admonition boxes |
| `frontend/src/components/markdown/CodeBlock.tsx` | Shiki syntax highlighting + copy + lang label |
| `frontend/src/components/markdown/DataTable.tsx` | Responsive table with scroll wrapper, striped rows, sticky header |
| `frontend/src/components/markdown/BlockQuote.tsx` | Styled blockquote with left bar |
| `frontend/src/components/markdown/TOCSidebar.tsx` | Right sticky TOC with scroll tracking |
| `frontend/src/components/markdown/remarkCallout.ts` | Remark plugin to parse `> [!type]` into callout nodes |
| `frontend/src/components/InsightsTableView.tsx` | Dataview-style table for list page |

### Modified Files
| File | Change |
|------|--------|
| `frontend/src/app/insights/[sourceId]/[docHash]/InsightDetailClient.tsx` | Replace inline ReactMarkdown with MarkdownRenderer + TOCSidebar + Export buttons |
| `frontend/src/app/insights/research/[researchId]/ResearchDetailClient.tsx` | Same MarkdownRenderer swap |
| `frontend/src/components/InsightsList.tsx` | Add Card/Table view toggle |
| `frontend/package.json` | Add `shiki`, `unist-util-visit` dependencies |

---

### Task 1: Install Dependencies and Create Directory

**Files:**
- Modify: `frontend/package.json`
- Create: `frontend/src/components/markdown/` (directory)

- [ ] **Step 1: Install shiki and unist-util-visit**
```bash
cd frontend && npm install shiki unist-util-visit
```

- [ ] **Step 2: Create markdown component directory**
```bash
mkdir -p src/components/markdown
```

- [ ] **Step 3: Verify shiki resolves**
```bash
node -e "require('shiki'); console.log('shiki OK')"
```
Expected: `shiki OK`

- [ ] **Step 4: Commit**
```bash
git add package.json package-lock.json src/components/markdown
git commit -m "chore: install shiki + create markdown component directory"
```

---

### Task 2: Heading Component

**Files:**
- Create: `frontend/src/components/markdown/Heading.tsx`

- [ ] **Step 1: Create Heading.tsx**

The component renders h1-h6 with: auto-generated `id` for anchor linking, left cyan accent bar on h2, hover anchor icon (#) on h2.

`slugify()` converts heading text to URL-safe IDs (supports Korean). `createHeadingComponent(level)` is a factory function used by MarkdownRenderer to map h1-h6 tags.

h2 has special treatment: 4px cyan left bar + anchor hover. h3-h6 use decreasing sizes.

See spec section "Heading Component" for full styling details.

- [ ] **Step 2: Verify TypeScript compiles**
```bash
npx tsc --noEmit 2>&1 | grep -i "Heading" | head -5
```
Expected: No errors

- [ ] **Step 3: Commit**
```bash
git add src/components/markdown/Heading.tsx
git commit -m "feat(markdown): Heading component with accent bar + anchor links"
```

---

### Task 3: BlockQuote Component

**Files:**
- Create: `frontend/src/components/markdown/BlockQuote.tsx`

- [ ] **Step 1: Create BlockQuote.tsx**

Renders blockquotes with: 3px left bar (`#00E5FF` at 40% opacity), translucent cyan background (3% opacity), italic text in muted color. Inner paragraphs have no margin.

- [ ] **Step 2: Commit**
```bash
git add src/components/markdown/BlockQuote.tsx
git commit -m "feat(markdown): BlockQuote with left accent bar"
```

---

### Task 4: DataTable Component

**Files:**
- Create: `frontend/src/components/markdown/DataTable.tsx`

- [ ] **Step 1: Create DataTable.tsx**

Exports: `Table`, `THead`, `TBody`, `TR`, `TH`, `TD` components. Table is wrapped in `overflow-x-auto` div with rounded border. THead has 4% white background. TBody rows have alternating 2% stripe via `nth-child(even)`. TH: left-aligned, xs font, semibold. TD: sm font, muted color. Rows separated by 5% white border.

- [ ] **Step 2: Commit**
```bash
git add src/components/markdown/DataTable.tsx
git commit -m "feat(markdown): DataTable with scroll wrapper + striped rows"
```

---

### Task 5: CodeBlock Component with Shiki

**Files:**
- Create: `frontend/src/components/markdown/CodeBlock.tsx`

- [ ] **Step 1: Create CodeBlock.tsx**

Two modes: block code (has `language-*` className) and inline code (no language).

Block code: Uses `useEffect` to lazy-import `shiki` and call `codeToHtml(code, { lang, theme: 'github-dark-default' })`. While loading, shows plain `<pre>` fallback. Top bar has language label (left) and Copy button (right). Copy uses `navigator.clipboard.writeText()` with 2s "Copied" state. Dark background `#0a0a0f` with `border-white/[0.08]`.

Inline code: Simple `<code>` with `bg-white/[0.05]` background and mono font.

Export both `CodeBlock` and `InlineCode`.

- [ ] **Step 2: Verify no TypeScript errors**
```bash
npx tsc --noEmit 2>&1 | grep -i "CodeBlock" | head -5
```

- [ ] **Step 3: Commit**
```bash
git add src/components/markdown/CodeBlock.tsx
git commit -m "feat(markdown): CodeBlock with Shiki highlighting + copy button"
```

---

### Task 6: Callout Component + Remark Plugin

**Files:**
- Create: `frontend/src/components/markdown/remarkCallout.ts`
- Create: `frontend/src/components/markdown/Callout.tsx`

- [ ] **Step 1: Create remarkCallout.ts**

Remark plugin that visits `blockquote` nodes. Checks if first child paragraph starts with `[!type]` pattern (regex: `/^\[!(summary|warning|tip|danger|info)\]\s*(.*)/i`). If matched, transforms the blockquote into a `div` with `data-callout` and `data-callout-title` hProperties. Uses `unist-util-visit` for tree traversal.

- [ ] **Step 2: Create Callout.tsx**

Maps callout types to colors: summary=cyan, warning=amber, tip=green, danger=red, info=blue. Each has: Material Symbols icon, 4px left accent bar, tinted background (4-6% opacity), title in accent color, body in muted text. Uses `CALLOUT_CONFIG` lookup object.

- [ ] **Step 3: Commit**
```bash
git add src/components/markdown/remarkCallout.ts src/components/markdown/Callout.tsx
git commit -m "feat(markdown): Callout admonition boxes + remark plugin"
```

---

### Task 7: TOCSidebar Component

**Files:**
- Create: `frontend/src/components/markdown/TOCSidebar.tsx`

- [ ] **Step 1: Create TOCSidebar.tsx**

Props: `contentRef: React.RefObject<HTMLDivElement | null>`.

On mount: queries `h2, h3` from contentRef, builds `TocItem[]` array with `{id, text, level}`. Sets up `IntersectionObserver` with `rootMargin: '-80px 0px -60% 0px'` to track active section. Returns `null` if fewer than 2 headings.

Desktop only: `hidden xl:block`, 264px width, sticky top-24. Title: "ON THIS PAGE" (11px, tracked 2px, gray). Items: h2 at level 0 (pl-3), h3 at level 1 (pl-6). Active item: 2px cyan left bar + cyan text + semibold. Click: `scrollIntoView({ behavior: 'smooth', block: 'start' })`.

- [ ] **Step 2: Commit**
```bash
git add src/components/markdown/TOCSidebar.tsx
git commit -m "feat(markdown): TOCSidebar with IntersectionObserver scroll tracking"
```

---

### Task 8: MarkdownRenderer -- Assemble All Components

**Files:**
- Create: `frontend/src/components/markdown/MarkdownRenderer.tsx`

- [ ] **Step 1: Create MarkdownRenderer.tsx**

Imports all markdown components and maps them via ReactMarkdown's `components` prop:
- h1-h6 -> `createHeadingComponent(N)`
- blockquote -> `BlockQuote`
- table/thead/tbody/tr/th/td -> DataTable components
- code -> `CodeBlock` (if `className` starts with `language-`) or `InlineCode`
- div -> `Callout` (if `data-callout` attribute present)
- p, ul, ol, li, a, hr, strong, em -> styled inline components

Plugins: `[remarkGfm, remarkCallout]` for remark, `[rehypeRaw, [rehypeSanitize, customSchema]]` for rehype. Custom sanitize schema allows `data-callout`, `data-callout-title`, `className` on div elements.

Single prop: `content: string`.

- [ ] **Step 2: Verify build**
```bash
npx tsc --noEmit 2>&1 | grep -i "error" | head -5
```
Expected: No errors

- [ ] **Step 3: Commit**
```bash
git add src/components/markdown/MarkdownRenderer.tsx
git commit -m "feat(markdown): MarkdownRenderer -- assemble all custom components"
```

---

### Task 9: Integrate MarkdownRenderer into InsightDetailClient

**Files:**
- Modify: `frontend/src/app/insights/[sourceId]/[docHash]/InsightDetailClient.tsx`

- [ ] **Step 1: Replace inline ReactMarkdown with MarkdownRenderer + TOCSidebar + Export**

Changes:
1. Remove imports: `ReactMarkdown`, `remarkGfm`, `rehypeRaw`, `rehypeSanitize`
2. Add imports: `MarkdownRenderer` from `@/components/markdown/MarkdownRenderer`, `TOCSidebar` from `@/components/markdown/TOCSidebar`
3. Add `useRef` to imports, create `const contentRef = useRef<HTMLDivElement>(null)`
4. Change layout: wrap content + TOC in `<div className="flex gap-0">`
5. Replace the `<div className="prose ..."><ReactMarkdown ...>` block with: `<div ref={contentRef}><MarkdownRenderer content={stripS3Header(doc.content)} /></div><TOCSidebar contentRef={contentRef} />`
6. Add Export dropdown in header card: Copy as Markdown (with YAML frontmatter) + Download .md

- [ ] **Step 2: Build and test**
```bash
npm run build 2>&1 | tail -5
```
Expected: Build succeeds

- [ ] **Step 3: Commit**
```bash
git add src/app/insights/\[sourceId\]/\[docHash\]/InsightDetailClient.tsx
git commit -m "feat(insights): integrate MarkdownRenderer + TOC + export into article detail"
```

---

### Task 10: Integrate MarkdownRenderer into ResearchDetailClient

**Files:**
- Modify: `frontend/src/app/insights/research/[researchId]/ResearchDetailClient.tsx`

- [ ] **Step 1: Swap ReactMarkdown for MarkdownRenderer**

Changes at line ~316-317:
1. Remove `ReactMarkdown`, `remarkGfm`, `rehypeRaw`, `rehypeSanitize` imports
2. Add `MarkdownRenderer` and `TOCSidebar` imports
3. Add `const contentRef = useRef<HTMLDivElement>(null)` (useRef already imported)
4. Replace `<div className="prose ..."><ReactMarkdown ...>{research.content}</ReactMarkdown></div>` with:
```tsx
<div className="flex gap-0">
  <div ref={contentRef} className="flex-1 min-w-0">
    <MarkdownRenderer content={research.content} />
  </div>
  <TOCSidebar contentRef={contentRef} />
</div>
```

- [ ] **Step 2: Build and verify**
```bash
npm run build 2>&1 | tail -5
```

- [ ] **Step 3: Commit**
```bash
git add src/app/insights/research/\[researchId\]/ResearchDetailClient.tsx
git commit -m "feat(research): integrate MarkdownRenderer + TOC into research detail"
```

---

### Task 11: InsightsTableView Component

**Files:**
- Create: `frontend/src/components/InsightsTableView.tsx`

- [ ] **Step 1: Create InsightsTableView.tsx**

Props: `documents: CrawledDocument[]`, `totalCount: number`, `page: number`, `limit: number`, `onTagClick?: (tag: string) => void`, `selectedTags?: string[]`.

Renders a `glass-panel` rounded table with columns: Title (40%), Source (12%), Date (10%), Tags (28%), KB (5%). Rows are clickable (navigate to detail). Tags render as small pills, clickable to toggle filter. KB column shows checkmark or dash. Footer shows "Showing X-Y of N documents".

- [ ] **Step 2: Commit**
```bash
git add src/components/InsightsTableView.tsx
git commit -m "feat(insights): InsightsTableView -- Dataview-style sortable table"
```

---

### Task 12: Add View Toggle to InsightsList

**Files:**
- Modify: `frontend/src/components/InsightsList.tsx`

- [ ] **Step 1: Add view mode state + toggle + table view**

Changes:
1. Import `InsightsTableView`
2. Add state: `const [viewMode, setViewMode] = useState<'card' | 'table'>(() => typeof window !== 'undefined' ? (localStorage.getItem('insights-view') as 'card' | 'table') || 'card' : 'card')`
3. Add persist effect: `useEffect(() => { localStorage.setItem('insights-view', viewMode); }, [viewMode])`
4. Add toggle buttons (grid_view / table_rows Material icons) next to sort dropdown in filter bar
5. In content section: render `InsightsTableView` when `viewMode === 'table'`, existing card list when `viewMode === 'card'`

- [ ] **Step 2: Build and test**
```bash
npm run build 2>&1 | tail -5
```

- [ ] **Step 3: Commit**
```bash
git add src/components/InsightsList.tsx
git commit -m "feat(insights): Card/Table view toggle with localStorage persistence"
```

---

### Task 13: Final Build Verification + Dev Server Test

- [ ] **Step 1: Full TypeScript check**
```bash
cd frontend && npx tsc --noEmit
```
Expected: No errors

- [ ] **Step 2: Production build**
```bash
npm run build
```
Expected: Build succeeds

- [ ] **Step 3: Start dev server and test**
```bash
npm run dev
```
Test manually:
1. `/insights` -- verify Card/Table toggle works, persists on reload
2. Click an article -- verify rich markdown (headings with cyan bars, styled tables, blockquotes)
3. Open a Research report -- verify TOC sidebar on desktop, scroll tracking works
4. Test code block with language -- verify Shiki highlighting + copy button
5. Test Export buttons (Copy Markdown with frontmatter, Download .md)
6. Mobile: verify TOC hidden, content full-width

- [ ] **Step 4: Final commit**
```bash
git add -A
git commit -m "feat: Insights UX redesign -- Obsidian-style markdown + table view + TOC + export

ADR-010: Custom ReactMarkdown component system
- Rich headings with accent bars + anchor links
- Callout boxes (summary/warning/tip/danger/info)
- Shiki syntax highlighting with copy button
- Responsive tables with sticky headers
- TOC sidebar with IntersectionObserver
- Card/Table view toggle (localStorage)
- Obsidian-compatible markdown export"
```
