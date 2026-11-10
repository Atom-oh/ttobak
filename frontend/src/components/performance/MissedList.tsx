'use client';

import Link from 'next/link';
import { useMemo } from 'react';
import { groupMissedItems, missedItemDetail, missedItemHref, type PerformanceMissedItem, type PerformanceMissedList } from '@/lib/performance';

function MissedGroup({ title, icon, empty, items, accountNames }: {
  title: string;
  icon: string;
  empty: string;
  items: PerformanceMissedItem[];
  accountNames: Map<string, string>;
}) {
  return (
    <div className="min-w-0">
      <h4 className="mb-2 flex items-center gap-2 text-sm font-semibold text-slate-700 dark:text-text-secondary">
        <span className="material-symbols-outlined text-lg text-primary" aria-hidden="true">{icon}</span>
        {title}
        <span className="rounded-full bg-primary/10 px-2 py-0.5 text-xs font-semibold text-primary">{items.length}</span>
      </h4>
      {items.length === 0 ? (
        <p className="rounded-lg bg-slate-50 px-3 py-4 text-sm text-slate-500 dark:bg-white/5 dark:text-text-muted">{empty}</p>
      ) : (
        <ul className="divide-y divide-slate-100 dark:divide-white/5">
          {items.map((item, index) => (
            <li key={`${index}:${item.kind}:${item.accountId}:${item.refKind ?? ''}:${item.refId ?? ''}:${item.sfdcAccountId ?? ''}`}>
              <Link href={missedItemHref(item)} className="flex min-w-0 items-center justify-between gap-3 rounded-lg px-2 py-2.5 hover:bg-slate-50 dark:hover:bg-white/5">
                <span className="min-w-0 truncate text-sm font-medium text-slate-800 dark:text-text-main">
                  {accountNames.get(item.accountId) ?? item.accountId}
                </span>
                <span className="shrink-0 text-xs text-slate-500 dark:text-text-muted">{missedItemDetail(item)}</span>
              </Link>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

/** Section B: logging gaps and silent customers. Unmapped data is counted, never guessed. */
export function MissedList({ list, accountNames }: { list: PerformanceMissedList; accountNames: Map<string, string> }) {
  const groups = useMemo(() => groupMissedItems(list.items), [list.items]);
  return (
    <div className="space-y-6">
      <div className="grid gap-6 md:grid-cols-2">
        <MissedGroup title="SFDC 로깅 누락" icon="edit_note" empty="이 기간에 로깅 누락이 없습니다."
          items={groups.loggingGaps} accountNames={accountNames} />
        <MissedGroup title="연락 끊긴 고객" icon="history" empty="60일 넘게 연락이 끊긴 고객이 없습니다."
          items={groups.silentCustomers} accountNames={accountNames} />
      </div>
      {list.unmappedCount > 0 && (
        <p className="text-xs text-slate-500 dark:text-text-muted">
          어카운트에 연결되지 않은 활동·일정 {list.unmappedCount.toLocaleString()}건은 추정하지 않고 목록에서 제외했습니다. 아래 매핑 대기열에서 SFDC 계정을 연결하세요.
        </p>
      )}
    </div>
  );
}
