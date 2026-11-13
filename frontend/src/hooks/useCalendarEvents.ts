'use client';

import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import { useAuth } from '@/components/auth/AuthProvider';
import { calendarApi } from '@/lib/api';
import type { CalendarEvent, CalendarSourceStatus } from '@/types/calendar';

type CalendarState = 'setup' | 'unavailable' | 'loading' | 'ready' | 'error';
interface Options {
  from: string;
  to: string;
  accountId?: string;
  enabled?: boolean;
  onAccessLost?: () => void;
}

function statusOf(error: unknown): number | undefined {
  return error && typeof error === 'object' && 'status' in error ? Number(error.status) : undefined;
}

function ordered(events: CalendarEvent[]): CalendarEvent[] {
  return [...new Map(events.map(event => [event.eventId, event])).values()]
    .sort((left, right) => Date.parse(left.startsAt) - Date.parse(right.startsAt) || left.eventId.localeCompare(right.eventId));
}

export function useCalendarWindow() {
  const [now, setNow] = useState(() => new Date());
  useEffect(() => {
    const timer = setInterval(() => setNow(new Date()), 60_000);
    return () => clearInterval(timer);
  }, []);
  const midnight = new Date(now);
  midnight.setHours(0, 0, 0, 0);
  const start = midnight.getTime();
  const window = useMemo(() => {
    const end = new Date(start);
    end.setDate(end.getDate() + 7);
    return { from: new Date(start).toISOString(), to: end.toISOString() };
  }, [start]);
  return { now, window };
}

