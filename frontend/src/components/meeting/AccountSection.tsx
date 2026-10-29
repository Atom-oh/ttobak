'use client';

import { useEffect, useRef, useState } from 'react';
import { accountApi, meetingAccountApi } from '@/lib/api';
import { shareFailureMessage } from '@/lib/accountShare';
import { isEditableAccount } from '@/lib/accountAccess';
import type { AccountSummary } from '@/types/meeting';

interface Props {
  meetingId: string;
  initialAccountId?: string;
  initialShared?: boolean;
  canManage?: boolean;
  onChanged?: (accountId: string, shared: boolean) => void;
}

export default function AccountSection({ meetingId, initialAccountId, initialShared, canManage = false, onChanged }: Props) {
  const [accounts, setAccounts] = useState<AccountSummary[]>([]);
  const [selection, setSelection] = useState({ baseline: initialAccountId || '', value: initialAccountId || '' });
  const selected = selection.baseline === (initialAccountId || '') ? selection.value : initialAccountId || '';
  const [busy, setBusy] = useState(false);
  const [loading, setLoading] = useState(true);
  const [listError, setListError] = useState('');
  const [shareError, setShareError] = useState('');
  const active = useRef(true);
  const [retry, setRetry] = useState(0);
  useEffect(() => {
    let current = true; active.current = true;
    accountApi.list().then((response) => {
      if (current) { setAccounts(response.accounts || []); setListError(''); }
    }).catch(() => { if (current) setListError('고객 목록을 불러오지 못했습니다.'); })
      .finally(() => { if (current) setLoading(false); });
    return () => { current = false; active.current = false; };
  }, [retry]);

  const linkAndShare = async () => {
    if (!selected || busy || !canManage || !accounts.some(account => account.accountId === selected && isEditableAccount(account))) return;
    setBusy(true); setShareError('');
    try {
      await meetingAccountApi.shareToAccount(meetingId, selected);
      if (active.current) onChanged?.(selected, true);
    } catch (failure) {
      if (active.current) setShareError(shareFailureMessage(failure));
    } finally { if (active.current) setBusy(false); }
  };
  const linked = accounts.find((account) => account.accountId === initialAccountId);
  return (
    <div className="rounded-xl border border-slate-200 bg-white p-4 dark:border-white/10 dark:bg-surface-lowest">
      <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
        <h4 className="text-sm font-semibold dark:text-text-main">고객 맥락 연결</h4>
        <span className="text-xs text-slate-500 dark:text-text-muted">{initialShared ? '고객 팀에 공유됨' : '팀 공유 없음'}</span>
      </div>
      {canManage ? <div className="flex flex-col gap-2 sm:flex-row">
        <select aria-label="미팅에 연결할 고객" value={selected} onChange={(event) => setSelection({ baseline: initialAccountId || '', value: event.target.value })} disabled={loading || busy}
          className="min-w-0 flex-1 rounded-lg border border-slate-200 bg-transparent px-3 py-2 text-sm dark:border-white/10 dark:text-text-main">
          <option value="">{loading ? '고객 불러오는 중…' : '고객 선택…'}</option>
          {accounts.filter(isEditableAccount).map((account) => <option key={account.accountId} value={account.accountId}>{account.name}</option>)}
        </select>
        <button type="button" onClick={() => void linkAndShare()} disabled={loading || busy || !accounts.some(account => account.accountId === selected && isEditableAccount(account))} className="rounded-lg bg-primary px-3 py-2 text-sm font-semibold text-white disabled:opacity-50">{busy ? '연결·공유 중…' : initialShared && selected === initialAccountId ? '팀 공유 갱신' : '연결하고 팀에 공유'}</button>
      </div> : <p className="text-sm text-slate-600 dark:text-text-muted">{linked?.name || (initialAccountId ? '고객 연결됨 · 고객 자료는 별도 권한이 필요합니다.' : '연결된 고객이 없습니다.')} · 연결과 팀 공유는 미팅 소유자가 관리합니다.</p>}
      <p className="mt-3 text-xs leading-5 text-slate-500 dark:text-text-muted">고객을 연결하면 미팅과 추출된 Insight가 해당 고객 팀에 함께 공유됩니다.</p>
      {linked && <a href={`/accounts/${encodeURIComponent(linked.accountId)}`} className="mt-2 inline-block text-xs font-semibold text-primary underline">{linked.name}의 누적 맥락 보기</a>}
      {shareError && <p role="alert" className="mt-2 text-sm text-red-600 dark:text-red-300">{shareError}</p>}
      {listError && <p role="alert" className="mt-2 text-sm text-red-600 dark:text-red-300">{listError} <button type="button" onClick={() => setRetry((value) => value + 1)} className="underline">목록 다시 불러오기</button></p>}
    </div>
  );
}
