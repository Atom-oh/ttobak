'use client';

import { useState, useCallback, useRef } from 'react';
import dynamic from 'next/dynamic';
import { Marked } from 'marked';
import { markdownLinkRenderer } from '@/lib/markdownHtml';
import TurndownService from 'turndown';
import { MarkdownRenderer } from '@/components/markdown/MarkdownRenderer';
import { bindMeetingCitations, restoreMeetingCitations } from '@/lib/meetingCitations';
import { englishFailures, type EnglishNoteView } from '@/lib/englishNote';
import type { EnglishNote } from '@/types/meeting';
import { TranslationControls, TranslationStatus, useMeetingTranslation } from './MeetingTranslation';

const MeetingEditor = dynamic(() => import('../MeetingEditor').then(m => ({ default: m.MeetingEditor })), {
  loading: () => <div className="animate-pulse bg-slate-100 dark:bg-slate-800 rounded-xl h-64" />,
});

// TipTap's MeetingEditor has no TaskList/TaskItem extension, so marked's default
// GFM checkbox (`<input type="checkbox">`) gets dropped entirely by the ProseMirror
// schema on edit. Render it as literal `[ ]`/`[x]` text instead so it round-trips.
// An isolated `Marked` instance (rather than the shared default export) keeps
// this renderer override from leaking into any other component that imports
// `marked` for a different purpose.
const marked = new Marked({ renderer: markdownLinkRenderer });
marked.use({ renderer: { checkbox({ checked }) { return checked ? '[x] ' : '[ ] '; } } });

// The editor loads markdown as HTML (marked.parse above) and emits HTML on
// save (editor.getHTML()). Convert back to markdown before persisting so the
// stored `content` stays markdown — downstream consumers (Notion/Obsidian
// export via markdownToNotionBlocks) parse it as markdown, and would otherwise
// render raw <h1>/<p> tags. atx headings + `-` bullets match the Bedrock
// summary skeleton (`# 회의록`, `- `) the exporter expects.
// hr: '---' keeps horizontal rules as `---` rather than turndown's default
// `* * *`, which normalizeMarkdown below would otherwise rewrite into a bullet.
const turndown = new TurndownService({ headingStyle: 'atx', bulletListMarker: '-', hr: '---' });

// turndown emits "-   " (marker + 3 spaces) and backslash-escapes the literal
// task-checkbox text ("\[ \]") that rides along because TipTap has no TaskList
// extension. Normalize both to the canonical "- [ ] "/"- [x] " the summary
// skeleton uses so stored markdown stays clean. (The backend export parser is
// tolerant of these too, but keeping the source canonical avoids surprises.)
function normalizeMarkdown(md: string): string {
  return md
    .replace(/^(\s*)[-*][ \t]+/gm, '$1- ')
    .replace(/^(\s*- )\\?\[([ xX])\\?\] /gm, '$1[$2] ');
}

const englishBadges = {
  generating: { label: '영문 회의록 생성 중', className: 'bg-primary/10 text-primary animate-pulse' },
  failed: { label: '영문 회의록 생성 실패', className: 'bg-red-50 text-red-700 dark:bg-red-950/40 dark:text-red-300' },
  stale: { label: '원본이 바뀌었어요', className: 'bg-amber-50 text-amber-800 dark:bg-amber-950/40 dark:text-amber-200' },
} as const;
const englishActions = { generate: '영문 회의록 생성', regenerate: '영문 회의록 다시 생성', retry: '다시 시도' } as const;

export interface EnglishNoteControls {
  view: EnglishNoteView;
  note: EnglishNote | null;
  busy: boolean;
  error: string | null;
  onRequest: () => void;
}

interface AISummaryCardProps {
  content?: string;
  summary?: string;
  canonicalContent?: string;
  resolveCitation?: (source: string) => string;
  transcriptA?: string;
  onSave?: (content: string) => Promise<void>;
  onDirtyChange?: (dirty: boolean) => void;
  interactionLocked?: boolean;
  meetingId?: string;
  /** Server-advertised meeting translation flag; hides the control when off. */
  translationEnabled?: boolean;
  /** English note state; absent hides every English control. */
  english?: EnglishNoteControls;
}

