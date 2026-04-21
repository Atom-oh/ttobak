# Frontend Redesign Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Redesign the Ttobak frontend to match the design sample, improve Live Q&A intuitiveness, add audio player, refactor the record page, and polish global design consistency.

**Architecture:** Component-first approach — extract reusable UI primitives, then compose into page layouts. Each task produces a working, visually testable change. Design sample (`design_sample/meeting-note-pc.html`) is the north star for Meeting Detail page.

**Tech Stack:** Next.js 16, React, Tailwind CSS v4, Material Symbols, TipTap

---

## File Structure

### New Files
| File | Responsibility |
|------|---------------|
| `frontend/src/components/AudioPlayer.tsx` | Floating pill-shaped audio playback bar |
| `frontend/src/components/meeting/MeetingHeader.tsx` | Meeting detail header (title, date, participants, actions) |
| `frontend/src/components/meeting/AISummaryCard.tsx` | AI summary card with prose rendering |
| `frontend/src/components/meeting/ActionItemsCard.tsx` | Action items checklist card |
| `frontend/src/components/meeting/TranscriptSection.tsx` | Full transcription with speaker colors and timestamps |
| `frontend/src/components/meeting/ProcessingStatus.tsx` | Processing status indicator with progress bar |
| `frontend/src/components/record/RecordingControls.tsx` | Recording state UI (timer, pause/stop/camera) |
| `frontend/src/components/record/PostRecordingBanner.tsx` | Post-recording toast/error banner |
| `frontend/src/components/record/RecordingConfig.tsx` | STT provider selector, summary interval, translation toggle |
| `frontend/src/components/qa/QAChatMessage.tsx` | Shared Q&A message bubble (question + answer with tool badges) |
| `frontend/src/components/qa/QASuggestedQuestions.tsx` | Suggested/detected question chips |
| `frontend/src/components/qa/QAEmptyState.tsx` | Empty state with icon and prompt |

### Modified Files
| File | Change |
|------|--------|
| `frontend/src/app/meeting/[id]/MeetingDetailClient.tsx` | 2-column grid layout, extract sub-components, audio player |
| `frontend/src/components/LiveQAPanel.tsx` | Extract shared components, improve empty state, add typing indicator |
| `frontend/src/components/QAPanel.tsx` | Extract shared components, improve UX |
| `frontend/src/app/record/page.tsx` | Extract sub-components, reduce from 847 to ~200 lines |
| `frontend/src/components/RecordButton.tsx` | Minor cleanup (used as-is, mostly stable) |
| `frontend/src/components/AttachmentGallery.tsx` | Add hover zoom animation, gradient overlay polish |
| `frontend/src/components/layout/AppLayout.tsx` | Consistent CSS variable usage |
| `frontend/src/app/globals.css` | Add shared animation keyframes, CSS custom properties for dark mode |

---

## Task 1: Global Design Tokens & Animation Foundation

**Files:**
- Modify: `frontend/src/app/globals.css`
- Modify: `frontend/tailwind.config.ts` (if exists, otherwise `frontend/postcss.config.mjs`)

This task establishes the design foundation that all subsequent tasks depend on.

- [ ] **Step 1: Add CSS custom properties for consistent theming**

In `globals.css`, add a `:root` / `.dark` block with design tokens:

```css
:root {
  --color-primary: #3211d4;
  --color-primary-hover: #2a0eb3;
  --color-surface: #ffffff;
  --color-surface-secondary: #f6f6f8;
  --color-border: #e2e8f0;
  --color-text-primary: #0f172a;
  --color-text-secondary: #475569;
  --color-text-muted: #94a3b8;
}

.dark {
  --color-surface: #0f172a;
  --color-surface-secondary: #1e293b;
  --color-border: #334155;
  --color-text-primary: #f1f5f9;
  --color-text-secondary: #cbd5e1;
  --color-text-muted: #64748b;
}
```

- [ ] **Step 2: Add shared animation keyframes**

```css
@keyframes slide-up {
  from { transform: translateY(100%); opacity: 0; }
  to { transform: translateY(0); opacity: 1; }
}
@keyframes fade-in {
  from { opacity: 0; transform: translateY(4px); }
  to { opacity: 1; transform: translateY(0); }
}
@keyframes pulse-ring {
  0% { transform: scale(0.9); opacity: 0.5; }
  50% { transform: scale(1.1); opacity: 0.2; }
  100% { transform: scale(0.9); opacity: 0.5; }
}
.animate-slide-up { animation: slide-up 0.3s ease-out; }
.animate-fade-in { animation: fade-in 0.2s ease-out; }
.animate-pulse-ring { animation: pulse-ring 2s ease-in-out infinite; }
```

- [ ] **Step 3: Verify animations render correctly**

Run: `cd /home/ec2-user/ttobak/frontend && npm run build`
Expected: Build succeeds with no CSS errors.

- [ ] **Step 4: Commit**

```bash
git add frontend/src/app/globals.css
git commit -m "style: add design tokens and shared animation keyframes"
```

---

## Task 2: Meeting Detail Page — 2-Column Layout Redesign (URGENT)

**Files:**
- Create: `frontend/src/components/meeting/MeetingHeader.tsx`
- Create: `frontend/src/components/meeting/AISummaryCard.tsx`
- Create: `frontend/src/components/meeting/ActionItemsCard.tsx`
- Create: `frontend/src/components/meeting/ProcessingStatus.tsx`
- Create: `frontend/src/components/meeting/TranscriptSection.tsx`
- Modify: `frontend/src/app/meeting/[id]/MeetingDetailClient.tsx`

