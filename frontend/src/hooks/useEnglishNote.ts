'use client';

import { useCallback, useEffect, useRef, useState } from 'react';
import { ApiError, meetingsApi } from '@/lib/api';
import { shouldLoadEnglishNote } from '@/lib/englishNote';
import type { EnglishNote } from '@/types/meeting';

const requestErrors: Record<string, string> = {
  MEETING_BUSY: '다른 작업이 진행 중입니다. 잠시 후 다시 시도해 주세요.',
  SOURCE_NOT_READY: '첨부 문서의 텍스트 추출을 먼저 완료해 주세요.',
  NO_SUMMARY_SOURCE: '영문 회의록을 만들 저장 자료가 없습니다.',
  SHARING_REGEN_REQUIRED: '회의록을 먼저 다시 생성해 주세요.',
  OUTPUT_TOO_LARGE: '생성된 영문 회의록이 저장 한도를 초과했습니다.',
};
export const englishFailures: Record<string, string> = {
  ...requestErrors,
  PUBLISH_FAILED: '영문 회의록 작업을 전달하지 못했습니다.',
  INTERRUPTED: '영문 회의록 작업이 완료되기 전에 중단되었습니다.',
  SOURCE_CHANGED: '저장된 자료가 변경되어 결과를 적용하지 않았습니다.',
  ACCESS_REVOKED: '요청자의 편집 권한이 변경되어 결과를 적용하지 않았습니다.',
  TIMEOUT: '영문 회의록 처리 시간이 초과되었습니다.',
};

/**
 * Reads the English note state on every visit (a run may be pending or failed
 * with no stored note) and polls while a run is pending with the re-summary interval and bound.
 */
export function useEnglishNote({ meetingId, enabled }: { meetingId: string; enabled: boolean }) {
  const [state, setState] = useState<{ meetingId: string; note: EnglishNote | null }>({ meetingId, note: null });
  const [error, setError] = useState<string | null>(null);
  const [requesting, setRequesting] = useState(false);
  const requestRef = useRef<AbortController | null>(null);
  const mutationRef = useRef<AbortController | null>(null);
  const alive = useRef(true);
  const note = state.meetingId === meetingId ? state.note : null;
  const pending = note?.status === 'queued' || note?.status === 'running';
  const shouldLoad = shouldLoadEnglishNote(enabled);
  useEffect(() => {
    alive.current = true;
    return () => { alive.current = false; requestRef.current?.abort(); mutationRef.current?.abort(); };
  }, [meetingId]);
  const refresh = useCallback(async () => {
    if (!shouldLoad || mutationRef.current || (requestRef.current && !requestRef.current.signal.aborted)) return;
    const controller = new AbortController(); requestRef.current = controller;
    try {
      const next = await meetingsApi.getEnglishNote(meetingId, { signal: controller.signal });
      if (!controller.signal.aborted && alive.current) { setState({ meetingId, note: next }); setError(null); }
    } catch {
      if (!controller.signal.aborted && alive.current) setError('영문 회의록 상태를 확인하지 못했습니다.');
    } finally { if (requestRef.current === controller) requestRef.current = null; }
  }, [shouldLoad, meetingId]);
  useEffect(() => { void refresh(); return () => requestRef.current?.abort(); }, [refresh]);
  useEffect(() => {
    if (!pending || requesting) return;
    let checks = 0;
    const timer = setInterval(() => {
      if (++checks > 90) { clearInterval(timer); setError('자동 확인을 마쳤습니다. 상태를 다시 확인해 주세요.'); return; }
      void refresh();
    }, 10000);
    return () => clearInterval(timer);
  }, [pending, requesting, refresh]);

  const request = async () => {
    if (!enabled || pending || mutationRef.current) return;
    requestRef.current?.abort();
    const controller = new AbortController(); mutationRef.current = controller; setRequesting(true); setError(null);
    let reconcile = false;
    try {
      const next = await meetingsApi.requestEnglishNote(meetingId, { signal: controller.signal });
      if (!controller.signal.aborted && alive.current) {
        // The 202 carries status only; keep any earlier content visible until the run publishes.
        setState((current) => ({ meetingId, note: { ...next, content: next.content ?? (current.meetingId === meetingId ? current.note?.content : undefined) } }));
      }
    } catch (cause) {
      if (!controller.signal.aborted && alive.current) {
        setError(cause instanceof ApiError ? requestErrors[cause.code] ?? '영문 회의록을 요청하지 못했습니다.' : '영문 회의록을 요청하지 못했습니다.');
        reconcile = cause instanceof ApiError && cause.code === 'MEETING_BUSY';
      }
    } finally {
      if (mutationRef.current === controller) mutationRef.current = null;
      if (!controller.signal.aborted && alive.current) { setRequesting(false); if (reconcile) void refresh(); }
    }
  };
  return { note, pending, requesting, error, request, refresh };
}
