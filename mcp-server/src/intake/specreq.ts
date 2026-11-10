// SpecReq reads, deterministic request-to-task linking and the single
// acknowledge. The only provider write here is `acknowledge_request`, called at
// most once per request ID: only after `get_request` shows NEXT_UP with me as
// resolver, and only after the pending ledger entry is durable. A later run
// decides an uncertain outcome by reading, never by calling again.

import type { McpSession } from './mcp-client.js';
import { resultJson, ToolResultError } from './mcp-client.js';
import { IntakeError } from './errors.js';
import { ackEntry, beginAck, finishAck, REQUEST_ID_PATTERN, type AckLedgerRef } from './ack-ledger.js';

export type SpecReqState = 'PENDING' | 'NEXT_UP' | 'WORKING' | 'WAITING' | 'COMPLETED' | 'CANCELLED';

export interface SpecReqSummary {
  requestId: string;
  title: string;
  stateId: SpecReqState;
  requesterLogin: string;
  resolverLogin: string;
  salesforceId: string;
  meetingDateRanges: string[];
  createdAt: number;
}

export const SEARCH_FIELDS = ['requestId', 'title', 'stateId', 'requesterLogin', 'resolverLogin', 'salesforceId',
  'meetingDateRanges', 'createdAt'] as const;
export const SEARCH_PAGE_SIZE = 50;
export const SEARCH_MAX_PAGES = 4;

const STATES: readonly SpecReqState[] = ['PENDING', 'NEXT_UP', 'WORKING', 'WAITING', 'COMPLETED', 'CANCELLED'];
/** Matches the backend's SpecReq text cap (specReqTextMaxBytes). */
const TITLE_MAX_BYTES = 512;
const MAX_RANGES = 50;
const LOGIN_PATTERN = /^[a-z0-9][a-z0-9._-]{0,63}$/;
const ACK_CONFIRMED: readonly SpecReqState[] = ['WORKING', 'WAITING', 'COMPLETED'];
const DAY_MS = 24 * 60 * 60 * 1000;

function clipUtf8(value: string, maxBytes: number): string {
  if (Buffer.byteLength(value) <= maxBytes) return value;
  let out = '';
  let bytes = 0;
  for (const character of value) {
    const size = Buffer.byteLength(character);
    if (bytes + size > maxBytes) break;
    out += character;
    bytes += size;
  }
  return out;
}

const login = (value: unknown): string => typeof value === 'string' ? value.trim().toLowerCase() : '';

/**
 * One request record, or null without a UUID `requestId` and a known `stateId`
 * (callers fail on null); any other malformed field becomes a value that can
 * never match.
 */
function parseSummary(raw: unknown): SpecReqSummary | null {
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return null;
  const r = raw as Record<string, unknown>;
  if (typeof r.requestId !== 'string' || !REQUEST_ID_PATTERN.test(r.requestId)) return null;
  if (!STATES.includes(r.stateId as SpecReqState)) return null;
  const ranges = Array.isArray(r.meetingDateRanges) && r.meetingDateRanges.length <= MAX_RANGES
    ? r.meetingDateRanges.filter((range): range is string => typeof range === 'string') : [];
  return {
    requestId: r.requestId.toLowerCase(),
    title: typeof r.title === 'string' ? clipUtf8(r.title, TITLE_MAX_BYTES) : '',
    stateId: r.stateId as SpecReqState,
    requesterLogin: login(r.requesterLogin),
    resolverLogin: login(r.resolverLogin),
    salesforceId: typeof r.salesforceId === 'string' ? r.salesforceId.trim() : '',
    meetingDateRanges: ranges,
    createdAt: typeof r.createdAt === 'number' && Number.isFinite(r.createdAt) ? r.createdAt : Number.NaN,
  };
}

function envelope(payload: unknown, tool: string): Record<string, unknown> {
  if (!payload || typeof payload !== 'object' || Array.isArray(payload)) {
    throw new IntakeError('PARSE_FAILED', `${tool} returned an unexpected shape`);
  }
  const p = payload as Record<string, unknown>;
  if (p.success !== true || !p.data || typeof p.data !== 'object' || Array.isArray(p.data)) {
    throw new IntakeError('PARSE_FAILED', `${tool} returned an unexpected shape`);
  }
  return p.data as Record<string, unknown>;
}

