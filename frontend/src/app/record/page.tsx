'use client';

import { useState, useRef, useEffect, useCallback, Suspense } from 'react';
import { useRouter, useSearchParams } from 'next/navigation';
import { useAuth } from '@/components/auth/AuthProvider';
import { AppLayout } from '@/components/layout/AppLayout';
import { RecordButton } from '@/components/RecordButton';
import { MicSelector } from '@/components/MicSelector';
import { FileUploader } from '@/components/FileUploader';
import { LiveTranscript } from '@/components/LiveTranscript';
import { RecordingTabs } from '@/components/RecordingTabs';
import { TranslationView } from '@/components/TranslationView';
import { LiveSummary } from '@/components/LiveSummary';
import { LiveQAPanel } from '@/components/LiveQAPanel';
import { RecordingConfig, SttProviderSelector, LiveSttSelector } from '@/components/record/RecordingConfig';
import { PostRecordingBanner } from '@/components/record/PostRecordingBanner';
import { useAudioDevices } from '@/hooks/useAudioDevices';
import { useRecordingSession } from '@/hooks/useRecordingSession';
import { useLiveSummary } from '@/hooks/useLiveSummary';
import { usePostRecording } from '@/hooks/usePostRecording';
import { uploadsApi, meetingsApi, kbApi } from '@/lib/api';
import { uploadFile } from '@/lib/upload';
import type { LiveSttProvider } from '@/lib/sttManager';

export default function RecordPage() {
  return (
    <Suspense fallback={<div className="min-h-screen flex items-center justify-center"><div className="animate-spin rounded-full h-8 w-8 border-2 border-primary border-t-transparent" /></div>}>
      <RecordPageInner />
    </Suspense>
  );
}

