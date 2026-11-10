import type { AccountContact, ContactInput } from '@/types/meeting';
import { CONTACT_FIELDS, isContactAccessDenied, requestPeopleResolve, type ContactField } from './contactImport';

/** An account the user can add contacts to, with its matching names. */
export interface MatchAccount {
  accountId: string; name: string; aliases: string[]; domains: string[];
  /** False for an ancestor/descendant the user can read but is not a direct member of; contacts cannot be added there. Absent means direct. */
  direct?: boolean;
}
const isDirect = (account?: MatchAccount) => !!account && account.direct !== false;

export type AccountMatch =
  | { kind: 'exact'; accountId: string }
  | { kind: 'similar'; candidates: string[] }
  | { kind: 'none' };

// Personal mail hosts never identify a company.
const PUBLIC_DOMAINS = new Set(['gmail.com', 'naver.com', 'daum.net', 'hanmail.net', 'kakao.com', 'nate.com', 'hotmail.com', 'outlook.com', 'live.com', 'yahoo.com', 'icloud.com', 'me.com', 'msn.com', 'korea.com']);
const LEGAL_FORMS = /\(주\)|\(유\)|\(사\)|\(재\)|㈜|주식회사|유한회사|유한책임회사|사단법인|재단법인|\b(?:co|corp|corporation|inc|incorporated|ltd|limited|llc|plc|gmbh|company)\b\.?/gi;

// Brand prefixes dropped before comparison ("KEB 하나은행" is the 하나은행 account). Keep this list small.
const BRAND_PREFIX = /^keb\s*(?=[가-힣])/;

/**
 * Company name reduced for comparison: no legal form ((주), 주식회사, ㈜, Co., Ltd. ...),
 * no "KEB " brand prefix, no case, spaces or punctuation.
 */