/** One `search_requests_advanced` page: `{ success, data: { results, lastEvaluatedIndex? } }`. */
export function parseSearch(payload: unknown): { requests: SpecReqSummary[]; next: string | null } {
  const data = envelope(payload, 'search_requests_advanced');
  if (!Array.isArray(data.results) || data.results.length > SEARCH_PAGE_SIZE) {
    throw new IntakeError('PARSE_FAILED', 'search_requests_advanced returned an unexpected shape');
  }
  const requests = data.results.map(parseSummary).filter((item): item is SpecReqSummary => item !== null);
  // A dropped record could be a competing link candidate, so a partial page is an error.
  const dropped = data.results.length - requests.length;
  if (dropped > 0) throw new IntakeError('PARSE_FAILED', `search_requests_advanced returned ${dropped} malformed records`);
  // The cursor is an opaque array such as [1791350014576]; it is sent back as JSON.
  const cursor = data.lastEvaluatedIndex;
  let next: string | null = null;
  if (typeof cursor === 'string' && cursor) next = cursor;
  else if (Array.isArray(cursor) ? cursor.length > 0 : cursor && typeof cursor === 'object') next = JSON.stringify(cursor);
  return { requests, next };
}

/** A `get_request` result: `{ success, data }` holding one request record. */
export function parseRequest(payload: unknown): SpecReqSummary {
  const summary = parseSummary(envelope(payload, 'get_request'));
  if (!summary) throw new IntakeError('PARSE_FAILED', 'get_request returned an unexpected shape');
  return summary;
}

export interface SearchFilter {
  resolverId?: string[];
  requesterId?: string[];
  stateId?: SpecReqState[];
  au_ca_gte?: number;
}

/**
 * Reads every page of one query, at most four pages of 50. A longer result is
 * an error rather than a prefix: a hidden request could break link uniqueness.
 */
export async function searchRequests(session: McpSession, filter: SearchFilter): Promise<SpecReqSummary[]> {
  const requests: SpecReqSummary[] = [];
  let next: string | null = null;
  for (let page = 0; page < SEARCH_MAX_PAGES; page++) {
    const result = await session.call('search_requests_advanced', {
      ...filter, pageSize: SEARCH_PAGE_SIZE, fields_json: JSON.stringify(SEARCH_FIELDS),
      ...(next ? { lastEvaluatedIndex_json: next } : {}),
    });
    const parsed = parseSearch(resultJson(result, 'search_requests_advanced'));
    // A full page without a cursor cannot be told apart from a truncated result.
    if (parsed.requests.length === SEARCH_PAGE_SIZE && !parsed.next) {
      throw new IntakeError('PARSE_FAILED', 'search_requests_advanced returned a full page without a cursor');
    }
    requests.push(...parsed.requests);
    next = parsed.next;
    if (!next) return requests;
  }
  throw new IntakeError('PARSE_FAILED', `search_requests_advanced exceeds ${SEARCH_MAX_PAGES} pages`);
}

const DATE_RANGE = /^(\d{4}-\d{2}-\d{2}) - (\d{4}-\d{2}-\d{2})$/;

function realDate(value: string): boolean {
  const time = Date.parse(`${value}T00:00:00Z`);
  return Number.isFinite(time) && new Date(time).toISOString().slice(0, 10) === value;
}

function localDate(instant: string, zone: string): string | null {
  const time = Date.parse(instant);
  if (!Number.isFinite(time)) return null;
  try {
    const parts = new Intl.DateTimeFormat('en-CA', { timeZone: zone, year: 'numeric', month: '2-digit', day: '2-digit' })
      .formatToParts(new Date(time));
    const part = (type: string) => parts.find(item => item.type === type)?.value ?? '';
    const date = `${part('year')}-${part('month')}-${part('day')}`;
    return /^\d{4}-\d{2}-\d{2}$/.test(date) ? date : null;
  } catch {
    return null;
  }
}

/** The occurrence's local date lies inside one inclusive `YYYY-MM-DD - YYYY-MM-DD` range. */
export function meetingInRanges(startsAt: string, zone: string, ranges: string[]): boolean {
  const date = localDate(startsAt, zone);
  if (!date || !Array.isArray(ranges)) return false;
  return ranges.some(range => {
    if (typeof range !== 'string') return false;
    const match = DATE_RANGE.exec(range);
    if (!match || !realDate(match[1]) || !realDate(match[2]) || match[1] > match[2]) return false;
    return match[1] <= date && date <= match[2];
  });
}

