'use client';

import { useCallback, useEffect, useRef, useState } from 'react';
import { SttManager, type TranscribeStreamingConfig } from '@/lib/sttManager';
import type { ExtraLiveLanguage, LiveLanguage, TranslationEntry } from '@/lib/liveTranslation';
import { getRuntimeConfig } from '@/lib/runtimeConfig';
import {
  INTERPRET_VERSION_SKEW_MESSAGE, isCommandNotFound, isTauri, onNativeListeningStopped, onNativePcmChunk,
  startNativeListening, stopNativeListening, type NativeSelectedInput,
} from '@/lib/tauri';
import { InterpretController, type InterpretState } from '@/lib/interpretController';

export type { InterpretPhase } from '@/lib/interpretController';

interface UseInterpretSessionOptions {
  targetLang: string;
  /** Translate partial captions too (opt-in, higher cost). */
  interimTranslation: boolean;
  /**
   * Every recognized segment, for the floating caption windows. The ID is
   * unique within one interpretation run, across caption reconnects. A
   * final segment also names its speaker when Transcribe told one apart.
   */
  onSegment?: (id: string, text: string, isFinal: boolean, languageCode?: string, speaker?: string) => void;
  /** Third recognition language chosen for the caption windows, read when a caption session starts. */
  getExtraLanguage?: () => ExtraLiveLanguage | undefined;
  /** The spoken languages to recognize (one or two); read when a caption session starts. */
  getSpeechLanguages?: () => LiveLanguage[] | undefined;
}

type SessionState = InterpretState<TranslationEntry, NativeSelectedInput>;
type Controller = InterpretController<TranscribeStreamingConfig, TranslationEntry, NativeSelectedInput>;

const IDLE_STATE: SessionState = {
  phase: 'idle', translations: [], error: null, notice: null, warnings: [], input: null, startedAt: null,
};

async function loadTranscribeConfig(): Promise<TranscribeStreamingConfig | null> {
  const cfg = await getRuntimeConfig();
  if (!cfg.cognito.identityPoolId || !cfg.cognito.userPoolId) return null;
  const config: TranscribeStreamingConfig = {
    region: cfg.cognito.region,
    identityPoolId: cfg.cognito.identityPoolId,
    userPoolId: cfg.cognito.userPoolId,
  };
  // Same custom-vocabulary lookup as a recording; captions work without it.
  try {
    const [{ dictionaryApi }, { liveVocabularyNamesFrom }] = await Promise.all([
      import('@/lib/api'), import('@/lib/liveVocabulary'),
    ]);
    const vocabularies = liveVocabularyNamesFrom((await dictionaryApi.get()).vocabularies);
    if (vocabularies) return { ...config, vocabularies };
  } catch {
    // Fall through to captions with no custom vocabulary.
  }
  return config;
}

/**
 * Live interpretation with no meeting (Mac app only). The Mac app captures
 * microphone plus system audio without writing a file (`start_listening`);
 * the PCM feeds Transcribe Streaming and each recognized segment goes to
 * the caption windows' engines (`onSegment`). Nothing is saved: no meeting, transcript, upload or
 * browser storage, and caption text is never logged here.
 *
 * The session logic lives in `lib/interpretController.ts`; this hook wires it
 * to the real capture and STT. Deliberately separate from
 * `useRecordingSession`, which carries the meeting, transcript and upload
 * flow this screen must never reach.
 */
