import Papa from 'papaparse';
import type { ContactInput } from '@/types/meeting';
import contactFields from './contactFields.json';

export type ContactField = keyof ContactInput;
export const CONTACT_FIELDS = contactFields.fields as [ContactField, string][];
export type ContactMapping = Partial<Record<ContactField, number>>;
const aliases: Record<ContactField, string[]> = contactFields.aliases;
const normalizeHeader = (value: string) => value.replace(/^\uFEFF/, '').toLowerCase().replace(/[\s_\-./()]/g, '');

export function mapContactHeaders(headers: string[]): ContactMapping {
  const mapping: ContactMapping = {};
  for (const [field] of CONTACT_FIELDS) {
    const idx = headers.findIndex((header) => aliases[field].includes(normalizeHeader(header)));
    if (idx >= 0) mapping[field] = idx;
  }
  return mapping;
}

export function contactFromRow(row: string[], mapping: ContactMapping): ContactInput {
  const result: ContactInput = { name: '' };
  for (const [field] of CONTACT_FIELDS) {
    const index = mapping[field];
    if (index !== undefined && row[index]?.trim()) result[field] = row[index].trim();
  }
  return result;
}

export function parseContactCsv(value: string): string[][] {
  const result = Papa.parse<string[]>(value, { skipEmptyLines: 'greedy' });
  // A single-column (names only) export has no delimiter to detect; that is not malformed.
  if (result.errors.some((e) => e.code !== 'UndetectableDelimiter')) throw new Error('CSV 형식을 확인해 주세요.');
  return result.data;
}

interface IdentityIndex { emails: Set<string>; phones: Set<string> }
const emailKey = (value?: string) => (value || '').trim().toLowerCase();
const phoneKey = (value?: string) => (value || '').replace(/[^0-9]/g, '').replace(/^82(?=[1-9])/, '0');

export function contactIdentityIndex(contacts: ContactInput[]): IdentityIndex {
  const index: IdentityIndex = { emails: new Set(), phones: new Set() };
  contacts.forEach((contact) => addContactIdentity(index, contact));
  return index;
}
export function addContactIdentity(index: IdentityIndex, contact: ContactInput): void {
  const email = emailKey(contact.email);
  if (email) index.emails.add(email);
  for (const phone of [phoneKey(contact.phone), phoneKey(contact.workPhone)]) if (phone) index.phones.add(phone);
}
export function isDuplicateContact(index: IdentityIndex, contact: ContactInput): boolean {
  const email = emailKey(contact.email);
  return (!!email && index.emails.has(email)) || [phoneKey(contact.phone), phoneKey(contact.workPhone)].some((phone) => !!phone && index.phones.has(phone));
}

export type ImportStatus = 'created' | 'duplicate' | 'failed' | 'invalid';

export function isContactAccessDenied(error: unknown): boolean {
  return typeof error === 'object' && error !== null && 'status' in error && (error.status === 401 || error.status === 403);
}

export async function importContacts(
  rows: ContactInput[],
  list: (cursor: string) => Promise<{ contacts: ContactInput[]; nextCursor?: string }>,
  create: (contact: ContactInput) => Promise<unknown>,
  onProgress?: (index: number, status: ImportStatus) => void,
): Promise<ImportStatus[]> {
  const index = contactIdentityIndex([]);
  const seen = new Set<string>();
  let cursor = '';
  do {
    if (seen.has(cursor)) throw new Error('연락처 목록을 불러오지 못했습니다.');
    seen.add(cursor);
    const page = await list(cursor);
    page.contacts.forEach((contact) => addContactIdentity(index, contact));
    cursor = page.nextCursor || '';
  } while (cursor);

  const statuses: ImportStatus[] = [];
  for (const row of rows) {
    let status: ImportStatus;
    if (!row.name.trim()) status = 'invalid';
    else if (isDuplicateContact(index, row)) status = 'duplicate';
    else {
      try {
        await create(row);
        addContactIdentity(index, row);
        status = 'created';
      } catch (error) {
        if (error instanceof DOMException && error.name === 'AbortError') throw error;
        if (isContactAccessDenied(error)) throw error;
        // A network error or server failure can arrive after the write committed.
        // Avoid submitting the same identity again until the list is refreshed.
        const rejected = typeof error === 'object' && error !== null && 'status' in error && [400, 413, 422].includes(Number(error.status));
        if (!rejected) addContactIdentity(index, row);
        status = 'failed';
      }
    }
    statuses.push(status);
    onProgress?.(statuses.length - 1, status);
  }
  return statuses;
}
