'use client';

import { useMemo, useState } from 'react';
import { ApiError, performanceApi } from '@/lib/api';
import { buildAccountTree, flattenAccountTree } from '@/lib/accountTree';
import { unmappedAssignments, type PerformanceMappingItem } from '@/lib/performance';
import type { AccountSummary } from '@/types/meeting';

function mappingError(error: unknown): string {
  if (error instanceof ApiError && error.status === 403) return '이 어카운트를 읽을 권한이 없습니다.';
  if (error instanceof ApiError && error.status === 404) return '내 담당 SFDC 계정이 아닙니다. SFDC 수집을 다시 실행해 보세요.';
  return '매핑을 저장하지 못했습니다.';
}

function MappingRow({ item, options, userId, onSaved }: {
  item: PerformanceMappingItem;
  options: { accountId: string; path: string }[];
  userId: string;
  onSaved: () => void;
}) {
  const [accountId, setAccountId] = useState('');
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const selectId = `mapping-${item.sfdcAccountId}`;

  const save = async () => {
    if (!accountId) return;
    setSaving(true);
    setError(null);
    try {
      await performanceApi.putMapping(item.sfdcAccountId, accountId, { expectedUserId: userId });
      onSaved();
    } catch (caught) {
      setError(mappingError(caught));
    } finally {
      setSaving(false);
    }
  };

  return (
    <li className="space-y-2 py-3">
      <div className="flex min-w-0 flex-wrap items-baseline justify-between gap-2">
        <label htmlFor={selectId} className="min-w-0 truncate text-sm font-medium text-slate-800 dark:text-text-main">{item.name || item.sfdcAccountId}</label>
        <span className="text-xs text-slate-500 dark:text-text-muted">{item.role ? `${item.role} · ` : ''}{item.sfdcAccountId}</span>
      </div>
      <div className="flex min-w-0 gap-2">
        <select id={selectId} value={accountId} onChange={event => setAccountId(event.target.value)} disabled={saving || !options.length}
          className="w-full min-w-0 flex-1 rounded-lg border border-slate-200 bg-white px-3 py-2 text-sm text-slate-700 disabled:opacity-50 dark:border-white/10 dark:bg-surface-lowest dark:text-text-secondary">
          <option value="">{options.length ? 'TTOBAK 어카운트 선택' : '연결할 어카운트가 없습니다'}</option>
          {options.map(option => <option key={option.accountId} value={option.accountId}>{option.path}</option>)}
        </select>
        <button type="button" onClick={save} disabled={saving || !accountId}
          className="shrink-0 rounded-lg bg-primary px-3 py-2 text-sm font-semibold text-white hover:bg-primary-hover disabled:opacity-40">
          {saving ? '저장 중…' : '연결'}
        </button>
      </div>
      {error && <p role="alert" className="text-xs text-red-600 dark:text-red-400">{error}</p>}
    </li>
  );
}

/** SFDC accounts assigned to the owner with no readable TTOBAK Account mapping. A manual mapping always wins. */
export function MappingQueue({ items, accounts, userId, onSaved }: {
  items: PerformanceMappingItem[];
  accounts: AccountSummary[];
  userId: string;
  onSaved: () => void;
}) {
  const queue = useMemo(() => unmappedAssignments(items), [items]);
  const options = useMemo(() => flattenAccountTree(buildAccountTree(accounts))
    .map(({ node, path }) => ({ accountId: node.accountId, path })), [accounts]);

  if (!queue.length) {
    return <p className="rounded-lg bg-slate-50 px-3 py-4 text-sm text-slate-500 dark:bg-white/5 dark:text-text-muted">매핑을 기다리는 SFDC 계정이 없습니다.</p>;
  }
  return (
    <ul className="divide-y divide-slate-100 dark:divide-white/5">
      {queue.map(item => <MappingRow key={item.sfdcAccountId} item={item} options={options} userId={userId} onSaved={onSaved} />)}
    </ul>
  );
}
