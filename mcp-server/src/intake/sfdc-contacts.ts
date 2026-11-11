// `sfdc-contacts` step: reads the customer contacts of every SFDC account
// mapped to a TTOBAK Account (aws-sentral-mcp `search_contacts`), matches them
// against that Account's TTOBAK contacts on the Mac, and sends one bounded
// batch per Account to `POST /api/accounts/{id}/contacts/sfdc-sync`.
//
// Privacy: SFDC and TTOBAK emails are compared here only. No email or phone is
// ever placed in a request body, a log line, the status file or a ledger; the
// SFDC phone fields are never read at all. Only IDs, name and title leave.

import { createHash } from 'node:crypto';
import type { SfdcContactSyncBody, SfdcContactSyncItem } from '../api.js';
import { deliveryFailure } from './batch.js';
import { SCOPE_PATTERN } from './calendar.js';
import { IntakeError, intakeCode, type IntakeCode } from './errors.js';
import { SFDC_CONTACTS_TOOL, parseFailed, parsePage, record, text, truncateBytes, type SfdcSource } from './sfdc.js';
import { canonicalSfdcId } from './sfdc-id.js';
import type { IntakeStore } from './store.js';

const PAGE_LIMIT = 500;
/** Departure-read cap per SFDC account; above it the snapshot is incomplete. */
const MAX_READ_PER_ACCOUNT = 5_000;
/** Page bound per SFDC account: a full read plus one terminal page. */
const MAX_PAGES = Math.ceil(MAX_READ_PER_ACCOUNT / PAGE_LIMIT) + 1;
/** Server caps (API-SPEC, sfdc-sync). */
const MAX_SFDC_ACCOUNTS = 20;
const MAX_ITEMS = 200;
const MAX_PRESENT_TOTAL = 20_000;
const MAX_BODY_BYTES = 512 * 1024;
const FIELD_BYTES = 256;
/** TTOBAK contact pages: 25 contacts of at most 12,000 serialized bytes each. */
const CONTACT_PAGE = 25;
const CONTACT_PAGE_BYTES = 512 * 1024;
const MAX_CONTACT_PAGES = 1_000;
const MAX_LEDGER_IDS = 20_000;
/** No new Account starts after this; the app kills a run at 20 minutes, and status must still be written. */
export const SFDC_CONTACTS_BUDGET_MS = 8 * 60_000;

const INTERNAL_RECORD_TYPES = new Set(['Stakeholder Map - Internal Contacts', 'Project Resource']);
const RESULTS = ['created', 'filled', 'unchanged', 'alreadyLinked', 'suppressed', 'conflict', 'invalid'] as const;
type SyncResult = typeof RESULTS[number];

export interface SfdcContactCounts {
  /** TTOBAK Accounts with at least one mapped SFDC account that this run processed. */
  accounts: number;
  /** `search_contacts` records read, before filtering. */
  fetched: number;
  created: number;
  filled: number;
  unchanged: number;
  alreadyLinked: number;
  suppressed: number;
  conflict: number;
  invalid: number;
  ambiguous: number;
  skippedInternal: number;
  /** Items beyond the 200-item or 512 KiB request cap; they follow on later runs. */
  skippedCap: number;
  /** SFDC accounts beyond the 20 per TTOBAK Account that were not read. */
  skippedAccountCap: number;
  /** TTOBAK Accounts sent without a departure snapshot. */
  incomplete: number;
  /** Unlinked SFDC contacts of an incomplete Account: no email/name link or create is inferred from a partial read. */
  deferredIncomplete: number;
  /** `search_contacts` records whose own account ID is not the queried SFDC account; dropped. */
  accountMismatch: number;
  marked: number;
  unmarked: number;
  /** Linked contacts the server's departure pass could not open. */
  skipped: number;
  /** TTOBAK Accounts the signed-in user cannot write (HTTP 403). */
  forbidden: number;
  /** SFDC IDs that are not valid 15- or 18-character IDs, dropped wherever they arrive. */
  invalidIds: number;
  /** Corrupt suppression ledgers that were treated as empty. */
  ledgerReset: number;
  /** TTOBAK Accounts not started because the time budget ran out; they run next time. */
  deferred: number;
}

export interface SfdcContactsResult { result: 'ok' | IntakeCode; counts: SfdcContactCounts }

