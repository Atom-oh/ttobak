'use client';

import { useCallback, useEffect, useRef, useState } from 'react';
import { ApiError, meetingsApi } from '@/lib/api';
import type { ResummaryStatus } from '@/types/meeting';

export async function loadVerifiedSummary(meetingId: string, expected: ResummaryStatus, signal: AbortSignal) {
  if (expected.status !== 'succeeded' || !expected.runId || !/^[a-f0-9]{64}$/.test(expected.resultHash ?? '')) {
    throw new Error('완료된 요약 결과가 없습니다.');
  }
  const chunks: string[] = [];
  const cursors = new Set<string>();
  let cursor: string | undefined, revision: string | undefined, offset = 0, bytes = 0, total: number | undefined;
  for (let i = 0; i < 512; i++) {
    const result = await meetingsApi.readSummary(meetingId, cursor, { signal });
    const page = result.page;
    const length = typeof result.content === 'string' ? [...result.content].length : -1;
    if (result.meetingId !== meetingId || result.source !== 'summary' || !page ||
      page.unit !== 'unicode_code_points' || page.startOffset !== offset || page.endOffset !== offset + length ||
      !Number.isInteger(page.totalCodePoints) || page.totalCodePoints < page.endOffset || page.totalCodePoints > 300 * 1024 ||
      (total !== undefined && total !== page.totalCodePoints) || (revision !== undefined && revision !== result.revision) ||
      page.complete !== !page.nextCursor || (length <= 0 && !page.complete) ||
      new TextEncoder().encode(JSON.stringify(result)).length > 14000) {
      throw new Error('요약 읽기 응답이 변경되었거나 올바르지 않습니다.');
    }
    revision = result.revision; total = page.totalCodePoints; offset = page.endOffset;
    bytes += new TextEncoder().encode(result.content).length;
    if (bytes > 300 * 1024) throw new Error('요약 결과가 읽기 한도를 초과했습니다.');
    chunks.push(result.content);
    if (page.complete) {
      if (offset !== total) throw new Error('요약을 끝까지 읽지 못했습니다.');
      const content = chunks.join('');
      const digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(content));
      const hash = Array.from(new Uint8Array(digest), (value) => value.toString(16).padStart(2, '0')).join('');
      const latest = await meetingsApi.getResummary(meetingId, { signal });
      if (hash !== expected.resultHash || latest.runId !== expected.runId || latest.status !== 'succeeded' || latest.resultHash !== hash) {
        throw new Error('저장된 요약이나 실행이 변경되었습니다. 상태를 다시 확인해 주세요.');
      }
      return content;
    }
    cursor = page.nextCursor ?? undefined;
    if (!cursor || cursors.has(cursor)) throw new Error('요약 읽기 커서가 올바르지 않습니다.');
    cursors.add(cursor);
  }
  throw new Error('요약 읽기 구간 수가 한도를 초과했습니다.');
}

export function useResummary({
  meetingId, enabled, canEdit, isDirty, onLoaded,
}: {
  meetingId: string;
  enabled: boolean;
  canEdit: boolean;
  isDirty: () => boolean;
  onLoaded: (content: string) => void;
}) {
  const [status, setStatus] = useState<ResummaryStatus>({ status: 'unknown' });
  const [error, setError] = useState<string | null>(null);
  const [action, setAction] = useState<'request' | 'load' | null>(null);
  const requestRef = useRef<AbortController | null>(null);
  const mutationRef = useRef<AbortController | null>(null);
  const alive = useRef(true);
  const callbacks = useRef({ isDirty, onLoaded });
  const pending = status.status === 'queued' || status.status === 'running';
  useEffect(() => { callbacks.current = { isDirty, onLoaded }; }, [isDirty, onLoaded]);
  useEffect(() => {
    alive.current = true;
    return () => { alive.current = false; requestRef.current?.abort(); mutationRef.current?.abort(); };
  }, [meetingId]);
  const refresh = useCallback(async () => {
    if (!enabled || mutationRef.current || (requestRef.current && !requestRef.current.signal.aborted)) return;
    const controller = new AbortController(); requestRef.current = controller;
    try {
      const next = await meetingsApi.getResummary(meetingId, { signal: controller.signal });
      if (!controller.signal.aborted && alive.current) { setStatus(next); setError(null); }
    } catch (cause) {
      if (!controller.signal.aborted && alive.current) {
        setError(cause instanceof ApiError && [401, 403, 404].includes(cause.status)
          ? '다시 요약 상태를 확인할 권한이 없거나 아직 사용할 수 없습니다.'
          : '다시 요약 상태를 확인하지 못했습니다.');
      }
    } finally { if (requestRef.current === controller) requestRef.current = null; }
  }, [enabled, meetingId]);
  useEffect(() => { void refresh(); return () => requestRef.current?.abort(); }, [refresh]);
  useEffect(() => {
    if (!pending || action) return;
    let checks = 0;
    const timer = setInterval(() => {
      if (++checks > 90) { clearInterval(timer); setError('자동 확인을 마쳤습니다. 상태를 다시 확인해 주세요.'); return; }
      void refresh();
    }, 10000);
    return () => clearInterval(timer);
  }, [pending, action, refresh]);

  const request = async () => {
    if (!enabled || !canEdit || pending || mutationRef.current || callbacks.current.isDirty()) return;
    requestRef.current?.abort();
    const controller = new AbortController(); mutationRef.current = controller; setAction('request'); setError(null);
    let reconcile = false;
    try {
      const next = await meetingsApi.requestResummary(meetingId, { signal: controller.signal });
      if (!controller.signal.aborted && alive.current) setStatus(next);
    } catch (cause) {
      if (!controller.signal.aborted && alive.current) { setError(cause instanceof Error ? cause.message : '요약 요청에 실패했습니다.'); reconcile = true; }
    } finally {
      if (mutationRef.current === controller) mutationRef.current = null;
      if (!controller.signal.aborted && alive.current) { setAction(null); if (reconcile) void refresh(); }
    }
  };
  const load = async () => {
    if (!enabled || mutationRef.current || callbacks.current.isDirty()) return;
    const controller = new AbortController(); mutationRef.current = controller; setAction('load'); setError(null);
    try {
      const content = await loadVerifiedSummary(meetingId, status, controller.signal);
      if (!controller.signal.aborted && alive.current) {
        if (callbacks.current.isDirty()) throw new Error('편집 중인 내용이 있어 새 요약을 적용하지 않았습니다. 먼저 저장해 주세요.');
        callbacks.current.onLoaded(content);
      }
    } catch (cause) {
      if (!controller.signal.aborted && alive.current) setError(cause instanceof Error ? cause.message : '새 요약을 읽지 못했습니다.');
    } finally {
      if (mutationRef.current === controller) mutationRef.current = null;
      if (!controller.signal.aborted && alive.current) setAction(null);
    }
  };
  return { status, pending, action, error, request, refresh, load };
}
