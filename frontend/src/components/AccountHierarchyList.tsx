'use client';

import Link from 'next/link';
import { useMemo, useState } from 'react';
import type { AccountSummary } from '@/types/meeting';
import { buildAccountTree, flattenAccountTree } from '@/lib/accountTree';

export function AccountHierarchyList({ accounts }: { accounts: AccountSummary[] }) {
  const rows = useMemo(() => flattenAccountTree(buildAccountTree(accounts)), [accounts]);
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set());
  return (
    <div className="divide-y divide-slate-200 rounded-xl border border-slate-200 bg-white dark:glass-panel dark:divide-white/5">
      {rows.filter(row => !row.ancestorIds.some(id => collapsed.has(id))).map(({ node, depth, path }) => (
        <div key={node.accountId} className="flex items-center gap-2 py-3 pr-4 hover:bg-slate-50 dark:hover:bg-white/5"
          style={{ paddingLeft: 12 + Math.min(depth, 8) * 18 }}>
          {node.children.length ? (
            <button type="button" aria-expanded={!collapsed.has(node.accountId)} aria-label={`${node.name} ${collapsed.has(node.accountId) ? '펼치기' : '접기'}`}
              onClick={() => setCollapsed(prev => { const next = new Set(prev); if (next.has(node.accountId)) next.delete(node.accountId); else next.add(node.accountId); return next; })}
              className="flex h-8 w-8 shrink-0 items-center justify-center rounded-lg text-slate-500 hover:bg-slate-100 dark:hover:bg-white/10">
              <span className="material-symbols-outlined text-xl" aria-hidden="true">{collapsed.has(node.accountId) ? 'chevron_right' : 'expand_more'}</span>
            </button>
          ) : <span className="w-8 shrink-0" />}
          <Link href={`/accounts/${encodeURIComponent(node.accountId)}`} title={path} className="flex min-w-0 flex-1 items-center gap-3 py-1">
            <span className="material-symbols-outlined shrink-0 text-primary" aria-hidden="true">{node.children.length ? 'account_tree' : 'corporate_fare'}</span>
            <span className="truncate font-medium text-slate-900 dark:text-text-main">{node.name}</span>
            {node.children.length > 0 && <span className="shrink-0 text-xs text-slate-400">{node.children.length}</span>}
          </Link>
          <span className="shrink-0 rounded-full bg-primary/10 px-2 py-1 text-xs font-semibold text-primary">{node.role}</span>
        </div>
      ))}
    </div>
  );
}
