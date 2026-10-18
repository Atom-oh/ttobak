import { MAX_READING_BYTES } from './reading.js';

export interface KBReadingOptions { pageSize: number; cursor?: string }

export function kbReadingOptions(args: Record<string, unknown>): KBReadingOptions {
  if (Object.keys(args).some(key => !['pageSize', 'cursor'].includes(key))) {
    throw new Error('INVALID_ARGUMENT: unknown KB catalog option');
  }
  const pageSize = args.pageSize === undefined ? 20 : args.pageSize;
  if (typeof pageSize !== 'number' || !Number.isInteger(pageSize) || pageSize < 1 || pageSize > 50) {
    throw new Error('INVALID_ARGUMENT: pageSize must be an integer from 1 to 50');
  }
  if (args.cursor !== undefined && (typeof args.cursor !== 'string' || !args.cursor.length || Buffer.byteLength(args.cursor) > 2048)) {
    throw new Error('INVALID_ARGUMENT: invalid catalog cursor');
  }
  return { pageSize, cursor: args.cursor as string | undefined };
}

function record(value: unknown): value is Record<string, unknown> {
  return !!value && typeof value === 'object' && !Array.isArray(value);
}

export function kbReadingResult(value: unknown, options: KBReadingOptions) {
  if (!record(value) || !Array.isArray(value.files) || value.files.length > options.pageSize ||
      value.consistency !== 'current_per_item') throw new Error('INVALID_READING_RESPONSE: invalid KB catalog');
  for (const file of value.files) {
    if (!record(file) || typeof file.fileId !== 'string' || !file.fileId ||
        Buffer.byteLength(file.fileId) > 1024 || /[/\\]/.test(file.fileId) ||
        typeof file.fileName !== 'string' || typeof file.fileType !== 'string' ||
        typeof file.size !== 'number' || !Number.isSafeInteger(file.size) || file.size < 0 ||
        typeof file.lastModified !== 'string' ||
        typeof file.metadataRevision !== 'string' || !/^[a-f0-9]{64}$/.test(file.metadataRevision)) {
      throw new Error('INVALID_READING_RESPONSE: invalid KB file metadata');
    }
  }
  const page = value.page;
  if (!record(page) || page.unit !== 'items' || page.returned !== value.files.length ||
      typeof page.complete !== 'boolean' ||
      (page.complete ? page.nextCursor !== null :
        typeof page.nextCursor !== 'string' || !page.nextCursor.length || page.nextCursor.length > 2048)) {
    throw new Error('INVALID_READING_RESPONSE: invalid KB continuation');
  }
  const result = { content: [{ type: 'text' as const, text: JSON.stringify(value) }] };
  if (Buffer.byteLength(JSON.stringify(result)) > MAX_READING_BYTES) {
    throw new Error('READING_LIMIT: KB catalog exceeds 32000 bytes');
  }
  return result;
}