/** The task fields linking reads (a subset of the TTOBAK task view). */
export interface LinkTask {
  eventId: string;
  accountId?: string;
  path?: string;
  state: string;
  counterpartUserId?: string;
  specReqId?: string;
  occurrenceStartsAt: string;
  occurrenceEndsAt: string;
  createdAt: string;
}

export interface LinkRule {
  path: 'mine' | 'theirs';
  /** My SpecReq login. */
  me: string;
  /** The zone that gives the occurrence's local date. */
  zone: string;
  /** The counterpart's SpecReq login (theirs), or null when unknown. */
  counterpartLogin: (task: LinkTask) => string | null;
}

/** States in which the backend accepts `linkSpecReq` (unlinked) or `recordAck` (linked). */
const UNLINKED_STATES: Record<LinkRule['path'], readonly string[]> = {
  mine: ['issue_needed'],
  theirs: ['request_needed', 'requested'],
};
const LINKED_STATES: Record<LinkRule['path'], readonly string[]> = {
  mine: [],
  theirs: ['request_needed', 'requested', 'ack_failed'],
};

function matches(request: SpecReqSummary, task: LinkTask, opptyAccounts: Map<string, Set<string>>, rule: LinkRule): boolean {
  if (!task.accountId || !request.salesforceId || !opptyAccounts.get(request.salesforceId)?.has(task.accountId)) return false;
  if (!meetingInRanges(task.occurrenceStartsAt, rule.zone, request.meetingDateRanges)) return false;
  const me = login(rule.me);
  if (!me) return false;
  if (rule.path === 'mine') {
    if (login(request.requesterLogin) !== me) return false;
    const created = Date.parse(task.createdAt);
    const ends = Date.parse(task.occurrenceEndsAt);
    return Number.isFinite(created) && Number.isFinite(ends) && request.createdAt >= created
      && request.createdAt < ends + 7 * DAY_MS;
  }
  const counterpart = login(rule.counterpartLogin(task));
  return counterpart !== '' && login(request.requesterLogin) === counterpart && login(request.resolverLogin) === me;
}

/**
 * Pairs requests with tasks of one path. A pair counts only when it is the sole
 * candidate for both its request and its task. A request already linked to a
 * task pairs only with that task; a task linked to one request is never a
 * candidate for another (the backend pins a linked ID).
 */
export function linkRequests(requests: SpecReqSummary[], tasks: LinkTask[], opptyAccounts: Map<string, Set<string>>,
  rule: LinkRule): Map<string, string> {
  const own = tasks.filter(task => task.path === rule.path);
  const linkedTo = new Map<string, LinkTask[]>();
  for (const task of tasks) {
    if (!task.specReqId) continue;
    const id = task.specReqId.toLowerCase();
    linkedTo.set(id, [...(linkedTo.get(id) ?? []), task]);
  }
  const pairs: Array<[string, string]> = [];
  for (const request of requests) {
    const linked = linkedTo.get(request.requestId.toLowerCase());
    const candidates = !linked
      ? own.filter(task => !task.specReqId && UNLINKED_STATES[rule.path].includes(task.state))
      : linked.length === 1 && linked[0].path === rule.path && LINKED_STATES[rule.path].includes(linked[0].state)
        ? linked : [];
    for (const task of candidates) {
      if (matches(request, task, opptyAccounts, rule)) pairs.push([request.requestId, task.eventId]);
    }
  }
  const count = (values: string[]) => values.reduce((map, value) => map.set(value, (map.get(value) ?? 0) + 1),
    new Map<string, number>());
  const perRequest = count(pairs.map(([requestId]) => requestId));
  const perTask = count(pairs.map(([, eventId]) => eventId));
  const out = new Map<string, string>();
  for (const [requestId, eventId] of pairs) {
    if (perRequest.get(requestId) === 1 && perTask.get(eventId) === 1) out.set(requestId, eventId);
  }
  return out;
}

/**
 * My SpecReq login. `check_user_is_tam` must name the local part of the signed-in
 * `@amazon.com` email; anything else stops the SpecReq step.
 */