**Layout context:** The current page has a `flex` layout: main content (left) + Q&A side panel `w-96` (right, `xl:flex` only). The design sample's `grid-cols-12` applies WITHIN the main content area, not full-page. Summary (`col-span-7`) + Action Items (`col-span-5`) go inside the existing `<div className="flex-1 p-4 lg:px-16 lg:py-12">` block. The Q&A side panel remains as-is. On `xl` screens this creates: [sidebar 256px] + [main with 2-col grid] + [Q&A 384px]. On `lg` screens without Q&A panel, the grid fills the main area.

- [ ] **Step 1: Create MeetingHeader component**

Extract the header section (breadcrumbs, title, date, participants, action buttons) from `MeetingDetailClient.tsx:252-306` into a focused component.

```tsx
// frontend/src/components/meeting/MeetingHeader.tsx
'use client';

import Link from 'next/link';
import { ExportMenu } from '@/components/ExportMenu';
import { ShareButton } from '@/components/ShareButton';
import type { MeetingDetail, SharedUser } from '@/types/meeting';

interface MeetingHeaderProps {
  meeting: MeetingDetail;
  onShare: (user: SharedUser) => void;
  onUnshare: (userId: string) => void;
}

// Note: Delete functionality uses DesktopDeleteButton (self-contained with internal state)
// and MobileMoreMenu — both imported and rendered inside this component.

function formatDate(dateString: string): string {
  return new Date(dateString).toLocaleDateString('en-US', {
    month: 'long', day: 'numeric', year: 'numeric',
  });
}

function formatTime(dateString: string): string {
  return new Date(dateString).toLocaleTimeString('en-US', {
    hour: 'numeric', minute: '2-digit', hour12: true,
  });
}

export function MeetingHeader({ meeting, onShare, onUnshare }: MeetingHeaderProps) {
  return (
    <header className="mb-8 lg:mb-10">
      {/* Desktop breadcrumbs */}
      <div className="hidden lg:flex items-center gap-1.5 text-sm text-[var(--color-text-muted)] mb-8">
        <Link href="/" className="hover:text-[var(--color-primary)] transition-colors">Meetings</Link>
        <span>/</span>
        <span className="text-[var(--color-text-primary)] font-medium">{meeting.title}</span>
      </div>

      {/* Tag + date */}
      <div className="flex items-center gap-2 mb-3">
        {meeting.tags?.[0] && (
          <span className="bg-slate-900 dark:bg-white text-white dark:text-slate-900 text-[10px] font-bold uppercase tracking-wider px-2 py-0.5 rounded">
            {meeting.tags[0]}
          </span>
        )}
        <span className="text-[var(--color-text-muted)] text-xs">
          {formatDate(meeting.date)} · {formatTime(meeting.date)}
        </span>
      </div>

      {/* Title */}
      <h1 className="text-3xl lg:text-4xl font-black tracking-tight text-[var(--color-text-primary)] mb-4">
        {meeting.title}
      </h1>

      {/* Participants + Actions */}
      <div className="flex flex-wrap items-center justify-between gap-4">
        <div className="flex items-center gap-3">
          <div className="flex -space-x-2">
            {meeting.participants?.slice(0, 4).map((p) => (
              <div key={p.id} className="size-8 lg:size-9 rounded-full border-2 border-[var(--color-surface)] bg-slate-200 flex items-center justify-center text-[10px] font-bold text-slate-500 overflow-hidden">
                {p.avatarUrl ? <img src={p.avatarUrl} alt={p.name} className="w-full h-full object-cover" /> : (p.initials || p.name?.charAt(0))}
              </div>
            ))}
            {(meeting.participants?.length || 0) > 4 && (
              <div className="size-8 lg:size-9 rounded-full border-2 border-[var(--color-surface)] bg-slate-100 dark:bg-slate-800 flex items-center justify-center text-[10px] font-bold text-slate-500">
                +{meeting.participants!.length - 4}
              </div>
            )}
          </div>
          <p className="text-xs text-[var(--color-text-muted)] font-medium hidden lg:block">
            {meeting.participants?.map(p => p.name?.split(' ')[0]).slice(0, 3).join(', ')}
            {(meeting.participants?.length || 0) > 3 && ` and ${meeting.participants!.length - 3} others`}
          </p>
        </div>
        <div className="flex items-center gap-2">
          <DesktopDeleteButton meetingId={meeting.meetingId} />
          <ExportMenu meetingId={meeting.meetingId} />
          <ShareButton meetingId={meeting.meetingId} sharedWith={meeting.sharedWith} onShare={onShare} onUnshare={onUnshare} />
        </div>
      </div>
    </header>
  );
}
```

- [ ] **Step 2: Create AISummaryCard component**

Matches the design sample's white card with border, `auto_awesome` icon, prose rendering.

```tsx
// frontend/src/components/meeting/AISummaryCard.tsx
'use client';

interface AISummaryCardProps {
  content?: string;
  summary?: string;
  transcriptA?: string;
}

export function AISummaryCard({ content, summary, transcriptA }: AISummaryCardProps) {
  const displayText = content || summary;

  return (
    <div className="bg-[var(--color-surface)] border border-[var(--color-border)] rounded-xl p-6 shadow-sm">
      <div className="flex items-center gap-2 mb-4 text-[var(--color-primary)]">
        <span className="material-symbols-outlined">auto_awesome</span>
        <h3 className="font-bold">AI Summary</h3>
      </div>
      <div className="prose prose-sm dark:prose-invert max-w-none text-[var(--color-text-secondary)] leading-relaxed whitespace-pre-wrap">
        {displayText || <span className="text-[var(--color-text-muted)] italic">요약이 아직 생성되지 않았습니다.</span>}
      </div>
      {transcriptA && (
        <details className="mt-6 border border-[var(--color-border)] rounded-lg">
          <summary className="px-4 py-3 text-sm font-medium text-[var(--color-text-secondary)] cursor-pointer hover:bg-slate-50 dark:hover:bg-slate-800 rounded-lg flex items-center gap-2">
            <span className="material-symbols-outlined text-lg">notes</span>
            원본 텍스트 보기
          </summary>
          <div className="px-4 pb-4 text-sm text-[var(--color-text-muted)] leading-relaxed whitespace-pre-wrap border-t border-[var(--color-border)] pt-3">
            {transcriptA}
          </div>
        </details>
      )}
    </div>
  );
}
```

