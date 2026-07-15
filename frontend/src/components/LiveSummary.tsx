'use client';

import { useEffect, useRef } from 'react';
import { MarkdownRenderer } from '@/components/markdown/MarkdownRenderer';

interface LiveSummaryProps {
  summary: string;
  isGenerating: boolean;
  wordCount: number;
  lastSummaryWordCount: number;
  summaryInterval?: number;
  /** Fill parent height (desktop hero mode) */
  fill?: boolean;
}

export function LiveSummary({ summary, isGenerating, wordCount, lastSummaryWordCount, summaryInterval = 50, fill = false }: LiveSummaryProps) {
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
    <div className={`bg-white dark:bg-surface-lowest glass-panel rounded-xl border border-slate-200 dark:border-white/10 ${fill ? 'flex flex-col h-full min-h-0' : ''}`}>
      {/* Header */}
      <div className="flex items-center gap-3 p-4 border-b border-slate-100 dark:border-white/5 shrink-0">
        <span className="material-symbols-outlined text-primary">auto_awesome</span>
        <h3 className="text-sm font-semibold text-slate-900 dark:text-white dark:font-headline">Live Summary</h3>
        <div className="ml-auto flex items-center gap-2">
          {isGenerating && (
            <div className="flex items-center gap-1.5 bg-primary/10 px-2.5 py-1 rounded-full border border-primary/20">
              <div className="animate-spin rounded-full h-3 w-3 border border-primary border-t-transparent" />
              <span className="text-xs text-primary font-bold uppercase tracking-wider">Updating</span>
            </div>
          )}
          <span className="text-xs text-slate-500 dark:text-text-muted bg-slate-100 dark:bg-white/5 px-2 py-0.5 rounded-full">
            Next: {nextThreshold} words
          </span>
        </div>
      </div>

      {/* Progress bar to next summary */}
      <div className="h-0.5 bg-slate-100 dark:bg-white/5 shrink-0">
        <div
          className="h-full bg-primary/40 transition-all duration-500"
          style={{ width: `${progress}%` }}
        />
      </div>

      {/* Summary Content */}
      <div ref={containerRef} className={`p-4 overflow-y-auto ${fill ? 'flex-1 min-h-0' : 'max-h-96'}`}>
        {!summary ? (
          <div className="flex flex-col items-center justify-center py-8 text-slate-400 dark:text-text-muted">
            <span className="material-symbols-outlined text-4xl mb-2">auto_awesome</span>
            <p className="text-sm">Summary will be generated at {summaryInterval.toLocaleString()} words</p>
            <p className="text-xs mt-1">
              {wordCount > 0 ? `${wordCount} / ${summaryInterval.toLocaleString()} words` : 'Waiting for speech...'}
            </p>
          </div>
        ) : (
          <div className="prose prose-sm dark:prose-invert max-w-none">
            {/* Live summary text comes from Bedrock, which is fed by
                transcript audio the meeting participants control — treat it
                as untrusted input and render through the sanitizing
                MarkdownRenderer rather than raw marked+dangerouslySetInnerHTML. */}
            <MarkdownRenderer content={summary} />
          </div>
        )}
      </div>
    </div>
  );
}