export function AISummaryCard({ content, summary, canonicalContent, resolveCitation, transcriptA, onSave, onDirtyChange, interactionLocked = false, meetingId, translationEnabled = false, english }: AISummaryCardProps) {
  const rawText = content || summary || '';
  const translation = useMeetingTranslation(meetingId, 'notes', canonicalContent ?? rawText);
  const renderEditorContent = () => bindMeetingCitations(
    marked.parse(canonicalContent ?? rawText, { async: false }) as string,
    resolveCitation ?? ((source) => source),
  );
  const [editing, setEditing] = useState(false);
  // The selected tab belongs to one meeting only and is never persisted.
  const [tabState, setTabState] = useState<{ meetingId?: string; tab: 'ko' | 'en' }>({ tab: 'ko' });
  const englishContent = english?.note?.content ?? '';
  const tab = tabState.meetingId === meetingId && english?.view.showToggle && englishContent ? tabState.tab : 'ko';
  const showEnglish = tab === 'en' && !editing;
  const [saving, setSaving] = useState(false);
  const [savedAt, setSavedAt] = useState<string | null>(null);
  const latestHTML = useRef('');
  const savedHTML = useRef('');
  const saveInFlight = useRef(false);
  const pendingSave = useRef<string | null>(null);
  const failedSave = useRef(false);
  const [dirty, setDirty] = useState(false);
  const [saveError, setSaveError] = useState<string | null>(null);
  const markDirty = useCallback((value: boolean) => { setDirty(value); onDirtyChange?.(value); }, [onDirtyChange]);
  const isContentSaved = useCallback((html: string) =>
    !saveInFlight.current && !failedSave.current && html === savedHTML.current, []);
  const handleContentApplied = useCallback((html: string) => {
    latestHTML.current = html;
    savedHTML.current = html;
    pendingSave.current = null;
    failedSave.current = false;
    setSaveError(null);
    setSavedAt(null);
    markDirty(false);
  }, [markDirty]);

  const handleAutoSave = useCallback(async () => {
    if (!onSave) return;
    // One request owns the writer; later debounce events share one latest slot.
    pendingSave.current = latestHTML.current;
    if (saveInFlight.current) return;
    if (!failedSave.current && pendingSave.current === savedHTML.current) {
      pendingSave.current = null;
      return;
    }
    saveInFlight.current = true;
    failedSave.current = false;
    markDirty(true);
    setSaving(true);
    setSaveError(null);
    try {
      while (pendingSave.current !== null) {
        const html: string = pendingSave.current;
        pendingSave.current = null;
        await onSave(normalizeMarkdown(turndown.turndown(restoreMeetingCitations(html))));
        savedHTML.current = html;
        setSavedAt(new Date().toLocaleTimeString('ko-KR', { hour: '2-digit', minute: '2-digit' }));
        // An undo may have replaced the queued draft with the just-saved value.
        if (pendingSave.current === html) pendingSave.current = null;
      }
    } catch (error) {
      // Stop on failure. The editor retains the latest text for explicit retry.
      pendingSave.current = null;
      failedSave.current = true;
      setSaveError(error instanceof Error ? error.message : '요약을 저장하지 못했습니다.');
    } finally {
      saveInFlight.current = false;
      setSaving(false);
      markDirty(failedSave.current || latestHTML.current !== savedHTML.current);
    }
  }, [onSave, markDirty]);

  return (
    <div className="bg-white dark:bg-surface-lowest glass-panel rounded-xl p-6 shadow-sm dark:border-l-4 dark:border-l-accent">
      <div className="flex items-center gap-2 mb-4">
        <span className="material-symbols-outlined text-primary dark:text-accent">auto_awesome</span>
        <h3 className="font-bold dark:font-headline text-primary dark:text-accent">AI Summary</h3>
        <div className="flex-1" />
        {english?.view.showToggle && !editing && (
          <div role="group" aria-label="회의록 언어" className="flex rounded-lg border border-slate-200 p-0.5 text-xs dark:border-white/10">
            {([['ko', '한국어'], ['en', 'English']] as const).map(([value, label]) => (
              <button key={value} type="button" aria-pressed={tab === value}
                onClick={() => setTabState({ meetingId, tab: value })}
                className={`rounded-md px-2.5 py-1 font-semibold ${tab === value ? 'bg-primary text-white' : 'text-slate-600 dark:text-text-secondary'}`}>{label}</button>
            ))}
          </div>
        )}
        {translationEnabled && meetingId && rawText && !editing && !showEnglish && <TranslationControls translation={translation} />}
        {rawText && onSave && !showEnglish && (
          <div className="flex items-center gap-2">
            {saving && <span className="text-xs text-slate-400 animate-pulse">Saving...</span>}
            {savedAt && !saving && <span className="text-xs text-slate-400">Saved {savedAt}</span>}
            <button
              onClick={() => {
                if (!editing) { translation.clear(); savedHTML.current = renderEditorContent(); latestHTML.current = savedHTML.current; }
                setEditing(!editing);
              }}
              disabled={interactionLocked || (editing && (dirty || saving))}
              className={`p-1.5 rounded-lg transition-colors ${
                editing
                  ? 'bg-primary/10 text-primary'
                  : 'text-slate-400 hover:text-slate-600 dark:hover:text-text-secondary'
              }`}
              title={editing ? 'View mode' : 'Edit summary'}
            >
              <span className="material-symbols-outlined text-lg">{editing ? 'visibility' : 'edit'}</span>
            </button>
          </div>
        )}
      </div>
      {english && !editing && (english.view.badge || english.view.action || english.error) && (
        <div className="mb-3 flex flex-wrap items-center gap-2 text-xs">
          {english.view.badge && <span role="status" className={`rounded-full px-2 py-0.5 font-semibold ${englishBadges[english.view.badge].className}`}>{englishBadges[english.view.badge].label}</span>}
          {english.view.badge === 'failed' && <span className="text-slate-600 dark:text-text-secondary">{englishFailures[english.note?.errorCode ?? ''] ?? '영문 회의록을 만들지 못했습니다.'}{english.note?.content ? ' 기존 영문 회의록은 유지됩니다.' : ''}</span>}
          {english.view.action && <button type="button" onClick={english.onRequest} disabled={english.busy}
            className="rounded-lg border border-primary/30 px-2.5 py-1 font-semibold text-primary disabled:opacity-40">{englishActions[english.view.action]}</button>}
          {english.error && <span role="alert" className="text-red-700 dark:text-red-300">{english.error}</span>}
        </div>
      )}
      {saveError && <p role="alert" className="mb-3 text-sm text-red-600 dark:text-red-300">{saveError} <button type="button" disabled={saving} onClick={() => { void handleAutoSave(); }} className="underline">저장 다시 시도</button></p>}
      {dirty && !saving && !saveError && <p className="mb-3 text-xs text-slate-500">변경 사항 저장 대기 중…</p>}

      {showEnglish ? (
        <div className="ai-summary-prose" lang="en">
          <MarkdownRenderer content={resolveCitation ? resolveCitation(englishContent) : englishContent} />
        </div>
      ) : editing ? (
        <MeetingEditor
          content={renderEditorContent()}
          preserveMeetingCitations
          readOnly={interactionLocked}
          preserveDraft={dirty || saving}
          onContentApplied={handleContentApplied}
          isContentSaved={isContentSaved}
          onChange={(html) => {
            latestHTML.current = html;
            if (pendingSave.current !== null) pendingSave.current = html;
            markDirty(failedSave.current || saveInFlight.current || html !== savedHTML.current);
          }}
          onAutoSave={handleAutoSave}
          autoSaveDelay={3000}
        />
      ) : rawText && translation.target ? (
        <div>
          {translation.notes && (
            <div className="ai-summary-prose" lang={translation.target}>
              {/* Same sanitized renderer as the source notes; citations resolve the same way. */}
              <MarkdownRenderer content={resolveCitation ? resolveCitation(translation.notes) : translation.notes} />
            </div>
          )}
          <TranslationStatus translation={translation} />
        </div>
      ) : rawText ? (
        <div className="ai-summary-prose">
          <MarkdownRenderer content={rawText} />
        </div>
      ) : (
        <div className="text-slate-600 dark:text-text-secondary dark:font-body leading-relaxed">요약이 없습니다.</div>
      )}

      {!editing && !showEnglish && transcriptA && (
        <details className="mt-6 border border-slate-200 dark:border-white/10 rounded-lg">
          <summary className="px-4 py-3 text-sm font-medium text-slate-600 dark:text-text-muted cursor-pointer hover:bg-slate-50 dark:hover:bg-white/5 rounded-lg flex items-center gap-2">
            <span className="material-symbols-outlined text-lg">notes</span>
            원본 텍스트 보기
          </summary>
          <div className="px-4 pb-4 text-sm text-slate-400 leading-relaxed whitespace-pre-wrap border-t border-slate-200 dark:border-white/10 pt-3">
            {transcriptA}
          </div>
        </details>
      )}
    </div>
  );
}