- [ ] **Step 3: Create ActionItemsCard component**

Matches the design sample's `primary/5` background card.

```tsx
// frontend/src/components/meeting/ActionItemsCard.tsx
'use client';

import type { ActionItem } from '@/types/meeting';

interface ActionItemsCardProps {
  items?: ActionItem[];
  onToggle: (itemId: string) => void;
}

export function ActionItemsCard({ items, onToggle }: ActionItemsCardProps) {
  return (
    <div className="bg-[var(--color-primary)]/5 dark:bg-[var(--color-primary)]/10 border border-[var(--color-primary)]/20 rounded-xl p-6">
      <div className="flex items-center gap-2 mb-4 text-[var(--color-primary)]">
        <span className="material-symbols-outlined">check_circle</span>
        <h3 className="font-bold">Action Items</h3>
      </div>
      <div className="space-y-4">
        {items && items.length > 0 ? (
          items.map((item) => (
            <div key={item.id} className="flex items-start gap-3">
              <input
                type="checkbox"
                checked={item.completed}
                onChange={() => onToggle(item.id)}
                className="mt-1 rounded border-[var(--color-primary)]/30 text-[var(--color-primary)] focus:ring-[var(--color-primary)] h-4 w-4"
              />
              <div className="flex flex-col">
                <span className={`text-sm font-medium transition-all duration-200 ${item.completed ? 'line-through text-[var(--color-text-muted)]' : 'text-[var(--color-text-primary)]'}`}>
                  {item.text}
                </span>
                {item.assignee && (
                  <span className="text-[10px] text-[var(--color-text-muted)] mt-0.5">
                    Assigned to: @{item.assignee}
                    {item.dueDate && ` · Due: ${item.dueDate}`}
                  </span>
                )}
              </div>
            </div>
          ))
        ) : (
          <p className="text-sm text-[var(--color-text-muted)] italic">액션 아이템이 없습니다.</p>
        )}
      </div>
    </div>
  );
}
```

- [ ] **Step 4: Create ProcessingStatus component**

```tsx
// frontend/src/components/meeting/ProcessingStatus.tsx
'use client';

interface ProcessingStatusProps {
  status: string;
}

const STATUS_CONFIG: Record<string, { label: string; detail: string; progress: string }> = {
  recording: { label: '오디오 업로드 준비 중...', detail: '잠시만 기다려주세요', progress: '25%' },
  transcribing: { label: 'AI 음성 인식 중... (화자 분리 포함)', detail: '음성을 텍스트로 변환하고 있습니다', progress: '50%' },
  summarizing: { label: 'AI 회의록 생성 중...', detail: '화자별 요약을 작성하고 있습니다', progress: '75%' },
};

export function ProcessingStatus({ status }: ProcessingStatusProps) {
  const config = STATUS_CONFIG[status] || { label: '처리 중...', detail: '잠시만 기다려주세요', progress: '90%' };

  return (
    <div className="mb-8 animate-fade-in">
      <div className="flex items-center gap-3 p-4 bg-[var(--color-primary)]/5 border border-[var(--color-primary)]/20 rounded-xl">
        <div className="animate-spin rounded-full h-5 w-5 border-2 border-[var(--color-primary)] border-t-transparent shrink-0" />
        <div className="flex-1">
          <span className="text-sm font-medium text-[var(--color-primary)] block">{config.label}</span>
          <span className="text-xs text-[var(--color-primary)]/60 mt-0.5 block">{config.detail}</span>
        </div>
      </div>
      <div className="mt-2 h-1 bg-slate-100 dark:bg-slate-800 rounded-full overflow-hidden">
        <div className="h-full bg-[var(--color-primary)] rounded-full animate-pulse" style={{ width: config.progress }} />
      </div>
    </div>
  );
}
```

- [ ] **Step 5: Create TranscriptSection component**

Extract from `MeetingDetailClient.tsx:443-487`.