/** One SFDC customer contact on the Mac; `email` exists only for matching. IDs are canonical (18 characters). */
export interface SfdcPerson {
  id: string;
  /** The name sent to TTOBAK. */
  name: string;
  /** Name spellings to match (CJK names in both orders); defaults to `[name]`. */
  names?: string[];
  title: string;
  email?: string;
  accountId: string;
}

/** The fields of an existing TTOBAK contact that matching needs; `email` stays on the Mac. */
export interface ExistingContact {
  contactId: string;
  revision: string;
  name: string;
  email: string;
  sfdcContactId?: string;
  title?: string;
  company?: string;
}

export interface MatchResult {
  /** Links (fills) first, then creates, each ordered by SFDC contact ID. */
  items: SfdcContactSyncItem[];
  ambiguous: number;
  /** Linked contacts with nothing left to fill. */
  unchanged: number;
  suppressed: number;
  /** SFDC contacts without a usable name. */
  invalid: number;
  /** Unlinked SFDC contacts not matched because `inferLinks` is false. */
  deferred: number;
}

function emptyCounts(): SfdcContactCounts {
  return { accounts: 0, fetched: 0, created: 0, filled: 0, unchanged: 0, alreadyLinked: 0, suppressed: 0, conflict: 0,
    invalid: 0, ambiguous: 0, skippedInternal: 0, skippedCap: 0, skippedAccountCap: 0, incomplete: 0,
    deferredIncomplete: 0, accountMismatch: 0, marked: 0,
    unmarked: 0, skipped: 0, forbidden: 0, invalidIds: 0, ledgerReset: 0, deferred: 0 };
}

// ---------- matching (pure) ----------

/** NFKC, lower case, `.` and `,` dropped, whitespace collapsed. */
export function normalizeName(value: string): string {
  return value.normalize('NFKC').toLowerCase().replace(/[.,]/g, '').replace(/\s+/g, ' ').trim();
}

const CJK = /[\p{Script=Hangul}\p{Script=Han}\p{Script=Hiragana}\p{Script=Katakana}]/u;

/** Hangul and other CJK names are written with or without a space, so the key drops spaces for them. */
function nameKey(value: string): string {
  const name = normalizeName(value);
  return CJK.test(name) ? name.replace(/ /g, '') : name;
}

function emailKey(value: string | undefined): string {
  return (value ?? '').normalize('NFKC').trim().toLowerCase();
}

function fillable(contact: ExistingContact, person: SfdcPerson): boolean {
  return (!(contact.title ?? '').trim() && !!person.title) || !(contact.company ?? '').trim();
}

function item(person: SfdcPerson, contact?: ExistingContact): SfdcContactSyncItem {
  const base = { sfdcContactId: person.id, sfdcAccountId: person.accountId, name: person.name, title: person.title };
  return contact ? { action: 'link', ...base, contactId: contact.contactId, revision: contact.revision }
    : { action: 'create', ...base };
}

const byId = (first: { sfdcContactId: string }, second: { sfdcContactId: string }) =>
  first.sfdcContactId < second.sfdcContactId ? -1 : first.sfdcContactId > second.sfdcContactId ? 1 : 0;

/**
 * Decides, per SFDC contact: an existing `sfdcContactId` link always wins
 * (filled only when a field is empty); then a one-to-one, case-insensitive
 * email match; then a unique normalized name match without a conflicting
 * email. A contact linked to another SFDC ID, or already matched by email, is
 * another person for a name match. Several candidates, or several SFDC
 * contacts for one candidate, are ambiguous and skipped; no match is a create.
 * With `inferLinks: false` (an incomplete SFDC read, where a missing person
 * could make a match ambiguous) only existing links are filled; every other
 * unsuppressed contact is `deferred`.
 */
