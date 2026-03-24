'use client';

import { useState } from 'react';
import { useRouter } from 'next/navigation';
import Link from 'next/link';
import { ExportMenu } from '@/components/ExportMenu';
import { ShareButton } from '@/components/ShareButton';
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
      className="hidden lg:flex items-center gap-1.5 px-3 py-1.5 rounded-lg border border-[var(--color-border)] text-sm font-medium text-[var(--color-text-muted)] hover:text-red-600 hover:border-red-200 dark:hover:text-red-400 dark:hover:border-red-800 transition-colors disabled:opacity-50"
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
}

export function MeetingHeader({ meeting, onShare, onUnshare }: MeetingHeaderProps) {
  return (
    <>
      {/* Breadcrumbs - Desktop */}
      <div className="hidden lg:flex items-center gap-1.5 text-sm text-[var(--color-text-muted)] mb-8">
        <Link href="/" className="hover:text-[var(--color-text-primary)] transition-colors">Meetings</Link>
        <span>/</span>
        <span className="text-[var(--color-text-primary)] font-medium">{meeting.title}</span>
      </div>

      {/* Header Section */}
      <header className="mb-8 lg:mb-10">
        <div className="flex items-center gap-2 mb-3">
          {meeting.tags?.[0] && (
            <span className="bg-slate-900 dark:bg-white text-white dark:text-slate-900 text-[10px] font-bold uppercase tracking-wider px-2 py-0.5 rounded">
              {meeting.tags[0]}
            </span>
          )}
          <span className="text-[var(--color-text-muted)] text-xs">
            {formatDate(meeting.date)} · {formatTime(meeting.date)}
          </span>
        </div>
        <h1 className="notion-title mb-4">
          {meeting.title}
        </h1>

        {/* Participants & Actions */}
        <div className="flex flex-wrap items-center justify-between gap-4">
          <div className="flex items-center gap-3">
            <div className="flex -space-x-2">
              {meeting.participants?.slice(0, 4).map((p) => (
                <div
                  key={p.id}
                  className="size-8 lg:size-9 rounded-full border-2 border-[var(--color-surface)] bg-slate-200 flex items-center justify-center text-[10px] font-bold text-slate-500 overflow-hidden"
                >
                  {p.avatarUrl ? (
                    <img src={p.avatarUrl} alt={p.name} className="w-full h-full object-cover" />
                  ) : (
                    p.initials || p.name?.charAt(0)
                  )}
                </div>
              ))}
              {(meeting.participants?.length || 0) > 4 && (
                <div className="size-8 lg:size-9 rounded-full border-2 border-[var(--color-surface)] bg-slate-100 dark:bg-slate-800 flex items-center justify-center text-[10px] font-bold text-slate-500">
                  +{meeting.participants!.length - 4}
                </div>
              )}
            </div>
            <p className="text-xs text-[var(--color-text-muted)] font-medium hidden lg:block">
              {meeting.participants?.map((p) => p.name?.split(' ')[0]).slice(0, 3).join(', ')}
              {(meeting.participants?.length || 0) > 3 && ` and ${meeting.participants!.length - 3} others`}
            </p>
          </div>

          <div className="flex items-center gap-2">
            <DesktopDeleteButton meetingId={meeting.meetingId} />
            <ExportMenu meetingId={meeting.meetingId} />
            <ShareButton
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