```tsx
// frontend/src/components/meeting/TranscriptSection.tsx
'use client';

import type { TranscriptSegment } from '@/types/meeting';

interface TranscriptSectionProps {
  transcription: TranscriptSegment[];
}

function formatTimestamp(seconds: number): string {
  const mins = Math.floor(seconds / 60);
  const secs = Math.floor(seconds % 60);
  return `${mins}:${secs.toString().padStart(2, '0')}`;
}

function speakerColor(speaker: string): string {
  const hue = speaker.split('').reduce((a, c) => a + c.charCodeAt(0), 0) % 360;
  return `hsl(${hue}, 70%, 55%)`;
}

export function TranscriptSection({ transcription }: TranscriptSectionProps) {
  if (!transcription || transcription.length === 0) return null;

  return (
    <section className="border-t border-[var(--color-border)] pt-12 mb-12">
      <div className="flex items-center justify-between mb-8">
        <h2 className="text-xl font-bold flex items-center gap-2 text-[var(--color-text-primary)]">
          <span className="material-symbols-outlined">notes</span>
          Full Transcription
        </h2>
        <div className="flex gap-2">
          <button className="px-3 py-1.5 rounded-lg border border-[var(--color-border)] text-xs font-semibold flex items-center gap-2 bg-[var(--color-surface)] hover:bg-slate-50 dark:hover:bg-slate-800 transition-colors">
            <span className="material-symbols-outlined text-sm">search</span>
            Search transcript
          </button>
          <button className="px-3 py-1.5 rounded-lg border border-[var(--color-border)] text-xs font-semibold flex items-center gap-2 bg-[var(--color-surface)] hover:bg-slate-50 dark:hover:bg-slate-800 transition-colors">
            <span className="material-symbols-outlined text-sm">download</span>
            Export
          </button>
        </div>
      </div>

      <div className="space-y-8">
        {transcription.map((segment) => (
          <div key={segment.id} className="flex gap-6 animate-fade-in">
            <div className="w-16 pt-1 flex-shrink-0">
              <span className="text-xs font-bold text-[var(--color-primary)] px-2 py-1 bg-[var(--color-primary)]/10 rounded">
                {formatTimestamp(segment.startTime)}
              </span>
            </div>
            <div className="flex-1 min-w-0">
              <div className="flex items-center gap-2 mb-2">
                <div className="w-2 h-2 rounded-full shrink-0" style={{ backgroundColor: speakerColor(segment.speaker) }} />
                <span className="text-sm font-black text-[var(--color-text-primary)]">{segment.speaker}</span>
                <span className="text-[10px] text-[var(--color-text-muted)]">{segment.timestamp}</span>
              </div>
              <p className="text-[var(--color-text-secondary)] text-sm leading-relaxed">{segment.text}</p>
            </div>
          </div>
        ))}
      </div>
    </section>
  );
}
```

- [ ] **Step 6: Rewrite MeetingDetailClient with 2-column grid layout**

Replace the current vertical layout with the design sample's `grid-cols-12` pattern:
- Summary (`col-span-7`) + Action Items (`col-span-5`) side by side
- Full-width transcript section below
- Use extracted components

Key layout change in `MeetingDetailClient.tsx`:

```tsx
{/* Core Content Grid — matches design sample */}
{meeting.status === 'done' ? (
  <div className="grid grid-cols-1 lg:grid-cols-12 gap-6 mb-12">
    <div className="lg:col-span-7">
      <AISummaryCard content={meeting.content} summary={meeting.summary} transcriptA={meeting.transcriptA} />
    </div>
    <div className="lg:col-span-5">
      <ActionItemsCard items={meeting.actionItems} onToggle={handleActionItemToggle} />
    </div>
  </div>
) : (
  <>
    <ProcessingStatus status={meeting.status} />
    <div className="mb-12">
      <h3 className="text-lg font-bold flex items-center gap-2 mb-4">
        <span className="material-symbols-outlined text-[var(--color-text-muted)]">subtitles</span>
        라이브 텍스트
      </h3>
      <p className="text-[var(--color-text-secondary)] leading-relaxed whitespace-pre-wrap">
        {meeting.transcriptA || meeting.content || '음성 인식 결과를 기다리는 중...'}
      </p>
    </div>
  </>
)}
```

- [ ] **Step 7: Verify the meeting detail page renders correctly**

Run: `cd /home/ec2-user/ttobak/frontend && npm run build`
Expected: Build succeeds, no TypeScript errors.

- [ ] **Step 8: Commit**

```bash
git add frontend/src/components/meeting/ frontend/src/app/meeting/
git commit -m "feat: meeting detail 2-column grid layout with extracted components"
```

---

## Task 3: Floating Audio Player Component

**Files:**
- Create: `frontend/src/components/AudioPlayer.tsx`
- Modify: `frontend/src/app/meeting/[id]/MeetingDetailClient.tsx`

Matches the design sample's sticky bottom pill-shaped player.

- [ ] **Step 1: Create AudioPlayer component**

