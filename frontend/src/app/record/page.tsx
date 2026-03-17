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
import { useAudioDevices } from '@/hooks/useAudioDevices';
import { meetingsApi, summaryApi, uploadsApi, qaApi } from '@/lib/api';
import { SttOrchestrator, SttSource } from '@/lib/sttOrchestrator';
import { countWords } from '@/lib/speechRecognition';

interface TranscriptEntry {
  text: string;
  isFinal: boolean;
  timestamp: string;
}

type PostRecordingStep = 'creating' | 'saving' | 'redirecting' | 'error';

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
  const [targetLang, setTargetLang] = useState('en');
  const [translations, setTranslations] = useState<{ original: string; translated: string; targetLang: string; timestamp: string }[]>([]);
  const [currentInterimTranslation, setCurrentInterimTranslation] = useState<{ original: string; translated: string; targetLang: string } | null>(null);

  // Summary state
  const [liveSummary, setLiveSummary] = useState('');
  const [isGeneratingSummary, setIsGeneratingSummary] = useState(false);

  // Server-pushed detected questions
  const [detectedQuestions, setDetectedQuestions] = useState<string[]>([]);

  // Sentence-based question detection refs
  const detectTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const accumulatedForDetectRef = useRef<string[]>([]);
  const askedQuestionsRef = useRef<string[]>([]);

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

          // Sentence-based question detection (debounced)
          accumulatedForDetectRef.current.push(text);
          if (detectTimerRef.current) clearTimeout(detectTimerRef.current);
          detectTimerRef.current = setTimeout(async () => {
            const accumulated = accumulatedForDetectRef.current.join('\n');
            if (accumulated.length < 50) return;
            accumulatedForDetectRef.current = [];
            try {
              const fullContext = transcriptsRef.current.map(t => t.text).join('\n');
              const result = await qaApi.detectQuestions(fullContext, askedQuestionsRef.current);
              if (result.questions.length > 0) {
                setDetectedQuestions(result.questions);
              }
            } catch {
              // silent fail — don't block recording flow
            }
          }, 1000);
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
    }, 'ko', targetLangRef.current);

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

      // Step 2: Save transcript and summary (15s timeout)
      setPostRecordingStep('saving');
      const transcriptText = transcriptsRef.current.map(t => t.text).join('\n');
      await withTimeout(
        meetingsApi.update(newMeetingId, {
          content: liveSummaryRef.current || transcriptText.slice(0, 500),
          transcriptA: transcriptText,
          status: 'done',
        }),
        15000, 'Save transcript'
      );

      // Step 3: Redirect immediately
      setPostRecordingStep('redirecting');
      router.push(`/meeting/${newMeetingId}`);

      // Step 4: Background audio upload (non-blocking, for archival)
      (async () => {
        try {
          const fileName = `recording_${Date.now()}.webm`;
          const { uploadUrl } = await uploadsApi.getPresignedUrl({
            fileName,
            fileType: mimeType || 'audio/webm',
            category: 'audio',
            meetingId: newMeetingId,
          });
          await fetch(uploadUrl, {
            method: 'PUT',
            body: blob,
            headers: { 'Content-Type': mimeType || 'audio/webm' },
          });
        } catch (err) {
          console.warn('Background audio upload failed:', err);
        }
      })();
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
      <header className="flex items-center justify-between px-6 lg:px-16 py-4 bg-white/80 dark:bg-slate-900/80 backdrop-blur-md sticky top-0 z-10">
        <button
          onClick={() => router.back()}
          className="p-2 hover:bg-[var(--notion-hover)] rounded-md transition-colors"
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
        <select
          value={summaryInterval}
          onChange={(e) => {
            const val = Number(e.target.value);
            setSummaryInterval(val);
            summaryIntervalRef.current = val;
          }}
          className="text-sm bg-surface-secondary border-none rounded-md px-3 py-1.5 text-text-secondary focus:outline-none focus:ring-2 focus:ring-primary/30"
        >
          <option value={100}>100w</option>
          <option value={200}>200w</option>
          <option value={500}>500w</option>
          <option value={1000}>1000w</option>
        </select>
        <select
          value={targetLang}
          onChange={(e) => setTargetLang(e.target.value)}
          className="text-sm bg-surface-secondary border-none rounded-md px-3 py-1.5 text-text-secondary focus:outline-none focus:ring-2 focus:ring-primary/30"
        >
          <option value="en">EN</option>
          <option value="ja">JA</option>
          <option value="zh">ZH</option>
          <option value="es">ES</option>
          <option value="fr">FR</option>
          <option value="de">DE</option>
        </select>
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
            <div className="flex rounded-lg overflow-hidden border border-slate-200 dark:border-slate-700">
              <button
                onClick={() => setSttProvider('transcribe')}
                disabled={isRecording}
                className={`px-4 py-1.5 text-xs font-medium transition-colors ${
                  sttProvider === 'transcribe'
                    ? 'bg-[#3211d4] text-white'
                    : 'bg-white dark:bg-slate-800 text-slate-600 dark:text-slate-400 hover:bg-slate-50 dark:hover:bg-slate-700'
                } disabled:opacity-50 disabled:cursor-not-allowed`}
              >
                Transcribe
              </button>
              <button
                onClick={() => setSttProvider('nova-sonic')}
                disabled={isRecording}
                className={`px-4 py-1.5 text-xs font-medium transition-colors border-l border-slate-200 dark:border-slate-700 ${
                  sttProvider === 'nova-sonic'
                    ? 'bg-[#3211d4] text-white'
                    : 'bg-white dark:bg-slate-800 text-slate-600 dark:text-slate-400 hover:bg-slate-50 dark:hover:bg-slate-700'
                } disabled:opacity-50 disabled:cursor-not-allowed`}
              >
                Nova Sonic V2
              </button>
            </div>
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
          />

          {/* STT Source Indicator */}
          {isRecording && (
            <div className="flex items-center gap-1.5 justify-center mt-2">
              <span className={`w-2 h-2 rounded-full ${
                sttSource === 'whisper' ? 'bg-green-500' :
                sttSource === 'fallback' ? 'bg-amber-500 animate-pulse' : 'bg-slate-400'
              }`} />
              <span className="text-xs text-slate-500">
                {sttSource === 'whisper' ? 'AI Engine' :
                 sttSource === 'fallback' ? 'AI Engine 준비 중...' : 'Idle'}
              </span>
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
              <TranslationView
                translations={translations}
                targetLang={targetLang}
                onTargetLangChange={setTargetLang}
                isActive={true}
                interimTranslation={currentInterimTranslation}
              />
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
        <div className="fixed top-[64px] left-0 right-0 z-40 mx-4 mt-2">
          <div className={`rounded-xl shadow-lg px-4 py-3 flex items-center gap-3 ${
            postRecordingStep === 'error'
              ? 'bg-red-50 dark:bg-red-900/30 border border-red-200 dark:border-red-800'
              : 'bg-white dark:bg-slate-800 border border-slate-200 dark:border-slate-700'
          }`}>
            {postRecordingStep === 'error' ? (
              <>
                <span className="material-symbols-outlined text-red-500">error</span>
                <p className="flex-1 text-sm text-red-700 dark:text-red-300 truncate">
                  {errorMessage || 'An unexpected error occurred.'}
                </p>
                <button
                  onClick={handleRetry}
                  className="px-3 py-1.5 rounded-lg text-xs font-medium border border-red-200 dark:border-red-700 text-red-600 dark:text-red-400 hover:bg-red-100 dark:hover:bg-red-900/40 transition-colors shrink-0"
                >
                  Try Again
                </button>
                <button
                  onClick={() => router.push('/')}
                  className="px-3 py-1.5 rounded-lg text-xs font-medium bg-primary text-white hover:bg-primary/90 transition-colors shrink-0"
                >
                  Home
                </button>
              </>
            ) : (
              <>
                <div className="animate-spin rounded-full h-5 w-5 border-2 border-primary border-t-transparent shrink-0" />
                <p className="flex-1 text-sm font-medium text-slate-700 dark:text-slate-300">
                  {postRecordingStep === 'creating' && 'Creating meeting...'}
                  {postRecordingStep === 'saving' && 'Saving transcript...'}
                  {postRecordingStep === 'redirecting' && 'Opening meeting...'}
                </p>
                <button
                  onClick={() => { setPostRecordingStep(null); router.push('/'); }}
                  className="p-1.5 hover:bg-slate-100 dark:hover:bg-slate-700 rounded-md transition-colors shrink-0"
                  title="Dismiss"
                >
                  <span className="material-symbols-outlined text-slate-400 text-lg">close</span>
                </button>
              </>
            )}
          </div>
        </div>
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
