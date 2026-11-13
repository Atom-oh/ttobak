'use client';

import { useCallback, useEffect, useRef, useState } from 'react';
import { SttManager, type TranscribeStreamingConfig } from '@/lib/sttManager';
import { MAX_TRANSLATION_ENTRIES, type TranslationEntry } from '@/lib/liveTranslation';
import { getRuntimeConfig } from '@/lib/runtimeConfig';
import {
  INTERPRET_VERSION_SKEW_MESSAGE, isCommandNotFound, isTauri, onNativeListeningStopped, onNativePcmChunk,
  startNativeListening, stopNativeListening, type NativeSelectedInput,
} from '@/lib/tauri';
import { IdleStop, INTERPRET_IDLE_STOP_MS, INTERPRET_MESSAGES, ReconnectBudget } from '@/lib/interpretSession';

export type InterpretPhase = 'idle' | 'starting' | 'running' | 'reconnecting' | 'stopping';

interface UseInterpretSessionOptions {
  targetLang: string;
  /** Translate partial captions too (opt-in, higher cost). */
  interimTranslation: boolean;
}

/** Keeps entries of successive STT sessions apart: each session restarts its ids and order. */
const SESSION_ORDER_SPAN = 1_000_000;

/**
 * Live interpretation with no meeting (Mac app only). The Mac app captures
 * microphone plus system audio without writing a file (`start_listening`);
 * the PCM feeds Transcribe Streaming and each caption is translated through
 * `/api/translate`. Nothing is saved: no meeting, transcript, upload or
 * browser storage, and caption text is never logged here.
 *
 * Deliberately separate from `useRecordingSession`, which carries the
 * meeting, transcript and upload flow this screen must never reach.
 */
