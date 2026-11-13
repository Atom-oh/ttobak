'use client';

import { useCallback, useEffect, useRef, useState } from 'react';
import { useAuth } from '@/components/auth/AuthProvider';
import { useCalendarWindow } from '@/hooks/useCalendarEvents';
import { calendarApi } from '@/lib/api';
import { SHARING_COPY, sharedByLabel } from '@/lib/calendarSharing';
import type { AccountCalendarEvent } from '@/types/calendar';

function when(event: AccountCalendarEvent): string {
  if (event.isAllDay) return event.startDate || '';
  const date = new Date(event.startsAt);
  return Number.isFinite(date.getTime())
    ? date.toLocaleString('ko-KR', { month: 'long', day: 'numeric', hour: '2-digit', minute: '2-digit' }) : '';
}

export function AccountSchedule({ accountId }: { accountId: string }) {
  const { window } = useCalendarWindow();
  const { user } = useAuth();
  const userId = user?.userId;
  const [events, setEvents] = useState<AccountCalendarEvent[]>([]);
  const [nextCursor, setNextCursor] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState(false);
  const [denied, setDenied] = useState(false);
  const [attempt, setAttempt] = useState(0);
  const busy = useRef(false);
  const generation = useRef(0);

  const load = useCallback(async (cursor?: string) => {
    if (!userId || busy.current) return;
    busy.current = true;
    const mine = generation.current;
    setLoading(true);
    setError(false);
    try {
      const page = await calendarApi.accountCalendar(accountId, { ...window, limit: 50, cursor }, { expectedUserId: userId });
      if (mine !== generation.current) return;
      if (cursor && page.nextCursor === cursor) throw new Error('Calendar continuation did not advance');
      setEvents(previous => cursor ? [...previous, ...page.events] : page.events);
      setNextCursor(page.nextCursor);
    } catch (caught) {
      const status = caught && typeof caught === 'object' && 'status' in caught ? Number(caught.status) : 0;
      if (mine === generation.current) { if (status === 403 || status === 404) setDenied(true); else setError(true); }
    } finally {
      if (mine === generation.current) { busy.current = false; setLoading(false); }
    }
  }, [accountId, userId, window]);

  useEffect(() => {
    generation.current += 1;
    busy.current = false;
    setEvents([]);
    setNextCursor(null);
    setDenied(false);
    void load();
    return () => { generation.current += 1; busy.current = false; };
  }, [load, attempt]);

  if (denied) return null;
  return (
    <section aria-label={SHARING_COPY.scheduleTitle} className="min-w-0 rounded-2xl border border-border-subtle bg-surface p-5 shadow-sm sm:p-6">
      <h2 className="text-lg font-bold text-text-main">{SHARING_COPY.scheduleTitle}</h2>
      {loading && !events.length && <p role="status" className="mt-3 text-sm text-text-secondary">{SHARING_COPY.loading}</p>}
      {error && <div role="alert" className="mt-3 text-sm text-text-secondary">
        {SHARING_COPY.loadError}{' '}
        <button type="button" onClick={() => events.length && nextCursor ? void load(nextCursor) : setAttempt(value => value + 1)}
          className="rounded-lg border border-border-subtle px-3 py-2 text-primary focus-visible:outline-2 focus-visible:outline-primary">{SHARING_COPY.retry}</button>
      </div>}
      {!loading && !error && !events.length && <p className="mt-3 text-sm text-text-secondary">{SHARING_COPY.empty}</p>}
      {events.length > 0 && <ul className="mt-4 space-y-3">
        {events.map(event => <li key={event.meetingKey} className="min-w-0 rounded-xl border border-border-subtle p-4">
          <p className="text-xs text-primary">{when(event)}</p>
          <h3 className="mt-1 break-words font-semibold text-text-main">{event.title}</h3>
          {event.location && <p className="mt-1 break-words text-sm text-text-secondary">{event.location}</p>}
          {event.sharedBy.length > 0 && <p className="mt-1 text-xs text-text-muted" title={event.sharedBy.join(', ')}>{sharedByLabel(event.sharedBy.length)}</p>}
        </li>)}
      </ul>}
      {nextCursor && !error && <button type="button" disabled={loading} onClick={() => void load(nextCursor)}
        className="mt-4 rounded-lg border border-border-subtle px-3 py-2 text-sm text-primary focus-visible:outline-2 focus-visible:outline-primary disabled:opacity-40">
        {SHARING_COPY.loadMore}
      </button>}
    </section>
  );
}
