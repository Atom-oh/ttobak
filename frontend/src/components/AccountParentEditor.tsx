'use client';

import Link from 'next/link';
import { useCallback, useEffect, useRef, useState } from 'react';
import { accountApi } from '@/lib/api';
import type { Account, AccountSummary } from '@/types/meeting';
import { AccountParentSelect } from '@/components/AccountParentSelect';

export function AccountParentEditor({ account, canEdit, onUpdated }: {
  account: Account;
  canEdit: boolean;
  onUpdated: (parentAccountId: string) => void;
}) {
  const [accounts, setAccounts] = useState<AccountSummary[]>([]);
  const [parentId, setParentId] = useState(account.parentAccountId || '');
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);
  const active = useRef(true);
  const loadGeneration = useRef(0);
  const load = useCallback(async () => {
    const generation = ++loadGeneration.current;
    const isCurrent = () => active.current && generation === loadGeneration.current;
    setLoading(true);
    try {
      const result = await accountApi.list();
      if (!isCurrent()) return;
      setAccounts(result.accounts ?? []);
      setError(null);
    } catch (err) {
      if (isCurrent()) setError(err instanceof Error ? err.message : '계층 목록을 불러오지 못했습니다.');
    } finally {
      if (isCurrent()) setLoading(false);
    }
  }, []);
  useEffect(() => {
    active.current = true;
    void load();
    return () => { active.current = false; };
  }, [load]);

  const save = async () => {
    if (saving || loading) return;
    setSaving(true);
    setError(null);
    setSaved(false);
    try {
      const result = await accountApi.updateParent(account.accountId, { parentAccountId: parentId });
      if (!active.current) return;
      onUpdated(result.parentAccountId || '');
      setSaved(true);
      await load();
    } catch (err) {
      if (active.current) setError(err instanceof Error ? err.message : '상위 어카운트를 변경하지 못했습니다.');
    } finally {
      if (active.current) setSaving(false);
    }
  };
  const parent = accounts.find(item => item.accountId === account.parentAccountId);
  const children = accounts.filter(item => item.parentAccountId === account.accountId).sort((a, b) => a.name.localeCompare(b.name, 'ko'));

  return (
    <section className="mb-8 rounded-xl border border-slate-200 bg-white p-4 dark:border-white/10 dark:bg-surface-lowest">
      <h3 className="mb-3 flex items-center gap-2 text-sm font-semibold text-slate-900 dark:text-text-main">
        <span className="material-symbols-outlined text-lg text-primary" aria-hidden="true">account_tree</span>어카운트 계층
      </h3>
      <p className="mb-3 text-sm text-slate-500 dark:text-text-muted">
        {parent ? <Link className="text-primary hover:underline" href={`/accounts/${encodeURIComponent(parent.accountId)}`}>{parent.name}</Link>
          : account.parentAccountId ? (loading ? '상위 어카운트 확인 중…' : '상위 어카운트가 내 목록에 없습니다.') : '최상위 어카운트'}
        <span aria-hidden="true" className="px-2">/</span><span className="text-slate-700 dark:text-text-secondary">{account.name}</span>
      </p>
      {canEdit && (
        <div className="flex flex-wrap items-end gap-2">
          <div className="min-w-0 flex-1">
            <AccountParentSelect accounts={accounts} accountId={account.accountId} value={parentId}
              onChange={value => { setParentId(value); setSaved(false); }} disabled={loading || saving} />
          </div>
          <button type="button" onClick={save} disabled={loading || saving || parentId === (account.parentAccountId || '')}
            className="rounded-lg bg-primary px-4 py-2 text-sm font-semibold text-white disabled:opacity-40">
            {saving ? '저장 중…' : '저장'}
          </button>
        </div>
      )}
      {saved && <p role="status" className="mt-2 text-xs text-primary">상위 어카운트를 저장했습니다.</p>}
      {error && <div role="alert" className="mt-2 text-sm text-red-600 dark:text-red-400">{error}
        <button type="button" onClick={load} disabled={loading || saving} className="ml-2 font-semibold underline">목록 다시 불러오기</button>
      </div>}
      {children.length > 0 && (
        <div className="mt-4 border-t border-slate-100 pt-3 dark:border-white/5">
          <p className="mb-2 text-xs font-semibold text-slate-500 dark:text-text-muted">하위 어카운트</p>
          <div className="flex flex-wrap gap-2">{children.map(child => (
            <Link key={child.accountId} href={`/accounts/${encodeURIComponent(child.accountId)}`}
              className="rounded-lg bg-primary/5 px-3 py-1.5 text-sm text-primary hover:bg-primary/10">{child.name}</Link>
          ))}</div>
        </div>
      )}
    </section>
  );
}
