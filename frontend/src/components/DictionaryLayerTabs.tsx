'use client';

import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react';
import { accountApi, ApiError, dictionaryApi } from '@/lib/api';
import {
  claimInFlight, dictionaryErrorMessage, findDuplicateTerm, LAYER_TABS, layerAllowsCreate, normalizeTermKey, parseHeardAs,
  resolveLayer, shouldReloadAfterError, type DictionaryLayer, type DictTermItem, type LayerTab, type TermPatch,
} from '@/lib/dictionary';
import type { AccountSummary } from '@/types/meeting';
import { DictionaryTable } from './DictionaryTable';
import { TermHistory } from './TermHistory';

const field = 'w-full px-3 py-2 text-sm bg-slate-100 dark:bg-surface-lowest dark:border dark:border-white/10 dark:text-text-main border-none rounded-lg focus:ring-2 focus:ring-primary/20';

/** One shared (Account or Common) layer: list, edit, delete, and for Common its history. */
function LayerDictionary({ layer }: { layer: DictionaryLayer }) {
  const [items, setItems] = useState<DictTermItem[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busyTermIds, setBusyTermIds] = useState<ReadonlySet<string>>(new Set());
  const inFlight = useRef(new Set<string>());
  const [history, setHistory] = useState<DictTermItem | null>(null);
  const [newDisplayAs, setNewDisplayAs] = useState('');
  const [newHeardAs, setNewHeardAs] = useState('');
  const seq = useRef(0);

  const load = useCallback(async () => {
    const mine = ++seq.current;
    try {
      const { items: rows } = await dictionaryApi.listLayer(layer);
      if (mine === seq.current) { setItems(rows ?? []); setError(null); }
    } catch (err) {
      if (mine === seq.current) { setItems((prev) => prev ?? []); setError(err instanceof Error ? err.message : '사전을 불러오지 못했습니다.'); }
    }
  }, [layer]);

  useEffect(() => {
    setItems(null); setHistory(null); setError(null);
    void load();
    const counter = seq;
    return () => { counter.current++; };
  }, [load]);

  const mutate = async (key: string, run: () => Promise<unknown>, done: () => void) => {
    if (!claimInFlight(inFlight.current, key)) return false;
    setBusyTermIds(new Set(inFlight.current));
    setError(null);
    try {
      await run();
      done();
      return true;
    } catch (err) {
      if (err instanceof ApiError && shouldReloadAfterError(err.code)) await load();
      setError(err instanceof ApiError ? dictionaryErrorMessage(err.code, err.message) ?? err.message : '요청을 처리하지 못했습니다. 다시 시도해 주세요.');
      return false;
    } finally {
      inFlight.current.delete(key);
      setBusyTermIds(new Set(inFlight.current));
    }
  };

  const onUpdate = (item: DictTermItem, patch: TermPatch) =>
    mutate(item.termId, () => dictionaryApi.updateLayerTerm(layer, item.termId, patch, item.version), () => void load());
  const onDelete = (item: DictTermItem) =>
    void mutate(item.termId, () => dictionaryApi.deleteLayerTerm(layer, item.termId, item.version), () => {
      setItems((prev) => prev?.filter((t) => t.termId !== item.termId) ?? prev);
      setHistory((h) => (h?.termId === item.termId ? null : h));
      // Common deletes keep a history row; reload picks up the tombstone state.
    });

  const add = async () => {
    const displayAs = newDisplayAs.trim();
    if (!normalizeTermKey(displayAs)) return setError('표기에는 글자나 숫자가 있어야 합니다.');
    const dup = findDuplicateTerm(items ?? [], displayAs);
    if (dup) return setError(`이미 사전에 있는 용어입니다: ${dup.displayAs}`);
    await mutate('new', () => dictionaryApi.createLayerTerm(layer, { displayAs, heardAs: parseHeardAs(newHeardAs) }), () => {
      setNewDisplayAs(''); setNewHeardAs('');
      void load();
    });
  };

  const isCommon = layer === 'common';
  if (items === null) {
    return <div className="flex justify-center py-12"><div className="animate-spin rounded-full h-8 w-8 border-2 border-primary border-t-transparent" /></div>;
  }
  return (
    <div className="space-y-6">
      {error && <div className="p-4 bg-red-50 dark:bg-red-900/20 text-red-600 dark:text-red-400 text-sm rounded-lg">{error}</div>}
      {isCommon && (
        <p className="text-xs text-slate-500 dark:text-text-muted">
          공통 사전은 모든 사용자가 함께 쓰며, 누구나 편집·삭제할 수 있습니다. 변경은 기록되고 복원할 수 있습니다.
        </p>
      )}
      <DictionaryTable
        items={items}
        busyTermIds={busyTermIds}
        onUpdate={onUpdate}
        onDelete={onDelete}
        onError={setError}
        onHistory={isCommon ? setHistory : undefined}
        emptyText={isCommon ? '아직 공통 용어가 없습니다.' : '아직 계정 용어가 없습니다.'}
      />
      {history && (
        <TermHistory layer={layer} term={history} onClose={() => setHistory(null)} onRestored={() => void load()} />
      )}
      {layerAllowsCreate(layer) && (
        <div className="glass-panel rounded-xl p-5">
          <h4 className="text-sm font-semibold text-slate-900 dark:text-text-main mb-3">용어 추가</h4>
          <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
            <input aria-label="표기" value={newDisplayAs} onChange={(e) => setNewDisplayAs(e.target.value)} placeholder="표기 *" className={field} />
            <input aria-label="인식된 표기 (쉼표로 구분)" value={newHeardAs} onChange={(e) => setNewHeardAs(e.target.value)} placeholder="인식된 표기 (쉼표로 구분)" className={field} />
          </div>
          <button
            onClick={add}
            disabled={!newDisplayAs.trim() || busyTermIds.has('new')}
            className="mt-3 px-4 py-2 text-sm font-medium text-primary hover:bg-primary/10 rounded-lg disabled:opacity-40"
          >추가</button>
        </div>
      )}
    </div>
  );
}