```tsx
// frontend/src/components/AudioPlayer.tsx
'use client';

import { useState, useRef, useEffect, useCallback } from 'react';

interface AudioPlayerProps {
  audioUrl?: string;
}

function formatTime(seconds: number): string {
  const mins = Math.floor(seconds / 60);
  const secs = Math.floor(seconds % 60);
  return `${mins.toString().padStart(2, '0')}:${secs.toString().padStart(2, '0')}`;
}

export function AudioPlayer({ audioUrl }: AudioPlayerProps) {
  const audioRef = useRef<HTMLAudioElement>(null);
  const [isPlaying, setIsPlaying] = useState(false);
  const [currentTime, setCurrentTime] = useState(0);
  const [duration, setDuration] = useState(0);
  const [volume, setVolume] = useState(1);

  useEffect(() => {
    const audio = audioRef.current;
    if (!audio) return;
    const onTime = () => setCurrentTime(audio.currentTime);
    const onMeta = () => setDuration(audio.duration);
    const onEnd = () => setIsPlaying(false);
    audio.addEventListener('timeupdate', onTime);
    audio.addEventListener('loadedmetadata', onMeta);
    audio.addEventListener('ended', onEnd);
    return () => {
      audio.removeEventListener('timeupdate', onTime);
      audio.removeEventListener('loadedmetadata', onMeta);
      audio.removeEventListener('ended', onEnd);
    };
  }, [audioUrl]);

  const togglePlay = useCallback(() => {
    const audio = audioRef.current;
    if (!audio) return;
    if (isPlaying) { audio.pause(); } else { audio.play(); }
    setIsPlaying(!isPlaying);
  }, [isPlaying]);

  const seek = useCallback((e: React.MouseEvent<HTMLDivElement>) => {
    const audio = audioRef.current;
    if (!audio || !duration) return;
    const rect = e.currentTarget.getBoundingClientRect();
    const pct = (e.clientX - rect.left) / rect.width;
    audio.currentTime = pct * duration;
  }, [duration]);

  const skip = useCallback((seconds: number) => {
    const audio = audioRef.current;
    if (!audio) return;
    audio.currentTime = Math.max(0, Math.min(duration, audio.currentTime + seconds));
  }, [duration]);

  const [error, setError] = useState(false);

  useEffect(() => { setError(false); }, [audioUrl]);

  if (!audioUrl || error) return null;

  const progress = duration > 0 ? (currentTime / duration) * 100 : 0;

  return (
    <div className="sticky bottom-6 mt-12 w-full max-w-2xl mx-auto z-30 animate-slide-up">
      <audio ref={audioRef} src={audioUrl} preload="metadata" onError={() => setError(true)} />
      <div className="bg-[var(--color-surface)]/80 backdrop-blur-md border border-[var(--color-border)] shadow-xl rounded-full px-6 py-3 flex items-center gap-4">
        {/* Play button */}
        <button onClick={togglePlay}
          className="size-10 rounded-full bg-[var(--color-primary)] flex items-center justify-center text-white shadow-lg shadow-[var(--color-primary)]/20 hover:scale-105 active:scale-95 transition-transform">
          <span className="material-symbols-outlined">{isPlaying ? 'pause' : 'play_arrow'}</span>
        </button>

        {/* Progress */}
        <div className="flex-1">
          <div className="flex justify-between text-[10px] font-bold text-[var(--color-text-muted)] mb-1">
            <span>{formatTime(currentTime)}</span>
            <span>{formatTime(duration)}</span>
          </div>
          <div className="h-1 w-full bg-slate-200 dark:bg-slate-800 rounded-full overflow-hidden cursor-pointer" onClick={seek}>
            <div className="h-full bg-[var(--color-primary)] rounded-full transition-all duration-150" style={{ width: `${progress}%` }} />
          </div>
        </div>

        {/* Controls */}
        <div className="flex items-center gap-3 text-[var(--color-text-muted)]">
          <button onClick={() => skip(-10)} className="material-symbols-outlined hover:text-[var(--color-primary)] transition-colors text-xl">fast_rewind</button>
          <button onClick={() => skip(10)} className="material-symbols-outlined hover:text-[var(--color-primary)] transition-colors text-xl">fast_forward</button>
          <button onClick={() => { const v = volume > 0 ? 0 : 1; setVolume(v); if (audioRef.current) audioRef.current.volume = v; }}
            className="material-symbols-outlined hover:text-[var(--color-primary)] transition-colors text-xl">
            {volume > 0 ? 'volume_up' : 'volume_off'}
          </button>
        </div>
      </div>
    </div>
  );
}
```

- [ ] **Step 2: Integrate AudioPlayer into MeetingDetailClient**

**Important:** `MeetingDetail` has `audioKey?: string` (S3 key like `audio/{userId}/{meetingId}/recording.webm`) but no `audioUrl`. We need to derive the URL using the uploads presigned URL API or construct from the S3 bucket.

Add audio URL derivation and player at the bottom of the main content area:

```tsx
import { AudioPlayer } from '@/components/AudioPlayer';
import { uploadsApi } from '@/lib/api';

// Inside component, derive audio URL from audioKey:
const [audioUrl, setAudioUrl] = useState<string | null>(null);
useEffect(() => {
  if (meeting?.audioKey && meeting.status === 'done') {
    // Use the existing attachments or construct a read URL
    // Option: find the audio attachment from meeting.attachments
    const audioAttachment = meeting.attachments?.find(a => a.type === 'audio');
    if (audioAttachment) setAudioUrl(audioAttachment.url);
  }
}, [meeting?.audioKey, meeting?.status, meeting?.attachments]);

// In JSX, at bottom of main content:
{audioUrl && <AudioPlayer audioUrl={audioUrl} />}
```

- [ ] **Step 3: Verify build succeeds**

Run: `cd /home/ec2-user/ttobak/frontend && npm run build`

- [ ] **Step 4: Commit**

```bash
git add frontend/src/components/AudioPlayer.tsx frontend/src/app/meeting/
git commit -m "feat: add floating audio player to meeting detail page"
```

---

## Task 4: Live Q&A Intuitiveness Improvements (URGENT)

**Files:**
- Create: `frontend/src/components/qa/QAChatMessage.tsx`
- Create: `frontend/src/components/qa/QASuggestedQuestions.tsx`
- Create: `frontend/src/components/qa/QAEmptyState.tsx`
- Modify: `frontend/src/components/LiveQAPanel.tsx`
- Modify: `frontend/src/components/QAPanel.tsx`

Current issues:
1. Empty state is generic — doesn't guide the user
2. Detected questions appear as small chips that are easy to miss
3. No typing/thinking animation beyond a simple spinner
4. Duplicated code between `QAPanel` and `LiveQAPanel` (~100 lines of identical rendering)
5. Tool badges and source rendering duplicated

- [ ] **Step 1: Create shared QAChatMessage component**

Extracts the duplicated question/answer bubble rendering from both panels.

