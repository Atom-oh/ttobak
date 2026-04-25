'use client';

import React, { useState, useEffect, useRef, useMemo } from 'react';
import { useRouter, usePathname } from 'next/navigation';
import { useAuth } from '@/components/auth/AuthProvider';
import { AppLayout } from '@/components/layout/AppLayout';
import { AudioPlayer } from '@/components/AudioPlayer';
import { AudioUploader } from '@/components/AudioUploader';
import { AttachmentGallery } from '@/components/AttachmentGallery';
import { FileUploader } from '@/components/FileUploader';
import { QAPanel } from '@/components/QAPanel';
import { MeetingHeader } from '@/components/meeting/MeetingHeader';
import { AISummaryCard } from '@/components/meeting/AISummaryCard';
import { ActionItemsCard } from '@/components/meeting/ActionItemsCard';
import { ProcessingStatus } from '@/components/meeting/ProcessingStatus';
import { TranscriptSection } from '@/components/meeting/TranscriptSection';
import { SpeakerMapEditor } from '@/components/meeting/SpeakerMapEditor';
import { meetingsApi } from '@/lib/api';
import type { Meeting, MeetingDetail, ActionItem, SharedUser } from '@/types/meeting';

/** Normalize action items from API — handles legacy `done` field and missing `id` */
function normalizeActionItems(raw: unknown): ActionItem[] | undefined {
  if (!Array.isArray(raw) || raw.length === 0) return undefined;
  return raw.map((item: Record<string, unknown>, i: number) => ({
    id: (item.id as string) || `ai_${i + 1}`,
    text: (item.text as string) || '',
    completed: (item.completed as boolean) ?? (item.done as boolean) ?? false,
    assignee: item.assignee as string | undefined,
    dueDate: item.dueDate as string | undefined,
  }));
}

class MeetingErrorBoundary extends React.Component<
  { children: React.ReactNode },
  { error: Error | null }
> {
  constructor(props: { children: React.ReactNode }) {
    super(props);
    this.state = { error: null };
  }
  static getDerivedStateFromError(error: Error) {
    return { error };
  }
  render() {
    if (this.state.error) {
      return (
        <div className="min-h-screen flex items-center justify-center p-6">
          <div className="max-w-md w-full bg-red-50 dark:bg-red-900/20 border border-red-200 dark:border-red-800 rounded-xl p-6">
            <h2 className="text-lg font-bold text-red-700 dark:text-red-300 mb-2">페이지 오류</h2>
            <p className="text-sm text-red-600 dark:text-red-400 mb-4 break-all">{this.state.error.message}</p>
            <pre className="text-xs text-red-500/70 overflow-auto max-h-40 mb-4">{this.state.error.stack}</pre>
            <button
              onClick={() => window.location.href = '/'}
              className="px-4 py-2 bg-primary text-white rounded-lg text-sm font-medium"
            >
              홈으로 돌아가기
            </button>
          </div>
        </div>
      );
    }
    return this.props.children;
  }
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
        <div className="absolute right-0 top-full mt-1 bg-white dark:bg-surface dark:glass-panel border border-slate-200 dark:border-white/10 rounded-lg shadow-lg z-20 min-w-[120px]">
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
      <h3 className="text-base font-bold flex items-center gap-2 mb-4 dark:font-[var(--font-headline)] dark:text-text-main">
        <span className="material-symbols-outlined text-slate-400 dark:text-[#849396]">subtitles</span>
        라이브 텍스트
      </h3>
      <p className="text-slate-600 dark:text-[#BAC9CC] dark:font-[var(--font-body)] leading-relaxed whitespace-pre-wrap">
        {meeting.transcriptA || meeting.content || '음성 인식 결과를 기다리는 중...'}
      </p>
    </section>
  );
}

