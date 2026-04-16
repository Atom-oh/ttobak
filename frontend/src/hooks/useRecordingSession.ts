'use client';

import { useState, useRef, useEffect, useCallback } from 'react';
import { SttManager, type LiveSttProvider } from '@/lib/sttManager';
import { countWords } from '@/lib/speechRecognition';

export interface TranscriptEntry {
  text: string;
  isFinal: boolean;
  timestamp: string;
}

interface TranslationEntry {
  original: string;
  translated: string;
  targetLang: string;
  timestamp: string;
}

interface InterimTranslation {
  original: string;
  translated: string;
  targetLang: string;
}

const speechErrorMessages: Record<string, string> = {
  'not-allowed': 'Microphone permission denied for speech recognition.',
  'network': 'Network error — speech recognition requires internet.',
  'service-not-allowed': 'Speech recognition service is not available.',
  'language-not-supported': 'Korean speech recognition is not supported in this browser.',
  'recognition-stalled': '음성 인식이 일시 중단되었습니다. 재시작 중...',
  'recognition-failed': '음성 인식이 중단되었습니다. 아래 버튼을 눌러 재시작해주세요.',
  'transcribe-auth-failed': 'AWS 인증 실패. Browser Speech로 전환합니다.',
  'transcribe-stream-error': 'Transcribe Streaming 오류. Browser Speech로 전환합니다.',
  'transcribe-no-stream': 'Transcribe Streaming 연결 실패. Browser Speech로 전환합니다.',
};

interface UseRecordingSessionOptions {
  targetLang: string;
  translationEnabled: boolean;
  /** Preferred live STT provider */
  liveSttProvider: LiveSttProvider;
  /** Called each time a final transcript arrives with updated word count and full text */
  onTranscriptUpdate?: (totalWordCount: number, allText: string) => void;
  /** Called when STT provider changes (e.g., fallback) */
  onProviderChange?: (provider: LiveSttProvider) => void;
}

// Transcribe Streaming config from env
const TRANSCRIBE_CONFIG = {
  region: process.env.NEXT_PUBLIC_AWS_REGION || 'ap-northeast-2',
  identityPoolId: process.env.NEXT_PUBLIC_COGNITO_IDENTITY_POOL_ID || '',
  userPoolId: process.env.NEXT_PUBLIC_COGNITO_USER_POOL_ID || '',
};

