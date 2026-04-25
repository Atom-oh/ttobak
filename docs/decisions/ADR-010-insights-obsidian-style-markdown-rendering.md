# ADR-010: Obsidian-style Rich Markdown Rendering for Insights

<a href="#english"><img src="https://img.shields.io/badge/lang-English-blue.svg" alt="English"></a>
<a href="#korean"><img src="https://img.shields.io/badge/lang-한국어-red.svg" alt="Korean"></a>

---

<a id="english"></a>

# English

## Status
Accepted

## Context
The Insights page renders Deep Research reports (5,000-20,000 words) and Tech articles using a basic `ReactMarkdown` component with inline Tailwind classes. The current rendering provides minimal visual distinction between headings, body text, code blocks, and tables -- resulting in a "notepad paste" reading experience that is inadequate for long-form research content.

Key pain points:
- No table of contents for long reports, making navigation difficult
- Headings lack visual hierarchy (size-only differentiation)
- Code blocks have no syntax highlighting or copy functionality
- Tables overflow on smaller screens and lack proper styling
- No callout/admonition boxes for highlighting key findings
- Links and references blend into body text
- No export capability for use in Obsidian or Notion

Future requirement: exported markdown must be compatible with Obsidian vault import (frontmatter + standard markdown).

## Options Considered

### Option 1: Obsidian-inspired Rich Markdown Components (Chosen)
Replace `ReactMarkdown` inline styles with a dedicated component system. Custom components for headings, callouts, code blocks, tables, blockquotes, and TOC. Existing S3 markdown content remains unchanged.

- **Pros**: Zero data migration, incremental adoption, Obsidian-compatible callout syntax, desktop-optimized reading experience
- **Cons**: Custom component maintenance, Shiki adds ~2MB lazy-loaded bundle

### Option 2: MDX with Embedded React Components
Convert markdown to MDX format, enabling direct React component embedding.

- **Pros**: Maximum flexibility, interactive elements possible
- **Cons**: Requires migrating all existing S3 markdown content, crawler output format change, complex build pipeline

### Option 3: Notion-style Block Editor (TipTap)
Render content through a block editor for WYSIWYG-like display.

- **Pros**: Best UX, editing capability
- **Cons**: Very high implementation complexity, markdown-to-block conversion needed, overkill for read-only display

## Decision
Adopt Option 1: Obsidian-inspired Rich Markdown component system. This approach upgrades the reading experience without changing the data layer. Crawlers and the Research Agent continue to output standard markdown, and the frontend renders it with rich visual components.

Key implementation details:
- **Component system**: `MarkdownRenderer`, `Heading`, `Callout`, `CodeBlock`, `DataTable`, `BlockQuote`, `TOCSidebar`
- **Callout syntax**: Obsidian-compatible `> [!type] title` parsed via remark plugin
- **Code highlighting**: Shiki (WASM-based, lazy loaded via `next/dynamic`)
- **TOC**: Right sticky sidebar with IntersectionObserver-based scroll tracking
- **View modes**: Card (default) + Table toggle for the list page
- **Export**: Copy as Markdown / Download .md with YAML frontmatter

## Consequences

### Positive
- Deep Research reports become readable with proper visual hierarchy, navigation, and highlighting
- Obsidian-compatible callout syntax enables future vault export without conversion
- Table view provides Dataview-style browsing for power users
- No backend or data migration required
- Shiki lazy loading minimizes bundle impact

### Negative
- 7 new components to maintain in `frontend/src/components/markdown/`
- Shiki adds ~2MB to lazy-loaded bundle (first code block render)
- Callout parsing requires a custom remark plugin
- TOC sidebar reduces content width on desktop (920px - 264px TOC = 656px usable)

## References
- Design spec: `docs/superpowers/specs/2026-04-25-insights-ux-redesign-design.md`
- Figma mockups: https://www.figma.com/design/f3DqT7x6kK6994MeUjMwuX
- Obsidian callout syntax: https://help.obsidian.md/callouts
- Shiki documentation: https://shiki.style
- Current implementation: `frontend/src/app/insights/[sourceId]/[docHash]/InsightDetailClient.tsx`

---

<a id="korean"></a>

# 한국어

## 상태
승인됨

## 배경
Insights 페이지는 Deep Research 보고서(5,000-20,000 단어)와 Tech 기사를 기본 `ReactMarkdown` 컴포넌트와 인라인 Tailwind 클래스로 렌더링합니다. 현재 렌더링은 헤딩, 본문, 코드 블록, 테이블 간의 시각적 구분이 최소화되어 있어, 장문 리서치 콘텐츠에 부적합한 "메모장 붙여넣기" 수준의 읽기 경험을 제공합니다.

