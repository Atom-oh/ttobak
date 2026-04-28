'use client';

import { useState, useCallback } from 'react';
import dynamic from 'next/dynamic';
import { MarkdownRenderer } from '@/components/markdown/MarkdownRenderer';

const MeetingEditor = dynamic(() => import('../MeetingEditor').then(m => ({ default: m.MeetingEditor })), {
  loading: () => <div className="animate-pulse bg-slate-100 dark:bg-slate-800 rounded-xl h-64" />,
});

function markdownToHtml(md: string): string {
  return md
    .replace(/^### (.+)$/gm, '<h3>$1</h3>')
    .replace(/^## (.+)$/gm, '<h2>$1</h2>')
    .replace(/^# (.+)$/gm, '<h1>$1</h1>')
    .replace(/\*\*(.+?)\*\*/g, '<strong>$1</strong>')
    .replace(/^- (.+)$/gm, '<li>$1</li>')
    .replace(/(<li>.*<\/li>\n?)+/gm, (m) => `<ul>${m}</ul>`)
    .replace(/\n\n/g, '</p><p>')
    .replace(/\n/g, '<br/>');
}

function isHtml(text: string): boolean {
  return /^\s*<[a-z][\s\S]*>/i.test(text);
}

interface AISummaryCardProps {
  content?: string;
  summary?: string;
  transcriptA?: string;
  onSave?: (content: string) => Promise<void>;
}

export function AISummaryCard({ content, summary, transcriptA, onSave }: AISummaryCardProps) {
  const rawText = content || summary || '';
  const contentIsHtml = isHtml(rawText);
  const [editing, setEditing] = useState(false);
  const [saving, setSaving] = useState(false);
  const [savedAt, setSavedAt] = useState<string | null>(null);

  const handleAutoSave = useCallback(async (html: string) => {
    if (!onSave) return;
    setSaving(true);
    try {
      await onSave(html);
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
          content={contentIsHtml ? rawText : markdownToHtml(rawText)}
          onAutoSave={handleAutoSave}
          autoSaveDelay={3000}
        />
      ) : rawText ? (
        contentIsHtml ? (
          /* Content is TipTap-generated HTML (trusted — from our own editor, not user-supplied raw HTML) */
          <div className="ai-summary-prose prose prose-sm dark:prose-invert max-w-none text-slate-600 dark:text-[#BAC9CC] leading-relaxed"
            dangerouslySetInnerHTML={{ __html: rawText }} />
        ) : (
          <div className="ai-summary-prose">
            <MarkdownRenderer content={rawText} />
          </div>
        )
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