/** 개인 / 계정 / 공통 tabs; `personal` is the caller's existing personal dictionary UI. */
export function DictionaryLayerTabs({ personal }: { personal: ReactNode }) {
  const [tab, setTab] = useState<LayerTab>('personal');
  const [accounts, setAccounts] = useState<AccountSummary[] | null>(null);
  const [accountId, setAccountId] = useState('');
  const [accountsError, setAccountsError] = useState(false);

  // Accounts load on first visit to the 계정 tab.
  useEffect(() => {
    if (tab !== 'account' || accounts !== null) return;
    let cancelled = false;
    accountApi.list()
      .then(({ accounts: list }) => {
        if (cancelled) return;
        setAccounts(list ?? []);
        setAccountId((cur) => cur || list?.[0]?.accountId || '');
      })
      .catch(() => { if (!cancelled) { setAccounts([]); setAccountsError(true); } });
    return () => { cancelled = true; };
  }, [tab, accounts]);

  const layer = resolveLayer(tab, accountId);

  return (
    <div className="space-y-6">
      <div role="tablist" aria-label="사전 계층" className="flex flex-wrap items-center gap-2">
        {LAYER_TABS.map((t) => (
          <button
            key={t.id}
            role="tab"
            aria-selected={tab === t.id}
            onClick={() => setTab(t.id)}
            className={`px-4 py-2 text-sm font-semibold rounded-full ${tab === t.id ? 'bg-primary text-white' : 'bg-slate-100 text-slate-600 dark:bg-white/5 dark:text-text-secondary'}`}
          >{t.label}</button>
        ))}
        {tab === 'account' && accounts && accounts.length > 0 && (
          <select
            aria-label="계정 선택"
            value={accountId}
            onChange={(e) => setAccountId(e.target.value)}
            className="px-3 py-2 text-sm rounded-lg bg-slate-100 dark:bg-surface-lowest dark:text-text-main border-none"
          >
            {accounts.map((a) => <option key={a.accountId} value={a.accountId}>{a.name}</option>)}
          </select>
        )}
      </div>

      {/* Kept mounted so personal edits and polling survive a tab switch. */}
      <div hidden={tab !== 'personal'}>{personal}</div>
      {tab === 'account' && accounts === null && (
        <div className="flex justify-center py-12"><div className="animate-spin rounded-full h-8 w-8 border-2 border-primary border-t-transparent" /></div>
      )}
      {tab === 'account' && accounts !== null && !layer && (
        <div className="glass-panel rounded-xl p-8 text-center text-sm text-slate-500 dark:text-text-muted">
          {accountsError ? '계정 목록을 불러오지 못했습니다.' : '참여 중인 계정이 없습니다.'}
        </div>
      )}
      {tab !== 'personal' && layer && <LayerDictionary key={layer} layer={layer} />}
    </div>
  );
}
