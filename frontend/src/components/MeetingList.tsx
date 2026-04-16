'use client';

import { useState, useRef, useEffect, useMemo } from 'react';
import Link from 'next/link';
import { meetingsApi } from '@/lib/api';
import type { Meeting, MeetingListFilter } from '@/types/meeting';
import { SkeletonCard } from '@/components/ui/Skeleton';

interface MeetingListProps {
  meetings: Meeting[];
  isLoading?: boolean;
  onTabChange?: (tab: string) => void;
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

  if (variant === 'secondary') {
    return `${lightClass} dark:bg-[#B026FF]/10 dark:text-[#B026FF] dark:border dark:border-[#B026FF]/20`;
  }
  return `${lightClass} dark:bg-[#00E5FF]/10 dark:text-[#00E5FF] dark:border dark:border-[#00E5FF]/20`;
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
      <div className={`glass-panel p-4 lg:p-6 rounded-xl hover:border-primary/30 lg:hover:border-slate-200 lg:dark:hover:border-[#00E5FF]/30 lg:hover:shadow-xl lg:hover:shadow-primary/5 lg:dark:hover:shadow-[0_0_15px_rgba(0,229,255,0.1)] transition-all cursor-pointer group ${isDeleting ? 'opacity-50 pointer-events-none' : ''}`}>
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
          <div className="flex items-center justify-center size-9 rounded-lg bg-[#00E5FF]/10 border border-[#00E5FF]/20">
            <span className="material-symbols-outlined text-[#00E5FF] text-lg">video_chat</span>
          </div>
          {meeting.duration != null && meeting.duration > 0 && (
            <span className="text-[11px] font-bold uppercase tracking-wider text-[#849396]">
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
          <span className="text-xs text-slate-400 dark:text-[#849396]">{formatDate(meeting.date)}</span>
        </div>
        <h4 className="hidden lg:block text-slate-900 dark:text-[#e4e1e9] font-bold text-lg leading-tight group-hover:text-primary transition-colors mb-2">
          {meeting.title}
        </h4>

        {/* Mobile: date row */}
        <div className="flex items-center gap-2 text-slate-400 text-xs mb-3 lg:hidden">
          <span className="material-symbols-outlined text-[14px]">calendar_today</span>
          <span>{formatDate(meeting.date)} &bull; {formatTime(meeting.date)}</span>
        </div>

        {meeting.summary && (
          <p className="text-slate-600 dark:text-[#BAC9CC] text-sm line-clamp-2 lg:line-clamp-3 leading-relaxed mb-4">
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
                    className="size-6 lg:size-7 rounded-full border-2 border-white dark:border-[#131318] bg-slate-200 dark:bg-[#1f1f25] overflow-hidden flex items-center justify-center text-[10px] font-bold text-slate-500 dark:text-[#849396]"
                  >
                    {p.avatarUrl ? (
                      <img src={p.avatarUrl} alt={p.name} className="w-full h-full object-cover" />
                    ) : (
                      p.initials || p.name?.charAt(0) || '?'
                    )}
                  </div>
                ))}
                {meeting.participants.length > 3 && (
                  <div className="size-6 lg:size-7 rounded-full border-2 border-white dark:border-[#131318] bg-slate-100 dark:bg-[#1f1f25] flex items-center justify-center text-[10px] font-bold text-slate-500 dark:text-[#849396]">
                    +{meeting.participants.length - 3}
                  </div>
                )}
              </div>
            ) : (
              <div />
            )}
            {/* Attendee count — dark mode */}
            {meeting.participants && meeting.participants.length > 0 && (
              <div className="hidden dark:flex items-center gap-1 text-[#849396] text-xs">
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

export function MeetingList({ meetings, isLoading, onTabChange, onDeleteMeeting }: MeetingListProps) {
  const [activeTab, setActiveTab] = useState<MeetingListFilter['tab']>('all');
  const [searchQuery, setSearchQuery] = useState('');
  const [selectedTags, setSelectedTags] = useState<string[]>([]);
  const [showTagFilter, setShowTagFilter] = useState(false);

  const allTags = useMemo(() => {
    const tagSet = new Set<string>();
    meetings.forEach(m => m.tags?.forEach(t => tagSet.add(t)));
    return Array.from(tagSet).sort();
  }, [meetings]);

  const filteredMeetings = meetings.filter((meeting) => {
    // Tab-based filtering: 'recent' shows only last 7 days
    if (activeTab === 'recent') {
      const weekAgo = new Date(Date.now() - 7 * 24 * 60 * 60 * 1000);
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

  // Group meetings by date
  const groupedMeetings = filteredMeetings.reduce((acc, meeting) => {
    const date = new Date(meeting.date);
    const now = new Date();
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

  if (isLoading) {
    return (
      <div className="px-4 lg:px-0 space-y-4 lg:grid lg:grid-cols-3 lg:gap-6 lg:space-y-0">
        {[0, 1, 2].map((i) => (
          <SkeletonCard key={i} />
        ))}
      </div>
    );
  }

  return (
    <div className="space-y-4">
      {/* Search Bar — mobile only (desktop has search in header) */}
      <div className="px-4 lg:hidden">
        <label className="flex flex-col min-w-40 h-11 w-full">
          <div className="flex w-full flex-1 items-stretch rounded-xl h-full shadow-sm">
            <div className="text-slate-400 dark:text-[#849396] flex bg-slate-100 dark:bg-white/5 items-center justify-center pl-4 rounded-l-xl">
              <span className="material-symbols-outlined text-[20px]">search</span>
            </div>
            <input
              value={searchQuery}
              onChange={(e) => setSearchQuery(e.target.value)}
              className="flex w-full min-w-0 flex-1 resize-none overflow-hidden rounded-r-xl text-slate-900 dark:text-[#e4e1e9] focus:outline-0 focus:ring-0 border-none bg-slate-100 dark:bg-white/5 placeholder:text-slate-400 dark:placeholder:text-[#849396] px-3 text-sm font-medium leading-normal"
              placeholder="Search meetings, notes, or tags"
            />
          </div>
        </label>
      </div>

      {/* Tabs + Filter */}
      <div className="px-4 lg:px-0">
        <div className="flex items-center justify-between border-b border-slate-200 dark:border-white/10">
          <div className="flex gap-6">
            {tabs.map((tab) => (
              <button
                key={tab.key}
                onClick={() => {
                  setActiveTab(tab.key);
                  onTabChange?.(tab.key);
                }}
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
          {allTags.length > 0 && (
            <button
              onClick={() => setShowTagFilter(!showTagFilter)}
              className={`flex items-center gap-1.5 pb-3 text-sm transition-colors ${
                selectedTags.length > 0
                  ? 'text-primary dark:text-[#00E5FF]'
                  : 'text-slate-400 hover:text-slate-600 dark:text-[#849396] dark:hover:text-[#00E5FF]'
              }`}
            >
              <span className="material-symbols-outlined text-lg">filter_list</span>
              {selectedTags.length > 0 && (
                <span className="text-[10px] font-bold bg-primary/10 text-primary dark:bg-[#00E5FF]/10 dark:text-[#00E5FF] px-1.5 py-0.5 rounded-full min-w-[18px] text-center">
                  {selectedTags.length}
                </span>
              )}
            </button>
          )}
        </div>

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
                      ? 'bg-primary text-white dark:bg-[#00E5FF] dark:text-[#09090E] ring-2 ring-primary/30 dark:ring-[#00E5FF]/30'
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
                className="text-xs font-medium text-slate-400 hover:text-slate-600 dark:text-[#849396] dark:hover:text-white px-2 py-1.5 transition-colors"
              >
                Clear
              </button>
            )}
          </div>
        )}
      </div>

      {/* Meeting Cards - Mobile: stacked, Desktop: grid */}
      <div className="px-4 lg:px-0 space-y-6">
        {Object.entries(groupedMeetings).map(([group, groupMeetings], groupIndex, arr) => (
          <div key={group}>
            <h3 className="text-slate-500 dark:text-[#849396] text-xs font-bold uppercase tracking-widest pb-3">
              {group}
            </h3>
            <div className="space-y-4 lg:grid lg:grid-cols-3 lg:gap-6 lg:space-y-0">
              {groupMeetings.map((meeting) => (
                <MeetingCard key={meeting.meetingId} meeting={meeting} onDelete={onDeleteMeeting} />
              ))}
              {/* "Record New Meeting" card — desktop only, in last group */}
              {groupIndex === arr.length - 1 && (
                <Link href="/record" className="hidden lg:flex border-2 border-dashed border-slate-200 dark:border-white/10 rounded-xl items-center justify-center min-h-[180px] hover:border-primary dark:hover:border-[#00E5FF]/30 hover:text-primary text-slate-400 dark:text-[#849396] transition-colors group/new">
                  <div className="flex flex-col items-center gap-2">
                    <span className="material-symbols-outlined text-3xl group-hover/new:text-primary transition-colors">add_circle</span>
                    <span className="text-sm font-semibold">Record New Meeting</span>
                  </div>
                </Link>
              )}
            </div>
          </div>
        ))}

        {filteredMeetings.length === 0 && (
          <div className="flex flex-col items-center justify-center py-16 text-center">
            {selectedTags.length > 0 ? (
              <>
                <span className="material-symbols-outlined text-5xl text-slate-300 dark:text-slate-600 mb-3">label_off</span>
                <h3 className="text-lg font-bold text-slate-900 dark:text-white mb-1">
                  No meetings with selected tags
                </h3>
                <p className="text-sm text-slate-500 max-w-xs mb-4">
                  Try selecting different tags or clear the filter.
                </p>
                <button
                  onClick={() => setSelectedTags([])}
                  className="text-sm font-semibold text-primary dark:text-[#00E5FF] hover:underline"
                >
                  Clear filter
                </button>
              </>
            ) : searchQuery ? (
              <>
                <span className="material-symbols-outlined text-5xl text-slate-300 dark:text-slate-600 mb-3">search_off</span>
                <h3 className="text-lg font-bold text-slate-900 dark:text-white mb-1">
                  No results for &lsquo;{searchQuery}&rsquo;
                </h3>
                <p className="text-sm text-slate-500 max-w-xs">
                  Try adjusting your search terms or browse all meetings.
                </p>
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
    </div>
  );
}
