'use client';

import { useEffect, useMemo, useState } from 'react';
import { useRouter } from 'next/navigation';
import { useAuth } from '@/components/auth/AuthProvider';
import { AppLayout } from '@/components/layout/AppLayout';
import { GoalsEditor } from '@/components/performance/GoalsEditor';
import { MappingQueue } from '@/components/performance/MappingQueue';
import { MissedList } from '@/components/performance/MissedList';
import { accountApi, performanceApi } from '@/lib/api';
import {
  accountListState, describeFreshness, PERFORMANCE_PERIODS, periodLabel, type AccountListResult,
  type PerformanceMappingItem, type PerformanceMissedList, type PerformancePeriod,
} from '@/lib/performance';
import type { AccountSummary } from '@/types/meeting';

const cardClass = 'rounded-xl border border-slate-200 bg-white p-5 dark:border-white/10 dark:bg-surface-lowest';
const headingClass = 'text-lg font-bold text-slate-900 dark:text-text-main dark:font-headline';

// The goals year follows Asia/Seoul, as the missed periods do (no DST).
const seoulYear = () => new Date(Date.now() + 9 * 60 * 60 * 1000).getUTCFullYear();

function Spinner() {
  return <div className="animate-spin rounded-full h-8 w-8 border-2 border-primary border-t-transparent" />;
}

