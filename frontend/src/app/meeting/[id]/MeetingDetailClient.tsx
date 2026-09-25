'use client';

import React, { useState, useEffect, useRef, useMemo, useCallback } from 'react';
import { useRouter, usePathname } from 'next/navigation';
import { useAuth } from '@/components/auth/AuthProvider';
import { AppLayout } from '@/components/layout/AppLayout';
import { AudioPlayer } from '@/components/AudioPlayer';
import { AudioCropEditor } from '@/components/meeting/AudioCropEditor';
import { ApiError } from '@/lib/api';
import { formatAudioTime } from '@/lib/audioRange';
import { AudioUploader } from '@/components/AudioUploader';
import { AttachmentGallery } from '@/components/AttachmentGallery';
import { FileUploader } from '@/components/FileUploader';
import { IndexStatus } from '@/components/IndexStatus';
import { ResummaryControls } from '@/components/meeting/ResummaryControls';
import { useResummary } from '@/hooks/useResummary';
import { QAPanel } from '@/components/QAPanel';
import { FieldInsightsSection } from '@/components/FieldInsightsSection';
import { MeetingNotesEditor } from '@/components/meeting/MeetingNotesEditor';
import { SAFollowUp } from '@/components/meeting/SAFollowUp';
import { appendMeetingNotes, qaNoteMarkdown, referenceMarkdown, type MeetingReference, type QAReferenceEvidence, type QuestionDraft } from '@/lib/meetingReferences';
import ReferenceTabs from '@/components/ReferenceTabs';
import ReferencePanel from '@/components/ReferencePanel';
import { MeetingHeader } from '@/components/meeting/MeetingHeader';
import { AISummaryCard } from '@/components/meeting/AISummaryCard';
import { MarkdownRenderer } from '@/components/markdown/MarkdownRenderer';
import { ActionItemsCard } from '@/components/meeting/ActionItemsCard';
import { ProcessingStatus } from '@/components/meeting/ProcessingStatus';
import { TranscriptSection } from '@/components/meeting/TranscriptSection';
import { SpeakerMapEditor } from '@/components/meeting/SpeakerMapEditor';
import AccountSection from '@/components/meeting/AccountSection';
import { SimCard } from '@/components/meeting/SimCard';
import { useResizablePanel } from '@/hooks/useResizablePanel';
import { meetingsApi } from '@/lib/api';
import type { MeetingDetail, ActionItem, SharedUser } from '@/types/meeting';

/** Map backend attachment response to frontend Attachment type */
function normalizeAttachments(raw: unknown): import('@/types/meeting').Attachment[] | undefined {
  if (!Array.isArray(raw) || raw.length === 0) return undefined;
  return raw.map((att) => ({
    id: att.attachmentId || att.id || '',
    name: att.fileName || att.name || 'Untitled',
    type: ['photo', 'screenshot', 'diagram', 'whiteboard'].includes(att.type) ? 'image' as const
      : att.type === 'audio_file' ? 'audio' as const : att.type,
    url: att.url || '',
    processedContent: att.processedContent,
    size: att.fileSize || att.size,
    mimeType: att.mimeType,
    status: att.status,
    createdAt: att.createdAt || '',
    originalKey: att.originalKey,
    textExtraction: att.textExtraction,
  }));
}

/** Replace attachment:// URLs in content with presigned download URLs */
function resolveAttachmentUrls(content: string, attachments?: import('@/types/meeting').Attachment[]): string {
  if (!content || !attachments?.length) return content;
  return content.replace(/attachment:\/\/([a-f0-9-]+)/gi, (match, id) => {
    const att = attachments.find((a) => a.id === id);
    return att?.url || match;
  });
}

/**
 * ADR-013: rewrite `transcript://{segmentId}` deep links emitted by the
 * summarize Lambda into in-page `#ts-{segmentId}` anchors. rehype-sanitize's
 * default schema only permits http/https/mailto/tel/hash hrefs, so the
 * `transcript://` scheme would otherwise be stripped. `MarkdownRenderer`
 * detects the `#ts-` prefix and attaches smooth-scroll + highlight on click.
 */
function resolveTranscriptLinks(content: string): string {
  if (!content) return content;
  return content.replace(/transcript:\/\/([a-zA-Z0-9_\-]+)/g, '#ts-$1');
}

/** Collapsible card showing the real-time summary captured during recording (markdown incl. mermaid). */
function LiveSummaryCard({ content }: { content: string }) {
  const [expanded, setExpanded] = useState(false);
  return (
    <div className="mb-8 rounded-xl border border-slate-200 dark:border-slate-800 bg-white dark:bg-slate-900 shadow-sm">
      <button
        type="button"
        onClick={() => setExpanded((v) => !v)}
        className="w-full flex items-center gap-2 px-5 py-4 text-left"
      >
        <span className="material-symbols-outlined text-primary">bolt</span>
        <h3 className="text-sm font-semibold text-slate-900 dark:text-white">실시간 요약</h3>
        <span className="ml-1 text-xs text-slate-400 dark:text-slate-500">녹음 중 생성됨</span>
        <span className="material-symbols-outlined ml-auto text-slate-400">
          {expanded ? 'expand_less' : 'expand_more'}
        </span>
      </button>
      {expanded && (
        <div className="px-5 pb-5 border-t border-slate-100 dark:border-slate-800 pt-4">
          <MarkdownRenderer content={content} />
        </div>
      )}
    </div>
  );
}

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
      <h3 className="text-base font-bold flex items-center gap-2 mb-4 dark:font-headline dark:text-text-main">
        <span className="material-symbols-outlined text-slate-400 dark:text-text-muted">subtitles</span>
        라이브 텍스트
      </h3>
      <p className="text-slate-600 dark:text-text-secondary dark:font-body leading-relaxed whitespace-pre-wrap">
        {meeting.transcriptA || meeting.content || '음성 인식 결과를 기다리는 중...'}
      </p>
    </section>
  );
}

