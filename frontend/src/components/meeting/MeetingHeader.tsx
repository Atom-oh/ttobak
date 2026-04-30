'use client';

import { useState, useRef, useEffect } from 'react';
import { useRouter } from 'next/navigation';
import Link from 'next/link';
import { ExportMenu } from '@/components/ExportMenu';
import { MeetingShareButton } from '@/components/ShareButton';
import { meetingsApi } from '@/lib/api';
import type { MeetingDetail, SharedUser } from '@/types/meeting';

function formatDate(dateString: string): string {
  const date = new Date(dateString);
  return date.toLocaleDateString('en-US', {
    month: 'long',
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

function DesktopDeleteButton({ meetingId }: { meetingId: string }) {
  const router = useRouter();
  const [isDeleting, setIsDeleting] = useState(false);

  const handleDelete = async () => {
    if (!confirm('이 미팅을 삭제하시겠습니까?')) return;
    setIsDeleting(true);
    try {
      await meetingsApi.delete(meetingId);
      router.push('/');
    } catch (err) {
      console.error('Failed to delete meeting:', err);
      alert('미팅 삭제에 실패했습니다.');
      setIsDeleting(false);
    }
  };

  return (
    <button
      onClick={handleDelete}
      disabled={isDeleting}
      className="hidden lg:flex items-center gap-1.5 px-3 py-1.5 rounded-lg border border-slate-200 dark:border-white/10 text-sm font-medium text-slate-400 hover:text-red-600 hover:border-red-200 dark:hover:text-red-400 dark:hover:border-red-800 transition-colors disabled:opacity-50"
    >
      <span className="material-symbols-outlined text-lg">delete</span>
      {isDeleting ? '삭제 중...' : '삭제'}
    </button>
  );
}

interface MeetingHeaderProps {
  meeting: MeetingDetail;
  onShare: (user: SharedUser) => void;
  onUnshare: (userId: string) => void;
  onTitleChange?: (newTitle: string) => void;
}

export function MeetingHeader({ meeting, onShare, onUnshare, onTitleChange }: MeetingHeaderProps) {
  const [isEditingTitle, setIsEditingTitle] = useState(false);
  const [editTitle, setEditTitle] = useState(meeting.title);
  const titleInputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (isEditingTitle && titleInputRef.current) {
      titleInputRef.current.focus();
      titleInputRef.current.select();
    }
  }, [isEditingTitle]);

  const saveTitle = () => {
    const trimmed = editTitle.trim();
    if (trimmed && trimmed !== meeting.title) {
      onTitleChange?.(trimmed);
    } else {
      setEditTitle(meeting.title);
    }
    setIsEditingTitle(false);
  };

  return (
    <>
      {/* Breadcrumbs - Desktop */}
      <div className="hidden lg:flex items-center gap-1.5 text-sm text-slate-400 dark:text-[#849396] mb-8">
        <Link href="/" className="hover:text-slate-900 dark:hover:text-text-main transition-colors">Meetings</Link>
        <span className="material-symbols-outlined text-base">chevron_right</span>
        <span className="text-slate-900 dark:text-text-main font-medium">{meeting.title}</span>
      </div>

      {/* Header Section */}
      <header className="mb-8 lg:mb-10">
        <div className="flex items-center gap-2 mb-3">
          {meeting.tags?.[0] && (
            <span className="bg-slate-900 dark:bg-[#00E5FF]/10 text-white dark:text-[#00E5FF] dark:border dark:border-[#00E5FF]/20 text-[10px] font-bold uppercase tracking-wider px-2 py-0.5 rounded">
              {meeting.tags[0]}
            </span>
          )}
          <span className="text-slate-400 dark:text-[#849396] text-xs">
            {formatDate(meeting.date)} · {formatTime(meeting.date)}
          </span>
        </div>
        {isEditingTitle ? (
          <input
            ref={titleInputRef}
            value={editTitle}
            onChange={(e) => setEditTitle(e.target.value)}
            onBlur={saveTitle}
            onKeyDown={(e) => {
              if (e.key === 'Enter') saveTitle();
              if (e.key === 'Escape') { setEditTitle(meeting.title); setIsEditingTitle(false); }
            }}
            className="w-full text-3xl font-bold tracking-tight lg:text-4xl lg:font-black dark:font-[var(--font-headline)] dark:text-[#00E5FF] mb-4 bg-transparent border-b-2 border-primary dark:border-[#00E5FF] outline-none text-slate-900"
          />
        ) : (
          <h1
            onClick={() => { setEditTitle(meeting.title); setIsEditingTitle(true); }}
            className="text-3xl font-bold tracking-tight lg:text-4xl lg:font-black dark:font-[var(--font-headline)] dark:neon-text-cyan mb-4 cursor-pointer group"
            title="클릭하여 제목 수정"
          >
            {meeting.title}
            <span className="material-symbols-outlined text-lg ml-2 opacity-0 group-hover:opacity-50 transition-opacity align-middle">edit</span>
          </h1>
        )}

        {/* Participants & Actions */}
        <div className="flex flex-wrap items-center justify-between gap-4">
          <div className="flex items-center gap-3">
            <div className="flex -space-x-2">
              {meeting.participants?.slice(0, 4).map((p) => (
                <div
                  key={p.id}
                  className="size-8 lg:size-9 rounded-full border-2 border-white dark:border-[#09090E] bg-slate-200 flex items-center justify-center text-[10px] font-bold text-slate-500 overflow-hidden"
                >
                  {p.avatarUrl ? (
                    <img src={p.avatarUrl} alt={p.name} className="w-full h-full object-cover" />
                  ) : (
                    p.initials || p.name?.charAt(0)
                  )}
                </div>
              ))}
              {(meeting.participants?.length || 0) > 4 && (
                <div className="size-8 lg:size-9 rounded-full border-2 border-white dark:border-[#09090E] bg-slate-100 dark:bg-slate-800 flex items-center justify-center text-[10px] font-bold text-slate-500">
                  +{meeting.participants!.length - 4}
                </div>
              )}
            </div>
            <p className="text-xs text-slate-400 dark:text-[#849396] font-medium hidden lg:block">
              {meeting.participants?.map((p) => p.name?.split(' ')[0]).slice(0, 3).join(', ')}
              {(meeting.participants?.length || 0) > 3 && ` and ${meeting.participants!.length - 3} others`}
            </p>
          </div>

          <div className="flex items-center gap-2">
            <DesktopDeleteButton meetingId={meeting.meetingId} />
            <ExportMenu meetingId={meeting.meetingId} />
            <MeetingShareButton
              meetingId={meeting.meetingId}
              sharedWith={meeting.sharedWith}
              onShare={onShare}
              onUnshare={onUnshare}
            />
          </div>
        </div>
      </header>
    </>
  );
}
