import { kbReadingOptions, type KBReadingOptions } from './kb-catalog.js';
import { MAX_READING_BYTES } from './reading.js';

export type NavigationKind = 'accounts' | 'projects';
export function navigationOptions(args: Record<string, unknown>): KBReadingOptions {
  if (Object.keys(args).some(key => !['pageSize', 'cursor'].includes(key))) {
    throw new Error('INVALID_ARGUMENT: unknown navigation option');
  }
  return kbReadingOptions(args);
}

function record(value: unknown): value is Record<string, unknown> {
  return !!value && typeof value === 'object' && !Array.isArray(value);
}

export function navigationResult(value: unknown, options: KBReadingOptions, kind: NavigationKind) {
  if (!record(value) || value.consistency !== 'current_per_item' ||
      !Array.isArray(value[kind]) || (value[kind] as unknown[]).length > options.pageSize) {
    throw new Error('INVALID_READING_RESPONSE: invalid navigation catalog');
  }
  const items = value[kind] as unknown[];
  if (kind === 'projects' && (value.unionKey !== 'projectId' || typeof value.overlapPossible !== 'boolean')) {
    throw new Error('INVALID_READING_RESPONSE: missing project union contract');
  }
  for (const item of items) {
    if (!record(item) || typeof item[kind === 'accounts' ? 'accountId' : 'projectId'] !== 'string' ||
        !/^[A-Za-z0-9_-]{1,128}$/.test(item[kind === 'accounts' ? 'accountId' : 'projectId'] as string) ||
        typeof item.name !== 'string' || typeof item.metadataRevision !== 'string' ||
        !/^[a-f0-9]{64}$/.test(item.metadataRevision)) {
      throw new Error('INVALID_READING_RESPONSE: invalid navigation metadata');
    }
  }
  const page = value.page;
  if (!record(page) || page.unit !== 'items' || page.returned !== items.length || typeof page.complete !== 'boolean' ||
      (page.complete ? page.nextCursor !== null :
        typeof page.nextCursor !== 'string' || !page.nextCursor.length || page.nextCursor.length > 2048)) {
    throw new Error('INVALID_READING_RESPONSE: invalid navigation continuation');
  }
  const result = { content: [{ type: 'text' as const, text: JSON.stringify(value) }] };
  if (Buffer.byteLength(JSON.stringify(result)) > MAX_READING_BYTES) {
    throw new Error('READING_LIMIT: navigation response exceeds 32000 bytes');
  }
  return result;
}
