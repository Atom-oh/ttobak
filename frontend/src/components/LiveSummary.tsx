'use client';

import { useEffect, useRef } from 'react';

interface LiveSummaryProps {
  summary: string;
  isGenerating: boolean;
  wordCount: number;
  lastSummaryWordCount: number;
  summaryInterval?: number;
}

export function LiveSummary({ summary, isGenerating, wordCount, lastSummaryWordCount, summaryInterval = 50 }: LiveSummaryProps) {
  const containerRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (containerRef.current) {
      containerRef.current.scrollTop = containerRef.current.scrollHeight;
    }
  }, [summary]);

  const nextThreshold = lastSummaryWordCount + summaryInterval;
  const progress = lastSummaryWordCount > 0
    ? Math.min(100, ((wordCount - lastSummaryWordCount) / summaryInterval) * 100)
    : Math.min(100, (wordCount / summaryInterval) * 100);

  return (
    <div className="bg-white dark:bg-[#0e0e13] glass-panel rounded-xl border border-slate-200 dark:border-white/10">
      {/* Header */}
      <div className="flex items-center gap-3 p-4 border-b border-slate-100 dark:border-white/5">
        <span className="material-symbols-outlined text-primary">auto_awesome</span>
        <h3 className="text-sm font-semibold text-slate-900 dark:text-white dark:font-[var(--font-headline)]">Live Summary</h3>
        <div className="ml-auto flex items-center gap-2">
          {isGenerating && (
            <div className="flex items-center gap-1.5 bg-primary/10 px-2.5 py-1 rounded-full border border-primary/20">
              <div className="animate-spin rounded-full h-3 w-3 border border-primary border-t-transparent" />
              <span className="text-xs text-primary font-bold uppercase tracking-wider">Updating</span>
            </div>
          )}
          <span className="text-xs text-slate-500 dark:text-[#8B8D98] bg-slate-100 dark:bg-white/5 px-2 py-0.5 rounded-full">
            Next: {nextThreshold} words
          </span>
        </div>
      </div>

      {/* Progress bar to next summary */}
      <div className="h-0.5 bg-slate-100 dark:bg-white/5">
        <div
          className="h-full bg-primary/40 transition-all duration-500"
          style={{ width: `${progress}%` }}
        />
      </div>

      {/* Summary Content */}
      <div ref={containerRef} className="p-4 max-h-96 overflow-y-auto">
        {!summary ? (
          <div className="flex flex-col items-center justify-center py-8 text-slate-400 dark:text-[#8B8D98]">
            <span className="material-symbols-outlined text-4xl mb-2">auto_awesome</span>
            <p className="text-sm">Summary will be generated at {summaryInterval.toLocaleString()} words</p>
            <p className="text-xs mt-1">
              {wordCount > 0 ? `${wordCount} / ${summaryInterval.toLocaleString()} words` : 'Waiting for speech...'}
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
    .replace(/^- (.+)$/gm, '<li class="ml-4 dark-summary-item">$1</li>')
    .replace(/\n\n/g, '<br/><br/>')
    .replace(/\n/g, '<br/>');
}
