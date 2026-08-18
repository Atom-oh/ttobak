# TTOBAK - Design Specification

> Design system extracted from design_sample/ HTML files.

## 1. Design Tokens

### 1.1 Colors

One brand (indigo/violet) shared across light/dark — neon cyan/purple, glow, and glass blur have been removed (`frontend/src/app/globals.css`). `primary`/`accent`/`secondary`/`surface-*`/`text-*`/`border-subtle`/`error` define light values in `:root`; `.dark` overrides the same CSS var names, so utilities like `text-primary` auto-pick the violet value in dark mode with no `dark:` prefix (Tailwind v4 `@theme inline` lazy `var()` resolution). Exception: `background-light`/`background-dark` are two separate tokens switched via explicit `dark:bg-background-dark`.

```css
/* :root (light) */
--primary: #3211d4;
--primary-hover: #2a0eb3;
--accent: #7c3aed;
--secondary: #a78bfa;
--background-light: #f6f6f8;
--background-dark: #0b0b0f;
--surface-lowest: #ffffff;
--surface: #f8fafc;
--surface-container: #f1f5f9;
--surface-high: #e2e8f0;
--text-main: #0f172a;
--text-muted: #94a3b8;
--text-secondary: #64748b;
--border-subtle: #e2e8f0;
--error: #dc2626;

/* .dark — overrides the same variable names (primary, accent, secondary, surface-*, text-*, border-subtle, error) */
--primary: #8b85f7;
--primary-hover: #a5a0f9;
--accent: #a78bfa;
--secondary: #c4b5fd;
--surface-lowest: #101014;
--surface: #131318;
--surface-container: #1c1c22;
--surface-high: #2a2a32;
--text-main: #e7e7ec;
--text-muted: #8a8f98;
--text-secondary: #b3b8c2;
--border-subtle: rgba(255, 255, 255, 0.08);
--error: #f87171;
```

Legacy classes (`glass-panel`, `glow-*`, `neon-text-*`, `active-pill`) are kept as no-ops (`box-shadow:none`/`text-shadow:none`) instead of being deleted, so existing usages don't break while the visual effect goes away.

```css
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
      <h1 class="font-bold text-slate-900">TTOBAK</h1>
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
  header: back button + title (input) + translation-language (select) + logout icon
  main:
    - circular timer (bg-primary/10 pulse, bg-primary/20, white circle with border-4 border-primary)
    - waveform bars (w-1 bg-primary rounded-full, heights varying)
    - "Recording in progress..." text
    - controls: [pause] [stop (primary, large)] [camera]
    - Recently Captured grid (3 columns)
  bottom-nav: fixed
```

### 2.6 Recording Screen (PC)

```
Layout:
  sidebar (w-64)
  main:
    header: nav + title + "RECORDING LIVE" badge + search + profile
    content (flex):
      center:
        - Status Card (rounded-2xl shadow-sm border, p-8)
          - timer: text-6xl font-black tracking-tighter
          - waveform: gradient bars
          - stats: Storage / Quality / Bitrate
        - Captured Assets Grid (4 columns)
      right-panel (w-80):
        - Live Transcription
        - Speaker entries with avatar initials + timestamp
        - Export button
```

### 2.6a Post-Recording Banner & System Audio Mode (current implementation, ADR-024)

`components/record/PostRecordingBanner.tsx` — a fixed top toast shown while `usePostRecording`'s `step` is non-null (`creating` → `notes` → `saving` → `uploading` → `redirecting`, or `error`; `notes` pauses the flow for the notes-input dialog before save/upload resumes).

```
Uploading step:
  spinner + "Uploading... N% (X MB / Y MB)" when `uploadProgress` is set
  (both browser blob uploads and Tauri native file uploads report this)
  else: plain "Uploading audio..." label
  thin progress bar (bg-primary, width = percentage) underneath the label

Error step:
  red banner, error icon, message truncated to one line
  [Try Again] — re-runs the upload from the retained pending payload
  [Home] — clears state and navigates away (does NOT retry)
```

In Tauri desktop System Audio mode (`audioSource === 'system'`, `isTauri()`), both the pre-recording setup notice and the during-recording banner (purple, `speaker` icon) note that live captions are best-effort (fed by Rust-downsampled PCM over `native-pcm-chunk` into the same Transcribe Streaming pipeline mic/tab modes use — no Web Speech fallback here) and that transcription still happens automatically once the meeting ends. `isNativeRecording` (`app/record/page.tsx`) drives the during-recording banner/title/nav-lock independently of `session.isRecording`, since it must be true from the moment native capture starts, before the STT session resolves.

