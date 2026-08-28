'use client';

import { useState, useRef, useEffect, useCallback } from 'react';
import { SttManager, type LiveSttProvider, type TranscribeStreamingConfig } from '@/lib/sttManager';
import { countWords } from '@/lib/speechRecognition';
import { getRuntimeConfig } from '@/lib/runtimeConfig';

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
  // System Audio mode has no microphone, so there is no Web Speech fallback
  // here — unlike the other transcribe-* errors above, this one can't
  // "switch to" anything.
  'transcribe-native-unavailable': '실시간 자막을 사용할 수 없습니다 (AWS 인증/연결 필요). 녹음은 계속되며 종료 후 자동으로 전사됩니다.',
  // Web Speech's own mic capture can end the recording's mic track on
  // iOS/Android (see SttManager.fallbackToWebSpeech), so it's never used
  // as a fallback on mobile while a mic/tab stream is recording. Recording
  // itself is unaffected — only live captions stop. Deliberately does NOT
  // promise an automatic recovery -- SttManager.retryWithConfig's
  // promotion only fires on a genuinely late config arrival, not on a
  // stalled AudioContext that's stuck for a gesture-policy reason nothing
  // automatic can work around. The retry button (wired to
  // manualStallRecovery via canRetryLiveCaptions below) is the actual path
  // back.
  'web-speech-mobile-unavailable': '이 기기에서는 브라우저 음성 인식을 실시간 자막에 사용할 수 없습니다. 녹음은 계속되며, 아래 버튼으로 자막 연결을 다시 시도할 수 있습니다.',
  // Fired immediately on the FIRST stall detection (SttManagerConfig's
  // onReconnecting), before the automatic one-shot reconnect even runs --
  // see that callback's doc comment. Not a terminal error: the automatic
  // attempt frequently succeeds on its own, this just stops the wait from
  // being completely silent.
  'transcribe-stream-reconnecting': '실시간 자막 연결이 불안정합니다. 자동으로 재연결을 시도합니다 — 바로 다시 연결하려면 아래 버튼을 누르세요.',
};

// speechError values that manualStallRecovery's retry button can actually
// do something about -- scoped to the stall/mobile-freeze scenario this
// exists for, not every error that happens to mention Transcribe Streaming
// (transcribe-native-unavailable has no fallback to retry into; the
// desktop-only recognition-failed already has its own
// handleRestartStt/isSttPermanentlyFailed path).
const RETRYABLE_LIVE_CAPTION_ERRORS = new Set([
  'transcribe-stream-reconnecting',
  'web-speech-mobile-unavailable',
]);

interface UseRecordingSessionOptions {
  targetLang: string;
  translationEnabled: boolean;
  /** Preferred live STT provider */
  liveSttProvider: LiveSttProvider;
  /** Called each time a final transcript arrives with updated word count and full text */
  onTranscriptUpdate?: (totalWordCount: number, allText: string) => void;
}

