import { MAX_READING_BYTES } from './reading.js';

export interface DocumentCatalogOptions {
  accountId?: string;
  docType?: string;
  pageSize: number;
  cursor?: string;
}

export function documentCatalogOptions(args: Record<string, unknown>): DocumentCatalogOptions {
  if (Object.keys(args).some(key => !['accountId', 'docType', 'pageSize', 'cursor'].includes(key))) {
    throw new Error('INVALID_ARGUMENT: unknown document catalog option');
  }
  if (args.accountId !== undefined && (typeof args.accountId !== 'string' || !/^[A-Za-z0-9_-]{1,128}$/.test(args.accountId))) {
    throw new Error('INVALID_ARGUMENT: accountId must be an account ID; omit it for personal documents');
  }
  if (args.docType !== undefined && (typeof args.docType !== 'string' || Array.from(args.docType).length > 256)) {
    throw new Error('INVALID_ARGUMENT: docType must contain at most 256 characters');
  }
  const pageSize = args.pageSize === undefined ? 20 : args.pageSize;
  if (typeof pageSize !== 'number' || !Number.isInteger(pageSize) || pageSize < 1 || pageSize > 50) {
    throw new Error('INVALID_ARGUMENT: pageSize must be an integer from 1 to 50');
  }
  if (args.cursor !== undefined && (typeof args.cursor !== 'string' || !args.cursor.length || args.cursor.length > 2048)) {
    throw new Error('INVALID_CURSOR: expected a nonempty server cursor of at most 2048 characters');
  }
  return { accountId: args.accountId as string | undefined, docType: args.docType as string | undefined,
    pageSize, cursor: args.cursor as string | undefined };
}

function record(value: unknown): value is Record<string, unknown> {
  return !!value && typeof value === 'object' && !Array.isArray(value);
}

export function documentCatalogResult(value: unknown, options: DocumentCatalogOptions) {
  if (!record(value) || value.accountId !== (options.accountId ?? '') ||
      value.consistency !== 'current_per_item' || !Array.isArray(value.documents) ||
      value.documents.length > options.pageSize) {
    throw new Error('INVALID_READING_RESPONSE: invalid document catalog identity');
  }
  for (const doc of value.documents) {
    if (!record(doc) || typeof doc.docId !== 'string' || !/^[A-Za-z0-9_-]{1,128}$/.test(doc.docId) ||
        typeof doc.title !== 'string' || typeof doc.metadataRevision !== 'string' ||
        !/^[a-f0-9]{64}$/.test(doc.metadataRevision) ||
        !['owner', 'edit', 'read'].includes(doc.permission as string) ||
        !(options.accountId ? doc.scope === 'account' : ['personal', 'shared'].includes(doc.scope as string)) ||
        'content' in doc || 'publicShareToken' in doc || 'fileKey' in doc) {
      throw new Error('INVALID_READING_RESPONSE: invalid document metadata');
    }
  }
  const page = value.page;
  if (!record(page) || page.unit !== 'items' || page.returned !== value.documents.length ||
      typeof page.complete !== 'boolean' ||
      (page.complete ? page.nextCursor !== null :
        typeof page.nextCursor !== 'string' || !page.nextCursor.length || page.nextCursor.length > 2048)) {
    throw new Error('INVALID_READING_RESPONSE: invalid catalog continuation');
  }
  const result = { content: [{ type: 'text' as const, text: JSON.stringify(value) }] };
  if (Buffer.byteLength(JSON.stringify(result)) > MAX_READING_BYTES) {
    throw new Error('READING_LIMIT: document catalog exceeds 32000 bytes');
  }
  return result;
}