export function useInterpretSession({
  targetLang, interimTranslation, onSegment, getExtraLanguage, getSpeechLanguages,
}: UseInterpretSessionOptions) {
  const [state, setState] = useState<SessionState>(IDLE_STATE);
  const [elapsedMs, setElapsedMs] = useState(0);
  const controllerRef = useRef<Controller | null>(null);
  const optionsRef = useRef({ targetLang, interimTranslation });
  const onSegmentRef = useRef(onSegment);
  const getExtraLanguageRef = useRef(getExtraLanguage);
  const getSpeechLanguagesRef = useRef(getSpeechLanguages);
  useEffect(() => {
    onSegmentRef.current = onSegment;
    getExtraLanguageRef.current = getExtraLanguage;
    getSpeechLanguagesRef.current = getSpeechLanguages;
  }, [onSegment, getExtraLanguage, getSpeechLanguages]);

  useEffect(() => {
    optionsRef.current.targetLang = targetLang;
    controllerRef.current?.setTargetLang(targetLang);
  }, [targetLang]);

  useEffect(() => {
    optionsRef.current.interimTranslation = interimTranslation;
    controllerRef.current?.setInterimTranslation(interimTranslation);
  }, [interimTranslation]);

  useEffect(() => {
    if (!isTauri()) return;
    let config: Promise<TranscribeStreamingConfig | null> | null = null;
    // Each caption session restarts its segment IDs; the prefix keeps them apart.
    let sttSession = 0;
    const controller: Controller = new InterpretController({
      loadConfig: () => (config ??= loadTranscribeConfig().catch(() => null)),
      startCapture: startNativeListening,
      stopCapture: stopNativeListening,
      createStt: (transcribeStreamingConfig, options, handlers) => {
        const session = ++sttSession;
        const manager = new SttManager({
          callbacks: {
            onTranscript: () => handlers.onTranscript(),
            onTranslation: handlers.onTranslation,
            onTranslationReset: handlers.onReset,
            onError: (code) => {
              if (code === 'transcribe-native-unavailable') handlers.onUnavailable();
            },
          },
          targetLang: options.targetLang,
          // The screen shows the caption windows' own translations (the
          // user's AWS profile); the server translation is not requested.
          translationEnabled: false,
          interimTranslation: options.interimTranslation,
          transcribeStreamingConfig,
          extraLanguage: getExtraLanguageRef.current?.(),
          nativeLanguages: getSpeechLanguagesRef.current?.(),
          speakerLabels: true,
          onSegment: (id, text, isFinal, languageCode, speaker) =>
            onSegmentRef.current?.(`${session}/${id}`, text, isFinal, languageCode, speaker),
        });
        return {
          start: () => manager.startNative('transcribe-streaming'),
          stop: () => manager.stop(),
          push: (chunk) => manager.pushNativeChunk(chunk),
          setTargetLang: (lang) => manager.updateTargetLang(lang),
          setInterimTranslation: (enabled) => manager.updateInterimTranslation(enabled),
        };
      },
      subscribe: ({ onChunk, onStopped }) => {
        const unsubscribe = [onNativePcmChunk(onChunk), onNativeListeningStopped(onStopped)];
        return () => unsubscribe.forEach((stop) => stop());
      },
      isCommandNotFound,
      versionSkewMessage: INTERPRET_VERSION_SKEW_MESSAGE,
    }, setState, { ...optionsRef.current });
    controllerRef.current = controller;
    void controller.init();
    return () => {
      controllerRef.current = null;
      void controller.dispose();
    };
  }, []);

  const { startedAt } = state;
  useEffect(() => {
    if (startedAt === null) return;
    const tick = () => setElapsedMs(Date.now() - startedAt);
    const first = setTimeout(tick, 0);
    const clock = setInterval(tick, 1000);
    return () => {
      clearTimeout(first);
      clearInterval(clock);
    };
  }, [startedAt]);

  const start = useCallback(async (inputDeviceUid?: string) => {
    await controllerRef.current?.start(inputDeviceUid);
  }, []);
  const stop = useCallback(async () => {
    await controllerRef.current?.stop();
  }, []);

  return {
    phase: state.phase,
    translations: state.translations,
    error: state.error,
    notice: state.notice,
    warnings: state.warnings,
    input: state.input,
    elapsedMs: startedAt === null ? 0 : elapsedMs,
    start,
    stop,
  };
}
