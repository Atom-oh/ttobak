'use client';

import { useCallback, useEffect, useRef, useState } from 'react';
import { meetingsApi } from '@/lib/api';
import type { ActionItem, ActionItemsAnalysis, ActionItemsResponse } from '@/types/meeting';

interface UseActionItemsOptions {
  meetingId: string;
  isDone: boolean;
  source: string;
  sourceRevision: number;
  items?: ActionItem[];
  analysis?: ActionItemsAnalysis;
}

/** Mount with a meeting key: detail refreshes must not replace newer API results. */
export function useActionItems({ meetingId, isDone, source, sourceRevision, items, analysis }: UseActionItemsOptions) {
  const [snapshot, setSnapshot] = useState(() => ({
    source, sourceRevision,
    result: { actionItems: items ?? [], analysis: analysis ?? { status: 'unknown' } } as ActionItemsResponse,
  }));
  // An old success is not evidence about a newly saved summary, even before
  // effects run or while its next status request is queued.
  const result = snapshot.source === source && snapshot.sourceRevision === sourceRevision
    ? snapshot.result
    : { ...snapshot.result, analysis: { status: 'unknown' } as ActionItemsAnalysis };
  const [pendingAction, setPendingAction] = useState<'retry' | 'save' | null>(null);
  const [pollError, setPollError] = useState<string | null>(null);
  const [mutationError, setMutationError] = useState<string | null>(null);
  const requestRef = useRef<AbortController | null>(null);
  const mutatingRef = useRef(false);
  const epochRef = useRef(0);
  const queuedRef = useRef(false);
  const aliveRef = useRef(true);
  const latestRefreshRef = useRef<() => Promise<void>>(async () => {});
  const isPending = result.analysis.status === 'queued' || result.analysis.status === 'running';

  const applyResponse = useCallback((next: ActionItemsResponse) => {
    // The server preserves prior items on failure. Its saved array remains
    // authoritative even if a newer summary makes a previous result stale.
    setSnapshot({ source, sourceRevision, result: next });
    setPollError(null);
  }, [source, sourceRevision]);

  useEffect(() => {
    aliveRef.current = true;
    return () => {
      aliveRef.current = false;
      queuedRef.current = false;
      requestRef.current?.abort();
      requestRef.current = null;
    };
  }, []);

  const refresh = useCallback(async () => {
    // A slow poll must not overlap another read or overwrite a checkbox save.
    if (requestRef.current || mutatingRef.current) {
      queuedRef.current = true;
      return;
    }
    queuedRef.current = false;
    const epoch = epochRef.current;
    const controller = new AbortController();
    requestRef.current = controller;
    try {
      const next = await meetingsApi.getActionItems(meetingId, { signal: controller.signal });
      if (!controller.signal.aborted && epoch === epochRef.current) applyResponse(next);
    } catch (error) {
      if (!controller.signal.aborted) {
        setPollError(`분석 상태를 불러오지 못했습니다. ${error instanceof Error ? error.message : '다시 확인해 주세요.'}`);
      }
    } finally {
      if (requestRef.current === controller) {
        requestRef.current = null;
        if (queuedRef.current && aliveRef.current) void latestRefreshRef.current();
      }
    }
  }, [meetingId, applyResponse]);

  useEffect(() => {
    latestRefreshRef.current = refresh;
  }, [refresh]);

  useEffect(() => {
    epochRef.current += 1;
    if (isDone) void refresh();
  }, [isDone, source, sourceRevision, analysis?.runId, analysis?.status, refresh]);

  useEffect(() => {
    if (!isDone || result.analysis.status !== 'unknown') return;
    // Summary status can become "done" just before analysis claims its run.
    // Observe that handoff, without polling legacy meetings indefinitely.
    const timer = setInterval(() => { void refresh(); }, 5000);
    const timeout = setTimeout(() => clearInterval(timer), 60000);
    return () => { clearInterval(timer); clearTimeout(timeout); };
  }, [isDone, result.analysis.status, refresh]);

  useEffect(() => {
    if (!isDone || !isPending || pendingAction) return;
    // Independent of the meeting's STT/summary polling, which stops at "done".
    const timer = setInterval(() => { void refresh(); }, 5000);
    return () => clearInterval(timer);
  }, [isDone, isPending, pendingAction, refresh]);

  const mutate = async (
    kind: 'retry' | 'save',
    operation: (signal: AbortSignal) => Promise<ActionItemsResponse>,
  ) => {
    if (mutatingRef.current) return;
    mutatingRef.current = true;
    const epoch = epochRef.current;
    // Abort synchronously, before awaiting the write, so an older GET is ignored.
    requestRef.current?.abort();
    const controller = new AbortController();
    requestRef.current = controller;
    setPendingAction(kind);
    setMutationError(null);
    let needsRefresh = false;
    try {
      const next = await operation(controller.signal);
      if (!controller.signal.aborted && epoch === epochRef.current) applyResponse(next);
    } catch (error) {
      if (!controller.signal.aborted) {
        const message = kind === 'retry' ? '분석 재시도에 실패했습니다.' : '완료 상태를 저장하지 못했습니다.';
        setMutationError(`${message} ${error instanceof Error ? error.message : '다시 시도해 주세요.'}`);
        needsRefresh = true;
      }
    } finally {
      if (requestRef.current === controller) requestRef.current = null;
      mutatingRef.current = false;
      if (!controller.signal.aborted) setPendingAction(null);
      // A failed response can follow a persisted failure state or an accepted
      // write whose response was lost. Reconcile without hiding the error.
      if ((needsRefresh || queuedRef.current) && !controller.signal.aborted && aliveRef.current) {
        void latestRefreshRef.current();
      }
    }
  };

  return {
    ...result,
    isPending,
    pendingAction,
    pollError,
    mutationError,
    refresh,
    retry: () => mutate('retry', (signal) => meetingsApi.retryActionItems(meetingId, { signal })),
    setCompleted: (itemId: string, completed: boolean) =>
      mutate('save', (signal) => meetingsApi.setActionItemCompleted(meetingId, itemId, completed, { signal })),
  };
}
