// Joins an Outlook occurrence to its amazon-meetings-mcp meeting and parses the
// attendee lists. The two servers use different meeting IDs, so the join is on
// exact start instant, end instant and subject, with exactly one candidate.

import type { OutlookItem, Window } from './calendar.js';
import { organizerDomain, pruneLedger } from './calendar.js';
import type { IntakeStore } from './store.js';

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

/** Returns null when the list exceeds the cap: a truncated list could hide a second candidate. */
export function parseMeetingList(payload: unknown): MeetingListItem[] | null {
  const all = records(payload);
  if (all.length > MAX_LIST) return null;
  const out: MeetingListItem[] = [];
  for (const raw of all) {
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
  if (typeof r.isPrivate !== 'boolean') return null;
  const seen = new Set<string>();
  const add = (value: unknown): void => {
    if (typeof value !== 'string') return;
    const address = value.trim().toLowerCase();
    if (address) seen.add(address);
  };
  for (const key of ['requiredAttendees', 'optionalAttendees']) {
    const list = r[key];
    if (Array.isArray(list)) list.forEach(add);
  }
  add(r.organizer);
  // A truncated list could hide a second SA, so overflow means unknown.
  if (seen.size > MAX_ATTENDEES) return null;
  return { addresses: [...seen], isPrivate: r.isPrivate };
}

// ---------- calendar account matching ----------
// The calendar run reads attendee domains and the private flag of each
// occurrence it may share, through the same join and `get_meeting` parse.
// Only domains and the flag are kept; addresses never leave this module.
// Facts are cached per occurrence and change key in
// `calendar/<sourceId>/meeting-facts.json`, pruned like the ledger.

const MAX_DOMAINS = 50;
const MAX_FACT_READS = 25;
const MAX_FACT_FAILURES = 3;

export interface MeetingFacts { domains: string[]; isPrivate: boolean }

/**
 * Null (no answer) unless the payload carries the privacy flag and an attendee
 * list or organizer, so a successful "not found" payload never reads as a
 * meeting without attendees.
 */
export function meetingFacts(payload: unknown): MeetingFacts | null {
  const parsed = parseAttendees(payload);
  // An explicit private flag holds even when the attendee list is over its cap.
  if (!parsed) return payload && typeof payload === 'object' && (payload as Record<string, unknown>).isPrivate === true
    ? { domains: [], isPrivate: true } : null;
  const r = payload as Record<string, unknown>;
  if (!Array.isArray(r.requiredAttendees) && !Array.isArray(r.optionalAttendees) && typeof r.organizer !== 'string') return null;
  const domains = new Set<string>();
  for (const address of parsed.addresses) {
    const domain = organizerDomain(address);
    if (domain) domains.add(domain);
  }
  // Too many domains to keep is no domain answer: a truncated list could turn an
  // ambiguous match into an automatic share. The private flag still holds.
  if (domains.size > MAX_DOMAINS) return parsed.isPrivate ? { domains: [], isPrivate: true } : null;
  return { domains: [...domains].sort(), isPrivate: parsed.isPrivate };
}

/** The meetings connector calls used for facts; `list` covers the whole sync window. */
export interface MeetingsReader {
  list(window: Window): Promise<unknown>;
  get(meetingId: string): Promise<unknown>;
}

interface FactsEntry extends MeetingFacts { changeKey: string; endsAt: string }
interface FactsCache { version: 1; entries: Record<string, FactsEntry> }

export class AttendeeFacts {
  private cache: FactsCache;
  private list: MeetingListItem[] | null | undefined;
  private failures = 0;
  private reads = 0;
  private changed = false;
  /** Lookups without an answer: no reader, no unique join, failed, or over the per-run budget. */
  unavailable = 0;

  constructor(private readonly store: IntakeStore, private readonly sourceId: string,
    private readonly reader: MeetingsReader | null, private readonly window: Window) {
    let stored: FactsCache | null = null;
    try { stored = store.read<FactsCache>('calendar', sourceId, 'meeting-facts.json'); } catch { stored = null; }
    this.cache = stored && stored.version === 1 && stored.entries && typeof stored.entries === 'object'
      ? stored : { version: 1, entries: {} };
  }

  async factsFor(item: OutlookItem): Promise<MeetingFacts | null> {
    const cached = this.cache.entries[item.meetingId];
    if (item.meetingChangeKey && cached?.changeKey === item.meetingChangeKey && Array.isArray(cached.domains) &&
        typeof cached.isPrivate === 'boolean') {
      return { domains: [...cached.domains], isPrivate: cached.isPrivate };
    }
    const facts = await this.read(item);
    if (!facts) {
      this.unavailable++;
      return null;
    }
    const end = new Date(item.end);
    if (item.meetingChangeKey && Number.isFinite(end.getTime())) {
      this.cache.entries[item.meetingId] = { changeKey: item.meetingChangeKey, ...facts, endsAt: end.toISOString() };
      this.changed = true;
    }
    return facts;
  }

  private async read(item: OutlookItem): Promise<MeetingFacts | null> {
    if (!this.reader || this.failures >= MAX_FACT_FAILURES || this.reads >= MAX_FACT_READS) return null;
    if (this.list === undefined) {
      // A truncated list could hide a second candidate, so an over-cap list joins nothing.
      try { this.list = parseMeetingList(await this.reader.list(this.window)); } catch { this.list = null; }
    }
    if (!this.list) return null;
    const joined = joinMeeting(item, this.list);
    if (!joined) return null;
    this.reads++;
    let facts: MeetingFacts | null;
    try { facts = meetingFacts(await this.reader.get(joined.meetingId)); } catch { facts = null; }
    if (!facts) {
      this.failures++;
      return null;
    }
    this.failures = 0;
    return facts;
  }

  save(now: Date): void {
    const pruned = pruneLedger(this.cache.entries, now);
    if (!this.changed && Object.keys(pruned).length === Object.keys(this.cache.entries).length) return;
    this.cache = { version: 1, entries: pruned };
    this.store.write(this.cache, 'calendar', this.sourceId, 'meeting-facts.json');
    this.changed = false;
  }
}
