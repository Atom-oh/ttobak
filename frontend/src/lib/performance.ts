// Pure helpers for the owner-only /performance page (SA activity). Wire types
// mirror backend/internal/model/performance.go. No imports, so node:test can
// load this file in isolation.

export type PerformancePeriod = 'week' | 'quarter' | 'year';

export interface PerformanceMissedItem {
  kind: 'logging_gap' | 'silent_customer' | string;
  accountId: string;
  sfdcAccountId?: string;
  refKind?: 'calendar' | 'meeting' | 'sfdc' | string;
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
  /** Earliest start (RFC3339 UTC) among the calendar events read; empty when none. */
  calendarCoverageStart: string;
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
  /** True when more than 500 assignments exist and only the first 500 are listed. */
  truncated: boolean;
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
  if (item.kind === 'logging_gap' && item.refKind === 'meeting' && item.refId) return `/meeting/${encodeURIComponent(item.refId)}`;
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

/**
 * Whether a stored mapping points at an account outside the loaded readable
 * list. `readable` is null until that list loads, so nothing is judged then.
 */
export function mappingTargetUnreadable(item: PerformanceMappingItem, readable: ReadonlySet<string> | null): boolean {
  return item.mapped && readable !== null && !readable.has(item.accountId ?? '');
}

/** The mapping queue: unmapped assignments and mappings whose account is not readable. */
export function unmappedAssignments(items: PerformanceMappingItem[], readable: ReadonlySet<string> | null = null): PerformanceMappingItem[] {
  return items.filter(item => !item.mapped || mappingTargetUnreadable(item, readable));
}

/** Notice that calendar-based gaps only cover retained calendar events. */
export function calendarCoverageNotice(start: string | undefined): string {
  const date = seoulParts(start)?.date;
  if (!date) return '수집된 캘린더 일정이 없어 캘린더 기반 로깅 누락은 표시되지 않습니다.';
  return `캘린더 기반 로깅 누락은 ${date} 이후 일정만 확인합니다.`;
}

/** Empty state for the logging-gap group, qualified by what the calendar coverage could evaluate. */
export function loggingGapsEmptyText(start: string | undefined): string {
  const date = seoulParts(start)?.date;
  if (!date) return '확인된 로깅 누락이 없습니다. 캘린더 일정이 없어 캘린더 기반 누락은 평가하지 않았습니다.';
  return `${date} 이후 일정 기준으로 확인된 로깅 누락이 없습니다.`;
}

/** The server caps the missed list at this many items (newest first, untouched silent customers last). */
export const MISSED_ITEM_CAP = 500;

/** Whether the missed list may have been cut at the server cap. */
export function missedListMayBeCapped(list: { items: unknown[] }): boolean {
  return list.items.length >= MISSED_ITEM_CAP;
}

/** Empty-state text for a missed group; never a clean result when the list may be capped. */
export function missedGroupEmptyText(base: string, capped: boolean): string {
  return capped ? `표시 한도(${MISSED_ITEM_CAP}건)에 도달해 이 항목은 표시되지 않았을 수 있습니다. 기간을 좁혀 다시 확인하세요.` : base;
}

export type AccountListResult<T> = { userId: string; key: string | number; accounts?: T[]; error?: boolean };

/**
 * Whether the readable account list for this user and request is still
 * loading, failed, or ready. A failure is never treated as an empty list.
 */
export function accountListState<T>(result: AccountListResult<T> | null, userId: string, key: string | number): { status: 'loading' | 'error' | 'ready'; accounts: T[] } {
  if (!result || result.userId !== userId || result.key !== key) return { status: 'loading', accounts: [] };
  if (result.error || !result.accounts) return { status: 'error', accounts: [] };
  return { status: 'ready', accounts: result.accounts };
}

/** Display name for a missed item; `names` is null when the account list failed to load. */
export function missedItemTitle(item: PerformanceMissedItem, names: Map<string, string> | null): string {
  if (!names) return '어카운트 이름을 불러오지 못함';
  return names.get(item.accountId) || '이름 없는 어카운트';
}

export type GoalsLoadStatus = 'loading' | 'error' | 'ready' | 'saving';

/** Goals are editable only after a successful load, so a save never overwrites unseen stored goals. */
export function goalsEditorControls({ status, dirty, tooLong }: { status: GoalsLoadStatus; dirty: boolean; tooLong: boolean }) {
  const editable = status === 'ready';
  return { editable, canSave: editable && dirty && !tooLong };
}
