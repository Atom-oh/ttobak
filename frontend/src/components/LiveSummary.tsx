'use client';

import { useEffect, useRef } from 'react';

interface LiveSummaryProps {
  summary: string;
  isGenerating: boolean;
  wordCount: number;
  lastSummaryWordCount: number;
}

export function LiveSummary({ summary, isGenerating, wordCount, lastSummaryWordCount }: LiveSummaryProps) {
  const containerRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (containerRef.current) {
      containerRef.current.scrollTop = containerRef.current.scrollHeight;
    }
  }, [summary]);

  const nextThreshold = lastSummaryWordCount + 1000;
  const progress = lastSummaryWordCount > 0
    ? Math.min(100, ((wordCount - lastSummaryWordCount) / 1000) * 100)
    : Math.min(100, (wordCount / 1000) * 100);

  return (
    <div className="bg-white dark:bg-slate-900 rounded-xl border border-slate-200 dark:border-slate-800">
      {/* Header */}
      <div className="flex items-center gap-3 p-4 border-b border-slate-100 dark:border-slate-800">
        <span className="material-symbols-outlined text-primary">summarize</span>
        <h3 className="text-sm font-semibold text-slate-900 dark:text-white">Live Summary</h3>
        <div className="ml-auto flex items-center gap-2">
          {isGenerating && (
            <div className="flex items-center gap-1.5">
              <div className="animate-spin rounded-full h-3 w-3 border border-primary border-t-transparent" />
              <span className="text-xs text-primary font-medium">Generating...</span>
            </div>
          )}
          <span className="text-xs text-slate-500 bg-slate-100 dark:bg-slate-800 px-2 py-0.5 rounded-full">
            Next: {nextThreshold} words
          </span>
        </div>
      </div>

      {/* Progress bar to next summary */}
      <div className="h-0.5 bg-slate-100 dark:bg-slate-800">
        <div
          className="h-full bg-primary/40 transition-all duration-500"
          style={{ width: `${progress}%` }}
        />
      </div>

      {/* Summary Content */}
      <div ref={containerRef} className="p-4 max-h-96 overflow-y-auto">
        {!summary ? (
          <div className="flex flex-col items-center justify-center py-8 text-slate-400">
            <span className="material-symbols-outlined text-4xl mb-2">summarize</span>
            <p className="text-sm">Summary will be generated at 1,000 words</p>
            <p className="text-xs mt-1">
              {wordCount > 0 ? `${wordCount} / 1,000 words` : 'Waiting for speech...'}
            </p>
          </div>
        ) : (
          <div className="prose prose-sm dark:prose-invert max-w-none">
            <div dangerouslySetInnerHTML={{ __html: renderMarkdown(summary) }} />
          </div>
        )}
      </div>
    </div>
  );
}

// Simple markdown renderer (bold, headers, lists, checkboxes)
function renderMarkdown(md: string): string {
  return md
    .replace(/^### (.+)$/gm, '<h3 class="text-base font-bold mt-4 mb-2">$1</h3>')
    .replace(/^## (.+)$/gm, '<h2 class="text-lg font-bold mt-4 mb-2">$1</h2>')
    .replace(/^# (.+)$/gm, '<h1 class="text-xl font-bold mt-4 mb-2">$1</h1>')
    .replace(/\*\*(.+?)\*\*/g, '<strong>$1</strong>')
    .replace(/^- \[ \] (.+)$/gm, '<div class="flex items-start gap-2"><input type="checkbox" disabled class="mt-1"><span>$1</span></div>')
    .replace(/^- \[x\] (.+)$/gm, '<div class="flex items-start gap-2"><input type="checkbox" checked disabled class="mt-1"><span>$1</span></div>')
    .replace(/^- (.+)$/gm, '<li class="ml-4">$1</li>')
    .replace(/\n\n/g, '<br/><br/>')
    .replace(/\n/g, '<br/>');
}