```tsx
// frontend/src/components/qa/QAChatMessage.tsx
'use client';

const TOOL_LABELS: Record<string, { label: string; color: string }> = {
  search_knowledge_base: { label: 'KB 검색', color: 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-400' },
  search_aws_docs: { label: 'AWS Docs', color: 'bg-blue-100 text-blue-700 dark:bg-blue-900/30 dark:text-blue-400' },
  search_transcript: { label: '회의록 검색', color: 'bg-violet-100 text-violet-700 dark:bg-violet-900/30 dark:text-violet-400' },
  get_aws_recommendation: { label: 'AWS 추천', color: 'bg-amber-100 text-amber-700 dark:bg-amber-900/30 dark:text-amber-400' },
};

interface QAChatMessageProps {
  question: string;
  answer: string;
  sources?: string[];
  usedKB?: boolean;
  usedDocs?: boolean;
  toolsUsed?: string[];
}

export function QAChatMessage({ question, answer, sources, usedKB, usedDocs, toolsUsed }: QAChatMessageProps) {
  return (
    <div className="space-y-3 animate-fade-in">
      {/* Question bubble */}
      <div className="flex justify-end">
        <div className="bg-[var(--color-primary)]/10 rounded-2xl rounded-tr-sm px-4 py-2.5 max-w-[85%]">
          <p className="text-sm text-[var(--color-text-primary)]">{question}</p>
        </div>
      </div>

      {/* Answer bubble */}
      <div className="flex justify-start gap-2">
        <div className="size-6 rounded-full bg-[var(--color-primary)]/10 flex items-center justify-center shrink-0 mt-1">
          <span className="material-symbols-outlined text-[var(--color-primary)] text-sm">auto_awesome</span>
        </div>
        <div className="bg-[var(--color-surface)] border border-[var(--color-border)] rounded-2xl rounded-tl-sm px-4 py-2.5 max-w-[85%]">
          {answer ? (
            <>
              <p className="text-sm text-[var(--color-text-secondary)] whitespace-pre-wrap leading-relaxed">{answer}</p>
              {/* Tool badges */}
              {toolsUsed && toolsUsed.length > 0 && (
                <div className="flex flex-wrap gap-1 mt-2">
                  {toolsUsed.map((tool) => {
                    const info = TOOL_LABELS[tool];
                    if (!info) return null;
                    return <span key={tool} className={`inline-block text-[10px] px-1.5 py-0.5 rounded-full font-medium ${info.color}`}>{info.label}</span>;
                  })}
                </div>
              )}
              {(!toolsUsed || toolsUsed.length === 0) && (usedKB || usedDocs) && (
                <div className="flex gap-1 mt-2">
                  {usedKB && <span className="inline-block text-[10px] px-1.5 py-0.5 rounded-full font-medium bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-400">KB 참조</span>}
                  {usedDocs && <span className="inline-block text-[10px] px-1.5 py-0.5 rounded-full font-medium bg-blue-100 text-blue-700 dark:bg-blue-900/30 dark:text-blue-400">AWS Docs</span>}
                </div>
              )}
              {sources && sources.length > 0 && (
                <div className="mt-3 pt-2 border-t border-[var(--color-border)]">
                  <p className="text-[10px] font-semibold text-[var(--color-text-muted)] uppercase mb-1.5">Sources</p>
                  <div className="flex flex-wrap gap-1.5">
                    {sources.map((source, idx) => (
                      source.startsWith('http') ? (
                        <a key={idx} href={source} target="_blank" rel="noopener noreferrer"
                          className="text-[10px] px-2 py-1 bg-blue-50 dark:bg-blue-900/20 text-blue-600 dark:text-blue-400 rounded-full hover:underline">
                          {new URL(source).hostname}
                        </a>
                      ) : (
                        <span key={idx} className="text-[10px] px-2 py-1 bg-slate-100 dark:bg-slate-700 rounded-full text-[var(--color-text-secondary)]">{source}</span>
                      )
                    ))}
                  </div>
                </div>
              )}
            </>
          ) : (
            <div className="flex items-center gap-2 py-1">
              <div className="flex gap-1">
                <div className="w-2 h-2 rounded-full bg-[var(--color-primary)] animate-bounce" style={{ animationDelay: '0ms' }} />
                <div className="w-2 h-2 rounded-full bg-[var(--color-primary)] animate-bounce" style={{ animationDelay: '150ms' }} />
                <div className="w-2 h-2 rounded-full bg-[var(--color-primary)] animate-bounce" style={{ animationDelay: '300ms' }} />
              </div>
              <span className="text-xs text-[var(--color-text-muted)]">답변을 생성하고 있어요...</span>
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
```

- [ ] **Step 2: Create QASuggestedQuestions component**

Makes detected questions more prominent with animation and visual hierarchy.

```tsx
// frontend/src/components/qa/QASuggestedQuestions.tsx
'use client';

interface QASuggestedQuestionsProps {
  questions: string[];
  isDetected?: boolean; // true = AI detected, false = static suggestions
  onAsk: (question: string) => void;
  disabled?: boolean;
}

export function QASuggestedQuestions({ questions, isDetected = false, onAsk, disabled }: QASuggestedQuestionsProps) {
  if (questions.length === 0) return null;

  return (
    <div className={`flex flex-col gap-2 ${isDetected ? 'animate-fade-in' : ''}`}>
      {isDetected && (
        <div className="flex items-center gap-1.5 mb-1">
          <span className="material-symbols-outlined text-amber-500 text-sm animate-pulse">psychology</span>
          <span className="text-[10px] font-bold text-amber-600 dark:text-amber-400 uppercase tracking-wider">AI가 감지한 질문</span>
        </div>
      )}
      {questions.map((q) => (
        <button key={q} onClick={() => onAsk(q)} disabled={disabled}
          className={`text-left text-sm px-4 py-3 rounded-xl transition-all disabled:opacity-50 disabled:cursor-not-allowed ${
            isDetected
              ? 'bg-amber-50 dark:bg-amber-900/20 text-amber-800 dark:text-amber-300 border border-amber-200 dark:border-amber-800 hover:bg-amber-100 dark:hover:bg-amber-900/40 hover:scale-[1.01] active:scale-[0.99]'
              : 'bg-[var(--color-primary)]/5 text-[var(--color-primary)]/80 hover:bg-[var(--color-primary)]/10 border border-transparent hover:border-[var(--color-primary)]/20'
          }`}>
          <span className="flex items-center gap-2">
            <span className="material-symbols-outlined text-base shrink-0">{isDetected ? 'help' : 'chat_bubble'}</span>
            {q}
          </span>
        </button>
      ))}
    </div>
  );
}
```

