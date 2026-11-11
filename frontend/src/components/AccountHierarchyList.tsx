'use client';

import { useMemo, useState } from 'react';
import type { AccountSummary } from '@/types/meeting';
import { buildAccountTree, flattenAccountTree } from '@/lib/accountTree';

const INHERITED_READ_LABEL = '상위 공유 · 읽기 전용';

/** Customer selector: the visible Account hierarchy in Korean name order. */
export function AccountHierarchyList({ accounts, selectedId, onSelect }: {
  accounts: AccountSummary[];
  selectedId: string;
  onSelect: (accountId: string) => void;
}) {
  const rows = useMemo(() => flattenAccountTree(buildAccountTree(accounts)), [accounts]);
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set());
  const [revealedId, setRevealedId] = useState('');
  // Reveal the selected row once per selection; later collapses stay user-controlled.
  const selectedAncestors = rows.find(row => row.node.accountId === selectedId)?.ancestorIds ?? [];
  if (revealedId !== selectedId && selectedAncestors.length > 0) {
    setRevealedId(selectedId);
    if (selectedAncestors.some(id => collapsed.has(id))) {
      setCollapsed(prev => new Set([...prev].filter(id => !selectedAncestors.includes(id))));
    }
  }
  const toggle = (accountId: string) => setCollapsed(prev => {
    const next = new Set(prev);
    if (next.has(accountId)) next.delete(accountId); else next.add(accountId);
    return next;
  });

  return (
    <ul className="flex flex-col gap-0.5">
      {rows.filter(row => !row.ancestorIds.some(id => collapsed.has(id))).map(({ node, depth, path }) => {
        const selected = node.accountId === selectedId;
        const isCollapsed = collapsed.has(node.accountId);
        const inheritedRead = Boolean(node.inheritedFromAccountId) && node.accessPermission === 'read';
        return (
          <li key={node.accountId} className="flex min-w-0 items-center gap-0.5" style={{ paddingLeft: Math.min(depth, 8) * 14 }}>
            {node.children.length ? (
              <button type="button" aria-expanded={!isCollapsed} aria-label={`${node.name} ${isCollapsed ? '펼치기' : '접기'}`}
                onClick={() => toggle(node.accountId)}
                className="flex h-6 w-6 shrink-0 items-center justify-center rounded-md text-slate-500 hover:bg-slate-100 focus-visible:outline-2 focus-visible:outline-primary dark:text-text-muted dark:hover:bg-white/10">
                <span className="material-symbols-outlined text-lg" aria-hidden="true">{isCollapsed ? 'chevron_right' : 'expand_more'}</span>
              </button>
            ) : <span className="w-6 shrink-0" aria-hidden="true" />}
            <button type="button" data-account-id={node.accountId} title={path} aria-current={selected ? 'true' : undefined}
              onClick={() => onSelect(node.accountId)}
              className={`flex min-w-0 flex-1 items-center gap-2 rounded-lg px-2 py-1.5 text-left text-sm font-medium transition-colors focus-visible:outline-2 focus-visible:outline-primary ${
                selected
                  ? 'bg-primary/10 text-primary dark:bg-primary/15'
                  : 'text-slate-600 hover:bg-slate-100 dark:text-text-secondary dark:hover:bg-white/5 dark:hover:text-text-main'
              }`}>
              <span className="material-symbols-outlined shrink-0 text-lg" aria-hidden="true">{node.children.length ? 'account_tree' : 'corporate_fare'}</span>
              <span className="min-w-0 flex-1 truncate">{node.name}</span>
              {inheritedRead && (
                <span className="material-symbols-outlined shrink-0 text-base text-slate-400 dark:text-text-muted" role="img"
                  aria-label={INHERITED_READ_LABEL} title={INHERITED_READ_LABEL}>visibility</span>
              )}
              {node.children.length > 0 && <span className="shrink-0 text-xs text-slate-400 dark:text-text-muted">{node.children.length}</span>}
            </button>
          </li>
        );
      })}
    </ul>
  );
}