export function normalizeCompany(value?: string): string {
  return (value || '').normalize('NFKC').toLowerCase().replace(LEGAL_FORMS, ' ').trim().replace(BRAND_PREFIX, '').replace(/[\s.,·・&'"()[\]{}\-_/\\]+/g, '');
}

/** Lowercase company domain of an email, or '' for personal mail and malformed values. */
export function companyDomain(email?: string): string {
  const at = (email || '').trim().toLowerCase().lastIndexOf('@');
  if (at < 0) return '';
  const domain = (email || '').trim().toLowerCase().slice(at + 1);
  return /^[a-z0-9.-]+\.[a-z]{2,}$/.test(domain) && !PUBLIC_DOMAINS.has(domain) ? domain : '';
}

const normalizeDomain = (value: string) => value.trim().toLowerCase().replace(/^@/, '').replace(/^www\./, '');
const domainMatches = (domain: string, accountDomain: string) => !!accountDomain && (domain === accountDomain || domain.endsWith(`.${accountDomain}`));

function bigrams(value: string): Map<string, number> {
  const out = new Map<string, number>();
  const chars = [...value];
  if (chars.length === 1) out.set(value, 1);
  for (let i = 0; i < chars.length - 1; i++) {
    const key = chars[i] + chars[i + 1];
    out.set(key, (out.get(key) || 0) + 1);
  }
  return out;
}

/** Dice coefficient over character bigrams of two normalized names. */
export function nameSimilarity(a: string, b: string): number {
  if (!a || !b) return 0;
  if (a === b) return 1;
  const left = bigrams(a); const right = bigrams(b);
  let overlap = 0; let total = 0;
  left.forEach((count, key) => { overlap += Math.min(count, right.get(key) || 0); total += count; });
  right.forEach((count) => { total += count; });
  return total ? (2 * overlap) / total : 0;
}

const SIMILAR_THRESHOLD = 0.5;

/** Matches a company/email to an account: exact name, alias or domain; otherwise similar candidates. */
export function matchAccount(company: string | undefined, email: string | undefined, accounts: MatchAccount[]): AccountMatch {
  return matchAccountDomains(company, [companyDomain(email)].filter(Boolean), accounts);
}

function matchAccountDomains(company: string | undefined, domains: string[], accounts: MatchAccount[]): AccountMatch {
  const key = normalizeCompany(company);
  // A company name or alias is stronger evidence than a mail domain.
  let exact = new Set(key ? accounts.filter((a) => [a.name, ...a.aliases].map(normalizeCompany).includes(key)).map((a) => a.accountId) : []);
  if (!exact.size && domains.length) {
    exact = new Set(accounts.filter((account) =>
      account.domains.some((accountDomain) => domains.some((domain) => domainMatches(domain, normalizeDomain(accountDomain)))))
      .map((account) => account.accountId));
  }
  if (exact.size === 1) return { kind: 'exact', accountId: [...exact][0] };
  if (exact.size > 1) return { kind: 'similar', candidates: [...exact] };
  if (!key) return { kind: 'none' };
  const scored = accounts.map((account) => {
    const names = [account.name, ...account.aliases].map(normalizeCompany).filter(Boolean);
    const score = Math.max(0, ...names.map((name) => {
      const short = name.length <= key.length ? name : key;
      const long = name.length <= key.length ? key : name;
      // "하나" inside "하나금융티아이" is a likely relation, not proof.
      if (short.length >= 2 && long.includes(short)) return Math.max(0.6, nameSimilarity(key, name));
      // A shared head ("하나" in 하나금융티아이/하나은행) suggests a group company.
      let head = 0;
      const a = [...key]; const b = [...name];
      while (head < a.length && head < b.length && a[head] === b[head]) head++;
      const prefix = head >= 2 ? head / Math.min(a.length, b.length) : 0;
      return Math.max(prefix, nameSimilarity(key, name));
    }));
    return { accountId: account.accountId, score };
  }).filter((s) => s.score >= SIMILAR_THRESHOLD).sort((a, b) => b.score - a.score).slice(0, 3);
  return scored.length ? { kind: 'similar', candidates: scored.map((s) => s.accountId) } : { kind: 'none' };
}

// A fuzzy match is preselected only on strong evidence: one name contains the other
// (at least this many characters), or the similarity reaches the threshold.
const MIN_CONTAINED_LENGTH = 2;
const STRONG_SIMILARITY = 0.8;

/** Accounts with strong name or domain evidence for a company that did not match exactly. */
function strongCandidates(company: string | undefined, domains: string[], accounts: MatchAccount[]): string[] {
  const key = normalizeCompany(company);
  return accounts.filter((account) => {
    if (domains.some((domain) => account.domains.some((accountDomain) => domainMatches(domain, normalizeDomain(accountDomain))))) return true;
    if (!key) return false;
    return [account.name, ...account.aliases].map(normalizeCompany).filter(Boolean).some((name) => {
      const short = name.length <= key.length ? name : key;
      const long = name.length <= key.length ? key : name;
      return (short.length >= MIN_CONTAINED_LENGTH && long.includes(short)) || nameSimilarity(key, name) >= STRONG_SIMILARITY;
    });
  }).map((account) => account.accountId);
}

/**
 * Rows grouped by company, so a choice applies to everyone at that company.
 * suggestedAccountId is a preselected fuzzy match the user can change; nonDirect lists
 * matched accounts the user may read but cannot add contacts to (not a direct member).
 */
export interface CompanyGroup { key: string; company: string; domain: string; rows: number[]; match: AccountMatch; suggestedAccountId?: string; nonDirect?: string[] }

export function groupByCompany(contacts: ContactInput[], accounts: MatchAccount[]): CompanyGroup[] {
  const groups = new Map<string, CompanyGroup>();
  contacts.forEach((contact, row) => {
    if (!contact.name.trim()) return;
    const company = normalizeCompany(contact.company);
    const domain = companyDomain(contact.email);
    const key = company ? `c:${company}` : domain ? `d:${domain}` : 'none';
    const group = groups.get(key);
    if (group) {
      group.rows.push(row);
      if (!group.domain && domain) group.domain = domain;
      return;
    }
    groups.set(key, { key, company: contact.company?.trim() || '', domain, rows: [row], match: { kind: 'none' } });
  });
  const byId = new Map(accounts.map((account) => [account.accountId, account]));
  for (const group of groups.values()) {
    const domains = [...new Set(group.rows.map((row) => companyDomain(contacts[row].email)).filter(Boolean))];
    if (group.key === 'none') { group.match = { kind: 'none' }; continue; }
    let match = matchAccountDomains(group.company, domains, accounts);
    const nonDirect = new Set<string>();
    if (match.kind === 'exact' && !isDirect(byId.get(match.accountId))) {
      // Never auto-import into an account the user cannot write to.
      nonDirect.add(match.accountId);
      match = { kind: 'similar', candidates: [match.accountId] };
    } else if (match.kind !== 'exact') {
      const strong = strongCandidates(group.company, domains, accounts);
      // Ambiguity (2+ strong candidates, direct or not) and non-direct targets stay undecided.
      if (strong.length === 1 && isDirect(byId.get(strong[0]))) group.suggestedAccountId = strong[0];
      if (match.kind === 'none' && strong.length === 1) match = { kind: 'similar', candidates: strong };
    }
    if (match.kind === 'similar') match.candidates.filter((id) => !isDirect(byId.get(id))).forEach((id) => nonDirect.add(id));
    if (nonDirect.size) group.nonDirect = [...nonDirect];
    group.match = match;
  }
  const order = { similar: 0, none: 1, exact: 2 } as const;
  return [...groups.values()].sort((a, b) => order[a.match.kind] - order[b.match.kind] || b.rows.length - a.rows.length);
}

export type GroupDecision =
  | { action: 'account'; accountId: string }
  | { action: 'new'; name: string }
  | { action: 'drop' };

/** Exact and strongly suggested matches preselect their account; everything else waits for a choice and drops by default. */
export function defaultDecision(group: CompanyGroup): GroupDecision {
  if (group.match.kind === 'exact') return { action: 'account', accountId: group.match.accountId };
  if (group.suggestedAccountId) return { action: 'account', accountId: group.suggestedAccountId };
  return { action: 'drop' };
}

const emailKey = (value?: string) => (value || '').trim().toLowerCase();
const phoneKey = (value?: string) => (value || '').replace(/[^0-9]/g, '').replace(/^82(?=[1-9])/, '0');

const nameKey = (value?: string) => (value || '').normalize('NFKC').replace(/\s+/g, '').toLowerCase();

/**
 * The existing contact that is the same person: the same email, or, when the
 * emails do not differ, the same name and a shared mobile or work phone. A
 * shared office line alone never identifies a person.
 */
export function findSameContact(existing: AccountContact[], contact: ContactInput): AccountContact | undefined {
  const email = emailKey(contact.email);
  if (email) {
    const byEmail = existing.find((other) => emailKey(other.email) === email);
    if (byEmail) return byEmail;
  }
  const name = nameKey(contact.name);
  const phones = [phoneKey(contact.phone), phoneKey(contact.workPhone)].filter(Boolean);
  if (!name || !phones.length) return undefined;
  return existing.find((other) => nameKey(other.name) === name
    && !(email && emailKey(other.email) && emailKey(other.email) !== email)
    && [phoneKey(other.phone), phoneKey(other.workPhone)].some((p) => !!p && phones.includes(p)));
}

export interface FieldConflict { field: ContactField; current: string; incoming: string }

/** Fills empty fields of an existing contact; differing values are reported, never overwritten. */
export function mergeContact(existing: AccountContact, incoming: ContactInput): { merged: ContactInput; filled: ContactField[]; conflicts: FieldConflict[] } {
  const merged: ContactInput = { name: existing.name };
  const filled: ContactField[] = [];
  const conflicts: FieldConflict[] = [];
  for (const [field] of CONTACT_FIELDS) {
    const current = (existing[field] || '').trim();
    const next = (incoming[field] || '').trim();
    if (current) merged[field] = existing[field] as string;
    if (!next) continue;
    if (!current) { merged[field] = next; filled.push(field); }
    else if (comparable(field, current) !== comparable(field, next)) conflicts.push({ field, current, incoming: next });
  }
  return { merged, filled, conflicts };
}

// Built from the field list (fields ending in "phone" plus fax) without naming them.
const PHONE_FIELDS = new Set(CONTACT_FIELDS.map(([field]) => field).filter((field) => /phone$|^fax$/i.test(field)));

function comparable(field: ContactField, value: string): string {
  if (field === 'email') return emailKey(value);
  if (PHONE_FIELDS.has(field)) return phoneKey(value);
  return value.replace(/\s+/g, ' ').trim();
}

export type BulkStatus = 'created' | 'filled' | 'unchanged' | 'dropped' | 'invalid' | 'failed';
export interface BulkRowResult { status: BulkStatus; accountId?: string; contactId?: string; conflicts?: FieldConflict[] }

export interface BulkApi {
  createAccount: (name: string, domains: string[]) => Promise<string>;
  listContacts: (accountId: string, cursor: string) => Promise<{ contacts: AccountContact[]; nextCursor?: string }>;
  createContact: (accountId: string, contact: ContactInput) => Promise<AccountContact>;
  updateContact: (accountId: string, contactId: string, contact: ContactInput & { revision: string }) => Promise<AccountContact>;
  /** One people resolve request per account with saved rows (written, filled or already present). */
  requestPeopleResolve?: (accountId: string, signal?: AbortSignal) => Promise<unknown>;
}

async function listAll(api: BulkApi, accountId: string): Promise<AccountContact[]> {
  const out: AccountContact[] = [];
  const seen = new Set<string>();
  let cursor = '';
  do {
    if (seen.has(cursor)) throw new Error('연락처 목록을 불러오지 못했습니다.');
    seen.add(cursor);
    const page = await api.listContacts(accountId, cursor);
    out.push(...page.contacts);
    cursor = page.nextCursor || '';
  } while (cursor);
  return out;
}

const isAbort = (error: unknown) => error instanceof DOMException && error.name === 'AbortError';

/**
 * Imports grouped rows: creates chosen new accounts, then per account adds new
 * people and fills empty fields of the same person. Access denial and aborts
 * stop the run; other failures are recorded per row.
 */
export async function runBulkImport(
  contacts: ContactInput[],
  groups: CompanyGroup[],
  decisions: Record<string, GroupDecision>,
  api: BulkApi,
  options: { onProgress?: (row: number, result: BulkRowResult) => void; onAccountCreated?: (groupKey: string, accountId: string) => void; onPeopleResolveFailed?: (accountId: string) => void; onPeopleResolved?: (accountId: string) => void; skipRows?: Set<number>; signal?: AbortSignal } = {},
): Promise<Record<number, BulkRowResult>> {
  const { onProgress, onAccountCreated, signal } = options;
  const skip = options.skipRows || new Set<number>();
  const results: Record<number, BulkRowResult> = {};
  const report = (row: number, result: BulkRowResult) => { results[row] = result; onProgress?.(row, result); };
  const checkAbort = () => { if (signal?.aborted) throw new DOMException('Aborted', 'AbortError'); };
  const byAccount = new Map<string, number[]>();
  // Accounts whose rows were all saved by an earlier run of this file.
  const savedBefore = new Set<string>();
  for (const group of groups) {
    checkAbort();
    const rows = group.rows.filter((row) => !skip.has(row));
    const decision = decisions[group.key] ?? defaultDecision(group);
    if (!rows.length) {
      if (group.rows.length && decision.action === 'account') { savedBefore.add(decision.accountId); byAccount.set(decision.accountId, byAccount.get(decision.accountId) || []); }
      continue;
    }
    if (decision.action === 'drop') { rows.forEach((row) => report(row, { status: 'dropped' })); continue; }
    let accountId: string;
    if (decision.action === 'new') {
      const name = decision.name.trim();
      if (!name) { rows.forEach((row) => report(row, { status: 'failed' })); continue; }
      try { accountId = await api.createAccount(name, group.domain ? [group.domain] : []); }
      catch (error) {
        if (isAbort(error) || isContactAccessDenied(error)) throw error;
        rows.forEach((row) => report(row, { status: 'failed' }));
        continue;
      }
      onAccountCreated?.(group.key, accountId);
    } else accountId = decision.accountId;
    byAccount.set(accountId, [...(byAccount.get(accountId) || []), ...rows]);
  }
  for (const [accountId, rows] of byAccount) {
    let saved = savedBefore.has(accountId);
    if (rows.length) await importAccountRows(accountId, rows, () => { saved = true; });
    // Also after a re-import that only finds saved rows: an earlier run may
    // have failed or been interrupted before its request.
    saved = saved || rows.some((row) => ['created', 'filled', 'unchanged'].includes(results[row]?.status || ''));
    const request = api.requestPeopleResolve;
    if (!saved || !request || signal?.aborted) continue;
    if (await requestPeopleResolve((s) => request(accountId, s), signal)) options.onPeopleResolved?.(accountId);
    else options.onPeopleResolveFailed?.(accountId);
  }
  return results;

  async function importAccountRows(accountId: string, rows: number[], onWrite: () => void) {
    let existing: AccountContact[];
    try { existing = await listAll(api, accountId); }
    catch (error) {
      if (isAbort(error) || isContactAccessDenied(error)) throw error;
      rows.forEach((row) => report(row, { status: 'failed', accountId }));
      return;
    }
    for (const row of rows) {
      checkAbort();
      const contact = contacts[row];
      if (!contact.name.trim()) { report(row, { status: 'invalid' }); continue; }
      const same = findSameContact(existing, contact);
      // A reserved identity from an ambiguous failure may already be saved.
      if (same && !same.contactId) { report(row, { status: 'failed', accountId }); continue; }
      try {
        if (!same) {
          onWrite();
          const created = await api.createContact(accountId, contact);
          existing.push(created);
          report(row, { status: 'created', accountId, contactId: created.contactId });
          continue;
        }
        const { merged, filled, conflicts } = mergeContact(same, contact);
        if (!filled.length) { report(row, { status: 'unchanged', accountId, contactId: same.contactId, conflicts }); continue; }
        onWrite();
        const updated = await api.updateContact(accountId, same.contactId, { ...merged, revision: same.revision });
        existing[existing.indexOf(same)] = updated;
        report(row, { status: 'filled', accountId, contactId: updated.contactId, conflicts });
      } catch (error) {
        if (isAbort(error) || isContactAccessDenied(error)) throw error;
        // A network or server failure can arrive after the write committed:
        // reserve the identity so a later row in this run is not added twice.
        const rejected = typeof error === 'object' && error !== null && 'status' in error && [400, 409, 413, 422].includes(Number(error.status));
        if (!rejected) {
          if (same) existing[existing.indexOf(same)] = { ...mergeContact(same, contact).merged, contactId: '', revision: '' };
          else existing.push({ ...contact, contactId: '', revision: '' });
        }
        report(row, { status: 'failed', accountId, contactId: same?.contactId });
      }
    }
  }
}
