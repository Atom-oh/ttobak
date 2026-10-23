import { MAX_READING_BYTES } from './reading.js';

export interface VaultReadingOptions { section: 'manifest' | 'file'; fileId?: string; pageSize: number; cursor?: string }
export function vaultReadingOptions(args: Record<string, unknown>): VaultReadingOptions {
  if (Object.keys(args).some(key => !['section', 'fileId', 'pageSize', 'cursor'].includes(key))) throw new Error('INVALID_ARGUMENT: unsupported vault reading option');
  const section = args.section ?? 'manifest', pageSize = args.pageSize ?? 4000;
  if (!['manifest', 'file'].includes(section as string) || !Number.isInteger(pageSize) || (pageSize as number) < 1 || (pageSize as number) > 8000 ||
      (section === 'file' ? typeof args.fileId !== 'string' || !args.fileId.length || args.fileId.length > 2048 : args.fileId !== undefined)) throw new Error('INVALID_ARGUMENT: invalid vault reading selection');
  if (args.cursor !== undefined && (typeof args.cursor !== 'string' || !args.cursor.length || args.cursor.length > 2048)) throw new Error('INVALID_CURSOR: invalid vault continuation');
  return { section: section as 'manifest' | 'file', fileId: args.fileId as string | undefined, pageSize: pageSize as number, cursor: args.cursor as string | undefined };
}
function record(value: unknown): value is Record<string, unknown> { return !!value && typeof value === 'object' && !Array.isArray(value); }
function path(value: unknown): value is string { return typeof value === 'string' && value.length <= 1024 && !/[\\\x00-\x1f\x7f]/.test(value) && value.split('/').every(part => !!part && part !== '.' && part !== '..') && !value.startsWith('/'); }
function hash(value: unknown): boolean { return typeof value === 'string' && /^[a-f0-9]{64}$/.test(value); }
export function vaultReadingResult(value: unknown, options: VaultReadingOptions) {
  if (!record(value) || value.kind !== `vault_${options.section}` || !record(value.page) || typeof value.page.complete !== 'boolean' ||
      (value.page.complete ? value.page.nextCursor !== null : typeof value.page.nextCursor !== 'string' || !value.page.nextCursor.length || value.page.nextCursor.length > 2048)) throw new Error('INVALID_READING_RESPONSE: invalid vault continuation');
  if (value.discoveryPending === true) {
    if (options.section !== 'manifest' || value.phase !== 'discovery' || Object.keys(value).some(key => !['kind', 'phase', 'discoveryPending', 'page'].includes(key))) throw new Error('INVALID_READING_RESPONSE: discovery contains file data');
  } else if (value.mode === 'non_text') {
    if (options.section !== 'file' || value.fileId !== options.fileId || !path(value.path) || value.textStatus !== 'not_available' || !value.page.complete ||
        !record(value.documentRead) || typeof value.documentRead.docId !== 'string' || !/^[A-Za-z0-9_-]{1,128}$/.test(value.documentRead.docId) || value.markdown !== undefined) throw new Error('INVALID_READING_RESPONSE: invalid non-text document');
  } else {
    const manifest = options.section === 'manifest';
    if (!hash(value.revision) || (manifest ? value.consistency !== 'current_group' || value.unionKey !== 'path' || !['meetings', 'personal_docs', 'account_docs'].includes(value.phase as string) :
      value.mode !== 'text' || value.fileId !== options.fileId || !path(value.path) || !hash(value.sourceRevision) || value.consistency !== 'current_source_and_group' ||
      !['meeting', 'document'].includes(value.sourceKind as string) || !['body', 'attachments'].includes(value.phase as string) ||
      (value.sourceKind === 'document' && value.phase === 'attachments') || (value.sourceKind === 'meeting' && value.phase === 'body' && value.page.complete))) throw new Error('INVALID_READING_RESPONSE: invalid vault source');
    const text = manifest ? value.dataJson : value.markdown, f = value.fragment;
    if (typeof text !== 'string' || !record(f) || f.unit !== 'unicode_code_points' || typeof f.complete !== 'boolean' ||
        ![f.startOffset, f.endOffset, f.totalCodePoints].every(n => typeof n === 'number' && Number.isSafeInteger(n) && n >= 0) ||
        (f.endOffset as number) < (f.startOffset as number) || (f.endOffset as number) > (f.totalCodePoints as number) ||
        Array.from(text).length !== (f.endOffset as number) - (f.startOffset as number) || f.complete !== (f.endOffset === f.totalCodePoints) ||
        (!f.complete && f.endOffset === f.startOffset) || (value.page.complete && !f.complete)) throw new Error('INVALID_READING_RESPONSE: invalid vault fragment');
    if (manifest && f.startOffset === 0 && f.complete) {
      try {
        const files = JSON.parse(text);
        if (!Array.isArray(files) || files.some(file => !record(file) || typeof file.fileId !== 'string' || !file.fileId.length || file.fileId.length > 2048 || !path(file.path) || !hash(file.sourceMetadataRevision) || !['text', 'non_text'].includes(file.mode as string))) throw new Error('bad manifest');
      } catch { throw new Error('INVALID_READING_RESPONSE: invalid completed vault manifest group'); }
    }
  }
  const result = { content: [{ type: 'text' as const, text: JSON.stringify(value) }] };
  if (Buffer.byteLength(JSON.stringify(result)) > MAX_READING_BYTES) throw new Error('READING_LIMIT: vault page exceeds 32000 bytes');
  return result;
}