- [ ] **Step 3: Create QAEmptyState component**

Better empty state with contextual guidance.

```tsx
// frontend/src/components/qa/QAEmptyState.tsx
'use client';

interface QAEmptyStateProps {
  isLive?: boolean; // during recording vs. post-meeting
}

export function QAEmptyState({ isLive = false }: QAEmptyStateProps) {
  return (
    <div className="flex flex-col items-center justify-center py-12 text-center px-4">
      <div className="size-16 rounded-2xl bg-[var(--color-primary)]/10 flex items-center justify-center mb-4">
        <span className="material-symbols-outlined text-[var(--color-primary)] text-3xl">assistant</span>
      </div>
      <h4 className="text-base font-bold text-[var(--color-text-primary)] mb-2">
        {isLive ? 'AI 어시스턴트 준비 완료' : 'Ask about this meeting'}
      </h4>
      <p className="text-sm text-[var(--color-text-muted)] max-w-xs leading-relaxed">
        {isLive
          ? '미팅 중 궁금한 점을 물어보세요. AI가 회의 내용과 지식 베이스를 참고하여 답변합니다.'
          : '회의 내용에 대해 질문하세요. 요약, 액션 아이템, 특정 논의 사항 등을 물어볼 수 있습니다.'}
      </p>
    </div>
  );
}
```

- [ ] **Step 4: Refactor LiveQAPanel to use shared components**

Replace inline rendering in `LiveQAPanel.tsx` with the new shared components. Remove duplicated `TOOL_LABELS`, message rendering logic. Use `QAEmptyState` + `QASuggestedQuestions` for empty state. Use `QAChatMessage` for history entries.

- [ ] **Step 5: Refactor QAPanel to use shared components**

Same treatment for the post-meeting `QAPanel.tsx`.

- [ ] **Step 6: Verify build succeeds**

Run: `cd /home/ec2-user/ttobak/frontend && npm run build`

- [ ] **Step 7: Commit**

```bash
git add frontend/src/components/qa/ frontend/src/components/LiveQAPanel.tsx frontend/src/components/QAPanel.tsx
git commit -m "feat: improve Q&A UX with shared components, better empty state, typing indicator"
```

---

## Task 5: Record Page Refactoring

**Files:**
- Create: `frontend/src/components/record/RecordingConfig.tsx`
- Create: `frontend/src/components/record/PostRecordingBanner.tsx`
- Modify: `frontend/src/app/record/page.tsx` (847 lines -> ~250 lines)

The record page is a 847-line monolith. Extract 3 logical sections into focused components while keeping the orchestration logic in the page.

- [ ] **Step 1: Create RecordingConfig component**

Extracts the header controls: summary interval selector, translation toggle, language picker.

```tsx
// frontend/src/components/record/RecordingConfig.tsx
'use client';

interface RecordingConfigProps {
  summaryInterval: number;
  onSummaryIntervalChange: (val: number) => void;
  translationEnabled: boolean;
  onTranslationToggle: (enabled: boolean) => void;
  targetLang: string;
  onTargetLangChange: (lang: string) => void;
  isRecording: boolean;
  sttProvider: 'transcribe' | 'nova-sonic';
  onSttProviderChange: (provider: 'transcribe' | 'nova-sonic') => void;
}

export function RecordingConfig({
  summaryInterval, onSummaryIntervalChange,
  translationEnabled, onTranslationToggle,
  targetLang, onTargetLangChange,
  isRecording, sttProvider, onSttProviderChange,
}: RecordingConfigProps) {
  return (
    <div className="flex items-center gap-2">
      <select value={summaryInterval} onChange={(e) => onSummaryIntervalChange(Number(e.target.value))}
        className="text-sm bg-slate-100 dark:bg-slate-800 border-none rounded-lg px-3 py-1.5 text-[var(--color-text-secondary)] focus:outline-none focus:ring-2 focus:ring-[var(--color-primary)]/30">
        <option value={100}>100w</option>
        <option value={200}>200w</option>
        <option value={500}>500w</option>
        <option value={1000}>1000w</option>
      </select>
      <label className="flex items-center gap-1.5 text-sm text-[var(--color-text-secondary)]">
        <input type="checkbox" checked={translationEnabled} onChange={(e) => onTranslationToggle(e.target.checked)}
          className="rounded border-slate-300 text-[var(--color-primary)] focus:ring-[var(--color-primary)]" />
        번역
      </label>
      {translationEnabled && (
        <select value={targetLang} onChange={(e) => onTargetLangChange(e.target.value)}
          className="text-sm bg-slate-100 dark:bg-slate-800 border-none rounded-lg px-3 py-1.5 text-[var(--color-text-secondary)] focus:outline-none focus:ring-2 focus:ring-[var(--color-primary)]/30">
          <option value="en">EN</option>
          <option value="ja">JA</option>
          <option value="zh">ZH</option>
          <option value="es">ES</option>
          <option value="fr">FR</option>
          <option value="de">DE</option>
        </select>
      )}
    </div>
  );
}
```

- [ ] **Step 2: Create PostRecordingBanner component**

Extracts the post-recording toast banner (lines 752-798).

