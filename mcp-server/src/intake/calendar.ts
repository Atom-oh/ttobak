// Calendar connector: reads aws-outlook-mcp `calendar_view` and syncs the
// normalized occurrences to the private TTOBAK calendar, following
// docs/runbooks/local-outlook-calendar-sync.md. Only explicit cancellations and
// exclusions of tracked occurrences become removals; absence never does.
// Account sharing: a matcher built from the TTOBAK share directory adds the
// matched account (with evidence kinds), candidate accounts or the `private`
// flag to each upsert. Matched text, attendees and categories never leave the Mac.

import { createHash, randomUUID } from 'node:crypto';
import type { TtobakApi } from '../api.js';
import { callCalendarTool, validateCalendarReceipt, validateCalendarSync,
  type CalendarSyncRequest } from '../calendar.js';
import { batches as splitBatches, deliveryFailure } from './batch.js';
import type { AttendeeFacts, MeetingFacts } from './attendees.js';
import { IntakeError } from './errors.js';
import { buildMatcher, DEFAULT_OWN_DOMAINS, type DirectoryAccount, type Matcher } from './match.js';
import type { IntakeStore } from './store.js';

export const CALENDAR_TOOL = { name: 'calendar_view', properties: ['start_date', 'end_date'] };
export const PAST_DAYS = 7;
export const FUTURE_DAYS = 30;
const MAX_ITEMS = 5000;
const LEDGER_RETENTION_DAYS = 120;
/** A shared, rescheduled upsert costs up to seven transaction items; 12 + 2 stay within 100. */
const MAX_BATCH = 12;
const UNTITLED = '(제목 없음)';

export interface OutlookItem {
  meetingId: string;
  subject?: string;
  start: string;
  end: string;
  location?: string;
  isCanceled?: boolean;
  isAllDay?: boolean;
  sensitivity?: string;
  /** Only the email is kept; the organizer's name never leaves the parser. */
  organizer?: { email?: string };
  meetingChangeKey?: string;
  seriesId?: string;
  /** Only whether a `Private` category exists is kept. */
  privateCategory?: boolean;
}

export interface CalendarEvent {
  externalId: string;
  title: string;
  startsAt: string;
  endsAt: string;
  timeZone: string;
  isAllDay: boolean;
  startDate?: string;
  endDate?: string;
  location?: string;
  /** Lowercase organizer email domain; never the name or local part. */
  organizerDomain?: string;
  seriesKey?: string;
  private?: boolean;
  shareAccountId?: string;
  shareEvidence?: string[];
  candidateAccountIds?: string[];
}

export interface ShareFields { shareAccountId?: string; shareEvidence?: string[]; candidateAccountIds?: string[]; private?: boolean }
export interface LedgerEntry { fp: string; startsAt: string; endsAt: string; share?: ShareFields }
export type Ledger = Record<string, LedgerEntry>;

export type Mutation = { op: 'upsert'; event: CalendarEvent } | { op: 'remove'; externalId: string };

export interface CalendarCounts {
  fetched: number;
  upserts: number;
  removals: number;
  unchanged: number;
  excluded: number;
  skippedLong: number;
  skippedInvalid: number;
  cancelledUntracked: number;
  unresolved: number;
  sensitivityUnknown: number;
  shared: number;
  candidates: number;
  private: number;
  attendeesUnavailable: number;
  directoryUnavailable: boolean;
}

export interface Window { start: Date; end: Date }

export interface CalendarState {
  version: 1;
  sourceId: string;
  /** Deployment and signed-in user this ledger and pending batch belong to. */
  scope: string;
  revision: number | null;
  ledger: Ledger;
  reconcileRequired: boolean;
  pending: null | { request: Record<string, unknown>; set: Ledger; remove: string[] };
}

// ---------- time helpers (pure) ----------

