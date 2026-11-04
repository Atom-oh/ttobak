import type { Tool } from '@modelcontextprotocol/sdk/types.js';

export type BillingSection = 'metadata' | 'dataset' | 'report';
export interface BillingReadingOptions {
  accountId: string; section: BillingSection; pageSize: number;
  source?: 'cot' | 'cur'; dataset?: string; versionId?: string; cursor?: string;
}
const version = /^v\d{14}[0-9a-f]{6}$/;
// The BFF enforces the complete dataset allowlist; this also rejects CUR/COT crossover before HTTP.
const cur = ['cur_summary', 'cur_trend', 'cur_tag_coverage', 'cur_resources', 'cur_tag_signals', 'agent_notes', 'period', 'checks'];
export function billingReadingOptions(args: Record<string, unknown>, section: BillingSection): BillingReadingOptions {
  const keys = ['accountId', 'pageSize', 'cursor', ...(section === 'metadata' ? [] :
    ['source', 'versionId', ...(section === 'dataset' ? ['dataset'] : [])])];
  if (Object.keys(args).some(k => !keys.includes(k)) ||
      typeof args.accountId !== 'string' || !/^[A-Za-z0-9_-]{1,64}$/.test(args.accountId)) throw Error('INVALID_ARGUMENT: Billing account/options');
  const pageSize = args.pageSize ?? 4000;
  if (typeof pageSize !== 'number' || !Number.isInteger(pageSize) || pageSize < 1000 || pageSize > 8000 ||
      (args.cursor !== undefined && (typeof args.cursor !== 'string' || !/^[A-Za-z0-9_-]{1,2048}$/.test(args.cursor))) ||
      (args.versionId !== undefined && (typeof args.versionId !== 'string' || !version.test(args.versionId))) ||
      (section !== 'metadata' && !['cot', 'cur'].includes(args.source as string))) throw Error('INVALID_ARGUMENT: Billing selection/page');
  if (section === 'dataset' && (typeof args.dataset !== 'string' || !/^[a-z][a-z0-9_]{0,63}$/.test(args.dataset) ||
      (args.source === 'cur' ? !cur.includes(args.dataset) : args.dataset.startsWith('cur_')))) throw Error('INVALID_ARGUMENT: Billing dataset/source');
  return { accountId: args.accountId, section, pageSize, source: args.source as 'cot' | 'cur' | undefined,
    dataset: args.dataset as string | undefined, versionId: args.versionId as string | undefined,
    cursor: args.cursor as string | undefined };
}
const record = (v: unknown): v is Record<string, unknown> => !!v && typeof v === 'object' && !Array.isArray(v);
export function billingReadingResult(value: unknown, options: BillingReadingOptions) {
  if (!record(value) || value.kind !== 'billing' || value.accountId !== options.accountId ||
      value.section !== options.section || value.source !== (options.source ?? null) ||
      value.dataset !== (options.dataset ?? null) || value.consistency !== 'current_source' ||
      (options.section === 'metadata' ? value.versionId !== null :
        typeof value.versionId !== 'string' || !version.test(value.versionId) ||
        (options.versionId !== undefined && options.versionId !== value.versionId)) ||
      typeof value.revision !== 'string' || !/^[a-f0-9]{64}$/.test(value.revision) ||
      Buffer.byteLength(JSON.stringify(value)) > 14000) throw Error('INVALID_READING_RESPONSE: Billing source');
  const data = value[options.section === 'report' ? 'markdown' : 'dataJson'];
  const f = value.fragment, p = value.page;
  if (typeof data !== 'string' || Buffer.byteLength(data) > options.pageSize || !record(f) || !record(p) ||
      f.unit !== 'utf8_bytes' || typeof f.complete !== 'boolean' || typeof p.complete !== 'boolean' ||
      ![f.startOffset, f.endOffset, f.totalBytes].every(n => typeof n === 'number' && Number.isSafeInteger(n) && n >= 0) ||
      (f.startOffset as number) > (f.endOffset as number) || (f.endOffset as number) > (f.totalBytes as number) ||
      (f.endOffset as number) - (f.startOffset as number) !== Buffer.byteLength(data) ||
      f.complete !== (f.endOffset === f.totalBytes) || p.complete !== f.complete ||
      (!f.complete && f.startOffset === f.endOffset) ||
      (p.complete ? p.nextCursor !== null : typeof p.nextCursor !== 'string' || !/^[A-Za-z0-9_-]{1,2048}$/.test(p.nextCursor))) {
    throw Error('INVALID_READING_RESPONSE: Billing fragment/continuation');
  }
  if (f.startOffset === 0 && f.complete && options.section !== 'report') {
    try {
      const parsed = JSON.parse(data);
      if (options.section === 'metadata' ? !record(parsed) || parsed.accountId !== options.accountId : !Array.isArray(parsed)) throw Error();
    } catch { throw Error('INVALID_READING_RESPONSE: Billing JSON group'); }
  }
  const result = { content: [{ type: 'text' as const, text: JSON.stringify(value) }] };
  if (Buffer.byteLength(JSON.stringify(result)) > 32000) throw Error('READING_LIMIT: Billing result');
  return result;
}

export const BILLING_TOOLS: Tool[] = (['account', 'dataset', 'report'] as const).map(kind => ({
  name: `ttobak_get_billing_${kind}`,
  description: (kind === 'account' ?
    'Read customer Billing metadata: current CUR/COT version, period, AI status and supported dataset selections. Start here after ttobak_list_accounts.' :
    kind === 'dataset' ? 'Read customer CUR/COT analysis data. Select a dataset advertised by ttobak_get_billing_account; a supported dataset may have no published file.' :
      'Read the customer CUR/COT Markdown cost report for meeting preparation.') +
    ' Reads enforce current Billing team access. Follow page.nextCursor with identical arguments until complete; concatenate same-revision dataJson (then parse) or markdown fragments. A stale cursor requires restarting. No uploads or reruns.',
  annotations: { readOnlyHint: true, destructiveHint: false },
  inputSchema: { type: 'object', additionalProperties: false,
    properties: { accountId: { type: 'string', minLength: 1, maxLength: 64 },
      pageSize: { type: 'integer', minimum: 1000, maximum: 8000, default: 4000, description: 'Maximum fragment UTF-8 bytes' },
      cursor: { type: 'string', maxLength: 2048 },
      ...(kind === 'account' ? {} : {
        source: { type: 'string', enum: ['cot', 'cur'] },
        versionId: { type: 'string', pattern: '^v[0-9]{14}[0-9a-f]{6}$', description: 'Optional published version; omit for current source' },
        ...(kind === 'dataset' ? { dataset: { type: 'string', minLength: 1, maxLength: 64 } } : {}),
      }) },
    required: ['accountId', ...(kind === 'account' ? [] : ['source', ...(kind === 'dataset' ? ['dataset'] : [])])],
  },
}));