function RecordPageInner() {
  const router = useRouter();
  const searchParams = useSearchParams();
  const isUploadMode = searchParams.get('mode') === 'upload';
  const { isAuthenticated, isLoading } = useAuth();
  const { devices, selectedDeviceId, selectDevice, refreshDevices } = useAudioDevices();

  // Config state
  const [meetingTitle, setMeetingTitle] = useState('');
  const [sttProvider, setSttProvider] = useState<'transcribe' | 'nova-sonic'>('transcribe');
  const [summaryInterval, setSummaryInterval] = useState(50);
  const [translationEnabled, setTranslationEnabled] = useState(false);
  const [targetLang, setTargetLang] = useState('en');
  const [attachments, setAttachments] = useState<{ name: string; url: string; s3Key?: string; mimeType?: string; status?: 'uploading' | 'complete' | 'error'; kbStatus?: 'idle' | 'copying' | 'done' | 'error' }[]>([]);
  const [liveSttProvider, setLiveSttProvider] = useState<LiveSttProvider>('web-speech');

  // Analyser nodes for MicSelector level meter
  const [analyserNode, setAnalyserNode] = useState<AnalyserNode | null>(null);
  const [previewAnalyser, setPreviewAnalyser] = useState<AnalyserNode | null>(null);
  const previewStreamRef = useRef<MediaStream | null>(null);
  const previewCtxRef = useRef<AudioContext | null>(null);

  // Client-side meeting ID (stable across re-renders)
  const [clientMeetingIdBase] = useState(() => `meeting_${Date.now()}`);

  // Upload mode state
  const [uploadProgress, setUploadProgress] = useState<string | null>(null);
  const audioInputRef = useRef<HTMLInputElement>(null);

  // Mobile Q&A bottom sheet state
  const [isQAOpen, setIsQAOpen] = useState(false);
  const [detectedCount, setDetectedCount] = useState(0);

  // --- Hooks ---
  const summary = useLiveSummary({ summaryInterval });

  const session = useRecordingSession({
    targetLang,
    translationEnabled,
    liveSttProvider,
    onProviderChange: setLiveSttProvider,
    onTranscriptUpdate: useCallback((totalWordCount: number, allText: string) => {
      const meetingId = postRecording.serverMeetingId || clientMeetingIdBase;
      summary.checkThreshold(totalWordCount, allText, meetingId);
    // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [clientMeetingIdBase]),
  });

  const postRecording = usePostRecording({
    meetingTitle,
    sttProvider,
  });

  const clientMeetingId = postRecording.serverMeetingId || clientMeetingIdBase;

  // Mic preview: create AudioContext + AnalyserNode when device changes (not recording)
  useEffect(() => {
    if (session.isRecording) return;

    const cleanupPreview = () => {
      previewStreamRef.current?.getTracks().forEach((t) => t.stop());
      previewStreamRef.current = null;
      previewCtxRef.current?.close().catch(() => {});
      previewCtxRef.current = null;
      setPreviewAnalyser(null);
    };

    if (!selectedDeviceId) { cleanupPreview(); return; }

    let cancelled = false;
    (async () => {
      try {
        const stream = await navigator.mediaDevices.getUserMedia({
          audio: { deviceId: { exact: selectedDeviceId } },
        });
        if (cancelled) { stream.getTracks().forEach((t) => t.stop()); return; }
        const ctx = new AudioContext();
        const source = ctx.createMediaStreamSource(stream);
        const analyser = ctx.createAnalyser();
        analyser.fftSize = 256;
        source.connect(analyser);
        previewStreamRef.current = stream;
        previewCtxRef.current = ctx;
        setPreviewAnalyser(analyser);
      } catch (err) {
        console.warn('Mic preview failed:', err);
      }
    })();

    return () => { cancelled = true; cleanupPreview(); };
  }, [selectedDeviceId, session.isRecording]);

  // --- Early returns ---
  if (isLoading) {
    return (
      <div className="min-h-screen flex items-center justify-center">
        <div className="animate-spin rounded-full h-8 w-8 border-2 border-primary border-t-transparent" />
      </div>
    );
  }
  if (!isAuthenticated && !session.isRecording) { router.push('/'); return null; }

  // --- Handlers ---
  const handleRecordingStart = async (stream: MediaStream) => {
    summary.reset();
    // Create draft meeting immediately for crash recovery
    await postRecording.createDraftMeeting();
    session.startSession(() => {
      previewStreamRef.current?.getTracks().forEach((t) => t.stop());
      previewStreamRef.current = null;
      previewCtxRef.current?.close().catch(() => {});
      previewCtxRef.current = null;
      setPreviewAnalyser(null);
    }, stream);
  };

  const handleCheckpoint = async (blob: Blob, mimeType: string) => {
    const meetingId = postRecording.serverMeetingId;
    if (!meetingId) return; // draft creation failed — skip checkpoint
    try {
      const fileName = 'recording_progress.webm'; // fixed name → S3 overwrite
      const { uploadUrl } = await uploadsApi.getPresignedUrl({
        fileName,
        fileType: mimeType || 'audio/webm',
        category: 'audio',
        meetingId,
      });
      await fetch(uploadUrl, {
        method: 'PUT',
        body: blob,
        headers: { 'Content-Type': mimeType || 'audio/webm' },
      });
    } catch {
      // Silent fail — checkpoint is best-effort
    }
  };

  const handleFileAttach = async (file: File) => {
    const localUrl = URL.createObjectURL(file);
    const category = file.type.startsWith('image/') ? 'image' as const : 'file' as const;
    setAttachments((prev) => [...prev, { name: file.name, url: localUrl, mimeType: file.type, status: 'uploading' }]);

    try {
      const { uploadUrl, key } = await uploadsApi.getPresignedUrl({
        fileName: file.name,
        fileType: file.type,
        category,
        meetingId: clientMeetingId,
      });
      await fetch(uploadUrl, { method: 'PUT', body: file, headers: { 'Content-Type': file.type } });
      await uploadsApi.notifyComplete({
        meetingId: clientMeetingId, key, category,
        fileName: file.name, fileSize: file.size, mimeType: file.type,
      }).catch((err) =>
        console.warn('notifyComplete failed (meeting may not exist yet):', err),
      );
      setAttachments((prev) =>
        prev.map((att) => att.url === localUrl ? { ...att, status: 'complete' as const, s3Key: key, kbStatus: 'idle' as const } : att),
      );
    } catch (err) {
      console.error('File attach failed:', err);
      setAttachments((prev) =>
        prev.map((att) => att.url === localUrl ? { ...att, status: 'error' as const } : att),
      );
    }
  };

  const handleCopyToKB = async (index: number) => {
    const att = attachments[index];
    if (!att?.s3Key || att.kbStatus === 'copying' || att.kbStatus === 'done') return;

    setAttachments((prev) =>
      prev.map((a, i) => i === index ? { ...a, kbStatus: 'copying' as const } : a),
    );
    try {
      await kbApi.copyAttachment(att.s3Key);
      setAttachments((prev) =>
        prev.map((a, i) => i === index ? { ...a, kbStatus: 'done' as const } : a),
      );
    } catch (err) {
      console.error('Failed to copy to KB:', err);
      setAttachments((prev) =>
        prev.map((a, i) => i === index ? { ...a, kbStatus: 'error' as const } : a),
      );
    }
  };

  const handleRetry = () => {
    postRecording.handleRetry();
    session.setSpeechError(null);
  };

  const handleAudioUpload = async (file: File) => {
    if (!file.type.startsWith('audio/')) {
      session.setSpeechError('음성 파일만 업로드할 수 있습니다.');
      return;
    }
    setUploadProgress('미팅 생성 중...');
    try {
      const title = meetingTitle || file.name.replace(/\.[^.]+$/, '');
      const meeting = await meetingsApi.create({ title, sttProvider });
      const meetingId = meeting.meetingId;

      setUploadProgress('음성 파일 업로드 중...');
      await uploadFile(file, (p) => {
        setUploadProgress(`업로드 중... ${p.percentage}%`);
      }, meetingId);

      setUploadProgress('전사 처리 시작...');
      // EventBridge auto-triggers ttobak-transcribe → ttobak-summarize
      router.push(`/meeting/${meetingId}`);
    } catch (err) {
      console.error('Audio upload failed:', err);
      setUploadProgress(null);
      session.setSpeechError(err instanceof Error ? err.message : '업로드에 실패했습니다.');
    }
  };

  return (
    <AppLayout activePath="/record" showMobileNav={true} isRecording={session.isRecording} breadcrumbs={[{ label: 'Recording' }, { label: meetingTitle || 'New Meeting' }]}>
      {/* Header */}
      <header className="lg:hidden flex items-center justify-between px-6 py-4 bg-white/80 dark:bg-[#09090E]/80 backdrop-blur-md sticky top-0 z-10 border-b border-slate-100 dark:border-white/10">
        <button
          onClick={() => router.back()}
          className="p-2 hover:bg-slate-50 dark:hover:bg-white/5 rounded-lg transition-colors"
        >
          <span className="material-symbols-outlined text-slate-600 dark:text-gray-400">arrow_back</span>
        </button>
        <input
          type="text"
          value={meetingTitle}
          onChange={(e) => setMeetingTitle(e.target.value)}
          placeholder="Meeting Title"
          className="text-lg font-bold tracking-tight bg-transparent border-none text-center focus:outline-none focus:ring-0 text-slate-900 dark:text-gray-100 placeholder:text-slate-400 flex-1 mx-4"
        />
        <RecordingConfig
          summaryInterval={summaryInterval}
          onSummaryIntervalChange={setSummaryInterval}
          translationEnabled={translationEnabled}
          onTranslationToggle={setTranslationEnabled}
          targetLang={targetLang}
          onTargetLangChange={setTargetLang}
        />
      </header>

      {/* Speech Recognition Error Banner */}
      {session.speechError && (
        <div className="mx-6 mt-2 px-4 py-3 bg-amber-50 dark:bg-amber-900/20 border border-amber-200 dark:border-amber-800 rounded-lg flex items-start gap-3">
          <span className="material-symbols-outlined text-amber-500 text-xl mt-0.5">warning</span>
          <div className="flex-1">
            <p className="text-sm text-amber-800 dark:text-amber-200">{session.speechError}</p>
            {session.isSttPermanentlyFailed && session.isRecording && (
              <button
                onClick={session.handleRestartStt}
                className="mt-2 px-3 py-1.5 bg-amber-600 text-white text-xs font-semibold rounded-lg hover:bg-amber-700 transition-colors"
              >
                음성 인식 재시작
              </button>
            )}
          </div>
          <button
            onClick={() => session.setSpeechError(null)}
            className="p-1 hover:bg-amber-100 dark:hover:bg-amber-900/40 rounded transition-colors"
          >
            <span className="material-symbols-outlined text-amber-400 text-lg">close</span>
          </button>
        </div>
      )}

      {/* Auth expired warning during recording */}
      {!isAuthenticated && session.isRecording && (
        <div className="mx-6 mt-2 px-4 py-3 bg-red-50 dark:bg-red-900/20 border border-red-200 dark:border-red-800 rounded-lg flex items-start gap-3">
          <span className="material-symbols-outlined text-red-500 text-xl mt-0.5">lock</span>
          <p className="text-sm text-red-800 dark:text-red-200">
            세션이 만료되었습니다. 녹음은 계속되지만, 저장 시 재로그인이 필요할 수 있습니다.
          </p>
        </div>
      )}

      {/* Main Content */}
      <div className="flex flex-1 min-h-0">
      <main className="flex-1 flex flex-col px-6 lg:px-8 pt-8 lg:pt-8 pb-32 lg:pb-8 overflow-y-auto">
        {/* Upload Mode — audio file upload flow */}
        {isUploadMode && !postRecording.step && !session.isRecording && (
          <div className="flex flex-col items-center gap-6 py-8">
            <div className="hidden lg:block mb-2">
              <span className="hidden dark:block text-[10px] font-bold uppercase tracking-[0.2em] text-[#8B8D98] text-center mb-2">Upload</span>
              <input
                type="text"
                value={meetingTitle}
                onChange={(e) => setMeetingTitle(e.target.value)}
                placeholder="Meeting Title"
                className="text-2xl font-bold tracking-tight bg-transparent border-none text-center focus:outline-none focus:ring-0 text-slate-900 dark:text-gray-100 dark:font-[var(--font-headline)] placeholder:text-slate-400 w-full"
              />
            </div>
            <SttProviderSelector
              sttProvider={sttProvider}
              onSttProviderChange={setSttProvider}
              isRecording={false}
            />
            {uploadProgress ? (
              <div className="flex flex-col items-center gap-3 py-8">
                <div className="animate-spin rounded-full h-10 w-10 border-2 border-primary border-t-transparent" />
                <p className="text-sm font-medium text-slate-600 dark:text-slate-300">{uploadProgress}</p>
              </div>
            ) : (
              <>
                <input
                  ref={audioInputRef}
                  type="file"
                  accept="audio/*"
                  className="hidden"
                  onChange={(e) => {
                    const file = e.target.files?.[0];
                    if (file) handleAudioUpload(file);
                    e.target.value = '';
                  }}
                />
                <button
                  onClick={() => audioInputRef.current?.click()}
                  className="w-full max-w-md border-2 border-dashed border-slate-200 dark:border-slate-700 hover:border-primary/40 hover:bg-slate-50 dark:hover:bg-slate-800/50 rounded-xl p-10 text-center transition-all cursor-pointer"
                >
                  <span className="material-symbols-outlined text-5xl text-slate-400 mb-3 block">audio_file</span>
                  <p className="text-slate-600 dark:text-slate-400 font-medium">음성 파일을 선택하세요</p>
                  <p className="text-slate-400 text-sm mt-1">MP3, WAV, M4A, WebM 등</p>
                </button>
                <button
                  onClick={() => router.push('/record')}
                  className="text-sm text-slate-500 hover:text-primary transition-colors flex items-center gap-1"
                >
                  <span className="material-symbols-outlined text-base">mic</span>
                  실시간 녹음으로 전환
                </button>
              </>
            )}
          </div>
        )}

        {/* Config Controls — visible only when idle (record mode) */}
        {!isUploadMode && !postRecording.step && !session.isRecording && (
          <div className="flex flex-col items-center gap-3">
            {/* Desktop: editable title */}
            <div className="hidden lg:block mb-4">
              <span className="hidden dark:block text-[10px] font-bold uppercase tracking-[0.2em] text-[#8B8D98] text-center mb-2">Studio</span>
              <input
                type="text"
                value={meetingTitle}
                onChange={(e) => setMeetingTitle(e.target.value)}
                placeholder="Meeting Title"
                className="text-2xl font-bold tracking-tight bg-transparent border-none text-center focus:outline-none focus:ring-0 text-slate-900 dark:text-gray-100 dark:font-[var(--font-headline)] placeholder:text-slate-400 w-full"
              />
            </div>
            <MicSelector
              devices={devices}
              selectedDeviceId={selectedDeviceId}
              onSelect={selectDevice}
              disabled={session.isRecording}
              analyser={session.isRecording ? analyserNode : previewAnalyser}
            />
            <SttProviderSelector
              sttProvider={sttProvider}
              onSttProviderChange={setSttProvider}
              isRecording={session.isRecording}
            />
            <LiveSttSelector
              liveSttProvider={liveSttProvider}
              onLiveSttProviderChange={setLiveSttProvider}
              activeProvider={session.activeProvider}
              isRecording={session.isRecording}
            />
          </div>
        )}

        {/* Desktop: Meeting title during recording */}
        {session.isRecording && (
          <div className="hidden lg:block mb-4">
            <p className="hidden dark:block text-[10px] font-bold uppercase tracking-[0.2em] text-[#8B8D98] text-center mb-1">Studio</p>
            <h1 className="text-xl font-bold text-slate-900 dark:text-white dark:font-[var(--font-headline)] text-center tracking-tight">
              {meetingTitle || 'Untitled Meeting'}
            </h1>
          </div>
        )}

        {/* Recording Section — hidden in upload mode */}
        {!isUploadMode && <div className="flex flex-col items-center justify-center mb-8">
          <RecordButton
            meetingId={clientMeetingId}
            meetingTitle={meetingTitle || 'Untitled Meeting'}
            deviceId={selectedDeviceId || undefined}
            onRecordingComplete={postRecording.handleRecordingComplete}
            onBlobReady={postRecording.handleBlobReady}
            onError={(error) => {
              if (session.isRecording) {
                postRecording.handleRetry(); // clear any previous state
                // setStep and errorMessage handled by handleBlobReady on real errors
                // For recording errors, show blocking overlay
                session.setSpeechError(null);
              } else {
                session.setSpeechError(error);
              }
            }}
            onRecordingStart={handleRecordingStart}
            onRecordingPause={session.pauseSession}
            onRecordingResume={session.resumeSession}
            onRecordingStop={session.stopSession}
            onPermissionGranted={refreshDevices}
            onCaptureImage={handleFileAttach}
            onAnalyserReady={setAnalyserNode}
            onCheckpoint={handleCheckpoint}
          />
        </div>}

        {/* Desktop: Live Transcript in main content (centered) during recording */}
        {session.isRecording && (
          <div className="hidden lg:flex lg:flex-col" style={{ height: '50vh' }}>
            <LiveTranscript
              transcripts={session.displayTranscripts}
              wordCount={session.totalWordCount}
            />
          </div>
        )}

        {/* Recording Tabs — mobile only */}
        {session.isRecording && (
          <div className="lg:hidden">
            <RecordingTabs
              captionsContent={
                <LiveTranscript
                  transcripts={session.displayTranscripts}
                  wordCount={session.totalWordCount}
                />
              }
              translationContent={
                translationEnabled ? (
                  <TranslationView
                    translations={session.translations}
                    targetLang={targetLang}
                    onTargetLangChange={setTargetLang}
                    isActive={true}
                    interimTranslation={session.currentInterimTranslation}
                  />
                ) : (
                  <div className="flex flex-col items-center justify-center py-12 text-center">
                    <span className="material-symbols-outlined text-4xl text-slate-300 dark:text-slate-600 mb-3">translate</span>
                    <p className="text-sm text-slate-400 dark:text-slate-500">
                      번역 기능을 활성화하려면 상단의 번역 체크박스를 켜세요
                    </p>
                  </div>
                )
              }
              summaryContent={
                <LiveSummary
                  summary={summary.liveSummary}
                  isGenerating={summary.isGenerating}
                  wordCount={session.totalWordCount}
                  lastSummaryWordCount={summary.lastSummaryWordCount}
                  summaryInterval={summaryInterval}
                />
              }
            />
          </div>
        )}

        {/* File Attachments — shown during recording only */}
        {!postRecording.step && session.isRecording && (
          <section className="mt-8">
            <div className="flex items-center justify-between mb-4 px-1">
              <h3 className="font-bold text-slate-800 dark:text-slate-200">첨부 파일</h3>
              {attachments.length > 0 && (
                <span className="text-xs text-slate-500 dark:text-slate-400">
                  {attachments.length}개 파일
                </span>
              )}
            </div>

            {/* Attachment thumbnails grid */}
            {attachments.length > 0 && (
              <div className="grid grid-cols-3 lg:grid-cols-4 gap-3 mb-4">
                {attachments.map((att, index) => (
                  <div key={index} className="group relative rounded-xl overflow-hidden aspect-video shadow-sm border border-slate-200 dark:border-white/10 bg-slate-100 dark:bg-slate-800">
                    {att.mimeType?.startsWith('image/') ? (
                      <img src={att.url} alt={att.name} className="w-full h-full object-cover" />
                    ) : (
                      <div className="w-full h-full flex flex-col items-center justify-center">
                        <span className="material-symbols-outlined text-2xl text-slate-400 dark:text-slate-500">
                          {att.mimeType?.startsWith('video/') ? 'videocam' :
                           att.mimeType?.startsWith('audio/') ? 'audio_file' :
                           'description'}
                        </span>
                        <span className="text-[10px] text-slate-500 dark:text-slate-400 mt-1 px-2 truncate max-w-full">{att.name}</span>
                      </div>
                    )}
                    {att.status === 'uploading' && (
                      <div className="absolute inset-0 bg-black/40 flex items-center justify-center">
                        <div className="animate-spin rounded-full h-6 w-6 border-2 border-white border-t-transparent" />
                      </div>
                    )}
                    {att.status === 'complete' && (
                      <div className="absolute inset-0 bg-green-500/20 flex items-center justify-center animate-fade-out">
                        <span className="material-symbols-outlined text-green-500 text-2xl drop-shadow">check_circle</span>
                      </div>
                    )}
                    {att.status === 'error' && (
                      <div className="absolute inset-0 bg-red-500/20 flex items-center justify-center">
                        <span className="material-symbols-outlined text-red-500 text-2xl drop-shadow">error</span>
                      </div>
                    )}
                    {/* KB copy button — visible on hover for completed uploads */}
                    {att.status === 'complete' && att.s3Key && (
                      <button
                        onClick={(e) => { e.stopPropagation(); handleCopyToKB(index); }}
                        disabled={att.kbStatus === 'copying' || att.kbStatus === 'done'}
                        className={`absolute top-1 right-1 p-1 rounded-md text-[10px] font-bold transition-all ${
                          att.kbStatus === 'done'
                            ? 'bg-green-500/80 text-white'
                            : att.kbStatus === 'copying'
                            ? 'bg-slate-500/60 text-white'
                            : att.kbStatus === 'error'
                            ? 'bg-red-500/80 text-white opacity-100'
                            : 'bg-black/50 text-white opacity-0 group-hover:opacity-100'
                        }`}
                        title="Knowledge Base에 추가"
                      >
                        {att.kbStatus === 'done' ? (
                          <span className="material-symbols-outlined text-sm">check</span>
                        ) : att.kbStatus === 'copying' ? (
                          <div className="animate-spin rounded-full h-3 w-3 border border-white border-t-transparent" />
                        ) : (
                          <span className="material-symbols-outlined text-sm">library_add</span>
                        )}
                      </button>
                    )}
                  </div>
                ))}
              </div>
            )}

            {/* Camera capture button — during recording only */}
            {session.isRecording && (
              <div className="hidden lg:flex mb-4">
                <label className="bg-white dark:bg-slate-900 border-2 border-dashed border-slate-200 dark:border-white/10 rounded-xl flex items-center gap-2 px-4 py-3 text-slate-400 hover:border-primary/40 hover:text-primary transition-all cursor-pointer">
                  <span className="material-symbols-outlined text-xl">add_a_photo</span>
                  <span className="text-xs font-bold uppercase tracking-wider">카메라 촬영</span>
                  <input
                    type="file"
                    accept="image/*"
                    className="hidden"
                    onChange={(e) => {
                      const file = e.target.files?.[0];
                      if (file) handleFileAttach(file);
                      e.target.value = '';
                    }}
                  />
                </label>
              </div>
            )}

            {/* Unified FileUploader — drag and drop + click */}
            <FileUploader
              meetingId={clientMeetingId}
              onUploadComplete={(files) => setAttachments((prev) => [...prev, ...files.map(f => ({ ...f, mimeType: f.mimeType }))])}
            />
          </section>
        )}
      </main>

      {/* Desktop Side Panel: Summary + QA during recording */}
      {session.isRecording && (
        <aside className="hidden lg:flex w-80 shrink-0 border-l border-slate-200 dark:border-white/10 flex-col overflow-y-auto">
          <div className="flex-1 min-h-0 flex flex-col overflow-y-auto">
            <LiveSummary
              summary={summary.liveSummary}
              isGenerating={summary.isGenerating}
              wordCount={session.totalWordCount}
              lastSummaryWordCount={summary.lastSummaryWordCount}
              summaryInterval={summaryInterval}
            />
          </div>
          <div className="shrink-0 border-t border-slate-200 dark:border-white/10">
            <LiveQAPanel
              transcriptContext={session.transcriptContext}
              onDetectedQuestionsChange={setDetectedCount}
              serverDetectedQuestions={summary.detectedQuestions}
              onAskedQuestion={summary.addAskedQuestion}
            />
          </div>
        </aside>
      )}
      </div>

      {/* Post-Recording Toast Banner */}
      {postRecording.step && (
        <PostRecordingBanner
          step={postRecording.step}
          errorMessage={postRecording.errorMessage}
          onRetry={handleRetry}
          onDismiss={() => { postRecording.handleRetry(); router.push('/'); }}
          onNotesSubmit={postRecording.handleNotesSubmit}
          onNotesSkip={postRecording.handleNotesSkip}
        />
      )}

      {/* Mobile Floating Q&A Button */}
      {session.isRecording && !isQAOpen && (
        <button
          onClick={() => setIsQAOpen(true)}
          className="lg:hidden fixed right-4 bottom-24 z-30 w-14 h-14 rounded-full bg-primary text-white shadow-lg shadow-primary/30 flex items-center justify-center hover:bg-primary/90 active:scale-95 transition-all"
        >
          <span className="material-symbols-outlined text-2xl">question_answer</span>
          {detectedCount > 0 && (
            <span className="absolute -top-1 -right-1 min-w-5 h-5 rounded-full bg-amber-500 text-white text-[10px] font-bold flex items-center justify-center px-1">
              {detectedCount}
            </span>
          )}
        </button>
      )}

      {/* Mobile Q&A Bottom Sheet */}
      {session.isRecording && isQAOpen && (
        <div className="lg:hidden fixed inset-0 z-40">
          <div className="absolute inset-0 bg-black/30" onClick={() => setIsQAOpen(false)} />
          <div className="absolute bottom-0 left-0 right-0 h-[50vh] bg-white dark:bg-slate-900 rounded-t-2xl shadow-2xl flex flex-col animate-slide-up">
            <button onClick={() => setIsQAOpen(false)} className="flex justify-center pt-3 pb-2">
              <div className="w-10 h-1 rounded-full bg-slate-300 dark:bg-slate-600" />
            </button>
            <div className="flex-1 min-h-0">
              <LiveQAPanel
                transcriptContext={session.transcriptContext}
                onDetectedQuestionsChange={setDetectedCount}
                serverDetectedQuestions={summary.detectedQuestions}
                onAskedQuestion={summary.addAskedQuestion}
              />
            </div>
          </div>
        </div>
      )}
    </AppLayout>
  );
}