```tsx
// frontend/src/components/record/PostRecordingBanner.tsx
'use client';

type PostRecordingStep = 'creating' | 'saving' | 'uploading' | 'redirecting' | 'error';

interface PostRecordingBannerProps {
  step: PostRecordingStep;
  errorMessage?: string | null;
  onRetry: () => void;
  onDismiss: () => void;
}

const STEP_LABELS: Record<string, string> = {
  creating: 'Creating meeting...',
  saving: 'Saving transcript...',
  uploading: 'Uploading audio...',
  redirecting: 'Opening meeting...',
};

export function PostRecordingBanner({ step, errorMessage, onRetry, onDismiss }: PostRecordingBannerProps) {
  const isError = step === 'error';

  return (
    <div className="fixed top-[64px] left-0 right-0 z-40 mx-4 mt-2 animate-slide-up">
      <div className={`rounded-xl shadow-lg px-4 py-3 flex items-center gap-3 ${
        isError
          ? 'bg-red-50 dark:bg-red-900/30 border border-red-200 dark:border-red-800'
          : 'bg-[var(--color-surface)] border border-[var(--color-border)]'
      }`}>
        {isError ? (
          <>
            <span className="material-symbols-outlined text-red-500">error</span>
            <p className="flex-1 text-sm text-red-700 dark:text-red-300 truncate">{errorMessage || 'An unexpected error occurred.'}</p>
            <button onClick={onRetry}
              className="px-3 py-1.5 rounded-lg text-xs font-medium border border-red-200 dark:border-red-700 text-red-600 dark:text-red-400 hover:bg-red-100 dark:hover:bg-red-900/40 transition-colors shrink-0">
              Try Again
            </button>
            <button onClick={onDismiss}
              className="px-3 py-1.5 rounded-lg text-xs font-medium bg-[var(--color-primary)] text-white hover:bg-[var(--color-primary-hover)] transition-colors shrink-0">
              Home
            </button>
          </>
        ) : (
          <>
            <div className="animate-spin rounded-full h-5 w-5 border-2 border-[var(--color-primary)] border-t-transparent shrink-0" />
            <p className="flex-1 text-sm font-medium text-[var(--color-text-primary)]">{STEP_LABELS[step]}</p>
            <button onClick={onDismiss}
              className="p-1.5 hover:bg-slate-100 dark:hover:bg-slate-700 rounded-md transition-colors shrink-0" title="Dismiss">
              <span className="material-symbols-outlined text-[var(--color-text-muted)] text-lg">close</span>
            </button>
          </>
        )}
      </div>
    </div>
  );
}
```

- [ ] **Step 3: Refactor record/page.tsx to use extracted components**

Replace inline JSX with `<RecordingConfig>` and `<PostRecordingBanner>`. Keep the orchestration state and callbacks in the page. The orchestration logic alone is ~400 lines, so realistic target is ~400-500 lines (down from 847). The key win is readability — JSX becomes a clean composition of named components.

- [ ] **Step 4: Verify build succeeds**

Run: `cd /home/ec2-user/ttobak/frontend && npm run build`

- [ ] **Step 5: Commit**

```bash
git add frontend/src/components/record/ frontend/src/app/record/page.tsx
git commit -m "refactor: decompose record page into focused components"
```

---

## Task 6: Attachment Gallery & Global Design Polish

**Files:**
- Modify: `frontend/src/components/AttachmentGallery.tsx`
- Modify: `frontend/src/app/globals.css`

- [ ] **Step 1: Polish AttachmentGallery hover effects**

The gallery already has hover zoom (`group-hover:scale-105`) and gradient overlay — it's close to the design sample. Add smooth transition timing:

In `AttachmentGallery.tsx`, update `AttachmentCard`:
- Add `transition-all duration-300` to the container
- Add `hover:shadow-lg` for elevation on hover
- Ensure gradient overlay uses `duration-300` for smoother fade

- [ ] **Step 2: Add notion-style divider and typography classes**

In `globals.css`, ensure these utility classes exist:

```css
.notion-title {
  font-size: 2.25rem;
  font-weight: 900;
  letter-spacing: -0.025em;
  line-height: 1.1;
  color: var(--color-text-primary);
}
.notion-subheading {
  font-size: 1rem;
  font-weight: 700;
  color: var(--color-text-primary);
}
.notion-divider {
  border: none;
  height: 1px;
  background: var(--color-border);
}
```

- [ ] **Step 3: Audit and fix inconsistent dark mode classes**

Search for hardcoded `text-slate-900 dark:text-slate-100` patterns in modified files and replace with `text-[var(--color-text-primary)]`. Do NOT touch files that weren't modified in this plan.

- [ ] **Step 4: Verify build and lint**

Run: `cd /home/ec2-user/ttobak/frontend && npm run build && npm run lint`

- [ ] **Step 5: Commit**

```bash
git add frontend/src/components/AttachmentGallery.tsx frontend/src/app/globals.css
git commit -m "style: polish attachment gallery transitions and global design tokens"
```

---

## Dependency Graph

```
Task 1 (Design Tokens) ──┬──> Task 2 (Meeting Detail)
                          ├──> Task 3 (Audio Player) ──> depends on Task 2
                          ├──> Task 4 (Q&A UX)
                          ├──> Task 5 (Record Page)
                          └──> Task 6 (Polish)
```

Tasks 2, 4, 5, 6 can run in parallel after Task 1. Task 3 integrates into the Meeting Detail page so it follows Task 2.

## Verification Checklist

After all tasks:
- [ ] `npm run build` succeeds with zero errors
- [ ] `npm run lint` passes
- [ ] Meeting detail page shows 2-column grid (Summary + Action Items) on desktop
- [ ] Audio player appears for completed meetings with audio
- [ ] Q&A panels show improved empty state, typing indicator, shared components
- [ ] Record page is < 300 lines
- [ ] Dark mode renders consistently across all modified pages
- [ ] Mobile layout remains functional (single column, bottom nav, FAB)
