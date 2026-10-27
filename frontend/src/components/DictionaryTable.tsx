'use client';

import { useState } from 'react';
import Link from 'next/link';
import { startTermEdit, termEditPatch, termSource, termSourceBadges, termTypeLabel, type DictTermItem, type TermEdit, type TermPatch } from '@/lib/dictionary';

const input = 'w-full px-2 py-1.5 text-sm bg-slate-100 dark:bg-surface-lowest dark:border dark:border-white/10 dark:text-text-main border-none rounded-lg';
const btn = 'px-2 py-1 text-xs font-medium rounded-lg hover:bg-slate-100 dark:hover:bg-white/5 disabled:opacity-40';
const cell = 'px-4 py-3 text-slate-600 dark:text-text-secondary';
const chip = 'ml-2 text-[11px] font-semibold px-1.5 py-0.5 rounded';

interface Props {
  items: DictTermItem[];
  busyTermIds: ReadonlySet<string>;
  onUpdate: (item: DictTermItem, patch: TermPatch) => Promise<boolean>;
  onDelete: (item: DictTermItem) => void;
  onError: (message: string) => void;
  /** Shows a 기록 button (Common rows). */
  onHistory?: (item: DictTermItem) => void;
  emptyText: string;
}

/** v2 term list: 표기 / 인식된 표기 / 유형 / 출처 plus row actions. */
export function DictionaryTable({ items, busyTermIds, onUpdate, onDelete, onError, onHistory, emptyText }: Props) {
  // The snapshot, not the polled row, is what save() diffs and versions.
  const [edit, setEdit] = useState<TermEdit | null>(null);

  const save = async (current: TermEdit) => {
    const result = termEditPatch(current);
    if ('error' in result) return onError(result.error);
    if (Object.keys(result.patch).length === 0 || (await onUpdate(current.base, result.patch))) {
      // Keep an editor opened on another row while this save was in flight.
      setEdit((e) => (e?.base.termId === current.base.termId ? null : e));
    }
  };

  if (items.length === 0) {
    return <div className="glass-panel rounded-xl p-8 text-center text-sm text-slate-500 dark:text-text-muted">{emptyText}</div>;
  }

  return (
    <div className="glass-panel rounded-xl overflow-x-auto">
      <table className="w-full text-sm">
        <thead>
          <tr className="bg-slate-50 dark:bg-surface-lowest border-b border-slate-200 dark:border-white/10 text-left text-slate-700 dark:text-text-secondary">
            {['표기', '인식된 표기', '유형', '출처'].map((h) => <th key={h} className="px-4 py-3 font-semibold">{h}</th>)}
            <th className="px-4 py-3"><span className="sr-only">작업</span></th>
          </tr>
        </thead>
        <tbody>
          {items.map((item) => {
            const busy = busyTermIds.has(item.termId);
            const source = termSource(item);
            const badges = termSourceBadges(item);
            const badgeChips = badges.map((b) => (
              <span key={b.kind} className={`${chip} bg-slate-100 text-slate-600 dark:bg-white/5 dark:text-text-muted`}>{b.label}</span>
            ));
            if (edit && edit.base.termId === item.termId) {
              return (
                <tr key={item.termId} className="border-b border-slate-100 dark:border-white/5 last:border-b-0">
                  <td className="px-4 py-2"><input aria-label="표기" value={edit.displayAs} onChange={(e) => setEdit({ ...edit, displayAs: e.target.value })} className={input} /></td>
                  <td className="px-4 py-2"><input aria-label="인식된 표기 (쉼표로 구분)" value={edit.heardAs} onChange={(e) => setEdit({ ...edit, heardAs: e.target.value })} className={input} /></td>
                  <td className={cell}>{item.type ? termTypeLabel(item.type) : '-'}</td>
                  <td className={cell}>{source.label}{badgeChips}</td>
                  <td className="px-4 py-2 text-right whitespace-nowrap">
                    <button onClick={() => save(edit)} disabled={busy} className={`${btn} text-primary`}>저장</button>
                    <button onClick={() => setEdit(null)} disabled={busy} className={btn}>취소</button>
                  </td>
                </tr>
              );
            }
            return (
              <tr key={item.termId} className="border-b border-slate-100 dark:border-white/5 last:border-b-0">
                <td className="px-4 py-3 font-medium text-slate-900 dark:text-text-main">
                  {item.displayAs}
                  {item.state === 'suggested' && <span className={`${chip} bg-amber-100 text-amber-700 dark:bg-amber-500/10 dark:text-amber-400`}>제안</span>}
                  {item.aliasMode === 'always' && <span className={`${chip} bg-slate-100 text-slate-600 dark:bg-white/5 dark:text-text-muted`}>유지</span>}
                </td>
                <td className={cell}>{item.heardAs.join(', ') || '-'}</td>
                <td className={cell}>{item.type ? termTypeLabel(item.type) : '-'}</td>
                <td className={cell}>
                  {source.meetingId
                    ? <Link href={`/meeting/${encodeURIComponent(source.meetingId)}`} className="text-primary hover:underline">{source.label}</Link>
                    : source.label}
                  {badgeChips}
                </td>
                <td className="px-4 py-3 text-right whitespace-nowrap">
                  {item.state === 'suggested' && (
                    <button onClick={() => onUpdate(item, { state: 'active' })} disabled={busy} className={`${btn} text-primary`}>추가</button>
                  )}
                  {item.state === 'active' && item.aliasMode !== 'always' && (
                    <button onClick={() => onUpdate(item, { aliasMode: 'always' })} disabled={busy} className={btn}>유지</button>
                  )}
                  <button
                    onClick={() => setEdit(startTermEdit(item))}
                    disabled={busy}
                    className={btn}
                  >편집</button>
                  {onHistory && <button onClick={() => onHistory(item)} className={btn}>기록</button>}
                  <button onClick={() => onDelete(item)} disabled={busy} className={`${btn} hover:text-red-500`}>삭제</button>
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}