export function matchContacts(sfdc: SfdcPerson[], existing: ExistingContact[], suppressed: Set<string>,
  { inferLinks = true }: { inferLinks?: boolean } = {}): MatchResult {
  const result: MatchResult = { items: [], ambiguous: 0, unchanged: 0, suppressed: 0, invalid: 0, deferred: 0 };
  const linked = new Map<string, ExistingContact>();
  const byEmail = new Map<string, ExistingContact[]>();
  const byName = new Map<string, ExistingContact[]>();
  for (const contact of existing) {
    if (contact.sfdcContactId) linked.set(contact.sfdcContactId, contact);
    const email = emailKey(contact.email);
    if (email) byEmail.set(email, [...(byEmail.get(email) ?? []), contact]);
    const name = nameKey(contact.name);
    if (name) byName.set(name, [...(byName.get(name) ?? []), contact]);
  }
  const links: SfdcContactSyncItem[] = [];
  const creates: SfdcContactSyncItem[] = [];
  const seen = new Set<string>();
  const remaining: SfdcPerson[] = [];
  for (const person of [...sfdc].sort((a, b) => a.id < b.id ? -1 : a.id > b.id ? 1 : 0)) {
    if (seen.has(person.id)) continue;
    seen.add(person.id);
    if (!person.name.trim()) { result.invalid++; continue; }
    const current = linked.get(person.id);
    if (current) {
      if (fillable(current, person)) links.push(item(person, current));
      else result.unchanged++;
      continue;
    }
    if (suppressed.has(person.id)) { result.suppressed++; continue; }
    remaining.push(person);
  }
  if (!inferLinks) {
    result.deferred = remaining.length;
    result.items = links.sort(byId);
    return result;
  }

  const sfdcEmails = new Map<string, number>();
  for (const person of remaining) {
    const email = emailKey(person.email);
    if (email) sfdcEmails.set(email, (sfdcEmails.get(email) ?? 0) + 1);
  }
  const claimed = new Set<string>();
  const byNameOnly: SfdcPerson[] = [];
  for (const person of remaining) {
    const email = emailKey(person.email);
    const targets = email ? byEmail.get(email) ?? [] : [];
    if (!targets.length) { byNameOnly.push(person); continue; }
    if (targets.length > 1 || sfdcEmails.get(email)! > 1 || targets[0].sfdcContactId) {
      // A contact whose email belongs to another SFDC person is never a name candidate either.
      for (const target of targets) claimed.add(target.contactId);
      result.ambiguous++;
      continue;
    }
    claimed.add(targets[0].contactId);
    links.push(item(person, targets[0]));
  }

  const nameClaims = new Map<string, { contact: ExistingContact; people: SfdcPerson[] }>();
  for (const person of byNameOnly) {
    const email = emailKey(person.email);
    const candidates = new Map<string, ExistingContact>();
    for (const key of new Set((person.names?.length ? person.names : [person.name]).map(nameKey))) {
      for (const contact of byName.get(key) ?? []) {
        if (contact.sfdcContactId || claimed.has(contact.contactId)) continue;
        const other = emailKey(contact.email);
        if (email && other && other !== email) continue;
        candidates.set(contact.contactId, contact);
      }
    }
    if (candidates.size === 0) creates.push(item(person));
    else if (candidates.size > 1) result.ambiguous++;
    else {
      const [contact] = candidates.values();
      const claim = nameClaims.get(contact.contactId) ?? { contact, people: [] };
      claim.people.push(person);
      nameClaims.set(contact.contactId, claim);
    }
  }
  for (const { contact, people } of nameClaims.values()) {
    if (people.length > 1) result.ambiguous += people.length;
    else links.push(item(people[0], contact));
  }
  result.items = [...links.sort(byId), ...creates.sort(byId)];
  return result;
}

// ---------- SFDC reads ----------

function internalEmail(email: string): boolean {
  const domain = email.slice(email.lastIndexOf('@') + 1).trim().toLowerCase();
  return domain === 'amazon.com' || domain.endsWith('.amazon.com') || domain === 'amazon.co.kr' || domain === 'aws.com';
}

/**
 * One customer contact, `'internal'` for an internal record type or AWS email,
 * `'invalid'` for an ID that is not an SFDC ID, or `'mismatch'` when the
 * record's own account ID is not the queried one. Phone fields are never read.
 */
