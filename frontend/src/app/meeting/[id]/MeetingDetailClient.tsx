'use client';

import { useState, useEffect, useRef, useMemo } from 'react';
import { useRouter, usePathname } from 'next/navigation';
import { useAuth } from '@/components/auth/AuthProvider';
import { AppLayout } from '@/components/layout/AppLayout';
import { AudioPlayer } from '@/components/AudioPlayer';
import { AttachmentGallery } from '@/components/AttachmentGallery';
import { FileUploader } from '@/components/FileUploader';
import { QAPanel } from '@/components/QAPanel';
import { MeetingHeader } from '@/components/meeting/MeetingHeader';
import { AISummaryCard } from '@/components/meeting/AISummaryCard';
import { ActionItemsCard } from '@/components/meeting/ActionItemsCard';
import { ProcessingStatus } from '@/components/meeting/ProcessingStatus';
import { TranscriptSection } from '@/components/meeting/TranscriptSection';
import { meetingsApi } from '@/lib/api';
import type { Meeting, MeetingDetail, SharedUser } from '@/types/meeting';

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

function LiveTranscriptSection({ meeting }: { meeting: MeetingDetail }) {
  return (
    <section className="mb-12">
      <h3 className="notion-subheading flex items-center gap-2 mb-4">
        <span className="material-symbols-outlined text-[var(--color-text-muted)]">subtitles</span>
        라이브 텍스트
      </h3>
      <p className="text-[var(--color-text-secondary)] leading-relaxed whitespace-pre-wrap">
        {meeting.transcriptA || meeting.content || '음성 인식 결과를 기다리는 중...'}
      </p>
    </section>
  );
}

export default function MeetingDetailPage() {
  const router = useRouter();
  const { isAuthenticated, isLoading: authLoading } = useAuth();
  const [meeting, setMeeting] = useState<MeetingDetail | null>(null);
  const [isLoading, setIsLoading] = useState(true);
  const [showUploader, setShowUploader] = useState(false);
  const [audioUrl, setAudioUrl] = useState<string | null>(null);

  // Derive audio URL from attachments when meeting is done
  useEffect(() => {
    if (meeting?.audioKey && meeting.status === 'done') {
      const audioAttachment = meeting.attachments?.find(a => a.type === 'audio');
      if (audioAttachment) setAudioUrl(audioAttachment.url);
    }
  }, [meeting?.audioKey, meeting?.status, meeting?.attachments]);

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
        <h2 className="text-[var(--color-text-primary)] text-sm font-bold flex-1 text-center">Meeting Report</h2>
        <MobileMoreMenu meetingId={meetingId} />
      </header>

      {/* Content */}
      <div className="flex flex-1 min-h-0">
        {/* Main Content */}
        <div className="flex-1 p-4 lg:px-16 lg:py-12 overflow-y-auto">
          <div className="lg:max-w-5xl">
          {/* Header */}
          <MeetingHeader
            meeting={meeting}
            onShare={handleShare}
            onUnshare={handleUnshare}
          />

          {/* Core Content Grid - matches design sample */}
          {meeting.status === 'done' ? (
            <div className="grid grid-cols-1 lg:grid-cols-12 gap-6 mb-12">
              <div className="lg:col-span-7">
                <AISummaryCard
                  content={meeting.content}
                  summary={meeting.summary}
                  transcriptA={meeting.transcriptA}
                />
              </div>
              <div className="lg:col-span-5">
                <ActionItemsCard
                  items={meeting.actionItems}
                  onToggle={handleActionItemToggle}
                />
              </div>
            </div>
          ) : meeting.status !== 'error' ? (
            <>
              <ProcessingStatus status={meeting.status} />
              <LiveTranscriptSection meeting={meeting} />
            </>
          ) : null}

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
            <TranscriptSection transcription={meeting.transcription} />
          )}

          {/* Inline Q&A - mobile only */}
          <section className="lg:hidden border-t border-border-default pt-8">
            <h2 className="text-lg font-bold flex items-center gap-2 text-text-primary mb-6">
              <span className="material-symbols-outlined">question_answer</span>
              Meeting Q&A
            </h2>
            <QAPanel meetingId={meeting.meetingId} />
          </section>

          {/* Audio Player */}
          {audioUrl && <AudioPlayer audioUrl={audioUrl} />}
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
