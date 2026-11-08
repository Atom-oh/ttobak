'use client';

import { useEffect, useRef, type MutableRefObject } from 'react';
import { isProcessing, PROCESSING_REFRESH_LIMIT_MS, PROCESSING_REFRESH_MS, refreshListed } from '@/lib/meetingStatus';

type Listed = { meetingId: string; status?: string };

/** The meeting a finished upload returned home with (`/?uploaded={id}`). */
export type UploadSeed = { owner?: string; id: string | null };

/**
 * Reads the upload hint once per signed-in principal. A different principal in
 * the same tab never inherits it.
 */
export function useUploadSeed(userId: string | undefined, isAuthenticated: boolean): MutableRefObject<UploadSeed> {
  const seed = useRef<UploadSeed>({ id: null });
  useEffect(() => {
    if (!isAuthenticated || !userId) {
      seed.current.id = null;
      return;
    }
    if (seed.current.owner) {
      if (seed.current.owner !== userId) seed.current.id = null;
      return;
    }
    const uploadedId = new URLSearchParams(window.location.search).get('uploaded');
    seed.current = { owner: userId, id: uploadedId && /^[a-zA-Z0-9_-]{1,128}$/.test(uploadedId) ? uploadedId : null };
  }, [isAuthenticated, userId]);
  return seed;
}

/** Applies the upload hint to a freshly loaded first page. */
export function withUploadSeed<T extends Listed>(meetings: T[], seed: MutableRefObject<UploadSeed>): T[] {
  return refreshListed(meetings, [], seed.current.id ?? undefined);
}

/**
 * While a listed meeting is processing or the uploaded meeting has not yet
 * appeared with a post-recording status, refreshes the current first page every
 * 15 s, only while visible and for at most 30 minutes. `scope` names every
 * input of `list` (principal, filters); a change aborts the request in flight.
 */
export function useProcessingRefresh<T extends Listed>({ enabled, meetings, seed, scope, list, setMeetings, busy }: {
  enabled: boolean;
  meetings: T[];
  seed: MutableRefObject<UploadSeed>;
  scope: readonly unknown[];
  list: (signal: AbortSignal) => Promise<T[]>;
  setMeetings: (update: (previous: T[]) => T[]) => void;
  busy?: () => boolean;
}): void {
  const hasProcessing = meetings.some(m => isProcessing(m.status));
  const started = useRef<number | null>(null);
  useEffect(() => {
    if (!enabled || (!hasProcessing && !seed.current.id)) { started.current = null; return; }
    started.current ??= Date.now();
    let controller: AbortController | null = null;
    const timer = setInterval(() => {
      if ((!hasProcessing && !seed.current.id) || Date.now() - (started.current ?? 0) > PROCESSING_REFRESH_LIMIT_MS) {
        clearInterval(timer);
        return;
      }
      if (document.visibilityState !== 'visible' || busy?.() || controller) return;
      const current = new AbortController();
      controller = current;
      list(current.signal)
        .then(fresh => {
          if (current.signal.aborted) return;
          const uploadedId = seed.current.id;
          setMeetings(previous => refreshListed(previous, fresh, uploadedId ?? undefined));
          if (uploadedId && fresh.some(m => m.meetingId === uploadedId && m.status && m.status !== 'recording')) seed.current.id = null;
        })
        .catch(err => { if (!current.signal.aborted) console.warn('Meeting status refresh failed:', err); })
        .finally(() => { if (controller === current) controller = null; });
    }, PROCESSING_REFRESH_MS);
    return () => { clearInterval(timer); controller?.abort(); };
    // scope lists every input of list(); the callbacks themselves may be recreated.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [enabled, hasProcessing, ...scope]);
}
