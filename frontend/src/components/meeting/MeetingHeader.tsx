'use client';

import { useState, useRef, useEffect } from 'react';
import { useRouter } from 'next/navigation';
import Link from 'next/link';
import { ExportMenu } from '@/components/ExportMenu';
import { MeetingShareButton } from '@/components/ShareButton';
import { LinkMeetingsModal } from '@/components/meeting/LinkMeetingsModal';
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
  /** ADR-014 Phase 6: called with the updated linked id list after the picker saves. */
  onLinkedMeetingsChange?: (linkedMeetingIds: string[]) => void;
}

export function MeetingHeader({ meeting, onShare, onUnshare, onTitleChange, onLinkedMeetingsChange }: MeetingHeaderProps) {
  const [isEditingTitle, setIsEditingTitle] = useState(false);
  const [editTitle, setEditTitle] = useState(meeting.title);
  const [showLinkPicker, setShowLinkPicker] = useState(false);
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
      <div className="hidden lg:flex items-center gap-1.5 text-sm text-slate-400 dark:text-text-muted mb-8">
        <Link href="/" className="hover:text-slate-900 dark:hover:text-text-main transition-colors">Meetings</Link>
        <span className="material-symbols-outlined text-base">chevron_right</span>
        <span className="text-slate-900 dark:text-text-main font-medium">{meeting.title}</span>
      </div>

      {/* Header Section */}
      <header className="mb-8 lg:mb-10">
        <div className="flex items-center gap-2 mb-3">
          {meeting.tags?.[0] && (
            <span className="bg-slate-900 dark:bg-primary/10 text-white dark:text-primary dark:border dark:border-primary/20 text-[10px] font-bold uppercase tracking-wider px-2 py-0.5 rounded">
              {meeting.tags[0]}
            </span>
          )}
          <span className="text-slate-400 dark:text-text-muted text-xs">
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
            className="w-full text-3xl font-bold tracking-tight lg:text-4xl lg:font-black dark:font-headline dark:text-primary mb-4 bg-transparent border-b-2 border-primary dark:border-primary outline-none text-slate-900"
          />
        ) : (
          <h1
            onClick={() => { setEditTitle(meeting.title); setIsEditingTitle(true); }}
            className="text-3xl font-bold tracking-tight lg:text-4xl lg:font-black dark:font-headline dark:neon-text-cyan mb-4 cursor-pointer group"
            title="클릭하여 제목 수정"
          >
            {meeting.title}
            <span className="material-symbols-outlined text-lg ml-2 opacity-0 group-hover:opacity-50 transition-opacity align-middle">edit</span>
          </h1>
        )}

        {/* Linked predecessor meetings (ADR-014 Phase 6) — chips for existing
            links + a "+ 연결" button to open the picker. The summarize Lambda
            prepends these meetings' summaries as prior context, so surfacing
            them here lets the user manage the chain and jump back to source
            material. */}
        <div className="flex flex-wrap items-center gap-1.5 mb-4 text-xs">
          {meeting.linkedMeetingIds && meeting.linkedMeetingIds.length > 0 ? (
            <>
              <span className="text-slate-400 dark:text-text-muted font-medium">
                <span className="material-symbols-outlined text-sm align-middle mr-1">link</span>
                연결된 이전 미팅:
              </span>
              {meeting.linkedMeetingIds.map((linkedId) => (
                <Link
                  key={linkedId}
                  href={`/meeting/${linkedId}`}
                  className="inline-flex items-center gap-1 px-2 py-0.5 rounded-md bg-primary/5 dark:bg-primary/10 text-primary hover:bg-primary/10 dark:hover:bg-primary/20 transition-colors font-mono"
                  title="이전 미팅으로 이동"
                >
                  {linkedId.slice(-8)}
                </Link>
              ))}
            </>
          ) : null}
          <button
            onClick={() => setShowLinkPicker(true)}
            className="inline-flex items-center gap-1 px-2 py-0.5 rounded-md border border-dashed border-slate-300 dark:border-white/15 text-slate-500 dark:text-text-muted hover:border-primary/40 hover:text-primary dark:hover:border-primary/40 dark:hover:text-primary transition-colors"
            title="이전 미팅 연결"
          >
            <span className="material-symbols-outlined text-sm">add_link</span>
            {meeting.linkedMeetingIds && meeting.linkedMeetingIds.length > 0 ? '편집' : '이전 미팅 연결'}
          </button>
        </div>

        {showLinkPicker && (
          <LinkMeetingsModal
            meetingId={meeting.meetingId}
            meetingDate={meeting.date}
            initialLinkedIds={meeting.linkedMeetingIds ?? []}
            onClose={() => setShowLinkPicker(false)}
            onLinked={(ids) => onLinkedMeetingsChange?.(ids)}
          />
        )}

        {/* Participants & Actions */}
        <div className="flex flex-wrap items-center justify-between gap-4">
          <div className="flex items-center gap-3">
            <div className="flex -space-x-2">
              {meeting.participants?.slice(0, 4).map((p) => (
                <div
                  key={p.id}
                  className="size-8 lg:size-9 rounded-full border-2 border-white dark:border-background-dark bg-slate-200 flex items-center justify-center text-[10px] font-bold text-slate-500 overflow-hidden"
                >
                  {p.avatarUrl ? (
                    <img src={p.avatarUrl} alt={p.name} className="w-full h-full object-cover" />
                  ) : (
                    p.initials || p.name?.charAt(0)
                  )}
                </div>
              ))}
              {(meeting.participants?.length || 0) > 4 && (
                <div className="size-8 lg:size-9 rounded-full border-2 border-white dark:border-background-dark bg-slate-100 dark:bg-slate-800 flex items-center justify-center text-[10px] font-bold text-slate-500">
                  +{meeting.participants!.length - 4}
                </div>
              )}
            </div>
            <p className="text-xs text-slate-400 dark:text-text-muted font-medium hidden lg:block">
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
