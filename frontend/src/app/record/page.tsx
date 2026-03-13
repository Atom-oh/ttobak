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
import { meetingsApi, translateApi, summaryApi, uploadsApi } from '@/lib/api';
import { BrowserSpeechRecognition, countWords } from '@/lib/speechRecognition';

interface TranscriptEntry {
  text: string;
  isFinal: boolean;
  timestamp: string;
}

type PostRecordingStep = 'uploading' | 'creating' | 'redirecting' | 'error';

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
  const [capturedImages, setCapturedImages] = useState<{ name: string; url: string }[]>([]);

  // Analyser node for MicSelector level meter
  const [analyserNode, setAnalyserNode] = useState<AnalyserNode | null>(null);

  // Mic preview (level meter before recording)
  const [previewAnalyser, setPreviewAnalyser] = useState<AnalyserNode | null>(null);
  const previewStreamRef = useRef<MediaStream | null>(null);
  const previewCtxRef = useRef<AudioContext | null>(null);

  // STT provider selection
  const [sttProvider, setSttProvider] = useState<'transcribe' | 'nova-sonic'>('transcribe');

  // Recording state
  const [isRecording, setIsRecording] = useState(false);
  const [isPaused, setIsPaused] = useState(false);

  // Speech recognition state
  const [transcripts, setTranscripts] = useState<TranscriptEntry[]>([]);
  const [currentInterim, setCurrentInterim] = useState<string>('');
  const [totalWordCount, setTotalWordCount] = useState(0);
  const lastSummaryWordCountRef = useRef(0);
  const speechRef = useRef<BrowserSpeechRecognition | null>(null);

  // Translation state
  const [targetLang, setTargetLang] = useState('en');
  const [translations, setTranslations] = useState<{ original: string; translated: string; targetLang: string; timestamp: string }[]>([]);

  // Summary state
  const [liveSummary, setLiveSummary] = useState('');
  const [isGeneratingSummary, setIsGeneratingSummary] = useState(false);

  // Translation debounce refs
  const pendingTranslationsRef = useRef<{ text: string; timestamp: string }[]>([]);
  const translateTimerRef = useRef<NodeJS.Timeout>(undefined);

  // Refs for closures in speech callback
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

  // Mobile Q&A bottom sheet state
  const [isQAOpen, setIsQAOpen] = useState(false);
  const [detectedCount, setDetectedCount] = useState(0);

  // Sync refs with state for closure access
  useEffect(() => { targetLangRef.current = targetLang; }, [targetLang]);
  useEffect(() => { liveSummaryRef.current = liveSummary; }, [liveSummary]);
  useEffect(() => { transcriptsRef.current = transcripts; }, [transcripts]);

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

  // Cleanup translation debounce timer on unmount
  useEffect(() => {
    return () => {
      if (translateTimerRef.current) clearTimeout(translateTimerRef.current);
    };
  }, []);

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

  const startSpeechRecognition = () => {
    if (!BrowserSpeechRecognition.isSupported()) {
      setSpeechError('Speech recognition is not supported in this browser. Recording will continue without live captions.');
      return;
    }

    const speech = new BrowserSpeechRecognition('ko-KR');
    speechRef.current = speech;

    const started = speech.start((result) => {
      if (result.isFinal) {
        setTranscripts((prev) => [...prev, result]);
        setCurrentInterim('');

        // Queue translation with 300ms debounce for batching
        pendingTranslationsRef.current.push({ text: result.text, timestamp: result.timestamp });
        if (translateTimerRef.current) clearTimeout(translateTimerRef.current);
        translateTimerRef.current = setTimeout(() => {
          const batch = pendingTranslationsRef.current.splice(0);
          if (batch.length === 0) return;
          const combined = batch.map(b => b.text).join('\n');
          const lang = targetLangRef.current;
          translateApi.translate(combined, 'ko', lang)
            .then((res) => {
              const parts = res.translatedText.split('\n');
              setTranslations((prev) => [...prev, ...batch.map((b, i) => ({
                original: b.text,
                translated: parts[i] || '',
                targetLang: lang,
                timestamp: b.timestamp,
              }))]);
            })
            .catch((err) => console.error('Translation failed:', err));
        }, 300);

        // Track word count
        const words = countWords(result.text);
        setTotalWordCount((prev) => {
          const newTotal = prev + words;
          // Trigger live summary when crossing 200-word threshold
          if (newTotal - lastSummaryWordCountRef.current >= 200) {
            lastSummaryWordCountRef.current = newTotal;
            const allText = [...transcriptsRef.current, result].map(t => t.text).join('\n');
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
      } else {
        setCurrentInterim(result.text);
      }
    }, (error) => {
      setSpeechError(speechErrorMessages[error] || `Speech recognition error: ${error}`);
    });

    if (!started) {
      setSpeechError('Failed to start speech recognition. Recording will continue without live captions.');
    }
  };

  const handleRecordingStart = () => {
    // Stop mic preview (recording will use its own stream)
    previewStreamRef.current?.getTracks().forEach((t) => t.stop());
    previewStreamRef.current = null;
    previewCtxRef.current?.close().catch(() => {});
    previewCtxRef.current = null;
    setPreviewAnalyser(null);

    setIsRecording(true);
    setIsPaused(false);
    setTranscripts([]);
    setCurrentInterim('');
    setTotalWordCount(0);
    lastSummaryWordCountRef.current = 0;
    setTranslations([]);
    setLiveSummary('');
    setIsGeneratingSummary(false);
    setSpeechError(null);
    startSpeechRecognition();
  };

  const handleRecordingPause = () => {
    setIsPaused(true);
    speechRef.current?.pause();
  };

  const handleRecordingResume = () => {
    setIsPaused(false);
    speechRef.current?.resume();
  };

  const handleRecordingStop = () => {
    setIsRecording(false);
    setIsPaused(false);
    speechRef.current?.stop();
    speechRef.current = null;
  };

  const handleRecordingComplete = async (audioUrl: string) => {
    setPostRecordingStep('uploading');

    try {
      setPostRecordingStep('creating');
      const result = await meetingsApi.create({
        title: meetingTitle || formatDefaultTitle(new Date()),
        sttProvider,
      });
      const newMeetingId = result.meetingId;
      setServerMeetingId(newMeetingId);

      console.log('Recording uploaded to:', audioUrl);

      setPostRecordingStep('redirecting');
      router.push(`/meeting/${newMeetingId}`);
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

  // Use client-side meetingId for RecordButton until server creates one
  const clientMeetingId = serverMeetingId || `meeting_${Date.now()}`;

  return (
    <AppLayout activePath="/record" showMobileNav={true}>
      {/* Header */}
      <header className="flex items-center justify-between px-6 py-4 bg-white/80 dark:bg-slate-900/80 backdrop-blur-md sticky top-0 z-10">
        <button
          onClick={() => router.back()}
          className="p-2 hover:bg-primary/10 rounded-full transition-colors"
        >
          <span className="material-symbols-outlined text-slate-700 dark:text-slate-300">arrow_back</span>
        </button>
        <input
          type="text"
          value={meetingTitle}
          onChange={(e) => setMeetingTitle(e.target.value)}
          placeholder="Meeting Title"
          className="text-lg font-bold tracking-tight bg-transparent border-none text-center focus:outline-none focus:ring-0 text-slate-900 dark:text-white placeholder:text-slate-400 flex-1 mx-4"
        />
        <select
          value={targetLang}
          onChange={(e) => setTargetLang(e.target.value)}
          className="text-sm bg-slate-100 dark:bg-slate-800 border-none rounded-lg px-3 py-1.5 text-slate-700 dark:text-slate-300 focus:outline-none focus:ring-2 focus:ring-primary/30"
        >
          <option value="en">EN</option>
          <option value="ja">JA</option>
          <option value="zh">ZH</option>
          <option value="es">ES</option>
          <option value="fr">FR</option>
          <option value="de">DE</option>
        </select>
        <button
          onClick={async () => {
            await logout();
            router.push('/');
          }}
          className="p-2 hover:bg-red-50 dark:hover:bg-red-900/20 rounded-full transition-colors"
          title="Logout"
        >
          <span className="material-symbols-outlined text-slate-500 dark:text-slate-400 hover:text-red-500 dark:hover:text-red-400">logout</span>
        </button>
      </header>

      {/* Speech Recognition Error Banner */}
      {speechError && (
        <div className="mx-6 mt-2 px-4 py-3 bg-amber-50 dark:bg-amber-900/20 border border-amber-200 dark:border-amber-800 rounded-lg flex items-start gap-3">
          <span className="material-symbols-outlined text-amber-500 text-xl mt-0.5">warning</span>
          <div className="flex-1">
            <p className="text-sm text-amber-800 dark:text-amber-200">{speechError}</p>
          </div>
          {isRecording && (
            <button
              onClick={() => {
                setSpeechError(null);
                speechRef.current?.stop();
                startSpeechRecognition();
              }}
              className="px-3 py-1 rounded-lg text-xs font-medium bg-amber-100 dark:bg-amber-900/40 text-amber-700 dark:text-amber-300 hover:bg-amber-200 dark:hover:bg-amber-900/60 transition-colors shrink-0"
            >
              Restart
            </button>
          )}
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
      <main className="flex-1 flex flex-col px-6 pt-8 pb-32 max-w-2xl mx-auto">
        {/* Mic Selector — always visible, disabled during recording */}
        {!postRecordingStep && (
          <div className="flex justify-center">
            <MicSelector
              devices={devices}
              selectedDeviceId={selectedDeviceId}
              onSelect={selectDevice}
              disabled={isRecording}
              analyser={isRecording ? analyserNode : previewAnalyser}
            />
          </div>
        )}

        {/* STT Provider Selector */}
        {!postRecordingStep && (
          <div className="flex items-center justify-center gap-2 mt-4 mb-2">
            <span className="text-xs font-medium text-slate-500 dark:text-slate-400">STT 엔진:</span>
            <div className="flex rounded-lg overflow-hidden border border-slate-200 dark:border-slate-700">
              <button
                onClick={() => setSttProvider('transcribe')}
                disabled={isRecording}
                className={`px-3 py-1.5 text-xs font-medium transition-all ${
                  sttProvider === 'transcribe'
                    ? 'bg-primary text-white'
                    : 'bg-white dark:bg-slate-800 text-slate-600 dark:text-slate-400 hover:bg-slate-50 dark:hover:bg-slate-700'
                } disabled:opacity-50`}
              >
                <span className="flex items-center gap-1.5">
                  <span className="material-symbols-outlined text-xs">subtitles</span>
                  Transcribe
                </span>
              </button>
              <button
                onClick={() => setSttProvider('nova-sonic')}
                disabled={isRecording}
                className={`px-3 py-1.5 text-xs font-medium transition-all ${
                  sttProvider === 'nova-sonic'
                    ? 'bg-primary text-white'
                    : 'bg-white dark:bg-slate-800 text-slate-600 dark:text-slate-400 hover:bg-slate-50 dark:hover:bg-slate-700'
                } disabled:opacity-50`}
              >
                <span className="flex items-center gap-1.5">
                  <span className="material-symbols-outlined text-xs">graphic_eq</span>
                  Nova Sonic V2
                </span>
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
              />
            }
            summaryContent={
              <LiveSummary
                summary={liveSummary}
                isGenerating={isGeneratingSummary}
                wordCount={totalWordCount}
                lastSummaryWordCount={lastSummaryWordCountRef.current}
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

      {/* Desktop Q&A Side Panel — visible only during recording */}
      {isRecording && (
        <aside className="hidden lg:flex w-80 shrink-0 h-[calc(100vh-64px)] sticky top-[64px] pr-4 pt-8 pb-32">
          <LiveQAPanel transcriptContext={transcriptContext} onDetectedQuestionsChange={setDetectedCount} />
        </aside>
      )}
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
                  {postRecordingStep === 'uploading' && 'Uploading audio...'}
                  {postRecordingStep === 'creating' && 'Creating meeting...'}
                  {postRecordingStep === 'redirecting' && 'Opening meeting...'}
                </p>
                <div className="flex gap-1.5 shrink-0">
                  {(['uploading', 'creating', 'redirecting'] as const).map((step, i) => {
                    const currentIdx = ['uploading', 'creating', 'redirecting'].indexOf(postRecordingStep);
                    return (
                      <div
                        key={step}
                        className={`w-1.5 h-1.5 rounded-full transition-all duration-300 ${
                          i <= currentIdx ? 'bg-primary' : 'bg-slate-300 dark:bg-slate-600'
                        }`}
                      />
                    );
                  })}
                </div>
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
              <LiveQAPanel transcriptContext={transcriptContext} onDetectedQuestionsChange={setDetectedCount} />
            </div>
          </div>
        </div>
      )}

    </AppLayout>
  );
}
