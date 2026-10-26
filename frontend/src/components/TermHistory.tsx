'use client';

import { useCallback, useEffect, useState } from 'react';
import { ApiError, dictionaryApi } from '@/lib/api';
import { dictionaryErrorMessage, type DictionaryLayer, type DictTermItem, type TermLogEntry } from '@/lib/dictionary';

const ACTION_LABELS: Record<string, string> = { update: '수정', delete: '삭제', restore: '복원' };
const btn = 'px-2 py-1 text-xs font-medium rounded-lg hover:bg-slate-100 dark:hover:bg-white/5 disabled:opacity-40';

const summary = (t?: DictTermItem) => (t ? [t.displayAs, t.heardAs.length ? `(${t.heardAs.join(', ')})` : ''].filter(Boolean).join(' ') : '-');

interface Props {
  layer: DictionaryLayer;
  term: DictTermItem;
  onClose: () => void;
  /** Called after a successful restore so the list can reload. */
  onRestored: () => void;
}

/** Change history of one Common term, newest first, with 복원. */
export function TermHistory({ layer, term, onClose, onRestored }: Props) {
  const [entries, setEntries] = useState<TermLogEntry[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [restoring, setRestoring] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      const { items } = await dictionaryApi.termLog(layer, term.termId);
      setEntries(items ?? []);
    } catch (err) {
      setError(err instanceof Error ? err.message : '기록을 불러오지 못했습니다.');
    }
  }, [layer, term.termId]);

  useEffect(() => { void load(); }, [load]);

  const restore = async (entry: TermLogEntry) => {
    if (restoring) return;
    setRestoring(entry.logId);
    setError(null);
    try {
      await dictionaryApi.restore(layer, term.termId, entry.logId);
      onRestored();
      await load();
    } catch (err) {
      setError(err instanceof ApiError ? dictionaryErrorMessage(err.code, err.message) ?? err.message : '복원하지 못했습니다.');
    } finally {
      setRestoring(null);
    }
  };

  return (
    <div className="glass-panel rounded-xl p-4 space-y-3">
      <div className="flex items-center justify-between">
        <h4 className="text-sm font-semibold text-slate-900 dark:text-text-main">기록: {term.displayAs}</h4>
        <button onClick={onClose} className={btn}>닫기</button>
      </div>
      {error && <p className="text-xs text-red-600 dark:text-red-400">{error}</p>}
      {entries === null && !error && <p className="text-xs text-slate-500 dark:text-text-muted">불러오는 중</p>}
      {entries?.length === 0 && <p className="text-xs text-slate-500 dark:text-text-muted">기록이 없습니다.</p>}
      <ul className="divide-y divide-slate-100 dark:divide-white/5">
        {entries?.map((e) => (
          <li key={e.logId} className="py-2 flex flex-wrap items-center gap-2 text-xs text-slate-600 dark:text-text-secondary">
            <span className="font-semibold">{ACTION_LABELS[e.action] ?? e.action}</span>
            <span>{new Date(e.at).toLocaleString('ko-KR')}</span>
            <span className="text-slate-500 dark:text-text-muted">{e.actor}</span>
            <span className="flex-1 min-w-[8rem]">{summary(e.before)} → {summary(e.after)}</span>
            {e.before && (
              <button onClick={() => restore(e)} disabled={restoring !== null} className={`${btn} text-primary`}>복원</button>
            )}
          </li>
        ))}
      </ul>
    </div>
  );
}
