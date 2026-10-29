/**
 * Pure helpers behind the Account detail Billing tab: which Account entry is
 * visible, which datasets to read, and how the dashboard's rows turn into the
 * summary tiles and cards. Field semantics follow services/billing/page/body.html.
 * Dependency-free so it can be unit tested.
 */

export type BillingSourceId = 'cot' | 'cur';

export interface BillingPeriod {
  first?: string;
  last?: string;
  months?: string[];
}

export interface BillingSourceInfo {
  versionId: string;
  period: BillingPeriod | null;
  ai: string | null;
}

/** One row of GET /api/billing/accounts. */
export interface BillingAccountEntry {
  id: string;
  name?: string;
  parentId?: string | null;
  member: boolean;
  canUpload: boolean;
  sources: { cot: BillingSourceInfo | null; cur: BillingSourceInfo | null };
}

export type BillingDatasetId = 'kpis' | 'period' | 'briefing' | 'sessions' | 'tag_signals' | 'cur_tag_signals';

/** Rows of each dataset; `null` means the read failed. */
export type BillingSummaryData = Record<BillingDatasetId, unknown[] | null>;

export type BillingDatasetFetcher = (id: BillingDatasetId, acct: string, signal?: AbortSignal) => Promise<unknown[]>;

export interface BillingSummaryItem {
  key: string;
  title: string;
  meta: string[];
}

export interface BillingKpiTiles {
  lastSpend: string;
  mom: string;
  coh: string;
  period: string[];
}

export const MISSING = '—';

type Row = Record<string, unknown>;

const isRow = (value: unknown): value is Row => !!value && typeof value === 'object' && !Array.isArray(value);
const finite = (value: unknown): number | null =>
  typeof value === 'number' && Number.isFinite(value) ? value : null;
const text = (value: unknown): string => (typeof value === 'string' ? value.trim() : '');

/** The entry of `accountId` when the caller is a member of it, else null. */
export function billingEntryFor(accounts: unknown, accountId: string): BillingAccountEntry | null {
  if (!Array.isArray(accounts) || !accountId) return null;
  const entry = accounts.find((a): a is BillingAccountEntry => isRow(a) && a.id === accountId);
  return entry && entry.member === true ? entry : null;
}

export function hasBillingData(entry: BillingAccountEntry): boolean {
  return !!(entry.sources?.cot || entry.sources?.cur);
}

/** COT datasets only when a COT version is current, the CUR dataset only with CUR; period always. */
export function billingDatasetsFor(entry: BillingAccountEntry): BillingDatasetId[] {
  const ids: BillingDatasetId[] = [];
  if (entry.sources?.cot) ids.push('kpis', 'briefing', 'sessions', 'tag_signals');
  if (entry.sources?.cur) ids.push('cur_tag_signals');
  if (hasBillingData(entry)) ids.push('period');
  return ids;
}

const compactNumber = new Intl.NumberFormat('en', { notation: 'compact', maximumFractionDigits: 1 });

/** `$1.2M`, `$45.3K`, `$0`; `—` when the value is missing. */
export function formatCompactUsd(value: unknown): string {
  const v = finite(value);
  if (v === null) return MISSING;
  // sign from the rounded value: anything that rounds to 0 is `$0`, never `-$0`
  const abs = compactNumber.format(Math.abs(v));
  return (v < 0 && abs !== '0' ? '-$' : '$') + abs;
}

/** `+3.4%`, `-1.0%`, `0.0%`; `—` when the value is missing. */
export function formatSignedPct(value: unknown): string {
  const v = finite(value);
  if (v === null) return MISSING;
  const rounded = Number(v.toFixed(1));
  if (rounded === 0) return '0.0%';
  return (rounded > 0 ? '+' : '-') + Math.abs(v).toFixed(1) + '%';
}

const rowsOf = (rows: unknown[] | null | undefined, accountId: string): Row[] =>
  Array.isArray(rows) ? rows.filter((r): r is Row => isRow(r) && r.entity === accountId) : [];

function spanOf(period: unknown): string {
  if (!isRow(period)) return '';
  const months = Array.isArray(period.months)
    ? period.months.filter((m): m is string => typeof m === 'string').sort()
    : [];
  const first = text(period.first) || months[0] || '';
  const last = text(period.last) || months[months.length - 1] || '';
  if (!first || !last) return '';
  return first === last ? first : `${first} ~ ${last}`;
}

/** One `COT 2026-01 ~ 2026-06` line per source present: period rows first, the account entry as fallback. */
export function billingPeriodLines(periodRows: unknown[] | null, entry: BillingAccountEntry, accountId: string): string[] {
  const rows = rowsOf(periodRows, accountId);
  const lines: string[] = [];
  for (const source of ['cot', 'cur'] as const) {
    if (!entry.sources?.[source]) continue;
    const row = rows.find((r) => (text(r.source) || 'cot') === source);
    const span = spanOf(row) || spanOf(entry.sources[source]?.period);
    if (span) lines.push(`${source.toUpperCase()} ${span}`);
  }
  return lines;
}

