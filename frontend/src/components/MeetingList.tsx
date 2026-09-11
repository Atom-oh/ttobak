'use client';

import { useState, useRef, useEffect, useMemo } from 'react';
import Link from 'next/link';
import { useSearchParams } from 'next/navigation';
import { meetingsApi } from '@/lib/api';
import type { AccountSummary, Meeting, MeetingListFilter } from '@/types/meeting';
import { SkeletonCard } from '@/components/ui/Skeleton';
import { AccountTreePicker } from '@/components/AccountTreePicker';

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
  { key: 'all', label: 'All Notes' },
  { key: 'recent', label: 'Recent' },
  { key: 'shared', label: 'Shared' },
];

function formatDate(dateString: string): string {
  const date = new Date(dateString);
  return date.toLocaleDateString('en-US', {
    month: 'short',
    day: 'numeric',
    year: 'numeric',
  });
}

function formatTime(dateString: string): string {
  const date = new Date(dateString);
  return date.toLocaleTimeString('en-US', {
    hour: 'numeric',
    minute: '2-digit',
    hour12: true,
  });
}

function getTagColor(tag: string, variant: 'primary' | 'secondary' = 'primary'): string {
  const lightColors: Record<string, string> = {
    internal: 'bg-primary/10 text-primary',
    design: 'bg-amber-100 text-amber-700',
    external: 'bg-green-100 text-green-700',
    engineering: 'bg-emerald-50 text-emerald-600',
    marketing: 'bg-amber-50 text-amber-600',
    strategy: 'bg-primary/10 text-primary',
  };
  const lightFallback = 'bg-slate-100 text-slate-600';
  const lightClass = lightColors[tag.toLowerCase()] || lightFallback;

  // Dark mode: tint only, no outline ring — a colored border around a
  // same-hue fill reads as a glowing outline; a flat tint is the calmer version.
  if (variant === 'secondary') {
    return `${lightClass} dark:bg-accent/10 dark:text-accent`;
  }
  return `${lightClass} dark:bg-primary/10 dark:text-primary`;
}