function zoneParts(instant: Date, zone: string): Record<string, number> {
  const parts = new Intl.DateTimeFormat('en-US', { timeZone: zone, year: 'numeric', month: '2-digit', day: '2-digit',
    hour: '2-digit', minute: '2-digit', second: '2-digit', hourCycle: 'h23' }).formatToParts(instant);
  return Object.fromEntries(parts.filter(part => part.type !== 'literal').map(part => [part.type, Number(part.value)]));
}

function offsetMs(instant: Date, zone: string): number {
  const parts = zoneParts(instant, zone);
  const asUtc = Date.UTC(parts.year, parts.month - 1, parts.day, parts.hour, parts.minute, parts.second);
  return asUtc - Math.floor(instant.getTime() / 1000) * 1000;
}

/** The instant of local midnight on the given calendar date in `zone`. */
export function zonedMidnight(year: number, month: number, day: number, zone: string): Date {
  const guess = Date.UTC(year, month - 1, day);
  let instant = new Date(guess - offsetMs(new Date(guess), zone));
  instant = new Date(guess - offsetMs(instant, zone));
  return instant;
}

export function localDate(instant: Date, zone: string): { year: number; month: number; day: number } {
  const parts = zoneParts(instant, zone);
  return { year: parts.year, month: parts.month, day: parts.day };
}

function addDays(date: { year: number; month: number; day: number }, days: number) {
  const value = new Date(Date.UTC(date.year, date.month - 1, date.day + days));
  return { year: value.getUTCFullYear(), month: value.getUTCMonth() + 1, day: value.getUTCDate() };
}

function isoDate(date: { year: number; month: number; day: number }): string {
  return `${String(date.year).padStart(4, '0')}-${String(date.month).padStart(2, '0')}-${String(date.day).padStart(2, '0')}`;
}

/** Past seven days through the next 30 days, as local-midnight instants. */
export function syncWindow(now: Date, zone: string): Window {
  const today = localDate(now, zone);
  const first = addDays(today, -PAST_DAYS), last = addDays(today, FUTURE_DAYS + 1);
  return { start: zonedMidnight(first.year, first.month, first.day, zone),
    end: zonedMidnight(last.year, last.month, last.day, zone) };
}

/** Seven-day `calendar_view` requests covering the window (MM-DD-YYYY, inclusive end). */
export function viewRequests(window: Window, zone: string): Record<string, string>[] {
  const requests: Record<string, string>[] = [];
  const format = (date: { year: number; month: number; day: number }) =>
    `${String(date.month).padStart(2, '0')}-${String(date.day).padStart(2, '0')}-${date.year}`;
  let cursor = localDate(window.start, zone);
  const last = localDate(new Date(window.end.getTime() - 1), zone);
  while (isoDate(cursor) <= isoDate(last)) {
    const end = addDays(cursor, 6);
    requests.push({ start_date: format(cursor), end_date: format(isoDate(end) > isoDate(last) ? last : end), view: 'week' });
    cursor = addDays(cursor, 7);
  }
  return requests;
}

// ---------- parsing and normalization (pure) ----------

function optionalType(value: Record<string, unknown>, key: string, type: 'string' | 'boolean'): boolean {
  return value[key] === undefined || value[key] === null || typeof value[key] === type;
}

