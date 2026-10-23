'use client';

import { useEffect, useRef, useState } from 'react';
import { useAuth } from '@/components/auth/AuthProvider';
import { ApiError, billingInsightApi } from '@/lib/api';
import { openBillingInsightHandoff } from '@/lib/billingInsight';
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
 * Rendered only when runtime config sets `billingInsight: true`.
 */
export function BillingInsightButton({ variant }: { variant: 'sidebar' | 'profile' }) {
  const { user } = useAuth();
  const [enabled, setEnabled] = useState(false);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const errorTimer = useRef<ReturnType<typeof setTimeout>>(undefined);

  useEffect(() => {
    let active = true;
    getRuntimeConfig().then(cfg => { if (active) setEnabled(cfg.billingInsight === true); });
    return () => { active = false; clearTimeout(errorTimer.current); };
  }, []);

  if (!enabled || !user) return null;

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

  return (
    <div className="flex flex-col gap-1">
      <button
        type="button"
        onClick={handleClick}
        disabled={pending}
        aria-busy={pending}
        className="flex items-center gap-3 px-2 py-1.5 rounded-lg text-sm text-slate-600 dark:text-text-secondary/70 hover:bg-slate-100 dark:hover:bg-white/5 dark:hover:text-primary transition-colors disabled:opacity-60"
      >
        {icon}
        <span className="text-xs font-medium">{LABEL}</span>
      </button>
      {message}
    </div>
  );
}
