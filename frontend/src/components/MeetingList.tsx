'use client';

import { useState, useRef, useEffect } from 'react';
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

function getTagColor(tag: string): string {
  const colors: Record<string, string> = {
    internal: 'bg-primary/10 text-primary',
    design: 'bg-amber-100 text-amber-700',
    external: 'bg-green-100 text-green-700',
    engineering: 'bg-emerald-50 text-emerald-600',
    marketing: 'bg-amber-50 text-amber-600',
    strategy: 'bg-primary/10 text-primary',
  };
  return colors[tag.toLowerCase()] || 'bg-slate-100 text-slate-600';
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
      <div className={`bg-white dark:bg-slate-800 border border-border-default p-4 rounded-lg notion-hover transition-colors duration-150 cursor-pointer group ${isDeleting ? 'opacity-50 pointer-events-none' : ''}`}>
        <div className="flex justify-between items-start mb-2 lg:mb-4">
          <h4 className="text-slate-900 dark:text-slate-100 font-bold text-base leading-tight group-hover:text-primary transition-colors">
            {meeting.title}
          </h4>
          {tag && (
            <span className={`text-[10px] font-bold px-2 py-0.5 rounded-full uppercase ${getTagColor(tag)}`}>
              {tag}
            </span>
          )}
        </div>

        <div className="flex items-center gap-2 text-slate-400 text-xs mb-3">
          <span className="material-symbols-outlined text-[14px]">calendar_today</span>
          <span>{formatDate(meeting.date)} &bull; {formatTime(meeting.date)}</span>
        </div>

        {meeting.summary && (
          <p className="text-slate-600 dark:text-slate-400 text-sm line-clamp-2 lg:line-clamp-3 leading-relaxed mb-4">
            {meeting.summary}
          </p>
        )}

        {meeting.tags && meeting.tags.length > 1 && (
          <div className="hidden lg:flex flex-wrap gap-2 mb-4">
            {meeting.tags.slice(1).map((t) => (
              <span key={t} className="text-xs px-2 py-1 bg-slate-100 dark:bg-slate-800 rounded text-slate-600 dark:text-slate-300">
                #{t}
              </span>
            ))}
          </div>
        )}

        <div className="flex items-center justify-between pt-4 border-t border-slate-100 dark:border-slate-800">
          {meeting.participants && meeting.participants.length > 0 ? (
            <div className="flex -space-x-2">
              {meeting.participants.slice(0, 3).map((p, i) => (
                <div
                  key={p.id || i}
                  className="size-6 lg:size-7 rounded-full border-2 border-white dark:border-slate-800 bg-slate-200 overflow-hidden flex items-center justify-center text-[10px] font-bold text-slate-500"
                >
                  {p.avatarUrl ? (
                    <img src={p.avatarUrl} alt={p.name} className="w-full h-full object-cover" />
                  ) : (
                    p.initials || p.name?.charAt(0) || '?'
                  )}
                </div>
              ))}
              {meeting.participants.length > 3 && (
                <div className="size-6 lg:size-7 rounded-full border-2 border-white dark:border-slate-800 bg-slate-100 dark:bg-slate-800 flex items-center justify-center text-[10px] font-bold text-slate-500">
                  +{meeting.participants.length - 3}
                </div>
              )}
            </div>
          ) : (
            <div />
          )}
          <div className="relative" ref={menuRef}>
            <button
              onClick={(e) => { e.preventDefault(); e.stopPropagation(); setMenuOpen(!menuOpen); }}
              className="text-slate-400 hover:text-primary transition-colors"
            >
              <span className="material-symbols-outlined text-xl">more_horiz</span>
            </button>
            {menuOpen && (
              <div className="absolute right-0 bottom-full mb-1 bg-white dark:bg-slate-800 border border-slate-200 dark:border-slate-700 rounded-lg shadow-lg z-20 min-w-[120px]">
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

  const filteredMeetings = meetings.filter((meeting) => {
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
      <div className="px-4 lg:px-0 space-y-4 lg:grid lg:grid-cols-2 xl:grid-cols-3 lg:gap-6 lg:space-y-0">
        {[0, 1, 2].map((i) => (
          <SkeletonCard key={i} />
        ))}
      </div>
    );
  }

  return (
    <div className="space-y-4">
      {/* Search Bar */}
      <div className="px-4 lg:px-0">
        <label className="flex flex-col min-w-40 h-9 w-full">
          <div className="flex w-full flex-1 items-stretch rounded-md h-full border border-border-default">
            <div className="text-text-muted flex items-center justify-center pl-3">
              <span className="material-symbols-outlined text-[18px]">search</span>
            </div>
            <input
              value={searchQuery}
              onChange={(e) => setSearchQuery(e.target.value)}
              className="flex w-full min-w-0 flex-1 resize-none overflow-hidden rounded-r-md text-text-primary focus:outline-0 focus:ring-0 border-none bg-transparent placeholder:text-text-muted px-3 text-sm leading-normal"
              placeholder="Search meetings, notes, or tags"
            />
          </div>
        </label>
      </div>

      {/* Tabs */}
      <div className="px-4 lg:px-0">
        <div className="flex gap-1">
          {tabs.map((tab) => (
            <button
              key={tab.key}
              onClick={() => {
                setActiveTab(tab.key);
                onTabChange?.(tab.key);
              }}
              className={`px-3 py-1.5 rounded-md text-sm font-medium transition-colors ${
                activeTab === tab.key
                  ? 'bg-[var(--notion-hover)] text-text-primary'
                  : 'text-text-secondary hover:bg-[var(--notion-hover)] hover:text-text-primary'
              }`}
            >
              {tab.label}
            </button>
          ))}
        </div>
      </div>

      {/* Meeting Cards - Mobile: stacked, Desktop: grid */}
      <div className="px-4 lg:px-0 space-y-6">
        {Object.entries(groupedMeetings).map(([group, groupMeetings]) => (
          <div key={group}>
            <h3 className="text-text-muted text-xs font-medium pb-3">
              {group}
            </h3>
            <div className="space-y-4 lg:grid lg:grid-cols-2 xl:grid-cols-3 lg:gap-6 lg:space-y-0">
              {groupMeetings.map((meeting) => (
                <MeetingCard key={meeting.meetingId} meeting={meeting} onDelete={onDeleteMeeting} />
              ))}
            </div>
          </div>
        ))}

        {filteredMeetings.length === 0 && (
          <div className="flex flex-col items-center justify-center py-16 text-center">
            {searchQuery ? (
              <>
                <span className="material-symbols-outlined text-5xl text-slate-300 dark:text-slate-600 mb-3">search_off</span>
                <h3 className="text-lg font-bold text-slate-900 dark:text-white mb-1">
                  No results for &lsquo;{searchQuery}&rsquo;
                </h3>
                <p className="text-sm text-slate-500 max-w-xs">
                  Try adjusting your search terms or browse all meetings.
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
