# Ttobak - Design Specification

> design_sample/ HTML 파일에서 추출한 정확한 디자인 시스템

## 1. Design Tokens

### 1.1 Colors

```css
/* Primary */
--primary: #3211d4;
--primary-10: rgba(50, 17, 212, 0.1);   /* bg-primary/10 */
--primary-20: rgba(50, 17, 212, 0.2);   /* bg-primary/20 */
--primary-40: rgba(50, 17, 212, 0.4);   /* bg-primary/40 */

/* Background */
--bg-light: #f6f6f8;
--bg-dark: #131022;

/* Accent (meeting-note에서 사용) */
--accent: #3211d4;

/* Semantic Colors */
--tag-internal: bg-primary/10 text-primary
--tag-design: bg-amber-100 text-amber-700
--tag-external: bg-green-100 text-green-700
--tag-engineering: bg-emerald-50 text-emerald-600
--tag-marketing: bg-amber-50 text-amber-600
--tag-strategy: bg-primary/10 text-primary

/* Status */
--status-recording: bg-red-50 text-red-600 border-red-100
--status-live-dot: bg-red-600
```

### 1.2 Typography

```css
font-family: 'Inter', sans-serif;

/* Heading hierarchy */
.page-title: text-3xl font-extrabold tracking-tight  /* PC meeting list */
.page-title-mobile: text-xl font-bold tracking-tight  /* Mobile header */
.card-title: text-lg font-bold  /* PC card */
.card-title-mobile: text-base font-bold leading-tight  /* Mobile card */
.section-label: text-xs font-bold uppercase tracking-[0.2em]  /* Section headers */
.section-label-wide: text-xs font-bold uppercase tracking-widest
.body-text: text-sm text-slate-600 leading-relaxed
.timestamp: text-xs text-slate-400
.tag: text-[10px] font-bold uppercase tracking-widest
.nav-label: text-[10px] font-bold uppercase tracking-wider  /* Mobile bottom nav */
.sidebar-nav: text-sm font-medium  /* PC sidebar */
```

### 1.3 Spacing & Layout

```css
/* Mobile Container */
.mobile-container: max-w-md mx-auto bg-white shadow-xl

/* PC Layout */
.pc-sidebar: w-64 border-r border-slate-200 bg-white
.pc-main: flex-1 overflow-hidden
.pc-header: h-16 border-b border-slate-200 bg-white/80 backdrop-blur-md
.pc-content: p-8 max-w-7xl mx-auto

/* Card Spacing */
.card-padding: p-4 (mobile), p-6 (PC)
.card-gap: space-y-4 (mobile), gap-6 (PC grid)
.section-gap: mb-8
```

### 1.4 Border Radius

```css
--radius-default: 0.25rem (rounded)
--radius-lg: 0.5rem (rounded-lg)
--radius-xl: 0.75rem (rounded-xl)
--radius-2xl: 1rem (rounded-2xl)
--radius-full: 9999px (rounded-full)

/* Usage */
.card: rounded-xl
.button: rounded-lg
.input: rounded-xl (mobile), rounded-lg (PC)
.avatar: rounded-full
.tag-badge: rounded-full (mobile), rounded (PC)
.fab: rounded-full
.sidebar-nav-item: rounded-lg
```

### 1.5 Shadows

```css
.card-shadow: shadow-sm
.card-hover: hover:shadow-xl hover:shadow-primary/5
.fab-shadow: shadow-lg
.recording-button-shadow: shadow-lg shadow-primary/40
.sidebar-button-shadow: shadow-lg shadow-primary/20
.floating-player: shadow-xl
```

## 2. Component Specifications

### 2.1 Mobile Bottom Navigation

```html
<!-- 4-5 items, fixed bottom, backdrop blur -->
<nav class="fixed bottom-0 w-full bg-white/90 backdrop-blur-md
            border-t border-slate-100 px-4 pb-6 pt-2 z-10">
  <!-- Each item -->
  <a class="flex flex-col items-center gap-1">
    <span class="material-symbols-outlined">icon_name</span>
    <span class="text-[10px] font-bold uppercase tracking-wider">Label</span>
  </a>
</nav>

<!-- Active: text-primary, fill-1 on icon -->
<!-- Inactive: text-slate-400 -->
```

