'use client';

import { useCallback, useEffect, useRef, useState } from 'react';
import { getReportState, isTauri, onReportFinished, onReportProgress } from '@/lib/tauri';
import type { ReportFinished, ReportStateView } from '@/lib/performanceReport';

const MAX_LINES = 200;

/**
 * Mac report generation state with live progress (ADR-023). `state` stays
 * null in a browser and in an app build without report commands. `onFinished`
 * runs after the state reloads for each `report://finished` event.
 */
export function useReport(onFinished?: (finished: ReportFinished) => void) {
  const [state, setState] = useState<ReportStateView | null>(null);
  const [progress, setProgress] = useState<string[]>([]);
  const [loadError, setLoadError] = useState<string | null>(null);
  // When this page first saw the current run; the runner does not report it.
  const [startedAt, setStartedAt] = useState<number | null>(null);
  const finishedRef = useRef(onFinished);
  useEffect(() => {
    finishedRef.current = onFinished;
  }, [onFinished]);

  const apply = useCallback((next: ReportStateView | null) => {
    setState(next);
    if (next) setProgress(Array.isArray(next.progress) ? next.progress.slice(-MAX_LINES) : []);
    setStartedAt(current => (next?.running ? current ?? Date.now() : null));
  }, []);

  const refresh = useCallback(async () => {
    if (!isTauri()) return null;
    try {
      const next = await getReportState();
      apply(next);
      setLoadError(null);
      return next;
    } catch (err) {
      setLoadError(err instanceof Error ? err.message : String(err));
      return null;
    }
  }, [apply]);

  useEffect(() => {
    if (!isTauri()) return;
    let active = true;
    void getReportState()
      .then((next) => { if (active) apply(next); })
      .catch((err) => { if (active) setLoadError(err instanceof Error ? err.message : String(err)); });
    const stopProgress = onReportProgress((line) => {
      setProgress((lines) => [...lines, line].slice(-MAX_LINES));
    });
    const stopFinished = onReportFinished((finished) => {
      void refresh().then(() => {
        if (active) finishedRef.current?.(finished);
      });
    });
    return () => {
      active = false;
      stopProgress();
      stopFinished();
    };
  }, [apply, refresh]);

  /** Marks a run as started locally (the runner clears its progress for it). */
  const markStarted = useCallback(() => {
    setProgress([]);
    setStartedAt(Date.now());
    setState((current) => (current ? { ...current, running: true } : current));
  }, []);

  return { state, progress, loadError, startedAt, refresh, markStarted };
}
