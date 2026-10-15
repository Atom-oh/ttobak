import { MAX_READING_BYTES } from './reading.js';

export interface DocumentReadingOptions {
  docId: string;
  accountId?: string;
  section: 'content' | 'metadata' | 'download' | 'preview';
  pageSize: number;
  cursor?: string;
  expectedRevision?: string;
}

export function documentReadingOptions(args: Record<string, unknown>): DocumentReadingOptions {
  if (Object.keys(args).some(key => !['docId', 'accountId', 'section', 'pageSize', 'cursor', 'expectedRevision'].includes(key))) {
    throw new Error('INVALID_ARGUMENT: unknown document reading option');
  }
  for (const key of ['docId', 'accountId']) {
    if (key === 'accountId' && args[key] === undefined) continue;
    if (typeof args[key] !== 'string' || !/^[A-Za-z0-9_-]{1,128}$/.test(args[key] as string)) {
      throw new Error(`INVALID_ARGUMENT: ${key} must be a document/account ID`);
    }
  }
  const section = args.section === undefined ? 'content' : args.section;
  const pageSize = args.pageSize === undefined ? 4000 : args.pageSize;
  if (section !== 'content' && section !== 'metadata' && section !== 'download' && section !== 'preview') {
    throw new Error('INVALID_ARGUMENT: section must be content, metadata, download or preview');
  }
  if (typeof pageSize !== 'number' || !Number.isInteger(pageSize) || pageSize < 1 || pageSize > 8000) {
    throw new Error('INVALID_ARGUMENT: pageSize must be an integer from 1 to 8000');
  }
  if (args.cursor !== undefined && (typeof args.cursor !== 'string' || !args.cursor.length || args.cursor.length > 2048)) {
    throw new Error('INVALID_CURSOR: cursor must be a nonempty string of at most 2048 characters');
  }
  if (args.expectedRevision !== undefined && (typeof args.expectedRevision !== 'string' || !/^[a-f0-9]{64}$/.test(args.expectedRevision))) {
    throw new Error('INVALID_ARGUMENT: expectedRevision must be a server-returned revision');
  }
  return { docId: args.docId as string, accountId: args.accountId as string | undefined,
    section, pageSize, cursor: args.cursor as string | undefined, expectedRevision: args.expectedRevision as string | undefined };
}

function record(value: unknown): value is Record<string, unknown> {
  return !!value && typeof value === 'object' && !Array.isArray(value);
}

/** Preserve server-owned content, revision and continuation without truncation. */
export function documentReadingResult(value: unknown, options: DocumentReadingOptions) {
  if (!record(value) || value.docId !== options.docId || value.accountId !== (options.accountId ?? '') ||
      value.section !== options.section || typeof value.content !== 'string' ||
      typeof value.revision !== 'string' || !/^[a-f0-9]{64}$/.test(value.revision) ||
      (options.expectedRevision !== undefined && value.revision !== options.expectedRevision) ||
      !['owner', 'edit', 'read'].includes(value.permission as string) ||
      !(options.accountId ? value.scope === 'account' : ['personal', 'shared'].includes(value.scope as string)) ||
      !['text', 'file'].includes(value.mode as string) ||
      !['available', 'empty', 'not_available'].includes(value.textStatus as string) ||
      'publicShareToken' in value || 'fileKey' in value) {
    throw new Error('INVALID_READING_RESPONSE: document identity or source metadata is invalid');
  }
  const page = value.page;
  if (!record(page) || page.unit !== 'unicode_code_points' || typeof page.complete !== 'boolean' ||
      ![page.startOffset, page.endOffset, page.totalCodePoints].every(n => typeof n === 'number' && Number.isSafeInteger(n) && n >= 0) ||
      (page.endOffset as number) < (page.startOffset as number) || (page.endOffset as number) > (page.totalCodePoints as number) ||
      Array.from(value.content).length !== (page.endOffset as number) - (page.startOffset as number) ||
      (page.complete ? page.endOffset !== page.totalCodePoints :
        page.endOffset === page.startOffset || (page.endOffset as number) >= (page.totalCodePoints as number)) ||
      (page.complete ? page.nextCursor !== null :
        typeof page.nextCursor !== 'string' || !page.nextCursor.length || page.nextCursor.length > 2048)) {
    throw new Error('INVALID_READING_RESPONSE: document page continuation is invalid');
  }
  if (value.mode === 'file' && (value.content !== '' || value.textStatus !== 'not_available')) {
    throw new Error('INVALID_READING_RESPONSE: file text is unavailable');
  }
  const result = { content: [{ type: 'text' as const, text: JSON.stringify(value) }] };
  if (Buffer.byteLength(JSON.stringify(result)) > MAX_READING_BYTES) {
    throw new Error('READING_LIMIT: document response exceeds 32000 bytes');
  }
  return result;
}
