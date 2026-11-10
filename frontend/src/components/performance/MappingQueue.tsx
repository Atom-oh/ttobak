'use client';

import { useMemo, useState } from 'react';
import { ApiError, performanceApi } from '@/lib/api';
import { buildAccountTree, flattenAccountTree } from '@/lib/accountTree';
import { mappingQueueGroups, mappingReasonText, type PerformanceMappingItem } from '@/lib/performance';
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

function MappingRow({ item, options, accountsStatus, userId, onSaved }: {
  item: PerformanceMappingItem;
  options: { accountId: string; path: string }[];
  accountsStatus: AccountsStatus;
  userId: string;
  onSaved: () => void;
}) {
  // An automatic mapping starts on its current account: saving it unchanged confirms it as manual.
  const current = item.mapped && !item.manual ? item.accountId ?? '' : '';
  const [accountId, setAccountId] = useState(current);
  const reason = mappingReasonText(item);
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
          {saving ? '저장 중…' : !current ? '연결' : accountId === current ? '확인' : '변경'}
        </button>
      </div>
      {reason && <p className="text-xs text-amber-700 dark:text-amber-400">{reason}</p>}
      {error && <p role="alert" className="text-xs text-red-600 dark:text-red-400">{error}</p>}
    </li>
  );
}

/**
 * SFDC accounts assigned to the owner with no readable TTOBAK Account mapping,
 * then automatic mappings the owner can confirm or correct. A manual mapping
 * always wins and is not listed.
 */
export function MappingQueue({ items, accounts, accountsStatus, onRetryAccounts, userId, onSaved }: {
  items: PerformanceMappingItem[];
  accounts: AccountSummary[];
  accountsStatus: AccountsStatus;
  onRetryAccounts: () => void;
  userId: string;
  onSaved: () => void;
}) {
  const groups = useMemo(() => mappingQueueGroups(items), [items]);
  const options = useMemo(() => flattenAccountTree(buildAccountTree(accounts))
    .map(({ node, path }) => ({ accountId: node.accountId, path })), [accounts]);

  if (!groups.unmapped.length && !groups.automatic.length) {
    return <p className="rounded-lg bg-slate-50 px-3 py-4 text-sm text-slate-500 dark:bg-white/5 dark:text-text-muted">매핑을 기다리는 SFDC 계정이 없습니다.</p>;
  }
  const row = (item: PerformanceMappingItem) => (
    <MappingRow key={`${item.sfdcAccountId}:${item.accountId ?? ''}:${item.manual}`} item={item} options={options}
      accountsStatus={accountsStatus} userId={userId} onSaved={onSaved} />
  );
  return (
    <div className="space-y-2">
      {accountsStatus === 'error' && (
        <div role="alert" className="flex flex-wrap items-center gap-2 text-sm text-red-600 dark:text-red-400">
          <span>연결할 어카운트 목록을 불러오지 못했습니다.</span>
          <button type="button" onClick={onRetryAccounts} className="font-semibold text-primary hover:underline">다시 불러오기</button>
        </div>
      )}
      {groups.unmapped.length > 0 && (
        <ul className="divide-y divide-slate-100 dark:divide-white/5">{groups.unmapped.map(row)}</ul>
      )}
      {groups.automatic.length > 0 && (
        <div className="pt-2">
          <h4 className="text-sm font-semibold text-slate-700 dark:text-text-secondary">자동 매핑 확인</h4>
          <p className="mt-1 text-xs text-slate-500 dark:text-text-muted">이름이 일치해 자동으로 연결된 계정입니다. 맞으면 확인하고, 다르면 다른 어카운트로 변경하세요.</p>
          <ul className="divide-y divide-slate-100 dark:divide-white/5">{groups.automatic.map(row)}</ul>
        </div>
      )}
    </div>
  );
}
