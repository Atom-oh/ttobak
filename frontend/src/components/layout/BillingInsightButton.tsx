'use client';

import Link from 'next/link';
import { type ReactNode, useEffect, useRef, useState } from 'react';
import { useAuth } from '@/components/auth/AuthProvider';
import { ApiError, billingInsightApi } from '@/lib/api';
import { billingEntryMode, openBillingInsightHandoff } from '@/lib/billingInsight';
import { getRuntimeConfig } from '@/lib/runtimeConfig';

const LABEL = '빌링 인사이트 열기';

function handoffErrorMessage(error: unknown): string {
  if (error instanceof ApiError && error.status === 404) return '빌링 인사이트를 지금 사용할 수 없습니다.';
  if (error instanceof ApiError && error.status === 403) return '이메일이 확인된 계정만 빌링 인사이트를 열 수 있습니다.';
  if (error instanceof Error && error.message) return error.message;
  return '빌링 인사이트를 열지 못했습니다. 다시 시도해주세요.';
}

/**
 * Opens the AWS Billing Insight companion through a single-use hand-off.
 * Rendered only when runtime config sets `billingInsight: true`; with
 * `billingService: true` it is instead a plain link to the in-app /billing page. In the
 * sidebar and the mobile menu sheet it is the "고객 분석 › Billing" item, with
 * `groupLabel` rendered above it so the group heading disappears with it.
 */
export function BillingInsightButton({ variant, groupLabel }: { variant: 'sidebar' | 'sheet' | 'profile'; groupLabel?: ReactNode }) {
  const { user } = useAuth();
  const [mode, setMode] = useState<'link' | 'handoff' | 'hidden'>('hidden');
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const errorTimer = useRef<ReturnType<typeof setTimeout>>(undefined);

  useEffect(() => {
    let active = true;
    getRuntimeConfig().then(cfg => { if (active) setMode(billingEntryMode(cfg)); });
    return () => { active = false; clearTimeout(errorTimer.current); };
  }, []);

  if (mode === 'hidden' || !user) return null;

  if (mode === 'link') {
    if (variant === 'profile') {
      return (
        <Link
          href="/billing"
          className="w-full flex items-center gap-3 px-4 py-3.5 hover:bg-slate-50 dark:hover:bg-slate-800 transition-colors text-left"
        >
          <span className="material-symbols-outlined text-slate-500">monitoring</span>
          <span className="text-sm font-medium text-slate-900 dark:text-white flex-1">Billing</span>
          <span className="material-symbols-outlined text-slate-400 text-lg">chevron_right</span>
        </Link>
      );
    }
    if (variant === 'sheet') {
      return (
        <div className="flex flex-col gap-1.5">
          {groupLabel}
          <Link
            href="/billing"
            className="self-start min-h-11 px-3.5 rounded-lg border border-slate-200 dark:border-white/10 text-sm text-slate-700 dark:text-text-secondary flex items-center gap-2"
          >
            <span className="material-symbols-outlined text-lg">monitoring</span>
            <span>Billing</span>
          </Link>
        </div>
      );
    }
    return (
      <div className="flex flex-col gap-1">
        {groupLabel}
        <Link
          href="/billing"
          className="flex items-center gap-3 px-3 py-2 rounded-lg text-sm font-medium text-slate-600 dark:text-text-secondary hover:bg-slate-100 dark:hover:bg-white/5 dark:hover:text-text-main transition-colors"
        >
          <span className="material-symbols-outlined text-xl">monitoring</span>
          <span className="flex-1 text-left">Billing</span>
        </Link>
      </div>
    );
  }

  const handleClick = () => {
    if (pending) return;
    setPending(true);
    setError(null);
    // openTab runs synchronously inside this click, before any await.
    openBillingInsightHandoff({
      openTab: () => window.open('', '_blank'),
      createHandoff: () => billingInsightApi.createHandoff(user.userId),
      navigateCurrent: url => window.location.assign(url),
    }).catch(err => {
      setError(handoffErrorMessage(err));
      clearTimeout(errorTimer.current);
      errorTimer.current = setTimeout(() => setError(null), 5000);
    }).finally(() => setPending(false));
  };

  const icon = <span className="material-symbols-outlined text-lg">{pending ? 'progress_activity' : 'monitoring'}</span>;
  const message = error && (
    <p role="alert" className="px-2 text-xs text-red-600 dark:text-red-400">{error}</p>
  );

  if (variant === 'profile') {
    return (
      <>
        <button
          type="button"
          onClick={handleClick}
          disabled={pending}
          aria-busy={pending}
          className="w-full flex items-center gap-3 px-4 py-3.5 hover:bg-slate-50 dark:hover:bg-slate-800 transition-colors disabled:opacity-60 text-left"
        >
          <span className="material-symbols-outlined text-slate-500">{pending ? 'progress_activity' : 'monitoring'}</span>
          <span className="text-sm font-medium text-slate-900 dark:text-white flex-1">{LABEL}</span>
          <span className="material-symbols-outlined text-slate-400 text-lg">open_in_new</span>
        </button>
        {message && <div className="px-2 py-2">{message}</div>}
      </>
    );
  }

  if (variant === 'sheet') {
    return (
      <div className="flex flex-col gap-1.5">
        {groupLabel}
        <button
          type="button"
          onClick={handleClick}
          disabled={pending}
          aria-busy={pending}
          className="self-start min-h-11 px-3.5 rounded-lg border border-slate-200 dark:border-white/10 text-sm text-slate-700 dark:text-text-secondary flex items-center gap-2 disabled:opacity-60"
        >
          {icon}
          <span>Billing</span>
          <span className="material-symbols-outlined text-base text-slate-400">open_in_new</span>
        </button>
        {message}
      </div>
    );
  }

  return (
    <div className="flex flex-col gap-1">
      {groupLabel}
      <button
        type="button"
        onClick={handleClick}
        disabled={pending}
        aria-busy={pending}
        title={LABEL}
        className="flex items-center gap-3 px-3 py-2 rounded-lg text-sm font-medium text-slate-600 dark:text-text-secondary hover:bg-slate-100 dark:hover:bg-white/5 dark:hover:text-text-main transition-colors disabled:opacity-60"
      >
        <span className="material-symbols-outlined text-xl">{pending ? 'progress_activity' : 'monitoring'}</span>
        <span className="flex-1 text-left">Billing</span>
        <span className="material-symbols-outlined text-base text-slate-400">open_in_new</span>
      </button>
      {message}
    </div>
  );
}