export function useInterpretSession({ targetLang, interimTranslation }: UseInterpretSessionOptions) {
  const [phase, setPhase] = useState<InterpretPhase>('idle');
  const [translations, setTranslations] = useState<TranslationEntry[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [warnings, setWarnings] = useState<string[]>([]);
  const [elapsedMs, setElapsedMs] = useState(0);
  const [input, setInput] = useState<NativeSelectedInput | null>(null);

  const phaseRef = useRef<InterpretPhase>('idle');
  const managerRef = useRef<SttManager | null>(null);
  /** Bumped by every start and every end; a stale value marks a superseded run. */
  const runRef = useRef(0);
  const sessionNoRef = useRef(0);
  /** The Mac app is capturing for this screen. */
  const capturingRef = useRef(false);
  const startPromiseRef = useRef<Promise<void> | null>(null);
  const budgetRef = useRef(new ReconnectBudget());
  const idleRef = useRef<IdleStop | null>(null);
  const reconnectTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const clockRef = useRef<ReturnType<typeof setInterval> | null>(null);
  const unsubscribeRef = useRef<Array<() => void>>([]);
  const configRef = useRef<Promise<TranscribeStreamingConfig | null> | null>(null);
  const targetLangRef = useRef(targetLang);
  const interimRef = useRef(interimTranslation);

  const changePhase = useCallback((next: InterpretPhase) => {
    phaseRef.current = next;
    setPhase(next);
  }, []);

  useEffect(() => {
    targetLangRef.current = targetLang;
    managerRef.current?.updateTargetLang(targetLang);
  }, [targetLang]);

  useEffect(() => {
    interimRef.current = interimTranslation;
    managerRef.current?.updateInterimTranslation(interimTranslation);
  }, [interimTranslation]);

  const loadConfig = useCallback(() => {
    configRef.current ??= getRuntimeConfig()
      .then(async (cfg): Promise<TranscribeStreamingConfig | null> => {
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
      })
      .catch(() => null);
    return configRef.current;
  }, []);

  /** Stop everything this run owns. Safe in any phase and safe to repeat. */
  const end = useCallback(async (outcome: { error?: string; notice?: string }, stopCapture: boolean) => {
    runRef.current += 1;
    if (reconnectTimerRef.current) clearTimeout(reconnectTimerRef.current);
    reconnectTimerRef.current = null;
    if (clockRef.current) clearInterval(clockRef.current);
    clockRef.current = null;
    idleRef.current?.stop();
    idleRef.current = null;
    const manager = managerRef.current;
    managerRef.current = null;
    manager?.stop();
    unsubscribeRef.current.forEach((unsubscribe) => unsubscribe());
    unsubscribeRef.current = [];
    const wasCapturing = capturingRef.current;
    capturingRef.current = false;
    if (stopCapture && wasCapturing) {
      await stopNativeListening().catch(() => undefined);
    }
    if (outcome.error) setError(outcome.error);
    if (outcome.notice) setNotice(outcome.notice);
    changePhase('idle');
  }, [changePhase]);

  const connect = useCallback(async (run: number, config: TranscribeStreamingConfig) => {
    const sessionNo = ++sessionNoRef.current;
    let connected = false;
    let failed = false;
    let heard = false;
    // Every callback checks that its run and its manager are still current:
    // a superseded session must not add entries, reset the idle timer or
    // start another reconnect.
    const current = () => runRef.current === run && managerRef.current === manager;

    const fail = () => {
      if (!current()) return;
      managerRef.current = null;
      manager.stop();
      if (!connected && sessionNo === 1) {
        void end({ error: INTERPRET_MESSAGES.unavailable }, true);
        return;
      }
      const delay = budgetRef.current.next();
      if (delay === null) {
        void end({ error: INTERPRET_MESSAGES.reconnectFailed }, true);
        return;
      }
      changePhase('reconnecting');
      setNotice(INTERPRET_MESSAGES.reconnecting);
      reconnectTimerRef.current = setTimeout(() => {
        reconnectTimerRef.current = null;
        if (runRef.current === run) void connect(run, config);
      }, delay);
    };

    const manager: SttManager = new SttManager({
      callbacks: {
        onTranscript: () => {
          if (!current()) return;
          idleRef.current?.touch();
          if (!heard) {
            // A transcript proves the stream is alive again.
            heard = true;
            budgetRef.current.reset();
          }
        },
        onTranslation: (entry) => {
          if (!current()) return;
          const next: TranslationEntry = {
            ...entry,
            id: `${sessionNo}/${entry.id}`,
            order: sessionNo * SESSION_ORDER_SPAN + entry.order,
          };
          setTranslations((previous) => [...previous.filter((item) => item.id !== next.id), next]
            .sort((left, right) => left.order - right.order)
            .slice(-MAX_TRANSLATION_ENTRIES));
        },
        // Earlier text stays on screen across reconnects and after a stop.
        onTranslationReset: () => undefined,
        onError: (code) => {
          if (code !== 'transcribe-native-unavailable') return;
          failed = true;
          fail();
        },
      },
      targetLang: targetLangRef.current,
      translationEnabled: true,
      interimTranslation: interimRef.current,
      transcribeStreamingConfig: config,
    });
    managerRef.current = manager;

    await manager.startNative('transcribe-streaming');
    if (failed || !current()) return;
    connected = true;
    changePhase('running');
    setNotice(null);
  }, [changePhase, end]);

  const start = useCallback(async (inputDeviceUid?: string) => {
    if (phaseRef.current !== 'idle') return;
    const run = ++runRef.current;
    changePhase('starting');
    setError(null);
    setNotice(null);
    setWarnings([]);
    setTranslations([]);
    setElapsedMs(0);
    setInput(null);
    sessionNoRef.current = 0;
    budgetRef.current.reset();

    const starting = (async () => {
      const config = await loadConfig();
      if (runRef.current !== run) return;
      if (!config) {
        await end({ error: INTERPRET_MESSAGES.unavailable }, false);
        return;
      }
      let started;
      try {
        started = await startNativeListening(inputDeviceUid);
      } catch (err) {
        if (runRef.current !== run) return;
        const detail = err instanceof Error ? err.message : String(err);
        await end({
          error: isCommandNotFound(err) ? INTERPRET_VERSION_SKEW_MESSAGE : `통역을 시작하지 못했습니다: ${detail}`,
        }, false);
        return;
      }
      if (runRef.current !== run) {
        // Stopped or left while the Mac app was still starting the capture.
        await stopNativeListening().catch(() => undefined);
        return;
      }
      capturingRef.current = true;
      setWarnings(started.warnings ?? []);
      setInput(started.input ?? null);

      unsubscribeRef.current = [
        onNativePcmChunk((chunk) => managerRef.current?.pushNativeChunk(chunk)),
        onNativeListeningStopped((reason) => {
          if (runRef.current !== run) return;
          // The Mac app already stopped the capture.
          capturingRef.current = false;
          void end({
            notice: reason === 'system-sleep' ? INTERPRET_MESSAGES.sleepStopped : INTERPRET_MESSAGES.recordingStopped,
          }, false);
        }),
      ];
      const startedAt = Date.now();
      clockRef.current = setInterval(() => setElapsedMs(Date.now() - startedAt), 1000);
      idleRef.current = new IdleStop(INTERPRET_IDLE_STOP_MS, () => {
        if (runRef.current === run) void end({ notice: INTERPRET_MESSAGES.idleStopped }, true);
      });
      idleRef.current.start();

      await connect(run, config);
    })();
    startPromiseRef.current = starting;
    await starting;
  }, [changePhase, connect, end, loadConfig]);

  const stop = useCallback(async () => {
    if (phaseRef.current === 'idle' || phaseRef.current === 'stopping') return;
    changePhase('stopping');
    // Supersede the run first: a start still in flight then stops the
    // capture it was creating instead of leaving it running.
    runRef.current += 1;
    await startPromiseRef.current?.catch(() => undefined);
    await end({}, true);
  }, [changePhase, end]);

  useEffect(() => {
    if (!isTauri()) return;
    // A reload can leave the Mac app capturing for a page that no longer
    // exists. An old app without the command rejects; that is not an error.
    void stopNativeListening().catch(() => undefined);
    void loadConfig();
    return () => {
      const starting = startPromiseRef.current;
      void end({}, true).then(async () => {
        // A start that was in flight notices the superseded run and stops
        // its own capture; this second stop covers a capture it installed.
        await starting?.catch(() => undefined);
        await stopNativeListening().catch(() => undefined);
      });
    };
  }, [end, loadConfig]);

  return { phase, translations, error, notice, warnings, elapsedMs, input, start, stop };
}