/** Strictly validates calendar_view items; a malformed page fails the run. */
export function parseOutlookItems(payload: unknown): OutlookItem[] {
  const list = Array.isArray(payload) ? payload
    : payload && typeof payload === 'object' && Array.isArray((payload as Record<string, unknown>).meetings)
      ? (payload as Record<string, unknown>).meetings as unknown[] : null;
  if (!list || list.length > MAX_ITEMS) throw new IntakeError('PARSE_FAILED', 'calendar_view returned an unexpected shape');
  return list.map(item => {
    if (!item || typeof item !== 'object' || Array.isArray(item)) throw new IntakeError('PARSE_FAILED', 'calendar item is not an object');
    const value = item as Record<string, unknown>;
    if (typeof value.meetingId !== 'string' || !value.meetingId || typeof value.start !== 'string' ||
        typeof value.end !== 'string' || !optionalType(value, 'subject', 'string') ||
        !optionalType(value, 'location', 'string') || !optionalType(value, 'isCanceled', 'boolean') ||
        !optionalType(value, 'isAllDay', 'boolean') || !optionalType(value, 'sensitivity', 'string') ||
        !optionalType(value, 'meetingChangeKey', 'string') || !optionalType(value, 'seriesId', 'string') ||
        !(value.categories === undefined || value.categories === null ||
          (Array.isArray(value.categories) && value.categories.every(category => typeof category === 'string')))) {
      throw new IntakeError('PARSE_FAILED', 'calendar item has unexpected field types');
    }
    const privateCategory = Array.isArray(value.categories) &&
      value.categories.some(category => (category as string).trim().toLowerCase() === 'private');
    const organizer = value.organizer;
    const email = organizer && typeof organizer === 'object' && !Array.isArray(organizer)
      ? (organizer as Record<string, unknown>).email : undefined;
    return {
      meetingId: value.meetingId, start: value.start, end: value.end,
      ...(typeof value.subject === 'string' ? { subject: value.subject } : {}),
      ...(typeof value.location === 'string' ? { location: value.location } : {}),
      ...(typeof value.isCanceled === 'boolean' ? { isCanceled: value.isCanceled } : {}),
      ...(typeof value.isAllDay === 'boolean' ? { isAllDay: value.isAllDay } : {}),
      ...(typeof value.sensitivity === 'string' ? { sensitivity: value.sensitivity } : {}),
      ...(typeof email === 'string' ? { organizer: { email } } : {}),
      ...(typeof value.meetingChangeKey === 'string' && value.meetingChangeKey ? { meetingChangeKey: value.meetingChangeKey } : {}),
      ...(typeof value.seriesId === 'string' && value.seriesId ? { seriesId: value.seriesId } : {}),
      ...(privateCategory ? { privateCategory: true } : {}),
    };
  });
}

function truncateBytes(value: string, bytes: number): string {
  if (Buffer.byteLength(value) <= bytes) return value;
  let result = '';
  for (const character of value) {
    if (Buffer.byteLength(result + character) > bytes) break;
    result += character;
  }
  return result;
}

/** Removes join links (and their passcodes) and repeated room names. */
export function sanitizeLocation(location: string | undefined): string | undefined {
  if (!location) return undefined;
  const parts: string[] = [];
  for (const raw of location.split(/[;\n]/)) {
    const part = raw.replace(/\b(?:https?|msteams|zoommtg|webex):\/\/\S+/gi, '')
      .replace(/\S*(?:pwd|passcode|password)=\S*/gi, '').replace(/\s+/g, ' ').trim();
    if (part && !parts.includes(part)) parts.push(part);
  }
  const joined = truncateBytes(parts.join('; '), 256).trim();
  return joined || undefined;
}

/** The lowercase domain after the last `@`, or undefined when absent or malformed. */
export function organizerDomain(email: string | undefined): string | undefined {
  if (typeof email !== 'string') return undefined;
  const at = email.lastIndexOf('@');
  if (at < 0) return undefined;
  const domain = email.slice(at + 1).trim().toLowerCase();
  return /^[a-z0-9.-]{1,253}$/.test(domain) ? domain : undefined;
}

function excludedSensitivity(item: OutlookItem): boolean {
  return item.sensitivity !== undefined && /^(private|confidential)$/i.test(item.sensitivity.trim());
}

function instantOf(value: string): Date | null {
  const date = new Date(value);
  return Number.isFinite(date.getTime()) ? date : null;
}

function isLocalMidnight(instant: Date, zone: string): boolean {
  const parts = zoneParts(instant, zone);
  return parts.hour === 0 && parts.minute === 0 && parts.second === 0 && instant.getMilliseconds() === 0;
}