function contactPerson(value: Record<string, unknown>, accountId: string): SfdcPerson | 'internal' | 'invalid' | 'mismatch' {
  const id = canonicalSfdcId(value.id);
  if (!id) return 'invalid';
  if (canonicalSfdcId(value.accountId) !== accountId) return 'mismatch';
  if (!(value.recordType === undefined || value.recordType === null || record(value.recordType))) {
    throw parseFailed('contact record has an unexpected record type');
  }
  const recordType = record(value.recordType) ? text(value.recordType, 'name', FIELD_BYTES) : '';
  const email = text(value, 'email', 1024);
  if (INTERNAL_RECORD_TYPES.has(recordType) || (email && internalEmail(email))) return 'internal';
  const first = text(value, 'firstName', FIELD_BYTES);
  const last = text(value, 'lastName', FIELD_BYTES);
  // Korean names read family name first without a space; others given name first.
  const korean = /\p{Script=Hangul}/u.test(first + last);
  const name = truncateBytes(korean ? `${last}${first}` : [first, last].filter(Boolean).join(' '), FIELD_BYTES);
  // Only CJK names are matched in either order; a Latin name matches as given.
  const names = CJK.test(first + last) ? [name, `${first} ${last}`, `${last} ${first}`].filter(item => item.trim()) : [name];
  return { id, name, names, title: text(value, 'title', FIELD_BYTES), accountId, ...(email ? { email } : {}) };
}

interface AccountRead {
  people: SfdcPerson[]; ids: string[]; fetched: number; internal: number; invalidIds: number; mismatch: number;
  complete: boolean; failure?: IntakeCode;
}

/**
 * Every page of one SFDC account, up to 5,000 contacts and 11 pages. An empty
 * page that claims more, or any repeated cursor, fails closed. More than 5,000
 * kept IDs (uneven pages) make the read incomplete and are cut to 5,000. Only
 * Midway expiry throws.
 */
async function readAccount(source: SfdcSource, accountId: string): Promise<AccountRead> {
  const read: AccountRead = { people: [], ids: [], fetched: 0, internal: 0, invalidIds: 0, mismatch: 0, complete: false };
  const seen = new Set<string>();
  const cursors = new Set<string>();
  let after: string | undefined;
  try {
    for (let index = 0; ; index++) {
      if (read.fetched >= MAX_READ_PER_ACCOUNT) break;
      if (index >= MAX_PAGES) throw parseFailed(`search_contacts exceeded ${MAX_PAGES} pages`);
      const page = parsePage(await source.call(SFDC_CONTACTS_TOOL.name, {
        condition: { field: 'accountId', operator: 'EXACT_MATCH', value: accountId }, limit: PAGE_LIMIT,
        ...(after ? { after } : {}) }), SFDC_CONTACTS_TOOL.name);
      const people = page.records.map(value => contactPerson(value, accountId));
      for (const person of people) {
        if (typeof person !== 'string' && seen.size === seen.add(person.id).size) {
          throw parseFailed('search_contacts repeated a record ID');
        }
      }
      read.fetched += page.records.length;
      for (const person of people) {
        if (person === 'internal') read.internal++;
        else if (person === 'invalid') read.invalidIds++;
        else if (person === 'mismatch') read.mismatch++;
        else { read.people.push(person); read.ids.push(person.id); }
      }
      if (!page.hasNextPage) {
        if (read.fetched < page.totalCount) throw parseFailed('search_contacts result is incomplete');
        read.complete = true;
        break;
      }
      if (!page.records.length) throw parseFailed('search_contacts returned an empty page with more pages');
      if (cursors.size === cursors.add(page.cursor!).size) throw parseFailed('search_contacts repeated a cursor');
      after = page.cursor!;
    }
    if (read.ids.length > MAX_READ_PER_ACCOUNT) {
      read.complete = false;
      read.ids = read.ids.slice(0, MAX_READ_PER_ACCOUNT);
      read.people = read.people.slice(0, MAX_READ_PER_ACCOUNT);
    }
    return read;
  } catch (error) {
    if (intakeCode(error) === 'AUTH_EXPIRED_MIDWAY') throw error;
    read.failure = intakeCode(error);
    return read;
  }
}

// ---------- TTOBAK API ----------

export interface SfdcContactsApi {
  performanceMappings(): Promise<unknown>;
  listAccountContacts(accountId: string, options: { limit: number; cursor?: string }, maxResponseBytes?: number): Promise<unknown>;
  syncSfdcContacts(accountId: string, body: SfdcContactSyncBody): Promise<unknown>;
}

/** An API failure: login stops the step, 403 is `forbidden`, anything else fails this Account. */
function apiFailure(error: unknown): 'forbidden' | IntakeCode {
  const failure = deliveryFailure(error);
  if (failure.kind === 'login') throw new IntakeError('TTOBAK_LOGIN_REQUIRED');
  if (failure.kind === 'rejected') return failure.status === 403 ? 'forbidden' : failure.status === 409 ? 'TTOBAK_CONFLICT' : 'TTOBAK_REJECTED';
  return 'TTOBAK_UNAVAILABLE';
}

