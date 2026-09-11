'use client';

import { useId, useMemo } from 'react';
import type { AccountSummary } from '@/types/meeting';
import { accountDescendantIds, buildAccountTree, flattenAccountTree } from '@/lib/accountTree';

export function AccountParentSelect({ accounts, value, onChange, accountId, disabled }: {
  accounts: AccountSummary[];
  value: string;
  onChange: (value: string) => void;
  accountId?: string;
  disabled?: boolean;
}) {
  const id = useId();
  const rows = useMemo(() => flattenAccountTree(buildAccountTree(accounts)), [accounts]);
  const excluded = useMemo(() => new Set(accountId ? accountDescendantIds(accounts, accountId) : []), [accounts, accountId]);
  const options = rows.filter(row => !excluded.has(row.node.accountId));
  return (
    <div className="min-w-0 space-y-1.5">
      <label htmlFor={id} className="block text-xs font-semibold text-slate-500 dark:text-text-muted">상위 어카운트</label>
      <select id={id} value={value} onChange={event => onChange(event.target.value)} disabled={disabled}
        className="w-full min-w-0 rounded-lg border border-slate-200 bg-white px-3 py-2 text-sm text-slate-700 disabled:opacity-50 dark:border-white/10 dark:bg-surface-lowest dark:text-text-secondary">
        <option value="">없음 — 최상위 어카운트</option>
        {value && !options.some(row => row.node.accountId === value) && <option value={value} disabled>현재 상위 어카운트 (목록에 없음)</option>}
        {options.map(({ node, path }) => <option key={node.accountId} value={node.accountId}>{path}</option>)}
      </select>
    </div>
  );
}
