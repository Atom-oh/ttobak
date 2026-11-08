import type { CallToolResult, Tool } from '@modelcontextprotocol/sdk/types.js';
import { ApiResponseError, type TtobakApi } from './api.js';
import { readingError } from './reading.js';

export const MAX_CALENDAR_API_BYTES = 14_000;
const MAX_RESULT_BYTES = 32_000;
const MAX_REVISION = Number.MAX_SAFE_INTEGER - 1;
const UUID_PATTERN = '^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$';
const uuidSchema = { type: 'string', pattern: UUID_PATTERN, description: 'Nonzero UUID; either letter case is accepted.' };
const timestampSchema = { type: 'string', format: 'date-time', description: 'RFC3339 instant, including offsets and fractional seconds.' };
const identifierSchema = { type: 'string', pattern: '^[A-Za-z0-9_-]{1,128}$' };
const textSchema = (bytes: number, required = true) => ({
  type: 'string', minLength: required ? 1 : 0, maxLength: bytes,
  description: `Preserved UTF-8 text, at most ${bytes} UTF-8 bytes.${required ? ' Must not be blank.' : ''}`,
});
const eventFields = {
  externalId: textSchema(512), title: textSchema(512), startsAt: timestampSchema, endsAt: timestampSchema,
  timeZone: { ...textSchema(128), description: 'IANA time zone or UTC; Local is not accepted. Map provider zones locally.' },
  isAllDay: { type: 'boolean', default: false },
  startDate: { type: 'string', format: 'date', description: 'All-day local start date at midnight.' },
  endDate: { type: 'string', format: 'date', description: 'All-day exclusive local end date at midnight.' },
  location: textSchema(256, false), accountId: identifierSchema,
};
const requiredEventFields = ['externalId', 'title', 'startsAt', 'endsAt', 'timeZone'];
const syncFields = {
  sourceId: uuidSchema, batchId: uuidSchema,
  expectedRevision: { type: 'integer', minimum: 0, maximum: MAX_REVISION,
    description: 'Exact expected revision. The next receipt revision must also be a safe JavaScript integer.' },
  windowStart: timestampSchema, windowEnd: timestampSchema, complete: { type: 'boolean' },
  mutations: {
    type: 'array', maxItems: 20, description: 'At most 20 distinct occurrence IDs. Only explicit removals delete events.',
    items: { oneOf: [
      { type: 'object', properties: { op: { const: 'upsert' }, event: {
        type: 'object', properties: eventFields, required: requiredEventFields, additionalProperties: false,
        allOf: [
          { if: { properties: { isAllDay: { const: true } }, required: ['isAllDay'] },
            then: { required: ['startDate', 'endDate'] },
            else: { not: { anyOf: [{ required: ['startDate'] }, { required: ['endDate'] }] } } },
        ],
      } }, required: ['op', 'event'], additionalProperties: false },
      { type: 'object', properties: { op: { const: 'remove' }, externalId: textSchema(512) },
        required: ['op', 'externalId'], additionalProperties: false },
    ] },
  },
};
const pageFields = {
  limit: { type: 'integer', minimum: 1, maximum: 50,
    description: 'Events default to 50; sources default to 20 and cap effective limits at 20. Retain when paging.' },
  cursor: { ...textSchema(4096), description: 'Opaque server nextCursor, at most 4096 UTF-8 bytes. Preserve the query scope.' },
};

