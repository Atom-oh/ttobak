'use client';

import { useCallback, useEffect, useMemo, useState } from 'react';
import { useReport } from '@/hooks/useReport';
import { performanceApi } from '@/lib/api';
import { intakeCodeLabel, loadErrorText } from '@/lib/intake';
import { REPORT_COPY, reportFinishedOutcome, reportSummaryRows, type PerformanceReportList, type ReportFinished } from '@/lib/performanceReport';
import { isTauri } from '@/lib/tauri';
import { ReportGenerateDialog } from './ReportGenerateDialog';
import { ReportList } from './ReportList';
import { ReportView } from './ReportView';

const cardClass = 'rounded-xl border border-slate-200 bg-white p-5 dark:border-white/10 dark:bg-surface-lowest';

function Spinner() {
  return <div className="flex justify-center py-8"><div className="animate-spin rounded-full h-8 w-8 border-2 border-primary border-t-transparent" /></div>;
}

/**
 * The report tab of /performance: the owner's stored reports and the selected
 * one. Generation is Mac app only; a browser lists and reads reports.
 */
export function ReportTab({ userId, membershipRevision, reloadKey }: { userId: string; membershipRevision: number; reloadKey: number }) {
  const [listReload, setListReload] = useState(0);
  const [listResult, setListResult] = useState<{ key: string; data?: PerformanceReportList; error?: string } | null>(null);
  const [selected, setSelected] = useState<string | null>(null);
  const [dialogOpen, setDialogOpen] = useState(false);
  const [message, setMessage] = useState('');
  // A deleted report leaves the list at once, before the reload returns.
  const [deleted, setDeleted] = useState<string | null>(null);
  const listKey = `${userId}:${membershipRevision}:${reloadKey}:${listReload}`;

  useEffect(() => {
    const controller = new AbortController();
    performanceApi.reports({ signal: controller.signal, expectedUserId: userId })
      .then(data => setListResult({ key: listKey, data }))
      .catch(() => { if (!controller.signal.aborted) setListResult({ key: listKey, error: '리포트 목록을 불러오지 못했어요.' }); });
    return () => controller.abort();
  }, [listKey, userId]);

  // Keep the previous list visible while a reload runs.
  const ownList = listResult?.key.startsWith(`${userId}:${membershipRevision}:`) ? listResult : null;
  const rows = useMemo(() => reportSummaryRows(ownList?.data).filter(row => row.periodKey !== deleted), [ownList?.data, deleted]);
  const listError = ownList?.key === listKey ? ownList.error ?? null : null;
  const selectedKey = selected && rows.some(row => row.periodKey === selected) ? selected : rows[0]?.periodKey ?? null;

  const onFinished = useCallback((finished: ReportFinished) => {
    const outcome = reportFinishedOutcome(finished, intakeCodeLabel);
    setMessage(outcome.message);
    if (outcome.periodKey) {
      setSelected(outcome.periodKey);
      setDeleted(current => (current === outcome.periodKey ? null : current));
      setListReload(value => value + 1);
      setDialogOpen(false);
    }
  }, []);
  const report = useReport(onFinished);
  const closeDialog = useCallback(() => setDialogOpen(false), []);
  const canGenerate = isTauri() && report.state !== null;

  return (
    <section className={cardClass} aria-labelledby="perf-reports">
      <div className="mb-4 flex flex-wrap items-center justify-between gap-3">
        <h3 id="perf-reports" className="text-lg font-bold text-slate-900 dark:text-text-main dark:font-headline">SA 활동 리포트</h3>
        {canGenerate ? (
          <button type="button" onClick={() => { setDialogOpen(true); }}
            className="rounded-lg bg-primary px-3 py-1.5 text-sm font-semibold text-white hover:opacity-90">
            {report.state?.running ? '리포트 만드는 중…' : REPORT_COPY.makeReport}
          </button>
        ) : !isTauri() ? (
          <p className="text-xs text-slate-500 dark:text-text-muted">Mac 앱에서 리포트를 만들 수 있어요</p>
        ) : report.loadError ? (
          <p role="alert" className="text-xs text-red-600 dark:text-red-400">{loadErrorText(report.loadError)}</p>
        ) : null}
      </div>
      {message && !dialogOpen && <p role="status" className="mb-3 text-sm text-amber-700 dark:text-amber-300">{message}</p>}

      {listError ? (
        <div role="alert" className="flex flex-wrap items-center gap-2 text-sm text-red-600 dark:text-red-400">
          <span>{listError}</span>
          <button type="button" onClick={() => setListReload(value => value + 1)} className="font-semibold text-primary hover:underline">다시 불러오기</button>
        </div>
      ) : !ownList?.data ? <Spinner /> : (
        <div className="grid gap-6 lg:grid-cols-[14rem_minmax(0,1fr)]">
          <ReportList rows={rows} truncated={ownList.data.truncated === true} selectedKey={selectedKey} onSelect={setSelected} />
          {selectedKey && (
            <ReportView key={selectedKey} periodKey={selectedKey} userId={userId} revision={listKey}
              onDeleted={periodKey => { setDeleted(periodKey); setSelected(null); setListReload(value => value + 1); }} />
          )}
        </div>
      )}

      {dialogOpen && report.state && (
        <ReportGenerateDialog rows={rows} userId={userId} state={report.state} progress={report.progress} startedAt={report.startedAt}
          message={message} onStarted={() => { setMessage(''); report.markStarted(); }} onRefresh={report.refresh} onClose={closeDialog} />
      )}
    </section>
  );
}