Items: Home (home), Record (mic), Files (description), Profile (person)

### 2.2 PC Sidebar

```html
<aside class="w-64 border-r border-slate-200 bg-white flex flex-col">
  <!-- Logo/Workspace header -->
  <div class="p-6 flex items-center gap-3">
    <div class="w-8 h-8 bg-primary rounded-lg flex items-center justify-center text-white">
      <span class="material-symbols-outlined">record_voice_over</span>
    </div>
    <div>
      <h1 class="font-bold text-slate-900">또박</h1>
      <p class="text-[10px] text-slate-500 font-medium uppercase tracking-wider">AI Meeting Assistant</p>
    </div>
  </div>

  <!-- Nav items -->
  <nav class="flex-1 px-4 space-y-1">
    <!-- Active -->
    <a class="flex items-center gap-3 px-3 py-2 rounded-lg bg-primary/10 text-primary font-semibold">
      <span class="material-symbols-outlined">videocam</span>
      <span class="text-sm">Meetings</span>
    </a>
    <!-- Inactive -->
    <a class="flex items-center gap-3 px-3 py-2 rounded-lg text-slate-600 hover:bg-slate-50 font-medium">
      <span class="material-symbols-outlined text-slate-400">icon</span>
      <span class="text-sm">Label</span>
    </a>
  </nav>

  <!-- Bottom: New Meeting button + user profile -->
  <div class="p-4 border-t border-slate-100">
    <button class="w-full bg-primary text-white py-2.5 rounded-lg font-bold text-sm
                   shadow-lg shadow-primary/20 flex items-center justify-center gap-2">
      <span class="material-symbols-outlined text-lg">add_circle</span>
      New Meeting
    </button>
  </div>
</aside>
```

Sidebar nav items: Meetings (videocam), Files (folder_open), Insights (analytics), Team (group), Settings (settings)

### 2.3 Meeting Card (Mobile)

```html
<div class="bg-white border border-slate-100 p-4 rounded-xl shadow-sm
            hover:border-primary/30 transition-all cursor-pointer">
  <!-- Top: title + tag -->
  <div class="flex justify-between items-start mb-2">
    <h4 class="text-slate-900 font-bold text-base leading-tight">Title</h4>
    <span class="text-[10px] font-bold bg-primary/10 text-primary px-2 py-0.5
                 rounded-full uppercase">Tag</span>
  </div>
  <!-- Date -->
  <div class="flex items-center gap-2 text-slate-400 text-xs mb-3">
    <span class="material-symbols-outlined text-[14px]">calendar_today</span>
    <span>Oct 24, 2023 · 10:00 AM</span>
  </div>
  <!-- Summary -->
  <p class="text-slate-600 text-sm line-clamp-2 leading-relaxed">
    AI Summary preview...
  </p>
  <!-- Participants -->
  <div class="mt-4 flex -space-x-2">
    <div class="size-6 rounded-full border-2 border-white bg-slate-200 overflow-hidden">
      <img />
    </div>
    <div class="size-6 rounded-full border-2 border-white bg-slate-200
                flex items-center justify-center text-[10px] font-bold text-slate-500">+3</div>
  </div>
</div>
```

### 2.4 Meeting Card (PC)

```html
<div class="bg-white border border-slate-200 rounded-xl p-6
            hover:shadow-xl hover:shadow-primary/5 transition-all group cursor-pointer">
  <!-- Top: tag + date -->
  <div class="flex justify-between items-start mb-4">
    <span class="text-[10px] font-bold uppercase tracking-widest text-primary
                 bg-primary/10 px-2 py-0.5 rounded">Tag</span>
    <span class="text-xs text-slate-400">Oct 12, 2023</span>
  </div>
  <!-- Title (hover effect) -->
  <h3 class="text-lg font-bold mb-2 group-hover:text-primary transition-colors">Title</h3>
  <!-- Summary -->
  <p class="text-sm text-slate-600 line-clamp-3 mb-4 leading-relaxed">AI Summary...</p>
  <!-- Tags -->
  <div class="flex flex-wrap gap-2 mb-4">
    <span class="text-xs px-2 py-1 bg-slate-100 rounded text-slate-600">#tag</span>
  </div>
  <!-- Footer: avatars + more -->
  <div class="flex items-center justify-between pt-4 border-t border-slate-100">
    <div class="flex -space-x-2">
      <img class="w-7 h-7 rounded-full border-2 border-white" />
    </div>
    <button class="text-slate-400 hover:text-primary">
      <span class="material-symbols-outlined text-xl">more_horiz</span>
    </button>
  </div>
</div>
```