function MeetingCard({ meeting, onDelete }: { meeting: Meeting; onDelete?: (meetingId: string) => void }) {
  const tag = meeting.tags?.[0];
  const [menuOpen, setMenuOpen] = useState(false);
  const [isDeleting, setIsDeleting] = useState(false);
  const menuRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!menuOpen) return;
    const handleClickOutside = (e: MouseEvent) => {
      if (menuRef.current && !menuRef.current.contains(e.target as Node)) {
        setMenuOpen(false);
      }
    };
    document.addEventListener('mousedown', handleClickOutside);
    return () => document.removeEventListener('mousedown', handleClickOutside);
  }, [menuOpen]);

  const handleDelete = async (e: React.MouseEvent) => {
    e.preventDefault();
    e.stopPropagation();
    setMenuOpen(false);
    if (!confirm('이 미팅을 삭제하시겠습니까?')) return;
    setIsDeleting(true);
    try {
      await meetingsApi.delete(meeting.meetingId);
      onDelete?.(meeting.meetingId);
    } catch (err) {
      console.error('Failed to delete meeting:', err);
      alert('미팅 삭제에 실패했습니다.');
    } finally {
      setIsDeleting(false);
    }
  };

  return (
    <Link href={`/meeting/${meeting.meetingId}`}>
      <div className={`glass-panel p-4 lg:p-6 rounded-xl hover:border-primary/30 lg:hover:border-slate-200 lg:dark:hover:border-primary/30 lg:hover:shadow-xl lg:hover:shadow-primary/5 transition-all cursor-pointer group ${isDeleting ? 'opacity-50 pointer-events-none' : ''}`}>
        {/* Mobile: title left, tag right */}
        <div className="flex justify-between items-start mb-2 lg:hidden">
          <h4 className="text-slate-900 dark:text-slate-100 font-bold text-base leading-tight group-hover:text-primary transition-colors">
            {meeting.title}
          </h4>
          <div className="flex items-center gap-1.5">
            {meeting.status === 'recording' && (
              <span className="text-[10px] font-bold px-2 py-0.5 rounded-full uppercase bg-red-50 dark:bg-red-900/30 text-red-600 dark:text-red-400 border border-red-100 dark:border-red-800">
                중단됨
              </span>
            )}
            {tag && (
              <span className={`text-[10px] font-bold px-2 py-0.5 rounded-full uppercase ${getTagColor(tag)}`}>
                {tag}
              </span>
            )}
          </div>
        </div>

        {/* PC: icon badge + duration (dark mode), tag + date row, title */}
        <div className="hidden dark:lg:flex items-center gap-3 mb-4">
          <div className="flex items-center justify-center size-9 rounded-lg bg-primary/10">
            <span className="material-symbols-outlined text-primary text-lg">video_chat</span>
          </div>
          {meeting.duration != null && meeting.duration > 0 && (
            <span className="text-[11px] font-bold uppercase tracking-wider text-text-muted">
              {`${Math.floor(meeting.duration / 60)}:${String(meeting.duration % 60).padStart(2, '0')} MIN`}
            </span>
          )}
        </div>
        <div className="hidden lg:flex justify-between items-start mb-4">
          <div className="flex items-center gap-2">
            {meeting.status === 'recording' && (
              <span className="text-[10px] font-bold uppercase tracking-widest px-2 py-0.5 rounded bg-red-50 dark:bg-red-900/30 text-red-600 dark:text-red-400 border border-red-100 dark:border-red-800">
                녹음 중단됨
              </span>
            )}
            {tag ? (
              <span className={`text-[10px] font-bold uppercase tracking-widest px-2 py-0.5 rounded ${getTagColor(tag)}`}>
                {tag}
              </span>
            ) : !meeting.status?.startsWith('recording') && (
              <span />
            )}
          </div>
          <span className="text-xs text-slate-400 dark:text-text-muted">{formatDate(meeting.date)}</span>
        </div>
        <h4 className="hidden lg:block text-slate-900 dark:text-text-main font-bold text-lg leading-tight group-hover:text-primary transition-colors mb-2">
          {meeting.title}
        </h4>

        {/* Mobile: date row */}
        <div className="flex items-center gap-2 text-slate-400 text-xs mb-3 lg:hidden">
          <span className="material-symbols-outlined text-[14px]">calendar_today</span>
          <span>{formatDate(meeting.date)} &bull; {formatTime(meeting.date)}</span>
        </div>

        {meeting.summary && (
          <p className="text-slate-600 dark:text-text-secondary text-sm line-clamp-2 lg:line-clamp-3 leading-relaxed mb-4">
            {meeting.summary}
          </p>
        )}

        {meeting.tags && meeting.tags.length > 1 && (
          <div className="hidden lg:flex flex-wrap gap-2 mb-4">
            {meeting.tags.slice(1).map((t, i) => (
              <span key={t} className={`text-[10px] font-bold uppercase tracking-wider px-2 py-1 rounded ${getTagColor(t, i % 2 === 0 ? 'secondary' : 'primary')}`}>
                {t}
              </span>
            ))}
          </div>
        )}

        <div className="flex items-center justify-between pt-4 border-t border-slate-100 dark:border-white/5">
          <div className="flex items-center gap-3">
            {meeting.participants && meeting.participants.length > 0 ? (
              <div className="flex -space-x-2">
                {meeting.participants.slice(0, 3).map((p, i) => (
                  <div
                    key={p.id || i}
                    className="size-6 lg:size-7 rounded-full border-2 border-white dark:border-surface bg-slate-200 dark:bg-[#1f1f25] overflow-hidden flex items-center justify-center text-[10px] font-bold text-slate-500 dark:text-text-muted"
                  >
                    {p.avatarUrl ? (
                      <img src={p.avatarUrl} alt={p.name} className="w-full h-full object-cover" />
                    ) : (
                      p.initials || p.name?.charAt(0) || '?'
                    )}
                  </div>
                ))}
                {meeting.participants.length > 3 && (
                  <div className="size-6 lg:size-7 rounded-full border-2 border-white dark:border-surface bg-slate-100 dark:bg-[#1f1f25] flex items-center justify-center text-[10px] font-bold text-slate-500 dark:text-text-muted">
                    +{meeting.participants.length - 3}
                  </div>
                )}
              </div>
            ) : (
              <div />
            )}
            {/* Attendee count — dark mode */}
            {meeting.participants && meeting.participants.length > 0 && (
              <div className="hidden dark:flex items-center gap-1 text-text-muted text-xs">
                <span className="material-symbols-outlined text-sm">person</span>
                <span>{meeting.participants.length}</span>
              </div>
            )}
          </div>
          <div className="relative" ref={menuRef}>
            <button
              onClick={(e) => { e.preventDefault(); e.stopPropagation(); setMenuOpen(!menuOpen); }}
              className="text-slate-400 hover:text-primary transition-colors"
            >
              <span className="material-symbols-outlined text-xl">more_horiz</span>
            </button>
            {menuOpen && (
              <div className="absolute right-0 bottom-full mb-1 bg-white dark:bg-[#1f1f25] border border-slate-200 dark:border-white/10 rounded-lg shadow-lg dark:shadow-[0_4px_20px_rgba(0,0,0,0.4)] z-20 min-w-[120px]">
                <button
                  onClick={handleDelete}
                  className="w-full flex items-center gap-2 px-4 py-2.5 text-sm text-red-600 dark:text-red-400 hover:bg-red-50 dark:hover:bg-red-900/20 rounded-lg transition-colors"
                >
                  <span className="material-symbols-outlined text-lg">delete</span>
                  삭제
                </button>
              </div>
            )}
          </div>
        </div>
      </div>
    </Link>
  );
}