Before that, the native start path runs a **preflight** check (`assertUploadRecordingAvailable`, `frontend/src/lib/tauri.ts`) that fails the start outright — an instant rejection, not a timed check — if the installed app predates the `upload_recording` command; the amber "speech error" banner shows an update prompt with no draft meeting created (see ADR-024, motivated by an incident where this version skew silently lost 83 minutes of System Audio recording).

Once uploading, native mode's `uploadRecordingWithRetry` (`lib/tauri.ts`) is network-aware: on going offline mid-upload it waits for the browser's `online` event rather than failing, then re-presigns before every retry (uploads use a 1h presign TTL). This wait/retry cycle is bounded by a cumulative 45-minute budget (not reset per offline/online cycle); non-network failures instead get a small bounded retry (2 retries, linear backoff). Both the wait and its enclosing flow are cancelled on reset/unmount via an `AbortController` plus a generation counter (`flowGenerationRef`) — the counter is what actually gates the flow, since abort alone can't retroactively cancel a PUT already in flight. An abandoned-but-completed PUT still triggers the server-side transcribe/summarize pipeline regardless (tracked as a known orphan-cleanup gap in ADR-024's Consequences); the WAV itself is never at risk, since `cleanupRecording` only runs after the backend confirms upload-complete.

### 2.7 Meeting Detail (Mobile)

```
Layout:
  header: back button + "Meeting Report" + more
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
    - AI Summary + Action Items row (drag-resizable, useResizablePanel):
      - AI Summary (bg-white border rounded-xl p-6) — drag-resizable width 400-900px,
        persisted to localStorage `ttobak:meetingSummaryWidth`
      - w-2 divider (bg-slate-300 dark:bg-white/20, hover:bg-primary/60,
        cursor-col-resize) — visual boundary and drag handle
      - Action Items (bg-primary/5 border-primary/20 rounded-xl p-6, flex-1)
      - side-by-side vs. stacked switches on measured row width (ResizeObserver),
        not a viewport breakpoint — auto-stacks when the min width + reserve won't fit
    - Attachments Gallery (4-column grid, hover overlay)
    - Full Transcription (timestamp badges + speaker entries)
    - Floating Audio Player (sticky bottom-6, rounded-full, backdrop-blur)
  reference aside (right side, drag-resizable 280-640px, localStorage
    `ttobak:meetingAsideWidth`) — same w-2 divider pattern
```

### 2.9 LiveTranscript Component

Displays streaming real-time transcription and translation results.

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
        <p class="text-sm text-slate-600">Transcribed text appears here.</p>
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
        <p class="text-sm text-slate-500">Currently speaking...</p>
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

Panel for asking questions during a meeting and displaying KB RAG answers.

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
        <p class="text-sm font-medium text-slate-700">What deadline was decided in this meeting?</p>
      </div>
      <!-- Answer -->
      <div class="ml-6 bg-primary/5 rounded-lg p-3">
        <p class="text-sm text-slate-600">The deadline was set for March 15.</p>
        <!-- Sources -->
        <div class="mt-2 pt-2 border-t border-slate-200">
          <span class="text-[10px] font-bold uppercase tracking-wider text-slate-400">Sources</span>
          <div class="mt-1 space-y-1">
            <a class="flex items-center gap-1 text-xs text-primary hover:underline">
              <span class="material-symbols-outlined text-[14px]">description</span>
              Product Strategy Sync (this meeting)
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

**Proactive search** (`LiveQAPanel`, live recording panel only): a header toggle chip ("Proactive search ON/OFF", `travel_explore` icon, pill shape) — ON uses sky tones (`bg-sky-50 border-sky-300 text-sky-600`, dark `bg-sky-900/20 border-sky-700 text-sky-400`), OFF uses slate. **Default OFF**, since conversation-derived search terms go out to an external web search — opt-in with a tooltip disclosure, state persisted to localStorage `ttobak.proactiveSearchEnabled`. Auto-fired question bubbles are visually distinct from manual questions (sky background + border, `travel_explore` icon, "Proactive search · detected from conversation" caption). Answer tool badges add `search_web` → "Web Search" (sky) alongside the existing KB search (emerald)/AWS Docs (blue)/meeting-transcript search (violet)/AWS recommendation (amber) badges.

