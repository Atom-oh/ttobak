import { MAX_READING_BYTES } from './reading.js';

export type AccountSection = 'detail' | 'brief' | 'metadata' | 'members' | 'meetings' | 'insights';
export interface AccountSectionOptions {
  accountId: string;
  section: AccountSection;
  pageSize: number;
  cursor?: string;
  from?: string;
  to?: string;
  types?: string[];
}

const insightTypes = ['trend', 'need', 'competitive', 'risk', 'opportunity', 'tech', 'stakeholder', 'action'];
const phases: Record<AccountSection, string[]> = {
  detail: ['metadata', 'members'], brief: ['metadata', 'members', 'meetings', 'insights'],
  metadata: ['metadata'], members: ['members'], meetings: ['meetings'], insights: ['insights'],
};

export function accountSectionOptions(args: Record<string, unknown>, fallback: AccountSection): AccountSectionOptions {
  if (Object.keys(args).some(key => !['accountId', 'section', 'pageSize', 'cursor', 'from', 'to', 'types'].includes(key)) ||
      typeof args.accountId !== 'string' || !/^[A-Za-z0-9_-]{1,128}$/.test(args.accountId)) {
    throw new Error('INVALID_ARGUMENT: invalid account reading identity/options');
  }
  const section = args.section === undefined ? fallback : args.section;
  const allowed = fallback === 'brief' ? ['brief', 'metadata', 'members', 'meetings', 'insights'] :
    fallback === 'detail' ? ['detail', 'metadata', 'members'] : [fallback];
  if (typeof section !== 'string' || !allowed.includes(section)) throw new Error('INVALID_ARGUMENT: unsupported account section');
  const pageSize = args.pageSize === undefined ? 4000 : args.pageSize;
  if (typeof pageSize !== 'number' || !Number.isInteger(pageSize) || pageSize < 1 || pageSize > 8000) {
    throw new Error('INVALID_ARGUMENT: pageSize must be 1–8000 Unicode code points');
  }
  if (args.cursor !== undefined && (typeof args.cursor !== 'string' || !args.cursor.length || args.cursor.length > 2048)) {
    throw new Error('INVALID_CURSOR: invalid account continuation');
  }
  for (const name of ['from', 'to']) {
    const value = args[name];
    if (value !== undefined && (typeof value !== 'string' || value.length > 64 ||
        !/^\d{4}-\d\d-\d\dT/.test(value) || !Number.isFinite(Date.parse(value)))) {
      throw new Error(`INVALID_ARGUMENT: ${name} must be RFC3339`);
    }
  }
  if (args.types !== undefined && (!Array.isArray(args.types) || args.types.length > 8 ||
      args.types.some(value => typeof value !== 'string' || !insightTypes.includes(value)))) {
    throw new Error('INVALID_ARGUMENT: invalid insight types');
  }
  if (section !== 'brief' && section !== 'insights' && ['from', 'to', 'types'].some(key => args[key] !== undefined)) {
    throw new Error('INVALID_ARGUMENT: insight filters require brief or insights');
  }
  return { accountId: args.accountId, section: section as AccountSection, pageSize,
    cursor: args.cursor as string | undefined, from: args.from as string | undefined,
    to: args.to as string | undefined, types: args.types as string[] | undefined };
}

function record(value: unknown): value is Record<string, unknown> {
  return !!value && typeof value === 'object' && !Array.isArray(value);
}

export function accountSectionResult(value: unknown, options: AccountSectionOptions) {
  if (!record(value) || value.kind !== 'account' || value.accountId !== options.accountId || value.consistency !== 'current_group' ||
      value.section !== options.section || !phases[options.section].includes(value.phase as string) ||
      typeof value.revision !== 'string' || !/^[a-f0-9]{64}$/.test(value.revision) ||
      typeof value.dataJson !== 'string') throw new Error('INVALID_READING_RESPONSE: invalid account source');
  const fragment = value.fragment;
  if (!record(fragment) || fragment.unit !== 'unicode_code_points' || typeof fragment.complete !== 'boolean' ||
      ![fragment.startOffset, fragment.endOffset, fragment.totalCodePoints].every(n => typeof n === 'number' && Number.isSafeInteger(n) && n >= 0) ||
      (fragment.endOffset as number) <= (fragment.startOffset as number) ||
      (fragment.endOffset as number) > (fragment.totalCodePoints as number) ||
      Array.from(value.dataJson).length !== (fragment.endOffset as number) - (fragment.startOffset as number) ||
      (fragment.complete ? fragment.endOffset !== fragment.totalCodePoints :
        (fragment.endOffset as number) >= (fragment.totalCodePoints as number))) {
    throw new Error('INVALID_READING_RESPONSE: invalid account JSON fragment');
  }
  if (fragment.startOffset === 0 && fragment.complete) {
    try {
      const parsed = JSON.parse(value.dataJson);
      if (value.phase === 'metadata' ? !record(parsed) : !Array.isArray(parsed)) throw new Error('wrong JSON shape');
    } catch { throw new Error('INVALID_READING_RESPONSE: invalid completed account JSON group'); }
  }
  const page = value.page;
  if (!record(page) || typeof page.complete !== 'boolean' || (page.complete &&
      (!fragment.complete || value.phase !== phases[options.section].at(-1))) ||
      (page.complete ? page.nextCursor !== null :
        typeof page.nextCursor !== 'string' || !page.nextCursor.length || page.nextCursor.length > 2048)) {
    throw new Error('INVALID_READING_RESPONSE: invalid account continuation');
  }
  const result = { content: [{ type: 'text' as const, text: JSON.stringify(value) }] };
  if (Buffer.byteLength(JSON.stringify(result)) > MAX_READING_BYTES) throw new Error('READING_LIMIT: account page exceeds 32000 bytes');
  return result;
}