export function billingKpiTiles(data: BillingSummaryData, entry: BillingAccountEntry, accountId: string): BillingKpiTiles {
  const kpi = rowsOf(data.kpis, accountId)[0];
  return {
    lastSpend: formatCompactUsd(kpi?.last_spend),
    mom: formatSignedPct(kpi?.mom_pct),
    // 0 means COH is not active (the dashboard's briefing tile shows — for it)
    coh: finite(kpi?.coh_dedup) !== null && kpi?.coh_dedup !== 0 ? formatCompactUsd(kpi?.coh_dedup) : MISSING,
    period: billingPeriodLines(data.period, entry, accountId),
  };
}

// finite numbers and numeric strings rank; anything else (null, '', booleans) sorts last
const rankOf = (r: Row): number => {
  const v = r.rank;
  if (typeof v === 'number') return Number.isFinite(v) ? v : Number.MAX_SAFE_INTEGER;
  if (typeof v === 'string' && v.trim() !== '' && Number.isFinite(Number(v))) return Number(v);
  return Number.MAX_SAFE_INTEGER;
};
const byRank = (a: Row, b: Row) => rankOf(a) - rankOf(b);

// the latest month value the dashboard sorts signals by (body.html sigNow)
const signalValue = (r: Row): number | null => finite(r.source === 'cur' ? r.run_rate_last : r.est_monthly_cost);

/** Top three tag signals (COT then CUR); sorted by monthly value when every row carries one. */
export function topSignals(tagRows: unknown[] | null, curRows: unknown[] | null, accountId: string): BillingSummaryItem[] {
  const rows = [...rowsOf(tagRows, accountId), ...rowsOf(curRows, accountId)].filter((r) => text(r.tag_value));
  const sorted = rows.every((r) => signalValue(r) !== null)
    ? rows.slice().sort((a, b) => (signalValue(b) as number) - (signalValue(a) as number))
    : rows;
  return sorted.slice(0, 3).map((r, i) => ({
    key: `${text(r.source) || 'cot'}|${text(r.tag_key)}|${text(r.tag_value)}|${i}`,
    title: text(r.tag_value),
    meta: (Array.isArray(r.kinds) ? r.kinds : []).map(text).filter(Boolean),
  }));
}

/** Top three generated session proposals by rank. */
export function topSessions(rows: unknown[] | null, accountId: string): BillingSummaryItem[] {
  return rowsOf(rows, accountId)
    .filter((r) => r.origin === 'generated' && text(r.title))
    .sort(byRank)
    .slice(0, 3)
    .map((r, i) => ({ key: `${text(r.title)}|${i}`, title: text(r.title), meta: [text(r.type)].filter(Boolean) }));
}

/** Top three briefing actions by rank, with the monthly impact (— when 0 or missing) and owner. */
export function topActions(rows: unknown[] | null, accountId: string): BillingSummaryItem[] {
  return rowsOf(rows, accountId)
    .filter((r) => text(r.title))
    .sort(byRank)
    .slice(0, 3)
    .map((r, i) => {
      const impact = finite(r.impact);
      return {
        key: `${text(r.title)}|${i}`,
        title: text(r.title),
        meta: [impact ? `${formatCompactUsd(impact)}/월` : MISSING, text(r.owner)].filter(Boolean),
      };
    });
}

export function billingDashboardHref(accountId: string): string {
  return `/billing?${new URLSearchParams({ acct: accountId })}`;
}

export function billingUploadHref(accountId: string): string {
  return `/billing?${new URLSearchParams({ view: 'upload', acct: accountId })}`;
}

/**
 * Reads every dataset the entry has in parallel. A failed card dataset becomes
 * `null` (its card shows —); a failed kpis read rejects (the tab shows the error state).
 */
export async function loadAccountBilling(
  fetcher: BillingDatasetFetcher, entry: BillingAccountEntry, accountId: string, signal?: AbortSignal,
): Promise<BillingSummaryData> {
  const ids = billingDatasetsFor(entry);
  const settled = await Promise.allSettled(ids.map((id) => fetcher(id, accountId, signal)));
  const data: BillingSummaryData = {
    kpis: [], period: [], briefing: [], sessions: [], tag_signals: [], cur_tag_signals: [],
  };
  ids.forEach((id, i) => {
    const result = settled[i];
    if (result.status === 'fulfilled') {
      data[id] = result.value;
      return;
    }
    if (id === 'kpis') throw result.reason;
    data[id] = null;
  });
  return data;
}