export function toEvent(item: OutlookItem, zone: string): CalendarEvent | null {
  const start = instantOf(item.start), end = instantOf(item.end);
  if (!start || !end || end <= start) return null;
  const title = truncateBytes(item.subject?.trim() ? item.subject : UNTITLED, 512);
  const event: CalendarEvent = {
    externalId: item.meetingId, title, startsAt: item.start, endsAt: item.end, timeZone: zone, isAllDay: false,
  };
  if (item.isAllDay && isLocalMidnight(start, zone) && isLocalMidnight(end, zone)) {
    event.isAllDay = true;
    event.startDate = isoDate(localDate(start, zone));
    event.endDate = isoDate(localDate(end, zone));
  }
  const location = sanitizeLocation(item.location);
  if (location) event.location = location;
  const domain = organizerDomain(item.organizer?.email);
  if (domain) event.organizerDomain = domain;
  return event;
}

export function fingerprint(event: CalendarEvent): string {
  return createHash('sha256').update(JSON.stringify(event)).digest('hex').slice(0, 32);
}

const LONG_EVENT_MS = 93 * 24 * 60 * 60 * 1000;

function validEvent(event: CalendarEvent, window: Window): boolean {
  try {
    validateCalendarSync({ sourceId: '11111111-1111-4111-8111-111111111111', batchId: '22222222-2222-4222-8222-222222222222',
      expectedRevision: 0, windowStart: window.start.toISOString(), windowEnd: window.end.toISOString(),
      complete: false, mutations: [{ op: 'upsert', event: { ...event } }] });
    return true;
  } catch {
    return false;
  }
}

export interface PlanSharing {
  sourceId: string;
  /** Null when the share directory could not be read: previous share fields are kept. */
  matcher: Matcher | null;
  /** Meeting facts per meetingId; null when the lookup had no answer. Missing means not looked up. */
  facts: Map<string, MeetingFacts | null>;
}

/** Items that would be upserted with a sharing decision (and so need meeting facts). */
export function sharingCandidates(items: OutlookItem[], window: Window): OutlookItem[] {
  const seen = new Set<string>();
  return items.filter(item => {
    if (seen.has(item.meetingId)) return false;
    const start = instantOf(item.start), end = instantOf(item.end);
    if (!start || !end || !(end > window.start && start < window.end)) return false;
    seen.add(item.meetingId);
    return !item.isCanceled && !excludedSensitivity(item) && !item.privateCategory &&
      end.getTime() - start.getTime() <= LONG_EVENT_MS;
  });
}

function shareFields(event: CalendarEvent): ShareFields | undefined {
  const fields: ShareFields = {
    ...(event.shareAccountId ? { shareAccountId: event.shareAccountId } : {}),
    ...(event.shareEvidence ? { shareEvidence: [...event.shareEvidence] } : {}),
    ...(event.candidateAccountIds ? { candidateAccountIds: [...event.candidateAccountIds] } : {}),
    ...(event.private ? { private: true } : {}),
  };
  return Object.keys(fields).length ? fields : undefined;
}

function applyShare(event: CalendarEvent, fields: ShareFields | undefined): void {
  if (fields?.private) {
    event.private = true;
    return;
  }
  if (fields?.shareAccountId) {
    event.shareAccountId = fields.shareAccountId;
    if (fields.shareEvidence?.length) event.shareEvidence = [...fields.shareEvidence].sort();
  }
  const candidates = (fields?.candidateAccountIds ?? []).filter(id => id !== fields?.shareAccountId);
  if (candidates.length) event.candidateAccountIds = [...new Set(candidates)].sort().slice(0, 5);
}

