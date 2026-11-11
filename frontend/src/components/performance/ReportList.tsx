'use client';

import type { ReportSummaryRow } from '@/lib/performanceReport';

/** The owner's stored reports, newest first. Every string is a text node. */
export function ReportList({ rows, truncated, selectedKey, onSelect }: {
  rows: ReportSummaryRow[];
  truncated: boolean;
  selectedKey: string | null;
  onSelect: (periodKey: string) => void;
}) {
  if (rows.length === 0) {
    return <p className="text-sm text-slate-500 dark:text-text-muted">저장된 리포트가 없어요.</p>;
  }
  return (
    <div>
      <ul className="space-y-1" aria-label="저장된 리포트">
        {rows.map(row => {
          const selected = row.periodKey === selectedKey;
          return (
            <li key={row.periodKey}>
              <button type="button" aria-current={selected ? 'true' : undefined} onClick={() => onSelect(row.periodKey)}
                className={`w-full rounded-lg px-3 py-2 text-left text-sm ${selected
                  ? 'bg-primary/10 text-primary'
                  : 'text-slate-700 hover:bg-slate-50 dark:text-text-main dark:hover:bg-white/5'}`}>
                <span className="block font-semibold break-words">{row.label}</span>
                <span className="mt-0.5 flex flex-wrap items-center gap-1.5 text-xs text-slate-500 dark:text-text-muted">
                  <span>{row.kindLabel}</span>
                  {row.generatedText && <span>· {row.generatedText}</span>}
                  {row.partial && (
                    <span className="rounded bg-amber-100 px-1.5 py-0.5 font-semibold text-amber-800 dark:bg-amber-400/15 dark:text-amber-300">일부</span>
                  )}
                </span>
              </button>
            </li>
          );
        })}
      </ul>
      {truncated && <p className="mt-2 text-xs text-slate-500 dark:text-text-muted">최근 리포트만 표시해요.</p>}
    </div>
  );
}