function RecoveryBanner({ meetingId, onRecovered }: { meetingId: string; onRecovered: () => void }) {
  const router = useRouter();
  const [recovering, setRecovering] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [isNoProgress, setIsNoProgress] = useState(false);

  const handleRecover = async () => {
    setRecovering(true);
    setError(null);
    setIsNoProgress(false);
    try {
      await meetingsApi.recover(meetingId);
      onRecovered();
    } catch (err) {
      setIsNoProgress(err instanceof ApiError && err.code === 'RECORDING_CHECKPOINT_MISSING');
      setError(err instanceof Error ? err.message : '복구에 실패했습니다');
    } finally {
      setRecovering(false);
    }
  };

  return (
    <div className="mb-8 animate-fade-in">
      <div className="flex items-start gap-3 p-4 bg-red-50 dark:bg-red-900/20 border border-red-200 dark:border-red-800 rounded-xl">
        <span className="material-symbols-outlined text-red-500 mt-0.5">warning</span>
        <div className="flex-1">
          <span className="text-sm font-medium text-red-700 dark:text-red-300 block">
            종료되지 않은 녹음이 있습니다
          </span>
          {isNoProgress ? (
            <span className="text-xs text-red-600/70 dark:text-red-400/70 mt-0.5 block">
              서버 중간 저장본이 없습니다. 녹음 화면에서 이 브라우저에 보관된 녹음을 확인해 주세요.
            </span>
          ) : (
            <span className="text-xs text-red-600/70 dark:text-red-400/70 mt-0.5 block">
              다른 탭에서 녹음 중이면 먼저 종료해 주세요. 서버의 마지막 중간 저장본으로 마무리할 수 있습니다.
            </span>
          )}
          {error && !isNoProgress && (
            <span className="text-xs text-red-600 dark:text-red-400 mt-1 block">{error}</span>
          )}
        </div>
        <div className="flex gap-2 shrink-0">
          {isNoProgress ? (
            <button onClick={() => router.push('/record')} className="px-4 py-2 text-xs font-bold text-primary">기기 보관본 확인</button>
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
  const { isAuthenticated, isLoading: authLoading, user } = useAuth();
  const [meeting, setMeeting] = useState<MeetingDetail | null>(null);
  const [summarySave, setSummarySave] = useState<{ revision: number; hasContent: boolean } | null>(null);
  const summaryRevisionRef = useRef(0);
  const [indexRevision, setIndexRevision] = useState(0);
  const [summaryDirty, setSummaryDirty] = useState(false);
  const [titleDirty, setTitleDirty] = useState(false);
  const [notesDirty, setNotesDirty] = useState(false);
  const [notesDraft, setNotesDraft] = useState('');
  const notesDraftRef = useRef('');
  const savedNotesRef = useRef('');
  const savedNotesRevisionRef = useRef<string | undefined>(undefined);
  const notesUnconfirmedRef = useRef(false);
  const notesDraftRevisionRef = useRef(0);
  const notesSaveRevisionRef = useRef(0);
  const relationRevisionRef = useRef(0);
  const relationsRef = useRef<{ accountId?: string; sharedToAccount?: boolean; projectIds?: string[] }>({});
  const [notesMessage, setNotesMessage] = useState('');
  const [referenceTab, setReferenceTab] = useState<'qa' | 'ref'>('ref');
  const [questionDraft, setQuestionDraft] = useState<QuestionDraft>();
  const [transcriptDirty, setTranscriptDirty] = useState(false);
  const dirtyRef = useRef({ summary: false, title: false, transcript: false, notes: false });
  const onSummaryDirtyChange = useCallback((dirty: boolean) => { dirtyRef.current.summary = dirty; setSummaryDirty(dirty); }, []);
  const onTitleDirtyChange = useCallback((dirty: boolean) => { dirtyRef.current.title = dirty; setTitleDirty(dirty); }, []);
  const onTranscriptDirtyChange = useCallback((dirty: boolean) => { dirtyRef.current.transcript = dirty; setTranscriptDirty(dirty); }, []);
  const isResummaryDirty = useCallback(() => Object.values(dirtyRef.current).some(Boolean), []);
  const onIndexedContentSaved = useCallback(() => setIndexRevision((value) => value + 1), []);
  const [isLoading, setIsLoading] = useState(true);
  const [showUploader, setShowUploader] = useState(false);
  const [showAudioUploader, setShowAudioUploader] = useState(false);
  const [audioSource, setAudioSource] = useState<{ meetingId: string; revision: string; url?: string; urls: string[] } | null>(null);
  const audioRevision = JSON.stringify([meeting?.audioKey, meeting?.audioKeys]);
  const audioUrl = audioSource?.meetingId === meeting?.meetingId && audioSource?.revision === audioRevision ? audioSource?.url : undefined;
  const audioUrls = audioSource?.meetingId === meeting?.meetingId && audioSource?.revision === audioRevision ? audioSource?.urls || [] : [];
  // reserve = space owed to the sibling column: divider width + row gaps + the
  // sibling's own min-width, so effectiveMax leaves that column at least its
  // floor rather than a flat viewport ratio that can't see what's next to it.
  const { width: asideWidth, startDrag: startAsideDrag, containerRef: pageRowRef } = useResizablePanel('ttobak:meetingAsideWidth', 384, 280, 640, 'right', 488);
  const { width: summaryWidth, startDrag: startSummaryDrag, containerRef: summaryRowRef, fits: summaryFits } = useResizablePanel('ttobak:meetingSummaryWidth', 640, 400, 900, 'left', 352);

  // Extract meeting ID from URL. usePathname() updates on client-side navigation,
  // unlike window.location.pathname in a mount-only effect which goes stale.
  // CloudFront rewrites /meeting/{id} → /meeting/_ for static export,
  // so useParams() returns "_" instead of the actual ID.
  const pathname = usePathname();
  const meetingId = useMemo(
    () => pathname.split('/meeting/')[1]?.split('/')[0] || '',
    [pathname]
  );
  const onNotesChange = useCallback((value: string) => {
    notesDraftRevisionRef.current++;
    notesDraftRef.current = value;
    dirtyRef.current.notes = notesUnconfirmedRef.current || value !== savedNotesRef.current;
    setNotesDirty(dirtyRef.current.notes);
    setNotesDraft(value);
  }, []);
  const onNotesDirtyChange = useCallback((dirty: boolean, requiresConfirmation = false) => {
    notesUnconfirmedRef.current = requiresConfirmation;
    dirtyRef.current.notes = dirty || requiresConfirmation || notesDraftRef.current !== savedNotesRef.current;
    setNotesDirty(dirtyRef.current.notes);
  }, []);
  const onNotesSaved = useCallback((value: string, revision: string) => {
    notesSaveRevisionRef.current++;
    setNotesMessage('');
    savedNotesRef.current = value;
    savedNotesRevisionRef.current = revision;
    dirtyRef.current.notes = notesUnconfirmedRef.current || notesDraftRef.current !== value;
    setNotesDirty(dirtyRef.current.notes);
    setMeeting((current) => current?.meetingId === meetingId ? { ...current, notes: value, notesRevision: revision } : current);
    setIndexRevision((value) => value + 1);
  }, [meetingId]);
  const applyNotesFromDetail = useCallback((detail: MeetingDetail, acknowledgedEpoch: number, draftEpoch: number) => {
    if (!dirtyRef.current.notes && acknowledgedEpoch === notesSaveRevisionRef.current &&
        draftEpoch === notesDraftRevisionRef.current) {
      savedNotesRef.current = detail.notes || '';
      savedNotesRevisionRef.current = detail.notesRevision;
      notesDraftRef.current = detail.notes || '';
      setNotesDraft(detail.notes || '');
    } else {
      // Never pair the acknowledged text with a version from a stale background read.
      detail.notes = savedNotesRef.current;
      detail.notesRevision = savedNotesRevisionRef.current;
    }
  }, []);
  const appendNotes = useCallback((block: string) => {
    const next = appendMeetingNotes(notesDraftRef.current, block);
    onNotesChange(next);
    setNotesMessage('참고 내용을 메모 초안에 추가했습니다. 메모 저장을 눌러 반영해 주세요.');
    document.getElementById('meeting-notes')?.scrollIntoView({ behavior: 'smooth', block: 'start' });
  }, [onNotesChange]);
  const onAddReference = useCallback((reference: MeetingReference) => appendNotes(referenceMarkdown(reference)), [appendNotes]);
  const onSaveQAToNotes = useCallback((question: string, answer: string, evidence?: QAReferenceEvidence) => {
    appendNotes(qaNoteMarkdown(question, answer, evidence));
  }, [appendNotes]);
  const onPrepareQuestion = useCallback((text: string) => {
    setQuestionDraft((current) => ({ id: (current?.id || 0) + 1, text })); setReferenceTab('qa');
    document.getElementById('meeting-qa-mobile')?.scrollIntoView({ behavior: 'smooth', block: 'start' });
  }, []);
  const onAccountChanged = useCallback((accountId: string, sharedToAccount: boolean) => {
    relationRevisionRef.current++;
    relationsRef.current = { ...relationsRef.current, accountId, sharedToAccount };
    setMeeting((current) => current?.meetingId === meetingId ? { ...current, ...relationsRef.current } : current);
  }, [meetingId]);
  const onProjectsChanged = useCallback((projectIds: string[]) => {
    relationRevisionRef.current++;
    relationsRef.current = { ...relationsRef.current, projectIds };
    setMeeting((current) => current?.meetingId === meetingId ? { ...current, ...relationsRef.current } : current);
  }, [meetingId]);
  const applyResummary = useCallback((content: string) => {
    if (isResummaryDirty()) return;
    summaryRevisionRef.current++;
    setMeeting((current) => current?.meetingId === meetingId ? { ...current, content, summary: undefined } : current);
    setSummarySave((current) => ({ revision: (current?.revision ?? 0) + 1, hasContent: !!content.trim() }));
    setIndexRevision((value) => value + 1);
  }, [meetingId, isResummaryDirty]);
  const resummary = useResummary({
    meetingId,
    enabled: isAuthenticated && meeting?.meetingId === meetingId,
    canEdit: !!meeting && (meeting.permission === 'edit' || (!meeting.isShared && meeting.permission !== 'read')),
    isDirty: isResummaryDirty,
    onLoaded: applyResummary,
  });

  const hasAudio = meeting?.audioKey || (meeting?.audioKeys && meeting.audioKeys.length > 0);
  useEffect(() => {
    let canceled = false;
    if (hasAudio && meeting?.meetingId === meetingId && (meeting?.status === 'done' || meeting?.status === 'error') && meetingId) {
      meetingsApi.audioUrl(meetingId).then(res => {
        if (!canceled) setAudioSource({ meetingId, revision: audioRevision, url: res.audioUrl, urls: res.audioUrls || [] });
      }).catch(() => {});
    }
    return () => { canceled = true; };
  }, [hasAudio, meeting?.status, meeting?.meetingId, meetingId, audioRevision]);

  useEffect(() => {
    if (!isAuthenticated || !meetingId) return;
    const controller = new AbortController();

    const fetchMeeting = async () => {
      try {
        const notesEpoch = notesSaveRevisionRef.current;
        const draftEpoch = notesDraftRevisionRef.current;
        const relationRevision = relationRevisionRef.current;
        const data = await meetingsApi.get(meetingId, { signal: controller.signal });
        if (controller.signal.aborted) return;
        const detail = data as MeetingDetail;
        detail.actionItems = normalizeActionItems(detail.actionItems);
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        detail.attachments = normalizeAttachments((data as any).attachments);
        // API returns the shared-users list as `shares`; MeetingHeader/ShareButton
        // read `sharedWith` -- without this, a fresh fetch always shows an empty
        // share list even though the share was persisted server-side.
        detail.sharedWith = detail.shares;
        applyNotesFromDetail(detail, notesEpoch, draftEpoch);
        if (relationRevision === relationRevisionRef.current) relationsRef.current = { accountId: detail.accountId, sharedToAccount: detail.sharedToAccount, projectIds: detail.projectIds };
        else Object.assign(detail, relationsRef.current);
        setMeeting(detail);
      } catch (err) {
        if (!controller.signal.aborted) console.error('Failed to fetch meeting:', err);
      } finally {
        if (!controller.signal.aborted) setIsLoading(false);
      }
    };
    fetchMeeting();
    return () => controller.abort();
  }, [isAuthenticated, meetingId, applyNotesFromDetail]);

  const refetchMeeting = async () => {
    if (!meetingId) return;
    try {
      const notesEpoch = notesSaveRevisionRef.current;
      const draftEpoch = notesDraftRevisionRef.current;
      const relationRevision = relationRevisionRef.current;
      const data = await meetingsApi.get(meetingId);
      const detail = data as MeetingDetail;
      detail.actionItems = normalizeActionItems(detail.actionItems);
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      detail.attachments = normalizeAttachments((data as any).attachments);
      detail.sharedWith = detail.shares;
      applyNotesFromDetail(detail, notesEpoch, draftEpoch);
      if (relationRevision === relationRevisionRef.current) relationsRef.current = { accountId: detail.accountId, sharedToAccount: detail.sharedToAccount, projectIds: detail.projectIds };
      else Object.assign(detail, relationsRef.current);
      setMeeting(detail);
    } catch (err) {
      console.error('Failed to refetch meeting:', err);
    }
  };

  // Polling for in-progress meetings (timeout after 5 minutes)
  const pollCountRef = useRef(0);
  const [pollTimedOut, setPollTimedOut] = useState(false);
  const MAX_POLLS = 60; // 60 * 5s = 5 minutes
  const polledMeetingId = meeting?.meetingId;
  const polledMeetingStatus = meeting?.status;

  useEffect(() => {
    if (!polledMeetingId || !polledMeetingStatus || !['transcribing', 'summarizing'].includes(polledMeetingStatus)) return;
    if (pollTimedOut) return;

    const interval = setInterval(async () => {
      pollCountRef.current += 1;
      if (pollCountRef.current >= MAX_POLLS) {
        clearInterval(interval);
        setPollTimedOut(true);
        return;
      }
      try {
        const notesEpoch = notesSaveRevisionRef.current;
        const draftEpoch = notesDraftRevisionRef.current;
        const relationRevision = relationRevisionRef.current;
        const data = await meetingsApi.get(polledMeetingId);
        const detail = data as MeetingDetail;
        detail.actionItems = normalizeActionItems(detail.actionItems);
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        detail.attachments = normalizeAttachments((data as any).attachments);
        detail.sharedWith = detail.shares;
        applyNotesFromDetail(detail, notesEpoch, draftEpoch);
        if (relationRevision === relationRevisionRef.current) relationsRef.current = { accountId: detail.accountId, sharedToAccount: detail.sharedToAccount, projectIds: detail.projectIds };
        else Object.assign(detail, relationsRef.current);
        setMeeting(detail);
        if (data.status === 'done' || data.status === 'error') {
          clearInterval(interval);
        }
      } catch (err) {
        console.error('Polling failed:', err);
      }
    }, 5000);

    return () => clearInterval(interval);
  }, [polledMeetingId, polledMeetingStatus, pollTimedOut, applyNotesFromDetail]);

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

  const usingTranscriptB = Boolean(meeting.transcriptB?.trim() && (meeting.selectedTranscript === 'B' || !meeting.transcriptA?.trim()));
  const displayedTranscript = usingTranscriptB ? meeting.transcriptB : (meeting.transcriptA?.trim() ? meeting.transcriptA : undefined);
  const canEdit = meeting.permission === 'edit' || (!meeting.isShared && meeting.permission !== 'read');
  const canEditNotes = canEdit && meeting.supportsNotesComparison === true && typeof meeting.notesRevision === 'string';
  const canManageAssociations = meeting.userId === user?.userId;
  const canUpload = !meeting.isShared && meeting.permission !== 'read';

  return (
    <AppLayout activePath="/">
      {/* Mobile Header */}
      <header className="lg:hidden sticky top-0 z-10 flex items-center bg-white/90 dark:bg-background-dark/90 backdrop-blur-md p-4 border-b border-slate-200 dark:border-white/10 justify-between">
        <button onClick={() => router.back()} className="text-slate-700 dark:text-text-main flex size-10 items-center justify-center rounded-full hover:bg-slate-100 dark:hover:bg-white/5">
          <span className="material-symbols-outlined">arrow_back</span>
        </button>
        <h2 className="text-slate-900 dark:text-text-main text-sm font-bold dark:font-headline flex-1 text-center">Meeting Report</h2>
        <MobileMoreMenu meetingId={meetingId} />
      </header>

      {/* Content */}
      <div ref={pageRowRef} className="flex flex-1 min-h-0">
        {/* Main Content */}
        <div className="flex-1 p-4 lg:p-8 overflow-y-auto">
          <div className="lg:max-w-7xl lg:mx-auto">
          {/* Header */}
          <MeetingHeader
            meeting={meeting}
            onShare={handleShare}
            onUnshare={handleUnshare}
            onTitleDirtyChange={onTitleDirtyChange}
            onTitleChange={canEdit ? async (newTitle) => {
              await meetingsApi.update(meeting.meetingId, { title: newTitle });
              setMeeting((current) => current?.meetingId === meeting.meetingId ? { ...current, title: newTitle } : current);
              setIndexRevision((value) => value + 1);
            } : undefined}
            onLinkedMeetingsChange={(ids) => setMeeting({ ...meeting, linkedMeetingIds: ids })}
          />
          <IndexStatus target={{ kind: 'meeting', meetingId: meeting.meetingId }} savedRevision={indexRevision} dirty={summaryDirty || titleDirty || transcriptDirty || notesDirty} />
          <ResummaryControls
            status={resummary.status}
            canRequest={canEdit && (meeting.status === 'done' || meeting.status === 'error')}
            dirty={summaryDirty || titleDirty || transcriptDirty || notesDirty}
            busy={!!resummary.action}
            error={resummary.error}
            onRequest={() => { void resummary.request(); }}
            onRefresh={() => { void resummary.refresh(); }}
            onLoad={() => { void resummary.load(); }}
          />

          {/* Recovery banner for crashed recordings */}
          {(meeting.canRecoverRecording ?? (meeting.status === 'recording' && meeting.userId === user?.userId)) && (
            <RecoveryBanner meetingId={meetingId} onRecovered={refetchMeeting} />
          )}
          {meeting.audioCrop && <div className="mb-6 rounded-lg border border-slate-200 p-3 text-sm dark:border-white/10">
            <p>원본의 {formatAudioTime(meeting.audioCrop.startSeconds)} ~ {formatAudioTime(meeting.audioCrop.endSeconds)} 구간으로 만든 미팅입니다.</p>
            {meeting.audioCrop.state === 'failed' && <p role="alert" className="mt-1 text-red-600 dark:text-red-300">구간 처리에 실패했습니다. 원본 미팅에서 다시 요청할 수 있습니다.</p>}
            <button type="button" onClick={() => router.push(`/meeting/${encodeURIComponent(meeting.audioCrop!.sourceMeetingId)}`)} className="mt-2 text-xs text-primary">원본 미팅 열기</button>
          </div>}

          {/* Speaker Name Mapping — show when transcript exists (done or error with partial data) */}
          {(meeting.status === 'done' || meeting.transcription) && (
            <SpeakerMapEditor
              transcription={meeting.transcription}
              content={meeting.content}
              speakerMap={meeting.speakerMap}
              onSave={async (speakerMap) => {
                await meetingsApi.updateSpeakers(meeting.meetingId, speakerMap);
                setIndexRevision((value) => value + 1);
                const summaryRevision = summaryRevisionRef.current;
                const refreshed = await meetingsApi.get(meeting.meetingId);
                setMeeting((current) => current?.meetingId === meeting.meetingId ? {
                  ...current, ...(refreshed as MeetingDetail),
                  // The user may have started editing while either request was pending.
                  ...(dirtyRef.current.summary || summaryRevision !== summaryRevisionRef.current
                    ? { content: current.content, summary: current.summary } : {}),
                  ...(dirtyRef.current.transcript ? {
                    transcriptA: current.transcriptA,
                    transcriptB: current.transcriptB,
                    transcription: current.transcription,
                    selectedTranscript: current.selectedTranscript,
                  } : {}),
                  // Refreshed download URLs also change the summary editor's HTML.
                  attachments: dirtyRef.current.summary ? current.attachments : normalizeAttachments(refreshed.attachments),
                } : current);
              }}
              sttProvider={meeting.sttProvider}
              audioPartCount={meeting.audioPartCount}
              onRediarize={async (speakerCount) => {
                await meetingsApi.rediarize(meeting.meetingId, speakerCount);
                const summaryRevision = summaryRevisionRef.current;
                const refreshed = await meetingsApi.get(meeting.meetingId);
                setMeeting((current) => current?.meetingId === meeting.meetingId ? {
                  ...current, ...(refreshed as MeetingDetail),
                  ...(dirtyRef.current.summary || summaryRevision !== summaryRevisionRef.current
                    ? { content: current.content, summary: current.summary } : {}),
                  ...(dirtyRef.current.transcript ? {
                    transcriptA: current.transcriptA,
                    transcriptB: current.transcriptB,
                    transcription: current.transcription,
                    selectedTranscript: current.selectedTranscript,
                  } : {}),
                  attachments: dirtyRef.current.summary ? current.attachments : normalizeAttachments(refreshed.attachments),
                } : current);
              }}
            />
          )}

          {/* Real-time summary captured during recording (collapsed by default) */}
          {meeting.liveSummary && <LiveSummaryCard content={meeting.liveSummary} />}

          {/* Core Content Grid - show summary when done OR when content exists (e.g. error with saved live summary).
              Side-by-side vs stacked is driven by summaryFits (the hook's
              ResizeObserver measurement of THIS row minus the reserve owed to
              the action-items column), not a viewport breakpoint -- a
              breakpoint can't see how much width the app sidebar and the
              resizable reference aside are already claiming, so any fixed
              cutoff overflows for some combination of those. */}
          {(meeting.status === 'done' || meeting.content || meeting.summary) ? (
            <div id="meeting-summary" ref={summaryRowRef} className={`scroll-mt-20 flex ${summaryFits ? 'flex-row' : 'flex-col'} gap-8 mb-12`}>
              <div
                className={summaryFits ? 'shrink-0' : 'w-full'}
                style={summaryFits ? { width: summaryWidth } : undefined}
              >
                <AISummaryCard
                  content={resolveTranscriptLinks(resolveAttachmentUrls(meeting.content || '', meeting.attachments))}
                  summary={resolveTranscriptLinks(meeting.summary || '')}
                  canonicalContent={meeting.content || meeting.summary || ''}
                  resolveCitation={(source) => resolveTranscriptLinks(resolveAttachmentUrls(source, meeting.attachments))}
                  transcriptA={meeting.transcriptA}
                  onDirtyChange={onSummaryDirtyChange}
                  interactionLocked={resummary.action === 'load'}
                  onSave={canEdit ? async (content) => {
                    await meetingsApi.update(meeting.meetingId, { content });
                    // Publish the saved snapshot; AISummaryCard keeps newer typing local.
                    summaryRevisionRef.current++;
                    setMeeting((current) => current?.meetingId === meeting.meetingId
                      ? { ...current, content, summary: undefined } : current);
                    setSummarySave((current) => ({ revision: (current?.revision ?? 0) + 1, hasContent: !!content.trim() }));
                    setIndexRevision((value) => value + 1);
                  } : undefined}
                />
              </div>
              <div
                onMouseDown={startSummaryDrag}
                className={`${summaryFits ? 'flex' : 'hidden'} w-2 shrink-0 self-stretch cursor-col-resize bg-slate-300 dark:bg-white/20 hover:bg-primary/60 active:bg-primary/80 transition-colors rounded-full`}
              />
              <div className="flex-1 min-w-0">
                <ActionItemsCard
                  onSaved={onIndexedContentSaved}
                  key={meeting.meetingId}
                  meetingId={meeting.meetingId}
                  meetingStatus={meeting.status}
                  items={meeting.actionItems}
                  analysis={meeting.actionItemsAnalysis}
                  canEdit={canEdit}
                  savedSummary={meeting.content ?? ''}
                  sourceRevision={summarySave?.revision ?? 0}
                  hasSavedSummary={summarySave?.hasContent ?? !!meeting.content?.trim()}
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

          {/* Account */}
          <section className="mb-12">
            <h3 className="text-base font-bold flex items-center gap-2 mb-4 dark:text-text-main">
              <span className="material-symbols-outlined text-primary">corporate_fare</span>
              Account
            </h3>
            <AccountSection
              key={meeting.meetingId}
              canManage={canManageAssociations && meeting.supportsPrivateAccountLink === true}
              onChanged={onAccountChanged}
              meetingId={meeting.meetingId}
              initialAccountId={meeting.accountId}
              initialShared={meeting.sharedToAccount}
            />
          </section>

          <MeetingNotesEditor key={`notes-${meeting.meetingId}`} meetingId={meeting.meetingId}
            savedNotes={meeting.notes || ''} savedNotesRevision={meeting.notesRevision} value={notesDraft} onChange={onNotesChange}
            onSaved={onNotesSaved} onDirtyChange={onNotesDirtyChange} canEdit={canEditNotes} unavailable={canEdit && !canEditNotes} />
          {notesMessage && <p role="status" className="mb-5 text-xs text-primary">{notesMessage}</p>}
          {(meeting.fieldInsights?.length || meeting.fieldInsightsError) ? (
            <FieldInsightsSection
              description="저장된 추출 결과입니다. 이후 수정한 메모·요약을 반영하지 않을 수 있으므로 근거를 검토하세요."
              insights={(meeting.fieldInsights || []).map((insight) => ({ ...insight, sourceId: meeting.meetingId, sourceType: 'meeting', occurredAt: meeting.date }))}
              error={meeting.fieldInsightsError}
              onAddToNotes={canEditNotes ? (insight) => onAddReference({ id: `insight-${insight.sourceId}-${insight.type}-${insight.text}`, kind: 'insight', title: meeting.title,
                href: `/meeting/${encodeURIComponent(meeting.meetingId)}`, excerpt: `${insight.text}${insight.implication ? `\n의미: ${insight.implication}` : ''}${insight.nextAction ? `\n검토할 후속: ${insight.nextAction}` : ''}`, caveats: ['이전 추출 결과 · 현재 원문과 대조 필요'] }) : undefined}
            />
          ) : <p className="mb-8 rounded-lg border border-dashed border-slate-200 p-4 text-sm text-slate-500 dark:border-white/10 dark:text-text-muted">표시할 필드 Insight가 없습니다. 참조 자료와 Q&A를 검토해 고객 신호·검증할 가설을 메모에 남겨 주세요.</p>}
          {meeting.fieldInsightsTruncated && <p className="mb-4 text-xs text-amber-700 dark:text-amber-300">인사이트 일부만 표시합니다.</p>}
          <SAFollowUp key={`followup-${meeting.meetingId}`} meeting={meeting} canManage={canManageAssociations}
            hasUnsavedChanges={summaryDirty || titleDirty || transcriptDirty || notesDirty}
            onProjectsChanged={onProjectsChanged}
            onAddReference={canEditNotes ? onAddReference : undefined} />

          {/* Cost/sizing simulator (ADR-033) — only once the note itself is
              done; simRun has its own lifecycle independent of meeting.status
              (see useSimulation's doc comment), never written back onto it. */}
          {meeting.status === 'done' && (
            <SimCard
              meetingId={meeting.meetingId}
              simRun={meeting.simRun}
              onUpdate={(simRun) => setMeeting((prev) => (prev ? { ...prev, simRun } : prev))}
            />
          )}

          {/* Attachments Gallery */}
          {((meeting.attachments?.length ?? 0) > 0 || canUpload) && (
            <section className="mb-12">
              <AttachmentGallery
                key={meeting.meetingId}
                meetingId={meeting.meetingId}
                canEdit={canEdit}
                summaryRevision={summarySave?.revision ?? 0}
                attachments={meeting.attachments ?? []}
                onUploadClick={canUpload ? () => setShowUploader(true) : undefined}
                onResummarize={canEdit ? () => { void resummary.request(); document.getElementById('resummary-status')?.scrollIntoView({ block: 'center' }); } : undefined}
                resummaryDisabled={resummary.pending || !!resummary.action || summaryDirty || titleDirty || transcriptDirty || notesDirty || (meeting.status !== 'done' && meeting.status !== 'error')}
              />
            </section>
          )}

          {/* Upload Modal */}
          {showUploader && canUpload && (
            <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 backdrop-blur-sm p-4">
              <div className="max-h-[90dvh] overflow-y-auto bg-white dark:bg-surface-lowest glass-panel rounded-xl p-6 max-w-lg w-full dark:border dark:border-white/10">
                <div className="flex items-center justify-between mb-4">
                  <h3 className="font-bold text-slate-900 dark:text-text-main dark:font-headline">파일 첨부</h3>
                  <button type="button" aria-label="파일 첨부 닫기" onClick={() => setShowUploader(false)} className="text-slate-400 hover:text-slate-600 dark:hover:text-text-muted">
                    <span className="material-symbols-outlined">close</span>
                  </button>
                </div>
                <FileUploader
                  meetingId={meeting.meetingId}
                  onAttachmentsChanged={async () => {
                    try {
                      const summaryRevision = summaryRevisionRef.current;
                      const data = await meetingsApi.get(meeting.meetingId);
                      const attachments = normalizeAttachments(data.attachments);
                      setMeeting((current) => current?.meetingId === meeting.meetingId
                        ? {
                          ...current, attachments,
                          ...(!dirtyRef.current.summary && summaryRevision === summaryRevisionRef.current
                            ? { content: (data as MeetingDetail).content, summary: (data as MeetingDetail).summary } : {}),
                        } : current);
                    } catch (err) {
                      console.error('Failed to refresh meeting:', err);
                      throw new Error('첨부 목록을 새로 불러오지 못했습니다. 파일 창을 닫고 페이지를 다시 확인해 주세요.');
                    }
                  }}
                />
              </div>
            </div>
          )}

          {/* Full Transcription */}
          {((meeting.transcription?.length ?? 0) > 0 || displayedTranscript) && (
            <TranscriptSection
              onDirtyChange={onTranscriptDirtyChange}
              transcription={meeting.transcription || []}
              rawTranscript={displayedTranscript}
              onSaveRawTranscript={usingTranscriptB || meeting.permission === 'read' ? undefined : async (text) => {
                await meetingsApi.update(meeting.meetingId, { transcriptA: text });
                setIndexRevision((value) => value + 1);
                setMeeting((current) => current?.meetingId === meeting.meetingId ? {
                  ...current,
                  transcriptA: text,
                  transcription: text === current.transcriptA ? current.transcription : [],
                } : current);
                let refreshed: MeetingDetail;
                try {
                  refreshed = await meetingsApi.get(meeting.meetingId) as MeetingDetail;
                } catch {
                  throw new Error('원문은 저장했지만 최신 미팅을 불러오지 못했습니다. 새로고침해주세요.');
                }
                setMeeting((current) => current?.meetingId === meeting.meetingId ? {
                  ...current,
                  transcriptA: refreshed.transcriptA,
                  transcriptB: refreshed.transcriptB,
                  selectedTranscript: refreshed.selectedTranscript,
                  transcription: refreshed.transcription || [],
                  updatedAt: refreshed.updatedAt,
                } : current);
              }}
            />
          )}

          {/* Inline Q&A - mobile only */}
          <section id="meeting-qa-mobile" className="lg:hidden border-t border-slate-200 dark:border-white/10 pt-8">
            <h2 className="text-lg font-bold flex items-center gap-2 text-slate-900 dark:text-text-main dark:font-headline mb-6">
              <span className="material-symbols-outlined">question_answer</span>
              Meeting Q&A
            </h2>
            <div className="h-[32rem]"><ReferenceTabs activeTab={referenceTab} onTabChange={setReferenceTab}
              qaPanel={<QAPanel meetingId={meeting.meetingId} questionDraft={questionDraft} onSaveToNotes={canEditNotes ? onSaveQAToNotes : undefined} />}
              referencePanel={<ReferencePanel accountId={meeting.accountId} onAddReference={canEditNotes ? onAddReference : undefined} onPrepareQuestion={onPrepareQuestion} />} /></div>
          </section>

          {/* Audio Player / Uploader */}
          {audioUrls.length > 0 || audioUrl ? (
            <>
              <AudioPlayer audioUrl={audioUrl ?? undefined} audioUrls={audioUrls.length > 0 ? audioUrls : undefined} />
              {meeting.supportsAudioCrop && meeting.userId === user?.userId && (meeting.status === 'done' || meeting.status === 'error') && (meeting.audioKeys?.length ?? 0) <= 1 && (meeting.audioPartCount ?? 0) <= 1 && (
                <AudioCropEditor key={meeting.meetingId} meetingId={meeting.meetingId} audioUrl={audioUrl || audioUrls[0]} duration={meeting.duration}
                  dirty={summaryDirty || titleDirty || transcriptDirty || notesDirty} onCropped={(id) => router.push(`/meeting/${id}`)} />
              )}
              {!meeting.audioCrop && (meeting.status === 'done' || meeting.status === 'error') && !showAudioUploader && (
                <div className="flex justify-center mt-4">
                  <button
                    onClick={() => setShowAudioUploader(true)}
                    className="flex items-center gap-1.5 text-sm text-slate-400 hover:text-primary transition-colors"
                  >
                    <span className="material-symbols-outlined text-lg">add_circle</span>
                    파일 추가 (오디오·문서)
                  </button>
                </div>
              )}
              {showAudioUploader && (
                <AudioUploader meetingId={meeting.meetingId} onUploadComplete={() => { setShowAudioUploader(false); refetchMeeting(); }} />
              )}
            </>
          ) : !meeting.audioCrop && (meeting.status === 'done' || meeting.status === 'error') && !hasAudio ? (
            <AudioUploader meetingId={meeting.meetingId} onUploadComplete={refetchMeeting} />
          ) : null}
          </div>
        </div>

        {/* Drag-to-resize divider - Desktop only. Carries the visible boundary
            line itself (aside no longer has its own border-l) so the resize
            handle is actually discoverable at rest, not just on hover.
            self-stretch is a defensive no-op (flex default) against any
            ancestor accidentally not giving this row a definite height. */}
        <div
          onMouseDown={startAsideDrag}
          className="hidden lg:flex w-2 shrink-0 self-stretch cursor-col-resize bg-slate-300 dark:bg-white/20 hover:bg-primary/60 active:bg-primary/80 transition-colors"
        />

        {/* Q&A / 참조 Side Panel - Desktop only */}
        <aside
          className="hidden lg:flex dark:bg-surface-lowest/50 flex-col sticky top-0 h-screen"
          style={{ width: asideWidth }}
        >{/* width persisted via useResizablePanel, see hooks/useResizablePanel.ts */}
          <ReferenceTabs activeTab={referenceTab} onTabChange={setReferenceTab}
            qaPanel={<QAPanel meetingId={meeting.meetingId} questionDraft={questionDraft} onSaveToNotes={canEditNotes ? onSaveQAToNotes : undefined} />}
            referencePanel={<ReferencePanel accountId={meeting.accountId} onAddReference={canEditNotes ? onAddReference : undefined} onPrepareQuestion={onPrepareQuestion} />}
          />
        </aside>
      </div>
    </AppLayout>
  );
}

export default function MeetingDetailPage() {
  const pathname = usePathname();
  return (
    <MeetingErrorBoundary key={pathname}>
      <MeetingDetailContent />
    </MeetingErrorBoundary>
  );
}