### 2.5 Recording Screen (Mobile)

```
Layout:
  header: 뒤로가기 + 타이틀(input) + 번역언어(select) + 로그아웃(logout icon)
  main:
    - 원형 타이머 (bg-primary/10 pulse, bg-primary/20, white circle with border-4 border-primary)
    - 웨이브폼 바 (w-1 bg-primary rounded-full, heights varying)
    - "Recording in progress..." 텍스트
    - 컨트롤: [일시정지] [정지(primary, large)] [카메라]
    - Recently Captured 그리드 (3열)
  bottom-nav: 고정
```

### 2.6 Recording Screen (PC)

```
Layout:
  sidebar (w-64)
  main:
    header: 네비게이션 + 타이틀 + "RECORDING LIVE" 배지 + 검색 + 프로필
    content (flex):
      center:
        - Status Card (rounded-2xl shadow-sm border, p-8)
          - 타이머: text-6xl font-black tracking-tighter
          - 웨이브폼: gradient bars
          - 통계: Storage / Quality / Bitrate
        - Captured Assets Grid (4열)
      right-panel (w-80):
        - Live Transcription
        - Speaker entries with avatar initials + timestamp
        - Export button
```

### 2.7 Meeting Detail (Mobile)

```
Layout:
  header: 뒤로가기 + "Meeting Report" + more
  main (scrollable):
    - Tag + Date
    - Title: text-3xl font-bold
    - Visual Comparison Workspace (dark bg, side-by-side images)
    - AI Summary (bg-slate-50 border rounded-xl p-5)
    - Action Items (checkbox list)
    - Participants (avatar stack)
    - Transcription (border-l-2 timeline)
  bottom-nav: fixed with centered FAB
```

### 2.8 Meeting Detail (PC)

```
Layout:
  sidebar (w-64, fixed)
  main (ml-64, p-8, max-w-5xl mx-auto):
    - Breadcrumbs
    - Title: text-4xl font-black tracking-tight
    - Date + Folder
    - Participants stack
    - Grid (7/12 + 5/12):
      - AI Summary (bg-white border rounded-xl p-6)
      - Action Items (bg-primary/5 border-primary/20 rounded-xl p-6)
    - Attachments Gallery (4열 grid, hover overlay)
    - Full Transcription (timestamp badges + speaker entries)
    - Floating Audio Player (sticky bottom-6, rounded-full, backdrop-blur)
```

### 2.9 LiveTranscript Component

실시간 전사 및 번역 결과를 스트리밍으로 표시하는 컴포넌트.

