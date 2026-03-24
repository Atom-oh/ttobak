'use client';

import { useState, useEffect, useRef, useMemo } from 'react';
import { useRouter, usePathname } from 'next/navigation';
import Link from 'next/link';
import { useAuth } from '@/components/auth/AuthProvider';
import { AppLayout } from '@/components/layout/AppLayout';
import { MeetingEditor } from '@/components/MeetingEditor';
import { ShareButton } from '@/components/ShareButton';
import { AttachmentGallery } from '@/components/AttachmentGallery';
import { FileUploader } from '@/components/FileUploader';
import { ExportMenu } from '@/components/ExportMenu';
import { QAPanel } from '@/components/QAPanel';
import { meetingsApi } from '@/lib/api';
import type { Meeting, MeetingDetail, ActionItem, TranscriptSegment, Attachment, SharedUser } from '@/types/meeting';

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

function formatTimestamp(seconds: number): string {
  const mins = Math.floor(seconds / 60);
  const secs = Math.floor(seconds % 60);
  return `${mins}:${secs.toString().padStart(2, '0')}`;
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
      className="hidden lg:flex items-center gap-1.5 px-3 py-1.5 rounded-lg border border-slate-200 dark:border-slate-700 text-sm font-medium text-slate-600 dark:text-slate-400 hover:text-red-600 hover:border-red-200 dark:hover:text-red-400 dark:hover:border-red-800 transition-colors disabled:opacity-50"
    >
      <span className="material-symbols-outlined text-lg">delete</span>
      {isDeleting ? '삭제 중...' : '삭제'}
    </button>
  );
}

