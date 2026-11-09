'use client';

import { useCallback, useEffect, useRef, useState } from 'react';
import { accountApi } from '@/lib/api';
import { confirmDialog } from '@/lib/dialog';
import {
  ACCOUNT_KB_COPY, ACCOUNT_KB_KINDS, ACCOUNT_KB_KIND_LABELS, ACCOUNT_KB_STATE_LABELS,
  canUntagKBItem, kbItemHref, type AccountKBItem, type AccountKBKind,
} from '@/lib/accountKB';

const STATE_CLASS: Record<AccountKBItem['indexState'], string> = {
  INDEXED: 'bg-emerald-50 text-emerald-700 dark:bg-emerald-900/20 dark:text-emerald-400',
  PENDING: 'bg-amber-50 text-amber-700 dark:bg-amber-900/20 dark:text-amber-400',
  FAILED: 'bg-red-50 text-red-600 dark:bg-red-900/20 dark:text-red-400',
  UNTRACKED: 'bg-surface-container text-text-secondary',
};

/** Account-tagged KB materials (direct members), one kind at a time. */
export function AccountKB({ accountId, userId, isOwner, onAccessLost }: {
  accountId: string;
  userId: string;
  isOwner: boolean;
  onAccessLost: () => void;
}) {
  const [kind, setKind] = useState<AccountKBKind>('upload');
  const [items, setItems] = useState<AccountKBItem[]>([]);
  const [next, setNext] = useState<string | undefined>();
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [attempt, setAttempt] = useState(0);
  const [busy, setBusy] = useState(false);
  const mounted = useRef(true);
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; }; }, []);

  const failed = useCallback((failure: unknown, text: string) => {
    if (!mounted.current) return;
    if ([401, 403, 404].includes((failure as { status?: number })?.status ?? 0)) { onAccessLost(); return; }
    setError(text);
  }, [onAccessLost]);

  useEffect(() => {
    const controller = new AbortController();
    setLoading(true); setError(''); setItems([]); setNext(undefined);
    accountApi.kb(accountId, kind, undefined, { signal: controller.signal, expectedUserId: userId })
      .then(page => {
        if (controller.signal.aborted) return;
        setItems(page.items ?? []); setNext(page.nextCursor); setLoading(false);
      })
      .catch(failure => {
        if (controller.signal.aborted) return;
        setLoading(false); failed(failure, '자료를 불러오지 못했습니다.');
      });
    return () => controller.abort();
  }, [accountId, userId, kind, attempt, failed]);

  const loadMore = async () => {
    if (!next || busy) return;
    setBusy(true); setError('');
    try {
      const page = await accountApi.kb(accountId, kind, next, { expectedUserId: userId });
      if (!mounted.current) return;
      setItems(prev => [...prev, ...(page.items ?? [])]); setNext(page.nextCursor);
    } catch (failure) { failed(failure, '자료를 더 불러오지 못했습니다.'); }
    finally { if (mounted.current) setBusy(false); }
  };

  const download = async (item: AccountKBItem) => {
    if (busy) return;
    setBusy(true); setError('');
    try {
      const { url } = await accountApi.kbDownload(accountId, item.id, { expectedUserId: userId });
      // Short-lived presigned URL: open immediately, never store it.
      if (mounted.current) window.open(url, '_blank', 'noopener,noreferrer');
    } catch (failure) { failed(failure, '다운로드할 수 없습니다.'); }
    finally { if (mounted.current) setBusy(false); }
  };

  const untag = async (item: AccountKBItem) => {
    if (busy) return;
    if (!await confirmDialog(`'${item.title}' 자료의 태그를 해제할까요?`) || !mounted.current) return;
    setBusy(true); setError('');
    try {
      await accountApi.kbUntag(accountId, item.id, { expectedUserId: userId });
      if (!mounted.current) return;
      setItems(prev => prev.filter(i => i.id !== item.id));
    } catch (failure) { failed(failure, '태그를 해제하지 못했습니다.'); }
    finally { if (mounted.current) setBusy(false); }
  };

  return <section>
    <div className="mb-4 flex flex-wrap gap-2" role="group" aria-label="자료 종류">
      {ACCOUNT_KB_KINDS.map(k => <button key={k} type="button" aria-pressed={kind === k} onClick={() => setKind(k)}
        className={`min-h-9 rounded-full px-3 text-sm focus-visible:outline-2 focus-visible:outline-primary ${
          kind === k ? 'bg-primary text-white font-bold' : 'bg-surface-container text-text-secondary hover:text-text-main'}`}>
        {ACCOUNT_KB_KIND_LABELS[k]}
      </button>)}
    </div>
    {error && <div role="alert" className="mb-4 rounded-lg bg-red-50 p-3 text-sm text-red-600 dark:bg-red-900/20 dark:text-red-400">
      {error} <button type="button" onClick={() => setAttempt(a => a + 1)} className="ml-2 underline">다시 시도</button>
    </div>}
    {loading ? <p role="status" className="py-4 text-sm text-text-secondary">불러오는 중…</p>
      : items.length === 0 && !error ? <p className="py-4 text-sm text-text-secondary">{ACCOUNT_KB_COPY.empty}</p>
      : <ul className="divide-y divide-border-subtle rounded-lg border border-border-subtle">
        {items.map(item => {
          const href = kbItemHref(item);
          return <li key={`${item.kind}:${item.id}`} className="flex min-w-0 flex-wrap items-center justify-between gap-2 p-3 text-sm">
            <div className="min-w-0">
              {href ? <a href={href} className="truncate font-medium text-primary hover:underline">{item.title}</a>
                : <span className="truncate font-medium text-text-main">{item.title}</span>}
              {item.date && <span className="ml-2 text-xs text-text-secondary">{item.date}</span>}
            </div>
            <div className="flex items-center gap-2">
              <span className={`rounded-full px-2 py-0.5 text-xs ${STATE_CLASS[item.indexState] ?? STATE_CLASS.UNTRACKED}`}>
                {ACCOUNT_KB_STATE_LABELS[item.indexState] ?? ACCOUNT_KB_STATE_LABELS.UNTRACKED}
              </span>
              {item.canDownload && <button type="button" disabled={busy} onClick={() => void download(item)}
                className="text-xs font-semibold text-primary disabled:opacity-40">{ACCOUNT_KB_COPY.download}</button>}
              {canUntagKBItem(item, userId, isOwner) && <button type="button" disabled={busy} onClick={() => void untag(item)}
                className="text-xs font-semibold text-red-600 disabled:opacity-40">{ACCOUNT_KB_COPY.untag}</button>}
            </div>
          </li>;
        })}
      </ul>}
    {next && !loading && <button type="button" disabled={busy} onClick={() => void loadMore()}
      className="mt-3 text-sm font-semibold text-primary disabled:opacity-40">{ACCOUNT_KB_COPY.more}</button>}
  </section>;
}