### 2.11 KBFileList Component

Component for uploading and managing Knowledge Base files.

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

Dropdown menu offering meeting export options.

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

UI for configuring external service API keys.

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

Checkbox on the recording screen for switching between offline/online mode.

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

Checkbox group for selecting real-time translation target languages.

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
    </label>

    <label class="flex items-center gap-3 p-2 rounded-lg hover:bg-slate-50 cursor-pointer">
      <input type="checkbox" class="w-4 h-4 rounded text-primary focus:ring-primary" checked />
      <span class="text-sm text-slate-600">English → Korean</span>
    </label>

    <label class="flex items-center gap-3 p-2 rounded-lg hover:bg-slate-50 cursor-pointer">
      <input type="checkbox" class="w-4 h-4 rounded text-primary focus:ring-primary" />
      <span class="text-sm text-slate-600">Japanese → Korean</span>
    </label>

    <label class="flex items-center gap-3 p-2 rounded-lg hover:bg-slate-50 cursor-pointer">
      <input type="checkbox" class="w-4 h-4 rounded text-primary focus:ring-primary" />
      <span class="text-sm text-slate-600">Chinese → Korean</span>
    </label>
  </div>

  <p class="mt-3 text-xs text-slate-400">
    Select languages to translate in real-time during recording.
  </p>
</div>
```

### 2.16 Cost/Sizing Simulator Card (`SimCard`, ADR-031)

Appears on the meeting detail page only once the meeting note itself is `done`. Five states driven by `SimRun.status`, one card, no separate page:

- **idle** (no `SimRun` yet): one-line explainer + a single "시뮬레이션 실행" button (`query_stats` icon in the section header)
- **extracted**: confirm/correct form — one row per extracted `SimRequirement` (label, required-marker, an editable value input, and a "녹취록에서 확인" link when `evidence` is present) + 2–3 architecture-option name/description input pairs. "실행" is disabled until every required requirement has a value and at least 2 options have a name — this is a UX convenience only, the server re-validates everything (ADR-031)
- **queued / running**: spinner + "1~3분 소요, 다른 작업을 계속하셔도 됩니다" — no page-blocking modal, since this is a background job
- **done**: an amber "추정치 — 검증 필요" banner with the price-snapshot timestamp, then the generated `report.md` rendered through the same `MarkdownRenderer` every other markdown surface uses, with `sim://chart_N` rewritten to each chart's presigned URL before render (same rewrite-before-render shape as `resolveAttachmentUrls`) — charts inherit `ZoomPanViewport` for free through `MermaidBlock`'s sibling markdown image handling
- **error**: `errorMessage` + "다시 시도" (re-runs extraction)

No dedicated color token — reuses the existing primary/amber/red palette (amber = "needs verification" banner, red = error text), consistent with the rest of the meeting detail page.

### 2.17 Account Picker (select-based, with loading/error states)

프로젝트 상세 페이지(`ProjectDetailClient.tsx`)의 "계정 연결" 컨트롤. 이전에는
사용자가 직접 계정 UUID를 입력해야 하는 텍스트 입력창이었으나(선택할 방법이
없어 실질적으로 사용 불가), `accountApi.list()`로 가져온 접근 가능한 전체
계정 목록을 이름 기준(한국어 로케일)으로 정렬해 보여주는 `<select>`로 대체.
이미 연결된 계정은 옵션에서 제외한다. 로딩/에러 상태를 명시적으로 분리해,
목록을 가져오는 fetch가 실패한 경우와 "실제로 계정이 0개인 경우"를 사용자가
구분할 수 있게 하고(실패 시 다시 시도 버튼 제공), fetch가 아직 완료되지 않은
로딩 구간을 "선택 가능한 계정 없음"으로 잘못 표시하지 않는다. `AccountsClient`의
계정 목록도 동일하게 이름 기준 정렬을 적용한다. UI 문자열은 이 앱의 나머지
전체와 마찬가지로 한국어(원래 구현에 남아있던 영어 문자열은 이 변경으로 정리).

