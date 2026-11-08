export interface CalendarEvent {
  eventId: string;
  sourceId: string;
  provider: 'outlook';
  title: string;
  startsAt: string;
  endsAt: string;
  timeZone: string;
  isAllDay: boolean;
  startDate?: string;
  endDate?: string;
  location?: string;
  accountId?: string;
}

export interface CalendarSourceStatus {
  sourceId: string;
  provider: 'outlook';
  revision: number;
  lastAttemptAt: string;
  lastCompletedAt?: string;
  lastCompletedRevision?: number;
  completedWindowStart?: string;
  completedWindowEnd?: string;
}

export interface CalendarEventPage {
  events: CalendarEvent[];
  nextCursor: string | null;
}

export interface CalendarSourcePage {
  sources: CalendarSourceStatus[];
  nextCursor: string | null;
}

export interface CalendarEventQuery {
  from: string;
  to: string;
  accountId?: string;
  limit?: number;
  cursor?: string;
}
