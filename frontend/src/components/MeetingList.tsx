'use client';

import { useMemo, useState } from 'react';
import Link from 'next/link';
import { usePathname, useSearchParams } from 'next/navigation';
import type { AccountSummary, Meeting, MeetingListFilter } from '@/types/meeting';
import { AccountTreePicker } from '@/components/AccountTreePicker';
import { MeetingRow } from '@/components/workspace/MeetingRow';
import {
  groupMeetingRows, meetingDateBoundary, meetingPreview, meetingStatusLabels, selectMeetingRows,
  type MeetingViewOptions,
} from '@/lib/meetingWorkspace';
import {
  INTERNAL_CATEGORIES, INTERNAL_GROUP_LABEL, internalCategoryLabel, matchesCategoryFilter, meetingInternalCategory,
  type MeetingCategoryFilter,
} from '@/lib/meetingCategory';

interface MeetingListProps {
  meetings: Meeting[];
  isLoading?: boolean;
  activeTab: MeetingListFilter['tab'];
  onTabChange: (tab: MeetingListFilter['tab']) => void;
  selectedAccountIds: string[];
  onAccountChange: (accountIds: string[]) => void;
  accounts: AccountSummary[];
  isLoadingAccounts: boolean;
  accountsError: string | null;
  onRetryAccounts: () => void;
  hasMore: boolean;
  error: string | null;
  onRetry: () => void;
  onDeleteMeeting?: (meetingId: string) => void;
}

const tabs: { key: MeetingListFilter['tab']; label: string }[] = [
  { key: 'all', label: '전체 미팅' },
  { key: 'recent', label: '최근 7일' },
  { key: 'shared', label: '공유받은 미팅' },
];
const windowSize = 100;
const controlClass = 'max-w-full rounded-lg border border-border-subtle bg-surface-lowest px-3 py-2 text-xs text-text-secondary focus-visible:ring-2 focus-visible:ring-primary/50';
const actionClass = 'rounded px-2 py-1 text-xs font-medium text-primary focus-visible:ring-2 focus-visible:ring-primary/50 hover:underline';