```html
<div class="bg-white border border-slate-200 rounded-xl p-4 h-[400px] overflow-y-auto">
  <!-- Header -->
  <div class="flex items-center justify-between mb-4 sticky top-0 bg-white pb-2 border-b">
    <div class="flex items-center gap-2">
      <span class="material-symbols-outlined text-primary">mic</span>
      <span class="text-sm font-bold uppercase tracking-wider">Live Transcription</span>
    </div>
    <div class="flex items-center gap-1">
      <span class="w-2 h-2 bg-red-500 rounded-full animate-pulse"></span>
      <span class="text-xs text-slate-500">LIVE</span>
    </div>
  </div>

  <!-- Transcript Entries -->
  <div class="space-y-3">
    <!-- Final transcript -->
    <div class="flex gap-3">
      <div class="w-8 h-8 rounded-full bg-primary/10 flex items-center justify-center
                  text-xs font-bold text-primary shrink-0">S1</div>
      <div>
        <div class="flex items-center gap-2 mb-1">
          <span class="text-xs font-semibold text-slate-700">Speaker 1</span>
          <span class="text-[10px] text-slate-400">10:23:45</span>
        </div>
        <p class="text-sm text-slate-600">전사된 텍스트가 여기에 표시됩니다.</p>
        <!-- Translation (if enabled) -->
        <p class="text-sm text-primary/80 mt-1 pl-2 border-l-2 border-primary/30">
          The transcribed text appears here.
        </p>
      </div>
    </div>

    <!-- Interim transcript (typing indicator) -->
    <div class="flex gap-3 opacity-60">
      <div class="w-8 h-8 rounded-full bg-slate-100 flex items-center justify-center
                  text-xs font-bold text-slate-400 shrink-0">S2</div>
      <div>
        <p class="text-sm text-slate-500">현재 말하고 있는 내용...</p>
        <div class="flex gap-1 mt-1">
          <span class="w-1.5 h-1.5 bg-slate-400 rounded-full animate-bounce"></span>
          <span class="w-1.5 h-1.5 bg-slate-400 rounded-full animate-bounce [animation-delay:0.1s]"></span>
          <span class="w-1.5 h-1.5 bg-slate-400 rounded-full animate-bounce [animation-delay:0.2s]"></span>
        </div>
      </div>
    </div>
  </div>
</div>
```

### 2.10 QAPanel Component

회의 중 질문을 입력하고 KB RAG 응답을 표시하는 패널.

```html
<div class="bg-white border border-slate-200 rounded-xl overflow-hidden">
  <!-- Header -->
  <div class="bg-slate-50 px-4 py-3 border-b flex items-center gap-2">
    <span class="material-symbols-outlined text-primary">question_answer</span>
    <span class="text-sm font-bold">Meeting Q&A</span>
  </div>

  <!-- Q&A History -->
  <div class="p-4 space-y-4 max-h-[300px] overflow-y-auto">
    <!-- Question/Answer pair -->
    <div class="space-y-2">
      <!-- Question -->
      <div class="flex gap-2">
        <span class="material-symbols-outlined text-slate-400 text-lg">help</span>
        <p class="text-sm font-medium text-slate-700">이 회의에서 결정된 마감일은?</p>
      </div>
      <!-- Answer -->
      <div class="ml-6 bg-primary/5 rounded-lg p-3">
        <p class="text-sm text-slate-600">마감일은 3월 15일로 결정되었습니다.</p>
        <!-- Sources -->
        <div class="mt-2 pt-2 border-t border-slate-200">
          <span class="text-[10px] font-bold uppercase tracking-wider text-slate-400">Sources</span>
          <div class="mt-1 space-y-1">
            <a class="flex items-center gap-1 text-xs text-primary hover:underline">
              <span class="material-symbols-outlined text-[14px]">description</span>
              Product Strategy Sync (이 회의)
            </a>
            <a class="flex items-center gap-1 text-xs text-primary hover:underline">
              <span class="material-symbols-outlined text-[14px]">folder</span>
              project-timeline.pdf
            </a>
          </div>
        </div>
      </div>
    </div>
  </div>

  <!-- Input -->
  <div class="p-4 border-t bg-slate-50">
    <div class="flex gap-2">
      <input type="text" placeholder="Ask a question about this meeting..."
             class="flex-1 px-3 py-2 text-sm border border-slate-200 rounded-lg
                    focus:outline-none focus:ring-2 focus:ring-primary/20 focus:border-primary" />
      <button class="px-4 py-2 bg-primary text-white rounded-lg text-sm font-medium
                     hover:bg-primary/90 transition-colors">
        <span class="material-symbols-outlined text-lg">send</span>
      </button>
    </div>
    <label class="flex items-center gap-2 mt-2 text-xs text-slate-500">
      <input type="checkbox" class="rounded text-primary focus:ring-primary" checked />
      Include Knowledge Base in search
    </label>
  </div>
</div>
```

### 2.11 KBFileList Component

Knowledge Base 파일 업로드 및 목록 관리 컴포넌트.