function decideSharing(event: CalendarEvent, item: OutlookItem, previous: LedgerEntry | undefined, sharing: PlanSharing): void {
  if (item.seriesId) event.seriesKey = createHash('sha256').update(`${sharing.sourceId}|${item.seriesId}`).digest('hex');
  const facts = sharing.facts.get(item.meetingId);
  // Fail closed: a private meeting, or one that was private when its lookup failed or was not
  // attempted (for example with the meetings connector deselected), is never shared.
  if (item.privateCategory || facts?.isPrivate || (facts == null && previous?.share?.private)) {
    event.private = true;
    return;
  }
  if (!sharing.matcher) {
    applyShare(event, previous?.share);
    return;
  }
  const domains = [...(facts?.domains ?? []), ...(event.organizerDomain ? [event.organizerDomain] : [])];
  const result = sharing.matcher.match({ title: event.title, location: event.location, domains });
  // A lookup without an answer must not drop a share that rested on attendee domains.
  if (facts === null && previous?.share?.shareEvidence?.includes('domain') &&
      !(result.kind === 'matched' && result.accountId === previous.share.shareAccountId)) {
    applyShare(event, previous.share);
  } else if (result.kind === 'matched') {
    applyShare(event, { shareAccountId: result.accountId, shareEvidence: result.evidence });
  } else if (result.kind === 'candidate') {
    applyShare(event, { candidateAccountIds: result.accountIds });
  }
}

export interface Plan {
  mutations: Mutation[];
  set: Ledger;
  remove: string[];
  counts: CalendarCounts;
  preview: { title: string; startsAt: string; op: 'upsert' | 'remove' | 'unchanged' }[];
}

/** Computes the mutations for one complete read of the window. */
export function planCalendar(items: OutlookItem[], ledger: Ledger, window: Window, zone: string,
  sharing?: PlanSharing): Plan {
  const counts: CalendarCounts = { fetched: 0, upserts: 0, removals: 0, unchanged: 0, excluded: 0, skippedLong: 0,
    skippedInvalid: 0, cancelledUntracked: 0, unresolved: 0, sensitivityUnknown: 0, shared: 0, candidates: 0, private: 0,
    attendeesUnavailable: 0, directoryUnavailable: sharing ? sharing.matcher === null : false };
  const mutations: Mutation[] = [];
  const set: Ledger = {};
  const remove: string[] = [];
  const preview: Plan['preview'] = [];
  const seen = new Set<string>();
  const removeTracked = (id: string) => {
    if (!ledger[id] || remove.includes(id)) return false;
    mutations.push({ op: 'remove', externalId: id });
    remove.push(id);
    counts.removals++;
    return true;
  };
  for (const item of items) {
    if (seen.has(item.meetingId)) continue;
    const start = instantOf(item.start), end = instantOf(item.end);
    if (!start || !end) {
      counts.skippedInvalid++;
      continue;
    }
    // Half-open overlap with the window; providers also return edge-touching items.
    if (!(end > window.start && start < window.end)) continue;
    seen.add(item.meetingId);
    counts.fetched++;
    if (item.sensitivity === undefined) counts.sensitivityUnknown++;
    if (item.isCanceled) {
      if (!removeTracked(item.meetingId)) counts.cancelledUntracked++;
      else preview.push({ title: item.subject ?? UNTITLED, startsAt: item.start, op: 'remove' });
      continue;
    }
    if (excludedSensitivity(item)) {
      counts.excluded++;
      removeTracked(item.meetingId);
      continue;
    }
    if (end.getTime() - start.getTime() > LONG_EVENT_MS) {
      counts.skippedLong++;
      continue;
    }
    const event = toEvent(item, zone);
    if (event && sharing) decideSharing(event, item, ledger[item.meetingId], sharing);
    else if (event && item.privateCategory) event.private = true;
    if (!event || !validEvent(event, window)) {
      counts.skippedInvalid++;
      continue;
    }
    if (event.private) counts.private++;
    if (event.shareAccountId) counts.shared++;
    else if (event.candidateAccountIds) counts.candidates++;
    const fp = fingerprint(event);
    if (ledger[event.externalId]?.fp === fp) {
      counts.unchanged++;
      preview.push({ title: event.title, startsAt: event.startsAt, op: 'unchanged' });
      continue;
    }
    mutations.push({ op: 'upsert', event });
    const share = shareFields(event);
    set[event.externalId] = { fp, startsAt: new Date(event.startsAt).toISOString(), endsAt: new Date(event.endsAt).toISOString(),
      ...(share ? { share } : {}) };
    counts.upserts++;
    preview.push({ title: event.title, startsAt: event.startsAt, op: 'upsert' });
  }
  for (const [id, entry] of Object.entries(ledger)) {
    if (seen.has(id)) continue;
    const start = new Date(entry.startsAt), end = new Date(entry.endsAt);
    if (end > window.start && start < window.end) counts.unresolved++;
  }
  const key = (mutation: Mutation) => mutation.op === 'upsert' ? mutation.event.externalId : mutation.externalId;
  mutations.sort((first, second) => key(first) < key(second) ? -1 : key(first) > key(second) ? 1 : 0);
  return { mutations, set, remove, counts, preview };
}

