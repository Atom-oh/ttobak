import type { CalendarEvent } from '@/types/calendar';

// Customer meetings are events the server resolved to a TTOBAK Account.
// All-day events are always internal; input order is kept in each group.
export function partitionCustomerEvents(events: CalendarEvent[]): { customer: CalendarEvent[]; internal: CalendarEvent[] } {
  const customer: CalendarEvent[] = [];
  const internal: CalendarEvent[] = [];
  for (const event of events) (!event.isAllDay && event.accountId ? customer : internal).push(event);
  return { customer, internal };
}

export function internalEventsToggleLabel(count: number): string {
  return `사내 일정 ${count}건`;
}

export const INTERNAL_EVENTS_COLLAPSED_KEY = 'ttobak-calendar-internal-collapsed';

type StorageLike = Pick<Storage, 'getItem' | 'setItem'>;

function defaultStorage(): StorageLike | null {
  try {
    return typeof window === 'undefined' ? null : window.localStorage;
  } catch {
    return null;
  }
}

/** Collapsed unless this browser saved an expanded choice. */
export function readInternalEventsCollapsed(storage?: StorageLike | null): boolean {
  try {
    const store = storage === undefined ? defaultStorage() : storage;
    return store?.getItem(INTERNAL_EVENTS_COLLAPSED_KEY) !== 'expanded';
  } catch {
    return true;
  }
}

export function writeInternalEventsCollapsed(collapsed: boolean, storage?: StorageLike | null): void {
  try {
    const store = storage === undefined ? defaultStorage() : storage;
    store?.setItem(INTERNAL_EVENTS_COLLAPSED_KEY, collapsed ? 'collapsed' : 'expanded');
  } catch {
    // A convenience preference only.
  }
}