```html
<div class="bg-white border border-slate-200 rounded-xl">
  <!-- Header -->
  <div class="px-6 py-4 border-b flex items-center justify-between">
    <div class="flex items-center gap-2">
      <span class="material-symbols-outlined text-primary">library_books</span>
      <h3 class="font-bold">Knowledge Base</h3>
    </div>
    <span class="text-xs text-slate-400">12 files indexed</span>
  </div>

  <!-- Upload Area -->
  <div class="p-4 border-b border-dashed border-slate-200 bg-slate-50/50">
    <div class="border-2 border-dashed border-slate-300 rounded-lg p-6 text-center
                hover:border-primary/50 hover:bg-primary/5 transition-colors cursor-pointer">
      <span class="material-symbols-outlined text-3xl text-slate-400 mb-2">upload_file</span>
      <p class="text-sm font-medium text-slate-600">Drop files here or click to upload</p>
      <p class="text-xs text-slate-400 mt-1">PDF, Markdown, PPTX, DOCX (max 50MB)</p>
    </div>
  </div>

  <!-- File List -->
  <div class="divide-y divide-slate-100 max-h-[400px] overflow-y-auto">
    <!-- File item -->
    <div class="px-4 py-3 flex items-center justify-between hover:bg-slate-50 group">
      <div class="flex items-center gap-3">
        <div class="w-10 h-10 bg-red-50 rounded-lg flex items-center justify-center">
          <span class="material-symbols-outlined text-red-500">picture_as_pdf</span>
        </div>
        <div>
          <p class="text-sm font-medium text-slate-700">project-spec.pdf</p>
          <div class="flex items-center gap-2 text-xs text-slate-400">
            <span>1.2 MB</span>
            <span>·</span>
            <span>Indexed Mar 5, 2026</span>
          </div>
        </div>
      </div>
      <div class="flex items-center gap-2">
        <span class="px-2 py-0.5 bg-green-100 text-green-700 text-[10px] font-bold rounded">
          INDEXED
        </span>
        <button class="p-1 text-slate-400 hover:text-red-500 opacity-0 group-hover:opacity-100
                       transition-opacity">
          <span class="material-symbols-outlined text-lg">delete</span>
        </button>
      </div>
    </div>

    <!-- Indexing file -->
    <div class="px-4 py-3 flex items-center justify-between bg-amber-50/50">
      <div class="flex items-center gap-3">
        <div class="w-10 h-10 bg-blue-50 rounded-lg flex items-center justify-center">
          <span class="material-symbols-outlined text-blue-500">description</span>
        </div>
        <div>
          <p class="text-sm font-medium text-slate-700">meeting-notes.md</p>
          <div class="flex items-center gap-2 text-xs text-slate-400">
            <span>256 KB</span>
            <span>·</span>
            <span>Uploading...</span>
          </div>
        </div>
      </div>
      <div class="flex items-center gap-2">
        <span class="px-2 py-0.5 bg-amber-100 text-amber-700 text-[10px] font-bold rounded
                     animate-pulse">
          INDEXING
        </span>
      </div>
    </div>
  </div>
</div>
```

### 2.12 ExportMenu Component

회의 내보내기 옵션을 제공하는 드롭다운 메뉴.

