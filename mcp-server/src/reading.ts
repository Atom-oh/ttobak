export const MAX_READING_BYTES = 32_000;
type RecordValue = Record<string, unknown>;
type Kind = 'meeting' | 'transcript';
type CommonOptions = { meetingId: string; pageSize: number; cursor?: string };
export type ReadingOptions = CommonOptions & (
  { kind: 'meeting'; section: 'notes' | 'summary' | 'actionItems' } |
  { kind: 'transcript'; source: 'selected' | 'A' | 'B'; startTime?: number; endTime?: number }
);

export function readingOptions(args: RecordValue, kind: Kind): ReadingOptions {
  const allowed = ['meetingId', 'pageSize', 'cursor', ...(kind === 'meeting'
    ? ['section'] : ['source', 'startTime', 'endTime'])];
  if (Object.keys(args).some((key) => !allowed.includes(key))) throw new Error('INVALID_ARGUMENT: unknown reading option');
  if (typeof args.meetingId !== 'string' || !/^[A-Za-z0-9_-]{1,128}$/.test(args.meetingId)) {
    throw new Error('INVALID_ARGUMENT: meetingId must be an ID of at most 128 characters');
  }
  const pageSize = args.pageSize === undefined ? 4000 : args.pageSize;
  if (typeof pageSize !== 'number' || !Number.isInteger(pageSize) || pageSize < 1 || pageSize > 8000) {
    throw new Error('INVALID_ARGUMENT: pageSize must be an integer from 1 to 8000');
  }
  // Cursors are server-owned. Validate only their transport bound, never decode
  // or re-encode their contents or assume a particular revision/hash format.
  if (args.cursor !== undefined && (typeof args.cursor !== 'string' || !args.cursor.length || args.cursor.length > 2048)) {
    throw new Error('INVALID_CURSOR: expected a nonempty cursor of at most 2048 characters');
  }
  const common = { meetingId: args.meetingId, pageSize, cursor: args.cursor as string | undefined };
  if (kind === 'meeting') {
    const section = args.section === undefined ? 'notes' : args.section;
    if (section !== 'notes' && section !== 'summary' && section !== 'actionItems') {
      throw new Error('INVALID_ARGUMENT: section must be notes, summary or actionItems');
    }
    return { ...common, kind, section };
  }
  const source = args.source === undefined ? 'selected' : args.source;
  if (source !== 'selected' && source !== 'A' && source !== 'B') throw new Error('INVALID_ARGUMENT: source must be selected, A or B');
  if (args.startTime !== undefined || args.endTime !== undefined) {
    if (typeof args.startTime !== 'number' || typeof args.endTime !== 'number' ||
        !Number.isFinite(args.startTime) || !Number.isFinite(args.endTime) ||
        args.startTime < 0 || args.endTime <= args.startTime) {
      throw new Error('INVALID_RANGE: supply finite seconds with 0 <= startTime < endTime');
    }
  }
  return { ...common, kind, source, startTime: args.startTime as number | undefined, endTime: args.endTime as number | undefined };
}

function record(value: unknown): value is RecordValue {
  return !!value && typeof value === 'object' && !Array.isArray(value);
}

/** Wrap a backend page without selecting sources, slicing text, or minting cursors. */
export function readingResult(value: unknown, options: ReadingOptions) {
  if (!record(value) || value.meetingId !== options.meetingId) {
    throw new Error('INVALID_READING_RESPONSE: meeting identity does not match');
  }
  const page = value.page;
  if (!record(page) || page.unit !== 'unicode_code_points' || typeof page.complete !== 'boolean' ||
      (page.complete ? page.nextCursor !== null :
        typeof page.nextCursor !== 'string' || !page.nextCursor.length || page.nextCursor.length > 2048)) {
    throw new Error('INVALID_READING_RESPONSE: missing or invalid page continuation');
  }
  let result = value;
  if (options.kind === 'meeting') {
    const field = options.section === 'notes' ? 'notes' : options.section === 'summary' ? 'content' : 'actionItemsJson';
    if (typeof value[field] !== 'string') throw new Error('INVALID_READING_RESPONSE: missing section text');
    if (value.actionItemsAnalysis === undefined || value.actionItemsAnalysis === null) {
      result = { ...value, actionItemsAnalysis: { status: 'unknown' } };
    } else if (!record(value.actionItemsAnalysis) ||
        !['unknown', 'queued', 'running', 'failed', 'succeeded'].includes(value.actionItemsAnalysis.status as string)) {
      throw new Error('INVALID_READING_RESPONSE: invalid action analysis state');
    }
  } else if (!Array.isArray(value.chunks)) {
    throw new Error('INVALID_READING_RESPONSE: missing transcript chunks');
  }
  const output = { content: [{ type: 'text' as const, text: JSON.stringify(result) }] };
  if (Buffer.byteLength(JSON.stringify(output), 'utf8') > MAX_READING_BYTES) {
    throw new Error('READING_LIMIT: wrapped reading response exceeds 32000 bytes');
  }
  return output;
}

export function readingError(message: string) {
  const points = Array.from(message);
  const detail = points.slice(0, 1000).join('') + (points.length > 1000 ? ' [error message truncated]' : '');
  return { isError: true, content: [{ type: 'text' as const, text: `Error: ${detail}` }] };
}