/** Splits mutations into batches of at most 12 within the request byte budget. */
export function batches(mutations: Mutation[]): Mutation[][] {
  return splitBatches(mutations, MAX_BATCH);
}

export function pruneLedger<T extends { endsAt: string }>(ledger: Record<string, T>, now: Date): Record<string, T> {
  const cutoff = now.getTime() - LEDGER_RETENTION_DAYS * 24 * 60 * 60 * 1000;
  return Object.fromEntries(Object.entries(ledger).filter(([, entry]) => new Date(entry.endsAt).getTime() >= cutoff));
}

// ---------- orchestration ----------

export interface CalendarSource {
  /** One complete read of `calendar_view` per request; throws IntakeError on failure. */
  view(args: Record<string, string>): Promise<unknown>;
}

export interface CalendarRunResult {
  counts: CalendarCounts;
  revision: number | null;
  preview?: Plan['preview'];
  /** Synced runs only: the occurrences this run upserted (first item per ID). */
  upserted?: OutlookItem[];
}

function emptyState(sourceId: string, scope: string): CalendarState {
  return { version: 1, sourceId, scope, revision: null, ledger: {}, reconcileRequired: false, pending: null };
}

/** Scope key: a hex digest of the TTOBAK deployment and the signed-in user. */
export const SCOPE_PATTERN = /^[0-9a-f]{32}$/;

function statePath(state: { sourceId: string; scope: string }): string[] {
  return ['calendar', state.sourceId, state.scope, 'state.json'];
}

/**
 * The ledger and pending batch of one source for one deployment and user.
 * Another account or deployment reads its own state, so a batch built for one
 * account is never sent to another.
 */
export function loadState(store: IntakeStore, sourceId: string, scope: string): CalendarState {
  if (!SCOPE_PATTERN.test(scope)) throw new IntakeError('INVALID_ARGUMENT', 'calendar state scope is invalid');
  const state = store.read<CalendarState>(...statePath({ sourceId, scope }));
  if (state === null) return emptyState(sourceId, scope);
  if (state.version !== 1 || state.sourceId !== sourceId || state.scope !== scope ||
      typeof state.ledger !== 'object' || state.ledger === null) {
    throw new IntakeError('STORE_INVALID', 'calendar state does not match its source');
  }
  return state;
}

async function serverRevision(api: TtobakApi, sourceId: string): Promise<number> {
  let cursor: string | undefined;
  for (let page = 0; page < 1000; page++) {
    const result = await callCalendarTool('ttobak_calendar_sync_status', cursor ? { cursor } : {}, api);
    const text = result.content?.[0]?.type === 'text' ? result.content[0].text : '';
    if (result.isError) {
      if (/TTOBAK_LOGIN_REQUIRED/.test(text)) throw new IntakeError('TTOBAK_LOGIN_REQUIRED');
      throw new IntakeError('TTOBAK_UNAVAILABLE', 'calendar source status could not be read');
    }
    const value = JSON.parse(text) as { sources: { sourceId: string; revision: number }[]; nextCursor: string | null };
    const match = value.sources.find(source => source.sourceId.toLowerCase() === sourceId.toLowerCase());
    if (match) return match.revision;
    if (value.nextCursor === null) return 0;
    cursor = value.nextCursor;
  }
  throw new IntakeError('TTOBAK_UNAVAILABLE', 'calendar source status did not finish paging');
}