```html
<div class="relative">
  <!-- Trigger Button -->
  <button class="flex items-center gap-2 px-4 py-2 bg-slate-100 hover:bg-slate-200
                 rounded-lg text-sm font-medium text-slate-700 transition-colors">
    <span class="material-symbols-outlined text-lg">file_download</span>
    Export
    <span class="material-symbols-outlined text-lg">expand_more</span>
  </button>

  <!-- Dropdown Menu -->
  <div class="absolute right-0 mt-2 w-56 bg-white rounded-xl shadow-xl border border-slate-200
              py-2 z-50">
    <!-- PDF -->
    <button class="w-full px-4 py-2 flex items-center gap-3 hover:bg-slate-50 text-left">
      <span class="material-symbols-outlined text-red-500">picture_as_pdf</span>
      <div>
        <p class="text-sm font-medium text-slate-700">PDF</p>
        <p class="text-xs text-slate-400">Formatted document</p>
      </div>
    </button>

    <!-- Markdown -->
    <button class="w-full px-4 py-2 flex items-center gap-3 hover:bg-slate-50 text-left">
      <span class="material-symbols-outlined text-slate-600">code</span>
      <div>
        <p class="text-sm font-medium text-slate-700">Markdown</p>
        <p class="text-xs text-slate-400">Plain text with formatting</p>
      </div>
    </button>

    <div class="my-2 border-t border-slate-100"></div>

    <!-- Notion -->
    <button class="w-full px-4 py-2 flex items-center gap-3 hover:bg-slate-50 text-left">
      <span class="material-symbols-outlined text-slate-800">note_alt</span>
      <div>
        <p class="text-sm font-medium text-slate-700">Notion</p>
        <p class="text-xs text-slate-400">Create Notion page</p>
      </div>
      <!-- API key required indicator -->
      <span class="ml-auto material-symbols-outlined text-amber-500 text-lg"
            title="API key required">vpn_key</span>
    </button>

    <!-- Obsidian -->
    <button class="w-full px-4 py-2 flex items-center gap-3 hover:bg-slate-50 text-left">
      <span class="material-symbols-outlined text-purple-600">link</span>
      <div>
        <p class="text-sm font-medium text-slate-700">Obsidian</p>
        <p class="text-xs text-slate-400">Markdown with [[wikilinks]]</p>
      </div>
    </button>
  </div>
</div>
```

### 2.13 IntegrationSettings Component

외부 서비스 API 키 설정 UI.

```html
<div class="bg-white border border-slate-200 rounded-xl">
  <div class="px-6 py-4 border-b">
    <h3 class="font-bold flex items-center gap-2">
      <span class="material-symbols-outlined text-primary">extension</span>
      Integrations
    </h3>
    <p class="text-sm text-slate-500 mt-1">Connect external services for export</p>
  </div>

  <div class="divide-y divide-slate-100">
    <!-- Notion Integration -->
    <div class="p-6">
      <div class="flex items-center justify-between mb-4">
        <div class="flex items-center gap-3">
          <div class="w-10 h-10 bg-slate-900 rounded-lg flex items-center justify-center">
            <span class="material-symbols-outlined text-white">note_alt</span>
          </div>
          <div>
            <p class="font-medium text-slate-700">Notion</p>
            <p class="text-xs text-slate-400">Export meetings to Notion pages</p>
          </div>
        </div>
        <!-- Connected status -->
        <span class="px-2 py-1 bg-green-100 text-green-700 text-xs font-bold rounded">
          CONNECTED
        </span>
      </div>

      <!-- API Key Input (masked) -->
      <div class="space-y-2">
        <label class="text-xs font-medium text-slate-500 uppercase tracking-wider">
          API Key
        </label>
        <div class="flex gap-2">
          <input type="password" value="ntn_****abcd" readonly
                 class="flex-1 px-3 py-2 text-sm border border-slate-200 rounded-lg
                        bg-slate-50 text-slate-500" />
          <button class="px-4 py-2 text-sm font-medium text-slate-600 border border-slate-200
                         rounded-lg hover:bg-slate-50 transition-colors">
            Edit
          </button>
          <button class="px-4 py-2 text-sm font-medium text-red-600 border border-red-200
                         rounded-lg hover:bg-red-50 transition-colors">
            Remove
          </button>
        </div>
        <p class="text-xs text-slate-400">Connected on Mar 5, 2026</p>
      </div>
    </div>

    <!-- Obsidian (no API key needed) -->
    <div class="p-6">
      <div class="flex items-center justify-between">
        <div class="flex items-center gap-3">
          <div class="w-10 h-10 bg-purple-100 rounded-lg flex items-center justify-center">
            <span class="material-symbols-outlined text-purple-600">link</span>
          </div>
          <div>
            <p class="font-medium text-slate-700">Obsidian</p>
            <p class="text-xs text-slate-400">Download .md files with [[wikilinks]]</p>
          </div>
        </div>
        <span class="text-xs text-slate-400">No API key required</span>
      </div>
    </div>

    <!-- Not connected example -->
    <div class="p-6 bg-slate-50/50">
      <div class="flex items-center justify-between mb-4">
        <div class="flex items-center gap-3">
          <div class="w-10 h-10 bg-slate-200 rounded-lg flex items-center justify-center">
            <span class="material-symbols-outlined text-slate-400">cloud</span>
          </div>
          <div>
            <p class="font-medium text-slate-700">Other Service</p>
            <p class="text-xs text-slate-400">Coming soon</p>
          </div>
        </div>
        <span class="px-2 py-1 bg-slate-100 text-slate-500 text-xs font-bold rounded">
          NOT CONNECTED
        </span>
      </div>

      <div class="flex gap-2">
        <input type="text" placeholder="Enter API key..."
               class="flex-1 px-3 py-2 text-sm border border-slate-200 rounded-lg
                      focus:outline-none focus:ring-2 focus:ring-primary/20 focus:border-primary" />
        <button class="px-4 py-2 bg-primary text-white text-sm font-medium rounded-lg
                       hover:bg-primary/90 transition-colors">
          Connect
        </button>
      </div>
    </div>
  </div>
</div>
```