주요 문제점:
- 긴 보고서에 목차가 없어 네비게이션이 어려움
- 헤딩이 크기만 다를 뿐 시각적 계층 구분 부족
- 코드 블록에 구문 강조나 복사 기능 없음
- 테이블이 작은 화면에서 overflow되고 스타일 부족
- 핵심 발견사항을 강조하는 Callout/Admonition 박스 없음
- 링크와 참조가 본문에 섞여 구분 어려움
- Obsidian이나 Notion으로의 내보내기 기능 없음

향후 요구사항: 내보낸 마크다운이 Obsidian vault import와 호환되어야 합니다 (frontmatter + 표준 마크다운).

## 검토한 옵션

### 옵션 1: Obsidian 스타일 Rich Markdown 컴포넌트 (선택됨)
`ReactMarkdown` 인라인 스타일을 전용 컴포넌트 시스템으로 교체합니다. 헤딩, Callout, 코드 블록, 테이블, 블록쿼트, TOC를 위한 커스텀 컴포넌트를 구현합니다. 기존 S3 마크다운 콘텐츠는 변경 없이 유지됩니다.

- **장점**: 데이터 마이그레이션 불필요, 점진적 적용 가능, Obsidian 호환 Callout 문법, 데스크톱 최적화 읽기 경험
- **단점**: 커스텀 컴포넌트 유지보수, Shiki가 약 2MB 지연 로드 번들 추가

### 옵션 2: MDX + 임베디드 React 컴포넌트
마크다운을 MDX 포맷으로 변환하여 React 컴포넌트를 직접 임베드합니다.

- **장점**: 최대 유연성, 인터랙티브 요소 가능
- **단점**: 기존 S3 마크다운 콘텐츠 전체 마이그레이션 필요, 크롤러 출력 포맷 변경, 복잡한 빌드 파이프라인

### 옵션 3: Notion 스타일 블록 에디터 (TipTap)
블록 에디터를 통해 WYSIWYG 유사 방식으로 콘텐츠를 렌더링합니다.

- **장점**: 최고의 UX, 편집 기능
- **단점**: 구현 복잡도 매우 높음, 마크다운에서 블록으로의 변환 필요, 읽기 전용 표시에는 과도한 솔루션

## 결정
옵션 1: Obsidian 스타일 Rich Markdown 컴포넌트 시스템을 채택합니다. 이 접근 방식은 데이터 계층을 변경하지 않고 읽기 경험을 업그레이드합니다. 크롤러와 Research Agent는 계속 표준 마크다운을 출력하고, 프론트엔드에서 풍부한 시각적 컴포넌트로 렌더링합니다.

주요 구현 사항:
- **컴포넌트 시스템**: `MarkdownRenderer`, `Heading`, `Callout`, `CodeBlock`, `DataTable`, `BlockQuote`, `TOCSidebar`
- **Callout 문법**: Obsidian 호환 `> [!type] title`을 remark 플러그인으로 파싱
- **코드 강조**: Shiki (WASM 기반, `next/dynamic`으로 지연 로드)
- **TOC**: IntersectionObserver 기반 스크롤 추적이 포함된 우측 고정 사이드바
- **뷰 모드**: 카드(기본) + 테이블 토글 (목록 페이지)
- **내보내기**: Markdown 복사 / YAML frontmatter 포함 .md 다운로드

## 영향

### 긍정적
- Deep Research 보고서가 적절한 시각적 계층, 네비게이션, 강조로 읽기 편해집니다
- Obsidian 호환 Callout 문법으로 변환 없이 vault 내보내기가 가능합니다
- 테이블 뷰가 파워 유저를 위한 Dataview 스타일 탐색을 제공합니다
- 백엔드나 데이터 마이그레이션이 필요하지 않습니다
- Shiki 지연 로딩으로 번들 영향을 최소화합니다

### 부정적
- `frontend/src/components/markdown/`에 7개의 새 컴포넌트를 유지보수해야 합니다
- Shiki가 지연 로드 번들에 약 2MB를 추가합니다 (첫 코드 블록 렌더링 시)
- Callout 파싱에 커스텀 remark 플러그인이 필요합니다
- TOC 사이드바가 데스크톱에서 콘텐츠 너비를 줄입니다 (920px - 264px TOC = 656px 사용 가능)

## 참고 자료
- 디자인 스펙: `docs/superpowers/specs/2026-04-25-insights-ux-redesign-design.md`
- Figma 목업: https://www.figma.com/design/f3DqT7x6kK6994MeUjMwuX
- Obsidian Callout 문법: https://help.obsidian.md/callouts
- Shiki 문서: https://shiki.style
- 현재 구현: `frontend/src/app/insights/[sourceId]/[docHash]/InsightDetailClient.tsx`
