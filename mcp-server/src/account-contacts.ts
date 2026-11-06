import { createHash } from 'node:crypto';
import type { Tool } from '@modelcontextprotocol/sdk/types.js';
import { MAX_READING_BYTES } from './reading.js';

// Staged rollout: this adapter is intentionally not registered as an MCP tool
// yet. The dependent PR #607 registers its tools and adds discovery/dispatch
// coverage; until then, existing server capabilities remain unchanged.
const fields = ['name', 'company', 'title', 'department', 'email', 'address', 'workPhone', 'fax', 'phone', 'cardRegisteredAt', 'cardBook', 'group', 'notes'] as const;
export type ContactInput = { name: string } & Partial<Record<typeof fields[number], string>>;
export type ContactListOptions = { limit: number; cursor?: string };
type ContactApi = {
  listAccountContacts(accountId: string, options: ContactListOptions): Promise<unknown>;
  createAccountContact(accountId: string, contact: ContactInput, idempotencyKey?: string): Promise<unknown>;
  readonly requiresContactIdempotency?: boolean;
  replayAccountContact?(accountId: string, contact: ContactInput, idempotencyKey: string): Promise<unknown>;
};
const MAX_ROWS = 25;
const MAX_SCAN_PAGES = 1000;
const fieldLimit = (field: string) => field === 'notes' ? 8192 : field === 'address' ? 1024 : 256;
const CONTACT_PROPERTIES = Object.fromEntries(fields.map(field => [field, {
  type: 'string', maxLength: fieldLimit(field), description: `Maximum ${fieldLimit(field)} UTF-8 bytes`,
}]));
const CONTACT_SCHEMA = { type: 'object', properties: CONTACT_PROPERTIES, required: ['name'], additionalProperties: false };
const ACCOUNT_ID = { type: 'string', minLength: 1, maxLength: 128 };
const IDEMPOTENCY_KEY = { type: 'string', minLength: 1, maxLength: 128, pattern: '^[A-Za-z0-9_-]+$', description: 'Stable retry key; required for private OAuth connector writes.' };

export const ACCOUNT_CONTACT_TOOLS: Tool[] = [
  {
    name: 'ttobak_list_account_contacts',
    description: 'List one authorized account contact page. Follow nextCursor for all pages. Reduce limit if the result exceeds the MCP byte limit.',
    annotations: { readOnlyHint: true },
    inputSchema: { type: 'object', properties: { accountId: ACCOUNT_ID,
      limit: { type: 'integer', minimum: 1, maximum: 25, default: 25 }, cursor: { type: 'string', minLength: 1, maxLength: 2048 } },
      required: ['accountId'], additionalProperties: false },
  },
  {
    name: 'ttobak_create_account_contact',
    description: 'Create one contact in an authorized account using structured fields.',
    inputSchema: { type: 'object', properties: { accountId: ACCOUNT_ID, contact: CONTACT_SCHEMA, idempotencyKey: IDEMPOTENCY_KEY },
      required: ['accountId', 'contact'], additionalProperties: false },
  },
  {
    name: 'ttobak_import_account_contacts',
    description: 'Import 1–25 structured contact records. Scans all authorized pages first and skips matching normalized emails or phone numbers. Returns one outcome per row; concurrent writers can still race duplicate checks. Parse files on the client.',
    inputSchema: { type: 'object', properties: { accountId: ACCOUNT_ID,
      contacts: { type: 'array', minItems: 1, maxItems: MAX_ROWS, items: CONTACT_SCHEMA }, idempotencyKey: IDEMPOTENCY_KEY },
      required: ['accountId', 'contacts'], additionalProperties: false },
  },
];