export function useCalendarEvents({ from, to, accountId, enabled = true, onAccessLost }: Options) {
  const { user, isAuthenticated, membershipRevision } = useAuth();
  const userId = isAuthenticated ? user?.userId : undefined;
  const [attempt, setAttempt] = useState(0);
  const onAccessLostRef = useRef(onAccessLost);
  useLayoutEffect(() => { onAccessLostRef.current = onAccessLost; }, [onAccessLost]);
  const scope = useMemo(() => ({
    userId, membershipRevision, from, to, accountId, enabled, attempt,
    controller: new AbortController(), active: false, eventsBusy: false, sourcesBusy: false,
  }), [userId, membershipRevision, from, to, accountId, enabled, attempt]);
  const [result, setResult] = useState<{
    scope: typeof scope;
    events: CalendarEvent[];
    sources: CalendarSourceStatus[];
    nextCursor: string | null;
    sourcesNextCursor: string | null;
    state: CalendarState;
    eventsError: boolean;
    sourcesError: boolean;
    isLoadingMore: boolean;
    isLoadingSources: boolean;
  } | null>(null);

  useLayoutEffect(() => {
    if (scope.controller.signal.aborted) scope.controller = new AbortController();
    scope.active = true;
    return () => { scope.active = false; scope.controller.abort(); };
  }, [scope]);

  const denyAccess = useCallback((kind: 'events' | 'sources') => {
    if (!scope.active || scope.controller.signal.aborted || !userId || !enabled) return;
    scope.controller.abort();
    scope.eventsBusy = false;
    scope.sourcesBusy = false;
    setResult({
      scope, events: [], sources: [], nextCursor: null, sourcesNextCursor: null,
      state: 'error', eventsError: kind === 'events', sourcesError: kind === 'sources',
      isLoadingMore: false, isLoadingSources: false,
    });
    if (accountId) onAccessLostRef.current?.();
  }, [scope, userId, enabled, accountId]);

  useEffect(() => {
    if (!userId || !enabled || scope.controller.signal.aborted) return;
    let active = true;
    const options = { signal: scope.controller.signal, expectedUserId: userId };
    const handleFailure = (kind: 'events' | 'sources') => (error: unknown) => {
      if (active && [401, 403].includes(statusOf(error) ?? 0)) denyAccess(kind);
      throw error;
    };
    void Promise.allSettled([
      calendarApi.events({ from, to, accountId, limit: 50 }, options).catch(handleFailure('events')),
      calendarApi.sources({ limit: 50 }, options).catch(handleFailure('sources')),
    ]).then(([events, sources]) => {
      if (!active || !scope.active || scope.controller.signal.aborted) return;
      const unavailable = sources.status === 'rejected' && [404, 501, 503].includes(statusOf(sources.reason) ?? 0)
        && (statusOf(sources.reason) !== 503 || sources.reason?.code === 'CALENDAR_UNAVAILABLE');
      if (events.status === 'rejected' && !unavailable && statusOf(events.reason) === 404) {
        denyAccess('events');
        return;
      }
      const rows = events.status === 'fulfilled' ? ordered(events.value.events) : [];
      const progress = sources.status === 'fulfilled' ? sources.value.sources : [];
      const nextCursor = events.status === 'fulfilled' ? events.value.nextCursor : null;
      const sourcesNextCursor = sources.status === 'fulfilled' ? sources.value.nextCursor : null;
      setResult({
        scope, events: rows, sources: progress, nextCursor, sourcesNextCursor,
        state: unavailable ? 'unavailable' : events.status === 'rejected' || sources.status === 'rejected' ? 'error'
          : !rows.length && !progress.length && !nextCursor && !sourcesNextCursor ? 'setup' : 'ready',
        eventsError: events.status === 'rejected', sourcesError: sources.status === 'rejected',
        isLoadingMore: false, isLoadingSources: false,
      });
    });
    return () => { active = false; };
  }, [scope, userId, enabled, from, to, accountId, denyAccess]);

  const current = result?.scope === scope && enabled && userId ? result : null;
  const valid = () => scope.active && !scope.controller.signal.aborted && !!userId && enabled;
  const loadMore = async () => {
    if (!valid() || !current?.nextCursor || scope.eventsBusy) return;
    scope.eventsBusy = true;
    setResult(previous => previous?.scope === scope ? { ...previous, isLoadingMore: true, eventsError: false } : previous);
    try {
      const page = await calendarApi.events({ from, to, accountId, limit: 50, cursor: current.nextCursor }, {
        signal: scope.controller.signal, expectedUserId: userId,
      });
      if (!valid()) return;
      if (page.nextCursor === current.nextCursor) throw new Error('Calendar continuation did not advance');
      setResult(previous => previous?.scope === scope ? {
        ...previous, events: ordered([...previous.events, ...page.events]), nextCursor: page.nextCursor,
      } : previous);
    } catch (error) {
      if (!valid()) return;
      const denied = [401, 403, 404].includes(statusOf(error) ?? 0);
      if (denied) {
        denyAccess('events');
        return;
      }
      setResult(previous => previous?.scope === scope ? {
        ...previous, eventsError: true,
      } : previous);
    } finally {
      scope.eventsBusy = false;
      if (valid()) setResult(previous => previous?.scope === scope ? { ...previous, isLoadingMore: false } : previous);
    }
  };
  const loadMoreSources = async () => {
    if (!valid() || !current?.sourcesNextCursor || scope.sourcesBusy) return;
    scope.sourcesBusy = true;
    setResult(previous => previous?.scope === scope ? { ...previous, isLoadingSources: true, sourcesError: false } : previous);
    try {
      const page = await calendarApi.sources({ limit: 50, cursor: current.sourcesNextCursor }, {
        signal: scope.controller.signal, expectedUserId: userId,
      });
      if (!valid()) return;
      if (page.nextCursor === current.sourcesNextCursor) throw new Error('Calendar source continuation did not advance');
      setResult(previous => previous?.scope === scope ? {
        ...previous, sources: [...new Map([...previous.sources, ...page.sources].map(source => [source.sourceId, source])).values()],
        sourcesNextCursor: page.nextCursor,
      } : previous);
    } catch (error) {
      if (!valid()) return;
      const denied = [401, 403].includes(statusOf(error) ?? 0);
      if (denied) {
        denyAccess('sources');
        return;
      }
      setResult(previous => previous?.scope === scope ? {
        ...previous, sourcesError: true,
      } : previous);
    } finally {
      scope.sourcesBusy = false;
      if (valid()) setResult(previous => previous?.scope === scope ? { ...previous, isLoadingSources: false } : previous);
    }
  };
  return {
    userId,
    events: current?.events ?? [], sources: current?.sources ?? [],
    state: current?.state ?? 'loading' as CalendarState,
    nextCursor: current?.nextCursor ?? null, sourcesNextCursor: current?.sourcesNextCursor ?? null,
    eventsError: current?.eventsError ?? false, sourcesError: current?.sourcesError ?? false,
    isLoadingMore: current?.isLoadingMore ?? false, isLoadingSources: current?.isLoadingSources ?? false,
    loadMore, loadMoreSources, retry: () => { if (scope.active && userId && enabled) setAttempt(previous => previous + 1); },
  };
}