function RecoveryBanner({ meetingId, onRecovered }: { meetingId: string; onRecovered: () => void }) {
  const router = useRouter();
  const [recovering, setRecovering] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [deleting, setDeleting] = useState(false);

  const handleRecover = async () => {
    setRecovering(true);
    setError(null);
    try {
      await meetingsApi.recover(meetingId);
      onRecovered();
    } catch (err) {
      setError(err instanceof Error ? err.message : '복구에 실패했습니다');
    } finally {
      setRecovering(false);
    }
  };

  const handleDelete = async () => {
    setDeleting(true);
    try {
      await meetingsApi.delete(meetingId);
      router.push('/');
    } catch {
      setDeleting(false);
    }
  };

  const isNoProgress = error?.includes('progress file missing');

  return (
    <div className="mb-8 animate-fade-in">
      <div className="flex items-start gap-3 p-4 bg-red-50 dark:bg-red-900/20 border border-red-200 dark:border-red-800 rounded-xl">
        <span className="material-symbols-outlined text-red-500 mt-0.5">warning</span>
        <div className="flex-1">
          <span className="text-sm font-medium text-red-700 dark:text-red-300 block">
            이 녹음은 비정상 종료된 것으로 보입니다
          </span>
          {isNoProgress ? (
            <span className="text-xs text-red-600/70 dark:text-red-400/70 mt-0.5 block">
              저장된 체크포인트가 없어 복구할 수 없습니다. 이 미팅을 삭제하시겠습니까?
            </span>
          ) : (
            <span className="text-xs text-red-600/70 dark:text-red-400/70 mt-0.5 block">
              마지막 체크포인트까지의 오디오를 복구할 수 있습니다
            </span>
          )}
          {error && !isNoProgress && (
            <span className="text-xs text-red-600 dark:text-red-400 mt-1 block">{error}</span>
          )}
        </div>
        <div className="flex gap-2 shrink-0">
          {isNoProgress ? (
            <button
              onClick={handleDelete}
              disabled={deleting}
              className="px-4 py-2 rounded-lg text-xs font-bold bg-red-600 text-white hover:bg-red-700 transition-colors disabled:opacity-50"
            >
              {deleting ? '삭제 중...' : '미팅 삭제'}
            </button>
          ) : (
            <button
              onClick={handleRecover}
              disabled={recovering}
              className="px-4 py-2 rounded-lg text-xs font-bold border border-red-300 dark:border-red-700 text-red-700 dark:text-red-300 hover:bg-red-100 dark:hover:bg-red-900/40 transition-colors disabled:opacity-50"
            >
              {recovering ? '복구 중...' : '녹음 복구'}
            </button>
          )}
        </div>
      </div>
    </div>
  );
}