export async function resolveMe(session: McpSession, email: string): Promise<string> {
  const normalized = typeof email === 'string' ? email.trim().toLowerCase() : '';
  const match = /^([^@\s]+)@amazon\.com$/.exec(normalized);
  if (!match) throw new IntakeError('IDENTITY_MISMATCH', 'the signed-in email is not an @amazon.com address');
  const payload = resultJson(await session.call('check_user_is_tam', {}), 'check_user_is_tam');
  const data = payload && typeof payload === 'object' && (payload as Record<string, unknown>).success !== false
    ? (payload as Record<string, unknown>).data : null;
  const employee = data && typeof data === 'object' ? login((data as Record<string, unknown>).employee_login) : '';
  if (!LOGIN_PATTERN.test(employee) || employee !== match[1]) {
    throw new IntakeError('IDENTITY_MISMATCH', 'the SpecReq login differs from the signed-in identity');
  }
  return employee;
}

async function readRequest(session: McpSession, requestId: string): Promise<SpecReqSummary> {
  const request = parseRequest(resultJson(await session.call('get_request', { request_id: requestId }), 'get_request'));
  if (request.requestId !== requestId) throw new IntakeError('PARSE_FAILED', 'get_request returned another request');
  return request;
}

function ackSucceeded(result: Awaited<ReturnType<McpSession['call']>>): 'ok' | 'rejected' | 'unknown' {
  let payload: unknown;
  try { payload = resultJson(result, 'acknowledge_request'); } catch { return 'unknown'; }
  if (!payload || typeof payload !== 'object' || Array.isArray(payload)) return 'unknown';
  const success = (payload as Record<string, unknown>).success;
  return success === true ? 'ok' : success === false ? 'rejected' : 'unknown';
}

/**
 * Acknowledges one linked request at most once. An existing ledger entry (any
 * outcome) means no provider call at all; `resolveAck` decides it instead.
 */
export async function acknowledgeOnce(session: McpSession, ledger: AckLedgerRef,
  request: { requestId: string; eventId: string }, me: string): Promise<'ok' | 'rejected' | 'unknown' | 'not_eligible'> {
  const requestId = typeof request.requestId === 'string' ? request.requestId.toLowerCase() : '';
  if (!REQUEST_ID_PATTERN.test(requestId)) throw new IntakeError('INVALID_ARGUMENT', 'SpecReq request ID is invalid');
  const now = () => ledger.now?.() ?? new Date();
  const existing = ackEntry(ledger.store, ledger.scope, requestId);
  if (existing) return existing.outcome === 'pending' ? 'unknown' : existing.outcome;
  const myLogin = login(me);
  const current = await readRequest(session, requestId);
  if (!myLogin || current.stateId !== 'NEXT_UP' || current.resolverLogin !== myLogin) return 'not_eligible';
  beginAck(ledger.store, ledger.scope, requestId, request.eventId, now());
  let outcome: 'ok' | 'rejected' | 'unknown';
  try {
    outcome = ackSucceeded(await session.call('acknowledge_request', { request_id: requestId }));
  } catch (error) {
    // Only a tool-level error is a definite refusal; a timeout, crash or
    // authentication failure may have reached the provider.
    outcome = error instanceof ToolResultError ? 'rejected' : 'unknown';
  }
  finishAck(ledger.store, ledger.scope, requestId, outcome);
  return outcome;
}

export type AckResolution = { state: 'acked' } | { state: 'ack_failed'; failureCode: 'ACK_UNCONFIRMED' | 'ACK_REJECTED' };

/**
 * Decides a recorded attempt by reading the request. WORKING, WAITING or
 * COMPLETED with me as resolver confirms it (the entry becomes `ok`); anything
 * else is `ack_failed`. Never calls `acknowledge_request`.
 */
export async function resolveAck(session: McpSession, ledger: AckLedgerRef, requestId: string,
  me: string): Promise<AckResolution> {
  const id = typeof requestId === 'string' ? requestId.toLowerCase() : '';
  if (!REQUEST_ID_PATTERN.test(id)) throw new IntakeError('INVALID_ARGUMENT', 'SpecReq request ID is invalid');
  const entry = ackEntry(ledger.store, ledger.scope, id);
  if (!entry) throw new IntakeError('STORE_INVALID', 'no acknowledge attempt is recorded for this request');
  if (entry.outcome === 'ok') return { state: 'acked' };
  const current = await readRequest(session, id);
  const myLogin = login(me);
  if (myLogin && current.resolverLogin === myLogin && ACK_CONFIRMED.includes(current.stateId)) {
    finishAck(ledger.store, ledger.scope, id, 'ok');
    return { state: 'acked' };
  }
  return { state: 'ack_failed', failureCode: entry.outcome === 'rejected' ? 'ACK_REJECTED' : 'ACK_UNCONFIRMED' };
}
