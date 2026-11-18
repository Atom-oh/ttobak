'use client';

import { ONBOARDING_COPY, summaryText, type OnboardingSummary } from '@/lib/onboarding';

/** Mapped SFDC accounts with a core-role summary and an invite button each. */
export function OnboardingList({ summary, onInvite }: {
  summary: OnboardingSummary;
  onInvite: (sfdcAccountId: string, name: string) => void;
}) {
  if (!summary.items.length) {
    return <p className="rounded-lg bg-slate-50 px-3 py-4 text-sm text-slate-500 dark:bg-white/5 dark:text-text-muted">{ONBOARDING_COPY.empty}</p>;
  }
  return (
    <div className="space-y-2">
      {summary.truncated && <p className="text-xs text-amber-700 dark:text-amber-400">{ONBOARDING_COPY.truncated}</p>}
      <ul className="divide-y divide-slate-100 dark:divide-white/5">
        {summary.items.map(item => {
          const text = summaryText(item.roles);
          return (
            <li key={item.sfdcAccountId} className="flex min-w-0 flex-wrap items-center justify-between gap-2 py-3">
              <div className="min-w-0">
                <p className="truncate text-sm font-medium text-slate-800 dark:text-text-main">{item.name || item.sfdcAccountId}</p>
                <p className="text-xs text-slate-500 dark:text-text-muted">{text || 'SFDC 팀에 AM, TAM, SA가 없습니다'}</p>
              </div>
              <button type="button" onClick={() => onInvite(item.sfdcAccountId, item.name || item.sfdcAccountId)}
                className="shrink-0 rounded-lg border border-slate-200 px-3 py-1.5 text-sm font-semibold text-slate-700 hover:bg-slate-50 dark:border-white/10 dark:text-text-secondary dark:hover:bg-white/5">
                {ONBOARDING_COPY.inviteTeam}
              </button>
            </li>
          );
        })}
      </ul>
    </div>
  );
}