export const CALENDAR_TOOLS: Tool[] = [
  {
    name: 'ttobak_sync_calendar_events',
    description: 'Locally sync caller-supplied Outlook occurrences to your private calendar. Requires user intent. ' +
      'Use stable occurrence-level external IDs and a positive window of at most 93 elapsed days. ' +
      'An empty complete batch reports progress without inferring deletions. ' +
      'On ambiguous delivery, retry the identical batch ID, expected revision, source and full payload. ' +
      'Returns the actual server receipt; never fetches Outlook or creates meetings.',
    annotations: { readOnlyHint: false, destructiveHint: true, idempotentHint: true },
    inputSchema: { type: 'object', properties: syncFields, required: Object.keys(syncFields), additionalProperties: false },
  },
  {
    name: 'ttobak_list_calendar_events',
    description: 'Read your private calendar in a positive window of at most 93 elapsed days. ' +
      'Follow actual nextCursor with unchanged query scope, including empty pages, until null.',
    annotations: { readOnlyHint: true },
    inputSchema: { type: 'object', properties: { from: timestampSchema, to: timestampSchema,
      accountId: identifierSchema, ...pageFields }, required: ['from', 'to'], additionalProperties: false },
  },
  {
    name: 'ttobak_calendar_sync_status',
    description: 'Read private calendar source progress. Follow nextCursor until null. ' +
      'Compare revision and optional lastCompletedRevision for partial progress; completion is caller-reported, not verified provider freshness.',
    annotations: { readOnlyHint: true },
    inputSchema: { type: 'object', properties: pageFields, additionalProperties: false },
  },
];
export const CALENDAR_TOOL_NAMES: string[] = CALENDAR_TOOLS.map(tool => tool.name);

type RecordValue = Record<string, unknown>;
export interface CalendarSyncRequest {
  batchId: string;
  expectedRevision: number;
  windowStart: string;
  windowEnd: string;
  complete: boolean;
  mutations: RecordValue[];
}
export interface CalendarPageQuery { limit?: number; cursor?: string }
export interface CalendarEventQuery extends CalendarPageQuery { from: string; to: string; accountId?: string }