function record(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}
function argsFor(args: Record<string, unknown>, keys: string[]): string {
  if (Object.keys(args).some(key => !keys.includes(key)) || typeof args.accountId !== 'string' ||
      !/^[A-Za-z0-9_-]{1,128}$/.test(args.accountId)) throw new Error('INVALID_ARGUMENT: invalid account or options');
  return args.accountId;
}
function contactInput(value: unknown): ContactInput {
  if (!record(value) || Object.keys(value).some(k => !fields.includes(k as typeof fields[number])) ||
      typeof value.name !== 'string' || !value.name.trim()) throw new Error('INVALID_CONTACT');
  for (const [key, v] of Object.entries(value)) {
    if (typeof v !== 'string' || Buffer.byteLength(v) > fieldLimit(key)) throw new Error('INVALID_CONTACT');
  }
  if (Buffer.byteLength(JSON.stringify(value)) > 64 * 1024) throw new Error('INVALID_CONTACT');
  return value as ContactInput;
}
function pageOptions(args: Record<string, unknown>): ContactListOptions {
  const limit = args.limit ?? 25;
  if (!Number.isInteger(limit) || (limit as number) < 1 || (limit as number) > 25 ||
      (args.cursor !== undefined && (typeof args.cursor !== 'string' || !args.cursor.length || args.cursor.length > 2048))) {
    throw new Error('INVALID_ARGUMENT: invalid contact pagination');
  }
  return { limit: limit as number, ...(args.cursor === undefined ? {} : { cursor: args.cursor as string }) };
}
function page(value: unknown, limit: number): { contacts: Record<string, unknown>[]; nextCursor?: string | null } {
  if (!record(value) || !Array.isArray(value.contacts) || value.contacts.length > limit ||
      value.contacts.some(contact => !record(contact) || typeof contact.name !== 'string' ||
        ['email', 'phone', 'workPhone'].some(key => contact[key] !== undefined && typeof contact[key] !== 'string')) ||
      (value.nextCursor !== undefined && value.nextCursor !== null &&
        (typeof value.nextCursor !== 'string' || value.nextCursor.length === 0 || value.nextCursor.length > 2048))) {
    throw new Error('INVALID_RESPONSE: invalid contact page');
  }
  bounded(value);
  return value as { contacts: Record<string, unknown>[]; nextCursor?: string | null };
}
function bounded(value: unknown) {
  const result = { content: [{ type: 'text' as const, text: JSON.stringify(value) }] };
  if (Buffer.byteLength(JSON.stringify(result)) > MAX_READING_BYTES) throw new Error('RESULT_TOO_LARGE: reduce the contact page limit');
  return result;
}
function keys(contact: Record<string, unknown>) {
  const email = typeof contact.email === 'string' ? contact.email.trim().toLowerCase() : '';
  const phones = ['phone', 'workPhone'].map(field => typeof contact[field] === 'string' ? (contact[field] as string).replace(/[^0-9]/g, '').replace(/^82(?=[1-9])/, '0') : '').filter(Boolean);
  return { email, phones };
}
function retryKey(args: Record<string, unknown>, api: ContactApi): string | undefined {
  const key = args.idempotencyKey;
  if (key === undefined && !api.requiresContactIdempotency) return undefined;
  if (key !== undefined && api.requiresContactIdempotency === false) throw new Error('INVALID_ARGUMENT: idempotencyKey requires private connector transport');
  if (typeof key !== 'string' || !/^[A-Za-z0-9_-]{1,128}$/.test(key)) throw new Error('INVALID_ARGUMENT: idempotencyKey required');
  return key;
}
function failure(error: unknown, privateWrite = false): { code: string; outcome?: string } {
  const status = error instanceof Error ? /^HTTP (\d{3})(?:\s|:)/.exec(error.message)?.[1] : undefined;
  if (status && status !== '408' && !(privateWrite && status === '403') && Number(status) >= 400 && Number(status) < 500) return { code: `HTTP_${status}` };
  return { code: 'WRITE_FAILED', outcome: 'unknown' };
}
function safeReadError(error: unknown): Error {
  const status = error instanceof Error ? /^HTTP (\d{3})(?:\s|:)/.exec(error.message)?.[1] : undefined;
  return new Error(status ? `CONTACT_READ_FAILED: HTTP ${status}` : 'CONTACT_READ_FAILED: unable to read contact page');
}

