'use client';

interface AISummaryCardProps {
  content?: string;
  summary?: string;
  transcriptA?: string;
}

export function AISummaryCard({ content, summary, transcriptA }: AISummaryCardProps) {
  return (
    <div className="bg-[var(--color-surface)] border border-[var(--color-border)] rounded-xl p-6 shadow-sm">
      <div className="flex items-center gap-2 mb-4 text-[var(--color-primary)]">
        <span className="material-symbols-outlined">auto_awesome</span>
        <h3 className="font-bold">AI Summary</h3>
      </div>
      <div className="prose prose-sm dark:prose-invert max-w-none text-[var(--color-text-secondary)] leading-relaxed whitespace-pre-wrap">
        {content || summary || '요약이 없습니다.'}
      </div>

      {/* Collapsible raw transcript */}
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
