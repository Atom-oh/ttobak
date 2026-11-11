'use client';

import { useCallback, useEffect, useRef, useState } from 'react';
import { getIntakeState, isTauri, onIntakeFinished, onIntakeProgress } from '@/lib/tauri';
import type { IntakeFinished, IntakeStateView } from '@/lib/intake';

const MAX_LINES = 200;

/**
 * Mac local intake state with live progress (ADR-003, local intake). `state` stays null in
 * a browser and in an app build without intake. `onFinished` runs after the
 * state reloads for each `intake://finished` event.
 */
export function useIntake(onFinished?: (finished: IntakeFinished) => void) {
  const [state, setState] = useState<IntakeStateView | null>(null);
  const [progress, setProgress] = useState<string[]>([]);
  const [loadError, setLoadError] = useState<string | null>(null);
  const finishedRef = useRef(onFinished);
  useEffect(() => {
    finishedRef.current = onFinished;
  }, [onFinished]);

  const refresh = useCallback(async () => {
    if (!isTauri()) return null;
    try {
      const next = await getIntakeState();
      setState(next);
      if (next) setProgress(next.progress.slice(-MAX_LINES));
      setLoadError(null);
      return next;
    } catch (err) {
      setLoadError(err instanceof Error ? err.message : String(err));
      return null;
    }
  }, []);

  useEffect(() => {
    if (!isTauri()) return;
    let active = true;
    void getIntakeState()
      .then((next) => {
        if (!active) return;
        setState(next);
        if (next) setProgress(next.progress.slice(-MAX_LINES));
      })
      .catch((err) => {
        if (active) setLoadError(err instanceof Error ? err.message : String(err));
      });
    const stopProgress = onIntakeProgress((line) => {
      setProgress((lines) => [...lines, line].slice(-MAX_LINES));
    });
    const stopFinished = onIntakeFinished((finished) => {
      void refresh().then(() => {
        if (active) finishedRef.current?.(finished);
      });
    });
    return () => {
      active = false;
      stopProgress();
      stopFinished();
    };
  }, [refresh]);

  /** Marks a run as started locally (the runner clears its progress for it). */
  const markStarted = useCallback((action: 'run' | 'setup' | 'login') => {
    setProgress([]);
    setState((current) => (current ? { ...current, running: action } : current));
  }, []);

  return { state, setState, progress, loadError, refresh, markStarted };
}
