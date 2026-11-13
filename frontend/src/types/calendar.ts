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
  shareAccountId?: string;
  shareEvidence?: string[];
  candidateAccountIds?: string[];
  private?: boolean;
  sharingMode?: 'auto' | 'account' | 'hidden';
}

export type CalendarSharingRequest =
  | { mode: 'auto' | 'hidden' }
  | { mode: 'account'; accountId: string };

export interface AccountCalendarEvent {
  meetingKey: string;
  title: string;
  startsAt: string;
  endsAt: string;
  timeZone: string;
  isAllDay: boolean;
  startDate?: string;
  endDate?: string;
  location?: string;
  sharedBy: string[];
  sharedByMe: boolean;
}

export interface AccountCalendarPage {
  events: AccountCalendarEvent[];
  nextCursor: string | null;
}

export interface CalendarShareDirectoryAccount {
  accountId: string;
  name: string;
  aliases: string[];
  domains: string[];
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