```html
<!-- Loading -->
<p class="pt-2 text-sm text-slate-400 dark:text-text-muted">계정 목록을 불러오는 중…</p>

<!-- Error -->
<div class="flex items-center gap-2 pt-2 text-sm text-red-500">
  <span>계정 목록을 불러오지 못했습니다.</span>
  <button class="font-semibold hover:underline">다시 시도</button>
</div>

<!-- Loaded: select + submit, same input/button styling as other project forms -->
<form class="flex gap-2 pt-2">
  <select class="flex-1 min-w-0 px-3 py-2 rounded-lg border border-slate-200 dark:border-white/10 bg-white dark:bg-surface-lowest text-sm">
    <option disabled>계정 선택…</option>
    <option>Acme Corp</option>
    <option>Globex</option>
  </select>
  <button class="px-3 py-2 rounded-lg bg-primary hover:bg-primary-hover text-white text-sm disabled:opacity-50">
    연결
  </button>
</form>
```

**참고**: Account/Project 페이지 전반(`AccountsClient`, `AccountDetailClient`,
`ProjectDetailClient`)은 이 문서에 아직 별도 섹션이 없다 — 이 항목은 이번에
새로 만든 패턴만 다루며, 나머지 페이지 전체를 다루는 문서화는 이 변경의
범위 밖이다 (별도 후속 작업으로 남김).

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

### 3.5 Diagram Zoom/Pan (`ZoomPanViewport`, `DiagramLightbox`)
- Plain wheel scroll always scrolls the page; only `Ctrl`/`⌘`+wheel zooms (0.25×–8×) — a diagram must never trap normal page scroll while reading a note
- One-finger drag / pointer drag pans; two-finger pinch zooms (`touch-action: none` applied only once zoomed past 1× — the default scale still scrolls normally on touch)
- Zoom controls (`zoom_in`/`zoom_out`/`fit_screen`/`fullscreen`) fade in on hover (desktop) or stay visible (touch, `group-focus-within`)
- Fullscreen opens `DiagramLightbox`, reusing `AttachmentGallery`'s image-modal visual language (`fixed inset-0 z-50 bg-black/80 backdrop-blur-sm`, backdrop-click or `Esc` to close, top-right close button) rather than a new one
- Applied to every mermaid diagram via `MermaidBlock` — since `MarkdownRenderer` is the single markdown surface, this covers meeting notes, live summary, docs, insights, and research uniformly

## 4. Icon Mapping

| Purpose | Material Symbol | Used In |
|---------|----------------|-----------|
| Home | home | Mobile bottom nav |
| Meetings | videocam / video_camera_front | Sidebar |
| Record | mic | Mobile bottom nav |
| Files | description / folder_open | Navigation |
| Settings | settings | Navigation |
| Profile | person / account_circle | Navigation |
| Search | search | Search bar |
| Calendar | calendar_today / calendar_month | Date display |
| Add | add / add_circle | FAB, new meeting |
| Back | arrow_back | Mobile header |
| More | more_horiz | Card menu |
| AI | auto_awesome | AI Summary |
| Check | check_circle | Action items |
| Pause | pause | Recording controls |
| Stop | stop | Recording controls |
| Camera | add_a_photo | Capture during recording |
| Transcribe | translate | Live transcription |
| Attachment | attachment | Attached files |
| Notes | notes | Transcript |
| Play | play_arrow | Audio player |
| Download | download | Export |
| Notifications | notifications | Header |
| Share | share | Share button |
| Compare | compare | Image comparison |
| Upload | upload_file | File upload |
| Team | group | Sidebar |
| Project (SFDC Oppty) | work | Sidebar (excluded from mobile bottom nav — limited to 4-5 items, §2.1) |
| Insights | analytics / insights | Sidebar |
| Translation | translate | Real-time translation |
| Q&A | question_answer | Meeting Q&A |
| Knowledge Base | library_books | KB management |
| Export | file_download | Export menu |
| API Key | vpn_key | Integration settings |
| Obsidian | link | Obsidian export |
| Extensions | extension | Integrations |
| WiFi | wifi | Online mode |
| Lightning | bolt | Real-time mode |
| PDF | picture_as_pdf | PDF export |
| Code | code | Markdown export |
| Note | note_alt | Notion export |
| Help | help | Q&A question |
| Send | send | Q&A send button |
| Delete | delete | Delete KB file |
| Cloud | cloud | External service |
| Zoom in | zoom_in | Diagram zoom/pan controls (`ZoomPanViewport`) |
| Zoom out | zoom_out | Diagram zoom/pan controls |
| Fit to screen | fit_screen | Diagram zoom/pan reset |
| Fullscreen | fullscreen | Diagram lightbox (`DiagramLightbox`) |
| Cost simulator | query_stats | Cost/sizing simulator card (`SimCard`, ADR-031) |
