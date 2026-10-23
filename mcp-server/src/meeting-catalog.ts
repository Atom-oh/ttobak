import { MAX_READING_BYTES } from './reading.js';

export interface MeetingCatalogOptions { tab: 'all' | 'shared'; limit: number; pageSize: number; cursor?: string; accountIds?: string[] }
export function meetingCatalogOptions(args: Record<string, unknown>): MeetingCatalogOptions {
  if (Object.keys(args).some(key => !['tab', 'limit', 'pageSize', 'cursor', 'accountIds'].includes(key))) throw new Error('INVALID_ARGUMENT: unsupported meeting catalog option');
  const tab = args.tab ?? 'all', limit = args.limit ?? 20, pageSize = args.pageSize ?? 4000;
  if (!['all', 'shared'].includes(tab as string) || !Number.isInteger(limit) || (limit as number) < 1 || (limit as number) > 20 ||
      !Number.isInteger(pageSize) || (pageSize as number) < 1 || (pageSize as number) > 8000) throw new Error('INVALID_ARGUMENT: invalid meeting catalog selection');
  if (args.cursor !== undefined && (typeof args.cursor !== 'string' || !args.cursor.length || args.cursor.length > 2048)) throw new Error('INVALID_CURSOR: invalid meeting catalog continuation');
  if (args.accountIds !== undefined && (!Array.isArray(args.accountIds) || !args.accountIds.length || args.accountIds.length > 100 ||
      args.accountIds.some(id => typeof id !== 'string' || !/^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(id)))) throw new Error('INVALID_ARGUMENT: invalid account IDs');
  return { tab: tab as 'all' | 'shared', limit: limit as number, pageSize: pageSize as number,
    cursor: args.cursor as string | undefined, accountIds: args.accountIds as string[] | undefined };
}
function record(value: unknown): value is Record<string, unknown> { return !!value && typeof value === 'object' && !Array.isArray(value); }
export function meetingCatalogResult(value: unknown) {
  if (!record(value) || value.kind !== 'meeting_catalog' || !record(value.page) || typeof value.page.complete !== 'boolean' ||
      (value.page.complete ? value.page.nextCursor !== null : typeof value.page.nextCursor !== 'string' || !value.page.nextCursor.length || value.page.nextCursor.length > 2048)) throw new Error('INVALID_READING_RESPONSE: invalid meeting catalog continuation');
  if (value.discoveryPending === true) {
    if (value.phase !== 'discovery' || Object.keys(value).some(key => !['kind', 'phase', 'discoveryPending', 'page'].includes(key))) throw new Error('INVALID_READING_RESPONSE: discovery contains premature data');
  } else {
    if (!['owned', 'direct', 'team'].includes(value.phase as string) || value.consistency !== 'current_group' ||
        value.overlapPossible !== true || value.unionKey !== 'meetingId' ||
        typeof value.revision !== 'string' || !/^[a-f0-9]{64}$/.test(value.revision) || typeof value.dataJson !== 'string') throw new Error('INVALID_READING_RESPONSE: invalid meeting catalog source');
    const f = value.fragment;
    if (!record(f) || f.unit !== 'unicode_code_points' || typeof f.complete !== 'boolean' ||
        ![f.startOffset, f.endOffset, f.totalCodePoints].every(n => typeof n === 'number' && Number.isSafeInteger(n) && n >= 0) ||
        (f.endOffset as number) <= (f.startOffset as number) || (f.endOffset as number) > (f.totalCodePoints as number) ||
        Array.from(value.dataJson).length !== (f.endOffset as number) - (f.startOffset as number) ||
        f.complete !== (f.endOffset === f.totalCodePoints) || (value.page.complete && (!f.complete || value.phase !== 'team'))) throw new Error('INVALID_READING_RESPONSE: invalid meeting catalog fragment');
    if (f.startOffset === 0 && f.complete) {
      try { if (!Array.isArray(JSON.parse(value.dataJson))) throw new Error('Expected array'); }
      catch { throw new Error('INVALID_READING_RESPONSE: invalid completed meeting group'); }
    }
  }
  const result = { content: [{ type: 'text' as const, text: JSON.stringify(value) }] };
  if (Buffer.byteLength(JSON.stringify(result)) > MAX_READING_BYTES) throw new Error('READING_LIMIT: meeting catalog exceeds 32000 bytes');
  return result;
}
