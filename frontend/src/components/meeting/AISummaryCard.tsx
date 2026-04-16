'use client';

/**
 * Simple markdown renderer — same approach as LiveSummary.tsx.
 * Content is server-generated (Bedrock Claude), not user-supplied.
 */
function renderMarkdown(md: string): string {
  // Phase 1: Extract and render tables before line-level processing
  const tableRegex = /^(\|.+\|)\n(\|[-: |]+\|)\n((?:\|.+\|\n?)+)/gm;
  let processed = md.replace(tableRegex, (_match, headerRow: string, _separator: string, bodyRows: string) => {
    const headers = headerRow.split('|').filter((c: string) => c.trim()).map((c: string) => c.trim());
    const rows = bodyRows.trim().split('\n').map((row: string) =>
      row.split('|').filter((c: string) => c.trim()).map((c: string) => c.trim())
    );
    const th = headers.map((h: string) => `<th class="px-3 py-2 text-left text-xs font-semibold">${h}</th>`).join('');
    const tbody = rows.map((cols: string[]) =>
      '<tr>' + cols.map((c: string) => `<td class="px-3 py-2 text-sm">${c}</td>`).join('') + '</tr>'
    ).join('');
    return `<div class="overflow-x-auto my-4"><table class="w-full border-collapse border border-slate-200 dark:border-white/10 rounded-lg text-sm"><thead class="bg-slate-50 dark:bg-white/5"><tr>${th}</tr></thead><tbody class="divide-y divide-slate-200 dark:divide-white/10">${tbody}</tbody></table></div>`;
  });

  // Phase 2: Line-level replacements
  return processed
    .replace(/^#### (.+)$/gm, '<h4 class="text-sm font-bold mt-3 mb-1.5">$1</h4>')
    .replace(/^### (.+)$/gm, '<h3 class="text-base font-bold mt-4 mb-2">$1</h3>')
    .replace(/^## (.+)$/gm, '<h2 class="text-lg font-bold mt-4 mb-2">$1</h2>')
    .replace(/^# (.+)$/gm, '<h1 class="text-xl font-bold mt-4 mb-2">$1</h1>')
    .replace(/\*\*(.+?)\*\*/g, '<strong>$1</strong>')
    .replace(/^- \[ \] (.+)$/gm, '<div class="flex items-start gap-2"><input type="checkbox" disabled class="mt-1"><span>$1</span></div>')
    .replace(/^- \[x\] (.+)$/gm, '<div class="flex items-start gap-2"><input type="checkbox" checked disabled class="mt-1"><span class="line-through text-slate-400">$1</span></div>')
    .replace(/^- (.+)$/gm, '<li class="ml-4">$1</li>')
    .replace(/\n\n/g, '<br/><br/>')
    .replace(/\n/g, '<br/>');
}

interface AISummaryCardProps {
  content?: string;
  summary?: string;
  transcriptA?: string;
}

export function AISummaryCard({ content, summary, transcriptA }: AISummaryCardProps) {
  const rawText = content || summary || '';

  return (
    <div className="bg-white dark:bg-[#0e0e13] glass-panel rounded-xl p-6 shadow-sm dark:border-l-4 dark:border-l-accent">
      <div className="flex items-center gap-2 mb-4 text-primary dark:text-[#B026FF]">
        <span className="material-symbols-outlined">auto_awesome</span>
        <h3 className="font-bold dark:font-[var(--font-headline)]">AI Summary</h3>
      </div>
      {rawText ? (
        <div className="ai-summary-prose prose prose-sm dark:prose-invert max-w-none text-slate-600 dark:text-[#BAC9CC] dark:font-[var(--font-body)] leading-relaxed"
          dangerouslySetInnerHTML={{ __html: renderMarkdown(rawText) }} />
      ) : (
        <div className="text-slate-600 dark:text-[#BAC9CC] dark:font-[var(--font-body)] leading-relaxed">요약이 없습니다.</div>
      )}

      {/* Collapsible raw transcript */}
      {transcriptA && (
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
