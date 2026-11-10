// Joins an Outlook occurrence to its amazon-meetings-mcp meeting and parses the
// attendee lists. The two servers use different meeting IDs, so the join is on
// exact start instant, end instant and subject, with exactly one candidate.

import type { OutlookItem } from './calendar.js';

const MAX_LIST = 5000;
const MAX_ATTENDEES = 500;

export interface MeetingListItem {
  meetingId: string;
  subject: string;
  start: string;
  end: string;
  isRecurring: boolean;
}

function records(payload: unknown): unknown[] {
  if (Array.isArray(payload)) return payload;
  if (payload && typeof payload === 'object') {
    const meetings = (payload as Record<string, unknown>).meetings;
    if (Array.isArray(meetings)) return meetings;
  }
  return [];
}

export function parseMeetingList(payload: unknown): MeetingListItem[] {
  const out: MeetingListItem[] = [];
  for (const raw of records(payload)) {
    if (out.length >= MAX_LIST) break;
    if (!raw || typeof raw !== 'object') continue;
    const r = raw as Record<string, unknown>;
    if (typeof r.meetingId !== 'string' || !r.meetingId || typeof r.subject !== 'string'
      || typeof r.startTime !== 'string' || typeof r.endTime !== 'string') continue;
    out.push({ meetingId: r.meetingId, subject: r.subject, start: r.startTime, end: r.endTime,
      isRecurring: r.isRecurring === true });
  }
  return out;
}

export function joinMeeting(item: OutlookItem, list: MeetingListItem[]): MeetingListItem | null {
  if (typeof item.subject !== 'string') return null;
  const start = Date.parse(item.start);
  const end = Date.parse(item.end);
  if (!Number.isFinite(start) || !Number.isFinite(end)) return null;
  const matches = list.filter(m => m.subject === item.subject
    && Date.parse(m.start) === start && Date.parse(m.end) === end);
  return matches.length === 1 ? matches[0] : null;
}

export function parseAttendees(payload: unknown): { addresses: string[]; isPrivate: boolean } | null {
  if (!payload || typeof payload !== 'object' || Array.isArray(payload)) return null;
  const r = payload as Record<string, unknown>;
  const seen = new Set<string>();
  const add = (value: unknown): void => {
    if (typeof value !== 'string') return;
    const address = value.trim().toLowerCase();
    if (address && seen.size < MAX_ATTENDEES) seen.add(address);
  };
  for (const key of ['requiredAttendees', 'optionalAttendees']) {
    const list = r[key];
    if (Array.isArray(list)) list.forEach(add);
  }
  add(r.organizer);
  return { addresses: [...seen], isPrivate: r.isPrivate === true };
}
