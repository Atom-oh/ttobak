'use client';

import { useState, useCallback } from 'react';
import dynamic from 'next/dynamic';
import { marked } from 'marked';
import TurndownService from 'turndown';
import { MarkdownRenderer } from '@/components/markdown/MarkdownRenderer';

const MeetingEditor = dynamic(() => import('../MeetingEditor').then(m => ({ default: m.MeetingEditor })), {
  loading: () => <div className="animate-pulse bg-slate-100 dark:bg-slate-800 rounded-xl h-64" />,
});

// TipTap's MeetingEditor has no TaskList/TaskItem extension, so marked's default
// GFM checkbox (`<input type="checkbox">`) gets dropped entirely by the ProseMirror
// schema on edit. Render it as literal `[ ]`/`[x]` text instead so it round-trips.
marked.use({ renderer: { checkbox({ checked }) { return checked ? '[x] ' : '[ ] '; } } });

// The editor loads markdown as HTML (marked.parse above) and emits HTML on
// save (editor.getHTML()). Convert back to markdown before persisting so the
// stored `content` stays markdown — downstream consumers (Notion/Obsidian
// export via markdownToNotionBlocks) parse it as markdown, and would otherwise
// render raw <h1>/<p> tags. atx headings + `-` bullets match the Bedrock
// summary skeleton (`# 회의록`, `- `) the exporter expects.
const turndown = new TurndownService({ headingStyle: 'atx', bulletListMarker: '-' });

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

interface AISummaryCardProps {
  content?: string;
  summary?: string;
  transcriptA?: string;
  onSave?: (content: string) => Promise<void>;
}

export function AISummaryCard({ content, summary, transcriptA, onSave }: AISummaryCardProps) {
  const rawText = content || summary || '';
  const [editing, setEditing] = useState(false);
  const [saving, setSaving] = useState(false);
  const [savedAt, setSavedAt] = useState<string | null>(null);

  const handleAutoSave = useCallback(async (html: string) => {
    if (!onSave) return;
    setSaving(true);
    try {
      await onSave(normalizeMarkdown(turndown.turndown(html)));
      setSavedAt(new Date().toLocaleTimeString('ko-KR', { hour: '2-digit', minute: '2-digit' }));
    } finally {
      setSaving(false);
    }
  }, [onSave]);

  return (
    <div className="bg-white dark:bg-[#0e0e13] glass-panel rounded-xl p-6 shadow-sm dark:border-l-4 dark:border-l-accent">
      <div className="flex items-center gap-2 mb-4">
        <span className="material-symbols-outlined text-primary dark:text-[#B026FF]">auto_awesome</span>
        <h3 className="font-bold dark:font-[var(--font-headline)] text-primary dark:text-[#B026FF]">AI Summary</h3>
        <div className="flex-1" />
        {rawText && onSave && (
          <div className="flex items-center gap-2">
            {saving && <span className="text-xs text-slate-400 animate-pulse">Saving...</span>}
            {savedAt && !saving && <span className="text-xs text-slate-400">Saved {savedAt}</span>}
            <button
              onClick={() => setEditing(!editing)}
              className={`p-1.5 rounded-lg transition-colors ${
                editing
                  ? 'bg-primary/10 text-primary dark:bg-[#00E5FF]/10 dark:text-[#00E5FF]'
                  : 'text-slate-400 hover:text-slate-600 dark:hover:text-[#BAC9CC]'
              }`}
              title={editing ? 'View mode' : 'Edit summary'}
            >
              <span className="material-symbols-outlined text-lg">{editing ? 'visibility' : 'edit'}</span>
            </button>
          </div>
        )}
      </div>

      {editing ? (
        <MeetingEditor
          content={marked.parse(rawText, { async: false }) as string}
          onAutoSave={handleAutoSave}
          autoSaveDelay={3000}
        />
      ) : rawText ? (
        <div className="ai-summary-prose">
          <MarkdownRenderer content={rawText} />
        </div>
      ) : (
        <div className="text-slate-600 dark:text-[#BAC9CC] dark:font-[var(--font-body)] leading-relaxed">요약이 없습니다.</div>
      )}

      {!editing && transcriptA && (
        <details className="mt-6 border border-slate-200 dark:border-white/10 rounded-lg">
          <summary className="px-4 py-3 text-sm font-medium text-slate-600 dark:text-[#849396] cursor-pointer hover:bg-slate-50 dark:hover:bg-white/5 rounded-lg flex items-center gap-2">
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
