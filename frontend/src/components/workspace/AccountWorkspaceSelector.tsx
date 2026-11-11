'use client';

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { accountApi } from '@/lib/api';
import { buildAccountTree, flattenAccountTree } from '@/lib/accountTree';
import { AccountHierarchyList } from '@/components/AccountHierarchyList';
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
  const rows = useMemo(() => flattenAccountTree(buildAccountTree(accounts)), [accounts]);
  return (
    <nav aria-label="고객 계층" className="min-w-0 lg:sticky lg:top-6 lg:max-h-[calc(100vh-6rem)] lg:overflow-y-auto">
      <label htmlFor="account-workspace-select" className="mb-2 block text-xs font-semibold text-text-secondary lg:hidden">고객 선택</label>
      <select id="account-workspace-select" value={selectedId} onChange={event => onSelect(event.target.value)}
        className="min-h-11 w-full rounded-lg border border-border-subtle bg-surface px-3 text-sm text-text-main focus-visible:outline-2 focus-visible:outline-primary lg:hidden">
        {!accounts.some(account => account.accountId === selectedId) && <option value={selectedId}>고객을 선택하세요</option>}
        {rows.map(({ node, depth, path }) => (
          <option key={node.accountId} value={node.accountId} title={path}>{`${'\u00a0\u00a0'.repeat(Math.min(depth, 8))}${node.name}`}</option>
        ))}
      </select>
      <div className="hidden lg:block">
        <p className="px-3 pb-1 text-[11px] font-bold tracking-wider text-slate-500 dark:text-text-muted">고객 · {accounts.length}</p>
        <AccountHierarchyList accounts={accounts} selectedId={selectedId} onSelect={onSelect} />
      </div>
    </nav>
  );
}