export function useRecordingSession({
  targetLang,
  translationEnabled,
  liveSttProvider,
  onTranscriptUpdate,
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
  const transcribeConfigRef = useRef<TranscribeStreamingConfig | null>(null);
  // Read inside the config-load effect below, which has an empty dep array
  // (it must only run once, not re-fetch on every liveSttProvider change) --
  // a plain closure over the `liveSttProvider` param would freeze at
  // whatever it was on mount.
  const liveSttProviderRef = useRef(liveSttProvider);
  useEffect(() => { liveSttProviderRef.current = liveSttProvider; }, [liveSttProvider]);

  // Load runtime Cognito config once (fetched from /config.json at startup)
  useEffect(() => {
    let cancelled = false;
    getRuntimeConfig().then(async (cfg) => {
      if (cancelled) return;
      if (cfg.cognito.identityPoolId && cfg.cognito.userPoolId) {
        let vocabularyName: string | undefined;
        try {
          const { dictionaryApi } = await import('@/lib/api');
          const dict = await dictionaryApi.get();
          if (dict.status === 'READY' && dict.vocabularyName) {
            vocabularyName = dict.vocabularyName;
          }
        } catch {
          // Dictionary not available — proceed without custom vocabulary
        }
        const config: TranscribeStreamingConfig = {
          region: cfg.cognito.region,
          identityPoolId: cfg.cognito.identityPoolId,
          userPoolId: cfg.cognito.userPoolId,
          vocabularyName,
        };
        transcribeConfigRef.current = config;
        // A recording that started (createManager, below) before this fetch
        // resolved was permanently forced onto Web Speech -- or, on mobile,
        // blocked from live captions entirely (SttManager.fallbackToWebSpeech)
        // -- for the rest of that recording, with no way to ever pick
        // Transcribe Streaming back up. Promote it now that a config is
        // actually available, but only if the live preference genuinely
        // wants Transcribe Streaming -- never override a user's explicit
        // 'web-speech' choice.
        if (liveSttProviderRef.current === 'transcribe-streaming') {
          sttManagerRef.current?.retryWithConfig(config);
        }
      }
    });
    return () => { cancelled = true; };
  }, []);

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

  // Whether the CURRENT speechError is one manualStallRecovery can actually
  // fix -- gates the retry button separately from isSttPermanentlyFailed's
  // own (desktop-only) button.
  const canRetryLiveCaptions = speechError !== null &&
    Object.entries(speechErrorMessages).some(
      ([code, message]) => message === speechError && RETRYABLE_LIVE_CAPTION_ERRORS.has(code),
    );

  /**
   * User-initiated recovery for a stuck live-caption pipeline (stalled
   * AudioContext, or the mobile-unavailable dead end) -- MUST be called
   * synchronously from the retry button's onClick, before any `await`.
   * See SttManager.manualStallRecovery's doc comment for why: everything
   * automatic already tried to fix this from an event listener, not a
   * real click, and that's the actual reason it's stuck.
   */
  const retryLiveCaptions = useCallback(() => {
    sttManagerRef.current?.manualStallRecovery();
  }, []);

  const handleRestartStt = useCallback(() => {
    setSpeechError(null);
    // Stop and restart the manager
    if (sttManagerRef.current) {
      sttManagerRef.current.stop();
      sttManagerRef.current = null;
    }
  }, []);

  /** Shared setup for both `startSession` (browser mic/tab, has a
   * MediaStream) and `startNativeSession` (Tauri System Audio, no
   * MediaStream — see below): resets transcript/translation state and
   * constructs the SttManager with the same callbacks either way. Callers
   * are responsible for calling `manager.start(stream, ...)` or
   * `manager.startNative(...)` themselves. */
  const createManager = useCallback(() => {
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

    const transcribeConfig = transcribeConfigRef.current;
    const hasTranscribeConfig = !!transcribeConfig;

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
      transcribeStreamingConfig: transcribeConfig ?? undefined,
      onReconnecting: () => {
        setSpeechError(speechErrorMessages['transcribe-stream-reconnecting']);
      },
      onProviderChange: (provider) => {
        setActiveProvider(provider);
        // Both the mobile-unavailable and reconnecting banners describe a
        // temporary state that Transcribe Streaming actually coming back
        // resolves -- clear either one instead of leaving it to sit
        // alongside captions that are flowing again.
        setSpeechError((prev) =>
          provider === 'transcribe-streaming' &&
          (prev === speechErrorMessages['web-speech-mobile-unavailable'] ||
            prev === speechErrorMessages['transcribe-stream-reconnecting'])
            ? null
            : prev,
        );
      },
    });

    // Stop any previous manager before overwriting the ref — a restart
    // after a failed native start would otherwise leak its Transcribe
    // WebSocket (nothing else holds a reference to stop it).
    sttManagerRef.current?.stop();
    sttManagerRef.current = manager;

    // What's actually about to run, for the UI's immediate best guess
    // before SttManager settles (its own onProviderChange callback fires
    // once it actually decides/falls back).
    const initialActiveProvider: LiveSttProvider = liveSttProvider === 'transcribe-streaming' && hasTranscribeConfig
      ? 'transcribe-streaming'
      : 'web-speech';
    setActiveProvider(initialActiveProvider);

    // What to hand to SttManager.start() as the preferred provider: the
    // user's actual selection, NOT the config-availability-downgraded
    // value above. start() already handles a missing config internally
    // (falls back to Web Speech for now) while still recording the
    // original ask as `preferredProvider` -- downgrading it here to
    // 'web-speech' just because the config race hasn't resolved YET would
    // permanently lock retryWithConfig/resume()'s promotion gate closed
    // for the rest of the recording, since they check `preferredProvider`
    // precisely to avoid overriding an explicit 'web-speech' choice. If
    // this were downgraded, there'd be no way to tell "config wasn't
    // ready yet" apart from "user explicitly chose Web Speech" once the
    // config does arrive (see ADR-030).
    return { manager, preferredProvider: liveSttProvider };
  }, [translationEnabled, liveSttProvider]);

  const startSession = useCallback((previewCleanup: () => void, stream: MediaStream) => {
    previewCleanup();
    const { manager, preferredProvider } = createManager();
    manager.start(stream, preferredProvider);
  }, [createManager]);

  /**
   * Start live captions with no MediaStream — Tauri System Audio mode.
   * Capture happens in Rust via ScreenCaptureKit; audio arrives via
   * `pushNativePcmChunk` instead of an AudioWorklet. There is no Web
   * Speech fallback in this mode (it requires a microphone that doesn't
   * exist here) — see `SttManager.startNative`.
   */
  const startNativeSession = useCallback(() => {
    const { manager } = createManager();
    // Ignore createManager's preferredProvider here: it respects the
    // browser-mode provider toggle, which defaults to 'web-speech' -- but
    // native mode has no Web Speech fallback (no microphone MediaStream),
    // so gating on the toggle means default users NEVER get System Audio
    // captions. Use Transcribe Streaming whenever it's configured; only an
    // actually-missing config surfaces `transcribe-native-unavailable`.
    const nativeProvider: LiveSttProvider = transcribeConfigRef.current
      ? 'transcribe-streaming'
      : 'web-speech';
    setActiveProvider(nativeProvider);
    manager.startNative(nativeProvider);
  }, [createManager]);

  /** Feed one PCM chunk (from `lib/tauri.ts`'s `onNativePcmChunk`) into the
   * active native session. No-op if `startNativeSession` wasn't called. */
  const pushNativePcmChunk = useCallback((chunk: Uint8Array) => {
    sttManagerRef.current?.pushNativeChunk(chunk);
  }, []);

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
    canRetryLiveCaptions,
    retryLiveCaptions,
    translations,
    currentInterimTranslation,
    displayTranscripts,
    transcriptContext,
    activeProvider,
    startSession,
    startNativeSession,
    pushNativePcmChunk,
    pauseSession,
    resumeSession,
    stopSession,
    handleRestartStt,
    speechErrorMessages,
  };
}