async function send(api: TtobakApi, store: IntakeStore, state: CalendarState): Promise<void> {
  const pending = state.pending!;
  const { sourceId, body } = validateCalendarSync(pending.request);
  let receipt: unknown;
  try {
    receipt = await api.syncCalendarEvents(sourceId, body);
  } catch (error) {
    const failure = deliveryFailure(error);
    if (failure.kind === 'login') throw new IntakeError('TTOBAK_LOGIN_REQUIRED');
    if (failure.kind === 'rejected') {
      // A definitive rejection: drop the batch and require reconciliation on a 409.
      state.pending = null;
      if (failure.status === 409) state.reconcileRequired = true;
      store.write(state, ...statePath(state));
      throw new IntakeError(failure.status === 409 ? 'TTOBAK_CONFLICT' : 'TTOBAK_REJECTED', `HTTP ${failure.status}`);
    }
    // Ambiguous delivery: keep the identical pending batch for the next run.
    throw new IntakeError('TTOBAK_UNAVAILABLE', 'calendar sync unconfirmed; the identical batch will be retried');
  }
  let revision: number;
  try { revision = validateCalendarReceipt(receipt, body as CalendarSyncRequest); } catch {
    throw new IntakeError('TTOBAK_UNAVAILABLE', 'calendar sync receipt invalid; the identical batch will be retried');
  }
  for (const id of pending.remove) delete state.ledger[id];
  Object.assign(state.ledger, pending.set);
  state.revision = revision;
  state.pending = null;
  store.write(state, ...statePath(state));
}

export interface CalendarRunOptions {
  store: IntakeStore;
  api?: TtobakApi;
  source: CalendarSource;
  sourceId: string;
  /**
   * `SCOPE_PATTERN` key of the deployment and signed-in user. Required to
   * sync; a dry run without one plans against an empty ledger.
   */
  scope?: string;
  zone: string;
  now: Date;
  dryRun: boolean;
  reconcile?: boolean;
  /** The user confirmed the source hides or flags private items itself. */
  acceptUnknownSensitivity?: boolean;
  /** Optional meeting facts; without them matching uses title, location and organizer domain. */
  attendees?: AttendeeFacts;
  ownDomains?: string[];
}

const IDENTIFIER = /^[A-Za-z0-9_-]{1,128}$/;
const MAX_DIRECTORY_ACCOUNTS = 1000;
// At most 200 accounts per server page; stop at ten pages (2,000 accounts).
const MAX_DIRECTORY_PAGES = 10;

/** Validates the share directory; malformed entries are dropped, a malformed whole is null. */
export function parseDirectory(value: unknown): DirectoryAccount[] | null {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return null;
  const accounts = (value as Record<string, unknown>).accounts;
  if (!Array.isArray(accounts) || accounts.length > MAX_DIRECTORY_ACCOUNTS) return null;
  const strings = (list: unknown) => Array.isArray(list) && list.length <= 100 &&
    list.every(entry => typeof entry === 'string' && entry.length <= 256);
  const result: DirectoryAccount[] = [];
  for (const entry of accounts) {
    if (!entry || typeof entry !== 'object' || Array.isArray(entry)) continue;
    const account = entry as Record<string, unknown>;
    const aliases = account.aliases ?? [], domains = account.domains ?? [];
    if (typeof account.accountId !== 'string' || !IDENTIFIER.test(account.accountId) ||
        typeof account.name !== 'string' || account.name.length > 256 || !strings(aliases) || !strings(domains)) continue;
    result.push({ accountId: account.accountId, name: account.name, aliases: aliases as string[], domains: domains as string[] });
  }
  return result;
}

/** The server pages the directory; a cursor must be a later accountId. */
export function directoryCursor(value: unknown, previous: string): string | null {
  const next = value && typeof value === 'object' ? (value as Record<string, unknown>).nextCursor : undefined;
  if (next === undefined || next === '') return '';
  return typeof next === 'string' && IDENTIFIER.test(next) && next > previous ? next : null;
}

