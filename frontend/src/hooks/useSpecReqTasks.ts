'use client';

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useAuth } from '@/components/auth/AuthProvider';
import { specReqTaskApi } from '@/lib/api';
import { parseTaskHighlight, type SpecReqTaskChange, type SpecReqTaskView } from '@/types/specreqTask';

export type SpecReqTasksState = 'loading' | 'ready' | 'unavailable' | 'error';

/** 50 pages of 20: 1,000 tasks, the runner's cap. */
export const MAX_PAGES = 50;
const PAGE_SIZE = 20;

function statusOf(error: unknown): number | undefined {
  return error && typeof error === 'object' && 'status' in error ? Number(error.status) : undefined;
}

function merge(tasks: SpecReqTaskView[]): SpecReqTaskView[] {
  return [...new Map(tasks.map(task => [task.eventId, task])).values()]
    .sort((left, right) => Date.parse(left.occurrenceStartsAt) - Date.parse(right.occurrenceStartsAt)
      || left.eventId.localeCompare(right.eventId));
}

export function useSpecReqTasks({ accountId, highlight }: { accountId?: string; highlight?: string } = {}) {
  const { user, isAuthenticated, membershipRevision } = useAuth();
  const userId = isAuthenticated ? user?.userId : undefined;
  const [attempt, setAttempt] = useState(0);
  const key = `${userId ?? ''}:${membershipRevision}:${attempt}`;
  const [result, setResult] = useState<{ key: string; tasks: SpecReqTaskView[]; state: SpecReqTasksState; partial: boolean } | null>(null);
  const controllerRef = useRef<AbortController | null>(null);
  const keyRef = useRef(key);
  keyRef.current = key;
  const wanted = parseTaskHighlight(highlight);

  useEffect(() => {
    if (!userId) return;
    const controller = new AbortController();
    controllerRef.current = controller;
    const options = { signal: controller.signal, expectedUserId: userId };
    const finish = (tasks: SpecReqTaskView[], state: SpecReqTasksState, partial = false) => {
      if (!controller.signal.aborted) setResult({ key, tasks, state, partial });
    };
    void (async () => {
      try {
        const tasks: SpecReqTaskView[] = [];
        let cursor: string | undefined;
        for (let page = 0; page < MAX_PAGES; page++) {
          const response = await specReqTaskApi.list({ limit: PAGE_SIZE, cursor }, options);
          tasks.push(...response.tasks);
          cursor = response.nextCursor || undefined;
          if (!cursor) break;
        }
        if (wanted && !tasks.some(task => task.eventId === wanted)) {
          try { tasks.push(await specReqTaskApi.get(wanted, options)); } catch (error) {
            if (controller.signal.aborted) return;
            if ([401, 403].includes(statusOf(error) ?? 0)) throw error;
          }
        }
        // A cursor left after the last page means some tasks are not shown.
        finish(merge(tasks), 'ready', !!cursor);
      } catch (error) {
        if (controller.signal.aborted) return;
        finish([], [404, 501, 503].includes(statusOf(error) ?? 0) ? 'unavailable' : 'error');
      }
    })();
    return () => { controller.abort(); };
  }, [key, userId, wanted]);

  const current = result?.key === key && userId ? result : null;
  const tasks = useMemo(() => (current?.tasks ?? []).filter(task => !accountId || task.accountId === accountId),
    [current, accountId]);
  const byEventId = useMemo(() => new Map(tasks.map(task => [task.eventId, task])), [tasks]);
  const taskRef = useRef(current?.tasks ?? []);
  taskRef.current = current?.tasks ?? [];

  const replace = useCallback((next: SpecReqTaskView, scopeKey: string) => {
    if (keyRef.current !== scopeKey) return;
    setResult(previous => previous?.key === scopeKey ? { ...previous, tasks: merge([...previous.tasks, next]) } : previous);
  }, []);

  const transition = useCallback(async (eventId: string, change: SpecReqTaskChange) => {
    const controller = controllerRef.current;
    const task = taskRef.current.find(item => item.eventId === eventId);
    if (!userId || !controller || controller.signal.aborted || !task) throw new Error('Task is not loaded');
    const scopeKey = key;
    const options = { signal: controller.signal, expectedUserId: userId };
    try {
      const next = await specReqTaskApi.transition(eventId, { ...change, version: task.version }, options);
      replace(next, scopeKey);
      return next;
    } catch (error) {
      if (statusOf(error) === 409 && !controller.signal.aborted) {
        try { replace(await specReqTaskApi.get(eventId, options), scopeKey); } catch {
          // The row shown is stale; the caller must not claim the latest state was loaded.
          throw Object.assign(new Error('Task changed and could not be reloaded'), { status: 409, reloadFailed: true });
        }
      }
      throw error;
    }
  }, [userId, key, replace]);

  return {
    tasks, byEventId, state: current?.state ?? 'loading' as SpecReqTasksState, partial: current?.partial ?? false,
    transition, retry: () => { if (userId) setAttempt(previous => previous + 1); },
  };
}
