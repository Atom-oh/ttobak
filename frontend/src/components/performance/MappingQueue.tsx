'use client';

import { useMemo, useState } from 'react';
import { ApiError, performanceApi } from '@/lib/api';
import { buildAccountTree, flattenAccountTree } from '@/lib/accountTree';
import { mappingTargetUnreadable, unmappedAssignments, type PerformanceMappingItem } from '@/lib/performance';
import type { AccountSummary } from '@/types/meeting';

function mappingError(error: unknown): string {
  if (error instanceof ApiError && error.status === 403) return '이 어카운트를 읽을 권한이 없습니다.';
  if (error instanceof ApiError && error.status === 404) return '내 담당 SFDC 계정이 아닙니다. SFDC 수집을 다시 실행해 보세요.';
  return '매핑을 저장하지 못했습니다.';
}

type AccountsStatus = 'loading' | 'error' | 'ready';

function placeholder(status: AccountsStatus, hasOptions: boolean): string {
  if (status === 'loading') return '어카운트 불러오는 중…';
  if (status === 'error') return '어카운트 목록을 불러오지 못했습니다';
  return hasOptions ? 'TTOBAK 어카운트 선택' : '연결할 어카운트가 없습니다';
}

function MappingRow({ item, unreadable, options, accountsStatus, userId, onSaved }: {
  item: PerformanceMappingItem;
  unreadable: boolean;
  options: { accountId: string; path: string }[];
  accountsStatus: AccountsStatus;
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
        <select id={selectId} value={accountId} onChange={event => setAccountId(event.target.value)} disabled={saving || accountsStatus !== 'ready' || !options.length}
          className="w-full min-w-0 flex-1 rounded-lg border border-slate-200 bg-white px-3 py-2 text-sm text-slate-700 disabled:opacity-50 dark:border-white/10 dark:bg-surface-lowest dark:text-text-secondary">
          <option value="">{placeholder(accountsStatus, options.length > 0)}</option>
          {options.map(option => <option key={option.accountId} value={option.accountId}>{option.path}</option>)}
        </select>
        <button type="button" onClick={save} disabled={saving || accountsStatus !== 'ready' || !accountId}
          className="shrink-0 rounded-lg bg-primary px-3 py-2 text-sm font-semibold text-white hover:bg-primary-hover disabled:opacity-40">
          {saving ? '저장 중…' : '연결'}
        </button>
      </div>
      {unreadable && <p className="text-xs text-amber-700 dark:text-amber-400">연결했던 어카운트를 읽을 수 없어 매핑되지 않은 것으로 봅니다. 다시 연결하세요.</p>}
      {error && <p role="alert" className="text-xs text-red-600 dark:text-red-400">{error}</p>}
    </li>
  );
}

/**
 * SFDC accounts assigned to the owner with no readable TTOBAK Account mapping,
 * including mappings whose account is not in the loaded readable list. A
 * manual mapping always wins.
 */
export function MappingQueue({ items, truncated, accounts, accountsStatus, onRetryAccounts, userId, onSaved }: {
  items: PerformanceMappingItem[];
  truncated: boolean;
  accounts: AccountSummary[];
  accountsStatus: AccountsStatus;
  onRetryAccounts: () => void;
  userId: string;
  onSaved: () => void;
}) {
  // Readability is judged only against a loaded list; a failed list judges nothing.
  const readable = useMemo(() => accountsStatus === 'ready' ? new Set(accounts.map(account => account.accountId)) : null,
    [accounts, accountsStatus]);
  const queue = useMemo(() => unmappedAssignments(items, readable), [items, readable]);
  const options = useMemo(() => flattenAccountTree(buildAccountTree(accounts))
    .map(({ node, path }) => ({ accountId: node.accountId, path })), [accounts]);

  return (
    <div className="space-y-2">
      {accountsStatus === 'error' && (
        <div role="alert" className="flex flex-wrap items-center gap-2 text-sm text-red-600 dark:text-red-400">
          <span>연결할 어카운트 목록을 불러오지 못했습니다.</span>
          <button type="button" onClick={onRetryAccounts} className="font-semibold text-primary hover:underline">다시 불러오기</button>
        </div>
      )}
      {truncated && (
        <p className="text-xs text-amber-700 dark:text-amber-400">담당 SFDC 계정이 500개를 넘어 앞의 500개만 표시합니다. 나머지는 표시되지 않습니다.</p>
      )}
      {queue.length ? (
        <ul className="divide-y divide-slate-100 dark:divide-white/5">
          {queue.map(item => <MappingRow key={item.sfdcAccountId} item={item} unreadable={mappingTargetUnreadable(item, readable)}
            options={options} accountsStatus={accountsStatus} userId={userId} onSaved={onSaved} />)}
        </ul>
      ) : (
        <p className="rounded-lg bg-slate-50 px-3 py-4 text-sm text-slate-500 dark:bg-white/5 dark:text-text-muted">
          {readable ? '매핑을 기다리는 SFDC 계정이 없습니다.' : '연결되지 않은 SFDC 계정이 없습니다. 기존 매핑의 어카운트는 어카운트 목록을 불러온 뒤 확인합니다.'}
        </p>
      )}
    </div>
  );
}