export function MeetingList({
  meetings, isLoading, activeTab, onTabChange, selectedAccountIds, onAccountChange,
  accounts, isLoadingAccounts, accountsError, onRetryAccounts, hasMore, error,
  onRetry, onDeleteMeeting,
}: MeetingListProps) {
  const pathname = usePathname();
  const urlQuery = useSearchParams().get('q') ?? '';
  const [searchQuery, setSearchQuery] = useState(urlQuery);
  const [seenUrlQuery, setSeenUrlQuery] = useState(urlQuery);
  if (seenUrlQuery !== urlQuery) {
    setSeenUrlQuery(urlQuery);
    if (searchQuery.trim() !== urlQuery) setSearchQuery(urlQuery);
  }
  const [selectedTags, setSelectedTags] = useState<string[]>([]);
  const [categoryFilter, setCategoryFilter] = useState<MeetingCategoryFilter>('');
  const [showTagFilter, setShowTagFilter] = useState(false);
  const [sortBy, setSortBy] = useState<MeetingViewOptions['sort']>('newest');
  const [grouped, setGrouped] = useState(true);
  const [status, setStatus] = useState<Meeting['status'] | ''>('');
  const [fromDate, setFromDate] = useState('');
  const [toDate, setToDate] = useState('');
  const [filterTime, setFilterTime] = useState(() => Date.now());
  const [displayWindow, setDisplayWindow] = useState({ key: '', page: 0 });

  const loadedMeetings = useMemo(() => Array.from(new Map(meetings.map(meeting => [meeting.meetingId, meeting])).values()), [meetings]);
  const accountNames = useMemo(() => new Map(accounts.map(account => [account.accountId, account.name])), [accounts]);
  const allTags = useMemo(() => {
    const tagSet = new Set(selectedTags);
    loadedMeetings.forEach(meeting => meeting.tags?.forEach(tag => tagSet.add(tag)));
    return Array.from(tagSet).sort();
  }, [loadedMeetings, selectedTags]);
  const hasLocalFilters = Boolean(selectedTags.length || searchQuery.trim() || status || fromDate || toDate || categoryFilter);
  const hasActiveFilters = hasLocalFilters || selectedAccountIds.length > 0 || activeTab === 'recent';

  const filteredMeetings = useMemo(() => {
    const localFrom = meetingDateBoundary(fromDate, 'from');
    const recentFrom = activeTab === 'recent' ? filterTime - 7 * 24 * 60 * 60 * 1000 : undefined;
    const from = recentFrom === undefined ? localFrom
      : new Date(Math.max(recentFrom, localFrom ? Date.parse(localFrom) : recentFrom)).toISOString();
    return selectMeetingRows(loadedMeetings, {
      sort: sortBy, query: searchQuery, statuses: status ? [status] : [],
      from, to: meetingDateBoundary(toDate, 'to'),
    }).filter(meeting => !selectedTags.length || meeting.tags?.some(tag => selectedTags.includes(tag)))
      .filter(meeting => matchesCategoryFilter(meeting, categoryFilter));
  }, [loadedMeetings, sortBy, searchQuery, status, fromDate, toDate, activeTab, filterTime, selectedTags, categoryFilter]);

  const viewKey = JSON.stringify([sortBy, grouped, searchQuery, status, fromDate, toDate, activeTab, selectedTags, selectedAccountIds, categoryFilter]);
  const lastPage = Math.max(0, Math.ceil(filteredMeetings.length / windowSize) - 1);
  const page = displayWindow.key === viewKey ? Math.min(displayWindow.page, lastPage) : 0;
  if (displayWindow.key !== viewKey || displayWindow.page !== page) {
    setDisplayWindow({ key: viewKey, page });
  }
  const start = page * windowSize;
  const visibleMeetings = filteredMeetings.slice(start, start + windowSize);
  const groups = grouped ? groupMeetingRows(visibleMeetings)
    : visibleMeetings.length ? [{ accountId: null, category: null, meetings: visibleMeetings }] : [];
  const customerGroups = groups.filter(group => !group.category);
  const internalGroups = groups.filter(group => group.category);
  const accountName = (accountId: string | null) => accountId
    ? accountNames.get(accountId) ?? '어카운트 정보 없음' : '고객 미지정';
  const meetingContext = (meeting: Meeting) => {
    const category = meetingInternalCategory(meeting);
    return category ? `${INTERNAL_GROUP_LABEL} · ${internalCategoryLabel(category)}` : accountName(meeting.accountId || null);
  };
  const updateSearchQuery = (next: string) => {
    setSearchQuery(next);
    const params = new URLSearchParams(window.location.search);
    if (next) params.set('q', next);
    else params.delete('q');
    const query = params.toString();
    window.history.replaceState(null, '', `${pathname}${query ? `?${query}` : ''}${window.location.hash}`);
  };
  const clearLocalFilters = () => {
    updateSearchQuery('');
    setSelectedTags([]);
    setStatus('');
    setFromDate('');
    setToDate('');
    setCategoryFilter('');
  };

  return (
    <div className="space-y-4 px-4 lg:px-0">
      <div className="flex flex-wrap items-center justify-between gap-3 border-b border-border-subtle">
        <div className="flex flex-wrap gap-4">
          {tabs.map(tab => (
            <button key={tab.key} type="button" aria-pressed={activeTab === tab.key}
              onClick={() => { setFilterTime(Date.now()); onTabChange(tab.key); }}
              className={`border-b-2 pb-3 text-xs font-medium focus-visible:ring-2 focus-visible:ring-primary/50 ${activeTab === tab.key ? 'border-primary text-primary' : 'border-transparent text-text-secondary hover:text-text-main'}`}>
              {tab.label}
            </button>
          ))}
        </div>
        <Link href="/record" className="mb-2 inline-flex items-center gap-1.5 rounded-lg px-2 py-1 text-xs font-medium text-primary focus-visible:outline-2 focus-visible:outline-primary">
          <span aria-hidden="true" className="material-symbols-outlined !text-base">mic</span>새 미팅 녹음
        </Link>
      </div>

      <div className="space-y-3">
        <div className="flex flex-wrap items-center gap-2">
          <label className="flex min-w-40 flex-1 items-center gap-2 rounded-lg border border-border-subtle bg-surface-lowest px-3 py-2 focus-within:ring-2 focus-within:ring-primary/30">
            <span aria-hidden="true" className="material-symbols-outlined !text-base text-text-secondary">search</span>
            <input aria-label="미팅 검색" value={searchQuery} onChange={event => updateSearchQuery(event.target.value)}
              placeholder="미팅 제목, 요약, 태그 검색"
              className="w-full min-w-0 bg-transparent text-xs text-text-main outline-none placeholder:text-text-secondary" />
          </label>
          <select value={status} onChange={event => setStatus(event.target.value as Meeting['status'] | '')} aria-label="미팅 상태" className={controlClass}>
            <option value="">모든 상태</option>
            {Object.entries(meetingStatusLabels).map(([value, label]) => <option key={value} value={value}>{label}</option>)}
          </select>
          <select value={categoryFilter} onChange={event => setCategoryFilter(event.target.value as MeetingCategoryFilter)} aria-label="미팅 분류" className={controlClass}>
            <option value="">모든 분류</option>
            <option value="customer">고객 미팅</option>
            <option value="internal">{INTERNAL_GROUP_LABEL} 전체</option>
            {INTERNAL_CATEGORIES.map(category => (
              <option key={category.value} value={category.value}>{INTERNAL_GROUP_LABEL} · {category.label}</option>
            ))}
          </select>
          <select value={sortBy} onChange={event => setSortBy(event.target.value as MeetingViewOptions['sort'])} aria-label="미팅 정렬" className={controlClass}>
            <option value="newest">미팅 일시 · 최신순</option>
            <option value="oldest">미팅 일시 · 오래된순</option>
          </select>
          <div role="group" aria-label="미팅 표시 방식" className="flex rounded-lg border border-border-subtle bg-surface p-1">
            {[{ value: true, label: '고객별', icon: 'account_tree' }, { value: false, label: '시간순', icon: 'format_list_bulleted' }].map(view => (
              <button key={view.label} type="button" aria-pressed={grouped === view.value} onClick={() => setGrouped(view.value)}
                className={`inline-flex items-center gap-1 rounded px-2 py-1 text-xs focus-visible:ring-2 focus-visible:ring-primary/50 ${grouped === view.value ? 'bg-surface-lowest font-medium text-text-main shadow-sm' : 'text-text-secondary'}`}>
                <span aria-hidden="true" className="material-symbols-outlined !text-sm">{view.icon}</span>{view.label}
              </button>
            ))}
          </div>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <AccountTreePicker accounts={accounts} selectedIds={selectedAccountIds} onChange={onAccountChange}
            loading={isLoadingAccounts} disabled={isLoadingAccounts && accounts.length === 0}
            statusId={isLoadingAccounts || accountsError || accounts.length === 0 ? 'meeting-account-status' : undefined} />
          {selectedAccountIds.length > 0 && <button type="button" onClick={() => onAccountChange([])} className={actionClass}>어카운트 해제</button>}
          <label className="flex min-w-0 items-center gap-2 text-xs text-text-secondary">
            시작<input type="date" aria-label="시작일" value={fromDate} max={toDate || undefined}
              onChange={event => setFromDate(event.target.value)} className={`${controlClass} min-w-0 dark:[color-scheme:dark]`} />
          </label>
          <label className="flex min-w-0 items-center gap-2 text-xs text-text-secondary">
            종료<input type="date" aria-label="종료일" value={toDate} min={fromDate || undefined}
              onChange={event => setToDate(event.target.value)} className={`${controlClass} min-w-0 dark:[color-scheme:dark]`} />
          </label>
          {allTags.length > 0 && (
            <button type="button" aria-label="태그 필터" aria-expanded={showTagFilter} onClick={() => setShowTagFilter(!showTagFilter)} className={controlClass}>
              태그{selectedTags.length ? ` ${selectedTags.length}` : ''}
            </button>
          )}
          {hasLocalFilters && <button type="button" onClick={clearLocalFilters} className={actionClass}>검색·필터 해제</button>}
        </div>
        {isLoadingAccounts ? (
          <p id="meeting-account-status" role="status" className="text-xs text-text-secondary">어카운트 목록을 불러오는 중…</p>
        ) : accountsError ? (
          <div id="meeting-account-status" role="alert" className="flex flex-wrap items-center gap-2 text-xs text-error">
            <span>{accountsError}</span><button type="button" onClick={onRetryAccounts} className={actionClass}>다시 시도</button>
          </div>
        ) : accounts.length === 0 ? (
          <p id="meeting-account-status" className="text-xs text-text-secondary">사용 가능한 어카운트가 없습니다.</p>
        ) : null}
        {showTagFilter && (
          <div role="group" aria-label="태그 선택" className="flex flex-wrap gap-2">
            {allTags.map(tag => (
              <button key={tag} type="button" aria-pressed={selectedTags.includes(tag)}
                onClick={() => setSelectedTags(previous => previous.includes(tag) ? previous.filter(selected => selected !== tag) : [...previous, tag])}
                className={`${controlClass} ${selectedTags.includes(tag) ? 'border-primary text-primary' : ''}`}>{tag}</button>
            ))}
            {selectedTags.length > 0 && <button type="button" onClick={() => setSelectedTags([])} className={actionClass}>태그 해제</button>}
          </div>
        )}
      </div>

      <div role="status" className="space-y-1 text-xs text-text-secondary">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <p>불러온 {loadedMeetings.length}개 미팅 · 조건 일치 {filteredMeetings.length}개</p>
          <p>{grouped ? '고객·Internal 구분 · 각 그룹 안에서 일시순' : '미팅 일시순'} · 브라우저 현지 시간</p>
        </div>
        {hasMore && <p>검색·정렬·상태·기간 필터는 불러온 결과에만 적용됩니다. Load More로 다음 서버 결과를 불러오세요.</p>}
        {(fromDate || toDate) && <p>날짜 미정 미팅도 함께 표시합니다.</p>}
      </div>

      {error && (
        <div role="alert" className="flex flex-wrap items-center gap-2 text-sm text-error">
          <span>{error}</span><button type="button" onClick={onRetry} className={actionClass}>다시 시도</button>
        </div>
      )}
      {isLoading && (
        <div role="status" className="text-xs text-text-secondary">
          {loadedMeetings.length ? '미팅 목록 새로고침 중…' : '미팅 목록을 불러오는 중…'}
          {!loadedMeetings.length && (
            <div className="mt-3 divide-y divide-border-subtle rounded-xl border border-border-subtle bg-surface-lowest">
              {[0, 1, 2].map(index => <div key={index} aria-hidden="true" className="h-20 animate-pulse bg-surface" />)}
            </div>
          )}
        </div>
      )}

      <div className="space-y-4">
        {customerGroups.map(group => (
          renderMeetingGroup({ key: group.accountId ?? 'unassigned', label: grouped ? accountName(group.accountId) : '전체 미팅',
            icon: 'business', showHeader: grouped, meetings: group.meetings, meetingContext, onDeleteMeeting })
        ))}
        {internalGroups.length > 0 && (
          <section aria-label={INTERNAL_GROUP_LABEL} className="space-y-3 rounded-xl border border-border-subtle bg-surface p-3">
            <div className="flex min-w-0 items-center gap-2 px-1">
              <span aria-hidden="true" className="material-symbols-outlined !flex !size-7 items-center justify-center rounded-lg border border-border-subtle bg-surface-lowest !text-base text-primary">groups</span>
              <h3 className="text-xs font-semibold text-text-main">{INTERNAL_GROUP_LABEL}</h3>
              <span className="shrink-0 text-[10px] text-text-secondary">
                {internalGroups.reduce((total, group) => total + group.meetings.length, 0)}개 표시
              </span>
            </div>
            {internalGroups.map(group => (
              renderMeetingGroup({ key: group.category!, label: internalCategoryLabel(group.category!),
                ariaLabel: `${INTERNAL_GROUP_LABEL} ${internalCategoryLabel(group.category!)}`,
                icon: group.category === 'internal_1on1' ? 'person' : 'groups', showHeader: true,
                meetings: group.meetings, meetingContext, onDeleteMeeting })
            ))}
          </section>
        )}
      </div>

      {!isLoading && filteredMeetings.length === 0 && !error && (
        <div className="rounded-xl border border-border-subtle bg-surface-lowest px-4 py-10 text-center">
          <span aria-hidden="true" className="material-symbols-outlined mb-2 text-text-secondary">search_off</span>
          <h3 className="text-sm font-semibold text-text-main">
            {hasMore ? '불러온 결과에 조건에 맞는 미팅이 없습니다' : hasActiveFilters ? '선택한 조건에 맞는 미팅이 없습니다' : '아직 미팅이 없습니다'}
          </h3>
          <p className="mt-2 text-xs text-text-secondary">
            {hasMore ? 'Load More로 다음 결과를 확인할 수 있습니다.' : hasActiveFilters ? '검색어 또는 필터를 변경해 보세요.' : '새 미팅을 녹음하고 기록을 시작해 보세요.'}
          </p>
          <div className="mt-3 flex flex-wrap justify-center gap-2">
            {hasLocalFilters && <button type="button" onClick={clearLocalFilters} className={actionClass}>검색·필터 해제</button>}
            {selectedAccountIds.length > 0 && <button type="button" onClick={() => onAccountChange([])} className={actionClass}>어카운트 해제</button>}
            {activeTab === 'recent' && <button type="button" onClick={() => onTabChange('all')} className={actionClass}>전체 미팅 보기</button>}
          </div>
        </div>
      )}

      {filteredMeetings.length > 0 && (
        <nav aria-label="불러온 미팅 페이지" className="flex flex-wrap items-center justify-between gap-2 text-xs text-text-secondary">
          <span>{start + 1}–{Math.min(start + windowSize, filteredMeetings.length)} / 조건 일치 {filteredMeetings.length}개</span>
          <div className="flex gap-2">
            <button type="button" disabled={page === 0} onClick={() => setDisplayWindow({ key: viewKey, page: page - 1 })}
              className={`${controlClass} disabled:cursor-default disabled:opacity-40`}>이전 100개</button>
            <button type="button" disabled={page === lastPage} onClick={() => setDisplayWindow({ key: viewKey, page: page + 1 })}
              className={`${controlClass} disabled:cursor-default disabled:opacity-40`}>다음 100개</button>
          </div>
        </nav>
      )}
    </div>
  );
}

