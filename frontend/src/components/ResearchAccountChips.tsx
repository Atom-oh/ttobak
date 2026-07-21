'use client';

import { useEffect, useState } from 'react';
import { accountApi, researchApi } from '@/lib/api';
import type { AccountSummary } from '@/types/meeting';

interface Props {
  researchId: string;
  accountIds: string[];
  onChange: (accountIds: string[]) => void;
}

// Owner-only account multi-select for a research report (chips, toggle on click).
// Mirrors AccountSection.tsx's account dropdown but as toggleable chips since
// a research report can link to several accounts (many-to-many), not one.
export default function ResearchAccountChips({ researchId, accountIds, onChange }: Props) {
  const [accounts, setAccounts] = useState<AccountSummary[]>([]);
  const [busyId, setBusyId] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    accountApi.list().then((r) => setAccounts(r?.accounts ?? [])).catch(() => {});
  }, []);

  if (accounts.length === 0) return null;

  const toggle = async (accountId: string) => {
    const linked = accountIds.includes(accountId);
    setBusyId(accountId);
    setError(null);
    try {
      if (linked) {
        await researchApi.unlinkAccount(researchId, accountId);
        onChange(accountIds.filter((id) => id !== accountId));
      } else {
        const res = await researchApi.linkAccount(researchId, accountId);
        onChange(res.accountIds);
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : '연결에 실패했습니다.');
    } finally {
      setBusyId(null);
    }
  };

  return (
    <div className="mt-4">
      <p className="text-xs font-semibold text-slate-500 dark:text-text-muted mb-2">연결된 어카운트</p>
      <div className="flex flex-wrap gap-2">
        {accounts.map((a) => {
          const linked = accountIds.includes(a.accountId);
          return (
            <button
              key={a.accountId}
              onClick={() => toggle(a.accountId)}
              disabled={busyId === a.accountId}
              className={`inline-flex items-center gap-1 text-xs font-medium px-3 py-1.5 rounded-full border transition-colors disabled:opacity-50 ${
                linked
                  ? 'bg-primary text-white border-primary'
                  : 'bg-transparent text-slate-500 dark:text-text-muted border-slate-200 dark:border-white/10 hover:border-primary/50'
              }`}
            >
              <span className="material-symbols-outlined text-sm">
                {linked ? 'check_circle' : 'business'}
              </span>
              {a.name}
            </button>
          );
        })}
      </div>
      {error && <p className="text-xs text-red-500 dark:text-red-400 mt-1.5">{error}</p>}
    </div>
  );
}