export function MeetingList({
  meetings, isLoading, activeTab, onTabChange, selectedAccountIds, onAccountChange,
  accounts, isLoadingAccounts, accountsError, onRetryAccounts, hasMore, error,
  onRetry, onDeleteMeeting,
}: MeetingListProps) {
  // The desktop header's search box writes `?q=` (see DesktopHeader's
  // HeaderSearch); this list is what actually filters on it. URL → list is
  // one-way: the mobile bar below edits local state only and does not write
  // the URL (the two inputs are never visible at the same breakpoint).
  // Callers must render this component inside a <Suspense> boundary
  // (useSearchParams requirement under static export).
  const urlQuery = useSearchParams().get('q') ?? '';
  const [searchQuery, setSearchQuery] = useState(urlQuery);
  // Re-sync from the URL only when it actually changes (adjust-state-during-
  // render pattern, not an effect) and only if it differs from what's shown,
  // so a URL that merely echoes the current text can't rewind typing.
  const [seenUrlQuery, setSeenUrlQuery] = useState(urlQuery);
  if (seenUrlQuery !== urlQuery) {
    setSeenUrlQuery(urlQuery);
    if (searchQuery.trim() !== urlQuery) setSearchQuery(urlQuery);
  }
  const [selectedTags, setSelectedTags] = useState<string[]>([]);
  const [showTagFilter, setShowTagFilter] = useState(false);
  const [sortBy, setSortBy] = useState<'newest' | 'name'>('newest');
  const [filterTime, setFilterTime] = useState(() => Date.now());

  const allTags = useMemo(() => {
    // Keep active tags removable even when a new account/page has none of them.
    const tagSet = new Set(selectedTags);
    meetings.forEach(m => m.tags?.forEach(t => tagSet.add(t)));
    return Array.from(tagSet).sort();
  }, [meetings, selectedTags]);
  const selectedAccountLabel = selectedAccountIds.length === 1
    ? accounts.find(account => account.accountId === selectedAccountIds[0])?.name || '선택한 어카운트'
    : `${selectedAccountIds.length}개 어카운트`;
  const hasActiveFilters = Boolean(selectedAccountIds.length || selectedTags.length > 0 || searchQuery);

  const filteredMeetings = meetings.filter((meeting) => {
    // Tab-based filtering: 'recent' shows only last 7 days
    if (activeTab === 'recent') {
      const weekAgo = new Date(filterTime - 7 * 24 * 60 * 60 * 1000);
      if (new Date(meeting.date) < weekAgo) return false;
    }

    // Tag filter (OR logic: show meetings matching ANY selected tag)
    if (selectedTags.length > 0) {
      if (!meeting.tags?.some(t => selectedTags.includes(t))) return false;
    }

    if (searchQuery) {
      const query = searchQuery.toLowerCase();
      return (
        meeting.title?.toLowerCase().includes(query) ||
        meeting.summary?.toLowerCase().includes(query) ||
        meeting.tags?.some((t) => t.toLowerCase().includes(query))
      );
    }
    return true;
  });

  const sortedMeetings = [...filteredMeetings].sort((a, b) =>
    sortBy === 'name'
      ? a.title.localeCompare(b.title, 'ko')
      : new Date(b.date).getTime() - new Date(a.date).getTime()
  );

  // Group by date (newest-first only; name sort renders as a flat list)
  const groupedMeetings = sortBy === 'name'
    ? (sortedMeetings.length > 0 ? { '': sortedMeetings } : {})
    : sortedMeetings.reduce((acc, meeting) => {
        const date = new Date(meeting.date);
        const now = new Date(filterTime);
        const weekAgo = new Date(now.getTime() - 7 * 24 * 60 * 60 * 1000);

        let group = 'Older';
        if (date > weekAgo) {
          group = 'This Week';
        } else if (date > new Date(now.getTime() - 14 * 24 * 60 * 60 * 1000)) {
          group = 'Last Week';
        }

        if (!acc[group]) acc[group] = [];
        acc[group].push(meeting);
        return acc;
      }, {} as Record<string, Meeting[]>);

  return (
    <div className="space-y-4">
      {/* Search Bar — mobile only (desktop uses the header search, which
          drives the same filter through ?q=) */}
      <div className="px-4 lg:hidden">
        <label className="flex flex-col min-w-40 h-11 w-full">
          <div className="flex w-full flex-1 items-stretch rounded-xl h-full shadow-sm">
            <div className="text-slate-400 dark:text-text-muted flex bg-slate-100 dark:bg-white/5 items-center justify-center pl-4 rounded-l-xl">
              <span className="material-symbols-outlined text-[20px]">search</span>
            </div>
            <input
              value={searchQuery}
              onChange={(e) => setSearchQuery(e.target.value)}
              className="flex w-full min-w-0 flex-1 resize-none overflow-hidden rounded-r-xl text-slate-900 dark:text-text-main focus:outline-0 focus:ring-0 border-none bg-slate-100 dark:bg-white/5 placeholder:text-slate-400 dark:placeholder:text-text-muted px-3 text-sm font-medium leading-normal"
              placeholder="Search meetings, notes, or tags"
            />
          </div>
        </label>
      </div>

      {/* Tabs + Filter */}
      <div className="px-4 lg:px-0">
        <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2 border-b border-slate-200 dark:border-white/10">
          <div className="flex shrink-0 gap-6">
            {tabs.map((tab) => (
              <button
                key={tab.key}
                onClick={() => {
                  setFilterTime(Date.now());
                  onTabChange(tab.key);
                }}
                aria-pressed={activeTab === tab.key}
                className={`pb-3 border-b-2 text-sm font-semibold transition-colors ${
                  activeTab === tab.key
                    ? 'border-primary text-primary'
                    : 'border-transparent text-slate-500 hover:text-slate-700'
                }`}
              >
                {tab.label}
              </button>
            ))}
          </div>
          <div className="flex min-w-0 max-w-full flex-wrap items-center gap-x-3 gap-y-2 pb-2">
            <AccountTreePicker accounts={accounts} selectedIds={selectedAccountIds} onChange={onAccountChange}
              loading={isLoadingAccounts} disabled={isLoadingAccounts && accounts.length === 0}
              statusId={isLoadingAccounts || accountsError || accounts.length === 0 ? 'meeting-account-status' : undefined} />
            {selectedAccountIds.length > 0 && (
              <button
                onClick={() => onAccountChange([])}
                className="text-xs font-semibold text-primary hover:underline"
              >
                어카운트 해제
              </button>
            )}
            <select
              value={sortBy}
              onChange={(e) => setSortBy(e.target.value as 'newest' | 'name')}
              aria-label="미팅 정렬"
              className="px-3 py-1.5 rounded-lg text-sm bg-slate-50 dark:bg-surface-lowest border border-slate-200 dark:border-white/10 text-slate-700 dark:text-text-secondary focus:outline-none focus:ring-2 focus:ring-primary/30"
            >
              <option value="newest">최신순</option>
              <option value="name">이름순</option>
            </select>
            {allTags.length > 0 && (
              <button
                onClick={() => setShowTagFilter(!showTagFilter)}
                aria-label="태그 필터"
                aria-expanded={showTagFilter}
                className={`flex items-center gap-1.5 text-sm transition-colors ${
                  selectedTags.length > 0
                    ? 'text-primary'
                    : 'text-slate-400 hover:text-slate-600 dark:text-text-muted dark:hover:text-primary'
                }`}
              >
                <span className="material-symbols-outlined text-lg">filter_list</span>
                {selectedTags.length > 0 && (
                  <span className="text-[10px] font-bold bg-primary/10 text-primary px-1.5 py-0.5 rounded-full min-w-[18px] text-center">
                    {selectedTags.length}
                  </span>
                )}
              </button>
            )}
          </div>
        </div>

        {isLoadingAccounts ? (
          <p id="meeting-account-status" role="status" className="pt-2 text-xs text-slate-500 dark:text-text-muted">
            어카운트 목록을 불러오는 중…
          </p>
        ) : accountsError ? (
          <div id="meeting-account-status" role="alert" className="flex flex-wrap items-center gap-2 pt-2 text-xs text-red-600 dark:text-red-400">
            <span>{accountsError}</span>
            <button onClick={onRetryAccounts} className="font-semibold hover:underline">
              다시 시도
            </button>
          </div>
        ) : accounts.length === 0 ? (
          <p id="meeting-account-status" className="pt-2 text-xs text-slate-500 dark:text-text-muted">
            사용 가능한 어카운트가 없습니다.
          </p>
        ) : null}

        {/* Tag filter chips */}
        {showTagFilter && allTags.length > 0 && (
          <div className="flex flex-wrap gap-2 py-3">
            {allTags.map((tag) => {
              const isSelected = selectedTags.includes(tag);
              return (
                <button
                  key={tag}
                  onClick={() => {
                    setSelectedTags(prev =>
                      isSelected ? prev.filter(t => t !== tag) : [...prev, tag]
                    );
                  }}
                  className={`text-xs font-bold px-3 py-1.5 rounded-full transition-all ${
                    isSelected
                      ? 'bg-primary text-white ring-2 ring-primary/30'
                      : getTagColor(tag)
                  }`}
                >
                  {tag}
                </button>
              );
            })}
            {selectedTags.length > 0 && (
              <button
                onClick={() => setSelectedTags([])}
                className="text-xs font-medium text-slate-400 hover:text-slate-600 dark:text-text-muted dark:hover:text-white px-2 py-1.5 transition-colors"
              >
                Clear
              </button>
            )}
          </div>
        )}
      </div>

      {error && (
        <div role="alert" className="mx-4 lg:mx-0 flex flex-wrap items-center gap-2 text-sm text-red-600 dark:text-red-400">
          <span>{error}</span>
          <button onClick={onRetry} className="font-semibold hover:underline">다시 시도</button>
        </div>
      )}

      {/* Keep filters mounted and usable while replacing the result set. */}
      {isLoading ? (
        <div role="status" className="px-4 lg:px-0">
          <span className="sr-only">미팅 목록을 불러오는 중…</span>
          <div className="space-y-4 lg:grid lg:grid-cols-3 lg:gap-6 lg:space-y-0">
            {[0, 1, 2].map((i) => <SkeletonCard key={i} />)}
          </div>
        </div>
      ) : (
      <div className="px-4 lg:px-0 space-y-6">
        {Object.entries(groupedMeetings).map(([group, groupMeetings], groupIndex, arr) => (
          <div key={group}>
            {group && (
              <h3 className="text-slate-500 dark:text-text-muted text-xs font-bold uppercase tracking-widest pb-3">
                {group}
              </h3>
            )}
            <div className="space-y-4 lg:grid lg:grid-cols-3 lg:gap-6 lg:space-y-0">
              {groupMeetings.map((meeting) => (
                <MeetingCard key={meeting.meetingId} meeting={meeting} onDelete={onDeleteMeeting} />
              ))}
              {/* "Record New Meeting" card — desktop only, in last group */}
              {groupIndex === arr.length - 1 && (
                <Link href="/record" className="hidden lg:flex border-2 border-dashed border-slate-200 dark:border-white/10 rounded-xl items-center justify-center min-h-[180px] hover:border-primary dark:hover:border-primary/30 hover:text-primary text-slate-400 dark:text-text-muted transition-colors group/new">
                  <div className="flex flex-col items-center gap-2">
                    <span className="material-symbols-outlined text-3xl group-hover/new:text-primary transition-colors">add_circle</span>
                    <span className="text-sm font-semibold">Record New Meeting</span>
                  </div>
                </Link>
              )}
            </div>
          </div>
        ))}

        {filteredMeetings.length === 0 && !error && (
          <div className="flex flex-col items-center justify-center py-16 text-center">
            {hasMore ? (
              <>
                <span className="material-symbols-outlined text-5xl text-slate-300 dark:text-slate-600 mb-3">more_horiz</span>
                <h3 className="text-lg font-bold text-slate-900 dark:text-white mb-1">
                  아직 표시할 미팅이 없습니다
                </h3>
                <p role="status" className="text-sm text-slate-500 max-w-xs">
                  더 확인할 미팅이 있습니다. Load More로 다음 결과를 불러오세요.
                </p>
              </>
            ) : hasActiveFilters ? (
              <>
                <span className="material-symbols-outlined text-5xl text-slate-300 dark:text-slate-600 mb-3">search_off</span>
                <h3 className="text-lg font-bold text-slate-900 dark:text-white mb-1">
                  선택한 조건에 맞는 미팅이 없습니다
                </h3>
                <p className="text-sm text-slate-500 max-w-xs mb-4">
                  {selectedAccountIds.length
                    ? `${selectedAccountLabel}에서 다른 태그나 검색어를 사용하거나 어카운트 필터를 해제해 보세요.`
                    : '태그나 검색어를 변경해 보세요.'}
                </p>
                <div className="flex flex-wrap justify-center gap-3">
                  {selectedAccountIds.length > 0 && (
                    <button onClick={() => onAccountChange([])} className="text-sm font-semibold text-primary hover:underline">
                      어카운트 해제
                    </button>
                  )}
                  {selectedTags.length > 0 && (
                    <button onClick={() => setSelectedTags([])} className="text-sm font-semibold text-primary hover:underline">
                      태그 해제
                    </button>
                  )}
                  {searchQuery && (
                    <button onClick={() => setSearchQuery('')} className="text-sm font-semibold text-primary hover:underline">
                      검색어 지우기
                    </button>
                  )}
                </div>
              </>
            ) : activeTab === 'recent' ? (
              <>
                <span className="material-symbols-outlined text-5xl text-slate-300 dark:text-slate-600 mb-3">calendar_today</span>
                <h3 className="text-lg font-bold text-slate-900 dark:text-white mb-1">
                  No recent meetings
                </h3>
                <p className="text-sm text-slate-500 max-w-xs">
                  No meetings from the past 7 days. Check &lsquo;All Notes&rsquo; for older meetings.
                </p>
              </>
            ) : (
              <>
                <span className="material-symbols-outlined text-5xl text-slate-300 dark:text-slate-600 mb-3">video_camera_front</span>
                <h3 className="text-lg font-bold text-slate-900 dark:text-white mb-1">
                  No meetings yet
                </h3>
                <p className="text-sm text-slate-500 max-w-xs mb-4">
                  Record your first meeting to get started with AI transcription and summaries.
                </p>
                <a
                  href="/record"
                  className="inline-flex items-center gap-2 px-5 py-2.5 bg-primary text-white rounded-lg text-sm font-semibold hover:bg-primary/90 transition-colors active:scale-[0.97]"
                >
                  <span className="material-symbols-outlined text-lg">mic</span>
                  Start Recording
                </a>
              </>
            )}
          </div>
        )}
      </div>
      )}
    </div>
  );
}
