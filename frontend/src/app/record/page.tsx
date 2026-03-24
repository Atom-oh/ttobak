'use client';

import { useState, useRef, useEffect } from 'react';
import { useRouter } from 'next/navigation';
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
import { RecordingConfig, SttProviderSelector } from '@/components/record/RecordingConfig';
import { PostRecordingBanner, PostRecordingStep } from '@/components/record/PostRecordingBanner';
import { useAudioDevices } from '@/hooks/useAudioDevices';
import { meetingsApi, summaryApi, uploadsApi, qaApi } from '@/lib/api';
import { SttOrchestrator, SttSource } from '@/lib/sttOrchestrator';
import { countWords } from '@/lib/speechRecognition';
import { uploadAudioWithRetry } from '@/lib/upload';

interface TranscriptEntry {
  text: string;
  isFinal: boolean;
  timestamp: string;
}

function formatDefaultTitle(date: Date): string {
  const month = date.getMonth() + 1;
  const day = date.getDate();
  const hour = date.getHours();
  const minute = date.getMinutes();
  return minute > 0
    ? `${month}월 ${day}일 ${hour}시 ${minute}분 미팅`
    : `${month}월 ${day}일 ${hour}시 미팅`;
}

export default function RecordPage() {
  const router = useRouter();
  const { isAuthenticated, isLoading, logout } = useAuth();
  const { devices, selectedDeviceId, selectDevice, refreshDevices } = useAudioDevices();
  const [meetingTitle, setMeetingTitle] = useState('');
  const [sttProvider, setSttProvider] = useState<'transcribe' | 'nova-sonic'>('transcribe');
  const [summaryInterval, setSummaryInterval] = useState(200);
  const summaryIntervalRef = useRef(200);
  const [capturedImages, setCapturedImages] = useState<{ name: string; url: string }[]>([]);

  // Analyser node for MicSelector level meter
  const [analyserNode, setAnalyserNode] = useState<AnalyserNode | null>(null);

  // Mic preview (level meter before recording)
  const [previewAnalyser, setPreviewAnalyser] = useState<AnalyserNode | null>(null);
  const previewStreamRef = useRef<MediaStream | null>(null);
  const previewCtxRef = useRef<AudioContext | null>(null);

  // Recording state
  const [isRecording, setIsRecording] = useState(false);
  const [isPaused, setIsPaused] = useState(false);

  // STT Orchestrator
  const orchestratorRef = useRef<SttOrchestrator | null>(null);
  const [sttSource, setSttSource] = useState<SttSource>('idle');

  // Speech recognition state
  const [transcripts, setTranscripts] = useState<TranscriptEntry[]>([]);
  const [currentInterim, setCurrentInterim] = useState<string>('');
  const [totalWordCount, setTotalWordCount] = useState(0);
  const [lastSummaryWordCount, setLastSummaryWordCount] = useState(0);
  const lastSummaryWordCountRef = useRef(0); // Internal tracking for callback closures

  // Translation state
  const [translationEnabled, setTranslationEnabled] = useState(false);
  const [targetLang, setTargetLang] = useState('en');
  const [translations, setTranslations] = useState<{ original: string; translated: string; targetLang: string; timestamp: string }[]>([]);
  const [currentInterimTranslation, setCurrentInterimTranslation] = useState<{ original: string; translated: string; targetLang: string } | null>(null);

  // Summary state
  const [liveSummary, setLiveSummary] = useState('');
  const [isGeneratingSummary, setIsGeneratingSummary] = useState(false);

  // Server-pushed detected questions
  const [detectedQuestions, setDetectedQuestions] = useState<string[]>([]);

  // Question detection refs
  const detectTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const accumulatedForDetectRef = useRef<string[]>([]);
  const askedQuestionsRef = useRef<string[]>([]);
  const detectInFlightRef = useRef(false);
  const [detectWordInterval, setDetectWordInterval] = useState(100);
  const detectWordIntervalRef = useRef(100);
  const detectWordsAccumRef = useRef(0);

  // Backend audio key (from ECS realtime server)
  const [backendAudioKey, setBackendAudioKey] = useState<string | null>(null);

  // VAD (Voice Activity Detection) state
  const [vadEnabled, setVadEnabled] = useState(true);
  const [vadIsSpeaking, setVadIsSpeaking] = useState(false);
  const [vadSavedPercent, setVadSavedPercent] = useState(0);
  const vadEnabledRef = useRef(true);

  // Refs for closures in callbacks
  const targetLangRef = useRef(targetLang);
  const liveSummaryRef = useRef(liveSummary);
  const transcriptsRef = useRef(transcripts);

  // Post-recording state
  const [postRecordingStep, setPostRecordingStep] = useState<PostRecordingStep | null>(null);
  const [errorMessage, setErrorMessage] = useState<string | null>(null);

  // Speech recognition error (shown as dismissible banner, not blocking overlay)
  const [speechError, setSpeechError] = useState<string | null>(null);

  // Server meeting ID
  const [serverMeetingId, setServerMeetingId] = useState<string | null>(null);

  // Client-side meeting ID (stable across re-renders)
  const [clientMeetingIdBase] = useState(() => `meeting_${Date.now()}`);

  // Mobile Q&A bottom sheet state
  const [isQAOpen, setIsQAOpen] = useState(false);
  const [detectedCount, setDetectedCount] = useState(0);

  // Sync refs with state for closure access
  useEffect(() => { targetLangRef.current = targetLang; }, [targetLang]);
  useEffect(() => { liveSummaryRef.current = liveSummary; }, [liveSummary]);
  useEffect(() => { transcriptsRef.current = transcripts; }, [transcripts]);

  // Update orchestrator when target language changes
  useEffect(() => {
    orchestratorRef.current?.updateTargetLang(targetLang);
  }, [targetLang]);

  // Update orchestrator when translation toggle changes
  useEffect(() => {
    orchestratorRef.current?.updateTranslationEnabled(translationEnabled);
  }, [translationEnabled]);

  // Sync VAD enabled ref and update orchestrator
  useEffect(() => {
    vadEnabledRef.current = vadEnabled;
    orchestratorRef.current?.setVadConfig({ enabled: vadEnabled });
  }, [vadEnabled]);

  // Poll VAD stats while recording with whisper
  useEffect(() => {
    if (!isRecording || sttSource !== 'whisper') {
      setVadSavedPercent(0);
      return;
    }
    const interval = setInterval(() => {
      const stats = orchestratorRef.current?.getVadStats();
      if (stats) {
        setVadSavedPercent(stats.savedPercent);
      }
    }, 2000);
    return () => clearInterval(interval);
  }, [isRecording, sttSource]);

  // Mic preview: create AudioContext + AnalyserNode when device changes (not recording)
  useEffect(() => {
    if (isRecording) return;

    // Cleanup previous preview
    const cleanupPreview = () => {
      previewStreamRef.current?.getTracks().forEach((t) => t.stop());
      previewStreamRef.current = null;
      previewCtxRef.current?.close().catch(() => {});
      previewCtxRef.current = null;
      setPreviewAnalyser(null);
    };

    if (!selectedDeviceId) {
      cleanupPreview();
      return;
    }

    let cancelled = false;

    (async () => {
      try {
        const stream = await navigator.mediaDevices.getUserMedia({
          audio: { deviceId: { exact: selectedDeviceId } },
        });
        if (cancelled) {
          stream.getTracks().forEach((t) => t.stop());
          return;
        }
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

    return () => {
      cancelled = true;
      cleanupPreview();
    };
  }, [selectedDeviceId, isRecording]);

  if (isLoading) {
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

  const speechErrorMessages: Record<string, string> = {
    'not-allowed': 'Microphone permission denied for speech recognition.',
    'network': 'Network error — speech recognition requires internet.',
    'service-not-allowed': 'Speech recognition service is not available.',
    'language-not-supported': 'Korean speech recognition is not supported in this browser.',
    'recognition-stalled': '음성 인식이 중단되었습니다. 자동으로 재시작합니다...',
  };

  // Use client-side meetingId for RecordButton until server creates one
  const clientMeetingId = serverMeetingId || clientMeetingIdBase;

  const handleRecordingStart = (stream: MediaStream) => {
    // Stop mic preview (recording will use its own stream)
    previewStreamRef.current?.getTracks().forEach((t) => t.stop());
    previewStreamRef.current = null;
    previewCtxRef.current?.close().catch(() => {});
    previewCtxRef.current = null;
    setPreviewAnalyser(null);

    // Reset state
    setIsRecording(true);
    setIsPaused(false);
    setTranscripts([]);
    setCurrentInterim('');
    setTotalWordCount(0);
    setLastSummaryWordCount(0);
    lastSummaryWordCountRef.current = 0;
    setTranslations([]);
    setLiveSummary('');
    setIsGeneratingSummary(false);
    setSpeechError(null);
    setDetectedQuestions([]);
    accumulatedForDetectRef.current = [];
    askedQuestionsRef.current = [];
    detectWordsAccumRef.current = 0;
    setBackendAudioKey(null);
    if (detectTimerRef.current) { clearTimeout(detectTimerRef.current); detectTimerRef.current = null; }

    // Create orchestrator with callbacks
    const orchestrator = new SttOrchestrator({
      onTranscript: (text, isFinal) => {
        if (isFinal) {
          const entry: TranscriptEntry = { text, isFinal: true, timestamp: new Date().toISOString() };
          setTranscripts((prev) => [...prev, entry]);
          setCurrentInterim('');

          // Track word count and trigger summary
          const words = countWords(text);
          setTotalWordCount((prev) => {
            const newTotal = prev + words;
            // Trigger live summary when crossing 200-word threshold
            if (newTotal - lastSummaryWordCountRef.current >= summaryIntervalRef.current) {
              lastSummaryWordCountRef.current = newTotal;
              setLastSummaryWordCount(newTotal);
              const allText = [...transcriptsRef.current, entry].map(t => t.text).join('\n');
              setIsGeneratingSummary(true);
              summaryApi.summarizeLive(clientMeetingId, allText, liveSummaryRef.current || undefined)
                .then((res) => {
                  setLiveSummary(res.summary);
                })
                .catch((err) => console.error('Summary failed:', err))
                .finally(() => setIsGeneratingSummary(false));
            }
            return newTotal;
          });

          // Word-count-based question detection (triggers every N words)
          detectWordsAccumRef.current += words;
          accumulatedForDetectRef.current.push(text);
          if (detectWordsAccumRef.current >= detectWordIntervalRef.current) {
            detectWordsAccumRef.current = 0;
            if (detectTimerRef.current) clearTimeout(detectTimerRef.current);
            detectTimerRef.current = setTimeout(async () => {
              if (detectInFlightRef.current) return;
              accumulatedForDetectRef.current = [];
              detectInFlightRef.current = true;
              try {
                const fullContext = transcriptsRef.current.map(t => t.text).join('\n');
                const trimmedContext = fullContext.length > 2000
                  ? fullContext.slice(-2000)
                  : fullContext;
                const result = await qaApi.detectQuestions(
                  trimmedContext,
                  askedQuestionsRef.current,
                  liveSummaryRef.current || undefined,
                );
                if (result.questions.length > 0) {
                  setDetectedQuestions(result.questions);
                }
              } catch {
                // silent fail — don't block recording flow
              } finally {
                detectInFlightRef.current = false;
              }
            }, 1500);
          }
        } else {
          setCurrentInterim(text);
        }
      },
      onTranslation: (original, translated, lang, isFinal) => {
        if (isFinal) {
          setTranslations((prev) => [...prev, {
            original,
            translated,
            targetLang: lang,
            timestamp: new Date().toISOString(),
          }]);
          setCurrentInterimTranslation(null);
        } else {
          setCurrentInterimTranslation({ original, translated, targetLang: lang });
        }
      },
      onQuestion: (questions) => {
        setDetectedQuestions(questions);
      },
      onSourceChange: (source) => {
        setSttSource(source);
      },
      onError: (error) => {
        setSpeechError(speechErrorMessages[error] || error);
      },
      onBackendAudioSaved: (key) => {
        setBackendAudioKey(key);
      },
      onVadStatus: (isSpeaking) => {
        setVadIsSpeaking(isSpeaking);
      },
    }, 'ko', targetLangRef.current, translationEnabled, undefined, clientMeetingId);

    // Apply VAD configuration
    orchestrator.setVadConfig({ enabled: vadEnabledRef.current });

    orchestratorRef.current = orchestrator;
    orchestrator.start(stream);
  };

  const handleRecordingPause = () => {
    setIsPaused(true);
    // Note: orchestrator continues running in background during pause
    // This is intentional — STT will keep processing audio
  };

  const handleRecordingResume = () => {
    setIsPaused(false);
  };

  const handleRecordingStop = async () => {
    setIsRecording(false);
    setIsPaused(false);
    if (detectTimerRef.current) { clearTimeout(detectTimerRef.current); detectTimerRef.current = null; }
    // Fire-and-forget: don't block UI while ECS scales down
    orchestratorRef.current?.stop().catch(() => {});
    orchestratorRef.current = null;
  };

  const handleBlobReady = async (blob: Blob, mimeType: string) => {
    // Helper: race a promise against a timeout
    const withTimeout = <T,>(promise: Promise<T>, ms: number, label: string): Promise<T> =>
      Promise.race([
        promise,
        new Promise<never>((_, reject) => setTimeout(() => reject(new Error(`${label} timed out (${ms / 1000}s)`)), ms)),
      ]);

    try {
      // Step 1: Create meeting (15s timeout)
      setPostRecordingStep('creating');
      const result = await withTimeout(
        meetingsApi.create({ title: meetingTitle || formatDefaultTitle(new Date()), sttProvider }),
        15000, 'Create meeting'
      );
      const newMeetingId = result.meetingId;
      setServerMeetingId(newMeetingId);

      // Step 2: Save live transcript (status='transcribing' — backend pipeline will upgrade to full summary)
      setPostRecordingStep('saving');
      const transcriptText = transcriptsRef.current.map(t => t.text).join('\n');
      await withTimeout(
        meetingsApi.update(newMeetingId, {
          content: liveSummaryRef.current || transcriptText.slice(0, 500),
          transcriptA: transcriptText,
          status: 'transcribing',
        }),
        15000, 'Save transcript'
      );

      // Step 3: Upload audio — prefer backend-aggregated audio if available
      setPostRecordingStep('uploading');
      const savedBackendKey = backendAudioKey || orchestratorRef.current?.getBackendAudioKey();

      if (savedBackendKey) {
        // Backend ECS already saved high-quality audio to S3
        await withTimeout(
          uploadsApi.notifyComplete({ meetingId: newMeetingId, key: savedBackendKey, category: 'audio' }),
          15000, 'Notify backend audio'
        );
      } else {
        // Fallback: upload MediaRecorder blob
        const fileName = `recording_${Date.now()}.webm`;
        const { uploadUrl, key } = await withTimeout(
          uploadsApi.getPresignedUrl({
            fileName,
            fileType: mimeType || 'audio/webm',
            category: 'audio',
            meetingId: newMeetingId,
          }),
          15000, 'Get upload URL'
        );
        await uploadAudioWithRetry(blob, uploadUrl, mimeType);
        await withTimeout(
          uploadsApi.notifyComplete({ meetingId: newMeetingId, key, category: 'audio' }),
          15000, 'Notify upload complete'
        );
      }

      // Step 4: Redirect to meeting detail
      setPostRecordingStep('redirecting');
      router.push(`/meeting/${newMeetingId}`);
    } catch (err) {
      console.error('Failed to process recording:', err);
      setErrorMessage(err instanceof Error ? err.message : 'Failed to process recording');
      setPostRecordingStep('error');
    }
  };

  // Legacy callback (kept for iOS native capture fallback)
  const handleRecordingComplete = async (audioUrl: string) => {
    // For native capture, audio is already uploaded; just create meeting and redirect
    try {
      setPostRecordingStep('creating');
      const result = await meetingsApi.create({
        title: meetingTitle || formatDefaultTitle(new Date()),
      });
      setPostRecordingStep('redirecting');
      router.push(`/meeting/${result.meetingId}`);
    } catch (err) {
      console.error('Failed to create meeting:', err);
      setErrorMessage(err instanceof Error ? err.message : 'Failed to create meeting');
      setPostRecordingStep('error');
    }
  };

  const handleRetry = () => {
    setPostRecordingStep(null);
    setErrorMessage(null);
    setSpeechError(null);
  };

  const handleCheckpoint = async (blob: Blob, mimeType: string) => {
    // Fire-and-forget checkpoint upload for audio loss prevention
    try {
      const fileName = `checkpoint_${Date.now()}.webm`;
      const { uploadUrl } = await uploadsApi.getPresignedUrl({
        fileName,
        fileType: mimeType || 'audio/webm',
        category: 'audio',
        meetingId: clientMeetingId,
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

  const handleUploadComplete = (files: { name: string; url: string }[]) => {
    setCapturedImages((prev) => [...prev, ...files]);
  };

  const handleCaptureImage = async (file: File) => {
    try {
      const { uploadUrl, key } = await uploadsApi.getPresignedUrl({
        fileName: file.name,
        fileType: file.type,
        category: 'image',
      });
      await fetch(uploadUrl, {
        method: 'PUT',
        body: file,
        headers: { 'Content-Type': file.type },
      });
      // Notify backend so process-image Lambda is triggered
      await uploadsApi.notifyComplete({ meetingId: clientMeetingId, key, category: 'image' }).catch((err) =>
        console.warn('notifyComplete failed (meeting may not exist yet):', err)
      );
      setCapturedImages((prev) => [...prev, {
        name: file.name,
        url: URL.createObjectURL(file),
      }]);
    } catch (err) {
      console.error('Image capture failed:', err);
    }
  };

  // Combine final transcripts + current interim for display
  const displayTranscripts: TranscriptEntry[] = [
    ...transcripts,
    ...(currentInterim
      ? [{ text: currentInterim, isFinal: false, timestamp: new Date().toISOString() }]
      : []),
  ];

  // Build transcript context for Live Q&A (include interim for real-time detection)
  const transcriptContext = [
    ...transcripts.map((t) => t.text),
    ...(currentInterim ? [currentInterim] : []),
  ].join('\n');

  return (
    <AppLayout activePath="/record" showMobileNav={true}>
      {/* Header */}
      <header className="flex items-center justify-between px-6 lg:px-16 py-4 bg-white/80 dark:bg-slate-900/80 backdrop-blur-md sticky top-0 z-10 border-b border-slate-100 dark:border-slate-800">
        <button
          onClick={() => router.back()}
          className="p-2 hover:bg-[var(--notion-hover)] rounded-lg transition-colors"
        >
          <span className="material-symbols-outlined text-text-secondary">arrow_back</span>
        </button>
        <input
          type="text"
          value={meetingTitle}
          onChange={(e) => setMeetingTitle(e.target.value)}
          placeholder="Meeting Title"
          className="text-lg font-bold tracking-tight bg-transparent border-none text-center focus:outline-none focus:ring-0 text-text-primary placeholder:text-text-muted flex-1 mx-4"
        />
        <RecordingConfig
          summaryInterval={summaryInterval}
          onSummaryIntervalChange={(val) => {
            setSummaryInterval(val);
            summaryIntervalRef.current = val;
          }}
          detectWordInterval={detectWordInterval}
          onDetectWordIntervalChange={(val) => {
            setDetectWordInterval(val);
            detectWordIntervalRef.current = val;
          }}
          translationEnabled={translationEnabled}
          onTranslationToggle={setTranslationEnabled}
          targetLang={targetLang}
          onTargetLangChange={setTargetLang}
          isRecording={isRecording}
          sttProvider={sttProvider}
          onSttProviderChange={setSttProvider}
          vadEnabled={vadEnabled}
          onVadToggle={setVadEnabled}
        />
      </header>

      {/* Speech Recognition Error Banner */}
      {speechError && (
        <div className="mx-6 mt-2 px-4 py-3 bg-amber-50 dark:bg-amber-900/20 border border-amber-200 dark:border-amber-800 rounded-lg flex items-start gap-3">
          <span className="material-symbols-outlined text-amber-500 text-xl mt-0.5">warning</span>
          <div className="flex-1">
            <p className="text-sm text-amber-800 dark:text-amber-200">{speechError}</p>
          </div>
          <button
            onClick={() => setSpeechError(null)}
            className="p-1 hover:bg-amber-100 dark:hover:bg-amber-900/40 rounded transition-colors"
          >
            <span className="material-symbols-outlined text-amber-400 text-lg">close</span>
          </button>
        </div>
      )}

      {/* Main Content */}
      <div className="flex flex-1">
      <main className="flex-1 flex flex-col px-6 lg:px-16 pt-8 lg:pt-12 pb-32 max-w-4xl mx-auto">
        {/* Mic Selector — always visible, disabled during recording */}
        {!postRecordingStep && (
          <div className="flex flex-col items-center gap-3">
            <MicSelector
              devices={devices}
              selectedDeviceId={selectedDeviceId}
              onSelect={selectDevice}
              disabled={isRecording}
              analyser={isRecording ? analyserNode : previewAnalyser}
            />
            {/* STT Engine Selector (for batch transcription after recording) */}
            <SttProviderSelector
              sttProvider={sttProvider}
              onSttProviderChange={setSttProvider}
              isRecording={isRecording}
            />
          </div>
        )}

        {/* Recording Section */}
        <div className="flex flex-col items-center justify-center mb-8">
          <RecordButton
            meetingId={clientMeetingId}
            meetingTitle={meetingTitle || 'Untitled Meeting'}
            deviceId={selectedDeviceId || undefined}
            onRecordingComplete={handleRecordingComplete}
            onBlobReady={handleBlobReady}
            onError={(error) => {
              if (isRecording) {
                // Error during/after recording — show blocking overlay
                setErrorMessage(error);
                setPostRecordingStep('error');
              } else {
                // Error before recording (e.g. mic permission) — show dismissible banner
                setSpeechError(error);
              }
            }}
            onRecordingStart={handleRecordingStart}
            onRecordingPause={handleRecordingPause}
            onRecordingResume={handleRecordingResume}
            onRecordingStop={handleRecordingStop}
            onPermissionGranted={refreshDevices}
            onCaptureImage={handleCaptureImage}
            onAnalyserReady={setAnalyserNode}
            onCheckpoint={handleCheckpoint}
          />

          {/* STT Source Indicator */}
          {isRecording && (
            <div className="flex items-center gap-3 justify-center mt-2">
              <div className="flex items-center gap-1.5">
                <span className={`w-2 h-2 rounded-full ${
                  sttSource === 'whisper' ? 'bg-green-500' :
                  sttSource === 'fallback' ? 'bg-amber-500 animate-pulse' : 'bg-slate-400'
                }`} />
                <span className="text-xs text-slate-500">
                  {sttSource === 'whisper' ? 'AI Engine' :
                   sttSource === 'fallback' ? 'AI Engine 준비 중...' : 'Idle'}
                </span>
              </div>
              {/* VAD Status Indicator */}
              {vadEnabled && sttSource === 'whisper' && (
                <div className="flex items-center gap-1.5">
                  <span className={`w-2 h-2 rounded-full transition-colors ${
                    vadIsSpeaking ? 'bg-green-500' : 'bg-slate-300'
                  }`} />
                  <span className="text-xs text-slate-500">
                    VAD {vadSavedPercent > 0 ? `(-${vadSavedPercent}%)` : ''}
                  </span>
                </div>
              )}
            </div>
          )}
        </div>
        {!isRecording && !postRecordingStep && (
          <p className="text-center text-sm text-slate-400 dark:text-slate-500 -mt-4 mb-4">
            Tap the microphone to start recording
          </p>
        )}

        {/* Recording Tabs — shown when recording */}
        {isRecording && (
          <RecordingTabs
            captionsContent={
              <LiveTranscript
                transcripts={displayTranscripts}
                wordCount={totalWordCount}
              />
            }
            translationContent={
              translationEnabled ? (
                <TranslationView
                  translations={translations}
                  targetLang={targetLang}
                  onTargetLangChange={setTargetLang}
                  isActive={true}
                  interimTranslation={currentInterimTranslation}
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
                summary={liveSummary}
                isGenerating={isGeneratingSummary}
                wordCount={totalWordCount}
                lastSummaryWordCount={lastSummaryWordCount}
                summaryInterval={summaryInterval}
              />
            }
          />
        )}

        {/* Captured Images Section */}
        {!isRecording && !postRecordingStep && (
          <section className="mt-8">
            <div className="flex items-center justify-between mb-4">
              <h2 className="text-base font-bold text-slate-800 dark:text-slate-200 uppercase tracking-wider text-xs">
                Captured Images
              </h2>
              {capturedImages.length > 0 && (
                <button className="text-primary text-sm font-semibold">
                  View All ({capturedImages.length})
                </button>
              )}
            </div>

            <div className="grid grid-cols-3 gap-3">
              {capturedImages.slice(0, 2).map((img, index) => (
                <div key={index} className="relative group cursor-pointer">
                  <div className="aspect-square bg-slate-200 dark:bg-slate-800 rounded-lg overflow-hidden border border-slate-200 dark:border-slate-700">
                    <img
                      src={img.url}
                      alt={img.name}
                      className="w-full h-full object-cover"
                    />
                  </div>
                </div>
              ))}

              {/* Add File Button */}
              <label className="relative cursor-pointer">
                <div className="aspect-square bg-slate-200 dark:bg-slate-800 rounded-lg overflow-hidden border border-slate-200 dark:border-slate-700 flex flex-col items-center justify-center bg-primary/5 border-dashed hover:bg-primary/10 transition-colors">
                  <span className="material-symbols-outlined text-primary/40 text-3xl">upload_file</span>
                  <span className="text-[10px] font-medium text-primary/60 mt-1">Add File</span>
                </div>
                <input
                  type="file"
                  accept="image/*"
                  multiple
                  className="hidden"
                  onChange={(e) => {
                    if (e.target.files) {
                      const files = Array.from(e.target.files).map((f) => ({
                        name: f.name,
                        url: URL.createObjectURL(f),
                      }));
                      setCapturedImages((prev) => [...prev, ...files]);
                    }
                  }}
                />
              </label>
            </div>
          </section>
        )}

        {/* Drag and Drop Uploader (Desktop) */}
        {!isRecording && !postRecordingStep && (
          <section className="hidden lg:block mt-8">
            <FileUploader
              meetingId={clientMeetingId}
              onUploadComplete={handleUploadComplete}
              accept="image/*"
            />
          </section>
        )}
      </main>

      {/* Desktop Q&A Side Panel — always visible on PC */}
      <aside className="hidden lg:flex w-80 shrink-0 h-[calc(100vh-56px)] sticky top-[56px] border-l border-border-default">
        <LiveQAPanel
          transcriptContext={transcriptContext}
          onDetectedQuestionsChange={setDetectedCount}
          serverDetectedQuestions={detectedQuestions}
          onAskedQuestion={(q) => { askedQuestionsRef.current.push(q); }}
        />
      </aside>
      </div>

      {/* Post-Recording Toast Banner */}
      {postRecordingStep && (
        <PostRecordingBanner
          step={postRecordingStep}
          errorMessage={errorMessage}
          onRetry={handleRetry}
          onDismiss={() => {
            setPostRecordingStep(null);
            router.push('/');
          }}
        />
      )}

      {/* Mobile Floating Q&A Button — visible during recording on small screens */}
      {isRecording && !isQAOpen && (
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
      {isRecording && isQAOpen && (
        <div className="lg:hidden fixed inset-0 z-40">
          {/* Backdrop */}
          <div
            className="absolute inset-0 bg-black/30"
            onClick={() => setIsQAOpen(false)}
          />
          {/* Sheet */}
          <div className="absolute bottom-0 left-0 right-0 h-[50vh] bg-white dark:bg-slate-900 rounded-t-2xl shadow-2xl flex flex-col animate-slide-up">
            {/* Drag Handle */}
            <button
              onClick={() => setIsQAOpen(false)}
              className="flex justify-center pt-3 pb-2"
            >
              <div className="w-10 h-1 rounded-full bg-slate-300 dark:bg-slate-600" />
            </button>
            {/* Q&A Panel */}
            <div className="flex-1 min-h-0">
              <LiveQAPanel
                transcriptContext={transcriptContext}
                onDetectedQuestionsChange={setDetectedCount}
                serverDetectedQuestions={detectedQuestions}
                onAskedQuestion={(q) => { askedQuestionsRef.current.push(q); }}
              />
            </div>
          </div>
        </div>
      )}

    </AppLayout>
  );
}