// Owner-only SA activity page: every request reads the caller's own rows and
// the page has no share control (ADR-068).
export default function PerformancePage() {
  const { isLoading, isAuthenticated, user } = useAuth();
  const router = useRouter();
  const userId = user?.userId ?? '';
  const [period, setPeriod] = useState<PerformancePeriod>('week');
  const [reloadKey, setReloadKey] = useState(0);
  // Results are tagged with the request that produced them, so a period
  // change or reload shows the spinner without resetting state in an effect.
  const [missedResult, setMissedResult] = useState<{ key: string; data?: PerformanceMissedList; error?: string } | null>(null);
  const [mappingsResult, setMappingsResult] = useState<{ key: string; data?: PerformanceMappingItem[]; error?: string } | null>(null);
  const [accountsResult, setAccountsResult] = useState<AccountListResult<AccountSummary> | null>(null);
  const [accountsAttempt, setAccountsAttempt] = useState(0);
  const [year] = useState(seoulYear);

  useEffect(() => {
    if (!isLoading && !isAuthenticated) router.replace('/');
  }, [isLoading, isAuthenticated, router]);

  useEffect(() => {
    if (!userId) return;
    let active = true;
    const key = `${reloadKey}:${accountsAttempt}`;
    // A failed list is an error state with a retry, never an empty account list.
    accountApi.list({ expectedUserId: userId })
      .then(result => { if (active) setAccountsResult(Array.isArray(result?.accounts) ? { userId, key, accounts: result.accounts } : { userId, key, error: true }); })
      .catch(() => { if (active) setAccountsResult({ userId, key, error: true }); });
    return () => { active = false; };
  }, [userId, reloadKey, accountsAttempt]);

  useEffect(() => {
    if (!userId) return;
    const controller = new AbortController();
    const key = `${userId}:${period}:${reloadKey}`;
    performanceApi.missed(period, { signal: controller.signal, expectedUserId: userId })
      .then(data => setMissedResult({ key, data }))
      .catch(() => { if (!controller.signal.aborted) setMissedResult({ key, error: '놓친 항목을 불러오지 못했습니다.' }); });
    return () => controller.abort();
  }, [userId, period, reloadKey]);

  useEffect(() => {
    if (!userId) return;
    const controller = new AbortController();
    const key = `${userId}:${reloadKey}`;
    performanceApi.mappings({ signal: controller.signal, expectedUserId: userId })
      .then(result => setMappingsResult({ key, data: result.items }))
      .catch(() => { if (!controller.signal.aborted) setMappingsResult({ key, error: 'SFDC 매핑 목록을 불러오지 못했습니다.' }); });
    return () => controller.abort();
  }, [userId, reloadKey]);

  // Every result is bound to the signed-in user; another user's rows never render.
  const currentMissed = missedResult?.key === `${userId}:${period}:${reloadKey}` ? missedResult : null;
  const missed = currentMissed?.data ?? null;
  const missedError = currentMissed?.error ?? null;
  // Keep the previous queue visible while a reload after a saved mapping runs.
  const ownMappings = mappingsResult?.key.startsWith(`${userId}:`) ? mappingsResult : null;
  const mappings = ownMappings?.data ?? null;
  const mappingsError = ownMappings?.key === `${userId}:${reloadKey}` ? ownMappings.error ?? null : null;
  const accountList = useMemo(() => accountListState(accountsResult, userId, `${reloadKey}:${accountsAttempt}`), [accountsResult, userId, reloadKey, accountsAttempt]);
  const accounts = accountList.accounts;
  const accountNames = useMemo(() => accountList.status === 'error' ? null : new Map(accounts.map(account => [account.accountId, account.name])), [accountList.status, accounts]);
  const retryAccounts = () => setAccountsAttempt(attempt => attempt + 1);

  if (isLoading) {
    return <div className="min-h-screen flex items-center justify-center"><Spinner /></div>;
  }
  if (!isAuthenticated || !userId) return null;

  return (
    <AppLayout activePath="/performance">
      <header className="lg:hidden flex items-center bg-white dark:bg-[var(--surface)] px-4 py-4 justify-between border-b border-slate-100 dark:border-white/10 sticky top-0 z-10">
        <div className="flex items-center gap-3">
          <div className="text-primary flex size-10 shrink-0 items-center justify-center bg-primary/10 rounded-lg">
            <span className="material-symbols-outlined">insights</span>
          </div>
          <h1 className="text-slate-900 dark:text-text-main dark:font-headline text-xl font-bold leading-tight tracking-tight">SA 활동</h1>
        </div>
      </header>

      <div className="flex-1 overflow-y-auto pb-24 lg:pb-8">
        <div className="p-4 lg:px-16 lg:pt-16 lg:pb-8 max-w-5xl w-full space-y-6">
          <div className="flex flex-wrap items-end justify-between gap-4">
            <div>
              <h2 className="hidden lg:block text-3xl font-bold tracking-tight lg:text-4xl lg:font-black dark:font-headline dark:text-text-main">SA 활동</h2>
              <p className="text-slate-600 dark:text-text-muted mt-2">나만 볼 수 있는 활동 점검 페이지입니다. 다른 사람과 공유되지 않습니다.</p>
            </div>
            <div role="group" aria-label="기간" className="inline-flex rounded-lg border border-slate-200 bg-slate-50 p-1 dark:border-white/10 dark:bg-surface-lowest">
              {PERFORMANCE_PERIODS.map(option => (
                <button key={option.value} type="button" aria-pressed={period === option.value}
                  onClick={() => {
                    setPeriod(option.value);
                    // A period change also retries a failed account list.
                    if (accountList.status === 'error') setAccountsAttempt(attempt => attempt + 1);
                  }}
                  className={`rounded-md px-3 py-1.5 text-sm font-semibold ${period === option.value
                    ? 'bg-white text-primary shadow-sm dark:bg-surface-high'
                    : 'text-slate-600 hover:text-slate-900 dark:text-text-muted dark:hover:text-text-main'}`}>
                  {option.label}
                </button>
              ))}
            </div>
          </div>

          <section className={cardClass} aria-labelledby="perf-scorecard">
            <h3 id="perf-scorecard" className={headingClass}>스코어카드</h3>
            <p className="mt-1 mb-4 text-sm text-slate-500 dark:text-text-muted">AI 평가와 요약, 월별 활동 흐름은 다음 단계에서 제공됩니다. 평가 기준이 될 목표를 먼저 적어 두세요.</p>
            <GoalsEditor key={`${userId}:${year}`} year={year} userId={userId} />
          </section>

          <section className={cardClass} aria-labelledby="perf-missed">
            <h3 id="perf-missed" className={`${headingClass} mb-4`}>놓친 항목 · {periodLabel(period)}</h3>
            {missedError ? (
              <div role="alert" className="flex flex-wrap items-center gap-2 text-sm text-red-600 dark:text-red-400">
                <span>{missedError}</span>
                <button type="button" onClick={() => setReloadKey(key => key + 1)} className="font-semibold text-primary hover:underline">다시 불러오기</button>
              </div>
            ) : missed && accountList.status !== 'loading' ? <MissedList list={missed} accountNames={accountNames} onRetryAccounts={retryAccounts} />
                : <div className="flex justify-center py-8"><Spinner /></div>}
          </section>

          <section className={cardClass} aria-labelledby="perf-mapping">
            <h3 id="perf-mapping" className={headingClass}>SFDC 계정 매핑 대기열</h3>
            <p className="mt-1 mb-3 text-sm text-slate-500 dark:text-text-muted">연결되지 않은 SFDC 계정은 추정하지 않습니다. 직접 연결한 매핑이 자동 매핑보다 우선합니다.</p>
            {mappingsError ? <p role="alert" className="text-sm text-red-600 dark:text-red-400">{mappingsError}</p>
              : mappings ? <MappingQueue items={mappings} accounts={accounts} accountsStatus={accountList.status}
                onRetryAccounts={retryAccounts} userId={userId} onSaved={() => setReloadKey(key => key + 1)} />
                : <div className="flex justify-center py-8"><Spinner /></div>}
          </section>

          {missed && missed.sourceFreshness.length > 0 && (
            <section aria-label="데이터 수집 상태" className="space-y-1 text-xs text-slate-500 dark:text-text-muted">
              {missed.sourceFreshness.map(entry => {
                const line = describeFreshness(entry);
                return (
                  <p key={line.source}>
                    {line.text}
                    {line.command && <> · Mac에서 <code className="rounded bg-slate-100 px-1.5 py-0.5 font-mono dark:bg-white/10">{line.command}</code> 실행</>}
                  </p>
                );
              })}
            </section>
          )}
        </div>
      </div>
    </AppLayout>
  );
}