export async function callAccountContactTool(name: string, args: Record<string, unknown>, api: ContactApi) {
  if (name === 'ttobak_list_account_contacts') {
    const accountId = argsFor(args, ['accountId', 'limit', 'cursor']);
    const options = pageOptions(args);
    let limit = options.limit;
    for (;;) {
      try { return bounded(page(await api.listAccountContacts(accountId, { ...options, limit }), limit)); }
      catch (e) {
        if (e instanceof Error && /^(READING_LIMIT|RESULT_TOO_LARGE):/.test(e.message)) {
          if (limit > 1) { limit = Math.floor(limit / 2); continue; }
          throw e;
        }
        if (e instanceof Error && /^INVALID_RESPONSE:/.test(e.message)) throw e;
        throw safeReadError(e);
      }
    }
  }
  if (name === 'ttobak_create_account_contact') {
    const accountId = argsFor(args, ['accountId', 'contact', 'idempotencyKey']);
    const input = contactInput(args.contact);
    const key = retryKey(args, api);
    let value: unknown;
    try { value = await api.createAccountContact(accountId, input, key); }
    catch (e) {
      const info = failure(e, api.requiresContactIdempotency);
      throw new Error(info.outcome ? (api.requiresContactIdempotency ? 'CONTACT_WRITE_FAILED: outcome unknown; retry the same idempotencyKey and contact' : 'CONTACT_WRITE_FAILED: outcome unknown; verify before retrying') : `CONTACT_WRITE_FAILED: ${info.code}`);
    }
    if (!record(value) || typeof value.contactId !== 'string' || !/^[A-Za-z0-9_-]{1,128}$/.test(value.contactId)) {
      throw new Error('CONTACT_WRITE_FAILED: outcome unknown; verify before retrying');
    }
    return bounded({ contactId: value.contactId });
  }
  const accountId = argsFor(args, ['accountId', 'contacts', 'idempotencyKey']);
  const batchKey = retryKey(args, api);
  if (!Array.isArray(args.contacts) || !args.contacts.length || args.contacts.length > MAX_ROWS ||
      Buffer.byteLength(JSON.stringify(args.contacts)) > 512 * 1024) throw new Error('INVALID_ARGUMENT: expected 1–25 bounded contacts');
  const emails = new Set<string>(), phones = new Set<string>(), seen = new Set<string>();
  const uncertainEmails = new Set<string>(), uncertainPhones = new Set<string>();
  let cursor: string | undefined;
  let limit = 25;
  for (let n = 0; ; n++) {
    if (n >= MAX_SCAN_PAGES) throw new Error('CONTACT_SCAN_LIMIT: full contact scan exceeded 1000 pages; no contacts created');
    let current: ReturnType<typeof page>;
    try { current = page(await api.listAccountContacts(accountId, { limit, ...(cursor ? { cursor } : {}) }), limit); }
    catch (e) {
      if (e instanceof Error && /^(READING_LIMIT|RESULT_TOO_LARGE):/.test(e.message) && limit > 1) {
        limit = Math.floor(limit / 2);
        continue;
      }
      if (e instanceof Error && /^(READING_LIMIT|RESULT_TOO_LARGE|INVALID_RESPONSE):/.test(e.message)) throw e;
      throw safeReadError(e);
    }
    for (const contact of current.contacts) {
      const k = keys(contact);
      if (k.email) emails.add(k.email);
      k.phones.forEach(phone => phones.add(phone));
    }
    cursor = current.nextCursor || undefined;
    if (!cursor) break;
    if (seen.has(cursor)) throw new Error('INVALID_RESPONSE: repeated contact cursor; no contacts created');
    seen.add(cursor);
  }
  const results: Record<string, unknown>[] = [];
  for (let i = 0; i < args.contacts.length; i++) {
    let input: ContactInput;
    try { input = contactInput(args.contacts[i]); }
    catch { results.push({ row: i + 1, status: 'failed', code: 'INVALID_CONTACT' }); continue; }
    const k = keys(input);
    const rowKey = batchKey === undefined ? undefined : createHash('sha256').update('ttobak-contact-import-v1\0').update(batchKey).update('\0').update(String(i)).digest('hex');
    if (api.requiresContactIdempotency) {
      if (!rowKey || !api.replayAccountContact) throw new Error('CONTACT_WRITE_FAILED: replay verification unavailable');
      let receipt: unknown;
      try { receipt = await api.replayAccountContact(accountId, input, rowKey); }
      catch (e) { const info = failure(e, api.requiresContactIdempotency); if (k.email) uncertainEmails.add(k.email); k.phones.forEach(phone => uncertainPhones.add(phone)); results.push({ row: i + 1, status: 'failed', ...info }); continue; }
      if (receipt !== null) {
        if (!record(receipt) || typeof receipt.contactId !== 'string' || !/^[A-Za-z0-9_-]{1,128}$/.test(receipt.contactId)) throw new Error('CONTACT_WRITE_FAILED: invalid replay receipt');
        if (k.email) emails.add(k.email);
        k.phones.forEach(phone => phones.add(phone));
        results.push({ row: i + 1, status: 'created', contactId: receipt.contactId });
        continue;
      }
    }
    if ((k.email && emails.has(k.email)) || k.phones.some(phone => phones.has(phone))) {
      results.push({ row: i + 1, status: 'duplicate' }); continue;
    }
    if ((k.email && uncertainEmails.has(k.email)) || k.phones.some(phone => uncertainPhones.has(phone))) {
      results.push({ row: i + 1, status: 'duplicate', reason: 'previous_write_unknown' }); continue;
    }
    let info: { code: string; outcome?: string } | undefined;
    let id: unknown;
    try {
      const value = await api.createAccountContact(accountId, input, rowKey);
      if (!record(value) || typeof value.contactId !== 'string' || !/^[A-Za-z0-9_-]{1,128}$/.test(value.contactId)) throw new Error('Invalid create response');
      id = value.contactId;
    } catch (e) { info = failure(e, api.requiresContactIdempotency); }
    if (!info) {
      if (k.email) emails.add(k.email);
      k.phones.forEach(phone => phones.add(phone));
    } else if (info.outcome === 'unknown') {
      if (k.email) uncertainEmails.add(k.email);
      k.phones.forEach(phone => uncertainPhones.add(phone));
    }
    results.push(info ? { row: i + 1, status: 'failed', ...info } : { row: i + 1, status: 'created', contactId: id });
  }
  return bounded({ results });
}