// A plain render helper (not a component) keeps rows in the list's element tree.
function renderMeetingGroup({ key, label, ariaLabel, icon, showHeader, meetings, meetingContext, onDeleteMeeting }: {
  key: string;
  label: string;
  ariaLabel?: string;
  icon: string;
  showHeader: boolean;
  meetings: Meeting[];
  meetingContext: (meeting: Meeting) => string;
  onDeleteMeeting: MeetingListProps['onDeleteMeeting'];
}) {
  return (
    <section key={key} aria-label={ariaLabel ?? label} className="overflow-hidden rounded-xl border border-border-subtle bg-surface-lowest">
      {showHeader && (
        <div className="flex min-w-0 items-center gap-2 border-b border-border-subtle bg-surface px-4 py-3 md:px-5">
          <span aria-hidden="true" className="material-symbols-outlined !flex !size-7 items-center justify-center rounded-lg border border-border-subtle bg-surface-lowest !text-base text-primary">{icon}</span>
          <h3 className="min-w-0 truncate text-xs font-semibold text-text-main">{label}</h3>
          <span className="shrink-0 text-[10px] text-text-secondary">{meetings.length}개 표시</span>
        </div>
      )}
      <div aria-hidden="true" className="hidden grid-cols-[minmax(0,1fr)_7.5rem_5.5rem_3.5rem_2rem] gap-x-3 border-b border-border-subtle px-5 py-2 text-[10px] text-text-secondary md:grid">
        <span>미팅 · 핵심 내용</span><span>미팅 일시</span><span>상태</span><span>녹음 시간</span><span />
      </div>
      <ul>
        {meetings.map(meeting => (
          <MeetingRow key={meeting.meetingId} meeting={meeting} accountName={meetingContext(meeting)}
            preview={meetingPreview(meeting.summary ?? '')} onDelete={onDeleteMeeting} />
        ))}
      </ul>
    </section>
  );
}
