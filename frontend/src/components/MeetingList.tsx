'use client';

import { useState } from 'react';
import Link from 'next/link';
import type { Meeting, MeetingListFilter } from '@/types/meeting';

interface MeetingListProps {
  meetings: Meeting[];
  isLoading?: boolean;
  onTabChange?: (tab: string) => void;
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

function MeetingCard({ meeting }: { meeting: Meeting }) {
  const tag = meeting.tags?.[0];

  return (
    <Link href={`/meeting/${meeting.meetingId}`}>
      <div className="bg-white dark:bg-slate-800 border border-slate-100 dark:border-slate-700 p-4 lg:p-6 rounded-xl shadow-sm hover:border-primary/30 hover:shadow-xl hover:shadow-primary/5 transition-all cursor-pointer group">
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
          <button className="text-slate-400 hover:text-primary transition-colors lg:block hidden">
            <span className="material-symbols-outlined text-xl">more_horiz</span>
          </button>
        </div>
      </div>
    </Link>
  );
}

export function MeetingList({ meetings, isLoading, onTabChange }: MeetingListProps) {
  const [activeTab, setActiveTab] = useState<MeetingListFilter['tab']>('all');
  const [searchQuery, setSearchQuery] = useState('');

  const filteredMeetings = meetings.filter((meeting) => {
    if (searchQuery) {
      const query = searchQuery.toLowerCase();
      return (
        meeting.title.toLowerCase().includes(query) ||
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
      <div className="flex items-center justify-center py-12">
        <div className="animate-spin rounded-full h-8 w-8 border-2 border-primary border-t-transparent" />
      </div>
    );
  }

  return (
    <div className="space-y-4">
      {/* Search Bar */}
      <div className="px-4 lg:px-0">
        <label className="flex flex-col min-w-40 h-11 w-full">
          <div className="flex w-full flex-1 items-stretch rounded-xl h-full shadow-sm">
            <div className="text-slate-400 flex bg-slate-100 dark:bg-slate-800 items-center justify-center pl-4 rounded-l-xl">
              <span className="material-symbols-outlined text-[20px]">search</span>
            </div>
            <input
              value={searchQuery}
              onChange={(e) => setSearchQuery(e.target.value)}
              className="flex w-full min-w-0 flex-1 resize-none overflow-hidden rounded-r-xl text-slate-900 dark:text-slate-100 focus:outline-0 focus:ring-0 border-none bg-slate-100 dark:bg-slate-800 placeholder:text-slate-400 px-3 text-sm font-medium leading-normal"
              placeholder="Search meetings, notes, or tags"
            />
          </div>
        </label>
      </div>

      {/* Tabs */}
      <div className="bg-white dark:bg-slate-900">
        <div className="flex border-b border-slate-100 dark:border-slate-800 px-4 lg:px-0 gap-4 lg:gap-6">
          {tabs.map((tab) => (
            <button
              key={tab.key}
              onClick={() => {
                setActiveTab(tab.key);
                onTabChange?.(tab.key);
              }}
              className={`flex flex-col items-center justify-center border-b-2 pb-3 pt-2 transition-colors ${
                activeTab === tab.key
                  ? 'border-primary text-primary'
                  : 'border-transparent text-slate-500 dark:text-slate-400 hover:text-slate-700'
              }`}
            >
              <p className="text-sm font-semibold">{tab.label}</p>
            </button>
          ))}
        </div>
      </div>

      {/* Meeting Cards - Mobile: stacked, Desktop: grid */}
      <div className="px-4 lg:px-0 space-y-6">
        {Object.entries(groupedMeetings).map(([group, groupMeetings]) => (
          <div key={group}>
            <h3 className="text-slate-500 dark:text-slate-400 text-xs font-bold uppercase tracking-widest pb-3">
              {group}
            </h3>
            <div className="space-y-4 lg:grid lg:grid-cols-2 xl:grid-cols-3 lg:gap-6 lg:space-y-0">
              {groupMeetings.map((meeting) => (
                <MeetingCard key={meeting.meetingId} meeting={meeting} />
              ))}
            </div>
          </div>
        ))}

        {filteredMeetings.length === 0 && (
          <div className="text-center py-12">
            <span className="material-symbols-outlined text-4xl text-slate-300 mb-2">description</span>
            <p className="text-slate-500">No meetings found</p>
          </div>
        )}
      </div>
    </div>
  );
}