interface MappedAccounts { accounts: Map<string, { ids: string[]; invalid: boolean }>; truncated: boolean; invalidIds: number }

/**
 * TTOBAK Account -> sorted canonical SFDC account IDs. A truncated listing may
 * miss an Account's other SFDC accounts, so no Account gets a snapshot from it.
 */
async function mappedAccounts(api: SfdcContactsApi): Promise<MappedAccounts> {
  let value: unknown;
  try { value = await api.performanceMappings(); } catch (error) {
    if (deliveryFailure(error).kind === 'login') throw new IntakeError('TTOBAK_LOGIN_REQUIRED');
    throw new IntakeError('TTOBAK_UNAVAILABLE', 'performance mappings could not be read');
  }
  if (!record(value) || !Array.isArray(value.items) || typeof value.truncated !== 'boolean') {
    throw new IntakeError('TTOBAK_UNAVAILABLE', 'performance mappings have an unexpected shape');
  }
  const result = new Map<string, { ids: string[]; invalid: boolean }>();
  let invalidIds = 0;
  for (const entry of value.items as unknown[]) {
    if (!record(entry) || entry.mapped !== true || typeof entry.accountId !== 'string' || !entry.accountId) continue;
    const group = result.get(entry.accountId) ?? { ids: [], invalid: false };
    const sfdcAccountId = canonicalSfdcId(entry.sfdcAccountId);
    if (sfdcAccountId) {
      if (!group.ids.includes(sfdcAccountId)) group.ids.push(sfdcAccountId);
    } else { group.invalid = true; invalidIds++; }
    result.set(entry.accountId, group);
  }
  for (const group of result.values()) group.ids.sort();
  return { accounts: new Map([...result].sort(([a], [b]) => a < b ? -1 : a > b ? 1 : 0).filter(([, group]) => group.ids.length)),
    truncated: value.truncated, invalidIds };
}

const optionalText = (value: unknown) => typeof value === 'string' ? value : '';

/** Every contact of one Account; emails are kept in memory only, for matching. */
async function existingContacts(api: SfdcContactsApi, accountId: string, counts: SfdcContactCounts): Promise<ExistingContact[]> {
  const contacts: ExistingContact[] = [];
  let cursor: string | undefined;
  for (let index = 0; index < MAX_CONTACT_PAGES; index++) {
    const page = await api.listAccountContacts(accountId, { limit: CONTACT_PAGE, ...(cursor ? { cursor } : {}) }, CONTACT_PAGE_BYTES);
    if (!record(page) || !Array.isArray(page.contacts) || page.contacts.length > CONTACT_PAGE) {
      throw new IntakeError('TTOBAK_UNAVAILABLE', 'account contacts have an unexpected shape');
    }
    for (const value of page.contacts as unknown[]) {
      if (!record(value) || typeof value.contactId !== 'string' || typeof value.revision !== 'string' || typeof value.name !== 'string') {
        throw new IntakeError('TTOBAK_UNAVAILABLE', 'account contacts have an unexpected shape');
      }
      // A malformed stored link still marks the contact as linked (never a name candidate) but matches no SFDC ID.
      const stored = optionalText(value.sfdcContactId);
      const link = stored ? canonicalSfdcId(stored) : null;
      if (stored && !link) counts.invalidIds++;
      contacts.push({ contactId: value.contactId, revision: value.revision, name: value.name, email: optionalText(value.email),
        title: optionalText(value.title), company: optionalText(value.company),
        ...(stored ? { sfdcContactId: link ?? `invalid:${value.contactId}` } : {}) });
    }
    const next = page.nextCursor;
    if (next === undefined || next === null || next === '') return contacts;
    if (typeof next !== 'string' || next === cursor) throw new IntakeError('TTOBAK_UNAVAILABLE', 'account contacts cursor is invalid');
    cursor = next;
  }
  throw new IntakeError('TTOBAK_UNAVAILABLE', `account contacts exceed ${MAX_CONTACT_PAGES} pages`);
}

interface SyncResponse { results: { sfdcContactId: string; result: SyncResult }[]; marked: number; unmarked: number; skipped: number }

