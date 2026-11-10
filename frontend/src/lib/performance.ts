// Pure helpers for the owner-only /performance page (SA activity). Wire types
// mirror backend/internal/model/performance.go. No imports, so node:test can
// load this file in isolation.

export type PerformancePeriod = 'week' | 'quarter' | 'year';

export interface PerformanceMissedItem {
  kind: 'logging_gap' | 'silent_customer' | string;
  accountId: string;
  sfdcAccountId?: string;
  refKind?: 'calendar' | 'meeting' | string;
  refId?: string;
  at?: string;
  /** -1 when the account had no touchpoint in the lookback window. */
  daysSince: number;
}

export interface PerformanceSourceFreshness {
  source: string;
  lastCompletedAt?: string;
}

export interface PerformanceMissedList {
  items: PerformanceMissedItem[];
  unmappedCount: number;
  sourceFreshness: PerformanceSourceFreshness[];
}

export interface PerformanceGoals {
  year: number;
  text: string;
  updatedAt: string;
}

export interface PerformanceMappingItem {
  sfdcAccountId: string;
  name: string;
  role: string;
  mapped: boolean;
  accountId?: string;
  manual: boolean;
  updatedAt?: string;
}

export interface PerformanceMappingList {
  items: PerformanceMappingItem[];
}

export interface AccountMapping {
  sfdcAccountId: string;
  accountId: string;
  manual: boolean;
  updatedAt: string;
}

export interface PerformanceSourceStatus {
  source: string;
  revision: number;
  lastAttemptAt?: string;
  lastCompletedAt?: string;
}

export interface PerformanceSourceList {
  sources: PerformanceSourceStatus[];
}

export const PERFORMANCE_PERIODS: { value: PerformancePeriod; label: string }[] = [
  { value: 'week', label: '이번 주' },
  { value: 'quarter', label: '분기' },
  { value: 'year', label: '올해' },
];

export const GOALS_MAX_BYTES = 8000;

export function normalizePeriod(value: string | null | undefined): PerformancePeriod {
  return PERFORMANCE_PERIODS.some(period => period.value === value) ? value as PerformancePeriod : 'week';
}

export function periodLabel(value: PerformancePeriod): string {
  return PERFORMANCE_PERIODS.find(period => period.value === value)?.label ?? value;
}

function timeOf(item: PerformanceMissedItem): number {
  const parsed = item.at ? Date.parse(item.at) : NaN;
  return Number.isNaN(parsed) ? -Infinity : parsed;
}

/** Splits missed items into the two section-B groups, newest `at` first; items without `at` go last. */
export function groupMissedItems(items: PerformanceMissedItem[]) {
  const newestFirst = (a: PerformanceMissedItem, b: PerformanceMissedItem) => timeOf(b) - timeOf(a) || 0;
  return {
    loggingGaps: items.filter(item => item.kind === 'logging_gap').sort(newestFirst),
    silentCustomers: items.filter(item => item.kind === 'silent_customer').sort(newestFirst),
  };
}

export function missedItemHref(item: PerformanceMissedItem): string {
  if (item.refKind === 'meeting' && item.refId) return `/meeting/${encodeURIComponent(item.refId)}`;
  return `/accounts/${encodeURIComponent(item.accountId)}`;
}

const SEOUL_OFFSET_MS = 9 * 60 * 60 * 1000; // Asia/Seoul has no DST.

function seoulParts(iso: string | undefined): { date: string; time: string } | null {
  const parsed = iso ? Date.parse(iso) : NaN;
  if (Number.isNaN(parsed)) return null;
  const shifted = new Date(parsed + SEOUL_OFFSET_MS).toISOString();
  return { date: shifted.slice(0, 10), time: shifted.slice(11, 16) };
}

export function missedItemDetail(item: PerformanceMissedItem): string {
  const date = seoulParts(item.at)?.date;
  if (item.kind === 'silent_customer') {
    if (item.daysSince < 0 || !date) return '조회 기간 내 접점 없음';
    return `마지막 접점 ${item.daysSince}일 전 · ${date}`;
  }
  const kind = item.refKind === 'meeting' ? '회의' : '캘린더 일정';
  return date ? `${kind} · ${date}` : kind;
}

const SOURCE_LABELS: Record<string, { label: string; command: string }> = {
  'sfdc-assignments': { label: 'SFDC 담당 고객', command: 'ttobak-mcp intake sfdc' },
  'sfdc-tasks': { label: 'SFDC 활동', command: 'ttobak-mcp intake sfdc' },
  calendar: { label: '캘린더', command: 'ttobak-mcp intake calendar' },
};

/** Freshness line for one source; a never-completed source carries the intake command to run. */
export function describeFreshness(entry: PerformanceSourceFreshness): { source: string; text: string; command?: string } {
  const known = SOURCE_LABELS[entry.source];
  const label = known?.label ?? entry.source;
  const parts = seoulParts(entry.lastCompletedAt);
  if (parts) return { source: entry.source, text: `${label} 마지막 수집: ${parts.date} ${parts.time}` };
  const result: { source: string; text: string; command?: string } = { source: entry.source, text: `${label} 수집 기록 없음` };
  if (known) result.command = known.command;
  return result;
}

/** UTF-8 byte length, matching the backend's 8,000-byte goals limit. */
export function utf8ByteLength(text: string): number {
  let bytes = 0;
  for (const char of text) {
    const code = char.codePointAt(0) ?? 0;
    bytes += code < 0x80 ? 1 : code < 0x800 ? 2 : code < 0x10000 ? 3 : 4;
  }
  return bytes;
}

export function unmappedAssignments(items: PerformanceMappingItem[]): PerformanceMappingItem[] {
  return items.filter(item => !item.mapped);
}
