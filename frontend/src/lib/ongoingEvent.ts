import type { CalendarEvent, CalendarEventPage } from '@/types/calendar';

const EARLY_MS = 10 * 60_000;
const WINDOW_MS = 60 * 60_000;

/** The calendar event a recording starting at `now` belongs to, if any.
 * Candidates are timed events with `startsAt - 10 min <= now < endsAt`.
 * Events with an Account win, then the start closest to `now`; a tie between
 * different Accounts is ambiguous and returns null. */
export function ongoingEventFor(events: CalendarEvent[], now: Date): CalendarEvent | null {
  const time = now.getTime();
  const candidates = events.filter(event => {
    const start = Date.parse(event.startsAt);
    const end = Date.parse(event.endsAt);
    return !event.isAllDay && Number.isFinite(start) && Number.isFinite(end) && start - EARLY_MS <= time && time < end;
  });
  const withAccount = candidates.filter(event => event.accountId);
  const pool = withAccount.length ? withAccount : candidates;
  if (!pool.length) return null;
  const distance = (event: CalendarEvent) => Math.abs(Date.parse(event.startsAt) - time);
  const best = Math.min(...pool.map(distance));
  const closest = pool.filter(event => distance(event) === best)
    .sort((left, right) => left.eventId.localeCompare(right.eventId));
  if (new Set(closest.map(event => event.accountId || '')).size > 1) return null;
  return closest[0];
}

/** Where the record page's `referenceAccountId` came from. */
export type AccountLinkSource = 'none' | 'url' | 'mcp' | 'user' | 'auto';

/** Auto-link only fills an empty Account nobody chose. After the user's change
 * (`user`) or an earlier auto link (`auto`), it is not applied again. A lookup
 * whose `generation` is no longer `currentGeneration` (stop, failure, leftover
 * upload or a newer start) applies nothing. */
export function shouldAutoLink(state: {
  accountId: string; recordingAccountId?: string; source: AccountLinkSource;
  generation?: number; currentGeneration?: number;
}): boolean {
  return state.generation === state.currentGeneration
    && !state.accountId && !state.recordingAccountId && state.source === 'none';
}

export const ONGOING_EVENT_MAX_PAGES = 5;

/** Reads every page of the lookup window, at most `maxPages`. Returns null
 * (abstain) when a cursor remains after the bound or the lookup went stale:
 * a partial set could pick the wrong event. */
export async function readOngoingEvents(
  fetchPage: (cursor?: string) => Promise<CalendarEventPage>,
  isCurrent: () => boolean = () => true,
  maxPages = ONGOING_EVENT_MAX_PAGES,
): Promise<CalendarEvent[] | null> {
  const events: CalendarEvent[] = [];
  let cursor: string | undefined;
  for (let page = 0; page < maxPages; page++) {
    if (!isCurrent()) return null;
    const result = await fetchPage(cursor);
    events.push(...result.events);
    if (!result.nextCursor) return events;
    cursor = result.nextCursor;
  }
  return null;
}

export function ongoingEventWindow(now: Date): { from: string; to: string } {
  return { from: new Date(now.getTime() - WINDOW_MS).toISOString(), to: new Date(now.getTime() + WINDOW_MS).toISOString() };
}

/** Meeting titles have no server bound; a long calendar subject is trimmed
 * to this many code points before it fills the title. */
export const AUTO_TITLE_MAX = 200;

export function autoLinkTitle(subject: string): string {
  return Array.from(subject.trim()).slice(0, AUTO_TITLE_MAX).join('').trim();
}

export function autoLinkedLabel(name: string): string {
  return `고객사: ${name} (일정에서 자동 연결)`;
}