function syncResponse(value: unknown, items: SfdcContactSyncItem[]): SyncResponse {
  const count = (key: string) => record(value) && Number.isSafeInteger(value[key]) && (value[key] as number) >= 0;
  if (!record(value) || !Array.isArray(value.results) || value.results.length !== items.length ||
      !count('marked') || !count('unmarked') || !count('skipped')) {
    throw new IntakeError('TTOBAK_UNAVAILABLE', 'sfdc-sync response has an unexpected shape');
  }
  const results = (value.results as unknown[]).map((entry, index) => {
    if (!record(entry) || canonicalSfdcId(entry.sfdcContactId) !== items[index].sfdcContactId ||
        !(RESULTS as readonly unknown[]).includes(entry.result)) {
      throw new IntakeError('TTOBAK_UNAVAILABLE', 'sfdc-sync response has an unexpected shape');
    }
    return { sfdcContactId: items[index].sfdcContactId, result: entry.result as SyncResult };
  });
  return { results, marked: value.marked as number, unmarked: value.unmarked as number, skipped: value.skipped as number };
}

/** Drops trailing items until the serialized body fits 512 KiB. */
function fitBody(body: SfdcContactSyncBody): number {
  const items = body.items;
  let size = Buffer.byteLength(JSON.stringify({ ...body, items: [] }));
  let kept = 0;
  for (const entry of items) {
    const next = Buffer.byteLength(JSON.stringify(entry)) + (kept ? 1 : 0);
    if (size + next > MAX_BODY_BYTES) break;
    size += next;
    kept++;
  }
  body.items = items.slice(0, kept);
  return items.length - kept;
}

// ---------- suppression ledger ----------

/** SFDC contact IDs the server reported suppressed (IDs only), so they stop taking batch slots. */
interface SuppressionLedger { version: 1; suppressed: string[] }

function ledgerPath(scope: string, accountId: string): string[] {
  return ['sfdc-contacts', scope, `${createHash('sha256').update(accountId).digest('hex').slice(0, 32)}.json`];
}

/** The Account's suppressed IDs; an unreadable or invalid ledger is empty (`reset`), never a failure. */
function readLedger(store: IntakeStore | undefined, scope: string | undefined, accountId: string): { ids: Set<string>; reset: boolean } {
  if (!store || !scope) return { ids: new Set(), reset: false };
  try {
    const ledger = store.read<SuppressionLedger>(...ledgerPath(scope, accountId));
    if (ledger === null) return { ids: new Set(), reset: false };
    const ids = record(ledger) && ledger.version === 1 && Array.isArray(ledger.suppressed)
      ? ledger.suppressed.map(canonicalSfdcId) : [null];
    if (ids.some(id => id === null)) return { ids: new Set(), reset: true };
    return { ids: new Set(ids as string[]), reset: false };
  } catch {
    return { ids: new Set(), reset: true };
  }
}

// ---------- orchestration ----------

export interface SfdcContactsOptions {
  /** The `sfdc` connector's aws-sentral-mcp session. */
  session: SfdcSource;
  api: SfdcContactsApi;
  dryRun: boolean;
  /** Local state for the suppression ledger; without both, nothing is remembered. */
  store?: IntakeStore;
  scope?: string;
  /** Milliseconds clock; injectable for tests. */
  now?: () => number;
  /** Defaults to `SFDC_CONTACTS_BUDGET_MS`. */
  budgetMs?: number;
}

/**
 * Runs the step for every mapped TTOBAK Account. Midway expiry throws so the
 * run stops; any other failure is the step's result while later Accounts
 * still run. A dry run reads and plans but sends nothing.
 */
