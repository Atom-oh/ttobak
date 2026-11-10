'use client';

import Link from 'next/link';
import { useMemo } from 'react';
import { calendarCoverageNotice, groupMissedItems, loggingGapsEmptyText, MISSED_ITEM_CAP, missedGroupEmptyText, missedListMayBeCapped, missedItemDetail, missedItemHref, missedItemTitle, type PerformanceMissedItem, type PerformanceMissedList } from '@/lib/performance';

function MissedGroup({ title, icon, empty, items, accountNames }: {
  title: string;
  icon: string;
  empty: string;
  items: PerformanceMissedItem[];
  accountNames: Map<string, string> | null;
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
                <span className={`min-w-0 truncate text-sm font-medium ${accountNames?.has(item.accountId) ? 'text-slate-800 dark:text-text-main' : 'italic text-slate-500 dark:text-text-muted'}`}>
                  {missedItemTitle(item, accountNames)}
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

/**
 * Section B: logging gaps and silent customers. Unmapped data is counted, never
 * guessed. `accountNames` is null when the account list failed to load;
 * `onRetryAccounts` reloads it.
 */
export function MissedList({ list, accountNames, onRetryAccounts }: {
  list: PerformanceMissedList;
  accountNames: Map<string, string> | null;
  onRetryAccounts: () => void;
}) {
  const groups = useMemo(() => groupMissedItems(list.items), [list.items]);
  const capped = missedListMayBeCapped(list);
  return (
    <div className="space-y-6">
      {!accountNames && (
        <p role="status" className="rounded-lg bg-slate-50 px-3 py-2 text-xs text-slate-600 dark:bg-white/5 dark:text-text-muted">
          어카운트 목록을 불러오지 못해 이름 없이 표시합니다. 각 항목을 눌러 어카운트를 확인하세요.{' '}
          <button type="button" onClick={onRetryAccounts} className="font-semibold text-primary hover:underline">다시 불러오기</button>
        </p>
      )}
      <p role="note" className="text-xs text-slate-500 dark:text-text-muted">{calendarCoverageNotice(list.calendarCoverageStart)}</p>
      {capped && (
        <p role="alert" className="text-xs font-semibold text-amber-700 dark:text-amber-300">
          항목이 {MISSED_ITEM_CAP}건 표시 한도에 도달했습니다. 건수는 최소값이며, 활동이 전혀 없는 고객부터 빠졌을 수 있습니다. 기간을 좁혀 확인하세요.
        </p>
      )}
      <div className="grid gap-6 md:grid-cols-2">
        <MissedGroup title="SFDC 로깅 누락" icon="edit_note" empty={missedGroupEmptyText(loggingGapsEmptyText(list.calendarCoverageStart), capped)}
          items={groups.loggingGaps} accountNames={accountNames} />
        <MissedGroup title="연락 끊긴 고객" icon="history" empty={missedGroupEmptyText('연락이 끊긴 고객이 없습니다.', capped)}
          items={groups.silentCustomers} accountNames={accountNames} />
      </div>
      {list.unmappedCount > 0 && (
        <p className="text-xs text-slate-500 dark:text-text-muted">
          어카운트에 연결되지 않은 활동·일정 {list.unmappedCount.toLocaleString()}건은 추정하지 않고 목록에서 제외했습니다. 담당 SFDC 계정의 활동은 아래 매핑 대기열에서 그 계정을 연결하면 포함됩니다. SFDC 계정이 없는 활동과 어카운트를 찾지 못한 캘린더 일정은 대기열에 나타나지 않습니다.
        </p>
      )}
    </div>
  );
}
