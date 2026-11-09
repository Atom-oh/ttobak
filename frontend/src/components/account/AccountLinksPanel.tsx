'use client';

import { useCallback, useEffect, useRef, useState } from 'react';
import { accountApi } from '@/lib/api';
import {
  AUTO_LINK_BADGE, LOAD_MORE_LABEL, MANAGE_LINKS_TITLE, NO_LINKS_TEXT, autoLinkTitle, isAutoLink,
  linkRowKey, linkRowText, mergeLinkPages, shortDate,
} from '@/lib/accountOverview';
import type { AccountLink } from '@/types/meeting';
import { UnlinkButton } from './LinkToOpportunity';

/**
 * Every stored account link (manual and auto), paged from GET /links. This is
 * the one place that can remove tech, event, older news and auto
 * news→meeting links. Collapsed until opened; reloads when `revision` changes.
 */
export function AccountLinksPanel({ accountId, userId, projectNames, revision, onChanged, onAccessLost }: {
  accountId: string;
  userId: string;
  projectNames: Record<string, string>;
  revision: number;
  onChanged: () => void;
  onAccessLost: () => void;
}) {
  const [open, setOpen] = useState(false);
  const [links, setLinks] = useState<AccountLink[]>([]);
  const [cursor, setCursor] = useState('');
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const controller = useRef<AbortController | null>(null);

  const load = useCallback(async (next: string) => {
    controller.current?.abort();
    const active = new AbortController();
    controller.current = active;
    setLoading(true); setError('');
    try {
      const page = await accountApi.links.list(accountId, next, { signal: active.signal, expectedUserId: userId });
      if (active.signal.aborted) return;
      setLinks(previous => next ? mergeLinkPages(previous, page.links ?? []) : (page.links ?? []));
      setCursor(page.nextCursor || '');
    } catch (failure) {
      if (active.signal.aborted) return;
      if ((failure as { status?: number })?.status === 403) { onAccessLost(); return; }
      setError('연결 목록을 불러오지 못했습니다.');
    } finally {
      if (!active.signal.aborted) setLoading(false);
    }
  }, [accountId, userId, onAccessLost]);

  useEffect(() => {
    if (!open) return;
    void load('');
    return () => controller.current?.abort();
  }, [open, revision, load]);

  return <section className="mb-8" aria-labelledby="account-links-heading">
    <button type="button" id="account-links-heading" aria-expanded={open} aria-controls="account-links-panel"
      onClick={() => setOpen(value => !value)}
      className="flex items-center gap-1 text-base font-bold text-text-main focus-visible:outline-2 focus-visible:outline-primary">
      <span className="material-symbols-outlined text-lg" aria-hidden="true">{open ? 'expand_less' : 'expand_more'}</span>
      {MANAGE_LINKS_TITLE}
    </button>
    {open && <div id="account-links-panel" className="glass-panel mt-3 rounded-xl p-4">
      {error && <p role="alert" className="mb-2 text-sm text-error">{error}</p>}
      {!loading && !error && links.length === 0 && <p className="text-sm text-text-secondary">{NO_LINKS_TEXT}</p>}
      <ul className="space-y-2">
        {links.map(link => {
          const text = linkRowText(link, projectNames);
          return <li key={linkRowKey(link)} className="flex flex-wrap items-center gap-2 border-b border-border-subtle pb-2 text-sm last:border-0">
            <span className="min-w-0 break-all text-text-main">{text}</span>
            {isAutoLink(link.origin) && <span title={autoLinkTitle(link.evidence)}
              className="cursor-help rounded-full bg-primary/10 px-1.5 py-0.5 text-[11px] font-semibold text-primary">{AUTO_LINK_BADGE}</span>}
            {link.createdAt && <span className="text-xs text-text-secondary">{shortDate(link.createdAt)}</span>}
            <UnlinkButton accountId={accountId} userId={userId} targetLabel={text}
              input={{ fromKind: link.fromKind, fromId: link.fromId, toKind: link.toKind, toId: link.toId }}
              onUnlinked={onChanged} onAccessLost={onAccessLost} />
          </li>;
        })}
      </ul>
      {loading && <p role="status" className="mt-2 text-xs text-text-secondary">불러오는 중…</p>}
      {cursor && !loading && <button type="button" onClick={() => void load(cursor)}
        className="mt-3 text-sm text-primary">{LOAD_MORE_LABEL}</button>}
    </div>}
  </section>;
}