export async function runSfdcContacts(options: SfdcContactsOptions): Promise<SfdcContactsResult> {
  const { session, api, dryRun, store } = options;
  const now = options.now ?? Date.now;
  const budgetMs = options.budgetMs ?? SFDC_CONTACTS_BUDGET_MS;
  const startedAt = now();
  const scope = options.scope && SCOPE_PATTERN.test(options.scope) ? options.scope : undefined;
  const counts = emptyCounts();
  let failure: IntakeCode | undefined;
  const fail = (code: IntakeCode) => { failure ??= code; };
  try {
    const mappings = await mappedAccounts(api);
    counts.invalidIds += mappings.invalidIds;
    let index = 0;
    for (const [accountId, group] of mappings.accounts) {
      // Out of time: the remaining Accounts follow on the next run, and the result stays ok.
      if (now() - startedAt >= budgetMs) { counts.deferred = mappings.accounts.size - index; break; }
      index++;
      counts.accounts++;
      let current: ExistingContact[];
      try { current = await existingContacts(api, accountId, counts); } catch (error) {
        if (error instanceof IntakeError) { if (error.code === 'TTOBAK_LOGIN_REQUIRED') throw error; fail(error.code); continue; }
        const code = apiFailure(error);
        if (code === 'forbidden') counts.forbidden++;
        else fail(code);
        continue;
      }
      const sfdcAccountIds = group.ids.slice(0, MAX_SFDC_ACCOUNTS);
      counts.skippedAccountCap += group.ids.length - sfdcAccountIds.length;
      let complete = !mappings.truncated && !group.invalid && group.ids.length <= MAX_SFDC_ACCOUNTS;
      const people: SfdcPerson[] = [];
      const present: Record<string, string[]> = {};
      for (const sfdcAccountId of sfdcAccountIds) {
        const read = await readAccount(session, sfdcAccountId);
        counts.fetched += read.fetched;
        counts.skippedInternal += read.internal;
        counts.invalidIds += read.invalidIds;
        counts.accountMismatch += read.mismatch;
        people.push(...read.people);
        present[sfdcAccountId] = read.ids;
        if (!read.complete) complete = false;
        if (read.failure) fail(read.failure);
      }
      if (Object.values(present).reduce((sum, ids) => sum + ids.length, 0) > MAX_PRESENT_TOTAL) complete = false;
      if (!complete) counts.incomplete++;

      const ledger = readLedger(store, scope, accountId);
      if (ledger.reset) counts.ledgerReset++;
      const suppressed = ledger.ids;
      // A partial read could hide a same-named or same-email person, so only existing links are filled.
      const match = matchContacts(people, current, suppressed, { inferLinks: complete });
      // Emails were needed only for matching; none reaches the request.
      for (const person of people) delete person.email;
      for (const contact of current) contact.email = '';
      counts.ambiguous += match.ambiguous;
      counts.unchanged += match.unchanged;
      counts.suppressed += match.suppressed;
      counts.invalid += match.invalid;
      counts.deferredIncomplete += match.deferred;
      counts.skippedCap += Math.max(0, match.items.length - MAX_ITEMS);
      const body: SfdcContactSyncBody = { snapshotComplete: complete, sfdcAccountIds,
        ...(complete ? { present } : {}), items: match.items.slice(0, MAX_ITEMS) };
      counts.skippedCap += fitBody(body);
      if (dryRun) {
        for (const entry of body.items) {
          if (entry.action === 'create') counts.created++;
          else counts.filled++;
        }
        continue;
      }
      if (!body.items.length && !complete) continue;

      let response: SyncResponse;
      try { response = syncResponse(await api.syncSfdcContacts(accountId, body), body.items); } catch (error) {
        if (error instanceof IntakeError) { if (error.code === 'TTOBAK_LOGIN_REQUIRED') throw error; fail(error.code); continue; }
        const code = apiFailure(error);
        if (code === 'forbidden') counts.forbidden++;
        else fail(code);
        continue;
      }
      counts.marked += response.marked;
      counts.unmarked += response.unmarked;
      counts.skipped += response.skipped;
      const newlySuppressed: string[] = [];
      for (const { sfdcContactId, result } of response.results) {
        counts[result]++;
        if (result === 'suppressed') newlySuppressed.push(sfdcContactId);
      }
      if (newlySuppressed.length && store && scope) {
        const ids = [...new Set([...suppressed, ...newlySuppressed])].sort().slice(-MAX_LEDGER_IDS);
        // An unwritable ledger only costs batch slots next run; later Accounts still run.
        try { store.write({ version: 1, suppressed: ids } satisfies SuppressionLedger, ...ledgerPath(scope, accountId)); } catch {
          fail('STORE_INVALID');
        }
      }
    }
  } catch (error) {
    if (intakeCode(error) === 'AUTH_EXPIRED_MIDWAY') throw error;
    fail(intakeCode(error));
  }
  return { result: failure ?? 'ok', counts };
}
