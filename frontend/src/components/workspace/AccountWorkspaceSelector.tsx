'use client';

import { useCallback, useEffect, useRef, useState } from 'react';
import { accountApi } from '@/lib/api';
import type { AccountSummary } from '@/types/meeting';

export function useAccountWorkspaceAccounts(userId: string) {
  const [accounts, setAccounts] = useState<AccountSummary[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const generation = useRef(0);
  const refresh = useCallback(async () => {
    const current = ++generation.current;
    setError(null);
    try {
      const cached = accountApi.peekList({ expectedUserId: userId });
      if (cached) setAccounts(cached.accounts);
      setLoading(!cached);
      const result = await accountApi.list({ expectedUserId: userId });
      if (current === generation.current) setAccounts(result.accounts);
    } catch {
      if (current === generation.current) {
        setAccounts([]);
        setError('어카운트 목록을 불러오지 못했습니다.');
      }
    } finally {
      if (current === generation.current) setLoading(false);
    }
  }, [userId]);
  const invalidate = useCallback(() => { generation.current++; }, []);
  useEffect(() => {
    void refresh();
    return invalidate;
  }, [refresh, invalidate]);
  return { accounts, loading, error, refresh };
}

export function AccountWorkspaceSelector({ accounts, selectedId, onSelect }: {
  accounts: AccountSummary[];
  selectedId: string;
  onSelect: (accountId: string) => void;
}) {
  return (
    <aside aria-label="고객 선택" className="min-w-0 rounded-2xl border border-border-subtle bg-surface p-3 lg:sticky lg:top-6">
      <label htmlFor="account-workspace-select" className="mb-2 block text-xs font-semibold text-text-secondary lg:hidden">고객 선택</label>
      <select id="account-workspace-select" value={selectedId} onChange={event => onSelect(event.target.value)}
        className="min-h-11 w-full rounded-lg border border-border-subtle bg-surface px-3 text-sm text-text-main focus-visible:outline-2 focus-visible:outline-primary lg:hidden">
        {!accounts.some(account => account.accountId === selectedId) && <option value={selectedId}>고객을 선택하세요</option>}
        {accounts.map(account => <option key={account.accountId} value={account.accountId}>{account.name}</option>)}
      </select>
      <div className="hidden lg:block">
        <p className="px-2 py-2 text-xs text-text-secondary">내 어카운트 · {accounts.length}</p>
        <div className="max-h-[65vh] space-y-1 overflow-y-auto">
          {accounts.map(account => (
            <button key={account.accountId} type="button" aria-pressed={selectedId === account.accountId}
              onClick={() => onSelect(account.accountId)}
              className={`flex min-h-16 w-full items-center gap-3 rounded-xl border p-3 text-left focus-visible:outline-2 focus-visible:outline-primary ${
                selectedId === account.accountId ? 'border-primary/20 bg-primary/10 text-primary' : 'border-transparent text-text-main hover:bg-primary/5'
              }`}>
              <span aria-hidden="true" className="flex size-9 shrink-0 items-center justify-center rounded-lg border border-border-subtle bg-surface font-semibold">
                {Array.from(account.name)[0]}
              </span>
              <span className="min-w-0">
                <span className="block break-words text-sm font-semibold">{account.name}</span>
                {account.inheritedFromAccountId && <span className="mt-1 block text-xs text-text-secondary">상위 어카운트에서 공유됨</span>}
              </span>
            </button>
          ))}
        </div>
      </div>
    </aside>
  );
}
