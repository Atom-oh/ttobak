'use client';

import { useEffect, useState } from 'react';
import { performanceApi } from '@/lib/api';
import { formatGeneratedAt, groupActions, REPORT_COPY, type PerformanceReport } from '@/lib/performanceReport';

const list = <T,>(value: T[] | null | undefined): T[] => (Array.isArray(value) ? value : []);

const SEVERITY_CLASS: Record<string, string> = {
  hi: 'bg-red-100 text-red-700 dark:bg-red-400/15 dark:text-red-300',
  mid: 'bg-amber-100 text-amber-800 dark:bg-amber-400/15 dark:text-amber-300',
  lo: 'bg-slate-100 text-slate-600 dark:bg-white/10 dark:text-text-muted',
};

const HIGHLIGHT_LABELS: [keyof PerformanceReport['highlights'], string][] = [
  ['projects', '프로젝트'], ['initiatives', '이니셔티브'], ['channels', '채널'], ['programs', '프로그램'],
];

/**
 * One stored report rendered natively from its validated JSON. Every string is
 * a React text node: no HTML, no Markdown and no links built from content.
 */
export function ReportView({ periodKey, userId, revision, onDeleted }: {
  periodKey: string;
  userId: string;
  /** Changes when the list reloads, so a regenerated report is refetched. */
  revision: string;
  onDeleted: (periodKey: string) => void;
}) {
  const key = `${userId}:${revision}:${periodKey}`;
  const [result, setResult] = useState<{ key: string; data?: PerformanceReport; error?: string } | null>(null);
  const [attempt, setAttempt] = useState(0);
  const [confirm, setConfirm] = useState<{ key: string; deleting: boolean; error?: string } | null>(null);

  useEffect(() => {
    const controller = new AbortController();
    performanceApi.report(periodKey, { signal: controller.signal, expectedUserId: userId })
      .then(data => setResult({ key, data }))
      .catch(() => { if (!controller.signal.aborted) setResult({ key, error: '리포트를 불러오지 못했어요.' }); });
    return () => controller.abort();
  }, [key, periodKey, userId, attempt]);

  const current = result?.key === key ? result : null;
  const confirming = confirm?.key === key ? confirm : null;

  if (current?.error) {
    return (
      <div role="alert" className="flex flex-wrap items-center gap-2 text-sm text-red-600 dark:text-red-400">
        <span>{current.error}</span>
        <button type="button" onClick={() => setAttempt(value => value + 1)} className="font-semibold text-primary hover:underline">다시 불러오기</button>
      </div>
    );
  }
  const report = current?.data;
  if (!report) {
    return <div className="flex justify-center py-8"><div className="animate-spin rounded-full h-8 w-8 border-2 border-primary border-t-transparent" /></div>;
  }

  const remove = async () => {
    setConfirm({ key, deleting: true });
    try {
      await performanceApi.deleteReport(periodKey, { expectedUserId: userId });
      setConfirm(null);
      onDeleted(periodKey);
    } catch {
      setConfirm({ key, deleting: false, error: '리포트를 삭제하지 못했어요.' });
    }
  };

  const missing = list(report.missingSources);
  const highlights = HIGHLIGHT_LABELS.flatMap(([field, label]) => {
    const items = list(report.highlights?.[field]);
    return items.length ? [{ field, label, items }] : [];
  });
  const actionGroups = groupActions(report.actions);
  const dataBasis = list(report.dataBasis);
  const generated = formatGeneratedAt(report.generatedAt);

  return (
    <article className="space-y-6 text-sm text-slate-700 dark:text-text-main">
      <header className="space-y-2">
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="min-w-0">
            <h4 className="text-xl font-bold text-slate-900 dark:text-text-main break-words">{report.period?.label}</h4>
            <p className="mt-1 text-xs text-slate-500 dark:text-text-muted">
              {report.period?.start} ~ {report.period?.end}
              {generated && <> · 생성 {generated}</>}
              {report.model && <> · {report.model}</>}
            </p>
          </div>
          {!confirming && (
            <button type="button" onClick={() => setConfirm({ key, deleting: false })}
              className="rounded-lg border border-slate-200 px-3 py-1.5 text-xs font-semibold text-slate-600 hover:border-red-300 hover:text-red-600 dark:border-white/10 dark:text-text-muted dark:hover:text-red-400">
              삭제
            </button>
          )}
        </div>
        {confirming && (
          <div role="group" aria-label="리포트 삭제 확인"
            className="flex flex-wrap items-center gap-2 rounded-lg border border-red-200 bg-red-50 px-3 py-2 text-xs text-red-700 dark:border-red-400/30 dark:bg-red-400/10 dark:text-red-300">
            <span>이 리포트를 삭제할까요? 되돌릴 수 없어요.</span>
            <button type="button" disabled={confirming.deleting} onClick={() => void remove()}
              className="rounded-md bg-red-600 px-2.5 py-1 font-semibold text-white hover:bg-red-700 disabled:opacity-50">
              {confirming.deleting ? '삭제 중…' : '삭제'}
            </button>
            <button type="button" disabled={confirming.deleting} onClick={() => setConfirm(null)}
              className="font-semibold hover:underline disabled:opacity-50">{REPORT_COPY.cancel}</button>
            {confirming.error && <span role="alert">{confirming.error}</span>}
          </div>
        )}
        {report.partial && (
          <p className="inline-flex flex-wrap items-center gap-1.5 rounded-lg bg-amber-50 px-3 py-1.5 text-xs text-amber-800 dark:bg-amber-400/10 dark:text-amber-300">
            <span className="font-semibold">일부 소스 누락</span>
            {missing.length > 0 && <span>{missing.join(', ')}</span>}
          </p>
        )}
        {highlights.length > 0 && (
          <dl className="flex flex-wrap gap-x-4 gap-y-1 text-xs text-slate-500 dark:text-text-muted">
            {highlights.map(group => (
              <div key={group.field} className="flex flex-wrap gap-1">
                <dt className="font-semibold">{group.label}</dt>
                <dd>{group.items.join(', ')}</dd>
              </div>
            ))}
          </dl>
        )}
      </header>

      {report.summary && <p className="whitespace-pre-line leading-relaxed">{report.summary}</p>}

      {list(report.ledger).length > 0 && (
        <ul className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-4">
          {list(report.ledger).map((item, index) => (
            <li key={index} className="rounded-lg border border-slate-200 p-3 dark:border-white/10">
              <p className="text-xs text-slate-500 dark:text-text-muted break-words">{item.label}</p>
              <p className="mt-1 text-lg font-bold text-slate-900 dark:text-text-main break-words">
                {item.value}{item.unit && <span className="ml-0.5 text-xs font-semibold text-slate-500 dark:text-text-muted">{item.unit}</span>}
              </p>
              {item.note && <p className="mt-1 whitespace-pre-line text-xs text-slate-500 dark:text-text-muted">{item.note}</p>}
            </li>
          ))}
        </ul>
      )}

      {list(report.sections).map(section => (
        <section key={section.id} className="space-y-3">
          <h5 className="text-base font-bold text-slate-900 dark:text-text-main break-words">{section.title}</h5>
          {section.lede && <p className="whitespace-pre-line text-slate-600 dark:text-text-muted">{section.lede}</p>}
          {list(section.paragraphs).map((paragraph, index) => (
            <p key={index} className="whitespace-pre-line leading-relaxed">{paragraph}</p>
          ))}
          {list(section.tables).map((table, tableIndex) => (
            <div key={tableIndex} className="overflow-x-auto rounded-lg border border-slate-200 dark:border-white/10">
              <table className="min-w-full text-left text-xs">
                <thead className="bg-slate-50 dark:bg-white/5">
                  <tr>{list(table.columns).map((column, index) => <th key={index} scope="col" className="px-3 py-2 font-semibold">{column}</th>)}</tr>
                </thead>
                <tbody>
                  {list(table.rows).map((row, rowIndex) => (
                    <tr key={rowIndex} className="border-t border-slate-100 dark:border-white/5">
                      {list(row).map((cell, index) => <td key={index} className="whitespace-pre-line px-3 py-2 align-top">{cell}</td>)}
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ))}
          {list(section.cards).length > 0 && (
            <ul className="grid gap-3 sm:grid-cols-2">
              {list(section.cards).map((card, index) => (
                <li key={index} className="rounded-lg border border-slate-200 p-3 dark:border-white/10">
                  {card.title && <p className="font-semibold text-slate-900 dark:text-text-main break-words">{card.title}</p>}
                  {card.body && <p className="mt-1 whitespace-pre-line text-slate-600 dark:text-text-muted">{card.body}</p>}
                </li>
              ))}
            </ul>
          )}
        </section>
      ))}

      {actionGroups.length > 0 && (
        <section className="space-y-3">
          <h5 className="text-base font-bold text-slate-900 dark:text-text-main">{REPORT_COPY.actions}</h5>
          {actionGroups.map(group => (
            <div key={group.severity} className="space-y-2">
              <p className="text-xs font-semibold text-slate-500 dark:text-text-muted">{group.label} · {group.items.length}</p>
              <ul className="space-y-2">
                {group.items.map((action, index) => (
                  <li key={index} className="rounded-lg border border-slate-200 p-3 dark:border-white/10">
                    <p className="flex flex-wrap items-center gap-2">
                      <span className={`rounded px-1.5 py-0.5 text-xs font-semibold ${SEVERITY_CLASS[group.severity]}`}>{group.label}</span>
                      <span className="font-semibold text-slate-900 dark:text-text-main break-words">{action.title}</span>
                    </p>
                    {action.detail && <p className="mt-1 whitespace-pre-line text-slate-600 dark:text-text-muted">{action.detail}</p>}
                    {action.source && <p className="mt-1 text-xs text-slate-500 dark:text-text-muted">출처: {action.source}</p>}
                  </li>
                ))}
              </ul>
            </div>
          ))}
        </section>
      )}

      {dataBasis.length > 0 && (
        <details className="rounded-lg border border-slate-200 p-3 dark:border-white/10">
          <summary className="cursor-pointer text-sm font-semibold text-slate-900 dark:text-text-main">{REPORT_COPY.dataBasis}</summary>
          <dl className="mt-3 space-y-3 text-xs">
            {dataBasis.map((basis, index) => (
              <div key={index} className="space-y-1">
                <dt className="font-semibold text-slate-900 dark:text-text-main break-words">{basis.source}</dt>
                {basis.query && <dd className="whitespace-pre-line text-slate-600 dark:text-text-muted">조회: {basis.query}</dd>}
                {basis.exclusions && <dd className="whitespace-pre-line text-slate-600 dark:text-text-muted">제외: {basis.exclusions}</dd>}
                {basis.estimates && <dd className="whitespace-pre-line text-slate-600 dark:text-text-muted">추정: {basis.estimates}</dd>}
              </div>
            ))}
          </dl>
        </details>
      )}
    </article>
  );
}