function MeetingDetailContent() {
  const router = useRouter();
  const { isAuthenticated, isLoading: authLoading } = useAuth();
  const [meeting, setMeeting] = useState<MeetingDetail | null>(null);
  const [isLoading, setIsLoading] = useState(true);
  const [showUploader, setShowUploader] = useState(false);
  const [audioUrl, setAudioUrl] = useState<string | null>(null);

  // Extract meeting ID from URL. usePathname() updates on client-side navigation,
  // unlike window.location.pathname in a mount-only effect which goes stale.
  // CloudFront rewrites /meeting/{id} → /meeting/_ for static export,
  // so useParams() returns "_" instead of the actual ID.
  const pathname = usePathname();
  const meetingId = useMemo(
    () => pathname.split('/meeting/')[1]?.split('/')[0] || '',
    [pathname]
  );

  // Fetch a fresh presigned audio URL when meeting has audio and is done
  useEffect(() => {
    if (meeting?.audioKey && meeting.status === 'done' && meetingId) {
      meetingsApi.audioUrl(meetingId).then(res => setAudioUrl(res.audioUrl)).catch(() => {});
    }
  }, [meeting?.audioKey, meeting?.status, meetingId]);

  useEffect(() => {
    if (!isAuthenticated || !meetingId) return;

    const fetchMeeting = async () => {
      try {
        const data = await meetingsApi.get(meetingId);
        const detail = data as MeetingDetail;
        detail.actionItems = normalizeActionItems(detail.actionItems);
        setMeeting(detail);
      } catch (err) {
        console.error('Failed to fetch meeting:', err);
      } finally {
        setIsLoading(false);
      }
    };
    fetchMeeting();
  }, [isAuthenticated, meetingId]);

  const refetchMeeting = async () => {
    if (!meetingId) return;
    try {
      const data = await meetingsApi.get(meetingId);
      const detail = data as MeetingDetail;
      detail.actionItems = normalizeActionItems(detail.actionItems);
      setMeeting(detail);
    } catch (err) {
      console.error('Failed to refetch meeting:', err);
    }
  };

  // Polling for in-progress meetings (timeout after 5 minutes)
  const pollCountRef = useRef(0);
  const [pollTimedOut, setPollTimedOut] = useState(false);
  const MAX_POLLS = 60; // 60 * 5s = 5 minutes

  useEffect(() => {
    if (!meeting || !['transcribing', 'summarizing'].includes(meeting.status)) return;
    if (pollTimedOut) return;

    const interval = setInterval(async () => {
      pollCountRef.current += 1;
      if (pollCountRef.current >= MAX_POLLS) {
        clearInterval(interval);
        setPollTimedOut(true);
        return;
      }
      try {
        const data = await meetingsApi.get(meeting.meetingId);
        const detail = data as MeetingDetail;
        detail.actionItems = normalizeActionItems(detail.actionItems);
        setMeeting(detail);
        if (data.status === 'done' || data.status === 'error') {
          clearInterval(interval);
        }
      } catch (err) {
        console.error('Polling failed:', err);
      }
    }, 5000);

    return () => clearInterval(interval);
  }, [meeting?.meetingId, meeting?.status, pollTimedOut]);

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
      <header className="lg:hidden sticky top-0 z-10 flex items-center bg-white/90 dark:bg-[#09090E]/90 backdrop-blur-md p-4 border-b border-slate-200 dark:border-white/10 justify-between">
        <button onClick={() => router.back()} className="text-slate-700 dark:text-text-main flex size-10 items-center justify-center rounded-full hover:bg-slate-100 dark:hover:bg-white/5">
          <span className="material-symbols-outlined">arrow_back</span>
        </button>
        <h2 className="text-slate-900 dark:text-text-main text-sm font-bold dark:font-[var(--font-headline)] flex-1 text-center">Meeting Report</h2>
        <MobileMoreMenu meetingId={meetingId} />
      </header>

      {/* Content */}
      <div className="flex flex-1 min-h-0">
        {/* Main Content */}
        <div className="flex-1 p-4 lg:p-8 overflow-y-auto">
          <div className="lg:max-w-5xl lg:mx-auto">
          {/* Header */}
          <MeetingHeader
            meeting={meeting}
            onShare={handleShare}
            onUnshare={handleUnshare}
            onTitleChange={async (newTitle) => {
              setMeeting({ ...meeting, title: newTitle });
              try {
                await meetingsApi.update(meeting.meetingId, { title: newTitle });
              } catch (err) {
                console.error('Failed to update title:', err);
                setMeeting(meeting);
              }
            }}
          />

          {/* Recovery banner for crashed recordings */}
          {meeting.status === 'recording' && (
            <RecoveryBanner meetingId={meetingId} onRecovered={refetchMeeting} />
          )}

          {/* Speaker Name Mapping — show when transcript exists (done or error with partial data) */}
          {(meeting.status === 'done' || meeting.transcription) && (
            <SpeakerMapEditor
              transcription={meeting.transcription}
              content={meeting.content}
              speakerMap={meeting.speakerMap}
              onSave={async (speakerMap) => {
                await meetingsApi.updateSpeakers(meeting.meetingId, speakerMap);
                const refreshed = await meetingsApi.get(meeting.meetingId);
                setMeeting(refreshed as Meeting);
              }}
            />
          )}

          {/* Core Content Grid - show summary when done OR when content exists (e.g. error with saved live summary) */}
          {(meeting.status === 'done' || meeting.content || meeting.summary) ? (
            <div className="grid grid-cols-1 lg:grid-cols-12 gap-8 mb-12">
              <div className="lg:col-span-7">
                <AISummaryCard
                  content={meeting.content}
                  summary={meeting.summary}
                  transcriptA={meeting.transcriptA}
                  onSave={async (html) => {
                    await meetingsApi.update(meeting.meetingId, { content: html });
                  }}
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
              {pollTimedOut ? (
                <div className="mb-8 animate-fade-in">
                  <div className="flex items-center gap-3 p-4 bg-amber-50 dark:bg-amber-900/20 border border-amber-200 dark:border-amber-800 rounded-xl">
                    <span className="material-symbols-outlined text-amber-500">schedule</span>
                    <div className="flex-1">
                      <span className="text-sm font-medium text-amber-700 dark:text-amber-300 block">
                        처리 시간이 초과되었습니다
                      </span>
                      <span className="text-xs text-amber-600/70 dark:text-amber-400/70 mt-0.5 block">
                        음성 변환이 예상보다 오래 걸리고 있습니다
                      </span>
                    </div>
                    <button
                      onClick={() => { pollCountRef.current = 0; setPollTimedOut(false); }}
                      className="px-3 py-1.5 rounded-lg text-xs font-medium border border-amber-300 dark:border-amber-700 text-amber-700 dark:text-amber-300 hover:bg-amber-100 dark:hover:bg-amber-900/40 transition-colors shrink-0"
                    >
                      다시 확인
                    </button>
                  </div>
                </div>
              ) : (
                <ProcessingStatus status={meeting.status} />
              )}
              <LiveTranscriptSection meeting={meeting} />
            </>
          ) : null}

          {/* Meeting Notes */}
          {meeting.notes && (
            <section className="mb-12">
              <h3 className="text-base font-bold flex items-center gap-2 mb-4 dark:font-[var(--font-headline)] dark:text-text-main">
                <span className="material-symbols-outlined text-slate-400 dark:text-[#849396]">edit_note</span>
                미팅 노트
              </h3>
              <div className="bg-white dark:bg-surface-lowest glass-panel rounded-xl p-5 dark:border dark:border-white/10">
                <p className="text-slate-700 dark:text-[#BAC9CC] dark:font-[var(--font-body)] leading-relaxed whitespace-pre-wrap text-sm">
                  {meeting.notes}
                </p>
              </div>
            </section>
          )}

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
              <div className="bg-white dark:bg-surface-lowest glass-panel rounded-xl p-6 max-w-lg w-full dark:border dark:border-white/10">
                <div className="flex items-center justify-between mb-4">
                  <h3 className="font-bold text-slate-900 dark:text-text-main dark:font-[var(--font-headline)]">Upload Files</h3>
                  <button onClick={() => setShowUploader(false)} className="text-slate-400 hover:text-slate-600 dark:hover:text-[#849396]">
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
          {((meeting.transcription?.length ?? 0) > 0 || meeting.transcriptA) && (
            <TranscriptSection
              transcription={meeting.transcription || []}
              rawTranscript={meeting.transcriptA}
              onSaveRawTranscript={async (text) => {
                await meetingsApi.update(meeting.meetingId, { transcriptA: text });
              }}
            />
          )}

          {/* Inline Q&A - mobile only */}
          <section className="lg:hidden border-t border-slate-200 dark:border-white/10 pt-8">
            <h2 className="text-lg font-bold flex items-center gap-2 text-slate-900 dark:text-text-main dark:font-[var(--font-headline)] mb-6">
              <span className="material-symbols-outlined">question_answer</span>
              Meeting Q&A
            </h2>
            <QAPanel meetingId={meeting.meetingId} />
          </section>

          {/* Audio Player / Uploader */}
          {audioUrl ? (
            <AudioPlayer audioUrl={audioUrl} />
          ) : (meeting.status === 'done' || meeting.status === 'error') && !meeting.audioKey ? (
            <AudioUploader meetingId={meeting.meetingId} onUploadComplete={refetchMeeting} />
          ) : null}
          </div>
        </div>

        {/* Q&A Side Panel - Desktop only */}
        <aside className="hidden lg:flex lg:w-80 xl:w-96 border-l border-slate-200 dark:border-white/10 dark:bg-surface-lowest/50 flex-col sticky top-0 h-screen">
          <QAPanel meetingId={meeting.meetingId} />
        </aside>
      </div>
    </AppLayout>
  );
}

export default function MeetingDetailPage() {
  return (
    <MeetingErrorBoundary>
      <MeetingDetailContent />
    </MeetingErrorBoundary>
  );
}
