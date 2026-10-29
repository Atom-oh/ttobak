'use client';

import { useEffect, useState } from 'react';
import { fetchAccountDataset } from '@/lib/billingApi';
import {
  MISSING, billingDashboardHref, billingKpiTiles, billingUploadHref, hasBillingData, loadAccountBilling,
  topActions, topSessions, topSignals,
  type BillingAccountEntry, type BillingSummaryData, type BillingSummaryItem,
} from '@/lib/billingSummary';

interface Props {
  accountId: string;
  entry: BillingAccountEntry;
}

// The request a result belongs to; a result of any other request renders as loading.
type LoadState =
  | { accountId: string; attempt: number; status: 'error' }
  | { accountId: string; attempt: number; status: 'ready'; data: BillingSummaryData };

const NO_DATA = '이 계정에는 아직 빌링 데이터가 없습니다.';
const BUTTON_SECONDARY = 'min-h-11 inline-flex items-center rounded-lg border border-slate-200 dark:border-white/10 px-4 text-sm font-medium text-slate-700 dark:text-text-secondary hover:bg-slate-50 dark:hover:bg-white/5';
const BUTTON_PRIMARY = 'min-h-11 inline-flex items-center rounded-lg bg-primary hover:bg-primary-hover px-4 text-sm font-semibold text-white';

function UploadButton({ accountId }: { accountId: string }) {
  return <a href={billingUploadHref(accountId)} className={BUTTON_SECONDARY}>COT·CUR 업로드</a>;
}

function Footnote() {
  return <p className="text-xs text-slate-500 dark:text-text-muted">이 계정의 멤버에게만 보이는 탭입니다.</p>;
}

function KpiTile({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="glass-panel rounded-xl p-4 min-w-0">
      <p className="text-xs text-slate-500 dark:text-text-muted">{label}</p>
      <div className="mt-1 text-xl font-bold text-slate-900 dark:text-text-main break-words">{children}</div>
    </div>
  );
}

function SummaryCard({ title, desc, items, href }: { title: string; desc: string; items: BillingSummaryItem[]; href: string }) {
  return (
    <div className="glass-panel rounded-xl p-4 flex flex-col min-w-0">
      <h3 className="text-sm font-bold text-slate-900 dark:text-text-main">{title}</h3>
      <p className="mt-1 text-xs text-slate-500 dark:text-text-muted">{desc}</p>
      {items.length === 0 ? (
        <p className="mt-3 flex-1 text-sm text-slate-500 dark:text-text-muted">{MISSING}</p>
      ) : (
        <ol className="mt-3 flex-1 space-y-2">
          {items.map((item) => (
            <li key={item.key} className="text-sm min-w-0">
              <p className="font-medium text-slate-800 dark:text-text-secondary truncate" title={item.title}>{item.title}</p>
              {item.meta.length > 0 && (
                <p className="text-xs text-slate-500 dark:text-text-muted truncate">{item.meta.join(' · ')}</p>
              )}
            </li>
          ))}
        </ol>
      )}
      <a href={href} aria-label={`${title} 대시보드에서 보기`}
        className="mt-2 min-h-11 inline-flex items-center self-start text-xs font-semibold text-primary hover:underline">대시보드에서 보기</a>
    </div>
  );
}

export function AccountBillingTab({ accountId, entry }: Props) {
  const hasData = hasBillingData(entry);
  const [state, setState] = useState<LoadState | null>(null);
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    if (!hasData) return;
    // Aborted on Account change, retry or unmount; a response that still lands is
    // dropped by the abort check, and a stale result never renders (accountId/attempt).
    const controller = new AbortController();
    loadAccountBilling(fetchAccountDataset, entry, accountId, controller.signal).then(
      (data) => { if (!controller.signal.aborted) setState({ accountId, attempt, status: 'ready', data }); },
      () => { if (!controller.signal.aborted) setState({ accountId, attempt, status: 'error' }); },
    );
    return () => controller.abort();
  }, [accountId, entry, hasData, attempt]);

  const dashboardHref = billingDashboardHref(accountId);

  if (!hasData) {
    return (
      <section className="space-y-4">
        <div className="glass-panel rounded-xl p-6 flex flex-col items-start gap-3">
          <p className="text-sm text-slate-600 dark:text-text-muted">{NO_DATA}</p>
          {entry.canUpload && <UploadButton accountId={accountId} />}
        </div>
        <Footnote />
      </section>
    );
  }

  const current = state?.accountId === accountId && state.attempt === attempt ? state : null;
  if (!current) {
    return (
      <div role="status" className="flex items-center justify-center py-16">
        <div className="animate-spin rounded-full h-8 w-8 border-2 border-primary border-t-transparent" />
        <span className="sr-only">불러오는 중</span>
      </div>
    );
  }
  if (current.status === 'error') {
    return (
      <section className="space-y-4">
        <div className="glass-panel rounded-xl p-6 flex flex-col items-start gap-3">
          <p role="alert" className="text-sm text-red-600 dark:text-red-400">Billing 데이터를 불러오지 못했습니다.</p>
          <button type="button" onClick={() => setAttempt((n) => n + 1)} className={BUTTON_SECONDARY}>다시 시도</button>
        </div>
        <Footnote />
      </section>
    );
  }

  const { data } = current;
  const tiles = billingKpiTiles(data, entry, accountId);
  return (
    <section className="space-y-6">
      <div className="grid grid-cols-2 md:grid-cols-4 gap-3">
        <KpiTile label="최근 월 지출">{tiles.lastSpend}</KpiTile>
        <KpiTile label="전월 대비">{tiles.mom}</KpiTile>
        <KpiTile label="COH 권고 (중복 제거, 월)">{tiles.coh}</KpiTile>
        <KpiTile label="데이터 기간">
          {tiles.period.length === 0 ? MISSING : tiles.period.map((line) => (
            <span key={line} className="block text-sm font-semibold">{line}</span>
          ))}
        </KpiTile>
      </div>

      <div className="grid grid-cols-1 md:grid-cols-3 gap-3">
        <SummaryCard title="사업 신호" desc="새로 생기거나 커지는 서비스 태그 항목"
          items={topSignals(data.tag_signals, data.cur_tag_signals, accountId)} href={dashboardHref} />
        <SummaryCard title="세션 제안" desc="신호에 맞춘 아키텍처 리뷰·데모·딥다이브 제안"
          items={topSessions(data.sessions, accountId)} href={dashboardHref} />
        <SummaryCard title="바로 할 일" desc="금액·담당(SA·TAM·AM)·근거가 붙은 우선순위 액션"
          items={topActions(data.briefing, accountId)} href={dashboardHref} />
      </div>

      <div className="flex flex-wrap gap-2">
        {entry.canUpload && <UploadButton accountId={accountId} />}
        <a href={dashboardHref} className={BUTTON_PRIMARY}>전체 대시보드 열기</a>
      </div>
      <Footnote />
    </section>
  );
}