function record(value: unknown): value is RecordValue {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function invalid(field: string): never {
  throw new Error(`INVALID_ARGUMENT: invalid calendar ${field}`);
}

function exact(value: unknown, allowed: string[], required: string[]): asserts value is RecordValue {
  if (!record(value) || Object.keys(value).some(key => !allowed.includes(key) || value[key] == null) ||
      required.some(key => !Object.hasOwn(value, key))) invalid('fields');
}

function validText(value: unknown, bytes: number, nonblank = true): value is string {
  return typeof value === 'string' && Buffer.byteLength(value, 'utf8') <= bytes &&
    Buffer.from(value, 'utf8').toString('utf8') === value &&
    (!nonblank || /[^\t\n\v\f\r \u0085\u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000]/.test(value));
}

function validUUID(value: unknown): value is string {
  return typeof value === 'string' && new RegExp(UUID_PATTERN).test(value) &&
    value !== '00000000-0000-0000-0000-000000000000';
}

function instant(value: unknown): { nanos: bigint; utc: string; date: Date } {
  if (typeof value !== 'string') invalid('timestamp');
  const match = /^(\d{4}-\d{2}-\d{2})T(\d{2}):(\d{2}):(\d{2})(?:[.,](\d+))?(Z|([+-])(\d{2}):(\d{2}))$/.exec(value);
  if (!match) invalid('RFC3339 timestamp');
  const [, day, hour, minute, second, fraction = '', zone, sign, offsetHour = '0', offsetMinute = '0'] = match;
  const date = new Date(`${day}T${hour}:${minute}:${second}Z`);
  if (!Number.isFinite(date.getTime()) || date.toISOString().slice(0, 19) !== `${day}T${hour}:${minute}:${second}` ||
      Number(hour) > 23 || Number(minute) > 59 || Number(second) > 59 ||
      Number(offsetHour) > 23 || Number(offsetMinute) > 59) invalid('timestamp');
  const offset = zone === 'Z' ? 0 : (Number(offsetHour) * 60 + Number(offsetMinute)) * (sign === '+' ? 1 : -1);
  date.setTime(date.getTime() - offset * 60_000);
  if (date.getUTCFullYear() < 1 || date.getUTCFullYear() > 9999) invalid('timestamp year');
  const nanoFraction = fraction.slice(0, 9).padEnd(9, '0');
  const nanos = BigInt(date.getTime()) * 1_000_000n + BigInt(nanoFraction);
  const trimmed = nanoFraction.replace(/0+$/, '');
  return { nanos, date, utc: date.toISOString().slice(0, 19) + (trimmed ? '.' + trimmed : '') + 'Z' };
}

function interval(start: unknown, end: unknown) {
  const first = instant(start), last = instant(end);
  if (first.utc === '0001-01-01T00:00:00Z' || last.nanos <= first.nanos ||
      last.nanos - first.nanos > 93n * 24n * 60n * 60n * 1_000_000_000n) invalid('interval (maximum 93 days)');
  return { first, last };
}

function eventInput(value: unknown): asserts value is RecordValue {
  exact(value, Object.keys(eventFields), requiredEventFields);
  for (const field of ['externalId', 'title']) if (!validText(value[field], 512)) invalid(field);
  if ('location' in value && !validText(value.location, 256, false)) invalid('location');
  if ('accountId' in value && (typeof value.accountId !== 'string' || !/^[A-Za-z0-9_-]{1,128}$/.test(value.accountId))) invalid('accountId');
  if (!validText(value.timeZone, 128) || value.timeZone === 'Local' || /^[+-]/.test(value.timeZone)) invalid('timeZone');
  if ('isAllDay' in value && typeof value.isAllDay !== 'boolean') invalid('isAllDay');
  let formatter: Intl.DateTimeFormat;
  try {
    formatter = new Intl.DateTimeFormat('en-US', { timeZone: value.timeZone, year: 'numeric', month: '2-digit',
      day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit', hourCycle: 'h23' });
  } catch { invalid('timeZone'); }
  const { first, last } = interval(value.startsAt, value.endsAt);
  if (value.isAllDay === true) {
    for (const [field, timestamp] of [['startDate', first], ['endDate', last]] as const) {
      const date = value[field];
      if (typeof date !== 'string' || !/^\d{4}-\d{2}-\d{2}$/.test(date) ||
          timestamp.nanos % 1_000_000_000n !== 0n) invalid('all-day date');
      const parts = Object.fromEntries(formatter.formatToParts(timestamp.date).map(part => [part.type, part.value]));
      if (`${parts.year.padStart(4, '0')}-${parts.month}-${parts.day}` !== date ||
          parts.hour !== '00' || parts.minute !== '00' || parts.second !== '00') invalid('all-day midnight');
    }
  } else if ('startDate' in value || 'endDate' in value) invalid('timed event dates');
  const payload: RecordValue = { ...value, startsAt: first.utc, endsAt: last.utc, isAllDay: value.isAllDay ?? false };
  for (const field of ['location', 'accountId', 'startDate', 'endDate']) {
    if (payload[field] === '') delete payload[field];
  }
  const encoded = JSON.stringify(payload) + '\n';
  const separatorBytes = Array.from(encoded).filter(character => [8232, 8233].includes(character.charCodeAt(0))).length * 3;
  if (Buffer.byteLength(encoded) + separatorBytes > 4096) invalid('encrypted event payload byte limit');
}

function syncInput(args: RecordValue): { sourceId: string; body: CalendarSyncRequest } {
  exact(args, Object.keys(syncFields), Object.keys(syncFields));
  if (!validUUID(args.sourceId) || !validUUID(args.batchId)) invalid('sourceId or batchId');
  if (!Number.isSafeInteger(args.expectedRevision) || (args.expectedRevision as number) < 0 ||
      (args.expectedRevision as number) > MAX_REVISION) invalid('expectedRevision');
  interval(args.windowStart, args.windowEnd);
  if (typeof args.complete !== 'boolean' || !Array.isArray(args.mutations) || args.mutations.length > 20) invalid('batch');
  const seen = new Set<string>();
  for (const mutation of args.mutations) {
    if (!record(mutation)) invalid('mutation');
    let externalId: string;
    if (mutation.op === 'upsert') {
      exact(mutation, ['op', 'event'], ['op', 'event']);
      eventInput(mutation.event);
      externalId = mutation.event.externalId as string;
    } else if (mutation.op === 'remove') {
      exact(mutation, ['op', 'externalId'], ['op', 'externalId']);
      if (!validText(mutation.externalId, 512)) invalid('externalId');
      externalId = mutation.externalId;
    } else invalid('mutation operation');
    if (seen.has(externalId)) invalid('duplicate externalId');
    seen.add(externalId);
  }
  const { sourceId, ...body } = args;
  const serialized = JSON.stringify(body);
  if (Buffer.byteLength(serialized) > 65_536) invalid('sync request byte limit');
  return { sourceId, body: JSON.parse(serialized) as CalendarSyncRequest };
}

function queryInput(args: RecordValue, events: boolean): CalendarPageQuery | CalendarEventQuery {
  exact(args, events ? ['from', 'to', 'accountId', 'limit', 'cursor'] : ['limit', 'cursor'], events ? ['from', 'to'] : []);
  if ('limit' in args && (!Number.isInteger(args.limit) || (args.limit as number) < 1 || (args.limit as number) > 50)) invalid('limit');
  if ('cursor' in args && !validText(args.cursor, 4096, false)) invalid('cursor');
  if (args.cursor === '') invalid('cursor');
  if (events) {
    interval(args.from, args.to);
    if ('accountId' in args && (typeof args.accountId !== 'string' || !/^[A-Za-z0-9_-]{1,128}$/.test(args.accountId))) invalid('accountId');
  }
  const query = new URLSearchParams(Object.entries(args).map(([key, value]) => [key, String(value)]));
  if (Buffer.byteLength(query.toString()) > 8192) invalid('encoded query byte limit');
  return { ...args } as CalendarPageQuery | CalendarEventQuery;
}

function calendarResult(value: unknown, kind: 'sync' | 'events' | 'sources', input?: CalendarSyncRequest): CallToolResult {
  const invalidResponse = () => { throw new Error('INVALID_CALENDAR_RESPONSE: invalid receipt or page'); };
  if (!record(value) || value.error) invalidResponse();
  const result = value as RecordValue;
  if (kind === 'sync') {
    if (!input || result.batchId !== input.batchId.toLowerCase() || !Number.isSafeInteger(result.revision) ||
        result.revision !== input.expectedRevision + 1 || result.completed !== input.complete ||
        result.upserted !== input.mutations.filter(mutation => mutation.op === 'upsert').length ||
        result.removed !== input.mutations.filter(mutation => mutation.op === 'remove').length) invalidResponse();
  } else {
    const entries = result[kind];
    if (!Array.isArray(entries) || entries.length > (kind === 'events' ? 50 : 20) ||
        !(result.nextCursor === null || validText(result.nextCursor, 4096))) invalidResponse();
    for (const entry of entries as unknown[]) {
      if (!record(entry) || !validUUID(entry.sourceId) || typeof entry.provider !== 'string') invalidResponse();
      const row = entry as RecordValue;
      if (kind === 'sources' && (!Number.isSafeInteger(row.revision) || (row.revision as number) < 0 ||
          ('lastCompletedRevision' in row && (!Number.isSafeInteger(row.lastCompletedRevision) ||
            (row.lastCompletedRevision as number) < 0 || (row.lastCompletedRevision as number) > (row.revision as number))))) invalidResponse();
    }
  }
  const output: CallToolResult = { content: [{ type: 'text', text: JSON.stringify(value) }] };
  if (Buffer.byteLength(JSON.stringify(output)) > MAX_RESULT_BYTES) {
    throw new Error('CALENDAR_LIMIT: serialized tool result exceeds 32000 bytes');
  }
  return output;
}

export async function callCalendarTool(name: string, args: Record<string, unknown>, api: TtobakApi): Promise<CallToolResult> {
  let syncAttempted = false;
  try {
    switch (name) {
      case 'ttobak_sync_calendar_events': {
        const { sourceId, body } = syncInput(args);
        syncAttempted = true;
        return calendarResult(await api.syncCalendarEvents(sourceId, body), 'sync', body);
      }
      case 'ttobak_list_calendar_events':
        return calendarResult(await api.listCalendarEvents(queryInput(args, true) as CalendarEventQuery), 'events');
      case 'ttobak_calendar_sync_status':
        return calendarResult(await api.calendarSyncStatus(queryInput(args, false)), 'sources');
      default:
        return readingError('Unknown calendar tool');
    }
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    if (!syncAttempted) return readingError(message);
    if (error instanceof ApiResponseError && error.status >= 400 && error.status < 500) {
      return readingError((error.status === 409
        ? 'Calendar sync was rejected. Refetch source status and reconcile before syncing again. '
        : '') + message);
    }
    return readingError('Calendar sync is unconfirmed. Retry the identical batch ID and expected revision with unchanged source and full payload. ' + message);
  }
}
