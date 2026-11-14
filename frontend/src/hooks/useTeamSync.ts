'use client';

import { useCallback, useEffect, useRef, useState } from 'react';
import {
  hasTeamSyncCommand, onTeamSyncFinished, onTeamSyncProgress,
  teamSyncCancel, teamSyncGetState, teamSyncRun,
} from '@/lib/tauri';
import {
  finishedFailureCode, isApplyResult, isCancelled, isPreviewResult, shouldRefetchAfterFinish,
  type TeamSyncApplyResult, type TeamSyncFinished, type TeamSyncPhase, type TeamSyncPreview,
} from '@/lib/teamSync';

const MAX_LINES = 200;

/**
 * Admin team sync state (Mac app only). `available` is true when the app has
 * the team_sync commands. `onApplied` runs whenever an apply run finishes, however it ended.
 */
export function useTeamSync(onApplied?: () => void) {
  const [available, setAvailable] = useState(false);
  const [phase, setPhase] = useState<TeamSyncPhase>('idle');
  const [preview, setPreview] = useState<TeamSyncPreview | null>(null);
  const [result, setResult] = useState<TeamSyncApplyResult | null>(null);
  const [errorCode, setErrorCode] = useState<string | null>(null);
  const [progress, setProgress] = useState<string[]>([]);
  const [applyFailed, setApplyFailed] = useState(false);
  const appliedRef = useRef(onApplied);
  useEffect(() => {
    appliedRef.current = onApplied;
  }, [onApplied]);

  const handleFinished = useCallback((finished: TeamSyncFinished) => {
    if (shouldRefetchAfterFinish(finished.mode)) appliedRef.current?.();
    const failure = finishedFailureCode(finished);
    if (failure) {
      if (finished.mode === 'preview' && isCancelled(failure)) {
        setErrorCode(null);
        setPhase('idle');
        return;
      }
      setErrorCode(failure);
      setApplyFailed(finished.mode === 'apply');
      setPhase('error');
      return;
    }
    setApplyFailed(false);
    setErrorCode(null);
    if (finished.mode === 'preview' && isPreviewResult(finished.result)) {
      setPreview(finished.result);
      setPhase('preview');
    } else if (finished.mode === 'apply' && isApplyResult(finished.result)) {
      setResult(finished.result);
      setPhase('done');
    } else {
      setErrorCode('UNKNOWN');
      setPhase('error');
    }
  }, []);

  useEffect(() => {
    let active = true;
    let stops: Array<() => void> = [];
    void hasTeamSyncCommand().then(async (ok) => {
      if (!active || !ok) return;
      setAvailable(true);
      stops = [
        onTeamSyncProgress((line) => setProgress((lines) => [...lines, line].slice(-MAX_LINES))),
        onTeamSyncFinished((finished) => {
          if (active) handleFinished(finished);
        }),
      ];
      try {
        const state = await teamSyncGetState();
        if (!active) return;
        setProgress(Array.isArray(state.progress) ? state.progress.slice(-MAX_LINES) : []);
        if (state.running) setPhase('previewing');
        else if (state.lastResult) handleFinished(state.lastResult);
      } catch {
        // The finished event still updates the state.
      }
      if (!active) stops.forEach((stop) => stop());
    });
    return () => {
      active = false;
      stops.forEach((stop) => stop());
    };
  }, [handleFinished]);

  const start = useCallback(async (mode: 'preview' | 'apply', previewId?: string) => {
    setErrorCode(null);
    setApplyFailed(false);
    setProgress([]);
    if (mode === 'preview') {
      setPreview(null);
      setResult(null);
    }
    setPhase(mode === 'preview' ? 'previewing' : 'applying');
    try {
      await teamSyncRun(mode, previewId);
    } catch (err) {
      setErrorCode(typeof err === 'string' ? err : err instanceof Error ? err.message : 'UNKNOWN');
      setPhase('error');
    }
  }, []);

  const runPreview = useCallback(() => start('preview'), [start]);
  const runApply = useCallback(() => {
    if (preview) void start('apply', preview.previewId);
  }, [preview, start]);
  const cancel = useCallback(() => { void teamSyncCancel().catch(() => undefined); }, []);
  const dismiss = useCallback(() => {
    setPhase('idle');
    setPreview(null);
    setResult(null);
    setErrorCode(null);
    setApplyFailed(false);
  }, []);

  return { available, phase, preview, result, errorCode, applyFailed, progress, runPreview, runApply, cancel, dismiss };
}