### 2.14 Recording Mode Toggle

녹음 화면에서 오프라인/온라인 모드 전환 체크박스.

```html
<div class="bg-white border border-slate-200 rounded-xl p-4">
  <div class="flex items-center justify-between">
    <div class="flex items-center gap-3">
      <span class="material-symbols-outlined text-primary">wifi</span>
      <div>
        <p class="text-sm font-medium text-slate-700">Real-time Mode</p>
        <p class="text-xs text-slate-400">Stream audio for live transcription</p>
      </div>
    </div>
    <label class="relative inline-flex items-center cursor-pointer">
      <input type="checkbox" class="sr-only peer" checked />
      <div class="w-11 h-6 bg-slate-200 peer-focus:ring-2 peer-focus:ring-primary/20 rounded-full
                  peer peer-checked:after:translate-x-full peer-checked:after:border-white
                  after:content-[''] after:absolute after:top-[2px] after:left-[2px]
                  after:bg-white after:border-slate-300 after:border after:rounded-full
                  after:h-5 after:w-5 after:transition-all peer-checked:bg-primary"></div>
    </label>
  </div>

  <!-- Offline mode info -->
  <div class="mt-3 p-3 bg-slate-50 rounded-lg text-xs text-slate-500 hidden">
    <span class="material-symbols-outlined text-sm align-middle mr-1">info</span>
    Offline mode: Audio will be transcribed after upload completes.
  </div>

  <!-- Online mode info (shown when checked) -->
  <div class="mt-3 p-3 bg-primary/5 rounded-lg text-xs text-primary">
    <span class="material-symbols-outlined text-sm align-middle mr-1">bolt</span>
    Real-time mode: See transcription as you speak.
  </div>
</div>
```

### 2.15 Translation Language Selector

번역 대상 언어 선택 체크박스 그룹.

```html
<div class="bg-white border border-slate-200 rounded-xl p-4">
  <div class="flex items-center gap-2 mb-3">
    <span class="material-symbols-outlined text-primary">translate</span>
    <span class="text-sm font-medium text-slate-700">Real-time Translation</span>
  </div>

  <div class="space-y-2">
    <!-- Language options -->
    <label class="flex items-center gap-3 p-2 rounded-lg hover:bg-slate-50 cursor-pointer">
      <input type="checkbox" class="w-4 h-4 rounded text-primary focus:ring-primary" />
      <span class="text-sm text-slate-600">Korean → English</span>
      <span class="text-xs text-slate-400 ml-auto">한→영</span>
    </label>

    <label class="flex items-center gap-3 p-2 rounded-lg hover:bg-slate-50 cursor-pointer">
      <input type="checkbox" class="w-4 h-4 rounded text-primary focus:ring-primary" checked />
      <span class="text-sm text-slate-600">English → Korean</span>
      <span class="text-xs text-slate-400 ml-auto">영→한</span>
    </label>

    <label class="flex items-center gap-3 p-2 rounded-lg hover:bg-slate-50 cursor-pointer">
      <input type="checkbox" class="w-4 h-4 rounded text-primary focus:ring-primary" />
      <span class="text-sm text-slate-600">Japanese → Korean</span>
      <span class="text-xs text-slate-400 ml-auto">일→한</span>
    </label>

    <label class="flex items-center gap-3 p-2 rounded-lg hover:bg-slate-50 cursor-pointer">
      <input type="checkbox" class="w-4 h-4 rounded text-primary focus:ring-primary" />
      <span class="text-sm text-slate-600">Chinese → Korean</span>
      <span class="text-xs text-slate-400 ml-auto">중→한</span>
    </label>
  </div>

  <p class="mt-3 text-xs text-slate-400">
    Select languages to translate in real-time during recording.
  </p>
</div>
```

