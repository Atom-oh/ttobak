'use client';

import { useEffect, useMemo, useRef, useState } from 'react';
import { accountApi, ApiError, performanceApi } from '@/lib/api';
import { accountCreationAttempt } from '@/lib/accountCreation';
import { ONBOARDING_COPY } from '@/lib/onboarding';
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

/**
 * Creates a TTOBAK Account named after an unmapped SFDC account, with the
 * caller as Owner, then maps it manually. A retry after an unknown outcome
 * replays the same requestId, so it never creates a second Account.
 */
function CreateCustomerButton({ item, userId, disabled, busy, setBusy, onCreated, onReload }: {
  item: PerformanceMappingItem;
  userId: string;
  disabled: boolean;
  /** Owned by the row, which keeps its select and save disabled while a creation runs. */
  busy: boolean;
  setBusy: (busy: boolean) => void;
  onCreated: (sfdcAccountId: string, name: string) => void;
  /** Reloads the account list so a created but unmapped Account can be selected. */
  onReload: () => void;
}) {
  const attempt = useRef<ReturnType<typeof accountCreationAttempt> | null>(null);
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => { mounted.current = false; };
  }, []);
  const [error, setError] = useState<string | null>(null);
  const name = (item.name || item.sfdcAccountId).trim();

  const create = async () => {
    setBusy(true);
    setError(null);
    let accountId: string;
    try {
      const request = { name };
      attempt.current = accountCreationAttempt(request, attempt.current, () => crypto.randomUUID());
      accountId = (await accountApi.create({ ...request, requestId: attempt.current.requestId }, userId)).accountId;
    } catch {
      setError(ONBOARDING_COPY.createFailed);
      setBusy(false);
      return;
    }
    // A row that went away (its assignment was mapped or removed meanwhile) must not be overwritten.
    if (!mounted.current) return;
    try {
      await performanceApi.putMapping(item.sfdcAccountId, accountId, { expectedUserId: userId });
    } catch {
      setError(ONBOARDING_COPY.mapFailed);
      setBusy(false);
      onReload();
      return;
    }
    setBusy(false);
    onCreated(item.sfdcAccountId, name);
  };

  return (
    <>
      <button type="button" onClick={() => void create()} disabled={disabled || busy}
        className="shrink-0 rounded-lg border border-primary/40 px-3 py-2 text-sm font-semibold text-primary hover:bg-primary/5 disabled:opacity-40">
        {busy ? ONBOARDING_COPY.creating : ONBOARDING_COPY.createCustomer}
      </button>
      {error && <p role="alert" className="basis-full text-xs text-red-600 dark:text-red-400">{error}</p>}
    </>
  );
}

function MappingRow({ item, reason, options, accountsStatus, userId, onSaved, onCreated }: {
  item: PerformanceMappingItem;
  reason: string | null;
  options: { accountId: string; path: string }[];
  accountsStatus: AccountsStatus;
  userId: string;
  onSaved: () => void;
  onCreated?: (sfdcAccountId: string, name: string) => void;
}) {
  // An automatic mapping starts on its current account: saving it unchanged confirms it as manual.
  // An unreadable one starts empty, like any unmapped assignment.
  const current = item.mapped && !item.manual && !reason ? item.accountId ?? '' : '';
  const [accountId, setAccountId] = useState(current);
  const [saving, setSaving] = useState(false);
  const [creating, setCreating] = useState(false);
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
      <div className="flex min-w-0 flex-wrap gap-2">
        <select id={selectId} value={accountId} onChange={event => setAccountId(event.target.value)} disabled={saving || creating || accountsStatus !== 'ready' || !options.length}
          className="w-full min-w-0 flex-1 rounded-lg border border-slate-200 bg-white px-3 py-2 text-sm text-slate-700 disabled:opacity-50 dark:border-white/10 dark:bg-surface-lowest dark:text-text-secondary">
          <option value="">{placeholder(accountsStatus, options.length > 0)}</option>
          {options.map(option => <option key={option.accountId} value={option.accountId}>{option.path}</option>)}
        </select>
        <button type="button" onClick={save} disabled={saving || creating || accountsStatus !== 'ready' || !accountId}
          className="shrink-0 rounded-lg bg-primary px-3 py-2 text-sm font-semibold text-white hover:bg-primary-hover disabled:opacity-40">
          {saving ? '저장 중…' : !current ? '연결' : accountId === current ? '확인' : '변경'}
        </button>
        {onCreated && !item.mapped && !item.reason && <CreateCustomerButton item={item} userId={userId} disabled={saving} busy={creating} setBusy={setCreating}
          onCreated={onCreated} onReload={onSaved} />}
      </div>
      {reason && <p className="text-xs text-amber-700 dark:text-amber-400">{reason}</p>}
      {error && <p role="alert" className="text-xs text-red-600 dark:text-red-400">{error}</p>}
    </li>
  );
}

/**
 * SFDC accounts assigned to the owner with no readable TTOBAK Account mapping,
 * including mappings whose account is not readable, then automatic mappings
 * the owner can confirm or correct. A manual mapping always wins and is not
 * listed.
 */
export function MappingQueue({ items, truncated, accounts, accountsStatus, onRetryAccounts, userId, onSaved, onCreated }: {
  items: PerformanceMappingItem[];
  truncated: boolean;
  accounts: AccountSummary[];
  accountsStatus: AccountsStatus;
  onRetryAccounts: () => void;
  userId: string;
  onSaved: () => void;
  /** An Account was created and mapped for an unmapped SFDC account; the invite step opens. */
  onCreated?: (sfdcAccountId: string, name: string) => void;
}) {
  // Readability is judged only against a loaded list; a failed list judges nothing.
  const readable = useMemo(() => accountsStatus === 'ready' ? new Set(accounts.map(account => account.accountId)) : null,
    [accounts, accountsStatus]);
  const groups = useMemo(() => mappingQueueGroups(items, readable), [items, readable]);
  const options = useMemo(() => flattenAccountTree(buildAccountTree(accounts))
    .map(({ node, path }) => ({ accountId: node.accountId, path })), [accounts]);
  const row = (item: PerformanceMappingItem) => (
    <MappingRow key={`${item.sfdcAccountId}:${item.accountId ?? ''}:${item.manual}`} item={item} reason={mappingReasonText(item, readable)}
      options={options} accountsStatus={accountsStatus} userId={userId} onSaved={onSaved} onCreated={onCreated} />
  );

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
      {!groups.unmapped.length && !groups.automatic.length && (
        <p className="rounded-lg bg-slate-50 px-3 py-4 text-sm text-slate-500 dark:bg-white/5 dark:text-text-muted">
          {readable ? '매핑을 기다리는 SFDC 계정이 없습니다.' : '연결되지 않은 SFDC 계정이 없습니다. 기존 매핑의 어카운트는 어카운트 목록을 불러온 뒤 확인합니다.'}
        </p>
      )}
    </div>
  );
}
