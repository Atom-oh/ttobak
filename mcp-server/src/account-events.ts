import type { Tool } from '@modelcontextprotocol/sdk/types.js';
import { MAX_READING_BYTES } from './reading.js';

// Account events are shared schedules of one customer account, visible to its
// direct members. They are separate from the private calendar tools.
const accountEventFields = ['title', 'startAt', 'endAt', 'allDay', 'location', 'attendees', 'notes'] as const;
const accountEventAttendeeFields = ['contactId', 'name', 'email'] as const;
export type EventAttendeeInput = { contactId?: string; name: string; email?: string };
export type EventInput = {
  title: string; startAt: string; endAt?: string; allDay?: boolean;
  location?: string; attendees?: EventAttendeeInput[]; notes?: string;
};
export type EventListOptions = { from?: string; to?: string; order?: 'asc' | 'desc'; limit: number; cursor?: string };
type EventApi = {
  listAccountEvents(accountId: string, options: EventListOptions): Promise<unknown>;
  createAccountEvent(accountId: string, event: EventInput, idempotencyKey?: string): Promise<unknown>;
  readonly requiresEventIdempotency?: boolean;
};

function eventOrderedFields<T extends readonly string[]>(source: Record<string, unknown>, keys: T): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const key of keys) if (Object.hasOwn(source, key)) out[key] = source[key];
  return out;
}

// The private bridge binds an idempotent write to the exact request bytes, so
// a retry must serialize the same event identically regardless of the
// caller's property order: schema order at every level.
export function canonicalEvent(event: EventInput): EventInput {
  const out = eventOrderedFields(event as unknown as Record<string, unknown>, accountEventFields);
  if (Array.isArray(event.attendees)) out.attendees = event.attendees.map(a => eventOrderedFields(a as unknown as Record<string, unknown>, accountEventAttendeeFields));
  return out as unknown as EventInput;
}

const EVENT_TIME = { type: 'string', minLength: 20, maxLength: 40, description: 'RFC 3339 with offset, e.g. 2026-11-10T14:00:00+09:00' };
const EVENT_ATTENDEE = {
  type: 'object', additionalProperties: false, required: ['name'], properties: {
    contactId: { type: 'string', description: 'Optional contact ID from ttobak_list_account_contacts in the same account' },
    name: { type: 'string', minLength: 1, maxLength: 256 }, email: { type: 'string', maxLength: 256 },
  },
};
const EVENT_SCHEMA = {
  type: 'object', additionalProperties: false, required: ['title', 'startAt'], properties: {
    title: { type: 'string', minLength: 1, maxLength: 256 }, startAt: EVENT_TIME, endAt: EVENT_TIME,
    allDay: { type: 'boolean' }, location: { type: 'string', maxLength: 512 },
    attendees: { type: 'array', maxItems: 50, items: EVENT_ATTENDEE }, notes: { type: 'string', maxLength: 8192 },
  },
};
const EVENT_ACCOUNT_ID = { type: 'string', minLength: 1, maxLength: 128 };
const EVENT_IDEMPOTENCY_KEY = { type: 'string', minLength: 1, maxLength: 128, pattern: '^[A-Za-z0-9_-]+$', description: 'Stable retry key; required for private OAuth connector writes.' };

export const ACCOUNT_EVENT_TOOLS: Tool[] = [
  {
    name: 'ttobak_list_account_events',
    description: 'List one page of an authorized account\'s shared events (schedules). Without from, ascending order lists upcoming events. For past events use order desc with to set to now. Pass the returned nextCursor as cursor for more.',
    annotations: { readOnlyHint: true },
    inputSchema: { type: 'object', additionalProperties: false, required: ['accountId'], properties: {
      accountId: EVENT_ACCOUNT_ID, from: EVENT_TIME, to: EVENT_TIME, order: { type: 'string', enum: ['asc', 'desc'] },
      limit: { type: 'integer', minimum: 1, maximum: 25, default: 25 }, cursor: { type: 'string', minLength: 1, maxLength: 1024 } } },
  },
  {
    name: 'ttobak_create_account_event',
    description: 'Add one shared event (schedule) to an authorized account. Attendees may reference the account\'s contacts by contactId.',
    inputSchema: { type: 'object', additionalProperties: false, required: ['accountId', 'event'], properties: {
      accountId: EVENT_ACCOUNT_ID, event: EVENT_SCHEMA, idempotencyKey: EVENT_IDEMPOTENCY_KEY } },
  },
];

function isEventRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}
function eventAccountId(args: Record<string, unknown>, keys: string[]): string {
  if (Object.keys(args).some(key => !keys.includes(key)) || typeof args.accountId !== 'string' ||
      !/^[A-Za-z0-9_-]{1,128}$/.test(args.accountId)) throw new Error('INVALID_ARGUMENT: invalid account or options');
  return args.accountId;
}
const eventTextFits = (v: unknown, max: number) => typeof v === 'string' && Buffer.byteLength(v) <= max;
function eventToolInput(value: unknown): EventInput {
  if (!isEventRecord(value) || Object.keys(value).some(k => !accountEventFields.includes(k as typeof accountEventFields[number])) ||
      typeof value.title !== 'string' || !value.title.trim() || !eventTextFits(value.title, 256) || !eventTextFits(value.startAt, 40) ||
      (value.endAt !== undefined && !eventTextFits(value.endAt, 40)) || (value.allDay !== undefined && typeof value.allDay !== 'boolean') ||
      (value.location !== undefined && !eventTextFits(value.location, 512)) || (value.notes !== undefined && !eventTextFits(value.notes, 8192)) ||
      (value.attendees !== undefined && (!Array.isArray(value.attendees) || value.attendees.length > 50 || value.attendees.some(a =>
        !isEventRecord(a) || Object.keys(a).some(k => !accountEventAttendeeFields.includes(k as typeof accountEventAttendeeFields[number])) ||
        typeof a.name !== 'string' || !a.name.trim() || !eventTextFits(a.name, 256) ||
        (a.email !== undefined && !eventTextFits(a.email, 256)) || (a.contactId !== undefined && (typeof a.contactId !== 'string' || !/^[0-9a-f-]{36}$/.test(a.contactId)))))) ||
      Buffer.byteLength(JSON.stringify(value)) > 12_000) throw new Error('INVALID_EVENT');
  return value as unknown as EventInput;
}
function eventListOptions(args: Record<string, unknown>): EventListOptions {
  const limit = args.limit ?? 25;
  if (!Number.isInteger(limit) || (limit as number) < 1 || (limit as number) > 25 ||
      (args.from !== undefined && !eventTextFits(args.from, 40)) || (args.to !== undefined && !eventTextFits(args.to, 40)) ||
      (args.order !== undefined && args.order !== 'asc' && args.order !== 'desc') ||
      (args.cursor !== undefined && (typeof args.cursor !== 'string' || !args.cursor.length || args.cursor.length > 1024))) {
    throw new Error('INVALID_ARGUMENT: invalid event list options');
  }
  const out: EventListOptions = { limit: limit as number };
  for (const key of ['from', 'to', 'order', 'cursor'] as const) if (args[key] !== undefined) (out as Record<string, unknown>)[key] = args[key];
  return out;
}
function boundedEventResult(value: unknown) {
  const result = { content: [{ type: 'text' as const, text: JSON.stringify(value) }] };
  if (Buffer.byteLength(JSON.stringify(result)) > MAX_READING_BYTES) throw new Error('RESULT_TOO_LARGE: reduce the event page limit');
  return result;
}
function eventPage(value: unknown, limit: number) {
  if (!isEventRecord(value) || !Array.isArray(value.events) || value.events.length > limit ||
      value.events.some(e => !isEventRecord(e) || typeof e.eventId !== 'string' || typeof e.title !== 'string' || typeof e.startAt !== 'string') ||
      (value.nextCursor !== undefined && value.nextCursor !== null && (typeof value.nextCursor !== 'string' || !value.nextCursor.length || value.nextCursor.length > 1024))) {
    throw new Error('INVALID_RESPONSE: invalid event page');
  }
  return value;
}
function eventHttpStatus(error: unknown): string | undefined {
  return error instanceof Error ? /^HTTP (\d{3})(?:\s|:)/.exec(error.message)?.[1] : undefined;
}

export async function callAccountEventTool(name: string, args: Record<string, unknown>, api: EventApi) {
  if (name === 'ttobak_list_account_events') {
    const accountId = eventAccountId(args, ['accountId', 'from', 'to', 'order', 'limit', 'cursor']);
    const options = eventListOptions(args);
    let limit = options.limit;
    for (;;) {
      try { return boundedEventResult(eventPage(await api.listAccountEvents(accountId, { ...options, limit }), limit)); }
      catch (e) {
        if (e instanceof Error && /^(READING_LIMIT|RESULT_TOO_LARGE):/.test(e.message) && limit > 1) { limit = Math.floor(limit / 2); continue; }
        if (e instanceof Error && /^(READING_LIMIT|RESULT_TOO_LARGE|INVALID_RESPONSE):/.test(e.message)) throw e;
        const code = eventHttpStatus(e);
        throw new Error(code ? `EVENT_READ_FAILED: HTTP ${code}` : 'EVENT_READ_FAILED: unable to read event page');
      }
    }
  }
  const accountId = eventAccountId(args, ['accountId', 'event', 'idempotencyKey']);
  const input = eventToolInput(args.event);
  const key = args.idempotencyKey;
  if (key !== undefined && api.requiresEventIdempotency === false) throw new Error('INVALID_ARGUMENT: idempotencyKey requires private connector transport');
  if ((key !== undefined || api.requiresEventIdempotency) && (typeof key !== 'string' || !/^[A-Za-z0-9_-]{1,128}$/.test(key))) throw new Error('INVALID_ARGUMENT: idempotencyKey required');
  let value: unknown;
  try { value = await api.createAccountEvent(accountId, input, key as string | undefined); }
  catch (e) {
    const code = eventHttpStatus(e);
    // A 4xx (other than a timeout or a pre-call private denial) is definitive.
    if (code && code !== '408' && !(api.requiresEventIdempotency && code === '403') && Number(code) >= 400 && Number(code) < 500) throw new Error(`EVENT_WRITE_FAILED: HTTP_${code}`);
    throw new Error(api.requiresEventIdempotency ? 'EVENT_WRITE_FAILED: outcome unknown; retry the same idempotencyKey and event' : 'EVENT_WRITE_FAILED: outcome unknown; list events before retrying');
  }
  if (!isEventRecord(value) || typeof value.eventId !== 'string' || !/^[A-Za-z0-9_-]{1,128}$/.test(value.eventId)) {
    throw new Error('EVENT_WRITE_FAILED: outcome unknown; list events before retrying');
  }
  return boundedEventResult({ eventId: value.eventId, ...(value.replayed === true ? { replayed: true } : {}) });
}
