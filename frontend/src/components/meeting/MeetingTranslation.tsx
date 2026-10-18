'use client';

import { useCallback, useEffect, useRef, useState } from 'react';
import { ApiError, meetingsApi } from '@/lib/api';
import type { MeetingTranslationPage } from '@/types/meeting';

export type TranslationTarget = 'ko' | 'en';
type Part = MeetingTranslationPage['part'];
type Segment = NonNullable<MeetingTranslationPage['segments']>[number];

interface TranslationState {
  target: TranslationTarget | null;
  notes: string;
  segments: Segment[];
  nextCursor: string | null;
  loading: boolean;
  error: string | null;
  untranslatedLines: number;
}

const idle: TranslationState = { target: null, notes: '', segments: [], nextCursor: null, loading: false, error: null, untranslatedLines: 0 };

function translationError(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.code === 'RATE_LIMITED') return '오늘 번역 한도를 모두 사용했습니다. 내일 다시 시도해 주세요.';
    if (error.code === 'STALE_CURSOR') return '원문이 바뀌었습니다. 처음부터 다시 번역해 주세요.';
    if (error.code === 'SHARING_REGEN_REQUIRED') return error.message;
    if (error.code === 'FEATURE_DISABLED') return '번역 기능이 꺼져 있습니다.';
  }
  return '번역하지 못했습니다. 잠시 후 다시 시도해 주세요.';
}

/**
 * Translates the reading pages of one meeting part on demand. Results live in
 * component memory only and are discarded when the source text changes.
 */
export function useMeetingTranslation(meetingId: string | undefined, part: Part, sourceKey: string) {
  const [state, setState] = useState<TranslationState>(idle);
  const generation = useRef(0);
  const controller = useRef<AbortController | null>(null);

  const clear = useCallback(() => {
    generation.current++;
    controller.current?.abort();
    controller.current = null;
    setState(idle);
  }, []);

  useEffect(() => clear, [clear, meetingId, part, sourceKey]);

  const fetchPage = useCallback(async (target: TranslationTarget, cursor: string | null) => {
    if (!meetingId) return;
    const run = ++generation.current;
    controller.current?.abort();
    const abort = new AbortController();
    controller.current = abort;
    setState((current) => cursor
      ? { ...current, loading: true, error: null }
      : { ...idle, target, loading: true });
    try {
      const page = await meetingsApi.translate(meetingId, { part, target, ...(cursor ? { cursor } : {}) }, { signal: abort.signal });
      if (run !== generation.current) return;
      setState((current) => ({
        ...current,
        target,
        notes: current.notes + (page.notes ?? ''),
        segments: [...current.segments, ...(page.segments ?? [])],
        nextCursor: page.nextCursor,
        untranslatedLines: current.untranslatedLines + page.untranslatedLines,
        loading: false,
      }));
    } catch (error) {
      if (run !== generation.current || abort.signal.aborted) return;
      setState((current) => ({ ...current, loading: false, error: translationError(error),
        nextCursor: error instanceof ApiError && error.code === 'STALE_CURSOR' ? null : current.nextCursor }));
    }
  }, [meetingId, part]);

  const start = useCallback((target: TranslationTarget) => { void fetchPage(target, null); }, [fetchPage]);
  const more = useCallback(() => {
    if (state.target && state.nextCursor) void fetchPage(state.target, state.nextCursor);
  }, [fetchPage, state.target, state.nextCursor]);

  return { ...state, start, more, clear };
}

export function TranslationControls({ translation }: { translation: ReturnType<typeof useMeetingTranslation> }) {
  return (
    <label className="flex items-center gap-1 text-xs text-slate-500 dark:text-text-muted">
      <span className="material-symbols-outlined text-base" aria-hidden="true">translate</span>
      <select
        aria-label="번역 언어"
        value={translation.target ?? ''}
        disabled={translation.loading}
        onChange={(event) => {
          const value = event.target.value;
          if (value === 'ko' || value === 'en') translation.start(value);
          else translation.clear();
        }}
        className="rounded-md border border-slate-200 dark:border-white/10 bg-white dark:bg-surface-lowest px-1.5 py-1 text-xs"
      >
        <option value="">원문</option>
        <option value="ko">한국어로 번역</option>
        <option value="en">영어로 번역</option>
      </select>
    </label>
  );
}

export function TranslationStatus({ translation }: { translation: ReturnType<typeof useMeetingTranslation> }) {
  if (!translation.target) return null;
  return (
    <div className="mt-3 flex flex-wrap items-center gap-3 text-xs text-slate-500 dark:text-text-muted" role="status">
      {translation.loading && <span className="animate-pulse">번역 중…</span>}
      {translation.error && <span role="alert" className="text-red-600 dark:text-red-300">{translation.error}</span>}
      {!translation.loading && translation.untranslatedLines > 0 && (
        <span>{translation.untranslatedLines}개 줄은 번역하지 못해 원문으로 표시합니다.</span>
      )}
      <span>기계 번역이며 저장되지 않습니다.</span>
      {translation.nextCursor && !translation.loading && (
        <button type="button" onClick={translation.more} className="underline">다음 부분 번역</button>
      )}
    </div>
  );
}