/** Reads every directory page; any bad page makes the whole directory unavailable. */
async function readDirectory(api: TtobakApi | undefined): Promise<DirectoryAccount[] | null> {
  if (!api) return null;
  const all: DirectoryAccount[] = [];
  let cursor = '';
  try {
    for (let page = 0; page < MAX_DIRECTORY_PAGES; page++) {
      const value = await api.calendarShareDirectory(cursor);
      const accounts = parseDirectory(value);
      const next = directoryCursor(value, cursor);
      if (!accounts || next === null) return null;
      all.push(...accounts);
      if (!next) return all;
      cursor = next;
    }
  } catch { return null; }
  return null;
}

export async function runCalendar(options: CalendarRunOptions): Promise<CalendarRunResult> {
  const { store, source, sourceId, zone, now, dryRun } = options;
  if (!dryRun && !options.scope) throw new IntakeError('INVALID_ARGUMENT', 'TTOBAK account scope is required');
  const state = options.scope ? loadState(store, sourceId, options.scope) : emptyState(sourceId, '0'.repeat(32));
  if (!dryRun) {
    if (!options.api) throw new IntakeError('INVALID_ARGUMENT', 'TTOBAK API is required');
    if (state.reconcileRequired && !options.reconcile) {
      throw new IntakeError('TTOBAK_CONFLICT', 'source revision conflict; run with --reconcile');
    }
    if (state.pending) await send(options.api, store, state);
    if (options.reconcile || state.revision === null) {
      state.revision = await serverRevision(options.api, sourceId);
      state.reconcileRequired = false;
      store.write(state, ...statePath(state));
    }
  }

  const window = syncWindow(now, zone);
  const items: OutlookItem[] = [];
  for (const request of viewRequests(window, zone)) items.push(...parseOutlookItems(await source.view(request)));
  // One directory read per run. Without it, events keep their previous share fields.
  const directory = await readDirectory(options.api);
  const matcher = directory ? buildMatcher(directory, options.ownDomains?.length ? options.ownDomains : DEFAULT_OWN_DOMAINS) : null;
  const facts = new Map<string, MeetingFacts | null>();
  if (matcher && options.attendees) {
    for (const item of sharingCandidates(items, window)) facts.set(item.meetingId, await options.attendees.factsFor(item));
    options.attendees.save(now);
  }
  const plan = planCalendar(items, state.ledger, window, zone, { sourceId, matcher, facts });
  plan.counts.attendeesUnavailable = options.attendees?.unavailable ?? 0;
  if (dryRun) return { counts: plan.counts, revision: state.revision, preview: plan.preview };
  if (plan.counts.sensitivityUnknown > 0 && !options.acceptUnknownSensitivity) {
    throw new IntakeError('SENSITIVITY_UNKNOWN',
      'items carry no sensitivity flag; confirm private items are hidden, then select with --accept-unknown-sensitivity');
  }

  const api = options.api!;
  const groups = batches(plan.mutations);
  if (!groups.length) groups.push([]);
  for (let index = 0; index < groups.length; index++) {
    const group = groups[index];
    const ids = new Set(group.map(mutation => mutation.op === 'upsert' ? mutation.event.externalId : mutation.externalId));
    state.pending = {
      request: {
        sourceId, batchId: randomUUID(), expectedRevision: state.revision!,
        windowStart: window.start.toISOString(), windowEnd: window.end.toISOString(),
        complete: index === groups.length - 1, mutations: group,
      },
      set: Object.fromEntries(Object.entries(plan.set).filter(([id]) => ids.has(id))),
      remove: plan.remove.filter(id => ids.has(id)),
    };
    validateCalendarSync(state.pending.request);
    store.write(state, ...statePath(state));
    await send(api, store, state);
  }
  state.ledger = pruneLedger(state.ledger, now);
  store.write(state, ...statePath(state));
  const upserted = new Map<string, OutlookItem>();
  for (const item of items) if (Object.hasOwn(plan.set, item.meetingId) && !upserted.has(item.meetingId)) upserted.set(item.meetingId, item);
  return { counts: plan.counts, revision: state.revision, upserted: [...upserted.values()] };
}