export function useRecordingSession({
  targetLang,
  translationEnabled,
  liveSttProvider,
  onTranscriptUpdate,
  onProviderChange,
}: UseRecordingSessionOptions) {
  const [isRecording, setIsRecording] = useState(false);
  const [isPaused, setIsPaused] = useState(false);
  const [transcripts, setTranscripts] = useState<TranscriptEntry[]>([]);
  const [currentInterim, setCurrentInterim] = useState('');
  const [totalWordCount, setTotalWordCount] = useState(0);
  const [speechError, setSpeechError] = useState<string | null>(null);
  const [activeProvider, setActiveProvider] = useState<LiveSttProvider>('web-speech');

  // Translation state
  const [translations, setTranslations] = useState<TranslationEntry[]>([]);
  const [currentInterimTranslation, setCurrentInterimTranslation] = useState<InterimTranslation | null>(null);

  const sttManagerRef = useRef<SttManager | null>(null);
  const targetLangRef = useRef(targetLang);
  const transcriptsRef = useRef(transcripts);
  const onTranscriptUpdateRef = useRef(onTranscriptUpdate);

  // Keep refs in sync
  useEffect(() => { targetLangRef.current = targetLang; }, [targetLang]);
  useEffect(() => { transcriptsRef.current = transcripts; }, [transcripts]);
  useEffect(() => { onTranscriptUpdateRef.current = onTranscriptUpdate; }, [onTranscriptUpdate]);

  // Propagate target language changes to STT manager
  useEffect(() => {
    sttManagerRef.current?.updateTargetLang(targetLang);
  }, [targetLang]);

  // Propagate translation toggle to STT manager
  useEffect(() => {
    sttManagerRef.current?.updateTranslationEnabled(translationEnabled);
  }, [translationEnabled]);

  const isSttPermanentlyFailed = speechError === speechErrorMessages['recognition-failed'];

  const handleRestartStt = useCallback(() => {
    setSpeechError(null);
    // Stop and restart the manager
    if (sttManagerRef.current) {
      sttManagerRef.current.stop();
      sttManagerRef.current = null;
    }
  }, []);

  const startSession = useCallback((previewCleanup: () => void, stream: MediaStream) => {
    previewCleanup();

    // Reset state
    setIsRecording(true);
    setIsPaused(false);
    setTranscripts([]);
    setCurrentInterim('');
    setTotalWordCount(0);
    setTranslations([]);
    setSpeechError(null);
    transcriptsRef.current = [];

    const handleTranscriptResult = (text: string, isFinal: boolean) => {
      if (isFinal) {
        const entry: TranscriptEntry = { text, isFinal: true, timestamp: new Date().toISOString() };
        setTranscripts((prev) => {
          const updated = [...prev, entry];
          transcriptsRef.current = updated;
          return updated;
        });
        setCurrentInterim('');

        const words = countWords(text);
        setTotalWordCount((prev) => {
          const newTotal = prev + words;
          const allText = [...transcriptsRef.current].map(t => t.text).join('\n');
          onTranscriptUpdateRef.current?.(newTotal, allText);
          return newTotal;
        });
      } else {
        setCurrentInterim(text);
      }
    };

    const hasTranscribeConfig = !!(TRANSCRIBE_CONFIG.identityPoolId && TRANSCRIBE_CONFIG.userPoolId);

    const manager = new SttManager({
      callbacks: {
        onTranscript: handleTranscriptResult,
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
        onError: (error) => {
          setSpeechError(speechErrorMessages[error] || error);
        },
      },
      targetLang: targetLangRef.current,
      translationEnabled,
      transcribeStreamingConfig: hasTranscribeConfig ? TRANSCRIBE_CONFIG : undefined,
      onProviderChange: (provider) => {
        setActiveProvider(provider);
        onProviderChange?.(provider);
      },
    });

    sttManagerRef.current = manager;

    // Choose provider: use transcribe-streaming only if configured
    const preferredProvider = liveSttProvider === 'transcribe-streaming' && hasTranscribeConfig
      ? 'transcribe-streaming'
      : 'web-speech';

    setActiveProvider(preferredProvider);
    manager.start(stream, preferredProvider, 'ko-KR');
  }, [translationEnabled, liveSttProvider, onProviderChange]);

  const pauseSession = useCallback(() => {
    setIsPaused(true);
    sttManagerRef.current?.pause();
  }, []);

  const resumeSession = useCallback(() => {
    setIsPaused(false);
    sttManagerRef.current?.resume();
  }, []);

  const stopSession = useCallback(() => {
    setIsRecording(false);
    setIsPaused(false);
    sttManagerRef.current?.stop();
    sttManagerRef.current = null;
  }, []);

  /** Combined transcripts + current interim for display */
  const displayTranscripts: TranscriptEntry[] = [
    ...transcripts,
    ...(currentInterim
      ? [{ text: currentInterim, isFinal: false, timestamp: new Date().toISOString() }]
      : []),
  ];

  /** Full transcript text including interim */
  const transcriptContext = [
    ...transcripts.map(t => t.text),
    ...(currentInterim ? [currentInterim] : []),
  ].join('\n');

  return {
    isRecording,
    isPaused,
    transcripts,
    transcriptsRef,
    currentInterim,
    totalWordCount,
    speechError,
    setSpeechError,
    isSttPermanentlyFailed,
    translations,
    currentInterimTranslation,
    displayTranscripts,
    transcriptContext,
    activeProvider,
    startSession,
    pauseSession,
    resumeSession,
    stopSession,
    handleRestartStt,
    speechErrorMessages,
  };
}