## 3. Interaction Patterns

### 3.1 Hover States
- Cards: `hover:border-primary/30` (mobile), `hover:shadow-xl hover:shadow-primary/5` (PC)
- Card title: `group-hover:text-primary` (PC only)
- Buttons: `hover:bg-primary/90`, `hover:scale-105`
- Nav items: `hover:bg-slate-50` (sidebar), `hover:text-primary` (icons)
- Image gallery: overlay with action buttons on hover

### 3.2 Active/Selected States
- Nav: `bg-primary/10 text-primary font-semibold` + `border-b-2 border-primary` (tabs)
- Buttons: `active:scale-95`
- Checkbox: `text-primary focus:ring-primary` (PC), `text-slate-900` (mobile)

### 3.3 Transitions
- `transition-colors` for color changes
- `transition-all` for multi-property
- `transition-transform` for scale
- `transition-opacity` for fade

### 3.4 Animations
- Recording pulse: `animate-pulse` on outer ring
- Recording waveform: varying height bars
- Live transcription dots: `animate-bounce` with staggered delays
- No other heavy animations

## 4. Icon Mapping

| 용도 | Material Symbol | 사용 위치 |
|------|----------------|-----------|
| 홈 | home | 모바일 하단 네비 |
| 회의 | videocam / video_camera_front | 사이드바 |
| 녹음 | mic | 모바일 하단 네비 |
| 파일 | description / folder_open | 네비게이션 |
| 설정 | settings | 네비게이션 |
| 프로필 | person / account_circle | 네비게이션 |
| 검색 | search | 검색바 |
| 캘린더 | calendar_today / calendar_month | 날짜 표시 |
| 추가 | add / add_circle | FAB, 새 회의 |
| 뒤로 | arrow_back | 모바일 헤더 |
| 더보기 | more_horiz | 카드 메뉴 |
| AI | auto_awesome | AI Summary |
| 체크 | check_circle | 액션아이템 |
| 일시정지 | pause | 녹음 컨트롤 |
| 정지 | stop | 녹음 컨트롤 |
| 카메라 | add_a_photo | 녹음 중 캡처 |
| 전사 | translate | 라이브 전사 |
| 첨부 | attachment | 첨부파일 |
| 메모 | notes | 전사본 |
| 재생 | play_arrow | 오디오 플레이어 |
| 다운로드 | download | 내보내기 |
| 알림 | notifications | 헤더 |
| 공유 | share | 공유 버튼 |
| 비교 | compare | 이미지 비교 |
| 업로드 | upload_file | 파일 업로드 |
| 팀 | group | 사이드바 |
| 인사이트 | analytics / insights | 사이드바 |
| 번역 | translate | 실시간 번역 |
| Q&A | question_answer | 미팅 Q&A |
| Knowledge Base | library_books | KB 관리 |
| 내보내기 | file_download | Export 메뉴 |
| API 키 | vpn_key | 연동 설정 |
| Obsidian | link | Obsidian 내보내기 |
| 확장 | extension | Integrations |
| WiFi | wifi | 온라인 모드 |
| 번개 | bolt | 실시간 모드 |
| PDF | picture_as_pdf | PDF 내보내기 |
| 코드 | code | Markdown 내보내기 |
| 노트 | note_alt | Notion 내보내기 |
| 도움말 | help | Q&A 질문 |
| 전송 | send | Q&A 전송 버튼 |
| 삭제 | delete | KB 파일 삭제 |
| 클라우드 | cloud | 외부 서비스 |
