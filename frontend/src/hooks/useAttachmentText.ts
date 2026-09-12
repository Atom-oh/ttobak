'use client';

import { useCallback, useEffect, useRef, useState } from 'react';
import { ApiError, attachmentTextApi } from '@/lib/api';
import { unknownExtraction } from '@/lib/attachmentText';
import type { AttachmentTextStatus } from '@/types/meeting';

/** Document-local state: responses never replace meeting/editor content. */
export function useAttachmentText(
  meetingId: string,
  attachmentId: string,
  initial: AttachmentTextStatus | undefined,
  canRetry: boolean,
  summaryRevision: number,
) {
  const seedKey = JSON.stringify(initial ?? null);
  const initialState = initial?.status;
  const [snapshot, setSnapshot] = useState(() => ({ seedKey, status: initial ?? unknownExtraction }));
  // A detail refresh can announce a new run before its status request finishes.
  // Older detail snapshots cannot replace a more recent polling/retry response.
  const initialTime = Date.parse(initial?.updatedAt ?? '') || 0;
  const localTime = Date.parse(snapshot.status.updatedAt ?? '') || 0;
  const status = snapshot.seedKey !== seedKey && initial && initialTime > localTime ? initial : snapshot.status;
  const [retrying, setRetrying] = useState(false);
  const [pollError, setPollError] = useState<string | null>(null);
  const [mutationError, setMutationError] = useState<string | null>(null);
  const [accessDenied, setAccessDenied] = useState(false);
  const requestRef = useRef<AbortController | null>(null);
  const mutationRef = useRef<AbortController | null>(null);
  const epochRef = useRef(0);
  const aliveRef = useRef(true);
  const latestRefresh = useRef<() => Promise<void>>(async () => {});
  const isPending = status.status === 'queued' || status.status === 'running';

  useEffect(() => {
    aliveRef.current = true;
    return () => {
      aliveRef.current = false;
      requestRef.current?.abort();
      mutationRef.current?.abort();
    };
  }, [meetingId, attachmentId]);

  const apply = useCallback((next: AttachmentTextStatus) => {
    setSnapshot({ seedKey, status: next });
    setPollError(null);
    setAccessDenied(false);
  }, [seedKey]);

  const refresh = useCallback(async () => {
    if (mutationRef.current || (requestRef.current && !requestRef.current.signal.aborted)) return;
    const controller = new AbortController();
    requestRef.current = controller;
    const epoch = epochRef.current;
    try {
      const next = await attachmentTextApi.status(meetingId, attachmentId, { signal: controller.signal });
      if (!controller.signal.aborted && aliveRef.current && epoch === epochRef.current) apply(next);
    } catch (error) {
      if (!controller.signal.aborted && aliveRef.current && epoch === epochRef.current) {
        setPollError(error instanceof Error ? error.message : '추출 상태를 확인하지 못했습니다.');
        setAccessDenied(error instanceof ApiError && [401, 403, 404].includes(error.status));
      }
    } finally {
      if (requestRef.current === controller) requestRef.current = null;
    }
  }, [meetingId, attachmentId, apply]);

  useEffect(() => { latestRefresh.current = refresh; }, [refresh]);

  useEffect(() => {
    if (!initialState || initialState === 'unknown' || initialState === 'queued' || initialState === 'running' || summaryRevision > 0) {
      void refresh();
    }
    return () => requestRef.current?.abort();
  }, [initialState, seedKey, summaryRevision, refresh]); // primitives keep editor renders out of this effect

  useEffect(() => {
    if (!isPending || retrying || accessDenied) return;
    const timer = setInterval(() => { void refresh(); }, 5000);
    return () => clearInterval(timer);
  }, [isPending, retrying, accessDenied, refresh]);

  const retry = async () => {
    if (!canRetry || mutationRef.current || isPending) return;
    requestRef.current?.abort();
    const epoch = ++epochRef.current;
    const controller = new AbortController();
    mutationRef.current = controller;
    setRetrying(true);
    setMutationError(null);
    let reconcile = false;
    try {
      const next = await attachmentTextApi.retry(meetingId, attachmentId, { signal: controller.signal });
      if (!controller.signal.aborted && aliveRef.current && epoch === epochRef.current) apply(next);
    } catch (error) {
      if (!controller.signal.aborted && aliveRef.current && epoch === epochRef.current) {
        setMutationError(error instanceof Error ? error.message : '다시 추출하지 못했습니다.');
        reconcile = true; // Delivery failure may already be stored on the server.
      }
    } finally {
      if (mutationRef.current === controller) mutationRef.current = null;
      if (!controller.signal.aborted && aliveRef.current) {
        setRetrying(false);
        if (reconcile) void latestRefresh.current();
      }
    }
  };

  return { status, isPending, retrying, pollError, mutationError, accessDenied, refresh, retry, apply };
}
