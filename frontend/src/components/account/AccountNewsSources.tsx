'use client';

import { useCallback, useEffect, useRef, useState } from 'react';
import Link from 'next/link';
import { accountApi } from '@/lib/api';
import { confirmDialog } from '@/lib/dialog';
import { newsSourcesHref } from '@/lib/newsRegistration';
import type { CrawlerSource } from '@/types/meeting';

/**
 * Account-owned news sources (direct members): the list and its delete.
 * Registration and editing live in the Insights news sources view.
 */
export function AccountNewsSources({ accountId, userId, accountName, onChanged, onAccessLost }: {
  accountId: string;
  userId: string;
  accountName: string;
  onChanged: () => void;
  onAccessLost: () => void;
}) {
  const [sources, setSources] = useState<CrawlerSource[] | null>(null);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const [attempt, setAttempt] = useState(0);
  const mounted = useRef(true);
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; }; }, []);

  useEffect(() => {
    const controller = new AbortController();
    accountApi.newsSources.list(accountId, { signal: controller.signal, expectedUserId: userId })
      .then(page => { if (!controller.signal.aborted) setSources(page.sources ?? []); })
      .catch(failure => {
        if (controller.signal.aborted) return;
        if (failure?.status === 403) { onAccessLost(); return; }
        setSources([]); setError('뉴스 소스를 불러오지 못했습니다.');
      });
    return () => controller.abort();
  }, [accountId, userId, attempt, onAccessLost]);

  const changed = useCallback(() => { setAttempt(a => a + 1); onChanged(); }, [onChanged]);

  const remove = async (source: CrawlerSource) => {
    if (saving) return;
    if (!await confirmDialog(`'${source.sourceName}' 뉴스 소스를 삭제할까요? 수집이 중단됩니다.`) || !mounted.current) return;
    setSaving(true); setError('');
    try {
      await accountApi.newsSources.remove(accountId, source.sourceId, { expectedUserId: userId });
      if (mounted.current) changed();
    } catch (failure) {
      if (!mounted.current) return;
      if ((failure as { status?: number })?.status === 403) { onAccessLost(); return; }
      setError('뉴스 소스를 삭제하지 못했습니다.');
    } finally { if (mounted.current) setSaving(false); }
  };

  return <div className="mt-3 text-sm">
    <div className="flex flex-wrap items-center gap-2">
      <span className="text-xs font-semibold text-text-secondary">뉴스 소스</span>
      {(sources ?? []).map(source => <span key={source.sourceId} className="inline-flex items-center gap-1 rounded-full bg-surface-container px-2 py-0.5 text-xs text-text-main">
        {source.sourceName}
        <button type="button" disabled={saving} aria-label={`${source.sourceName} 뉴스 소스 삭제`} onClick={() => void remove(source)}
          className="text-text-muted hover:text-error disabled:opacity-40">×</button>
      </span>)}
      <Link href={newsSourcesHref(accountId)} aria-label={`${accountName} 뉴스 소스 등록`}
        className="rounded-sm text-xs font-semibold text-primary hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/50">뉴스 소스 등록</Link>
    </div>
    {error && <p role="alert" className="mt-2 text-xs text-error">{error}</p>}
  </div>;
}
