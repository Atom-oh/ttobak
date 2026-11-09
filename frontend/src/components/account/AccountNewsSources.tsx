'use client';

import { useCallback, useEffect, useRef, useState } from 'react';
import { accountApi } from '@/lib/api';
import { confirmDialog } from '@/lib/dialog';
import { newsSourceInput, type NewsSourceForm } from '@/lib/accountOverview';
import type { CrawlerSource } from '@/types/meeting';

const inputClass = 'mt-1 w-full rounded border border-border-subtle bg-surface-lowest p-2 text-sm text-text-main';
const EMPTY_FORM: NewsSourceForm = { sourceName: '', queries: '', aliases: '' };

/**
 * Account-owned news sources (direct members). Default queries are the
 * account name and aliases, so only the name is required.
 */
export function AccountNewsSources({ accountId, userId, accountName, onChanged, onAccessLost }: {
  accountId: string;
  userId: string;
  accountName: string;
  onChanged: () => void;
  onAccessLost: () => void;
}) {
  const [sources, setSources] = useState<CrawlerSource[] | null>(null);
  const [form, setForm] = useState<NewsSourceForm | null>(null);
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

  const failed = (failure: unknown, text: string) => {
    if (!mounted.current) return;
    if ((failure as { status?: number })?.status === 403) { onAccessLost(); return; }
    setError(text);
  };

  const create = async () => {
    if (!form || saving) return;
    const { input, error: invalid } = newsSourceInput(form);
    if (!input) { setError(invalid); return; }
    setSaving(true); setError('');
    try {
      await accountApi.newsSources.create(accountId, input, { expectedUserId: userId });
      if (!mounted.current) return;
      setForm(null);
      changed();
    } catch (failure) {
      failed(failure, (failure as { status?: number })?.status === 400
        ? '뉴스 소스를 추가할 수 없습니다. 입력과 소스 개수(최대 10개)를 확인해 주세요.'
        : '뉴스 소스를 추가하지 못했습니다.');
    } finally { if (mounted.current) setSaving(false); }
  };

  const remove = async (source: CrawlerSource) => {
    if (saving) return;
    if (!await confirmDialog(`'${source.sourceName}' 뉴스 소스를 삭제할까요? 수집이 중단됩니다.`) || !mounted.current) return;
    setSaving(true); setError('');
    try {
      await accountApi.newsSources.remove(accountId, source.sourceId, { expectedUserId: userId });
      if (mounted.current) changed();
    } catch (failure) {
      failed(failure, '뉴스 소스를 삭제하지 못했습니다.');
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
      {!form && <button type="button" disabled={saving || sources === null}
        onClick={() => { setError(''); setForm({ ...EMPTY_FORM, sourceName: accountName }); }}
        className="text-xs font-semibold text-primary disabled:opacity-40">뉴스 소스 추가</button>}
    </div>
    {form && <div className="mt-2 grid gap-2 rounded-lg border border-border-subtle p-3 sm:grid-cols-2">
      <label className="text-xs text-text-secondary sm:col-span-2">이름 *
        <input value={form.sourceName} maxLength={200} onChange={e => setForm({ ...form, sourceName: e.target.value })} className={inputClass} /></label>
      <label className="text-xs text-text-secondary">검색어 (줄 또는 쉼표로 구분, 비우면 어카운트 이름과 별칭)
        <textarea value={form.queries} rows={3} onChange={e => setForm({ ...form, queries: e.target.value })} className={inputClass} /></label>
      <label className="text-xs text-text-secondary">별칭 (줄 또는 쉼표로 구분)
        <textarea value={form.aliases} rows={3} onChange={e => setForm({ ...form, aliases: e.target.value })} className={inputClass} /></label>
      <div className="flex gap-3 sm:col-span-2">
        <button type="button" disabled={saving || !form.sourceName.trim()} onClick={() => void create()}
          className="rounded bg-primary px-3 py-1.5 text-sm font-semibold text-white disabled:opacity-40">{saving ? '저장 중…' : '저장'}</button>
        <button type="button" disabled={saving} onClick={() => { setForm(null); setError(''); }} className="text-sm text-text-secondary">취소</button>
      </div>
    </div>}
    {error && <p role="alert" className="mt-2 text-xs text-error">{error}</p>}
  </div>;
}