function MobileMoreMenu({ meetingId }: { meetingId: string }) {
  const router = useRouter();
  const [open, setOpen] = useState(false);
  const [isDeleting, setIsDeleting] = useState(false);
  const menuRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const handleClickOutside = (e: MouseEvent) => {
      if (menuRef.current && !menuRef.current.contains(e.target as Node)) {
        setOpen(false);
      }
    };
    document.addEventListener('mousedown', handleClickOutside);
    return () => document.removeEventListener('mousedown', handleClickOutside);
  }, [open]);

  const handleDelete = async () => {
    setOpen(false);
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
    <div className="relative" ref={menuRef}>
      <button
        onClick={() => setOpen(!open)}
        disabled={isDeleting}
        className="flex size-10 items-center justify-center rounded-full hover:bg-slate-100 dark:hover:bg-slate-800"
      >
        {isDeleting ? (
          <div className="animate-spin rounded-full h-5 w-5 border-2 border-red-500 border-t-transparent" />
        ) : (
          <span className="material-symbols-outlined text-slate-700 dark:text-slate-300">more_horiz</span>
        )}
      </button>
      {open && (
        <div className="absolute right-0 top-full mt-1 bg-white dark:bg-slate-800 border border-slate-200 dark:border-slate-700 rounded-lg shadow-lg z-20 min-w-[120px]">
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
  );
}

export default function MeetingDetailPage() {
  const router = useRouter();
  const { isAuthenticated, isLoading: authLoading } = useAuth();
  const [meeting, setMeeting] = useState<MeetingDetail | null>(null);
  const [isLoading, setIsLoading] = useState(true);
  const [showUploader, setShowUploader] = useState(false);

  // Extract meeting ID from URL. usePathname() updates on client-side navigation,
  // unlike window.location.pathname in a mount-only effect which goes stale.
  // CloudFront rewrites /meeting/{id} → /meeting/_ for static export,
  // so useParams() returns "_" instead of the actual ID.
  const pathname = usePathname();
  const meetingId = useMemo(
    () => pathname.split('/meeting/')[1]?.split('/')[0] || '',
    [pathname]
  );

  useEffect(() => {
    if (!isAuthenticated || !meetingId) return;

    const fetchMeeting = async () => {
      try {
        const data = await meetingsApi.get(meetingId);
        setMeeting(data as MeetingDetail);
      } catch (err) {
        console.error('Failed to fetch meeting:', err);
      } finally {
        setIsLoading(false);
      }
    };
    fetchMeeting();
  }, [isAuthenticated, meetingId]);

  // Polling for in-progress meetings
  useEffect(() => {
    if (!meeting || !['transcribing', 'summarizing'].includes(meeting.status)) return;

    const interval = setInterval(async () => {
      try {
        const data = await meetingsApi.get(meeting.meetingId);
        setMeeting(data as MeetingDetail);
        if (data.status === 'done' || data.status === 'error') {
          clearInterval(interval);
        }
      } catch (err) {
        console.error('Polling failed:', err);
      }
    }, 5000);

    return () => clearInterval(interval);
  }, [meeting?.meetingId, meeting?.status]);

  const handleActionItemToggle = (itemId: string) => {
    if (!meeting) return;
    setMeeting({
      ...meeting,
      actionItems: meeting.actionItems?.map((item) =>
        item.id === itemId ? { ...item, completed: !item.completed } : item
      ),
    });
  };

  const handleShare = (user: SharedUser) => {
    if (!meeting) return;
    setMeeting({
      ...meeting,
      sharedWith: [...(meeting.sharedWith || []), user],
    });
  };

  const handleUnshare = (userId: string) => {
    if (!meeting) return;
    setMeeting({
      ...meeting,
      sharedWith: meeting.sharedWith?.filter((u) => u.userId !== userId),
    });
  };

  if (authLoading || isLoading) {
    return (
      <div className="min-h-screen flex items-center justify-center">
        <div className="animate-spin rounded-full h-8 w-8 border-2 border-primary border-t-transparent" />
      </div>
    );
  }

  if (!isAuthenticated) {
    router.push('/');
    return null;
  }

  if (!meeting) {
    return (
      <div className="min-h-screen flex items-center justify-center">
        <p className="text-slate-500">Meeting not found</p>
      </div>
    );
  }

  return (
    <AppLayout activePath="/">
      {/* Mobile Header */}
      <header className="lg:hidden sticky top-0 z-10 flex items-center bg-white/90 dark:bg-slate-900/90 backdrop-blur-md p-4 border-b border-slate-200 dark:border-slate-800 justify-between">
        <button onClick={() => router.back()} className="text-slate-700 dark:text-slate-300 flex size-10 items-center justify-center rounded-full hover:bg-slate-100 dark:hover:bg-slate-800">
          <span className="material-symbols-outlined">arrow_back</span>
        </button>
        <h2 className="text-slate-900 dark:text-slate-100 text-sm font-bold flex-1 text-center">Meeting Report</h2>
        <MobileMoreMenu meetingId={meetingId} />
      </header>

      {/* Content */}
      <div className="flex flex-1 min-h-0">
        {/* Main Content */}
        <div className="flex-1 p-4 lg:px-16 lg:py-12 overflow-y-auto">
          <div className="lg:max-w-3xl">
          {/* Breadcrumbs - Desktop */}
          <div className="hidden lg:flex items-center gap-1.5 text-sm text-text-muted mb-8">
            <Link href="/" className="hover:text-text-primary transition-colors">Meetings</Link>
            <span>/</span>
            <span className="text-text-primary font-medium">{meeting.title}</span>
          </div>

          {/* Header Section */}
          <header className="mb-8 lg:mb-10">
            <div className="flex items-center gap-2 mb-3">
              {meeting.tags?.[0] && (
                <span className="bg-slate-900 dark:bg-white text-white dark:text-slate-900 text-[10px] font-bold uppercase tracking-wider px-2 py-0.5 rounded">
                  {meeting.tags[0]}
                </span>
              )}
              <span className="text-text-muted text-xs">
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
                      className="size-8 lg:size-9 rounded-full border-2 border-white dark:border-slate-900 bg-slate-200 flex items-center justify-center text-[10px] font-bold text-slate-500 overflow-hidden"
                    >
                      {p.avatarUrl ? (
                        <img src={p.avatarUrl} alt={p.name} className="w-full h-full object-cover" />
                      ) : (
                        p.initials || p.name?.charAt(0)
                      )}
                    </div>
                  ))}
                  {(meeting.participants?.length || 0) > 4 && (
                    <div className="size-8 lg:size-9 rounded-full border-2 border-white dark:border-slate-900 bg-slate-100 dark:bg-slate-800 flex items-center justify-center text-[10px] font-bold text-slate-500">
                      +{meeting.participants!.length - 4}
                    </div>
                  )}
                </div>
                <p className="text-xs text-text-muted font-medium hidden lg:block">
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
                  onShare={handleShare}
                  onUnshare={handleUnshare}
                />
              </div>
            </div>
          </header>

          {/* Processing Status Indicator */}
          {meeting.status !== 'done' && meeting.status !== 'error' && (
            <div className="mb-8">
              <div className="flex items-center gap-3 p-4 bg-primary/5 border border-primary/20 rounded-xl">
                <div className="animate-spin rounded-full h-5 w-5 border-2 border-primary border-t-transparent shrink-0" />
                <div className="flex-1">
                  <span className="text-sm font-medium text-primary block">
                    {meeting.status === 'transcribing' ? 'AI 음성 인식 중... (화자 분리 포함)' :
                     meeting.status === 'summarizing' ? 'AI 회의록 생성 중...' :
                     meeting.status === 'recording' ? '오디오 업로드 준비 중...' : '처리 중...'}
                  </span>
                  <span className="text-xs text-primary/60 mt-0.5 block">
                    {meeting.status === 'transcribing' ? '음성을 텍스트로 변환하고 있습니다' :
                     meeting.status === 'summarizing' ? '화자별 요약을 작성하고 있습니다' : '잠시만 기다려주세요'}
                  </span>
                </div>
              </div>
              <div className="mt-2 h-1 bg-slate-100 dark:bg-slate-800 rounded-full overflow-hidden">
                <div className="h-full bg-primary rounded-full animate-pulse" style={{
                  width: meeting.status === 'recording' ? '25%' : meeting.status === 'transcribing' ? '50%' : meeting.status === 'summarizing' ? '75%' : '90%'
                }} />
              </div>
            </div>
          )}

          {/* AI Summary / Live Transcript Section */}
          <section className="mb-12">
            {meeting.status === 'done' ? (
              <>
                <h3 className="notion-subheading flex items-center gap-2 mb-4 text-primary">
                  <span className="material-symbols-outlined">auto_awesome</span>
                  <span>AI 회의록</span>
                </h3>
                <div className="prose prose-sm dark:prose-invert max-w-none text-text-secondary leading-relaxed whitespace-pre-wrap">
                  {meeting.content || meeting.summary}
                </div>
                {/* Collapsible raw transcript */}
                {meeting.transcriptA && (
                  <details className="mt-6 border border-slate-200 dark:border-slate-700 rounded-lg">
                    <summary className="px-4 py-3 text-sm font-medium text-text-secondary cursor-pointer hover:bg-slate-50 dark:hover:bg-slate-800 rounded-lg flex items-center gap-2">
                      <span className="material-symbols-outlined text-lg">notes</span>
                      원본 텍스트 보기
                    </summary>
                    <div className="px-4 pb-4 text-sm text-text-muted leading-relaxed whitespace-pre-wrap border-t border-slate-200 dark:border-slate-700 pt-3">
                      {meeting.transcriptA}
                    </div>
                  </details>
                )}
              </>
            ) : (
              <>
                <h3 className="notion-subheading flex items-center gap-2 mb-4">
                  <span className="material-symbols-outlined text-text-muted">subtitles</span>
                  라이브 텍스트
                </h3>
                <p className="text-text-secondary leading-relaxed whitespace-pre-wrap">
                  {meeting.transcriptA || meeting.content || '음성 인식 결과를 기다리는 중...'}
                </p>
              </>
            )}
          </section>

          <div className="notion-divider mb-12" />

          {/* Action Items Section */}
          <section className="mb-12">
            <div className="bg-primary/5 dark:bg-primary/10 border border-primary/20 rounded-xl p-6">
              <h3 className="flex items-center gap-2 mb-4 text-primary font-semibold">
                <span className="material-symbols-outlined">check_circle</span>
                Action Items
              </h3>
              <div className="space-y-4">
              {meeting.actionItems?.map((item) => (
                <div key={item.id} className="flex items-start gap-3">
                  <input
                    type="checkbox"
                    checked={item.completed}
                    onChange={() => handleActionItemToggle(item.id)}
                    className="mt-1 rounded border-border-default text-primary focus:ring-primary h-4 w-4"
                  />
                  <div className="flex flex-col">
                    <span className={`text-sm font-medium transition-all duration-200 ${item.completed ? 'line-through text-text-muted' : 'text-text-primary'}`}>
                      {item.text}
                    </span>
                    {item.assignee && (
                      <span className="text-[10px] text-text-muted mt-0.5">
                        Assigned to: @{item.assignee}
                      </span>
                    )}
                  </div>
                </div>
              ))}
              </div>
            </div>
          </section>

          <div className="notion-divider mb-12" />

          {/* Attachments Gallery */}
          {meeting.attachments && meeting.attachments.length > 0 && (
            <section className="mb-12">
              <AttachmentGallery
                attachments={meeting.attachments}
                onUploadClick={() => setShowUploader(true)}
              />
            </section>
          )}

          {/* Upload Modal */}
          {showUploader && (
            <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 backdrop-blur-sm p-4">
              <div className="bg-white dark:bg-slate-900 rounded-xl p-6 max-w-lg w-full">
                <div className="flex items-center justify-between mb-4">
                  <h3 className="font-bold text-text-primary">Upload Files</h3>
                  <button onClick={() => setShowUploader(false)} className="text-text-muted hover:text-text-secondary">
                    <span className="material-symbols-outlined">close</span>
                  </button>
                </div>
                <FileUploader
                  meetingId={meeting.meetingId}
                  onUploadComplete={async (files) => {
                    setShowUploader(false);
                    try {
                      const data = await meetingsApi.get(meeting.meetingId);
                      setMeeting(data as Meeting);
                    } catch (err) {
                      console.error('Failed to refresh meeting:', err);
                    }
                  }}
                />
              </div>
            </div>
          )}

          {/* Full Transcription */}
          {meeting.transcription && meeting.transcription.length > 0 && (
            <section className="mb-12">
              <div className="flex items-center justify-between mb-6">
                <h3 className="notion-subheading flex items-center gap-2">
                  <span className="material-symbols-outlined text-text-muted">notes</span>
                  Full Transcription
                </h3>
                <div className="flex gap-2">
                  <button className="px-3 py-1.5 rounded-md border border-border-default text-xs font-medium flex items-center gap-2 notion-hover">
                    <span className="material-symbols-outlined text-sm">search</span>
                    <span className="hidden sm:inline">Search</span>
                  </button>
                  <button className="px-3 py-1.5 rounded-md border border-border-default text-xs font-medium flex items-center gap-2 notion-hover">
                    <span className="material-symbols-outlined text-sm">download</span>
                    <span className="hidden sm:inline">Export</span>
                  </button>
                </div>
              </div>

              <div className="space-y-6">
                {meeting.transcription.map((segment) => (
                  <div key={segment.id} className="flex gap-4 lg:gap-6">
                    <div className="w-14 lg:w-16 pt-1 flex-shrink-0">
                      <span className="text-xs font-bold text-primary px-2 py-1 bg-primary/10 rounded">
                        {formatTimestamp(segment.startTime)}
                      </span>
                    </div>
                    <div className="flex-1 min-w-0">
                      <div className="flex items-center gap-2 mb-1.5">
                        <div
                          className="w-2 h-2 rounded-full shrink-0"
                          style={{ backgroundColor: `hsl(${segment.speaker.split('').reduce((a, c) => a + c.charCodeAt(0), 0) % 360}, 70%, 55%)` }}
                        />
                        <span className="text-sm font-black text-text-primary">{segment.speaker}</span>
                        <span className="text-[10px] text-text-muted">{segment.timestamp}</span>
                      </div>
                      <p className="text-text-secondary text-sm leading-relaxed">
                        {segment.text}
                      </p>
                    </div>
                  </div>
                ))}
              </div>
            </section>
          )}

          {/* Inline Q&A - mobile only */}
          <section className="lg:hidden border-t border-border-default pt-8">
            <h2 className="text-lg font-bold flex items-center gap-2 text-text-primary mb-6">
              <span className="material-symbols-outlined">question_answer</span>
              Meeting Q&A
            </h2>
            <QAPanel meetingId={meeting.meetingId} />
          </section>
          </div>
        </div>

        {/* Q&A Side Panel - Desktop only */}
        <aside className="hidden xl:flex w-96 border-l border-border-default flex-col sticky top-0 h-screen">
          <QAPanel meetingId={meeting.meetingId} />
        </aside>
      </div>
    </AppLayout>
  );
}
