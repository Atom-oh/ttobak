'use client';

import { useEffect, useId, useMemo, useRef, useState } from 'react';
import type { AccountSummary } from '@/types/meeting';
import { accountSelectionChips, accountSubtreeIds, buildAccountTree, flattenAccountTree, MAX_ACCOUNT_FILTERS } from '@/lib/accountTree';

interface Props {
  accounts: AccountSummary[];
  selectedIds: string[];
  onChange: (ids: string[]) => void;
  disabled?: boolean;
  loading?: boolean;
}

function TreeCheckbox({ label, checked, partial, onChange }: {
  label: string; checked: boolean; partial: boolean; onChange: () => void;
}) {
  const ref = useRef<HTMLInputElement>(null);
  useEffect(() => { if (ref.current) ref.current.indeterminate = partial; }, [partial]);
  return <input ref={ref} type="checkbox" checked={checked} aria-label={label} aria-checked={partial ? 'mixed' : checked}
    onChange={onChange} className="h-4 w-4 shrink-0 accent-primary" />;
}

export function AccountTreePicker({ accounts, selectedIds, onChange, disabled, loading }: Props) {
  const [open, setOpen] = useState(false);
  const [search, setSearch] = useState('');
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set());
  const [limitErrorFor, setLimitErrorFor] = useState<string | null>(null);
  const container = useRef<HTMLDivElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  const panelId = useId();
  const tree = useMemo(() => buildAccountTree(accounts), [accounts]);
  const rows = useMemo(() => flattenAccountTree(tree), [tree]);
  const chips = useMemo(() => accountSelectionChips(tree, selectedIds), [tree, selectedIds]);
  const selected = new Set(selectedIds);
  const selectionKey = selectedIds.join(',');
  const query = search.trim().toLocaleLowerCase('ko');
  const visible = new Set<string>();
  if (query) {
    for (const { node, ancestorIds } of rows) {
      if (node.name.toLocaleLowerCase('ko').includes(query)) {
        [...ancestorIds, ...accountSubtreeIds(node)].forEach(id => visible.add(id));
      }
    }
  }

  useEffect(() => {
    if (!open) return;
    const outside = (event: PointerEvent) => {
      if (!container.current?.contains(event.target as Node)) setOpen(false);
    };
    const keydown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') { setOpen(false); trigger.current?.focus(); }
    };
    document.addEventListener('pointerdown', outside);
    document.addEventListener('keydown', keydown);
    return () => {
      document.removeEventListener('pointerdown', outside);
      document.removeEventListener('keydown', keydown);
    };
  }, [open]);

  const toggle = (ids: string[]) => {
    const next = new Set(selectedIds);
    if (ids.every(id => next.has(id))) ids.forEach(id => next.delete(id));
    else ids.forEach(id => next.add(id));
    if (next.size > MAX_ACCOUNT_FILTERS) {
      setLimitErrorFor(selectionKey);
      return;
    }
    setLimitErrorFor(null);
    onChange([...next].sort());
  };

  return (
    <div className="min-w-0 max-w-full space-y-2">
      <div ref={container} className="relative">
        <button ref={trigger} type="button" disabled={disabled} aria-expanded={open} aria-controls={panelId} aria-busy={loading}
          onClick={() => { setOpen(!open); setLimitErrorFor(null); }}
          className="inline-flex max-w-full items-center gap-2 rounded-lg border border-slate-200 bg-slate-50 px-3 py-1.5 text-sm text-slate-700 focus:outline-none focus:ring-2 focus:ring-primary/30 disabled:opacity-50 dark:border-white/10 dark:bg-surface-lowest dark:text-text-secondary">
          <span className="material-symbols-outlined text-lg" aria-hidden="true">account_tree</span>
          {selectedIds.length ? `어카운트 ${selectedIds.length}개` : '전체 어카운트'}
          <span className="material-symbols-outlined text-base" aria-hidden="true">expand_more</span>
        </button>
        {open && (
          <div id={panelId} className="absolute left-0 top-full z-40 mt-2 w-80 max-w-[calc(100vw-3rem)] rounded-xl border border-slate-200 bg-white p-3 shadow-xl sm:left-auto sm:right-0 dark:border-white/10 dark:bg-surface-low">
            <input autoFocus value={search} onChange={event => setSearch(event.target.value)} aria-label="어카운트 검색"
              placeholder="그룹 또는 어카운트 검색"
              className="mb-2 w-full rounded-lg border border-slate-200 bg-transparent px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-primary/30 dark:border-white/10" />
            <div className="mb-2 flex items-center justify-between text-xs text-slate-500 dark:text-text-muted">
              <span>그룹 선택 시 하위 어카운트 포함</span>
              <button type="button" disabled={!selectedIds.length} onClick={() => { setLimitErrorFor(null); onChange([]); }}
                className="font-semibold text-primary disabled:opacity-40">전체 해제</button>
            </div>
            <div className="max-h-72 overflow-y-auto" role="group" aria-label="어카운트 선택">
              {rows.filter(row => query ? visible.has(row.node.accountId) : !row.ancestorIds.some(id => collapsed.has(id))).map(({ node, depth }) => {
                const ids = accountSubtreeIds(node);
                const checked = ids.every(id => selected.has(id));
                const partial = !checked && ids.some(id => selected.has(id));
                return (
                  <div key={node.accountId} className="flex min-h-9 items-center gap-2 rounded-lg py-1 pr-2 hover:bg-slate-50 dark:hover:bg-white/5" style={{ paddingLeft: Math.min(depth, 8) * 12 }}>
                    {node.children.length ? (
                      <button type="button" disabled={Boolean(query)} aria-label={`${node.name} ${collapsed.has(node.accountId) ? '펼치기' : '접기'}`}
                        aria-expanded={Boolean(query) || !collapsed.has(node.accountId)}
                        onClick={() => setCollapsed(prev => { const next = new Set(prev); if (next.has(node.accountId)) next.delete(node.accountId); else next.add(node.accountId); return next; })}
                        className="flex h-6 w-6 shrink-0 items-center justify-center text-slate-400">
                        <span className="material-symbols-outlined text-lg" aria-hidden="true">{!query && collapsed.has(node.accountId) ? 'chevron_right' : 'expand_more'}</span>
                      </button>
                    ) : <span className="w-6 shrink-0" />}
                    <TreeCheckbox label={node.children.length ? `${node.name} 및 하위 어카운트` : node.name} checked={checked} partial={partial} onChange={() => toggle(ids)} />
                    <button type="button" onClick={() => toggle(ids)} className="min-w-0 flex-1 truncate text-left text-sm text-slate-700 dark:text-text-secondary" title={node.name}>{node.name}</button>
                  </div>
                );
              })}
              {(!rows.length || (query && !visible.size)) && <p className="py-6 text-center text-sm text-slate-500">{loading ? '불러오는 중…' : query ? '검색 결과가 없습니다.' : '선택할 어카운트가 없습니다.'}</p>}
            </div>
            {limitErrorFor === selectionKey && <p role="alert" className="mt-2 text-xs text-red-600 dark:text-red-400">한 번에 최대 {MAX_ACCOUNT_FILTERS}개 어카운트를 선택할 수 있습니다.</p>}
          </div>
        )}
      </div>
      {chips.length > 0 && (
        <div className="flex max-w-full flex-wrap gap-1.5" aria-label="선택한 어카운트">
          {chips.map(chip => (
            <button key={chip.key} type="button" onClick={() => { const remove = new Set(chip.ids); onChange(selectedIds.filter(id => !remove.has(id))); }}
              aria-label={`${chip.label} 필터 해제`} className="inline-flex max-w-full items-center gap-1 rounded-full bg-primary/10 px-2.5 py-1 text-xs font-medium text-primary">
              <span className="truncate">{chip.label}</span><span aria-hidden="true">×</span>
            </button>
          ))}
        </div>
      )}
    </div>
  );
}
